package docengine

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

var shared *Engine

func engine(t testing.TB) *Engine {
	t.Helper()
	if shared == nil {
		e, err := New(Options{Runtimes: 2})
		if err != nil {
			t.Fatal(err)
		}
		shared = e
	}
	return shared
}

// The Go host must produce byte-identical results to the browser engine.
func TestFixturesRoundTripInGo(t *testing.T) {
	e := engine(t)
	if e.Version() < 1 {
		t.Fatalf("version %d", e.Version())
	}
	files, _ := filepath.Glob("../../testdata/fidelity/*.md")
	if len(files) < 5 {
		t.Fatal("fixtures missing")
	}
	for _, f := range files {
		b, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		r, err := e.Parse(context.Background(), string(b))
		if err != nil {
			t.Fatalf("%s: %v", f, err)
		}
		out, err := e.Serialize(context.Background(), r.Doc, r.SourceMap)
		if err != nil {
			t.Fatalf("%s: %v", f, err)
		}
		if out != string(b) {
			t.Errorf("%s: round trip differs", filepath.Base(f))
		}
	}
}

func TestSerializeEditedDocWithoutSourceMap(t *testing.T) {
	e := engine(t)
	r, err := e.Parse(context.Background(), "# Hi\n\nSome *text*.\n")
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		Type    string            `json:"type"`
		Content []json.RawMessage `json:"content"`
	}
	if err := json.Unmarshal(r.Doc, &doc); err != nil || len(doc.Content) != 2 {
		t.Fatalf("doc: %s %v", r.Doc, err)
	}
	out, err := e.Serialize(context.Background(), r.Doc, nil)
	if err != nil || out != "# Hi\n\nSome *text*.\n" {
		t.Fatalf("%q %v", out, err)
	}
}

func TestParseCache(t *testing.T) {
	e := engine(t)
	md := "cache me " + strings.Repeat("x", 100)
	a, _ := e.Parse(context.Background(), md)
	start := time.Now()
	b, _ := e.Parse(context.Background(), md)
	if time.Since(start) > 5*time.Millisecond || string(a.Doc) != string(b.Doc) {
		t.Fatal("second parse should come from cache")
	}
}

func TestTimeoutDiscardsRuntime(t *testing.T) {
	e, err := New(Options{Runtimes: 1})
	if err != nil {
		t.Fatal(err)
	}
	big := strings.Repeat("Paragraph with *emphasis* and `code` and [link](x).\n\n", 4000)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Millisecond)
	defer cancel()
	if _, err := e.Parse(ctx, big); !errors.Is(err, ErrTimeout) {
		t.Fatalf("expected timeout, got %v", err)
	}
	// The pool recovers with a fresh runtime.
	r, err := e.Parse(context.Background(), "ok\n")
	if err != nil || len(r.Doc) == 0 {
		t.Fatalf("after timeout: %v", err)
	}
}

func TestTooLarge(t *testing.T) {
	e, _ := New(Options{Runtimes: 1, MaxInputBytes: 10})
	if _, err := e.Parse(context.Background(), strings.Repeat("a", 11)); !errors.Is(err, ErrTooLarge) {
		t.Fatal(err)
	}
}

func TestConcurrentUse(t *testing.T) {
	e := engine(t)
	var wg sync.WaitGroup
	errs := make(chan error, 8)
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			md := strings.Repeat("- item\n", i+1)
			r, err := e.Parse(context.Background(), md)
			if err != nil {
				errs <- err
				return
			}
			out, err := e.Serialize(context.Background(), r.Doc, r.SourceMap)
			if err != nil || out != md {
				errs <- errors.New("mismatch: " + out)
			}
		}(i)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Error(err)
	}
}

func BenchmarkParse10KB(b *testing.B) {
	e := engine(b)
	src, _ := os.ReadFile("../../docs/specs/06-git-and-forges.md")
	md := string(src)[:10*1024]
	for i := 0; b.Loop(); i++ {
		if _, err := e.Parse(context.Background(), md+strings.Repeat(" ", i%1000)); err != nil {
			b.Fatal(err)
		}
	}
}

func TestYjsRoundTrip(t *testing.T) {
	e, err := New(Options{Runtimes: 1})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	md := "---\ntitle: First week\n---\n\n# First week\n\n* [x] Laptop\n* [ ] Badge\n\n| a | b |\n|---|:-:|\n| 1 | 2 |\n\nSee [the handbook](../index.md).\n"
	u, sm, err := e.YFromMarkdown(ctx, md, 7)
	if err != nil {
		t.Fatal(err)
	}
	if got, err := e.YMaterialize(ctx, u, sm); err != nil || got != md {
		t.Fatalf("materialize: %v\n%q", err, got)
	}
	if ids, err := e.YClients(ctx, u); err != nil || len(ids) != 1 || ids[0] != 7 {
		t.Fatalf("clients: %v %v", ids, err)
	}
	sv, err := e.YStateVector(ctx, u)
	if err != nil {
		t.Fatal(err)
	}
	missing, err := e.YDiff(ctx, u, sv)
	if err != nil || len(missing) > 4 {
		t.Fatalf("diff against own state should be empty: %v %d bytes", err, len(missing))
	}
	merged, err := e.YMerge(ctx, [][]byte{u, missing})
	if err != nil {
		t.Fatal(err)
	}
	if got, _ := e.YMaterialize(ctx, merged, sm); got != md {
		t.Fatalf("after merge: %q", got)
	}
	// Two replicas that each created the page concurrently: both copies survive
	// the merge (why the server, not clients, creates documents).
	u2, _, _ := e.YFromMarkdown(ctx, "# Other\n", 8)
	both, err := e.YMerge(ctx, [][]byte{u, u2})
	if err != nil {
		t.Fatal(err)
	}
	got, _ := e.YMaterialize(ctx, both, "")
	if !strings.Contains(got, "# Other") || !strings.Contains(got, "# First week") {
		t.Fatalf("merge of two replicas: %q", got)
	}
	// An edit made on the server applies on top and materializes.
	edit, err := e.YApplyMarkdown(ctx, u, strings.Replace(md, "Badge", "Badge and keys", 1), 9)
	if err != nil {
		t.Fatal(err)
	}
	if ids, _ := e.YClients(ctx, edit); len(ids) != 1 || ids[0] != 9 {
		t.Fatalf("edit clients: %v", ids)
	}
	edited, _ := e.YMerge(ctx, [][]byte{u, edit})
	if got, _ := e.YMaterialize(ctx, edited, sm); got != strings.Replace(md, "Badge", "Badge and keys", 1) {
		t.Fatalf("after edit: %q", got)
	}
	if noop, err := e.YApplyMarkdown(ctx, edited, strings.Replace(md, "Badge", "Badge and keys", 1), 9); err != nil || len(noop) > 2 {
		t.Fatalf("no-op edit: %v %d bytes", err, len(noop))
	}
	if _, err := e.YClients(ctx, []byte{0xff, 0xff, 0xff}); !errors.Is(err, ErrBadUpdate) {
		t.Fatalf("garbage update: %v", err)
	}
}

func BenchmarkYMaterialize10KB(b *testing.B) {
	e, _ := New(Options{Runtimes: 1})
	ctx := context.Background()
	var sb strings.Builder
	for i := 0; sb.Len() < 10<<10; i++ {
		fmt.Fprintf(&sb, "## Section %d\n\nSome *text* with a [link](x.md) and `code`.\n\n- item\n- item\n\n", i)
	}
	u, sm, err := e.YFromMarkdown(ctx, sb.String(), 1)
	if err != nil {
		b.Fatal(err)
	}
	b.ResetTimer()
	for range b.N {
		if _, err := e.YMaterialize(ctx, u, sm); err != nil {
			b.Fatal(err)
		}
	}
}
