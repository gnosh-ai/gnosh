package model

import (
	"fmt"
	"image"
	"maps"
	"slices"
	"strconv"
	"strings"
	"time"

	"charm.land/bubbles/v2/key"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/crush/internal/agent"
	"github.com/charmbracelet/crush/internal/message"
	uv "github.com/charmbracelet/ultraviolet"
	"github.com/charmbracelet/x/ansi"
)

// ---- State

// explorerSize is how much of the terminal the frame explorer takes.
type explorerSize int

const (
	explorerHalf explorerSize = iota
	explorerFull
)

// explorerTab is a view of the explorer.
type explorerTab int

const (
	explorerTree explorerTab = iota
	explorerPulse
	explorerContext
	explorerTabCount
)

var explorerTabNames = [explorerTabCount]string{"Tree", "Pulse", "Context"}

// explorerHalfWidth is the narrowest terminal the explorer takes half of,
// each side keeping about 80 columns.
const explorerHalfWidth = 160

// explorer is the frame explorer: the session's frames over half of the
// terminal or all of it.
type explorer struct {
	open bool
	// want is the size asked for; a terminal too small for half draws it
	// full.
	want explorerSize
	tab  explorerTab
	// following is whether the selection is the frame the agent is in;
	// otherwise selected is the entry selected, by key, and seen how many
	// entries there were when following stopped.
	following bool
	selected  string
	seen      int
	// flipped is the entries folded or open against their default: a
	// finished top-level task folded, any other open.
	flipped map[string]bool
	// searching is whether / reads a query; query is what it read, and
	// from the selection to put back when the search is dropped.
	searching bool
	query     string
	from      explorerSelection
	// row is the Context tab's selection among the biggest rows; lane and
	// pulse the Pulse tab's, a lane and an entry in it, or with onWaits an
	// entry of what waits on the user.
	row     int
	lane    pulseLane
	pulse   int
	onWaits bool
	// work is the selected frame's work and edits, as last worked out.
	work frameWorkCache
	// back is the focus to give back on closing.
	back uiFocusState
}

// explorerSelection is what the explorer selects, the agent's frame or the
// entry selected, and the entries it folds against their default.
type explorerSelection struct {
	following bool
	selected  string
	flipped   map[string]bool
}

// selection is what the explorer selects now.
func (e *explorer) selection() explorerSelection {
	return explorerSelection{following: e.following, selected: e.selected, flipped: maps.Clone(e.flipped)}
}

// restore selects s again.
func (e *explorer) restore(s explorerSelection) {
	e.following, e.selected, e.flipped = s.following, s.selected, maps.Clone(s.flipped)
}

// halfFits reports whether the terminal keeps two sides of about 80
// columns outside compact mode.
func (m *UI) halfFits() bool {
	return !m.isCompact && m.width >= explorerHalfWidth
}

// explorerDrawn is the size the explorer is drawn at: half where asked
// and where half fits, else full.
func (m *UI) explorerDrawn() explorerSize {
	if m.explorer.want == explorerHalf && m.halfFits() {
		return explorerHalf
	}
	return explorerFull
}

// sizeExplorer asks for the explorer at size and lays the UI out for
// the size it is drawn at.
func (m *UI) sizeExplorer(size explorerSize) {
	m.explorer.want = size
	m.updateLayoutAndSize()
}

// explorerBeside reports whether the explorer is open beside the chat.
func (m *UI) explorerBeside() bool {
	return m.explorer.open && m.explorerDrawn() == explorerHalf
}

// frameEntries is how many frame entries the session has.
func (m *UI) frameEntries() int {
	roots, _ := m.sessionFrames.tree()
	n := 0
	eachFrame(roots, func(*frameNode) { n++ })
	return n
}

// openExplorer opens the explorer on the entry key, in the Tree tab, or
// following the agent in the tab last open for "", and gives it the keys.
// The entry of the frame the agent is in opens following, in the Tree
// tab.
func (m *UI) openExplorer(key string) {
	if key != "" {
		m.explorer.tab = explorerTree
		if _, stack := m.sessionFrames.tree(); len(stack) > 0 && stack[len(stack)-1].key == key {
			key = ""
		}
	}
	m.explorer.open = true
	m.explorer.searching = false
	m.explorer.following = key == ""
	m.explorer.selected = key
	m.explorer.seen = m.frameEntries()
	m.focusExplorer()
	m.updateLayoutAndSize()
}

// focusExplorer gives the explorer the keys, remembering who had them.
func (m *UI) focusExplorer() {
	if m.focus != uiFocusExplorer {
		m.explorer.back = m.focus
	}
	m.focus = uiFocusExplorer
	m.textarea.Blur()
	m.chat.Blur()
}

// closeExplorer closes the explorer, clears the marks it left in the
// chat, and gives the keys back.
func (m *UI) closeExplorer() tea.Cmd {
	m.chat.ShowFrame(nil)
	return m.hideExplorer()
}

// hideExplorer closes the explorer, leaving its marks in the chat, and
// gives the keys back.
func (m *UI) hideExplorer() tea.Cmd {
	m.explorer.open = false
	var cmd tea.Cmd
	if m.focus == uiFocusExplorer {
		if m.explorer.back == uiFocusMain {
			m.focus = uiFocusMain
			m.chat.Focus()
		} else {
			m.focus = uiFocusEditor
			cmd = m.textarea.Focus()
		}
	}
	m.updateLayoutAndSize()
	return cmd
}

// toggleExplorer is ctrl+x: it opens the explorer, gives it the keys back,
// or closes it once it has them. A session without frames has nothing to
// open.
func (m *UI) toggleExplorer() (tea.Cmd, bool) {
	switch {
	case !m.explorer.open:
		if m.frameEntries() == 0 {
			return nil, false
		}
		m.openExplorer("")
	case m.focus != uiFocusExplorer:
		m.focusExplorer()
	default:
		return m.closeExplorer(), true
	}
	return nil, true
}

// ---- Selection

// explorerRow is a line of the explorer's tree: a frame entry, or a slide
// under the frame it slid.
type explorerRow struct {
	node   *frameNode
	slide  *frameSlide
	parent string
	// lead is the tree's branches drawn before the entry.
	lead  string
	depth int
	// folded is whether the entry hides the frames entered from it.
	folded bool
	// A row that shows no entry and no slide shows either step, a step of
	// the goal at work not started, or result, what the finished task
	// above it ended with while none of its frames show.
	step, result string
}

// name is what the row says after its marker.
func (r explorerRow) name() string {
	switch {
	case r.slide != nil:
		return "slid · " + plural(r.slide.rows, "row")
	case r.result != "":
		return r.result
	case r.node == nil:
		return r.step
	}
	return shortName(r.node.id, r.parent)
}

// folds reports whether n's frames are hidden at depth: a finished
// top-level task folds by default, the rest are open, and the user flips
// either.
func (e explorer) folds(n *frameNode, depth int) bool {
	return (depth == 0 && !n.open()) != e.flipped[n.key]
}

// rows is the tree's lines, as folded. A slide stands under the last
// entry of the frame it slid pushed before it, among that entry's frames
// by time; at the top where there is none. steps, those of the goal at
// work not started, stand last under its entry.
func (e explorer) rows(roots []*frameNode, slides []frameSlide, steps []string) []explorerRow {
	if len(roots) == 0 {
		return nil
	}
	goal := slices.IndexFunc(roots, (*frameNode).open)
	owned := map[*frameNode][]*frameSlide{}
	var top []*frameSlide
	for i := range slides {
		s := &slides[i]
		var owner *frameNode
		eachFrame(roots, func(n *frameNode) {
			if n.id == s.frame && n.since <= s.at {
				owner = n
			}
		})
		if owner == nil {
			top = append(top, s)
		} else {
			owned[owner] = append(owned[owner], s)
		}
	}
	var rows []explorerRow
	var walk func(nodes []*frameNode, slides []*frameSlide, last []string, parent, cont string, depth int)
	walk = func(nodes []*frameNode, slides []*frameSlide, last []string, parent, cont string, depth int) {
		var items []explorerRow
		for _, n := range nodes {
			for len(slides) > 0 && slides[0].at < n.since {
				items, slides = append(items, explorerRow{slide: slides[0]}), slides[1:]
			}
			items = append(items, explorerRow{node: n})
		}
		for _, s := range slides {
			items = append(items, explorerRow{slide: s})
		}
		for _, s := range last {
			items = append(items, explorerRow{step: firstLine(s)})
		}
		for i, item := range items {
			lead, next := "", ""
			if depth > 0 {
				lead, next = cont+"├ ", cont+"│ "
				if i == len(items)-1 {
					lead, next = cont+"└ ", cont+"  "
				}
			}
			item.parent, item.lead, item.depth = parent, lead, depth
			n := item.node
			item.folded = n != nil && len(n.children) > 0 && e.folds(n, depth)
			rows = append(rows, item)
			if n != nil && depth == 0 && n.popped && n.result != "" && (item.folded || len(n.children) == 0) {
				rows = append(rows, explorerRow{parent: n.id, lead: next, depth: depth + 1, result: firstLine(n.result)})
			}
			if n != nil && !e.folds(n, depth) {
				var under []string
				if depth == 0 && goal >= 0 && n == roots[goal] {
					under = steps
				}
				walk(n.children, owned[n], under, n.id, next, depth+1)
			}
		}
	}
	walk(roots, top, nil, "", "", 0)
	return rows
}

// firstLine is s up to its first newline: a row draws one line.
func firstLine(s string) string {
	line, _, _ := strings.Cut(s, "\n")
	return line
}

// treeRows is the explorer's tree lines for the session's frames.
func (m *UI) treeRows() []explorerRow {
	roots, _ := m.sessionFrames.tree()
	return m.explorer.rows(roots, m.sessionFrames.slides(), m.stepsNotStarted())
}

// treeLayout lays the Tree tab out for rows, inner wide and height tall.
func (m *UI) treeLayout(rows []explorerRow, inner, height int) treeLayout {
	roots, _ := m.sessionFrames.tree()
	open := explorer{flipped: map[string]bool{}}
	for _, n := range roots {
		open.flipped[n.key] = !n.open()
	}
	return treeLayoutFor(rows, open.rows(roots, m.sessionFrames.slides(), m.stepsNotStarted()), inner, height)
}

// stepsNotStarted is the session's todos not started; none without a
// session.
func (m *UI) stepsNotStarted() []string {
	if m.session == nil {
		return nil
	}
	return notStarted(m.session.Todos)
}

// followed is the entry the agent is in: the top of the stack, or with
// none open, the last one entered; nil without frames.
func followed(roots, stack []*frameNode) *frameNode {
	if len(stack) > 0 {
		return stack[len(stack)-1]
	}
	var last *frameNode
	eachFrame(roots, func(n *frameNode) { last = n })
	return last
}

// holds reports whether the entry key is n or entered from it.
func holds(n *frameNode, key string) bool {
	return n.key == key || slices.ContainsFunc(n.children, func(c *frameNode) bool { return holds(c, key) })
}

// selectedFrame is the entry the explorer shows; nil without frames.
func (m *UI) selectedFrame() *frameNode {
	roots, stack := m.sessionFrames.tree()
	if !m.explorer.following {
		var found *frameNode
		eachFrame(roots, func(n *frameNode) {
			if n.key == m.explorer.selected {
				found = n
			}
		})
		if found != nil {
			return found
		}
	}
	return followed(roots, stack)
}

// selectedRow is where the selection is among rows, or its nearest shown
// frame when it is folded away.
func (m *UI) selectedRow(rows []explorerRow) int {
	sel := m.selectedFrame()
	if sel == nil {
		return 0
	}
	at := slices.IndexFunc(rows, func(r explorerRow) bool { return r.node != nil })
	for i, r := range rows {
		if r.node == nil {
			continue
		}
		if r.node.key == sel.key {
			return i
		}
		if holds(r.node, sel.key) {
			at = i
		}
	}
	return at
}

// pauseExplorer selects the entry key and stops following the agent.
func (m *UI) pauseExplorer(key string) {
	if m.explorer.following {
		m.explorer.seen = m.frameEntries()
	}
	m.explorer.following = false
	m.explorer.selected = key
}

// moveExplorer moves the selection by delta frames, over the slides.
func (m *UI) moveExplorer(delta int) {
	rows := m.treeRows()
	if len(rows) == 0 {
		return
	}
	i, step := m.selectedRow(rows), 1
	if delta < 0 {
		delta, step = -delta, -1
	}
	for range delta {
		j := i + step
		for j >= 0 && j < len(rows) && rows[j].node == nil {
			j += step
		}
		if j < 0 || j >= len(rows) {
			break
		}
		i = j
	}
	m.pauseExplorer(rows[i].node.key)
}

// foldExplorer folds or opens the selected entry's frames.
func (m *UI) foldExplorer(fold bool) {
	rows := m.treeRows()
	if len(rows) == 0 {
		return
	}
	r := rows[m.selectedRow(rows)]
	if len(r.node.children) == 0 || m.explorer.folds(r.node, r.depth) == fold {
		return
	}
	m.explorer.flip(r.node)
	m.pauseExplorer(r.node.key)
}

// flip folds n's frames if they show, else opens them.
func (e *explorer) flip(n *frameNode) {
	if e.flipped == nil {
		e.flipped = map[string]bool{}
	}
	e.flipped[n.key] = !e.flipped[n.key]
}

// ---- Search

// matches reports whether n's id or purpose holds the query q, lowered.
func matches(n *frameNode, q string) bool {
	return strings.Contains(strings.ToLower(n.id), q) || strings.Contains(strings.ToLower(n.purpose), q)
}

// searchKey reads a key into the search: text adds to the query,
// backspace takes from it, each selecting the first frame it matches;
// enter keeps the selection, esc puts back the one from before.
func (m *UI) searchKey(msg tea.KeyPressMsg) {
	e := &m.explorer
	switch {
	case key.Matches(msg, m.keyMap.Explorer.Close):
		e.searching = false
		e.restore(e.from)
		return
	case key.Matches(msg, m.keyMap.Explorer.Show):
		e.searching = false
		return
	case msg.Code == tea.KeyBackspace:
		r := []rune(e.query)
		e.query = string(r[:max(len(r)-1, 0)])
	case msg.Text != "":
		e.query += msg.Text
	default:
		return
	}
	m.findFrame()
}

// findFrame selects the first frame the query matches, in any case, and
// opens the frames above it; with no query or no match, the selection
// and the folds from before the search.
func (m *UI) findFrame() {
	e := &m.explorer
	e.restore(e.from)
	q := strings.ToLower(e.query)
	if q == "" {
		return
	}
	roots, _ := m.sessionFrames.tree()
	var find func(nodes []*frameNode, depth int) *frameNode
	find = func(nodes []*frameNode, depth int) *frameNode {
		for _, n := range nodes {
			if matches(n, q) {
				return n
			}
			if found := find(n.children, depth+1); found != nil {
				if e.folds(n, depth) {
					e.flip(n)
				}
				return found
			}
		}
		return nil
	}
	if found := find(roots, 0); found != nil {
		m.pauseExplorer(found.key)
	}
}

// showFrameInChat shows the selected frame's rows in the chat: the chat
// starts at its push and marks its rows and its pop with a gutter.
func (m *UI) showFrameInChat() tea.Cmd {
	n := m.selectedFrame()
	if n == nil {
		return nil
	}
	var ids []string
	for _, r := range m.sessionFrames.frameRows(n) {
		ids = append(ids, poppedItemIDs(r)...)
	}
	if n.popCall != "" {
		ids = append(ids, n.popCall)
		for _, r := range m.sessionFrames.rows {
			if slices.ContainsFunc(r.ToolCalls(), func(c message.ToolCall) bool { return c.ID == n.popCall }) {
				ids = append(ids, r.ID)
			}
		}
	}
	return m.showInChat(ids, n.pushCall)
}

// showInChat marks the chat items ids with a gutter and scrolls the chat
// to the item at. A full explorer makes room: it turns half where half
// fits, else it closes, leaving the marks and the keys to the chat.
func (m *UI) showInChat(ids []string, at string) tea.Cmd {
	m.chat.ShowFrame(ids)
	m.chat.ScrollToItem(at)
	switch {
	case !m.explorer.open, m.explorerDrawn() == explorerHalf:
		return nil
	case m.halfFits():
		m.sizeExplorer(explorerHalf)
		return nil
	}
	m.explorer.back = uiFocusMain
	return m.hideExplorer()
}

// ---- Input

// handleExplorerKey handles a key while the explorer has focus, and
// reports false for one it leaves to the global keys.
func (m *UI) handleExplorerKey(msg tea.KeyPressMsg) (tea.Cmd, bool) {
	k, e := m.keyMap.Explorer, &m.explorer
	if e.searching && !key.Matches(msg, m.keyMap.Chat.Frames) {
		m.searchKey(msg)
		return nil, true
	}
	tree := e.tab == explorerTree
	switch {
	case key.Matches(msg, k.Close), key.Matches(msg, m.keyMap.Chat.Frames):
		return m.closeExplorer(), true
	case tree && key.Matches(msg, k.Search):
		e.searching, e.query, e.from = true, "", e.selection()
	case key.Matches(msg, k.Show):
		return m.showOnTab(), true
	case key.Matches(msg, k.Full):
		m.sizeExplorer(explorerFull)
	case key.Matches(msg, k.Half):
		m.sizeExplorer(explorerHalf)
	case key.Matches(msg, k.NextTab):
		return m.setTab((e.tab + 1) % explorerTabCount), true
	case key.Matches(msg, k.PrevTab):
		return m.setTab((e.tab + explorerTabCount - 1) % explorerTabCount), true
	case key.Matches(msg, k.Tree):
		return m.setTab(explorerTree), true
	case key.Matches(msg, k.Pulse):
		return m.setTab(explorerPulse), true
	case key.Matches(msg, k.Context):
		return m.setTab(explorerContext), true
	case key.Matches(msg, k.Up):
		m.moveOnTab(-1)
	case key.Matches(msg, k.Down):
		m.moveOnTab(1)
	case tree && key.Matches(msg, k.Fold):
		m.foldExplorer(true)
	case tree && key.Matches(msg, k.Unfold):
		m.foldExplorer(false)
	case e.tab == explorerPulse && key.Matches(msg, k.Fold):
		m.changeLane(-1)
	case e.tab == explorerPulse && key.Matches(msg, k.Unfold):
		m.changeLane(1)
	case key.Matches(msg, k.Follow):
		e.following, e.selected = true, ""
	case key.Matches(msg, k.Focus):
		if m.explorerBeside() {
			m.focus = uiFocusEditor
			return m.textarea.Focus(), true
		}
	default:
		return nil, false
	}
	return nil, true
}

// moveOnTab moves the open tab's selection by delta: the frame on the
// Tree tab, the row on the Context tab.
func (m *UI) moveOnTab(delta int) {
	switch m.explorer.tab {
	case explorerTree:
		m.moveExplorer(delta)
	case explorerContext:
		m.moveContextRow(delta)
	case explorerPulse:
		m.movePulse(delta)
	}
}

// showOnTab shows the open tab's selection: in the chat from the Tree and
// Context tabs, and from the Pulse tab's what waits on the user, else in
// the Tree tab.
func (m *UI) showOnTab() tea.Cmd {
	switch m.explorer.tab {
	case explorerTree:
		return m.showFrameInChat()
	case explorerContext:
		return m.showRowInChat()
	case explorerPulse:
		return m.showOnPulse()
	}
	return nil
}

// setTab opens tab; the Pulse tab reads what mem says anew.
func (m *UI) setTab(tab explorerTab) tea.Cmd {
	m.explorer.tab = tab
	if tab == explorerPulse {
		return m.loadMemPulse()
	}
	return nil
}

// handleExplorerClick handles a click on the explorer: a header button, a
// tab's name, a frame of the tree, what waits on the user in Pulse. It
// gives the explorer the keys.
func (m *UI) handleExplorerClick(msg tea.MouseClickMsg) tea.Cmd {
	area := m.layout.explorer
	x, y := msg.X-area.Min.X, msg.Y-area.Min.Y
	m.focusExplorer()
	switch {
	case y == explorerHeaderRow:
		switch explorerButtonAt(area.Dx(), x) {
		case buttonHalf:
			m.sizeExplorer(explorerHalf)
		case buttonFull:
			m.sizeExplorer(explorerFull)
		case buttonClose:
			return m.closeExplorer()
		}
	case y == explorerTabsRow:
		if tab := explorerTabAt(x); tab >= 0 {
			return m.setTab(tab)
		}
	case m.explorer.tab == explorerTree:
		rows := m.treeRows()
		l := m.treeLayout(rows, explorerInner(area.Dx()), area.Dy())
		first := explorerBodyRow + l.head
		i := explorerTreeTop(m.selectedRow(rows), l.tree) + y - first
		if y >= first && y < first+l.tree && i < len(rows) && rows[i].node != nil {
			m.pauseExplorer(rows[i].node.key)
		}
	case m.explorer.tab == explorerPulse:
		if i := y - pulseWaitsRow; i >= 0 && i < len(m.waitsOnUser()) {
			m.explorer.onWaits, m.explorer.pulse = true, i
			return m.showOnPulse()
		}
	}
	return nil
}

// ---- Drawing

const (
	explorerHeaderRow = iota
	explorerTabsRow
	explorerRuleRow
	explorerBodyRow
)

// explorerPad is the room between the explorer's edges and its content.
const explorerPad = 1

// explorerInner is the width of an explorer's content.
func explorerInner(width int) int {
	return max(width-2*explorerPad, 1)
}

// explorerButton is a button at the end of the explorer's header.
type explorerButton int

const (
	buttonHalf explorerButton = iota
	buttonFull
	buttonClose
)

var explorerButtons = [...]string{buttonHalf: "◧ half", buttonFull: "■ full", buttonClose: "× close"}

// explorerButtonAt is the header button over column x of an explorer
// width wide, or -1. The buttons end the header, two columns apart.
func explorerButtonAt(width, x int) explorerButton {
	right := width - explorerPad
	for i := len(explorerButtons) - 1; i >= 0; i-- {
		left := right - lipgloss.Width(explorerButtons[i])
		if x >= left && x < right {
			return explorerButton(i)
		}
		right = left - 2
	}
	return -1
}

// explorerTabAt is the tab whose name is over column x, or -1. The names
// follow each other three columns apart.
func explorerTabAt(x int) explorerTab {
	left := explorerPad
	for i, name := range explorerTabNames {
		right := left + lipgloss.Width(name)
		if x >= left && x < right {
			return explorerTab(i)
		}
		left = right + 3
	}
	return -1
}

// frameDetailRows is the most lines frameDetail draws.
const frameDetailRows = 10

// explorerTreeHeights is how many rows the Tree tab gives its tree and its
// detail pane in an explorer height rows tall. A short one trims the
// detail; a very short one has none.
func explorerTreeHeights(height int) (tree, detail int) {
	body := height - explorerBodyRow
	switch {
	case body >= 24:
		detail = frameDetailRows
	case body >= 10:
		detail = 4
	}
	if detail == 0 {
		return max(body, 0), 0
	}
	return body - detail - 1, detail
}

// explorerTreeTop is the first tree row in view for the selection at row
// sel to show in tree rows.
func explorerTreeTop(sel, tree int) int {
	return max(sel-tree+1, 0)
}

// listView is the entries a list shows, and the rows that say how many
// it hides above and below them, "" for none.
type listView struct {
	first, shown int
	above, below string
}

// listWindow is the view of n entries, sel selected, in room rows, each
// entry per rows tall. Where the room holds one entry and no two counts,
// below says both.
func listWindow(n, sel, room, per int) listView {
	shown := max(room/per, 1)
	if n <= shown {
		return listView{shown: n}
	}
	first := 0
	for _, said := range []int{1, 2} {
		shown = max((room-said)/per, 1)
		first = min(max(sel-shown+1, 0), n-shown)
		if first == 0 || first+shown == n {
			break
		}
	}
	v := listView{first: first, shown: shown}
	more := n - first - shown
	switch {
	case first > 0 && more > 0 && shown*per+2 > room:
		v.below = fmt.Sprintf("… %d above · %d more", first, more)
	default:
		if first > 0 {
			v.above = fmt.Sprintf("… %d above", first)
		}
		if more > 0 {
			v.below = fmt.Sprintf("… %d more", more)
		}
	}
	return v
}

// drawExplorer draws the explorer over area, the selected frame's row
// lit.
func (m *UI) drawExplorer(scr uv.Screen, area uv.Rectangle) {
	view, lit := m.explorerView(area.Dx(), area.Dy())
	uv.NewStyledString(view).Draw(scr, area)
	if lit < 0 {
		return
	}
	bg := m.com.Styles.Explorer.Selected.GetBackground()
	for x := area.Min.X; x < area.Max.X; x++ {
		if c := scr.CellAt(x, area.Min.Y+lit); c != nil {
			cell := *c
			cell.Style.Bg = bg
			scr.SetCell(x, area.Min.Y+lit, &cell)
		}
	}
}

// explorerView renders the explorer width × height, and says which of its
// rows is the selected frame's, or -1.
func (m *UI) explorerView(width, height int) (string, int) {
	t := m.com.Styles
	inner := explorerInner(width)
	lines := []string{
		m.explorerHeader(inner),
		m.explorerTabs(),
		t.Files.TruncationHint.Render(strings.Repeat("─", inner)),
	}
	lit := -1
	switch m.explorer.tab {
	case explorerTree:
		var body []string
		body, lit = m.explorerTree(inner, height)
		lines = append(lines, body...)
	case explorerContext:
		lines = append(lines, m.contextTab(inner, height-explorerBodyRow)...)
	case explorerPulse:
		lines = append(lines, m.pulseTab(inner, height-explorerBodyRow)...)
	}
	if lit >= 0 {
		lit += explorerBodyRow
	}
	pad := strings.Repeat(" ", explorerPad)
	out := make([]string, max(height, 0))
	for i := range out {
		line := ""
		if i < len(lines) {
			line = ansi.Truncate(lines[i], inner, "…")
		}
		out[i] = pad + line + strings.Repeat(" ", max(width-explorerPad-lipgloss.Width(line), 0))
	}
	return strings.Join(out, "\n"), lit
}

// explorerHeader is the explorer's first line, inner wide: what the
// session's frames freed, whether it follows the agent, and the size and
// close buttons at its end.
func (m *UI) explorerHeader(inner int) string {
	t := m.com.Styles
	roots, _ := m.sessionFrames.tree()
	pops := 0
	eachFrame(roots, func(n *frameNode) {
		if n.popped {
			pops++
		}
	})
	slides := map[string]bool{}
	for _, r := range m.sessionFrames.cut {
		if _, slid := agent.SlidFrame(r.mark); slid {
			slides[r.mark] = true
		}
	}
	stats := []string{t.Files.EmptyMessage.Render(plural(pops, "pop")), t.Files.EmptyMessage.Render(plural(len(slides), "slide"))}
	if freed := freedLabel(t, m.sessionFrames); freed != "" {
		stats = append([]string{freed}, stats...)
	}
	// state says whether the explorer follows the agent; brief drops the
	// hint for a narrow header.
	state := t.Files.Additions.Render("● following")
	brief := state
	if !m.explorer.following {
		brief = t.LSP.WarningDiagnostic.Render("◌ paused")
		if n := m.frameEntries() - m.explorer.seen; n > 0 {
			brief += t.Files.EmptyMessage.Render(fmt.Sprintf(" · %d new below", n))
		}
		state = brief + t.Files.EmptyMessage.Render(" · G to follow")
	}
	drawn := map[explorerSize]explorerButton{explorerHalf: buttonHalf, explorerFull: buttonFull}[m.explorerDrawn()]
	var buttons []string
	for i, b := range explorerButtons {
		style := t.Files.EmptyMessage
		if explorerButton(i) == drawn {
			style = t.Files.ArtifactLabel.Underline(true)
		}
		buttons = append(buttons, style.Render(b))
	}
	right := strings.Join(buttons, "  ")

	title := ""
	if m.session != nil {
		title = t.Files.EmptyMessage.Render(fmt.Sprintf("session %q", m.session.Title))
	}
	sep := t.Files.TruncationHint.Render(" · ")
	head := lipgloss.NewStyle().Bold(true).Render("Frames")
	for _, left := range []string{
		head + "  " + title + "  " + strings.Join(stats, sep) + "  " + state,
		head + "  " + strings.Join(stats, sep) + "  " + state,
		head + "  " + strings.Join(stats, sep) + "  " + brief,
		head + "  " + brief,
		head,
	} {
		if gap := inner - lipgloss.Width(left) - lipgloss.Width(right); gap >= 2 {
			return left + strings.Repeat(" ", gap) + right
		}
	}
	// The buttons end the header, where a click looks for them.
	if cut := lipgloss.Width(right) - inner; cut > 0 {
		return ansi.TruncateLeft(right, cut+1, "…")
	}
	return strings.Repeat(" ", inner-lipgloss.Width(right)) + right
}

// plural writes n things: "1 pop", "3 pops".
func plural(n int, thing string) string {
	if n == 1 {
		return "1 " + thing
	}
	return strconv.Itoa(n) + " " + thing + "s"
}

// explorerTabs is the tabs' names, the open one underlined, then the
// search's query while / reads one.
func (m *UI) explorerTabs() string {
	t := m.com.Styles
	names := make([]string, len(explorerTabNames))
	for i, name := range explorerTabNames {
		style := t.Files.EmptyMessage
		if explorerTab(i) == m.explorer.tab {
			style = t.Files.ArtifactLabel.Underline(true)
		}
		names[i] = style.Render(name)
	}
	line := strings.Join(names, "   ")
	if !m.explorer.searching {
		return line
	}
	q := strings.ToLower(m.explorer.query)
	hint := "  enter keeps it · esc goes back"
	if q != "" {
		roots, _ := m.sessionFrames.tree()
		found := false
		eachFrame(roots, func(n *frameNode) { found = found || matches(n, q) })
		if !found {
			hint = "  no match · esc goes back"
		}
	}
	cursor := lipgloss.NewStyle().Foreground(t.Header.LogoGradToColor).Render("▏")
	return line + "      " + t.Files.ArtifactLabel.Render("/"+m.explorer.query) + cursor + t.Files.EmptyMessage.Render(hint)
}

// explorerTree is the Tree tab's lines, inner wide, for an explorer height
// rows tall: where there is room, the context chart and the time axis;
// the tree in view around the selection, each frame on the axis where
// there is room; then the selected frame's detail; and the row of the
// selection among the lines.
func (m *UI) explorerTree(inner, height int) ([]string, int) {
	t := m.com.Styles
	roots, _ := m.sessionFrames.tree()
	slides := m.sessionFrames.slides()
	rows := m.explorer.rows(roots, slides, m.stepsNotStarted())
	if len(rows) == 0 {
		return nil, -1
	}
	l := m.treeLayout(rows, inner, height)
	sel := m.selectedRow(rows)
	top := explorerTreeTop(sel, l.tree)
	r := m.explorerRenderer()
	r.width = inner - treeGutter
	axis := m.sessionFrames.treeAxis(roots, r.look, l.cols.bars)
	lines := m.treeHead(l, axis, roots, slides)
	for i := top; i < min(top+l.tree, len(rows)); i++ {
		lines = append(lines, gutterMark(t, i == sel)+" "+r.treeLine(rows[i], l.cols, axis, inner-treeGutter))
	}
	lit := l.head + sel - top
	if l.detail == 0 {
		return lines, lit
	}
	for len(lines) < l.head+l.tree {
		lines = append(lines, "")
	}
	lines = append(lines, t.Files.TruncationHint.Render(strings.Repeat("─", inner)))
	return append(lines, m.detailPane(rows[sel].node, r, inner, l.detail)...), lit
}

// listBesideWidth is the narrowest explorer content that lists what a
// frame held beside its detail.
const listBesideWidth = 120

// detailPane is the detail pane's lines for n, inner wide and at most
// height tall: its detail, and where there is room, beside it, what it
// held.
func (m *UI) detailPane(n *frameNode, r *stackRenderer, inner, height int) []string {
	rows := m.sessionFrames.frameRows(n)
	held := heldOf(n, rows)
	detail := m.frameDetail(n, r, rows, held)
	if inner < listBesideWidth {
		return detail[:min(height, len(detail))]
	}
	left := inner / 2
	list := m.heldList(n, held, inner-left-2, height)
	lines := make([]string, min(height, max(len(detail), len(list))))
	for i := range lines {
		d, h := "", ""
		if i < len(detail) {
			d = detail[i]
		}
		if i < len(list) {
			h = list[i]
		}
		lines[i] = fitLeft(d, left) + "  " + h
	}
	return lines
}

// heldList is, width wide and at most height lines, what the model no
// longer has from n once it is popped, or what it holds from it while it
// is open, held: a line per chat item showing those rows, the first the
// chat draws for it.
func (m *UI) heldList(n *frameNode, held []message.Message, width, height int) []string {
	t := m.com.Styles
	title := "what the model no longer has · "
	if !n.popped {
		title = "what the model holds · "
	}
	lines := []string{t.Files.TruncationHint.Render(title + plural(len(held), "row"))}
	var items []string
	for _, row := range held {
		if row.Role == message.Tool {
			continue
		}
		ids := []string{row.ID}
		for _, c := range row.ToolCalls() {
			ids = append(ids, c.ID)
		}
		for _, id := range ids {
			if line := m.chatLine(id); line != "" {
				items = append(items, line)
			}
		}
	}
	if room := max(height-2, 1); len(items) > room {
		items = append(items[:room-1], fmt.Sprintf("… %d more", len(items)-room+1))
	}
	for _, item := range items {
		lines = append(lines, "  "+t.Files.EmptyMessage.Render(ansi.Truncate(item, width-2, "…")))
	}
	if len(held) > 0 {
		lines = append(lines, t.Files.TruncationHint.Render("enter: show these rows in the chat"))
	}
	return lines
}

// chatLine is the first line the chat draws for its item id, without its
// colors; "" where the chat has no such item or it draws nothing.
func (m *UI) chatLine(id string) string {
	item := m.chat.MessageItem(id)
	if item == nil {
		return ""
	}
	for line := range strings.SplitSeq(item.Render(max(m.chat.list.Width(), 1)), "\n") {
		if s := strings.TrimSpace(ansi.Strip(line)); s != "" {
			return s
		}
	}
	return ""
}

// frameDetail is the detail pane's lines for n, of its rows and those it
// held, the most needed first: its name and id, purpose, result, what it
// freed or holds, when it ran, its work, its edits, its thoughts.
func (m *UI) frameDetail(n *frameNode, r *stackRenderer, rows, held []message.Message) []string {
	t := m.com.Styles
	label := func(s string) string { return t.Files.TruncationHint.Render(fmt.Sprintf("%-10s", s)) }
	value := t.Files.ArtifactLabel
	muted := t.Files.EmptyMessage
	tokens := m.sessionFrames.tokensOf(held)

	marker := t.Files.Additions.Render("✓")
	if n.open() {
		marker = t.LSP.WarningDiagnostic.Render("▸")
	}
	lines := []string{
		marker + " " + value.Bold(true).Render(shortName(n.id, parentID(n.id))) + "   " + muted.Render(n.id),
		label("purpose") + value.Render(n.purpose),
	}
	if n.popped {
		result := n.result
		if result == "" {
			result = "nothing said"
		}
		lines = append(lines, label("result")+value.Render(result))
		freed := muted.Render("nothing: its pop took nothing out of the model's context")
		if len(held) > 0 {
			freed = t.Files.Additions.Render("−"+shortTokens(tokens)+" tok") +
				muted.Render(fmt.Sprintf(" · %s out of the model's context", plural(len(held), "row")))
		}
		lines = append(lines, label("freed")+freed)
		lines = append(lines, label("ran")+value.Render(fmt.Sprintf("%s → %s · %s",
			time.Unix(n.since, 0).Format(time.TimeOnly), time.Unix(n.until, 0).Format(time.TimeOnly), runningFor(time.Duration(n.until-n.since)*time.Second))))
	} else {
		lines = append(lines, label("result")+muted.Render("not popped yet"))
		lines = append(lines, label("holds")+value.Render(fmt.Sprintf("≈%s tok", shortTokens(tokens)))+muted.Render(fmt.Sprintf(" in %s", plural(len(held), "row"))))
		ran := "since " + time.Unix(n.since, 0).Format(time.TimeOnly)
		if r.working() {
			ran += " · " + runningFor(r.look.now.Sub(time.Unix(n.since, 0)))
		}
		lines = append(lines, label("ran")+value.Render(ran))
	}
	uses, edits := m.frameWorkOf(n, rows)
	var work []string
	for _, u := range uses {
		work = append(work, fmt.Sprintf("%s %d", u.name, u.n))
	}
	if len(work) == 0 {
		lines = append(lines, label("work")+muted.Render("nothing yet"))
	} else {
		lines = append(lines, label("work")+value.Render(strings.Join(work, " · ")))
	}
	if len(edits) > 0 {
		lines = append(lines, label("edited")+value.Render(strings.Join(edits, ", ")))
	} else {
		lines = append(lines, label("edited")+muted.Render("nothing"))
	}
	if n.thoughts > 0 {
		lines = append(lines, label("thoughts")+value.Render(plural(n.thoughts, "thought")+" kept"))
	} else {
		lines = append(lines, label("thoughts")+muted.Render("none"))
	}
	if len(rows) > 0 && m.chat.framed[rows[0].ID] {
		lines = append(lines, "", muted.Render("the chat shows its rows, marked ▌"))
	}
	return lines
}

// parentID is the id of the frame id is under, "" for a top-level one.
func parentID(id string) string {
	if i := strings.LastIndex(id, "/"); i >= 0 {
		return id[:i]
	}
	return ""
}

// explorerAt reports whether the point is on the open explorer.
func (m *UI) explorerAt(x, y int) bool {
	return m.explorer.open && image.Pt(x, y).In(m.layout.explorer)
}

// lineSpan is n lines of a view from its line first.
type lineSpan struct{ first, n int }

// holds reports whether line is one of the span's.
func (s lineSpan) holds(line int) bool {
	return line >= s.first && line < s.first+s.n
}

// stackLines is where the Stack's lines start in a view, and the frame
// entry each line shows, "" for a line that shows none.
type stackLines struct {
	first int
	keys  []string
}

// at is the entry line shows, if it is one of the Stack's.
func (s stackLines) at(line int) (string, bool) {
	if i := line - s.first; i >= 0 && i < len(s.keys) {
		return s.keys[i], true
	}
	return "", false
}

// stackFrameAt is the frame entry the Stack shows at the screen point x,
// y, "" for the Stack but no entry, with false where no Stack is: the
// sidebar's section, compact mode's trail, or the ctrl+d column.
func (m *UI) stackFrameAt(x, y int) (string, bool) {
	if m.frameEntries() == 0 {
		return "", false
	}
	p := image.Pt(x, y)
	switch {
	case m.isCompact && m.detailsOpen:
		details := m.sessionDetailsIn(m.layout.sessionDetails)
		if !p.In(details.stack) {
			return "", false
		}
		return details.keys[y-details.stack.Min.Y], true
	case m.isCompact:
		return "", p.In(m.layout.stackTrail)
	}
	line, ok := m.sidebarLineAt(x, y)
	if !ok {
		return "", false
	}
	return m.sidebarStack.at(line)
}
