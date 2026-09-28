package kmdn_test

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"testing/fstest"

	"github.com/go-chi/chi/v5"

	"github.com/kmdn-app/kmdn/pkg/kmdn"
)

// An embedding program uses only this package: two orgs, its own routes,
// migrations, settings, limits and events. If this test needs internal/,
// the public surface is missing something.
func TestEmbedding(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	cfg := kmdn.DefaultConfig()
	cfg.DataDir, cfg.DB.URL = dir, "sqlite://"+filepath.Join(dir, "kmdn.db")
	cfg.SecretKey = base64.StdEncoding.EncodeToString(make([]byte, 32))
	cfg.Orgs.Mode = "multi"
	cfg.SMTP.Host, cfg.SMTP.From = "log", "kmdn@acme.test" // mail goes to the log
	cfg.Server.ContentBaseURL = "http://content.localhost"

	var mu sync.Mutex
	var seen []kmdn.Event
	var app *kmdn.App
	app, err := kmdn.New(ctx, cfg, slog.New(slog.NewTextHandler(io.Discard, nil)),
		kmdn.WithPolicy(&kmdn.Policy{Strict: true, Limits: func(context.Context, string) kmdn.Limits { return kmdn.Limits{Members: 3} }}),
		kmdn.WithManagedSettings(func(_ context.Context, o kmdn.Org) map[string]any {
			if o.Slug == "globex" {
				return map[string]any{"assistant": false, "monthly_tokens": 1000}
			}
			return nil
		}),
		kmdn.WithEvents(func(_ context.Context, e kmdn.Event) { mu.Lock(); seen = append(seen, e); mu.Unlock() }),
		kmdn.WithMigrations(fstest.MapFS{"0001_plans.sql": {Data: []byte("CREATE TABLE saas_plans (org_id TEXT PRIMARY KEY, plan TEXT NOT NULL);")}}, "saas_migrations"),
		kmdn.WithRoutes(func(r chi.Router) {
			r.Get("/whoami", func(w http.ResponseWriter, r *http.Request) {
				s, ok := app.Authenticate(r)
				if !ok {
					http.Error(w, "", http.StatusUnauthorized)
					return
				}
				_ = json.NewEncoder(w).Encode(map[string]string{"email": s.User.Email, "csrf": s.CSRF, "method": s.Method})
			})
			r.Get("/signup/finish", func(w http.ResponseWriter, r *http.Request) {
				if err := app.SignIn(w, r, r.URL.Query().Get("email")); err != nil {
					http.Error(w, err.Error(), http.StatusBadRequest)
				}
			})
		}),
		kmdn.WithAPIRoutes(func(r chi.Router) {
			r.Get("/saas/me", func(w http.ResponseWriter, r *http.Request) {
				u, ok := kmdn.CurrentUser(r)
				if !ok {
					http.Error(w, "", http.StatusUnauthorized)
					return
				}
				_ = json.NewEncoder(w).Encode(map[string]string{"email": u.Email})
			})
		}),
		kmdn.WithSignInProvider(sso{}),
		kmdn.WithOrgRoutes(func(r chi.Router) {
			r.Get("/billing", func(w http.ResponseWriter, r *http.Request) {
				o, role, _ := kmdn.CurrentOrg(r)
				_ = json.NewEncoder(w).Encode(map[string]string{"org": o.Slug, "role": role})
			})
		}),
	)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = app.Close() })

	acme, err := app.CreateOrg(ctx, kmdn.NewOrg{Name: "Acme", OwnerEmail: "alice@acme.dev", OwnerName: "Alice"})
	if err != nil || acme.Slug != "acme" {
		t.Fatalf("create acme: %+v %v", acme, err)
	}
	globex, err := app.CreateOrg(ctx, kmdn.NewOrg{Slug: "globex", Name: "Globex", OwnerEmail: "bob@globex.dev"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := app.CreateOrg(ctx, kmdn.NewOrg{Slug: "globex", Name: "Again", OwnerEmail: "x@y.dev"}); !errors.Is(err, kmdn.ErrSlugTaken) {
		t.Fatalf("taken slug: %v", err)
	}
	if n, err := app.Seats(ctx, acme.ID); err != nil || n != 1 {
		t.Fatalf("seats: %d %v", n, err)
	}
	if st, err := app.OrgSettings(ctx, globex.ID); err != nil || st.Assistant || st.MonthlyTokens != 1000 {
		t.Fatalf("managed settings: %+v %v", st, err)
	}
	if _, err := app.DB().ExecContext(ctx, `INSERT INTO saas_plans (org_id, plan) VALUES (?, 'unlimited')`, acme.ID); err != nil {
		t.Fatalf("the program's own table: %v", err)
	}

	srv := httptest.NewServer(app.Handler())
	t.Cleanup(srv.Close)
	jar, _ := cookiejar.New(nil)
	c := &http.Client{Jar: jar}
	get := func(path string) (int, map[string]string) {
		res, err := c.Get(srv.URL + path)
		if err != nil {
			t.Fatal(err)
		}
		defer res.Body.Close()
		var out map[string]string
		_ = json.NewDecoder(res.Body).Decode(&out)
		return res.StatusCode, out
	}
	if code, _ := get("/signup/finish?email=alice@acme.dev"); code != 200 {
		t.Fatalf("sign in: %d", code)
	}
	if code, who := get("/whoami"); code != 200 || who["email"] != "alice@acme.dev" || who["csrf"] == "" || who["method"] != "embedded" {
		t.Fatalf("authenticate on a program route: %d %v", code, who)
	}
	if u, err := app.UserByEmail(ctx, "Alice@acme.dev"); err != nil || u.Email != "alice@acme.dev" {
		t.Fatalf("user by email: %v %+v", err, u)
	}
	if _, err := app.UserByEmail(ctx, "nobody@acme.dev"); !errors.Is(err, kmdn.ErrNotFound) {
		t.Fatalf("missing user: %v", err)
	}
	if err := kmdn.ValidSlug("signup"); err == nil || kmdn.ValidSlug(kmdn.Slugify("Acme Corp")) != nil {
		t.Fatalf("slugs: %v", err)
	}
	if err := app.SendMail(ctx, kmdn.Mail{To: "alice@acme.dev", Subject: "Hi", Text: "Hello"}); err != nil {
		t.Fatalf("send mail: %v", err)
	}
	if code, me := get("/api/v1/saas/me"); code != 200 || me["email"] != "alice@acme.dev" {
		t.Fatalf("api route: %d %v", code, me)
	}
	if code, b := get("/api/v1/orgs/acme/billing"); code != 200 || b["org"] != "acme" || b["role"] != kmdn.RoleOwner {
		t.Fatalf("org route: %d %v", code, b)
	}
	if code, _ := get("/api/v1/orgs/globex/billing"); code != 404 {
		t.Fatalf("another org's route: %d", code)
	}

	if err := app.SuspendOrg(ctx, acme.ID, "Payment failed."); err != nil {
		t.Fatal(err)
	}
	if o, _ := app.OrgByID(ctx, acme.ID); o.Status != "suspended" || o.StatusReason != "Payment failed." {
		t.Fatalf("suspended: %+v", o)
	}
	if err := app.ResumeOrg(ctx, acme.ID); err != nil {
		t.Fatal(err)
	}
	if res, err := http.Get(srv.URL + "/api/v1/auth/providers"); err != nil || res.StatusCode != 200 {
		t.Fatalf("sign-in providers: %v", err)
	} else {
		b, _ := io.ReadAll(res.Body)
		res.Body.Close()
		if !strings.Contains(string(b), `"id":"sso"`) {
			t.Fatalf("sign-in providers: %s", b)
		}
	}
	if _, err := app.Provisioner().Upsert(ctx, acme.ID, "scim", kmdn.ProvisionedMember{Email: "bob@acme.dev", Active: true}); err != nil {
		t.Fatal(err)
	}

	mu.Lock()
	defer mu.Unlock()
	count := map[string]int{}
	for _, e := range seen {
		count[e.Type]++
	}
	if count[kmdn.EventOrgCreated] != 2 || count[kmdn.EventMemberAdded] != 3 || count[kmdn.EventOrgStatusChanged] != 2 {
		t.Fatalf("events: %v", count)
	}
}

// sso is a provider as an embedder writes one.
type sso struct{}

func (sso) Info() kmdn.ProviderInfo { return kmdn.ProviderInfo{ID: "sso", Name: "SSO", Listed: true} }
func (sso) Start(w http.ResponseWriter, r *http.Request, state string) error {
	http.Redirect(w, r, "https://idp.example.com/?state="+state, http.StatusFound)
	return nil
}
func (sso) Callback(r *http.Request) (kmdn.Identity, string, error) {
	return kmdn.Identity{Issuer: "https://idp.example.com", Subject: r.FormValue("sub")}, r.FormValue("state"), nil
}
