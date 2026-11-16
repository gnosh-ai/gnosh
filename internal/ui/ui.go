package ui

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/gnosh-ai/gnosh/internal/agent"
	"github.com/gnosh-ai/gnosh/internal/app"
	"github.com/gnosh-ai/gnosh/internal/term"
	"github.com/gnosh-ai/gnosh/internal/text"
	"github.com/gnosh-ai/gnosh/internal/ui/editor"
)

// Options are what the interface is made with.
type Options struct {
	// In and Out are the two ends of the terminal: the process's standard
	// input and standard output.
	In, Out *os.File
	// Getenv looks up an environment variable of the process. It is used
	// when the app hands over none of its own. Nil means an empty
	// environment.
	Getenv func(string) string
	// Theme, when not nil, is the look, in place of DefaultTheme for the
	// terminal's colours.
	Theme *Theme
	// Now is the clock. Nil means time.Now.
	Now func() time.Time
	// Frame is how long the interface gathers what the session reports
	// before it draws: the answer to "how often is the screen drawn while
	// a reply streams". Zero means 10 ms. Keys are drawn at once.
	Frame time.Duration

	// Open opens the terminal. Nil means the real one, on In and Out. The
	// tests pass one of their own.
	Open func(TerminalHooks) (Terminal, error)
	// Editor lets the person edit text in an editor and returns what they
	// saved. Nil means the program VISUAL or EDITOR names, run on the
	// terminal.
	Editor func(text string) (string, error)
	// TmuxFeatures returns what tmux says the terminal it is shown in can
	// do, as its client_termfeatures format gives it ("256,RGB,title").
	// Nil means asking tmux, which is only done inside tmux.
	TmuxFeatures func() string
}

// New returns the maker of the full interface, for app.Options.Interactive:
//
//	Interactive: ui.New(ui.Options{In: os.Stdin, Out: os.Stdout, Getenv: os.Getenv}),
//
// It returns nil when this process has no terminal the interface could
// draw on: standard input or standard output is not one, or TERM says the
// terminal is dumb. The app then uses its plain front end, which reads
// lines and works anywhere.
func New(o Options) func(app.FrontendInfo) app.Frontend {
	if o.Open == nil {
		if !term.IsTerminal(o.In) || !term.IsTerminal(o.Out) {
			return nil
		}
		if o.Getenv != nil && strings.EqualFold(o.Getenv("TERM"), "dumb") {
			return nil
		}
	}
	return func(info app.FrontendInfo) app.Frontend { return newUI(o, info) }
}

// UI is the interactive session a person sees and types into: gnosh's
// full front end. It draws in the terminal's own scrollback. What is
// finished is written once, above, and never touched again; a few rows at
// the bottom are drawn in place. doc.go describes the screen and the keys.
//
// It is an app.Frontend and an app.Asker. Everything it shows is decided
// by a session (session.go), which the loop in Run feeds one thing at a
// time: what the app reported, what the terminal reported, and the clock.
type UI struct {
	o      Options
	env    func(string) string
	stderr io.Writer
	now    func() time.Time
	th     Theme
	m      text.Metrics
	first  string // the prompt the command line gave, when the app hands it over

	// The inbox: what the app has reported and the loop has not taken
	// yet. Event only ever adds to it, so the session never waits for
	// the screen.
	mu    sync.Mutex
	inbox []any
	poke  chan struct{}

	// The terminal, while it is open, and what it said it can do. shown
	// is the renderer as the terminal's signal hooks may use it, from a
	// goroutine of the terminal's own.
	t      Terminal
	r      *term.Renderer
	shown  atomic.Pointer[term.Renderer]
	caps   term.Capabilities
	probed bool
	// ahead are the keys typed before the interface was ready for them.
	ahead []term.Event
	// resumed is when the terminal last said it was the interface's
	// again, to a call that waited for that.
	resumed time.Time
	// held says the terminal is not the interface's to draw on just now:
	// a signal has put it back as it was found, or the process is being
	// stopped.
	held atomic.Bool
	// retitled says the interface has set the terminal's title, and so
	// puts the old one back as it leaves.
	retitled bool
	// sigs are the signals the terminal passed on, and owed the signals
	// that one of the two sides has told of and the other has not yet
	// (see signal).
	sigs chan os.Signal
	owed []heard
	// lent says another program has the terminal (the person's editor),
	// and editorInts counts the SIGINTs that arrived meanwhile: a Ctrl+C
	// typed there was that program's.
	lent       atomic.Bool
	editorInts atomic.Int32
	// over says the interface has left the terminal for good.
	over atomic.Bool

	s    *session
	jobs chan func()
	quit chan struct{}
}

// newUI makes the interface. No terminal is touched until a question is
// put or Run is called.
func newUI(o Options, info app.FrontendInfo) *UI {
	u := &UI{
		o:      o,
		env:    info.Getenv,
		stderr: info.Stderr,
		now:    o.Now,
		poke:   make(chan struct{}, 1),
		sigs:   make(chan os.Signal, 8),
		jobs:   make(chan func(), 16),
		quit:   make(chan struct{}),
	}
	if u.env == nil {
		u.env = o.Getenv
	}
	if u.env == nil {
		u.env = func(string) string { return "" }
	}
	if u.now == nil {
		u.now = time.Now
	}
	if u.stderr == nil {
		u.stderr = io.Discard
	}
	u.first = firstPrompt(info)
	if o.Theme != nil {
		u.th = *o.Theme
	} else {
		u.th = DefaultTheme(u.profile())
	}
	return u
}

// firstPrompt reads the prompt the command line gave (`gnosh "fix the
// test"`) out of what the app hands the maker of a front end: the field
// Prompt of app.FrontendInfo. The field is looked up by its name, as it
// was before the app had it.
func firstPrompt(info any) string {
	v := reflect.ValueOf(info)
	if v.Kind() != reflect.Struct {
		return ""
	}
	if f := v.FieldByName("Prompt"); f.IsValid() && f.Kind() == reflect.String {
		return f.String()
	}
	return ""
}

// profile works out how much colour the terminal takes. Inside tmux the
// environment cannot be trusted for 24-bit colour: tmux passes it on only
// to a terminal it knows can show it, and rounds it to 256 colours
// otherwise, whatever COLORTERM says. So tmux is asked.
func (u *UI) profile() text.Profile {
	p := text.DetectProfile(func(name string) (string, bool) {
		v := u.env(name)
		return v, v != ""
	}, true)
	if p == text.TrueColor && u.env("TMUX") != "" {
		features := ""
		switch {
		case u.o.TmuxFeatures != nil:
			features = u.o.TmuxFeatures()
		case u.o.Open == nil:
			features = tmuxFeatures()
		default:
			return p
		}
		rgb := false
		for _, f := range strings.Split(features, ",") {
			rgb = rgb || strings.TrimSpace(f) == "RGB"
		}
		if !rgb {
			p = text.ANSI256
		}
	}
	return p
}

// tmuxFeatures asks tmux what the terminal it is shown in can do.
func tmuxFeatures() string {
	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()
	out, err := exec.CommandContext(ctx, "tmux", "display-message", "-p", "#{client_termfeatures}").Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

// Event takes in one thing the session reports. It only puts it in line
// for the loop and returns, so a screen that is slow to draw never holds
// the session up. Pieces of live output that follow each other are joined
// while they wait; of a running command's output only the end is kept,
// because only its last lines are ever shown, and the whole of it arrives
// with the call's result.
func (u *UI) Event(ev any) {
	if u.over.Load() {
		u.late(ev)
		return
	}
	u.mu.Lock()
	u.add(ev)
	u.mu.Unlock()
	select {
	case u.poke <- struct{}{}:
	default:
	}
}

// Size is the terminal's size as the interface last heard it, for the
// status line command's COLUMNS and LINES; 80 by 24 before it has a
// terminal.
func (u *UI) Size() (cols, rows int) {
	if r := u.shown.Load(); r != nil {
		return r.Size()
	}
	return 80, 24
}

// late takes in something the session reports after the interface has
// left the terminal: the session is being closed, and its last hooks and
// warnings still have something to say. Nothing is silent, so a warning,
// an error and a hook that did more than run are written as plain lines
// where errors go; the terminal is an ordinary one again.
func (u *UI) late(ev any) {
	switch ev := ev.(type) {
	case *agent.NoticeEvent:
		if ev.Level != agent.LevelInfo {
			fmt.Fprintln(u.stderr, "gnosh: "+strings.Join(safeLines(ev.Text), "\n  "))
		}
	case *app.HookEvent:
		for _, r := range ev.Records {
			if string(r.Result) != "none" {
				fmt.Fprintln(u.stderr, "gnosh: hook: "+oneLine(r.String()))
			}
		}
	}
}

// add puts ev in the inbox. The caller holds the lock.
//
// A piece of the reply (its text, its reasoning, a call's arguments) is
// joined to the piece before it when that is of the same kind, and a
// piece of a running command's output to the earlier output of the same
// command. Output and the reply have nothing to do with each other, so
// each is joined across the other: two commands printing at once, or a
// command printing while a sub-agent's words arrive, still wait as a
// handful of events and not as thousands. Nothing is ever joined across
// an event of another kind, so the order things happened in is kept.
func (u *UI) add(ev any) {
	same := func(a, b *agent.Meta) bool { return a.Agent == b.Agent && a.Parent == b.Parent && a.Turn == b.Turn }
	// before is the event a piece of the reply would follow, looking
	// past any output that waits: the piece is joined to it when the two
	// are of one kind.
	before := func() any {
		for i := len(u.inbox) - 1; i >= 0 && i >= len(u.inbox)-joinReach; i-- {
			if _, output := u.inbox[i].(*agent.ToolProgressEvent); !output {
				return u.inbox[i]
			}
		}
		return nil
	}
	switch e := ev.(type) {
	case *agent.TextEvent:
		if l, ok := before().(*agent.TextEvent); ok && same(&l.Meta, &e.Meta) {
			l.Text += e.Text
			return
		}
		cp := *e
		ev = &cp
	case *agent.ThinkingEvent:
		if l, ok := before().(*agent.ThinkingEvent); ok && same(&l.Meta, &e.Meta) {
			l.Text += e.Text
			return
		}
		cp := *e
		ev = &cp
	case *agent.ToolInputEvent:
		if l, ok := before().(*agent.ToolInputEvent); ok && same(&l.Meta, &e.Meta) && l.CallID == e.CallID {
			l.Text += e.Text
			return
		}
		cp := *e
		ev = &cp
	case *agent.ToolProgressEvent:
		for i := len(u.inbox) - 1; i >= 0 && i >= len(u.inbox)-joinReach; i-- {
			l, isOutput := u.inbox[i].(*agent.ToolProgressEvent)
			if isOutput && l.CallID == e.CallID && same(&l.Meta, &e.Meta) {
				l.Text += e.Text
				if len(l.Text) > 2*tailKeep {
					l.Text = l.Text[len(l.Text)-tailKeep:]
				}
				return
			}
			if !isOutput {
				switch u.inbox[i].(type) {
				case *agent.TextEvent, *agent.ThinkingEvent, *agent.ToolInputEvent:
					continue
				}
				break
			}
		}
		cp := *e
		if len(cp.Text) > 2*tailKeep {
			cp.Text = cp.Text[len(cp.Text)-tailKeep:]
		}
		ev = &cp
	}
	u.inbox = append(u.inbox, ev)
}

// joinReach is how far back in the inbox a piece of live output looks for
// the piece it can be joined to.
const joinReach = 64

// The most the loop takes out of the inbox in one go, so that a long
// backlog is drawn in steps and keys are answered in between.
const (
	takeEvents = 256
	takeBytes  = 64 << 10
)

// take takes the next events out of the inbox: at most takeEvents of
// them, and at most takeBytes of the answer's text. A longer piece of
// text is handed out in parts. more says the inbox still holds some.
func (u *UI) take() (evs []any, more bool) {
	u.mu.Lock()
	defer u.mu.Unlock()
	n, bytes := 0, 0
	for n < len(u.inbox) && n < takeEvents && bytes < takeBytes {
		ev := u.inbox[n]
		if t, ok := ev.(*agent.TextEvent); ok {
			if room := takeBytes - bytes; len(t.Text) > room {
				// Cut on a character's edge.
				cut := room
				for cut > 0 && cut < len(t.Text) && t.Text[cut]&0xC0 == 0x80 {
					cut--
				}
				if cut == 0 {
					cut = min(room, len(t.Text))
				}
				head := *t
				head.Text = t.Text[:cut]
				t.Text = t.Text[cut:]
				evs = append(evs, &head)
				bytes = takeBytes
				break
			}
			bytes += len(t.Text)
		}
		evs = append(evs, ev)
		n++
	}
	// What was taken is let go of, so that it can be collected.
	kept := copy(u.inbox, u.inbox[n:])
	clear(u.inbox[kept:])
	u.inbox = u.inbox[:kept]
	return evs, len(u.inbox) > 0
}

// open takes the terminal: raw mode, the modes the terminal said it
// understands, a renderer that begins at the start of an empty line.
func (u *UI) open() error {
	hooks := TerminalHooks{
		OnTerminate: func(sig os.Signal) {
			if sig == os.Interrupt && u.lent.Load() {
				// A Ctrl+C typed into the person's editor. The terminal
				// sends it to gnosh as well, which runs in the same
				// foreground group, and it is the editor's alone.
				u.editorInts.Add(1)
				return
			}
			// The terminal has been put back as it was found. Nothing
			// more is drawn until the loop has decided what the signal
			// means.
			u.held.Store(true)
			select {
			case u.sigs <- sig:
			default:
			}
		},
		OnStop: func() {
			// The process is being stopped from outside. The live region
			// is erased before the shell gets the terminal, and nothing
			// is drawn until the terminal says it is back.
			u.held.Store(true)
			if r := u.shown.Load(); r != nil {
				_ = r.Clear()
			}
		},
	}
	var t Terminal
	var err error
	if u.o.Open != nil {
		t, err = u.o.Open(hooks)
	} else {
		t, err = openTerminal(u.o.In, u.o.Out, hooks)
	}
	if err != nil {
		return err
	}
	u.t = t
	u.held.Store(false)
	events := t.Events()
	if !u.probed {
		// Ask once what the terminal understands. What was typed
		// meanwhile is kept and acted on in order.
		caps, others, _ := term.Probe(context.Background(), t, events, 0)
		u.caps, u.probed = caps, true
		u.ahead = append(u.ahead, others...)
		if caps.LightBackground && u.o.Theme == nil {
			// The person's white and the answers' bright green are for a
			// dark background (Palette.Prompt, Reply).
			p := u.profile()
			u.th = NewTheme(p, DefaultPalette(p).OnLight(p))
		}
	}
	u.setModes()
	cols, rows, err := t.Size()
	if err != nil || cols < 1 || rows < 1 {
		cols, rows = 80, 24
	}
	u.r = term.NewRenderer(t, cols, rows, u.m.Width)
	u.r.SetSynchronized(u.caps.SynchronizedOutput)
	u.shown.Store(u.r)

	// The renderer begins at the cursor and takes it to be at the start
	// of an empty line. The command before gnosh may have left it in the
	// middle of one, so a terminal that answers questions is asked.
	if u.caps.Answered && t.QueryCursorPosition() == nil {
		timer := time.NewTimer(200 * time.Millisecond)
		defer timer.Stop()
	wait:
		for {
			select {
			case ev, ok := <-events:
				if !ok {
					break wait
				}
				if r, isReply := ev.(term.Reply); isReply && r.Kind == term.ReplyCursorPosition {
					if r.Col > 1 {
						_, _ = t.Write([]byte("\r\n"))
					}
					break wait
				}
				u.ahead = append(u.ahead, ev)
			case <-timer.C:
				break wait
			}
		}
	}
	return nil
}

// setModes turns on the terminal modes the terminal said it understands:
// bracketed paste for any terminal that answers questions at all, and the
// kitty keyboard level for one that said it has it.
func (u *UI) setModes() {
	_ = u.t.SetModes(term.Modes{BracketedPaste: u.caps.Answered, KittyKeyboard: u.caps.KittyKeyboard})
	if u.r != nil {
		u.r.SetSynchronized(u.caps.SynchronizedOutput)
	}
}

// shut erases the live region and gives the terminal back.
func (u *UI) shut() {
	if u.t == nil {
		return
	}
	u.shown.Store(nil)
	if u.s != nil && u.s.view != nil {
		// Whatever ended the interface, the terminal leaves the view's
		// screen, and the panel's.
		_, _ = u.s.view.p.Close()
		u.s.view = nil
	}
	if u.s != nil && u.s.panel != nil {
		_, _ = u.s.panel.Close()
		u.s.panel = nil
	}
	if u.r != nil {
		_ = u.r.Clear()
	}
	if u.retitled {
		_, _ = io.WriteString(u.t, term.TitlePop)
		u.retitled = false
	}
	_ = u.t.Close()
	u.t, u.r = nil, nil
}

// AskBefore puts a question to the person before a session exists:
// whether the folder is trusted, which model to use, which session to
// resume. The terminal is taken for the question and given back after it,
// so that what the program prints before the session starts is printed on
// an ordinary terminal.
//
// The question and its answer stay in the scrollback. Esc on the trust
// question is a no; on the others it leaves them unanswered, which is an
// error to the caller, as is Ctrl+C on any of them. Keys typed before the
// question could have been read do not answer it: they are kept, and go
// to the prompt box when the session starts.
func (u *UI) AskBefore(q agent.Question) (ans agent.Answer, err error) {
	if err := u.open(); err != nil {
		return agent.Answer{}, fmt.Errorf("the question could not be put: %w", err)
	}
	t := u.t
	defer t.RestoreOnPanic()
	defer u.shut()

	cols, rows, sizeErr := t.Size()
	if sizeErr != nil || cols < 1 || rows < 1 {
		cols, rows = 80, 24
	}
	qs := newQuestion(&u.th, u.m, cols, q, -1)
	opened := u.now()
	record := func(words string) {
		title := oneLine(firstNonEmpty(q.Title, q.Kind, "question"))
		_ = u.r.Commit(wrap(u.m, u.th.Answer.Render(oneLine(words)), cols, u.th.Ask.Render(u.th.AskMark)+" "+title+" ", "  ")...)
	}
	for {
		qs.ui.SetMaxHeight(max(liveBound(rows), 3))
		v := qs.ui.Render(cols)
		if err := u.r.SetLive(v.Lines, v.CursorRow, v.CursorCol); err != nil {
			return agent.Answer{}, fmt.Errorf("the question could not be drawn: %w", err)
		}
		select {
		case <-u.sigs:
			return agent.Answer{}, errors.New("stopped before the question was answered")
		case ev, ok := <-t.Events():
			if !ok {
				return agent.Answer{}, errors.New("the terminal went away before the question was answered")
			}
			switch ev := ev.(type) {
			case term.Resize:
				cols, rows = ev.Cols, ev.Rows
				qs.rebuild(&u.th, u.m, cols)
				_ = u.r.Resize(cols, rows)
				continue
			case term.Resume:
				u.r.Reset()
				continue
			case term.Reply:
				if u.caps.Note(ev) {
					u.setModes()
				}
				continue
			case term.Key:
				if ev.Is('c', term.ModCtrl) {
					record("left unanswered")
					return agent.Answer{}, errors.New("left before the question was answered")
				}
			}
			if u.now().Sub(opened) < guardShown {
				// Typed before the question could have been read: not
				// meant for it. It is kept for the prompt box, like
				// everything typed before gnosh was ready.
				u.ahead = append(u.ahead, ev)
				continue
			}
			res := qs.ui.Handle(ev)
			if !res.Done {
				continue
			}
			ans, words, how := qs.answer(res)
			record(words)
			if how == endLeft {
				return agent.Answer{}, errors.New("the question was left unanswered")
			}
			return ans, nil
		}
	}
}

// Run drives the session from its first prompt to its end and returns the
// program's exit code.
func (u *UI) Run(ctx context.Context, c app.Controls) (code int) {
	if err := u.open(); err != nil {
		// No terminal after all. The session cannot be shown, so it
		// ends, and says why on the stream errors go to.
		fmt.Fprintf(u.stderr, "gnosh: the terminal could not be used for the interface (%v), so the session was not started.\n", err)
		c.Quit("other")
		return 1
	}
	t := u.t
	defer t.RestoreOnPanic()
	defer close(u.quit)

	cols, rows, err := t.Size()
	if err != nil || cols < 1 || rows < 1 {
		cols, rows = 80, 24
	}
	s := newSession(config{
		theme: u.th, metrics: u.m, now: u.now, controls: c, renderer: u.r, host: u,
		cols: cols, rows: rows, home: u.env("HOME"), selectKey: selectKey(u.env("TERM_PROGRAM")),
		branch: gitBranch, files: listDir,
	})
	u.s = s
	s.start()
	// The verbose option: reasoning and whole outputs as they come.
	s.detail = s.info.Verbose
	// The editorMode and vimInsertModeRemaps options: vim's keys.
	s.ed.SetVimRemaps(s.info.VimRemaps)
	s.ed.SetVim(s.info.Vim)
	s.reportVim()
	if s.info.SidePanel {
		// The sidePanel option: the session starts with the panel on.
		s.openPanel()
	}
	for len(u.ahead) > 0 {
		ev := u.ahead[0]
		u.ahead = u.ahead[1:]
		u.terminal(ev)
	}
	if strings.TrimSpace(u.first) != "" {
		s.send(editor.Draft{Text: u.first})
	}
	u.draw()

	budget := u.o.Frame
	if budget <= 0 {
		budget = 10 * time.Millisecond
	}
	frame := time.NewTimer(time.Hour)
	frame.Stop()
	wake := time.NewTimer(time.Hour)
	wake.Stop()
	var frameC <-chan time.Time
	events := t.Events()
	for {
		switch {
		case s.done:
			return u.finish(c, s.code, s.why)
		case s.werr != nil:
			// The terminal cannot be written to: it is gone.
			return u.finish(c, 129, "other")
		}
		if s.dirty && frameC == nil {
			frame.Reset(budget)
			frameC = frame.C
		}
		var wakeC <-chan time.Time
		if at := s.wake(); !at.IsZero() {
			wake.Reset(max(at.Sub(u.now()), 0))
			wakeC = wake.C
		}

		select {
		case ev, ok := <-events:
			if !ok {
				// The terminal's input ended: the terminal went away.
				return u.finish(c, 129, "other")
			}
			u.terminal(ev)
			// What else was typed already is taken with it, so that a
			// paste that arrives as keys is drawn once.
		drain:
			for range takeEvents {
				select {
				case ev, ok := <-events:
					if !ok {
						break drain
					}
					u.terminal(ev)
				default:
					break drain
				}
			}
			// Keys are answered at once.
			u.draw()
		case <-u.poke:
			evs, more := u.take()
			for _, ev := range evs {
				s.handle(ev)
			}
			if more {
				select {
				case u.poke <- struct{}{}:
				default:
				}
			}
		case then := <-u.jobs:
			then()
		case <-frameC:
			frameC = nil
			u.draw()
		case <-wakeC:
			s.tick()
		case sig := <-c.Signals():
			if sig == os.Interrupt && u.editorInts.Load() > 0 {
				// The app's telling of a Ctrl+C that was the editor's.
				u.editorInts.Add(-1)
				continue
			}
			if u.signal(c, sig, false) {
				return u.finish(c, signalCode(sig), "other")
			}
		case sig := <-u.sigs:
			if u.signal(c, sig, true) {
				return u.finish(c, signalCode(sig), "other")
			}
		case <-ctx.Done():
			return u.finish(c, 1, "other")
		}
	}
}

// terminal takes in one thing the terminal reported, and then whatever
// was reported while the terminal was lent to the shell or to an editor.
func (u *UI) terminal(ev term.Event) {
	u.terminalOne(ev)
	for len(u.ahead) > 0 {
		next := u.ahead[0]
		u.ahead = u.ahead[1:]
		u.terminalOne(next)
	}
}

func (u *UI) terminalOne(ev term.Event) {
	switch ev := ev.(type) {
	case term.Reply:
		// An answer that came later than the probe waited for it.
		if u.caps.Note(ev) {
			u.setModes()
		}
		return
	case term.Resume:
		u.held.Store(false)
		if !u.resumed.IsZero() && u.now().Sub(u.resumed) < 500*time.Millisecond {
			// The terminal says a second time that it is back. The
			// interface has drawn since the first, and what it drew
			// stands.
			return
		}
	}
	u.s.input(ev)
}

// draw writes what the session has waiting, unless the terminal is not
// the interface's to draw on just now.
func (u *UI) draw() {
	if u.held.Load() {
		return
	}
	u.s.frame()
}

// heard is a signal that one of the two sides has told of and the other
// has not yet.
type heard struct {
	sig      os.Signal
	terminal bool // the terminal told of it; otherwise the app did
	at       time.Time
}

// signalEcho is how long the second telling of a signal is waited for
// before the signal is taken to have been told of once only.
const signalEcho = 3 * time.Second

// signal acts on a signal the process received, and reports whether the
// session should end. terminal says which side told of it.
//
// A signal is told of twice: by the app (Controls.Signals) and by the
// terminal, which has by then put itself back as it was found. Which of
// the two comes first is not known. What the signal means is decided at
// the first telling, once. The terminal is taken back at the terminal's
// telling, whichever that is: any earlier and the terminal's own handler,
// which may not have run yet, would undo it.
//
// Keys send no signals here, so a signal comes from outside. SIGINT while
// something runs stops the turn, as Ctrl+C does; every other signal, and
// SIGINT with nothing running, ends the session in order.
func (u *UI) signal(c app.Controls, sig os.Signal, terminal bool) (end bool) {
	now := u.now()
	second := false
	waiting := u.owed[:0]
	for _, h := range u.owed {
		switch {
		case now.Sub(h.at) >= signalEcho:
			// The other side never told of this one.
		case !second && h.sig == sig && h.terminal != terminal:
			second = true
		default:
			waiting = append(waiting, h)
		}
	}
	u.owed = waiting
	s := u.s
	if !second {
		if sig != os.Interrupt || !(s.busy || s.cmdRun != "") {
			if terminal {
				// The app has the same signal waiting on its own
				// channel. It is taken from there, so that the app,
				// which closes the session next, does not take it for
				// a second signal: that would end the process at once,
				// in the middle of closing.
				timer := time.NewTimer(100 * time.Millisecond)
				defer timer.Stop()
				select {
				case <-c.Signals():
				case <-timer.C:
				}
			}
			return true
		}
		u.owed = append(u.owed, heard{sig: sig, terminal: terminal, at: now})
		s.interrupt()
		s.dirty = true
	}
	if terminal {
		// The terminal was put back as it was found when the signal
		// came. Take it again: the live region is erased, the terminal
		// is given up and taken back, and the region is drawn afresh
		// where it was.
		_ = u.r.Clear()
		_ = u.t.Release()
		_ = u.t.Resume()
		u.awaitResume()
		u.held.Store(false)
		s.dirty = true
	}
	return false
}

// signalCode is the exit code of a program that a signal stopped: 128
// plus the signal's number, so 130 for SIGINT, 143 for SIGTERM and 129
// for SIGHUP.
func signalCode(sig os.Signal) int {
	if n, ok := sig.(syscall.Signal); ok && n > 0 {
		return 128 + int(n)
	}
	return 143
}

// finish ends the interface: what waits to be committed is written, with
// the line that says how to get the session back; the live region is
// erased; and the terminal is put back as it was found. What was on the
// screen stays on the screen.
func (u *UI) finish(c app.Controls, code int, why string) int {
	s := u.s
	// What the session reported and the loop had not taken in yet still
	// reaches the scrollback.
	for range 64 {
		evs, more := u.take()
		for _, ev := range evs {
			s.handle(ev)
		}
		if !more {
			break
		}
	}
	s.endReply()
	// The transcript view and the side panel give the screen back, and
	// what was said while they were on goes into the scrollback, where
	// the rest of the session is.
	s.closeView()
	s.closePanel()
	info := c.Info()
	if _, err := os.Stat(info.Path); err == nil && info.Resume != "" && !s.restarting {
		s.begin(blkTurn)
		s.put(wrap(u.m, u.th.wordmark()+" "+u.th.Quiet.Render("To resume this session: ")+oneLine(info.Resume), s.cols, "", "  ")...)
	}
	if len(s.pending) > 0 {
		if s.onCommit != nil {
			s.onCommit(s.pending)
		}
		_ = u.r.Commit(s.pending...)
		s.pending = nil
	}
	u.shut()
	u.over.Store(true)
	c.Quit(why)
	return code
}

// spawn runs job away from the loop and then runs what it returns on
// the loop. It is how a slash command runs without freezing the screen.
func (u *UI) spawn(job func() (then func())) {
	t := u.t
	go func() {
		if t != nil {
			defer t.RestoreOnPanic()
		}
		then := job()
		select {
		case u.jobs <- then:
		case <-u.quit:
		}
	}()
}

// suspend gives the terminal back to the shell and stops the process. It
// returns when the shell has continued it and the terminal has said that
// it is the interface's again; or with an error, and nothing done, when
// the process could not be stopped.
func (u *UI) suspend() error {
	if err := u.t.Suspend(); err != nil {
		return err
	}
	u.awaitResume()
	return nil
}

// awaitResume reads what the terminal reports until it says it is the
// interface's again. What else it reports meanwhile is kept and acted on
// in order afterwards.
func (u *UI) awaitResume() {
	timer := time.NewTimer(2 * time.Second)
	defer timer.Stop()
	for {
		select {
		case ev, ok := <-u.t.Events():
			if !ok {
				return
			}
			if _, resumed := ev.(term.Resume); resumed {
				u.held.Store(false)
				u.resumed = u.now()
				return
			}
			u.ahead = append(u.ahead, ev)
		case <-timer.C:
			return
		}
	}
}

// size is the terminal's size as it stands.
// title sets the terminal's title (OSC 2), having kept the one it had on
// the terminal's own stack, which shut puts back. CLAUDE_CODE_DISABLE_TERMINAL_TITLE=1
// turns it off, as it does Claude Code's.
func (u *UI) title(text string) error {
	if u.t == nil || u.env("CLAUDE_CODE_DISABLE_TERMINAL_TITLE") == "1" {
		return nil
	}
	seq := term.SetTitle(text)
	if !u.retitled {
		seq = term.TitlePush + seq
		u.retitled = true
	}
	_, err := io.WriteString(u.t, seq)
	return err
}

// desktop tells a person who seems to be away that the session waits for
// them, by the channel they chose (the preferredNotifChannel option), as
// Claude Code does. With text empty it only reports whether the channel
// reaches anyone.
func (u *UI) desktop(channel, text string) bool {
	seq := noteSeq(channel, text, u.env)
	if seq == "" || u.t == nil {
		return false
	}
	if text != "" {
		_, _ = io.WriteString(u.t, seq)
	}
	return true
}

// noteSeq is what a channel writes to the terminal for a note, or "" when
// it writes nothing. "auto" is a desktop note through a terminal that is
// known to take one (iTerm2, WezTerm, Ghostty: OSC 9; kitty: OSC 99), not
// inside tmux, where gnosh cannot see the terminal. A channel the person
// named is sent through tmux too, wrapped for its passthrough; the bell
// needs no wrapping. With text empty it says whether the channel writes
// anything at all, as a bell.
func noteSeq(channel, text string, env func(string) string) string {
	kind := ""
	switch channel {
	case "notifications_disabled":
		return ""
	case "terminal_bell":
		return term.Bell
	case "iterm2", "ghostty", "iterm2_with_bell":
		kind = "osc9"
	case "kitty":
		kind = "osc99"
	default:
		switch {
		case env("TMUX") != "":
			return ""
		case env("KITTY_WINDOW_ID") != "" || env("TERM") == "xterm-kitty":
			kind = "osc99"
		case env("TERM_PROGRAM") == "iTerm.app" || env("TERM_PROGRAM") == "WezTerm" ||
			env("TERM_PROGRAM") == "ghostty" || env("TERM") == "xterm-ghostty":
			kind = "osc9"
		default:
			return ""
		}
	}
	seq := term.DesktopNote(kind, firstNonEmpty(text, "gnosh"))
	if env("TMUX") != "" {
		seq = term.TmuxWrap(seq)
	}
	if channel == "iterm2_with_bell" {
		seq += term.Bell
	}
	return seq
}

// copy puts text on the person's clipboard through the terminal (OSC 52):
// the person's /copy asks for it. The sequence moves nothing on the screen.
func (u *UI) copy(text string) error {
	if u.t == nil {
		return errors.New("there is no terminal to write to")
	}
	_, err := io.WriteString(u.t, term.ClipboardWrite(text))
	return err
}

// openPanel takes the terminal's whole screen for the side panel.
func (u *UI) openPanel(cols, rows int) (*term.Panel, error) {
	if u.t == nil {
		return nil, errors.New("there is no terminal to draw on")
	}
	return term.OpenPanel(u.t, cols, rows, u.m.Width, u.caps.SynchronizedOutput)
}

func (u *UI) openView(cols, rows int) (*term.Panel, error) {
	if u.t == nil {
		return nil, errors.New("there is no terminal to draw on")
	}
	return term.OpenView(u.t, cols, rows, u.m.Width, u.caps.SynchronizedOutput)
}

func (u *UI) size() (cols, rows int, ok bool) {
	cols, rows, err := u.t.Size()
	return cols, rows, err == nil && cols > 0 && rows > 0
}

// edit lets the person edit text in their own editor: the terminal is
// handed to the program VISUAL or EDITOR names, with the text in a file,
// and taken back when that program ends.
func (u *UI) edit(draft string) (string, error) {
	if u.o.Editor != nil {
		u.lent.Store(true)
		defer u.lent.Store(false)
		return u.o.Editor(draft)
	}
	program := firstNonEmpty(u.env("VISUAL"), u.env("EDITOR"))
	if program == "" {
		return "", errors.New("Set VISUAL or EDITOR to edit the prompt in your own editor.")
	}
	f, err := os.CreateTemp(u.env("TMPDIR"), "gnosh-prompt-*.md")
	if err != nil {
		return "", fmt.Errorf("The prompt could not be written to a file for the editor: %v", err)
	}
	path := f.Name()
	defer os.Remove(path)
	_, werr := f.WriteString(draft)
	if cerr := f.Close(); werr == nil {
		werr = cerr
	}
	if werr != nil {
		return "", fmt.Errorf("The prompt could not be written to a file for the editor: %v", werr)
	}
	ran, err := u.runEditor(program, path)
	if err != nil {
		return "", err
	}
	if ran != nil {
		return "", fmt.Errorf("%s did not end well (%v), so the box is as it was.", oneLine(program), ran)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("What the editor saved could not be read: %v", err)
	}
	return string(data), nil
}

// editFile lets the person edit a file in their own editor (VISUAL,
// EDITOR), in place: /memory's picker opens a context file so.
func (u *UI) editFile(path string) error {
	if u.o.Editor != nil {
		data, _ := os.ReadFile(path)
		u.lent.Store(true)
		out, err := u.o.Editor(string(data))
		u.lent.Store(false)
		if err != nil {
			return err
		}
		return os.WriteFile(path, []byte(out), 0o644)
	}
	program := firstNonEmpty(u.env("VISUAL"), u.env("EDITOR"))
	if program == "" {
		return errors.New("Set VISUAL or EDITOR to open it in your own editor.")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("%s cannot be made: %v", filepath.Dir(path), err)
	}
	ran, err := u.runEditor(program, path)
	if err == nil && ran != nil {
		err = fmt.Errorf("%s did not end well (%v).", oneLine(program), ran)
	}
	return err
}

// runEditor hands the terminal to the editor program on path, and takes
// it back. ran is how the editor ended; err says the terminal could not
// be handed over or taken back.
func (u *UI) runEditor(program, path string) (ran, err error) {
	_ = u.r.Clear()
	if err := u.t.Release(); err != nil {
		return nil, fmt.Errorf("The terminal could not be handed to the editor: %v", err)
	}
	cmd := exec.Command("/bin/sh", "-c", program+` "$1"`, "sh", path)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = u.o.In, u.o.Out, u.o.Out
	u.lent.Store(true)
	ran = cmd.Run()
	u.lent.Store(false)
	if err := u.t.Resume(); err != nil {
		return ran, fmt.Errorf("The terminal could not be taken back from the editor: %v", err)
	}
	u.awaitResume()
	u.r.Reset()
	return ran, nil
}
