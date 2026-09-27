//go:build unix

package doctor

import "syscall"

// freeBytes is the space left for unprivileged writes in dir.
func freeBytes(dir string) (uint64, bool) {
	var st syscall.Statfs_t
	if err := syscall.Statfs(dir, &st); err != nil {
		return 0, false
	}
	return st.Bavail * uint64(st.Bsize), true //nolint:unconvert // Bsize is int64 on linux, uint32 on darwin
}
