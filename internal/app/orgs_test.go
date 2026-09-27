package app

import (
	"context"
	"strings"
	"testing"

	"github.com/kmdn-app/kmdn/internal/config"
	"github.com/kmdn-app/kmdn/internal/mail"
	"github.com/kmdn-app/kmdn/internal/users"
)

func multiOrgs(c *config.Config) { c.Orgs.Mode = "multi"; c.Orgs.AllowCreate = "anyone" }

// Two orgs on one instance see nothing of each other.
func TestOrgsKeepToThemselves(t *testing.T) {
	a, root := newApp(t, multiOrgs)
	ctx := context.Background()
	alice, _ := users.Create(ctx, a.DB, "alice@acme.dev", "Alice", false)
	bob, _ := users.Create(ctx, a.DB, "bob@globex.dev", "Bob", false)
	signIn(t, a, root, alice)
	bobC := &tc{t: t, base: root.base, c: newClient()}
	signIn(t, a, bobC, bob)

	code, acme := root.do("POST", "/orgs", map[string]any{"name": "Acme Docs"})
	if code != 201 || acme["slug"] != "acme-docs" || acme["role"] != "owner" {
		t.Fatalf("create acme: %d %v", code, acme)
	}
	if code, _ := bobC.do("POST", "/orgs", map[string]any{"name": "Globex", "slug": "acme-docs"}); code != 409 {
		t.Fatalf("taken slug: %d", code)
	}
	if code, _ := bobC.do("POST", "/orgs", map[string]any{"name": "Globex", "slug": "admin"}); code != 422 {
		t.Fatalf("reserved slug: %d", code)
	}
	if code, g := bobC.do("POST", "/orgs", map[string]any{"name": "Globex", "slug": "globex"}); code != 201 {
		t.Fatalf("create globex: %d %v", code, g)
	}
	acmeRepo, _ := connectLocalIn(t, a, root, "acme-docs", map[string]string{"docs/index.md": "# Acme\n"})
	globexRepo, _ := connectLocalIn(t, a, bobC, "globex", map[string]string{"docs/index.md": "# Globex\n"})

	// Each sees only their org.
	if code, l := root.do("GET", "/orgs", nil); code != 200 || len(l["items"].([]any)) != 1 {
		t.Fatalf("alice's orgs: %v", l)
	}
	for _, c := range []struct {
		who  *tc
		path string
		want int
	}{
		{root, "/orgs/acme-docs/repos", 200},
		{root, "/orgs/globex", 404},
		{root, "/orgs/globex/repos", 404},
		{root, "/orgs/default/repos", 404}, // the default org is an org like any other in multi mode
		{root, "/repos/" + globexRepo, 404},
		{bobC, "/repos/" + acmeRepo, 404},
		{bobC, "/repos/" + globexRepo, 200},
		{bobC, "/orgs/acme-docs/users?q=ali", 404},
		{root, "/orgs/acme-docs/repos/by-slug/x/y", 404},
	} {
		if code, b := c.who.do("GET", c.path, nil); code != c.want {
			t.Errorf("GET %s: %d, want %d (%v)", c.path, code, c.want, b)
		}
	}
	if _, l := root.do("GET", "/orgs/acme-docs/repos", nil); len(l["items"].([]any)) != 1 || l["items"].([]any)[0].(map[string]any)["id"] != acmeRepo {
		t.Fatalf("acme repos: %v", l)
	}
	// The directory only lists the org's members.
	if _, l := root.do("GET", "/orgs/acme-docs/users?q=bob", nil); len(l["items"].([]any)) != 0 {
		t.Fatalf("bob found in acme's directory: %v", l)
	}
	// Grants and groups can't reach outside the org.
	if code, _ := root.do("PUT", "/repos/"+acmeRepo+"/members/user/"+bob.ID, map[string]any{"role": "viewer"}); code != 422 {
		t.Fatalf("grant to an outsider: %d", code)
	}
	_, g := bobC.do("POST", "/orgs/globex/admin/groups", map[string]any{"name": "Writers"})
	if code, _ := root.do("PUT", "/repos/"+acmeRepo+"/members/group/"+g["id"].(string), map[string]any{"role": "viewer"}); code != 422 {
		t.Fatalf("grant to another org's group: %d", code)
	}
	if code, _ := root.do("GET", "/orgs/acme-docs/admin/groups/"+g["id"].(string)+"/members", nil); code != 404 {
		t.Fatalf("another org's group: %d", code)
	}
	if _, l := root.do("GET", "/orgs/acme-docs/admin/audit", nil); len(l["items"].([]any)) == 0 {
		t.Fatal("acme's audit log is empty")
	} else {
		for _, e := range l["items"].([]any) {
			if e.(map[string]any)["repo_id"] == globexRepo {
				t.Fatalf("globex event in acme's audit log: %v", e)
			}
		}
	}
}

func TestOrgMembers(t *testing.T) {
	a, root := newApp(t, multiOrgs)
	ctx := context.Background()
	alice, _ := users.Create(ctx, a.DB, "alice@acme.dev", "Alice", false)
	carl, _ := users.Create(ctx, a.DB, "carl@acme.dev", "Carl", false)
	signIn(t, a, root, alice)
	carlC := &tc{t: t, base: root.base, c: newClient()}
	signIn(t, a, carlC, carl)
	root.do("POST", "/orgs", map[string]any{"name": "Acme", "slug": "acme"})

	if code, _ := carlC.do("GET", "/orgs/acme", nil); code != 404 {
		t.Fatalf("carl before joining: %d", code)
	}
	if code, _ := root.do("PATCH", "/orgs/acme/members/"+carl.ID, map[string]any{"role": "admin"}); code != 404 {
		t.Fatalf("changing a non-member: %d", code)
	}
	if _, err := a.DB.ExecContext(ctx, `INSERT INTO org_members (org_id, user_id, role, status, joined_at) SELECT id, ?, 'member', 'active', 0 FROM orgs WHERE slug = 'acme'`, carl.ID); err != nil {
		t.Fatal(err)
	}
	if code, o := carlC.do("GET", "/orgs/acme", nil); code != 200 || o["role"] != "member" {
		t.Fatalf("carl as member: %d %v", code, o)
	}
	if code, _ := carlC.do("GET", "/orgs/acme/members", nil); code != 403 {
		t.Fatalf("member lists members: %d", code)
	}
	if code, _ := carlC.do("PATCH", "/orgs/acme", map[string]any{"name": "Carl's"}); code != 403 {
		t.Fatalf("member renames the org: %d", code)
	}
	if code, _ := root.do("PATCH", "/orgs/acme/members/"+alice.ID, map[string]any{"role": "member"}); code != 409 {
		t.Fatalf("demote the last owner: %d", code)
	}
	if code, _ := root.do("PATCH", "/orgs/acme/members/"+carl.ID, map[string]any{"role": "admin"}); code != 200 {
		t.Fatalf("make carl admin: %d", code)
	}
	if code, _ := carlC.do("PATCH", "/orgs/acme/members/"+alice.ID, map[string]any{"role": "member"}); code != 403 {
		t.Fatalf("admin demotes an owner: %d", code)
	}
	if code, _ := carlC.do("PATCH", "/orgs/acme", map[string]any{"slug": "carl"}); code != 403 {
		t.Fatalf("admin changes the address: %d", code)
	}
	if code, o := carlC.do("PATCH", "/orgs/acme", map[string]any{"name": "Acme Inc"}); code != 200 || o["name"] != "Acme Inc" {
		t.Fatalf("admin renames: %d %v", code, o)
	}
	code, l := carlC.do("GET", "/orgs/acme/members", nil)
	if code != 200 || len(l["items"].([]any)) != 2 {
		t.Fatalf("members: %d %v", code, l)
	}
	if code, _ := root.do("PATCH", "/orgs/acme/members/"+carl.ID, map[string]any{"status": "deactivated"}); code != 200 {
		t.Fatalf("deactivate carl: %d", code)
	}
	if code, _ := carlC.do("GET", "/orgs/acme", nil); code != 404 {
		t.Fatalf("deactivated member: %d", code)
	}
	if code, _ := root.do("DELETE", "/orgs/acme/members/"+alice.ID, nil); code != 409 {
		t.Fatalf("the last owner leaves: %d", code)
	}
	if code, _ := root.do("PATCH", "/orgs/acme", map[string]any{"slug": "acme-2"}); code != 200 {
		t.Fatalf("owner changes the address: %d", code)
	}
	if code, _ := root.do("GET", "/orgs/acme", nil); code != 404 {
		t.Fatalf("old address: %d", code)
	}
}

func TestSingleModeOrgs(t *testing.T) {
	a, root := newApp(t, nil)
	ctx := context.Background()
	maya, _ := users.Create(ctx, a.DB, "maya@northwind.dev", "Maya", true)
	tom, _ := users.Create(ctx, a.DB, "tom@northwind.dev", "Tom", false)
	signIn(t, a, root, maya)
	tomC := &tc{t: t, base: root.base, c: newClient()}
	signIn(t, a, tomC, tom)
	if code, l := tomC.do("GET", "/orgs", nil); code != 200 || len(l["items"].([]any)) != 1 || l["items"].([]any)[0].(map[string]any)["role"] != "member" {
		t.Fatalf("tom's orgs: %d %v", code, l)
	}
	if code, o := root.do("GET", "/orgs/default", nil); code != 200 || o["role"] != "owner" {
		t.Fatalf("admin in the default org: %d %v", code, o)
	}
	if code, _ := root.do("POST", "/orgs", map[string]any{"name": "Another"}); code != 409 {
		t.Fatalf("create in single mode: %d", code)
	}
	if code, _ := tomC.do("DELETE", "/orgs/default/members/"+tom.ID, nil); code != 409 {
		t.Fatalf("leave the only org: %d", code)
	}
	if _, l := root.do("GET", "/orgs/default/members", nil); len(l["items"].([]any)) != 2 {
		t.Fatalf("every account is a member: %v", l)
	}
}

// Invites bring people into one org; agent keys only see their org.
func TestOrgInvitesAndKeys(t *testing.T) {
	a, root := newApp(t, multiOrgs)
	ctx := context.Background()
	capture := &mail.Capture{}
	a.Invites.Mail = capture
	alice, _ := users.Create(ctx, a.DB, "alice@acme.dev", "Alice", false)
	bob, _ := users.Create(ctx, a.DB, "bob@globex.dev", "Bob", false)
	signIn(t, a, root, alice)
	bobC := &tc{t: t, base: root.base, c: newClient()}
	signIn(t, a, bobC, bob)
	root.do("POST", "/orgs", map[string]any{"name": "Acme", "slug": "acme"})
	bobC.do("POST", "/orgs", map[string]any{"name": "Globex", "slug": "globex"})
	acmeRepo, _ := connectLocalIn(t, a, root, "acme", map[string]string{"docs/index.md": "# Acme\n"})
	globexRepo, _ := connectLocalIn(t, a, bobC, "globex", map[string]string{"docs/index.md": "# Globex\n"})

	// An existing account outside the org gets an email to accept, not a silent grant.
	code, res := root.do("POST", "/orgs/acme/admin/invites", map[string]any{"email": "bob@globex.dev", "repo_id": acmeRepo, "role": "viewer"})
	if code != 201 || res["status"] != "invited" {
		t.Fatalf("invite bob: %d %v", code, res)
	}
	m, _ := capture.Last()
	if !strings.Contains(m.Text, " on Acme.") {
		t.Fatalf("invite mail names the org: %q", m.Text)
	}
	if code, _ := root.do("POST", "/orgs/acme/admin/invites", map[string]any{"email": "x@y.dev", "repo_id": globexRepo, "role": "viewer"}); code != 422 {
		t.Fatalf("invite to another org's repo: %d", code)
	}
	if code, _ := bobC.do("GET", "/repos/"+acmeRepo, nil); code != 404 {
		t.Fatalf("bob before accepting: %d", code)
	}
	token := inviteRe.FindStringSubmatch(m.Text)[1]
	if code, d := bobC.do("GET", "/invites/"+token, nil); code != 200 || d["org_name"] != "Acme" || d["has_account"] != true {
		t.Fatalf("details: %d %v", code, d)
	}
	if code, _ := bobC.do("POST", "/invites/"+token+"/accept", map[string]any{}); code != 200 {
		t.Fatalf("accept: %d", code)
	}
	if code, r := bobC.do("GET", "/repos/"+acmeRepo, nil); code != 200 || r["role"] != "viewer" {
		t.Fatalf("bob after accepting: %d %v", code, r)
	}
	// Bob kept his own org, as its owner.
	if _, o := bobC.do("GET", "/orgs/globex", nil); o["role"] != "owner" {
		t.Fatalf("bob in globex: %v", o)
	}
	if _, l := bobC.do("GET", "/orgs", nil); len(l["items"].([]any)) != 2 {
		t.Fatalf("bob's orgs: %v", l)
	}

	// Agent keys.
	if code, _ := root.do("POST", "/orgs/acme/admin/agent-keys", map[string]any{"name": "Bot", "repo_ids": []string{globexRepo}}); code != 422 {
		t.Fatalf("key for another org's repo: %d", code)
	}
	code, created := root.do("POST", "/orgs/acme/admin/agent-keys", map[string]any{"name": "Bot", "all_repos": true})
	if code != 201 {
		t.Fatalf("create key: %d %v", code, created)
	}
	keyID := created["key"].(map[string]any)["id"].(string)
	if _, l := bobC.do("GET", "/orgs/globex/admin/agent-keys", nil); len(l["items"].([]any)) != 0 {
		t.Fatalf("globex sees acme's key: %v", l)
	}
	if code, _ := bobC.do("POST", "/orgs/globex/admin/agent-keys/"+keyID+"/revoke", nil); code != 409 {
		t.Fatalf("globex revokes acme's key: %d", code)
	}
	if code, _ := bobC.do("GET", "/orgs/globex/admin/agent-keys/"+keyID+"/calls", nil); code != 404 {
		t.Fatalf("globex reads acme's key calls: %d", code)
	}
}

// An org can turn AI features off for its repositories.
func TestOrgSettingsAssistantSwitch(t *testing.T) {
	a, admin := newApp(t, nil)
	ctx := context.Background()
	maya, _ := users.Create(ctx, a.DB, "maya@northwind.dev", "Maya", true)
	tom, _ := users.Create(ctx, a.DB, "tom@northwind.dev", "Tom", false)
	signIn(t, a, admin, maya)
	tomC := &tc{t: t, base: admin.base, c: newClient()}
	signIn(t, a, tomC, tom)
	a.LLM.Override = &scripted{}

	if code, st := admin.do("GET", "/orgs/default/admin/settings", nil); code != 200 || st["settings"].(map[string]any)["assistant"] != true {
		t.Fatalf("settings: %d %v", code, st)
	}
	if code, _ := tomC.do("PATCH", "/orgs/default/admin/settings", map[string]any{"assistant": false}); code != 403 {
		t.Fatalf("member changes settings: %d", code)
	}
	if code, _ := admin.do("PATCH", "/orgs/default/admin/settings", map[string]any{"assistant": "no"}); code != 422 {
		t.Fatalf("bad value: %d", code)
	}
	if _, st := tomC.do("GET", "/orgs/default/assistant/status", nil); st["enabled"] != true {
		t.Fatalf("status before: %v", st)
	}
	if code, _ := admin.do("PATCH", "/orgs/default/admin/settings", map[string]any{"assistant": false}); code != 200 {
		t.Fatalf("turn off: %d", code)
	}
	if _, st := tomC.do("GET", "/orgs/default/assistant/status", nil); st["enabled"] != false {
		t.Fatalf("status after: %v", st)
	}
	repoID := connectLocal(t, a, admin, map[string]string{"docs/index.md": "# Handbook\n"})
	if code, b := admin.do("POST", "/repos/"+repoID+"/assistant/threads", map[string]any{"text": "Hi"}); code != 409 {
		t.Fatalf("assistant used while off: %d %v", code, b)
	}
}
