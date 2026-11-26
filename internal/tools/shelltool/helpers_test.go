package shelltool

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/gnosh-ai/gnosh/internal/core"
)

// mark is a variable that every command these tests start carries in its
// environment, and passes on to whatever it starts. When the tests are
// over, any process that still carries it was left behind by one of them.
var mark = fmt.Sprintf("GNOSH_SHELLTOOL_TEST=%d-%d", os.Getpid(), time.Now().UnixNano())

// TestMain runs the tests and then looks for what they left behind:
// processes, and goroutines of the managers. Either is a failure. A process
// is killed, so that it does not stay on the machine.
func TestMain(m *testing.M) {
	if dir := os.Getenv(standInEnv); dir != "" {
		// This process was started as the stand-in for ssh: see
		// standin_test.go. No test runs in it.
		os.Exit(standIn(dir, os.Args[1:]))
	}
	code := m.Run()
	if left := strays(5 * time.Second); len(left) > 0 {
		fmt.Fprintf(os.Stderr, "FAIL: %d processes started by these tests were left running:\n", len(left))
		for pid, name := range left {
			fmt.Fprintf(os.Stderr, "  pid %d (%s)\n", pid, name)
			syscall.Kill(pid, syscall.SIGKILL)
		}
		code = 1
	}
	// Every manager was closed, so every goroutine of this package's should
	// be gone, give or take the moment it takes the last ones to return.
	deadline := time.Now().Add(5 * time.Second)
	left := ownGoroutines()
	for len(left) > 0 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
		left = ownGoroutines()
	}
	if len(left) > 0 && code == 0 {
		fmt.Fprintf(os.Stderr, "FAIL: %d goroutines of this package are still running after the tests:\n\n%s\n", len(left), strings.Join(left, "\n\n"))
		code = 1
	}
	os.Exit(code)
}

// ownGoroutines returns the stacks of the goroutines that are running this
// package's code, other than the one that asks.
func ownGoroutines() []string {
	buf := make([]byte, 1<<20)
	buf = buf[:runtime.Stack(buf, true)]
	var own []string
	for _, stack := range strings.Split(string(buf), "\n\n") {
		if strings.Contains(stack, "/internal/tools/shelltool.") && !strings.Contains(stack, "shelltool.ownGoroutines") {
			own = append(own, stack)
		}
	}
	return own
}

// strays lists the live processes that carry mark, by pid, giving them up
// to wait to finish dying first. It can only look on Linux, where a
// process's environment can be read; elsewhere it finds nothing.
func strays(wait time.Duration) map[int]string {
	if runtime.GOOS != "linux" {
		return nil
	}
	deadline := time.Now().Add(wait)
	for {
		found := make(map[int]string)
		entries, _ := os.ReadDir("/proc")
		for _, entry := range entries {
			pid, err := strconv.Atoi(entry.Name())
			if err != nil || pid == os.Getpid() {
				continue
			}
			environ, err := os.ReadFile("/proc/" + entry.Name() + "/environ")
			if err != nil || !bytes.Contains(environ, []byte(mark+"\x00")) {
				continue
			}
			stat, err := os.ReadFile("/proc/" + entry.Name() + "/stat")
			if err != nil {
				continue
			}
			if st, ok := parseStat(stat); ok && st.state != 'Z' {
				found[pid] = st.name
			}
		}
		if len(found) == 0 || time.Now().After(deadline) {
			return found
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// newManager makes a manager for a test: its commands carry mark, its
// output files go under the test's own directory, its graces are short,
// and it is closed when the test ends. Fields the test set in o are kept.
func newManager(t *testing.T, o Options) *Manager {
	t.Helper()
	o.Env = append([]string{mark}, o.Env...)
	if o.SpoolDir == "" {
		o.SpoolDir = t.TempDir()
	}
	if o.TermGrace == 0 {
		o.TermGrace = 500 * time.Millisecond
	}
	if o.PipeGrace == 0 {
		o.PipeGrace = 100 * time.Millisecond
	}
	if o.RunningAfter == 0 {
		// What a command runs is told only to the tests that ask for it,
		// so that the others see their live output alone.
		o.RunningAfter = time.Hour
	}
	if o.Local == "" {
		o.Local = "dev@box"
	}
	m := New(o)
	t.Cleanup(func() {
		if err := m.Close(); err != nil {
			t.Errorf("Close: %v", err)
		}
	})
	return m
}

// newEnv makes a tool environment whose working directory is a new
// directory, which is also its only root.
func newEnv(t *testing.T) *core.ToolEnv {
	t.Helper()
	root := t.TempDir()
	return &core.ToolEnv{Cwd: root, Roots: []string{root}, SessionID: "test", Store: &sync.Map{}}
}

// toolNamed returns the manager's tool with the given name.
func toolNamed(t *testing.T, m *Manager, name string) core.Tool {
	t.Helper()
	for _, tool := range m.Tools() {
		if tool.Spec().Name == name {
			return tool
		}
	}
	t.Fatalf("the manager has no tool named %s", name)
	return nil
}

// args is a tool call's arguments.
type args map[string]any

// run calls a tool and fails the test if the harness itself failed.
func run(t *testing.T, ctx context.Context, m *Manager, name string, env *core.ToolEnv, a args) core.ToolResult {
	t.Helper()
	input, err := json.Marshal(a)
	if err != nil {
		t.Fatal(err)
	}
	res, err := toolNamed(t, m, name).Run(ctx, core.ToolCall{ID: "call-" + name, Name: name, Input: input}, env)
	if err != nil {
		t.Fatalf("%s %s: the tool failed to run: %v", name, input, err)
	}
	return res
}

// begin calls a tool on a goroutine of its own and returns the channel its
// result will come on, for tests that must do something while it runs.
func begin(t *testing.T, ctx context.Context, m *Manager, name string, env *core.ToolEnv, a args) <-chan core.ToolResult {
	t.Helper()
	input, err := json.Marshal(a)
	if err != nil {
		t.Fatal(err)
	}
	tool := toolNamed(t, m, name)
	done := make(chan core.ToolResult, 1)
	go func() {
		res, err := tool.Run(ctx, core.ToolCall{ID: "call-" + name, Name: name, Input: input}, env)
		if err != nil {
			t.Errorf("%s %s: the tool failed to run: %v", name, input, err)
		}
		done <- res
	}()
	return done
}

// bash runs a command in the foreground with a timeout that only a hung
// test would reach.
func bash(t *testing.T, m *Manager, env *core.ToolEnv, command string) core.ToolResult {
	t.Helper()
	return run(t, t.Context(), m, BashName, env, args{argCommand: command, argTimeout: 60_000})
}

// stampOf returns the line a result begins with, which says where the
// command ran, and the rest of the result's text. A result that is not a
// command's (a refusal, the answer of TaskStop) has no such line.
func stampOf(res core.ToolResult) (stamp, rest string) {
	text := core.TextOf(res.Content)
	if !strings.HasPrefix(text, "<host>") {
		return "", text
	}
	stamp, rest, _ = strings.Cut(text, "\n")
	return stamp, rest
}

// parts splits a result's text into the line above the output, the output,
// and the notes under it, as compose laid them out. The line that says
// where the command ran is left out.
func parts(res core.ToolResult) (headline, body string, notes []string) {
	_, text := stampOf(res)
	// Only a command that did not simply end gets a line above its output.
	if reason, _ := res.Meta[metaReason].(string); reason != ReasonExited && strings.HasPrefix(text, "[") {
		line, rest, _ := strings.Cut(text, "\n")
		headline, text = strings.TrimSuffix(strings.TrimPrefix(line, "["), "]"), rest
	}
	body = text
	for {
		i := strings.LastIndex(body, "\n[")
		if i < 0 || !strings.HasSuffix(body, "]") || strings.Contains(body[i+2:], "\n") {
			break
		}
		notes = append([]string{body[i+2 : len(body)-1]}, notes...)
		body = body[:i]
	}
	body = strings.TrimSuffix(body, "\n")
	if body == "(no output)" {
		body = ""
	}
	return headline, body, notes
}

// printed returns just the command's output from a result.
func printed(res core.ToolResult) string {
	_, body, _ := parts(res)
	return body
}

// hasNote reports whether one of the result's notes contains want.
func hasNote(res core.ToolResult, want string) bool {
	_, _, notes := parts(res)
	for _, note := range notes {
		if strings.Contains(note, want) {
			return true
		}
	}
	return false
}

// exitCode returns the exit code a result recorded.
func exitCode(t *testing.T, res core.ToolResult) int {
	t.Helper()
	code, ok := res.Meta[metaExitCode].(int)
	if !ok {
		t.Fatalf("the result has no exit code in Meta: %v", res.Meta)
	}
	return code
}

// alive reports whether a process is still running. A zombie, which has
// ended and only waits to be reaped, does not count.
func alive(pid int) bool {
	if err := syscall.Kill(pid, 0); errors.Is(err, syscall.ESRCH) {
		return false
	}
	if runtime.GOOS == "linux" {
		stat, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
		if err != nil {
			return false
		}
		if st, ok := parseStat(stat); ok && st.state == 'Z' {
			return false
		}
	}
	return true
}

// eventually waits, for as short a time as it takes and for ten seconds
// at the most, until cond holds.
func eventually(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("gave up waiting: %s", what)
		}
		time.Sleep(2 * time.Millisecond)
	}
}

// allGone waits until none of the processes is running any more.
func allGone(t *testing.T, pids map[string]int) {
	t.Helper()
	for name, pid := range pids {
		eventually(t, fmt.Sprintf("%s (pid %d) to be gone", name, pid), func() bool { return !alive(pid) })
	}
}

var pidLine = regexp.MustCompile(`(?m)^(\w+)=(\d+)$`)

// pidsIn finds the "name=pid" lines a test command printed.
func pidsIn(text string) map[string]int {
	pids := make(map[string]int)
	for _, m := range pidLine.FindAllStringSubmatch(text, -1) {
		pid, _ := strconv.Atoi(m[2])
		pids[m[1]] = pid
	}
	return pids
}

// collector gathers the live output of tool calls.
type collector struct {
	mu     sync.Mutex
	stdout strings.Builder
	status []string
	seen   chan struct{} // receives a token whenever something arrives
}

func newCollector() *collector {
	return &collector{seen: make(chan struct{}, 1)}
}

// take is the Progress function to give a tool environment.
func (c *collector) take(p core.Progress) {
	c.mu.Lock()
	if p.Stream == "stdout" {
		c.stdout.WriteString(p.Text)
	} else {
		c.status = append(c.status, p.Stream+": "+p.Text)
	}
	c.mu.Unlock()
	select {
	case c.seen <- struct{}{}:
	default:
	}
}

// text returns the output gathered so far.
func (c *collector) text() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.stdout.String()
}

// waitFor blocks until the gathered output contains want.
func (c *collector) waitFor(t *testing.T, want string) {
	t.Helper()
	deadline := time.After(10 * time.Second)
	for !strings.Contains(c.text(), want) {
		select {
		case <-c.seen:
		case <-deadline:
			t.Fatalf("gave up waiting for %q in the live output; got %q", want, c.text())
		}
	}
}
