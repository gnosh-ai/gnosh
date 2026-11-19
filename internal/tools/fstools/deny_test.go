package fstools

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gnosh-ai/gnosh/internal/core"
)

// Grep and Glob leave out of a folder they walk what a Read deny rule
// covers: its content is not searched and its name is not listed (the
// security review of 2026-11-18 found both read it).
func TestSearchLeavesOutWhatReadIsDenied(t *testing.T) {
	f := newFixture(t, Options{ReadDenied: func(p string) bool { return strings.HasSuffix(p, string(filepath.Separator)+"c.go") }})
	grepTree(f)
	a, c := f.path("a.txt"), f.path("sub/c.go")
	if got := f.ok(f.grep, in{"pattern": "alpha"}); got != a+"\n" {
		t.Errorf("Grep found %q", got)
	}
	if got := f.ok(f.grep, in{"pattern": "lpha", "output_mode": "content", "path": f.path("sub")}); strings.Contains(got, "Alpha") {
		t.Errorf("Grep showed a denied file's content: %q", got)
	}
	if got := f.ok(f.glob, in{"pattern": "**/*"}); strings.Contains(got, c) || !strings.Contains(got, a) {
		t.Errorf("Glob listed %q", got)
	}
}

// A Write runs on the file its call was looked at for: when a folder on
// the way became a link between the question and the run, nothing is
// written.
func TestWriteHeldToWhatWasAskedAbout(t *testing.T) {
	f := newFixture(t, Options{})
	out := f.path("out")
	elsewhere := f.path("elsewhere")
	for _, d := range []string{out, elsewhere} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	c := call(in{"file_path": filepath.Join(out, "notes.md"), "content": "x\n"})
	if _, err := f.write.(core.Describer).Describe(c, f.env); err != nil {
		t.Fatalf("Describe: %v", err)
	}
	// Between the question and the run, out becomes a link elsewhere.
	if err := os.Remove(out); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(elsewhere, out); err != nil {
		t.Fatal(err)
	}
	res, err := f.write.Run(context.Background(), c, f.env)
	if err != nil {
		t.Fatal(err)
	}
	if !res.IsError || !strings.Contains(core.TextOf(res.Content), "leads somewhere else than when the call was looked at") {
		t.Errorf("the write ran: %q", core.TextOf(res.Content))
	}
	if _, err := os.Stat(filepath.Join(elsewhere, "notes.md")); err == nil {
		t.Error("the file was written where nobody was asked about")
	}
	// A call looked at as it stands runs.
	c2 := call(in{"file_path": filepath.Join(elsewhere, "ok.md"), "content": "y\n"})
	c2.ID = "call-2"
	if _, err := f.write.(core.Describer).Describe(c2, f.env); err != nil {
		t.Fatal(err)
	}
	if res, err := f.write.Run(context.Background(), c2, f.env); err != nil || res.IsError {
		t.Errorf("a plain write failed: %v %q", err, core.TextOf(res.Content))
	}
}
