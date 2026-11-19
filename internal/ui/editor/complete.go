package editor

import "strings"

// completion is the list of candidates while it is open under the box.
type completion struct {
	items []Candidate
	sel   int // the candidate chosen
	top   int // the first candidate shown
	// moved is true once the person has chosen with the arrows, as
	// opposed to the choice the list opened with.
	moved bool
	// text and cursor are what Complete was given: the candidates'
	// offsets are into this text.
	text   string
	cursor int
}

// replaced returns the part of the text that candidate c would replace.
func (c *completion) replaced(cand Candidate) string {
	return c.text[cand.Start:max(cand.End, c.cursor)]
}

// same reports whether taking the candidate would change nothing at all.
func (c *completion) same(cand Candidate) bool {
	return c.replaced(cand) == cand.Text
}

// typed reports whether the candidate is what the person has already
// typed in full: taking it would add nothing but, at most, the space
// that follows it.
func (c *completion) typed(cand Candidate) bool {
	return c.replaced(cand) == strings.TrimRight(cand.Text, " ")
}

// extends reports whether the candidate begins with what the person has
// typed, whatever the case of the letters: it completes the word, as
// opposed to merely resembling it.
func (c *completion) extends(cand Candidate) bool {
	typed := c.replaced(cand)
	return len(cand.Text) >= len(typed) && strings.EqualFold(cand.Text[:len(typed)], typed)
}

// move moves the choice by n, going round.
func (c *completion) move(n int) {
	c.sel = ((c.sel+n)%len(c.items) + len(c.items)) % len(c.items)
	c.moved = true
}

// menuKey handles a key while the list of candidates is open. done is
// false when the key is not one of the list's and the box should take it.
func (e *Editor) menuKey(cmd command) (res Result, done bool) {
	c := e.comp
	handled := Result{Handled: true}
	switch cmd {
	case cmdUp:
		c.move(-1)
	case cmdDown:
		c.move(1)
	case cmdPageUp:
		c.sel, c.moved = max(c.sel-e.menuHeight, 0), true
	case cmdPageDown:
		c.sel, c.moved = min(c.sel+e.menuHeight, len(c.items)-1), true
	case cmdTab:
		// Tab takes the candidate chosen. When that candidate is already
		// in the box, letter for letter, there is nothing to take, and
		// Tab goes on to the next one.
		cand := c.items[c.sel]
		switch {
		case !c.same(cand):
			e.accept(cand)
		case len(c.items) > 1:
			c.move(1)
		default:
			e.comp = nil
		}
	case cmdSubmit:
		// Enter takes the candidate chosen when it completes what was
		// typed or the person chose it with the arrows. Otherwise Enter
		// means what it always means, and what is sent is what was
		// typed: a command typed in full runs, and a candidate that
		// only resembles the word never takes its place unasked. On a
		// shell line Enter takes one only when it was chosen with the
		// arrows: a command run before begins with what was typed, and
		// Enter must run the line as typed, not an older one.
		cand := c.items[c.sel]
		if c.typed(cand) || !c.moved && (!c.extends(cand) || e.Mode() == ModeShell) {
			e.comp = nil
			return Result{}, false
		}
		e.accept(cand)
	case cmdEscape:
		e.comp = nil
		e.dismissed, e.hasDismissed = e.wordStart(), true
	default:
		return Result{}, false
	}
	e.skipRefresh = true
	return handled, true
}

// accept puts a candidate into the box, and opens the list again if the
// new text can be completed further (a directory just taken has files in
// it).
func (e *Editor) accept(cand Candidate) {
	c := e.comp
	e.comp = nil
	from, ok1 := e.posAt(cand.Start)
	to, ok2 := e.posAt(max(cand.End, c.cursor))
	if !ok1 || !ok2 {
		return
	}
	e.edit(from, to, clean(cand.Text), kindOther)
	e.fix()
	if !e.inTriggerWord() {
		return
	}
	e.refresh(false, true)
	if c := e.comp; c != nil {
		// Only worth showing if something in it goes further than what
		// was just taken.
		for _, it := range c.items {
			if !c.typed(it) {
				return
			}
		}
		e.comp = nil
	}
}

// Recomplete asks the application for candidates again, as when the text
// has just changed, where a list is open or would open by itself at the
// cursor: for candidates that came in after the list was made (a command
// of the person's own that answered later). Elsewhere it does nothing.
func (e *Editor) Recomplete() {
	if e.comp == nil && !e.inTriggerWord() {
		return
	}
	e.refresh(false, false)
}

// refresh asks the application for candidates and opens, updates or
// closes the list to match. explicit says the person asked with Tab: a
// single candidate is then taken at once. changed says the text changed
// since the list was last made, which decides what is chosen first.
func (e *Editor) refresh(explicit, changed bool) {
	old := e.comp
	e.comp = nil
	text, cursor := e.Text(), e.Cursor()
	var items []Candidate
	for _, cand := range e.complete(text, cursor) {
		end := max(cand.End, cursor)
		if cand.Start < 0 || cand.Start > cursor || end > len(text) {
			continue
		}
		// Both ends must be places the cursor could be.
		if _, ok := e.posAt(cand.Start); !ok {
			continue
		}
		if _, ok := e.posAt(end); !ok {
			continue
		}
		items = append(items, cand)
	}
	if len(items) == 0 {
		return
	}
	c := &completion{items: items, text: text, cursor: cursor}
	if explicit && len(items) == 1 {
		e.comp = c
		if c.same(items[0]) {
			e.comp = nil
			return
		}
		e.accept(items[0])
		return
	}
	// What is chosen first: a candidate that is exactly what was typed
	// always wins over one that merely resembles it; otherwise a choice
	// the person made with the arrows is kept; otherwise the first.
	exact, kept := -1, -1
	for i, it := range items {
		if exact < 0 && c.typed(it) {
			exact = i
		}
		if kept < 0 && old != nil && old.moved && it.Text == old.items[old.sel].Text && it.Start == old.items[old.sel].Start {
			kept = i
		}
	}
	switch {
	case changed && exact >= 0:
		c.sel = exact
	case kept >= 0:
		c.sel, c.moved = kept, true
	case exact >= 0:
		c.sel = exact
	}
	if old != nil {
		c.top = old.top
	}
	e.comp = c
}

// wordStart returns where the word the cursor is in, or just after,
// begins: after the last space, tab or collapsed paste before the cursor
// on its line.
func (e *Editor) wordStart() pos {
	s := e.lines[e.cur.line].s
	i := e.cur.col
	for i > 0 {
		c := s[i-1]
		if c == ' ' || c == '\t' {
			break
		}
		if c == pasteClose[2] && strings.HasSuffix(s[:i], pasteClose) {
			break
		}
		i--
	}
	return pos{e.cur.line, i}
}

// inTriggerWord reports whether the cursor is somewhere the list opens by
// itself: in a word that begins with one of the trigger characters, or
// anywhere on the line of a command, whose words after the first are for
// the command to complete (the name of a model after "/model").
//
// A shell line never opens it by itself, and a slash opens it only where
// it begins the box and makes the line a command: elsewhere (a path, a
// command named in a prompt, a route such as /me) Tab opens it, so that
// Enter sends or runs what was typed.
func (e *Editor) inTriggerWord() bool {
	if e.Mode() == ModeShell {
		return false
	}
	start := e.wordStart()
	if e.cur.line == 0 && e.Mode() == ModeCommand && start.col > 0 {
		return true
	}
	if start.col >= e.cur.col {
		return false
	}
	c := e.lines[start.line].s[start.col]
	if c == '/' && (start.line > 0 || start.col > 0) {
		return false
	}
	return strings.IndexByte(e.triggers, c) >= 0
}

// atTrigger reports whether typing should open the list now: the cursor
// is in a word that begins with a trigger character, and the person has
// not closed the list for that very word with Esc.
func (e *Editor) atTrigger() bool {
	if e.hasDismissed {
		if e.wordStart() == e.dismissed && e.inTriggerWord() {
			return false
		}
		e.hasDismissed = false
	}
	return e.inTriggerWord()
}

// cutLeft shortens s to width cells by dropping its beginning, which it
// marks with an ellipsis: candidates in one list mostly differ at the
// end.
func (e *Editor) cutLeft(s string, width int) string {
	if e.m.Width(s) <= width {
		return s
	}
	const ellipsis = "…"
	room := width - e.m.Width(ellipsis)
	if room < 1 {
		return e.m.Truncate(s, width, "")
	}
	// Find the longest ending that fits beside the ellipsis.
	var starts, widths []int
	for rest, at := s, 0; rest != ""; {
		c, w, after := e.m.NextGrapheme(rest)
		starts = append(starts, at)
		widths = append(widths, w)
		at += len(c)
		rest = after
	}
	from := len(s)
	for i := len(starts) - 1; i >= 0 && widths[i] <= room; i-- {
		room -= widths[i]
		from = starts[i]
	}
	return ellipsis + s[from:]
}
