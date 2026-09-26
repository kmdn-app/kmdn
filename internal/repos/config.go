package repos

import (
	"fmt"
	"path"
	"strings"

	"github.com/bmatcuk/doublestar/v4"
	"gopkg.in/yaml.v3"
)

// KmdnYML is the optional .kmdn.yml at the root of the target branch.
// It can override scope and assets settings but never the target branch.
// See docs/specs/06-git-and-forges.md#repo-connection-settings.
type KmdnYML struct {
	Root      *string           `yaml:"root" json:"root,omitempty"`
	Include   []string          `yaml:"include" json:"include,omitempty"`
	Exclude   []string          `yaml:"exclude" json:"exclude,omitempty"`
	Assets    *AssetsConfig     `yaml:"assets" json:"assets,omitempty"`
	Routes    map[string]string `yaml:"routes" json:"routes,omitempty"`
	Templates string            `yaml:"templates" json:"templates,omitempty"`
	// Error is set when the file exists but can't be parsed (shown in settings).
	Error string `yaml:"-" json:"error,omitempty"`
}

// AssetsConfig controls where uploaded images go.
type AssetsConfig struct {
	Path          string `yaml:"path" json:"path"`
	MaxSizeMB     int    `yaml:"maxSizeMB" json:"max_size_mb"`
	ConvertToWebp bool   `yaml:"convertToWebp" json:"convert_to_webp"`
}

// ParseKmdnYML parses the file content; errors are reported in the result.
func ParseKmdnYML(b []byte) KmdnYML {
	var k KmdnYML
	dec := yaml.NewDecoder(strings.NewReader(string(b)))
	dec.KnownFields(true)
	if err := dec.Decode(&k); err != nil && err.Error() != "EOF" {
		return KmdnYML{Error: err.Error()}
	}
	for _, g := range append(append([]string{}, k.Include...), k.Exclude...) {
		if !doublestar.ValidatePattern(g) {
			return KmdnYML{Error: fmt.Sprintf("invalid glob %q", g)}
		}
	}
	return k
}

// DefaultInclude lists the files kmdn shows when no include globs are set.
var DefaultInclude = []string{"**/*.md", "**/*.markdown", "**/*.mdx", "**/*.{png,jpg,jpeg,gif,svg,webp,avif}"}

// Scope decides which paths of the repository kmdn exposes.
type Scope struct {
	Root    string   `json:"root"`    // "" or "docs" (no leading/trailing slash)
	Include []string `json:"include"` // relative to the repo root
	Exclude []string `json:"exclude"`
	// Which values came from .kmdn.yml (for the settings UI).
	FromFile []string `json:"from_file,omitempty"`
}

// NormalizeRoot cleans a content root: "docs/" → "docs", "/" → "".
func NormalizeRoot(r string) string {
	r = strings.Trim(path.Clean("/"+strings.TrimSpace(r)), "/")
	if r == "." {
		return ""
	}
	return r
}

// EffectiveScope merges repo settings with .kmdn.yml overrides.
func EffectiveScope(root string, include, exclude []string, k KmdnYML) Scope {
	s := Scope{Root: NormalizeRoot(root), Include: include, Exclude: exclude}
	if k.Error == "" {
		if k.Root != nil {
			s.Root = NormalizeRoot(*k.Root)
			s.FromFile = append(s.FromFile, "root")
		}
		if len(k.Include) > 0 {
			s.Include = k.Include
			s.FromFile = append(s.FromFile, "include")
		}
		if len(k.Exclude) > 0 {
			s.Exclude = k.Exclude
			s.FromFile = append(s.FromFile, "exclude")
		}
	}
	if len(s.Include) == 0 {
		s.Include = DefaultInclude
	}
	return s
}

// Contains reports whether a repo-relative file path is exposed by kmdn.
func (s Scope) Contains(p string) bool {
	p = strings.TrimPrefix(p, "/")
	if s.Root != "" && !strings.HasPrefix(p, s.Root+"/") {
		return false
	}
	rel := strings.TrimPrefix(p, s.Root+"/")
	if s.Root == "" {
		rel = p
	}
	matches := func(globs []string) bool {
		for _, g := range globs {
			g = strings.TrimPrefix(g, "/")
			// Globs may be written relative to the repo root or to the content root.
			if ok, _ := doublestar.Match(g, p); ok {
				return true
			}
			if ok, _ := doublestar.Match(g, rel); ok {
				return true
			}
		}
		return false
	}
	return matches(s.Include) && !matches(s.Exclude)
}

// IsMarkdown reports whether a path is an editable markdown document.
func IsMarkdown(p string) bool {
	switch strings.ToLower(path.Ext(p)) {
	case ".md", ".markdown", ".mdx":
		return true
	}
	return false
}
