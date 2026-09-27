// Package consistency finds places where a repository says the same thing
// twice or says contradicting things: a passage index with embeddings,
// brute-force neighbour search, and LLM judgment of close pairs
// (docs/specs/08-assistant.md#consistency-check-duplicates-and-contradictions).
package consistency

import (
	"crypto/sha256"
	"encoding/hex"
	"path"
	"regexp"
	"strings"

	"github.com/kmdn-app/kmdn/internal/search"
)

// Passage is a heading section of a page, or part of one.
type Passage struct {
	Path    string `json:"path"`
	Seq     int    `json:"-"`
	Slug    string `json:"slug"`
	Heading string `json:"heading"`
	Line    int    `json:"line"`
	Text    string `json:"text"`
	// Context is the page title and heading trail, embedded with the text.
	Context string `json:"-"`
	Hash    string `json:"-"`
}

// Embedded is what gets embedded and hashed.
func (p Passage) Embedded() string {
	if p.Context == "" {
		return p.Text
	}
	return p.Context + "\n\n" + p.Text
}

const (
	// maxChars is about 300 tokens.
	maxChars = 1200
	// minWords: shorter passages rarely state anything to contradict.
	minWords = 12
)

var atx = regexp.MustCompile(`^(#{1,6})\s+(.*?)\s*#*\s*$`)

// Chunk splits a page into passages: one per heading section, split further
// at paragraph (then sentence) boundaries near maxChars.
func Chunk(p, md string) []Passage {
	lines := strings.Split(strings.ReplaceAll(md, "\r\n", "\n"), "\n")
	i := skipFrontmatter(lines)
	title := strings.TrimSuffix(path.Base(p), path.Ext(p))
	trail := make([]string, 7) // heading text by depth
	var out []Passage
	type para struct {
		text string
		line int
	}
	var paras []para
	var cur strings.Builder
	curLine := 0
	heading, slug, headLine := "", "", 0
	endPara := func() {
		if t := strings.TrimSpace(cur.String()); t != "" {
			paras = append(paras, para{t, curLine})
		}
		cur.Reset()
		curLine = 0
	}
	endSection := func() {
		endPara()
		ctx := title
		for _, h := range trail[1:] {
			if h != "" && h != title {
				ctx += " › " + h
			}
		}
		var buf []string
		size, line := 0, 0
		flush := func() {
			text := strings.Join(buf, "\n")
			if len(strings.Fields(text)) >= minWords {
				out = append(out, Passage{Path: p, Seq: len(out), Slug: slug, Heading: heading, Line: max(line, headLine), Text: text, Context: ctx})
			}
			buf, size, line = nil, 0, 0
		}
		for _, pa := range paras {
			for _, piece := range splitLong(pa.text) {
				if size > 0 && size+len(piece) > maxChars {
					flush()
				}
				if line == 0 {
					line = pa.line
				}
				buf = append(buf, piece)
				size += len(piece) + 1
			}
		}
		if size > 0 {
			flush()
		}
		paras = nil
	}
	inFence := false
	for ; i < len(lines); i++ {
		raw := lines[i]
		t := strings.TrimSpace(raw)
		if strings.HasPrefix(t, "```") || strings.HasPrefix(t, "~~~") {
			inFence = !inFence
			continue
		}
		// Code isn't prose that can contradict anything.
		if inFence {
			continue
		}
		if m := atx.FindStringSubmatch(t); m != nil {
			endSection()
			depth := len(m[1])
			h := search.Clean(m[2])
			trail[depth] = h
			for d := depth + 1; d < len(trail); d++ {
				trail[d] = ""
			}
			if depth == 1 && len(out) == 0 {
				title = h
				trail[1] = h
			}
			heading, slug, headLine = h, search.Slug(h), i+1
			continue
		}
		if t == "" {
			endPara()
			continue
		}
		if strings.HasPrefix(t, "|") && strings.Trim(t, "|-: ") == "" {
			continue
		}
		c := search.Clean(strings.TrimLeft(search.StripListMarker(t), "> "))
		if c == "" {
			continue
		}
		if curLine == 0 {
			curLine = i + 1
		}
		cur.WriteString(c)
		cur.WriteByte(' ')
	}
	endSection()
	for k := range out {
		out[k].Seq = k
		out[k].Hash = hash(out[k].Embedded())
	}
	return out
}

func skipFrontmatter(lines []string) int {
	if len(lines) == 0 {
		return 0
	}
	fence := strings.TrimSpace(lines[0])
	if fence != "---" && fence != "+++" {
		return 0
	}
	for j := 1; j < len(lines); j++ {
		if strings.TrimSpace(lines[j]) == fence {
			return j + 1
		}
	}
	return 0
}

// splitLong breaks a paragraph longer than maxChars at sentence ends.
func splitLong(s string) []string {
	if len(s) <= maxChars {
		return []string{s}
	}
	var out []string
	for len(s) > maxChars {
		cut := strings.LastIndex(s[:maxChars], ". ")
		if cut < maxChars/3 {
			cut = strings.LastIndex(s[:maxChars], " ")
		}
		if cut <= 0 {
			cut = maxChars - 1
		}
		out = append(out, strings.TrimSpace(s[:cut+1]))
		s = strings.TrimSpace(s[cut+1:])
	}
	if s != "" {
		out = append(out, s)
	}
	return out
}

func hash(s string) string {
	h := sha256.Sum256([]byte(s))
	return hex.EncodeToString(h[:16])
}

// PairKey identifies a pair of passages by content, in either order.
func PairKey(a, b string) string {
	if a > b {
		a, b = b, a
	}
	return a + ":" + b
}
