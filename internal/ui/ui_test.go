package ui

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/gnosh-ai/gnosh/internal/agent"
	"github.com/gnosh-ai/gnosh/internal/app"
	"github.com/gnosh-ai/gnosh/internal/core"
	"github.com/gnosh-ai/gnosh/internal/term"
	"github.com/gnosh-ai/gnosh/internal/term/vtest"
	"github.com/gnosh-ai/gnosh/internal/text"
	"github.com/gnosh-ai/gnosh/internal/ui/editor/keyscript"
)

// fakeTerm is a terminal for the interface's loop: an emulated screen to
// write to, which answers the questions a real terminal answers, and a
// channel the test types into.
type fakeTerm struct {
	mu     sync.Mutex
	scr    *vtest.Screen
	dec    term.Decoder
	events chan term.Event
	hooks  TerminalHooks

	writes                               int
	modes                                term.Modes
	released, resumed, suspended, closed int
	restored                             int
	// gate, when not nil, makes every write wait until it is closed: a
	// terminal that is slow to take what is written.
	gate chan struct{}
}

func newFakeTerm(cols, rows int, answers vtest.Answers) *fakeTerm {
	f := &fakeTerm{scr: vtest.New(cols, rows), events: make(chan term.Event, 1<<16)}
	f.scr.SetWidth(func(s string) int { return text.Width(s) })
	f.scr.SetAnswers(answers)
	return f
}

func (f *fakeTerm) Write(p []byte) (int, error) {
	f.mu.Lock()
	gate := f.gate
	f.mu.Unlock()
	if gate != nil {
		<-gate
	}
	f.mu.Lock()
	f.writes++
	n, err := f.scr.Write(p)
	evs := f.dec.Feed(f.scr.TakeReplies())
	f.mu.Unlock()
	for _, ev := range evs {
		f.events <- ev
	}
	return n, err
}

func (f *fakeTerm) Events() <-chan term.Event { return f.events }

func (f *fakeTerm) Size() (int, int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	cols, rows := f.scr.Size()
	return cols, rows, nil
}

func (f *fakeTerm) SetModes(m term.Modes) error {
	f.mu.Lock()
	old := f.modes
	f.modes = m
	f.mu.Unlock()
	seq := ""
	if m.BracketedPaste != old.BracketedPaste {
		seq += map[bool]string{true: term.BracketedPasteOn, false: term.BracketedPasteOff}[m.BracketedPaste]
	}
	if m.KittyKeyboard != old.KittyKeyboard {
		seq += map[bool]string{true: term.KittyKeyboardPush, false: term.KittyKeyboardPop}[m.KittyKeyboard]
	}
	if seq != "" {
		f.Write([]byte(seq))
	}
	return nil
}

func (f *fakeTerm) QueryCursorPosition() error {
	f.mu.Lock()
	f.dec.ExpectCursorPosition()
	f.mu.Unlock()
	_, err := f.Write([]byte(term.QueryCursorPosition))
	return err
}

func (f *fakeTerm) Release() error {
	f.mu.Lock()
	f.released++
	f.mu.Unlock()
	return nil
}

func (f *fakeTerm) Resume() error {
	f.mu.Lock()
	f.resumed++
	f.mu.Unlock()
	f.events <- term.Resume{}
	return nil
}

func (f *fakeTerm) Suspend() error {
	f.mu.Lock()
	f.suspended++
	f.mu.Unlock()
	f.events <- term.Resume{}
	return nil
}

func (f *fakeTerm) RestoreOnPanic() {
	if r := recover(); r != nil {
		f.mu.Lock()
		f.restored++
		f.mu.Unlock()
		panic(r)
	}
}

func (f *fakeTerm) Close() error {
	f.SetModes(term.Modes{})
	f.mu.Lock()
	f.closed++
	f.mu.Unlock()
	return nil
}

// look runs f with the screen, while nothing writes to it.
func (f *fakeTerm) look(see func(scr *vtest.Screen)) {
	f.mu.Lock()
	defer f.mu.Unlock()
	see(f.scr)
}

// text is every line of the terminal, as one text.
func (f *fakeTerm) text() string {
	var out string
	f.look(func(scr *vtest.Screen) { out = strings.Join(scr.AllLines(), "\n") })
	return out
}

// loop is the interface running against a fake terminal and controls
// that stand in for a session.
type loop struct {
	t    *testing.T
	ft   *fakeTerm
	c    *controls
	u    *UI
	done chan int
	// now, when not nil, is the interface's clock.
	now func() time.Time
	// editor, when not nil, stands in for the person's own editor.
	editor func(string) (string, error)
}

// clock is a clock a test moves by hand.
type clock struct {
	mu sync.Mutex
	at time.Time
}

func (c *clock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.at
}

func (c *clock) add(d time.Duration) {
	c.mu.Lock()
	c.at = c.at.Add(d)
	c.mu.Unlock()
}

// do runs f on the interface's own goroutine, where its session may be
// looked at, and waits for it.
func (l *loop) do(f func(s *session)) {
	l.t.Helper()
	ran := make(chan struct{})
	l.u.jobs <- func() { f(l.u.s); close(ran) }
	select {
	case <-ran:
	case <-time.After(20 * time.Second):
		l.t.Fatal("the loop did not run a job within 20 s")
	}
}

// startLoop makes the interface and runs it.
func startLoop(t *testing.T, cols, rows int, answers vtest.Answers, set ...func(*controls)) *loop {
	t.Helper()
	l := &loop{t: t, ft: newFakeTerm(cols, rows, answers), c: newControls(), done: make(chan int, 1)}
	for _, f := range set {
		f(l.c)
	}
	l.u = l.make()
	go func() { l.done <- l.u.Run(context.Background(), l.c) }()
	l.eventually("the prompt box is drawn", func(s string) bool { return strings.Contains(s, "manual mode") })
	return l
}

// make makes the interface without running it.
func (l *loop) make() *UI {
	maker := New(Options{
		Open:   func(h TerminalHooks) (Terminal, error) { l.ft.hooks = h; return l.ft, nil },
		Frame:  time.Millisecond,
		Now:    l.now,
		Editor: l.editor,
	})
	return maker(app.FrontendInfo{Getenv: func(string) string { return "" }}).(*UI)
}

// eventually waits until the terminal's text satisfies ok.
func (l *loop) eventually(what string, ok func(text string) bool) {
	l.t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	for {
		if ok(l.ft.text()) {
			return
		}
		if time.Now().After(deadline) {
			var dump string
			l.ft.look(func(scr *vtest.Screen) { dump = scr.Dump() })
			l.t.Fatalf("waited 20 s and it never came about that %s\n%s", what, dump)
		}
		time.Sleep(2 * time.Millisecond)
	}
}

// has waits until the terminal shows each piece.
func (l *loop) has(pieces ...string) *loop {
	l.t.Helper()
	for _, p := range pieces {
		l.eventually(fmt.Sprintf("the terminal shows %q", p), func(s string) bool { return strings.Contains(s, p) })
	}
	return l
}

// keys types a key script.
func (l *loop) keys(script string) *loop {
	l.t.Helper()
	evs, err := keyscript.Parse(script)
	if err != nil {
		l.t.Fatal(err)
	}
	for _, ev := range evs {
		l.ft.events <- ev
	}
	return l
}

// ev reports things to the interface as the app does.
func (l *loop) ev(evs ...any) *loop {
	for _, ev := range evs {
		l.u.Event(ev)
	}
	return l
}

// end waits for the loop to return and gives its exit code.
func (l *loop) end() int {
	l.t.Helper()
	select {
	case code := <-l.done:
		l.done <- code
		return code
	case <-time.After(20 * time.Second):
		l.t.Fatal("the interface did not return within 20 s")
	}
	return -1
}

// The interface turns on only what the terminal said it understands, and
// turns it all off again when it leaves.
func TestTerminalModes(t *testing.T) {
	cases := []struct {
		name          string
		answers       vtest.Answers
		paste, kitty  bool
		synchronized  bool
		beforeTimeout bool
	}{
		{"a terminal of today", vtest.AllAnswers, true, true, true, true},
		{"one that only says what it is", vtest.Answers{DeviceAttributes: true, CursorPosition: true}, true, false, false, true},
		{"one that answers nothing", vtest.Answers{}, false, false, false, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			l := startLoop(t, 80, 24, tc.answers)
			l.ft.look(func(scr *vtest.Screen) {
				if got := scr.Mode(2004); got != tc.paste {
					t.Errorf("bracketed paste is %v, want %v", got, tc.paste)
				}
				if _, depth := scr.KittyFlags(); (depth > 0) != tc.kitty {
					t.Errorf("the kitty keyboard stack is %d deep, want pushed: %v", depth, tc.kitty)
				}
				if begun, _ := scr.SyncUpdates(); (begun > 0) != tc.synchronized {
					t.Errorf("%d synchronized updates were begun, want some: %v", begun, tc.synchronized)
				}
				if scr.AltScreenEver() || scr.MouseEver() {
					t.Error("the alternate screen or the mouse was asked for")
				}
			})
			l.keys(`<Ctrl+D> <Ctrl+D>`)
			if code := l.end(); code != 0 {
				t.Fatalf("exit code %d", code)
			}
			l.ft.look(func(scr *vtest.Screen) {
				if scr.Mode(2004) {
					t.Error("bracketed paste was left on")
				}
				if _, depth := scr.KittyFlags(); depth != 0 {
					t.Error("the kitty keyboard level was left pushed")
				}
				if !scr.CursorVisible() {
					t.Error("the cursor was left hidden")
				}
				if !scr.Mode(7) {
					t.Error("line wrapping was left off")
				}
				if text := strings.Join(scr.AllLines(), "\n"); strings.Contains(text, "manual mode") || !strings.Contains(text, "GNOSH") {
					t.Errorf("the live region was not erased, or the scrollback was:\n%s", scr.Dump())
				}
			})
			if l.ft.closed != 1 {
				t.Errorf("the terminal was closed %d times", l.ft.closed)
			}
			if l.c.quit != "prompt_input_exit" {
				t.Errorf("the session was told %q", l.c.quit)
			}
		})
	}
}

// The command before gnosh left the cursor in the middle of a line: the
// interface begins on a line of its own.
func TestStartsOnAFreshLine(t *testing.T) {
	l := &loop{t: t, ft: newFakeTerm(80, 24, vtest.AllAnswers), c: newControls(), done: make(chan int, 1)}
	l.ft.scr.WriteString("no newline at the end of this")
	l.u = l.make()
	go func() { l.done <- l.u.Run(context.Background(), l.c) }()
	l.has("manual mode")
	l.ft.look(func(scr *vtest.Screen) {
		if got := scr.Line(0); got != "no newline at the end of this" {
			t.Errorf("the line that was there is now %q", got)
		}
		if got := scr.Line(1); !strings.HasPrefix(got, "GNOSH") {
			t.Errorf("the header is not on the line after it:\n%s", scr.Dump())
		}
	})
	l.keys(`<Ctrl+D> <Ctrl+D>`).end()
}

// A whole session through the loop: a prompt typed, a turn reported by
// the session from its own goroutine, and the way out, which says how to
// come back.
func TestLoopSession(t *testing.T) {
	file := filepath.Join(t.TempDir(), "session.jsonl")
	if err := os.WriteFile(file, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	l := startLoop(t, 80, 24, vtest.AllAnswers, func(c *controls) { c.info.Path = file })
	l.has("GNOSH test-build", "idle")
	l.keys(`"say hello" <Enter>`)
	l.has("queued: say hello")
	if got := l.c.sent(); len(got) != 1 || got[0] != "say hello" {
		t.Fatalf("submitted %q", got)
	}
	l.ev(busy(), turnStart(), promptOut("p1", "say hello"), request(1))
	l.has("> say hello", "* waiting for qwen3.8-27b to start its reply")
	l.ev(said("Hello "), said("there.\n\nA second "), said("paragraph.\n"), replyEnd(core.StopEndTurn), turnEnd(agent.EndCompleted), idle())
	l.has("Hello there.", "A second paragraph.", "done in 42s", "idle")
	// A paste arrives whole, as a paste.
	l.ft.events <- term.Paste{Text: "pasted\nwords"}
	l.has("pasted", "words")
	l.keys(`<Ctrl+C> <Ctrl+C>`)
	if code := l.end(); code != 0 {
		t.Fatalf("exit code %d", code)
	}
	text := l.ft.text()
	if !strings.Contains(text, "To resume this session: gnosh -r 0123abcd") {
		t.Errorf("the way back is not said:\n%s", text)
	}
	if strings.Count(text, "Hello there.") != 1 || strings.Contains(text, "manual mode") {
		t.Errorf("after leaving the terminal holds:\n%s", text)
	}
}

// A flood of pieces from the session does not flood the terminal with
// writes, does not hold the session up, and leaves what was typed
// meanwhile where it was typed.
func TestLoopFlood(t *testing.T) {
	l := startLoop(t, 100, 30, vtest.AllAnswers)
	l.keys(`"go" <Enter>`)
	l.ev(busy(), turnStart(), promptOut("p1", "go"), request(1))
	const pieces = 30000
	l.ft.mu.Lock()
	before := l.ft.writes
	l.ft.mu.Unlock()
	start := time.Now()
	done := make(chan struct{})
	go func() {
		defer close(done)
		for i := range pieces {
			l.u.Event(said(fmt.Sprintf("piece %d of the answer. ", i)))
			if i%500 == 499 {
				l.u.Event(said("\n\n"))
			}
			if i%1000 == 0 {
				l.u.Event(toolStart("c1", "Bash", map[string]any{"command": "make"}))
			}
			l.u.Event(progress("c1", strings.Repeat("output ", 20)+"\n"))
		}
	}()
	typed := "typed while it floods"
	for _, ch := range typed {
		l.ft.events <- term.Key{Code: term.KeyRune, Rune: ch, Text: string(ch)}
		time.Sleep(time.Millisecond)
	}
	<-done
	if took := time.Since(start); took > 15*time.Second {
		t.Errorf("reporting %d pieces took %v: the interface held the session up", pieces, took)
	}
	l.ev(replyEnd(core.StopEndTurn), turnEnd(agent.EndCompleted), idle())
	l.has("piece 0 of the answer.", fmt.Sprintf("piece %d of the answer.", pieces-1), "done in 42s", typed)
	l.ft.mu.Lock()
	writes := l.ft.writes - before
	l.ft.mu.Unlock()
	if writes > pieces/4 {
		t.Errorf("%d pieces were drawn in %d writes: the screen is drawn for every piece", pieces, writes)
	}
	l.do(func(s *session) {
		if got := s.ed.Text(); got != typed {
			t.Errorf("the box holds %q", got)
		}
	})
	l.keys(`<Ctrl+U> <Ctrl+D> <Ctrl+D>`).end()
}

// A terminal that is slow to take what is written to it does not hold up
// the session: what it reports is taken at once and waits its turn.
func TestSlowTerminal(t *testing.T) {
	l := startLoop(t, 80, 24, vtest.AllAnswers)
	gate := make(chan struct{})
	l.ft.mu.Lock()
	l.ft.gate = gate
	l.ft.mu.Unlock()
	start := time.Now()
	for i := range 20000 {
		l.u.Event(said(fmt.Sprintf("word%d ", i)))
		l.u.Event(progress("c1", strings.Repeat("x", 4096)))
	}
	if took := time.Since(start); took > 5*time.Second {
		t.Fatalf("with the terminal stuck, reporting took %v", took)
	}
	l.u.mu.Lock()
	waiting := len(l.u.inbox)
	l.u.mu.Unlock()
	if waiting > 64 {
		t.Errorf("%d events wait in line; pieces of live output should have been joined", waiting)
	}
	close(gate)
	l.ft.mu.Lock()
	l.ft.gate = nil
	l.ft.mu.Unlock()
	l.ev(replyEnd(core.StopEndTurn))
	l.has("word19999")
	l.keys(`<Ctrl+D> <Ctrl+D>`).end()
}

// Signals from outside: SIGTERM and SIGHUP end the session in order, with
// the exit code a signal gives; SIGINT while a turn runs stops the turn
// and the session goes on.
func TestLoopSignals(t *testing.T) {
	// A signal is told of twice, by the app and by the terminal, in either
	// order. SIGINT while a turn runs stops the turn once, and the terminal,
	// which put itself back as it was found, is taken again once: when
	// the terminal has told of the signal, and not before.
	for _, order := range []string{"the app alone", "the app first", "the terminal first"} {
		l := startLoop(t, 80, 24, vtest.AllAnswers)
		l.keys(`"go" <Enter>`).ev(busy(), turnStart(), promptOut("p1", "go"), request(1))
		l.has("* waiting for qwen3.8-27b")
		retaken := 0
		switch order {
		case "the app alone":
			l.c.sigs <- os.Interrupt
		case "the app first":
			l.c.sigs <- os.Interrupt
			l.has("* stopping the turn")
			l.do(func(*session) {
				if l.ft.released != 0 {
					t.Errorf("%s: the terminal was taken again before it told of the signal", order)
				}
			})
			l.ft.hooks.OnTerminate(os.Interrupt)
			retaken = 1
		case "the terminal first":
			l.ft.hooks.OnTerminate(os.Interrupt)
			l.has("* stopping the turn")
			l.c.sigs <- os.Interrupt
			retaken = 1
		}
		l.has("* stopping the turn")
		l.ev(turnEnd(agent.EndStopped), idle()).has("stopped by you", "idle")
		// The session goes on: what is typed is still drawn.
		l.keys(`"still here"`).has("> still here")
		l.do(func(*session) {
			if n := l.c.stopped(); n != 1 {
				t.Errorf("%s: SIGINT stopped the turn %d times", order, n)
			}
			if l.ft.released != retaken || l.ft.resumed != retaken {
				t.Errorf("%s: the terminal was given up %d times and taken back %d times, want %d", order, l.ft.released, l.ft.resumed, retaken)
			}
			if l.u.held.Load() {
				t.Errorf("%s: the interface still holds off drawing", order)
			}
		})
		select {
		case code := <-l.done:
			t.Fatalf("%s: the session ended with exit code %d after a SIGINT that stopped a turn", order, code)
		default:
		}
		l.c.sigs <- syscall.SIGTERM
		if code := l.end(); code != 143 {
			t.Fatalf("%s: SIGTERM gave exit code %d", order, code)
		}
		if l.ft.closed != 1 || l.c.quit != "other" {
			t.Errorf("%s: after SIGTERM the terminal was closed %d times, the session was told %q", order, l.ft.closed, l.c.quit)
		}
	}

	// The terminal tells of a signal that ends the session before the app
	// does. The app's telling is taken too: left on the app's channel it
	// would look like a second signal to the app, which ends the process
	// at once on one.
	// Both tellings are there when the loop looks, and it takes either
	// first, so this is tried a number of times.
	for range 12 {
		l := startLoop(t, 80, 24, vtest.AllAnswers)
		l.do(func(*session) {
			l.c.sigs <- syscall.SIGHUP
			l.ft.hooks.OnTerminate(syscall.SIGHUP)
		})
		if code := l.end(); code != 129 {
			t.Fatalf("SIGHUP gave exit code %d", code)
		}
		if n := len(l.c.sigs); n != 0 {
			t.Fatalf("%d signals were left for the app to take for a second one", n)
		}
	}

	// The terminal passes a signal on that the app does not: the same end.
	l := startLoop(t, 80, 24, vtest.AllAnswers)
	l.ft.hooks.OnTerminate(syscall.SIGQUIT)
	if code := l.end(); code != 131 {
		t.Fatalf("SIGQUIT gave exit code %d", code)
	}

	// SIGINT with nothing running ends the session.
	l = startLoop(t, 80, 24, vtest.AllAnswers)
	l.ft.hooks.OnTerminate(os.Interrupt)
	l.c.sigs <- os.Interrupt
	if code := l.end(); code != 130 {
		t.Fatalf("SIGINT with nothing running gave exit code %d", code)
	}

	// The terminal goes away.
	l = startLoop(t, 80, 24, vtest.AllAnswers)
	close(l.ft.events)
	if code := l.end(); code != 129 {
		t.Fatalf("a terminal that went away gave exit code %d", code)
	}
}

// A Ctrl+C typed into the person's own editor is the editor's. The
// terminal sends gnosh the signal as well, because the two run in one
// foreground group, and it neither ends the session nor costs the draft.
func TestEditorInterrupt(t *testing.T) {
	l := &loop{t: t, ft: newFakeTerm(80, 24, vtest.AllAnswers), c: newControls(), done: make(chan int, 1)}
	l.editor = func(string) (string, error) {
		l.ft.hooks.OnTerminate(os.Interrupt)
		l.c.sigs <- os.Interrupt
		return "", errors.New("vi did not end well (signal: interrupt), so the box is as it was.")
	}
	l.u = l.make()
	go func() { l.done <- l.u.Run(context.Background(), l.c) }()
	l.has("manual mode")
	l.keys(`"a draft" <Ctrl+G>`)
	l.has("so the box is as it was", "> a draft")
	l.keys(`" goes on"`).has("> a draft goes on")
	l.do(func(*session) {
		if n := len(l.c.sigs); n != 0 || l.u.held.Load() {
			t.Errorf("after the editor: %d signals wait, drawing held off: %v", n, l.u.held.Load())
		}
	})
	select {
	case code := <-l.done:
		t.Fatalf("a Ctrl+C in the editor ended the session with exit code %d", code)
	default:
	}
	// The next SIGINT is not the editor's. Nothing runs, so it ends the
	// session.
	l.ft.hooks.OnTerminate(os.Interrupt)
	l.c.sigs <- os.Interrupt
	if code := l.end(); code != 130 {
		t.Fatalf("SIGINT with nothing running gave exit code %d", code)
	}
}

// A panic in the loop puts the terminal back before it goes on its way.
func TestLoopPanic(t *testing.T) {
	l := &loop{t: t, ft: newFakeTerm(80, 24, vtest.AllAnswers), c: newControls(), done: make(chan int, 1)}
	l.u = l.make()
	panicked := make(chan any, 1)
	go func() {
		defer func() { panicked <- recover() }()
		l.u.Run(context.Background(), l.c)
	}()
	l.has("manual mode")
	l.u.spawn(func() func() { return func() { panic("boom") } })
	select {
	case v := <-panicked:
		if v != "boom" {
			t.Fatalf("the loop ended with %v", v)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("the panic did not come out of Run")
	}
	if l.ft.restored != 1 {
		t.Errorf("the terminal was restored %d times", l.ft.restored)
	}
}

// A question before the session exists: the terminal is taken for it and
// given back after it, keys typed before it could be read do not answer
// it, and the answer stays in the scrollback.
func TestAskBefore(t *testing.T) {
	trust := agent.Question{
		Kind: "trust", Title: "Trust this folder?",
		Text: "Do you trust the files in this folder?\n\n  /home/jae/src/proj\n\nIts own settings are not in force. If you say yes, they would add:\n\n  - a hook: ./check.sh\n\nWhat could go wrong, if whoever wrote them meant harm:\n\n  - It would run as you.",
		Choices: []agent.Choice{
			{Key: "no", Label: "No: start with my own settings only"},
			{Key: "yes", Label: "Yes: use this folder's settings"},
		},
	}
	ask := func(keys string) (agent.Answer, error, *loop) {
		at := &clock{at: stamp}
		l := &loop{t: t, ft: newFakeTerm(80, 30, vtest.AllAnswers), c: newControls(), now: at.Now}
		l.u = l.make()
		type result struct {
			ans agent.Answer
			err error
		}
		got := make(chan result, 1)
		go func() {
			ans, err := l.u.AskBefore(trust)
			got <- result{ans, err}
		}()
		l.has("Trust this folder?", "a hook: ./check.sh", "It would run as you.", "> 1. No: start with my own settings only")
		// At once: not meant for the question. The marker after it shows
		// that the key was taken before the clock is moved on.
		l.keys(`"2" <Shift+Up>`)
		for len(l.ft.events) > 0 {
			time.Sleep(time.Millisecond)
		}
		at.add(time.Second)
		l.keys(keys)
		select {
		case r := <-got:
			return r.ans, r.err, l
		case <-time.After(20 * time.Second):
			t.Fatal("the question was not answered")
		}
		return agent.Answer{}, nil, l
	}

	ans, err, l := ask(`<Enter>`)
	if err != nil || ans.Choice != "no" {
		t.Fatalf("Enter answered %+v, %v: no must be first", ans, err)
	}
	if l.ft.closed != 1 {
		t.Errorf("the terminal was closed %d times after the question", l.ft.closed)
	}
	if text := l.ft.text(); !strings.Contains(text, "? Trust this folder? No: start with my own settings only") || strings.Contains(text, "Enter choose") {
		t.Errorf("after the question the terminal holds:\n%s", text)
	}
	if ans, err, _ = ask(`<Down> <Enter>`); err != nil || ans.Choice != "yes" {
		t.Fatalf("Down Enter answered %+v, %v", ans, err)
	}
	if ans, err, _ = ask(`<Esc>`); err != nil || ans.Choice != "no" {
		t.Fatalf("Esc answered %+v, %v", ans, err)
	}
	if _, err, l = ask(`<Ctrl+C>`); err == nil {
		t.Fatal("Ctrl+C answered the question")
	}
	if l.ft.closed != 1 {
		t.Errorf("the terminal was closed %d times after Ctrl+C", l.ft.closed)
	}

	// The session then runs on the same terminal, asked what it can do
	// only the once. What was typed before the question could be read
	// is in the prompt box.
	l.c = newControls()
	l.done = make(chan int, 1)
	go func() { l.done <- l.u.Run(context.Background(), l.c) }()
	l.has("manual mode", "> 2")
	l.keys(`<Ctrl+U> <Ctrl+D> <Ctrl+D>`)
	if code := l.end(); code != 0 {
		t.Fatalf("exit code %d", code)
	}
}

// The interface is offered only where it can draw.
func TestNewNeedsATerminal(t *testing.T) {
	if New(Options{}) != nil {
		t.Error("New made an interface with no terminal at all")
	}
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	defer w.Close()
	if New(Options{In: r, Out: w}) != nil {
		t.Error("New made an interface on a pipe")
	}
}

// The prompt of the command line reaches the interface the day the app
// hands it over.
func TestFirstPrompt(t *testing.T) {
	if got := firstPrompt(app.FrontendInfo{}); got != "" {
		t.Errorf("the app's own info gave %q", got)
	}
	if got := firstPrompt(struct{ Prompt string }{"fix the test"}); got != "fix the test" {
		t.Errorf("a field named Prompt gave %q", got)
	}
}

// Inside tmux the colours follow what tmux says the terminal can show.
func TestProfileInsideTmux(t *testing.T) {
	env := map[string]string{"TERM": "tmux-256color", "COLORTERM": "truecolor", "TMUX": "/tmp/tmux-0/default,1,0"}
	profile := func(features string) text.Profile {
		u := newUI(Options{TmuxFeatures: func() string { return features }}, app.FrontendInfo{Getenv: func(k string) string { return env[k] }})
		return u.th.Profile
	}
	if got := profile("256,RGB,title"); got != text.TrueColor {
		t.Errorf("tmux says RGB and the profile is %v", got)
	}
	if got := profile("256,title"); got != text.ANSI256 {
		t.Errorf("tmux says no RGB and the profile is %v", got)
	}
	delete(env, "TMUX")
	if got := profile(""); got != text.TrueColor {
		t.Errorf("outside tmux the profile is %v", got)
	}
}

// What the session reports after the interface has left the terminal is
// not lost: warnings and hooks that did something are written as plain
// lines where errors go.
func TestLateEvents(t *testing.T) {
	var errs buffer
	l := &loop{t: t, ft: newFakeTerm(80, 24, vtest.AllAnswers), c: newControls(), done: make(chan int, 1)}
	maker := New(Options{Open: func(h TerminalHooks) (Terminal, error) { return l.ft, nil }, Frame: time.Millisecond})
	l.u = maker(app.FrontendInfo{Stderr: &errs}).(*UI)
	go func() { l.done <- l.u.Run(context.Background(), l.c) }()
	l.has("manual mode")
	// Reported just before the way out: it still reaches the scrollback.
	l.ev(warning("a last warning before leaving"))
	l.keys(`<Ctrl+D> <Ctrl+D>`)
	if code := l.end(); code != 0 {
		t.Fatalf("exit code %d", code)
	}
	if text := l.ft.text(); !strings.Contains(text, "warning: a last warning before leaving") {
		t.Errorf("the warning reported before leaving is not on the terminal:\n%s", text)
	}
	before := l.ft.text()
	l.ev(warning("the SessionEnd hook took too long\x1b[2J"), note("a quiet line"), hookEvent("SessionEnd", "", "", "none", "blocked"))
	got := errs.String()
	for _, want := range []string{"gnosh: the SessionEnd hook took too long^[[2J\n", "gnosh: hook: SessionEnd hook `check.sh` blocked"} {
		if !strings.Contains(got, want) {
			t.Errorf("stderr does not hold %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, "a quiet line") || strings.Contains(got, "had nothing to say") {
		t.Errorf("stderr holds what nobody needs to read:\n%s", got)
	}
	if l.ft.text() != before {
		t.Error("the terminal was written to after the interface left it")
	}
}

// /restart ends the interface without the line that says how to resume:
// gnosh starts again in the session at once, and says it itself if it
// cannot.
func TestLoopRestart(t *testing.T) {
	file := filepath.Join(t.TempDir(), "session.jsonl")
	if err := os.WriteFile(file, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	l := startLoop(t, 80, 24, vtest.AllAnswers, func(c *controls) {
		c.info.Path = file
		c.info.Commands = append(c.info.Commands, "restart")
		c.command = func(ctx context.Context, line string) (app.CommandResult, error) {
			return app.CommandResult{Handled: true, Name: "restart", Text: "gnosh starts again, in this session.", Quit: true}, nil
		}
	})
	l.has("GNOSH test-build")
	l.keys(`"/restart" <Enter>`)
	if code := l.end(); code != 0 {
		t.Fatalf("exit code %d", code)
	}
	text := l.ft.text()
	if !strings.Contains(text, "gnosh starts again, in this session.") || strings.Contains(text, "To resume this session") {
		t.Errorf("after /restart the terminal holds:\n%s", text)
	}
}
