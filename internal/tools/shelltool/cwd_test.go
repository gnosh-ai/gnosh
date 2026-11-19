package shelltool

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/gnosh-ai/gnosh/internal/core"
)

// project makes a session directory with a subdirectory in it, and a
// directory beside it that is not the session's.
func project(t *testing.T) (env *core.ToolEnv, sub, outside string) {
	t.Helper()
	env = newEnv(t)
	sub = filepath.Join(env.Cwd, "sub", "deeper")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	return env, sub, t.TempDir()
}

func TestWorkingDirectory(t *testing.T) {
	t.Parallel()
	t.Run("default shell", func(t *testing.T) { workingDirectory(t, "") })
	t.Run("sh", func(t *testing.T) { workingDirectory(t, "/bin/sh") })
}

// workingDirectory checks, with the given shell, where the session ends up
// after commands that change directory in every way there is.
func workingDirectory(t *testing.T, shell string) {
	t.Parallel()
	m := newManager(t, Options{Shell: shell})
	env, sub, outside := project(t)
	if err := os.Symlink(outside, filepath.Join(env.Cwd, "way-out")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(sub, filepath.Join(env.Cwd, "shortcut")); err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name    string
		command string
		cwd     string // the directory the session should move to; empty to stay
		note    string // a note the result must carry; empty for none about directories
		code    int
		bash    bool // the command is written for bash
	}{
		{name: "no cd", command: "echo hi"},
		{name: "cd into the project", command: "cd sub/deeper", cwd: sub, note: "working directory is now " + sub},
		{name: "cd and back", command: "cd sub && cd .."},
		{name: "cd then exit 3", command: "cd sub/deeper; exit 3", cwd: sub, note: "working directory is now " + sub, code: 3},
		{name: "cd then a failing command", command: "cd sub/deeper; false", cwd: sub, note: "working directory is now", code: 1},
		{name: "cd then errexit", command: "set -e; cd sub/deeper; false; echo never", cwd: sub, note: "working directory is now", code: 1},
		{name: "cd then a syntax error", command: "cd sub/deeper\nif then", cwd: sub, note: "working directory is now", code: 2},
		{name: "cd in a subshell", command: "(cd sub/deeper; pwd >/dev/null)"},
		{name: "cd in a pipeline", command: "cd sub/deeper | cat"},
		{name: "cd in a child shell", command: "sh -c 'cd sub/deeper'"},
		{name: "cd in a function", command: "go() { cd sub; }; go; cd deeper", cwd: sub, note: "working directory is now"},
		{name: "pushd", command: "pushd sub >/dev/null", cwd: filepath.Dir(sub), note: "working directory is now", bash: true},
		{name: "cd that fails", command: "cd no/such/dir 2>/dev/null; true"},
		{name: "cd outside", command: "cd " + outside, note: "working directory reset to"},
		{name: "cd to the root", command: "cd /", note: "working directory reset to"},
		{name: "cd above the project", command: "cd ..", note: "working directory reset to"},
		{name: "cd outside then exit 3", command: "cd /; exit 3", note: "working directory reset to", code: 3},
		{name: "cd out through a link", command: "cd way-out", note: "working directory reset to"},
		{name: "cd in through a link", command: "cd shortcut", cwd: sub, note: "working directory is now " + sub},
		{name: "exec replaces the shell", command: "cd sub; exec true", note: "did not say where it ended"},
		{name: "a trap of its own", command: "trap 'true' EXIT; cd sub", note: "did not say where it ended"},
		{name: "pwd redefined", command: "pwd() { echo /lies; }; cd sub/deeper", cwd: sub, note: "working directory is now"},
		// The file the shell reports in can be written by the command. What
		// is in it is checked: a link is followed, and only a directory that
		// is there is taken.
		{name: "a forged report of a link out", command: `trap - EXIT; echo "$PWD/way-out" > "$__gnosh_cwd"`, note: "working directory reset to"},
		{name: "a forged report of nowhere", command: `trap - EXIT; echo "$PWD/no/such/place" > "$__gnosh_cwd"`, note: "did not say where it ended"},
		{name: "a forged report of a file", command: `trap - EXIT; echo x > f.txt; echo "$PWD/f.txt" > "$__gnosh_cwd"`, note: "did not say where it ended"},
		{name: "a forged report that is not a path", command: `trap - EXIT; echo "sub" > "$__gnosh_cwd"`, note: "did not say where it ended"},
	}
	for _, tt := range tests {
		if tt.bash && filepath.Base(m.shell) != "bash" {
			continue
		}
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			res := bash(t, m, env, tt.command)
			if res.Cwd != tt.cwd {
				t.Errorf("Cwd = %q, want %q", res.Cwd, tt.cwd)
			}
			if code := exitCode(t, res); code != tt.code {
				t.Errorf("exit code = %d, want %d", code, tt.code)
			}
			_, _, notes := parts(res)
			about := ""
			for _, note := range notes {
				if strings.Contains(note, "directory") {
					about = note
				}
			}
			switch {
			case tt.note == "" && about != "":
				t.Errorf("unexpected note %q", about)
			case !strings.Contains(about, tt.note):
				t.Errorf("note = %q, want one with %q", about, tt.note)
			}
			if strings.Contains(tt.note, "reset") && !strings.Contains(about, env.Cwd) {
				t.Errorf("note %q does not say where the session still is, %s", about, env.Cwd)
			}
		})
	}
}

func TestWorkingDirectoryCarriesOver(t *testing.T) {
	t.Parallel()
	m := newManager(t, Options{})
	env, sub, _ := project(t)

	res := bash(t, m, env, "cd sub")
	if want := filepath.Dir(sub); res.Cwd != want {
		t.Fatalf("Cwd = %q, want %q", res.Cwd, want)
	}
	// The session moves, as the agent loop would move it.
	env.Cwd = res.Cwd
	res = bash(t, m, env, "pwd; cd deeper")
	if got, want := printed(res), env.Cwd; got != want {
		t.Errorf("the next command ran in %q, want %q", got, want)
	}
	env.Cwd = res.Cwd
	res = bash(t, m, env, "pwd; cd ../..; pwd")
	if got, want := printed(res), sub+"\n"+env.Roots[0]; got != want {
		t.Errorf("output = %q, want %q", got, want)
	}
	if res.Cwd != env.Roots[0] {
		t.Errorf("Cwd = %q, want the root %q", res.Cwd, env.Roots[0])
	}
}

func TestWorkingDirectoryThroughALinkedRoot(t *testing.T) {
	t.Parallel()
	m := newManager(t, Options{})
	// The session knows its project by a path that goes through a link,
	// as every temporary directory does on macOS. The shell reports where
	// it is by the real path; the session must get its own spelling back.
	realRoot := t.TempDir()
	if err := os.Mkdir(filepath.Join(realRoot, "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	linked := filepath.Join(t.TempDir(), "project")
	if err := os.Symlink(realRoot, linked); err != nil {
		t.Fatal(err)
	}
	env := newEnv(t)
	env.Cwd, env.Roots = linked, []string{linked}

	res := bash(t, m, env, "pwd; cd sub")
	if got := printed(res); got != linked {
		t.Errorf("the command ran in %q, want the session's spelling %q", got, linked)
	}
	if want := filepath.Join(linked, "sub"); res.Cwd != want {
		t.Errorf("Cwd = %q, want %q", res.Cwd, want)
	}
	res = bash(t, m, env, "cd .")
	if _, _, notes := parts(res); res.Cwd != "" || len(notes) != 0 {
		t.Errorf("staying put gave Cwd %q and notes %q", res.Cwd, notes)
	}
}

func TestWorkingDirectoryRoots(t *testing.T) {
	t.Parallel()
	m := newManager(t, Options{})
	env, sub, outside := project(t)

	// A second root: the session may go there too.
	env.Roots = append(env.Roots, outside)
	if res := bash(t, m, env, "cd "+outside); res.Cwd != outside {
		t.Errorf("Cwd = %q, want the added root %q", res.Cwd, outside)
	}

	// The innermost root that holds the directory gives it its spelling.
	env.Roots = []string{env.Cwd, filepath.Dir(sub)}
	if res := bash(t, m, env, "cd sub/deeper"); res.Cwd != sub {
		t.Errorf("Cwd = %q, want %q", res.Cwd, sub)
	}

	// With no roots given, the directory the command started in is the
	// boundary.
	env.Roots = nil
	if res := bash(t, m, env, "cd sub"); res.Cwd != filepath.Dir(sub) {
		t.Errorf("Cwd = %q, want %q", res.Cwd, filepath.Dir(sub))
	}
	if res := bash(t, m, env, "cd .."); res.Cwd != "" || !hasNote(res, "working directory reset to "+env.Cwd) {
		_, _, notes := parts(res)
		t.Errorf("leaving the starting directory with no roots: Cwd %q, notes %q", res.Cwd, notes)
	}
}

func TestStoppedCommandDoesNotMoveTheSession(t *testing.T) {
	t.Parallel()
	m := newManager(t, Options{})
	env, _, _ := project(t)
	res := run(t, t.Context(), m, BashName, env, args{argCommand: "cd sub; echo ready; sleep 300", argTimeout: 2000})
	if res.Cwd != "" {
		t.Errorf("Cwd = %q after a timeout, want none", res.Cwd)
	}
	// A recent bash says where it was even when it is terminated; other
	// shells may not, and then there is nothing to say. Nor is there when
	// the timeout came before the cd ran (a box under load can take that
	// long to start the shell): "ready" says it ran.
	_, body, notes := parts(res)
	if runtime.GOOS == "linux" && filepath.Base(m.shell) == "bash" && strings.Contains(body, "ready") && !hasNote(res, "its change of directory is not kept") {
		t.Errorf("notes = %q, want word that the session did not move", notes)
	}
}

func TestPlace(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	physicalRoot := physical(root)
	env := &core.ToolEnv{Cwd: root, Roots: []string{root}}
	tests := []struct {
		final   string
		to      string
		outside string
	}{
		{final: ""},
		{final: physicalRoot},
		{final: filepath.Join(physicalRoot, "a", "b"), to: filepath.Join(root, "a", "b")},
		{final: filepath.Dir(physicalRoot), outside: filepath.Dir(physicalRoot)},
		{final: physicalRoot + "-sibling", outside: physicalRoot + "-sibling"},
		{final: "/", outside: "/"},
	}
	for _, tt := range tests {
		got := place(tt.final, env)
		if got.to != tt.to || got.outside != tt.outside {
			t.Errorf("place(%q) = %+v, want to %q, outside %q", tt.final, got, tt.to, tt.outside)
		}
	}
	// The root of the file system as a root holds everything.
	everything := &core.ToolEnv{Cwd: root, Roots: []string{"/"}}
	if got := place("/usr", everything); got.to != "/usr" {
		t.Errorf("under the root /, place(/usr) = %+v", got)
	}
	// A session with a work tree of its own is followed into its tree,
	// and not into the checkouts it was made from, roots though they are.
	tree := filepath.Join(root, ".gnosh", "worktrees", "a")
	inner := filepath.Join(tree, ".gnosh", "worktrees", "b")
	for _, d := range []string{tree, inner} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	treed := &core.ToolEnv{Cwd: inner, Roots: []string{root, inner}, Tree: &core.WorkTree{Path: inner, Repo: tree, Outer: []string{root}}}
	if got := place(filepath.Join(physical(inner), "x"), treed); got.to != filepath.Join(inner, "x") {
		t.Errorf("in its work tree, place = %+v", got)
	}
	for _, final := range []string{physicalRoot, filepath.Join(physicalRoot, "src"), physical(tree)} {
		if got := place(final, treed); got.to != "" || got.outside != final || !got.checkout {
			t.Errorf("in a checkout its work tree was made from, place(%q) = %+v", final, got)
		}
	}
}

// A working directory that was taken away (a checkout without the folder,
// a clean) no longer keeps every command from running: the command runs in
// the nearest folder above it that is there, inside the session's, and the
// session moves there; a command that takes its own working directory away
// moves the session up as well. (The shell tool review of 2026-11-19:
// nothing could run again, not even the person's own cd.)
func TestWorkingDirectoryTakenAway(t *testing.T) {
	t.Parallel()
	m := newManager(t, Options{})
	env, sub, _ := project(t)
	root := env.Roots[0]
	env.Cwd = sub
	if err := os.RemoveAll(filepath.Dir(sub)); err != nil {
		t.Fatal(err)
	}
	res := bash(t, m, env, "pwd")
	if _, body, notes := parts(res); res.Cwd != root || !strings.Contains(body, root) || !hasNote(res, "was not there any more, so the command ran in "+root) {
		t.Errorf("a command in a folder taken away: Cwd %q, output %q, notes %q", res.Cwd, body, notes)
	}

	gone := filepath.Join(root, "gone")
	if err := os.MkdirAll(gone, 0o755); err != nil {
		t.Fatal(err)
	}
	env.Cwd = gone
	res = bash(t, m, env, `rm -rf "$PWD"`)
	if _, _, notes := parts(res); res.Cwd != root || !hasNote(res, "the command took away its working directory "+gone+", so the working directory is now "+root) {
		t.Errorf("a command that took its own folder away: Cwd %q, notes %q", res.Cwd, notes)
	}
}

// The file in which the shell says where it ended is read with care: one
// a command put a pipe in place of (nobody writes to it) does not hold the
// result up, and one it made a link to an endless device does not fill
// memory. (The shell tool review of 2026-11-19.)
func TestWhereItEndedIsAPlainFile(t *testing.T) {
	t.Parallel()
	m := newManager(t, Options{})
	env := newEnv(t)
	start := time.Now()
	res := run(t, t.Context(), m, BashName, env, args{argCommand: `rm -f "$__gnosh_cwd"; mkfifo "$__gnosh_cwd"`, argTimeout: 2000})
	if took := time.Since(start); res.Cwd != "" || took > 15*time.Second {
		t.Errorf("a pipe in place of the file: Cwd %q after %v", res.Cwd, took)
	}
	start = time.Now()
	res = bash(t, m, env, `ln -sf /dev/zero "$__gnosh_cwd"`)
	if took := time.Since(start); res.Cwd != "" || took > 15*time.Second {
		t.Errorf("a link to /dev/zero in place of the file: Cwd %q after %v", res.Cwd, took)
	}
}
