package search

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/kmdn-app/kmdn/internal/store"
	"github.com/kmdn-app/kmdn/internal/storetest"
)

func TestExtract(t *testing.T) {
	d := Extract("---\ntitle: \"Your first week\"\n---\n\n# Welcome\n\nPick up your **laptop** from [IT](it.md).\n\n## Laptop policy\n\n- [x] Choose a *MacBook*\n\n```bash\nbrew install tailscale\n```\n\n{{< youtube id >}}\n", "first-week")
	if d.Title != "Your first week" || len(d.Headings) != 2 || d.Headings[1] != "Laptop policy" {
		t.Fatalf("%+v", d)
	}
	if !strings.Contains(d.Body, "Pick up your laptop from IT.") || !strings.Contains(d.Body, "Choose a MacBook") || !strings.Contains(d.Body, "brew install tailscale") || strings.Contains(d.Body, "youtube") {
		t.Fatalf("body: %q", d.Body)
	}
	if Extract("Setext\n======\n\ntext", "x").Title != "Setext" {
		t.Fatal("setext title")
	}
	if Slug("Your laptop (2026)!") != "your-laptop-2026" {
		t.Fatal(Slug("Your laptop (2026)!"))
	}
}

func TestIndexAndSearch(t *testing.T) {
	db := storetest.Open(t)
	ctx := context.Background()
	if _, err := store.Exec(ctx, db, `INSERT INTO forge_hosts (id, kind, display_name, created_at) VALUES ('fh', 'git', 'g', 1)`); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Exec(ctx, db, `INSERT INTO repos (id, forge_host_id, owner, name, display_name, target_branch, created_at) VALUES ('rep_1', 'fh', 'o', 'n', 'n', 'main', ?)`, store.Millis(time.Now())); err != nil {
		t.Fatal(err)
	}
	x := &Index{DB: db}
	put := func(p, md string) {
		if err := x.Put(ctx, db, "rep_1", Published, p, "sha-"+p, Extract(md, p)); err != nil {
			t.Fatal(err)
		}
	}
	put("docs/it-setup.md", "# IT setup\n\n## Your laptop\n\nYour laptop is waiting on your desk.\n")
	put("docs/remote.md", "# Remote work\n\nYou can work from abroad for 30 days.\n")
	put("docs/laptops.md", "# Laptop policy\n\nLaptops are refreshed every three years.\n")
	hits, err := x.Search(ctx, "rep_1", Published, "lapt", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) != 2 {
		t.Fatalf("hits: %+v", hits)
	}
	var itHit Hit
	for _, h := range hits {
		if h.Path == "docs/it-setup.md" {
			itHit = h
		}
	}
	if itHit.Heading != "Your laptop" || itHit.HeadingSlug != "your-laptop" || !strings.Contains(itHit.Snippet, "\x02laptop\x03") {
		t.Fatalf("heading hit: %+v", itHit)
	}
	if hits, _ := x.Search(ctx, "rep_1", Published, "abroad days", 10); len(hits) != 1 || hits[0].Title != "Remote work" {
		t.Fatalf("multi-term: %+v", hits)
	}
	put("docs/remote.md", "# Remote work\n\nNo more travel.\n")
	if hits, _ := x.Search(ctx, "rep_1", Published, "abroad", 10); len(hits) != 0 {
		t.Fatalf("stale index: %+v", hits)
	}
	if err := x.Delete(ctx, db, "rep_1", Published, "docs/laptops.md"); err != nil {
		t.Fatal(err)
	}
	if hits, _ := x.Search(ctx, "rep_1", Published, "refreshed", 10); len(hits) != 0 {
		t.Fatal("deleted doc still found")
	}
	if hits, _ := x.Search(ctx, "rep_1", Published, `"; DROP TABLE x; --`, 10); hits == nil {
		t.Fatal("hostile query should just return no hits")
	}
	idx, _ := x.Indexed(ctx, "rep_1", Published)
	if len(idx) != 2 || idx["docs/it-setup.md"] != "sha-docs/it-setup.md" {
		t.Fatalf("indexed: %v", idx)
	}
}
