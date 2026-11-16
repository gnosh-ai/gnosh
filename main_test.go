package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/gnosh-ai/gnosh/internal/app"
	"github.com/gnosh-ai/gnosh/internal/app/apptest"
	"github.com/gnosh-ai/gnosh/internal/transcript"
)

// These tests run the real program: TestMain builds the binary from this
// directory, and each test starts it as a process of its own, in a world
// with its own home directory and a scripted model server. They are the
// same handful that internal/app runs through app.Main, here with a real
// process, real signals and a real terminal.

// binary is the gnosh binary the tests run.
var binary string

func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "gnosh-binary-")
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	binary = filepath.Join(dir, "gnosh")
	gotool, err := exec.LookPath("go")
	if err != nil {
		gotool = "/usr/local/go/bin/go"
	}
	args := []string{"build", "-o", binary}
	if raceDetector {
		args = append(args, "-race")
	}
	build := exec.Command(gotool, append(args, ".")...)
	if out, err := build.CombinedOutput(); err != nil {
		fmt.Fprintf(os.Stderr, "the gnosh binary could not be built: %v\n%s", err, out)
		os.RemoveAll(dir)
		os.Exit(1)
	}
	code := m.Run()
	os.RemoveAll(dir)
	os.Exit(code)
}

// outcome is how one run of the binary ended.
type outcome struct {
	code     int
	out, err string
}

// process is a gnosh that is running.
type process struct {
	t        *testing.T
	cmd      *exec.Cmd
	out, err *lockedBuffer
	done     chan struct{}
	state    error
}

type lockedBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (l *lockedBuffer) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.Write(p)
}

func (l *lockedBuffer) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.String()
}

// start starts the binary in the world's folder with the world's
// environment and nothing on standard input.
func start(t *testing.T, w *apptest.World, args ...string) *process {
	t.Helper()
	p := &process{t: t, cmd: exec.Command(binary, args...), out: &lockedBuffer{}, err: &lockedBuffer{}, done: make(chan struct{})}
	p.cmd.Dir = w.Dir
	p.cmd.Env = w.Env()
	p.cmd.Stdout, p.cmd.Stderr = p.out, p.err
	if err := p.cmd.Start(); err != nil {
		t.Fatal(err)
	}
	go func() {
		p.state = p.cmd.Wait()
		close(p.done)
	}()
	t.Cleanup(func() {
		select {
		case <-p.done:
		default:
			p.cmd.Process.Kill()
			<-p.done
		}
	})
	return p
}

// wait waits for the process to end, and fails the test when it does not
// within the limit.
func (p *process) wait(limit time.Duration) outcome {
	p.t.Helper()
	select {
	case <-p.done:
	case <-time.After(limit):
		p.cmd.Process.Kill()
		<-p.done
		p.t.Fatalf("gnosh did not end within %s.\nstdout:\n%s\nstderr:\n%s", limit, p.out.String(), p.err.String())
	}
	code := 0
	var exit *exec.ExitError
	if errors.As(p.state, &exit) {
		code = exit.ExitCode()
		if ws, ok := exit.Sys().(syscall.WaitStatus); ok && ws.Signaled() {
			code = -int(ws.Signal())
		}
	} else if p.state != nil {
		p.t.Fatalf("gnosh could not be waited for: %v", p.state)
	}
	return outcome{code: code, out: p.out.String(), err: p.err.String()}
}

// gnosh runs the binary to its end.
func gnosh(t *testing.T, w *apptest.World, args ...string) outcome {
	t.Helper()
	return start(t, w, args...).wait(60 * time.Second)
}

func (o outcome) wantCode(t *testing.T, code int) outcome {
	t.Helper()
	if o.code != code {
		t.Fatalf("exit code %d, want %d\nstdout:\n%s\nstderr:\n%s", o.code, code, o.out, o.err)
	}
	return o
}

// waitFor waits until ok says yes.
func waitFor(t *testing.T, what string, ok func() bool) {
	t.Helper()
	deadline := time.Now().Add(time.Minute)
	for !ok() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// gone reports whether no process has the id any more.
func gone(pid int) bool { return syscall.Kill(pid, 0) == syscall.ESRCH }

// readPid reads the process id a command wrote to a file.
func readPid(t *testing.T, w *apptest.World, name string) int {
	t.Helper()
	waitFor(t, "the command to start", func() bool { return w.Exists(name) && strings.TrimSpace(w.Read(name)) != "" })
	pid, err := strconv.Atoi(strings.TrimSpace(w.Read(name)))
	if err != nil {
		t.Fatal(err)
	}
	return pid
}

// checkSession fails the test unless the world's one session file reads
// back as a whole conversation, without mending.
func checkSession(t *testing.T, w *apptest.World) {
	t.Helper()
	sessions := w.Sessions()
	if len(sessions) != 1 {
		t.Fatalf("session files: %v", sessions)
	}
	s, err := transcript.New(w.DataDir()).OpenPath(context.Background(), sessions[0], true)
	if err != nil {
		t.Fatalf("the session cannot be opened: %v", err)
	}
	defer s.Close()
	msgs, _ := s.View(transcript.ViewOptions{})
	if err := transcript.Check(msgs); err != nil {
		t.Errorf("the session does not hold a conversation a provider accepts: %v", err)
	}
}

// The worker dispatch comes before everything: with a command line gnosh
// would refuse, an environment it would refuse and no home directory, the
// word gnovm-worker still reaches the worker, which ends for want of the
// pipes its parent hands it.
func TestWorkerDispatchComesFirst(t *testing.T) {
	cmd := exec.Command(binary, "gnovm-worker", "--no-such-flag")
	cmd.Env = []string{"GNOSH_NO_SUCH_VARIABLE=1"}
	out, err := cmd.CombinedOutput()
	var exit *exec.ExitError
	if !errors.As(err, &exit) || exit.ExitCode() != 2 {
		t.Fatalf("gnovm-worker ended with %v, want exit code 2\n%s", err, out)
	}
	if !strings.Contains(string(out), "gnovm-worker:") || !strings.Contains(string(out), "handshake") {
		t.Errorf("the worker did not speak: %s", out)
	}
	if strings.Contains(string(out), "gnosh:") {
		t.Errorf("gnosh's own startup ran before the worker: %s", out)
	}
	// The same words to the program itself are refused, as they must be.
	out, err = exec.Command(binary, "--no-such-flag").CombinedOutput()
	if !errors.As(err, &exit) || exit.ExitCode() != 1 || !strings.Contains(string(out), "--no-such-flag is not a flag of gnosh") {
		t.Errorf("gnosh --no-such-flag: %v\n%s", err, out)
	}
}

// gnosh ssh comes before everything too: every word after it is ssh's,
// gnosh flags among them, and it ends with ssh's status. Without a
// terminal it is plain ssh. -h or --help alone shows gnosh's help for it.
func TestSSHDispatchComesFirst(t *testing.T) {
	dir := t.TempDir()
	ssh := filepath.Join(dir, "ssh")
	if err := os.WriteFile(ssh, []byte("#!/bin/sh\necho \"ssh got: $*\"\nexit 5\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(binary, "ssh", "-p", "2222", "--model", "x", "box")
	cmd.Env = []string{"PATH=" + dir + ":/usr/bin:/bin"}
	out, err := cmd.CombinedOutput()
	var exit *exec.ExitError
	if !errors.As(err, &exit) || exit.ExitCode() != 5 {
		t.Fatalf("gnosh ssh ended with %v, want ssh's exit code 5\n%s", err, out)
	}
	if string(out) != "ssh got: -p 2222 --model x box\n" {
		t.Errorf("gnosh ssh: %q", out)
	}
	for _, help := range []string{"-h", "--help"} {
		cmd := exec.Command(binary, "ssh", help)
		cmd.Env = []string{"HOME=" + t.TempDir(), "PATH=" + dir + ":/usr/bin:/bin"}
		out, err := cmd.CombinedOutput()
		if err != nil || !strings.Contains(string(out), "Run ssh, putting what the far side copies") || strings.Contains(string(out), "ssh got") {
			t.Errorf("gnosh ssh %s: %v\n%s", help, err, out)
		}
	}
}

// What needs no model.
func TestVersionHelpDoctor(t *testing.T) {
	w := apptest.NewWorld(t)
	if o := gnosh(t, w, "version").wantCode(t, 0); !strings.HasSuffix(o.out, "(gnosh)\n") {
		t.Errorf("version: %q", o.out)
	}
	if o := gnosh(t, w, "--help").wantCode(t, 0); !strings.Contains(o.out, "gnosh -p [flags] [prompt]") || o.err != "" {
		t.Errorf("help: %q %q", o.out, o.err)
	}
	// The model server is there: doctor looks without disturbing it, and
	// says a turn can run.
	o := gnosh(t, w, "doctor").wantCode(t, 0)
	for _, piece := range []string{"local at " + w.Model.URL, "model: local/test-model", "A turn can run."} {
		if !strings.Contains(o.out, piece) {
			t.Errorf("doctor does not say %q:\n%s", piece, o.out)
		}
	}
	for _, path := range w.Model.Paths() {
		if path != "GET /v1/models" {
			t.Errorf("doctor asked the model server for %q: a look sends one GET of the models list and nothing else", path)
		}
	}
	// With no provider at all it says so and exits 1.
	bare := apptest.NewWorld(t)
	os.Remove(bare.SettingsFile())
	o = gnosh(t, bare, "doctor").wantCode(t, 1)
	if !strings.Contains(o.out, "no provider is configured") || !strings.Contains(o.out, "A turn cannot run") {
		t.Errorf("doctor with no provider:\n%s", o.out)
	}
}

// shipped runs the program in this process, as main does but for the
// worker dispatch and the terminal, with the features it ships with
// (features.go), and returns its exit code and what it wrote. It is for
// what the command line alone decides, where starting the binary would
// add nothing but time.
func shipped(t *testing.T, args ...string) (code int, out, errs string) {
	t.Helper()
	var stdout, stderr bytes.Buffer
	code = app.Main(context.Background(), app.Options{
		Args:     args,
		Stdout:   &stdout,
		Stderr:   &stderr,
		Environ:  []string{"HOME=" + t.TempDir()},
		Cwd:      t.TempDir(),
		Features: features(),
	})
	return code, stdout.String(), stderr.String()
}

// The command line of the program as it ships holds only what is built
// (spec/DECISIONS.md, 26): the help of gnosh, and of every command that
// help lists, marks nothing as not built, and a command or flag that was
// taken off is refused, not read as what it once was.
func TestCommandLineHoldsOnlyWhatIsBuilt(t *testing.T) {
	var titles []string
	var visit func(path ...string)
	visit = func(path ...string) {
		title := strings.Join(append([]string{"gnosh"}, path...), " ")
		titles = append(titles, title)
		code, text, errs := shipped(t, append([]string{"help"}, path...)...)
		if code != 0 || errs != "" {
			t.Fatalf("the help of %s: exit code %d, stderr %q", title, code, errs)
		}
		if strings.Contains(text, "ot built") {
			t.Errorf("the help of %s says something is not built:\n%s", title, text)
		}
		// The names under the Commands heading, each with a help of its own.
		_, list, _ := strings.Cut(text, "\nCommands\n")
		for _, line := range strings.Split(list, "\n") {
			if line == "" {
				break
			}
			if strings.HasPrefix(line, "  ") && !strings.HasPrefix(line, "   ") {
				visit(append(append([]string{}, path...), strings.Fields(line)[0])...)
			}
		}
	}
	visit()
	want := []string{
		"gnosh", "gnosh doctor", "gnosh trust", "gnosh models",
		"gnosh session", "gnosh session list", "gnosh session show", "gnosh session last", "gnosh session rename",
		"gnosh mem", "gnosh bridge", "gnosh logs", "gnosh dirs", "gnosh ssh",
		"gnosh mcp", "gnosh mcp add", "gnosh mcp add-json", "gnosh mcp get", "gnosh mcp list", "gnosh mcp remove",
		"gnosh help", "gnosh version",
	}
	if strings.Join(titles, ", ") != strings.Join(want, ", ") {
		t.Errorf("the commands help lists:\n got %s\nwant %s", strings.Join(titles, ", "), strings.Join(want, ", "))
	}

	for _, args := range [][]string{
		{"-p", "-q", "x"}, {"-s", "abc", "-p", "x"}, {"-D", t.TempDir(), "-p", "x"}, {"--max-budget-usd", "1", "-p", "x"},
		{"session", "delete", "abc"}, {"session", "web"}, {"help", "run"}, {"mcp", "serve"},
	} {
		code, out, errs := shipped(t, args...)
		if code != 1 || out != "" || (!strings.Contains(errs, "is not a flag of gnosh") && !strings.Contains(errs, "does not go with gnosh") && !strings.Contains(errs, "is not a command of gnosh")) {
			t.Errorf("gnosh %s: exit code %d, stdout %q, stderr %q", strings.Join(args, " "), code, out, errs)
		}
	}

	// The binary itself agrees, for one line of each kind.
	w := apptest.NewWorld(t)
	if o := gnosh(t, w, "help", "session").wantCode(t, 0); strings.Contains(o.out, "ot built") || !strings.Contains(o.out, "rename <id> <title...>") {
		t.Errorf("gnosh help session, from the binary:\n%s", o.out)
	}
	if o := gnosh(t, w, "-p", "-q", "x").wantCode(t, 1); !strings.Contains(o.err, "-q is not a flag of gnosh") {
		t.Errorf("gnosh -p -q x, from the binary: %s", o.err)
	}
	if n := len(w.Model.Requests()); n != 0 {
		t.Errorf("the model server was sent %d requests by a command line that was refused", n)
	}
}

// A prompt is answered, in the text and in the json format.
func TestBinaryAnswers(t *testing.T) {
	w := apptest.NewWorld(t, apptest.Reply{Text: "The answer is 4."}, apptest.Reply{Text: "Still 4."})
	o := gnosh(t, w, "-p", "what is 2+2?").wantCode(t, 0)
	if o.out != "The answer is 4.\n" || o.err != "" {
		t.Errorf("stdout %q, stderr %q", o.out, o.err)
	}
	o = gnosh(t, w, "-c", "-p", "--output-format", "json", "sure?").wantCode(t, 0)
	var obj map[string]any
	if err := json.Unmarshal([]byte(o.out), &obj); err != nil {
		t.Fatalf("stdout is not one JSON object: %v\n%s", err, o.out)
	}
	if obj["type"] != "result" || obj["subtype"] != "success" || obj["result"] != "Still 4." || obj["is_error"] != false {
		t.Errorf("result: %s", o.out)
	}
	// -c went on in the first run's session: the model was sent the
	// earlier messages again.
	second := string(w.Model.Requests()[1].Body)
	for _, piece := range []string{"what is 2+2?", "The answer is 4.", "sure?"} {
		if !strings.Contains(second, piece) {
			t.Errorf("the continued session's request does not hold %q", piece)
		}
	}
	w.Model.Done()
	checkSession(t, w)
}

// A turn that reads, changes and runs: with -y the files are changed;
// without it the change is refused, the model is told why, and the result
// lists the refusal.
func TestBinaryToolsAndPermissions(t *testing.T) {
	script := func() []apptest.Reply {
		return []apptest.Reply{
			{Calls: []apptest.Call{{ID: "c1", Name: "Read", Args: `{"file_path":"a.txt"}`}}},
			{Calls: []apptest.Call{
				{ID: "c2", Name: "Edit", Args: `{"file_path":"a.txt","old_string":"two plus","new_string":"three plus"}`},
				{ID: "c3", Name: "Bash", Args: `{"command":"echo made by bash > b.txt"}`},
			}},
			{Text: "Over."},
		}
	}
	w := apptest.NewWorld(t, script()...)
	w.Write("a.txt", "two plus two\n")
	gnosh(t, w, "-y", "-p", "change it").wantCode(t, 0)
	if w.Read("a.txt") != "three plus two\n" || w.Read("b.txt") != "made by bash\n" {
		t.Errorf("a.txt = %q, b.txt = %q", w.Read("a.txt"), w.Read("b.txt"))
	}
	checkSession(t, w)

	w = apptest.NewWorld(t, script()...)
	w.Write("a.txt", "two plus two\n")
	o := gnosh(t, w, "-p", "--output-format", "json", "change it").wantCode(t, 0)
	if w.Read("a.txt") != "two plus two\n" || w.Exists("b.txt") {
		t.Errorf("without -y: a.txt = %q, b.txt exists = %v", w.Read("a.txt"), w.Exists("b.txt"))
	}
	if !strings.Contains(o.out, `"tool_name":"Edit"`) || !strings.Contains(o.out, `"tool_name":"Bash"`) {
		t.Errorf("the result does not list the refusals: %s", o.out)
	}
	if !strings.Contains(o.err, "--allowedTools") {
		t.Errorf("the person is not told how to let a run act: %s", o.err)
	}
	if third := string(w.Model.Requests()[2].Body); !strings.Contains(third, "nobody is here to give it") {
		t.Errorf("the model was not told why its calls did not run")
	}
}

// longJob is a reply that starts a command which writes its process id
// and then sleeps.
func longJob() apptest.Reply {
	return apptest.Reply{Text: "Starting.", Calls: []apptest.Call{{ID: "c1", Name: "Bash", Args: `{"command":"echo $$ > pid.txt; sleep 300"}`}}}
}

// sessionEndHook makes a SessionEnd hook that writes the reason it was
// given to a file.
func sessionEndHook(w *apptest.World) {
	hook := w.Script(w.Home+"/hooks/end.sh", `cat > /dev/null; echo "$GNOSH_SESSION_END_REASON" > "$HOME/ended"`)
	w.Hook("SessionEnd", "", hook)
}

// Ctrl-C in the middle of a turn: the turn is cancelled, the command it
// started is stopped with everything under it, the SessionEnd hooks run,
// the session's file is whole, nothing is printed on standard output, and
// the exit code is 130.
func TestBinarySIGINT(t *testing.T) {
	w := apptest.NewWorld(t, longJob())
	sessionEndHook(w)
	p := start(t, w, "-y", "-p", "run the long job")
	pid := readPid(t, w, "pid.txt")
	p.cmd.Process.Signal(os.Interrupt)
	o := p.wait(30*time.Second).wantCode(t, 130)
	if o.out != "Starting.\n" && o.out != "" {
		t.Errorf("stdout after Ctrl-C: %q", o.out)
	}
	if strings.Contains(strings.ToLower(o.out+o.err), "context canceled") {
		t.Errorf("Ctrl-C printed an internal error: %q %q", o.out, o.err)
	}
	waitFor(t, "the command to be stopped", func() bool { return gone(pid) })
	if got := strings.TrimSpace(w.Read(w.Home + "/ended")); got != "other" {
		t.Errorf("the SessionEnd hook was told %q, want other", got)
	}
	checkSession(t, w)
}

// SIGTERM in the middle of a turn: the same, with exit code 143. In the two
// JSON formats the run still prints its result object before it goes, so
// that whatever stopped it learns what it was: the replies the model had
// written, and that the run was stopped and by what.
func TestBinarySIGTERM(t *testing.T) {
	for _, format := range []string{"stream-json", "json"} {
		t.Run(format, func(t *testing.T) {
			w := apptest.NewWorld(t, longJob())
			sessionEndHook(w)
			p := start(t, w, "-y", "-p", "--output-format", format, "run the long job")
			pid := readPid(t, w, "pid.txt")
			p.cmd.Process.Signal(syscall.SIGTERM)
			o := p.wait(30*time.Second).wantCode(t, 143)
			waitFor(t, "the command to be stopped", func() bool { return gone(pid) })
			if got := strings.TrimSpace(w.Read(w.Home + "/ended")); got != "other" {
				t.Errorf("the SessionEnd hook was told %q, want other", got)
			}
			checkSession(t, w)

			lines := strings.Split(strings.TrimRight(o.out, "\n"), "\n")
			if format == "json" && len(lines) != 1 {
				t.Errorf("the json format printed %d lines, want the result alone:\n%s", len(lines), o.out)
			}
			var result struct {
				Type           string   `json:"type"`
				Subtype        string   `json:"subtype"`
				IsError        bool     `json:"is_error"`
				NumTurns       int      `json:"num_turns"`
				Errors         []string `json:"errors"`
				TerminalReason string   `json:"terminal_reason"`
				SessionID      string   `json:"session_id"`
			}
			if err := json.Unmarshal([]byte(lines[len(lines)-1]), &result); err != nil {
				t.Fatalf("the last line of the output is not a JSON object (%v):\n%s", err, o.out)
			}
			if result.Type != "result" || result.Subtype != "error_during_execution" || !result.IsError || result.NumTurns != 1 ||
				result.TerminalReason != "aborted_tools" || result.SessionID == "" ||
				len(result.Errors) != 1 || result.Errors[0] != "the run was stopped by SIGTERM after 1 turns of the model" {
				t.Errorf("the result object of a run stopped by SIGTERM: %s", lines[len(lines)-1])
			}
		})
	}
}

// The result of a stopped run is not waited for when a second signal
// comes: a command that takes no notice of being stopped holds the turn's
// end back, and the second signal ends the program all the same.
func TestBinarySecondSignalDoesNotWaitForTheResult(t *testing.T) {
	w := apptest.NewWorld(t, apptest.Reply{Calls: []apptest.Call{
		{ID: "c1", Name: "Bash", Args: `{"command":"trap '' TERM INT HUP; echo $$ > pid.txt; sleep 300"}`},
	}})
	p := start(t, w, "-y", "-p", "--output-format", "json", "run the stubborn job")
	pid := readPid(t, w, "pid.txt")
	// Whatever the program leaves behind, the test does not.
	defer syscall.Kill(-pid, syscall.SIGKILL)
	p.cmd.Process.Signal(syscall.SIGTERM)
	time.Sleep(100 * time.Millisecond)
	began := time.Now()
	p.cmd.Process.Signal(syscall.SIGTERM)
	p.wait(10*time.Second).wantCode(t, 143)
	if took := time.Since(began); took > 1500*time.Millisecond {
		t.Errorf("the second signal took %s to end the program", took)
	}
}

// Only the first signal is handled in order. A second one, while the
// program is still shutting down, ends it at once.
func TestBinarySecondSignalDoesNotWait(t *testing.T) {
	w := apptest.NewWorld(t, longJob())
	hook := w.Script(w.Home+"/hooks/slow-end.sh", `cat > /dev/null; echo $$ > "$HOME/ending"; sleep 40`)
	w.Hook("SessionEnd", "", hook, 60)
	p := start(t, w, "-y", "-p", "run the long job")
	pid := readPid(t, w, "pid.txt")
	p.cmd.Process.Signal(os.Interrupt)
	hookPid := readPid(t, w, w.Home+"/ending")
	// The hook runs in a process group of its own. The program is about to
	// leave it behind, which is what "at once" costs; the test does not.
	defer syscall.Kill(-hookPid, syscall.SIGKILL)
	began := time.Now()
	p.cmd.Process.Signal(os.Interrupt)
	p.wait(10*time.Second).wantCode(t, 130)
	if took := time.Since(began); took > 5*time.Second {
		t.Errorf("the second signal took %s to end the program", took)
	}
	// The command the turn had started was stopped by the first signal.
	waitFor(t, "the command to be stopped", func() bool { return gone(pid) })
}

// A second signal ends the program at once, and on its way out the
// commands the session started are still told to stop: a command in the
// background is not left running with nobody to stop it.
func TestBinarySecondSignalStopsBackgroundCommands(t *testing.T) {
	w := apptest.NewWorld(t, apptest.Reply{Calls: []apptest.Call{
		{ID: "c1", Name: "Bash", Args: `{"command":"echo $$ > bg.pid; sleep 300","run_in_background":true}`},
		// A command that ignores the signal that asks it to stop: the
		// first Ctrl-C then has to wait for it, and that is when the
		// second one arrives.
		{ID: "c2", Name: "Bash", Args: `{"command":"trap '' TERM INT HUP; echo $$ > fg.pid; sleep 300"}`},
	}})
	p := start(t, w, "-y", "-p", "start both")
	bg := readPid(t, w, "bg.pid")
	fg := readPid(t, w, "fg.pid")
	// Whatever the program leaves behind, the test does not.
	defer syscall.Kill(-fg, syscall.SIGKILL)
	defer syscall.Kill(-bg, syscall.SIGKILL)
	p.cmd.Process.Signal(os.Interrupt)
	time.Sleep(300 * time.Millisecond)
	began := time.Now()
	p.cmd.Process.Signal(os.Interrupt)
	p.wait(10*time.Second).wantCode(t, 130)
	if took := time.Since(began); took > 3*time.Second {
		t.Errorf("the second signal took %s to end the program", took)
	}
	waitFor(t, "the background command to be stopped", func() bool { return gone(bg) })
}

// A build made where there is no version control to ask, as one from a
// source archive is, and named at link time: --version says the name.
func TestBinaryVersionOfANamedBuild(t *testing.T) {
	gotool, err := exec.LookPath("go")
	if err != nil {
		gotool = "/usr/local/go/bin/go"
	}
	named := filepath.Join(t.TempDir(), "gnosh")
	build := exec.Command(gotool, "build", "-buildvcs=false",
		"-ldflags", "-X github.com/gnosh-ai/gnosh/internal/buildinfo.BuildID=release-2026-11-13", "-o", named, ".")
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("the named build could not be made: %v\n%s", err, out)
	}
	out, err := exec.Command(named, "--version").CombinedOutput()
	if err != nil || string(out) != "unknown-time release-2026-11-13 (gnosh)\n" {
		t.Errorf("--version of the named build prints %q (%v)", out, err)
	}
}

// With mem and the machines joined, as the real program has them: the
// session is offered the machine tools, and a sub-agent of the stock kind
// is not, which is the join's own choice for a sub-agent.
func TestBinarySubAgentHasNoMachineTools(t *testing.T) {
	w := apptest.NewWorld(t,
		apptest.Reply{Calls: []apptest.Call{{ID: "c1", Name: "Agent", Args: `{"description":"say hello","prompt":"Say hello."}`}}},
		apptest.Reply{Text: "Hello from the sub-agent."},
		apptest.Reply{Text: "It said hello."})
	o := gnosh(t, w, "-p", "have it say hello").wantCode(t, 0)
	if o.out != "It said hello.\n" {
		t.Errorf("stdout %q, stderr %q", o.out, o.err)
	}
	reqs := w.Model.Requests()
	if len(reqs) != 3 {
		t.Fatalf("the model was sent %d requests, want 3", len(reqs))
	}
	names := func(r apptest.Request) string {
		var out []string
		list, _ := r.JSON["tools"].([]any)
		for _, t := range list {
			out = append(out, t.(map[string]any)["function"].(map[string]any)["name"].(string))
		}
		return strings.Join(out, " ")
	}
	session, sub := names(reqs[0]), names(reqs[1])
	if !strings.Contains(session, "gnovm_run") || !strings.Contains(session, "Agent") {
		t.Errorf("the session's tools are %q", session)
	}
	if strings.Contains(sub, "gnovm_") || !strings.Contains(sub, "Read") {
		t.Errorf("the stock sub-agent's tools are %q", sub)
	}
	// Only the session the person is in may put questions to them.
	if !strings.Contains(session, "AskUserQuestion") || strings.Contains(sub, "AskUserQuestion") {
		t.Errorf("the question tool: the session's tools are %q, the sub-agent's %q", session, sub)
	}
	checkSession(t, w)
}
