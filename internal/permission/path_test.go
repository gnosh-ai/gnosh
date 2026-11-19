package permission

import (
	"path"
	"slices"
	"strings"
	"testing"
)

// pathCase holds one path against one rule in one list.
type pathCase struct {
	list List
	rule string
	path string
	want bool
}

func TestPathPatterns(t *testing.T) {
	// The engine's working directory is /proj, the home /home/u, and the
	// settings file lives in /home/u/.config/gnosh.
	tests := []pathCase{
		// The four anchors.
		{ListDeny, "Read(//etc/secrets/**)", "/etc/secrets/a/b", true},
		{ListDeny, "Read(//etc/secrets/**)", "/proj/etc/secrets/a", false},
		{ListDeny, "Read(~/Documents/*.pdf)", "/home/u/Documents/a.pdf", true},
		{ListDeny, "Read(~/Documents/*.pdf)", "/home/u/Documents/sub/a.pdf", false},
		{ListDeny, "Read(~/Documents/*.pdf)", "/proj/Documents/a.pdf", false},
		{ListDeny, "Read(/secrets/**)", "/home/u/.config/gnosh/secrets/k", true}, // the settings file's directory
		{ListDeny, "Read(/secrets/**)", "/proj/secrets/k", false},
		{ListDeny, "Read(/secrets/**)", "/secrets/k", false},
		{ListDeny, "Read(./notes.txt)", "/proj/notes.txt", true},
		{ListDeny, "Read(./notes.txt)", "/proj/sub/notes.txt", false},

		// * stays in a segment, ** crosses them, ? is one character.
		{ListAllow, "Edit(//proj/src/*.ts)", "/proj/src/a.ts", true},
		{ListAllow, "Edit(//proj/src/*.ts)", "/proj/src/sub/a.ts", false},
		{ListAllow, "Edit(//proj/src/**/*.ts)", "/proj/src/a.ts", true},
		{ListAllow, "Edit(//proj/src/**/*.ts)", "/proj/src/x/y/a.ts", true},
		{ListAllow, "Edit(//proj/src/**/*.ts)", "/proj/src/a.js", false},
		{ListAllow, "Edit(//proj/**)", "/proj/a/b/c", true},
		{ListAllow, "Edit(//proj/**)", "/projx/a", false},
		{ListAllow, "Edit(//proj/a?c)", "/proj/abc", true},
		{ListAllow, "Edit(//proj/a?c)", "/proj/a/c", false},
		{ListAllow, "Edit(//proj/[ab].go)", "/proj/a.go", true},
		{ListAllow, "Edit(//proj/[ab].go)", "/proj/c.go", false},
		{ListAllow, "Edit(//proj/[!ab].go)", "/proj/c.go", true},
		{ListAllow, "Edit(//proj/[a-c]x)", "/proj/bx", true},
		{ListAllow, `Edit(//proj/a\*b)`, "/proj/a*b", true},
		{ListAllow, `Edit(//proj/a\*b)`, "/proj/axb", false},
		{ListAllow, "Edit(//proj/a[b)", "/proj/a[b", true}, // an unclosed [ is itself

		// Depth: a bare name matches at any depth below the working
		// directory, and nowhere else.
		{ListDeny, "Read(.env)", "/proj/.env", true},
		{ListDeny, "Read(.env)", "/proj/a/b/.env", true},
		{ListDeny, "Read(.env)", "/other/.env", false},
		{ListDeny, "Read(*.env)", "/proj/x/prod.env", true},
		{ListAllow, "Read(*.md)", "/proj/docs/a.md", true},
		{ListAllow, "Read(*.md)", "/home/u/a.md", false},
		// One directory segment: an allow rule is that directory in the
		// working directory; a deny or ask rule is one at any depth.
		{ListAllow, "Edit(src/**)", "/proj/src/a/b.go", true},
		{ListAllow, "Edit(src/**)", "/proj/pkg/src/a.go", false},
		{ListDeny, "Edit(src/**)", "/proj/src/a.go", true},
		{ListDeny, "Edit(src/**)", "/proj/pkg/src/a.go", true},
		{ListAsk, "Edit(src/**)", "/proj/pkg/src/a.go", true},
		{ListDeny, "Edit(src/**)", "/other/src/a.go", false},
		// Every other shape is the same in every list.
		{ListAllow, "Edit(src/components/**)", "/proj/src/components/a.tsx", true},
		{ListDeny, "Edit(src/components/**)", "/proj/x/src/components/a.tsx", false},
		{ListAllow, "Edit(**/src/**)", "/proj/x/y/src/a.go", true},
		{ListAllow, "Edit(**/src/**)", "/proj/src/a.go", true},

		// A deny or ask rule that names a directory covers what is under
		// it; an allow rule must match the path itself.
		{ListDeny, "Read(//etc)", "/etc/passwd", true},
		{ListDeny, "Read(secrets)", "/proj/a/secrets/k.pem", true},
		{ListAllow, "Read(//opt/data)", "/opt/data/x", false},
		{ListAllow, "Read(//opt/data)", "/opt/data", true},
		{ListAllow, "Read(//opt/data/)", "/opt/data/x/y", true}, // a trailing slash means the directory's contents
		{ListAllow, "Read(//opt/data/**)", "/opt/data", true},

		// Deny and ask rules ignore letter case; allow rules do not.
		{ListDeny, "Read(.env)", "/proj/.ENV", true},
		{ListDeny, "Read(//etc/Secrets/**)", "/ETC/secrets/x", true},
		{ListAllow, "Edit(//proj/README.md)", "/proj/readme.md", false},
	}
	for _, tt := range tests {
		o := testOptions(ModeDefault, rules{})
		switch tt.list {
		case ListAllow:
			o.Layers[0].Allow = []string{tt.rule}
		case ListAsk:
			o.Layers[0].Ask = []string{tt.rule}
		case ListDeny:
			o.Layers[0].Deny = []string{tt.rule}
		}
		e := mustEngine(t, o)
		var c *crule
		for _, list := range []List{ListDeny, ListAsk, ListAllow} {
			if rs := e.rules(list); len(rs) > 0 {
				c = rs[0]
			}
		}
		if got := c.path.match(tt.path); got != tt.want {
			t.Errorf("%s %s on %s: got %v, want %v (segments %q)", tt.list, tt.rule, tt.path, got, tt.want, c.path.segs)
		}
	}
}

func TestWorkingDirectoryNamesAreNotPatterns(t *testing.T) {
	o := testOptions(ModeDefault, rules{deny: []string{"Read(.env)"}})
	o.Cwd = "/work/my[proj]*"
	e := mustEngine(t, o)
	if d := file(e, "Read", "/work/my[proj]*/.env"); d.Behavior != Deny {
		t.Errorf("a working directory with pattern characters in its name: %+v", d)
	}
	if d := file(e, "Read", "/work/myp/.env"); d.Behavior == Deny {
		t.Errorf("the working directory's name was read as a pattern: %+v", d)
	}
}

func TestEscapePath(t *testing.T) {
	for _, p := range []string{"/proj/a*b", "/proj/[x]/y?", `/proj/back\slash`, "/proj/!bang", "/proj/plain.go"} {
		e := engine(t, ModeDefault, rules{allow: []string{"Edit(/" + EscapePath(p) + ")"}})
		if d := file(e, "Edit", p); d.Behavior != Allow {
			t.Errorf("escaped rule for %q does not match it: %+v", p, d)
		}
	}
	e := engine(t, ModeDefault, rules{allow: []string{"Edit(/" + EscapePath("/proj/a*b") + ")"}})
	if d := file(e, "Edit", "/proj/axxb"); d.Behavior != Ask {
		t.Errorf("an escaped rule matched another path: %+v", d)
	}
}

func TestReadAndEditRulesAcrossTools(t *testing.T) {
	e := engine(t, ModeAcceptEdits, rules{
		deny:  []string{"Read(.env)", "Edit(//proj/gen/**)"},
		allow: []string{"Edit(//opt/out/**)", "Read(//opt/data/**)"},
	})
	for _, tt := range []struct {
		tool, path string
		want       Behavior
	}{
		// Read(path) covers Read, Grep and Glob; a Read deny also stops
		// Edit and Write, even for a file that does not exist yet, but
		// not NotebookEdit.
		{"Read", "/proj/.env", Deny},
		{"Grep", "/proj/sub/.env", Deny},
		{"Glob", "/proj/.env", Deny},
		{"Edit", "/proj/.env", Deny},
		{"Write", "/proj/new/.env", Deny},
		{"NotebookEdit", "/proj/.env", Allow},
		// Edit(path) covers every tool that edits, and does not stop
		// reading.
		{"Edit", "/proj/gen/a.go", Deny},
		{"Write", "/proj/gen/a.go", Deny},
		{"NotebookEdit", "/proj/gen/a.ipynb", Deny},
		{"Read", "/proj/gen/a.go", Allow},
		// An Edit allow also grants reading the same path; a Read allow
		// does not grant editing.
		{"Write", "/opt/out/a", Allow},
		{"Edit", "/opt/out/a", Allow},
		{"NotebookEdit", "/opt/out/a.ipynb", Allow},
		{"Read", "/opt/out/a", Allow},
		{"Grep", "/opt/out", Allow},
		{"Read", "/opt/data/a", Allow},
		{"Edit", "/opt/data/a", Ask},
		// Nothing says anything about these.
		{"Read", "/opt/other", Ask},
		{"Edit", "/proj/a.go", Allow}, // acceptEdits, inside the working directory
		{"Edit", "/opt/other", Ask},
	} {
		if d := file(e, tt.tool, tt.path); d.Behavior != tt.want {
			t.Errorf("%s %s: got %s (%s), want %s", tt.tool, tt.path, d.Behavior, d.Reason, tt.want)
		}
	}
}

func TestNegation(t *testing.T) {
	o := testOptions(ModeBypass, rules{})
	o.Layers = []Layer{
		{Scope: ScopeUser, Name: "user", Base: "/home/u/.config/gnosh",
			Deny: []string{"Read(*.pem)", "Read(!public.pem)", "Read(private/**)", "Read(!private/readme.md)", "Read(vault)", "Read(!vault/ok.txt)", "Read(//etc/**)", "Read(!hosts)"}},
		{Scope: ScopeProject, Name: "project",
			Deny: []string{"Read(*.key)", "Read(!*.pem)"}},
	}
	e := mustEngine(t, o)
	for p, want := range map[string]Behavior{
		"/proj/server.pem":        Deny,
		"/proj/public.pem":        Allow, // reopened by the "!" after it
		"/proj/a/public.pem":      Allow,
		"/proj/private/x":         Deny,
		"/proj/private/readme.md": Allow,
		"/proj/vault/ok.txt":      Deny, // inside a directory shut as a whole
		"/etc/hosts":              Deny, // "!" only reopens working-directory rules
		"/proj/a.key":             Deny,
		"/proj/other.pem":         Deny, // a "!" in another source changes nothing here
	} {
		if d := file(e, "Read", p); d.Behavior != want {
			t.Errorf("%s: got %s (%s), want %s", p, d.Behavior, d.Reason, want)
		}
	}
}

func TestSymlinks(t *testing.T) {
	fs := fakeFS{links: map[string]string{
		"/proj/notes.txt": "/home/u/.ssh/id_ed25519",
		"/proj/out":       "/opt/out",
		"/opt/alias.env":  "/proj/.env",
		"/proj/loop":      "",
	}}
	o := testOptions(ModeAcceptEdits, rules{
		deny:  []string{"Read(.env)"},
		allow: []string{"Read(//opt/**)"},
	})
	o.Resolve = func(p string) (string, error) {
		if p == "/proj/loop" {
			return "", errLoop
		}
		return fs.resolve(p)
	}
	e := mustEngine(t, o)
	for _, tt := range []struct {
		tool, path string
		want       Behavior
	}{
		{"Read", "/proj/a.go", Allow},
		{"Read", "/proj/notes.txt", Ask}, // in the folder as written, outside it for real
		{"Edit", "/proj/out/a", Ask},     // acceptEdits covers where the path really leads
		{"Edit", "/proj/a.go", Allow},
		{"Read", "/opt/alias.env", Deny}, // a deny rule applies when either path matches
		{"Read", "/opt/x", Allow},
		{"Read", "/proj/loop", Ask}, // links that cannot be followed
		{"Edit", "/proj/loop", Ask},
	} {
		if d := file(e, tt.tool, tt.path); d.Behavior != tt.want {
			t.Errorf("%s %s: got %s (%s), want %s", tt.tool, tt.path, d.Behavior, d.Reason, tt.want)
		}
	}
}

// errLoop stands for a link loop.
var errLoop = errString("too many levels of symbolic links")

type errString string

func (e errString) Error() string { return string(e) }

// The folder of work trees counts only where it is what it says: a link in
// its place (a repository can ship ".gnosh/worktrees -> ..") or a data
// folder named like another protected one (.git) gives no exemption, and
// the project's .git and the rest stay protected.
// A session whose project is a work tree gnosh made, and kept, has two
// folders of work trees: its own data folder's, and the one its project
// lies in. Its project's files are open; the data folders' own files and
// the work tree's .git are not.
func TestSessionInAKeptWorkTree(t *testing.T) {
	o := testOptions(ModeAcceptEdits, rules{allow: []string{"Edit", "Write"}})
	o.Cwd = "/proj/.gnosh/worktrees/agent-1"
	o.Protected = []string{"/proj/.gnosh/worktrees/agent-1/.gnosh"}
	o.WorkTrees = []string{"/proj/.gnosh/worktrees/agent-1/.gnosh/worktrees", "/proj/.gnosh/worktrees"}
	e := mustEngine(t, o)
	for _, p := range []string{"/proj/.gnosh/worktrees/agent-1/a.go", "/proj/.gnosh/worktrees/agent-1/sub/b.go"} {
		if d := file(e, "Write", p); d.Behavior != Allow {
			t.Errorf("%s: got %s (%s), want allow", p, d.Behavior, d.Reason)
		}
	}
	for _, p := range []string{"/proj/.gnosh/worktrees/agent-1/.gnosh/settings.json", "/proj/.gnosh/worktrees/agent-1/.git", "/proj/.gnosh/settings.json"} {
		if d := file(e, "Write", p); d.Behavior != Ask {
			t.Errorf("%s: got %s (%s), want ask", p, d.Behavior, d.Reason)
		}
	}
}

func TestWorkTreesThatAreNot(t *testing.T) {
	for name, set := range map[string]func(o *Options){
		"a link to the project": func(o *Options) {
			o.WorkTrees = []string{"/proj/.gnosh/worktrees"}
			o.Resolve = func(p string) (string, error) {
				if rest, ok := strings.CutPrefix(p, "/proj/.gnosh/worktrees"); ok {
					return path.Clean("/proj/" + rest), nil
				}
				return p, nil
			}
		},
		"a data folder named .git": func(o *Options) { o.WorkTrees = []string{"/proj/.git/worktrees"} },
	} {
		o := testOptions(ModeAcceptEdits, rules{allow: []string{"Edit", "Write", "Edit(**)", "Edit(//**)"}})
		set(&o)
		e := mustEngine(t, o)
		for _, p := range []string{"/proj/.git/hooks/pre-commit", "/proj/.gnosh/worktrees/x/.git/hooks/pre-commit", "/proj/.git/worktrees/x/config", "/proj/.claude/settings.json"} {
			if d := file(e, "Write", p); d.Behavior != Ask {
				t.Errorf("%s: a write to %s is %s (%s), want a question", name, p, d.Behavior, d.Reason)
			}
		}
	}
}

func TestProtectedPaths(t *testing.T) {
	protected := []string{
		"/proj/.git/config", "/proj/.git/hooks/pre-commit", "/proj/sub/.git/HEAD", "/proj/.GIT/config",
		"/home/u/.config/git/config", "/proj/.vscode/settings.json", "/proj/.idea/x", "/proj/.husky/pre-commit",
		"/proj/.cargo/config.toml", "/proj/.devcontainer/devcontainer.json", "/proj/.yarn/releases/y.cjs",
		"/proj/.mvn/wrapper/x", "/proj/.claude/settings.json", "/proj/.gnosh/settings.local.json",
		"/proj/.gitconfig", "/proj/.gitmodules", "/home/u/.bashrc", "/home/u/.zshrc", "/home/u/.ZSHRC",
		"/home/u/.profile", "/proj/.envrc", "/proj/.npmrc", "/proj/.yarnrc.yml", "/proj/bunfig.toml",
		"/proj/.bazelrc", "/proj/.pre-commit-config.yaml", "/proj/lefthook.yml",
		"/proj/gradle/wrapper/gradle-wrapper.properties", "/proj/.devcontainer.json", "/proj/.ripgreprc",
		"/proj/pyrightconfig.json", "/proj/.mcp.json", "/home/u/.claude.json", "/proj/gnosh.json",
		"/proj/sub/GNOSH.JSON", "/home/u/.config/gnosh/gnosh.json", "/home/u/.ssh/authorized_keys",
		"/custom/settings.json", "/var/mem/store/x",
		// The folder of work trees and a work tree's own folder are gnosh's;
		// a protected name inside a work tree is protected as anywhere.
		"/proj/.gnosh/worktrees/agent-1", "/proj/.gnosh/worktrees/.gitignore",
		"/proj/.gnosh/worktrees/agent-1/.git", "/proj/.gnosh/worktrees/agent-1/.gnosh/settings.local.json",
		"/proj/.gnosh/worktrees/agent-1/sub/.envrc",
	}
	open := []string{
		"/proj/a.go", "/proj/git/config", "/proj/.github/workflows/ci.yml", "/proj/.claude/worktrees/w/a.go",
		"/proj/zshrc", "/proj/.env", "/proj/gitconfig",
		// (The line-format files gnosh once read are files like any now.)
		"/proj/gnoshrc", "/proj/.gnoshrc", "/proj/gnosh.json.bak",
		// What is inside a work tree gnosh made for a sub-agent.
		"/proj/.gnosh/worktrees/agent-1/a.go", "/proj/.gnosh/worktrees/agent-1/sub/b.go",
	}
	o := testOptions(ModeAcceptEdits, rules{allow: []string{"Edit", "Write", "Edit(**)", "Edit(//**)"}})
	o.Protected = []string{"/custom/settings.json", "/var/mem/store", "/proj/.gnosh"}
	o.WorkTrees = []string{"/proj/.gnosh/worktrees"}
	e := mustEngine(t, o)
	for _, p := range protected {
		// An allow rule and acceptEdits do not pre-approve it.
		d := file(e, "Write", p)
		if d.Behavior != Ask || d.Stage != StageSafety || len(d.Suggestions) != 0 {
			t.Errorf("%s: got %s at %s with %d suggestions, want a plain ask at the safety stage", p, d.Behavior, d.Stage, len(d.Suggestions))
		}
		// Nor does a hook's approval.
		if d := d.WithHook(HookAllow, ""); d.Behavior != Ask {
			t.Errorf("%s: a hook approved a protected write", p)
		}
		// Reading it is another matter.
		if d := file(e, "Read", p); d.Stage == StageSafety {
			t.Errorf("%s: reading a protected path hit the safety check", p)
		}
	}
	for _, p := range open {
		if d := file(e, "Write", p); d.Behavior != Allow {
			t.Errorf("%s: got %s (%s), want allow", p, d.Behavior, d.Reason)
		}
	}
	for mode, want := range map[Mode]Behavior{
		ModeDefault: Ask, ModeAcceptEdits: Ask, ModeDontAsk: Deny, ModeBypass: Allow,
	} {
		if err := e.SetMode(mode); err != nil {
			t.Fatal(err)
		}
		if d := file(e, "Edit", "/proj/.git/config"); d.Behavior != want {
			t.Errorf("protected write in %s: got %s, want %s", mode, d.Behavior, want)
		}
	}
	// A protected path reached through a link is judged where it leads.
	o = testOptions(ModeAcceptEdits, rules{})
	o.Resolve = fakeFS{links: map[string]string{"/proj/innocent": "/proj/.git/config"}}.resolve
	if d := file(mustEngine(t, o), "Edit", "/proj/innocent"); d.Behavior != Ask || d.Stage != StageSafety {
		t.Errorf("a link to a protected file: %+v", d)
	}
}

// CommandDirs: where the shell may be while a command runs, or that it
// cannot be told.
func TestCommandDirs(t *testing.T) {
	e := mustEngine(t, testOptions(ModeDefault, rules{}))
	for _, c := range []struct {
		dir, cmd string
		want     string // "" with ok false
	}{
		{"/proj", "ls", "/proj"},
		{"/proj/src", "git status", "/proj/src"},
		{"/proj", "cd src && ls", "/proj /proj/src"},
		{"/proj", "cd /tmp; git status", "/proj /tmp"},
		{"/proj", "cd .. && ls", "/proj /"},
		{"/proj", "cd $X && ls", ""},
		{"/proj", `eval "$CMD"`, ""},
		{"/proj", "ls 'unclosed", ""},
		// What sends a cd elsewhere than its words say, in every way a
		// variable is given a value; and, with no cd or ~ in the line,
		// nothing (review, 16 November).
		{"/proj", "CDPATH=/repo; cd src && make", ""},
		{"/proj", "export HOME=/repo; cd && make", ""},
		{"/proj", "HOME=/repo cd ~ && make", ""},
		{"/proj", "read CDPATH <<< /repo; cd internal && go generate ./...", ""},
		{"/proj", "printf -v CDPATH /repo; cd internal", ""},
		{"/proj", ": ${CDPATH:=/repo}; cd internal", ""},
		{"/proj", `export "CDPATH=/repo"; cd internal`, ""},
		{"/proj", "read HOME <<< /repo; cd && make", ""},
		{"/proj", "read HOME <<< /repo; echo x > ~/f", ""},
		{"/proj", "HOME=$(mktemp -d) go test ./...", "/proj"},
		{"/proj", "grep -n HOME= .env", "/proj"},
		// A program a flag of its own sends to another folder, however the
		// flag is written: gnosh does not follow what it names from there.
		{"/proj", "env -C /repo make", ""},
		{"/proj", "env --chdir=/repo git commit -am x", ""},
		{"/proj", "env -C/repo make", ""},
		{"/proj", `env "-C" /repo make`, ""},
		{"/proj", "env --ch=/repo make", ""},
		{"/proj", "make -C sub all", ""},
		{"/proj", `make --directory="$HOME/src/app" install`, ""},
		{"/proj", `make -C"/repo" test`, ""},
		{"/proj", "make -sC /repo", ""},
		{"/proj", "make --dir=/repo", ""},
		{"/proj", "tar -xf a.tar -C /repo", ""},
		{"/proj", "sudo -D /repo ls", ""},
		{"/proj", "go -C /repo generate ./...", ""},
		{"/proj", "env -C \"$D\" make", ""},
		{"/proj", "make $FLAGS", ""},
		// Flags that are no such thing stay as they were.
		{"/proj", "grep -C 3 x f", "/proj"},
		{"/proj", "env FOO=1 grep -C 3 x f", "/proj"},
		{"/proj", "env -u FOO make all", "/proj"},
		{"/proj", "sudo -u root ls", "/proj"},
		{"/proj", "make -j4 test", "/proj"},
		{"/proj", "tar -xzf a.tgz", "/proj"},
		{"/proj", "go test -count=1 ./...", "/proj"},
	} {
		dirs, why := e.CommandDirs(c.dir, c.cmd)
		got, ok := strings.Join(dirs, " "), why == ""
		if c.want == "" {
			if ok {
				t.Errorf("CommandDirs(%q, %q) = %q, want that it cannot be told", c.dir, c.cmd, got)
			}
			continue
		}
		if !ok || got != c.want {
			t.Errorf("CommandDirs(%q, %q) = %q, %v; want %q", c.dir, c.cmd, got, ok, c.want)
		}
	}
}

// LinkAndMore: a line that makes a link and runs anything besides.
func TestLinkAndMore(t *testing.T) {
	e := mustEngine(t, testOptions(ModeDefault, rules{}))
	for cmd, want := range map[string]bool{
		"ln -s /repo r && cd r && make install": true,
		"ln /repo/main.go m.go; cp /tmp/x m.go": true,
		"cp -s /repo/a b && echo x > b":         true,
		"cp --link /repo/a b; cat b":            true,
		"ln -s /repo r":                         false,
		"sudo ln -s /repo r":                    false,
		"cp a b && cat b":                       false,
		"make && make test":                     false,
		"link /repo/a b":                        false,
	} {
		if got := e.LinkAndMore(cmd); got != want {
			t.Errorf("LinkAndMore(%q) = %v, want %v", cmd, got, want)
		}
	}
}

// CommandWrites: the files a line names for writing, where they can be
// told.
func TestCommandWrites(t *testing.T) {
	e := mustEngine(t, testOptions(ModeDefault, rules{}))
	for _, c := range []struct {
		cmd  string
		want string // in the result, or "" for nothing
	}{
		{"echo x > /repo/out.txt", "/repo/out.txt"},
		{"cp /repo/a.go b.go", "/proj/b.go"},
		{"cp a.go /repo/b.go", "/repo/b.go"},
		{"sed -i s/x/y/ /repo/f.go", "/repo/f.go"},
		{"cd /repo && touch x", "/repo/x"},
		{"cat /repo/a.go", ""},
		{"echo hi > /dev/null", ""},
	} {
		got, _ := e.CommandWrites("/proj", c.cmd)
		switch {
		case c.want == "" && len(got) > 0:
			t.Errorf("CommandWrites(%q) = %q, want none", c.cmd, got)
		case c.want != "" && !slices.Contains(got, c.want):
			t.Errorf("CommandWrites(%q) = %q, want %s among them", c.cmd, got, c.want)
		}
	}
}

// CommandWrites says when a file a line writes is named by text computed
// when it runs, which no path can stand for; a file read so, or a target
// the line itself gives its value, is not one (review, 17 November: a
// write to "$(git rev-parse --git-common-dir)/config" was read as /config).
func TestCommandWritesUnknown(t *testing.T) {
	e := mustEngine(t, testOptions(ModeDefault, rules{}))
	for cmd, want := range map[string]bool{
		`printf x >> "$(git rev-parse --git-common-dir)/config"`: true,
		`echo x > "$OUT"`:             true,
		`cp a.txt "$DEST"`:            true,
		`cat "$F" > out.txt`:          false,
		`f=out.txt; echo x > "$f"`:    false,
		`echo x > /tmp/known.txt`:     false,
		`grep -r "$PAT" . > hits.txt`: false,
	} {
		if _, why := e.CommandWrites("/proj", cmd); (why != "") != want {
			t.Errorf("CommandWrites(%q): unknown %q, want unknown %v", cmd, why, want)
		}
	}
}

// PathKey: every name macOS takes for one file has one key, and a key is
// under another where the path is.
func TestPathKey(t *testing.T) {
	same := [][2]string{
		{"/Users/jae/src/app", "/users/JAE/Src/app/"},
		{"/System/Volumes/Data/Users/jae/src", "/Users/jae/src"},
		{"/.nofollow/Users/jae", "/Users/jae"},
	}
	for _, c := range same {
		if PathKey(c[0]) != PathKey(c[1]) {
			t.Errorf("PathKey(%q) = %q and PathKey(%q) = %q, want them alike", c[0], PathKey(c[0]), c[1], PathKey(c[1]))
		}
	}
	if k, dir := PathKey("/System/Volumes/Data/Users/jae/SRC/app/x.go"), PathKey("/Users/jae/src/app"); !strings.HasPrefix(k, dir+"/") {
		t.Errorf("PathKey: %q is not under %q", k, dir)
	}
	if PathKey("/Users/jae/src/app") == PathKey("/Users/jae/src/apps") {
		t.Errorf("PathKey: two folders got one key")
	}
}

// GitPlaces: where a command points git, or that it cannot be told: also
// where a command's own name is only known when it runs, since it may be
// git (the fourth of Claude Code's checks).
func TestGitPlaces(t *testing.T) {
	e := mustEngine(t, testOptions(ModeDefault, rules{}))
	for _, c := range []struct {
		cmd  string
		want string // "-" when it cannot be told
	}{
		{"git status", ""},
		{"git log --oneline -C x", ""}, // after the subcommand: its own words
		{"git -C /repo commit -am x", "/repo"},
		{"git -C ../repo log", "-"}, // (a .. goes through a link first)
		{"git -C lnk/.. status", "-"},
		{"git --git-dir=/repo/x/../.git log", "-"},
		{"git --git-dir=/repo/.git log", "/repo/.git"},
		{"git --git-dir /repo/.git --work-tree /repo log", "/repo/.git /repo"},
		{"GIT_DIR=/repo/.git git log", "/repo/.git"},
		{"export GIT_WORK_TREE=/repo; git status", "/repo"},
		{"env GIT_DIR=/repo/.git git log", "/repo/.git"},
		{"git -c core.pager=cat -C /repo log", "/repo"},
		{"git -C $X log", "-"},
		{"GIT_DIR=$X git log", "-"},
		{"git $SUB", "-"},
		{"$CMD -C /repo commit", "-"},
		{"x=git; $x status", "-"},
		{"ls $DIR", ""},
	} {
		places, why := e.GitPlaces("/proj", c.cmd)
		got, ok := strings.Join(places, " "), why == ""
		if c.want == "-" {
			if ok {
				t.Errorf("GitPlaces(%q) = %q, want that it cannot be told", c.cmd, got)
			}
			continue
		}
		if !ok || got != c.want {
			t.Errorf("GitPlaces(%q) = %q, %v; want %q", c.cmd, got, ok, c.want)
		}
	}
}

// A protected file or folder that is a link protects what it leads to:
// that is the file the settings are read from. And a work tree named as a
// protected folder (.git) gets no work tree's exemption.
func TestProtectedLinksAndTrees(t *testing.T) {
	o := testOptions(ModeAcceptEdits, rules{allow: []string{"Edit", "Write", "Edit(**)", "Edit(//**)"}})
	o.Protected = []string{"/home/u/.config/gnosh"}
	o.Resolve = func(p string) (string, error) {
		if p == "/home/u/.config/gnosh" {
			return "/home/u/dotfiles/gnosh", nil
		}
		return p, nil
	}
	o.WorkTrees = []string{"/proj/.gnosh/worktrees"}
	e := mustEngine(t, o)
	for _, p := range []string{"/home/u/dotfiles/gnosh/settings.json", "/proj/.gnosh/worktrees/.git/config"} {
		if d := file(e, "Write", p); d.Behavior != Ask || d.Stage != StageSafety {
			t.Errorf("%s: got %s at %s, want a plain ask at the safety stage", p, d.Behavior, d.Stage)
		}
	}
	if d := file(e, "Write", "/home/u/dotfiles/other.txt"); d.Stage == StageSafety {
		t.Errorf("a file beside the linked folder hit the safety check")
	}
}
