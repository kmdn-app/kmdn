package assistant

import (
	"strings"
	"testing"
)

func TestFindSkills(t *testing.T) {
	got := findSkills(".skills", []string{
		".skills/README.md",
		".skills/glossary.md",
		".skills/notes.txt",
		".skills/release-notes/SKILL.md",
		".skills/release-notes/examples/2026-09.md",
		".skills/release-notes/template.md",
		".skills/half-done/draft.md", // no SKILL.md: not a skill
	})
	var names []string
	for _, sk := range got {
		names = append(names, sk.Name+"="+sk.Path+"["+strings.Join(sk.Files, ",")+"]")
	}
	want := "glossary=.skills/glossary.md[], release-notes=.skills/release-notes/SKILL.md[examples/2026-09.md,template.md]"
	if strings.Join(names, ", ") != want {
		t.Fatalf("skills: %v", names)
	}
}

func TestFrontmatter(t *testing.T) {
	m, body := frontmatter("---\r\nname: release-notes\r\ndescription: >\r\n  How we write\r\n  release notes.\r\n---\r\n# Release notes\r\n")
	if m.Name != "release-notes" || oneLine(m.Description, 300) != "How we write release notes." || !strings.HasPrefix(body, "# Release notes") {
		t.Fatalf("meta %+v body %q", m, body)
	}
	if m, body := frontmatter("# No frontmatter\n"); m.Name != "" || body != "# No frontmatter\n" {
		t.Fatalf("plain: %+v %q", m, body)
	}
	if got := oneLine(strings.Repeat("a", 10), 4); got != "aaaa…" {
		t.Fatalf("oneLine: %q", got)
	}
}

func TestGuidancePrompt(t *testing.T) {
	if (&guidance{}).prompt() != "" {
		t.Fatal("no guidance must not change the prompt")
	}
	g := &guidance{
		agents: []guideFile{{"AGENTS.md", "Say team, not staff."}, {"docs/AGENTS.md", "Pages start with a summary."}},
		skills: []skill{{Name: "release-notes", Description: "How we write release notes."}, {Name: "glossary"}},
	}
	p := g.prompt()
	for _, want := range []string{
		`<instructions path="AGENTS.md">` + "\nSay team, not staff.\n</instructions>",
		`<instructions path="docs/AGENTS.md">`,
		"takes precedence",
		"the rules above: those always win",
		"- release-notes: How we write release notes.\n",
		"- glossary: (no description)\n",
	} {
		if !strings.Contains(p, want) {
			t.Fatalf("prompt lacks %q:\n%s", want, p)
		}
	}
	if strings.Index(p, `"AGENTS.md"`) > strings.Index(p, `"docs/AGENTS.md"`) {
		t.Fatal("the nearer AGENTS.md must come last")
	}
}
