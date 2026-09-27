package links

import (
	"testing"

	"github.com/kmdn-app/kmdn/internal/repos"
)

func TestResolve(t *testing.T) {
	r := repos.Repo{}
	r.KmdnYML.Routes = map[string]string{"/docs/": "docs/", "/": "site/"}
	cases := []struct {
		from, dest string
		want       Target
	}{
		{"docs/guides/a.md", "../index.md#start", Target{KindRelative, "docs/index.md", "start"}},
		{"docs/guides/a.md", "b.md", Target{KindRelative, "docs/guides/b.md", ""}},
		{"docs/guides/a.md", "./sub/", Target{KindRelative, "docs/guides/sub/", ""}},
		{"docs/guides/a.md", "my%20page.md", Target{KindRelative, "docs/guides/my page.md", ""}},
		{"docs/guides/a.md", "#usage", Target{KindAnchor, "docs/guides/a.md", "usage"}},
		{"docs/guides/a.md", "/docs/api/intro", Target{KindRoute, "docs/api/intro", ""}},
		{"docs/guides/a.md", "/about", Target{KindRoute, "site/about", ""}},
		{"docs/guides/a.md", "https://example.com/x.md", Target{KindExternal, "", ""}},
		{"docs/guides/a.md", "mailto:a@b.c", Target{KindExternal, "", ""}},
		{"docs/guides/a.md", "b.md?raw=1#x", Target{KindRelative, "docs/guides/b.md", "x"}},
	}
	for _, c := range cases {
		if got := Resolve(r, c.from, c.dest); got != c.want {
			t.Errorf("Resolve(%s, %s) = %+v, want %+v", c.from, c.dest, got, c.want)
		}
	}
}

func TestRehrefKeepsStyle(t *testing.T) {
	r := repos.Repo{}
	r.KmdnYML.Routes = map[string]string{"/docs/": "docs/"}
	cases := []struct {
		from, old, newFile, want string
	}{
		{"docs/index.md", "guides/a.md", "docs/howto/a.md", "howto/a.md"},
		{"docs/index.md", "./guides/a.md#top", "docs/howto/a.md", "./howto/a.md#top"},
		{"docs/index.md", "guides/a", "docs/howto/b.md", "howto/b"},
		{"docs/guides/x.md", "../index.md", "docs/start.md", "../start.md"},
		{"docs/index.md", "guides/", "docs/howto/index.md", "howto/"},
		{"docs/index.md", "/docs/guides/a", "docs/howto/a.md", "/docs/howto/a"},
	}
	for _, c := range cases {
		tg := Resolve(r, c.from, c.old)
		if got := rehref(r, c.from, c.old, tg, c.newFile); got != c.want {
			t.Errorf("rehref(%s, %s → %s) = %s, want %s", c.from, c.old, c.newFile, got, c.want)
		}
	}
}
