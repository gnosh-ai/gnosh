package shelltool

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"
)

// The stand-in for ssh.
//
// No test here reaches another machine, or runs the real ssh. The manager
// is given this test binary as its ssh program (Options.SSH), with one
// variable in the environment that says where the stand-in's files are;
// TestMain sees the variable, plays ssh, and exits before any test runs.
// The stand-in writes down exactly how it was called, and then does what
// the test's plan says: it runs the command the way an ssh server would,
// or fails the way ssh fails.
//
// Run as ssh, the binary is given ssh's arguments, which no test binary
// understands: should the variable ever go missing, the binary refuses its
// arguments and exits, and nothing runs the tests a second time. The
// stand-in also ends by itself after two minutes, whatever happens.
//
// "The other machine" is a directory of its own and a shell started there
// in a session of its own, with an environment that has nothing of this
// one, and with its output carried through pipes the stand-in copies from.
// So what the real thing does when ssh is stopped happens here too: the
// command is not signalled, a command that prints dies at its next write,
// and one that prints nothing runs on.

// standInEnv names the directory of the stand-in's files. It is set only
// in the environment a manager gives its ssh.
const standInEnv = "GNOSH_SHELLTOOL_STANDIN_SSH"

// plan says what the stand-in does when it is called.
type plan struct {
	// Mode is "run" (the default), or one of the ways ssh fails: "refuse",
	// "timeout", "unknown-key", "changed-key", "denied", "resolve"; or
	// "hang", which never connects; "drop", which loses the connection
	// while the command runs; "noshell", an account whose login shell does
	// not read sh.
	Mode string `json:"mode"`
	// Home is the directory a command starts in on the made-up machine.
	Home string `json:"home"`
	// Shell is the login shell there. Empty means /bin/sh.
	Shell string `json:"shell"`
	// DropAfterMS is how long "drop" lets the command run.
	DropAfterMS int `json:"dropAfterMs"`
	// Says is something ssh itself says before it does anything else.
	Says string `json:"says"`
	// EndsItself makes the stand-in take SIGTERM the way the real ssh
	// does: it says so and exits with 255, instead of dying of the signal.
	EndsItself bool `json:"endsItself"`
}

// sshCall is one call of the stand-in, as it wrote it down.
type sshCall struct {
	// PID is the stand-in's own process.
	PID int `json:"pid"`
	// Args are its arguments, exactly.
	Args []string `json:"args"`
	// Stdin is what it could read from its standard input before the end.
	Stdin string `json:"stdin"`
	// Dir is the directory it was started in.
	Dir string `json:"dir"`
}

// The words the real ssh uses, for the ways it fails.
var sshFailures = map[string]string{
	"refuse":      "ssh: connect to host 203.0.113.7 port 22: Connection refused",
	"timeout":     "ssh: connect to host 203.0.113.7 port 22: Connection timed out",
	"unknown-key": "No ED25519 host key is known for 203.0.113.7 and you have requested strict checking.\nHost key verification failed.",
	"changed-key": "@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@\n@    WARNING: REMOTE HOST IDENTIFICATION HAS CHANGED!     @\n@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@\nIT IS POSSIBLE THAT SOMEONE IS DOING SOMETHING NASTY!\nHost key for 203.0.113.7 has changed and you have requested strict checking.\nHost key verification failed.",
	"denied":      "root@203.0.113.7: Permission denied (publickey).",
	"resolve":     "ssh: Could not resolve hostname render.example.com: Name or service not known",
}

// standIn plays ssh. dir holds its plan and takes its notes; args are what
// ssh was given. It returns the exit code.
func standIn(dir string, args []string) int {
	time.AfterFunc(2*time.Minute, func() { os.Exit(254) })
	var p plan
	if data, err := os.ReadFile(filepath.Join(dir, "plan.json")); err == nil {
		_ = json.Unmarshal(data, &p)
	}
	stdin, _ := io.ReadAll(os.Stdin)
	cwd, _ := os.Getwd()
	note(filepath.Join(dir, "calls"), sshCall{PID: os.Getpid(), Args: args, Stdin: string(stdin), Dir: cwd})
	if p.Says != "" {
		fmt.Fprintln(os.Stderr, p.Says)
	}
	if p.EndsItself {
		asked := make(chan os.Signal, 1)
		signal.Notify(asked, syscall.SIGTERM)
		go func() {
			<-asked
			fmt.Fprintln(os.Stderr, "Killed by signal 15.")
			os.Exit(255)
		}()
	}
	if words, ok := sshFailures[p.Mode]; ok {
		fmt.Fprintln(os.Stderr, words)
		return 255
	}
	switch p.Mode {
	case "hang":
		select {}
	case "noshell":
		fmt.Println("fish: Unsupported use of '='. In fish, please use 'set PAGER cat'.")
		return 127
	}
	if len(args) == 0 {
		return 255
	}

	// The other machine: its own directory, session and environment.
	shell := p.Shell
	if shell == "" {
		shell = "/bin/sh"
	}
	cmd := exec.Command(shell, "-c", args[len(args)-1])
	cmd.Dir = p.Home
	cmd.Env = []string{"PATH=" + os.Getenv("PATH"), "HOME=" + p.Home, "USER=root"}
	for _, kv := range os.Environ() {
		// The mark of these tests, so that a process left behind is found.
		if strings.HasPrefix(kv, "GNOSH_SHELLTOOL_TEST=") {
			cmd.Env = append(cmd.Env, kv)
		}
	}
	outR, outW, err1 := os.Pipe()
	errR, errW, err2 := os.Pipe()
	if err1 != nil || err2 != nil {
		return 255
	}
	cmd.Stdout, cmd.Stderr = outW, errW
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := cmd.Start(); err != nil {
		fmt.Fprintln(os.Stderr, "the stand-in could not start the far shell:", err)
		return 255
	}
	outW.Close()
	errW.Close()
	note(filepath.Join(dir, "far"), cmd.Process.Pid)
	// The far shell writes the line that reports the start on its standard
	// error before anything else, and ssh passes it on first; two copies
	// racing could pass the command's first output on before it. So
	// standard output waits for that first line, or for the end of
	// standard error.
	var copies sync.WaitGroup
	first := make(chan struct{})
	copies.Go(func() {
		r := bufio.NewReader(errR)
		line, err := r.ReadBytes('\n')
		os.Stderr.Write(line)
		close(first)
		if err == nil {
			io.Copy(os.Stderr, r)
		}
	})
	copies.Go(func() {
		<-first
		io.Copy(os.Stdout, outR)
	})
	if p.Mode == "drop" {
		time.AfterFunc(time.Duration(p.DropAfterMS)*time.Millisecond, func() {
			fmt.Fprintln(os.Stderr, "client_loop: send disconnect: Broken pipe")
			os.Exit(255)
		})
	}
	err := cmd.Wait()
	copies.Wait()
	if state := cmd.ProcessState; state != nil && err != nil {
		if ws, ok := state.Sys().(syscall.WaitStatus); ok && ws.Signaled() {
			return 255
		}
		return state.ExitCode()
	}
	if err != nil {
		return 255
	}
	return 0
}

// note adds one JSON line to a file.
func note(path string, v any) {
	line, err := json.Marshal(v)
	if err != nil {
		return
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0o600)
	if err != nil {
		return
	}
	defer f.Close()
	f.Write(append(line, '\n'))
}

// farBox is a made-up machine for a test: the stand-in's files, and the
// directory commands start in there.
type farBox struct {
	t    *testing.T
	dir  string // the stand-in's plan and notes
	home string // where a command starts on the made-up machine
}

// The hosts these tests declare. The addresses are from the range kept for
// documentation, and nothing ever connects to them.
var testHosts = []Host{
	{Name: "render", Target: "root@203.0.113.7", Description: "the render box"},
	{Name: "web", Target: "deploy@203.0.113.8"},
}

// newFarBox makes a made-up machine and returns it with the options that
// point a manager at it: the stand-in as ssh, and the two test hosts. What
// the test set in o is kept. Whatever the stand-in started on the made-up
// machine is killed when the test ends.
func newFarBox(t *testing.T, p plan, o Options) (*farBox, Options) {
	t.Helper()
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	home, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	b := &farBox{t: t, dir: t.TempDir(), home: home}
	if p.Home == "" {
		p.Home = home
	}
	b.plan(p)
	o.SSH = self
	// With the race detector built in, a program waits a second before it
	// exits, to let reports be written. The stand-in is told not to: each
	// command on the made-up machine would otherwise take that second.
	o.Env = append(o.Env, standInEnv+"="+b.dir, "GORACE=atexit_sleep_ms=0")
	if o.Hosts == nil {
		o.Hosts = testHosts
	}
	t.Cleanup(func() {
		for _, pid := range b.far() {
			syscall.Kill(-pid, syscall.SIGKILL)
		}
	})
	return b, o
}

// plan writes what the stand-in is to do from now on.
func (b *farBox) plan(p plan) {
	b.t.Helper()
	if p.Home == "" {
		p.Home = b.home
	}
	data, err := json.Marshal(p)
	if err != nil {
		b.t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(b.dir, "plan.json"), data, 0o600); err != nil {
		b.t.Fatal(err)
	}
}

// calls returns every call of the stand-in so far, in order.
func (b *farBox) calls() []sshCall {
	b.t.Helper()
	var out []sshCall
	data, _ := os.ReadFile(filepath.Join(b.dir, "calls"))
	for line := range strings.Lines(string(data)) {
		var c sshCall
		if err := json.Unmarshal([]byte(line), &c); err != nil {
			b.t.Fatalf("a note of the stand-in cannot be read: %v", err)
		}
		out = append(out, c)
	}
	return out
}

// far returns the processes the stand-in started on the made-up machine:
// each leads a session of its own.
func (b *farBox) far() []int {
	var out []int
	data, _ := os.ReadFile(filepath.Join(b.dir, "far"))
	for line := range strings.Lines(string(data)) {
		if pid, err := strconv.Atoi(strings.TrimSpace(line)); err == nil && pid > 1 {
			out = append(out, pid)
		}
	}
	return out
}

// lastCall returns the stand-in's last call, and fails the test when it was
// not called exactly n times.
func (b *farBox) lastCall(n int) sshCall {
	b.t.Helper()
	calls := b.calls()
	if len(calls) != n {
		b.t.Fatalf("ssh was run %d times, want %d", len(calls), n)
	}
	return calls[n-1]
}

// script is what ssh is given as its last argument for a command: the line
// gnosh puts in front, written out here so that a change to it shows.
func script(quoted string) string {
	return `[ -n "${ZSH_VERSION-}" ] && setopt no_equals; printf 'gnosh-host-cwd:%s\n' "$PWD" >&2; exec 2>&1; export PAGER=cat GIT_PAGER=cat GIT_TERMINAL_PROMPT=0 TERM=dumb GNOSH=1 AGENT=gnosh AI_AGENT=gnosh; eval ` + quoted
}
