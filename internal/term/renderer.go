package term

import (
	"io"
	"strings"
	"sync"
)

// Renderer draws gnosh's interface in the terminal's own scrollback.
//
// What gnosh has finished saying is committed: written once, above, and
// from then on it belongs to the terminal. It is never redrawn and never
// erased, so the person can scroll back through it, select it and copy it
// with the terminal's own means. Below it is the live region: a few lines
// that are redrawn in place as things change (the answer as it streams,
// the input box, a status line).
//
// The renderer knows one thing about the screen: how many rows down from
// the top of the live region the cursor is. Everything it does is relative
// to that. It moves up with "cursor up", down with line feeds (which make
// room at the bottom of the screen when needed), and never names a row by
// number, because the screen is not gnosh's.
//
// It may also be told which row of the screen the cursor is on, as the
// terminal reports it (AskedCursor, CursorAnswer); it then follows that row
// through its own writes. Knowing it, the renderer keeps the live region
// on the bottom rows of the screen, as the input box of a program that
// owns the screen stays put: from the first frame on, when the region is
// shorter than the rows from its top to the screen's last, blank rows
// above it make up the difference, instead of the rows below it being left
// empty, and the next committed lines fill those blank rows first (Jae, 22
// November 2026: "the text field is not fixed on bottom of screen"). The
// first rows of the region may go on with the committed lines, as what is
// still being written does (an answer's open rows, a call in flight);
// those stand right under the committed lines, and the blank rows go below
// them (SetJoined). A resize, or the screen lent to another program, loses
// the row until the terminal is asked again (WantsCursor).
//
// Lines given to the renderer are already styled and already wrapped. Each
// must stand on its own: its colours end with it, and it takes exactly one
// row. The renderer enforces the second: it lets through only text, SGR
// styles and OSC 8 hyperlinks, cuts off what is wider than the terminal,
// and turns the terminal's own wrapping off while it draws, so that a line
// the terminal measures wider than gnosh did is clipped instead of pushing
// the lines after it down.
//
// Every update goes out in a single Write. A Renderer may be used from
// several goroutines.
type Renderer struct {
	mu     sync.Mutex
	out    io.Writer
	cols   int
	rows   int
	width  func(string) int
	synced bool
	reflow bool

	// lines is the live region as last asked for, all of it, and
	// wantRow, wantCol and wantShown are where the cursor was asked for.
	// joined is how many of its first lines go on with the committed
	// lines (SetJoined).
	lines     []rline
	wantRow   int
	wantCol   int
	wantShown bool
	joined    int

	// screen is what the terminal shows from the top of the live region
	// down: the last rows of lines, as they were drawn. row and col are
	// where the terminal's cursor is, counted from the live region's top
	// left; col is -1 when it is not known. shown is whether the cursor
	// is visible.
	screen []rline
	row    int
	col    int
	shown  bool
	// stale is true when the terminal changed size after screen was
	// drawn: the terminal may have rearranged it.
	stale bool

	// at is the row of the screen the cursor is on, counted from 0 at
	// the top, while known is true. owed counts the cursor position
	// questions the terminal has not answered yet, and void how many of
	// those no longer count (they were asked before a resize or a Reset).
	// moved is what the renderer did to the cursor since the last question
	// was asked, for its answer to be brought up to date: rows up as
	// negative numbers, line feeds as positive ones.
	at    int
	known bool
	owed  int
	void  int
	moved []int

	body []byte
	buf  []byte
}

// maxMoved is how many moves of the cursor are kept for an answer that
// is on its way. A terminal that has not answered by then is asked again.
const maxMoved = 4096

// NewRenderer makes a renderer that writes to out for a terminal of cols
// by rows cells. width says how many cells a piece of text takes; it is
// called with text that holds no escape sequences. The cursor must be at
// the start of an empty line; the live region begins there.
//
// The renderer starts out assuming a terminal that wraps its lines again
// when it is resized (see SetReflow) and that does not know synchronized
// output (see SetSynchronized).
func NewRenderer(out io.Writer, cols, rows int, width func(string) int) *Renderer {
	r := &Renderer{out: out, width: width, reflow: true, wantShown: true, shown: true}
	r.cols, r.rows = saneSize(cols, rows)
	return r
}

// saneSize replaces a size that cannot be right with the classic 80 by 24.
func saneSize(cols, rows int) (int, int) {
	if cols < 1 {
		cols = 80
	}
	if rows < 1 {
		rows = 24
	}
	return cols, rows
}

// SetSynchronized says whether the terminal knows synchronized output
// (mode 2026). When it does, each update is wrapped in the marks that make
// it appear all at once.
func (r *Renderer) SetSynchronized(on bool) {
	r.mu.Lock()
	r.synced = on
	r.mu.Unlock()
}

// SetReflow says what the terminal does to lines that no longer fit when
// it is made narrower: true (the default) if it wraps them again onto more
// rows, as iTerm2, Apple's Terminal, Ghostty and tmux do; false if it
// leaves every line on its row, as xterm does. Resize uses it to find the
// top of the live region again. It makes no difference while the cursor
// is hidden, because the renderer then keeps the cursor at the live
// region's top left, which stays put either way.
func (r *Renderer) SetReflow(on bool) {
	r.mu.Lock()
	r.reflow = on
	r.mu.Unlock()
}

// Commit writes lines above the live region, for good. If a live region is
// showing, the lines take its place on the screen and it is drawn again
// below them in the same write. A line that holds line feeds is committed
// as several lines.
func (r *Renderer) Commit(lines ...string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	commit := r.prepareCommit(lines)
	if len(commit) == 0 {
		return nil
	}
	return r.paint(commit)
}

// prepareCommit makes lines to commit safe, a row a line.
func (r *Renderer) prepareCommit(lines []string) []rline {
	var commit []rline
	for _, l := range lines {
		if !strings.Contains(l, "\n") {
			commit = append(commit, r.prepare(l))
			continue
		}
		for _, part := range strings.Split(l, "\n") {
			commit = append(commit, r.prepare(strings.TrimSuffix(part, "\r")))
		}
	}
	return commit
}

// SetLive replaces the live region with lines and says where to leave the
// cursor: on line cursorRow of lines, at cell cursorCol, both counted from
// 0. A negative cursorRow hides the cursor. Only what changed is redrawn,
// and nothing at all is written when nothing did.
//
// When there are more lines than the terminal has rows, the last rows are
// shown; the rest wait until there is room. When the renderer knows the
// cursor's row (see Renderer), blank rows above lines that do not reach the
// screen's last row take the rest, so that the region's last line is the
// screen's.
func (r *Renderer) SetLive(lines []string, cursorRow, cursorCol int) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.setLive(lines, cursorRow, cursorCol)
	return r.paint(nil)
}

// SetJoined says that the first n lines of the live region go on with the
// committed lines above it, as the open rows of a paragraph whose first
// rows were committed do (Jae, 26 November 2026: "green text has newlines
// where it shouldn't", of a paragraph seen in two pieces). They stand
// right under the committed lines, and the blank rows that keep the
// region on the bottom of the screen go below them instead of above. It
// counts from the next update on; zero, the default, puts the whole
// region below the blank rows.
func (r *Renderer) SetJoined(n int) {
	r.mu.Lock()
	r.joined = max(n, 0)
	r.mu.Unlock()
}

func (r *Renderer) setLive(lines []string, cursorRow, cursorCol int) {
	r.lines = r.prepareAll(lines)
	r.wantShown = cursorRow >= 0
	r.wantRow, r.wantCol = max(cursorRow, 0), max(cursorCol, 0)
}

// CommitLive is Commit and then SetLive, in one write. It is how lines
// move from the live region to the scrollback: Commit alone draws the
// old live region again under the lines it commits, so lines that were in
// that region would be on the screen twice, and at the bottom of the
// screen the terminal would scroll to make room for them, only for the
// region to shrink a moment later and leave the rows below it empty.
func (r *Renderer) CommitLive(commit, lines []string, cursorRow, cursorCol int) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.setLive(lines, cursorRow, cursorCol)
	return r.paint(r.prepareCommit(commit))
}

// WantsCursor reports whether the renderer has lost track of the row the
// cursor is on and no question that would tell it is on its way. The
// caller may then ask the terminal (Terminal.QueryCursorPosition) and say
// so with AskedCursor.
func (r *Renderer) WantsCursor() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return !r.known && r.owed == r.void
}

// AskedCursor tells the renderer that a cursor position question has just
// been sent to the terminal, after everything the renderer wrote so far.
// The answer goes to CursorAnswer.
func (r *Renderer) AskedCursor() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.owed++
	r.moved = r.moved[:0]
}

// CursorAnswer takes in the terminal's answer to a cursor position
// question: the row, counted from 1. Only the answer to the last question
// counts, brought up to date with what the renderer drew since it was
// asked, and only when the terminal was not resized and the screen not
// lent to another program in between. An answer to no question is
// ignored.
func (r *Renderer) CursorAnswer(row int) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.owed == 0 {
		return
	}
	r.owed--
	if r.void > 0 {
		r.void--
		return
	}
	if r.owed > 0 {
		// An answer to an earlier question: the last one's is to come.
		return
	}
	moved := r.moved
	r.moved = r.moved[:0]
	if row < 1 || row > r.rows {
		return
	}
	r.at, r.known = row-1, true
	for _, d := range moved {
		r.at = r.step(r.at, d)
	}
}

// follow keeps account of a move of the cursor the renderer is about to
// write: d rows up when d is negative, d line feeds when it is positive.
func (r *Renderer) follow(d int) {
	switch {
	case r.known:
		r.at = r.step(r.at, d)
	case r.owed > r.void:
		if len(r.moved) >= maxMoved {
			// Too long without an answer: the questions on their way
			// no longer count, and the terminal is asked again.
			r.void, r.moved = r.owed, r.moved[:0]
			return
		}
		r.moved = append(r.moved, d)
	}
}

// step is row at after the move d: the cursor stops at the top of the
// screen going up, and a line feed on the last row scrolls the screen
// and leaves the cursor where it is.
func (r *Renderer) step(at, d int) int {
	if d < 0 {
		return max(at+d, 0)
	}
	return min(at+d, r.rows-1)
}

// lose forgets the row of the cursor, and the answers on their way.
func (r *Renderer) lose() {
	r.known = false
	r.void = r.owed
	r.moved = r.moved[:0]
}

// prepareAll makes lines safe, reusing the work done for lines that were
// in the live region before.
func (r *Renderer) prepareAll(lines []string) []rline {
	out := make([]rline, len(lines))
	var seen map[string]rline
	for i, l := range lines {
		if i < len(r.lines) && r.lines[i].raw == l {
			out[i] = r.lines[i]
			continue
		}
		if seen == nil {
			seen = make(map[string]rline, len(r.lines))
			for _, old := range r.lines {
				seen[old.raw] = old
			}
		}
		if old, ok := seen[l]; ok {
			out[i] = old
		} else {
			out[i] = r.prepare(l)
		}
	}
	return out
}

// Size returns the terminal's size as the renderer last heard it.
func (r *Renderer) Size() (cols, rows int) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.cols, r.rows
}

// Resize tells the renderer the terminal's new size and draws the live
// region again.
//
// By the time gnosh hears of a resize the terminal has already rearranged
// what was on it, and each terminal does that its own way. The renderer
// works out how far above the cursor the top of the live region now is,
// erases from there to the end of the screen, and draws the live region
// afresh; everything above is treated as committed. The working out is
// exact while the cursor is hidden (the renderer keeps it at the live
// region's top left, which no rearranging moves). With the cursor shown
// inside the live region it follows SetReflow, and is exact for a
// terminal that behaves as told. Where a terminal behaves otherwise, a
// row or two of the old live region may be left behind above the new one,
// or a row or two of committed text just above it may be erased from the
// screen. The same is true when the old live region no longer fits on the
// screen: rows that scrolled off the top cannot be reached.
func (r *Renderer) Resize(cols, rows int) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	cols, rows = saneSize(cols, rows)
	if cols == r.cols && rows == r.rows {
		return nil
	}
	narrower := cols < r.cols
	wider := cols > r.cols
	r.cols, r.rows = cols, rows
	if narrower || wider {
		// Cut the lines again for the new width. A line that was cut
		// for a narrower terminal gets its full text back.
		for i, l := range r.lines {
			r.lines[i] = r.prepare(l.raw)
		}
	}
	r.stale = true
	// Where the cursor now is on the screen is the terminal's own
	// business; the region is drawn from its top again, without the
	// blank rows that kept it down, until the terminal says.
	r.lose()
	return r.paint(nil)
}

// Clear erases the live region and shows the cursor, leaving it where the
// live region began. Call it before gnosh exits and before handing the
// terminal to another program. The renderer then knows of nothing on the
// screen; the next Commit or SetLive starts at the cursor, which must
// again be at the start of an empty line. It still knows which row the
// cursor is on, until Reset says another program has had the screen.
func (r *Renderer) Clear() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.lines = nil
	r.wantShown, r.wantRow, r.wantCol = true, 0, 0
	err := r.paint(nil)
	r.forget()
	return err
}

// Reset makes the renderer forget what it drew, without writing anything.
// Use it when the screen is no longer what the renderer left (the process
// was stopped and continued, or another program wrote to the terminal):
// the next update draws the live region afresh at the cursor, which must
// be at the start of an empty line, and whatever is above is left alone.
// Which row the cursor is on is forgotten too (see WantsCursor).
func (r *Renderer) Reset() {
	r.mu.Lock()
	r.forget()
	r.lose()
	r.mu.Unlock()
}

// forget forgets what the renderer drew.
func (r *Renderer) forget() {
	r.screen = nil
	r.row, r.col = 0, 0
	r.stale = false
}

// window is the part of the live region that fits on the screen: its last
// rows.
func (r *Renderer) window() []rline {
	if len(r.lines) > r.rows {
		return r.lines[len(r.lines)-r.rows:]
	}
	return r.lines
}

// rowsAboveCursor works out, after a resize, how many rows the cursor is
// below the top of what was the live region.
func (r *Renderer) rowsAboveCursor() int {
	if !r.reflow || r.row >= len(r.screen) {
		return r.row
	}
	// The terminal wrapped every line that no longer fits onto more
	// rows and kept the cursor on its character.
	up := 0
	for _, l := range r.screen[:r.row] {
		rows, _ := r.wrapRows(l.plain, r.cols, 0)
		up += rows
	}
	_, within := r.wrapRows(r.screen[r.row].plain, r.cols, max(r.col, 0))
	return up + within
}

// padding is how many blank rows go between n committed lines and a live
// region of h rows, so that the region's last row is the screen's last row
// (see Renderer). The region's joined rows stand above them.
func (r *Renderer) padding(n, h int) int {
	if !r.known || r.stale || h == 0 {
		return 0
	}
	top := r.at - r.row
	if top < 0 {
		return 0
	}
	return max(r.rows-top-n-h, 0)
}

// paint makes the terminal show the lines of commit followed by the live
// region, starting at the row where the live region began, and then moves
// the top of the live region below the committed lines.
func (r *Renderer) paint(commit []rline) error {
	vis := r.window()
	n := len(commit)
	pad := r.padding(n, len(vis))
	// The region's first rows that go on with the committed lines stand
	// right under them, above the blank rows. (When the region is taller
	// than the screen its first rows are not shown, and there are no
	// blank rows.)
	head := 0
	if len(vis) == len(r.lines) {
		head = min(r.joined, len(vis))
	}
	desired := vis
	if n > 0 || pad > 0 {
		desired = make([]rline, 0, n+pad+len(vis))
		desired = append(desired, commit...)
		desired = append(desired, vis[:head]...)
		for range pad {
			desired = append(desired, rline{})
		}
		desired = append(desired, vis[head:]...)
	}
	old := r.screen
	b := r.body[:0]
	pos, col := r.row, r.col
	drew := false
	// erased is true when every row from the top of the live region
	// down has just been erased.
	erased := false
	// up and lf move the cursor, and keep account of where it goes.
	up := func(k int) {
		if k > 0 {
			b = append(b, CursorUp(k)...)
			r.follow(-k)
		}
	}
	lf := func() {
		b = append(b, '\n')
		r.follow(1)
	}

	if r.stale {
		// The terminal was resized: go to the top of what was the
		// live region, erase every row from there to the bottom of
		// the screen, and start over.
		up(r.rowsAboveCursor())
		b = append(b, SaveCursor...)
		for i := 0; i < r.rows; i++ {
			if i > 0 {
				b = append(b, "\x1b[B"...)
			}
			b = append(b, EraseLine...)
		}
		b = append(b, RestoreCursor...)
		old, pos, col, drew, erased = nil, 0, -1, true, true
		r.stale = false
	}

	// have is how many rows exist from the top of the live region down.
	// The cursor's own row always does.
	have := max(len(old), 1)
	// moveTo puts the cursor on row k, which exists.
	moveTo := func(k int) {
		if k < pos {
			up(pos - k)
		}
		for ; pos < k; pos++ {
			lf()
		}
		pos = k
	}
	// newRow makes the row after the last one and puts the cursor at
	// its start. At the bottom of the screen the terminal scrolls.
	newRow := func() {
		moveTo(have - 1)
		b = append(b, '\r')
		lf()
		pos = have
		have++
	}

	for k, l := range desired {
		if k < len(old) && old[k].text == l.text {
			continue
		}
		if k < have {
			moveTo(k)
			b = append(b, '\r')
		} else {
			newRow()
		}
		// Erase the row, then write it. The other order (write, then
		// erase what is left of the old text) would spare a terminal
		// without synchronized output a moment's blank row, but tmux
		// goes on counting cells erased that way as part of the
		// line, and wraps the line onto a blank row of its own when
		// the pane is made narrower.
		if !erased {
			b = append(b, EraseLine...)
		}
		b = append(b, l.text...)
		col, drew = -1, true
	}
	// The live region got shorter: erase the rows it gave up.
	for k := len(desired); k < len(old); k++ {
		moveTo(k)
		b = append(b, EraseLine...)
		drew = true
	}

	// Where the cursor goes: where it was asked for, or the top left of
	// the live region (its blank rows included) when it is hidden or
	// there is no live region.
	row, to := n, 0
	if r.wantShown && len(vis) > 0 {
		k := min(max(r.wantRow-(len(r.lines)-len(vis)), 0), len(vis)-1)
		if k >= head {
			k += pad
		}
		row += k
		to = min(r.wantCol, r.cols-1)
	}
	if row >= have {
		// Only committed lines were drawn: the live region, empty,
		// begins on the row after them.
		newRow()
		b = append(b, EraseLine...)
		col = 0
	}
	moveTo(row)
	if col != to {
		if to == 0 {
			b = append(b, '\r')
		} else {
			b = append(b, CursorColumn(to+1)...)
		}
		col = to
	}

	r.body = b
	r.screen = append(r.screen[:0], desired[n:]...)
	r.row, r.col = pos-n, col
	wasShown := r.shown
	r.shown = r.wantShown
	if len(b) == 0 && wasShown == r.shown {
		return nil
	}

	out := r.buf[:0]
	if drew {
		// Hold the display still, hide the cursor and turn the
		// terminal's own wrapping off for the length of the update.
		if r.synced {
			out = append(out, SyncBegin...)
		}
		if wasShown {
			out = append(out, HideCursor...)
		}
		out = append(out, AutoWrapOff...)
		out = append(out, b...)
		out = append(out, AutoWrapOn...)
		if r.shown {
			out = append(out, ShowCursor...)
		}
		if r.synced {
			out = append(out, SyncEnd...)
		}
	} else {
		// Only the cursor moves.
		out = append(out, b...)
		switch {
		case r.shown && !wasShown:
			out = append(out, ShowCursor...)
		case wasShown && !r.shown:
			out = append(out, HideCursor...)
		}
	}
	r.buf = out
	_, err := r.out.Write(out)
	return err
}
