package app

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"io"
	"strings"
	"testing"

	"github.com/kmdn-app/kmdn/internal/blobs"
	"github.com/kmdn-app/kmdn/internal/config"
	"github.com/kmdn-app/kmdn/internal/groups"
	"github.com/kmdn-app/kmdn/internal/lifecycle"
	"github.com/kmdn-app/kmdn/internal/orgs"
	"github.com/kmdn-app/kmdn/internal/store"
	"github.com/kmdn-app/kmdn/internal/storetest"
	"github.com/kmdn-app/kmdn/internal/users"
)

// An org's archive loads into another instance with its revisions, threads,
// uploads and people, and again into the same instance as a copy.
func TestExportImportOrg(t *testing.T) {
	a, root := newApp(t, multiOrgs)
	ctx := context.Background()
	olga, _ := users.Create(ctx, a.DB, "olga@acme.dev", "Olga", false)
	pat, _ := users.Create(ctx, a.DB, "pat@acme.dev", "Pat", false)
	signIn(t, a, root, olga)
	_, org := root.do("POST", "/orgs", map[string]any{"name": "Acme", "slug": "acme"})
	orgID := org["id"].(string)
	repoID, _ := connectLocalIn(t, a, root, "acme", map[string]string{"docs/index.md": "# Acme\n"})
	_ = orgs.AddMember(ctx, a.DB, orgID, pat.ID, orgs.Member, "")
	_, rev := root.do("POST", "/repos/"+repoID+"/revisions", map[string]any{"title": "Welcome page"})
	revID := rev["id"].(string)
	root.do("POST", "/revisions/"+revID+"/files", map[string]any{"op": "add", "path": "docs/welcome.md", "content": "# Welcome\n"})
	if code, b := root.do("POST", "/revisions/"+revID+"/threads", map[string]any{"path": "docs/index.md", "body": "Keep this short."}); code != 201 {
		t.Fatalf("thread: %d %v", code, b)
	}
	// A name that looks like someone else's id stays a name.
	zed, _ := users.Create(ctx, a.DB, "zed@globex.dev", "Zed", false)
	if _, err := groups.Create(ctx, a.DB, orgID, zed.ID, ""); err != nil {
		t.Fatal(err)
	}
	img := pngBytes(t)
	if code, up := root.upload("/revisions/"+revID+"/assets", "docs/welcome.md", "desk.png", img); code != 201 {
		t.Fatalf("upload: %d %v", code, up)
	}

	// Export: owners and admins; members can't.
	patC := &tc{t: t, base: root.base, c: newClient()}
	signIn(t, a, patC, pat)
	if res, _ := patC.c.Get(root.base + "/api/v1/orgs/acme/admin/export"); res.StatusCode != 403 {
		t.Fatalf("member export: %d", res.StatusCode)
	}
	res, err := root.c.Get(root.base + "/api/v1/orgs/acme/admin/export")
	if err != nil {
		t.Fatal(err)
	}
	archive, _ := io.ReadAll(res.Body)
	_ = res.Body.Close()
	if res.StatusCode != 200 || !strings.Contains(res.Header.Get("Content-Disposition"), "acme-") || len(archive) < 100 {
		t.Fatalf("export: %d %d bytes", res.StatusCode, len(archive))
	}
	if unzipped := gunzip(t, archive); strings.Contains(unzipped, "zed@globex.dev") {
		t.Fatal("the export names an outsider")
	}
	var exported int
	_ = store.QueryRow(ctx, a.DB, `SELECT COUNT(*) FROM audit_log WHERE org_id = ? AND action = 'org.exported'`, orgID).Scan(&exported)
	if exported != 1 {
		t.Fatal("export not audited")
	}

	// Another instance.
	b, bc := newApp(t, multiOrgs)
	got, err := b.Lifecycle.Import(ctx, bytes.NewReader(archive), lifecycle.ImportOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if got.OrgID != orgID || got.Slug != "acme" || got.Users != 2 || got.Uploads != 1 || got.Rows["revisions"] != 1 || got.Rows["comments"] != 1 {
		t.Fatalf("import: %+v", got)
	}
	olgaB, err := users.ByEmail(ctx, b.DB, "olga@acme.dev")
	if err != nil {
		t.Fatal(err)
	}
	signIn(t, b, bc, olgaB)
	if code, o := bc.do("GET", "/orgs/acme", nil); code != 200 || o["role"] != "owner" {
		t.Fatalf("org in the other instance: %d %v", code, o)
	}
	if _, th := bc.do("GET", "/revisions/"+revID+"/threads", nil); !strings.Contains(toJSON(th), "Keep this short.") || !strings.Contains(toJSON(th), "Olga") {
		t.Fatalf("threads: %v", th)
	}
	var sha string
	_ = store.QueryRow(ctx, b.DB, `SELECT sha256 FROM uploads WHERE org_id = ?`, orgID).Scan(&sha)
	if got, err := b.Revisions.Blobs.Get(ctx, blobs.Key(orgID, sha)); err != nil || !bytes.Equal(got, img) {
		t.Fatalf("upload bytes: %v", err)
	}
	var token any
	_ = store.QueryRow(ctx, b.DB, `SELECT token_ref FROM repos WHERE id = ?`, repoID).Scan(&token)
	if token != nil {
		t.Fatalf("a credential reference came along: %v", token)
	}

	// The same instance: a copy under another slug, with new ids.
	cp, err := a.Lifecycle.Import(ctx, bytes.NewReader(archive), lifecycle.ImportOptions{Slug: "acme-copy"})
	if err != nil {
		t.Fatal(err)
	}
	if cp.OrgID == orgID || cp.Users != 0 {
		t.Fatalf("copy: %+v", cp)
	}
	var revs int
	_ = store.QueryRow(ctx, a.DB, `SELECT COUNT(*) FROM revisions r JOIN repos p ON p.id = r.repo_id WHERE p.org_id = ?`, cp.OrgID).Scan(&revs)
	if revs != 1 {
		t.Fatalf("revisions in the copy: %d", revs)
	}
	if _, err := a.Lifecycle.Import(ctx, bytes.NewReader(archive), lifecycle.ImportOptions{Slug: "acme-copy"}); err == nil {
		t.Fatal("imported onto a taken slug")
	}
	if _, err := a.Lifecycle.Import(ctx, strings.NewReader("not an archive"), lifecycle.ImportOptions{}); err == nil {
		t.Fatal("garbage imported")
	}

	// An edited archive can't reach outside itself: into another org, to an
	// account it doesn't list, or to another org's rows.
	c, cc := newApp(t, multiOrgs)
	_ = cc
	victimOrg, _ := users.Create(ctx, c.DB, "vic@victim.dev", "Vic", false)
	for name, row := range map[string]string{
		"another org":     `{"org_id":"` + orgs.DefaultID + `","user_id":"` + olga.ID + `","role":"owner","status":"active","joined_at":0}`,
		"another account": `{"org_id":"` + orgID + `","user_id":"` + victimOrg.ID + `","role":"member","status":"active","joined_at":0}`,
	} {
		edited := editArchive(t, archive, "tables/org_members.ndjson", row)
		if _, err := c.Lifecycle.Import(ctx, bytes.NewReader(edited), lifecycle.ImportOptions{Slug: "acme-" + strings.ReplaceAll(name, " ", "-")}); err == nil {
			t.Fatalf("%s: imported", name)
		}
	}
	edited := editArchive(t, archive, "tables/repo_members.ndjson", `{"repo_id":"rp_01m3k3s0sk0ex9xqdxfqse05dm","principal_type":"user","principal_id":"`+olga.ID+`","role":"admin","added_at":0}`)
	if _, err := c.Lifecycle.Import(ctx, bytes.NewReader(edited), lifecycle.ImportOptions{Slug: "acme-repo"}); err == nil || !strings.Contains(err.Error(), "isn't in the archive") {
		t.Fatalf("a grant on a repository outside the archive: %v", err)
	}
	var orgsThere int
	_ = store.QueryRow(ctx, c.DB, `SELECT COUNT(*) FROM orgs WHERE slug LIKE 'acme%'`).Scan(&orgsThere)
	if orgsThere != 0 {
		t.Fatalf("a refused import left %d orgs", orgsThere)
	}
}

func gunzip(t *testing.T, b []byte) string {
	t.Helper()
	zr, err := gzip.NewReader(bytes.NewReader(b))
	if err != nil {
		t.Fatal(err)
	}
	out, _ := io.ReadAll(zr)
	return string(out)
}

// editArchive appends line to the archive's file name.
func editArchive(t *testing.T, archive []byte, name, line string) []byte {
	t.Helper()
	zr, err := gzip.NewReader(bytes.NewReader(archive))
	if err != nil {
		t.Fatal(err)
	}
	tr := tar.NewReader(zr)
	var out bytes.Buffer
	zw := gzip.NewWriter(&out)
	tw := tar.NewWriter(zw)
	for {
		h, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		b, _ := io.ReadAll(tr)
		if h.Name == name {
			b = append(b, []byte(line+"\n")...)
			h.Size = int64(len(b))
		}
		if err := tw.WriteHeader(h); err != nil {
			t.Fatal(err)
		}
		_, _ = tw.Write(b)
	}
	_ = tw.Close()
	_ = zw.Close()
	return out.Bytes()
}

// An archive from SQLite loads into Postgres (booleans, blobs and the
// Postgres-only org_id columns differ).
func TestExportAcrossEngines(t *testing.T) {
	if !storetest.PostgresEnabled() {
		t.Skip("needs Postgres")
	}
	ctx := context.Background()
	a, root := newApp(t, func(c *config.Config) { multiOrgs(c); c.DB.URL = "sqlite://" + c.DataDir + "/kmdn.db" })
	olga, _ := users.Create(ctx, a.DB, "olga@acme.dev", "Olga", false)
	signIn(t, a, root, olga)
	root.do("POST", "/orgs", map[string]any{"name": "Acme", "slug": "acme"})
	repoID, _ := connectLocalIn(t, a, root, "acme", map[string]string{"docs/index.md": "# Acme\n"})
	_, rev := root.do("POST", "/repos/"+repoID+"/revisions", map[string]any{"title": "Edit"})
	root.do("POST", "/revisions/"+rev["id"].(string)+"/threads", map[string]any{"path": "docs/index.md", "body": "Across engines."})
	root.upload("/revisions/"+rev["id"].(string)+"/assets", "docs/index.md", "a.png", pngBytes(t))
	var buf bytes.Buffer
	o, _ := orgs.BySlug(ctx, a.DB, "acme")
	if _, err := a.Lifecycle.Export(ctx, o.ID, &buf); err != nil {
		t.Fatal(err)
	}
	b, _ := newApp(t, multiOrgs)
	if b.DB.Dialect != store.Postgres || a.DB.Dialect != store.SQLite {
		t.Fatalf("engines: %s → %s", a.DB.Dialect, b.DB.Dialect)
	}
	got, err := b.Lifecycle.Import(ctx, &buf, lifecycle.ImportOptions{})
	if err != nil || got.Rows["comments"] != 1 {
		t.Fatalf("import into Postgres: %+v %v", got, err)
	}
}
