package blobs

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestFS(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	f := FS{Root: root}
	sha := "ab" + "cdef0123456789abcdef0123456789abcdef0123456789abcdef0123456789"
	if Key(DefaultOrg, sha) != "ab/"+sha || Key("org_x", sha) != "org_x/ab/"+sha {
		t.Fatalf("keys: %s %s", Key(DefaultOrg, sha), Key("org_x", sha))
	}
	if err := f.Put(ctx, Key("org_x", sha), []byte("png")); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(root, "org_x", "ab", sha)); err != nil {
		t.Fatal(err)
	}
	if b, err := f.Get(ctx, Key("org_x", sha)); err != nil || string(b) != "png" {
		t.Fatalf("get: %q %v", b, err)
	}
	if _, err := f.Get(ctx, Key(DefaultOrg, sha)); !errors.Is(err, ErrNotFound) {
		t.Fatalf("another org's key: %v", err)
	}
	if n, err := f.Stat(ctx, Key("org_x", sha)); err != nil || n != 3 {
		t.Fatalf("stat: %d %v", n, err)
	}
	for _, bad := range []string{"", "../x", "/etc/passwd", "a/../../x"} {
		if err := f.Put(ctx, bad, nil); err == nil {
			t.Errorf("key %q accepted", bad)
		}
	}
	if err := f.Delete(ctx, Key("org_x", sha)); err != nil {
		t.Fatal(err)
	}
	if _, err := f.Stat(ctx, Key("org_x", sha)); !errors.Is(err, ErrNotFound) {
		t.Fatalf("after delete: %v", err)
	}
}
