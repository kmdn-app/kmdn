//go:build !unix

package doctor

func freeBytes(string) (uint64, bool) { return 0, false }
