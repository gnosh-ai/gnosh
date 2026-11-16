package ui

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gnosh-ai/gnosh/internal/agent"
	"github.com/gnosh-ai/gnosh/internal/app"
	"github.com/gnosh-ai/gnosh/internal/core"
	"github.com/gnosh-ai/gnosh/internal/hooks"
	"github.com/gnosh-ai/gnosh/internal/term"
	"github.com/gnosh-ai/gnosh/internal/term/vtest"
	"github.com/gnosh-ai/gnosh/internal/text"
	"github.com/gnosh-ai/gnosh/internal/ui/editor/keyscript"
)

// rig is a session under test. What it writes goes through the real
// renderer into an emulated terminal, and after every step the rig checks
// that the terminal shows exactly what the session meant it to: every line
// it committed, once and in order, and under them the live region, with
// the cursor where it was asked for.
type rig struct {
	t          *testing.T
	copied     []string
	titles     []string
	takesNotes bool
	// free says the test draws on the whole screen (the side panel), where
	// the checks of what is in the scrollback do not hold.
	free       bool
	notes      []string
	scr        *vtest.Screen
	s          *session
	c          *controls
	now        time.Time
	cols, rows int

	// committed is every line the session committed, without its escape
	// sequences. base is how many lines of the terminal's history stand
	// above the first of committed[from:]: zero until the terminal is
	// resized, which rearranges what was written before.
	committed []string
	from      int
	base      int
	// live is the live region as last set, and where the cursor was
	// asked for.
	live             []string
	liveRow, liveCol int
	// most is the tallest the live region has been.
	most int
	// checks counts the checks made, and widthsTo how many committed
	// lines have had their width checked.
	checks   int
	widthsTo int
	// writes is every write the renderer made, as it was written.
	writes []string

	// hold keeps slash commands from finishing until release is called.
	hold     bool
	held     []func() func()
	suspends int
	// suspendErr, when not nil, is why the process cannot be stopped.
	suspendErr error
	editor     func(string) (string, error)
	// editedFiles are the files opened in the editor, in order.
	editedFiles []string
	files       map[string][]fileEntry
}

// stamp is where every rig's clock starts.
var stamp = time.Date(2026, 11, 13, 9, 0, 0, 0, time.UTC)

// newRig starts a session in a terminal of cols by rows cells and draws
// its first frame. set may change the controls before the session reads
// them.
func newRig(t *testing.T, cols, rows int, profile text.Profile, set ...func(*controls)) *rig {
	t.Helper()
	g := &rig{t: t, scr: vtest.New(cols, rows), now: stamp, cols: cols, rows: rows, files: map[string][]fileEntry{}}
	g.scr.SetWidth(func(s string) int { return text.Width(s) })
	g.c = newControls()
	for _, f := range set {
		f(g.c)
	}
	g.s = newSession(config{
		theme:    DefaultTheme(profile),
		now:      func() time.Time { return g.now },
		controls: g.c,
		renderer: term.NewRenderer(tee{g}, cols, rows, text.Width),
		host:     g,
		cols:     cols, rows: rows,
		home:   "/home/jae",
		branch: func(string) string { return "main" },
		files:  func(dir string) []fileEntry { return g.files[dir] },
	})
	g.s.onCommit = func(lines []string) {
		for _, l := range lines {
			g.committed = append(g.committed, seen(l))
		}
	}
	g.s.onLive = func(lines []string, row, col int) {
		g.live = g.live[:0]
		for _, l := range lines {
			g.live = append(g.live, seen(l))
		}
		g.liveRow, g.liveCol = row, col
		g.most = max(g.most, len(lines))
	}
	g.s.start()
	g.frame()
	t.Cleanup(func() {
		if !t.Failed() {
			g.verify(true)
		}
	})
	return g
}

// tee is the terminal the renderer writes to: the emulated screen, with a
// record of every write.
type tee struct{ g *rig }

func (w tee) Write(p []byte) (int, error) {
	w.g.writes = append(w.g.writes, string(p))
	return w.g.scr.Write(p)
}

// seen is a line as a person sees it on the terminal: without its escape
// sequences, without the characters that take no room (the emulator keeps
// those only where they sit on a letter), and without the blanks at its
// end.
func seen(l string) string {
	l = text.Strip(l)
	if text.Width(l) != len(l) {
		var b strings.Builder
		for _, r := range l {
			if text.Width(string(r)) > 0 {
				b.WriteRune(r)
			}
		}
		l = b.String()
	}
	return strings.TrimRight(l, " ")
}

// spawn runs a slash command's job at once, or keeps it until release.
func (g *rig) spawn(job func() func()) {
	if g.hold {
		g.held = append(g.held, job)
		return
	}
	job()()
}

// release lets the slash commands that were held finish.
func (g *rig) release() *rig {
	g.t.Helper()
	held := g.held
	g.held = nil
	for _, job := range held {
		job()()
	}
	g.frame()
	return g
}

func (g *rig) suspend() error {
	g.suspends++
	return g.suspendErr
}

func (g *rig) edit(s string) (string, error) {
	if g.editor == nil {
		return "", errors.New("Set VISUAL or EDITOR to edit the prompt in your own editor.")
	}
	return g.editor(s)
}

func (g *rig) desktop(channel, text string) bool {
	if !g.takesNotes || channel == "notifications_disabled" {
		return false
	}
	if text != "" {
		g.notes = append(g.notes, text)
	}
	return true
}

func (g *rig) title(text string) error {
	g.titles = append(g.titles, text)
	return nil
}

func (g *rig) copy(text string) error {
	g.copied = append(g.copied, text)
	return nil
}

func (g *rig) openPanel(cols, rows int) (*term.Panel, error) {
	return term.OpenPanel(g.scr, cols, rows, vtest.Width, false)
}

func (g *rig) editFile(path string) error {
	g.editedFiles = append(g.editedFiles, path)
	return nil
}

func (g *rig) openView(cols, rows int) (*term.Panel, error) {
	return term.OpenView(g.scr, cols, rows, vtest.Width, false)
}

func (g *rig) size() (int, int, bool) {
	cols, rows := g.scr.Size()
	return cols, rows, true
}

// frame draws and checks the screen.
func (g *rig) frame() *rig {
	g.t.Helper()
	g.s.frame()
	g.check()
	return g
}

// ev hands the session things the app reports, drawing and checking
// after each.
func (g *rig) ev(evs ...any) *rig {
	g.t.Helper()
	for _, ev := range evs {
		g.s.handle(ev)
		g.frame()
	}
	return g
}

// keys types a key script, drawing and checking after each key.
func (g *rig) keys(script string) *rig {
	g.t.Helper()
	evs, err := keyscript.Parse(script)
	if err != nil {
		g.t.Fatal(err)
	}
	for _, ev := range evs {
		if r, ok := ev.(term.Resize); ok {
			g.resize(r.Cols, r.Rows)
			continue
		}
		g.s.input(ev)
		g.frame()
	}
	return g
}

// wait moves the clock on and lets the session see it.
func (g *rig) wait(d time.Duration) *rig {
	g.t.Helper()
	g.now = g.now.Add(d)
	g.s.tick()
	g.frame()
	return g
}

// resize changes the terminal's size, as a person dragging its edge does:
// the terminal rearranges what is on it, and then gnosh hears of it.
func (g *rig) resize(cols, rows int) *rig {
	g.t.Helper()
	g.scr.Resize(cols, rows)
	g.cols, g.rows = cols, rows
	g.s.input(term.Resize{Cols: cols, Rows: rows})
	g.s.frame()
	// What was written before the resize is the terminal's to rearrange.
	// From here on the check starts where the live region now begins,
	// which the cursor tells.
	g.from = len(g.committed)
	row, _ := g.scr.Cursor()
	if g.liveRow >= 0 {
		row -= max(g.liveRow-(len(g.live)-len(g.shown())), 0)
	}
	g.base = len(g.scr.Scrollback()) + max(row, 0)
	g.verify(true)
	return g
}

// all is every line of the terminal, as a person sees it: its scrollback
// and every row of its screen.
func (g *rig) all() []string {
	lines := append(g.scr.Scrollback(), g.scr.Lines()...)
	for i, l := range lines {
		lines[i] = seen(l)
	}
	return lines
}

// shown is the part of the live region the terminal has room for, as the
// terminal shows it: cut to its width, without the blanks at the end of
// a row.
func (g *rig) shown() []string {
	live := g.live
	if len(live) > g.rows {
		live = live[len(live)-g.rows:]
	}
	out := make([]string, len(live))
	for i, l := range live {
		out[i] = strings.TrimRight(text.Truncate(l, g.cols, ""), " ")
	}
	return out
}

// check holds the screen to what the session meant it to be. Most checks
// read the screen alone, which is where anything new is: the live region
// in its place with the cursor in it, the last committed lines right above
// it, nothing below it. Every sixteenth check, and the last of a test,
// reads the terminal's whole history as well: everything committed is in
// it once, in order, as it was written.
func (g *rig) check() {
	g.t.Helper()
	g.checks++
	g.verify(g.checks%16 == 0)
}

// verify is check; whole says to read the terminal's whole history.
func (g *rig) verify(whole bool) {
	g.t.Helper()
	if g.free {
		return
	}
	fail := func(format string, args ...any) {
		g.t.Helper()
		g.t.Fatalf(format+"\n%s", append(args, g.dump())...)
	}
	if g.scr.AltScreenEver() {
		fail("the alternate screen was switched to")
	}
	if g.scr.MouseEver() {
		fail("mouse reporting was turned on")
	}
	if u := g.scr.Unhandled(); len(u) > 0 {
		fail("the terminal was sent sequences it does not know: %q", u)
	}
	if s := g.scr.Strings(); len(s) > 0 {
		fail("the terminal was sent strings it has no use for: %q", s)
	}
	if bound := liveBound(g.rows); len(g.live) > bound {
		fail("the live region has %d rows, more than its bound of %d", len(g.live), bound)
	}
	for i := max(g.widthsTo, g.from); i < len(g.committed); i++ {
		if w := text.Width(g.committed[i]); w > g.cols {
			fail("committed line %d is %d cells wide in a terminal of %d: %q", i, w, g.cols, g.committed[i])
		}
	}
	g.widthsTo = len(g.committed)
	for i, l := range g.live {
		if w := text.Width(l); w > g.cols {
			fail("row %d of the live region is %d cells wide in a terminal of %d: %q", i, w, g.cols, l)
		}
	}

	// The cursor is where the session asked for it, which also says
	// where on the screen the live region begins.
	shown := g.shown()
	hidden := len(g.live) - len(shown)
	row, col := g.scr.Cursor()
	top := row
	switch {
	case g.liveRow < 0:
		if g.scr.CursorVisible() {
			fail("the cursor is shown, and the live region asked for it to be hidden")
		}
	default:
		if !g.scr.CursorVisible() {
			fail("the cursor is hidden, and the live region asked for it on row %d", g.liveRow)
		}
		if want := min(g.liveCol, g.cols-1); col != want {
			fail("the cursor is at cell %d, and should be at cell %d", col, want)
		}
		top = row - max(g.liveRow-hidden, 0)
	}
	if top < 0 {
		fail("the cursor is on row %d of the screen, too high for row %d of the live region", row, g.liveRow)
	}

	// The screen: the live region, the committed lines above it, and
	// nothing below it.
	grid := g.scr.Lines()
	for i := range grid {
		grid[i] = seen(grid[i])
	}
	for i, want := range shown {
		if top+i >= len(grid) || grid[top+i] != want {
			got := "(off the screen)"
			if top+i < len(grid) {
				got = grid[top+i]
			}
			fail("row %d of the live region is on the screen as\n  %q\nand should be\n  %q", hidden+i, got, want)
		}
	}
	for i := top + len(shown); i < len(grid); i++ {
		if grid[i] != "" {
			fail("row %d of the screen, below the live region, holds %q", i, grid[i])
		}
	}
	for i, c := top-1, len(g.committed)-1; i >= 0 && c >= g.from; i, c = i-1, c-1 {
		if grid[i] != g.committed[c] {
			fail("row %d of the screen is\n  %q\nand should be committed line %d,\n  %q", i, grid[i], c, g.committed[c])
		}
	}
	if !whole {
		return
	}

	// The whole history: what was committed, once and in order, and
	// then the live region.
	want := append(append([]string{}, g.committed[g.from:]...), shown...)
	for len(want) > 0 && want[len(want)-1] == "" {
		want = want[:len(want)-1]
	}
	got := g.all()
	if len(got) < g.base {
		fail("the terminal holds %d lines, fewer than the %d that stood above the live region", len(got), g.base)
	}
	got = got[g.base:]
	for len(got) > 0 && got[len(got)-1] == "" {
		got = got[:len(got)-1]
	}
	for i := 0; i < len(want) || i < len(got); i++ {
		var w, h string
		if i < len(want) {
			w = want[i]
		}
		if i < len(got) {
			h = got[i]
		}
		if w != h {
			fail("line %d of the terminal is\n  %q\nand should be\n  %q", g.base+i, h, w)
		}
	}
}

// dump shows the terminal and the live region for a failed check.
func (g *rig) dump() string {
	last := g.writes[max(len(g.writes)-3, 0):]
	return fmt.Sprintf("the terminal:\n%s\nthe live region as set (cursor row %d, cell %d):\n  %s\nthe last writes:\n  %q",
		g.scr.Dump(), g.liveRow, g.liveCol, strings.Join(g.live, "\n  "), last)
}

// scroll is everything committed, as one text.
func (g *rig) scroll() string { return strings.Join(g.committed, "\n") }

// region is the live region, as one text.
func (g *rig) region() string { return strings.Join(g.live, "\n") }

// flat puts a text on one line, with single spaces: what is looked for
// in the scrollback is found whether or not it was wrapped on its way
// there.
func flat(s string) string { return strings.Join(strings.Fields(s), " ") }

// committedHas fails the test unless the scrollback holds each piece.
func (g *rig) committedHas(pieces ...string) *rig {
	g.t.Helper()
	for _, p := range pieces {
		if !strings.Contains(flat(g.scroll()), flat(p)) {
			g.t.Fatalf("the scrollback does not hold %q\n%s", p, g.dump())
		}
	}
	return g
}

// committedLacks fails the test if the scrollback holds any piece.
func (g *rig) committedLacks(pieces ...string) *rig {
	g.t.Helper()
	for _, p := range pieces {
		if strings.Contains(flat(g.scroll()), flat(p)) {
			g.t.Fatalf("the scrollback holds %q and should not\n%s", p, g.dump())
		}
	}
	return g
}

// liveHas fails the test unless the live region holds each piece.
func (g *rig) liveHas(pieces ...string) *rig {
	g.t.Helper()
	for _, p := range pieces {
		if !strings.Contains(flat(g.region()), flat(p)) {
			g.t.Fatalf("the live region does not hold %q\n%s", p, g.dump())
		}
	}
	return g
}

// liveLacks fails the test if the live region holds any piece.
func (g *rig) liveLacks(pieces ...string) *rig {
	g.t.Helper()
	for _, p := range pieces {
		if strings.Contains(flat(g.region()), flat(p)) {
			g.t.Fatalf("the live region holds %q and should not\n%s", p, g.dump())
		}
	}
	return g
}

// once fails the test unless the scrollback holds the piece exactly once.
func (g *rig) once(pieces ...string) *rig {
	g.t.Helper()
	for _, p := range pieces {
		if n := strings.Count(flat(g.scroll()), flat(p)); n != 1 {
			g.t.Fatalf("the scrollback holds %q %d times, and should hold it once\n%s", p, n, g.dump())
		}
	}
	return g
}

// box is the text in the prompt box.
func (g *rig) box() string { return g.s.ed.Text() }

// controls stands in for the session behind the interface: it records
// what the interface asks of it and answers from what a test set.
type controls struct {
	mu        sync.Mutex
	vimModes  []string // what SetVimMode was told, in order
	prompts   []agent.Prompt
	ids       []string
	submitErr error
	stops     int
	answers   []answer
	answerErr error
	queue     []agent.QueuedPrompt
	mode      string
	bypass    bool
	commands  []string
	command   func(ctx context.Context, line string) (app.CommandResult, error)
	state     agent.State
	ctxs      agent.ContextStatus
	info      app.SessionInfo
	notified  []string
	quit      string
	sigs      chan os.Signal
	tasks     []app.Task
	deliv     []app.Deliverable
	trans     []app.TranscriptEntry
	models    []app.ModelChoice
	options   []app.ConfigChoice
	memFiles  []app.MemoryFile
	servers   []app.MCPChoice
	rules     []app.PermissionRule
	skills    []app.SkillChoice
	levels    []string
	level     string
	// agentsStopped counts the calls of StopBackgroundAgents, and
	// agentsRunning is what each says it stopped.
	agentsStopped int
	agentsRunning int
}

// StopBackgroundAgents counts its calls.
func (c *controls) StopBackgroundAgents() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.agentsStopped++
	return c.agentsRunning
}

// Effort is the model's thinking levels as a test set them.
func (c *controls) Effort() ([]string, string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return slices.Clone(c.levels), c.level
}

// MCPChoices is the MCP servers a test set.
func (c *controls) MCPChoices() []app.MCPChoice {
	c.mu.Lock()
	defer c.mu.Unlock()
	return slices.Clone(c.servers)
}

// SkillChoices is the skills a test set.
func (c *controls) SkillChoices() []app.SkillChoice {
	c.mu.Lock()
	defer c.mu.Unlock()
	return slices.Clone(c.skills)
}

// PermissionRules is the rules a test set, in the default mode.
func (c *controls) PermissionRules() (string, []app.PermissionRule) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return "default", slices.Clone(c.rules)
}

// MemoryFiles is the context files a test set.
func (c *controls) MemoryFiles() []app.MemoryFile {
	c.mu.Lock()
	defer c.mu.Unlock()
	return slices.Clone(c.memFiles)
}

// ConfigChoices is the options a test set.
func (c *controls) ConfigChoices() []app.ConfigChoice {
	c.mu.Lock()
	defer c.mu.Unlock()
	return slices.Clone(c.options)
}

// ModelChoices is the models a test set.
func (c *controls) ModelChoices(context.Context) []app.ModelChoice {
	c.mu.Lock()
	defer c.mu.Unlock()
	return slices.Clone(c.models)
}

// Transcript is the session's conversation as a test set it.
func (c *controls) Transcript() []app.TranscriptEntry {
	c.mu.Lock()
	defer c.mu.Unlock()
	return slices.Clone(c.trans)
}

// answer is an answer the interface gave to a question.
type answer struct {
	id  string
	ans agent.Answer
}

func newControls() *controls {
	return &controls{
		mode: "default",
		info: app.SessionInfo{
			ID: "0123abcd", Path: "/nonexistent/session.jsonl", Cwd: "/home/jae/src/proj",
			Provider: "local", Model: "qwen3.8-27b", Thinking: "on",
			Commands: []string{"help", "exit", "clear", "compact", "think", "no_think", "model", "status", "loop", "noloop"},
			Version:  "test-build", Interactive: true, Resume: "gnosh -r 0123abcd",
		},
		ctxs: agent.ContextStatus{Window: 262144, Known: true, Point: 242144, Limit: 262144, Used: 31000},
		sigs: make(chan os.Signal, 4),
	}
}

func (c *controls) Submit(p agent.Prompt) (string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.submitErr != nil {
		return "", c.submitErr
	}
	c.prompts = append(c.prompts, p)
	id := fmt.Sprintf("p%d", len(c.prompts))
	c.ids = append(c.ids, id)
	return id, nil
}

func (c *controls) Interrupt() {
	c.mu.Lock()
	c.stops++
	c.mu.Unlock()
}

func (c *controls) Answer(id string, a agent.Answer) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.answerErr != nil {
		return c.answerErr
	}
	c.answers = append(c.answers, answer{id, a})
	return nil
}

func (c *controls) Queued() []agent.QueuedPrompt {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]agent.QueuedPrompt(nil), c.queue...)
}

func (c *controls) TakeQueued() []agent.Prompt {
	c.mu.Lock()
	defer c.mu.Unlock()
	var taken []agent.Prompt
	var kept []agent.QueuedPrompt
	for _, q := range c.queue {
		if q.Prompt.Hidden {
			kept = append(kept, q)
		} else {
			taken = append(taken, q.Prompt)
		}
	}
	c.queue = kept
	return taken
}

func (c *controls) Mode() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.mode
}

// SetVimMode notes the mode of the box's vim keys the session is told.
func (c *controls) SetVimMode(mode string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.vimModes = append(c.vimModes, mode)
}

func (c *controls) SetMode(mode string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if mode == "bypassPermissions" && !c.bypass {
		return errors.New("skipping permissions can only be switched to in a session that was started with it within reach")
	}
	c.mode = mode
	return nil
}

func (c *controls) Command(ctx context.Context, line string) (app.CommandResult, error) {
	c.mu.Lock()
	c.commands = append(c.commands, line)
	f := c.command
	c.mu.Unlock()
	if f != nil {
		return f(ctx, line)
	}
	word, _, _ := strings.Cut(strings.TrimPrefix(line, "/"), " ")
	for _, name := range c.info.Commands {
		if name == word {
			return app.CommandResult{Handled: true, Name: name, Text: "ran " + line, Quit: name == "exit"}, nil
		}
	}
	return app.CommandResult{}, nil
}

func (c *controls) State() agent.State {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.state
}

func (c *controls) Context() agent.ContextStatus {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.ctxs
}

func (c *controls) Info() app.SessionInfo {
	c.mu.Lock()
	defer c.mu.Unlock()
	info := c.info
	info.Mode = c.mode
	return info
}

func (c *controls) Usage() agent.Totals { return agent.Totals{} }
func (c *controls) Tasks() []app.Task {
	c.mu.Lock()
	defer c.mu.Unlock()
	return slices.Clone(c.tasks)
}

// Deliverables is the session's deliverables as a test set them.
func (c *controls) Deliverables() []app.Deliverable {
	c.mu.Lock()
	defer c.mu.Unlock()
	return slices.Clone(c.deliv)
}

func (c *controls) RunPrompt(context.Context) (agent.Prompt, bool) { return agent.Prompt{}, false }

func (c *controls) Signals() <-chan os.Signal { return c.sigs }

func (c *controls) Notify(kind, title, message string) {
	c.mu.Lock()
	c.notified = append(c.notified, kind)
	c.mu.Unlock()
}

func (c *controls) RunEnd(res agent.TurnResult, exit int) int { return exit }
func (c *controls) Wait(ctx context.Context) error            { return nil }

func (c *controls) Quit(reason string) {
	c.mu.Lock()
	c.quit = reason
	c.mu.Unlock()
}

// stopped is how many times the interface stopped the turn.
func (c *controls) stopped() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.stops
}

// sent is the text of every prompt the interface submitted.
func (c *controls) sent() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make([]string, len(c.prompts))
	for i, p := range c.prompts {
		out[i] = p.Text
	}
	return out
}

// The events of a session, as the app would report them, written the
// short way.

func busy() any               { return &agent.BusyEvent{} }
func idle() any               { return &agent.IdleEvent{} }
func turnStart() any          { return &agent.TurnStartEvent{} }
func said(s string) any       { return &agent.TextEvent{Text: s} }
func thought(s string) any    { return &agent.ThinkingEvent{Text: s} }
func note(s string) any       { return &agent.NoticeEvent{Level: agent.LevelInfo, Text: s} }
func warning(s string) any    { return &agent.NoticeEvent{Level: agent.LevelWarning, Text: s} }
func queued(ps ...string) any { return &agent.QueueEvent{Queued: queue(ps...)} }

func queue(ps ...string) []agent.QueuedPrompt {
	var out []agent.QueuedPrompt
	for i, p := range ps {
		out = append(out, agent.QueuedPrompt{ID: fmt.Sprintf("q%d", i+1), Prompt: agent.Prompt{Text: p}})
	}
	return out
}

// queueAs is one prompt that waits, under the id the session gave it.
func queueAs(id, words string) []agent.QueuedPrompt {
	return []agent.QueuedPrompt{{ID: id, Prompt: agent.Prompt{ID: id, Text: words}}}
}

func request(round int) any {
	return &agent.RequestEvent{Purpose: "main", Round: round, Attempt: 1, Provider: "local", Model: "qwen3.8-27b", Messages: 3, Estimate: 14200}
}

func promptOut(id, words string) any {
	return &agent.PromptEvent{ID: id, Prompt: agent.Prompt{ID: id, Text: words}}
}

func replyEnd(stop core.StopReason, blocks ...core.Block) any {
	return &agent.ReplyEvent{
		Purpose: "main", Round: 1, StopReason: stop, Message: core.Message{Role: core.RoleAssistant, Blocks: blocks},
		Usage: core.Usage{InputTokens: 14200, OutputTokens: 300},
	}
}

func turnEnd(reason agent.EndReason) any {
	return &agent.TurnEndEvent{Result: agent.TurnResult{
		Turn: "t1", Reason: reason, Rounds: 2, Duration: 42 * time.Second,
		Usage: core.Usage{InputTokens: 18200, OutputTokens: 1100},
	}}
}

func toolCall(id, name string) any { return &agent.ToolCallEvent{CallID: id, Name: name} }
func toolInput(id, piece string) any {
	return &agent.ToolInputEvent{CallID: id, Text: piece}
}

func toolStart(id, name string, args map[string]any) any {
	in, _ := json.Marshal(args)
	ev := &agent.ToolStartEvent{CallID: id, Name: name, Input: in}
	if cmd, _ := args["command"].(string); cmd != "" {
		ev.Action = core.Action{Kind: core.ActionExecute, Command: cmd}
	}
	return ev
}

func progress(id, out string) any {
	return &agent.ToolProgressEvent{CallID: id, Stream: "stdout", Text: out}
}

func toolEnd(id, name string, args map[string]any, res core.ToolResult, took time.Duration) *agent.ToolEndEvent {
	in, _ := json.Marshal(args)
	return &agent.ToolEndEvent{CallID: id, Name: name, Input: in, Result: res, Duration: took, Ran: true}
}

// pasteOf is a bracketed paste.
func pasteOf(s string) term.Event { return term.Paste{Text: s} }

// hookEvent is what the app reports when hooks ran: one record for each
// result named, all of the same hook.
func hookEvent(event, callID, tool string, results ...string) any {
	ev := &app.HookEvent{Event: hooks.Event(event), CallID: callID, Tool: tool}
	for _, r := range results {
		ev.Records = append(ev.Records, hooks.Record{
			Event: hooks.Event(event), Command: "check.sh", Result: hooks.Result(r), Duration: 12 * time.Millisecond,
		})
	}
	return ev
}

// start is the events that open a turn for the prompt the interface
// submitted last.
func (g *rig) start() *rig {
	g.t.Helper()
	g.c.mu.Lock()
	n := len(g.c.prompts)
	g.c.mu.Unlock()
	if n == 0 {
		g.t.Fatal("no prompt was submitted")
	}
	id := fmt.Sprintf("p%d", n)
	g.c.mu.Lock()
	words := g.c.prompts[n-1].Text
	meta := g.c.prompts[n-1].Meta
	g.c.mu.Unlock()
	return g.ev(busy(), turnStart(), &agent.PromptEvent{ID: id, Prompt: agent.Prompt{ID: id, Text: words, Meta: meta}}, request(1))
}
