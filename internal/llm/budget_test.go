package llm

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/kmdn-app/kmdn/internal/store"
	"github.com/kmdn-app/kmdn/internal/storetest"
)

// Each org's monthly budget is its own: one org using it up doesn't stop
// another, and the instance's cap still applies to everyone.
func TestOrgBudgets(t *testing.T) {
	ctx := context.Background()
	db := storetest.Open(t)
	now := store.Millis(time.Now())
	for _, q := range []struct {
		sql  string
		args []any
	}{
		{`INSERT INTO orgs (id, slug, name, created_at) VALUES ('org_a', 'a-org', 'A', 0), ('org_b', 'b-org', 'B', 0)`, nil},
		{`INSERT INTO forge_hosts (id, kind, display_name, created_at) VALUES ('fh_1', 'git', 'Git', 0)`, nil},
		{`INSERT INTO repos (id, org_id, forge_host_id, owner, name, display_name, target_branch, created_at) VALUES ('rep_a', 'org_a', 'fh_1', 'o', 'a', 'a', 'main', 0), ('rep_b', 'org_b', 'fh_1', 'o', 'b', 'b', 'main', 0)`, nil},
		{`INSERT INTO assistant_runs (id, org_id, repo_id, task, provider, model, input_tokens, status, started_at) VALUES ('run_1', 'org_a', 'rep_a', 'chat', 'x', 'y', 900, 'done', ?)`, []any{now}},
	} {
		if _, err := store.Exec(ctx, db, q.sql, q.args...); err != nil {
			t.Fatal(err)
		}
	}
	budgets := map[string]int{"org_a": 1000, "org_b": 1000}
	s := &Service{DB: db, OrgBudget: func(_ context.Context, org string) int { return budgets[org] }}
	if err := s.CheckBudget(ctx, "", "rep_a"); err != nil {
		t.Fatalf("under budget: %v", err)
	}
	if _, err := store.Exec(ctx, db, `UPDATE assistant_runs SET input_tokens = 1000 WHERE id = 'run_1'`); err != nil {
		t.Fatal(err)
	}
	var be *ErrBudget
	if err := s.CheckBudget(ctx, "", "rep_a"); !errors.As(err, &be) {
		t.Fatalf("org a over budget: %v", err)
	}
	if err := s.CheckBudget(ctx, "", "rep_b"); err != nil {
		t.Fatalf("org b affected by org a: %v", err)
	}
	budgets["org_a"] = 0 // no budget of its own
	if err := s.CheckBudget(ctx, "", "rep_a"); err != nil {
		t.Fatalf("no org budget: %v", err)
	}
}
