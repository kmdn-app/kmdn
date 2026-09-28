package app

import (
	"context"
	"strings"
	"testing"

	"github.com/kmdn-app/kmdn/internal/llm"
	"github.com/kmdn-app/kmdn/internal/users"
)

func hasTool(req llm.ChatRequest, name string) bool {
	for _, t := range req.Tools {
		if t.Name == name {
			return true
		}
	}
	return false
}

// The assistant follows the repository's AGENTS.md files and style guide,
// lists its .skills/ and loads one when the task matches.
func TestAssistantRepositoryGuidance(t *testing.T) {
	a, admin := newApp(t, nil)
	ctx := context.Background()
	maya, _ := users.Create(ctx, a.DB, "maya@northwind.dev", "Maya", true)
	signIn(t, a, admin, maya)
	repoID, remote := connectLocalRemote(t, a, admin, map[string]string{
		"docs/policy.md":                      "# Equipment policy\n\nLaptops every three years.\n",
		"AGENTS.md":                           "Say team, not staff.\n",
		"docs/AGENTS.md":                      "Every page starts with a one-line summary.\n",
		"STYLE.md":                            "Use sentence case in headings.\n",
		".skills/release-notes/SKILL.md":      "---\nname: release-notes\ndescription: How we write release notes for the handbook.\n---\n# Release notes\n\nStart from template.md.\n",
		".skills/release-notes/template.md":   "## What changed\n",
		".skills/glossary.md":                 "---\ndescription: Terms we use.\n---\nTeam: the people who work here.\n",
		"docs/.skills/release-notes/SKILL.md": "---\nname: release-notes\ndescription: The docs' own release notes.\n---\nUse the docs template.\n",
	})
	model := &scripted{}
	a.LLM.Override = model
	model.push(
		func(llm.ChatRequest) []llm.ChatEvent { return call("s1", "load_skill", `{"name":"glossary"}`) },
		func(llm.ChatRequest) []llm.ChatEvent { return call("s2", "load_skill", `{"name":"nope"}`) },
		func(llm.ChatRequest) []llm.ChatEvent {
			return call("s3", "load_skill", `{"name":"release-notes","file":"../../AGENTS.md"}`)
		},
		func(llm.ChatRequest) []llm.ChatEvent { return say("Done.") },
	)
	code, created := admin.do("POST", "/repos/"+repoID+"/assistant/threads", map[string]any{"text": "What does team mean?"})
	if code != 201 {
		t.Fatalf("create: %d %v", code, created)
	}
	a.Assistant.Wait()

	first := model.reqs[0]
	sys := first.System[0]
	if !sys.Cache {
		t.Fatal("the guidance belongs in the cached system prompt")
	}
	for _, want := range []string{
		"<instructions path=\"AGENTS.md\">\nSay team, not staff.\n</instructions>",
		"<instructions path=\"docs/AGENTS.md\">\nEvery page starts with a one-line summary.\n</instructions>",
		"<style_guide path=\"STYLE.md\">\nUse sentence case in headings.\n</style_guide>",
		"- glossary: Terms we use.\n",
		"- release-notes: The docs' own release notes.\n", // the content root's skill wins
	} {
		if !strings.Contains(sys.Text, want) {
			t.Fatalf("system prompt lacks %q:\n%s", want, sys.Text)
		}
	}
	if !hasTool(first, "load_skill") {
		t.Fatal("no load_skill tool")
	}
	results := func(i int) llm.Block {
		msgs := model.reqs[i].Messages
		return msgs[len(msgs)-1].Content[0]
	}
	if r := results(1); r.IsError || !strings.Contains(r.Content, "Skill: glossary") || !strings.Contains(r.Content, "Team: the people who work here.") || strings.Contains(r.Content, "description:") {
		t.Fatalf("load_skill: %+v", r)
	}
	if r := results(2); !r.IsError || !strings.Contains(r.Content, "glossary, release-notes") {
		t.Fatalf("unknown skill: %+v", r)
	}
	if r := results(3); !r.IsError || strings.Contains(r.Content, "Say team") {
		t.Fatalf("a path out of the skill's folder: %+v", r)
	}

	// A new AGENTS.md is used once the repo syncs; the root skill's other
	// files can be read.
	writeFile(t, remote, "AGENTS.md", "Say colleagues.\n")
	gitIn(t, remote, "rm", "--quiet", "-r", "docs/.skills")
	gitIn(t, remote, "commit", "--quiet", "-am", "New instructions")
	admin.do("POST", "/repos/"+repoID+"/refresh", nil)
	runJobs(t, a)
	model.push(
		func(llm.ChatRequest) []llm.ChatEvent {
			return call("s4", "load_skill", `{"name":"release-notes","file":"template.md"}`)
		},
		func(llm.ChatRequest) []llm.ChatEvent { return say("Here.") },
	)
	threadID := created["thread"].(map[string]any)["id"].(string)
	if code, _ := admin.do("POST", "/assistant/threads/"+threadID+"/messages", map[string]any{"text": "Draft release notes"}); code != 202 {
		t.Fatalf("post: %d", code)
	}
	a.Assistant.Wait()
	next := model.reqs[4]
	if !strings.Contains(next.System[0].Text, "Say colleagues.") || strings.Contains(next.System[0].Text, "Say team") || !strings.Contains(next.System[0].Text, "- release-notes: How we write release notes for the handbook.") {
		t.Fatalf("after the sync:\n%s", next.System[0].Text)
	}
	if r := results(5); r.IsError || !strings.Contains(r.Content, "## What changed") {
		t.Fatalf("skill file: %+v", r)
	}
}
