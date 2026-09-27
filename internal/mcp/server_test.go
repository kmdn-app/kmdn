package mcp

import (
	"log/slog"
	"testing"
	"time"

	"github.com/kmdn-app/kmdn/internal/docengine"
)

func TestServerBuilds(t *testing.T) {
	s := &Service{Log: slog.Default()}
	s.init()
	if s.server(call{}) == nil {
		t.Fatal("no server")
	}
}

func TestLimiter(t *testing.T) {
	now := time.Unix(0, 0)
	l := newLimiter(2, 1)
	l.now = func() time.Time { return now }
	r1, _, ok := l.acquire("k")
	if !ok {
		t.Fatal("first call refused")
	}
	if _, _, ok := l.acquire("k"); ok {
		t.Fatal("second concurrent call allowed")
	}
	r1()
	r1() // releasing twice is harmless
	r2, _, ok := l.acquire("k")
	if !ok {
		t.Fatal("second call refused")
	}
	r2()
	if _, retry, ok := l.acquire("k"); ok || retry < 29*time.Second {
		t.Fatalf("third call in the minute: ok=%v retry=%v", ok, retry)
	}
	now = now.Add(31 * time.Second)
	if _, _, ok := l.acquire("k"); !ok {
		t.Fatal("not refilled")
	}
	if _, _, ok := l.acquire("other"); !ok {
		t.Fatal("keys share a bucket")
	}
}

func TestTree(t *testing.T) {
	out := tree([]docengine.Heading{{Depth: 1, Text: "A", Slug: "a"}, {Depth: 2, Text: "B", Slug: "b"}, {Depth: 3, Text: "C", Slug: "c"}, {Depth: 2, Text: "D", Slug: "d"}, {Depth: 1, Text: "E", Slug: "e"}})
	if len(out) != 2 || len(out[0].Children) != 2 || len(out[0].Children[0].(headingOut).Children) != 1 || out[1].Slug != "e" {
		t.Fatalf("tree: %+v", out)
	}
	body := "# A\n\nx\n\n## B\n\ny\n\n### C\n\nz\n\n## D\n\nw\n"
	hs := []docengine.Heading{{Depth: 1, Text: "A", Slug: "a", Line: 1}, {Depth: 2, Text: "B", Slug: "b", Line: 5}, {Depth: 3, Text: "C", Slug: "c", Line: 9}, {Depth: 2, Text: "D", Slug: "d", Line: 13}}
	if sec, ok := section(body, hs, "b"); !ok || sec != "## B\n\ny\n\n### C\n\nz\n" {
		t.Fatalf("section: %q", sec)
	}
	if fm, rest := splitFrontmatter("---\ntitle: T\ntags: [a]\n---\n# T\n"); fm["title"] != "T" || rest != "# T\n" {
		t.Fatalf("front matter: %v %q", fm, rest)
	}
}
