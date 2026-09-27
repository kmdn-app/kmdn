package revisions

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"
)

// SnapshotHash identifies captured content using the same inputs as approvals
// and unsaved-change tracking. Both slices must be ordered by path.
func SnapshotHash(files []File, assets []Asset) string {
	h := sha256.New()
	for _, f := range files {
		h.Write([]byte(f.Path + "\x00" + f.Op + "\x00" + f.FromPath + "\x00" + f.ContentHash + "\n"))
	}
	paths := make([]string, 0, len(assets))
	for _, a := range assets {
		paths = append(paths, a.Path+"\x00"+a.SHA256)
	}
	h.Write([]byte(strings.Join(paths, "\n")))
	return hex.EncodeToString(h.Sum(nil)[:16])
}
