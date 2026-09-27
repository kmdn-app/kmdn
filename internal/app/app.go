// Package app assembles kmdn's services from configuration.
package app

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"

	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/kmdn-app/kmdn/internal/access"
	"github.com/kmdn-app/kmdn/internal/admin"
	"github.com/kmdn-app/kmdn/internal/api"
	"github.com/kmdn-app/kmdn/internal/auth"
	"github.com/kmdn-app/kmdn/internal/collab"
	"github.com/kmdn-app/kmdn/internal/config"
	"github.com/kmdn-app/kmdn/internal/docengine"
	"github.com/kmdn-app/kmdn/internal/gitmirror"
	"github.com/kmdn-app/kmdn/internal/invites"
	"github.com/kmdn-app/kmdn/internal/jobs"
	"github.com/kmdn-app/kmdn/internal/linking"
	"github.com/kmdn-app/kmdn/internal/links"
	"github.com/kmdn-app/kmdn/internal/mail"
	"github.com/kmdn-app/kmdn/internal/notify"
	"github.com/kmdn-app/kmdn/internal/publish"
	"github.com/kmdn-app/kmdn/internal/realtime"
	"github.com/kmdn-app/kmdn/internal/repos"
	"github.com/kmdn-app/kmdn/internal/revisions"
	"github.com/kmdn-app/kmdn/internal/search"
	"github.com/kmdn-app/kmdn/internal/secrets"
	"github.com/kmdn-app/kmdn/internal/server"
	"github.com/kmdn-app/kmdn/internal/setup"
	"github.com/kmdn-app/kmdn/internal/store"
	"github.com/kmdn-app/kmdn/internal/threads"
	"github.com/kmdn-app/kmdn/internal/updates"
	"github.com/kmdn-app/kmdn/internal/users"
)

// App holds the running services.
type App struct {
	Config    config.Config
	Log       *slog.Logger
	DB        *store.DB
	Secrets   *secrets.Store
	Jobs      *jobs.Queue
	Server    *server.Server
	Mail      *mail.Service
	Auth      *auth.Service
	AuthH     *auth.HTTP
	Setup     *setup.Service
	Repos     *repos.Service
	Revisions *revisions.Service
	Engine    *docengine.Engine
	Realtime  *realtime.Hub
	Collab    *collab.Hub
	Links     *links.Service
	Publish   *publish.Service
	Threads   *threads.Service
	Updates   *updates.Service
	Notify    *notify.Service
	Invites   *invites.Service
}

// New opens the database, applies migrations and builds the services.
func New(ctx context.Context, cfg config.Config, log *slog.Logger) (*App, error) {
	db, err := store.Open(ctx, cfg.DB.URL)
	if err != nil {
		return nil, fmt.Errorf("database: %w", err)
	}
	n, err := db.Migrate(ctx)
	if err != nil {
		db.Close()
		return nil, fmt.Errorf("migrations: %w", err)
	}
	if n > 0 {
		log.Info("applied migrations", "count", n, "dialect", db.Dialect.String())
	}
	kek, err := cfg.SecretKeyBytes()
	if err != nil {
		db.Close()
		return nil, err
	}
	sec, err := secrets.New(db, kek)
	if err != nil {
		db.Close()
		return nil, err
	}
	a := &App{
		Config:  cfg,
		Log:     log,
		DB:      db,
		Secrets: sec,
		Jobs:    jobs.New(db, jobs.Options{Logger: log}),
		Server:  server.New(server.Options{Config: cfg, Logger: log}),
	}
	a.Server.AddReadyCheck("database", func(ctx context.Context) error { return db.PingContext(ctx) })

	trusted, err := api.ParseCIDRs(cfg.Server.TrustedProxies)
	if err != nil {
		db.Close()
		return nil, err
	}
	a.Mail = mail.NewService(cfg, db, sec, log)
	a.Auth = &auth.Service{DB: db, Mail: a.Mail, BaseURL: cfg.Server.BaseURL, SessionTTL: cfg.Auth.SessionTTL, AutoJoinDomains: cfg.Auth.AutoJoinDomains, Log: log}
	a.AuthH = auth.NewHTTP(a.Auth, strings.HasPrefix(cfg.Server.BaseURL, "https://"), trusted)
	a.Setup = &setup.Service{DB: db, Mail: a.Mail, Auth: a.AuthH, BaseURL: cfg.Server.BaseURL, Log: log}

	r := a.Server.API()
	r.Use(a.AuthH.Middleware)
	a.AuthH.Routes(r)
	a.Setup.Routes(r)
	(&admin.SMTP{DB: db, Mail: a.Mail, Secrets: sec, Log: log}).Routes(r)

	a.Repos = &repos.Service{
		DB: db, Secrets: sec, Jobs: a.Jobs, Git: &gitmirror.Git{}, DataDir: cfg.DataDir, BaseURL: strings.TrimRight(cfg.Server.BaseURL, "/"), Log: log,
		Adapters: &repos.Adapters{DB: db, Secrets: sec, HTTP: &http.Client{Timeout: 30 * time.Second}},
	}
	a.Repos.Register()
	idx := &search.Indexer{Index: search.Index{DB: db}, Repos: a.Repos, Jobs: a.Jobs}
	idx.Register()
	idx.Routes(r)
	a.Repos.Routes(r)
	a.Repos.ForgeRoutes(r)
	a.Revisions = &revisions.Service{DB: db, Repos: a.Repos, Log: log, DataDir: cfg.DataDir, UploadMaxMB: cfg.Limits.UploadMaxMB}
	a.Revisions.Routes(r)
	eng, err := docengine.New(docengine.Options{})
	if err != nil {
		db.Close()
		return nil, err
	}
	a.Engine = eng
	a.Realtime = &realtime.Hub{Origin: originOf(cfg.Server.BaseURL), Log: log, Authorize: a.authorizeScope}
	a.Collab = &collab.Hub{DB: db, Engine: eng, Revisions: a.Revisions, Log: log, Publish: a.Realtime.Publish}
	a.Realtime.Rooms = a.Collab
	a.Collab.Routes(r)
	a.Publish = &publish.Service{DB: db, Repos: a.Repos, Revisions: a.Revisions, Docs: collabDocs{a}, Engine: eng, Jobs: a.Jobs, BaseURL: a.Repos.BaseURL, DataDir: cfg.DataDir, Log: log}
	a.Publish.Register()
	a.Publish.Routes(r)
	a.Threads = &threads.Service{DB: db, Publish: a.Realtime.Publish, Repos: a.Repos, Revisions: a.Revisions, Text: eng.PlainText,
		PlaceAnchor: func(ctx context.Context, rev revisions.Revision, c revisions.Caller, p, threadID string, pos []byte) error {
			repo, err := repos.Get(ctx, db, rev.RepoID)
			if err != nil {
				return err
			}
			return a.Collab.PutAnchor(ctx, repo, rev, c, p, threadID, pos)
		},
	}
	a.Notify = &notify.Service{DB: db, Jobs: a.Jobs, PublishUser: a.Realtime.PublishUser, BaseURL: a.Repos.BaseURL, Log: log,
		Present: func(revID, userID string) bool {
			for _, p := range a.Collab.Presence(revID) {
				if p.ID == userID {
					return true
				}
			}
			return false
		},
		Push: &notify.Pusher{DB: db, Secrets: sec, Subject: pushSubject(cfg.Server.BaseURL), Log: log},
	}
	a.Notify.Repos = a.Repos
	a.Notify.Register()
	a.Notify.Routes(r)
	a.Notify.FollowRoutes(r)
	a.Threads.OnComment = a.Notify.Comment
	a.Threads.Routes(r)
	a.Repos.OnHeadChanged = append(a.Repos.OnHeadChanged, a.Threads.Reanchor)
	a.Updates = &updates.Service{DB: db, Repos: a.Repos, Revisions: a.Revisions, Engine: eng, Docs: collabDocs{a}, Jobs: a.Jobs, Publish: a.Realtime.Publish, Log: log}
	a.Updates.Register()
	a.Updates.Routes(r)
	a.Links = &links.Service{DB: db, Repos: a.Repos, Revisions: a.Revisions, Engine: eng, Jobs: a.Jobs, Log: log}
	a.Links.Register()
	a.Links.Routes(r, links.ApplierFunc(func(ctx context.Context, repo repos.Repo, rev revisions.Revision, c revisions.Caller, p, md, kind string) error {
		return a.Collab.Apply(ctx, repo, rev, c, p, md, kind)
	}))
	a.Revisions.Changed = func(ctx context.Context, rev revisions.Revision, kind string) {
		a.Collab.RevisionChanged(ctx, rev)
		a.Notify.Kick(ctx)
		if kind == "published" {
			// Discussions this revision fixed are resolved with it.
			if _, err := a.Threads.RevisionPublished(ctx, rev); err != nil {
				log.Error("resolve fixed discussions", "err", err, "revision", rev.ID)
			}
		}
		ev := map[string]any{"type": "revision", "kind": kind, "revision": rev.ID, "number": rev.Number, "state": rev.State}
		a.Realtime.Publish("revision:"+rev.ID, ev)
		a.Realtime.Publish("repo:"+rev.RepoID, map[string]any{"type": "revision", "kind": kind, "revision": rev.ID, "number": rev.Number, "state": rev.State})
	}
	a.Revisions.Removed = func(_ context.Context, rev revisions.Revision, p string) { a.Collab.FileRemoved(rev, p) }
	a.Revisions.OnSubmitted = func(ctx context.Context, rev revisions.Revision, by string) {
		if _, err := a.Collab.Checkpoint(ctx, rev, by, "", collab.CheckpointSubmit); err != nil {
			log.Error("submit checkpoint", "err", err, "revision", rev.ID)
		}
	}
	a.Server.Mount("/ws", a.AuthH.Middleware(a.Realtime))
	a.Server.Mount("/hooks", a.Repos.WebhookHandler())
	(&admin.People{DB: db, Auth: a.Auth}).Routes(r)
	a.Invites = &invites.Service{DB: db, Mail: a.Mail, Auth: a.AuthH, BaseURL: a.Repos.BaseURL}
	a.Invites.Routes(r)
	(&linking.Service{DB: db, Secrets: sec, AuthH: a.AuthH, BaseURL: a.Repos.BaseURL, HTTP: a.Repos.Adapters.HTTP}).Routes(r)
	if _, err := repos.EnsureGitHost(ctx, db); err != nil {
		db.Close()
		return nil, err
	}

	a.Jobs.Register("auth.purge", func(ctx context.Context, _ jobs.Job) (any, error) {
		if err := notify.Purge(ctx, a.DB); err != nil {
			return nil, err
		}
		return nil, a.Auth.PurgeExpired(ctx)
	})
	return a, nil
}

// Run serves HTTP and runs background workers until ctx is cancelled.
func (a *App) Run(ctx context.Context) error {
	if _, err := a.Setup.Prepare(ctx); err != nil {
		return err
	}
	go a.periodic(ctx, time.Hour, "auth.purge")
	// Events recorded without a revision change (updates prepared, threads)
	// still reach people within a minute.
	go a.periodic(ctx, time.Minute, notify.JobDrain)
	a.catchUpIndexes(ctx)
	done := make(chan struct{})
	go func() { a.Jobs.Run(ctx); close(done) }()
	err := a.Server.Run(ctx)
	a.Realtime.Close()
	fctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
	a.Collab.Flush(fctx)
	cancel()
	<-done
	return err
}

// catchUpIndexes schedules an incremental link index of every repo, so
// repos connected before the index existed get one without waiting for a push.
func (a *App) catchUpIndexes(ctx context.Context) {
	list, err := repos.List(ctx, a.DB, nil, true)
	if err != nil {
		a.Log.Error("list repos for indexing", "error", err)
		return
	}
	for _, r := range list {
		if r.HeadSHA == "" {
			continue
		}
		if _, err := a.Jobs.Enqueue(ctx, a.DB, links.JobIndex, map[string]string{"repo_id": r.ID}, jobs.EnqueueOptions{Key: links.JobIndex + ":" + r.ID}); err != nil {
			a.Log.Error("enqueue link index", "repo", r.ID, "error", err)
		}
	}
}

// collabDocs lets publishing reach the current hub (tests swap it).
type collabDocs struct{ a *App }

func (d collabDocs) FlushRevision(ctx context.Context, revID string) {
	d.a.Collab.FlushRevision(ctx, revID)
}
func (d collabDocs) StateOf(ctx context.Context, revID, p string) (string, []byte, error) {
	return d.a.Collab.StateOf(ctx, revID, p)
}
func (d collabDocs) ApplyDoc(ctx context.Context, repo repos.Repo, rev revisions.Revision, c revisions.Caller, p string, doc json.RawMessage, kind string) error {
	return d.a.Collab.ApplyDoc(ctx, repo, rev, c, p, doc, kind)
}
func (d collabDocs) CheckpointBeforeUpdate(ctx context.Context, rev revisions.Revision, by string) error {
	_, err := d.a.Collab.Checkpoint(ctx, rev, by, "", collab.CheckpointPreUpdate)
	return err
}

// originOf returns scheme://host of the base URL (the only allowed WebSocket Origin).
func originOf(base string) string {
	u, err := url.Parse(base)
	if err != nil {
		return ""
	}
	return u.Scheme + "://" + u.Host
}

// authorizeScope decides who may follow realtime events for a scope.
func (a *App) authorizeScope(ctx context.Context, u users.User, scope string) bool {
	kind, id, ok := strings.Cut(scope, ":")
	if !ok {
		return false
	}
	repoID := id
	switch kind {
	case "repo":
	case "revision":
		rev, err := revisions.Get(ctx, a.DB, id)
		if err != nil {
			return false
		}
		repoID = rev.RepoID
	default:
		return false
	}
	role, err := access.Effective(ctx, a.DB, u, repoID)
	return err == nil && role != access.None
}

// periodic enqueues a maintenance job every interval (deduplicated by key).
func (a *App) periodic(ctx context.Context, every time.Duration, kind string) {
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		if _, err := a.Jobs.Enqueue(ctx, a.DB, kind, nil, jobs.EnqueueOptions{Key: kind, MaxAttempts: 1}); err != nil && ctx.Err() == nil {
			a.Log.Error("enqueue periodic job", "kind", kind, "error", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

// Close releases resources.
func (a *App) Close() error { return a.DB.Close() }

// pushSubject identifies this instance to Web Push services.
func pushSubject(base string) string {
	if strings.HasPrefix(base, "https://") {
		return base
	}
	u, err := url.Parse(base)
	if err != nil || u.Hostname() == "" {
		return "mailto:kmdn@localhost"
	}
	return "mailto:kmdn@" + u.Hostname()
}
