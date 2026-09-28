package assistant

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path"
	"strings"

	"github.com/kmdn-app/kmdn/internal/llm"
	"github.com/kmdn-app/kmdn/internal/repos"
	"github.com/kmdn-app/kmdn/internal/revisions"
	"github.com/kmdn-app/kmdn/internal/search"
	"github.com/kmdn-app/kmdn/internal/threads"
)

// MaxToolResult bounds what a tool returns to the model.
const MaxToolResult = 16_000

func schema(s string) json.RawMessage { return json.RawMessage(s) }

// readTools are available in every context (docs/specs/08-assistant.md#tools).
var readTools = []llm.Tool{
	{Name: "search", Description: "Full-text search over the repository's published pages. Returns path, heading and a snippet per hit.",
		Schema: schema(`{"type":"object","properties":{"query":{"type":"string"},"path_prefix":{"type":"string","description":"Only pages under this folder"},"limit":{"type":"integer","minimum":1,"maximum":20}},"required":["query"]}`)},
	{Name: "list_tree", Description: "Files and folders under the content root, or under a folder.",
		Schema: schema(`{"type":"object","properties":{"path":{"type":"string","description":"A folder; empty for the whole content root"}}}`)},
	{Name: "read_file", Description: "A page's markdown with its heading outline. Use from_heading to start at a section of a long page.",
		Schema: schema(`{"type":"object","properties":{"path":{"type":"string"},"from_heading":{"type":"string","description":"A heading's text or slug"},"max_chars":{"type":"integer"}},"required":["path"]}`)},
	{Name: "get_history", Description: "Recent published versions of a page: when, who, and the commit message.",
		Schema: schema(`{"type":"object","properties":{"path":{"type":"string"},"limit":{"type":"integer","minimum":1,"maximum":20}},"required":["path"]}`)},
	{Name: "get_links", Description: "Links from a page to other pages (broken ones flagged) and the pages linking to it.",
		Schema: schema(`{"type":"object","properties":{"path":{"type":"string"}},"required":["path"]}`)},
	{Name: "read_comments", Description: "Open discussions readers left on published pages (one page with path).",
		Schema: schema(`{"type":"object","properties":{"path":{"type":"string"}}}`)},
}

// proposeTool is how a Q&A thread hands a change off to a revision.
var proposeTool = llm.Tool{Name: "propose_revision", Description: "Propose starting a revision to make a change the person asked for. They confirm in the UI; stop after calling it.",
	Schema: schema(`{"type":"object","properties":{"title":{"type":"string","description":"Short, like a commit title"},"description":{"type":"string","description":"What will change and why"},"files":{"type":"array","items":{"type":"string"},"description":"Pages to change or create"}},"required":["title","description","files"]}`)}

// toolLabels describe a call in the UI ("Reading docs/a.md…").
func toolLabel(name string, input json.RawMessage) string {
	var in map[string]any
	_ = json.Unmarshal(input, &in)
	str := func(k string) string { s, _ := in[k].(string); return s }
	switch name {
	case "search":
		return fmt.Sprintf("Searching for “%s”", str("query"))
	case "list_tree":
		if p := str("path"); p != "" {
			return "Listing " + p
		}
		return "Listing pages"
	case "read_file":
		return "Reading " + str("path")
	case "get_history":
		return "Looking at the history of " + str("path")
	case "get_links":
		return "Checking links of " + str("path")
	case "read_comments":
		return "Reading discussions"
	case "propose_revision":
		return "Proposing a revision: " + str("title")
	case "edit_file":
		return "Suggesting changes in " + str("path")
	case "create_file":
		return "Drafting " + str("path")
	case "rename_file":
		return "Proposing to rename " + str("from") + " to " + str("to")
	case "delete_file":
		return "Proposing to delete " + str("path")
	case "reply_to_thread":
		return "Replying in a comment thread"
	case "load_skill":
		if f := str("file"); f != "" {
			return "Reading " + f + " from the skill " + str("name")
		}
		return "Using the skill " + str("name")
	}
	return name
}

// env is what tools can see: the repository, and in a revision's thread
// the revision and the person the assistant acts for.
type env struct {
	s      *Service
	repo   repos.Repo
	rev    *revisions.Revision
	caller revisions.Caller
	guide  *guidance // the repository's AGENTS.md, style guide and skills
}

var errTool = errors.New("tool failed")

func (e env) clean(p string) string {
	return strings.TrimPrefix(path.Clean("/"+strings.TrimSpace(p)), "/")
}

// run executes a tool call and returns its text result.
func (e env) run(ctx context.Context, name string, input json.RawMessage) (string, error) {
	var in struct {
		Query       string `json:"query"`
		PathPrefix  string `json:"path_prefix"`
		Limit       int    `json:"limit"`
		Path        string `json:"path"`
		FromHeading string `json:"from_heading"`
		MaxChars    int    `json:"max_chars"`
		Name        string `json:"name"`
		File        string `json:"file"`
	}
	if err := json.Unmarshal(input, &in); err != nil {
		return "", fmt.Errorf("%w: bad input: %w", errTool, err)
	}
	switch name {
	case "search":
		return e.search(ctx, in.Query, in.PathPrefix, in.Limit)
	case "list_tree":
		if e.rev != nil {
			return e.revisionTree(ctx, in.Path)
		}
		return e.listTree(ctx, in.Path)
	case "read_file":
		return e.readFile(ctx, in.Path, in.FromHeading, in.MaxChars)
	case "edit_file", "create_file", "reply_to_thread", "rename_file", "delete_file":
		if e.rev == nil {
			return "", fmt.Errorf("%w: edits happen in a revision: call propose_revision", errTool)
		}
		switch name {
		case "edit_file":
			return e.editFile(ctx, input)
		case "create_file":
			return e.createFile(ctx, input)
		case "reply_to_thread":
			return e.replyToThread(ctx, input)
		}
		if err := e.canEdit(ctx); err != nil {
			return "", err
		}
		return "Proposed. The person confirms it in the thread; carry on with other changes or stop.", nil
	case "get_history":
		return e.history(ctx, in.Path, in.Limit)
	case "get_links":
		return e.links(ctx, in.Path)
	case "read_comments":
		if e.rev != nil {
			return e.revisionComments(ctx, in.Path)
		}
		return e.comments(ctx, in.Path)
	case "load_skill":
		return e.loadSkill(ctx, in.Name, in.File)
	case "propose_revision":
		return "Proposed. The person sees a card to start the revision; stop here and wait for them.", nil
	}
	return "", fmt.Errorf("%w: no tool named %s", errTool, name)
}

func (e env) search(ctx context.Context, q, prefix string, limit int) (string, error) {
	if limit <= 0 || limit > 20 {
		limit = 8
	}
	hits, err := e.s.Search.Search(ctx, e.repo.ID, search.Published, q, 30)
	if err != nil {
		return "", err
	}
	var b strings.Builder
	n := 0
	for _, h := range hits {
		if prefix != "" && !strings.HasPrefix(h.Path, strings.TrimSuffix(e.clean(prefix), "/")+"/") {
			continue
		}
		snippet := strings.NewReplacer("\x02", "", "\x03", "").Replace(h.Snippet)
		ref := h.Path
		if h.HeadingSlug != "" {
			ref += "#" + h.HeadingSlug
		}
		fmt.Fprintf(&b, "- %s — %s: %s\n", ref, h.Title, snippet)
		if n++; n >= limit {
			break
		}
	}
	if n == 0 {
		return "No pages match.", nil
	}
	return b.String(), nil
}

func (e env) listTree(ctx context.Context, dir string) (string, error) {
	nodes, err := e.s.Repos.Tree(ctx, e.repo)
	if err != nil {
		return "", err
	}
	dir = strings.TrimSuffix(e.clean(dir), "/")
	var b strings.Builder
	n := 0
	for _, nd := range nodes {
		if dir != "" && dir != "." && !strings.HasPrefix(nd.Path, dir+"/") {
			continue
		}
		if nd.Type == "dir" {
			fmt.Fprintf(&b, "%s/\n", nd.Path)
		} else if nd.Markdown {
			fmt.Fprintf(&b, "%s\n", nd.Path)
		}
		if n++; n > 500 {
			b.WriteString("… (more)\n")
			break
		}
	}
	if n == 0 {
		return "Nothing there.", nil
	}
	return b.String(), nil
}

func (e env) readFile(ctx context.Context, p, from string, max int) (string, error) {
	p = e.clean(p)
	if !repos.IsMarkdown(p) {
		return "", fmt.Errorf("%w: %s isn't a page", errTool, p)
	}
	content, inRev, err := e.pageMarkdown(ctx, p)
	if err != nil {
		return "", err
	}
	_, headings, _ := e.s.Engine.Links(ctx, content)
	var b strings.Builder
	fmt.Fprintf(&b, "Path: %s\n", p)
	if inRev {
		b.WriteString("(This revision's version; suggestions still pending aren't included.)\n")
	}
	b.WriteString("Outline:\n")
	for _, h := range headings {
		fmt.Fprintf(&b, "%s- %s (#%s)\n", strings.Repeat("  ", max0(h.Depth-1)), h.Text, h.Slug)
	}
	body := content
	if from != "" {
		want := strings.ToLower(strings.TrimPrefix(from, "#"))
		for _, h := range headings {
			if strings.ToLower(h.Text) == want || h.Slug == want {
				if i := strings.Index(body, h.Text); i >= 0 {
					if j := strings.LastIndex(body[:i], "\n"); j >= 0 {
						i = j + 1
					}
					body = body[i:]
				}
				break
			}
		}
	}
	if max <= 0 || max > MaxToolResult {
		max = MaxToolResult - b.Len()
	}
	if len(body) > max {
		body = body[:max] + "\n… (truncated: read from a later heading to continue)"
	}
	b.WriteString("\n---\n")
	b.WriteString(body)
	return b.String(), nil
}

func max0(n int) int {
	if n < 0 {
		return 0
	}
	return n
}

func (e env) history(ctx context.Context, p string, limit int) (string, error) {
	if limit <= 0 || limit > 20 {
		limit = 5
	}
	log, err := e.s.Repos.Mirror(e.repo).Log(ctx, e.repo.HeadSHA, e.clean(p), limit)
	if err != nil {
		return "", err
	}
	if len(log) == 0 {
		return "No published versions.", nil
	}
	var b strings.Builder
	for _, c := range log {
		people := []string{c.AuthorName}
		for _, co := range c.CoAuthors {
			people = append(people, co.Name)
		}
		fmt.Fprintf(&b, "- %s %s by %s: %s\n", c.Date.Format("2006-01-02"), c.SHA[:min(7, len(c.SHA))], strings.Join(people, ", "), c.Title)
	}
	return b.String(), nil
}

func (e env) links(ctx context.Context, p string) (string, error) {
	pl, err := e.s.Links.ForPublished(ctx, e.repo, e.clean(p))
	if err != nil {
		return "", fmt.Errorf("%w: %w", errTool, err)
	}
	var b strings.Builder
	b.WriteString("Links from this page:\n")
	for _, o := range pl.Outgoing {
		status := ""
		if o.Broken != "" {
			status = " (broken: " + o.Broken + ")"
		}
		target := o.Path
		if target == "" {
			target = o.URL
		}
		fmt.Fprintf(&b, "- %s%s\n", target, status)
	}
	b.WriteString("Pages linking here:\n")
	for _, in := range pl.Incoming {
		fmt.Fprintf(&b, "- %s (line %d)\n", in.From, in.Line)
	}
	return b.String(), nil
}

func (e env) comments(ctx context.Context, p string) (string, error) {
	f := threads.Filter{RepoID: e.repo.ID, Kind: threads.KindDiscussion, State: threads.StateOpen}
	if p != "" {
		f.Path = e.clean(p)
	}
	list, err := threads.List(ctx, e.s.DB, f)
	if err != nil {
		return "", err
	}
	if len(list) == 0 {
		return "No open discussions.", nil
	}
	var b strings.Builder
	for _, t := range list {
		var a threads.Anchor
		_ = json.Unmarshal(t.Anchor, &a)
		fmt.Fprintf(&b, "- %s on “%s”:\n", t.Path, a.Quote)
		for _, c := range t.Comments {
			if !c.Deleted {
				fmt.Fprintf(&b, "  %s: %s\n", nameOr(c.AuthorName), strings.ReplaceAll(c.Body, "\n", " "))
			}
		}
	}
	return b.String(), nil
}

func nameOr(s string) string {
	if s == "" {
		return "someone"
	}
	return s
}
