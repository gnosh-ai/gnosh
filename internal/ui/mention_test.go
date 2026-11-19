package ui

import (
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"testing"

	"github.com/gnosh-ai/gnosh/internal/text"
	"github.com/gnosh-ai/gnosh/internal/ui/editor"
)

// After a mention's @, the project's files and folders whose paths hold
// what was typed are offered after the folder's own names, best first:
// names that begin with it, then names and paths that hold it, then paths
// that hold its letters in order. Hidden paths and names that cannot be
// typed as one word are not offered; Tab takes the one chosen.
func TestFuzzyMention(t *testing.T) {
	g := newRig(t, 100, 24, text.NoColor)
	g.files["/home/jae/src/proj"] = []fileEntry{{name: "internal", dir: true}, {name: "main.go"}}
	g.s.listProject = func(string, bool) []string {
		return []string{"main.go", "internal/", "internal/ui/", "internal/app/", "internal/ui/focus.go", "internal/ui/focus_test.go",
			"internal/app/frontend.go", ".github/focus.yml", "docs/a focus.md", "internal/ui/refocus.go"}
	}
	g.keys(`"look at @focus"`)
	g.liveHas("internal/ui/focus.go", "internal/ui/focus_test.go", "internal/ui/refocus.go").liveLacks(".github", "a focus")
	g.keys(`<Tab>`)
	if g.box() != "look at @internal/ui/focus.go" {
		t.Fatalf("the box holds %q", g.box())
	}
	// Its letters in order, anywhere in the path.
	g.keys(`<Ctrl+U> "@uifoc"`)
	g.liveHas("internal/ui/focus.go")
	// The folder's own names come first, and are not offered twice.
	g.keys(`<Ctrl+U> "@ma"`)
	g.liveHas("main.go")
	if got := g.s.complete("@ma", 3); len(got) == 0 || got[0].Text != "@main.go" || slices.IndexFunc(got[1:], func(c editor.Candidate) bool { return c.Text == "@main.go" }) >= 0 {
		t.Errorf("for @ma: %v", got)
	}
}

// The project is listed by git in a repository, without what it ignores,
// and by a walk by the ignore rules elsewhere; both give the folders too.
// With the respectGitignore option off, both list what is ignored too.
func TestListProject(t *testing.T) {
	dir := t.TempDir()
	for name, body := range map[string]string{
		"a/b/c.go": "x", "a/d.txt": "x", "build/out.bin": "x", ".hidden/x": "x", "top.md": "x", ".gitignore": "build/\n",
	} {
		p := filepath.Join(dir, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	check := func(how string, got []string, not ...string) {
		t.Helper()
		for _, want := range []string{"a/b/c.go", "a/d.txt", "top.md", "a/", "a/b/"} {
			if !slices.Contains(got, want) {
				t.Errorf("%s did not list %s: %v", how, want, got)
			}
		}
		for _, n := range not {
			if slices.Contains(got, n) {
				t.Errorf("%s listed %s: %v", how, n, got)
			}
		}
	}
	check("the walk", listProject(dir, false), "build/out.bin", "build/", ".hidden/x")
	// With the respectGitignore option off, what is ignored is listed too
	// (but not the dot folders).
	check("the walk of all", listProject(dir, true), ".hidden/x")
	if all := listProject(dir, true); !slices.Contains(all, "build/out.bin") {
		t.Errorf("the walk of all left out build/out.bin: %v", all)
	}
	if _, err := exec.LookPath("git"); err != nil {
		return
	}
	if out, err := exec.Command("git", "-C", dir, "init", "-q").CombinedOutput(); err != nil {
		t.Fatalf("git init: %v %s", err, out)
	}
	check("git", listProject(dir, false), "build/out.bin", "build/")
	if all := listProject(dir, true); !slices.Contains(all, "build/out.bin") || !slices.Contains(all, "build/") {
		t.Errorf("git's list of all left out build/out.bin: %v", all)
	}
}
