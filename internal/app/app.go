// Package app assembles kmdn's services from configuration.
package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"

	"net/http"
	"net/url"
	"path/filepath"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/kmdn-app/kmdn/internal/access"
	"github.com/kmdn-app/kmdn/internal/admin"
	"github.com/kmdn-app/kmdn/internal/api"
	"github.com/kmdn-app/kmdn/internal/assistant"
	"github.com/kmdn-app/kmdn/internal/auth"
	"github.com/kmdn-app/kmdn/internal/blobs"
	"github.com/kmdn-app/kmdn/internal/branches"
	"github.com/kmdn-app/kmdn/internal/collab"
	"github.com/kmdn-app/kmdn/internal/config"
	"github.com/kmdn-app/kmdn/internal/consistency"
	"github.com/kmdn-app/kmdn/internal/content"
	"github.com/kmdn-app/kmdn/internal/docengine"
	"github.com/kmdn-app/kmdn/internal/events"
	"github.com/kmdn-app/kmdn/internal/gitmirror"
	"github.com/kmdn-app/kmdn/internal/hooks"
	"github.com/kmdn-app/kmdn/internal/invites"
	"github.com/kmdn-app/kmdn/internal/jobs"
	"github.com/kmdn-app/kmdn/internal/linking"
	"github.com/kmdn-app/kmdn/internal/links"
	"github.com/kmdn-app/kmdn/internal/llm"
	"github.com/kmdn-app/kmdn/internal/mail"
	"github.com/kmdn-app/kmdn/internal/mcp"
	"github.com/kmdn-app/kmdn/internal/notify"
	"github.com/kmdn-app/kmdn/internal/orghttp"
	"github.com/kmdn-app/kmdn/internal/orgs"
	"github.com/kmdn-app/kmdn/internal/outbound"
	"github.com/kmdn-app/kmdn/internal/policy"
	"github.com/kmdn-app/kmdn/internal/provision"
	"github.com/kmdn-app/kmdn/internal/publish"
	"github.com/kmdn-app/kmdn/internal/realtime"
	"github.com/kmdn-app/kmdn/internal/repos"
	"github.com/kmdn-app/kmdn/internal/revisions"
	"github.com/kmdn-app/kmdn/internal/search"
	"github.com/kmdn-app/kmdn/internal/secrets"
	"github.com/kmdn-app/kmdn/internal/server"
	"github.com/kmdn-app/kmdn/internal/setup"
	"github.com/kmdn-app/kmdn/internal/store"
	"github.com/kmdn-app/kmdn/internal/summaries"
	"github.com/kmdn-app/kmdn/internal/telemetry"
	"github.com/kmdn-app/kmdn/internal/threads"
	"github.com/kmdn-app/kmdn/internal/updates"
	"github.com/kmdn-app/kmdn/internal/users"
	"github.com/kmdn-app/kmdn/internal/version"
)

// App holds the running services.
type App struct {
	Config      config.Config
	Log         *slog.Logger
	DB          *store.DB
	Secrets     *secrets.Store
	Jobs        *jobs.Queue
	Server      *server.Server
	Mail        *mail.Service
	Auth        *auth.Service
	AuthH       *auth.HTTP
	Setup       *setup.Service
	Repos       *repos.Service
	Revisions   *revisions.Service
	Engine      *docengine.Engine
	Realtime    *realtime.Hub
	Collab      *collab.Hub
	Links       *links.Service
	Publish     *publish.Service
	Branches    *branches.Service
	Threads     *threads.Service
	Updates     *updates.Service
	Notify      *notify.Service
	Hooks       *hooks.Service
	LLM         *llm.Service
	Assistant   *assistant.Service
	Summaries   *summaries.Service
	Consistency *consistency.Service
	MCP         *mcp.Service
	Orgs        *orghttp.Service
	// Policy is what orgs may do; set from the config.
	Policy *policy.Policy
	// OrgSettings reads org settings through the managed overlay.
	OrgSettings *orgs.SettingsStore
	// Events receives what happens in orgs (Options.Events).
	Events events.Sink
	// Content serves user bytes from server.content_base_url, when set.
	Content *content.Origin
	// Provision changes org membership from an outside source.
	Provision *provision.Service

	stopTracing func(context.Context) error
	Invites     *invites.Service
}

// Options are what an embedding program adds (pkg/kmdn,
// docs/specs/16-organizations.md#embedding). The zero value is kmdn alone.
type Options struct {
	// Policy replaces the one built from the config's policy section.
	Policy *policy.Policy
	// Managed fixes org settings per org (read-only in the console).
	Managed orgs.Managed
	// Events receives what happens in orgs.
	Events events.Sink
	// Routes adds routes to the root router; APIRoutes to /api/v1, after the
	// session middleware (auth.FromContext works there); OrgRoutes to
	// /api/v1/orgs/{org}, where the org is resolved (orgs.FromContext).
	Routes, APIRoutes, OrgRoutes func(chi.Router)
	// Migrations are the program's own NNNN_name.sql files, run after
	// kmdn's and recorded in MigrationsTable.
	Migrations      fs.FS
	MigrationsTable string
	// Providers are extra ways to sign in (auth.Provider).
	Providers []auth.Provider
	// MailFilter sees every email before it's sent (mail.Filter).
	MailFilter mail.Filter
}

// New opens the database, applies migrations and builds the services.
func New(ctx context.Context, cfg config.Config, log *slog.Logger) (*App, error) {
	return NewWith(ctx, cfg, log, Options{})
}

// NewWith is New with an embedding program's options.
func NewWith(ctx context.Context, cfg config.Config, log *slog.Logger, opts Options) (*App, error) {
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
	if err := orgs.SetMode(ctx, db, cfg.Orgs.Mode); err != nil {
		db.Close()
		return nil, err
	}
	if opts.Migrations != nil {
		if _, err := db.MigrateFS(ctx, opts.Migrations, opts.MigrationsTable); err != nil {
			db.Close()
			return nil, fmt.Errorf("embedded migrations: %w", err)
		}
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
		Jobs:    jobs.New(db, jobOptions(cfg, log)),
		Server:  server.New(server.Options{Config: cfg, Logger: log}),
	}
	a.Server.AddReadyCheck("database", func(ctx context.Context) error { return db.PingContext(ctx) })

	trusted, err := api.ParseCIDRs(cfg.Server.TrustedProxies)
	if err != nil {
		db.Close()
		return nil, err
	}
	a.Policy = &policy.Policy{Strict: cfg.Policy.Strict, NoOrgForges: cfg.Policy.NoOrgForges}
	if opts.Policy != nil {
		a.Policy = opts.Policy
	}
	if a.Policy.Strict && cfg.Server.ContentBaseURL == "" {
		db.Close()
		return nil, errors.New("strict policy needs server.content_base_url, so user files aren't served from the app origin")
	}
	if a.Content, err = content.New(cfg.Server.ContentBaseURL, kek); err != nil {
		db.Close()
		return nil, err
	}
	a.Mail = mail.NewService(cfg, db, sec, log)
	a.Mail.Filter = opts.MailFilter
	a.Auth = &auth.Service{DB: db, Mail: a.Mail, BaseURL: cfg.Server.BaseURL, SessionTTL: cfg.Auth.SessionTTL, AutoJoinDomains: cfg.Auth.AutoJoinDomains, Policy: a.Policy, Events: opts.Events, Log: log}
	a.AuthH = auth.NewHTTP(a.Auth, strings.HasPrefix(cfg.Server.BaseURL, "https://"), trusted)
	a.AuthH.BaseURL = cfg.Server.BaseURL
	a.AuthH.LegacyCookies = !a.Content.Enabled()
	a.Setup = &setup.Service{DB: db, Mail: a.Mail, Auth: a.AuthH, BaseURL: cfg.Server.BaseURL, SignupURL: cfg.Orgs.SignupURL, Log: log}

	r := a.Server.API()
	r.Use(a.AuthH.Middleware)
	for _, p := range opts.Providers {
		if err := a.AuthH.AddProvider(p); err != nil {
			db.Close()
			return nil, err
		}
	}
	a.AuthH.Routes(r)
	a.Provision = &provision.Service{DB: db, Policy: a.Policy, Events: opts.Events}
	a.Setup.Routes(r)
	(&admin.SMTP{DB: db, Mail: a.Mail, Secrets: sec, Log: log}).Routes(r)

	a.Repos = &repos.Service{
		DB: db, Secrets: sec, Jobs: a.Jobs, Git: &gitmirror.Git{AllowProtocols: a.Policy.GitProtocols()}, DataDir: cfg.DataDir, BaseURL: strings.TrimRight(cfg.Server.BaseURL, "/"), Log: log,
		Adapters: &repos.Adapters{DB: db, Secrets: sec, HTTP: &http.Client{Timeout: 30 * time.Second, Transport: telemetry.Transport(nil)}},
		Policy:   a.Policy,
		Events:   opts.Events,
		Content:  a.Content,
	}
	if a.Policy.Strict {
		// Hosts an org added only reach public addresses.
		a.Repos.Adapters.OrgHTTP = outbound.Client(false)
	}
	a.Repos.Register()
	idx := &search.Indexer{Index: search.Index{DB: db}, Repos: a.Repos, Jobs: a.Jobs}
	idx.Register()
	idx.Routes(r)
	a.Repos.Routes(r)
	a.Repos.ForgeRoutes(r)
	uploads := blobs.FS{Root: filepath.Join(cfg.DataDir, "uploads")}
	a.Revisions = &revisions.Service{DB: db, Repos: a.Repos, Log: log, DataDir: cfg.DataDir, Blobs: uploads, UploadMaxMB: cfg.Limits.UploadMaxMB, Policy: a.Policy, Content: a.Content}
	a.Revisions.Routes(r)
	if a.Content.Enabled() {
		a.Content.Register("repo", a.Repos.ContentResolver)
		a.Content.Register("upload", a.Revisions.ContentResolver)
		a.Server.SetContentHandler(a.Content.Handler())
	}
	eng, err := docengine.New(docengine.Options{})
	if err != nil {
		db.Close()
		return nil, err
	}
	a.Engine = eng
	a.Realtime = &realtime.Hub{Auth: a.Auth, Origin: originOf(cfg.Server.BaseURL), Log: log, Authorize: a.authorizeScope}
	a.Branches = &branches.Service{DB: db, Repos: a.Repos, Revisions: a.Revisions, Jobs: a.Jobs, BaseURL: a.Repos.BaseURL, DataDir: cfg.DataDir, Blobs: uploads, Log: log}
	a.Branches.Register()
	a.Collab = &collab.Hub{DB: db, Engine: eng, Revisions: a.Revisions, Branches: a.Branches, Log: log, Publish: a.Realtime.Publish}
	a.Realtime.Rooms = a.Collab
	a.Collab.Routes(r)
	a.Publish = &publish.Service{DB: db, Repos: a.Repos, Revisions: a.Revisions, Docs: collabDocs{a}, Engine: eng, Jobs: a.Jobs, Branches: a.Branches, Log: log}
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
	orgSettings := &orgs.SettingsStore{DB: db, Managed: opts.Managed}
	a.LLM = &llm.Service{DB: db, Secrets: sec, Disabled: !cfg.Assistant.Enabled, Env: llm.EnvFrom(cfg.Assistant), Log: log,
		OrgAllows: func(ctx context.Context, orgID string) bool {
			st, _, err := orgSettings.Get(ctx, orgID)
			return err == nil && st.Assistant
		},
		OrgBudget: func(ctx context.Context, orgID string) int {
			st, _, _ := orgSettings.Get(ctx, orgID)
			return st.MonthlyTokens
		},
		Events: opts.Events}
	a.LLM.Routes(r)
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
	a.Hooks = &hooks.Service{DB: db, Secrets: sec, Jobs: a.Jobs, BaseURL: a.Repos.BaseURL, AllowPrivate: a.Policy.AllowPrivateOutbound(cfg.Hooks.AllowPrivate), Log: log}
	a.Hooks.Register()
	a.Hooks.Routes(r)
	a.Notify.OnEvent = a.Hooks.RevisionEvent
	a.Threads.OnComment = func(ctx context.Context, t threads.Thread, c threads.Comment, by users.User) {
		a.Notify.Comment(ctx, t, c, by)
		a.Hooks.Discussion(ctx, t, c, by)
	}
	a.Threads.Routes(r)
	a.Repos.OnHeadChanged = append(a.Repos.OnHeadChanged, a.Threads.Reanchor)
	a.Updates = &updates.Service{DB: db, Repos: a.Repos, Revisions: a.Revisions, Engine: eng, Docs: collabDocs{a}, Jobs: a.Jobs, Publish: a.Realtime.Publish, Log: log}
	a.Updates.Register()
	a.Updates.Routes(r)
	a.Links = &links.Service{DB: db, Repos: a.Repos, Revisions: a.Revisions, Engine: eng, Jobs: a.Jobs, Log: log}
	a.Links.Register()
	a.Assistant = &assistant.Service{DB: db, LLM: a.LLM, Repos: a.Repos, Revisions: a.Revisions, Search: &idx.Index, Links: a.Links, Engine: eng, Docs: collabDocs{a}, Publish: a.Realtime.Publish, Log: log}
	a.Assistant.Routes(r)
	a.Summaries = &summaries.Service{DB: db, LLM: a.LLM, Repos: a.Repos, Revisions: a.Revisions, Links: a.Links, Engine: eng, Docs: collabDocs{a}, Jobs: a.Jobs, Publish: a.Realtime.Publish, Log: log,
		ThreadText: func(ctx context.Context, revID string) string { return assistant.PeopleSaid(ctx, db, revID) }}
	a.Summaries.Register()
	a.Summaries.Routes(r)
	a.Publish.SuggestedCommit = a.Summaries.SuggestedCommit
	a.Consistency = &consistency.Service{DB: db, LLM: a.LLM, Repos: a.Repos, Revisions: a.Revisions, Jobs: a.Jobs, Docs: collabDocs{a}, Publish: a.Realtime.Publish, Log: log,
		Brief: func(ctx context.Context, rev revisions.Revision, u users.User, text, p string) error {
			t, err := assistant.RevisionThread(ctx, db, rev.RepoID, rev.ID)
			if err != nil {
				return err
			}
			_, err = a.Assistant.Post(ctx, t, u, text, assistant.Context{Path: p})
			return err
		}}
	a.Consistency.Register()
	a.Consistency.Routes(r)
	a.Links.Duplicates = a.Consistency.Duplicates
	a.Revisions.OnContent = func(ctx context.Context, revID, _ string) {
		a.Consistency.RequestRevision(ctx, revID, consistency.RevisionDebounce)
	}
	a.Links.Routes(r, links.ApplierFunc(func(ctx context.Context, repo repos.Repo, rev revisions.Revision, c revisions.Caller, p, md, kind string) error {
		return a.Collab.Apply(ctx, repo, rev, c, p, md, kind)
	}))
	a.Revisions.Changed = func(ctx context.Context, rev revisions.Revision, kind string) {
		a.Collab.RevisionChanged(ctx, rev)
		a.Branches.Changed(ctx, rev)
		a.Notify.Kick(ctx)
		if kind == "published" {
			a.Summaries.RequestChanges(ctx, rev)
			// Discussions this revision fixed are resolved with it.
			if _, err := a.Threads.RevisionPublished(ctx, rev); err != nil {
				log.Error("resolve fixed discussions", "err", err, "revision", rev.ID)
			}
		}
		ev := map[string]any{"type": "revision", "kind": kind, "revision": rev.ID, "number": rev.Number, "state": rev.State}
		a.Realtime.Publish("revision:"+rev.ID, ev)
		a.Realtime.Publish("repo:"+rev.RepoID, map[string]any{"type": "revision", "kind": kind, "revision": rev.ID, "number": rev.Number, "state": rev.State})
	}
	a.Revisions.PendingSuggestions = func(ctx context.Context, rev revisions.Revision) (int, error) {
		list, err := a.Collab.Suggestions(ctx, rev, "")
		return len(list), err
	}
	a.Revisions.Removed = func(_ context.Context, rev revisions.Revision, p string) { a.Collab.FileRemoved(rev, p) }
	a.Revisions.BeforeSubmit = func(ctx context.Context, repo repos.Repo, rev revisions.Revision, c revisions.Caller) error {
		return a.Collab.SaveFirst(ctx, repo, rev, c.User, "Save for review")
	}
	a.Revisions.OnSubmitted = func(ctx context.Context, rev revisions.Revision, by string) {
		a.Summaries.RequestReview(ctx, rev.ID, by)
		a.Consistency.RequestRevision(ctx, rev.ID, 0)
	}
	a.Server.Mount("/ws", a.AuthH.Middleware(a.Realtime))
	a.Server.Mount("/hooks", a.Repos.WebhookHandler())
	a.MCP = &mcp.Service{DB: db, Repos: a.Repos, Search: &idx.Index, Links: a.Links, Engine: eng, BaseURL: a.Repos.BaseURL, Log: log}
	a.Server.Mount("/mcp", a.MCP.Handler(a.AuthH.TrustedProxies))
	people := &admin.People{DB: db, Auth: a.Auth, Log: log}
	people.Routes(r)
	a.Orgs = &orghttp.Service{DB: db, Settings: orgSettings, AllowCreate: cfg.Orgs.AllowCreate, OrgForges: a.Policy.OrgForgesAllowed(), SignupURL: cfg.Orgs.SignupURL, Policy: a.Policy, Events: opts.Events, Log: log}

	(&admin.System{DB: db, Config: cfg, Started: time.Now()}).Routes(r)
	a.Invites = &invites.Service{DB: db, Mail: a.Mail, Auth: a.AuthH, BaseURL: a.Repos.BaseURL, Policy: a.Policy, Events: opts.Events}
	a.Invites.Routes(r)
	link := &linking.Service{DB: db, Secrets: sec, AuthH: a.AuthH, BaseURL: a.Repos.BaseURL, HTTP: a.Repos.Adapters.HTTP, Adapters: a.Repos.Adapters}
	link.Routes(r)
	a.Orgs.Routes(r, a.Repos.OrgRoutes, people.OrgRoutes, a.Invites.OrgRoutes, a.MCP.OrgRoutes, a.LLM.OrgRoutes, a.Repos.OrgForgeRoutes, link.OrgRoutes, func(r chi.Router) {
		if opts.OrgRoutes != nil {
			opts.OrgRoutes(r)
		}
	})
	if opts.APIRoutes != nil {
		opts.APIRoutes(r)
	}
	if opts.Routes != nil {
		opts.Routes(a.Server.Router())
	}
	a.OrgSettings, a.Events = orgSettings, opts.Events
	if _, err := repos.EnsureGitHost(ctx, db); err != nil {
		db.Close()
		return nil, err
	}

	telemetry.SetSources(telemetry.Sources{
		WebSockets: a.Realtime.Connections,
		Rooms:      func() int { return a.Collab.Rooms() },
		Engine:     eng.Pool,
		Jobs: func(ctx context.Context) (map[string]int, map[string]int, error) {
			rows, err := store.Query(ctx, db, `SELECT kind, status, COUNT(*) FROM jobs WHERE status IN ('pending', 'failed') GROUP BY kind, status`)
			if err != nil {
				return nil, nil, err
			}
			defer rows.Close()
			queued, failed := map[string]int{}, map[string]int{}
			for rows.Next() {
				var kind, status string
				var n int
				if err := rows.Scan(&kind, &status, &n); err != nil {
					return nil, nil, err
				}
				if status == "pending" {
					queued[kind] = n
				} else {
					failed[kind] = n
				}
			}
			return queued, failed, rows.Err()
		},
	})
	if ep := cfg.Telemetry.OTLPEndpoint; ep != "" {
		shutdown, err := telemetry.SetupTracing(ctx, ep, version.Get().Version)
		if err != nil {
			db.Close()
			return nil, fmt.Errorf("telemetry.otlp_endpoint: %w", err)
		}
		a.stopTracing = shutdown
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
	ctx, stop := context.WithCancel(ctx)
	defer stop()
	if _, err := a.Setup.Prepare(ctx); err != nil {
		return err
	}
	go a.LLM.CheckEnv(ctx)
	go a.periodic(ctx, time.Hour, "auth.purge")
	go a.periodic(ctx, time.Hour, consistency.JobSchedule)
	// Events recorded without a revision change (updates prepared, threads)
	// still reach people within a minute.
	go a.periodic(ctx, time.Minute, notify.JobDrain)
	go a.periodic(ctx, time.Minute, publish.JobRecover)
	a.catchUpIndexes(ctx)
	done := make(chan struct{})
	go func() { a.Jobs.Run(ctx); close(done) }()
	err := a.Server.Run(ctx)
	stop()
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

func (d collabDocs) FlushRevisionChecked(ctx context.Context, revID string) error {
	return d.a.Collab.FlushRevisionChecked(ctx, revID)
}

func (d collabDocs) FlushRevision(ctx context.Context, revID string) {
	d.a.Collab.FlushRevision(ctx, revID)
}
func (d collabDocs) StateOf(ctx context.Context, revID, p string) (string, []byte, error) {
	return d.a.Collab.StateOf(ctx, revID, p)
}
func (d collabDocs) ApplyDoc(ctx context.Context, repo repos.Repo, rev revisions.Revision, c revisions.Caller, p string, doc json.RawMessage, kind string) error {
	return d.a.Collab.ApplyDoc(ctx, repo, rev, c, p, doc, kind)
}
func (d collabDocs) Suggest(ctx context.Context, repo repos.Repo, rev revisions.Revision, c revisions.Caller, p, markdown string, attrs docengine.SuggestAttrs, kind string) (bool, error) {
	return d.a.Collab.Suggest(ctx, repo, rev, c, p, markdown, attrs, kind)
}
func (d collabDocs) SaveBeforePublish(ctx context.Context, repo repos.Repo, rev revisions.Revision, by users.User) error {
	return d.a.Collab.SaveCurrentLocked(ctx, repo, rev, by, "Save before publishing")
}
func (d collabDocs) SaveBeforeUpdate(ctx context.Context, repo repos.Repo, rev revisions.Revision, c revisions.Caller) error {
	return d.a.Collab.SaveFirst(ctx, repo, rev, c.User, "Save before applying updates from Published")
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
	case "assistant":
		t, err := assistant.GetThread(ctx, a.DB, id)
		return err == nil && a.Assistant.CanRead(ctx, u, t)
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

// Close releases resources (and flushes traces).
func (a *App) Close() error {
	if a.stopTracing != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		_ = a.stopTracing(ctx)
		cancel()
	}
	return a.DB.Close()
}

// jobOptions shares workers fairly between orgs in multi mode; with one org
// there's nothing to share.
func jobOptions(cfg config.Config, log *slog.Logger) jobs.Options {
	o := jobs.Options{Logger: log}
	if cfg.Orgs.Mode != orgs.Multi {
		o.PerOrg = 1 << 10
	}
	return o
}

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
