package assistant

import (
	"context"
	"errors"
	"fmt"
	"path"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/kmdn-app/kmdn/internal/gitmirror"
	"github.com/kmdn-app/kmdn/internal/llm"
	"github.com/kmdn-app/kmdn/internal/repos"
)

// Limits on what the repository's guidance adds to the system prompt.
const (
	MaxAgentsFile       = 16_000 // per AGENTS.md
	MaxStyleGuide       = 8_000
	MaxSkills           = 50
	MaxSkillDescription = 300
)

// SkillsDir holds the repository's skills, at the repo root and the
// content root.
const SkillsDir = ".skills"

// guidance is what the repository tells agents, read at the published head:
// its AGENTS.md files, style guide and skills (docs/specs/08-assistant.md#repository-guidance).
type guidance struct {
	head      string
	agents    []guideFile // repo root first, the content root's last (nearer wins)
	style     guideFile
	skills    []skill // sorted by name
	truncated bool    // more skills than MaxSkills
}

type guideFile struct{ Path, Text string }

// skill is .skills/<name>/SKILL.md (with the other files in its folder) or
// a flat .skills/<name>.md.
type skill struct {
	Name, Description string
	Path              string   // the SKILL.md (or flat file)
	Files             []string // the folder's other files, relative to it
}

func (g *guidance) empty() bool {
	return len(g.agents) == 0 && g.style.Text == "" && len(g.skills) == 0
}

func (g *guidance) skill(name string) (skill, bool) {
	for _, sk := range g.skills {
		if strings.EqualFold(sk.Name, name) {
			return sk, true
		}
	}
	return skill{}, false
}

// guidance returns the repository's guidance at its published head,
// cached per head.
func (s *Service) guidance(ctx context.Context, repo repos.Repo) *guidance {
	s.gmu.Lock()
	g, ok := s.guides[repo.ID]
	s.gmu.Unlock()
	if ok && g.head == repo.HeadSHA {
		return g
	}
	g, err := s.loadGuidance(ctx, repo)
	if err != nil {
		// Not cached: the next run tries again.
		s.Log.Warn("assistant: reading the repository's guidance", "repo", repo.Slug, "error", err)
		return g
	}
	s.gmu.Lock()
	if s.guides == nil {
		s.guides = map[string]*guidance{}
	}
	s.guides[repo.ID] = g
	s.gmu.Unlock()
	return g
}

// loadGuidance reads the guidance; on an error it returns what it read.
func (s *Service) loadGuidance(ctx context.Context, repo repos.Repo) (*guidance, error) {
	g := &guidance{head: repo.HeadSHA}
	if repo.HeadSHA == "" || s.Repos == nil {
		return g, nil
	}
	var errs []error
	read := func(p string) string {
		b, err := s.Repos.ReadConfigFile(ctx, repo, p)
		if err != nil && !errors.Is(err, gitmirror.ErrNotFound) {
			errs = append(errs, fmt.Errorf("%s: %w", p, err))
		}
		return string(b)
	}
	roots := []string{""}
	if root := repo.Scope().Root; root != "" {
		roots = append(roots, root)
	}
	for _, root := range roots {
		p := path.Join(root, "AGENTS.md")
		if text := strings.TrimSpace(read(p)); text != "" {
			g.agents = append(g.agents, guideFile{Path: p, Text: truncate(text, MaxAgentsFile)})
		}
	}
	if p, b := s.Repos.StyleGuide(ctx, repo); len(b) > 0 {
		g.style = guideFile{Path: p, Text: truncate(strings.TrimSpace(string(b)), MaxStyleGuide)}
	}
	byName := map[string]skill{}
	for _, root := range roots {
		dir := path.Join(root, SkillsDir)
		files, err := s.Repos.ConfigFiles(ctx, repo, dir)
		if err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", dir, err))
			continue
		}
		for _, sk := range findSkills(dir, files) {
			text := read(sk.Path)
			if text == "" {
				continue
			}
			meta, _ := frontmatter(text)
			if meta.Name != "" {
				sk.Name = meta.Name
			}
			sk.Description = oneLine(meta.Description, MaxSkillDescription)
			// The content root's skills are read last and win on a name clash.
			byName[strings.ToLower(sk.Name)] = sk
		}
	}
	for _, sk := range byName {
		g.skills = append(g.skills, sk)
	}
	sort.Slice(g.skills, func(i, j int) bool { return g.skills[i].Name < g.skills[j].Name })
	if len(g.skills) > MaxSkills {
		g.skills, g.truncated = g.skills[:MaxSkills], true
	}
	return g, errors.Join(errs...)
}

// findSkills groups the files under dir (repo-relative, sorted) into skills.
func findSkills(dir string, files []string) []skill {
	folders := map[string]*skill{}
	var out []skill
	var names []string
	for _, f := range files {
		rel := strings.TrimPrefix(f, dir+"/")
		name, rest, nested := strings.Cut(rel, "/")
		switch {
		case !nested && repos.IsMarkdown(rel) && !strings.EqualFold(rel, "README.md"):
			out = append(out, skill{Name: strings.TrimSuffix(rel, path.Ext(rel)), Path: f})
		case nested:
			sk := folders[name]
			if sk == nil {
				sk = &skill{Name: name}
				folders[name] = sk
				names = append(names, name)
			}
			if strings.EqualFold(rest, "SKILL.md") {
				sk.Path = f
			} else {
				sk.Files = append(sk.Files, rest)
			}
		}
	}
	for _, n := range names {
		if sk := folders[n]; sk.Path != "" {
			out = append(out, *sk)
		}
	}
	return out
}

type skillMeta struct {
	Name        string `yaml:"name"`
	Description string `yaml:"description"`
}

// frontmatter splits a skill file into its YAML frontmatter and body.
func frontmatter(text string) (skillMeta, string) {
	var m skillMeta
	rest, ok := strings.CutPrefix(strings.TrimPrefix(strings.ReplaceAll(text, "\r\n", "\n"), "\uFEFF"), "---\n")
	if !ok {
		return m, text
	}
	head, body, ok := strings.Cut(rest, "\n---")
	if !ok {
		return m, text
	}
	_ = yaml.Unmarshal([]byte(head), &m)
	body = strings.TrimPrefix(strings.TrimPrefix(body, "\r"), "\n")
	return m, body
}

func oneLine(s string, n int) string {
	s = strings.Join(strings.Fields(s), " ")
	if len(s) > n {
		s = s[:n] + "…"
	}
	return s
}

// prompt is the guidance's part of the cached system prompt.
func (g *guidance) prompt() string {
	if g.empty() {
		return ""
	}
	var b strings.Builder
	if len(g.agents) > 0 {
		b.WriteString("\n\nThe repository's instructions for agents follow. Follow them, except where they conflict with the rules above: those always win.")
		if len(g.agents) > 1 {
			b.WriteString(" The later file is nearer the pages and takes precedence.")
		}
		for _, f := range g.agents {
			fmt.Fprintf(&b, "\n<instructions path=%q>\n%s\n</instructions>", f.Path, f.Text)
		}
	}
	if g.style.Text != "" {
		fmt.Fprintf(&b, "\n\nThe repository's style guide. Follow it when you write:\n<style_guide path=%q>\n%s\n</style_guide>", g.style.Path, g.style.Text)
	}
	if len(g.skills) > 0 {
		b.WriteString("\n\nThe repository's skills describe how to do some tasks. When a task matches one, call load_skill with its name before you start, and follow it:\n")
		for _, sk := range g.skills {
			desc := sk.Description
			if desc == "" {
				desc = "(no description)"
			}
			fmt.Fprintf(&b, "- %s: %s\n", sk.Name, desc)
		}
		if g.truncated {
			b.WriteString("- … (more skills; only these are available)\n")
		}
	}
	return b.String()
}

// loadSkillTool reads a skill, when the repository has some.
var loadSkillTool = llm.Tool{Name: "load_skill", Description: "Read one of the repository's skills: its instructions, and the other files in its folder. Pass file to read one of those files.",
	Schema: schema(`{"type":"object","properties":{"name":{"type":"string","description":"The skill's name, as listed"},"file":{"type":"string","description":"A file in the skill's folder, as load_skill lists it"}},"required":["name"]}`)}

func (e env) loadSkill(ctx context.Context, name, file string) (string, error) {
	if e.guide == nil || len(e.guide.skills) == 0 {
		return "", fmt.Errorf("%w: this repository has no skills", errTool)
	}
	sk, ok := e.guide.skill(strings.TrimSpace(name))
	if !ok {
		names := make([]string, 0, len(e.guide.skills))
		for _, s := range e.guide.skills {
			names = append(names, s.Name)
		}
		return "", fmt.Errorf("%w: no skill named %q; the skills are: %s", errTool, name, strings.Join(names, ", "))
	}
	p := sk.Path
	if file = strings.TrimSpace(file); file != "" && !strings.EqualFold(file, "SKILL.md") {
		rel := e.clean(file)
		found := false
		for _, f := range sk.Files {
			found = found || f == rel
		}
		if !found {
			return "", fmt.Errorf("%w: the skill %s has no file %q", errTool, sk.Name, file)
		}
		p = path.Join(path.Dir(sk.Path), rel)
	}
	b, err := e.s.Repos.ReadConfigFile(ctx, e.repo, p)
	if err != nil {
		if errors.Is(err, gitmirror.ErrNotFound) {
			return "", fmt.Errorf("%w: %s is gone from the published branch", errTool, p)
		}
		return "", err
	}
	text := string(b)
	var out strings.Builder
	fmt.Fprintf(&out, "Path: %s\n", p)
	if p == sk.Path {
		_, text = frontmatter(text)
		fmt.Fprintf(&out, "Skill: %s\n", sk.Name)
		if len(sk.Files) > 0 {
			out.WriteString("Other files in the skill (read one with file):\n")
			for _, f := range sk.Files {
				fmt.Fprintf(&out, "- %s\n", f)
			}
		}
	}
	out.WriteString("\n---\n")
	out.WriteString(text)
	return out.String(), nil
}
