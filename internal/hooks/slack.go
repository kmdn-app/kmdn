package hooks

import (
	"fmt"
	"strings"
)

// Slack Block Kit message for an event: a linked title, what happened and
// who did it, then the repository, state and page count.
func slackMessage(p Payload) map[string]any {
	actor, _ := p.Actor["name"].(string)
	if actor == "" {
		actor = "kmdn"
	}
	actor = escape(actor)
	repo, _ := p.Repo["slug"].(string)
	repoURL, _ := p.Repo["url"].(string)
	var title, link, what, state string
	files := 0
	if p.Revision != nil {
		title = fmt.Sprintf("#%v %v", p.Revision["number"], p.Revision["title"])
		link, _ = p.Revision["url"].(string)
		state = fmt.Sprint(p.Revision["state"])
	}
	if n, ok := p.Data["files"].(float64); ok {
		files = int(n)
	}
	switch p.Type {
	case "revision.submitted":
		what = actor + " asked for review"
	case "revision.approved":
		what = "Every reviewer approved: ready to publish"
	case "revision.changes_requested":
		what = actor + " asked for changes"
		if note, _ := p.Data["note"].(string); note != "" {
			what += ": " + quote(note)
		}
	case "revision.update_available":
		what = "Published changed pages in this revision"
	case "revision.published":
		what = actor + " published it"
	case "revision.closed":
		what = actor + " closed it"
	case "discussion.created":
		path, _ := p.Data["path"].(string)
		link, _ = p.Data["url"].(string)
		title = path
		what = actor + " commented"
		if q, _ := p.Data["quote"].(string); q != "" {
			what += " on " + quote(q)
		}
		if b, _ := p.Data["body"].(string); b != "" {
			what += "\n>" + strings.ReplaceAll(escape(truncate(b, 300)), "\n", "\n>")
		}
	case "ping":
		title, link, what = repo, repoURL, "kmdn can reach this channel."
	default:
		what = p.Type
	}
	head := escape(title)
	if link != "" {
		head = "<" + link + "|" + escape(title) + ">"
	}
	context := []any{map[string]any{"type": "mrkdwn", "text": "<" + repoURL + "|" + escape(repo) + ">"}}
	if state != "" {
		context = append(context, map[string]any{"type": "mrkdwn", "text": strings.ReplaceAll(state, "_", " ")})
	}
	if files > 0 {
		unit := "pages"
		if files == 1 {
			unit = "page"
		}
		context = append(context, map[string]any{"type": "mrkdwn", "text": fmt.Sprintf("%d %s", files, unit)})
	}
	return map[string]any{
		"text": title + ": " + what,
		"blocks": []any{
			map[string]any{"type": "section", "text": map[string]any{"type": "mrkdwn", "text": "*" + head + "*\n" + what}},
			map[string]any{"type": "context", "elements": context},
		},
	}
}

func quote(s string) string {
	return "“" + escape(truncate(strings.Join(strings.Fields(s), " "), 120)) + "”"
}

func truncate(s string, n int) string {
	if r := []rune(s); len(r) > n {
		return string(r[:n-1]) + "…"
	}
	return s
}

// escape makes text safe for Slack mrkdwn.
func escape(s string) string {
	return strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;").Replace(s)
}
