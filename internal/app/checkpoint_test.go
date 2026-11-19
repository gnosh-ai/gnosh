package app

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

// A session keeps the copies of its checkpointPrompts most recent prompts:
// older ones are let go, noted as such, and still noted when the session
// is opened again, so that a rewind of the files to before one of them is
// refused instead of putting back too little.
func TestCheckpointsLetGoOfOldPrompts(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "work", "f.txt")
	if err := os.MkdirAll(filepath.Dir(file), 0o755); err != nil {
		t.Fatal(err)
	}
	clock := time.Date(2026, 11, 18, 0, 0, 0, 0, time.UTC)
	now := func() time.Time { clock = clock.Add(time.Second); return clock }
	store := filepath.Join(dir, "checkpoints")
	c := openCheckpoints(store, now)
	prompt := func(i int) string { return fmt.Sprintf("prompt-%03d", i) }
	for i := 1; i <= checkpointPrompts+2; i++ {
		if err := os.WriteFile(file, []byte(fmt.Sprintf("v%d\n", i)), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := c.keep(prompt(i), file); err != nil {
			t.Fatalf("keep %d: %v", i, err)
		}
	}
	if !c.gone([]string{prompt(1)}) || !c.gone([]string{prompt(2)}) || c.gone([]string{prompt(3), prompt(checkpointPrompts + 2)}) {
		t.Errorf("let go of: 1 %v, 2 %v, 3 %v", c.gone([]string{prompt(1)}), c.gone([]string{prompt(2)}), c.gone([]string{prompt(3)}))
	}
	blobs, _ := filepath.Glob(filepath.Join(store, "*-*"))
	if len(blobs) != checkpointPrompts {
		t.Errorf("%d copies are kept, want %d", len(blobs), checkpointPrompts)
	}
	// Opened again, the store still knows what it let go, and what it kept.
	again := openCheckpoints(store, now)
	if !again.gone([]string{prompt(2)}) || again.gone([]string{prompt(3)}) {
		t.Error("a store opened again forgot which prompts' copies were let go")
	}
	if got := len(again.since(map[string]bool{prompt(3): true})); got != 1 {
		t.Errorf("a kept prompt's copy is not found after the store is opened again (%d)", got)
	}
}

// A file with another name (a hard link, as pnpm makes them) is left as
// it is: putting it back would cut it off from its other name.
func TestRestoreLeavesHardLinkedFiles(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("hard links are told apart on unix")
	}
	dir := t.TempDir()
	file := filepath.Join(dir, "f.txt")
	if err := os.WriteFile(file, []byte("before\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	c := openCheckpoints(filepath.Join(dir, "checkpoints"), time.Now)
	if err := c.keep("p1", file); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(file, []byte("after\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Link(file, filepath.Join(dir, "other-name.txt")); err != nil {
		t.Skip("no hard links here: ", err)
	}
	done, failed := c.restore(c.since(map[string]bool{"p1": true}), nil)
	if len(done) != 0 || len(failed) != 1 || !strings.Contains(failed[0], "hard-linked") {
		t.Errorf("done %q, failed %q", done, failed)
	}
	if b, _ := os.ReadFile(file); string(b) != "after\n" {
		t.Errorf("the hard-linked file holds %q", b)
	}
}

// A copy that cannot be made is noted: a later change of the same file
// under the same prompt is not taken for what it held before the first,
// and a rewind says the file cannot be put back.
func TestFailedCopyIsNoted(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "f.txt")
	if err := os.WriteFile(file, []byte("v0\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	// The store's folder cannot be made: a file stands where it would.
	store := filepath.Join(dir, "checkpoints")
	if err := os.WriteFile(store, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	c := openCheckpoints(store, time.Now)
	if err := c.keep("p1", file); err == nil {
		t.Fatal("a copy that could not be written was taken for made")
	}
	if err := os.WriteFile(file, []byte("v1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	_ = c.keep("p1", file)
	_, failed := c.restore(c.since(map[string]bool{"p1": true}), nil)
	if len(failed) != 1 || !strings.Contains(failed[0], "could not be kept") {
		t.Errorf("failed %q", failed)
	}
	if b, _ := os.ReadFile(file); string(b) != "v1\n" {
		t.Errorf("the file holds %q", b)
	}
}
