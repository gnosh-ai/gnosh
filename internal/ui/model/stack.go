package model

import (
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/crush/internal/agent"
	"github.com/charmbracelet/crush/internal/gnovm"
	"github.com/charmbracelet/crush/internal/message"
	"github.com/charmbracelet/crush/internal/session"
	"github.com/charmbracelet/crush/internal/ui/common"
	"github.com/charmbracelet/crush/internal/ui/fade"
	"github.com/charmbracelet/crush/internal/ui/styles"
	"github.com/charmbracelet/x/ansi"
)

// sessionFrames is what the sidebar's Stack section knows of a session's
// frames in mem, built from the rows the UI is sent: each gnovm_run call
// that pushed or popped one of the session's frames, in the order its row
// came, and the rows a pop or a slide took out of the model's context.
type sessionFrames struct {
	calls []frameCall
	cut   map[string]cutRow
	// rows is the session's rows in the order the UI was sent them, and
	// at where each row id stands in it.
	rows []message.Message
	at   map[string]int
	// tokens is about how many tokens each row holds for the model, by row
	// id, as the agent counts what a pop takes out.
	tokens map[string]int64
	// asked is askCall's answers by question; nil once a mem call's row
	// comes or a row goes.
	asked map[string]string
	// worked is what draws work out from the rows, kept until a row comes
	// or goes; nil keeps nothing.
	worked *framesWorked
}

// framesWorked is what draws work out from a session's rows.
type framesWorked struct {
	// freed is freedBy's answers by entry key.
	freed map[string]int64
	// series is contextSeries' answer; nil until asked.
	series []contextPoint
}

// frameCall is the frames one gnovm_run call pushed and popped, in order.
type frameCall struct {
	rowID, callID string
	// at is when its row was made, in Unix seconds.
	at int64
	// seen is when the UI was first sent its row as it happened; zero for
	// a row loaded with the session.
	seen   time.Time
	frames []gnovm.MemFrame
}

// cutRow is a row out of the model's context: its mark (message.Popped),
// the id of the frame whose pop took it out or a slide's mark, and about
// how many tokens it held.
type cutRow struct {
	mark   string
	tokens int64
}

func deriveFrames(sessionID string, rows []message.Message) sessionFrames {
	var f sessionFrames
	for _, row := range rows {
		f.observe(sessionID, row, time.Time{})
	}
	return f
}

// observe takes in row, new or updated, sent at now as it happened (zero
// for a row loaded with the session), and reports whether that changed
// what the section shows.
func (f *sessionFrames) observe(sessionID string, row message.Message, now time.Time) bool {
	if i, ok := f.at[row.ID]; ok {
		f.rows[i] = row
	} else {
		if f.at == nil {
			f.at = map[string]int{}
		}
		f.at[row.ID] = len(f.rows)
		f.rows = append(f.rows, row)
	}
	if f.tokens == nil {
		f.tokens = map[string]int64{}
	}
	f.tokens[row.ID] = agent.RowTokens(row)
	f.worked = &framesWorked{freed: map[string]int64{}}
	if callsMem(row) {
		f.asked = nil
	}
	changed := false
	for _, call := range frameCallsOf(sessionID, row) {
		i := slices.IndexFunc(f.calls, func(c frameCall) bool { return c.callID == call.callID })
		switch {
		case i < 0:
			call.seen = now
			f.calls = append(f.calls, call)
			changed = true
		case !slices.Equal(f.calls[i].frames, call.frames):
			call.seen = f.calls[i].seen
			f.calls[i] = call
			changed = true
		}
	}
	was, wasCut := f.cut[row.ID]
	switch {
	case row.Popped != "":
		if now := (cutRow{mark: row.Popped, tokens: f.tokens[row.ID]}); !wasCut || was != now {
			if f.cut == nil {
				f.cut = map[string]cutRow{}
			}
			f.cut[row.ID] = now
			changed = true
		}
	case wasCut:
		delete(f.cut, row.ID)
		changed = true
	}
	return changed
}

// forget drops a deleted row, and reports whether the section showed
// anything of it.
func (f *sessionFrames) forget(rowID string) bool {
	if i, ok := f.at[rowID]; ok {
		f.rows = slices.Delete(f.rows, i, i+1)
		delete(f.at, rowID)
		delete(f.tokens, rowID)
		for j := i; j < len(f.rows); j++ {
			f.at[f.rows[j].ID] = j
		}
		f.asked = nil
		f.worked = &framesWorked{freed: map[string]int64{}}
	}
	n := len(f.calls)
	f.calls = slices.DeleteFunc(f.calls, func(c frameCall) bool { return c.rowID == rowID })
	_, wasCut := f.cut[rowID]
	delete(f.cut, rowID)
	return wasCut || len(f.calls) != n
}

// frameNode is one entry into a frame: its push, and its pop once the
// agent left it.
type frameNode struct {
	id, purpose string
	// key tells this entry from another of the same frame: its push's
	// call and place in it.
	key string
	// pushCall and popCall are the gnovm_run calls that pushed and popped
	// it; popCall is "" while it is on the stack. pushRow is the row of
	// pushCall's result.
	pushCall, popCall string
	pushRow           string
	// since and until are when it was pushed and popped, in Unix seconds.
	since, until int64
	popped       bool
	// popIndex is its pop's place among the session's pops, from 1; 0
	// while it is on the stack.
	popIndex int
	result   string
	// thoughts is how many thoughts it held when it was popped.
	thoughts int
	// poppedAt is when the UI saw its pop happen; zero for a pop loaded
	// with the session.
	poppedAt time.Time
	children []*frameNode
}

// open reports whether the frame, or one entered from it, is still on
// the stack.
func (n *frameNode) open() bool {
	return !n.popped || slices.ContainsFunc(n.children, (*frameNode).open)
}

// tree is the frames the session entered, each under the frame it was
// entered from, in order; and the stack, those not yet left, bottom
// first. A pop takes off the frame it names, wherever it stands, and a
// frame entered again is a new entry.
func (f sessionFrames) tree() (roots, stack []*frameNode) {
	pops := 0
	for _, call := range f.calls {
		for e, ev := range call.frames {
			switch ev.Kind {
			case "push":
				n := &frameNode{id: ev.Id, purpose: ev.Purpose, since: call.at, pushCall: call.callID, pushRow: call.rowID, key: call.callID + ":" + strconv.Itoa(e)}
				if len(stack) == 0 {
					roots = append(roots, n)
				} else {
					parent := stack[len(stack)-1]
					parent.children = append(parent.children, n)
				}
				stack = append(stack, n)
			case "pop":
				for i := len(stack) - 1; i >= 0; i-- {
					if stack[i].id == ev.Id {
						pops++
						stack[i].popped, stack[i].result, stack[i].poppedAt, stack[i].popIndex = true, ev.Result, call.seen, pops
						stack[i].popCall, stack[i].until, stack[i].thoughts = call.callID, call.at, ev.Thoughts
						stack = slices.Delete(stack, i, i+1)
						break
					}
				}
			}
		}
	}
	return roots, stack
}

// popFlash is how long a pop the UI saw happen stays lit, its freed
// tokens counting up.
const popFlash = 1500 * time.Millisecond

// flashed is how far through its flash, from 0 to 1, a pop seen at
// poppedAt is at now; 1 for a pop loaded with the session.
func flashed(poppedAt, now time.Time) float64 {
	if poppedAt.IsZero() || now.IsZero() {
		return 1
	}
	return min(max(float64(now.Sub(poppedAt))/float64(popFlash), 0), 1)
}

// animating reports whether the section moves at now: while the agent
// works (busy) in a frame, or while a pop it just saw is lit.
func (f sessionFrames) animating(now time.Time, busy bool) bool {
	roots, stack := f.tree()
	if busy && len(stack) > 0 {
		return true
	}
	var lit func([]*frameNode) bool
	lit = func(nodes []*frameNode) bool {
		for _, n := range nodes {
			if n.popped && flashed(n.poppedAt, now) < 1 || lit(n.children) {
				return true
			}
		}
		return false
	}
	return lit(roots)
}

// freed is about how many tokens the rows pops and slides took out of the
// model's context held.
func (f sessionFrames) freed() int64 {
	var tokens int64
	for _, r := range f.cut {
		tokens += r.tokens
	}
	return tokens
}

// slid is how many rows slides took out of the model's context, and about
// how many tokens they held.
func (f sessionFrames) slid() (rows int, tokens int64) {
	for _, r := range f.cut {
		if _, slid := agent.SlidFrame(r.mark); slid {
			rows++
			tokens += r.tokens
		}
	}
	return rows, tokens
}

// frameCallsOf is the gnovm_run calls in row that pushed or popped a frame
// of the session. A frame another session made, in a row copied by a
// branch, is not the session's.
func frameCallsOf(sessionID string, row message.Message) []frameCall {
	if row.Role != message.Tool {
		return nil
	}
	var calls []frameCall
	for _, result := range row.ToolResults() {
		own := slices.DeleteFunc(agent.FrameEvents(result), func(ev gnovm.MemFrame) bool { return ev.Session != sessionID })
		if len(own) > 0 {
			calls = append(calls, frameCall{rowID: row.ID, callID: result.ToolCallID, at: row.CreatedAt, frames: own})
		}
	}
	return calls
}

// rowTokens is about how many tokens row, one of the session's, holds for
// the model.
func (f sessionFrames) rowTokens(row message.Message) int64 {
	return f.tokens[row.ID]
}

// maxFinishedTasks is how many finished top-level frames the section
// lists; the older ones are counted on one line.
const maxFinishedTasks = 3

// stackLook is what the section is drawn for: the time, and whether the
// agent is working, which sets the open frames' clocks running and the
// one it is in shimmering; still stops the shimmer and a pop's flash and
// count-up.
type stackLook struct {
	now   time.Time
	busy  bool
	still bool
}

// resultLines is which finished frames the Stack shows with their result.
type resultLines int

const (
	// lastResult is the result of the frame popped last.
	lastResult resultLines = iota
	noResult
)

// stackFit is how much of the Stack a drawing of it shows: how many
// finished top-level frames it lists, whether the frame popped last keeps
// its result, and whether it says what slid out.
type stackFit struct {
	finished int
	results  resultLines
	slid     bool
}

// stackFits are the ways the Stack is drawn in a column of fixed height,
// fullest first. Each drops the least needed of what the one before it
// shows: earlier finished tasks, then what slid out, then the last
// result. The frames the agent is in stay.
func stackFits() []stackFit {
	var fits []stackFit
	for n := maxFinishedTasks; n >= 0; n-- {
		fits = append(fits, stackFit{finished: n, results: lastResult, slid: true})
	}
	return append(fits, stackFit{results: lastResult}, stackFit{results: noResult})
}

// freedLabel says about how many tokens pops and slides took out of the
// model's context; "" before any did.
func freedLabel(t *styles.Styles, f sessionFrames) string {
	if total := f.freed(); total > 0 {
		return t.Files.Additions.Render(shortTokens(total) + " tok freed")
	}
	return ""
}

// notStarted is what todos holds not started: the steps of the goal at
// work after the one in progress, since mem writes a session's todos.
func notStarted(todos []session.Todo) []string {
	var steps []string
	for _, todo := range todos {
		if todo.Status == session.TodoStatusPending {
			steps = append(steps, todo.Content)
		}
	}
	return steps
}

// stackSectionLines renders the sidebar's Stack section: the frames the
// session entered as a tree, a finished one with its result and about how
// many tokens its pop took out of the model's context, the frame the agent
// is in with its purpose; a finished top-level frame folds its children,
// and only the last few are listed. Then what slides took out, and the
// todos not started; the heading counts those done. With it, for each of
// its lines, the key of the frame entry it shows, "" for a line that
// shows none. "" for a session with neither frames nor slides.
func stackSectionLines(t *styles.Styles, f sessionFrames, todos []session.Todo, width int, look stackLook) (string, []string) {
	body, keys := stackBody(t, f, width, stackFits()[0], look)
	if body == nil {
		return "", nil
	}
	var info []string
	if freed := freedLabel(t, f); freed != "" {
		info = append(info, freed)
	}
	title := "Stack"
	if len(todos) > 0 {
		done := 0
		for _, todo := range todos {
			if todo.Status == session.TodoStatusCompleted {
				done++
			}
		}
		title = fmt.Sprintf("Stack · %d/%d", done, len(todos))
	}
	lines := append([]string{common.Section(t, title, width, info...), ""}, body...)
	keys = append([]string{"", ""}, keys...)
	var next []string
	for _, step := range notStarted(todos) {
		next = append(next, "○ "+step)
	}
	if len(next) > 0 {
		// The body ends on a line of no frame where it ends on what slid.
		if keys[len(keys)-1] == "" {
			lines, keys = append(lines, ""), append(keys, "")
		}
		lines = append(lines, ansi.Truncate("   "+t.Files.EmptyMessage.Render(strings.Join(next, " · ")), width, "…"))
		keys = append(keys, "")
	}
	return lipgloss.NewStyle().Width(width).Render(strings.Join(lines, "\n")), keys
}

// stackColumnLines renders the Stack in a column width wide and at most
// height rows tall, as compact mode's details show it: the fullest of
// stackFits that fits, else the leanest with its first lines cut, since
// the frames the agent is in come last. With it, for each of its lines,
// the key of the frame entry it shows, "" for a line that shows none. ""
// for a session with neither frames nor slides.
func stackColumnLines(t *styles.Styles, f sessionFrames, width, height int, look stackLook) (string, []string) {
	if height <= 0 {
		return "", nil
	}
	title := t.Files.SectionTitle.Render("Stack")
	if freed := freedLabel(t, f); freed != "" {
		title += "  " + freed
	}
	lines, keys := []string{ansi.Truncate(title, width, "…"), ""}, []string{"", ""}
	var body, bodyKeys []string
	for _, fit := range stackFits() {
		body, bodyKeys = stackBody(t, f, width, fit, look)
		if len(lines)+len(body) <= height {
			break
		}
	}
	if body == nil {
		return "", nil
	}
	switch room := height - len(lines); {
	case room <= 0:
		lines, keys = lines[:height], keys[:height]
	case len(body) > room:
		lines, keys = append(lines, body[len(body)-room:]...), append(keys, bodyKeys[len(body)-room:]...)
	default:
		lines, keys = append(lines, body...), append(keys, bodyKeys...)
	}
	return lipgloss.NewStyle().Width(width).Render(strings.Join(lines, "\n")), keys
}

// stackBody is the lines of the Stack under its heading, drawn to fit, and
// the key of the frame entry each shows; nil for a session with neither
// frames nor slides.
func stackBody(t *styles.Styles, f sessionFrames, width int, fit stackFit, look stackLook) (lines, keys []string) {
	roots, stack := f.tree()
	slidRows, slidTokens := f.slid()
	if len(roots) == 0 && slidRows == 0 {
		return nil, nil
	}
	r := stackRenderer{t: t, width: width - 1, look: look, results: fit.results, last: lastPopped(roots), frames: f}
	if len(stack) > 0 {
		r.top = stack[len(stack)-1]
	}
	finished := 0
	for _, n := range roots {
		if !n.open() {
			finished++
		}
	}
	skip := max(finished-fit.finished, 0)
	if skip > 0 {
		r.add(t.Files.TruncationHint.Render(fmt.Sprintf("…%d earlier", skip)))
	}
	for _, n := range roots {
		if !n.open() && skip > 0 {
			skip--
			continue
		}
		r.node(n, "", "", "")
	}
	r.key = ""
	if slidRows > 0 && fit.slid {
		r.add("")
		r.add(t.LSP.WarningDiagnostic.Render("⟲") + " " + t.Files.Path.Render("slid out of context"))
		r.add("  " + t.Files.EmptyMessage.Render(fmt.Sprintf("%s · ≈%s tokens", plural(slidRows, "msg"), shortTokens(slidTokens))))
	}
	r.markTop()
	return r.lines, r.keys
}

// markTop starts each line with a column for the gutter: › on the line
// of the frame the agent is in, a space on the others.
func (r *stackRenderer) markTop() {
	marked := false
	for i, line := range r.lines {
		top := r.top != nil && !marked && r.keys[i] == r.top.key
		marked = marked || top
		r.lines[i] = gutterMark(r.t, top) + line
	}
}

// gutterMark is the column a line of a list starts with: › on the line
// the list marks, a space on the others.
func gutterMark(t *styles.Styles, marked bool) string {
	if !marked {
		return " "
	}
	return lipgloss.NewStyle().Foreground(t.Header.LogoGradToColor).Render("›")
}

// stackColumnWidth is the widest the Stack's column gets in compact mode's
// details.
const stackColumnWidth = 30

// trailGap is the least room between the trail's frames and its total.
const trailGap = 2

// stackTrail renders the Stack on one row width wide, for compact mode,
// which has no sidebar: the frames the agent is in, root first, with the
// clock of the one it works in, or the last task once all are done; and at
// the row's end what pops and slides freed. A narrow row folds the middle
// frames to "…", then drops the total, then the clock. "" for a session
// without frames.
func stackTrail(t *styles.Styles, f sessionFrames, width int, look stackLook) string {
	roots, stack := f.tree()
	if len(roots) == 0 || width <= 0 {
		return ""
	}
	r := stackRenderer{t: t, width: width, look: look}
	total := freedLabel(t, f)
	type trailFit struct{ left, right string }
	var fits []trailFit
	if len(stack) == 0 {
		last := roots[len(roots)-1]
		left := t.Files.Additions.Render("✓") + " " + r.name(last, t.Files.Path, last.id)
		if last.result != "" {
			left += "  " + t.Files.EmptyMessage.Render(last.result)
		}
		fits = []trailFit{{left, total}, {left, ""}}
	} else {
		r.top = stack[len(stack)-1]
		path, folded := r.path(stack, false), r.path(stack, true)
		clock := "  " + r.clock(r.top)
		fits = []trailFit{{path + clock, total}, {folded + clock, total}, {folded + clock, ""}, {folded, ""}}
	}
	for _, fit := range fits {
		pad := width - lipgloss.Width(fit.left) - lipgloss.Width(fit.right)
		if fit.right == "" && pad >= 0 || pad >= trailGap {
			return fit.left + strings.Repeat(" ", pad) + fit.right
		}
	}
	cut := ansi.Truncate(fits[len(fits)-1].left, width, "…")
	return cut + strings.Repeat(" ", max(width-lipgloss.Width(cut), 0))
}

type stackRenderer struct {
	t     *styles.Styles
	width int
	look  stackLook
	// top is the frame the agent is in; nil with an empty stack.
	top *frameNode
	// results is which finished frames show their result; last is the
	// frame popped last.
	results resultLines
	last    *frameNode
	// frames is the session's frames, for what a finished frame's pop
	// freed.
	frames sessionFrames
	// lines is what was drawn, and keys the key of the frame entry each
	// line shows; key is the entry the lines added now show.
	lines, keys []string
	key         string
}

// lastPopped is the frame popped last among nodes and the frames entered
// from them; nil when none was.
func lastPopped(nodes []*frameNode) *frameNode {
	var last *frameNode
	for _, n := range nodes {
		for _, c := range []*frameNode{n, lastPopped(n.children)} {
			if c != nil && c.popIndex > 0 && (last == nil || c.popIndex > last.popIndex) {
				last = c
			}
		}
	}
	return last
}

// working reports whether the agent works at the look's time, which sets
// the clocks running and the frame it is in shimmering.
func (r *stackRenderer) working() bool {
	return r.look.busy && !r.look.now.IsZero()
}

// atWork reports whether n is the entry of the frame the agent is in,
// whichever tree() of the session n comes from.
func (r *stackRenderer) atWork(n *frameNode) bool {
	return r.top != nil && n.key == r.top.key
}

// clock is how long open frame n has run while the agent works, else when
// the agent entered it.
func (r *stackRenderer) clock(n *frameNode) string {
	if r.working() {
		return r.t.Files.EmptyMessage.Render(runningFor(r.look.now.Sub(time.Unix(n.since, 0))))
	}
	return r.t.Files.EmptyMessage.Render(time.Unix(n.since, 0).Format("15:04"))
}

// name renders name, n's, in style: shimmering while the agent works in
// n, in the gradient while n's pop is lit.
func (r *stackRenderer) name(n *frameNode, style lipgloss.Style, name string) string {
	switch {
	case r.look.still:
	case r.atWork(n) && r.working():
		return r.paint(style, name, int(r.look.now.UnixMilli()/shimmerStep.Milliseconds()))
	case flashed(n.poppedAt, r.look.now) < 1:
		return r.paint(style, name, 0)
	}
	return style.Render(name)
}

// shortName is id as it reads under parent's: its last part, or all of it
// when it is not under it.
func shortName(id, parent string) string {
	if rest, ok := strings.CutPrefix(id, parent+"/"); ok && parent != "" {
		return rest
	}
	return id
}

// path names the frames on stack, root first, each as it reads under the
// one before it; folded, the ones between the root and the top are one
// "…".
func (r *stackRenderer) path(stack []*frameNode, fold bool) string {
	t := r.t
	var parts []string
	for i, n := range stack {
		if fold && i > 0 && i < len(stack)-1 {
			if i == 1 {
				parts = append(parts, t.Files.TruncationHint.Render("…"))
			}
			continue
		}
		parent, style := "", t.Files.Path
		if i > 0 {
			parent = stack[i-1].id
		}
		if r.atWork(n) {
			style = t.Files.ArtifactLabel
		}
		parts = append(parts, r.name(n, style, shortName(n.id, parent)))
	}
	return t.LSP.WarningDiagnostic.Render("▸") + " " + strings.Join(parts, t.Files.TruncationHint.Render(" › "))
}

// showsResult reports whether finished frame n keeps its result line.
func (r *stackRenderer) showsResult(n *frameNode) bool {
	return r.results == lastResult && n == r.last
}

// shimmerStep is how long the shimmer's colors stay before moving on by
// one cell.
const shimmerStep = 80 * time.Millisecond

// minShimmerPeriod is the fewest cells the shimmer's fade spans, however
// short the name.
const minShimmerPeriod = 8

// paint renders s in the theme's fade from its gradient's start to its end
// and back, shifted by phase cells, so that drawn at successive phases the
// colors run along it. Without the theme's colors it is base's alone.
func (r *stackRenderer) paint(base lipgloss.Style, s string, phase int) string {
	from, to := r.t.Header.LogoGradFromColor, r.t.Header.LogoGradToColor
	runes := []rune(s)
	if from == nil || to == nil || len(runes) == 0 {
		return base.Render(s)
	}
	period := max(2*len(runes), minShimmerPeriod)
	ramp := lipgloss.Blend1D(period, fade.Stops(from, to, from)...)
	var b strings.Builder
	for i, c := range runes {
		b.WriteString(base.Foreground(ramp[(i+phase)%period]).Render(string(c)))
	}
	return b.String()
}

// runningFor writes d as a running clock: 45s, 1m05s, 1h02m.
func runningFor(d time.Duration) string {
	d = max(d, 0).Truncate(time.Second)
	switch {
	case d >= time.Hour:
		return fmt.Sprintf("%dh%02dm", int(d.Hours()), int(d.Minutes())%60)
	case d >= time.Minute:
		return fmt.Sprintf("%dm%02ds", int(d.Minutes()), int(d.Seconds())%60)
	}
	return fmt.Sprintf("%ds", int(d.Seconds()))
}

// add appends line, cut to the section's width.
func (r *stackRenderer) add(line string) {
	r.lines = append(r.lines, ansi.Truncate(line, r.width, "…"))
	r.keys = append(r.keys, r.key)
}

// node adds n's line under lead: its name, the last part of its id under
// its parent's, with when it was entered while it is open or what its
// pop freed once it is done; then, under cont, the purpose of the frame
// the agent is in or a finished frame's result, and, while n is open, the
// frames entered from it.
func (r *stackRenderer) node(n *frameNode, parent, lead, cont string) {
	t := r.t
	r.key = n.key
	open := n.open()
	tail := ""
	if !open {
		tail = r.stepDots(n)
	}
	r.add(r.entry(n, shortName(n.id, parent), tail, lead, r.width))

	detail := t.Files.TruncationHint.Render(cont + "  ")
	switch {
	case r.atWork(n):
		r.add(detail + t.Files.EmptyMessage.Render(strconv.Quote(n.purpose)))
	case !open && n.result != "" && r.showsResult(n):
		r.add(detail + t.Files.EmptyMessage.Render(n.result))
	}
	if !open {
		return
	}
	for i, c := range n.children {
		branch, next := "├ ", "│ "
		if i == len(n.children)-1 {
			branch, next = "└ ", "  "
		}
		r.node(c, n.id, cont+branch, cont+next)
	}
}

// entry renders n's line, width wide: lead, its marker and name and tail,
// then when it was entered while it is open, or what its pop freed once
// done.
func (r *stackRenderer) entry(n *frameNode, name, tail, lead string, width int) string {
	right := r.entryRight(n)
	room := width - lipgloss.Width(right)
	if right != "" {
		room--
	}
	head := r.entryHead(n, name, tail, lead, room)
	if right == "" {
		return head
	}
	return head + strings.Repeat(" ", max(width-lipgloss.Width(head)-lipgloss.Width(right), 1)) + right
}

// entryHead renders lead, then n's marker and name, then tail after a
// space, name cut to fit width.
func (r *stackRenderer) entryHead(n *frameNode, name, tail, lead string, width int) string {
	t := r.t
	marker, nameStyle := t.Files.Additions.Render("✓"), t.Files.Path
	if n.open() {
		marker, nameStyle = t.LSP.WarningDiagnostic.Render("▸"), t.Files.ArtifactLabel
	}
	head := t.Files.TruncationHint.Render(lead) + marker + " "
	if tail != "" {
		tail = " " + tail
	}
	name = ansi.Truncate(name, max(width-lipgloss.Width(head)-lipgloss.Width(tail), 1), "…")
	return head + r.name(n, nameStyle, name) + tail
}

// stepDots is a dot for each frame entered from n: filled once it is
// done, hollow while it is open.
func (r *stackRenderer) stepDots(n *frameNode) string {
	var b strings.Builder
	for _, c := range n.children {
		if c.open() {
			b.WriteString(r.t.Files.EmptyMessage.Render("○"))
		} else {
			b.WriteString(r.t.Files.Additions.Render("●"))
		}
	}
	return b.String()
}

// entryRight is when n was entered while it is open, or what its pop
// freed once done; "" for a pop that freed nothing.
func (r *stackRenderer) entryRight(n *frameNode) string {
	if n.open() {
		return r.clock(n)
	}
	// A pop just seen counts its freed tokens up, easing out.
	lit := flashed(n.poppedAt, r.look.now)
	eased := 1 - (1-lit)*(1-lit)*(1-lit)
	if r.look.still {
		eased = 1
	}
	if freed := int64(float64(r.frames.freedBy(n)) * eased); freed > 0 {
		return r.t.Files.Additions.Render("−" + shortTokens(freed) + " tok")
	}
	return ""
}
