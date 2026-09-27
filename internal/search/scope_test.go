package search

import (
	"context"
	"fmt"
	"testing"

	"github.com/kmdn-app/kmdn/internal/store"
	"github.com/kmdn-app/kmdn/internal/storetest"
)

func TestExcludedHitsDoNotHideAllowedResults(t *testing.T) {
	db := storetest.Open(t)
	ctx := context.Background()
	if _, err := store.Exec(ctx, db, `INSERT INTO forge_hosts (id, kind, display_name, created_at) VALUES ('fh', 'git', 'Git', 1)`); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Exec(ctx, db, `INSERT INTO repos (id, forge_host_id, owner, name, display_name, target_branch, exclude_globs, created_at) VALUES ('rep', 'fh', 'o', 'n', 'n', 'main', '["hidden/**"]', 1)`); err != nil {
		t.Fatal(err)
	}
	x := Index{DB: db}
	for i := 0; i < 55; i++ {
		p := fmt.Sprintf("hidden/%02d.md", i)
		if err := x.Put(ctx, db, "rep", Published, p, "sha", Extract("# Needle\nSame content.", p)); err != nil {
			t.Fatal(err)
		}
	}
	if err := x.Put(ctx, db, "rep", Published, "visible.md", "sha", Extract("# Needle\nSame content.", "visible")); err != nil {
		t.Fatal(err)
	}
	hits, err := x.Search(ctx, "rep", Published, "Needle", 1)
	if err != nil || len(hits) != 1 || hits[0].Path != "visible.md" {
		t.Fatalf("allowed match lost behind stale entries: %v, %v", hits, err)
	}
}
