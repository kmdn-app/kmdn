// Package search indexes published markdown for full-text search (SQLite
// FTS5 or Postgres tsvector) and answers queries with heading-level hits.
package search

import (
	"regexp"
	"strings"
)

// Doc is the searchable form of a markdown file.
type Doc struct {
	Title    string
	Headings []string
	Body     string
}

var (
	fmTitle    = regexp.MustCompile(`(?m)^title:\s*["']?(.+?)["']?\s*$`)
	atxHeading = regexp.MustCompile(`^(#{1,6})\s+(.*?)\s*#*\s*$`)
	linkRe     = regexp.MustCompile(`!?\[([^\]]*)\]\([^)]*\)`)
	refLinkRe  = regexp.MustCompile(`!?\[([^\]]*)\]\[[^\]]*\]`)
	htmlTag    = regexp.MustCompile(`</?[A-Za-z][^>]*>`)
	emphasis   = regexp.MustCompile("[*_~`]+")
	listMarker = regexp.MustCompile(`^\s*(?:[-*+]|\d+[.)])\s+(?:\[[ xX]\]\s+)?`)
	shortcode  = regexp.MustCompile(`\{\{[<%].*?[>%]\}\}|\{%.*?%\}`)
)

// Extract turns markdown into title, headings and plain text. It's lossy on
// purpose: search needs words, not structure.
func Extract(md, fallbackTitle string) Doc {
	var d Doc
	lines := strings.Split(strings.ReplaceAll(md, "\r\n", "\n"), "\n")
	i := 0
	if len(lines) > 0 && (strings.TrimSpace(lines[0]) == "---" || strings.TrimSpace(lines[0]) == "+++") {
		fence := strings.TrimSpace(lines[0])
		for j := 1; j < len(lines); j++ {
			if strings.TrimSpace(lines[j]) == fence {
				fm := strings.Join(lines[1:j], "\n")
				if m := fmTitle.FindStringSubmatch(strings.ReplaceAll(fm, " = ", ": ")); m != nil {
					d.Title = strings.Trim(m[1], `"'`)
				}
				i = j + 1
				break
			}
		}
	}
	var body strings.Builder
	inFence := false
	for ; i < len(lines); i++ {
		l := lines[i]
		t := strings.TrimSpace(l)
		if strings.HasPrefix(t, "```") || strings.HasPrefix(t, "~~~") {
			inFence = !inFence
			continue
		}
		if inFence {
			body.WriteString(t)
			body.WriteByte('\n')
			continue
		}
		if m := atxHeading.FindStringSubmatch(t); m != nil {
			h := clean(m[2])
			d.Headings = append(d.Headings, h)
			if d.Title == "" && len(m[1]) == 1 {
				d.Title = h
			}
			continue
		}
		// Setext heading underline: previous line was a heading.
		if (strings.Trim(t, "=") == "" || strings.Trim(t, "-") == "") && len(t) >= 2 && i > 0 && strings.TrimSpace(lines[i-1]) != "" && !strings.HasPrefix(strings.TrimSpace(lines[i-1]), "-") {
			prev := clean(strings.TrimSpace(lines[i-1]))
			d.Headings = append(d.Headings, prev)
			if d.Title == "" && strings.HasPrefix(t, "=") {
				d.Title = prev
			}
			continue
		}
		if strings.HasPrefix(t, "|") && strings.Trim(t, "|-: ") == "" {
			continue
		}
		t = listMarker.ReplaceAllString(t, "")
		t = strings.TrimLeft(t, "> ")
		body.WriteString(clean(t))
		body.WriteByte('\n')
	}
	if d.Title == "" {
		d.Title = fallbackTitle
	}
	d.Body = strings.TrimSpace(collapseBlank(body.String()))
	return d
}

func clean(s string) string {
	s = shortcode.ReplaceAllString(s, " ")
	s = linkRe.ReplaceAllString(s, "$1")
	s = refLinkRe.ReplaceAllString(s, "$1")
	s = htmlTag.ReplaceAllString(s, " ")
	s = emphasis.ReplaceAllString(s, "")
	s = strings.ReplaceAll(s, "|", " ")
	return strings.Join(strings.Fields(s), " ")
}

func collapseBlank(s string) string {
	for strings.Contains(s, "\n\n\n") {
		s = strings.ReplaceAll(s, "\n\n\n", "\n\n")
	}
	return s
}

// Slug makes a GitHub-style heading anchor.
func Slug(h string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(h) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9', r == '-', r == '_':
			b.WriteRune(r)
		case r == ' ':
			b.WriteByte('-')
		case r > 127:
			b.WriteRune(r)
		}
	}
	return b.String()
}

// Clean strips inline markdown (links, emphasis, HTML, shortcodes) from a line.
func Clean(s string) string { return clean(s) }

// StripListMarker removes a leading list marker or task box.
func StripListMarker(s string) string { return listMarker.ReplaceAllString(s, "") }
