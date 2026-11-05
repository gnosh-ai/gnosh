package model

import (
	"cmp"
	"context"
	"fmt"
	"log/slog"
	"slices"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/crush/internal/gnovm"
	gnovmtools "github.com/charmbracelet/crush/internal/gnovm/tools"
	"github.com/charmbracelet/crush/internal/message"
)

// ---- Reading

// memPulseMsg carries what mem said goes on.
type memPulseMsg struct{ pulse gnovm.MemPulse }

// loadMemPulse reads what mem says goes on, off the update loop.
func (m *UI) loadMemPulse() tea.Cmd {
	return func() tea.Msg {
		pulse, err := m.com.Workspace.MemPulse(context.Background())
		if err != nil {
			slog.Debug("Could not read what mem says goes on", "error", err)
			return nil
		}
		return memPulseMsg{pulse}
	}
}

// callsMem reports whether row answers a gnovm_run call, after which mem
// may say something else.
func callsMem(row message.Message) bool {
	return row.Role == message.Tool && slices.ContainsFunc(row.ToolResults(), func(r message.ToolResult) bool {
		return r.Name == gnovmtools.RunToolName
	})
}

// waitsOnUser is what in mem waits on the user: its questions, and the
// steps that are the user's.
func (m *UI) waitsOnUser() []gnovm.MemPulseItem {
	waits := slices.Clone(m.memPulse.Asks)
	for _, g := range m.memPulse.Goals {
		for _, h := range g.Held {
			if h.Why == "user" && !slices.ContainsFunc(waits, func(w gnovm.MemPulseItem) bool { return w.Name == h.Name }) {
				waits = append(waits, gnovm.MemPulseItem{Name: h.Name, Title: h.Title})
			}
		}
	}
	return waits
}

// showAsk shows the first thing that waits on the user: the call of the
// session that asked it; with no such call, the Pulse tab, which lists it.
func (m *UI) showAsk() tea.Cmd {
	waits := m.waitsOnUser()
	if len(waits) == 0 {
		return nil
	}
	if call := m.askCall(waits[0].Name); call != "" {
		return m.showAskCall(call)
	}
	m.openExplorer("")
	return m.setTab(explorerPulse)
}

// answerWait shows the call of the session that asked name, if it did.
func (m *UI) answerWait(name string) tea.Cmd {
	if call := m.askCall(name); call != "" {
		return m.showAskCall(call)
	}
	return nil
}

// showAskCall marks the call that asked the user in the chat, and gives
// the editor the keys for the answer.
func (m *UI) showAskCall(call string) tea.Cmd {
	cmd := m.showInChat([]string{call}, call)
	m.chat.Blur()
	m.focus = uiFocusEditor
	return tea.Batch(cmd, m.textarea.Focus())
}

// askCall is the session's mem call that asked name, "" for none, looked
// for once while the session's mem calls stay as they are: the help asks
// at every draw.
// ceiling: finds mem's output line `asked <name>`; an ask kept in the
// run's metadata, as pushes and pops are, would not hang on its wording.
func (m *UI) askCall(name string) string {
	f := &m.sessionFrames
	if call, ok := f.asked[name]; ok {
		return call
	}
	if f.asked == nil {
		f.asked = map[string]string{}
	}
	said := "asked `" + name + "`"
	for _, r := range slices.Backward(f.rows) {
		for _, res := range r.ToolResults() {
			if res.Name == gnovmtools.RunToolName && strings.Contains(res.Content, said) {
				f.asked[name] = res.ToolCallID
				return res.ToolCallID
			}
		}
	}
	f.asked[name] = ""
	return ""
}

// ---- Lanes

// pulseLane is a lane of the Pulse tab.
type pulseLane int

const (
	laneNow pulseLane = iota
	laneDone
	laneNext
	laneCount
)

var laneNames = [laneCount]string{"NOW", "JUST FINISHED", "UP NEXT"}

// pulseItem is an entry of a lane: its line, with what ends it, and the
// line under it, "" for none; frame is the session's frame it is, or nil.
type pulseItem struct {
	head, right, sub string
	frame            *frameNode
}

// itemRows is how many rows each of items takes: two where any has a
// line under it, else one.
func itemRows(items []pulseItem) int {
	if slices.ContainsFunc(items, func(it pulseItem) bool { return it.sub != "" }) {
		return 2
	}
	return 1
}

// stackLook is what the frames are drawn for now, still where the TUI's
// reduce_motion option says so.
func (m *UI) stackLook() stackLook {
	opts := m.com.Config().Options
	still := opts != nil && opts.TUI != nil && opts.TUI.ReduceMotion
	return stackLook{now: time.Now(), busy: m.isAgentBusy(), still: still}
}

// explorerRenderer draws the session's frames for the explorer's tabs.
func (m *UI) explorerRenderer() *stackRenderer {
	r := &stackRenderer{t: m.com.Styles, look: m.stackLook(), frames: m.sessionFrames}
	if _, stack := m.sessionFrames.tree(); len(stack) > 0 {
		r.top = stack[len(stack)-1]
	}
	return r
}

// pulseLanes is what goes on now: the session's open frames, the frame at
// work first, and the frames other sessions work on; what the session
// finished, the last first; and what comes next: the session's todos
// not started, then each other goal's next step.
func (m *UI) pulseLanes(r *stackRenderer) [laneCount][]pulseItem {
	t := m.com.Styles
	muted, value := t.Files.EmptyMessage, t.Files.ArtifactLabel
	roots, stack := m.sessionFrames.tree()
	self := ""
	if m.session != nil {
		self = m.session.ID
	}
	var lanes [laneCount][]pulseItem

	for i := len(stack) - 1; i >= 0; i-- {
		n := stack[i]
		dot, clock := "◌", "paused · since "+time.Unix(n.since, 0).Format("15:04")
		if r.working() {
			dot, clock = "●", runningFor(r.look.now.Sub(time.Unix(n.since, 0)))
		}
		lanes[laneNow] = append(lanes[laneNow], pulseItem{
			head:  t.LSP.WarningDiagnostic.Render(dot) + " " + r.name(n, value, n.id),
			sub:   muted.Render("this session · " + clock),
			frame: n,
		})
	}
	peers := map[string]bool{}
	peer := func(name, title, doing string) {
		if doing == "" || doing == self || peers[name] {
			return
		}
		peers[name] = true
		lanes[laneNow] = append(lanes[laneNow], pulseItem{
			head: t.Files.Path.Render("●") + " " + value.Render(name),
			sub:  muted.Render("session " + gnovm.MemShortID(doing) + " · " + title),
		})
	}
	for _, g := range m.memPulse.Goals {
		peer(g.Name, g.Title, g.Doing)
		for _, h := range g.Held {
			if h.Why == "session" {
				peer(h.Name, h.Title, h.Doing)
			}
		}
	}

	var done []*frameNode
	eachFrame(roots, func(n *frameNode) {
		if n.popped {
			done = append(done, n)
		}
	})
	slices.SortFunc(done, func(a, b *frameNode) int { return cmp.Compare(b.popIndex, a.popIndex) })
	for _, n := range done {
		item := pulseItem{
			head:  t.Files.Additions.Render("✓") + " " + muted.Render(time.Unix(n.until, 0).Format("15:04")) + " " + r.name(n, t.Files.Path, n.id),
			right: r.entryRight(n),
			frame: n,
		}
		if n.result != "" {
			item.sub = muted.Render(firstLine(n.result))
		}
		lanes[laneDone] = append(lanes[laneDone], item)
	}

	// A session's todos are the steps of the goal it works on, so that
	// goal's next step is among them. The session's bottom frame is that
	// goal, or one of its steps under /goal, where mem pushes no goal.
	steps, bottom := m.stepsNotStarted(), ""
	if len(stack) > 0 && len(steps) > 0 {
		bottom = stack[0].id
	}
	next := func(name, note string) {
		lanes[laneNext] = append(lanes[laneNext], pulseItem{head: muted.Render("○") + " " + value.Render(name) + "  " + muted.Render(note)})
	}
	for _, s := range steps {
		next(s, "this session")
	}
	for _, g := range m.memPulse.Goals {
		if g.Next != "" && bottom != g.Name && !strings.HasPrefix(bottom, g.Name+"/") {
			next(g.Next, g.NextTitle+" · "+g.Title)
		}
	}
	return lanes
}

// ---- Drawing

// pulseTab is the Pulse tab's lines, inner wide and at most height tall:
// what waits on the user, where something does; the lanes, side by side
// where the explorer is wide, else one under the other; and a summary.
func (m *UI) pulseTab(inner, height int) []string {
	t := m.com.Styles
	muted := t.Files.EmptyMessage
	var lines []string
	waits := m.waitsOnUser()
	if len(waits) > 0 {
		lines = append(lines, t.LSP.WarningDiagnostic.Bold(true).Render("▲ WAITS ON YOU")+" "+muted.Render(fmt.Sprintf("(%d)", len(waits))))
		sel := m.selectedWait()
		for i, w := range waits {
			lines = append(lines, gutterMark(t, i == sel)+"  "+t.Files.ArtifactLabel.Render("`"+w.Name+"`")+"   "+muted.Render(w.Title))
		}
		lines = append(lines, "")
	}
	r := m.explorerRenderer()
	lanes := m.pulseLanes(r)
	room := max(height-len(lines)-2, laneMinRows)
	if inner >= listBesideWidth {
		lines = append(lines, m.lanesBeside(lanes, inner, room)...)
	} else {
		lines = append(lines, m.lanesUnder(lanes, inner, room)...)
	}

	// Working counts sessions: the peers, and this one while its agent is.
	working := 0
	if r.working() {
		working = 1
	}
	for _, it := range lanes[laneNow] {
		if it.frame == nil {
			working++
		}
	}
	summary := fmt.Sprintf("%d working · %d waits on you", working, len(waits))
	if at := m.memPulse.At; at > 0 {
		summary += " · updated " + updatedAgo(time.Since(time.Unix(at, 0)))
	}
	lines = lines[:min(len(lines), max(height-1, 0))]
	for len(lines) < height-1 {
		lines = append(lines, "")
	}
	return append(lines, muted.Render(summary))
}

// justNow is how long after a change updatedAgo says just now.
const justNow = 5 * time.Second

// updatedAgo says how long ago something changed: just now, 30s ago.
func updatedAgo(d time.Duration) string {
	if d < justNow {
		return "just now"
	}
	return runningFor(d) + " ago"
}

const (
	// laneGap is the room between lanes side by side.
	laneGap = 3
	// laneMinRows is the fewest rows a lane is given: its name, and an
	// entry with a line under it.
	laneMinRows = 3
)

// lanesBeside draws the lanes side by side, inner wide, at most rows
// tall.
func (m *UI) lanesBeside(lanes [laneCount][]pulseItem, inner, rows int) []string {
	width := (inner - laneGap*(int(laneCount)-1)) / int(laneCount)
	cols := make([][]string, laneCount)
	for i := range laneCount {
		cols[i] = m.laneLines(i, lanes[i], width, rows)
	}
	return beside(cols, width)
}

// beside sets cols side by side, laneGap apart, each but the last cut or
// padded to width.
func beside(cols [][]string, width int) []string {
	tallest := 0
	for _, col := range cols {
		tallest = max(tallest, len(col))
	}
	lines := make([]string, tallest)
	for y := range lines {
		var b strings.Builder
		for i, col := range cols {
			line := ""
			if y < len(col) {
				line = col[y]
			}
			if i < len(cols)-1 {
				line = fitLeft(line, width) + strings.Repeat(" ", laneGap)
			}
			b.WriteString(line)
		}
		lines[y] = b.String()
	}
	return lines
}

// lanesUnder draws the lanes one under the other, inner wide, sharing
// rows: each gets up to a third, then the rows the others leave go to
// those that need more, the first first.
func (m *UI) lanesUnder(lanes [laneCount][]pulseItem, inner, rows int) []string {
	var need, got [laneCount]int
	left := rows
	for i, items := range lanes {
		need[i] = 1 + max(itemRows(items)*len(items), 1)
		got[i] = min(need[i], max(rows/int(laneCount), laneMinRows))
		left -= got[i]
	}
	for i := range lanes {
		more := min(need[i]-got[i], max(left, 0))
		got[i] += more
		left -= more
	}
	var lines []string
	for i := range laneCount {
		lines = append(lines, m.laneLines(i, lanes[i], inner, got[i])...)
	}
	return lines
}

// laneLines draws lane i, width wide and at most rows tall: its name and
// how many entries it has, then the entries that fit around the one
// selected, it marked, and how many are hidden above and below them.
func (m *UI) laneLines(i pulseLane, items []pulseItem, width, rows int) []string {
	t := m.com.Styles
	muted := t.Files.EmptyMessage
	head := fmt.Sprintf("── %s (%d) ", laneNames[i], len(items))
	lines := []string{t.Files.TruncationHint.Render(head + strings.Repeat("─", max(width-lipgloss.Width(head), 0)))}
	if len(items) == 0 {
		return append(lines, "  "+muted.Render("nothing"))
	}
	sel := -1
	if m.explorer.lane == i && m.selectedWait() < 0 {
		sel = min(max(m.explorer.pulse, 0), len(items)-1)
	}
	per := itemRows(items)
	w := listWindow(len(items), sel, rows-1, per)
	if w.above != "" {
		lines = append(lines, "  "+muted.Render(w.above))
	}
	for j, item := range items[w.first : w.first+w.shown] {
		line := gutterMark(t, w.first+j == sel) + " " + item.head
		if item.right != "" {
			line = fitLeft(line, width-lipgloss.Width(item.right)-1) + " " + item.right
		}
		lines = append(lines, line)
		if per == 2 {
			lines = append(lines, "    "+item.sub)
		}
	}
	if w.below != "" {
		lines = append(lines, "  "+muted.Render(w.below))
	}
	return lines
}

// ---- Input

// pulseWaitsRow is the explorer row of the first thing that waits on the
// user, under the strip's heading.
const pulseWaitsRow = explorerBodyRow + 1

// movePulse moves the selection by delta in its lane, up from a lane's
// top into what waits on the user, and down from the last of those back
// into the lane.
func (m *UI) movePulse(delta int) {
	e := &m.explorer
	waits := len(m.waitsOnUser())
	if m.selectedWait() < 0 {
		// What it selected is answered: the lane's entry shows selected.
		e.onWaits = false
	}
	switch to := e.pulse + delta; {
	case e.onWaits && to >= waits:
		e.onWaits, e.pulse = false, 0
	case e.onWaits:
		e.pulse = max(to, 0)
	case to < 0 && waits > 0:
		e.onWaits, e.pulse = true, waits-1
	default:
		items := m.pulseLanes(m.explorerRenderer())[e.lane]
		e.pulse = min(max(to, 0), max(len(items)-1, 0))
	}
}

// changeLane moves the selection to the lane delta away.
func (m *UI) changeLane(delta int) {
	m.explorer.lane = min(max(m.explorer.lane+pulseLane(delta), 0), laneCount-1)
	m.explorer.pulse, m.explorer.onWaits = 0, false
}

// selectedWait is the entry of what waits on the user that the Pulse tab
// selects, -1 for none.
func (m *UI) selectedWait() int {
	if e := &m.explorer; e.onWaits && e.pulse < len(m.waitsOnUser()) {
		return e.pulse
	}
	return -1
}

// showOnPulse shows the Pulse tab's selection: the call that asked what
// waits on the user, or the frame in the Tree tab, following it where
// the agent is in it.
func (m *UI) showOnPulse() tea.Cmd {
	if i := m.selectedWait(); i >= 0 {
		return m.answerWait(m.waitsOnUser()[i].Name)
	}
	if n := m.pulseFrame(); n != nil {
		m.openExplorer(n.key)
	}
	return nil
}

// pulseFrame is the session's frame the selected entry is; nil for an
// entry that is none, or for what waits on the user.
func (m *UI) pulseFrame() *frameNode {
	if m.selectedWait() >= 0 {
		return nil
	}
	items := m.pulseLanes(m.explorerRenderer())[m.explorer.lane]
	if len(items) == 0 {
		return nil
	}
	return items[min(max(m.explorer.pulse, 0), len(items)-1)].frame
}
