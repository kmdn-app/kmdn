// Package ids generates prefixed, sortable identifiers such as usr_01J… (ULID).
package ids

import (
	"crypto/rand"
	"strings"
	"sync"
	"time"

	"github.com/oklog/ulid/v2"
)

// Prefixes used across kmdn. See docs/specs/10-data-model.md.
const (
	User       = "usr"
	Org        = "org"
	Session    = "ses"
	Repo       = "rep"
	Revision   = "rev"
	Group      = "grp"
	Invite     = "inv"
	Secret     = "sec"
	Job        = "job"
	Thread     = "thr"
	Comment    = "cmt"
	Suggestion = "sug"
	AgentKey   = "ak"
	Upload     = "upl"
	Audit      = "aud"

	RevisionEvent = "rve"
	RevisionFile  = "rvf"
	Checkpoint    = "chk"
	YDoc          = "ydc"
	YSnapshot     = "yss"
)

var (
	mu      sync.Mutex
	entropy = ulid.Monotonic(rand.Reader, 0)
)

// New returns prefix_ULID in lower case.
func New(prefix string) string {
	mu.Lock()
	id := ulid.MustNew(ulid.Timestamp(time.Now()), entropy)
	mu.Unlock()
	return prefix + "_" + strings.ToLower(id.String())
}

// HasPrefix reports whether id was made with the given prefix.
func HasPrefix(id, prefix string) bool { return strings.HasPrefix(id, prefix+"_") }
