package model

import (
	"cmp"
	"fmt"
	"image/color"
	"math"
	"slices"
	"strings"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/crush/internal/agent"
	"github.com/charmbracelet/crush/internal/message"
)

// ---- Figures

// contextShare is some of the rows the model is sent: how many, and about
// how many tokens they hold.
type contextShare struct {
	rows   int
	tokens int64
}

func (s *contextShare) add(tokens int64) {
	s.rows++
	s.tokens += tokens
}

// contextNow is what the model is sent now, in parts.
type contextNow struct {
	// total is what the next request sends: the estimates of the rows
	// still sent, plus always, what the provider's last count holds beyond
	// the rows (the system prompt, the tools, the files given), 0 with no
	// count.
	total, always int64
	// sent is the rows the model is still sent, and owner where the
	// deepest open frame holding a row stands in stack, by row id.
	sent  []message.Message
	owner map[string]int
	// outside is the rows in no open frame, and frames each open frame's
	// own rows, those of the frames opened from it apart.
	outside contextShare
	stack   []*frameNode
	frames  []contextShare
	// window is the model's context window and slideAt the total at which
	// the session slides, or compacts out of frames; 0 where unknown, or
	// for slideAt, off. holds is that total now (modelHolds).
	window, slideAt, holds int64
}

// sentRows is the rows the model is still sent: from the last summary on,
// those no pop or slide took out.
func (f sessionFrames) sentRows() []message.Message {
	start := 0
	for i, r := range f.rows {
		if r.IsSummaryMessage {
			start = i
		}
	}
	var sent []message.Message
	for _, r := range f.rows[start:] {
		if r.Popped == "" {
			sent = append(sent, r)
		}
	}
	return sent
}

// countedTokens is what the provider counted for the session's last
// request; 0 for none.
func (m *UI) countedTokens() int64 {
	if m.session == nil {
		return 0
	}
	return m.session.PromptTokens + m.session.CompletionTokens
}

// contextLimits is the model's context window and the running total at
// which the session slides or compacts: 0 where the window is unknown,
// and for the point, where auto-compact is off.
// ceiling: reads the compact variables of the UI's process, which a
// remote workspace's agent may not share.
func (m *UI) contextLimits() (window, slideAt int64) {
	model := m.selectedLargeModel()
	if model == nil || model.CatwalkCfg.ContextWindow <= 0 {
		return 0, 0
	}
	window = model.CatwalkCfg.ContextWindow
	opts := m.com.Workspace.Config().Options
	switch {
	case opts == nil:
		return window, agent.CompactAt(window, 0)
	case opts.DisableAutoSummarize:
		return window, 0
	}
	return window, agent.CompactAt(window, opts.AutoCompactWindow)
}

func (m *UI) contextNow() contextNow {
	_, stack := m.sessionFrames.tree()
	c := contextNow{stack: stack, frames: make([]contextShare, len(stack)), owner: map[string]int{}}
	c.window, c.slideAt = m.contextLimits()
	c.holds = m.modelHolds()
	for i, n := range stack {
		for _, r := range m.sessionFrames.heldRows(n) {
			c.owner[r.ID] = i
		}
	}
	c.sent = m.sessionFrames.sentRows()
	var estimated int64
	for _, r := range c.sent {
		tokens := m.sessionFrames.rowTokens(r)
		estimated += tokens
		if i, ok := c.owner[r.ID]; ok {
			c.frames[i].add(tokens)
		} else {
			c.outside.add(tokens)
		}
	}
	c.always = m.alwaysSent(m.sessionFrames.contextSeries())
	c.total = estimated + c.always
	return c
}

// held is what open frame i holds with the frames opened from it: what a
// pop of it now would take out.
func (c contextNow) held(i int) contextShare {
	var s contextShare
	for _, f := range c.frames[i:] {
		s.rows += f.rows
		s.tokens += f.tokens
	}
	return s
}

// frameName is open frame i's id as it reads under the frame below it.
func (c contextNow) frameName(i int) string {
	if i == 0 {
		return c.stack[0].id
	}
	return shortName(c.stack[i].id, c.stack[i-1].id)
}

// biggest is rows, the session's, the biggest first.
func (f sessionFrames) biggest(rows []message.Message) []message.Message {
	rows = slices.Clone(rows)
	slices.SortStableFunc(rows, func(a, b message.Message) int { return cmp.Compare(f.rowTokens(b), f.rowTokens(a)) })
	return rows
}

// ---- Drawing

const (
	// nowLabel is the width of the now bar's label.
	nowLabel = 22
	// minNowBar is the narrowest now bar drawn beside its label.
	minNowBar = 24
	// maxBiggest is how many of the biggest rows the tab lists at most.
	maxBiggest = 6
	// biggestAround is the rows the biggest rows' list takes beyond those
	// it lists: its heading, a line of what it hides, its hint, and the
	// blank under it.
	biggestAround = 4
	// chartLabel is the width of the chart's labels.
	chartLabel = 18
	// minContextChart is the fewest rows of the chart worth drawing.
	minContextChart = 4
	// listsBesideWidth is the narrowest tab that sets what a pop would
	// free and the biggest rows side by side.
	listsBesideWidth = 180
)

// contextTab is the Context tab's lines, inner wide and at most height
// tall: what the model is sent now, what a pop now would free, the
// biggest rows it still has, side by side where wide, and, where there
// is room, its context over the session.
func (m *UI) contextTab(inner, height int) []string {
	c := m.contextNow()
	lines := append(m.contextNowLines(c, inner), "")
	var foot []string
	if counted := m.countedTokens(); counted > 0 {
		foot = []string{m.com.Styles.Files.EmptyMessage.Render("≈ a row's tokens are estimated at 4 bytes a token; " + shortTokens(counted) + " is the provider's count.")}
	}
	rows := m.sessionFrames.biggest(c.sent)
	if inner >= listsBesideWidth {
		width := (inner - laneGap) / 2
		shown := min(len(rows), maxBiggest, max(height-len(lines)-len(foot)-biggestAround, 1))
		lines = append(lines, beside([][]string{m.contextPopLines(c, width), m.biggestLines(c, rows, shown, width)}, width)...)
	} else {
		lines = append(lines, m.contextPopLines(c, inner)...)
		lines = append(lines, "")
		shown := min(len(rows), maxBiggest, max(height-len(lines)-len(foot)-biggestAround, 1))
		lines = append(lines, m.biggestLines(c, rows, shown, inner)...)
	}
	lines = append(lines, "")
	if room := height - len(lines) - len(foot); room >= minContextChart+3 {
		lines = append(lines, m.contextChartLines(c, inner, room)...)
	}
	return append(lines[:min(len(lines), max(height-len(foot), 0))], foot...)
}

// segment is a part of the now bar.
type segment struct {
	name   string
	tokens int64
	color  color.Color
}

// contextNowLines is the now bar, what the model is sent against its
// window, its legend, and how far the next slide is.
func (m *UI) contextNowLines(c contextNow, inner int) []string {
	t := m.com.Styles
	muted := t.Files.EmptyMessage
	_, always := m.chartColors()
	segs := []segment{
		{"always sent", c.always, always.GetForeground()},
		{"outside every frame", c.outside.tokens, t.Files.TruncationHint.GetForeground()},
	}
	colors := m.frameColors(len(c.stack))
	for i := range c.stack {
		segs = append(segs, segment{c.frameName(i), c.frames[i].tokens, colors[i]})
	}
	title := t.Files.ArtifactLabel.Bold(true).Render("the model is sent now")
	right := shortTokens(c.total)
	if c.window > 0 {
		right += " of " + shortTokens(c.window)
	}
	right = t.Files.ArtifactLabel.Render(right)
	var lines []string
	if width := inner - nowLabel - 2 - lipgloss.Width(right); width >= minNowBar {
		lines = append(lines, fitLeft(title, nowLabel)+m.nowBar(c, segs, width)+"  "+right)
	} else {
		lines = append(lines, title+"  "+right, m.nowBar(c, segs, inner))
	}

	point := "slides"
	if len(c.stack) == 0 {
		point = "compacts"
	}
	var items []string
	for _, s := range segs {
		if s.name == "always sent" && s.tokens == 0 {
			continue
		}
		items = append(items, lipgloss.NewStyle().Foreground(s.color).Render("■")+" "+muted.Render(s.name+" "+shortTokens(s.tokens)))
	}
	if c.slideAt > 0 {
		items = append(items, t.LSP.WarningDiagnostic.Render("│")+" "+muted.Render(point+" at "+shortTokens(c.slideAt)))
	}
	lines = append(lines, wrapItems(items, "   ", inner)...)

	var said string
	switch {
	case c.window == 0:
		said = "The model's context window is not known."
	case c.slideAt == 0:
		said = "Auto-compact is off: nothing slides and nothing is compacted."
	case c.holds >= c.slideAt && len(c.stack) > 0:
		said = "Past the slide point: the next request takes the oldest part of the conversation out of the model's context."
	case c.holds >= c.slideAt:
		said = "Past the compaction point: the next request summarizes the conversation."
	case len(c.stack) > 0:
		said = shortTokens(c.slideAt-c.holds) + " before the next slide, which takes the oldest part of the conversation out of the model's context."
	default:
		said = shortTokens(c.slideAt-c.holds) + " before the next compaction, which summarizes the conversation."
	}
	return append(lines, wrapText(muted, said, inner)...)
}

// wrapText is text in style, wrapped to width.
func wrapText(style lipgloss.Style, text string, width int) []string {
	var lines []string
	for line := range strings.SplitSeq(lipgloss.NewStyle().Width(max(width, 1)).Render(text), "\n") {
		lines = append(lines, style.Render(strings.TrimRight(line, " ")))
	}
	return lines
}

// frameColors is a color per open frame, bottom first, along the theme's
// gradient: the frame at work in its end's.
func (m *UI) frameColors(n int) []color.Color {
	t := m.com.Styles
	from, to := t.Header.LogoGradFromColor, t.Header.LogoGradToColor
	if from == nil || to == nil {
		from, to = t.Files.ArtifactLabel.GetForeground(), t.Files.ArtifactLabel.GetForeground()
	}
	if n <= 1 {
		return []color.Color{to}
	}
	return lipgloss.Blend1D(n, from, to)
}

// nowBar is segs side by side, width wide for the window, or the total
// where the window is unknown; then the room left, ░, its part past the
// slide point in the warning's color, the point itself a │.
func (m *UI) nowBar(c contextNow, segs []segment, width int) string {
	t := m.com.Styles
	scale := c.window
	if scale <= 0 {
		scale = max(c.total, 1)
	}
	at := func(tokens int64) int {
		return min(int(math.Round(float64(tokens)*float64(width)/float64(scale))), width)
	}
	var b strings.Builder
	x, sum := 0, int64(0)
	for _, s := range segs {
		sum += s.tokens
		if end := at(sum); end > x {
			b.WriteString(lipgloss.NewStyle().Foreground(s.color).Render(strings.Repeat("█", end-x)))
			x = end
		}
	}
	slide := width
	if c.slideAt > 0 && c.window > 0 {
		slide = min(at(c.slideAt), width-1)
	}
	if slide > x {
		b.WriteString(t.Files.TruncationHint.Render(strings.Repeat("░", slide-x)))
		x = slide
	}
	if x == slide && x < width {
		b.WriteString(t.LSP.WarningDiagnostic.Render("│"))
		x++
	}
	if x < width {
		b.WriteString(t.LSP.WarningDiagnostic.Faint(true).Render(strings.Repeat("░", width-x)))
	}
	return b.String()
}

// wrapItems joins items with sep into lines of at most width.
func wrapItems(items []string, sep string, width int) []string {
	var lines []string
	line := ""
	for _, item := range items {
		switch {
		case line == "":
			line = item
		case lipgloss.Width(line)+len(sep)+lipgloss.Width(item) <= width:
			line += sep + item
		default:
			lines, line = append(lines, line), item
		}
	}
	if line != "" {
		lines = append(lines, line)
	}
	return lines
}

// Columns of the pop table; its names get what is left, up to
// maxPopName.
const (
	popRows    = 6
	popTokens  = 9
	popAfter   = 25
	maxPopName = 48
)

// sincePushGauge is the width of the gauge, under the pop table, of what
// the frame at work took in since its push.
const sincePushGauge = 20

// contextPopLines is what a pop of each open frame now would take out of
// the model's context and leave it, the frame at work first; the rows no
// pop takes; and how much the frame at work took in against the point at
// which its step is large.
func (m *UI) contextPopLines(c contextNow, inner int) []string {
	t := m.com.Styles
	muted, value := t.Files.EmptyMessage, t.Files.ArtifactLabel
	name := min(inner-treeGutter-popRows-popTokens-popAfter, maxPopName)
	lines := []string{strings.Repeat(" ", treeGutter) + fitLeft(value.Bold(true).Render("a pop now would free"), name) +
		fitRight(muted.Render("rows"), popRows) + fitRight(muted.Render("tokens"), popTokens) + "   " + muted.Render("then the model is sent")}
	for i := len(c.stack) - 1; i >= 0; i-- {
		mark := gutterMark(t, i == len(c.stack)-1) + " "
		label := t.LSP.WarningDiagnostic.Render("▸") + " " + value.Render(c.frameName(i))
		if with := withFrames(c, i); with != "" {
			label += "  " + muted.Render(with)
		}
		held := c.held(i)
		freed := muted.Render("0")
		if held.tokens > 0 {
			freed = t.Files.Additions.Render("−" + shortTokens(held.tokens))
		}
		lines = append(lines, mark+fitLeft(label, name)+fitRight(muted.Render(fmt.Sprint(held.rows)), popRows)+
			fitRight(freed, popTokens)+"   "+value.Render(shortTokens(max(c.total-held.tokens, 0))))
	}
	lines = append(lines, "    "+fitLeft(muted.Render("outside every frame"), name-2)+fitRight(muted.Render(fmt.Sprint(c.outside.rows)), popRows)+
		fitRight(muted.Render(shortTokens(c.outside.tokens)), popTokens)+"   "+muted.Render("no pop takes these"))

	if c.slideAt <= 0 || len(c.stack) == 0 {
		return lines
	}
	top, large := len(c.stack)-1, agent.LargeStepAt(c.slideAt)
	took := c.frames[top].tokens
	lines = append(lines, "  "+muted.Render("since its push")+"  "+gauge(lipgloss.NewStyle().Foreground(m.frameColors(len(c.stack))[top]), t.Files.TruncationHint, took, large, sincePushGauge)+" "+
		value.Render(shortTokens(took))+muted.Render(" of "+shortTokens(large)))
	said := fmt.Sprintf("%s has taken in %s since its push. At %s, half the slide point, gnosh tells the model the step is large.",
		c.frameName(top), shortTokens(took), shortTokens(large))
	for _, line := range wrapText(muted, said, inner-2) {
		lines = append(lines, "  "+line)
	}
	return lines
}

// withFrames names the open frames above open frame i, which its pop
// takes with it.
func withFrames(c contextNow, i int) string {
	switch above := len(c.stack) - 1 - i; above {
	case 0:
		return ""
	case 1:
		return "with " + shortName(c.stack[i+1].id, c.stack[i].id)
	case 2:
		return "with both"
	default:
		return fmt.Sprintf("with the %d above", above)
	}
}

// gauge is n of limit as a bar width wide between ▕ and ▏, filled in
// fill's color and the rest in rest's.
func gauge(fill, rest lipgloss.Style, n, limit int64, width int) string {
	full := 0
	if limit > 0 {
		full = min(int(math.Round(float64(n)*float64(width)/float64(limit))), width)
	}
	return rest.Render("▕") + fill.Render(strings.Repeat("█", full)) + rest.Render(strings.Repeat("░", width-full)+"▏")
}

// Columns of the biggest rows; their text gets what is left, up to
// maxBigText.
const (
	bigTokens  = 7
	bigIn      = 22
	maxBigText = 72
)

// biggestLines lists shown of rows, the biggest the model still has,
// around the one selected: about how many tokens each holds, the first
// line the chat draws for it, and the open frame it is in.
func (m *UI) biggestLines(c contextNow, rows []message.Message, shown, inner int) []string {
	t := m.com.Styles
	muted, value := t.Files.EmptyMessage, t.Files.ArtifactLabel
	text := min(inner-treeGutter-bigTokens-2-2-bigIn, maxBigText)
	lines := []string{strings.Repeat(" ", treeGutter) + fitLeft(value.Bold(true).Render("the biggest rows the model still has"), bigTokens+2+text+2) + muted.Render("in")}
	if len(rows) == 0 {
		return append(lines, "  "+muted.Render("none"))
	}
	sel := min(max(m.explorer.row, 0), len(rows)-1)
	w := listView{shown: len(rows)}
	if len(rows) > shown {
		w = listWindow(len(rows), sel, shown+1, 1)
	}
	if w.above != "" {
		lines = append(lines, "  "+muted.Render(w.above))
	}
	colors := m.frameColors(len(c.stack))
	for i, row := range rows[w.first : w.first+w.shown] {
		mark := gutterMark(t, w.first+i == sel) + " "
		in := t.Files.TruncationHint.Render("outside every frame")
		if f, ok := c.owner[row.ID]; ok {
			in = lipgloss.NewStyle().Foreground(colors[f]).Render(c.frameName(f))
		}
		lines = append(lines, mark+fitRight(value.Render("≈"+shortTokens(m.sessionFrames.rowTokens(row))), bigTokens)+"  "+
			fitLeft(m.bigRowLine(row), text)+"  "+fitLeft(in, bigIn))
	}
	if w.below != "" {
		lines = append(lines, "  "+muted.Render(w.below))
	}
	return append(lines, t.Files.TruncationHint.Render("enter: show the row in the chat"))
}

// bigRowLine is how the biggest rows name row: a prompt or a reply by
// its first line, any other by the first line the chat draws for it.
func (m *UI) bigRowLine(row message.Message) string {
	muted, value := m.com.Styles.Files.EmptyMessage, m.com.Styles.Files.ArtifactLabel
	text := firstLine(strings.TrimSpace(row.Content().Text))
	switch {
	case text != "" && row.Role == message.User:
		return muted.Render("prompt  ") + value.Render(text)
	case text != "" && row.Role == message.Assistant:
		return muted.Render("reply   ") + value.Render(`"`+text+`"`)
	}
	line, _ := m.rowLine(row)
	if line == "" {
		line = string(row.Role)
	}
	return muted.Render(line)
}

// rowLine is the first line the chat draws for row, and the id of the
// item that draws it: the row's own, else that of a call it made or
// answered; "" for both where the chat draws none.
func (m *UI) rowLine(row message.Message) (line, id string) {
	ids := []string{row.ID}
	for _, c := range row.ToolCalls() {
		ids = append(ids, c.ID)
	}
	for _, r := range row.ToolResults() {
		ids = append(ids, r.ToolCallID)
	}
	for _, id := range ids {
		if line := m.chatLine(id); line != "" {
			return line, id
		}
	}
	return "", ""
}

// contextChartLines is the model's context over the session, height rows
// with its title and its axis, its top the window where it is known, the
// slide point a dashed line.
func (m *UI) contextChartLines(c contextNow, inner, height int) []string {
	t := m.com.Styles
	muted := t.Files.EmptyMessage
	roots, _ := m.sessionFrames.tree()
	width := max(inner-chartLabel, 1)
	axis := m.sessionFrames.treeAxis(roots, m.stackLook(), width)
	series, always := m.contextOverTime()
	top := c.window
	for _, p := range series {
		top = max(top, p.tokens)
	}
	// What is always sent, near constant, takes one row at the foot; the
	// rows above run from it to the top.
	rows := height - 3
	if always > 0 {
		rows--
	}
	slide := -1
	if c.slideAt > always && top > always {
		slide = min(max(rows-1-int((c.slideAt-always)*int64(rows)/(top-always)), 0), rows-1)
	}
	rowsStyle, alwaysStyle := m.chartColors()
	lines := []string{t.Files.ArtifactLabel.Bold(true).Render("the model's context over the session")}
	for i, row := range contextChart(series, axis, rows, always, max(top, 1)) {
		label, labelStyle := "", muted
		switch {
		case i == slide:
			label, labelStyle = "slides at "+shortTokens(c.slideAt), t.LSP.WarningDiagnostic
		case i == 0 && c.window > 0 && top == c.window:
			label = shortTokens(c.window) + " window"
		case i == 0:
			label = shortTokens(top)
		case i == rows-1 && always == 0:
			label = "0"
		}
		drawn := rowsStyle.Render(row)
		if i == slide {
			drawn = dashBlanks(row, rowsStyle, t.LSP.WarningDiagnostic)
		}
		lines = append(lines, fitLeft(labelStyle.Render(label), chartLabel)+drawn)
	}
	if always > 0 {
		band := contextChart(series, axis, 1, 0, always)[0]
		lines = append(lines, fitLeft(muted.Render("always sent "+shortTokens(always)), chartLabel)+alwaysStyle.Render(band))
	}
	marks, labels := m.axisRows(axis, roots, m.sessionFrames.slides())
	return append(lines, fitLeft(muted.Render("▴ pop  ⟲ slide"), chartLabel)+marks, fitLeft(muted.Render("time"), chartLabel)+labels)
}

// dashBlanks renders row in style, its blank cells as ┄ in dash.
func dashBlanks(row string, style, dash lipgloss.Style) string {
	var b strings.Builder
	runes := []rune(row)
	for start := 0; start < len(runes); {
		end, blank := start, runes[start] == ' '
		for end < len(runes) && (runes[end] == ' ') == blank {
			end++
		}
		if blank {
			b.WriteString(dash.Render(strings.Repeat("┄", end-start)))
		} else {
			b.WriteString(style.Render(string(runes[start:end])))
		}
		start = end
	}
	return b.String()
}

// ---- Input

// moveContextRow moves the selection among the biggest rows by delta.
func (m *UI) moveContextRow(delta int) {
	n := len(m.sessionFrames.sentRows())
	m.explorer.row = min(max(m.explorer.row+delta, 0), max(n-1, 0))
}

// showRowInChat shows the selected one of the biggest rows in the chat.
func (m *UI) showRowInChat() tea.Cmd {
	rows := m.sessionFrames.biggest(m.sessionFrames.sentRows())
	if len(rows) == 0 {
		return nil
	}
	row := rows[min(max(m.explorer.row, 0), len(rows)-1)]
	_, at := m.rowLine(row)
	return m.showInChat(poppedItemIDs(row), at)
}
