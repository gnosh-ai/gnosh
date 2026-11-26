package ui

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/gnosh-ai/gnosh/internal/agent"
	"github.com/gnosh-ai/gnosh/internal/app"
	"github.com/gnosh-ai/gnosh/internal/app/apptest"
	"github.com/gnosh-ai/gnosh/internal/term"
	"github.com/gnosh-ai/gnosh/internal/term/vtest"
)

// helperVar is the variable that makes the test binary run as gnosh. Its
// name does not begin with GNOSH_: gnosh refuses to start with a variable
// of that family it does not know.
const helperVar = "UI_TEST_RUN_AS"

// TestMain lets the test binary stand in for gnosh itself, with this
// interface as its front end, when a test starts it inside a real
// terminal (see TestInTmux).
func TestMain(m *testing.M) {
	if os.Getenv(helperVar) == "gnosh" {
		os.Exit(helperGnosh())
	}
	os.Exit(m.Run())
}

// helperGnosh is the program as its main would start it, but for the
// GnoVM worker and the features: the process's own streams, environment
// and signals, and the full interface where there is a terminal for it.
func helperGnosh() int {
	signals := make(chan os.Signal, 8)
	signal.Notify(signals, os.Interrupt, syscall.SIGTERM, syscall.SIGHUP)
	cwd, _ := os.Getwd()
	return app.Main(context.Background(), app.Options{
		Stdin:            os.Stdin,
		Stdout:           os.Stdout,
		Stderr:           os.Stderr,
		StdinIsTerminal:  term.IsTerminal(os.Stdin),
		StdoutIsTerminal: term.IsTerminal(os.Stdout),
		Environ:          os.Environ(),
		Cwd:              cwd,
		Signals:          signals,
		Exit:             os.Exit,
		BuildID:          "tmux-test",
		Interactive:      New(Options{In: os.Stdin, Out: os.Stdout, Getenv: os.Getenv}),
	})
}

// buffer is a buffer several goroutines may write to and read.
type buffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *buffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *buffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

// program is the real program running with this interface on a fake
// terminal, against a model server that answers from a script.
type program struct {
	*loop
	w    *apptest.World
	errs *buffer
	at   *clock
}

// runProgram starts gnosh in the world's folder.
func runProgram(t *testing.T, w *apptest.World, args ...string) *program {
	t.Helper()
	p := &program{
		loop: &loop{t: t, ft: newFakeTerm(100, 40, vtest.AllAnswers), done: make(chan int, 1)},
		w:    w, errs: &buffer{}, at: &clock{at: stamp},
	}
	signals := make(chan os.Signal, 4)
	o := app.Options{
		Args:            args,
		Stdin:           strings.NewReader(""),
		Stdout:          io.Discard,
		Stderr:          p.errs,
		StdinIsTerminal: true, StdoutIsTerminal: true,
		Environ:   w.Env(),
		Cwd:       w.Dir,
		Signals:   signals,
		BuildID:   "test-build",
		StopGrace: 3 * time.Second,
		ModelWait: 5 * time.Second,
		Retry: agent.RetryPolicy{
			Pauses:          []time.Duration{10 * time.Millisecond},
			GoneAfter:       2 * time.Second,
			NotRunningPause: 10 * time.Millisecond,
			NotRunningAfter: 2 * time.Second,
			BusyMax:         20 * time.Millisecond,
		},
		Interactive: New(Options{
			Open:  func(h TerminalHooks) (Terminal, error) { p.ft.hooks = h; return p.ft, nil },
			Frame: time.Millisecond,
			Now:   p.at.Now,
		}),
	}
	go func() { p.done <- app.Main(context.Background(), o) }()
	t.Cleanup(func() {
		select {
		case code := <-p.done:
			p.done <- code
		default:
			// Still running: leave as a person would.
			p.keys(`<Ctrl+U> <Ctrl+D> <Ctrl+D>`)
			select {
			case <-p.done:
			case <-time.After(20 * time.Second):
				t.Errorf("the program did not end. stderr:\n%s", p.errs.String())
			}
		}
	})
	return p
}

// The real program with this interface: a prompt is typed and answered, a
// tool call asks for permission with the change in front of the person
// and runs when they say yes, and the way out says how to come back.
func TestWithTheProgram(t *testing.T) {
	w := apptest.NewWorld(t,
		apptest.Reply{Thinking: "It is a sum.", Text: "The answer is 4.\n\n- one\n- two\n"},
		apptest.Reply{Calls: []apptest.Call{{ID: "c1", Name: "Write", Args: `{"file_path":"made.txt","content":"made by the model\n"}`}}},
		apptest.Reply{Text: "The file is written."},
		apptest.Reply{Calls: []apptest.Call{{ID: "c2", Name: "Read", Args: `{"file_path":"made.txt"}`}}},
		apptest.Reply{Text: "It holds one line."},
	)
	p := runProgram(t, w)
	p.has("GNOSH test-build", "test-model", "manual mode", "idle")

	p.keys(`"what is 2+2?" <Enter>`)
	p.has("> what is 2+2?", "thought for", "The answer is 4.", "one", "two", "done in")

	p.keys(`"write the file" <Enter>`)
	p.has("Allow Write?", "made.txt", "+ made by the model", "Yes, this once")
	if w.Exists("made.txt") {
		t.Fatal("the file was written before the question was answered")
	}
	p.at.add(2 * time.Second)
	p.keys(`<Enter>`)
	p.has("? Allow Write? Yes, this once", "The file is written.")
	if got := w.Read("made.txt"); got != "made by the model\n" {
		t.Fatalf("the file holds %q", got)
	}

	p.keys(`"read it back" <Enter>`)
	p.has("Read made.txt", "It holds one line.")

	// A slash command of the program's own, and the keys.
	p.keys(`"/status" <Enter>`)
	p.has("> /status")
	p.keys(`<Shift+Tab>`).has("accept edits")

	p.keys(`<Ctrl+D> <Ctrl+D>`)
	if code := p.end(); code != 0 {
		t.Fatalf("exit code %d\nstderr:\n%s", code, p.errs.String())
	}
	text := p.ft.text()
	if !strings.Contains(text, "To resume this session: gnosh -r ") {
		t.Errorf("the way back is not said:\n%s", text)
	}
	for _, once := range []string{"The answer is 4.", "The file is written.", "> what is 2+2?", "It holds one line."} {
		if n := strings.Count(text, once); n != 1 {
			t.Errorf("%q is on the terminal %d times:\n%s", once, n, text)
		}
	}
	p.ft.look(func(scr *vtest.Screen) {
		if scr.AltScreenEver() || scr.MouseEver() {
			t.Error("the alternate screen or the mouse was asked for")
		}
		if u := scr.Unhandled(); len(u) > 0 {
			t.Errorf("the terminal was sent sequences it does not know: %q", u)
		}
	})
	if errs := p.errs.String(); strings.Contains(errs, "panic") || strings.Contains(errs, "crash") {
		t.Errorf("stderr:\n%s", errs)
	}
}

// The real program with a model server that fails and comes back: the
// wait is shown with its reason, and the answer arrives all the same.
// Then Esc stops a turn that waits on the model.
func TestWithAFailingServer(t *testing.T) {
	hold := make(chan struct{})
	w := apptest.NewWorld(t,
		apptest.Reply{Status: 503, Body: `{"error":{"message":"Loading model","code":503}}`},
		apptest.Reply{Text: "Here after all."},
		apptest.Reply{Text: "This one never ends.", Hold: hold},
	)
	defer close(hold)
	p := runProgram(t, w)
	p.has("manual mode")
	p.keys(`"hello" <Enter>`)
	p.has("warning:", "answers again", "Here after all.", "done in")

	p.keys(`"wait for it" <Enter>`)
	p.has("This one")
	p.keys(`"typed meanwhile" <Esc>`)
	p.has("stopped by you")
	text := p.ft.text()
	if strings.Contains(text, "context canceled") {
		t.Errorf("a stopped turn says more than that it was stopped:\n%s", text)
	}
	p.has("typed meanwhile")
}

// panes is a tmux server of a test's own.
type panes struct {
	t    *testing.T
	tmux func(args ...string) string
	self string
}

// pane is one pane of it, with gnosh running in it.
type pane struct {
	t    *testing.T
	tmux func(args ...string) string
	name string
}

// startPanes starts a tmux server for a test. The test is skipped unless
// GNOSH_UI_TMUX=1 and tmux is installed.
func startPanes(t *testing.T) *panes {
	t.Helper()
	if os.Getenv("GNOSH_UI_TMUX") != "1" {
		t.Skip("set GNOSH_UI_TMUX=1 to try the interface in tmux")
	}
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("tmux is not installed")
	}
	socket := fmt.Sprintf("gnosh-ui-%d", os.Getpid())
	tmux := func(args ...string) string {
		cmd := exec.Command("tmux", append([]string{"-L", socket, "-f", "/dev/null"}, args...)...)
		cmd.Env = append(os.Environ(), "LC_ALL=C.UTF-8", "TERM=xterm-256color")
		out, _ := cmd.CombinedOutput()
		return strings.TrimRight(string(out), "\n")
	}
	t.Cleanup(func() { tmux("kill-server") })
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	return &panes{t: t, tmux: tmux, self: self}
}

// gnosh is the command line that runs gnosh in a world, with the world's
// environment, what the test adds to it, and nothing else.
func (ps *panes) gnosh(w *apptest.World, env ...string) string {
	command := "env -i"
	for _, kv := range append(append(w.Env(), env...), helperVar+"=gnosh") {
		command += " '" + strings.ReplaceAll(kv, "'", `'\''`) + "'"
	}
	return command + " '" + ps.self + "'"
}

// start runs gnosh in a pane of its own, in the world's folder. It runs
// under a shell that says how it ended and then stays, so that the pane
// can be looked at afterwards. (tmux's own record of a pane's exit status
// is not always there.)
func (ps *panes) start(w *apptest.World, name string, env ...string) *pane {
	ps.tmux("new-session", "-d", "-s", name, "-x", "90", "-y", "30", "-c", w.Dir,
		ps.gnosh(w, env...)+"; echo \"gnosh ended with $?\"; exec sleep 3600")
	return &pane{t: ps.t, tmux: ps.tmux, name: name}
}

// shell starts a shell with job control in a pane of its own, as a person
// has one, in the world's folder. Its prompt is "shell:". gnosh is
// started in it by typing its command line.
func (ps *panes) shell(w *apptest.World, name string) *pane {
	ps.tmux("new-session", "-d", "-s", name, "-x", "90", "-y", "30", "-c", w.Dir,
		`env -i TERM="$TERM" PATH="$PATH" PS1='shell: ' bash --norc --noprofile -i`)
	return &pane{t: ps.t, tmux: ps.tmux, name: name}
}

// screen is everything the pane shows and has shown.
func (p *pane) screen() string { return p.tmux("capture-pane", "-p", "-t", p.name, "-S", "-") }

// see waits until the pane shows each piece.
func (p *pane) see(pieces ...string) {
	p.t.Helper()
	for _, piece := range pieces {
		p.until(fmt.Sprintf("tmux shows %q", piece), func() bool { return strings.Contains(p.screen(), piece) })
	}
}

// until waits for ok to come true.
func (p *pane) until(what string, ok func() bool) {
	p.t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for !ok() {
		if time.Now().After(deadline) {
			p.t.Fatalf("it never came about that %s. tmux shows:\n%s", what, p.screen())
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// send types into the pane: words as they are, and keys by tmux's names.
func (p *pane) send(words string, keys ...string) {
	if words != "" {
		p.tmux("send-keys", "-t", p.name, "-l", words)
	}
	if len(keys) > 0 {
		p.tmux(append([]string{"send-keys", "-t", p.name}, keys...)...)
	}
}

// raw reports whether the pane's terminal is in raw mode, as stty reads
// its settings, and whether that could be told at all.
func (p *pane) raw() (raw, known bool) {
	out, err := exec.Command("stty", "-F", p.says("#{pane_tty}"), "-a").CombinedOutput()
	if err != nil {
		return false, false
	}
	return strings.Contains(string(out), "-icanon") && strings.Contains(string(out), "-echo "), true
}

// says is what tmux says about the pane, for a format such as
// "#{pane_dead}".
func (p *pane) says(format string) string {
	return p.tmux("display-message", "-p", "-t", p.name, format)
}

// ended waits for gnosh to end and returns its exit code, as the shell
// that ran it reports it.
func (p *pane) ended() string {
	p.t.Helper()
	const says = "gnosh ended with "
	p.see(says)
	_, code, _ := strings.Cut(p.screen(), says)
	code, _, _ = strings.Cut(code, "\n")
	return strings.TrimSpace(code)
}

// pid is the process id of gnosh in the pane: the one child of the
// pane's shell.
func (p *pane) pid() string {
	p.t.Helper()
	out, err := exec.Command("pgrep", "-P", p.says("#{pane_pid}")).Output()
	pids := strings.Fields(string(out))
	if err != nil || len(pids) != 1 {
		p.t.Fatalf("the pane's shell has %d children (%v), and gnosh should be the one", len(pids), err)
	}
	return pids[0]
}

// TestInTmux runs the program inside tmux, a real terminal emulator: a
// prompt is typed and answered, a tool runs, the window is resized with a
// draft in the box, and the session is left. What tmux shows is checked.
// It runs only with GNOSH_UI_TMUX=1, on a machine with tmux, and uses a
// tmux server of its own.
func TestInTmux(t *testing.T) {
	ps := startPanes(t)
	w := apptest.NewWorld(t,
		apptest.Reply{Thinking: "It is a sum.", Text: "The answer is 4.\n\nIt is a longer answer too, long enough to be wrapped when the window is narrow, so that wrapping is seen.\n"},
		apptest.Reply{Calls: []apptest.Call{{ID: "c1", Name: "Read", Args: `{"file_path":"a.txt"}`}}},
		apptest.Reply{Text: "The file holds three lines."},
	)
	w.Write("a.txt", "one\ntwo\nthree\n")
	editor := w.Script(w.Home+"/editor.sh", `printf '\nfrom the editor\n' >> "$1"`)
	p := ps.start(w, "main", "TERM=tmux-256color", "EDITOR="+editor)
	p.see("GNOSH tmux-test", "manual mode", "idle")
	p.send("what is 2+2?", "Enter")
	p.see("> what is 2+2?", "The answer is 4.", "done in")
	p.send("read the file", "Enter")
	p.see("Read a.txt", "3 lines", "The file holds three lines.")

	for _, flag := range []string{"#{alternate_on}", "#{mouse_any_flag}"} {
		if got := p.says(flag); got != "0" {
			t.Errorf("tmux says %s is %s", flag, got)
		}
	}
	if raw, known := p.raw(); known && !raw {
		t.Error("the terminal is not in raw mode while gnosh has it")
	}
	// Narrower and wider again, with something typed in the box. tmux
	// tells a program of a second resize a moment after the first, so each
	// is waited for: the status line names more as the window gets wider.
	wide := "manual mode · test-model · thinking on"
	p.see(wide)
	p.send("a draft")
	p.tmux("resize-window", "-t", "main", "-x", "50", "-y", "20")
	p.see("> a draft")
	p.until("the status line follows the window to 50 columns", func() bool { return !strings.Contains(p.screen(), wide) })
	p.tmux("resize-window", "-t", "main", "-x", "110", "-y", "35")
	p.see(wide)
	p.send(" goes on")
	p.see("> a draft goes on")
	shown := p.screen()
	for _, once := range []string{"The file holds three lines.", "> what is 2+2?", "Read a.txt"} {
		if n := strings.Count(shown, once); n != 1 {
			t.Errorf("%q is on the terminal %d times after the resizes:\n%s", once, n, shown)
		}
	}
	if n := strings.Count(shown, "manual mode"); n != 1 {
		t.Errorf("the status line is on the terminal %d times after the resizes:\n%s", n, shown)
	}

	// The person's own editor gets the terminal and gives it back: what
	// it saved is in the box, and the screen is as it was.
	p.send("", "C-g")
	p.see("  from the editor")
	shown = p.screen()
	for _, once := range []string{"The file holds three lines.", "> a draft goes on", "manual mode"} {
		if n := strings.Count(shown, once); n != 1 {
			t.Errorf("%q is on the terminal %d times after the editor:\n%s", once, n, shown)
		}
	}
	if raw, known := p.raw(); known && !raw {
		t.Error("the terminal is not in raw mode after the editor gave it back")
	}

	// Ctrl+Z here, where gnosh is the pane's own program and no shell
	// could bring it back, stops nothing and says so.
	p.send("", "C-z")
	p.see("it was not stopped")
	if n := strings.Count(p.screen(), "manual mode"); n != 1 {
		t.Errorf("the status line is on the terminal %d times after a Ctrl+Z that stopped nothing:\n%s", n, p.screen())
	}

	// Out: Ctrl+C clears the box, then Ctrl+D twice.
	p.send("", "C-c")
	p.until("the box is empty", func() bool { return !strings.Contains(p.screen(), "from the editor") })
	p.send("", "C-d", "C-d")
	if code := p.ended(); code != "0" {
		t.Errorf("gnosh ended with exit code %s:\n%s", code, p.screen())
	}
	end := p.screen()
	if !strings.Contains(end, "To resume this session: gnosh -r ") || strings.Contains(end, "manual mode") {
		t.Errorf("after leaving tmux shows:\n%s", end)
	}
	if raw, known := p.raw(); known && raw {
		t.Error("the terminal was left in raw mode")
	}
}

// TestInTmuxStopping runs the program inside tmux through what stops
// things: a question that is answered, Esc on a reply that never ends,
// and a signal that ends the session, after which the terminal is an
// ordinary one again.
func TestInTmuxStopping(t *testing.T) {
	ps := startPanes(t)
	hold := make(chan struct{})
	defer close(hold)
	w := apptest.NewWorld(t,
		apptest.Reply{Calls: []apptest.Call{{ID: "c1", Name: "Write", Args: `{"file_path":"made.txt","content":"made by the model\n"}`}}},
		apptest.Reply{Text: "The file is written."},
		apptest.Reply{Text: "This reply begins and never ends.", Hold: hold},
		apptest.Reply{Text: "Nor does the one after it.", Hold: hold},
	)
	p := ps.start(w, "stop")
	p.see("manual mode", "idle")
	p.send("write the file", "Enter")
	p.see("Allow Write?", "+ made by the model", "Yes, this once")
	if w.Exists("made.txt") {
		t.Fatal("the file was written before the question was answered")
	}
	// The question takes keys once it has been read: a moment after it
	// was drawn, with the keyboard quiet. An Enter pressed at once goes
	// to the prompt box, where it does nothing.
	p.send("", "Enter")
	for quiet := time.Now(); time.Since(quiet) < guardShown+guardPause+300*time.Millisecond; {
		time.Sleep(20 * time.Millisecond)
	}
	if shown := p.screen(); !strings.Contains(shown, "Yes, this once") || strings.Contains(shown, "? Allow Write?") {
		t.Fatalf("an Enter pressed before the question could be read answered it:\n%s", shown)
	}
	p.send("", "Enter")
	p.see("? Allow Write? Yes, this once", "The file is written.", "done in")
	if got := w.Read("made.txt"); got != "made by the model\n" {
		t.Fatalf("the file holds %q", got)
	}

	p.send("go on for ever", "Enter")
	p.see("This reply")
	p.send("kept in the box", "Escape")
	p.see("stopped by you", "> kept in the box")
	if shown := p.screen(); strings.Contains(shown, "context canceled") {
		t.Errorf("a stopped turn says more than that it was stopped:\n%s", shown)
	}

	// SIGINT from outside while a turn runs stops the turn, and the
	// session goes on: the terminal, which was put back as it was found
	// when the signal came, is gnosh's again.
	pid := p.pid()
	p.send("", "C-u")
	p.send("and once more", "Enter")
	p.see("Nor does")
	if out, err := exec.Command("kill", "-INT", pid).CombinedOutput(); err != nil {
		t.Fatalf("kill -INT %s: %v %s", pid, err, out)
	}
	p.until("the second turn is stopped too", func() bool { return strings.Count(p.screen(), "stopped by you") == 2 })
	p.send("typed after the signal")
	p.see("> typed after the signal")
	if raw, known := p.raw(); known && !raw {
		t.Error("after a SIGINT that stopped a turn the terminal is not in raw mode")
	}
	if shown := p.screen(); strings.Count(shown, "manual mode") != 1 || strings.Contains(shown, "gnosh ended") {
		t.Errorf("after a SIGINT that stopped a turn tmux shows:\n%s", shown)
	}

	// A signal from outside that ends the session ends it in order.
	if out, err := exec.Command("kill", "-TERM", pid).CombinedOutput(); err != nil {
		t.Fatalf("kill -TERM %s: %v %s", pid, err, out)
	}
	if code := p.ended(); code != "143" {
		t.Errorf("after SIGTERM gnosh ended with exit code %s", code)
	}
	end := p.screen()
	if !strings.Contains(end, "To resume this session: gnosh -r ") || strings.Contains(end, "manual mode") {
		t.Errorf("after SIGTERM tmux shows:\n%s", end)
	}
	if raw, known := p.raw(); known && raw {
		t.Error("after SIGTERM the terminal was left in raw mode")
	}
}

// TestInTmuxSuspend runs the program from a shell with job control inside
// tmux: Ctrl+Z gives the terminal back to the shell and stops gnosh, with
// nothing of the live region left behind, and fg brings it back with the
// draft still in the box.
func TestInTmuxSuspend(t *testing.T) {
	ps := startPanes(t)
	if _, err := exec.LookPath("bash"); err != nil {
		t.Skip("bash is not installed")
	}
	w := apptest.NewWorld(t, apptest.Reply{Text: "Four."})
	p := ps.shell(w, "job")
	p.see("shell:")
	p.send(ps.gnosh(w), "Enter")
	p.see("manual mode", "idle")
	p.send("what is 2+2?", "Enter")
	p.see("Four.", "done in")
	p.send("a draft")
	p.see("> a draft")

	prompted := func() bool { return strings.HasSuffix(strings.TrimRight(p.screen(), " \n"), "shell:") }
	p.send("", "C-z")
	p.see("Stopped")
	p.until("the shell's prompt is back", prompted)
	if shown := p.screen(); strings.Contains(shown, "manual mode") || strings.Contains(shown, "> a draft") {
		t.Errorf("gnosh is stopped and its live region is still on the terminal:\n%s", shown)
	}
	p.send("echo the shell has the terminal", "Enter")
	p.until("the shell ran a command", func() bool { return strings.Count(p.screen(), "the shell has the terminal") == 2 && prompted() })

	p.send("fg", "Enter")
	p.see("manual mode", "> a draft")
	p.send(" goes on")
	p.see("> a draft goes on")
	shown := p.screen()
	if os.Getenv("GNOSH_UI_SHOW") != "" {
		t.Logf("after Ctrl+Z and fg, tmux shows:\n%s", shown)
	}
	for _, once := range []string{"Four.", "> what is 2+2?", "manual mode", "> a draft"} {
		if n := strings.Count(shown, once); n != 1 {
			t.Errorf("%q is on the terminal %d times after gnosh was brought back:\n%s", once, n, shown)
		}
	}

	p.send("", "C-u", "C-d", "C-d")
	p.see("To resume this session: gnosh -r ")
	p.until("the shell's prompt is back", prompted)
	p.send("echo gnosh left with $?", "Enter")
	p.see("gnosh left with 0")
	if shown := p.screen(); strings.Contains(shown, "manual mode") {
		t.Errorf("after leaving tmux shows:\n%s", shown)
	}
}

// With files that Write changed since a prompt, the menu offers to put
// them back, with the conversation or alone; the first choice does both.
func TestRewindFilesWithTheProgram(t *testing.T) {
	w := apptest.NewWorld(t,
		apptest.Reply{Text: "Nothing to do yet."},
		apptest.Reply{Calls: []apptest.Call{{ID: "c1", Name: "Write", Args: `{"file_path":"made.txt","content":"made\n"}`}}},
		apptest.Reply{Text: "The file is made."},
	)
	p := runProgram(t, w, "--permission-mode", "acceptEdits")
	p.has("idle")
	p.keys(`"hello" <Enter>`)
	p.has("Nothing to do yet.")
	p.eventually("the first turn has ended", func(s string) bool { return strings.Count(s, "done in") >= 1 })
	p.has("idle")
	p.keys(`"make the file" <Enter>`)
	p.has("The file is made.")
	p.eventually("the second turn has ended", func(s string) bool { return strings.Count(s, "done in") >= 2 })
	p.has("idle")
	if !w.Exists("made.txt") {
		t.Fatal("the model's Write did not make the file")
	}
	p.keys(`<Esc> <Esc>`)
	p.has("Go back to before which prompt?", "make the file")
	p.keys(`<Enter>`)
	p.has("Go back to before: make the file", "1 file changed since then: made.txt",
		"Go back, and put the files back", "Go back, the conversation only", "Put the files back only")
	p.keys(`<Enter>`)
	p.has("The conversation is back to before this prompt of yours", "Put back: ")
	if w.Exists("made.txt") {
		t.Error("the file Write made after that prompt is still there after going back with the files")
	}
	// The prompt is back in the box; Ctrl+C clears it, and then Ctrl+D
	// twice leaves.
	p.has("> make the file")
	p.keys(`<Ctrl+C> <Ctrl+D> <Ctrl+D>`)
	if code := p.end(); code != 0 {
		t.Errorf("exit code %d", code)
	}
}

// The real program with this interface: Esc twice in an empty box lists
// the prompts sent, and going back to before one takes the conversation
// there, with the prompt in the box again to change and send.
func TestRewindWithTheProgram(t *testing.T) {
	w := apptest.NewWorld(t,
		apptest.Reply{Text: "First answer."}, apptest.Reply{Text: "Second answer."}, apptest.Reply{Text: "Third answer."},
		apptest.Reply{Text: "The changed prompt is answered."},
	)
	p := runProgram(t, w)
	p.has("manual mode", "idle")
	for i, prompt := range []string{"the first prompt", "the second prompt", "the third prompt"} {
		p.keys(`"` + prompt + `" <Enter>`)
		p.has([]string{"First answer.", "Second answer.", "Third answer."}[i])
		// And then the turn over, by its closing line and the status line
		// at rest: a prompt typed while the turn is still ending would
		// wait in the queue, and the conversation would not be the one
		// this test goes back in. (On a busy machine that happened once
		// in some hundreds of runs.)
		p.eventually(fmt.Sprintf("turn %d has ended", i+1), func(s string) bool { return strings.Count(s, "done in") > i })
		p.has("idle")
	}

	// Never mind, at either step, changes nothing.
	p.keys(`<Esc> <Esc>`)
	p.has("Go back to before which prompt?", "the third prompt", "Never mind")
	p.keys(`<Esc>`)
	p.keys(`<Esc> <Esc>`)
	p.has("Go back to before which prompt?")
	p.keys(`<Down> <Enter>`)
	p.has("Go back to before: the second prompt", "has changed since then", "Go back, the conversation only", "Summarize from here", "Summarize up to here")
	p.keys(`<Esc>`)

	// And for real: the second prompt, the middle of the three.
	p.keys(`"/rewind" <Enter>`)
	p.has("Go back to before which prompt?")
	p.keys(`<Down> <Enter>`)
	p.has("Go back to before: the second prompt")
	p.keys(`<Enter>`)
	p.has("The conversation is back to before this prompt of yours", "> the second prompt")
	p.keys(`", changed" <Enter>`)
	p.has("> the second prompt, changed", "The changed prompt is answered.")

	reqs := w.Model.Requests()
	if len(reqs) != 4 {
		t.Fatalf("the model was sent %d requests, want 4", len(reqs))
	}
	sent := string(reqs[3].Body)
	for _, want := range []string{"the first prompt", "First answer.", "the second prompt, changed"} {
		if !strings.Contains(sent, want) {
			t.Errorf("after the rewind the model was not sent %q", want)
		}
	}
	for _, gone := range []string{"Second answer.", "the third prompt", "Third answer."} {
		if strings.Contains(sent, gone) {
			t.Errorf("after the rewind the model was still sent %q", gone)
		}
	}
	p.keys(`<Ctrl+D> <Ctrl+D>`)
	if code := p.end(); code != 0 {
		t.Fatalf("exit code %d\nstderr:\n%s", code, p.errs.String())
	}
}

// The keys of the queue, with the real program: Alt+Up takes back the one
// prompt queued last and leaves the others, and Ctrl+X Ctrl+S (Ctrl+Enter
// where the terminal tells it from Enter) has the model read the queue
// now: here the model is writing, which cannot be moved aside, so the
// turn is stopped and the queue starts the next one.
func TestQueueKeysWithTheProgram(t *testing.T) {
	hold := make(chan struct{})
	defer close(hold)
	w := apptest.NewWorld(t,
		apptest.Reply{Text: "A reply that is held back.", Hold: hold},
		apptest.Reply{Text: "The queued prompt is answered."},
	)
	p := runProgram(t, w)
	p.has("manual mode", "idle")
	p.keys(`"start" <Enter>`)
	p.has("A reply")
	p.keys(`"queued one" <Enter> "queued two" <Enter>`)
	p.has("queued one", "queued two")
	p.keys(`<Alt+Up>`)
	p.has("> queued two")
	// Taken back, it is not sent: the box is emptied.
	p.keys(`<Ctrl+U>`)
	p.keys(`<Ctrl+X> <Ctrl+S>`)
	p.has("stopped by you", "> queued one", "The queued prompt is answered.")
	reqs := w.Model.Requests()
	if len(reqs) != 2 {
		t.Fatalf("the model was sent %d requests, want 2", len(reqs))
	}
	if sent := string(reqs[1].Body); !strings.Contains(sent, "queued one") || strings.Contains(sent, "queued two") {
		t.Errorf("after one prompt was taken back and the queue sent at once, the model was sent:\n%s", sent)
	}
	p.keys(`<Ctrl+D> <Ctrl+D>`)
	if code := p.end(); code != 0 {
		t.Fatalf("exit code %d\nstderr:\n%s", code, p.errs.String())
	}
}

// /clear typed while the model writes, with the real program, does not
// wait for the turn: the turn is stopped first, then the new session
// opens, and the prompt that was queued is back in the box, sent only in
// the new session (Jae, 26 November 2026: "/clear doesn't work when it's
// thinking on a previous prompt... i have to wait. i don't want to").
func TestClearWhileTheModelWritesWithTheProgram(t *testing.T) {
	hold := make(chan struct{})
	defer close(hold)
	w := apptest.NewWorld(t,
		apptest.Reply{Text: "A reply that is held back.", Hold: hold},
		apptest.Reply{Text: "The new session's answer."},
	)
	p := runProgram(t, w)
	p.has("manual mode", "idle")
	p.keys(`"the held prompt" <Enter>`)
	p.has("A reply")
	p.keys(`"the queued prompt" <Enter>`)
	p.has("the queued prompt")
	p.keys(`"/clear" <Enter>`)
	p.has("stopped by you", "A new session is open")
	if text := p.ft.text(); strings.Index(text, "stopped by you") > strings.Index(text, "A new session is open") {
		t.Errorf("the new session opened before the turn had stopped:\n%s", text)
	}
	p.has("> the queued prompt")
	p.keys(`<Enter>`)
	p.has("The new session's answer.")
	reqs := w.Model.Requests()
	if len(reqs) != 2 {
		t.Fatalf("the model was sent %d requests, want 2", len(reqs))
	}
	if sent := string(reqs[1].Body); !strings.Contains(sent, "the queued prompt") || strings.Contains(sent, "the held prompt") {
		t.Errorf("the new session's first request was:\n%s", sent)
	}
	p.keys(`<Ctrl+D> <Ctrl+D>`)
	if code := p.end(); code != 0 {
		t.Fatalf("exit code %d\nstderr:\n%s", code, p.errs.String())
	}
}

// Sent at once while a command runs: the command moves to the background,
// where it goes on, and the model reads the queued prompt in the same
// turn, which is not stopped.
func TestSendAtOnceMovesACommandAside(t *testing.T) {
	w := apptest.NewWorld(t,
		apptest.Reply{Calls: []apptest.Call{{ID: "c1", Name: "Bash", Args: `{"command":"sleep 300"}`}}},
		apptest.Reply{Text: "I read what you sent, and the command goes on."},
	)
	w.Allow("Bash(sleep *)")
	p := runProgram(t, w)
	p.has("manual mode", "idle")
	p.keys(`"wait a while" <Enter>`)
	p.has("running sleep 300")
	p.keys(`"and tell me when" <Enter>`)
	p.has("and tell me when")
	p.keys(`<Ctrl+X> <Ctrl+S>`)
	p.has("I read what you sent, and the command goes on.", "1 in the background")
	if text := p.ft.text(); strings.Contains(text, "stopped by you") {
		t.Errorf("the turn was stopped:\n%s", text)
	}
	reqs := w.Model.Requests()
	if len(reqs) != 2 {
		t.Fatalf("the model was sent %d requests, want 2", len(reqs))
	}
	if sent := string(reqs[1].Body); !strings.Contains(sent, "and tell me when") || !strings.Contains(sent, "moved to the background") {
		t.Errorf("in the same turn the model was sent:\n%s", sent)
	}
	p.keys(`<Ctrl+D> <Ctrl+D>`)
	if code := p.end(); code != 0 {
		t.Fatalf("exit code %d\nstderr:\n%s", code, p.errs.String())
	}
}

// The same while a PreToolUse hook still runs: the screen shows the call
// before its command has started, and sending at once then moves the
// command aside as it starts, rather than stopping the turn.
func TestSendAtOnceWhileAHookRuns(t *testing.T) {
	w := apptest.NewWorld(t,
		apptest.Reply{Calls: []apptest.Call{{ID: "c1", Name: "Bash", Args: `{"command":"sleep 300"}`}}},
		apptest.Reply{Text: "I read what you sent, and the command goes on."},
	)
	w.Allow("Bash(sleep *)")
	w.Hook("PreToolUse", "Bash", "sleep 2")
	p := runProgram(t, w)
	p.has("manual mode", "idle")
	p.keys(`"wait a while" <Enter>`)
	// (The call is on the screen as the model writes it; "running" says
	// it has begun, and its hook runs: what is sent at once then moves
	// it to the background. On a loaded machine the keys came before.)
	p.has("running sleep 300")
	p.keys(`"and tell me when" <Enter>`)
	p.has("and tell me when")
	p.keys(`<Ctrl+X> <Ctrl+S>`)
	p.has("I read what you sent, and the command goes on.", "1 in the background")
	if text := p.ft.text(); strings.Contains(text, "stopped by you") || strings.Contains(text, "stopped before it ran") {
		t.Errorf("the turn was stopped:\n%s", text)
	}
	reqs := w.Model.Requests()
	if len(reqs) != 2 {
		t.Fatalf("the model was sent %d requests, want 2", len(reqs))
	}
	if sent := string(reqs[1].Body); !strings.Contains(sent, "and tell me when") || !strings.Contains(sent, "moved to the background") {
		t.Errorf("in the same turn the model was sent:\n%s", sent)
	}
	p.keys(`<Ctrl+D> <Ctrl+D>`)
	if code := p.end(); code != 0 {
		t.Fatalf("exit code %d\nstderr:\n%s", code, p.errs.String())
	}
}

// /model with nothing after it opens the picker over the real session:
// the server's models, the one in use marked; the one picked is switched
// to, and the next request names it.
func TestModelPickerWithTheProgram(t *testing.T) {
	w := apptest.NewWorld(t, apptest.Reply{Text: "An answer from the other model."})
	w.Model.SetModels("test-model", "other-model")
	p := runProgram(t, w)
	p.has("manual mode", "idle")
	p.keys(`"/model" <Enter>`)
	p.has("Which model? The choice is saved", "local/test-model", "in use", "local/other-model", "Keep the model as it is")
	// Its thinking next, the cursor on what a switch would take ("on").
	p.keys(`<Down> <Enter>`)
	p.has("How is local/other-model to think?", "what it would use")
	p.keys(`<Enter>`)
	p.has("The model is local/other-model", "thinking on")
	p.keys(`"which model are you?" <Enter>`)
	p.has("An answer from the other model.")
	reqs := w.Model.Requests()
	if got := reqs[len(reqs)-1].JSON["model"]; got != "other-model" {
		t.Errorf("the request after the picker names the model %v", got)
	}
	p.keys(`<Ctrl+D> <Ctrl+D>`)
	if code := p.end(); code != 0 {
		t.Fatalf("exit code %d\nstderr:\n%s", code, p.errs.String())
	}
}

// /resume with nothing after it lists the folder's other sessions, and
// the one picked is opened in place of the session that is open.
func TestResumeWithTheProgram(t *testing.T) {
	w := apptest.NewWorld(t,
		apptest.Reply{Text: "The first session's answer."}, apptest.Reply{Text: "The second session's answer."},
		apptest.Reply{Text: "Back in the first session."},
	)
	p := runProgram(t, w)
	p.has("manual mode", "idle")
	p.keys(`"the first session's prompt" <Enter>`)
	p.has("The first session's answer.")
	p.keys(`"/clear" <Enter>`)
	p.has("A new session is open")
	p.keys(`"the second session's prompt" <Enter>`)
	p.has("The second session's answer.")
	// The list of sessions opens only when no turn is running, so the
	// turn is let end first: the status line says idle again.
	p.has("idle")
	p.keys(`"/resume" <Enter>`)
	p.has("Open which session? This one is kept.", "the first session's prompt", "Never mind")
	p.keys(`<Enter>`)
	p.has("is open: the first session's prompt")
	p.keys(`"where were we?" <Enter>`)
	p.has("Back in the first session.")
	reqs := w.Model.Requests()
	sent := string(reqs[len(reqs)-1].Body)
	if !strings.Contains(sent, "The first session's answer.") || strings.Contains(sent, "The second session's answer.") {
		t.Errorf("after /resume the model was sent:\n%s", sent)
	}
	p.keys(`<Ctrl+D> <Ctrl+D>`)
	if code := p.end(); code != 0 {
		t.Fatalf("exit code %d\nstderr:\n%s", code, p.errs.String())
	}
}
