package app

import (
	"os"
	"path/filepath"
	"testing"
)

// A link that leads nowhere is read from the folder it is in as the
// system reads it, past the links on the way to that folder: here d leads
// to s, so d/dang's "../.." starts at s, not at the text's a/b/d (the
// security review of 2026-11-18 found the text's reading).
func TestResolveADanglingLinkThroughALinkedFolder(t *testing.T) {
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	for _, d := range []string{"proj/a/b", "proj/s", "outside"} {
		if err := os.MkdirAll(filepath.Join(root, d), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Symlink("../../s", filepath.Join(root, "proj/a/b/d")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("../../outside/x", filepath.Join(root, "proj/s/dang")); err != nil {
		t.Fatal(err)
	}
	got, err := resolvePath(filepath.Join(root, "proj/a/b/d/dang"))
	if err != nil {
		t.Fatal(err)
	}
	if want := filepath.Join(root, "outside/x"); got != want {
		t.Errorf("resolved to %s, want %s", got, want)
	}
}
