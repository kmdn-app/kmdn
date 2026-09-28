package app

import (
	"context"
	"strings"
	"testing"

	"github.com/kmdn-app/kmdn/internal/store"
	"github.com/kmdn-app/kmdn/internal/storetest"
	"github.com/kmdn-app/kmdn/internal/users"
)

// Row-level security holds without the service layer: a statement scoped to
// one org, with no org filter of its own, sees none of another org's rows in
// any tenant table and can't write any.
func TestRowLevelSecurity(t *testing.T) {
	if !storetest.PostgresEnabled() {
		t.Skip("row-level security is Postgres only")
	}
	a, root := newApp(t, multiOrgs)
	ctx := context.Background()
	for _, slug := range []string{"acme", "globex"} {
		u, _ := users.Create(ctx, a.DB, "owner@"+slug+".dev", slug, false)
		c := &tc{t: t, base: root.base, c: newClient()}
		signIn(t, a, c, u)
		c.do("POST", "/orgs", map[string]any{"name": slug, "slug": slug})
		repoID, _ := connectLocalIn(t, a, c, slug, map[string]string{"docs/index.md": "# " + slug + "\n"})
		_, rev := c.do("POST", "/repos/"+repoID+"/revisions", map[string]any{"title": "Edit"})
		revID := rev["id"].(string)
		c.do("POST", "/revisions/"+revID+"/files", map[string]any{"op": "add", "path": "docs/new.md", "content": "# New\n"})
		if code, b := c.do("POST", "/revisions/"+revID+"/threads", map[string]any{"path": "docs/index.md", "body": "A note."}); code != 201 {
			t.Fatalf("thread in %s: %d %v", slug, code, b)
		}
		c.do("POST", "/orgs/"+slug+"/admin/groups", map[string]any{"name": "Team"})
	}
	var acme, globex string
	_ = store.QueryRow(ctx, a.DB, `SELECT id FROM orgs WHERE slug = 'acme'`).Scan(&acme)
	_ = store.QueryRow(ctx, a.DB, `SELECT id FROM orgs WHERE slug = 'globex'`).Scan(&globex)

	rows, err := store.Query(ctx, a.DB, `SELECT tablename FROM pg_policies WHERE policyname = 'kmdn_org' AND schemaname = current_schema() ORDER BY 1`)
	if err != nil {
		t.Fatal(err)
	}
	var tables []string
	for rows.Next() {
		var n string
		_ = rows.Scan(&n)
		tables = append(tables, n)
	}
	rows.Close()
	if len(tables) < 50 {
		t.Fatalf("policies on %d tables", len(tables))
	}
	scoped := store.WithOrg(ctx, acme)
	var seen []string
	for _, tbl := range tables {
		col := "org_id"
		if tbl == "orgs" {
			col = "id"
		}
		var all, visible int
		if err := store.QueryRow(ctx, a.DB, `SELECT COUNT(*) FROM `+tbl+` WHERE `+col+` = ?`, globex).Scan(&all); err != nil {
			t.Fatalf("%s: %v", tbl, err)
		}
		if err := store.QueryRow(scoped, a.DB, `SELECT COUNT(*) FROM `+tbl+` WHERE `+col+` = ?`, globex).Scan(&visible); err != nil {
			t.Fatalf("%s scoped: %v", tbl, err)
		}
		if visible != 0 {
			t.Errorf("%s: acme's scope sees %d of globex's rows", tbl, visible)
		}
		if all > 0 {
			seen = append(seen, tbl)
		}
	}
	// The fixture put rows in the tables that matter, so the check means something.
	for _, want := range []string{"repos", "revisions", "revision_files", "threads", "comments", "org_members", "groups"} {
		if !strings.Contains(","+strings.Join(seen, ",")+",", ","+want+",") {
			t.Errorf("fixture has no globex rows in %s (has %v)", want, seen)
		}
	}

	// Writes into another org's rows fail or touch nothing.
	var globexRepo string
	_ = store.QueryRow(ctx, a.DB, `SELECT id FROM repos WHERE org_id = ?`, globex).Scan(&globexRepo)
	if res, err := store.Exec(scoped, a.DB, `UPDATE repos SET name = 'taken' WHERE id = ?`, globexRepo); err != nil {
		t.Fatal(err)
	} else if n, _ := res.RowsAffected(); n != 0 {
		t.Fatalf("updated %d of globex's repos", n)
	}
	if _, err := store.Exec(scoped, a.DB, `INSERT INTO repo_members (repo_id, principal_type, principal_id, role, added_at) VALUES (?, 'user', 'usr_x', 'admin', 0)`, globexRepo); err == nil || !strings.Contains(err.Error(), "org_id") {
		t.Fatalf("inserted a member into globex's repo: %v", err)
	}
	// A transaction keeps its scope, and a rolled-back one doesn't leak it.
	_ = a.DB.InTx(scoped, func(tx *store.Tx) error {
		var n int
		_ = store.QueryRow(scoped, tx, `SELECT COUNT(*) FROM repos`).Scan(&n)
		if n != 1 {
			t.Errorf("repos in acme's transaction: %d", n)
		}
		return context.Canceled
	})
	var n int
	_ = store.QueryRow(ctx, a.DB, `SELECT COUNT(*) FROM repos`).Scan(&n)
	if n != 2 {
		t.Fatalf("unscoped repos after a scoped rollback: %d", n)
	}
}
