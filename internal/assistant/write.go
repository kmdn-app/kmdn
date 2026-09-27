package assistant

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/kmdn-app/kmdn/internal/docengine"
	"github.com/kmdn-app/kmdn/internal/gitmirror"
	"github.com/kmdn-app/kmdn/internal/ids"
	"github.com/kmdn-app/kmdn/internal/repos"
	"github.com/kmdn-app/kmdn/internal/revisions"
	"github.com/kmdn-app/kmdn/internal/store"
	"github.com/kmdn-app/kmdn/internal/threads"

	"github.com/kmdn-app/kmdn/internal/llm"
)

// Docs is what revision edits need from the collaborative documents.
type Docs interface {
	FlushRevision(ctx context.Context, revID string)
	Suggest(ctx context.Context, repo repos.Repo, rev revisions.Revision, c revisions.Caller, p, markdown string, attrs docengine.SuggestAttrs, kind string) (bool, error)
}

// writeTools produce suggestions in the revision; file operations are
// proposals the person confirms (docs/specs/08-assistant.md#tools).
var writeTools = []llm.Tool{
	{Name: "edit_file", Description: "Suggest changes to a page of the revision. Prefer small edits: each find must match the page's current markdown exactly once. Or pass new_markdown to replace the whole page. People accept or reject the suggestions.",
		Schema: schema(`{"type":"object","properties":{"path":{"type":"string"},"edits":{"type":"array","items":{"type":"object","properties":{"find":{"type":"string"},"replace":{"type":"string"}},"required":["find","replace"]}},"new_markdown":{"type":"string"}},"required":["path"]}`)},
	{Name: "create_file", Description: "Suggest a new page in the revision (its content is one suggestion to accept).",
		Schema: schema(`{"type":"object","properties":{"path":{"type":"string"},"markdown":{"type":"string"}},"required":["path","markdown"]}`)},
	{Name: "rename_file", Description: "Propose renaming or moving a page. The person confirms it.",
		Schema: schema(`{"type":"object","properties":{"from":{"type":"string"},"to":{"type":"string"},"rewrite_links":{"type":"boolean"}},"required":["from","to"]}`)},
	{Name: "delete_file", Description: "Propose deleting a page. The person confirms it.",
		Schema: schema(`{"type":"object","properties":{"path":{"type":"string"}},"required":["path"]}`)},
	{Name: "reply_to_thread", Description: "Reply in one of the revision's comment threads, when asked to.",
		Schema: schema(`{"type":"object","properties":{"thread_id":{"type":"string"},"body":{"type":"string"}},"required":["thread_id","body"]}`)},
}

// fileOps are tool calls the person confirms.
var fileOps = map[string]bool{"rename_file": true, "delete_file": true}

// revisionFile is a page as the revision sees it (after flushing live edits).
func (e env) revisionFile(ctx context.Context, p string) (revisions.File, bool, error) {
	f, err := revisions.FileAt(ctx, e.s.DB, e.rev.ID, p)
	if errors.Is(err, store.ErrNotFound) {
		return f, false, nil
	}
	return f, err == nil, err
}

// pageMarkdown is a page's current markdown in the revision (pending suggestions excluded).
func (e env) pageMarkdown(ctx context.Context, p string) (string, bool, error) {
	if e.rev != nil {
		e.s.Docs.FlushRevision(ctx, e.rev.ID)
		f, ok, err := e.revisionFile(ctx, p)
		if err != nil {
			return "", false, err
		}
		if ok {
			if f.Op == revisions.OpDelete {
				return "", false, fmt.Errorf("%w: %s is deleted in this revision", errTool, p)
			}
			return f.ContentMD, true, nil
		}
	}
	f, err := e.s.Repos.ReadFile(ctx, e.repo, p, "")
	if err != nil {
		if errors.Is(err, gitmirror.ErrNotFound) {
			return "", false, fmt.Errorf("%w: there's no page at %s", errTool, p)
		}
		return "", false, err
	}
	return f.Content, false, nil
}

func (e env) canEdit(ctx context.Context) error {
	acc, err := e.s.Revisions.AccessFor(ctx, *e.rev, e.caller)
	if err != nil {
		return err
	}
	if !acc.CanEdit {
		return fmt.Errorf("%w: %s can't edit this revision right now, so no suggestions can be made", errTool, e.caller.User.Name)
	}
	return nil
}

func (e env) attrs() docengine.SuggestAttrs {
	return docengine.SuggestAttrs{ID: ids.New("sg"), Author: e.caller.User.ID, At: time.Now().UnixMilli(), Assistant: true}
}

func (e env) editFile(ctx context.Context, in json.RawMessage) (string, error) {
	ctx, freshRevision, unlock, gateErr := e.s.Revisions.Mutate(ctx, e.rev.ID)
	if gateErr != nil {
		return "", gateErr
	}
	defer unlock()
	e.rev = &freshRevision
	var args struct {
		Path  string `json:"path"`
		Edits []struct {
			Find    string `json:"find"`
			Replace string `json:"replace"`
		} `json:"edits"`
		NewMarkdown *string `json:"new_markdown"`
	}
	if err := json.Unmarshal(in, &args); err != nil {
		return "", fmt.Errorf("%w: bad input: %w", errTool, err)
	}
	if err := e.canEdit(ctx); err != nil {
		return "", err
	}
	p := e.clean(args.Path)
	if !repos.IsMarkdown(p) {
		return "", fmt.Errorf("%w: %s isn't a page", errTool, p)
	}
	current, inRev, err := e.pageMarkdown(ctx, p)
	if err != nil {
		return "", err
	}
	next := current
	switch {
	case args.NewMarkdown != nil:
		next = *args.NewMarkdown
	case len(args.Edits) == 0:
		return "", fmt.Errorf("%w: pass edits or new_markdown", errTool)
	default:
		for i, ed := range args.Edits {
			if ed.Find == "" {
				return "", fmt.Errorf("%w: edit %d has an empty find", errTool, i+1)
			}
			switch n := strings.Count(next, ed.Find); n {
			case 0:
				return "", fmt.Errorf("%w: edit %d: the page doesn't contain %q (read it again: it may have changed)", errTool, i+1, truncate(ed.Find, 80))
			case 1:
				next = strings.Replace(next, ed.Find, ed.Replace, 1)
			default:
				return "", fmt.Errorf("%w: edit %d: %q appears %d times; include more context", errTool, i+1, truncate(ed.Find, 80), n)
			}
		}
	}
	if next == current {
		return "No change: the page already reads that way.", nil
	}
	if !inRev {
		if _, err := e.s.Revisions.ApplyFileOp(ctx, e.repo, *e.rev, e.caller, revisions.FileOp{Op: revisions.OpModify, Path: p}); err != nil {
			return "", err
		}
	}
	changed, err := e.s.Docs.Suggest(ctx, e.repo, *e.rev, e.caller, p, next, e.attrs(), "assistant")
	if err != nil {
		return "", err
	}
	if !changed {
		return "No change.", nil
	}
	return fmt.Sprintf("Suggested the changes in %s. People review them there and accept or reject them.", p), nil
}

func (e env) createFile(ctx context.Context, in json.RawMessage) (string, error) {
	ctx, freshRevision, unlock, gateErr := e.s.Revisions.Mutate(ctx, e.rev.ID)
	if gateErr != nil {
		return "", gateErr
	}
	defer unlock()
	e.rev = &freshRevision
	var args struct {
		Path     string `json:"path"`
		Markdown string `json:"markdown"`
	}
	if err := json.Unmarshal(in, &args); err != nil {
		return "", fmt.Errorf("%w: bad input: %w", errTool, err)
	}
	if err := e.canEdit(ctx); err != nil {
		return "", err
	}
	p := e.clean(args.Path)
	if !repos.IsMarkdown(p) || !e.repo.Scope().Contains(p) {
		return "", fmt.Errorf("%w: pages are .md files under the content root", errTool)
	}
	if strings.TrimSpace(args.Markdown) == "" {
		return "", fmt.Errorf("%w: write the page's markdown", errTool)
	}
	// An empty page first: its whole content is then one suggestion.
	if _, err := e.s.Revisions.ApplyFileOp(ctx, e.repo, *e.rev, e.caller, revisions.FileOp{Op: revisions.OpAdd, Path: p, Content: "\n"}); err != nil {
		var cf *revisions.ErrConflict
		if errors.As(err, &cf) {
			return "", fmt.Errorf("%w: %s", errTool, cf.Msg)
		}
		return "", err
	}
	if _, err := e.s.Docs.Suggest(ctx, e.repo, *e.rev, e.caller, p, args.Markdown, e.attrs(), "assistant"); err != nil {
		return "", err
	}
	return fmt.Sprintf("Suggested the new page %s.", p), nil
}

func (e env) replyToThread(ctx context.Context, in json.RawMessage) (string, error) {
	var args struct {
		ThreadID string `json:"thread_id"`
		Body     string `json:"body"`
	}
	if err := json.Unmarshal(in, &args); err != nil {
		return "", fmt.Errorf("%w: bad input: %w", errTool, err)
	}
	t, err := threads.Get(ctx, e.s.DB, args.ThreadID)
	if err != nil || t.RevisionID != e.rev.ID {
		return "", fmt.Errorf("%w: no such thread in this revision", errTool)
	}
	if _, err := threads.Reply(ctx, e.s.DB, t.ID, e.caller.User.ID, strings.TrimSpace(args.Body)+"\n\n— via the assistant"); err != nil {
		return "", err
	}
	return "Replied.", nil
}

// revisionComments lists the revision's comment threads (read_comments in a revision).
func (e env) revisionComments(ctx context.Context, p string) (string, error) {
	f := threads.Filter{RevisionID: e.rev.ID, State: threads.StateOpen}
	if p != "" {
		f.Path = e.clean(p)
	}
	list, err := threads.List(ctx, e.s.DB, f)
	if err != nil {
		return "", err
	}
	if len(list) == 0 {
		return "No open comment threads.", nil
	}
	var b strings.Builder
	for _, t := range list {
		var a threads.Anchor
		_ = json.Unmarshal(t.Anchor, &a)
		fmt.Fprintf(&b, "- thread %s on %s, “%s”:\n", t.ID, t.Path, a.Quote)
		for _, c := range t.Comments {
			if !c.Deleted {
				fmt.Fprintf(&b, "  %s: %s\n", nameOr(c.AuthorName), strings.ReplaceAll(c.Body, "\n", " "))
			}
		}
	}
	return b.String(), nil
}

// revisionTree lists the content root as the revision sees it.
func (e env) revisionTree(ctx context.Context, dir string) (string, error) {
	nodes, err := e.s.Repos.Tree(ctx, e.repo)
	if err != nil {
		return "", err
	}
	files, err := revisions.Files(ctx, e.s.DB, e.rev.ID)
	if err != nil {
		return "", err
	}
	ops := map[string]string{}
	for _, f := range files {
		ops[f.Path] = f.Op
		if f.Op == revisions.OpRename {
			ops[f.FromPath] = revisions.OpDelete
		}
	}
	paths := map[string]string{}
	for _, n := range nodes {
		if n.Markdown {
			paths[n.Path] = ""
		}
	}
	for p, op := range ops {
		if op == revisions.OpDelete {
			delete(paths, p)
		} else if repos.IsMarkdown(p) {
			paths[p] = op
		}
	}
	dir = strings.TrimSuffix(e.clean(dir), "/")
	keys := make([]string, 0, len(paths))
	for p := range paths {
		if dir == "" || dir == "." || strings.HasPrefix(p, dir+"/") {
			keys = append(keys, p)
		}
	}
	sort.Strings(keys)
	var b strings.Builder
	for i, p := range keys {
		if i >= 500 {
			b.WriteString("… (more)\n")
			break
		}
		if op := paths[p]; op != "" {
			fmt.Fprintf(&b, "%s (%s in this revision)\n", p, op)
		} else {
			fmt.Fprintf(&b, "%s\n", p)
		}
	}
	if len(keys) == 0 {
		return "Nothing there.", nil
	}
	return b.String(), nil
}

// applyFileOp carries out a confirmed rename or delete.
func applyFileOp(ctx context.Context, s *Service, repo repos.Repo, rev revisions.Revision, c revisions.Caller, name string, input json.RawMessage) (string, error) {
	var args struct {
		From         string `json:"from"`
		To           string `json:"to"`
		Path         string `json:"path"`
		RewriteLinks bool   `json:"rewrite_links"`
	}
	if err := json.Unmarshal(input, &args); err != nil {
		return "", err
	}
	e := env{s: s, repo: repo}
	switch name {
	case "rename_file":
		from, to := e.clean(args.From), e.clean(args.To)
		if _, err := s.Revisions.ApplyFileOp(ctx, repo, rev, c, revisions.FileOp{Op: revisions.OpRename, Path: to, FromPath: from}); err != nil {
			return "", err
		}
		return fmt.Sprintf("Renamed %s to %s.", from, to), nil
	case "delete_file":
		p := e.clean(args.Path)
		if _, err := s.Revisions.ApplyFileOp(ctx, repo, rev, c, revisions.FileOp{Op: revisions.OpDelete, Path: p}); err != nil {
			return "", err
		}
		return fmt.Sprintf("Deleted %s.", p), nil
	}
	return "", errors.New("assistant: not a file operation")
}
