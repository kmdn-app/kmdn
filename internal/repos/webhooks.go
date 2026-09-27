package repos

import (
	"context"
	"errors"
	"io"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/kmdn-app/kmdn/internal/api"
	"github.com/kmdn-app/kmdn/internal/forge"
	"github.com/kmdn-app/kmdn/internal/store"
)

// WebhookHandler serves /hooks: GitHub (per App host), GitLab and plain git
// (per repository, token-authenticated). Pushes to a repo's target branch
// schedule a sync; everything is deduplicated by delivery id.
func (s *Service) WebhookHandler() http.Handler {
	r := chi.NewRouter()
	r.Post("/github/{host}", s.githubHook)
	r.Post("/gitlab/{repo}", s.repoHook(forge.KindGitLab))
	r.Post("/git/{repo}", s.repoHook(forge.KindGit))
	return r
}

func readBody(w http.ResponseWriter, r *http.Request) ([]byte, bool) {
	b, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 10<<20))
	if err != nil {
		http.Error(w, "body too large", http.StatusRequestEntityTooLarge)
		return nil, false
	}
	return b, true
}

// seen records a delivery and reports whether it was already processed.
func (s *Service) seen(ctx context.Context, id, hostID, repoID, event string) (bool, error) {
	if id == "" {
		return false, nil
	}
	_, err := store.Exec(ctx, s.DB, `INSERT INTO webhook_deliveries (id, forge_host_id, repo_id, event, received_at) VALUES (?, ?, ?, ?, ?)`,
		id, nullable(hostID), nullable(repoID), event, store.Millis(time.Now()))
	if store.IsUniqueViolation(err) {
		return true, nil
	}
	return false, err
}

func nullable(s string) any {
	if s == "" {
		return nil
	}
	return s
}

func (s *Service) githubHook(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	h, err := GetHost(ctx, s.DB, chi.URLParam(r, "host"))
	if err != nil || h.Kind != forge.KindGitHub {
		http.NotFound(w, r)
		return
	}
	body, ok := readBody(w, r)
	if !ok {
		return
	}
	secret, err := s.Adapters.secret(ctx, h.WebhookSecretRef)
	if err != nil {
		api.Error(w, r, err)
		return
	}
	app, err := s.Adapters.GitHubApp(ctx, h)
	if err != nil {
		api.Error(w, r, err)
		return
	}
	ev, err := app.ParseWebhook(r, body, secret)
	if err != nil {
		if errors.Is(err, forge.ErrBadSignature) {
			http.Error(w, "invalid signature", http.StatusUnauthorized)
			return
		}
		http.Error(w, "bad payload", http.StatusBadRequest)
		return
	}
	if dup, err := s.seen(ctx, "gh:"+ev.DeliveryID, h.ID, "", ev.Type); err != nil || dup {
		w.WriteHeader(http.StatusAccepted)
		return
	}
	switch ev.Type {
	case "push":
		repo, err := ByExternal(ctx, s.DB, h.ID, ev.Repo.ExternalID)
		if err == nil && ev.Branch == repo.TargetBranch {
			_, _ = s.EnqueueSync(ctx, repo.ID)
			s.Jobs.Notify()
		}
	case "change_request":
		if repo, err := ByExternal(ctx, s.DB, h.ID, ev.Repo.ExternalID); err == nil {
			s.changeRequest(ctx, repo, ev)
		}
	case "installation":
		if ev.Install != nil {
			if ev.Removed {
				_, _ = store.Exec(ctx, s.DB, `UPDATE repos SET health = ?, health_detail = ? WHERE install_id IN (SELECT id FROM forge_installs WHERE forge_host_id = ? AND external_id = ?)`,
					HealthDisconnected, "The GitHub App was uninstalled from this account.", h.ID, ev.Install.ExternalID)
			} else {
				_, _ = UpsertInstall(ctx, s.DB, h.ID, *ev.Install)
			}
		}
	case "repository":
		if ev.Removed {
			_, _ = store.Exec(ctx, s.DB, `UPDATE repos SET health = ?, health_detail = ? WHERE forge_host_id = ? AND external_id = ?`,
				HealthDisconnected, "The repository was deleted on GitHub.", h.ID, ev.Repo.ExternalID)
		} else if repo, err := ByExternal(ctx, s.DB, h.ID, ev.Repo.ExternalID); err == nil && ev.Repo.Name != "" && (repo.Owner != ev.Repo.Owner || repo.Name != ev.Repo.Name) {
			_, _ = store.Exec(ctx, s.DB, `UPDATE repos SET owner = ?, name = ? WHERE id = ?`, ev.Repo.Owner, ev.Repo.Name, repo.ID)
		}
	}
	w.WriteHeader(http.StatusAccepted)
}

func (s *Service) repoHook(kind string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx := r.Context()
		repo, err := Get(ctx, s.DB, chi.URLParam(r, "repo"))
		if err != nil || repo.ForgeKind != kind {
			http.NotFound(w, r)
			return
		}
		body, ok := readBody(w, r)
		if !ok {
			return
		}
		secret, err := s.webhookSecret(ctx, repo)
		if err != nil {
			api.Error(w, r, err)
			return
		}
		adapter, err := s.Adapters.ForRepo(ctx, repo)
		if err != nil {
			api.Error(w, r, err)
			return
		}
		ev, err := adapter.ParseWebhook(r, body, secret)
		if err != nil {
			if errors.Is(err, forge.ErrBadSignature) {
				http.Error(w, "invalid token", http.StatusUnauthorized)
				return
			}
			http.Error(w, "bad payload", http.StatusBadRequest)
			return
		}
		if dup, err := s.seen(ctx, kind+":"+ev.DeliveryID, repo.ForgeHostID, repo.ID, ev.Type); err != nil || dup {
			w.WriteHeader(http.StatusAccepted)
			return
		}
		if ev.Type == "push" && (ev.Branch == "" || ev.Branch == repo.TargetBranch) {
			_, _ = s.EnqueueSync(ctx, repo.ID)
			s.Jobs.Notify()
		}
		if ev.Type == "change_request" {
			s.changeRequest(ctx, repo, ev)
		}
		w.WriteHeader(http.StatusAccepted)
	}
}

// ChangeRequestHook reacts to a pull/merge request kmdn opened being merged or closed.
type ChangeRequestHook func(ctx context.Context, r Repo, ev forge.ChangeRequestEvent) error

func (s *Service) changeRequest(ctx context.Context, repo Repo, ev forge.Event) {
	if ev.ChangeRequest == nil {
		return
	}
	for _, f := range s.OnChangeRequest {
		if err := f(ctx, repo, *ev.ChangeRequest); err != nil && s.Log != nil {
			s.Log.Error("change request webhook", "err", err, "repo", repo.ID, "ref", ev.ChangeRequest.Ref)
		}
	}
	// A merge moved the target branch.
	if ev.ChangeRequest.Merged {
		_, _ = s.EnqueueSync(ctx, repo.ID)
		s.Jobs.Notify()
	}
}

// WebhookInfo tells repo admins how to configure plain git webhooks.
func (s *Service) WebhookInfo(ctx context.Context, r Repo) (url, secret string, err error) {
	secret, err = s.webhookSecret(ctx, r)
	switch r.ForgeKind {
	case forge.KindGitLab:
		url = s.BaseURL + "/hooks/gitlab/" + r.ID
	case forge.KindGit:
		url = s.BaseURL + "/hooks/git/" + r.ID
	}
	return url, secret, err
}
