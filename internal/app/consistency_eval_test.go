package app

import (
	"context"
	"hash/fnv"
	"math"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/kmdn-app/kmdn/internal/consistency"
	"github.com/kmdn-app/kmdn/internal/consistency/evalcorpus"
	"github.com/kmdn-app/kmdn/internal/llm"
	"github.com/kmdn-app/kmdn/internal/settings"
	"github.com/kmdn-app/kmdn/internal/store"
	"github.com/kmdn-app/kmdn/internal/users"
)

// bagOfWords is a crude local embedder (hashed word counts) for checking
// the harness's plumbing without a provider.
type bagOfWords struct{}

func (bagOfWords) Embed(_ context.Context, _ string, texts []string) ([][]float32, int, error) {
	out := make([][]float32, len(texts))
	for i, t := range texts {
		v := make([]float32, 512)
		for _, w := range strings.Fields(strings.ToLower(t)) {
			h := fnv.New32a()
			_, _ = h.Write([]byte(strings.Trim(w, ".,:;()")))
			v[h.Sum32()%512]++
		}
		out[i] = v
	}
	return out, len(texts), nil
}

// alwaysRelated judges every pair "related", to check the plumbing.
type alwaysRelated struct{}

func (alwaysRelated) Name() string { return "fake" }

func (alwaysRelated) CountTokens(context.Context, llm.ChatRequest) (int, error) { return 1, nil }

func (alwaysRelated) Stream(context.Context, llm.ChatRequest) (<-chan llm.ChatEvent, error) {
	evs := call("j", "judge_pair", `{"verdict":"related","explanation":"Same topic."}`)
	ch := make(chan llm.ChatEvent, len(evs))
	for _, e := range evs {
		ch <- e
	}
	close(ch)
	return ch, nil
}

// TestConsistencyQuality is spike S8 (docs/specs/14-roadmap.md#spikes):
// on a seeded 300-page corpus with 20 planted contradictions, the repo scan
// should find at least 80% of them with at most one false positive per 50
// pages. It needs real models; by default OpenAI for both judging and
// embeddings, with one key:
//
//	KMDN_EVAL_KEY=…               provider key (also used for embeddings)
//	KMDN_EVAL_PROVIDER=openai     or anthropic
//	KMDN_EVAL_BASE_URL=…          default https://api.openai.com/v1 for openai
//	KMDN_EVAL_MODEL=…             judging model (default gpt-5.6-terra on openai)
//	KMDN_EVAL_EMBED_URL=…         OpenAI-compatible embeddings base URL (default: the OpenAI base URL)
//	KMDN_EVAL_EMBED_KEY=…         default KMDN_EVAL_KEY on openai
//	KMDN_EVAL_EMBED_MODEL=…       default text-embedding-3-small on openai
//	KMDN_EVAL_NEAR=0.78 KMDN_EVAL_DUP=0.92   thresholds to try
//	KMDN_EVAL_MAX_CALLS=…         judgments per scan (default 10000; 20 is a cheap smoke run)
//
//	KMDN_EVAL_KEY=sk-… go test ./internal/app -run TestConsistencyQuality -v -timeout 60m
//
// KMDN_EVAL_FAKE=1 runs the pipeline with a local embedder and a judge that
// finds every pair merely related, to check the harness itself.
func TestConsistencyQuality(t *testing.T) {
	fake := os.Getenv("KMDN_EVAL_FAKE") == "1"
	if !fake && os.Getenv("KMDN_EVAL_KEY") == "" {
		t.Skip("set KMDN_EVAL_KEY (see the comment) to run the consistency quality eval")
	}
	a, admin := newApp(t, nil)
	ctx := context.Background()
	maya, _ := users.Create(ctx, a.DB, "maya@northwind.dev", "Maya", true)
	signIn(t, a, admin, maya)
	corpus := evalcorpus.Generate(300, 20, 10, 42)
	repoID := connectLocal(t, a, admin, corpus.Files)

	st, _ := a.LLM.Settings(ctx)
	st.Consistency.ScanMaxCalls = 10_000
	if n, err := strconv.Atoi(os.Getenv("KMDN_EVAL_MAX_CALLS")); err == nil {
		st.Consistency.ScanMaxCalls = n
	}
	if fake {
		a.LLM.EmbedOverride = bagOfWords{}
		a.LLM.Override = alwaysRelated{}
	} else {
		st.Provider = envOr("KMDN_EVAL_PROVIDER", llm.ProviderOpenAI)
		openai := st.Provider == llm.ProviderOpenAI
		st.BaseURL = os.Getenv("KMDN_EVAL_BASE_URL")
		if openai && st.BaseURL == "" {
			st.BaseURL = "https://api.openai.com/v1"
		}
		ref, err := a.LLM.Secrets.Put(ctx, a.DB, "ai_key", []byte(os.Getenv("KMDN_EVAL_KEY")))
		if err != nil {
			t.Fatal(err)
		}
		st.KeyRef = ref
		if st.Models == nil {
			st.Models = map[string]string{}
		}
		model := os.Getenv("KMDN_EVAL_MODEL")
		if model == "" && openai {
			model = "gpt-5.6-terra"
		}
		if model != "" {
			// The judge uses the short-text model; the provider check uses chat.
			st.Models[llm.TaskShortText], st.Models[llm.TaskChat] = model, model
		}
		st.Embeddings.BaseURL, st.Embeddings.Model = os.Getenv("KMDN_EVAL_EMBED_URL"), os.Getenv("KMDN_EVAL_EMBED_MODEL")
		embedKey := os.Getenv("KMDN_EVAL_EMBED_KEY")
		if openai {
			if st.Embeddings.BaseURL == "" {
				st.Embeddings.BaseURL = st.BaseURL
			}
			if st.Embeddings.Model == "" {
				st.Embeddings.Model = "text-embedding-3-small"
			}
			if embedKey == "" {
				embedKey = os.Getenv("KMDN_EVAL_KEY")
			}
		}
		if st.Embeddings.BaseURL == "" || st.Embeddings.Model == "" {
			t.Fatal("set KMDN_EVAL_EMBED_URL and KMDN_EVAL_EMBED_MODEL: the scan needs an embeddings endpoint")
		}
		if embedKey != "" {
			if st.Embeddings.KeyRef, err = a.LLM.Secrets.Put(ctx, a.DB, "ai_embeddings_key", []byte(embedKey)); err != nil {
				t.Fatal(err)
			}
		}
		// Fail fast with the provider's own error (a model name, a key, a
		// permission) rather than after embedding the whole corpus.
		c := a.LLM.RunCheck(ctx, st, "")
		if !c.OK || strings.Contains(c.Message, "Embeddings failed") {
			t.Fatalf("provider check: %s", c.Message)
		}
		t.Logf("provider check: %s", c.Message)
		st.Check = &c
	}
	if err := settings.Set(ctx, a.DB, "ai", st); err != nil {
		t.Fatal(err)
	}
	if v, err := strconv.ParseFloat(os.Getenv("KMDN_EVAL_NEAR"), 32); err == nil {
		a.Consistency.Near = float32(v)
	}
	if v, err := strconv.ParseFloat(os.Getenv("KMDN_EVAL_DUP"), 32); err == nil {
		a.Consistency.Duplicate = float32(v)
	}

	start := time.Now()
	sc, err := a.Consistency.Scan(ctx, repoID, "")
	if err != nil {
		t.Fatal(err)
	}
	findings, err := a.Consistency.List(ctx, repoID, consistency.ScopePublished, "open")
	if err != nil {
		t.Fatal(err)
	}

	planted := map[string]evalcorpus.Plant{}
	for _, p := range corpus.Contradictions {
		planted[p.Path] = p
	}
	found := map[string]bool{}
	falsePositives := 0
	var fpExamples []string
	for _, f := range findings {
		if f.Kind != consistency.Contradiction {
			continue
		}
		hit := false
		for _, side := range []string{f.A.Path, f.B.Path} {
			if p, ok := planted[side]; ok && samePrefix(f.A.Path, f.B.Path) {
				found[p.Path], hit = true, true
			}
		}
		if !hit {
			falsePositives++
			if len(fpExamples) < 5 {
				fpExamples = append(fpExamples, f.A.Path+" ↔ "+f.B.Path+": "+f.Explanation)
			}
		}
	}
	dupFound := 0
	for _, d := range corpus.Duplicates {
		for _, f := range findings {
			if f.Kind == consistency.Duplicate && ((f.A.Path == d[0] && f.B.Path == d[1]) || (f.A.Path == d[1] && f.B.Path == d[0])) {
				dupFound++
				break
			}
		}
	}
	var tokens int
	_ = store.QueryRow(ctx, a.DB, `SELECT COALESCE(SUM(input_tokens + output_tokens + cache_read_tokens + cache_write_tokens), 0) FROM assistant_runs`).Scan(&tokens)
	recall := float64(len(found)) / float64(len(corpus.Contradictions))
	fpPer50 := float64(falsePositives) / (float64(len(corpus.Files)) / 50)
	t.Logf("scan: %d passages, %d candidate pairs, %d judged, status %s, %s, %d tokens", sc.Passages, sc.Candidates, sc.Judged, sc.Status, time.Since(start).Round(time.Second), tokens)
	t.Logf("contradictions: %d/%d found (recall %.0f%%), %d false positives (%.2f per 50 pages)", len(found), len(corpus.Contradictions), recall*100, falsePositives, fpPer50)
	t.Logf("duplicates: %d/%d found; traps (scoped rules): %d pages", dupFound, len(corpus.Duplicates), len(corpus.Traps))
	if sc.Error != "" {
		t.Logf("scan error: %s", sc.Error)
	}
	for _, ex := range fpExamples {
		t.Logf("  false positive: %s", ex)
	}
	if fake {
		if sc.Passages < 600 || sc.Candidates == 0 || sc.Judged == 0 {
			t.Fatalf("the harness didn't index the corpus: %+v", sc)
		}
		return
	}
	if recall < 0.8 || fpPer50 > 1 || math.IsNaN(recall) {
		t.Errorf("below the S8 bar: recall %.0f%% (want ≥ 80%%), %.2f false positives per 50 pages (want ≤ 1)", recall*100, fpPer50)
	}
}

// samePrefix: both pages are in the same topic folder (the corpus plants
// contradictions within a topic).
func samePrefix(a, b string) bool {
	da, db := a[:strings.LastIndex(a, "/")], b[:strings.LastIndex(b, "/")]
	return da == db
}

func envOr(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}
