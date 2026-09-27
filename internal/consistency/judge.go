package consistency

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/kmdn-app/kmdn/internal/llm"
	"github.com/kmdn-app/kmdn/internal/store"
)

// Verdicts.
const (
	Contradiction = "contradiction"
	Duplicate     = "duplicate"
	Related       = "related"
	None          = "none"
)

// Judgment is the model's verdict on a pair.
type Judgment struct {
	Verdict     string `json:"verdict"`
	ClaimA      string `json:"claim_a"`
	ClaimB      string `json:"claim_b"`
	Explanation string `json:"explanation"`
}

var judgeTool = llm.Tool{
	Name:        "judge_pair",
	Description: "Report how passage A relates to passage B.",
	Schema: json.RawMessage(`{"type":"object","properties":{
"verdict":{"type":"string","enum":["contradiction","duplicate","related","none"]},
"claim_a":{"type":"string","description":"For a contradiction: the conflicting claim, quoted exactly from passage A (a phrase or sentence)."},
"claim_b":{"type":"string","description":"For a contradiction: the conflicting claim, quoted exactly from passage B."},
"explanation":{"type":"string","description":"One sentence a reader can act on, e.g. \"One page says 30 working days abroad, the other 20.\""}},
"required":["verdict","explanation"]}`),
}

const judgePrompt = `You check a documentation repository for consistency. You get two passages from different pages.

Decide:
- contradiction: they state incompatible facts about the same thing (numbers, dates, deadlines, limits, rules, names, steps). Quote the conflicting claims exactly as written.
- duplicate: they say substantially the same thing, so one page could link to the other instead.
- related: same topic, compatible.
- none: unrelated.

Be conservative. Different scopes aren't contradictions (a rule for contractors and one for employees; an old version explicitly marked as such). A passage that adds detail to the other isn't a contradiction. When unsure, answer related.`

// judge returns the verdict on a pair, from the cache or the model. called
// reports whether the model was called.
func (s *Service) judge(ctx context.Context, repoID string, a, b Passage) (j Judgment, called bool, err error) {
	key := PairKey(a.Hash, b.Hash)
	err = store.QueryRow(ctx, s.DB, `SELECT verdict, claim_a, claim_b, explanation FROM consistency_judgments WHERE repo_id = ? AND pair_key = ?`, repoID, key).
		Scan(&j.Verdict, &j.ClaimA, &j.ClaimB, &j.Explanation)
	if err == nil {
		return j, false, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return j, false, err
	}
	msg := fmt.Sprintf("Passage A — %s (%s):\n%s\n\nPassage B — %s (%s):\n%s", a.Path, a.Context, a.Text, b.Path, b.Context, b.Text)
	res, err := s.LLM.Complete(ctx, llm.TaskShortText, llm.Run{RepoID: repoID}, llm.ChatRequest{
		System:     []llm.System{{Text: judgePrompt, Cache: true}},
		Messages:   []llm.Message{llm.Text(llm.RoleUser, msg)},
		Tools:      []llm.Tool{judgeTool},
		ToolChoice: judgeTool.Name,
		MaxTokens:  400,
	})
	if err != nil {
		return j, true, err
	}
	for _, c := range res.Content {
		if c.Type == llm.BlockToolUse && c.Name == judgeTool.Name {
			if err := json.Unmarshal(c.Input, &j); err != nil {
				return j, true, fmt.Errorf("consistency: judge_pair input: %w", err)
			}
		}
	}
	switch j.Verdict {
	case Contradiction, Duplicate, Related, None:
	default:
		return j, true, errors.New("consistency: the model didn't judge the pair")
	}
	// A contradiction must quote both sides from the passages, or it can't be shown.
	if j.Verdict == Contradiction && (!quoted(a.Text, j.ClaimA) || !quoted(b.Text, j.ClaimB)) {
		j.ClaimA, j.ClaimB = trimQuote(a.Text, j.ClaimA), trimQuote(b.Text, j.ClaimB)
	}
	j.Explanation = truncate(strings.TrimSpace(j.Explanation), 500)
	_, err = store.Exec(ctx, s.DB, `INSERT INTO consistency_judgments (repo_id, pair_key, verdict, claim_a, claim_b, explanation, model, judged_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?) ON CONFLICT DO NOTHING`,
		repoID, key, j.Verdict, truncate(j.ClaimA, 500), truncate(j.ClaimB, 500), j.Explanation, "", store.Millis(time.Now()))
	return j, true, err
}

// quoted: the claim appears in the passage (whitespace and case folded).
func quoted(text, claim string) bool {
	c := fold(claim)
	return c != "" && strings.Contains(fold(text), c)
}

// trimQuote keeps a claim only when it's really in the passage.
func trimQuote(text, claim string) string {
	if quoted(text, claim) {
		return claim
	}
	return ""
}

func fold(s string) string { return strings.ToLower(strings.Join(strings.Fields(s), " ")) }

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	for n > 0 && s[n]&0xC0 == 0x80 {
		n--
	}
	return s[:n] + "…"
}
