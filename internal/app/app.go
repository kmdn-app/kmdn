// Package app assembles kmdn's services from configuration.
package app

import (
	"context"
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
	"github.com/kmdn-app/kmdn/internal/mail"
	"github.com/kmdn-app/kmdn/internal/realtime"
	"github.com/kmdn-app/kmdn/internal/repos"
	"github.com/kmdn-app/kmdn/internal/revisions"
	"github.com/kmdn-app/kmdn/internal/search"
	"github.com/kmdn-app/kmdn/internal/secrets"
	"github.com/kmdn-app/kmdn/internal/server"
	"github.com/kmdn-app/kmdn/internal/setup"
	"github.com/kmdn-app/kmdn/internal/store"
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
	a.Revisions = &revisions.Service{DB: db, Repos: a.Repos, Log: log}
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
	a.Revisions.Changed = func(ctx context.Context, rev revisions.Revision, kind string) {
		a.Collab.RevisionChanged(ctx, rev)
		ev := map[string]any{"type": "revision", "kind": kind, "revision": rev.ID, "number": rev.Number, "state": rev.State}
		a.Realtime.Publish("revision:"+rev.ID, ev)
		a.Realtime.Publish("repo:"+rev.RepoID, map[string]any{"type": "revision", "kind": kind, "revision": rev.ID, "number": rev.Number, "state": rev.State})
	}
	a.Revisions.Removed = func(_ context.Context, rev revisions.Revision, p string) { a.Collab.FileRemoved(rev, p) }
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

	a.Jobs.Register("auth.purge", func(ctx context.Context, _ jobs.Job) (any, error) { return nil, a.Auth.PurgeExpired(ctx) })
	return a, nil
}

// Run serves HTTP and runs background workers until ctx is cancelled.
func (a *App) Run(ctx context.Context) error {
	if _, err := a.Setup.Prepare(ctx); err != nil {
		return err
	}
	go a.periodic(ctx, time.Hour, "auth.purge")
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
