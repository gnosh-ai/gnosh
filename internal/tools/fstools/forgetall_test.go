package fstools

import (
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/gnosh-ai/gnosh/internal/core"
)

// After a summary every file is forgotten: the model no longer sees any
// copy of one, and Edit and Write wait for a fresh Read.
func TestForgetAll(t *testing.T) {
	dir := t.TempDir()
	env := &core.ToolEnv{Cwd: dir, Store: new(sync.Map)}
	var paths []string
	for _, name := range []string{"a.go", "b.go"} {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, []byte("x\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := MarkRead(env, p); err != nil {
			t.Fatal(err)
		}
		paths = append(paths, p)
	}
	if got := ForgetAll(env); len(got) != 2 {
		t.Fatalf("ForgetAll dropped %q", got)
	}
	if got := Forget(env, paths...); got != nil {
		t.Errorf("after ForgetAll the session still knew %q", got)
	}
}
