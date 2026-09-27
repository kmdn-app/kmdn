package mcp

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"path"
	"strconv"
	"strings"
	"time"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
	"gopkg.in/yaml.v3"

	"github.com/kmdn-app/kmdn/internal/audit"
	"github.com/kmdn-app/kmdn/internal/docengine"
	"github.com/kmdn-app/kmdn/internal/gitmirror"
	"github.com/kmdn-app/kmdn/internal/links"
	"github.com/kmdn-app/kmdn/internal/repos"
	"github.com/kmdn-app/kmdn/internal/search"
	"github.com/kmdn-app/kmdn/internal/store"
	"github.com/kmdn-app/kmdn/internal/telemetry"
	"github.com/kmdn-app/kmdn/internal/version"
)

// Service serves /mcp.
type Service struct {
	DB      *store.DB
	Repos   *repos.Service
	Search  *search.Index
	Links   *links.Service
	Engine  *docengine.Engine
	BaseURL string
	Log     *slog.Logger
	// Limits per key (zero: defaults).
	PerMinute, Concurrent int

	lim    *limiter
	schema *sdk.SchemaCache
}

const instructions = `Read-only access to the published documentation in kmdn. Start with search (or list_repos and list_tree), then read_doc for the pages you need. Every page has a url: cite it when you use what a page says.`

var (
	closedWorld = false
	readOnly    = &sdk.ToolAnnotations{ReadOnlyHint: true, OpenWorldHint: &closedWorld, IdempotentHint: true}
)

// call is one request's key and client, for tools and the audit log.
type call struct {
	key Key
	ip  string
}

// server builds the MCP server for a key (cheap: schemas are cached).
func (s *Service) server(c call) *sdk.Server {
	srv := sdk.NewServer(&sdk.Implementation{Name: "kmdn", Title: "kmdn", Version: version.Get().Version}, &sdk.ServerOptions{
		Instructions: instructions,
		Logger:       s.Log,
		SchemaCache:  s.schema,
		// Stateless: lists never change within a session.
		Capabilities: &sdk.ServerCapabilities{Tools: &sdk.ToolCapabilities{}, Resources: &sdk.ResourceCapabilities{}, Prompts: &sdk.PromptCapabilities{}},
		PageSize:     500,
	})
	t := tools{s: s, c: c}
	sdk.AddTool(srv, &sdk.Tool{Name: "list_repos", Title: "List repositories", Description: "Repositories this key can read: id, name (owner/name), forge and content root.", Annotations: readOnly}, t.listRepos)
	sdk.AddTool(srv, &sdk.Tool{Name: "search", Title: "Search docs", Description: "Full-text search over published pages. Returns hits with the matching heading, a snippet and the page's url.", Annotations: readOnly}, t.search)
	sdk.AddTool(srv, &sdk.Tool{Name: "list_tree", Title: "List files", Description: "Files and folders of a repository's published content (markdown pages and assets).", Annotations: readOnly}, t.listTree)
	sdk.AddTool(srv, &sdk.Tool{Name: "read_doc", Title: "Read a page", Description: "A published page: its content (markdown or plain text, optionally one section), front matter, outline, last commit and url.", Annotations: readOnly}, t.readDoc)
	sdk.AddTool(srv, &sdk.Tool{Name: "get_outline", Title: "Page outline", Description: "A page's heading tree with slugs (use a slug as read_doc's heading).", Annotations: readOnly}, t.outline)
	sdk.AddTool(srv, &sdk.Tool{Name: "get_history", Title: "Page history", Description: "Published versions of a page: commit sha, date, title and authors (display names).", Annotations: readOnly}, t.history)
	sdk.AddTool(srv, &sdk.Tool{Name: "read_doc_at", Title: "Read a past version", Description: "A page as it was at a published commit from get_history.", Annotations: readOnly}, t.readAt)
	sdk.AddTool(srv, &sdk.Tool{Name: "get_links", Title: "Page links", Description: "A page's outbound internal links (with broken ones flagged) and the pages linking to it.", Annotations: readOnly}, t.links)
	srv.AddResourceTemplate(&sdk.ResourceTemplate{Name: "doc", Title: "Published page", URITemplate: "kmdn://{owner}/{repo}/{+path}", MIMEType: "text/markdown",
		Description: "A published page's markdown, e.g. kmdn://acme/handbook/docs/travel.md"}, t.readResource)
	srv.AddPrompt(&sdk.Prompt{Name: "answer_from_docs", Title: "Answer from the docs", Description: "Answer a question from kmdn's published docs, with cited urls.",
		Arguments: []*sdk.PromptArgument{{Name: "question", Description: "What to answer", Required: true}, {Name: "repo", Description: "Only this repository (owner/name)"}}}, answerPrompt)
	srv.AddReceivingMiddleware(t.middleware)
	return srv
}

type tools struct {
	s *Service
	c call
}

// repos lists the key's repos with a published head.
func (t tools) repos(ctx context.Context) ([]repos.Repo, error) {
	all, err := repos.List(ctx, t.s.DB, nil, true)
	if err != nil {
		return nil, err
	}
	var out []repos.Repo
	for _, r := range all {
		if t.c.key.Sees(r) && r.HeadSHA != "" {
			out = append(out, r)
		}
	}
	return out, nil
}

var errNoRepo = errors.New("no such repository for this key: use list_repos")

// repo resolves an id or owner/name the key can see.
func (t tools) repo(ctx context.Context, ref string) (repos.Repo, error) {
	ref = strings.TrimSpace(ref)
	var r repos.Repo
	var err error
	if owner, name, ok := strings.Cut(ref, "/"); ok {
		r, err = repos.BySlug(ctx, t.s.DB, t.c.key.OrgID, owner, name)
	} else {
		r, err = repos.Get(ctx, t.s.DB, ref)
	}
	if err != nil || !t.c.key.Sees(r) || r.HeadSHA == "" {
		return repos.Repo{}, errNoRepo
	}
	return r, nil
}

// pageURL is where a person reads the page in kmdn.
func (t tools) pageURL(r repos.Repo, p, slug string) string {
	u := strings.TrimRight(t.s.BaseURL, "/") + "/" + url.PathEscape(r.Owner) + "/" + url.PathEscape(r.Name) + "/" + (&url.URL{Path: p}).EscapedPath()
	if slug != "" {
		u += "#" + slug
	}
	return u
}

func cleanPath(p string) string { return strings.TrimPrefix(path.Clean("/"+strings.TrimSpace(p)), "/") }

// readErr turns storage errors into messages an agent can act on.
func readErr(p string, err error) error {
	switch {
	case errors.Is(err, gitmirror.ErrNotFound):
		return fmt.Errorf("%s isn't published in this repository: use search or list_tree", p)
	case errors.Is(err, repos.ErrOutOfScope):
		return fmt.Errorf("%s is outside the repository's published content", p)
	}
	return err
}

type repoOut struct {
	ID          string `json:"id"`
	Name        string `json:"name" jsonschema:"owner/name, usable as the repo argument"`
	DisplayName string `json:"display_name"`
	Forge       string `json:"forge"`
	ContentRoot string `json:"content_root"`
	URL         string `json:"url"`
}

type listReposIn struct{}

type listReposOut struct {
	Repos []repoOut `json:"repos"`
}

func (t tools) listRepos(ctx context.Context, _ *sdk.CallToolRequest, _ listReposIn) (*sdk.CallToolResult, listReposOut, error) {
	list, err := t.repos(ctx)
	if err != nil {
		return nil, listReposOut{}, err
	}
	out := listReposOut{Repos: []repoOut{}}
	for _, r := range list {
		out.Repos = append(out.Repos, repoOut{ID: r.ID, Name: r.Slug, DisplayName: r.DisplayName, Forge: r.ForgeKind, ContentRoot: r.ContentRoot,
			URL: strings.TrimRight(t.s.BaseURL, "/") + "/" + url.PathEscape(r.Owner) + "/" + url.PathEscape(r.Name)})
	}
	return nil, out, nil
}

type searchIn struct {
	Query      string `json:"query" jsonschema:"words to look for"`
	Repo       string `json:"repo,omitempty" jsonschema:"only this repository (owner/name or id)"`
	PathPrefix string `json:"path_prefix,omitempty" jsonschema:"only pages under this folder"`
	Limit      int    `json:"limit,omitempty" jsonschema:"at most this many hits (default 10, up to 50)"`
}

type hitOut struct {
	Repo    string `json:"repo"`
	Path    string `json:"path"`
	Title   string `json:"title"`
	Heading string `json:"heading,omitempty"`
	Snippet string `json:"snippet"`
	URL     string `json:"url"`
}

type searchOut struct {
	Hits []hitOut `json:"hits"`
}

func (t tools) search(ctx context.Context, _ *sdk.CallToolRequest, in searchIn) (*sdk.CallToolResult, searchOut, error) {
	limit := in.Limit
	if limit <= 0 {
		limit = 10
	}
	limit = min(limit, 50)
	var list []repos.Repo
	if in.Repo != "" {
		r, err := t.repo(ctx, in.Repo)
		if err != nil {
			return nil, searchOut{}, err
		}
		list = []repos.Repo{r}
	} else {
		var err error
		if list, err = t.repos(ctx); err != nil {
			return nil, searchOut{}, err
		}
	}
	prefix := strings.TrimSuffix(cleanPath(in.PathPrefix), "/")
	out := searchOut{Hits: []hitOut{}}
	marks := strings.NewReplacer("\x02", "", "\x03", "")
	for _, r := range list {
		fetch := limit
		if prefix != "" {
			fetch = min(limit*4, 200)
		}
		hits, err := t.s.Search.Search(ctx, r.ID, search.Published, in.Query, fetch)
		if err != nil {
			return nil, out, err
		}
		for _, h := range hits {
			if prefix != "" && h.Path != prefix && !strings.HasPrefix(h.Path, prefix+"/") {
				continue
			}
			out.Hits = append(out.Hits, hitOut{Repo: r.Slug, Path: h.Path, Title: h.Title, Heading: h.Heading, Snippet: marks.Replace(h.Snippet), URL: t.pageURL(r, h.Path, h.HeadingSlug)})
			if len(out.Hits) >= limit {
				return nil, out, nil
			}
		}
	}
	return nil, out, nil
}

type treeIn struct {
	Repo  string `json:"repo" jsonschema:"owner/name or id"`
	Path  string `json:"path,omitempty" jsonschema:"a folder (default: the content root)"`
	Depth int    `json:"depth,omitempty" jsonschema:"levels below path (default: all)"`
}

type nodeOut struct {
	Path     string `json:"path"`
	Type     string `json:"type" jsonschema:"file or dir"`
	Markdown bool   `json:"markdown,omitempty"`
	Size     int64  `json:"size,omitempty"`
}

type treeOut struct {
	Items     []nodeOut `json:"items"`
	Truncated bool      `json:"truncated,omitempty"`
}

const maxTree = 2000

func (t tools) listTree(ctx context.Context, _ *sdk.CallToolRequest, in treeIn) (*sdk.CallToolResult, treeOut, error) {
	r, err := t.repo(ctx, in.Repo)
	if err != nil {
		return nil, treeOut{}, err
	}
	nodes, err := t.s.Repos.Tree(ctx, r)
	if err != nil {
		return nil, treeOut{}, err
	}
	base := strings.TrimSuffix(cleanPath(in.Path), "/")
	out := treeOut{Items: []nodeOut{}}
	for _, n := range nodes {
		rel := n.Path
		if base != "" {
			if !strings.HasPrefix(n.Path, base+"/") {
				continue
			}
			rel = strings.TrimPrefix(n.Path, base+"/")
		}
		if in.Depth > 0 && strings.Count(rel, "/") >= in.Depth {
			continue
		}
		if len(out.Items) >= maxTree {
			out.Truncated = true
			break
		}
		out.Items = append(out.Items, nodeOut{Path: n.Path, Type: n.Type, Markdown: n.Markdown, Size: n.Size})
	}
	return nil, out, nil
}

type readIn struct {
	Repo     string `json:"repo" jsonschema:"owner/name or id"`
	Path     string `json:"path" jsonschema:"the page's path, as in list_tree or search"`
	Heading  string `json:"heading,omitempty" jsonschema:"only this section: a heading slug or its text"`
	Format   string `json:"format,omitempty" jsonschema:"markdown (default) or text"`
	MaxChars int    `json:"max_chars,omitempty" jsonschema:"cut the content after this many characters (default 60000)"`
}

// headingOut is a heading and the ones under it. Children holds more
// headingOut values (typed loosely: schemas can't be recursive).
type headingOut struct {
	Depth    int    `json:"depth"`
	Text     string `json:"text"`
	Slug     string `json:"slug"`
	Children []any  `json:"children,omitempty" jsonschema:"headings under this one, same shape"`
}

type docOut struct {
	Repo        string         `json:"repo"`
	Path        string         `json:"path"`
	Content     string         `json:"content"`
	Truncated   bool           `json:"truncated,omitempty"`
	Frontmatter map[string]any `json:"frontmatter,omitempty"`
	Outline     []headingOut   `json:"outline"`
	CommitSHA   string         `json:"commit_sha"`
	UpdatedAt   *time.Time     `json:"updated_at,omitempty"`
	URL         string         `json:"url"`
}

// splitFrontmatter separates YAML front matter from the body.
func splitFrontmatter(md string) (map[string]any, string) {
	if !strings.HasPrefix(md, "---\n") && !strings.HasPrefix(md, "---\r\n") {
		return nil, md
	}
	rest := md[strings.Index(md, "\n")+1:]
	end := strings.Index(rest, "\n---")
	if end < 0 {
		return nil, md
	}
	var fm map[string]any
	if err := yaml.Unmarshal([]byte(rest[:end]), &fm); err != nil {
		fm = nil
	}
	body := rest[end+4:]
	if i := strings.Index(body, "\n"); i >= 0 {
		body = body[i+1:]
	} else {
		body = ""
	}
	return fm, body
}

// tree nests flat headings by depth.
func tree(hs []docengine.Heading) []headingOut {
	type node struct {
		h    docengine.Heading
		kids []*node
	}
	root := &node{}
	stack := []*node{root}
	for _, h := range hs {
		for len(stack) > 1 && stack[len(stack)-1].h.Depth >= h.Depth {
			stack = stack[:len(stack)-1]
		}
		n := &node{h: h}
		parent := stack[len(stack)-1]
		parent.kids = append(parent.kids, n)
		stack = append(stack, n)
	}
	var conv func(ns []*node) []headingOut
	conv = func(ns []*node) []headingOut {
		out := []headingOut{}
		for _, n := range ns {
			h := headingOut{Depth: n.h.Depth, Text: n.h.Text, Slug: n.h.Slug}
			for _, k := range conv(n.kids) {
				h.Children = append(h.Children, k)
			}
			out = append(out, h)
		}
		return out
	}
	return conv(root.kids)
}

// section cuts the body to one heading's section (by slug or text).
func section(body string, hs []docengine.Heading, want string) (string, bool) {
	w := strings.ToLower(strings.TrimPrefix(strings.TrimSpace(want), "#"))
	lines := strings.Split(body, "\n")
	for i, h := range hs {
		if h.Slug != w && strings.ToLower(h.Text) != w {
			continue
		}
		end := len(lines)
		for _, n := range hs[i+1:] {
			if n.Depth <= h.Depth {
				end = n.Line - 1
				break
			}
		}
		start := max(h.Line-1, 0)
		if start > len(lines) || end > len(lines) || start > end {
			return "", false
		}
		return strings.TrimRight(strings.Join(lines[start:end], "\n"), "\n") + "\n", true
	}
	return "", false
}

func (t tools) read(ctx context.Context, r repos.Repo, p, sha, heading, format string, maxChars int) (docOut, error) {
	p = cleanPath(p)
	f, err := t.s.Repos.ReadFile(ctx, r, p, sha)
	if err != nil {
		return docOut{}, readErr(p, err)
	}
	fm, body := splitFrontmatter(f.Content)
	out := docOut{Repo: r.Slug, Path: p, Frontmatter: fm, Outline: []headingOut{}, CommitSHA: f.CommitSHA, URL: t.pageURL(r, p, "")}
	var hs []docengine.Heading
	if f.Markdown {
		if _, hs, err = t.s.Engine.Links(ctx, body); err != nil {
			hs = nil
		}
		out.Outline = tree(hs)
	}
	content := body
	if heading != "" {
		sec, ok := section(body, hs, heading)
		if !ok {
			return docOut{}, fmt.Errorf("%s has no heading %q: see the outline", p, heading)
		}
		content = sec
		for _, h := range hs {
			if h.Slug == strings.ToLower(strings.TrimPrefix(heading, "#")) || strings.EqualFold(h.Text, heading) {
				out.URL = t.pageURL(r, p, h.Slug)
				break
			}
		}
	}
	if format == "text" && f.Markdown {
		if txt, err := t.s.Engine.PlainText(ctx, content); err == nil {
			content = txt
		}
	}
	if maxChars <= 0 {
		maxChars = 60_000
	}
	if len(content) > maxChars {
		cut := maxChars
		for cut > 0 && content[cut]&0xC0 == 0x80 {
			cut--
		}
		content, out.Truncated = content[:cut], true
	}
	out.Content = content
	if sha == "" {
		if hist, err := t.s.Repos.History(ctx, r, p, 1); err == nil && len(hist) > 0 {
			out.CommitSHA = hist[0].SHA
			d := hist[0].Date.UTC()
			out.UpdatedAt = &d
		}
	}
	return out, nil
}

func (t tools) readDoc(ctx context.Context, _ *sdk.CallToolRequest, in readIn) (*sdk.CallToolResult, docOut, error) {
	r, err := t.repo(ctx, in.Repo)
	if err != nil {
		return nil, docOut{}, err
	}
	out, err := t.read(ctx, r, in.Path, "", in.Heading, in.Format, in.MaxChars)
	return nil, out, err
}

type pageIn struct {
	Repo string `json:"repo" jsonschema:"owner/name or id"`
	Path string `json:"path" jsonschema:"the page's path"`
}

type outlineOut struct {
	Outline []headingOut `json:"outline"`
}

func (t tools) outline(ctx context.Context, _ *sdk.CallToolRequest, in pageIn) (*sdk.CallToolResult, outlineOut, error) {
	r, err := t.repo(ctx, in.Repo)
	if err != nil {
		return nil, outlineOut{}, err
	}
	p := cleanPath(in.Path)
	f, err := t.s.Repos.ReadFile(ctx, r, p, "")
	if err != nil {
		return nil, outlineOut{}, readErr(p, err)
	}
	_, body := splitFrontmatter(f.Content)
	_, hs, err := t.s.Engine.Links(ctx, body)
	if err != nil {
		return nil, outlineOut{}, err
	}
	return nil, outlineOut{Outline: tree(hs)}, nil
}

type historyIn struct {
	Repo  string `json:"repo" jsonschema:"owner/name or id"`
	Path  string `json:"path" jsonschema:"the page's path"`
	Limit int    `json:"limit,omitempty" jsonschema:"at most this many versions (default 20, up to 100)"`
}

type versionOut struct {
	SHA        string    `json:"sha"`
	Date       time.Time `json:"date"`
	Title      string    `json:"title"`
	Authors    []string  `json:"authors"`
	ReviewedBy []string  `json:"reviewed_by,omitempty"`
	Path       string    `json:"path,omitempty" jsonschema:"the page's path at that version, when it was renamed since"`
}

type historyOut struct {
	Versions []versionOut `json:"versions"`
}

// authors are display names only (no emails).
func authors(c gitmirror.Commit) []string {
	seen := map[string]bool{}
	var out []string
	add := func(n string) {
		if n = strings.TrimSpace(n); n != "" && !seen[n] {
			seen[n] = true
			out = append(out, n)
		}
	}
	add(c.AuthorName)
	for _, p := range c.CoAuthors {
		add(p.Name)
	}
	return out
}

func (t tools) history(ctx context.Context, _ *sdk.CallToolRequest, in historyIn) (*sdk.CallToolResult, historyOut, error) {
	r, err := t.repo(ctx, in.Repo)
	if err != nil {
		return nil, historyOut{}, err
	}
	n := in.Limit
	if n <= 0 {
		n = 20
	}
	p := cleanPath(in.Path)
	list, err := t.s.Repos.History(ctx, r, p, min(n, 100))
	if err != nil {
		return nil, historyOut{}, readErr(p, err)
	}
	out := historyOut{Versions: []versionOut{}}
	for _, c := range list {
		v := versionOut{SHA: c.SHA, Date: c.Date.UTC(), Title: c.Title, Authors: authors(c)}
		for _, rv := range c.ReviewedBy {
			v.ReviewedBy = append(v.ReviewedBy, rv.Name)
		}
		if c.Path != "" && c.Path != p {
			v.Path = c.Path
		}
		out.Versions = append(out.Versions, v)
	}
	return nil, out, nil
}

type readAtIn struct {
	Repo string `json:"repo" jsonschema:"owner/name or id"`
	Path string `json:"path" jsonschema:"the page's current path"`
	SHA  string `json:"sha" jsonschema:"a commit sha from get_history (7 characters or more)"`
}

func (t tools) readAt(ctx context.Context, _ *sdk.CallToolRequest, in readAtIn) (*sdk.CallToolResult, docOut, error) {
	r, err := t.repo(ctx, in.Repo)
	if err != nil {
		return nil, docOut{}, err
	}
	p := cleanPath(in.Path)
	sha := strings.ToLower(strings.TrimSpace(in.SHA))
	if len(sha) < 7 {
		return nil, docOut{}, errors.New("give at least 7 characters of the sha")
	}
	// Only published versions of this page: the sha must be in its history.
	list, err := t.s.Repos.History(ctx, r, p, 1000)
	if err != nil {
		return nil, docOut{}, readErr(p, err)
	}
	for _, c := range list {
		if strings.HasPrefix(c.SHA, sha) {
			at := p
			if c.Path != "" {
				at = c.Path
			}
			out, err := t.read(ctx, r, at, c.SHA, "", "", 0)
			if err == nil {
				d := c.Date.UTC()
				out.UpdatedAt, out.URL = &d, t.pageURL(r, p, "")
			}
			return nil, out, err
		}
	}
	return nil, docOut{}, fmt.Errorf("%s isn't a published version of %s: see get_history", in.SHA, p)
}

func (t tools) links(ctx context.Context, _ *sdk.CallToolRequest, in pageIn) (*sdk.CallToolResult, links.PageLinks, error) {
	r, err := t.repo(ctx, in.Repo)
	if err != nil {
		return nil, links.PageLinks{}, err
	}
	p := cleanPath(in.Path)
	pl, err := t.s.Links.ForPublished(ctx, r, p)
	if err != nil {
		return nil, pl, readErr(p, err)
	}
	if pl.Outgoing == nil {
		pl.Outgoing = []links.Outbound{}
	}
	if pl.Incoming == nil {
		pl.Incoming = []links.Inbound{}
	}
	return nil, pl, nil
}

// parseURI splits kmdn://owner/repo/path.
func parseURI(uri string) (owner, name, p string, ok bool) {
	rest, ok := strings.CutPrefix(uri, "kmdn://")
	if !ok {
		return "", "", "", false
	}
	parts := strings.SplitN(rest, "/", 3)
	if len(parts) != 3 || parts[2] == "" {
		return "", "", "", false
	}
	if u, err := url.PathUnescape(parts[2]); err == nil {
		parts[2] = u
	}
	return parts[0], parts[1], parts[2], true
}

func (t tools) readResource(ctx context.Context, req *sdk.ReadResourceRequest) (*sdk.ReadResourceResult, error) {
	owner, name, p, ok := parseURI(req.Params.URI)
	if !ok {
		return nil, sdk.ResourceNotFoundError(req.Params.URI)
	}
	r, err := t.repo(ctx, owner+"/"+name)
	if err != nil {
		return nil, sdk.ResourceNotFoundError(req.Params.URI)
	}
	f, err := t.s.Repos.ReadFile(ctx, r, cleanPath(p), "")
	if err != nil || !f.Markdown {
		return nil, sdk.ResourceNotFoundError(req.Params.URI)
	}
	return &sdk.ReadResourceResult{Contents: []*sdk.ResourceContents{{URI: req.Params.URI, MIMEType: "text/markdown", Text: f.Content}}}, nil
}

// resourcePage is how many docs resources/list returns at a time.
const resourcePage = 500

// listResources pages through the markdown pages of the key's repos; the
// cursor is an opaque offset.
func (t tools) listResources(ctx context.Context, cursor string) (*sdk.ListResourcesResult, error) {
	offset := 0
	if cursor != "" {
		b, err := base64.RawURLEncoding.DecodeString(cursor)
		if err != nil {
			return nil, errors.New("invalid cursor")
		}
		if offset, err = strconv.Atoi(string(b)); err != nil || offset < 0 {
			return nil, errors.New("invalid cursor")
		}
	}
	list, err := t.repos(ctx)
	if err != nil {
		return nil, err
	}
	out := &sdk.ListResourcesResult{Resources: []*sdk.Resource{}}
	i := 0
	for _, r := range list {
		nodes, err := t.s.Repos.Tree(ctx, r)
		if err != nil {
			return nil, err
		}
		for _, n := range nodes {
			if n.Type != "file" || !n.Markdown {
				continue
			}
			if i >= offset {
				if len(out.Resources) == resourcePage {
					out.NextCursor = base64.RawURLEncoding.EncodeToString([]byte(strconv.Itoa(i)))
					return out, nil
				}
				out.Resources = append(out.Resources, &sdk.Resource{
					URI: "kmdn://" + r.Owner + "/" + r.Name + "/" + n.Path, Name: n.Path, Title: r.Slug + ": " + n.Path,
					MIMEType: "text/markdown", Size: n.Size,
				})
			}
			i++
		}
	}
	return out, nil
}

func answerPrompt(_ context.Context, req *sdk.GetPromptRequest) (*sdk.GetPromptResult, error) {
	q := strings.TrimSpace(req.Params.Arguments["question"])
	if q == "" {
		return nil, errors.New("question is required")
	}
	where := "kmdn's published documentation"
	if r := strings.TrimSpace(req.Params.Arguments["repo"]); r != "" {
		where = fmt.Sprintf("the %s repository in kmdn", r)
	}
	text := fmt.Sprintf(`Answer this question from %s:

%s

Use the search tool first, then read_doc on the most relevant pages (a heading's section is enough when the page is long). Base the answer only on what the pages say, cite each fact with the page's url, and say so when the docs don't answer it.`, where, q)
	return &sdk.GetPromptResult{Description: "Answer from the docs", Messages: []*sdk.PromptMessage{{Role: "user", Content: &sdk.TextContent{Text: text}}}}, nil
}

// middleware serves resources/list and meters and audits calls.
func (t tools) middleware(next sdk.MethodHandler) sdk.MethodHandler {
	return func(ctx context.Context, method string, req sdk.Request) (sdk.Result, error) {
		switch method {
		case "resources/list":
			var cursor string
			if r, ok := req.(*sdk.ListResourcesRequest); ok && r.Params != nil {
				cursor = r.Params.Cursor
			}
			return t.listResources(ctx, cursor)
		case "tools/call", "resources/read":
		default:
			return next(ctx, method, req)
		}
		res, err := next(ctx, method, req)
		e := audit.Entry{ActorType: audit.ActorAgentKey, ActorID: t.c.key.ID, IP: t.c.ip, Data: map[string]any{"key_name": t.c.key.Name}}
		if r, ok := req.(*sdk.CallToolRequest); ok && r.Params != nil {
			e.Action = "mcp." + r.Params.Name
			var args map[string]any
			_ = json.Unmarshal(r.Params.Arguments, &args)
			for _, k := range []string{"repo", "path", "query", "sha", "heading"} {
				if v, ok := args[k].(string); ok && v != "" {
					e.Data[k] = truncate(v, 300)
				}
			}
			if rp, ok := args["repo"].(string); ok {
				if repo, err := t.repo(ctx, rp); err == nil {
					e.RepoID = repo.ID
				}
			}
		}
		if r, ok := req.(*sdk.ReadResourceRequest); ok && r.Params != nil {
			e.Action = "mcp.read_resource"
			e.Data["uri"] = truncate(r.Params.URI, 500)
		}
		if err != nil {
			e.Data["error"] = truncate(err.Error(), 300)
		} else if tr, ok := res.(*sdk.CallToolResult); ok && tr.IsError {
			e.Data["tool_error"] = true
		}
		wctx := context.WithoutCancel(ctx)
		if aerr := audit.Write(wctx, t.s.DB, e); aerr != nil {
			t.s.Log.Error("audit mcp call", "err", aerr)
		}
		telemetry.MCPCalls.WithLabelValues(strings.TrimPrefix(e.Action, "mcp."), t.c.key.ID).Inc()
		if cerr := count(wctx, t.s.DB, t.c.key.ID); cerr != nil {
			t.s.Log.Error("count mcp call", "err", cerr)
		}
		return res, err
	}
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	for n > 0 && s[n]&0xC0 == 0x80 {
		n--
	}
	return s[:n] + "…"
}
