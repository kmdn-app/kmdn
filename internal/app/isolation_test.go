package app

import (
	"context"
	"net/http"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"testing"

	"github.com/kmdn-app/kmdn/internal/mcp"
	"github.com/kmdn-app/kmdn/internal/orgs"
	"github.com/kmdn-app/kmdn/internal/repos"
	"github.com/kmdn-app/kmdn/internal/users"
)

// TestCrossOrgMatrix walks every API route as the owner of one org with
// another org's slugs and ids (docs/specs/16-organizations.md#isolation):
// nothing may succeed, nothing may crash, and resources of the other org
// answer 404 so their ids can't be probed. A route added later is covered
// without changing this test.
func TestCrossOrgMatrix(t *testing.T) {
	a, alice := newApp(t, multiOrgs)
	ctx := context.Background()
	aliceU, _ := users.Create(ctx, a.DB, "alice@acme.dev", "Alice", false)
	bobU, _ := users.Create(ctx, a.DB, "bob@globex.dev", "Bob", false)
	signIn(t, a, alice, aliceU)
	bob := &tc{t: t, base: alice.base, c: newClient()}
	signIn(t, a, bob, bobU)
	alice.do("POST", "/orgs", map[string]any{"name": "Acme", "slug": "acme"})
	bob.do("POST", "/orgs", map[string]any{"name": "Globex", "slug": "globex"})
	connectLocalIn(t, a, alice, "acme", map[string]string{"docs/index.md": "# Acme\n"})

	// Everything Globex has that an id can point at.
	repoID, _ := connectLocalIn(t, a, bob, "globex", map[string]string{"docs/index.md": "# Globex\n\nWork from anywhere.\n"})
	repo, _ := repos.Get(ctx, a.DB, repoID)
	_, rev := bob.do("POST", "/repos/"+repoID+"/revisions", map[string]any{"title": "A change", "path": "docs/index.md"})
	revID := rev["id"].(string)
	_, th := bob.do("GET", "/revisions/"+revID+"/assistant", nil)
	_, d := bob.do("POST", "/repos/"+repoID+"/discussions", map[string]any{"path": "docs/index.md", "anchor": map[string]any{"quote": "Work from anywhere"}, "body": "Still true?"})
	_, g := bob.do("POST", "/orgs/globex/admin/groups", map[string]any{"name": "Writers"})
	_, k := bob.do("POST", "/orgs/globex/admin/agent-keys", map[string]any{"name": "Bot", "all_repos": true})
	_, h := bob.do("POST", "/repos/"+repoID+"/hooks", map[string]any{"kind": "generic", "url": "https://hooks.globex.dev/kmdn", "events": []string{"revision.published"}})
	_, fh := bob.do("POST", "/orgs/globex/admin/forges", map[string]any{"kind": "gitlab", "base_url": "https://gitlab.globex.dev"})
	bob.do("POST", "/orgs/globex/admin/domains", map[string]any{"domain": "globex.dev"})
	ids := map[string]string{
		"repo": repoID, "revision": revID, "number": strconv.Itoa(int(rev["number"].(float64))),
		"owner": repo.Owner, "name": repo.Name, "user": bobU.ID, "domain": "globex.dev",
		"group": g["id"].(string), "key": k["key"].(map[string]any)["id"].(string), "hook": h["hook"].(map[string]any)["id"].(string),
		"host": fh["id"].(string), "kind": "+1",
	}
	if tm, ok := th["thread"].(map[string]any); ok {
		ids["thread"] = tm["id"].(string)
	}
	if d != nil {
		ids["discussion"] = d["id"].(string)
		if cs, ok := d["comments"].([]any); ok && len(cs) > 0 {
			ids["comment"] = cs[0].(map[string]any)["id"].(string)
		}
	}
	for name, id := range ids {
		if id == "" {
			t.Fatalf("no %s to point at", name)
		}
	}
	// {id} means what the route's collection holds.
	idFor := func(route string) string {
		switch {
		case strings.Contains(route, "/groups/"):
			return ids["group"]
		case strings.Contains(route, "/agent-keys/"):
			return ids["key"]
		case strings.Contains(route, "/forges/"):
			return ids["host"]
		case strings.Contains(route, "/users/"), strings.Contains(route, "/members/"):
			return ids["user"]
		}
		return ids["group"]
	}
	fillB := func(route, org string) string {
		route = strings.Replace(route, "/*", "/docs/index.md", 1)
		return param.ReplaceAllStringFunc(route, func(p string) string {
			switch name := strings.Trim(p, "{}"); name {
			case "org":
				return org
			case "id":
				return idFor(route)
			case "thread":
				if strings.HasPrefix(route, "/threads/") {
					return ids["discussion"]
				}
				return ids["thread"]
			case "path":
				return "docs/index.md"
			default:
				if v, ok := ids[name]; ok {
					return v
				}
				return "x_does_not_exist"
			}
		})
	}

	var leaks, probes []string
	checked := 0
	for _, r := range walkRoutes(t, a) {
		method, route, _ := strings.Cut(r, " ")
		if publicRoutes[r] || personal.MatchString(r) {
			continue
		}
		var body any
		if method != http.MethodGet && method != http.MethodDelete {
			body = map[string]any{}
		}
		// Under /orgs/{org}: Globex itself, and Acme with Globex's ids (a
		// route on Acme with none of Globex's ids is just Alice's own).
		var targets []string
		if acme := fillB(route, "acme"); acme != strings.Replace(route, "{org}", "acme", 1) {
			targets = append(targets, acme)
		}
		if strings.Contains(route, "{org}") {
			targets = append(targets, fillB(route, "globex"))
		}
		if browserFlows[r] {
			nc := &http.Client{Jar: alice.c.Jar, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
			for _, path := range targets {
				res, err := nc.Get(alice.base + "/api/v1" + path)
				if err != nil {
					t.Fatal(err)
				}
				res.Body.Close()
				if loc := res.Header.Get("Location"); res.StatusCode != 404 && !strings.Contains(loc, "_error=") {
					leaks = append(leaks, r+" "+path+" → "+strconv.Itoa(res.StatusCode)+" "+loc)
				}
			}
			continue
		}
		for _, path := range targets {
			checked++
			code, _ := alice.do(method, path, body)
			switch {
			case code >= 500 || code < 400:
				leaks = append(leaks, r+" "+path+" → "+strconv.Itoa(code))
			case code != 404 && pointsAtGlobex(route, path):
				probes = append(probes, r+" "+path+" → "+strconv.Itoa(code))
			}
		}
	}
	t.Logf("%d requests with Globex's slugs and ids", checked)
	if checked < 150 {
		t.Fatalf("checked only %d requests", checked)
	}
	sort.Strings(leaks)
	if len(leaks) > 0 {
		t.Errorf("an owner of another org got through (or crashed):\n  %s", strings.Join(leaks, "\n  "))
	}
	if len(probes) > 0 {
		t.Errorf("another org's resources answer something other than 404:\n  %s", strings.Join(probes, "\n  "))
	}

	// Beyond HTTP: live updates and agent keys stay inside their org.
	for _, scope := range []string{"repo:" + repoID, "revision:" + revID, "assistant:" + ids["thread"]} {
		if a.authorizeScope(ctx, aliceU, scope) {
			t.Errorf("Alice follows %s", scope)
		}
		if !a.authorizeScope(ctx, bobU, scope) {
			t.Errorf("Bob can't follow %s", scope)
		}
	}
	_, ak := alice.do("POST", "/orgs/acme/admin/agent-keys", map[string]any{"name": "Acme bot", "all_repos": true})
	acmeKey, err := mcp.GetKey(ctx, a.DB, ak["key"].(map[string]any)["id"].(string))
	if err != nil {
		t.Fatal(err)
	}
	if acmeKey.Sees(repo) {
		t.Fatal("Acme's all-repos key sees Globex's repo")
	}
	globexKey, _ := mcp.GetKey(ctx, a.DB, ids["key"])
	if o, _ := orgs.BySlug(ctx, a.DB, "globex"); globexKey.OrgID != o.ID || !globexKey.Sees(repo) {
		t.Fatalf("Globex's key: %+v", globexKey)
	}
}

// pointsAtGlobex reports whether a filled path names a Globex resource
// (rather than only an Acme collection).
func pointsAtGlobex(route, path string) bool {
	if strings.HasPrefix(route, "/admin/") {
		return false // the instance console: Alice isn't an instance admin (403)
	}
	return strings.Contains(path, "/globex") || !strings.Contains(route, "{org}")
}

// browserFlows redirect the browser; for another org's resources they must
// end on an error page.
var browserFlows = map[string]bool{"GET /orgs/{org}/admin/forges/{id}/connect": true}

// personal routes act on the caller themselves, not on an org's resources.
var personal = regexpMust(`^(GET|PATCH|POST|DELETE|PUT) /(me\b|inbox|notifications|push|setup/complete|auth/|orgs$|admin/forges/github/(callback|setup)$)`)

func regexpMust(s string) *regexp.Regexp { return regexp.MustCompile(s) }
