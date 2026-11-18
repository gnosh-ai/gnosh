package editor

import (
	"strings"
	"time"
	"unicode/utf8"

	"github.com/gnosh-ai/gnosh/internal/term"
	"github.com/gnosh-ai/gnosh/internal/text"
)

// Mode is what kind of line the box holds. The first character decides.
type Mode int

const (
	// ModePrompt is a prompt for the model.
	ModePrompt Mode = iota
	// ModeCommand is a line that begins with "/": a command for gnosh.
	ModeCommand
	// ModeShell is a line that begins with "!": a shell command of the
	// person's own.
	ModeShell
)

// String names the mode: "prompt", "command" or "shell".
func (m Mode) String() string {
	switch m {
	case ModeCommand:
		return "command"
	case ModeShell:
		return "shell"
	}
	return "prompt"
}

// Action is something a key asks the application to do. The editor never
// sends, stops or exits anything itself.
type Action int

const (
	// ActionNone asks for nothing.
	ActionNone Action = iota
	// ActionSubmit hands over what was typed: Result.Text, Result.Pastes
	// and Result.Mode. The box is empty afterwards.
	ActionSubmit
	// ActionInterrupt asks to stop what is running (Esc or Ctrl+C while
	// the application has said it is busy).
	ActionInterrupt
	// ActionExit asks to leave gnosh (Ctrl+C twice, or Ctrl+D twice on an
	// empty box).
	ActionExit
	// ActionRewind asks for the list of earlier prompts to go back to
	// (Esc twice on an empty box).
	ActionRewind
	// ActionHelp asks to show or hide the list of keys ("?" typed into
	// an empty box).
	ActionHelp
	// ActionEditor asks to open the text in the person's own editor
	// (Ctrl+G). The application reads Text or Draft, runs the editor and
	// puts the result back with SetText.
	ActionEditor
	// ActionQueue asks for the prompts that are waiting to be put back
	// in the box (Up on an empty box, after SetQueued(true)).
	ActionQueue
)

// String names the action.
func (a Action) String() string {
	switch a {
	case ActionSubmit:
		return "submit"
	case ActionInterrupt:
		return "interrupt"
	case ActionExit:
		return "exit"
	case ActionRewind:
		return "rewind"
	case ActionHelp:
		return "help"
	case ActionEditor:
		return "editor"
	case ActionQueue:
		return "queue"
	}
	return "none"
}

// Hint is a short notice a key press calls for, such as the warning that
// a second press will clear the box. The application shows it where it
// shows its notices; Hint.String has the words.
type Hint int

const (
	// HintNone is no notice.
	HintNone Hint = iota
	// HintEscClear says a second Esc will clear the box.
	HintEscClear
	// HintCtrlCExit says a second Ctrl+C will leave gnosh.
	HintCtrlCExit
	// HintCtrlDExit says a second Ctrl+D will leave gnosh.
	HintCtrlDExit
	// HintStashed says the text was put aside with Ctrl+S.
	HintStashed
)

// String is the notice in gnosh's words, or empty for HintNone.
func (h Hint) String() string {
	switch h {
	case HintEscClear:
		return "Esc again to clear the box (Up brings it back)"
	case HintCtrlCExit:
		return "Ctrl+C again to leave gnosh"
	case HintCtrlDExit:
		return "Ctrl+D again to leave gnosh"
	case HintStashed:
		return "Put aside. Ctrl+S on an empty box brings it back"
	}
	return ""
}

// Span is a part of a text: the bytes from Start up to End.
type Span struct{ Start, End int }

// Draft is a text as the box holds it: the full text, and which parts of
// it are pastes shown collapsed. It is what goes into the history and what
// comes back out of it, so that a prompt with a large paste in it is shown
// the same way when it is recalled.
type Draft struct {
	// Text is the whole text, with every paste in full.
	Text string
	// Pastes are the parts of Text that stand in the box as collapsed
	// pastes, in order, not overlapping.
	Pastes []Span
}

// Candidate is one way to complete what is being typed, as the
// application's Complete function offers it.
type Candidate struct {
	// Text is what the candidate puts into the box.
	Text string
	// Start is where in the text the part being completed begins: the
	// bytes from Start up to the cursor are replaced by Text. End, when
	// it is past the cursor, makes the replaced part reach that far.
	// Both are byte offsets into the text Complete was given.
	Start, End int
	// Label is what the list shows, when that should differ from Text.
	Label string
	// Detail is a few words about the candidate, shown beside it.
	Detail string
	// Run says that Enter, taking the candidate, also sends the line: a
	// command that needs nothing more after its name.
	Run bool
}

// View is what the editor looks like at one width: its rows, styled and
// already cut to fit, and where the cursor is. It is what the renderer's
// live region takes.
type View struct {
	// Lines are the rows. Each takes at most the width in cells and ends
	// its own styles.
	Lines []string
	// CursorRow and CursorCol are the row of Lines and the cell on it
	// where the cursor is, both counted from 0. CursorRow is -1 when the
	// cursor is hidden.
	CursorRow, CursorCol int
}

// Result is what an event did.
type Result struct {
	// Handled is false when the event meant nothing to the editor, so
	// the application may give the key a meaning of its own (Shift+Tab,
	// Ctrl+L, Ctrl+O and so on).
	Handled bool
	// Changed is true when the text in the box changed.
	Changed bool
	// Action is what the application is asked to do, if anything.
	Action Action
	// Text, Pastes and Mode are what was sent, when Action is
	// ActionSubmit: the whole text with every paste in full (and with the
	// "!" or "/" that set the mode still at its start), the parts of it
	// that were collapsed pastes, and the mode.
	Text   string
	Pastes []Span
	Mode   Mode
	// Now is true when the prompt was sent with Ctrl+Enter, which asks
	// for it to be dealt with at once rather than queued.
	Now bool
	// Hint is a notice to show.
	Hint Hint
	// Cleared is true when Esc Esc or Ctrl+C emptied the box. The text is
	// kept: Up brings it back.
	Cleared bool
	// Truncated is true when the paste just taken was cut short by the
	// terminal. The text that did arrive is in the box.
	Truncated bool
}

// Options are the choices made when an editor is made. The zero value is
// a prompt box with no colour, no placeholder and no completion.
type Options struct {
	// Theme is the look. The zero Theme means DefaultTheme(text.NoColor).
	Theme Theme
	// Placeholder is shown, dimmed, while the box is empty.
	Placeholder string
	// MaxHeight is the most rows of text shown at once; more than that
	// scrolls. Zero means 10.
	MaxHeight int
	// MenuHeight is the most candidates shown at once. Zero means 6.
	MenuHeight int
	// TabWidth is how many cells apart the tab stops are. Zero means 4.
	TabWidth int
	// Plain makes a plain text field for use inside another widget: no
	// modes, no history, no putting aside, no actions for the
	// application. Esc and Ctrl+C, Ctrl+D on an empty field, Up on the
	// first row and Down on the last are left to the caller (they come
	// back with Handled false).
	Plain bool
	// Complete offers ways to complete the text at the cursor. It is
	// given the whole text and the cursor as a byte offset into it. Nil
	// means no completion.
	Complete func(text string, cursor int) []Candidate
	// Triggers are the characters that open the list of candidates when
	// a word begins with one of them, as it is typed. The list also
	// opens by itself after the first word of a command, for what the
	// command takes. Elsewhere it opens only on Tab. Empty means "/@".
	Triggers string
	// PasteLines and PasteChars say when a paste is shown collapsed: when
	// it has more lines than PasteLines or more characters than
	// PasteChars. Zero means 10 lines and 800 characters; a negative
	// number switches that limit off.
	PasteLines, PasteChars int
	// Now is the clock for telling a second press of Esc, Ctrl+C or
	// Ctrl+D from a new first press. Nil means there is no clock: the
	// second press counts whenever no other key came between.
	Now func() time.Time
	// DoublePress is how soon the second press must follow the first.
	// Zero means DoublePress, the constant.
	DoublePress time.Duration
	// Metrics is how wide characters are counted. It must be the measure
	// the renderer uses. The zero value is what most terminals do.
	Metrics text.Metrics
	// UndoLimit is how many steps of undo are kept. Zero means 1000.
	UndoLimit int
	// Vim starts the box with vim's keys, in INSERT mode (SetVim), and
	// VimRemaps are the INSERT-mode pairs that stand for Esc
	// (SetVimRemaps).
	Vim       bool
	VimRemaps []string
}

// DoublePress is how soon a second press of Esc, Ctrl+C or Ctrl+D must
// follow the first to count as the pair the specs describe.
const DoublePress = 800 * time.Millisecond

const (
	defaultMaxHeight  = 10
	defaultMenuHeight = 6
	defaultTabWidth   = 4
	defaultPasteLines = 10
	defaultPasteChars = 800
	defaultUndoLimit  = 1000
	defaultTriggers   = "/@"
	killRingSize      = 16 // how many deletions Ctrl+Y and Alt+Y can reach
	clearedKept       = 10 // how many cleared drafts Up can bring back
	firstWidth        = 78 // the room for text assumed before the first Render
)

// press is a key that does something on its second press, and when it
// was first pressed.
type press struct {
	cmd command
	at  time.Time
}

// saved is a text put by for later with where the cursor was in it.
type saved struct {
	text string // as the editor keeps it
	cur  pos
}

// Editor is the box a person types into. It is a plain value with no
// terminal behind it: Handle takes what the terminal reported and Render
// says what to draw. It is not safe for use by several goroutines at once.
//
// Moving up and down goes by rows, which depend on the width, so Handle
// uses the width of the last Render.
type Editor struct {
	th          Theme
	m           text.Metrics
	plain       bool
	complete    func(string, int) []Candidate
	triggers    string
	now         func() time.Time
	pair        time.Duration // how soon a second press must follow the first
	placeholder string
	maxHeight   int
	menuHeight  int
	tabw        int
	pasteLines  int
	pasteChars  int
	undoLimit   int
	pw          int // cells the prefix takes

	lines []line // the text; never empty
	cur   pos    // the cursor
	want  int    // the cell Up and Down aim for, or -1

	cw  int // cells for text on a row, as of the last Render
	top int // the first row of the text that is shown

	undo, redo []change
	lastKind   editKind // the kind of edit the event before this one made
	thisKind   editKind // the kind of edit this event has made

	ring    []string // deleted text, newest last, as the editor keeps it
	killing bool     // this event deleted into the ring
	wasKill bool     // the event before it did
	yanking bool     // this event put text back from the ring
	wasYank bool     // the event before it did
	yankAt  pos      // where that text starts
	yankEnd pos      // and ends
	yankIdx int      // which entry of the ring it is

	hist    []entry       // the prompts Up and Down walk, oldest first
	found   []entry       // the prompts Ctrl+R searches, when not hist
	walk    int           // the entry of hist in the box, or -1 for the draft
	kept    map[int]saved // what each entry visited, and the draft, was left as
	stashed []saved       // texts put aside with Ctrl+S

	srch         *search     // the history search, when one is open
	comp         *completion // the list of candidates, when it is open
	dismissed    pos         // the word whose list Esc closed
	hasDismissed bool

	locked, busy, queued bool
	armed                press // the key waiting for its second press

	vim vimState // vim's keys (vim.go)

	// What the event being handled has done so far.
	changed     bool // the text changed
	typed       bool // by typing, deleting or pasting
	keepWant    bool // the cursor moved up or down
	skipRefresh bool // the list of candidates is already up to date
}

// New makes an editor with an empty box.
func New(o Options) *Editor {
	e := &Editor{
		th:          o.Theme,
		m:           o.Metrics,
		plain:       o.Plain,
		complete:    o.Complete,
		triggers:    o.Triggers,
		now:         o.Now,
		pair:        o.DoublePress,
		placeholder: o.Placeholder,
		maxHeight:   o.MaxHeight,
		menuHeight:  o.MenuHeight,
		tabw:        o.TabWidth,
		pasteLines:  o.PasteLines,
		pasteChars:  o.PasteChars,
		undoLimit:   o.UndoLimit,
		lines:       []line{{}},
		want:        -1,
		walk:        -1,
		cw:          firstWidth,
	}
	if e.th == (Theme{}) {
		e.th = DefaultTheme(text.NoColor)
	}
	if e.triggers == "" {
		e.triggers = defaultTriggers
	}
	if e.pair <= 0 {
		e.pair = DoublePress
	}
	if e.maxHeight < 1 {
		e.maxHeight = defaultMaxHeight
	}
	if e.menuHeight < 1 {
		e.menuHeight = defaultMenuHeight
	}
	if e.tabw < 1 {
		e.tabw = defaultTabWidth
	}
	if e.pasteLines == 0 {
		e.pasteLines = defaultPasteLines
	}
	if e.pasteChars == 0 {
		e.pasteChars = defaultPasteChars
	}
	if e.undoLimit < 1 {
		e.undoLimit = defaultUndoLimit
	}
	e.measurePrefix()
	e.SetVimRemaps(o.VimRemaps)
	if o.Vim && !e.plain {
		e.SetVim(true)
	}
	return e
}

// Handle takes one event from the terminal and says what it did. Events
// that are not keys or pastes mean nothing to it.
func (e *Editor) Handle(ev term.Event) Result {
	if e.locked {
		return Result{}
	}
	var res Result
	switch ev := ev.(type) {
	case term.Key:
		e.begin()
		before := e.cur
		res = e.key(ev)
		e.finish(&res, before)
	case term.Paste:
		e.begin()
		before := e.cur
		e.armed = press{}
		res = e.paste(ev)
		if res.Changed || e.changed {
			// A selection does not outlast a change under it.
			e.vimLeaveVisual()
		}
		e.finish(&res, before)
	}
	return res
}

// begin gets ready for an event.
func (e *Editor) begin() {
	e.changed, e.typed, e.keepWant, e.skipRefresh = false, false, false, false
	e.thisKind = kindNone
	e.wasKill, e.killing = e.killing, false
	e.wasYank, e.yanking = e.yanking, false
}

// finish tidies up after an event: the cursor is put where it may be, the
// list of candidates follows the text, and the result says whether the
// text changed.
func (e *Editor) finish(res *Result, before pos) {
	e.lastKind = e.thisKind
	if !e.keepWant {
		e.want = -1
	}
	e.fix()
	if e.changed {
		res.Changed = true
	}
	if e.srch != nil || e.complete == nil || e.skipRefresh {
		return
	}
	switch {
	case e.comp != nil && (e.changed || e.cur != before):
		e.refresh(false, e.changed)
	case e.comp == nil && e.changed && e.typed && e.atTrigger():
		e.refresh(false, true)
	}
}

// Text returns everything in the box, with every collapsed paste in full.
// A "!" or "/" that sets the mode is its first character.
func (e *Editor) Text() string {
	var b strings.Builder
	for i := range e.lines {
		if i > 0 {
			b.WriteByte('\n')
		}
		appendPlain(&b, e.lines[i].s)
	}
	return b.String()
}

// Draft returns everything in the box, with which parts of it are
// collapsed pastes.
func (e *Editor) Draft() Draft { return draftOf(e.whole()) }

// IsEmpty reports whether the box holds nothing at all.
func (e *Editor) IsEmpty() bool {
	return len(e.lines) == 1 && e.lines[0].s == ""
}

// Mode returns what kind of line the box holds, which its first character
// decides. A plain field is always in ModePrompt.
func (e *Editor) Mode() Mode {
	if e.plain || e.lines[0].s == "" {
		return ModePrompt
	}
	switch e.lines[0].s[0] {
	case '!':
		return ModeShell
	case '/':
		return ModeCommand
	}
	return ModePrompt
}

// MenuOpen reports whether something of the editor's own is showing under
// the box and taking keys: the list of candidates or the history search.
func (e *Editor) MenuOpen() bool { return e.comp != nil || e.srch != nil }

// Searching reports whether the history search is open.
func (e *Editor) Searching() bool { return e.srch != nil }

// Cursor returns where the cursor is, as a byte offset into Text.
func (e *Editor) Cursor() int { return e.offsetOf(e.cur) }

// SetCursor moves the cursor to a byte offset into Text. An offset inside
// a character or a collapsed paste goes to the nearest place a cursor can
// be.
func (e *Editor) SetCursor(offset int) {
	p, _ := e.posAt(max(offset, 0))
	p.col = e.itemBefore(e.lines[p.line].s, p.col, e.home(p.line))
	e.cur = p
	e.want = -1
	e.fix()
}

// SetText puts text in place of everything in the box, with the cursor at
// its end. It is one step of the undo history, so Ctrl+_ brings back what
// was there. Anything showing under the box is closed.
func (e *Editor) SetText(s string) { e.SetDraft(Draft{Text: s}) }

// SetDraft is SetText for a text some of which is to be shown as
// collapsed pastes: a prompt taken back from the queue, or one that a hook
// refused.
func (e *Editor) SetDraft(d Draft) {
	e.closeAll()
	e.kept = nil
	e.replaceAll(internalOf(d), -1)
	e.lastKind = kindNone
	e.want = -1
	e.fix()
}

// Insert puts text in at the cursor, as if it had been typed. It is never
// collapsed.
func (e *Editor) Insert(s string) {
	e.closeAll()
	e.edit(e.cur, e.cur, clean(s), kindOther)
	e.lastKind = kindNone
	e.want = -1
	e.fix()
}

// Reset empties the box and forgets its undo history. The prompt history,
// what was deleted and what was put aside are kept.
func (e *Editor) Reset() {
	e.closeAll()
	e.lines = []line{{}}
	e.cur, e.top, e.want = pos{}, 0, -1
	e.undo, e.redo = nil, nil
	e.walk, e.kept = -1, nil
	e.lastKind = kindNone
	e.armed = press{}
	if e.vim.grouping {
		// A change of the vim keys being made starts again from here.
		e.vim.grouping = false
		e.vimGroupBegin()
	}
}

// closeAll closes the list of candidates and the history search, putting
// back what the box held before the search.
func (e *Editor) closeAll() {
	if e.srch != nil {
		e.cancelSearch()
	}
	e.comp = nil
	e.hasDismissed = false
}

// SetLocked locks or unlocks the box. A locked box takes no events and
// shows no cursor: something else has the keyboard.
func (e *Editor) SetLocked(locked bool) { e.locked = locked }

// Locked reports whether the box is locked.
func (e *Editor) Locked() bool { return e.locked }

// SetBusy tells the editor whether something is running that Esc and
// Ctrl+C should stop. While it is, those keys ask for ActionInterrupt and
// leave the text alone.
func (e *Editor) SetBusy(busy bool) { e.busy = busy }

// SetQueued tells the editor whether prompts are waiting in the
// application's queue. While they are, Up on an empty box asks for them
// back (ActionQueue) instead of walking the history.
func (e *Editor) SetQueued(queued bool) { e.queued = queued }

// SetPlaceholder changes what is shown while the box is empty.
func (e *Editor) SetPlaceholder(s string) { e.placeholder = s }

// SetMaxHeight changes the most rows of text shown at once.
func (e *Editor) SetMaxHeight(rows int) {
	if rows < 1 {
		rows = defaultMaxHeight
	}
	e.maxHeight = rows
}

// SetTheme changes the look.
func (e *Editor) SetTheme(t Theme) {
	e.th = t
	e.measurePrefix()
}

// Stashed returns how many texts are put aside with Ctrl+S.
func (e *Editor) Stashed() int { return len(e.stashed) }

// Hint returns the notice that is in force now: the warning of the key
// that is waiting for its second press, until its time runs out.
func (e *Editor) Hint() Hint {
	if !e.again(e.armed, e.armed.cmd) {
		return HintNone
	}
	switch e.armed.cmd {
	case cmdEscape:
		if !e.IsEmpty() {
			return HintEscClear
		}
	case cmdInterrupt:
		return HintCtrlCExit
	case cmdEOF:
		return HintCtrlDExit
	}
	return HintNone
}

// arm notes that cmd was pressed once.
func (e *Editor) arm(cmd command) {
	e.armed = press{cmd: cmd}
	if e.now != nil {
		e.armed.at = e.now()
	}
}

// again reports whether was, the key that was waiting, is cmd and its
// time has not run out.
func (e *Editor) again(was press, cmd command) bool {
	if cmd == cmdNone || was.cmd != cmd {
		return false
	}
	return e.now == nil || e.now().Sub(was.at) <= e.pair
}

// Collapsed returns the draft as the box shows it: its text, with each
// collapsed paste standing as its label ("[pasted 120 lines]"). It is for
// showing a prompt that was sent without filling the screen with what was
// pasted into it.
func (d Draft) Collapsed() string {
	s := internalOf(d)
	if !strings.Contains(s, pasteOpen) {
		return plain(s)
	}
	var b strings.Builder
	for {
		i := strings.Index(s, pasteOpen)
		if i < 0 {
			appendPlain(&b, s)
			break
		}
		appendPlain(&b, s[:i])
		end := strings.Index(s[i:], pasteClose)
		if end < 0 {
			appendPlain(&b, s[i:])
			break
		}
		end += len(pasteClose)
		b.WriteString(pasteLabel(s[i : i+end]))
		s = s[i+end:]
	}
	return b.String()
}

// draftOf turns text as the editor keeps it into a Draft.
func draftOf(s string) Draft {
	if !strings.Contains(s, pasteOpen) {
		return Draft{Text: plain(s)}
	}
	var b strings.Builder
	var spans []Span
	for {
		i := strings.Index(s, pasteOpen)
		if i < 0 {
			appendPlain(&b, s)
			break
		}
		appendPlain(&b, s[:i])
		end := strings.Index(s[i:], pasteClose)
		if end < 0 {
			end = len(s) - i
		} else {
			end += len(pasteClose)
		}
		from := b.Len()
		appendPlain(&b, s[i:i+end])
		spans = append(spans, Span{from, b.Len()})
		s = s[i+end:]
	}
	return Draft{Text: b.String(), Pastes: spans}
}

// internalOf turns a Draft into text as the editor keeps it. Spans that
// make no sense (out of order, overlapping, outside the text) are left
// out, and the text is taken as typed.
func internalOf(d Draft) string {
	at := 0
	for _, sp := range d.Pastes {
		if sp.Start < at || sp.End < sp.Start || sp.End > len(d.Text) {
			return clean(d.Text)
		}
		at = sp.End
	}
	if len(d.Pastes) == 0 {
		return clean(d.Text)
	}
	var b strings.Builder
	at = 0
	for _, sp := range d.Pastes {
		b.WriteString(clean(d.Text[at:sp.Start]))
		if sp.End > sp.Start {
			b.WriteString(encodePaste(clean(d.Text[sp.Start:sp.End])))
		}
		at = sp.End
	}
	b.WriteString(clean(d.Text[at:]))
	return b.String()
}

// offsetOf returns the byte offset into Text of the place p.
func (e *Editor) offsetOf(p pos) int {
	n := 0
	for i := 0; i < p.line; i++ {
		n += plainLen(e.lines[i].s) + 1
	}
	return n + plainLen(e.lines[p.line].s[:p.col])
}

// posAt returns the place at a byte offset into Text. exact is false when
// the offset is inside a collapsed paste (the place is then the end of
// the paste) or past the end of the text (the place is then its end).
func (e *Editor) posAt(offset int) (p pos, exact bool) {
	for i := range e.lines {
		s := e.lines[i].s
		n := plainLen(s)
		if offset <= n {
			col, ok := lineOffset(s, offset)
			return pos{i, col}, ok
		}
		offset -= n + 1
	}
	return e.end(), false
}

// lineOffset returns the offset into s, a line as the editor keeps it,
// of the place that is offset bytes into plain(s). exact is false when
// that place is inside a collapsed paste; the offset returned is then the
// end of the paste.
func lineOffset(s string, offset int) (col int, exact bool) {
	if !strings.Contains(s, pasteOpen) {
		return min(offset, len(s)), utf8.RuneStart(byteAt(s, offset))
	}
	in := 0 // how far into s the walk has come
	for {
		i := strings.Index(s[in:], pasteOpen)
		if i < 0 || offset <= i {
			return in + offset, utf8.RuneStart(byteAt(s, in+offset))
		}
		offset -= i
		in += i
		end := strings.Index(s[in:], pasteClose)
		if end < 0 {
			return len(s), false
		}
		n := plainLen(s[in : in+end])
		in += end + len(pasteClose)
		if offset < n {
			return in, false
		}
		offset -= n
	}
}

// byteAt is the byte of s at i, or an ASCII byte when i is its end, so
// that the end of a string counts as the start of a character.
func byteAt(s string, i int) byte {
	if i >= len(s) {
		return 'x'
	}
	return s[i]
}
