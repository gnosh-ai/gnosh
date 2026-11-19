package ui

import (
	"fmt"
	"strings"
	"time"

	"github.com/gnosh-ai/gnosh/internal/app"
	"github.com/gnosh-ai/gnosh/internal/text"
	"github.com/gnosh-ai/gnosh/internal/ui/editor"
)

// The live region, from its first row to its last:
//
//	(a blank row)                      between the scrollback and what follows
//	the part of the answer that may still change, or the end of the
//	  model's reasoning while that is folded
//	each call being written or run: its heading, and its last output
//	(a blank row)
//	what gnosh is doing or waiting for   one row, only while it is busy
//	the prompts that wait for their turn
//	the question, when one is put; otherwise the prompt box
//	the session's task list, unless Ctrl+T has put it out of sight
//	the status line                    one row
//	a short notice a key called for    for a few seconds; wrapped, three rows at most
//
// It is kept small: everything above the row that says what gnosh is
// doing is cut from its top when the region would grow past its bound,
// because those rows are the ones that reach the scrollback anyway once
// they are final. The rows a person types into are never cut.

// The most rows the parts of the live region take.
const (
	maxQueueRows = 4  // prompts that wait, with the row that names the key
	maxTaskRows  = 8  // the task list
	maxHintRows  = 3  // a short notice a key called for, wrapped
	maxBoxRows   = 10 // the prompt box's text
	lockedRows   = 2  // the prompt box's text while a question has the keys
	liveCap      = 60 // the whole region, however tall the terminal
)

// liveBound is the most rows the live region takes in a terminal of rows
// rows: one row less than the terminal has, so that the last line of the
// scrollback stays on screen, and never more than liveCap.
func liveBound(rows int) int {
	return max(min(rows-1, liveCap), 1)
}

// compose lays the live region out for the terminal's width, and says
// where the cursor is: on row row of lines at cell col, or row -1 when it
// is hidden.
func (s *session) compose() (lines []string, row, col int) {
	w, bound := s.cols, liveBound(s.rows)
	now := s.now()

	// The rows are shared out in the order they matter when there are
	// few: the rows the keys go to, the status line, the line that says
	// what gnosh is doing, the short notice; then what waits, and last
	// what is still being written.
	need := 1
	if s.q != nil {
		need = min(3, bound)
	}
	left := bound - need
	take := func(want bool) bool {
		if want && left > 0 {
			left--
			return true
		}
		return false
	}
	act := ""
	var status, hint []string
	if len(s.custom) == 0 {
		if take(true) {
			status = []string{s.statusLine(w)}
		}
	} else {
		for _, l := range s.customLines(w) {
			if !take(true) {
				break
			}
			status = append(status, l)
		}
	}
	if a := s.activityLine(w, now); take(a != "") {
		act = a
	}
	for _, h := range s.hintLines(w, now) {
		if !take(true) {
			break
		}
		hint = append(hint, h)
	}
	room := need + left

	// The question or the prompt box: what the keys go to.
	var mid []string
	midRow, midCol := -1, 0
	s.lock()
	if q := s.q; q != nil {
		// A draft stays in sight under the question: whole while keys
		// still go to it, and as a reminder afterwards, where there are
		// rows for it. An empty box is not drawn at all, so that the
		// question does not jump when it takes the keys.
		box := editor.View{CursorRow: -1}
		switch {
		case s.ed.IsEmpty():
		case q.guarded:
			s.ed.SetMaxHeight(max(min(maxBoxRows, (room-3)/3), 1))
			box = s.ed.Render(w)
		case room >= 3+lockedRows+2:
			s.ed.SetMaxHeight(lockedRows)
			box = s.ed.Render(w)
		}
		q.ui.SetMaxHeight(max(room-len(box.Lines), 3))
		v := q.ui.Render(w)
		mid = append(mid, v.Lines...)
		switch {
		case q.guarded && box.CursorRow >= 0:
			midRow, midCol = len(mid)+box.CursorRow, box.CursorCol
		case !q.guarded && v.CursorRow >= 0:
			midRow, midCol = v.CursorRow, v.CursorCol
		}
		mid = append(mid, box.Lines...)
	} else {
		s.ed.SetMaxHeight(max(min(maxBoxRows, room/2), 1))
		s.ed.SetPlaceholder(s.placeholder())
		v := s.ed.Render(w)
		mid, midRow, midCol = v.Lines, v.CursorRow, v.CursorCol
	}
	room -= len(mid)

	// The task list and the queue, as far as there is room.
	var tasks, notes []string
	if room > 0 {
		tasks = s.taskLines(w, min(room, maxTaskRows))
		room -= len(tasks)
	}
	if room > 0 {
		notes = s.queueLines(w, min(room, maxQueueRows))
		room -= len(notes)
	}

	// What is still being written, in what room is left: its last rows
	// when it has more. No blank row stands above it or below it.
	var stream []string
	if room > 0 {
		stream = s.streamLines(w)
		if len(stream) > room {
			stream = stream[len(stream)-room:]
		}
	}

	lines = make([]string, 0, bound)
	lines = append(lines, stream...)
	if act != "" {
		lines = append(lines, act)
	}
	lines = append(lines, notes...)
	if midRow >= 0 {
		row, col = len(lines)+midRow, midCol
	} else {
		row = -1
	}
	lines = append(lines, mid...)
	lines = append(lines, tasks...)
	lines = append(lines, status...)
	lines = append(lines, hint...)

	// The prompt box may have opened a list under itself that the
	// sharing out did not count on. The top goes then, as far as the row
	// with the cursor, and after that the bottom.
	if over := len(lines) - bound; over > 0 {
		if row >= 0 {
			over = min(over, row)
			row -= over
		}
		lines = lines[over:]
		if len(lines) > bound {
			lines = lines[:bound]
			if row >= bound {
				row = bound - 1
			}
		}
	}
	for i, l := range lines {
		lines[i] = fit(s.m, l, w)
	}
	return lines, row, col
}

// lock gives the keys to whoever has them: the prompt box is locked while
// a question takes them, and at no other time.
func (s *session) lock() {
	s.ed.SetLocked(s.q != nil && !s.q.guarded)
}

// placeholder is what an empty prompt box says.
func (s *session) placeholder() string {
	switch {
	case s.suggestion != "" && !s.busy:
		return s.suggestion + "  (Tab takes it)"
	case s.busy && len(s.personQueue()) > 0:
		return "Type to queue another prompt; Up takes the queue back"
	case s.busy:
		return "Type to queue a prompt; Esc stops the turn"
	case s.failHint != "":
		return s.failHint
	}
	return "Ask for something, or ? for the keys"
}

// failHintFor is what the empty prompt box says after a turn failed in the
// way kind names (agent.TurnResult.ErrorKind): what to try next, as
// Claude Code 2.1.326 says it. The conversation keeps the prompt, so
// "go on" is all a second try needs.
func failHintFor(kind string) string {
	switch kind {
	case "rate_limit", "overloaded":
		return `The model's server is busy: wait a little, then send "go on" to try again`
	case "context_too_long":
		return `Too long for the model: /compact makes room, then send "go on"`
	case "authentication_failed":
		return "The model's server refused gnosh's key: /doctor says more, /model picks another model"
	case "invalid_request":
		return `The server refused the request: /rewind edits the prompt, /doctor says more, /model picks another`
	case "server_error":
		return `Send "go on" to try again; /doctor checks the model's server, /model picks another`
	}
	return `Send "go on" to try again; /debug looks into what went wrong`
}

// streamLines is what is still being written: the answer's lines that may
// change, the end of the reasoning while it is folded, and the calls in
// flight.
func (s *session) streamLines(width int) []string {
	var out []string
	if s.focus && s.fold.calls > 0 {
		// (Above what is being written, where the scrollback will have it.)
		out = append(out, fit(s.m, s.foldLine(true), width))
	}
	rp := &s.reply
	if rp.thinking {
		words := rp.thinkRest
		if !s.detail {
			words = rp.think.String()
			if len(words) > 4*width+256 {
				words = words[len(words)-(4*width+256):]
			}
		}
		rows := s.thinkRows(words, width)
		if n := len(rows); n > thinkLive {
			rows = rows[n-thinkLive:]
		}
		if !s.detail || !rp.thinkBegun {
			head := s.th.Thinking.Render(s.th.ThinkMark + " thinking")
			if !s.detail {
				// Folded, only its last rows show: say every time how
				// to have all of it.
				head += s.th.Quiet.Render(s.th.Sep + "Ctrl+O shows all of it")
			}
			out = append(out, fit(s.m, head, width))
		}
		out = append(out, rows...)
	}
	if rp.md != nil {
		out = append(out, rp.md.Live()...)
	}
	shown := s.calls
	const most = 4
	if len(shown) > most {
		shown = shown[len(shown)-most:]
		out = append(out, fit(s.m, s.th.Quiet.Render(count(len(s.calls)-most, "more call", "more calls")+" in flight"), width))
	}
	for _, c := range shown {
		out = append(out, s.liveRows(c)...)
	}
	return out
}

// thinkLive is how many rows of reasoning show in the live region while
// it arrives folded.
const thinkLive = 4

// thinkRows lays reasoning out as rows: plain words, set in by two cells,
// in the style of reasoning. It is outside text and is made safe here.
func (s *session) thinkRows(words string, width int) []string {
	var rows []string
	for _, l := range safeLines(words) {
		if strings.TrimSpace(l) == "" {
			continue
		}
		rows = append(rows, wrap(s.m, s.th.Thinking.Render(l), width, "  ", "  ")...)
	}
	return rows
}

// personQueue is the prompts of the person's own that have been sent and
// have not gone out: the ones the session lists as waiting, and the ones
// just sent that it has not listed yet.
func (s *session) personQueue() []string {
	var out []string
	listed := map[string]bool{}
	for _, q := range s.queue {
		listed[q.ID] = true
		if !q.Prompt.Hidden {
			out = append(out, q.Prompt.Text)
		}
	}
	for _, p := range s.sending {
		if !listed[p.id] {
			out = append(out, p.draft.Collapsed())
		}
	}
	return out
}

// queueLines draws the prompts and commands that wait for their turn, one
// row each, on at most most rows.
func (s *session) queueLines(width, most int) []string {
	waiting := s.personQueue()
	for _, line := range s.cmdq {
		shown, _ := shownCommand(line)
		waiting = append(waiting, shown+"  (runs when the turn ends)")
	}
	if len(waiting) == 0 || most < 1 {
		return nil
	}
	th := &s.th
	var rows []string
	room := most
	if most >= 2 {
		room-- // the row that names the key
	}
	for i, p := range waiting {
		if i == room-1 && len(waiting) > room {
			rows = append(rows, fit(s.m, th.Quiet.Render(fmt.Sprintf("  and %d more queued", len(waiting)-i)), width))
			break
		}
		first, more := firstLine(p)
		if more {
			first += th.Quiet.Render(" (and more lines)")
		}
		rows = append(rows, fit(s.m, th.Quiet.Render("  queued: ")+first, width))
	}
	if most >= 2 {
		rows = append(rows, fit(s.m, th.Quiet.Render("  Up in an empty box takes the queue back for editing"), width))
	}
	return rows
}

// taskLines draws the session's task list under the prompt box: a box for
// each task, ticked when it is done and pointed at when it is the one in
// hand, which reads the way it does while it is being done. A list longer
// than its rows shows the tasks around the one in hand, and counts the
// rest.
func (s *session) taskLines(width, most int) []string {
	n := len(s.tasks)
	if s.hideTasks || most < 1 || n == 0 {
		return nil
	}
	th := &s.th
	from, to := 0, n
	if n > most {
		// One row says how many are not shown. The window begins one
		// task before the one in hand, or before the first that is not
		// done, so that what was just finished is still in sight.
		now := s.taskInHand()
		keep := most - 1
		from = min(max(now-1, 0), n-keep)
		to = from + keep
	}
	rows := make([]string, 0, most)
	for _, t := range s.tasks[from:to] {
		row := ""
		switch t.Status {
		case app.TaskCompleted:
			row = th.Quiet.Render(th.TaskDone + " " + oneLine(t.Content))
		case app.TaskInProgress:
			row = th.Busy.Render(th.TaskNow) + " " + oneLine(firstNonEmpty(t.ActiveForm, t.Content))
		default:
			row = th.Quiet.Render(th.TaskTodo) + " " + oneLine(t.Content)
		}
		rows = append(rows, fit(s.m, "  "+row, width))
	}
	if n > most {
		done, _ := s.taskCount()
		rows = append(rows, fit(s.m, th.Quiet.Render(fmt.Sprintf("  and %s (%d of %d done)", count(n-(to-from), "more task", "more tasks"), done, n)), width))
	}
	return rows
}

// taskInHand is the place in the list of the task being done, or of the
// first that is not done when none is, or of the last.
func (s *session) taskInHand() int {
	for i, t := range s.tasks {
		if t.Status == app.TaskInProgress {
			return i
		}
	}
	for i, t := range s.tasks {
		if t.Status != app.TaskCompleted {
			return i
		}
	}
	return len(s.tasks) - 1
}

// taskCount is how many of the tasks are done, and what the one in hand
// says while it is being done.
func (s *session) taskCount() (done int, doing string) {
	for _, t := range s.tasks {
		switch t.Status {
		case app.TaskCompleted:
			done++
		case app.TaskInProgress:
			if doing == "" {
				doing = oneLine(firstNonEmpty(t.ActiveForm, t.Content))
			}
		}
	}
	return done, doing
}

// hintLines is the short notice under the status line: one a key press
// of the interface's called for, or the prompt box's warning that a
// second press will clear it or leave. It is wrapped to the terminal's
// width, onto three rows at most, so that it is read whole.
func (s *session) hintLines(width int, now time.Time) []string {
	words := ""
	switch {
	case s.hint != "" && now.Before(s.hintUntil):
		words = s.hint
	case s.q == nil || s.q.guarded:
		words = s.ed.Hint().String()
	}
	if words == "" {
		return nil
	}
	rows := wrap(s.m, s.th.Hint.Render(oneLine(words)), width, "", "")
	if len(rows) > maxHintRows {
		rows = rows[:maxHintRows]
	}
	return rows
}

// activityLine is the one line that says what gnosh is doing or waiting
// for, with how long that has lasted. It is empty when the session is
// idle; the status line then says so.
//
// Nothing in it looks like data: a mark, plain words, a time. With colour
// the mark turns and colour falls through the words, the more the longer
// the model has been in one thought (fall.go); the words stay as they
// are.
func (s *session) activityLine(width int, now time.Time) string {
	th := &s.th
	// what says it in full and brief in fewer words, for a terminal too
	// narrow for the first.
	what, brief, extra := "", "", ""
	since := s.since
	model := firstNonEmpty(oneLine(s.model), "the model")
	switch {
	case s.stopping:
		what = "stopping the turn"
	case s.q != nil:
		// The question itself stands right under this line.
		what = "waiting for your answer"
		since = s.q.opened
		switch d := s.q.deadline; {
		case !d.IsZero():
			extra = "answered as no in " + span(d.Sub(now))
		case s.q.idle > 0:
			if left := s.q.idle - now.Sub(s.q.idleSince(s.lastInput)); left <= awayCountdown {
				extra = "goes on without you in " + span(max(left, 0))
			}
		}
	case !s.busy && s.cmdRun != "":
		shown, _ := shownCommand(s.cmdRun)
		what = "running " + oneLine(shown)
	case !s.busy:
		return ""
	case s.hookStatus != "":
		// A hook's own words for what it does, while it runs.
		what = s.hookStatus
	default:
		switch s.ph {
		case phStarting:
			what = firstNonEmpty(s.said(), "starting")
		case phRequest:
			what, brief = "waiting for "+model+" to start its reply", "waiting for the model"
			if s.attempt > 1 {
				what += fmt.Sprintf(" (try %d)", s.attempt)
			}
			switch {
			case s.load != "":
				// The server's own word for the wait (app.ServerLoadEvent).
				extra = s.load
			case s.estimate > 0:
				extra = "about " + tokenCount(s.estimate) + " to read"
			}
		case phThinking:
			what, brief = model+" is thinking", "thinking"
			if s.thinkingFor(now) >= deepAfter {
				// The words say what the working mark shows.
				what, brief = model+" is deep in thought", "deep in thought"
			}
		case phWriting:
			what, brief = model+" is writing", "writing"
			if s.reply.chars > 0 {
				extra = "about " + tokenCount(max(s.reply.chars/4, 1)) + " so far"
			}
		case phCall:
			what, brief = model+" is writing a tool call", "writing a tool call"
			if n := len(s.calls); n > 0 {
				what = model + " is writing a " + oneLine(s.calls[n-1].name) + " call"
				brief = "writing a " + oneLine(s.calls[n-1].name) + " call"
			}
		case phTools:
			what = s.running()
		case phRetry:
			what, brief = "waiting to try "+model+" again", "waiting to try again"
			tail := ""
			if left := s.retryUntil.Sub(now); left > 0 {
				tail += " in " + span(left+time.Second-1)
			}
			if s.retries > 1 {
				tail += fmt.Sprintf(" (try %d)", s.retries+1)
			}
			what, brief = what+tail, brief+tail
			since = s.retrySince
		case phSummary:
			what, brief = "writing a summary of the conversation, to make room", "summarizing"
		default:
			what = firstNonEmpty(s.said(), "working")
		}
	}
	// With colour, and unless a question waits for the person (a line
	// that waits for someone does not move), the line moves: its mark
	// turns, and the fall passes over its words (fall.go).
	moving := th.Fall.moves() && s.q == nil
	markWidth := s.m.Width(th.BusyMark)
	if moving {
		markWidth = 1
	}
	room := width - markWidth - 1
	if brief != "" && s.m.Width(what) > room {
		what = brief
	}
	parts := []string{what}
	if took := now.Sub(since); !since.IsZero() && took >= time.Second {
		parts = append(parts, span(took))
	}
	if extra != "" {
		parts = append(parts, extra)
	}
	if !s.stopping && s.q == nil {
		parts = append(parts, "Esc stops")
	}
	// As many of the parts as fit; what gnosh is doing is always there.
	rest := ""
	used := s.m.Width(parts[0])
	for _, p := range parts[1:] {
		if used+s.m.Width(th.Sep)+s.m.Width(p) > room {
			break
		}
		rest += th.Sep + p
		used += s.m.Width(th.Sep) + s.m.Width(p)
	}
	if !moving {
		return fit(s.m, th.Busy.Render(th.BusyMark)+" "+th.Activity.Render(parts[0])+th.Quiet.Render(rest), width)
	}
	s.fall.step(now, depthOf(s.thinkingFor(now)), used)
	return fit(s.m, s.fall.mark(&th.Fall)+" "+s.fall.wash(&th.Fall, s.m, parts[0], rest), width)
}

// thinkingFor is how long the thought that is arriving now has lasted, or
// nothing when the model is not thinking.
func (s *session) thinkingFor(now time.Time) time.Duration {
	if !s.busy || s.ph != phThinking || !s.reply.thinking {
		return 0
	}
	return max(now.Sub(s.reply.thinkSince), 0)
}

// said is what the session itself says it is waiting for, in its own
// plain words, for the moments its events do not describe.
func (s *session) said() string {
	st := s.c.State()
	if !st.Busy {
		return ""
	}
	return oneLine(st.Waiting)
}

// running names the tool calls that are running.
func (s *session) running() string {
	var started []*call
	for _, c := range s.calls {
		if c.started {
			started = append(started, c)
		}
	}
	switch len(started) {
	case 0:
		return firstNonEmpty(s.said(), "running tool calls")
	case 1:
		c := started[0]
		h := s.headOf(c.name, c.input, &c.action)
		if h.shell {
			return "running " + firstNonEmpty(h.arg, "a command")
		}
		if h.arg != "" {
			return "running " + oneLine(c.name) + " " + h.arg
		}
		return "running " + oneLine(c.name)
	}
	names := make([]string, len(started))
	for i, c := range started {
		names[i] = oneLine(c.name)
	}
	return fmt.Sprintf("running %d tool calls: %s", len(started), strings.Join(names, ", "))
}

// segment is one part of the status line. keep is how long it stays as
// the terminal gets narrower: the part with the lowest keep goes first.
type segment struct {
	words string
	style text.Style
	keep  int
}

// The permission modes as the status line names them.
var modeNames = map[string]string{
	"default":           "manual mode",
	"acceptEdits":       "accept edits",
	"plan":              "plan mode",
	"dontAsk":           "don't ask",
	"bypassPermissions": "BYPASS PERMISSIONS",
}

// customLines is the status line the configured command printed (Claude
// Code's statusLine): the permission mode first, when it is not the
// default, and the runs in the background that wait on the person, since
// those are always in sight, and then the command's lines, made safe to
// draw and set in by the padding.
func (s *session) customLines(width int) []string {
	var out, head []string
	if v := s.vimWords(); v != "" {
		head = append(head, s.th.Mode.Render(v))
	}
	if s.mode != "" && s.mode != "default" {
		head = append(head, s.modeWords())
	}
	if v := s.agentsWord(); v != "" {
		head = append(head, s.th.Status.Render(v))
	}
	if len(head) > 0 {
		out = append(out, strings.Join(head, "  "))
	}
	pad := strings.Repeat(" ", min(s.customPad, width/2))
	for _, l := range s.custom {
		l = statusText(l)
		if s.th.Profile == text.NoColor {
			// No colour at all where the person asked for none (NO_COLOR),
			// or the terminal takes none: the command's go too.
			l = text.Strip(l)
		}
		out = append(out, pad+l)
	}
	return out
}

// agentsWord is what the status line says of the runs in the background
// (the "agents" fact): how many wait on the person, or are done since they
// looked, after the ← that opens the agent view over the session, as Claude
// Code's footer reads "← 2 agents"; for a session's first seconds, "← for
// agents". Where there is no agent view to open, gnosh agents answers them.
func (s *session) agentsWord() string {
	v := oneLine(s.facts["agents"])
	switch {
	case v == "" && s.agentsOffered() && s.now().Before(s.agentsHintUntil):
		return "← for agents"
	case v == "":
		return ""
	case s.agentsOffered():
		return "← " + v
	}
	return v + ": gnosh agents"
}

// vimWords names the mode of the box's vim keys as vim's status line does
// ("-- INSERT --"), or is "" when they are off.
func (s *session) vimWords() string {
	if v := s.ed.Vim(); v != editor.VimOff {
		return "-- " + v.String() + " --"
	}
	return ""
}

// modeWords is the permission mode as the status line names it, styled.
func (s *session) modeWords() string {
	th := &s.th
	name, ok := modeNames[s.mode]
	if !ok {
		name = oneLine(s.mode) + " mode"
	}
	switch s.mode {
	case "default":
		return th.Status.Render(name)
	case "bypassPermissions":
		return th.Bypass.Render("[" + name + "]")
	}
	return th.Mode.Render(name)
}

// statusLine is the one line under the prompt box: whether gnosh is idle,
// the permission mode, the model and how hard it thinks, how full its
// window is, the folder and its branch, and what the session has told of
// beyond that (the frame it is in, the task in hand, commands in the
// background, how many deliverables are delivered and how many pending).
// It never wraps: in a narrow terminal its parts go one by one, the least
// needed first, and the permission mode last.
func (s *session) statusLine(width int) string {
	th := &s.th
	var segs []segment
	if v := s.vimWords(); v != "" {
		segs = append(segs, segment{v, th.Mode, 95})
	}
	if !s.busy && s.cmdRun == "" && s.q == nil {
		segs = append(segs, segment{"idle", th.Status, 85})
	}

	// The mode is named in words, always, and the one in which nothing
	// asks is in capitals and brackets, so that it cannot be missed with
	// no colour at all.
	name, ok := modeNames[s.mode]
	if !ok {
		name = oneLine(s.mode) + " mode"
	}
	switch s.mode {
	case "default":
		segs = append(segs, segment{name, th.Status, 100})
	case "bypassPermissions":
		segs = append(segs, segment{"[" + name + "]", th.Bypass, 100})
	default:
		segs = append(segs, segment{name, th.Mode, 100})
	}

	if m := oneLine(firstNonEmpty(s.model, s.info.Model)); m != "" {
		segs = append(segs, segment{m, th.ModelName, 90})
	}
	if lvl := oneLine(s.info.Thinking); lvl != "" {
		segs = append(segs, segment{"thinking " + lvl, th.Status, 70})
	}
	if c := s.ctxs; c.Window > 0 {
		pct := c.Used * 100 / c.Window
		words := fmt.Sprintf("context %d%%", pct)
		if !c.Known {
			words += " of an assumed " + tokens(c.Window)
		}
		style := th.Status
		if c.Point > 0 && c.Used*10 >= c.Point*9 {
			style = th.Full
		}
		segs = append(segs, segment{words, style, 80})
	}
	if s.loop {
		segs = append(segs, segment{"/loop on", th.Mode, 75})
	}
	if s.cwd != "" {
		where := showPath("", s.home, s.cwd)
		if s.branch != "" {
			where += " (" + s.branch + ")"
		}
		segs = append(segs, segment{where, th.Status, 60})
	}
	if n := len(s.tasks); n > 0 {
		// How far the task list has come; and the task in hand, when
		// the list itself is out of sight.
		done, doing := s.taskCount()
		words, keep := fmt.Sprintf("tasks %d/%d", done, n), 55
		if s.hideTasks && doing != "" {
			words += ": " + s.m.Truncate(doing, 32, "...")
			keep = 72
		}
		segs = append(segs, segment{words, th.Status, keep})
	}
	if v := oneLine(s.facts["frame"]); v != "" {
		segs = append(segs, segment{"in " + v, th.Status, 50})
	}
	if v := oneLine(s.facts["goal"]); v != "" {
		segs = append(segs, segment{"goal: " + v, th.Status, 45})
	}
	if v := oneLine(s.facts["background"]); v != "" && v != "0" {
		segs = append(segs, segment{v + " in the background", th.Status, 65})
	}
	if v := s.agentsWord(); v != "" {
		segs = append(segs, segment{v, th.Status, 66})
	}
	if delivered, pending := app.CountDeliverables(s.deliv); delivered+pending > 0 {
		// What the agent has handed over and what it has promised, which
		// /deliverables lists: "2 delivered · 1 pending". They are two
		// parts, so that in a narrow terminal the promise goes first and
		// the count of what there is to open stays.
		if delivered > 0 {
			segs = append(segs, segment{fmt.Sprintf("%d delivered", delivered), th.Status, 68})
		}
		switch {
		case pending > 0 && delivered > 0:
			segs = append(segs, segment{fmt.Sprintf("%d pending", pending), th.Status, 62})
		case pending > 0:
			segs = append(segs, segment{count(pending, "deliverable", "deliverables") + " pending", th.Status, 62})
		}
	}
	if v := oneLine(s.facts["session"]); v != "" {
		segs = append(segs, segment{v, th.Status, 30})
	}
	if s.detail {
		segs = append(segs, segment{"detail on", th.Status, 40})
	}

	sep := s.m.Width(th.Sep)
	for {
		total := 0
		for i, g := range segs {
			if i > 0 {
				total += sep
			}
			total += s.m.Width(g.words)
		}
		if total <= width || len(segs) <= 1 {
			break
		}
		drop := 0
		for i, g := range segs {
			if g.keep < segs[drop].keep {
				drop = i
			}
		}
		segs = append(segs[:drop], segs[drop+1:]...)
	}
	var b strings.Builder
	for i, g := range segs {
		if i > 0 {
			b.WriteString(th.Status.Render(th.Sep))
		}
		b.WriteString(g.style.Render(g.words))
	}
	return fit(s.m, b.String(), width)
}
