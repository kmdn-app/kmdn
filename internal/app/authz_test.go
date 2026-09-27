package app

import (
	"context"
	"net/http"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/kmdn-app/kmdn/internal/users"
)

// Routes anyone may call without signing in.
var publicRoutes = map[string]bool{
	"GET /setup/status": true, "POST /setup/admin": true,
	"POST /auth/magic-link": true, "POST /auth/magic-link/verify": true, "POST /auth/logout": true,
	"POST /auth/passkey/options": true, "POST /auth/passkey/verify": true,
	"GET /auth/oauth/providers": true, "GET /auth/oauth/{host}/start": true, "GET /auth/oauth/{host}/callback": true, "GET /auth/oauth/callback": true,
	"GET /invites/{token}": true, "POST /invites/{token}/accept": true,
}

// Routes any signed-in person may call, whatever their repository access.
// Every signed-in account is a member of the default org in single mode, so
// the org's own collections (repos, directory, groups) and leaving it are
// theirs too.
var anyUserRoutes = regexp.MustCompile(`^(GET|PATCH|POST|DELETE|PUT) /(me\b|inbox|notifications|push|assistant/status|follows|setup/complete|auth/|orgs$|orgs/\{org\}$|orgs/\{org\}/(repos|users|groups|assistant/status|repos/by-slug/\{owner\}/\{name\})$|orgs/\{org\}/members/\{user\}$|admin/forges/github/(callback|setup)$)`)

func walkRoutes(t *testing.T, a *App) []string {
	t.Helper()
	var out []string
	err := chi.Walk(a.Server.API(), func(method, route string, _ http.Handler, _ ...func(http.Handler) http.Handler) error {
		route = strings.TrimSuffix(route, "/")
		if route == "" || route == "/*" {
			return nil
		}
		out = append(out, method+" "+route)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	sort.Strings(out)
	return out
}

var param = regexp.MustCompile(`\{[^}]+\}`)

// fill replaces route parameters with ids from ids (by name), or a
// placeholder, and wildcards with a page path.
func fill(route string, ids map[string]string) string {
	route = strings.Replace(route, "/*", "/docs/index.md", 1)
	return param.ReplaceAllStringFunc(route, func(p string) string {
		if v, ok := ids[strings.Trim(p, "{}")]; ok {
			return v
		}
		return "x_does_not_exist"
	})
}

// TestAuthzMatrix calls every API route as a stranger and as a signed-in
// person without access to the repository, with real ids: nothing but the
// public (or personal) routes may succeed, and nothing may crash.
func TestAuthzMatrix(t *testing.T) {
	a, admin := newApp(t, nil)
	ctx := context.Background()
	maya, _ := users.Create(ctx, a.DB, "maya@northwind.dev", "Maya", true)
	signIn(t, a, admin, maya)
	repoID := connectLocal(t, a, admin, map[string]string{"docs/index.md": "# Handbook\n"})
	_, rev := admin.do("POST", "/repos/"+repoID+"/revisions", map[string]any{"title": "A change", "path": "docs/index.md"})
	revID := rev["id"].(string)
	_, th := admin.do("GET", "/revisions/"+revID+"/assistant", nil)
	ids := map[string]string{"repo": repoID, "revision": revID, "id": maya.ID, "user": maya.ID, "org": "default"}
	if th != nil {
		if m, ok := th["thread"].(map[string]any); ok {
			ids["thread"] = m["id"].(string)
		}
	}
	out, _ := users.Create(ctx, a.DB, "outsider@else.where", "Outsider", false)
	outsider := &tc{t: t, base: admin.base, c: newClient()}
	signIn(t, a, outsider, out)
	stranger := &tc{t: t, base: admin.base, c: newClient()}

	routes := walkRoutes(t, a)
	if len(routes) < 150 {
		t.Fatalf("walked only %d routes", len(routes))
	}
	if code, _ := outsider.do("GET", "/repos/"+repoID, nil); code != 404 {
		t.Fatalf("outsider reads the repo: %d", code)
	}
	checked := 0
	var leaks []string
	for _, r := range routes {
		method, route, _ := strings.Cut(r, " ")
		path := fill(route, ids)
		var body any
		if method != http.MethodGet && method != http.MethodDelete {
			body = map[string]any{}
		}
		if !publicRoutes[r] {
			if code, _ := stranger.do(method, path, body); code < 400 || code >= 500 {
				leaks = append(leaks, "stranger "+r+" → "+strconv.Itoa(code))
			}
		}
		if !publicRoutes[r] && !anyUserRoutes.MatchString(r) {
			checked++
			if code, _ := outsider.do(method, path, body); code < 400 || code >= 500 {
				leaks = append(leaks, "outsider "+r+" → "+strconv.Itoa(code))
			}
		}
	}
	t.Logf("%d routes, %d checked as an outsider", len(routes), checked)
	if len(leaks) > 0 {
		t.Fatalf("routes that let the wrong person in (or crashed):\n  %s", strings.Join(leaks, "\n  "))
	}
}
