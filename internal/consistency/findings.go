package consistency

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"time"

	"github.com/kmdn-app/kmdn/internal/ids"
	"github.com/kmdn-app/kmdn/internal/store"
)

// Side is one of the two passages of a finding.
type Side struct {
	Path    string `json:"path"`
	Slug    string `json:"slug"`
	Heading string `json:"heading"`
	Line    int    `json:"line"`
	Text    string `json:"text"`
}

// FixRef is the revision fixing a finding.
type FixRef struct {
	ID     string `json:"id"`
	Number int    `json:"number"`
	Title  string `json:"title"`
	State  string `json:"state"`
}

// Finding is a contradiction or a duplicate between two passages.
type Finding struct {
	ID    string `json:"id"`
	Kind  string `json:"kind"`  // contradiction | duplicate
	Scope string `json:"scope"` // published | a revision id
	A     Side   `json:"a"`
	B     Side   `json:"b"`
	// ClaimA and ClaimB quote the conflicting claims (contradictions).
	ClaimA      string  `json:"claim_a,omitempty"`
	ClaimB      string  `json:"claim_b,omitempty"`
	Explanation string  `json:"explanation"`
	Similarity  float64 `json:"similarity"`
	// Status: open | ignored | closed.
	Status       string    `json:"status"`
	IgnoreReason string    `json:"ignore_reason,omitempty"`
	Fix          *FixRef   `json:"fix,omitempty"`
	FirstSeen    time.Time `json:"first_seen"`
	LastSeen     time.Time `json:"last_seen"`
	RepoID       string    `json:"-"`
	PairKey      string    `json:"-"`
}

// record stores a check's findings for a scope. A revision's findings are
// replaced; the repo report keeps history: findings not reproduced are
// closed unless their pair was left undecided (keep).
func (s *Service) record(ctx context.Context, repoID, scope string, found []Finding, keep map[string]bool) error {
	now := store.Millis(time.Now())
	return s.DB.InTx(ctx, func(tx *store.Tx) error {
		have := map[string]bool{}
		for _, f := range found {
			have[f.PairKey] = true
			if _, err := store.Exec(ctx, tx, `INSERT INTO consistency_findings (id, repo_id, scope, pair_key, kind, a_path, a_slug, a_heading, a_line, a_text, b_path, b_slug, b_heading, b_line, b_text, claim_a, claim_b, explanation, similarity, status, first_seen, last_seen)
				VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 'open', ?, ?)
				ON CONFLICT (repo_id, scope, pair_key) DO UPDATE SET kind = excluded.kind, a_path = excluded.a_path, a_slug = excluded.a_slug, a_heading = excluded.a_heading, a_line = excluded.a_line, a_text = excluded.a_text,
				b_path = excluded.b_path, b_slug = excluded.b_slug, b_heading = excluded.b_heading, b_line = excluded.b_line, b_text = excluded.b_text, claim_a = excluded.claim_a, claim_b = excluded.claim_b,
				explanation = excluded.explanation, similarity = excluded.similarity, status = 'open', last_seen = excluded.last_seen, closed_at = NULL`,
				ids.New("cf"), repoID, scope, f.PairKey, f.Kind, f.A.Path, f.A.Slug, f.A.Heading, f.A.Line, f.A.Text, f.B.Path, f.B.Slug, f.B.Heading, f.B.Line, f.B.Text,
				f.ClaimA, f.ClaimB, f.Explanation, int(f.Similarity*1000), now, now); err != nil {
				return err
			}
		}
		rows, err := store.Query(ctx, tx, `SELECT pair_key FROM consistency_findings WHERE repo_id = ? AND scope = ? AND status = 'open'`, repoID, scope)
		if err != nil {
			return err
		}
		var gone []string
		for rows.Next() {
			var k string
			if err := rows.Scan(&k); err != nil {
				rows.Close()
				return err
			}
			if !have[k] && !keep[k] {
				gone = append(gone, k)
			}
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return err
		}
		for _, k := range gone {
			q := `UPDATE consistency_findings SET status = 'closed', closed_at = ? WHERE repo_id = ? AND scope = ? AND pair_key = ?`
			args := []any{now, repoID, scope, k}
			if scope != ScopePublished {
				q, args = `DELETE FROM consistency_findings WHERE repo_id = ? AND scope = ? AND pair_key = ?`, args[1:]
			}
			if _, err := store.Exec(ctx, tx, q, args...); err != nil {
				return err
			}
		}
		return nil
	})
}

func (s *Service) ignored(ctx context.Context, repoID string) (map[string]bool, error) {
	rows, err := store.Query(ctx, s.DB, `SELECT pair_key FROM consistency_ignores WHERE repo_id = ?`, repoID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]bool{}
	for rows.Next() {
		var k string
		if err := rows.Scan(&k); err != nil {
			return nil, err
		}
		out[k] = true
	}
	return out, rows.Err()
}

const findingCols = `f.id, f.repo_id, f.scope, f.pair_key, f.kind, f.a_path, f.a_slug, f.a_heading, f.a_line, f.a_text, f.b_path, f.b_slug, f.b_heading, f.b_line, f.b_text,
	f.claim_a, f.claim_b, f.explanation, f.similarity, f.status, f.first_seen, f.last_seen, COALESCE(i.reason, ''), i.ignored_at IS NOT NULL,
	COALESCE(r.id, ''), COALESCE(r.number, 0), COALESCE(r.title, ''), COALESCE(r.state, '')`

const findingFrom = ` FROM consistency_findings f
	LEFT JOIN consistency_ignores i ON i.repo_id = f.repo_id AND i.pair_key = f.pair_key
	LEFT JOIN revisions r ON r.id = f.fix_revision_id`

func scanFinding(sc interface{ Scan(...any) error }) (Finding, error) {
	var f Finding
	var sim int
	var first, last int64
	var ignored bool
	var fix FixRef
	err := sc.Scan(&f.ID, &f.RepoID, &f.Scope, &f.PairKey, &f.Kind, &f.A.Path, &f.A.Slug, &f.A.Heading, &f.A.Line, &f.A.Text, &f.B.Path, &f.B.Slug, &f.B.Heading, &f.B.Line, &f.B.Text,
		&f.ClaimA, &f.ClaimB, &f.Explanation, &sim, &f.Status, &first, &last, &f.IgnoreReason, &ignored, &fix.ID, &fix.Number, &fix.Title, &fix.State)
	if err != nil {
		return f, err
	}
	f.Similarity = float64(sim) / 1000
	f.FirstSeen, f.LastSeen = time.UnixMilli(first).UTC(), time.UnixMilli(last).UTC()
	if ignored && f.Status == "open" {
		f.Status = "ignored"
	}
	if fix.ID != "" {
		f.Fix = &fix
	}
	return f, nil
}

// List returns a scope's findings: open ones first (contradictions, then
// by similarity), then ignored, then closed. status filters ("" = all).
func (s *Service) List(ctx context.Context, repoID, scope, status string) ([]Finding, error) {
	rows, err := store.Query(ctx, s.DB, `SELECT `+findingCols+findingFrom+` WHERE f.repo_id = ? AND f.scope = ? ORDER BY f.kind, f.similarity DESC`, repoID, scope)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Finding{}
	var later []Finding
	for rows.Next() {
		f, err := scanFinding(rows)
		if err != nil {
			return nil, err
		}
		if status != "" && f.Status != status {
			continue
		}
		if f.Status == "open" {
			out = append(out, f)
		} else {
			later = append(later, f)
		}
	}
	return append(out, later...), rows.Err()
}

// Get loads a finding.
func (s *Service) Get(ctx context.Context, id string) (Finding, error) {
	f, err := scanFinding(store.QueryRow(ctx, s.DB, `SELECT `+findingCols+findingFrom+` WHERE f.id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return f, store.ErrNotFound
	}
	return f, err
}

// Ignore remembers a pair as fine, with a reason, everywhere in the repo.
func (s *Service) Ignore(ctx context.Context, f Finding, by, reason string) error {
	_, err := store.Exec(ctx, s.DB, `INSERT INTO consistency_ignores (repo_id, pair_key, reason, ignored_by, ignored_at) VALUES (?, ?, ?, ?, ?)
		ON CONFLICT (repo_id, pair_key) DO UPDATE SET reason = excluded.reason, ignored_by = excluded.ignored_by, ignored_at = excluded.ignored_at`,
		f.RepoID, f.PairKey, truncate(strings.TrimSpace(reason), 500), by, store.Millis(time.Now()))
	return err
}

// Unignore brings an ignored pair back.
func (s *Service) Unignore(ctx context.Context, f Finding) error {
	_, err := store.Exec(ctx, s.DB, `DELETE FROM consistency_ignores WHERE repo_id = ? AND pair_key = ?`, f.RepoID, f.PairKey)
	return err
}

// SetFix links a finding to the revision fixing it.
func (s *Service) SetFix(ctx context.Context, f Finding, revID string) error {
	_, err := store.Exec(ctx, s.DB, `UPDATE consistency_findings SET fix_revision_id = ? WHERE id = ?`, revID, f.ID)
	return err
}

// LastScan returns the repo's latest scan (nil when none).
func (s *Service) LastScan(ctx context.Context, repoID string) (*Scan, error) {
	var sc Scan
	var started int64
	var finished sql.NullInt64
	err := store.QueryRow(ctx, s.DB, `SELECT id, status, passages, candidates, judged, found, error, started_at, finished_at FROM consistency_scans WHERE repo_id = ? ORDER BY started_at DESC LIMIT 1`, repoID).
		Scan(&sc.ID, &sc.Status, &sc.Passages, &sc.Candidates, &sc.Judged, &sc.Found, &sc.Error, &started, &finished)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	sc.StartedAt = time.UnixMilli(started).UTC()
	if finished.Valid {
		t := time.UnixMilli(finished.Int64).UTC()
		sc.FinishedAt = &t
	}
	return &sc, nil
}

// Duplicates returns the open duplicate pairs of the repo report, by path
// (the link graph's dotted edges).
func (s *Service) Duplicates(ctx context.Context, repoID string) ([][2]string, error) {
	rows, err := store.Query(ctx, s.DB, `SELECT DISTINCT f.a_path, f.b_path`+findingFrom+` WHERE f.repo_id = ? AND f.scope = ? AND f.kind = ? AND f.status = 'open' AND i.ignored_at IS NULL`, repoID, ScopePublished, Duplicate)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out [][2]string
	for rows.Next() {
		var a, b string
		if err := rows.Scan(&a, &b); err != nil {
			return nil, err
		}
		out = append(out, [2]string{a, b})
	}
	return out, rows.Err()
}
