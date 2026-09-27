package docengine

import (
	"context"
	"encoding/json"
	"errors"
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
