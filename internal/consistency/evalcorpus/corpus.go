// Package evalcorpus generates a handbook-like corpus with planted
// contradictions, duplicates and look-alike traps, to measure consistency
// check quality (spike S8, docs/specs/14-roadmap.md#spikes).
package evalcorpus

import (
	"fmt"
	"math/rand/v2"
	"path"
	"sort"
	"strings"
)

// Fact is a policy statement with a value; pages restate facts.
type Fact struct {
	Topic string
	Key   string
	// Say renders the fact with a value (it must contain the value verbatim).
	Say   func(v string) string
	Value string
	// Other is a different, conflicting value.
	Other string
	// Scoped renders a legitimately different rule for another group (a trap:
	// not a contradiction).
	Scoped func(v string) string
}

// Plant is a contradiction the corpus contains: Path states Fact with Other
// while other pages state Value.
type Plant struct {
	Path string
	Fact string // topic/key
}

// Corpus is the generated repository.
type Corpus struct {
	Files          map[string]string
	Contradictions []Plant
	// Duplicates: pairs of paths sharing a copied paragraph.
	Duplicates [][2]string
	// Traps: pages with a scoped rule that differs on purpose.
	Traps []string
	// FactPages lists, per fact, the pages stating its canonical value.
	FactPages map[string][]string
}

func facts() []Fact {
	f := func(topic, key, value, other string, say func(string) string, scoped func(string) string) Fact {
		return Fact{Topic: topic, Key: key, Value: value, Other: other, Say: say, Scoped: scoped}
	}
	return []Fact{
		f("travel", "notice", "14 days", "7 days", func(v string) string {
			return "Book flights at least " + v + " before you travel so the travel desk can find a fair fare."
		}, func(v string) string {
			return "Contractors book their own travel and aren't bound by the " + v + " rule."
		}),
		f("travel", "meals", "45 euros", "60 euros", func(v string) string { return "The daily meal allowance on business trips abroad is " + v + "." }, nil),
		f("travel", "class", "six hours", "four hours", func(v string) string {
			return "Economy class is the default; business class is allowed for flights longer than " + v + "."
		}, nil),
		f("remote", "abroad", "30 working days", "20 working days", func(v string) string {
			return "Employees can work from another country for up to " + v + " per calendar year."
		}, func(v string) string {
			return "Interns can work abroad for up to 10 working days a year, unlike the " + v + " for employees."
		}),
		f("remote", "stipend", "300 euros", "500 euros", func(v string) string {
			return "Everyone gets a one-time home office stipend of " + v + " in their first month."
		}, nil),
		f("expenses", "deadline", "30 days", "60 days", func(v string) string {
			return "Submit expense reports within " + v + " of the purchase, with the receipt attached."
		}, nil),
		f("expenses", "approval", "500 euros", "1,000 euros", func(v string) string { return "Purchases above " + v + " need your manager's approval before you buy." }, nil),
		f("laptops", "refresh", "three years", "four years", func(v string) string { return "Laptops are replaced every " + v + ", or sooner if they break." }, func(v string) string {
			return "Test devices in the QA lab are replaced every two years, not every " + v + " like laptops."
		}),
		f("laptops", "choice", "a MacBook Pro or a ThinkPad X1", "a MacBook Air or a Dell XPS", func(v string) string { return "New hires choose between " + v + " on their first day." }, nil),
		f("holidays", "days", "25 days", "28 days", func(v string) string {
			return "Full-time employees get " + v + " of paid holiday per year, plus public holidays."
		}, func(v string) string {
			return "Part-time employees get holiday pro rata, based on the " + v + " for full-time staff."
		}),
		f("holidays", "carryover", "five days", "ten days", func(v string) string {
			return "You can carry over up to " + v + " of unused holiday into the next year."
		}, nil),
		f("parental", "weeks", "16 weeks", "12 weeks", func(v string) string { return "Every new parent can take " + v + " of fully paid parental leave." }, nil),
		f("onboarding", "buddy", "the first two weeks", "the first month", func(v string) string { return "Each new hire gets a buddy who checks in daily during " + v + "." }, nil),
		f("security", "rotation", "90 days", "180 days", func(v string) string { return "Service account passwords are rotated every " + v + "." }, func(v string) string {
			return "Personal passwords don't expire; only service accounts rotate every " + v + "."
		}),
		f("security", "mfa", "a hardware key", "an authenticator app", func(v string) string { return "Production access requires " + v + " as the second factor." }, nil),
		f("office", "hours", "8:00 to 19:00", "7:00 to 20:00", func(v string) string { return "The office is open from " + v + " on weekdays." }, nil),
		f("training", "budget", "1,500 euros", "2,000 euros", func(v string) string {
			return "Each employee has a yearly learning budget of " + v + " for courses, books and conferences."
		}, nil),
		f("sick", "note", "three days", "five days", func(v string) string { return "A doctor's note is needed for sick leave longer than " + v + "." }, nil),
		f("equipment", "return", "10 working days", "30 days", func(v string) string { return "Return company equipment within " + v + " of your last day." }, nil),
		f("equipment", "monitor", "two monitors", "one monitor", func(v string) string { return "Desks in the office come with " + v + " and a docking station." }, nil),
	}
}

var filler = []string{
	"Ask in #people-ops if anything here is unclear.",
	"This page is maintained by the People team and reviewed every quarter.",
	"Your manager can help you plan around this.",
	"See the related pages in this folder for details.",
	"We keep this short on purpose: the policy itself is the source of truth.",
	"Exceptions are rare and need a written approval.",
	"Most questions are answered in the FAQ.",
	"When in doubt, check with your team lead before acting.",
}

var kinds = []string{"overview", "faq", "how-to", "team-notes", "checklist", "guide"}

// Generate builds a corpus of pages with plants contradictions and dups
// duplicates, deterministically from seed.
func Generate(pages, plants, dups int, seed uint64) Corpus {
	r := rand.New(rand.NewPCG(seed, seed^0x9e3779b97f4a7c15))
	fs := facts()
	byTopic := map[string][]Fact{}
	var topics []string
	for _, f := range fs {
		if _, ok := byTopic[f.Topic]; !ok {
			topics = append(topics, f.Topic)
		}
		byTopic[f.Topic] = append(byTopic[f.Topic], f)
	}
	c := Corpus{Files: map[string]string{}, FactPages: map[string][]string{}}
	type stmt struct {
		path string
		fact Fact
	}
	var stmts []stmt
	bodies := map[string][]string{} // path → sections
	var paths []string
	for i := 0; i < pages; i++ {
		topic := topics[i%len(topics)]
		p := fmt.Sprintf("docs/%s/%s-%03d.md", topic, kinds[r.IntN(len(kinds))], i)
		paths = append(paths, p)
		title := strings.ToUpper(topic[:1]) + topic[1:] + " " + kinds[i%len(kinds)]
		var secs []string
		secs = append(secs, "# "+title+"\n\n"+filler[r.IntN(len(filler))]+" "+filler[r.IntN(len(filler))])
		// Two or three facts from the topic, one per section.
		tf := byTopic[topic]
		n := 2 + r.IntN(2)
		for j := 0; j < n; j++ {
			f := tf[(i+j)%len(tf)]
			secs = append(secs, fmt.Sprintf("## %s\n\n%s %s", strings.ToUpper(f.Key[:1])+f.Key[1:], f.Say(f.Value), filler[r.IntN(len(filler))]))
			stmts = append(stmts, stmt{p, f})
			c.FactPages[f.Topic+"/"+f.Key] = append(c.FactPages[f.Topic+"/"+f.Key], p)
		}
		bodies[p] = secs
	}
	// Plant contradictions: restate a fact with the other value, once per fact.
	used := map[string]bool{}
	for _, i := range r.Perm(len(stmts)) {
		if len(c.Contradictions) == plants {
			break
		}
		s := stmts[i]
		key := s.fact.Topic + "/" + s.fact.Key
		if used[key] || used[s.path] || len(c.FactPages[key]) < 2 {
			continue
		}
		used[key], used[s.path] = true, true
		secs := bodies[s.path]
		for k, sec := range secs {
			if strings.Contains(sec, s.fact.Say(s.fact.Value)) {
				secs[k] = strings.Replace(sec, s.fact.Say(s.fact.Value), s.fact.Say(s.fact.Other), 1)
			}
		}
		c.Contradictions = append(c.Contradictions, Plant{Path: s.path, Fact: key})
		pages := c.FactPages[key][:0:0]
		for _, fp := range c.FactPages[key] {
			if fp != s.path {
				pages = append(pages, fp)
			}
		}
		c.FactPages[key] = pages
	}
	// Traps: scoped rules that differ on purpose, on pages without plants.
	for _, f := range fs {
		if f.Scoped == nil {
			continue
		}
		for _, p := range paths {
			if !used[p] && strings.Contains(p, "/"+f.Topic+"/") {
				bodies[p] = append(bodies[p], "## Other groups\n\n"+f.Scoped(f.Value))
				c.Traps = append(c.Traps, p)
				used[p] = true
				break
			}
		}
	}
	// Duplicates: copy a fact section verbatim into a page of another topic.
	for d := 0; d < dups; d++ {
		from, to := paths[r.IntN(len(paths))], paths[r.IntN(len(paths))]
		if from == to || path.Dir(from) == path.Dir(to) || used[to] {
			d--
			continue
		}
		bodies[to] = append(bodies[to], bodies[from][1])
		c.Duplicates = append(c.Duplicates, [2]string{from, to})
		used[to] = true
	}
	for p, secs := range bodies {
		c.Files[p] = strings.Join(secs, "\n\n") + "\n"
	}
	sort.Slice(c.Contradictions, func(i, j int) bool { return c.Contradictions[i].Path < c.Contradictions[j].Path })
	return c
}
