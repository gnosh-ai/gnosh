package ui

import (
	"context"
	"strings"
	"time"

	"github.com/gnosh-ai/gnosh/internal/agent"
	"github.com/gnosh-ai/gnosh/internal/app"
	"github.com/gnosh-ai/gnosh/internal/core"
	"github.com/gnosh-ai/gnosh/internal/markdown"
	"github.com/gnosh-ai/gnosh/internal/term"
	"github.com/gnosh-ai/gnosh/internal/text"
	"github.com/gnosh-ai/gnosh/internal/ui/editor"
)

// host is what a session asks of whoever owns the terminal and the
// goroutines: the interface's loop when gnosh runs, the tests' own when it
// is tested.
type host interface {
	// spawn runs job away from the loop, and then runs what job returns on
	// the loop, where the session's state may be touched.
	spawn(job func() (then func()))
	// suspend gives the terminal back to the shell and stops the process,
	// as Ctrl+Z does to any program. It returns when the shell has
	// continued the process and the terminal is the interface's again;
	// the caller erases the live region before and draws it afresh after.
	suspend() error
	// edit lets the person edit text in their own editor (VISUAL, EDITOR)
	// and returns what they saved. When it has given the terminal to the
	// editor, it has erased the live region first and made the renderer
	// start afresh afterwards.
	edit(text string) (string, error)
	// editFile lets the person edit a file in their own editor, in place,
	// as edit does a text.
	editFile(path string) error
	// size is the terminal's size as it stands, when it can be told.
	size() (cols, rows int, ok bool)
	// copy puts text on the person's clipboard through the terminal.
	copy(text string) error
	// title sets the terminal's title.
	title(text string) error
	// desktop raises a desktop notification through the terminal, when it
	// takes one; it reports whether it does.
	desktop(channel, text string) bool
	// openPanel takes the terminal's whole screen for the side panel
	// (panel.go), for a terminal of cols by rows, and openView for the
	// transcript view (transcript.go).
	openPanel(cols, rows int) (*term.Panel, error)
	openView(cols, rows int) (*term.Panel, error)
}

// screen is what a session draws on: the terminal's own scrollback
// (term.Renderer), or the whole screen with the side panel (term.Panel).
type screen interface {
	Commit(lines ...string) error
	SetLive(lines []string, cursorRow, cursorCol int) error
	Resize(cols, rows int) error
	Clear() error
	Reset()
}

// config is what a session is made with.
type config struct {
	theme Theme
	// profile is the colours the terminal takes, and light says it said
	// its background is light: what the theme option picks a theme from.
	// themed says theme is the interface's own, which the option may
	// change (a test's given one is kept).
	profile  text.Profile
	light    bool
	themed   bool
	metrics  text.Metrics
	now      func() time.Time
	controls app.Controls
	renderer *term.Renderer
	host     host
	cols     int
	rows     int
	home     string
	// selectKey is the key the terminal selects text with while the
	// mouse is reported (panel.go), when it is known.
	selectKey string
	// branch reads the git branch of a directory, or returns "".
	branch func(dir string) string
	// list lists a directory for file completion.
	files func(dir string) []fileEntry
	// project lists the project in a directory for the fuzzy @ search
	// (mention.go), or is nil for none.
	project func(dir string, ignored bool) []string
	// programs lists the programs on the PATH, for shell mode's Tab
	// (shellcomplete.go), or is nil for none. It is slow, and is run in
	// the background.
	programs func() []string
}

// phase is what the session is doing, as the line above the prompt box
// says it.
type phase int

const (
	phIdle     phase = iota // nothing
	phStarting              // a prompt is being taken in
	phRequest               // a request is out and nothing has come back
	phThinking              // the model's reasoning is arriving
	phWriting               // the answer is arriving
	phCall                  // the model is writing a tool call
	phTools                 // tool calls are running
	phRetry                 // waiting to send a failed request again
	phSummary               // a summary of the conversation is being written
	phWorking               // between two of the above; the session says what
)

// blockKind is a kind of thing written into the scrollback. Two blocks
// stand a blank line apart, except two of a kind that is one line long.
type blockKind int

const (
	blkNone blockKind = iota
	blkHeader
	blkPrompt
	blkAnswer
	blkThinking
	blkTool     // a tool call with something under its heading
	blkToolLine // a tool call that is its heading alone
	blkNotice
	blkAsk
	blkTurn
	blkText // what a command said
)

// sent is a prompt the person sent that has not been seen going out to
// the model yet.
type sent struct {
	id    string
	draft editor.Draft
}

// reply is the reply the model is writing.
type reply struct {
	// md renders the answer; it is nil until the answer's first piece.
	md *markdown.Renderer
	// begun says the answer has a line in the scrollback already.
	begun bool
	// fed is how many bytes of the answer md has been given, and lossy
	// says pieces after them were lost on the way: the rest then comes
	// with the reply's end.
	fed   int
	lossy bool
	// chars counts everything the reply holds so far, for the estimate
	// of how much has been written.
	chars int

	// thinking says reasoning is arriving, since thinkSince. think holds
	// all of it, thinkRest the part of it not yet in the scrollback when
	// detail is on, and thinkBegun whether that block has begun.
	thinking   bool
	thinkSince time.Time
	think      strings.Builder
	thinkRest  string
	thinkBegun bool
}

// session is the interactive session as the person sees it: everything
// that decides what is written into the scrollback and what the live
// region holds. It has no goroutine and no terminal of its own: the loop
// (or a test) hands it what the session and the terminal report, one
// thing at a time, and asks it to draw.
type session struct {
	th Theme
	// hookSaid is the reason of the last hook that blocked, when its
	// record showed it whole: the note that the turn goes on does not say
	// it again.
	hookSaid string
	// profile, light and themed are config's, for the theme option
	// (themecmd.go).
	profile text.Profile
	light   bool
	themed  bool
	m       text.Metrics
	now     func() time.Time
	c       app.Controls
	host    host
	// r is what the session draws on: inline, the terminal's scrollback,
	// panel while the side panel is on, or view's held screen while the
	// transcript view is open (transcript.go).
	r      screen
	inline *term.Renderer
	panel  *term.Panel
	view   *transcriptView
	// agents is the agent view while it is open over the session, and
	// leftAt when ← was pressed in the empty box (agentsover.go).
	// agentsHintUntil is when the status line stops saying "← for
	// agents", a few seconds after the session began.
	agents          *agentsOverlay
	leftAt          time.Time
	agentsHintUntil time.Time
	// changed are the files the session's calls changed, the newest
	// first, for the panel.
	changed []changedFile
	// selectKey is config's.
	selectKey string

	// cols is the width the session lays out for: the terminal's, or the
	// panel's left part while the panel is on. termCols is the
	// terminal's.
	cols, rows int
	termCols   int
	cwd, home  string
	branchOf   func(string) string
	listDir    func(string) []fileEntry
	// programs lists the PATH's programs, and progNames is the list as
	// last made, at progAt, or being made again when progLoading
	// (shellcomplete.go).
	programs    func() []string
	progNames   []string
	progAt      time.Time
	progLoading bool
	// listProject and index are the fuzzy @ search's lister and what it
	// last listed (mention.go); suggest is what the person's own @ search
	// command answered (filesuggest.go).
	listProject func(dir string, ignored bool) []string
	index       fileIndex
	suggest     suggested

	// pending are the lines that wait to be committed at the next frame,
	// last is the kind of block the last of them ends, and dirty says
	// the live region may have changed.
	pending []string
	last    blockKind
	dirty   bool
	// werr is the first error the terminal gave on a write.
	werr error

	// What the tests watch: every line committed, and every live region
	// set.
	onCommit func(lines []string)
	onLive   func(lines []string, row, col int)

	ed *editor.Editor
	// attach are attachments that came back with a prompt and go out
	// with the next one.
	attach []core.Block
	// sending are the prompts sent and not yet seen going out.
	sending []sent
	// lastInput is when a key was last pressed.
	lastInput time.Time
	// keys is the keybindings file as it was last read (keybindings.go),
	// nil before the first key.
	keys *keyFile
	// keyHeld is the first key of a chord of the keybindings, held for
	// the second since keyHeldAt.
	keyHeld   *term.Key
	keyHeldAt time.Time
	// chord says Ctrl+X was pressed and the key after it is awaited.
	chord bool
	// stopArmed is when Ctrl+X Ctrl+K was pressed once, waiting for the
	// second press that stops the background sub-agents.
	stopArmed time.Time
	// listing says a picker's list is being read (loadThen): heldInput is
	// what the person typed meanwhile, for the list; listAgain says how to
	// ask for it again; listGen counts the lists let go before they came.
	listing   bool
	heldInput []term.Event
	listAgain string
	listGen   int
	// refeed is input, set when the session is made: a list that came
	// takes in what was held for it by it (a call to input from there
	// would tie the slash commands' table to itself).
	refeed func(term.Event)

	// q is the question on the screen and qwait the ones behind it.
	q     *question
	qwait []*question
	// closedText is what the prompt box held when the last question
	// closed, and closedAt when: what is typed between an answer and a
	// masked question that follows it is taken out of the box again
	// (secretGap).
	closedText string
	closedAt   time.Time

	// busy says the session has work in hand: from its BusyEvent to its
	// IdleEvent. ph is what it is doing and since when.
	busy  bool
	ph    phase
	since time.Time
	// fall is what falls through the words of the line that says what
	// the session is doing, as it stood when the line was last drawn
	// (fall.go).
	fall fall
	// heard is when the session last reported anything.
	heard time.Time
	// stopping says the person stopped the turn and its end has not
	// come yet.
	stopping bool
	// hookStatus is what the hooks running now say they do (their
	// statusMessage), shown in place of the working line's own words.
	hookStatus string
	// model is the model main requests go to, estimate about how many
	// tokens the request that is out holds, and attempt which try it is.
	// load is what the model server says it is doing while the reply has
	// not begun (app.ServerLoadEvent), or "".
	model    string
	estimate int
	attempt  int
	load     string
	// The wait before a failed request is sent again: until when, the
	// kind of failure, and how many waits there have been since the
	// provider last answered, the first of them at retrySince.
	retryUntil time.Time
	retryKind  string
	retries    int
	retrySince time.Time

	reply reply
	calls []*call
	// queue is the prompts that wait for their turn, as the session
	// reports them, and loop whether /loop is on.
	queue []agent.QueuedPrompt
	loop  bool

	// cmdq are slash commands that wait for the turn to end, cmdRun the
	// one that is running, and cmdStop what stops it.
	cmdq    []string
	cmdRun  string
	cmdStop context.CancelFunc

	// What the status line says.
	info app.SessionInfo
	// recapped is the id of the session whose recap was shown (recap.go).
	recapped string
	// suggestion is the person's likely next prompt, shown in the empty
	// box until they type (promptSuggestionEnabled).
	suggestion string
	// failHint is what to try next after a turn failed, shown in the empty
	// box until the person sends something (failHintFor).
	failHint string
	// color is the colour of the prompt mark the session asked for
	// (/color), "" for the theme's own.
	color  string
	mode   string
	ctxs   agent.ContextStatus
	branch string
	facts  map[string]string
	detail bool
	// focus is focus view (focus.go): each run of tool calls is written
	// as one line, fold, when something else is written.
	focus bool
	fold  fold
	// vimSaid is the mode of the box's vim keys the session was last told
	// (reportVim).
	vimSaid string
	// custom is what the configured status line command printed, which is
	// shown in place of gnosh's own status line, set in by customPad.
	custom    []string
	customPad int
	// titled is the terminal title last set.
	titled string
	// tasks is the session's task list as it stands, and hideTasks says
	// Ctrl+T has put it out of sight.
	tasks     []app.Task
	hideTasks bool
	// deliv is what the agent has produced for the person and what it has
	// claimed, the latest version of each, as the session last gave it.
	deliv []app.Deliverable
	// tipped says the key that shows more has been named once.
	tipped bool

	// hint is a short notice under the status line, until hintUntil.
	hint      string
	hintUntil time.Time

	// idleSince is when the session last went idle, and idleTold whether
	// the Notification hooks were told of it.
	idleSince time.Time
	idleTold  bool

	// done says the session is over; code is the exit code and why the
	// reason, in the SessionEnd hooks' words. restarting says it ends for
	// /restart.
	done       bool
	restarting bool
	code       int
	why        string
}

// newSession makes a session. Nothing is drawn until start.
func newSession(cfg config) *session {
	s := &session{
		th: cfg.theme, profile: cfg.profile, light: cfg.light, themed: cfg.themed,
		m: cfg.metrics, now: cfg.now, c: cfg.controls, r: cfg.renderer, inline: cfg.renderer, host: cfg.host,
		cols: max(cfg.cols, 1), rows: max(cfg.rows, 1), termCols: max(cfg.cols, 1), home: cfg.home,
		branchOf: cfg.branch, listDir: cfg.files, listProject: cfg.project, programs: cfg.programs, selectKey: cfg.selectKey,
		facts: map[string]string{},
	}
	if s.now == nil {
		s.now = time.Now
	}
	s.refeed = s.input
	s.ed = editor.New(editor.Options{
		Theme:    s.th.Editor,
		Complete: s.complete,
		Now:      s.now,
		Metrics:  s.m,
		// Ten at once: a slash shows most of the commands without
		// scrolling, and a short terminal still has room for the box.
		MenuHeight: 10,
	})
	return s
}

// start writes the header and takes in what the session is.
func (s *session) start() {
	s.agentsHintUntil = s.now().Add(agentsHintFor)
	s.refresh()
	s.tasks = s.c.Tasks()
	s.readDeliverables()
	s.header()
	// (What in the keybindings file cannot be used is said at once.)
	s.keysNow()
	// (The project is listed for the fuzzy @ search while nothing waits,
	// and the PATH's programs for shell mode's Tab.)
	s.indexed()
	s.programNames()
	s.recap()
	if h, ok := s.c.(historian); ok {
		var drafts []editor.Draft
		for _, p := range h.History() {
			drafts = append(drafts, editor.Draft{Text: p})
		}
		s.ed.SetHistory(drafts)
	}
	s.dirty = true
}

// refresh reads again what the status line shows and the session can
// change behind the interface's back: its model, its mode, how full the
// model's window is, the git branch.
func (s *session) refresh() {
	s.info = s.c.Info()
	s.mode = s.c.Mode()
	s.ctxs = s.c.Context()
	s.cwd = s.info.Cwd
	if s.model == "" || !s.busy {
		s.model = s.info.Model
	}
	if s.branchOf != nil && s.cwd != "" {
		s.branch = s.branchOf(s.cwd)
	}
	if s.info.Color != s.color {
		// (/color, or a session opened again that had one.)
		s.color = s.info.Color
		s.th.tintPrompt(s.color)
		s.ed.SetTheme(s.th.Editor)
	}
	s.dirty = true
}

// header writes the wordmark and what the session is, once.
func (s *session) header() {
	th := &s.th
	s.begin(blkHeader)
	line := th.wordmark()
	if v := oneLine(s.info.Version); v != "" {
		line += " " + th.Quiet.Render(v)
	}
	s.put(fit(s.m, line, s.cols))
	model := oneLine(s.info.Model)
	if s.info.Provider != "" && model != "" {
		model = oneLine(s.info.Provider) + "/" + model
	}
	if model == "" {
		model = "no model"
	}
	what := th.Quiet.Render("model ") + model
	if lvl := oneLine(s.info.Thinking); lvl != "" {
		what += th.Quiet.Render(th.Sep+"thinking ") + lvl
	}
	s.put(wrap(s.m, what, s.cols, "", "  ")...)
	where := th.Quiet.Render("folder ") + showPath("", s.home, s.cwd)
	if s.branch != "" {
		where += th.Quiet.Render(" (" + s.branch + ")")
	}
	s.put(wrap(s.m, where, s.cols, "", "  ")...)
	s.put(wrap(s.m, th.Quiet.Render("? shows the keys"+th.Sep+"/help lists the commands"+th.Sep+"Esc stops a turn"), s.cols, "", "")...)
	s.put(wrap(s.m, th.credits(), s.cols, "", "")...)
}

// The addresses the header's last line names. gnosh's licence asks an
// interface built on gnosh to show "built on gnosh" with a link to
// gnoshURL, and the GnoVM's licence, gno.land's, asks an interface that
// includes the GnoVM to show "built on gno.land" with a link to gnoURL
// (LICENSE.md and internal/gnovm/LICENSE.md, the Additional Terms).
const (
	gnoshURL = "https://gnosh.ai"
	gnoURL   = "https://gno.land"
)

// credits is the header's last line: gnosh's address, the credit the
// GnoVM's licence asks for, and the command that shows the legal notices
// (DECISIONS item 139).
func (th *Theme) credits() string {
	return th.Profile.Hyperlink(gnoshURL, gnoshURL) + th.Quiet.Render(th.Sep+"built on gno.land (") +
		th.Profile.Hyperlink(gnoURL, gnoURL) + th.Quiet.Render(")"+th.Sep+"/license")
}

// begin starts a block of the given kind in the scrollback. No blank line
// stands between blocks: each begins with a mark of its own, and the
// screen is for what was said and done. (Jae, 14 November 2026: "remove
// the newline after prompt and before done etc, in general don't print
// empty lines".) The empty lines of the model's own answer, between its
// paragraphs, are the answer's and stay.
func (s *session) begin(kind blockKind) {
	if !s.fold.empty() {
		// (Focus view: the run of calls before this block is one line.)
		s.writeFold()
	}
	s.last = kind
}

// put adds lines to the block that was begun. They are committed at the
// next frame, in order, and never drawn again. Every line is laid out
// for the terminal's width by whoever made it; a line that is wider all
// the same (the terminal is narrower than the narrowest layout there is)
// is wrapped once more here, so that what is committed is what the
// terminal shows and nothing of it is cut off.
func (s *session) put(lines ...string) {
	for _, l := range lines {
		s.pending = append(s.pending, s.asRows(l)...)
	}
	s.dirty = true
}

// asRows returns a line as the rows it takes at the terminal's width: the
// line itself when it fits, as nearly every line does.
func (s *session) asRows(l string) []string {
	l = strings.TrimRight(l, " ")
	if s.m.VisibleWidth(l) <= s.cols {
		return []string{l}
	}
	rows := wrap(s.m, l, s.cols, "", "")
	for i, r := range rows {
		rows[i] = strings.TrimRight(r, " ")
	}
	return rows
}

// The words that lead a notice.
const (
	labelNote  = "note: "
	labelWarn  = "warning: "
	labelError = "error: "
)

// notice writes one notice into the scrollback: a quiet line, a warning or
// an error. The words are outside text and are made safe here.
func (s *session) notice(level agent.Level, words string) {
	th := &s.th
	label, style := th.NoteLabel.Render(labelNote), th.NoteText
	switch level {
	case agent.LevelWarning:
		label, style = th.WarnLabel.Render(labelWarn), text.Style{}
	case agent.LevelError:
		label, style = th.ErrorLabel.Render(labelError), text.Style{}
	}
	lines := safeLines(words)
	if len(lines) == 0 {
		return
	}
	s.begin(blkNotice)
	for i, l := range lines {
		first := "  "
		if i == 0 {
			first = label
		}
		s.put(wrap(s.m, style.Render(l), s.cols, first, "  ")...)
	}
}

// say writes a quiet line of gnosh's own into the scrollback.
func (s *session) say(words string) { s.notice(agent.LevelInfo, words) }

// flash shows a short notice under the status line for a few seconds. It
// is for what a key press calls for and nobody needs to read later.
func (s *session) flash(words string) {
	s.hint, s.hintUntil = words, s.now().Add(4*time.Second)
	s.dirty = true
}

// setPhase notes what the session is doing now.
func (s *session) setPhase(p phase) {
	if s.ph != p {
		s.ph, s.since = p, s.now()
	}
	s.dirty = true
}

// leave ends the session: the loop stops, erases the live region and
// returns code. why is the reason in the SessionEnd hooks' words.
func (s *session) leave(code int, why string) {
	s.done, s.code, s.why = true, code, why
}

// frame writes what is waiting: first the lines to commit, for good, and
// then the live region as it stands now. It is the only place the session
// writes to the terminal.
func (s *session) frame() {
	if len(s.pending) > 0 {
		if s.onCommit != nil {
			s.onCommit(s.pending)
		}
		s.keep(s.r.Commit(s.pending...))
		s.pending = s.pending[:0]
	}
	if s.agents != nil {
		s.agentsFrame()
		s.dirty = false
		return
	}
	if s.view != nil {
		s.viewFrame()
		s.dirty = false
		return
	}
	if s.panel != nil {
		s.keep(s.panel.SetPanel(s.panelLines(s.panel.TextWidth())))
	}
	lines, row, col := s.compose()
	if s.onLive != nil {
		s.onLive(lines, row, col)
	}
	s.keep(s.r.SetLive(lines, row, col))
	s.dirty = false
}

// keep remembers the first error a write to the terminal gave. A terminal
// that cannot be written to is gone, and the session ends.
func (s *session) keep(err error) {
	if err != nil && s.werr == nil {
		s.werr = err
	}
}

// resize takes in the terminal's new size. The renderer finds the live
// region again and draws it for the new size; what the session draws next
// is laid out for it, and what is in the scrollback stays as the terminal
// left it.
func (s *session) resize(cols, rows int) {
	if cols < 1 || rows < 1 || (cols == s.termCols && rows == s.rows) {
		return
	}
	s.termCols, s.rows = cols, rows
	if r, ok := s.c.(resizeTeller); ok {
		r.Resized()
	}
	if o := s.agents; o != nil {
		// The agent view is laid out again for the new size, as the
		// transcript view is.
		s.keep(o.scr.p.Resize(cols, rows))
		o.scr.drawn = nil
		s.relayout(cols)
		s.dirty = true
		return
	}
	if s.view != nil {
		// The view is laid out again for the new size; what the session
		// says meanwhile is laid out for the scrollback's.
		s.keep(s.view.p.Resize(cols, rows))
		s.refreshView(true)
		s.relayout(cols)
		s.dirty = true
		return
	}
	if s.panel != nil && !term.PanelFits(cols) {
		s.closePanel()
		s.flash("The terminal is too narrow for the side panel now; /panel brings it back once it is wider.")
	}
	layout := cols
	if s.panel != nil {
		s.keep(s.panel.Resize(cols, rows))
		layout, _ = s.panel.Size()
	} else {
		s.keep(s.r.Resize(cols, rows))
	}
	s.relayout(layout)
	s.dirty = true
}

// relayout lays out for a new width what waits to be drawn.
func (s *session) relayout(cols int) {
	s.cols = cols
	// Lines that wait to be committed were laid out for the old width.
	// In a narrower one they are wrapped again, so that the terminal
	// does not cut them off.
	waiting := s.pending
	s.pending = nil
	for _, l := range waiting {
		s.pending = append(s.pending, s.asRows(l)...)
	}
	if s.reply.md != nil {
		s.reply.md.SetWidth(cols)
	}
	if s.q != nil {
		s.q.rebuild(&s.th, s.m, cols)
	}
}

// tick lets time pass: what is shown with a clock on it is drawn again,
// what waited for a moment happens, and the session is asked whether it
// is still busy, so that the screen never says it is when it is not.
func (s *session) tick() {
	now := s.now()
	s.chordTimedOut()
	if s.q != nil && s.q.guarded && !now.Before(s.q.quiet) {
		s.q.guarded = false
		s.dirty = true
	}
	if q := s.q; q != nil && q.idle > 0 && !q.answered && now.Sub(q.idleSince(s.lastInput)) >= q.idle {
		s.questionAway(q)
	}
	if s.hint != "" && !now.Before(s.hintUntil) {
		s.hint = ""
		s.dirty = true
	}
	if s.busy || s.cmdRun != "" || s.q != nil || s.ed.Hint() != editor.HintNone {
		// Something on the screen counts seconds.
		s.dirty = true
	}
	if s.agents != nil {
		// The agent view counts seconds, and looks at the runs again.
		s.agents.v.tick()
		s.dirty = true
	}
	if !s.agentsHintUntil.IsZero() && !now.Before(s.agentsHintUntil) {
		s.agentsHintUntil = time.Time{}
		s.dirty = s.dirty || s.agentsOffered()
	}
	if s.busy && now.Sub(s.heard) > 3*time.Second {
		// Nothing may look busy with nothing in flight. The session
		// tells of going idle with an event; should that ever fail to
		// arrive, so that nothing has been heard for a while, its own
		// word decides.
		if st := s.c.State(); !st.Busy && st.Phase == agent.PhaseIdle {
			s.endReply()
			s.goIdle()
		}
	}
	s.notify(now)
}

// wake is when the loop should next call tick with nothing else going on,
// or the zero time when nothing on the screen waits for the clock.
func (s *session) wake() time.Time {
	now := s.now()
	var at time.Time
	soon := func(t time.Time) {
		if t.IsZero() {
			return
		}
		if t.Before(now) {
			t = now
		}
		if at.IsZero() || t.Before(at) {
			at = t
		}
	}
	if s.q != nil && s.q.guarded {
		soon(s.q.quiet)
	}
	if s.hint != "" {
		soon(s.hintUntil)
	}
	if s.busy || s.cmdRun != "" || s.q != nil || s.ed.Hint() != editor.HintNone {
		soon(now.Add(time.Second).Truncate(time.Second))
	}
	if s.agents != nil {
		soon(now.Add(agentsLook))
	}
	if !s.agentsHintUntil.IsZero() && s.agentsOffered() {
		soon(s.agentsHintUntil)
	}
	if (s.busy || s.cmdRun != "") && s.q == nil && s.th.Fall.moves() {
		// The fall goes on through the working line: the deeper the
		// thought, the sooner the next step.
		soon(now.Add(s.th.Fall.every(s.fall.depth)))
	}
	if s.info.NotifyHooks || s.host.desktop(s.info.NotifChannel, "") {
		if q := s.q; q != nil && !q.notified {
			soon(later(q.opened, s.lastInput).Add(askNotifyAfter))
		}
		if s.idleWaits() {
			soon(s.idleSince.Add(idleNotifyAfter))
		}
	}
	return at
}

// How long a question waits, and how long the session sits idle after a
// turn, before the person's Notification hooks are told (spec/cc
// interactive.md, section 6).
const (
	askNotifyAfter  = 6 * time.Second
	idleNotifyAfter = 60 * time.Second
)

// notify tells the Notification hooks, and the desktop through a terminal
// that takes such notes, that a question has waited a while with no key
// pressed, or that a turn ended a while ago and nothing was typed since:
// the person seems to be away.
func (s *session) notify(now time.Time) {
	hooks, desk := s.info.NotifyHooks, s.host.desktop(s.info.NotifChannel, "")
	if !hooks && !desk {
		return
	}
	tell := func(kind, title, message string) {
		if hooks {
			s.c.Notify(kind, title, message)
		}
		if desk {
			s.host.desktop(s.info.NotifChannel, message)
		}
	}
	if q := s.q; q != nil && !q.notified && !now.Before(later(q.opened, s.lastInput).Add(askNotifyAfter)) {
		q.notified = true
		title := firstNonEmpty(q.q.Title, "gnosh")
		tell("permission_prompt", title, "gnosh is waiting for an answer: "+title)
	}
	if s.idleWaits() && !now.Before(s.idleSince.Add(idleNotifyAfter)) {
		s.idleTold = true
		tell("idle_prompt", "gnosh", "gnosh has finished and waits for your next prompt")
	}
}

// pushAway is how long with no key pressed the person counts as away
// from the terminal, for a notification the model sends.
const pushAway = 30 * time.Second

// push sends a notification the model asked for (PushNotification) when
// the person seems to be away: a desktop note by their channel, and their
// Notification hooks, of the type push_notification. It says what it did.
func (s *session) push(msg string) string {
	if !s.lastInput.IsZero() && s.now().Sub(s.lastInput) < pushAway {
		return "not sent: the user is at the terminal (a key pressed in the last half minute) and reads what you write"
	}
	hooks, desk := s.info.NotifyHooks, s.host.desktop(s.info.NotifChannel, "")
	if !hooks && !desk {
		return "not sent: no way to notify reaches the user here (their preferredNotifChannel, or a Notification hook, would)"
	}
	if hooks {
		s.c.Notify("push_notification", "gnosh", msg)
	}
	if desk {
		s.host.desktop(s.info.NotifChannel, msg)
	}
	return "sent"
}

// idleWaits reports whether the hooks are still to be told that the
// session sits idle: a turn has ended, nothing was typed since, and they
// have not been told.
func (s *session) idleWaits() bool {
	return !s.busy && !s.idleTold && !s.idleSince.IsZero() && !s.lastInput.After(s.idleSince)
}

// later returns the later of two moments.
func later(a, b time.Time) time.Time {
	if b.After(a) {
		return b
	}
	return a
}

// goIdle notes that the session has nothing in hand.
func (s *session) goIdle() {
	s.busy, s.stopping = false, false
	s.ph, s.since = phIdle, s.now()
	s.idleSince, s.idleTold = s.now(), false
	s.retries = 0
	s.refresh()
	s.nextCommand()
}
