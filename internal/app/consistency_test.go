package app

import (
	"context"
	"hash/fnv"
	"strings"
	"testing"

	"github.com/kmdn-app/kmdn/internal/access"
	"github.com/kmdn-app/kmdn/internal/llm"
	"github.com/kmdn-app/kmdn/internal/repos"
	"github.com/kmdn-app/kmdn/internal/revisions"
	"github.com/kmdn-app/kmdn/internal/store"
	"github.com/kmdn-app/kmdn/internal/users"
)

// topicEmbedder is a deterministic stand-in for an embeddings model: one
// dimension per topic word, plus a per-text dimension picked by hash, so two
// different passages on one topic are ~0.85 similar and identical text is 1.
type topicEmbedder struct{}

var embedTopics = []string{"abroad", "laptop", "expense", "holiday"}

func (topicEmbedder) Embed(_ context.Context, _ string, texts []string) ([][]float32, int, error) {
	out := make([][]float32, len(texts))
	for i, t := range texts {
		body := t
		if _, after, ok := strings.Cut(t, "\n\n"); ok {
			body = after // the page title and heading trail don't count here
		}
		v := make([]float32, len(embedTopics)+64)
		for k, w := range embedTopics {
			if strings.Contains(strings.ToLower(body), w) {
				v[k] = 1
			}
		}
		h := fnv.New32a()
		_, _ = h.Write([]byte(body))
		v[len(embedTopics)+int(h.Sum32()%64)] = 0.42
		out[i] = v
	}
	return out, 10 * len(texts), nil
}

func judged(verdict, a, b, why string) func(llm.ChatRequest) []llm.ChatEvent {
	return func(llm.ChatRequest) []llm.ChatEvent {
		return call("j", "judge_pair", toJSON(map[string]string{"verdict": verdict, "claim_a": a, "claim_b": b, "explanation": why}))
	}
}

func judgeCalls(m *scripted) int {
	m.mu.Lock()
	defer m.mu.Unlock()
	n := 0
	for _, r := range m.reqs {
		if r.ToolChoice == "judge_pair" {
			n++
		}
	}
	return n
}

func findingsOf(v map[string]any) []map[string]any {
	var out []map[string]any
	for _, f := range v["findings"].([]any) {
		out = append(out, f.(map[string]any))
	}
	return out
}

func TestConsistencyChecks(t *testing.T) {
	a, admin := newApp(t, nil)
	ctx := context.Background()
	maya, _ := users.Create(ctx, a.DB, "maya@northwind.dev", "Maya", true)
	signIn(t, a, admin, maya)
	repoID, remote := connectLocalRemote(t, a, admin, map[string]string{
		"docs/index.md":     "# Handbook\n\nWelcome to the handbook. Read the pages on the left to learn how we work together here.\n",
		"docs/travel.md":    "# Travel\n\n## Working abroad\n\nEmployees can work abroad for up to 30 working days per year. Ask your manager first and tell HR before you go.\n",
		"docs/remote.md":    "# Remote work\n\n## From another country\n\nYou may work from abroad for up to 20 working days each year, as long as your manager agrees and HR knows.\n",
		"docs/equipment.md": "# Equipment\n\n## Laptops\n\nEveryone gets a new laptop every three years. Ask IT for a replacement if yours breaks before then.\n",
		"docs/faq.md":       "# FAQ\n\n## When do I get a new computer?\n\nEveryone gets a new laptop every three years. Ask IT for a replacement if yours breaks before then.\n",
	})
	mk := func(email, name string, role access.Role) (users.User, *tc) {
		u, _ := users.Create(ctx, a.DB, email, name, false)
		_ = access.Grant(ctx, a.DB, repoID, "user", u.ID, role)
		c := &tc{t: t, base: admin.base, c: newClient()}
		signIn(t, a, c, u)
		return u, c
	}
	sam, samC := mk("sam@northwind.dev", "Sam", access.Contributor)
	_, tomC := mk("tom@northwind.dev", "Tom", access.Maintainer)
	a.Collab.Options.QuietPeriod = 0
	model := &scripted{}
	a.LLM.Override = model
	a.LLM.EmbedOverride = topicEmbedder{}

	// The repo scan: maintainers run it; near-identical text is a duplicate
	// outright, the rest goes to the model.
	if code, _ := samC.do("POST", "/repos/"+repoID+"/consistency/scan", nil); code != 403 {
		t.Fatalf("contributor scans: %d", code)
	}
	model.push(judged("contradiction", "up to 20 working days", "up to 30 working days", "One page allows 20 working days abroad, the other 30."))
	if code, _ := tomC.do("POST", "/repos/"+repoID+"/consistency/scan", nil); code != 202 {
		t.Fatalf("scan: %d", code)
	}
	runJobs(t, a)
	_, rep := samC.do("GET", "/repos/"+repoID+"/consistency", nil)
	fs := findingsOf(rep)
	if len(fs) != 2 || fs[0]["kind"] != "contradiction" || fs[1]["kind"] != "duplicate" {
		t.Fatalf("findings: %v", rep)
	}
	con, dup := fs[0], fs[1]
	if con["a"].(map[string]any)["path"] != "docs/remote.md" || con["claim_b"] != "up to 30 working days" || con["status"] != "open" {
		t.Fatalf("contradiction: %v", con)
	}
	if dup["b"].(map[string]any)["path"] != "docs/faq.md" || dup["a"].(map[string]any)["heading"] != "Laptops" {
		t.Fatalf("duplicate: %v", dup)
	}
	if sc := rep["scan"].(map[string]any); sc["status"] != "done" || sc["passages"] != float64(5) || rep["can_run"] != false {
		t.Fatalf("scan: %v can_run %v", sc, rep["can_run"])
	}
	if n := judgeCalls(model); n != 1 {
		t.Fatalf("judge calls: %d", n)
	}
	var embedRuns int
	_ = store.QueryRow(ctx, a.DB, `SELECT COUNT(*) FROM assistant_runs WHERE task = 'embeddings' AND input_tokens > 0`).Scan(&embedRuns)
	if embedRuns == 0 {
		t.Fatal("embeddings not metered")
	}
	if _, g := samC.do("GET", "/repos/"+repoID+"/graph", nil); len(g["duplicates"].([]any)) != 1 {
		t.Fatalf("graph duplicates: %v", g["duplicates"])
	}

	// Ignoring needs a reason and holds across scans; verdicts are cached.
	if code, _ := samC.do("POST", "/consistency/findings/"+dup["id"].(string)+"/ignore", map[string]any{"reason": " "}); code != 422 && code != 400 {
		t.Fatalf("ignore without reason: %d", code)
	}
	if code, _ := samC.do("POST", "/consistency/findings/"+dup["id"].(string)+"/ignore", map[string]any{"reason": "The FAQ repeats it on purpose."}); code != 204 {
		t.Fatalf("ignore: %d", code)
	}
	tomC.do("POST", "/repos/"+repoID+"/consistency/scan", nil)
	runJobs(t, a)
	_, rep = samC.do("GET", "/repos/"+repoID+"/consistency", nil)
	fs = findingsOf(rep)
	if len(fs) != 2 || fs[0]["status"] != "open" || fs[1]["status"] != "ignored" || fs[1]["ignore_reason"] != "The FAQ repeats it on purpose." || judgeCalls(model) != 1 {
		t.Fatalf("after rescan: %v (judge calls %d)", fs, judgeCalls(model))
	}
	if _, g := samC.do("GET", "/repos/"+repoID+"/graph", nil); len(g["duplicates"].([]any)) != 0 {
		t.Fatalf("ignored duplicate drawn: %v", g["duplicates"])
	}

	// A revision's changed passages are checked against the rest of the repo.
	_, rev := samC.do("POST", "/repos/"+repoID+"/revisions", map[string]any{"title": "Laptops every four years", "path": "docs/equipment.md"})
	revID := rev["id"].(string)
	rv, _ := revisions.Get(ctx, a.DB, revID)
	repo, _ := repos.Get(ctx, a.DB, repoID)
	if err := a.Collab.Apply(ctx, repo, rv, revisions.Caller{User: sam, Role: access.Contributor}, "docs/equipment.md",
		"# Equipment\n\n## Laptops\n\nEveryone gets a new laptop every four years. Ask IT for a replacement if yours breaks before then.\n", "human"); err != nil {
		t.Fatal(err)
	}
	a.Collab.FlushRevision(ctx, revID)
	if _, rc := samC.do("GET", "/revisions/"+revID+"/consistency", nil); rc["pending"] != true {
		t.Fatalf("no check queued after the edit: %v", rc)
	}
	model.push(func(req llm.ChatRequest) []llm.ChatEvent {
		msg := req.Messages[0].Content[0].Text
		if !strings.Contains(msg, "Passage A — docs/equipment.md") || !strings.Contains(msg, "every four years") || !strings.Contains(msg, "docs/faq.md") {
			t.Errorf("judge request: %s", msg)
		}
		return judged("contradiction", "every four years", "every three years", "The equipment page now says four years; the FAQ still says three.")(req)
	})
	if err := a.Consistency.CheckRevision(ctx, revID); err != nil {
		t.Fatal(err)
	}
	_, rc := samC.do("GET", "/revisions/"+revID+"/consistency", nil)
	rf := findingsOf(rc)
	if len(rf) != 1 || rf[0]["kind"] != "contradiction" || rf[0]["claim_a"] != "every four years" || rf[0]["a"].(map[string]any)["path"] != "docs/equipment.md" {
		t.Fatalf("revision findings: %v", rc)
	}

	// Fix briefs the revision's assistant; "Link instead" is for duplicates.
	if code, _ := samC.do("POST", "/consistency/findings/"+rf[0]["id"].(string)+"/fix", map[string]any{"action": "link"}); code != 422 && code != 400 {
		t.Fatalf("link a contradiction: %d", code)
	}
	model.push(func(llm.ChatRequest) []llm.ChatEvent { return say("I'll align the FAQ.") })
	if code, _ := samC.do("POST", "/consistency/findings/"+rf[0]["id"].(string)+"/fix", map[string]any{"action": "fix"}); code != 202 {
		t.Fatalf("fix: %d", code)
	}
	a.Assistant.Wait()
	_, th := samC.do("GET", "/revisions/"+revID+"/assistant", nil)
	if !strings.Contains(toJSON(th["messages"]), "Make these two passages agree") {
		t.Fatalf("assistant not briefed: %v", th["messages"])
	}

	// "Start a revision to fix" from the report: both pages, briefed, linked.
	model.push(func(llm.ChatRequest) []llm.ChatEvent { return say("Looking into which is right.") })
	code, fixed := samC.do("POST", "/consistency/findings/"+con["id"].(string)+"/fix", map[string]any{"action": "fix"})
	if code != 201 {
		t.Fatalf("start fix revision: %d %v", code, fixed)
	}
	a.Assistant.Wait()
	fixRev := fixed["revision"].(map[string]any)
	if _, fr := samC.do("GET", "/revisions/"+fixRev["id"].(string), nil); fr["file_count"] != float64(2) || fr["title"] != "Align remote and travel" {
		t.Fatalf("fix revision files: %v", fr)
	}
	_, rep = samC.do("GET", "/repos/"+repoID+"/consistency", nil)
	if f := findingsOf(rep)[0]; f["fix"] == nil || f["fix"].(map[string]any)["number"] != fixRev["number"] {
		t.Fatalf("fix not linked: %v", f)
	}
	if code, _ := samC.do("POST", "/consistency/findings/"+con["id"].(string)+"/fix", map[string]any{"action": "fix"}); code != 409 {
		t.Fatalf("second fix: %d", code)
	}

	// Once the pages agree, the next scan closes the finding.
	writeFile(t, remote, "docs/remote.md", "# Remote work\n\n## From another country\n\nYou may work from abroad for up to 30 working days each year, as long as your manager agrees and HR knows.\n")
	gitIn(t, remote, "commit", "--quiet", "-am", "30 days")
	if err := a.Repos.Sync(ctx, repoID); err != nil {
		t.Fatal(err)
	}
	runJobs(t, a)
	model.push(judged("related", "", "", "Both allow 30 working days abroad."))
	tomC.do("POST", "/repos/"+repoID+"/consistency/scan", nil)
	runJobs(t, a)
	_, rep = samC.do("GET", "/repos/"+repoID+"/consistency", nil)
	_, closed := samC.do("GET", "/repos/"+repoID+"/consistency?status=closed", nil)
	if cf := findingsOf(closed); len(cf) != 1 || cf[0]["id"] != con["id"] {
		t.Fatalf("closed: %v (all: %v)", closed, rep["findings"])
	}
}

// failing is a provider whose every call fails, like a wrong model name.
type failing struct{}

func (failing) Name() string { return "fake" }

func (failing) CountTokens(context.Context, llm.ChatRequest) (int, error) { return 1, nil }

func (failing) Stream(context.Context, llm.ChatRequest) (<-chan llm.ChatEvent, error) {
	return nil, &llm.APIError{Status: 404, Message: "The model `gpt-nope` does not exist"}
}

// A provider failing every judgment fails the scan with its error, instead
// of reporting a capped scan with nothing judged.
func TestConsistencyScanJudgeFailures(t *testing.T) {
	a, admin := newApp(t, nil)
	ctx := context.Background()
	maya, _ := users.Create(ctx, a.DB, "maya@northwind.dev", "Maya", true)
	signIn(t, a, admin, maya)
	repoID := connectLocal(t, a, admin, map[string]string{
		"docs/travel.md": "# Travel\n\n## Working abroad\n\nEmployees can work abroad for up to 30 working days per year. Ask your manager first and tell HR before you go.\n",
		"docs/remote.md": "# Remote work\n\n## From another country\n\nYou may work from abroad for up to 20 working days each year, as long as your manager agrees and HR knows.\n",
	})
	a.LLM.Override = failing{}
	a.LLM.EmbedOverride = topicEmbedder{}
	if _, err := a.Consistency.Scan(ctx, repoID, ""); err == nil || !strings.Contains(err.Error(), "every judgment failed (1 pairs)") || !strings.Contains(err.Error(), "gpt-nope") {
		t.Fatalf("scan error: %v", err)
	}
	sc, err := a.Consistency.LastScan(ctx, repoID)
	if err != nil || sc == nil || sc.Status != "error" || !strings.Contains(sc.Error, "gpt-nope") {
		t.Fatalf("last scan: %+v %v", sc, err)
	}
}
