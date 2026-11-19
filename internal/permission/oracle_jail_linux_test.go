//go:build linux

package permission

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"testing"

	"github.com/gnosh-ai/gnosh/internal/tools/shelltool"
)

// This file is the oracle's jail: a throw-away directory tree, a set of
// stub programs, and a sandbox in which a real shell is asked what a
// command line does. Nothing here decides anything; oracle_check_test.go
// holds what the shell did against what the engine said.

// oracleNames lists every program name the jail has a stub for: the names
// the brief gives, every name in the engine's own tables, and the usual
// suspects besides.
func oracleNames() []string {
	set := map[string]bool{}
	for _, n := range strings.Fields(`ls cat rm git echo grep find curl sh bash env xargs sudo timeout nohup make
		python npm mkdir touch rmdir mv cp sed date test printf kill sleep id go node python3 perl ruby awk tee dd
		tar less more man wget ssh scp rsync nc ln chmod chown install truncate shred unlink strings xxd od base64
		tac bat hexdump printenv yes true false docker npx pip cargo gnokey pwd cd type command builtin exec eval
		source alias trap export noglob [`) {
		set[n] = true
	}
	for n := range roTable {
		set[n] = true
	}
	for n := range wrappers {
		set[n] = true
	}
	for n := range shellNames {
		set[n] = true
	}
	out := make([]string, 0, len(set))
	for n := range set {
		out = append(out, n)
	}
	sort.Strings(out)
	return out
}

// stubModes are the directories of stubs a run can have on its PATH: p is
// stubs that only write down that they ran, r lets the read-only tools,
// the wrappers and nested shells be the real programs, e adds the real
// file commands; q and f are r and e with the real tools told to keep to
// POSIX (see the stub); 0 and 1 are the exit status of a stub that stays
// a stub.
var stubModes = []string{"p0", "p1", "r0", "r1", "e0", "e1", "q0", "f0"}

// buildStub compiles the stub program into dir and returns its path.
func buildStub(dir string) (string, error) {
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module oraclestub\n\ngo 1.21\n"), 0o644); err != nil {
		return "", err
	}
	if err := os.WriteFile(filepath.Join(dir, "main.go"), []byte(stubSource), 0o644); err != nil {
		return "", err
	}
	goBin, err := exec.LookPath("go")
	if err != nil {
		goBin = filepath.Join(runtime.GOROOT(), "bin", "go")
	}
	bin := filepath.Join(dir, "stub")
	cmd := exec.Command(goBin, "build", "-o", bin, ".")
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "CGO_ENABLED=0", "GOFLAGS=-mod=mod", "GOPROXY=off", "GOTOOLCHAIN=local", "GOWORK=off")
	if out, err := cmd.CombinedOutput(); err != nil {
		return "", fmt.Errorf("building the stub program: %v\n%s", err, out)
	}
	return bin, nil
}

// Tokens are planted in files so that a read of one shows up in what a
// command prints or is given.
const (
	tokEnv     = "ENVTOKEN4242"
	tokSubEnv  = "ENVTOKEN4243"
	tokOutside = "OUTTOKEN7171"
	tokOutDir  = "OUTTOKEN7272"
	tokSSH     = "SSHTOKEN9191"
	tokHome    = "HOMETOKEN5151"
	tokPasswd  = "root:x:0:0"
)

// outsideTokens are the ones no read-only command may ever show.
var outsideTokens = []string{tokOutside, tokOutDir, tokSSH, tokHome, tokPasswd}

// jail is one worker's tree and sandbox.
type jail struct {
	root, work, home, outside, logDir, template string
	uid                                         uint32
	hasGit                                      bool
	srv                                         *exec.Cmd
	srvIn                                       io.WriteCloser
	srvOut                                      *bufio.Reader
	pristine                                    map[string]fileMeta
	runs                                        int
	// rawEnv, when not nil, is the environment shells are started with,
	// in place of the one the shell tool's builder makes (see env).
	rawEnv []string
	// fileLines holds every line of the files the jail starts with
	// (notes.md's "rm -rf x is only text here" among them): a command
	// whose whole text is one, in a run that sourced a file, came from it.
	fileLines map[string]bool
}

// fileMeta is what a snapshot keeps of one file.
type fileMeta struct {
	mode  fs.FileMode
	size  int64
	mtime int64
	ino   uint64
	link  string
}

// fileChange is one difference between two snapshots.
type fileChange struct {
	path string // absolute
	what string // created, changed, deleted
}

// newJail builds a jail for worker idx from the stub binary.
func newJail(stub string, idx int) (*jail, error) {
	root, err := os.MkdirTemp("", "gnosh-oracle-")
	if err != nil {
		return nil, err
	}
	if root, err = filepath.EvalSymlinks(root); err != nil {
		return nil, err
	}
	j := &jail{
		root: root, work: root + "/work", home: root + "/home", outside: root + "/outside",
		logDir: root + "/log", template: root + "/template", uid: uint32(61000 + idx),
	}
	fail := func(err error) (*jail, error) {
		j.close()
		return nil, err
	}
	if err := os.Chmod(root, 0o755); err != nil {
		return fail(err)
	}
	for _, d := range []string{"bin", "log", "template"} {
		if err := os.Mkdir(filepath.Join(root, d), 0o755); err != nil {
			return fail(err)
		}
	}
	base := filepath.Join(root, "bin", "stub")
	if err := copyFile(stub, base, 0o755); err != nil {
		return fail(err)
	}
	if err := os.Link(base, filepath.Join(root, "bin", "oracle-serve")); err != nil {
		return fail(err)
	}
	names := oracleNames()
	for _, m := range stubModes {
		dir := filepath.Join(root, "bin", m)
		if err := os.Mkdir(dir, 0o755); err != nil {
			return fail(err)
		}
		for _, n := range names {
			if err := os.Link(base, filepath.Join(dir, n)); err != nil {
				return fail(err)
			}
		}
	}
	for _, f := range []string{"stubs", "trace", "out", "err", "cwd"} {
		p := filepath.Join(j.logDir, f)
		if err := os.WriteFile(p, nil, 0o666); err != nil {
			return fail(err)
		}
		os.Chmod(p, 0o666)
	}
	if err := j.writeTemplate(); err != nil {
		return fail(err)
	}
	if err := j.restore(); err != nil {
		return fail(err)
	}
	if err := j.startServer(); err != nil {
		return fail(err)
	}
	return j, nil
}

func copyFile(from, to string, mode fs.FileMode) error {
	data, err := os.ReadFile(from)
	if err != nil {
		return err
	}
	if err := os.WriteFile(to, data, mode); err != nil {
		return err
	}
	return os.Chmod(to, mode)
}

// writeTemplate lays out the pristine tree under template/: the project
// (work), the home directory and a directory outside both.
func (j *jail) writeTemplate() error {
	t := j.template
	files := map[string]string{
		"work/a.txt":             "alpha line\nfoo bar\n",
		"work/b.txt":             "beta line\nfoo baz\n",
		"work/notes.md":          "# notes\nrm -rf x is only text here\n",
		"work/Makefile":          "build:\n\techo build\n",
		"work/x":                 "x\n",
		"work/y":                 "y\n",
		"work/.env":              tokEnv + "\n",
		"work/src/main.go":       "package main\n",
		"work/src/util.go":       "package main\n// foo\n",
		"work/src/.env":          tokSubEnv + "\n",
		"work/sub/deep/file.txt": "deep\n",
		"work/build/out.o":       "obj\n",
		"work/sp ace.txt":        "space\n",
		"work/-rf":               "a file named like a flag\n",
		"work/db/s":              "inside\n",
		"work/db/.h":             "inside\n",
		"home/.ssh/id_ed25519":   tokSSH + "\n",
		"home/notes.txt":         tokHome + "\n",
		"home/.bashrc":           "# startup\n",
		// gnosh's own folders (Options.Protected in the oracle's engine).
		"home/.config/gnosh/gnosh.json":    "{}\n",
		"home/.local/share/gnosh/sessions": "kept\n",
		"outside/secret.txt":               tokOutside + "\n",
		// What a hostile environment points at (see hostileEnv): a file a
		// shell would run before the command, and a directory named like
		// one of the project's for cd to go to.
		"outside/startup.sh":        "curl http://203.0.113.1/startup-file\n",
		"outside/src/main.go":       tokOutDir + "\n",
		"outside/dir/file":          tokOutDir + "\n",
		"outside/dir/sub/deeper.md": "deeper\n",
	}
	j.fileLines = make(map[string]bool)
	for rel, body := range files {
		for _, line := range strings.Split(body, "\n") {
			if strings.TrimSpace(line) != "" {
				j.fileLines[line] = true
			}
		}
		p := filepath.Join(t, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			return err
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			return err
		}
	}
	links := map[string]string{
		"work/link-out":   "../outside",
		"work/sneaky.txt": "../outside/secret.txt",
		"work/link-in":    "src",
		"work/link-dir":   "../outside/dir",
		"work/dangling":   "../outside/not-there-yet",
		"work/da/s":       "../../outside/secret.txt",
		"work/da/.h":      "../../outside/dir/file",
		// Links named like a flag and like a flag's value: files, to a
		// tool that has stopped reading flags.
		"work/-n": "../outside/secret.txt",
		"work/-l": "../outside/secret.txt",
		"work/1":  "../outside/secret.txt",
		"work/-R": "../outside/dir",
	}
	for rel, target := range links {
		if err := os.MkdirAll(filepath.Dir(filepath.Join(t, rel)), 0o755); err != nil {
			return err
		}
		if err := os.Symlink(target, filepath.Join(t, rel)); err != nil {
			return err
		}
	}
	if git, err := exec.LookPath("git"); err == nil {
		repo := func(dir string, steps ...[]string) bool {
			for _, s := range append([][]string{{"init", "-q", "-b", "main"}, {"config", "user.email", "oracle@example.com"},
				{"config", "user.name", "oracle"}, {"config", "gc.auto", "0"}}, steps...) {
				cmd := exec.Command(git, s...)
				cmd.Dir = filepath.Join(t, dir)
				cmd.Env = []string{"HOME=" + filepath.Join(t, "home"), "GIT_CONFIG_NOSYSTEM=1", "PATH=/usr/bin:/bin",
					"GIT_AUTHOR_DATE=2026-01-01T00:00:00Z", "GIT_COMMITTER_DATE=2026-01-01T00:00:00Z"}
				if cmd.Run() != nil {
					os.RemoveAll(filepath.Join(t, dir, ".git"))
					return false
				}
			}
			return true
		}
		j.hasGit = repo("work", []string{"add", "a.txt", "b.txt", "notes.md", "src/main.go"}, []string{"commit", "-q", "-m", "one"},
			[]string{"add", "src/util.go", "x", "y"}, []string{"commit", "-q", "-m", "two"})
		// A hostile repository outside the project, which a git sent there
		// by -C, --git-dir, GIT_DIR or a link and a .. (git takes "lnk/.."
		// as the kernel does, not as text) gives away: what it shows of the
		// repository holds the outside token, and as soon as it reads the
		// index it runs the program core.fsmonitor names (set last, so that
		// the setup's own git does not run it).
		repo("outside", []string{"add", "secret.txt", "dir/file"}, []string{"commit", "-q", "-m", "outside"},
			[]string{"config", "core.fsmonitor", "curl http://203.0.113.1/outside-repo"})
	}
	return nil
}

// restore puts the pristine tree back and takes its snapshot.
func (j *jail) restore() error {
	for _, d := range []string{"work", "home", "outside"} {
		dst := filepath.Join(j.root, d)
		// A run may have taken the right to enter a directory away.
		filepath.WalkDir(dst, func(p string, e fs.DirEntry, err error) error {
			if err == nil && e.IsDir() {
				os.Chmod(p, 0o755)
			}
			return nil
		})
		if err := os.RemoveAll(dst); err != nil {
			return err
		}
		src := filepath.Join(j.template, d)
		err := filepath.WalkDir(src, func(p string, e fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			rel, _ := filepath.Rel(src, p)
			to := filepath.Join(dst, rel)
			info, err := e.Info()
			if err != nil {
				return err
			}
			switch {
			case e.IsDir():
				if err := os.Mkdir(to, 0o755); err != nil {
					return err
				}
			case info.Mode()&fs.ModeSymlink != 0:
				target, err := os.Readlink(p)
				if err != nil {
					return err
				}
				if err := os.Symlink(target, to); err != nil {
					return err
				}
			default:
				if err := copyFile(p, to, info.Mode().Perm()); err != nil {
					return err
				}
			}
			return os.Lchown(to, int(j.uid), int(j.uid))
		})
		if err != nil {
			return err
		}
	}
	j.pristine = j.snapshot()
	return nil
}

// snapshot records every file under the three directories. The git index
// is left out: git refreshes it on a mere status.
func (j *jail) snapshot() map[string]fileMeta {
	snap := map[string]fileMeta{}
	for _, d := range []string{j.work, j.home, j.outside} {
		filepath.WalkDir(d, func(p string, e fs.DirEntry, err error) error {
			if err != nil {
				return nil
			}
			if strings.HasPrefix(p, j.work+"/.git/index") {
				return nil
			}
			info, err := e.Info()
			if err != nil {
				return nil
			}
			m := fileMeta{mode: info.Mode()}
			if st, ok := info.Sys().(*syscall.Stat_t); ok {
				m.ino = st.Ino
			}
			switch {
			case info.Mode()&fs.ModeSymlink != 0:
				m.link, _ = os.Readlink(p)
			case !e.IsDir():
				m.size, m.mtime = info.Size(), info.ModTime().UnixNano()
			}
			snap[p] = m
			return nil
		})
	}
	return snap
}

// changesSince lists what differs from the pristine snapshot.
func (j *jail) changesSince() []fileChange {
	now := j.snapshot()
	var out []fileChange
	for p, m := range now {
		old, ok := j.pristine[p]
		switch {
		case !ok:
			out = append(out, fileChange{p, "created"})
		case old != m:
			out = append(out, fileChange{p, "changed"})
		}
	}
	for p := range j.pristine {
		if _, ok := now[p]; !ok {
			out = append(out, fileChange{p, "deleted"})
		}
	}
	sort.Slice(out, func(a, b int) bool { return out[a].path < out[b].path })
	return out
}

// startServer starts the sandbox: a process in namespaces of its own that
// runs the shells.
func (j *jail) startServer() error {
	cmd := exec.Command(filepath.Join(j.root, "bin", "oracle-serve"), j.root)
	cmd.Env = []string{"PATH=/nonexistent"}
	cmd.Stderr = os.Stderr
	cmd.SysProcAttr = &syscall.SysProcAttr{
		Cloneflags: syscall.CLONE_NEWNS | syscall.CLONE_NEWPID | syscall.CLONE_NEWNET | syscall.CLONE_NEWIPC,
		Pdeathsig:  syscall.SIGKILL,
	}
	in, err := cmd.StdinPipe()
	if err != nil {
		return err
	}
	out, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("starting the sandbox: %w", err)
	}
	j.srv, j.srvIn, j.srvOut = cmd, in, bufio.NewReaderSize(out, 1<<16)
	var hello struct{ Err string }
	line, err := j.srvOut.ReadBytes('\n')
	if err == nil {
		err = json.Unmarshal(line, &hello)
	}
	if err == nil && hello.Err != "" {
		err = errors.New(hello.Err)
	}
	if err != nil {
		j.stopServer()
		return fmt.Errorf("the sandbox did not come up: %w", err)
	}
	return nil
}

func (j *jail) stopServer() {
	if j.srv == nil {
		return
	}
	j.srvIn.Close()
	j.srv.Process.Kill()
	j.srv.Wait()
	j.srv = nil
}

// close stops the sandbox and removes the tree.
func (j *jail) close() {
	j.stopServer()
	filepath.WalkDir(j.root, func(p string, e fs.DirEntry, err error) error {
		if err == nil && e.IsDir() {
			os.Chmod(p, 0o755)
		}
		return nil
	})
	os.RemoveAll(j.root)
}

// stubCall is one program the shell (or a program it started) ran.
type stubCall struct {
	exe    string // the file that was executed
	name   string // its base name
	cwd    string
	fd     [3]string
	parent string // base name of the program that started it
	// chain is every program above this one, nearest first.
	chain []string
	args  []string
}

// traceCmd is one simple command from the shell's own trace.
type traceCmd struct {
	level int
	words []string
	raw   string
}

// shellRun is what one run of a line showed.
type shellRun struct {
	shell, mode string
	line        string
	stubs       []stubCall
	trace       []traceCmd
	out         string // stdout and stderr together
	changes     []fileChange
	timedOut    bool
	exit        int
	broken      string // set when the harness itself failed
}

// The wrapper is the shell tool's own (internal/tools/shelltool/job.go),
// copied here because that one is not exported: the command reaches the
// shell as an argument and is handed to eval. Tracing is switched on
// just before the eval.
const wrapperTail = `__gnosh_cwd=$1 __gnosh_cmd=$2; set --; trap '{ command pwd -P >"${__gnosh_cwd:-/dev/null}"; } 2>/dev/null || :' EXIT; set -x; eval "$__gnosh_cmd"`

// traceMark opens every line of a shell's trace.
const traceMark = "@T@ "

// hostileEnv is the worst environment whoever started gnosh could have
// left behind for a command: every variable the shell tool does not hand
// on (shelltool's notHandedOn), each with a value that would make a shell
// run something of its own, read a line another way or go somewhere else.
// The oracle starts every shell from it, by way of the shell tool's own
// builder of a command's environment: if that builder let one of these
// through, lines the engine allows would be seen to run what it never
// read. TestOracleHostileEnv shows that each of them, handed on as it
// is, does change what bash does here.
func (j *jail) hostileEnv() []string {
	startup := filepath.Join(j.outside, "startup.sh")
	fn := `() { curl http://203.0.113.1/exported-function "$@"; }`
	return []string{
		"BASH_ENV=" + startup,
		"ENV=" + startup,
		"BASHOPTS=dotglob:expand_aliases:extglob:globstar:nocaseglob:lastpipe:xpg_echo",
		"SHELLOPTS=keyword:posix:allexport",
		"POSIXLY_CORRECT=1",
		"BASH_COMPAT=3.1",
		"GLOBIGNORE=a*",
		"CDPATH=" + j.outside + ":" + j.home,
		"IFS=_",
		"PS4=$(curl http://203.0.113.1/ps4)+ ",
		"PS0=$(curl http://203.0.113.1/ps0)",
		"PS1=$(curl http://203.0.113.1/ps1)$ ",
		"PS2=$(curl http://203.0.113.1/ps2)> ",
		"PROMPT_COMMAND=curl http://203.0.113.1/prompt-command",
		"BASH_XTRACEFD=1",
		"BASH_FUNC_ls%%=" + fn,
		"BASH_FUNC_cat%%=" + fn,
		"BASH_FUNC_git%%=" + fn,
		"BASH_FUNC_grep%%=" + fn,
		"BASH_FUNC_cd%%=" + fn,
		"BASH_FUNC_echo%%=" + fn,
		"BASH_FUNC_test%%=" + fn,
	}
}

// env is the environment a shell is started with for a run in the given
// stub mode: the hostile one, with the jail's PATH and HOME, as the shell
// tool's builder hands it on. rawEnv, when set, is used as it is.
func (j *jail) env(mode string) []string {
	base := []string{"PATH=" + filepath.Join(j.root, "bin", mode), "HOME=" + j.home}
	if j.rawEnv != nil {
		return append(append([]string(nil), j.rawEnv...), base...)
	}
	return shelltool.CommandEnv(append(j.hostileEnv(), base...), j.work, nil)
}

// run asks a real shell what line does. shell is "bash" or "dash"; mode
// is one of stubModes.
func (j *jail) run(shell, mode, line string) shellRun {
	res := shellRun{shell: shell, mode: mode, line: line}
	if strings.IndexByte(line, 0) >= 0 {
		res.broken = "the line holds a NUL, which no program can be handed"
		return res
	}
	if j.srv == nil {
		if err := j.startServer(); err != nil {
			res.broken = err.Error()
			return res
		}
	}
	for _, f := range []string{"stubs", "trace"} {
		os.Truncate(filepath.Join(j.logDir, f), 0)
	}
	trace := filepath.Join(j.logDir, "trace")
	bin, script := "/bin/bash", "exec 250>>'"+trace+"'; BASH_XTRACEFD=250; PS4='+"+traceMark+"'; "+wrapperTail
	if shell == "dash" {
		bin, script = "/bin/dash", "PS4='+"+traceMark+"'; "+wrapperTail
	}
	rq := map[string]any{
		"Argv":      []string{bin, "-c", script, shell, filepath.Join(j.logDir, "cwd"), line},
		"Env":       j.env(mode),
		"Dir":       j.work,
		"UID":       j.uid,
		"GID":       j.uid,
		"TimeoutMS": 2500,
		"Out":       filepath.Join(j.logDir, "out"),
		"Err":       filepath.Join(j.logDir, "out"),
	}
	data, _ := json.Marshal(rq)
	var resp struct {
		Exit, Signal int
		TimedOut     bool
		Err          string
	}
	_, err := j.srvIn.Write(append(data, '\n'))
	var reply []byte
	if err == nil {
		reply, err = j.srvOut.ReadBytes('\n')
	}
	if err == nil {
		err = json.Unmarshal(reply, &resp)
	}
	if err != nil {
		j.stopServer()
		res.broken = "the sandbox went away: " + err.Error()
		j.restore()
		return res
	}
	j.runs++
	if resp.Err != "" {
		res.broken = resp.Err
	}
	res.exit, res.timedOut = resp.Exit, resp.TimedOut
	res.stubs = readStubLog(filepath.Join(j.logDir, "stubs"))
	res.out = readCapped(filepath.Join(j.logDir, "out"), 256<<10)
	if shell == "bash" {
		res.trace = parseTrace(readCapped(trace, 1<<20), true)
	} else {
		res.trace = parseTrace(res.out, false)
	}
	res.changes = j.changesSince()
	if len(res.changes) > 0 {
		if err := j.restore(); err != nil {
			res.broken = "restoring the jail: " + err.Error()
		}
	}
	return res
}

// readCapped reads at most max bytes of a file. A read cut off at max
// ends at its last whole line: a trace line torn in two is a command
// nobody ran (seed 97 read "tru" at the end of an endless loop's trace of
// "true").
func readCapped(p string, max int64) string {
	f, err := os.Open(p)
	if err != nil {
		return ""
	}
	defer f.Close()
	data, _ := io.ReadAll(io.LimitReader(f, max))
	if int64(len(data)) == max {
		if i := strings.LastIndexByte(string(data), '\n'); i >= 0 {
			data = data[:i+1]
		}
	}
	return string(data)
}

// readStubLog reads what the stubs wrote down.
func readStubLog(p string) []stubCall {
	var out []stubCall
	for _, ln := range strings.Split(readCapped(p, 1<<20), "\n") {
		if ln == "" {
			continue
		}
		var fields []string
		good := true
		for _, q := range strings.Split(ln, "\t") {
			s, err := strconv.Unquote(q)
			if err != nil {
				good = false
				break
			}
			fields = append(fields, s)
		}
		if !good || len(fields) < 7 {
			continue
		}
		c := stubCall{exe: fields[0], name: filepath.Base(fields[0]), cwd: fields[1]}
		if fields[5] != "" {
			c.chain = strings.Split(fields[5], "<")
			c.parent = c.chain[0]
		}
		copy(c.fd[:], fields[2:5])
		c.args = fields[7:]
		out = append(out, c)
	}
	return out
}

// parseTrace reads a shell's trace: every line that opens with one or
// more plus signs and the mark is one simple command. A word that holds a
// line break is printed with the break in it, so with glue set the lines
// that follow a command and do not open with the mark belong to it (bash
// writes its trace to a file of its own; dash mixes it with what the
// commands print, so there only the first line is kept). The wrapper's
// own eval, and its EXIT trap, are left out.
func parseTrace(text string, glue bool) []traceCmd {
	type rec struct {
		level int
		raw   string
	}
	var recs []rec
	for _, ln := range strings.Split(text, "\n") {
		level := 0
		for level < len(ln) && ln[level] == '+' {
			level++
		}
		if level == 0 || !strings.HasPrefix(ln[level:], traceMark) {
			if glue && len(recs) > 0 {
				recs[len(recs)-1].raw += "\n" + ln
			}
			if i := strings.Index(ln, "+"+traceMark); !glue && i > 0 {
				// dash's trace goes where its commands print: a line of
				// it may stand after text that did not end in a line
				// break ("printf x; . file").
				level = 1
				for k := i - 1; k >= 0 && ln[k] == '+'; k-- {
					level++
				}
				// (Not where the mark is PS4's own value, printed: a bare
				// "set" lists every variable, PS4='+@T@ ' among them, and
				// "seq "$(set)"" is traced with that list as its argument.
				// What follows the mark there is the quote that closes the
				// value.)
				if rest := ln[i+1+len(traceMark):]; rest == "" || !strings.ContainsRune("'\"\\", rune(rest[0])) {
					recs = append(recs, rec{level, rest})
				}
			}
			continue
		}
		recs = append(recs, rec{level, ln[level+len(traceMark):]})
	}
	if glue && len(recs) > 0 {
		// The file ends with a line break that belongs to no word.
		recs[len(recs)-1].raw = strings.TrimSuffix(recs[len(recs)-1].raw, "\n")
	}
	var out []traceCmd
	sawEval, evalRaw := false, ""
	for _, r := range recs {
		words := traceWords(r.raw)
		if len(words) == 0 {
			continue
		}
		if !sawEval && r.level == 1 && words[0] == "eval" {
			sawEval, evalRaw = true, r.raw
			continue
		}
		if r.level == 1 && r.raw == evalRaw {
			// The wrapper's own eval again, printed inside a word: dash
			// traces a word as it is, line breaks and all, and a command
			// that read the trace as it was written ("grep -R '' ..",
			// oracle seed 109) hands it a word that holds this line.
			continue
		}
		if first := strings.Fields(strings.SplitN(r.raw, "\n", 2)[0]); r.raw == "command pwd -P" || len(first) > 0 && first[0] == ":" {
			// (A ":" runs nothing, whatever stands after it. And what
			// stands after it may not be its own: the stages of a
			// pipeline trace to one file at once, and a line another
			// stage wrote in the middle of a word of its own, held
			// together with this one by glue, read as ":"'s words. Oracle
			// seed 156: "then :;" beside "export $(set)" in a pipeline.)
			continue
		}
		out = append(out, traceCmd{level: r.level, words: words, raw: r.raw})
	}
	return out
}

// traceWords undoes the quoting bash puts on the words of a trace line:
// '...', $'...', "..." and a backslash before one character.
func traceWords(s string) []string {
	var out []string
	var cur strings.Builder
	have := false
	flush := func() {
		if have {
			out = append(out, cur.String())
			cur.Reset()
			have = false
		}
	}
	for i := 0; i < len(s); {
		c := s[i]
		switch {
		case c == ' ':
			flush()
			i++
		case c == '\'':
			have = true
			end := strings.IndexByte(s[i+1:], '\'')
			if end < 0 {
				cur.WriteString(s[i+1:])
				i = len(s)
				break
			}
			cur.WriteString(s[i+1 : i+1+end])
			i += end + 2
		case c == '$' && i+1 < len(s) && s[i+1] == '\'':
			have = true
			i += 2
			for i < len(s) && s[i] != '\'' {
				if s[i] != '\\' || i+1 >= len(s) {
					cur.WriteByte(s[i])
					i++
					continue
				}
				i++
				switch e := s[i]; e {
				case 'n':
					cur.WriteByte('\n')
				case 't':
					cur.WriteByte('\t')
				case 'r':
					cur.WriteByte('\r')
				case 'a':
					cur.WriteByte(7)
				case 'b':
					cur.WriteByte(8)
				case 'f':
					cur.WriteByte(12)
				case 'v':
					cur.WriteByte(11)
				case 'e', 'E':
					cur.WriteByte(27)
				case '0', '1', '2', '3', '4', '5', '6', '7':
					v, k := 0, 0
					for k < 3 && i+k < len(s) && s[i+k] >= '0' && s[i+k] <= '7' {
						v = v*8 + int(s[i+k]-'0')
						k++
					}
					cur.WriteByte(byte(v))
					i += k - 1
				case 'x':
					v, k := 0, 0
					for k < 2 && i+1+k < len(s) {
						d := strings.IndexByte("0123456789abcdef", s[i+1+k]|0x20)
						if d < 0 {
							break
						}
						v = v*16 + d
						k++
					}
					cur.WriteByte(byte(v))
					i += k
				default:
					cur.WriteByte(e)
				}
				i++
			}
			i++
		case c == '"':
			have = true
			i++
			for i < len(s) && s[i] != '"' {
				if s[i] == '\\' && i+1 < len(s) {
					i++
				}
				cur.WriteByte(s[i])
				i++
			}
			i++
		case c == '\\' && i+1 < len(s):
			have = true
			cur.WriteByte(s[i+1])
			i += 2
		default:
			have = true
			cur.WriteByte(c)
			i++
		}
	}
	flush()
	return out
}

// realResolve follows every symbolic link in a cleaned absolute path, as
// the kernel would, and keeps whatever part of the path does not exist
// yet. A link whose target is not there is still followed: writing
// through it makes the target. This is what Options.Resolve must do.
func realResolve(p string) (string, error) {
	parts := splitPath(p)
	cur := "/"
	links := 0
	for i := 0; i < len(parts); i++ {
		switch parts[i] {
		case ".":
			continue
		case "..":
			cur = filepath.Dir(cur)
			continue
		}
		next := filepath.Join(cur, parts[i])
		fi, err := os.Lstat(next)
		if err != nil {
			if errors.Is(err, fs.ErrNotExist) || errors.Is(err, syscall.ENOTDIR) {
				return filepath.Join(append([]string{cur}, parts[i:]...)...), nil
			}
			return "", err
		}
		if fi.Mode()&fs.ModeSymlink == 0 {
			cur = next
			continue
		}
		if links++; links > 40 {
			return "", errors.New("too many levels of symbolic links")
		}
		target, err := os.Readlink(next)
		if err != nil {
			return "", err
		}
		if strings.HasPrefix(target, "/") {
			cur = "/"
		}
		parts = append(splitPath(target), parts[i+1:]...)
		i = -1
	}
	return cur, nil
}

// shellGlob expands a file name pattern as Options.Glob must: * ? and
// [...] within one path segment, names that start with a dot among the
// matches (as filepath.Glob has them). Whether the shell that runs the
// line would leave those out is the engine's to work out (hidesDots),
// and the real shell's to confirm.
func shellGlob(pattern string) ([]string, error) {
	cur := []string{"/"}
	for _, seg := range splitPath(pattern) {
		var next []string
		if !hasPathMagic(seg) {
			for _, c := range cur {
				next = append(next, filepath.Join(c, seg))
			}
		} else {
			for _, c := range cur {
				ents, err := os.ReadDir(c)
				if err != nil {
					continue
				}
				for _, e := range ents {
					name := e.Name()
					if shellMatch(seg, name) {
						next = append(next, filepath.Join(c, name))
					}
				}
			}
		}
		if len(next) > 5000 {
			return nil, errors.New("the pattern matches too many files")
		}
		cur = next
	}
	var out []string
	for _, p := range cur {
		if _, err := os.Lstat(p); err == nil {
			out = append(out, p)
		}
	}
	return out, nil
}

// shellMatch matches one name against one pattern segment.
func shellMatch(pat, s string) bool {
	px, sx := 0, 0
	star, mark := -1, 0
	for sx < len(s) {
		if px < len(pat) {
			switch c := pat[px]; {
			case c == '*':
				star, mark = px, sx
				px++
				continue
			case c == '?':
				px++
				sx++
				continue
			case c == '[':
				if end, hit, ok := shellClass(pat[px:], s[sx]); ok {
					if hit {
						px += end
						sx++
						continue
					}
				} else if s[sx] == '[' {
					px++
					sx++
					continue
				}
			case c == s[sx]:
				px++
				sx++
				continue
			}
		}
		if star < 0 {
			return false
		}
		mark++
		px, sx = star+1, mark
	}
	for px < len(pat) && pat[px] == '*' {
		px++
	}
	return px == len(pat)
}

// shellClass reads the bracket expression that starts pat.
func shellClass(pat string, c byte) (end int, hit, ok bool) {
	i := 1
	neg := false
	if i < len(pat) && (pat[i] == '!' || pat[i] == '^') {
		neg = true
		i++
	}
	first := true
	for i < len(pat) {
		if pat[i] == ']' && !first {
			return i + 1, hit != neg, true
		}
		first = false
		lo, hi := pat[i], pat[i]
		if i+2 < len(pat) && pat[i+1] == '-' && pat[i+2] != ']' {
			hi = pat[i+2]
			i += 2
		}
		if c >= lo && c <= hi {
			hit = true
		}
		i++
	}
	return 0, false, false
}

// oracleSkip says why the oracle cannot run here, or "".
func oracleSkip() string {
	if os.Geteuid() != 0 {
		return "the oracle needs root: it runs each shell in namespaces of its own, as a user with no rights"
	}
	for _, sh := range []string{"/bin/bash", "/bin/dash"} {
		if _, err := os.Stat(sh); err != nil {
			return "the oracle needs " + sh
		}
	}
	return ""
}

// TestOracleJail checks the jail itself: that a stub writes down what it
// was given, that the trace is read back, that a write is seen, and that
// nothing outside the jail can be written.
// TestOracleHostileEnv shows that the hostile environment has teeth, and
// that the shell tool's builder pulls them: each variable, handed to a
// shell as it is, makes the shell run something the line does not say or
// read the line another way; started from the whole hostile environment
// by way of the builder, as every oracle run is, the same line does what
// it says.
func TestOracleHostileEnv(t *testing.T) {
	if why := oracleSkip(); why != "" {
		t.Skip(why)
	}
	stub, err := buildStub(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	j, err := newJail(stub, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer j.close()
	hostile := map[string]string{}
	for _, kv := range j.hostileEnv() {
		name, _, _ := strings.Cut(kv, "=")
		hostile[name] = kv
	}
	// ran reports whether a program of that name ran with an argument
	// that holds the text (in any directory when dir is empty).
	ran := func(r shellRun, name, arg, dir string) bool {
		for _, c := range r.stubs {
			if c.name != name || dir != "" && c.cwd != dir {
				continue
			}
			for _, a := range c.args {
				if strings.Contains(a, arg) {
					return true
				}
			}
			if arg == "" {
				return true
			}
		}
		return false
	}
	cases := []struct {
		name, shell, mode, line string
		// bites reports the hostile effect in a run.
		bites func(r shellRun) bool
		// set replaces the variable's value in hostileEnv, when one option
		// alone shows the effect better.
		set string
	}{
		{"BASH_ENV", "bash", "p0", "true", func(r shellRun) bool { return ran(r, "curl", "startup-file", "") }, ""},
		{"ENV", "bash", "r0", "dash -i -c 'echo inner'", func(r shellRun) bool { return ran(r, "curl", "startup-file", "") }, ""},
		{"BASH_FUNC_ls%%", "bash", "p0", "ls a.txt", func(r shellRun) bool { return ran(r, "curl", "exported-function", "") }, ""},
		{"BASHOPTS", "bash", "p0", "cat *", func(r shellRun) bool { return ran(r, "cat", ".env", "") }, "BASHOPTS=dotglob"},
		{"BASHOPTS", "bash", "p0", "cat .E*", func(r shellRun) bool { return ran(r, "cat", ".env", "") }, "BASHOPTS=nocaseglob"},
		{"BASHOPTS", "bash", "p0", "alias zz='curl aliased'\nzz", func(r shellRun) bool { return ran(r, "curl", "aliased", "") }, "BASHOPTS=expand_aliases"},
		{"SHELLOPTS", "bash", "p0", "make build X=1", func(r shellRun) bool { return ran(r, "make", "build", "") && !ran(r, "make", "X=1", "") }, "SHELLOPTS=keyword"},
		{"POSIXLY_CORRECT", "bash", "p0", "alias zz='curl aliased'\nzz", func(r shellRun) bool { return ran(r, "curl", "aliased", "") }, ""},
		{"BASH_COMPAT", "bash", "p0", "[[ abc =~ \"a.c\" ]] && curl matched", func(r shellRun) bool { return ran(r, "curl", "matched", "") }, ""},
		{"CDPATH", "bash", "p0", "cd src && cat main.go", func(r shellRun) bool { return ran(r, "cat", "main.go", j.outside+"/src") }, ""},
		{"CDPATH", "dash", "p0", "cd src && cat main.go", func(r shellRun) bool { return ran(r, "cat", "main.go", j.outside+"/src") }, ""},
		{"GLOBIGNORE", "bash", "p0", "GLOBIGNORE=$GLOBIGNORE; cat *", func(r shellRun) bool { return ran(r, "cat", ".env", "") && !ran(r, "cat", "a.txt", "") }, ""},
		{"PROMPT_COMMAND", "bash", "r0", "echo 'echo piped' | bash --norc -i", func(r shellRun) bool { return ran(r, "curl", "prompt-command", "") }, ""},
		{"PS1", "dash", "r0", "echo 'echo piped' | bash --norc -i", func(r shellRun) bool { return ran(r, "curl", "ps1", "") }, ""},
	}
	for _, c := range cases {
		kv := hostile[c.name]
		if c.set != "" {
			kv = c.set
		}
		if kv == "" {
			t.Fatalf("%s is not in the hostile environment", c.name)
		}
		// Handed on as it is.
		j.rawEnv = []string{kv}
		r := j.run(c.shell, c.mode, c.line)
		if r.broken != "" {
			t.Fatal(r.broken)
		}
		if !c.bites(r) {
			t.Errorf("%s handed to %s as it is did not change what %q does here: the test of the builder proves nothing for it\n  stubs: %v\n  out: %s", kv, c.shell, c.line, stubsText(r), r.out)
		}
		// Through the shell tool's builder, with every hostile variable
		// set at once.
		j.rawEnv = nil
		r = j.run(c.shell, c.mode, c.line)
		if r.broken != "" {
			t.Fatal(r.broken)
		}
		if c.bites(r) {
			t.Errorf("%s reached %s through the shell tool's environment: %q did what the variable makes of it\n  stubs: %v", c.name, c.shell, c.line, stubsText(r))
		}
	}
	// Every name the builder does not hand on is in the hostile
	// environment, and none of them is in what a shell is started with.
	built := j.env("p0")
	for _, kv := range built {
		name, _, _ := strings.Cut(kv, "=")
		if _, has := hostile[name]; has {
			t.Errorf("the shell tool's builder handed on %s", kv)
		}
	}
	for _, want := range []string{"PATH=" + filepath.Join(j.root, "bin", "p0"), "HOME=" + j.home, "PWD=" + j.work} {
		found := false
		for _, kv := range built {
			found = found || kv == want
		}
		if !found {
			t.Errorf("the environment a shell is started with lacks %s: %q", want, built)
		}
	}
}

// stubsText lists the programs a run started, for a message.
func stubsText(r shellRun) []string {
	var out []string
	for _, c := range r.stubs {
		out = append(out, c.name+" "+strings.Join(c.args, " ")+" (in "+c.cwd+")")
	}
	return out
}

func TestOracleJail(t *testing.T) {
	if why := oracleSkip(); why != "" {
		t.Skip(why)
	}
	stub, err := buildStub(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	j, err := newJail(stub, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer j.close()

	r := j.run("bash", "p0", `ls -la 'a b' "c;d" $'e\nf' && echo hi | cat - > made.txt; rm x`)
	if r.broken != "" {
		t.Fatal(r.broken)
	}
	var got []string
	for _, c := range r.stubs {
		got = append(got, c.name+" "+strings.Join(c.args, "|"))
	}
	if want := "ls -la|a b|c;d|e\nf,cat -,rm x"; strings.Join(got, ",") != want {
		t.Errorf("stubs: got %q, want %q", strings.Join(got, ","), want)
	}
	var traced []string
	for _, c := range r.trace {
		traced = append(traced, strings.Join(c.words, "|"))
	}
	// (The two sides of a pipe write their trace lines in whichever
	// order they get to it.)
	if got := strings.Join(traced, ","); got != "ls|-la|a b|c;d|e\nf,echo|hi,cat|-,rm|x" && got != "ls|-la|a b|c;d|e\nf,cat|-,echo|hi,rm|x" {
		t.Errorf("trace: got %q", got)
	}
	if len(r.changes) != 1 || r.changes[0].path != j.work+"/made.txt" || r.changes[0].what != "created" {
		t.Errorf("changes: %+v", r.changes)
	}
	if _, err := os.Lstat(j.work + "/made.txt"); err == nil {
		t.Errorf("the jail was not restored")
	}

	// Nothing outside the jail can be written, the network is not there,
	// and the shell is nobody in particular.
	probe := filepath.Join(os.TempDir(), "gnosh-oracle-probe")
	r = j.run("bash", "p0", "echo x > "+probe+"; echo x > /dev/shm/gnosh-oracle-probe; id -u; echo uid=$(command id -u 2>&1) $EUID; echo x > /dev/tcp/203.0.113.1/9 || echo nonet")
	if _, err := os.Lstat(probe); err == nil {
		os.Remove(probe)
		t.Errorf("a shell in the jail wrote to %s", probe)
	}
	if _, err := os.Lstat("/dev/shm/gnosh-oracle-probe"); err == nil {
		os.Remove("/dev/shm/gnosh-oracle-probe")
		t.Errorf("a shell in the jail wrote to /dev/shm")
	}
	if !strings.Contains(r.out, "61000") || !strings.Contains(r.out, "nonet") {
		t.Errorf("the shell is not confined as expected:\n%s", r.out)
	}

	// The real tools, in the mode that has them.
	r = j.run("bash", "r0", "cat a.txt | grep foo; find . -name x -exec rm {} ';'; echo 'rm y' | sh; sed -n 1p sneaky.txt")
	got = got[:0]
	for _, c := range r.stubs {
		got = append(got, c.name+"<"+strings.Join(c.chain, "<")+" "+strings.Join(c.args, "|"))
	}
	sort.Strings(got)
	if want := "cat<bash a.txt,find<bash .|-name|x|-exec|rm|{}|;,grep<bash foo,rm<dash<bash y,rm<find<bash ./x,sed<bash -n|1p|sneaky.txt,sh<bash "; strings.Join(got, ",") != want {
		t.Errorf("real mode stubs:\n got %q\nwant %q", strings.Join(got, ","), want)
	}
	if !strings.Contains(r.out, "foo bar") || !strings.Contains(r.out, tokOutside) {
		t.Errorf("real mode output:\n%s", r.out)
	}

	// dash, a loop that never ends, and a line that kills what it can.
	r = j.run("dash", "p0", "ls a; while true; do ls; done")
	if len(r.stubs) < 100 {
		t.Errorf("the loop ran %d times", len(r.stubs))
	}
	r = j.run("dash", "p0", "kill -9 -1; ls after")
	r = j.run("bash", "p0", "ls still-here")
	if r.broken != "" || len(r.stubs) != 1 {
		t.Errorf("after a kill -9 -1: %+v", r)
	}
	if got, err := realResolve(j.work + "/dangling/x"); err != nil || got != j.outside+"/not-there-yet/x" {
		t.Errorf("realResolve through a dangling link: %q, %v", got, err)
	}
	if got, _ := shellGlob(j.work + "/*.txt"); len(got) != 4 {
		t.Errorf("shellGlob: %q", got)
	}
}
