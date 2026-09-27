// Package hooks sends revision and discussion events to per-repository
// webhooks: generic JSON signed with HMAC, or Slack messages
// (docs/specs/11-api.md#outgoing-webhooks).
package hooks

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"strings"
	"syscall"
	"time"

	"github.com/kmdn-app/kmdn/internal/ids"
	"github.com/kmdn-app/kmdn/internal/jobs"
	"github.com/kmdn-app/kmdn/internal/repos"
	"github.com/kmdn-app/kmdn/internal/revisions"
	"github.com/kmdn-app/kmdn/internal/secrets"
	"github.com/kmdn-app/kmdn/internal/store"
	"github.com/kmdn-app/kmdn/internal/threads"
	"github.com/kmdn-app/kmdn/internal/users"
)

// JobDeliver sends one delivery (retried with backoff).
const JobDeliver = "hooks.deliver"

// Kinds of hooks.
const (
	KindGeneric = "generic"
	KindSlack   = "slack"
)

// Events a hook can subscribe to.
var Events = []string{"revision.submitted", "revision.approved", "revision.changes_requested", "revision.update_available", "revision.published", "revision.closed", "discussion.created"}

// revisionEvents maps revision log kinds to hook events.
var revisionEvents = map[string]string{
	"submitted":         "revision.submitted",
	"approved":          "revision.approved",
	"changes_requested": "revision.changes_requested",
	"update_available":  "revision.update_available",
	"published":         "revision.published",
	"closed":            "revision.closed",
}

// RetryFor is how long failed deliveries are retried.
var RetryFor = 24 * time.Hour

// Service manages hooks and deliveries.
type Service struct {
	DB      *store.DB
	Secrets *secrets.Store
	Jobs    *jobs.Queue
	BaseURL string
	// AllowPrivate lets hooks reach loopback and private addresses
	// (instances whose tools live on the internal network).
	AllowPrivate bool
	// HTTP overrides the delivery client (tests).
	HTTP *http.Client
	Log  *slog.Logger
}

// Hook is a configured webhook.
type Hook struct {
	ID        string    `json:"id"`
	Kind      string    `json:"kind"`
	URLHost   string    `json:"url_host"`
	Events    []string  `json:"events"`
	Active    bool      `json:"active"`
	CreatedAt time.Time `json:"created_at"`
	// LastDelivery summarizes the latest delivery, if any.
	LastDelivery *Delivery `json:"last_delivery,omitempty"`

	repoID, urlRef, secretRef string
}

// Delivery is one attempt to send an event to a hook.
type Delivery struct {
	ID           string     `json:"id"`
	Event        string     `json:"event"`
	Status       string     `json:"status"`
	Attempts     int        `json:"attempts"`
	ResponseCode int        `json:"response_code"`
	ResponseBody string     `json:"response_body,omitempty"`
	Error        string     `json:"error,omitempty"`
	CreatedAt    time.Time  `json:"created_at"`
	LastAttempt  *time.Time `json:"last_attempt_at,omitempty"`
	DeliveredAt  *time.Time `json:"delivered_at,omitempty"`
}

// Register wires the delivery job.
func (s *Service) Register() {
	s.Jobs.Register(JobDeliver, func(ctx context.Context, j jobs.Job) (any, error) {
		var in struct {
			DeliveryID string `json:"delivery_id"`
		}
		if err := j.Decode(&in); err != nil {
			return nil, jobs.Permanent(err)
		}
		return nil, s.deliver(ctx, in.DeliveryID)
	})
}

const hookCols = `id, repo_id, kind, url_ref, url_host, secret_ref, events, active, created_at`

func scanHook(row interface{ Scan(...any) error }) (Hook, error) {
	var h Hook
	var events string
	var created int64
	if err := row.Scan(&h.ID, &h.repoID, &h.Kind, &h.urlRef, &h.URLHost, &h.secretRef, &events, &h.Active, &created); err != nil {
		return h, store.NotFound(err)
	}
	_ = json.Unmarshal([]byte(events), &h.Events)
	if h.Events == nil {
		h.Events = []string{}
	}
	h.CreatedAt = store.FromMillis(created)
	return h, nil
}

// List returns a repository's hooks.
func List(ctx context.Context, q store.Querier, repoID string) ([]Hook, error) {
	rows, err := store.Query(ctx, q, `SELECT `+hookCols+` FROM repo_hooks WHERE repo_id = ? ORDER BY created_at`, repoID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Hook{}
	for rows.Next() {
		h, err := scanHook(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, h)
	}
	return out, rows.Err()
}

// GetHook loads a hook of a repository.
func GetHook(ctx context.Context, q store.Querier, repoID, id string) (Hook, error) {
	return scanHook(store.QueryRow(ctx, q, `SELECT `+hookCols+` FROM repo_hooks WHERE id = ? AND repo_id = ?`, id, repoID))
}

// Input configures a hook.
type Input struct {
	Kind   string   `json:"kind"`
	URL    string   `json:"url"`
	Events []string `json:"events"`
	Active *bool    `json:"active,omitempty"`
}

var errInvalid = errors.New("hooks: invalid")

type invalidErr struct{ field, msg string }

func (e *invalidErr) Error() string { return e.msg }
func (e *invalidErr) Unwrap() error { return errInvalid }

func (s *Service) checkURL(raw, kind string) (*url.URL, error) {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || u.Host == "" || (u.Scheme != "https" && u.Scheme != "http") {
		return nil, &invalidErr{"url", "Use an http or https URL."}
	}
	if kind == KindSlack && u.Scheme != "https" {
		return nil, &invalidErr{"url", "Slack webhook URLs start with https://."}
	}
	if u.User != nil {
		return nil, &invalidErr{"url", "Put credentials in the hook's secret, not the URL."}
	}
	return u, nil
}

func checkEvents(evs []string) error {
	known := map[string]bool{}
	for _, e := range Events {
		known[e] = true
	}
	if len(evs) == 0 {
		return &invalidErr{"events", "Pick at least one event."}
	}
	for _, e := range evs {
		if !known[e] {
			return &invalidErr{"events", "Unknown event: " + e}
		}
	}
	return nil
}

// Create adds a hook and returns it with, for generic hooks, the signing
// secret (shown once).
func (s *Service) Create(ctx context.Context, repoID, by string, in Input) (Hook, string, error) {
	if in.Kind != KindGeneric && in.Kind != KindSlack {
		return Hook{}, "", &invalidErr{"kind", "kind is generic or slack."}
	}
	u, err := s.checkURL(in.URL, in.Kind)
	if err != nil {
		return Hook{}, "", err
	}
	if err := checkEvents(in.Events); err != nil {
		return Hook{}, "", err
	}
	var secret string
	h := Hook{ID: ids.New("hk"), Kind: in.Kind, URLHost: u.Host, Events: in.Events, Active: true, CreatedAt: time.Now(), repoID: repoID}
	err = s.DB.InTx(ctx, func(tx *store.Tx) error {
		ref, err := s.Secrets.Put(ctx, tx, "hook_url", []byte(u.String()))
		if err != nil {
			return err
		}
		h.urlRef = ref
		if in.Kind == KindGeneric {
			b := make([]byte, 24)
			_, _ = rand.Read(b)
			secret = "whsec_" + hex.EncodeToString(b)
			if h.secretRef, err = s.Secrets.Put(ctx, tx, "hook_secret", []byte(secret)); err != nil {
				return err
			}
		}
		evs, _ := json.Marshal(in.Events)
		var byv any
		if by != "" {
			byv = by
		}
		_, err = store.Exec(ctx, tx, `INSERT INTO repo_hooks (id, repo_id, kind, url_ref, url_host, secret_ref, events, active, created_by, created_at) VALUES (?, ?, ?, ?, ?, ?, ?, TRUE, ?, ?)`,
			h.ID, repoID, h.Kind, h.urlRef, h.URLHost, h.secretRef, string(evs), byv, store.Millis(h.CreatedAt))
		return err
	})
	return h, secret, err
}

// Update changes a hook's URL, events or active flag.
func (s *Service) Update(ctx context.Context, h Hook, in Input) (Hook, error) {
	return h, s.DB.InTx(ctx, func(tx *store.Tx) error {
		if in.URL != "" {
			u, err := s.checkURL(in.URL, h.Kind)
			if err != nil {
				return err
			}
			if err := s.Secrets.Update(ctx, tx, h.urlRef, []byte(u.String())); err != nil {
				return err
			}
			h.URLHost = u.Host
		}
		if in.Events != nil {
			if err := checkEvents(in.Events); err != nil {
				return err
			}
			h.Events = in.Events
		}
		if in.Active != nil {
			h.Active = *in.Active
		}
		evs, _ := json.Marshal(h.Events)
		_, err := store.Exec(ctx, tx, `UPDATE repo_hooks SET url_host = ?, events = ?, active = ? WHERE id = ?`, h.URLHost, string(evs), h.Active, h.ID)
		return err
	})
}

// Delete removes a hook and its secrets.
func (s *Service) Delete(ctx context.Context, h Hook) error {
	return s.DB.InTx(ctx, func(tx *store.Tx) error {
		if _, err := store.Exec(ctx, tx, `DELETE FROM repo_hooks WHERE id = ?`, h.ID); err != nil {
			return err
		}
		for _, ref := range []string{h.urlRef, h.secretRef} {
			if ref != "" {
				if err := s.Secrets.Delete(ctx, tx, ref); err != nil {
					return err
				}
			}
		}
		return nil
	})
}

// Payload is the generic webhook body.
type Payload struct {
	ID        string         `json:"id"`
	Type      string         `json:"type"`
	CreatedAt time.Time      `json:"created_at"`
	Repo      map[string]any `json:"repo"`
	Revision  map[string]any `json:"revision,omitempty"`
	Actor     map[string]any `json:"actor,omitempty"`
	Data      map[string]any `json:"data"`
}

// RevisionEvent queues deliveries for a revision event (called once per
// event by the notification drain).
func (s *Service) RevisionEvent(ctx context.Context, rev revisions.Revision, actorID, kind string, data json.RawMessage) error {
	ev, ok := revisionEvents[kind]
	if !ok {
		return nil
	}
	repo, err := repos.Get(ctx, s.DB, rev.RepoID)
	if err != nil {
		return err
	}
	d := map[string]any{}
	_ = json.Unmarshal(data, &d)
	if files, err := revisions.Files(ctx, s.DB, rev.ID); err == nil {
		d["files"] = len(files)
	}
	p := Payload{ID: ids.New("evt"), Type: ev, CreatedAt: time.Now(), Repo: s.repoJSON(repo), Data: d,
		Revision: map[string]any{"id": rev.ID, "number": rev.Number, "title": rev.Title, "state": rev.State, "url": s.BaseURL + "/" + repo.Owner + "/" + repo.Name + "/revisions/" + fmt.Sprint(rev.Number)}}
	if actorID != "" {
		if u, err := users.ByID(ctx, s.DB, actorID); err == nil {
			p.Actor = map[string]any{"id": u.ID, "name": u.Name}
		}
	}
	return s.queue(ctx, repo.ID, p)
}

// Discussion queues deliveries for a new discussion on a published page.
func (s *Service) Discussion(ctx context.Context, t threads.Thread, c threads.Comment, by users.User) {
	if t.Kind != threads.KindDiscussion || len(t.Comments) == 0 || t.Comments[0].ID != c.ID {
		return
	}
	repo, err := repos.Get(ctx, s.DB, t.RepoID)
	if err != nil {
		return
	}
	var a threads.Anchor
	_ = json.Unmarshal(t.Anchor, &a)
	p := Payload{ID: ids.New("evt"), Type: "discussion.created", CreatedAt: time.Now(), Repo: s.repoJSON(repo),
		Actor: map[string]any{"id": by.ID, "name": by.Name},
		Data:  map[string]any{"thread": t.ID, "path": t.Path, "quote": a.Quote, "body": c.Body, "url": s.BaseURL + "/" + repo.Owner + "/" + repo.Name + "/" + t.Path}}
	if err := s.queue(ctx, repo.ID, p); err != nil && s.Log != nil {
		s.Log.Error("queue discussion hook", "err", err)
	}
}

func (s *Service) repoJSON(r repos.Repo) map[string]any {
	return map[string]any{"id": r.ID, "slug": r.Slug, "url": s.BaseURL + "/" + r.Owner + "/" + r.Name}
}

// queue records a delivery for each active hook of the repo that wants the event.
func (s *Service) queue(ctx context.Context, repoID string, p Payload) error {
	hs, err := List(ctx, s.DB, repoID)
	if err != nil {
		return err
	}
	body, _ := json.Marshal(p)
	for _, h := range hs {
		if !h.Active || !contains(h.Events, p.Type) {
			continue
		}
		if err := s.enqueue(ctx, h.ID, p.Type, body); err != nil {
			return err
		}
	}
	return nil
}

func (s *Service) enqueue(ctx context.Context, hookID, event string, body []byte) error {
	id := ids.New("dlv")
	if _, err := store.Exec(ctx, s.DB, `INSERT INTO hook_deliveries (id, hook_id, event_type, payload, created_at) VALUES (?, ?, ?, ?, ?)`, id, hookID, event, string(body), store.Millis(time.Now())); err != nil {
		return err
	}
	_, err := s.Jobs.Enqueue(ctx, s.DB, JobDeliver, map[string]string{"delivery_id": id}, jobs.EnqueueOptions{Key: id, MaxAttempts: 1})
	return err
}

func contains(l []string, s string) bool {
	for _, x := range l {
		if x == s {
			return true
		}
	}
	return false
}

// Ping sends a test event to a hook.
func (s *Service) Ping(ctx context.Context, h Hook) error {
	repo, err := repos.Get(ctx, s.DB, h.repoID)
	if err != nil {
		return err
	}
	body, _ := json.Marshal(Payload{ID: ids.New("evt"), Type: "ping", CreatedAt: time.Now(), Repo: s.repoJSON(repo), Data: map[string]any{"message": "kmdn can reach this hook."}})
	return s.enqueue(ctx, h.ID, "ping", body)
}

// Redeliver sends a past delivery's payload again, as a new delivery.
func (s *Service) Redeliver(ctx context.Context, h Hook, deliveryID string) error {
	var event, payload string
	if err := store.QueryRow(ctx, s.DB, `SELECT event_type, payload FROM hook_deliveries WHERE id = ? AND hook_id = ?`, deliveryID, h.ID).Scan(&event, &payload); err != nil {
		return store.NotFound(err)
	}
	return s.enqueue(ctx, h.ID, event, []byte(payload))
}

// backoff is the wait before attempt n+1 (1 min, 2, 4… capped at 2 h).
func backoff(n int) time.Duration {
	d := time.Minute << min(n, 7)
	return min(d, 2*time.Hour)
}

// deliver makes one attempt and schedules the next on failure, for RetryFor.
func (s *Service) deliver(ctx context.Context, id string) error {
	var hookID, event, payload, status string
	var attempts int
	var created int64
	err := store.QueryRow(ctx, s.DB, `SELECT hook_id, event_type, payload, status, attempts, created_at FROM hook_deliveries WHERE id = ?`, id).Scan(&hookID, &event, &payload, &status, &attempts, &created)
	if errors.Is(err, sql.ErrNoRows) || status != "pending" {
		return nil
	}
	if err != nil {
		return err
	}
	h, err := scanHook(store.QueryRow(ctx, s.DB, `SELECT `+hookCols+` FROM repo_hooks WHERE id = ?`, hookID))
	if errors.Is(err, store.ErrNotFound) {
		return nil // hook deleted
	} else if err != nil {
		return err
	}
	code, respBody, sendErr := s.send(ctx, h, event, id, []byte(payload))
	attempts++
	now := time.Now()
	errText := ""
	if sendErr != nil {
		errText = sendErr.Error()
	}
	ok := sendErr == nil && code >= 200 && code < 300
	next := "pending"
	switch {
	case ok:
		next = "delivered"
	case now.Add(backoff(attempts)).After(store.FromMillis(created).Add(RetryFor)) || (code >= 400 && code < 500 && code != 408 && code != 429):
		next = "failed" // out of time, or the receiver rejects it for good
	}
	var deliveredAt any
	if ok {
		deliveredAt = store.Millis(now)
	}
	if _, err := store.Exec(ctx, s.DB, `UPDATE hook_deliveries SET status = ?, attempts = ?, response_code = ?, response_body = ?, error = ?, last_attempt_at = ?, delivered_at = ? WHERE id = ?`,
		next, attempts, code, respBody, errText, store.Millis(now), deliveredAt, id); err != nil {
		return err
	}
	if next == "pending" {
		_, err := s.Jobs.Enqueue(ctx, s.DB, JobDeliver, map[string]string{"delivery_id": id}, jobs.EnqueueOptions{Key: id + fmt.Sprint(attempts), RunAt: now.Add(backoff(attempts)), MaxAttempts: 1})
		return err
	}
	return nil
}

// send posts the event: signed JSON, or a Slack message.
func (s *Service) send(ctx context.Context, h Hook, event, deliveryID string, payload []byte) (int, string, error) {
	raw, err := s.Secrets.Get(ctx, s.DB, h.urlRef)
	if err != nil {
		return 0, "", err
	}
	body := payload
	if h.Kind == KindSlack {
		var p Payload
		_ = json.Unmarshal(payload, &p)
		body, _ = json.Marshal(slackMessage(p))
	}
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, string(raw), bytes.NewReader(body))
	if err != nil {
		return 0, "", err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "kmdn-webhooks")
	if h.Kind == KindGeneric {
		req.Header.Set("X-Kmdn-Event", event)
		req.Header.Set("X-Kmdn-Delivery", deliveryID)
		if h.secretRef != "" {
			secret, err := s.Secrets.Get(ctx, s.DB, h.secretRef)
			if err != nil {
				return 0, "", err
			}
			req.Header.Set("X-Kmdn-Signature", Sign(secret, body))
		}
	}
	resp, err := s.client().Do(req)
	if err != nil {
		return 0, "", err
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
	return resp.StatusCode, string(b), nil
}

// Sign is the X-Kmdn-Signature value for a body.
func Sign(secret, body []byte) string {
	m := hmac.New(sha256.New, secret)
	m.Write(body)
	return "sha256=" + hex.EncodeToString(m.Sum(nil))
}

var errPrivate = errors.New("hooks: the address is on a private network")

// client refuses loopback, private and link-local addresses unless allowed,
// so hooks can't be used to reach the instance's own network.
func (s *Service) client() *http.Client {
	if s.HTTP != nil {
		return s.HTTP
	}
	d := &net.Dialer{Timeout: 10 * time.Second}
	if !s.AllowPrivate {
		d.Control = func(_, address string, _ syscall.RawConn) error {
			host, _, err := net.SplitHostPort(address)
			if err != nil {
				return err
			}
			ip := net.ParseIP(host)
			if ip == nil || ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() || ip.IsUnspecified() {
				return errPrivate
			}
			return nil
		}
	}
	return &http.Client{Transport: &http.Transport{DialContext: d.DialContext, TLSHandshakeTimeout: 10 * time.Second}, Timeout: 20 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
}

// Deliveries lists a hook's recent deliveries.
func Deliveries(ctx context.Context, q store.Querier, hookID string, limit int) ([]Delivery, error) {
	rows, err := store.Query(ctx, q, `SELECT id, event_type, status, attempts, response_code, response_body, error, created_at, last_attempt_at, delivered_at FROM hook_deliveries WHERE hook_id = ? ORDER BY created_at DESC LIMIT ?`, hookID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Delivery{}
	for rows.Next() {
		var d Delivery
		var created int64
		var last, delivered sql.NullInt64
		if err := rows.Scan(&d.ID, &d.Event, &d.Status, &d.Attempts, &d.ResponseCode, &d.ResponseBody, &d.Error, &created, &last, &delivered); err != nil {
			return nil, err
		}
		d.CreatedAt, d.LastAttempt, d.DeliveredAt = store.FromMillis(created), store.NullMillis(last), store.NullMillis(delivered)
		out = append(out, d)
	}
	return out, rows.Err()
}
