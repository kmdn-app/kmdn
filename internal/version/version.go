// Package version holds build metadata, set with -ldflags at build time.
package version

import "runtime"

var (
	Version = "dev"
	Commit  = "unknown"
	Date    = ""
)

// Info is the payload of GET /version.
type Info struct {
	Version string `json:"version"`
	Commit  string `json:"commit"`
	Date    string `json:"date,omitempty"`
	Go      string `json:"go"`
}

func Get() Info { return Info{Version: Version, Commit: Commit, Date: Date, Go: runtime.Version()} }
