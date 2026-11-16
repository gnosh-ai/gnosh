package editor

import (
	"sort"
	"strings"
	"unicode/utf8"
)

// How text is laid out on rows.
//
// A line of text is cut into rows no wider than the room there is. Every
// item of the line is on exactly one row and nothing is dropped, so every
// place the cursor can be has exactly one cell. The rules:
//
//   - A row ends after a space or tab when the next word does not fit, and
//     between characters that are written without spaces (wide characters
//     such as Chinese, and collapsed pastes). A word longer than a row is
//     cut wherever the row ends.
//   - Characters other than spaces and tabs stop one cell short of the
//     right edge. That last cell is for the space that follows a word, or
//     for the cursor at the end of the line. So typing a space never moves
//     a word that already fit, and the cursor after a word is never past
//     the edge.
//   - The place right after the last item of a row belongs to the next
//     row, where the next item is. When a line's last row is full to the
//     edge (it ends in a space in the last cell), the end of the line has
//     no cell there, and the line gets one more row, empty, for the cursor.
//   - A tab reaches to the next tab stop, counted from where the text
//     starts, and is cut short at the edge.

// row is one row of a line: the bytes of the line it shows.
type row struct{ start, end int }

// line is one line of the text with what was worked out for drawing it.
// A line whose text changes is replaced by a new one, so nothing here can
// go stale except rows, which also depends on the width.
type line struct {
	// s is the text, as the editor keeps it (see item.go).
	s string
	// rows is how the line is cut into rows at the editor's width, or nil
	// when that has not been worked out.
	rows []row
	// pastes holds what was worked out about each collapsed paste in the
	// line, by the offset at which the paste starts, so that a large
	// paste is measured once and not every time it is drawn or stepped
	// over.
	pastes map[int]pasteInfo
}

// pasteInfo is what was worked out about one collapsed paste.
type pasteInfo struct {
	end   int    // where the paste ends in its line
	label string // what is drawn for it
}

// The kinds of unit a row is made of.
const (
	unitText  uint8 = iota // a grapheme cluster, drawn as itself
	unitSpace              // a space
	unitTab                // a tab, drawn as spaces
	unitPaste              // a collapsed paste, drawn as its label
	unitMark               // a character with no cell of its own, drawn as a stand-in
)

// unit is one item of a line as it is drawn.
type unit struct {
	end  int   // where the item ends in the line
	w    int   // how many cells it takes
	kind uint8 // one of the unit constants
	// loose is true when a row may end just before the unit and just
	// after it: a wide character or a collapsed paste.
	loose bool
}

// unitAt describes the item of l that starts at off, drawn at cell col of
// its row. Only a tab's width depends on col.
func (e *Editor) unitAt(l *line, off, col int) unit {
	s := l.s
	// An ASCII character with ASCII after it, or nothing, is an item by
	// itself. With anything else after it, it may be the start of a longer
	// one (a space with a combining mark on it is one item, and not a
	// place to end a row).
	alone := off+1 == len(s) || s[off+1] < utf8.RuneSelf
	switch c := s[off]; {
	case c == '\t':
		w := e.tabw - col%e.tabw
		if room := e.cw - col; w > room {
			w = room
		}
		return unit{end: off + 1, w: max(w, 1), kind: unitTab}
	case c == ' ' && alone:
		return unit{end: off + 1, w: 1, kind: unitSpace}
	case c > 0x20 && c < 0x7f && alone:
		return unit{end: off + 1, w: 1, kind: unitText}
	}
	// A label or a stand-in is kept one cell short of a whole row, so the
	// cursor still has a cell after it.
	short := max(e.cw-1, 1)
	if strings.HasPrefix(s[off:], pasteOpen) {
		p := e.pasteAt(l, off)
		return unit{end: p.end, w: min(e.m.Width(p.label), short), kind: unitPaste, loose: true}
	}
	end := e.itemEnd(s, off)
	item := s[off:end]
	w := e.m.Width(item)
	if w == 0 {
		return unit{end: end, w: min(len(standIn(item)), short), kind: unitMark}
	}
	return unit{end: end, w: min(w, e.cw), kind: unitText, loose: w >= 2}
}

// pasteAt returns where the collapsed paste that starts at off in l ends
// and what is drawn for it, working them out the first time.
func (e *Editor) pasteAt(l *line, off int) pasteInfo {
	if p, ok := l.pastes[off]; ok {
		return p
	}
	end := e.itemEnd(l.s, off)
	p := pasteInfo{end: end, label: pasteLabel(l.s[off:end])}
	if l.pastes == nil {
		l.pastes = make(map[int]pasteInfo)
	}
	l.pastes[off] = p
	return p
}

// draw returns what is drawn for the unit u of l that starts at off, and
// which look it has.
func (e *Editor) draw(l *line, off int, u unit) (string, uint8) {
	item := l.s[off:u.end]
	switch u.kind {
	case unitSpace:
		return " ", unitText
	case unitTab:
		return strings.Repeat(" ", u.w), unitText
	case unitPaste:
		lab := e.pasteAt(l, off).label
		if e.m.Width(lab) > u.w {
			lab = e.m.Truncate(lab, u.w, "")
		}
		return lab, unitPaste
	case unitMark:
		st := standIn(item)
		if len(st) > u.w {
			st = st[:u.w]
		}
		return st, unitMark
	}
	if e.m.Width(item) > u.w {
		item = e.m.Truncate(item, u.w, "")
	}
	return item, unitText
}

// wrap cuts l into rows at the editor's width. skip is how many bytes at
// the start of the line are drawn elsewhere (the character that sets the
// mode, which is drawn as the prefix).
func (e *Editor) wrap(l *line, skip int) {
	s := l.s
	rows := make([]row, 0, 1+(len(s)-skip)/max(e.cw, 1))
	start, off, col := skip, skip, 0
	brk := -1 // where the row being filled may end, or -1
	for off < len(s) {
		u := e.unitAt(l, off, col)
		white := u.kind == unitSpace || u.kind == unitTab
		limit := e.cw
		if !white {
			limit--
		}
		if col == 0 || col+u.w <= limit {
			col += u.w
			if white || u.loose {
				brk = u.end
			}
			off = u.end
			continue
		}
		// The unit does not fit. End the row before it if a row may end
		// there, else where one last could, else here all the same.
		cut := off
		if !white && !u.loose && brk > start {
			cut = brk
		}
		rows = append(rows, row{start, cut})
		start, off, col, brk = cut, cut, 0, -1
	}
	rows = append(rows, row{start, len(s)})
	if col >= e.cw {
		// The last row is full to the edge: the end of the line gets a
		// row of its own.
		rows = append(rows, row{len(s), len(s)})
	}
	l.rows = rows
}

// laid returns line i with its rows worked out.
func (e *Editor) laid(i int) *line {
	l := &e.lines[i]
	if l.rows == nil {
		e.wrap(l, e.home(i))
	}
	return l
}

// setWidth sets how many cells a row has for text, and forgets every
// line's rows when that changed.
func (e *Editor) setWidth(cw int) {
	cw = max(cw, 1)
	if cw == e.cw {
		return
	}
	e.cw = cw
	e.forget()
}

// forget drops the rows worked out for every line.
func (e *Editor) forget() {
	for i := range e.lines {
		e.lines[i].rows = nil
	}
}

// rowOf returns which row of l the offset col is on. An offset where one
// row ends and the next begins is on the next.
func (l *line) rowOf(col int) int {
	i := sort.Search(len(l.rows), func(i int) bool { return l.rows[i].start > col }) - 1
	return max(i, 0)
}

// cellOf returns the cell, counted from the start of the text on the row,
// at which the item starting at offset col of row r is drawn.
func (e *Editor) cellOf(l *line, r row, col int) int {
	cell := 0
	for off := r.start; off < col && off < r.end; {
		u := e.unitAt(l, off, cell)
		cell += u.w
		off = u.end
	}
	return cell
}

// offsetAt returns the offset on row r that the cursor takes when it is
// wanted at cell want: the start of the item that covers that cell, or the
// last place on the row. last says that r is the last row of its line,
// where the cursor may stand after the last item; on any other row that
// place belongs to the next row.
func (e *Editor) offsetAt(l *line, r row, last bool, want int) int {
	off, cell := r.start, 0
	for off < r.end {
		u := e.unitAt(l, off, cell)
		if cell+u.w > want || u.end == r.end && !last {
			break
		}
		cell += u.w
		off = u.end
	}
	return off
}

// rowsBefore is how many rows the lines before line i take.
func (e *Editor) rowsBefore(i int) int {
	n := 0
	for j := 0; j < i; j++ {
		n += len(e.laid(j).rows)
	}
	return n
}

// cursorRow returns the row the cursor is on: counted from the first row
// of the text, and as a row of its own line.
func (e *Editor) cursorRow() (global, inLine int) {
	l := e.laid(e.cur.line)
	inLine = l.rowOf(e.cur.col)
	return e.rowsBefore(e.cur.line) + inLine, inLine
}

// cursorCell returns the cell the cursor is at, counted from the start of
// the text on its row.
func (e *Editor) cursorCell() int {
	l := e.laid(e.cur.line)
	return e.cellOf(l, l.rows[l.rowOf(e.cur.col)], e.cur.col)
}
