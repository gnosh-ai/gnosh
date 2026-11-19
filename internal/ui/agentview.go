package ui

import (
	"cmp"
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"time"

	"github.com/gnosh-ai/gnosh/internal/app"
	"github.com/gnosh-ai/gnosh/internal/core"
	"github.com/gnosh-ai/gnosh/internal/term"
	"github.com/gnosh-ai/gnosh/internal/text"
	"github.com/gnosh-ai/gnosh/internal/ui/editor"
)

// The agent view: gnosh agents on a terminal, as claude agents opens
// Claude Code's (code.claude.com/docs/en/agent-view, read 18 November
// 2026). On the whole screen are the runs in the background, the working
// ones and then those that are over, each with what it is doing or what
// it answered, and under them a box in which a prompt starts a new one.
// Space peeks at the run chosen; Enter watches a working run's
// conversation as it grows, or opens one that is over (gnosh -r, and the
// view comes back when that session is left); Ctrl+X stops a working
// run, and twice takes one that is over off the list. What it shows and
// does it asks of app.AgentSource (app/agentview.go).
//
//	The list has the keys
//	Up, Down, PgUp, PgDn,        choose a run
//	Home, End, the wheel
//	Space                        (empty box) peek at the run chosen, or stop
//	Enter, Right                 (empty box) watch the run, or open it once it is over
//	Enter                        (a prompt in the box) start a run on it, in the folder of the view
//	n:<text> s:<state> o:<text>  in the box: list only the runs whose name or prompt, state, or
//	                             answer has it; Enter then opens the run chosen
//	Ctrl+X                       stop the run; on one that is over, twice: take it off the list
//	?                            (empty box) the keys
//	Esc                          stop peeking, empty the box, or leave
//	Ctrl+C                       empty the box; twice: leave
//	Ctrl+L                       draw the screen again
//	Ctrl+Z                       suspend gnosh; fg brings it back
//
//	A run is watched
//	Up, Down, PgUp, PgDn,        scroll
//	Home, End, the wheel
//	Esc, Left, q, Ctrl+C         back to the list
//	Ctrl+X                       stop it
//	Enter                        open it, once it is over

// How often the view looks again, and how long what it says lasts.
const (
	// agentLook is how often the list is read again, and watchLook how
	// often the conversation of the run watched is.
	agentLook = time.Second
	watchLook = 300 * time.Millisecond
	// agentPair is how soon the second Ctrl+X or Ctrl+C must follow the
	// first, and agentSaid how long a line that says what came of a key
	// stays.
	agentPair = 2 * time.Second
	agentSaid = 6 * time.Second
	// minDispatch is the shortest prompt that starts a run: shorter is
	// taken for a slip of the keys, as Claude Code's "Too short" does.
	minDispatch = 4
	// peekRows is how many lines peeking shows of what a run did.
	peekRows = 8
)

// NewAgentView makes the agent view of gnosh agents (app.Options's
// AgentView), or returns nil where there is no terminal to draw it on, as
// New does.
func NewAgentView(o Options) func(app.AgentViewInfo) int {
	if !canDraw(o) {
		return nil
	}
	return func(info app.AgentViewInfo) int { return runAgentView(o, info) }
}

// agentView is what the view shows, and what it does with a key: a value
// with no terminal behind it, which the loop in runAgentView feeds and
// draws (and the tests drive).
type agentView struct {
	th   Theme
	m    text.Metrics
	now  func() time.Time
	info app.AgentViewInfo
	src  app.AgentSource
	// async runs work away from the loop; what it returns is done on the
	// loop afterwards.
	async func(work func() func(*agentView))

	runs []app.AgentRun
	// doing is what each working run is doing, from the end of its
	// conversation, its last step last; status says it in one line.
	doing  map[string][]string
	status map[string]string
	looked time.Time
	// sel is the id of the run chosen, and at its place in the list
	// shown, for when it is gone from the list.
	sel string
	at  int
	// top is the first run of the list shown, when the list is longer
	// than the screen.
	top int

	peek, help bool
	// byFolder groups the list by the runs' folders instead of by how
	// they stand (Ctrl+S, as Claude Code's).
	byFolder bool
	box      *editor.Editor
	// said is a line that says what came of a key, until saidUntil.
	said      string
	saidUntil time.Time
	// armed is the key waiting for its second press ("x <id>", or "c"),
	// until armedUntil.
	armed      string
	armedUntil time.Time
	// stopping are the runs asked to stop, and starting how many runs are
	// being started. told are the working runs sent words, which are sent
	// back to work on them if they end before they read them.
	stopping map[string]bool
	starting int
	told     map[string]bool

	// prev is how each run stood when the list was last read (nil before
	// it was); notes are what the person is to be told of the changes
	// since (a run needs input, or is done), for the loop to send by
	// their notification channel.
	prev  map[string]string
	notes []string
	// askedAt is when each question waiting on a run was first listed,
	// lastKey when the last key came, and keyGap how long before the key
	// in hand: a question takes no answer until it has been seen
	// (guardShown) and the person has stopped typing (guardPause), as the
	// session's own questions do (settled).
	askedAt map[string]time.Time
	lastKey time.Time
	keyGap  time.Duration
	// typedAt is when what is in the box began to be typed: words begun
	// before a question was seen are for the run, not an answer.
	typedAt time.Time

	// flags are what the runs started from the view are given: the -y,
	// mode, model and effort gnosh agents was given, as /model and
	// /effort typed in the box change them for this view (not saved).
	flags []string

	watch *watching
	// open is the run whose session is to be opened (gnosh -r), and
	// suspend says the person pressed Ctrl+Z: the loop does both.
	open    string
	suspend bool
	quit    bool
	clear   bool
}

// watching is the run whose conversation is on the screen, laid out, and
// the scrolling asked for that the loop is to do: rows and pages back (or
// forward, when less than zero), to the start, to the end.
type watching struct {
	id          string
	lines       []string
	shown       int
	width       int
	looked      time.Time
	rows, pages int
	start, end  bool
}

func newAgentView(th Theme, m text.Metrics, now func() time.Time, info app.AgentViewInfo) *agentView {
	v := &agentView{th: th, m: m, now: now, info: info, src: info.Source,
		doing: map[string][]string{}, status: map[string]string{}, stopping: map[string]bool{}, told: map[string]bool{}}
	v.async = func(work func() func(*agentView)) { work()(v) }
	v.flags = slices.Clone(info.Flags)
	v.box = editor.New(editor.Options{
		Theme:       th.Editor,
		Metrics:     m,
		Plain:       true,
		MaxHeight:   4,
		Now:         now,
		Placeholder: listPlaceholder,
	})
	return v
}

// focusOn opens the view on the run of the session id (gnosh attach): it
// is chosen, and watched if it works, its question shown if it waits on
// one, opened (gnosh -r) if it is over.
func (v *agentView) focusOn(id string) {
	_, ok := v.byID(id)
	if !ok {
		v.say("The run %s is not among the runs in the background.", app.RunShort(id))
		return
	}
	v.sel = id
	v.keepChoice()
	// (Before any key: the box is empty, and nothing is peeked at.)
	v.enter()
}

// refresh reads the list again, and what each working run is doing.
func (v *agentView) refresh() {
	v.looked = v.now()
	v.runs = v.src.Runs()
	v.sortRuns()
	asked := map[string]time.Time{}
	for _, r := range v.runs {
		if q := r.Question; q != nil {
			asked[q.ID] = v.looked
			if at, ok := v.askedAt[q.ID]; ok {
				asked[q.ID] = at
			}
		}
	}
	v.askedAt = asked
	doing, status := map[string][]string{}, map[string]string{}
	// (Those the person stopped are told of no more, and get no words
	// again.)
	stopped := map[string]bool{}
	for _, r := range v.runs {
		if !running(r) {
			stopped[r.ID] = v.stopping[r.ID] || r.State == "stopped"
			delete(v.stopping, r.ID)
			continue
		}
		doing[r.ID], status[r.ID] = v.activity(r, v.src.Conversation(r.ID))
	}
	v.doing, v.status = doing, status
	// What changed since the last look that the person is told of, as
	// Claude Code's view notifies: a run that needs input, and one that
	// is done. (Not what was there as the view opened.)
	states := make(map[string]string, len(v.runs))
	for _, r := range v.runs {
		states[r.ID] = r.State
		was, known := v.prev[r.ID]
		switch {
		case v.prev == nil || r.State == was:
		case r.State == "blocked" && r.Question != nil:
			v.notes = append(v.notes, runName(r)+" needs input: "+oneLine(r.Question.Title))
		case (r.State == "done" || r.State == "failed") && !stopped[r.ID] && (!known || was == "working" || was == "blocked"):
			last := "it printed nothing"
			if len(r.Output) > 0 {
				last = oneLine(r.Output[len(r.Output)-1])
			}
			how := " is done: "
			if r.State == "failed" {
				how = " failed: "
			}
			v.notes = append(v.notes, runName(r)+how+last)
		}
	}
	v.prev = states
	// Words a run ended before it read go to it again, as a reply to a
	// run that is over does.
	for _, r := range v.runs {
		if !v.told[r.ID] || running(r) {
			continue
		}
		delete(v.told, r.ID)
		if stopped[r.ID] {
			if unread := v.src.Unread(r.ID); len(unread) > 0 {
				v.say("%s was stopped before it read your words: %s", runName(r), oneLine(strings.Join(unread, " ")))
			}
			continue
		}
		if unread := v.src.Unread(r.ID); len(unread) > 0 {
			v.resume(r, strings.Join(unread, "\n\n"), "ended before it read your words, and goes on with them in the background")
		}
	}
	v.keepChoice()
}

// sortRuns puts the runs in the list's order: those that wait on the
// person first, then the working ones, whatever order the source keeps;
// grouped by folder, the folders by name first.
func (v *agentView) sortRuns() {
	slices.SortStableFunc(v.runs, func(a, b app.AgentRun) int {
		if v.byFolder {
			if c := cmp.Compare(runFolder(a), runFolder(b)); c != 0 {
				return c
			}
		}
		return cmp.Compare(rank(a), rank(b))
	})
}

// runFolder is the folder a run's group is named by when the list is
// grouped by folder.
func runFolder(r app.AgentRun) string { return firstNonEmpty(r.Folder, "(folder not known)") }

// rank is where a run's group stands in the list: Needs input, Working,
// Completed.
func rank(r app.AgentRun) int {
	switch {
	case r.State == "blocked":
		return 0
	case running(r):
		return 1
	}
	return 2
}

// running reports whether a run's process works: one that waits on a
// question works too.
func running(r app.AgentRun) bool { return r.State == "working" || r.State == "blocked" }

// keepChoice keeps the run chosen chosen: when it is no longer listed, the
// one now in its place is.
func (v *agentView) keepChoice() {
	shown := v.shown()
	if len(shown) == 0 {
		v.sel, v.at = "", 0
		return
	}
	for i, r := range shown {
		if r.ID == v.sel {
			v.at = i
			return
		}
	}
	v.at = min(max(v.at, 0), len(shown)-1)
	v.sel = shown[v.at].ID
}

// activity is what a working run's conversation says it did last, a line a
// step, and what it is doing now, in one line.
func (v *agentView) activity(r app.AgentRun, entries []app.TranscriptEntry) (lines []string, now string) {
	if len(entries) > 4*peekRows {
		entries = entries[len(entries)-4*peekRows:]
	}
	waiting := false // the model has the turn: it was sent a prompt or results
	for _, e := range entries {
		switch e.Kind {
		case "prompt":
			// (Its first prompt is said above what it did.)
			if texts := blockTexts(e.Message); len(texts) > 0 && !e.Hidden && strings.TrimSpace(texts[0]) != strings.TrimSpace(r.Prompt) {
				lines = append(lines, v.th.PromptMark+firstOf(texts[0]))
			}
			waiting = true
		case "results":
			for _, b := range e.Message.Blocks {
				if b.Type == core.BlockToolResult && b.IsError {
					lines = append(lines, v.th.FailMark+" "+firstOf(resultText(b)))
				}
			}
			waiting = true
		case "reply":
			for _, b := range e.Message.Blocks {
				switch b.Type {
				case core.BlockText:
					if l := firstOf(b.Text); l != "" {
						lines = append(lines, l)
					}
				case core.BlockToolUse:
					lines = append(lines, v.th.ToolMark+" "+callLine(callHead(r.Cwd, v.info.Home, b.Name, b.Input, nil)))
				}
			}
			waiting = false
		case "summary":
			lines = append(lines, "(the conversation so far was summed up)")
		}
	}
	if len(lines) > peekRows {
		lines = lines[len(lines)-peekRows:]
	}
	switch {
	case waiting && len(entries) > 0:
		now = "thinking"
	case len(lines) > 0:
		now = strings.TrimPrefix(lines[len(lines)-1], v.th.ToolMark+" ")
	default:
		now = "starting"
	}
	return lines, now
}

// firstOf is the first line of outside text that is not blank, safe to
// draw.
func firstOf(s string) string {
	l, _ := firstLine(s)
	return l
}

// callLine is a call in one line: the command behind "$", or the tool
// and what it is about.
func callLine(h head) string {
	switch {
	case h.shell:
		if h.host != "" {
			return h.host + " $ " + h.arg
		}
		return "$ " + h.arg
	case h.code:
		for _, l := range h.body {
			if strings.TrimSpace(l) != "" {
				return strings.TrimSpace(h.name+" "+h.arg) + ": " + strings.TrimSpace(l)
			}
		}
	}
	return strings.TrimSpace(h.name + " " + h.arg)
}

// filter is what the box says to list, when what is in it is a filter:
// words that begin with n:, s: or o:.
type filter struct{ names, states, outputs []string }

// boxFilter reads the box as a filter.
func (v *agentView) boxFilter() (filter, bool) {
	var f filter
	words := strings.Fields(v.box.Text())
	if len(words) == 0 {
		return f, false
	}
	for _, w := range words {
		key, val, ok := strings.Cut(w, ":")
		if !ok || len(key) != 1 {
			return filter{}, false
		}
		val = strings.ToLower(val)
		switch key {
		case "n":
			f.names = append(f.names, val)
		case "s":
			f.states = append(f.states, val)
		case "o":
			f.outputs = append(f.outputs, val)
		default:
			return filter{}, false
		}
	}
	return f, true
}

// passes reports whether the filter lists r.
func (f filter) passes(r app.AgentRun) bool {
	has := func(s, part string) bool { return strings.Contains(strings.ToLower(s), part) }
	for _, n := range f.names {
		if !has(r.Name, n) && !has(r.Prompt, n) && !strings.HasPrefix(r.ID, n) {
			return false
		}
	}
	for _, s := range f.states {
		// A state (s:blocked is Claude Code's for what waits on the
		// person), or a group: s:needs (input), s:completed.
		if !strings.HasPrefix(r.State, s) && !(s == "completed" && !running(r)) && !(s == "needs" && r.State == "blocked") {
			return false
		}
	}
	for _, o := range f.outputs {
		if o != "" && !has(strings.Join(r.Output, "\n"), o) || o == "" && len(r.Output) == 0 {
			return false
		}
	}
	return true
}

// shown are the runs listed, as the filter in the box leaves them.
func (v *agentView) shown() []app.AgentRun {
	f, ok := v.boxFilter()
	if !ok {
		return v.runs
	}
	var out []app.AgentRun
	for _, r := range v.runs {
		if f.passes(r) {
			out = append(out, r)
		}
	}
	return out
}

// chosen is the run chosen, if any.
func (v *agentView) chosen() (app.AgentRun, bool) {
	for _, r := range v.shown() {
		if r.ID == v.sel {
			return r, true
		}
	}
	return app.AgentRun{}, false
}

// rowName is what the list calls a run: the name it was given, or else
// its prompt's first line, as a session is named after its first prompt
// (Claude Code's view names each session too); its id when it has
// neither. Notes and the peek's head say runName, the id that gnosh logs,
// stop and -r take.
func rowName(r app.AgentRun) string {
	if r.Name != "" {
		return oneLine(r.Name)
	}
	if p := firstOf(r.Prompt); p != "" {
		return p
	}
	return r.Short
}

// runName is what a run is called in what the view says of it: its name,
// or the start of its id.
func runName(r app.AgentRun) string {
	if r.Name != "" {
		return oneLine(r.Name)
	}
	return r.Short
}

// say shows a line that says what came of a key, for a while.
func (v *agentView) say(format string, args ...any) {
	v.said, v.saidUntil = fmt.Sprintf(format, args...), v.now().Add(agentSaid)
}

// tick looks again when it is time, and lets lines and keys that waited
// too long go.
func (v *agentView) tick() {
	now := v.now()
	if now.Sub(v.looked) >= agentLook {
		v.refresh()
	}
	if v.watch != nil && now.Sub(v.watch.looked) >= watchLook {
		v.layWatch(false)
	}
	if v.said != "" && now.After(v.saidUntil) {
		v.said = ""
	}
	if v.armed != "" && now.After(v.armedUntil) {
		v.armed = ""
	}
}

// input takes in one thing the terminal reported.
func (v *agentView) input(ev term.Event) {
	now := v.now()
	v.keyGap, v.lastKey = now.Sub(v.lastKey), now
	if strings.TrimSpace(v.box.Text()) == "" {
		v.typedAt = now
	}
	if v.watch != nil {
		v.watchInput(ev)
		return
	}
	switch ev := ev.(type) {
	case term.Key:
		v.key(ev)
	case term.Paste:
		v.box.Handle(ev)
		v.keepChoice()
	case term.Mouse:
		switch ev.Action {
		case term.MouseWheelUp:
			v.move(-1)
		case term.MouseWheelDown:
			v.move(1)
		}
	}
}

// move chooses the run n places down the list (up, for a negative n).
func (v *agentView) move(n int) {
	shown := v.shown()
	if len(shown) == 0 {
		return
	}
	v.at = min(max(v.at+n, 0), len(shown)-1)
	v.sel = shown[v.at].ID
}

// groupOf is the group a run is listed under: its folder when the list is
// grouped by folder, else Needs input, Working or Completed.
func (v *agentView) groupOf(r app.AgentRun) string {
	if v.byFolder {
		return runFolder(r)
	}
	return [...]string{"Needs input", "Working", "Completed"}[rank(r)]
}

// tally counts the runs of each rank: those that wait on the person, the
// working ones, and those that are over.
func (v *agentView) tally() (n [3]int) {
	for _, r := range v.runs {
		n[rank(r)]++
	}
	return n
}

// jumpGroup chooses the first run of the next group, or going back, the
// first of the group the run chosen is in, or of the one before when it is
// that one already: Claude Code's Alt+↓ and Alt+↑ between group headers.
func (v *agentView) jumpGroup(down bool) {
	shown := v.shown()
	if len(shown) == 0 {
		return
	}
	at := min(max(v.at, 0), len(shown)-1)
	to := at
	for i := range shown {
		if i > 0 && v.groupOf(shown[i]) == v.groupOf(shown[i-1]) {
			continue // (not the first of a group)
		}
		if !down && i < at {
			to = i
		} else if down && i > at {
			to = i
			break
		}
	}
	v.move(to - v.at)
}

// key takes in a key while the list is on the screen.
func (v *agentView) key(k term.Key) {
	empty := strings.TrimSpace(v.box.Text()) == ""
	page := 10
	if k.Is('c', term.ModCtrl) {
		switch {
		case !empty:
			v.box.Reset()
			v.keepChoice()
		case v.armed == "c":
			v.quit = true
		default:
			v.armed, v.armedUntil = "c", v.now().Add(agentPair)
			v.say("Ctrl+C again leaves the view; the runs go on.")
		}
		return
	}
	if v.armed == "c" {
		v.armed = ""
	}
	switch {
	case k.IsKey(term.KeyUp, 0):
		v.move(-1)
	case k.IsKey(term.KeyDown, 0):
		v.move(1)
	case k.IsKey(term.KeyPageUp, 0):
		v.move(-page)
	case k.IsKey(term.KeyPageDown, 0):
		v.move(page)
	case k.IsKey(term.KeyHome, 0):
		v.move(-1 << 30)
	case k.IsKey(term.KeyEnd, 0):
		v.move(1 << 30)
	case k.IsKey(term.KeyDown, term.ModAlt), k.IsKey(term.KeyUp, term.ModAlt):
		v.jumpGroup(k.IsKey(term.KeyDown, term.ModAlt))
	case k.Is('f', term.ModCtrl):
		// Find by name: Claude Code's Ctrl+F is its n: filter.
		if !strings.HasPrefix(v.box.Text(), "n:") {
			v.box.SetText("n:")
			v.box.SetCursor(2)
			v.keepChoice()
		}
	case k.IsKey(term.KeyEscape, 0):
		switch {
		case v.help:
			v.help = false
		case v.peek:
			v.peek = false
		case !empty:
			v.box.Reset()
			v.keepChoice()
		default:
			v.quit = true
		}
	case k.Is('l', term.ModCtrl):
		v.clear = true
	case k.Is('z', term.ModCtrl):
		v.suspend = true
	case k.Is('x', term.ModCtrl):
		v.stopOrForget()
	case k.Is('r', term.ModCtrl):
		// Rename the run chosen: its name goes in the box, after /rename.
		if r, ok := v.chosen(); ok {
			v.box.SetText("/rename " + oneLine(r.Name))
			v.box.SetCursor(len(v.box.Text()))
			v.say("Enter names %s so; an empty name takes its name away.", r.Short)
		}
	case k.Is('s', term.ModCtrl):
		v.byFolder = !v.byFolder
		v.sortRuns()
		v.keepChoice()
		if v.byFolder {
			v.say("The runs are grouped by folder; a prompt starts a run in the folder of the one chosen. Ctrl+S groups them by how they stand.")
		} else {
			v.say("The runs are grouped by how they stand. Ctrl+S groups them by folder.")
		}
	case empty && v.answering() && v.settled() && k.Code == term.KeyRune && k.Rune >= '1' && k.Rune <= '9' && k.Mod&^term.ModShift == 0:
		r, _ := v.chosen()
		if i := int(k.Rune - '1'); i < len(r.Question.Choices) {
			v.answer(r, r.Question.Choices[i], "")
		}
	case !empty && v.replying() && k.IsKey(term.KeyEnter, 0):
		// A reply to the run peeked at.
		if r, ok := v.chosen(); ok {
			v.reply(r, strings.TrimSpace(v.box.Text()))
		}
	case !empty && v.answering() && !v.viewWords() && k.IsKey(term.KeyEnter, 0):
		// Words for the question: no, with them; or, typed before the
		// question was seen, words for the run, which it reads once it
		// goes on.
		r, _ := v.chosen()
		if v.wordsFor(r) {
			v.answerWords(r, strings.TrimSpace(v.box.Text()))
		} else {
			v.reply(r, strings.TrimSpace(v.box.Text()))
		}
	case empty && typedRune(k, ' '):
		v.peek, v.help = !v.peek, false
	case empty && typedRune(k, '?'):
		v.help = !v.help
	case k.IsKey(term.KeyEnter, 0), empty && k.IsKey(term.KeyRight, 0):
		v.enter()
	default:
		if v.box.Handle(k).Changed {
			// What was said of the last key gives way to the keys of
			// what is being typed.
			v.said = ""
		}
		v.keepChoice()
	}
}

// enter is Enter: start a run on what is in the box, or with the box empty
// (or a filter in it), watch or open the run chosen.
func (v *agentView) enter() {
	prompt := strings.TrimSpace(v.box.Text())
	if _, isFilter := v.boxFilter(); prompt != "" && !isFilter {
		v.dispatch(prompt)
		return
	}
	r, ok := v.chosen()
	if !ok {
		if prompt == "" {
			v.say("There is no run to open: type a prompt and Enter to start one.")
		}
		return
	}
	switch {
	case r.Question != nil && !v.peek:
		// What it asks is shown, to be answered; Enter again watches the
		// conversation, where a long question is whole.
		v.peek, v.help = true, false
		return
	case running(r):
		v.watch = &watching{id: r.ID, end: true}
		return
	}
	v.open = r.ID
}

// replying reports whether what is in the box is words for the run peeked
// at: not a filter or a command of the view's (/model ... ), which keep
// their meaning while peeking.
func (v *agentView) replying() bool {
	return v.peek && !v.help && !v.answering() && !v.viewWords()
}

// viewWords reports whether what is in the box is the view's own: a
// command (/model ... ) or a filter, which keep their meaning whatever is
// peeked at or asked.
func (v *agentView) viewWords() bool {
	if strings.HasPrefix(strings.TrimSpace(v.box.Text()), "/") {
		return true
	}
	_, isFilter := v.boxFilter()
	return isFilter
}

// answering reports whether the question of the run chosen is on the
// screen to be answered: peeking at a run that waits on one.
func (v *agentView) answering() bool {
	r, ok := v.chosen()
	return ok && v.peek && !v.help && r.Question != nil && len(r.Question.Choices) > 0
}

// answer answers the question run r waits on with choice c, and words
// that go with it.
func (v *agentView) answer(r app.AgentRun, c app.QuestionChoice, text string) {
	if err := v.src.Answer(r.ID, r.Question.ID, c.Key, text); err != nil {
		v.say("%s was not answered: %v.", runName(r), err)
		v.refresh()
		return
	}
	said := oneLine(c.Label)
	if text != "" {
		said = "No, with your words"
	}
	v.peek = false
	v.refresh()
	v.say("Answered %s: %s.", runName(r), strings.TrimSuffix(said, "."))
}

// answerWords answers the question run r waits on no, with the person's
// words, which the model reads and carries on with.
func (v *agentView) answerWords(r app.AgentRun, text string) {
	for _, c := range r.Question.Choices {
		if c.Key == "no" {
			v.answer(r, c, text)
			v.box.Reset()
			return
		}
	}
	v.say("This question takes no words: a number answers it.")
}

// reply sends words to the run r peeked at: one that is over goes on with
// them in the background (gnosh --bg -r); a working run takes none yet.
func (v *agentView) reply(r app.AgentRun, text string) {
	name := runName(r)
	if running(r) {
		if err := v.src.Tell(r.ID, text); err != nil {
			v.say("%s was not told: %v.", name, err)
			return
		}
		v.box.Reset()
		v.told[r.ID] = true
		v.say("Sent to %s: it reads your words between its steps.", name)
		return
	}
	v.box.Reset()
	v.resume(r, text, "goes on in the background: Enter watches it")
}

// resume sends run r, which is over, back to work on text, away from the
// loop, and says what came of it.
func (v *agentView) resume(r app.AgentRun, text, done string) {
	name := runName(r)
	v.starting++
	v.say("Sending %s back to work …", name)
	src, flags := v.src, slices.Clone(v.flags)
	v.async(func() func(*agentView) {
		ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
		defer cancel()
		err := src.Resume(ctx, r.ID, text, flags)
		return func(v *agentView) {
			v.starting--
			if err != nil {
				v.say("%s did not go on (%v); your words were: %s", name, err, oneLine(text))
				return
			}
			v.refresh()
			v.say("%s %s.", name, done)
		}
	})
}

// dispatch starts a run on the prompt: or, for /model and /effort, sets
// what the runs started from here are given, and /exit leaves. "@name
// prompt" starts it as the kind of agent named (--agent), or else in the
// folder named (a git repository one level below the view's folder, or a
// folder runs work in), as Claude Code's @<agent> and @<repo> do.
func (v *agentView) dispatch(prompt string) {
	if v.boxCommand(prompt) {
		return
	}
	flags := slices.Clone(v.flags)
	dir, where := v.dispatchFolder()
	if name, rest, ok := strings.Cut(prompt, " "); ok && strings.HasPrefix(name, "@") && len(name) > 1 {
		if folder, found := v.namedFolder(name[1:]); found {
			dir, where = folder, showPath("", v.info.Home, folder)
		} else {
			flags = append(slices.DeleteFunc(flags, func(f string) bool { return strings.HasPrefix(f, "--agent=") }), "--agent="+name[1:])
		}
		prompt = strings.TrimSpace(rest)
	}
	if len([]rune(prompt)) < minDispatch {
		v.say("Too short: a prompt of %d characters or more starts a run.", minDispatch)
		return
	}
	v.box.Reset()
	v.starting++
	v.say("Starting a run in %s …", where)
	src := v.src
	v.async(func() func(*agentView) {
		ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
		defer cancel()
		id, err := src.Dispatch(ctx, dir, prompt, flags)
		return func(v *agentView) {
			v.starting--
			if err != nil {
				v.say("The run was not started: %v.", err)
				return
			}
			v.refresh()
			v.sel = id
			v.keepChoice()
			v.say("Started %s in %s: Enter watches it.", app.RunShort(id), where)
		}
	})
}

// boxCommand does a command typed in the box, and reports whether it was
// one: /model and /effort set what the runs started from here are given
// (default puts back what gnosh agents was given), and /exit and /quit
// leave, as in Claude Code's view.
func (v *agentView) boxCommand(text string) bool {
	cmd, arg, _ := strings.Cut(strings.TrimSpace(text), " ")
	arg = strings.TrimSpace(arg)
	flag := ""
	switch cmd {
	case "/exit", "/quit":
		v.quit = true
		return true
	case "/stop":
		// The run chosen stops at once, as Ctrl+X stops it; Claude Code's
		// reply of exactly /stop.
		v.box.Reset()
		r, ok := v.chosen()
		switch {
		case !ok:
			v.say("There is no run to stop.")
		case !running(r) || v.stopping[r.ID]:
			v.say("%s is not running.", runName(r))
		default:
			v.stop(r)
		}
		return true
	case "/rename":
		v.box.Reset()
		r, ok := v.chosen()
		if !ok {
			v.say("There is no run to name.")
			return true
		}
		if err := v.src.Rename(r.ID, arg); err != nil {
			v.say("%s was not named: %v.", r.Short, err)
			return true
		}
		v.refresh()
		if arg == "" {
			v.say("%s has no name now.", r.Short)
		} else {
			v.say("%s is named %s.", r.Short, oneLine(arg))
		}
		return true
	case "/model":
		flag = "--model"
	case "/effort":
		flag = "--effort"
	default:
		return false
	}
	v.box.Reset()
	now := ""
	for _, f := range v.flags {
		if v, ok := strings.CutPrefix(f, flag+"="); ok {
			now = v
		}
	}
	switch {
	case arg == "" && now == "":
		v.say("New runs from here take the %s gnosh would. %s <name> changes that; default puts it back.", strings.TrimPrefix(flag, "--"), cmd)
	case arg == "":
		v.say("New runs from here start with %s=%s; %s default puts back what gnosh would take.", flag, now, cmd)
	case arg == "default":
		v.flags = slices.DeleteFunc(v.flags, func(f string) bool { return strings.HasPrefix(f, flag+"=") })
		for _, f := range v.info.Flags {
			if strings.HasPrefix(f, flag+"=") {
				v.flags = append(v.flags, f)
			}
		}
		v.say("New runs from here take the %s they took as the view opened.", strings.TrimPrefix(flag, "--"))
	default:
		v.flags = append(slices.DeleteFunc(v.flags, func(f string) bool { return strings.HasPrefix(f, flag+"=") }), flag+"="+arg)
		v.say("New runs from here start with %s=%s (for this view only).", flag, arg)
	}
	return true
}

// title is the terminal's title while the view is open: how many runs
// wait on the person, as Claude Code's tab title says.
func (v *agentView) title() string {
	n := v.tally()[0]
	if n == 0 {
		return "gnosh agents"
	}
	return fmt.Sprintf("%d awaiting input · gnosh agents", n)
}

// dispatchFolder is where a run started from the view works: the view's
// folder, or grouped by folder the chosen run's, as Claude Code's does;
// and the same the short way. "" is the view's own.
func (v *agentView) dispatchFolder() (dir, shown string) {
	if r, ok := v.chosen(); ok && v.byFolder && r.Cwd != "" {
		return r.Cwd, runFolder(r)
	}
	return "", v.folder()
}

// namedFolder is the folder "@name" names when no kind of agent has that
// name (Claude Code's agent wins over a same-named repo): a git
// repository one level below the view's folder, or else the one folder
// listed runs work in whose last part is name.
func (v *agentView) namedFolder(name string) (string, bool) {
	if slices.Contains(v.info.Kinds, name) {
		return "", false
	}
	for _, repo := range v.info.Repos {
		if filepath.Base(repo) == name {
			return repo, true
		}
	}
	found := ""
	for _, r := range v.runs {
		if r.Cwd == "" || filepath.Base(r.Cwd) != name || r.Cwd == found {
			continue
		}
		if found != "" {
			return "", false // (two such folders: not one named)
		}
		found = r.Cwd
	}
	return found, found != ""
}

// folder is where a run started from the view works, the short way.
func (v *agentView) folder() string {
	return showPath("", v.info.Home, v.info.Cwd)
}

// stopOrForget is Ctrl+X: a working run is asked to stop; one that is
// over is taken off the list at the second press.
func (v *agentView) stopOrForget() {
	r, ok := v.chosen()
	if !ok {
		return
	}
	name := runName(r)
	if running(r) {
		if v.stopping[r.ID] {
			v.say("%s was asked to stop, and ends what it was doing first.", name)
			return
		}
		v.stop(r)
		return
	}
	if v.armed == "x "+r.ID {
		v.armed = ""
		if err := v.src.Forget(r.ID); err != nil {
			v.say("%s was not taken off the list: %v.", name, err)
			return
		}
		v.refresh()
		v.say("%s is off the list. Its session is kept: gnosh -r %s opens it.", name, r.Short)
		return
	}
	v.armed, v.armedUntil = "x "+r.ID, v.now().Add(agentPair)
	v.say("Ctrl+X again takes %s off the list; its session is kept.", name)
}

// stop asks a working run to stop, away from the loop.
func (v *agentView) stop(r app.AgentRun) {
	v.stopping[r.ID] = true
	name := runName(r)
	v.say("%s was asked to stop; it ends what it was doing first.", name)
	src := v.src
	v.async(func() func(*agentView) {
		said, err := src.Stop(context.Background(), r.ID)
		return func(v *agentView) {
			v.refresh()
			if err != nil {
				delete(v.stopping, r.ID)
				v.say("%s: %v.", name, err)
				return
			}
			said = strings.ReplaceAll(said, r.ID, r.Short)
			v.say("%s", strings.TrimSuffix(strings.Replace(said, "the session "+r.Short, name, 1), "."))
		}
	})
}

// layWatch lays out the conversation of the run watched again, when it
// has more in it or force says to.
func (v *agentView) layWatch(force bool) {
	w := v.watch
	w.looked = v.now()
	entries := v.src.Conversation(w.id)
	if !force && len(entries) == w.shown {
		return
	}
	w.shown = len(entries)
	width := max(w.width, 10)
	w.lines, _ = layTranscript(&v.th, v.m, entries, width, false)
	if len(w.lines) == 0 {
		w.lines = []string{v.th.Quiet.Render(fit(v.m, "(nothing in its conversation yet)", width))}
	}
}

// watchInput takes in what the terminal reported while a run is watched.
func (v *agentView) watchInput(ev term.Event) {
	w := v.watch
	empty := strings.TrimSpace(v.box.Text()) == ""
	switch ev := ev.(type) {
	case term.Paste:
		v.box.Handle(ev)
	case term.Mouse:
		switch ev.Action {
		case term.MouseWheelUp:
			w.rows += 3
		case term.MouseWheelDown:
			w.rows -= 3
		}
	case term.Key:
		k := ev
		switch {
		case !empty && (k.IsKey(term.KeyEscape, 0) || k.Is('c', term.ModCtrl)):
			v.box.Reset()
		case k.IsKey(term.KeyEscape, 0), empty && k.IsKey(term.KeyLeft, 0), k.Is('c', term.ModCtrl):
			v.watch = nil
		case !empty && k.IsKey(term.KeyEnter, 0):
			// Words for the run watched: a working one reads them between
			// its steps, and one that is over goes on with them, as
			// Claude Code's attached session takes a prompt; one that
			// waits on a question is answered no, with them.
			if r, ok := v.byID(w.id); ok && r.Question != nil && v.wordsFor(r) {
				v.answerWords(r, strings.TrimSpace(v.box.Text()))
			} else if ok {
				v.reply(r, strings.TrimSpace(v.box.Text()))
			}
		case empty && k.Code == term.KeyRune && k.Rune >= '1' && k.Rune <= '9' && k.Mod&^term.ModShift == 0 && v.watchedAsks():
			// The question of the run watched, answered by its number.
			r, _ := v.byID(w.id)
			if i := int(k.Rune - '1'); i < len(r.Question.Choices) {
				v.answer(r, r.Question.Choices[i], "")
			}
		case k.IsKey(term.KeyUp, 0):
			w.rows++
		case k.IsKey(term.KeyDown, 0):
			w.rows--
		case k.IsKey(term.KeyPageUp, 0):
			w.pages++
		case k.IsKey(term.KeyPageDown, 0):
			w.pages--
		case k.IsKey(term.KeyHome, 0):
			w.start, w.end = true, false
		case k.IsKey(term.KeyEnd, 0):
			w.start, w.end = false, true
		case k.Is('x', term.ModCtrl):
			if r, ok := v.byID(w.id); ok && running(r) && !v.stopping[r.ID] {
				v.stop(r)
			}
		case k.IsKey(term.KeyEnter, 0):
			if r, ok := v.byID(w.id); ok && !running(r) {
				v.watch, v.open = nil, r.ID
			}
		case k.Is('l', term.ModCtrl):
			v.clear = true
		case k.Is('z', term.ModCtrl):
			v.suspend = true
		default:
			if v.box.Handle(k).Changed {
				v.said = ""
			}
		}
	}
}

// watchedAsks reports whether the run watched waits on a question with
// answers to choose from that takes an answer now (settledFor).
func (v *agentView) watchedAsks() bool {
	r, ok := v.byID(v.watch.id)
	return ok && r.Question != nil && len(r.Question.Choices) > 0 && v.settledFor(r)
}

// settled reports whether the question of the run chosen takes an answer
// now, and settledFor that of run r: it has been on the screen guardShown,
// and the key in hand came guardPause after the one before it. A digit or
// words typed before then are typing (a review: "2 tests still fail…"
// typed for a run that just came to a question answered it 2).
func (v *agentView) settled() bool {
	r, ok := v.chosen()
	return ok && v.settledFor(r)
}

func (v *agentView) settledFor(r app.AgentRun) bool {
	if r.Question == nil {
		return false
	}
	at, ok := v.askedAt[r.Question.ID]
	return ok && v.now().Sub(at) >= guardShown && v.keyGap >= guardPause
}

// wordsFor reports whether the words in the box answer the question of run
// r: they began to be typed once it had been on the screen guardShown.
func (v *agentView) wordsFor(r app.AgentRun) bool {
	if r.Question == nil {
		return false
	}
	at, ok := v.askedAt[r.Question.ID]
	return ok && !v.typedAt.Before(at.Add(guardShown))
}

// listPlaceholder and watchPlaceholder are what the box says while it is
// empty, under the list and under a run watched.
const (
	listPlaceholder  = "a prompt starts a run in the background · n: s: o: filter the list"
	watchPlaceholder = "words for this run: it reads them between its steps"
)

// byID is the run of the session id, as last listed.
func (v *agentView) byID(id string) (app.AgentRun, bool) {
	for _, r := range v.runs {
		if r.ID == id {
			return r, true
		}
	}
	return app.AgentRun{}, false
}

// age is how long a run has run: until now, or until it was over.
func (v *agentView) age(r app.AgentRun) string {
	end := v.now()
	if !running(r) && !r.Ended.IsZero() {
		end = r.Ended
	}
	return shortAge(end.Sub(r.Started))
}

// shortAge is a time in the fewest characters: "12s", "4m", "3h", "2d".
func shortAge(d time.Duration) string {
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%ds", max(int(d.Seconds()), 0))
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	case d < 48*time.Hour:
		return fmt.Sprintf("%dh", int(d.Hours()))
	}
	return fmt.Sprintf("%dd", int(d.Hours()/24))
}

// statusOf is a run's line in the list: what it is doing, or how it ended.
func (v *agentView) statusOf(r app.AgentRun) string {
	last := ""
	if len(r.Output) > 0 {
		last = oneLine(r.Output[len(r.Output)-1])
	}
	switch {
	case running(r) && v.stopping[r.ID]:
		return "stopping …"
	case r.Question != nil:
		return strings.TrimSpace(oneLine(r.Question.Title) + " " + firstOf(r.Question.Text))
	case running(r):
		return firstNonEmpty(v.status[r.ID], "starting")
	case r.State == "stopped" || r.State == "failed":
		return strings.TrimSuffix(r.State+v.th.Sep+firstNonEmpty(last, oneLine(r.Ending)), v.th.Sep)
	}
	return firstNonEmpty(last, "(it printed nothing)")
}

// render lays the view out for a screen cols by rows: the rows from the
// top (body, exactly as many as there is room for above live), and the
// rows at the bottom, with the cursor's place among them.
func (v *agentView) render(cols, rows int) (body, live []string, crow, ccol int) {
	if v.watch != nil {
		return v.renderWatch(cols)
	}
	th := &v.th
	width := max(cols, 10)
	v.box.SetPlaceholder(listPlaceholder)
	view := v.box.Render(width)
	live = append(live, view.Lines...)
	crow, ccol = view.CursorRow, view.CursorCol
	live = append(live, fit(v.m, v.hintLine(), width))
	room := max(rows-len(live), 1)

	shown := v.shown()
	n := v.tally()
	counts := count(n[1], "working", "working") + th.Sep + count(n[2], "completed", "completed")
	if n[0] > 0 {
		counts = count(n[0], "needs input", "need input") + th.Sep + counts
	}
	title := th.Wordmark.Render("gnosh agents") + th.Quiet.Render(th.Sep+counts)
	if v.info.Scoped {
		title += th.Quiet.Render(th.Sep + "in " + v.folder())
	}
	if v.starting > 0 {
		title += th.Quiet.Render(th.Sep + count(v.starting, "starting", "starting"))
	}
	body = append(body, fit(v.m, title, width))

	// What goes under the list: peeking, or the keys.
	var under []string
	switch {
	case v.help:
		under = v.helpLines(width)
	case v.peek:
		if r, ok := v.chosen(); ok {
			under = v.peekLines(r, width)
		}
	}
	if over := len(under) + len(body) + 1 - room; over > 0 && len(under) > 3 {
		// What peeking shows is too tall: the lines after its head go
		// first, so that a question's answers stay in sight.
		cut := min(over+1, len(under)-3)
		marker := th.Quiet.Render(fit(v.m, fmt.Sprintf("  … %s more (Enter watches the conversation, where it is whole)", count(cut, "line", "lines")), width))
		under = append([]string{under[0], marker}, under[1+cut:]...)
	}
	listRoom := max(room-len(body)-len(under), 1)

	var list []string
	selRow := -1
	if len(shown) == 0 {
		said := "No session runs in the background: type a prompt below and Enter to start one."
		switch _, isFilter := v.boxFilter(); {
		case isFilter:
			said = "No run is like that."
		case v.info.Scoped:
			said = "No session runs in the background from " + v.folder() + ": type a prompt below and Enter to start one."
		}
		list = append(list, wrap(v.m, th.Quiet.Render(said), width, "", "")...)
	}
	nameW := 0
	for _, r := range shown {
		nameW = max(nameW, v.m.Width(rowName(r)))
	}
	nameW = min(nameW, 24)
	group := ""
	for _, r := range shown {
		if g := v.groupOf(r); g != group {
			group = g
			list = append(list, th.Quiet.Render(fit(v.m, g, width)))
		}
		if r.ID == v.sel {
			selRow = len(list)
		}
		list = append(list, v.runRow(r, r.ID == v.sel, nameW, width))
	}
	// The rows shown keep the chosen run in sight.
	if len(list) > listRoom {
		if selRow >= 0 {
			if selRow < v.top {
				v.top = max(selRow-1, 0)
			}
			if selRow >= v.top+listRoom {
				v.top = selRow - listRoom + 1
			}
		}
		v.top = min(max(v.top, 0), len(list)-listRoom)
		list = list[v.top : v.top+listRoom]
	} else {
		v.top = 0
	}
	body = append(body, list...)
	body = append(body, under...)
	if len(body) > room {
		body = body[:room]
	}
	for len(body) < room {
		body = append(body, "")
	}
	return body, live, crow, ccol
}

// runRow is a run's row in the list: the pointer when it is the one
// chosen, its mark, its name, what it is doing or answered, and its age at
// the right.
func (v *agentView) runRow(r app.AgentRun, chosen bool, nameW, width int) string {
	th := &v.th
	pointer := strings.Repeat(" ", v.m.Width(th.Menu.Pointer)+1)
	if chosen {
		pointer = th.Menu.PointerStyle.Render(th.Menu.Pointer) + " "
	}
	mark := th.Quiet.Render("∙")
	switch {
	case r.State == "blocked":
		mark = th.Ask.Render(th.AskMark)
	case running(r):
		mark = th.Busy.Render("✽")
	case r.State == "failed":
		mark = th.Fail.Render(th.FailMark)
	}
	name := rowName(r)
	if w := v.m.Width(name); w > nameW {
		name = v.m.Truncate(name, nameW, "…")
	}
	name += strings.Repeat(" ", max(nameW-v.m.Width(name), 0))
	if chosen {
		name = th.Menu.Selected.Render(name)
	}
	age := v.age(r)
	lead := pointer + mark + " " + name + "  "
	room := width - v.m.VisibleWidth(lead) - v.m.Width(age) - 2
	status := v.statusOf(r)
	if v.byFolder && r.State != "stopped" && r.State != "failed" {
		// (The group no longer says how it stands.)
		word := map[string]string{"blocked": "needs input", "working": "working", "done": "done"}[r.State]
		status = strings.TrimSuffix(word+th.Sep+status, th.Sep)
	}
	if room < 8 {
		return fit(v.m, lead+status, width)
	}
	if v.m.Width(status) > room {
		status = v.m.Truncate(status, room, "…")
	}
	gap := strings.Repeat(" ", max(width-v.m.VisibleWidth(lead)-v.m.Width(status)-v.m.Width(age), 1))
	return lead + status + gap + th.Quiet.Render(age)
}

// peekLines are what peeking shows of a run: its prompt, then what it did
// last or what it answered.
func (v *agentView) peekLines(r app.AgentRun, width int) []string {
	th := &v.th
	state := r.State + " " + v.age(r)
	switch {
	case running(r) && v.stopping[r.ID]:
		state = "stopping " + v.age(r)
	case r.Question != nil:
		state = "needs input" + th.Sep + "waiting " + shortAge(v.now().Sub(r.Question.Since))
	}
	head := "─ " + runName(r) + th.Sep + state + th.Sep + firstNonEmpty(r.Folder, "(folder not known)") + " ─"
	out := []string{th.Quiet.Render(fit(v.m, head, width))}
	if p := strings.TrimSpace(r.Prompt); p != "" {
		rows := wrap(v.m, oneLine(p), width, th.Prompt.Render(th.PromptMark), strings.Repeat(" ", v.m.Width(th.PromptMark)))
		if len(rows) > 2 {
			rows = append(rows[:1], fit(v.m, rows[1], width-1)+"…")
		}
		out = append(out, rows...)
	}
	if e := r.Ending; e != "" && !running(r) {
		// How it failed, or that a signal stopped it.
		mark := th.Quiet.Render("∙")
		if r.State == "failed" {
			mark = th.Fail.Render(th.FailMark)
		}
		out = append(out, wrap(v.m, mark+" "+oneLine(e), width, "", "  ")...)
	}
	if q := r.Question; q != nil {
		return append(out, v.questionLines(q, width, 2*peekRows)...)
	}
	var lines []string
	none := "(it printed nothing)"
	if running(r) {
		lines, none = v.doing[r.ID], "(nothing in its conversation yet)"
	} else {
		lines = r.Output
		if len(lines) > peekRows {
			lines = lines[len(lines)-peekRows:]
		}
	}
	for _, l := range lines {
		out = append(out, fit(v.m, "  "+safeLine(l), width))
	}
	if len(lines) == 0 {
		// (gnosh's own words, styled: not made safe as what a run said
		// is, which would show their colours' codes.)
		out = append(out, fit(v.m, "  "+th.Quiet.Render(none), width))
	}
	return out
}

// questionLines are what a run asks, and the answers, numbered for the
// keys. Its lines wrap, since why gnosh asks is read whole, up to most
// rows of them.
func (v *agentView) questionLines(q *app.AgentQuestion, width, most int) []string {
	th := &v.th
	out := []string{fit(v.m, th.Ask.Render(th.AskMark)+" "+oneLine(q.Title), width)}
	var text []string
	for _, l := range safeLines(q.Text) {
		lead := "  " + strings.Repeat(" ", len(l)-len(strings.TrimLeft(l, " ")))
		text = append(text, wrap(v.m, strings.TrimLeft(l, " "), width, lead, lead)...)
	}
	if len(text) > most {
		text = append(text[:most:most], th.Quiet.Render(fit(v.m, fmt.Sprintf("  … %s", count(len(text)-most, "more row", "more rows")), width)))
	}
	out = append(out, text...)
	for i, c := range q.Choices {
		out = append(out, fit(v.m, fmt.Sprintf("  %d. %s", i+1, oneLine(c.Label)), width))
	}
	return out
}

// helpLines are the keys of the list.
func (v *agentView) helpLines(width int) []string {
	keys := [][2]string{
		{"↑ ↓ PgUp PgDn Home End", "choose a run"},
		{"Alt+↑ Alt+↓", "the first run of the group before, or the next"},
		{"Ctrl+F", "find runs by name (n:)"},
		{"Space", "peek at it, or stop peeking"},
		{"Enter, →", "watch a working run as it goes; open one that is over (gnosh -r)"},
		{"a prompt and Enter", "start a run on it in " + v.folder() + "; @name first runs it as that kind of agent, or in that folder"},
		{"/model, /effort", "what the runs started from here are given (default puts it back); /exit leaves"},
		{"/stop", "stop the run chosen, as Ctrl+X does"},
		{"n:<text> s:<state> o:<text>", "list only the runs whose name or prompt, state, or answer has it"},
		{"Ctrl+X", "stop a working run; twice, take one that is over off the list"},
		{"Ctrl+S", "group the runs by folder, or by how they stand"},
		{"Ctrl+R, /rename <name>", "name the run chosen"},
		{"Esc", "stop peeking, empty the box, or leave; the runs go on"},
	}
	kw := 0
	for _, k := range keys {
		kw = max(kw, v.m.Width(k[0]))
	}
	out := []string{v.th.Quiet.Render(fit(v.m, "─ keys ─", width))}
	for _, k := range keys {
		out = append(out, fit(v.m, "  "+k[0]+strings.Repeat(" ", kw-v.m.Width(k[0]))+"  "+v.th.Quiet.Render(k[1]), width))
	}
	return out
}

// hintLine is the last row: what came of a key, or the keys that matter
// now.
func (v *agentView) hintLine() string {
	th := &v.th
	if v.said != "" {
		return th.Hint.Render(oneLine(v.said))
	}
	text := strings.TrimSpace(v.box.Text())
	_, isFilter := v.boxFilter()
	var parts []string
	switch {
	case v.answering() && text != "" && !v.viewWords():
		parts = []string{"Enter answers no, with your words", "Esc empties the box"}
	case isFilter:
		parts = []string{"Enter opens the run chosen", "Esc empties the box"}
	case text != "" && v.replying():
		r, _ := v.chosen()
		send := "Enter sends " + runName(r) + " back to work on it"
		if running(r) {
			send = "Enter sends your words to " + runName(r)
		}
		parts = []string{send, "Esc empties the box"}
	case text != "":
		_, where := v.dispatchFolder()
		start := "Enter starts a run in " + where
		if len(v.flags) > 0 {
			start += " with " + strings.Join(v.flags, " ")
		}
		parts = []string{start, "Esc empties the box"}
	case v.answering():
		r, _ := v.chosen()
		parts = []string{fmt.Sprintf("1-%d answers", len(r.Question.Choices)), "words and Enter: no, with them", "Enter watches", "Esc closes"}
	default:
		parts = []string{"↑↓ choose", "Space peeks"}
		r, ok := v.chosen()
		switch {
		case ok && r.Question != nil:
			parts = append(parts, "Enter answers", "Ctrl+X stops")
		case ok && running(r):
			parts = append(parts, "Enter watches", "Ctrl+X stops")
		case ok:
			parts = append(parts, "Enter opens", "Ctrl+X twice takes it off")
		}
		parts = append(parts, "? keys", "Esc leaves")
	}
	return th.Quiet.Render(strings.Join(parts, th.Sep))
}

// renderWatch lays out a run that is watched: its conversation, and a row
// that says whose it is and how it stands.
func (v *agentView) renderWatch(cols int) (body, live []string, crow, ccol int) {
	w := v.watch
	th := &v.th
	if w.width != cols {
		w.width = cols
		v.layWatch(true)
	}
	parts := []string{"watching"}
	r, ok := v.byID(w.id)
	if ok {
		state := r.State
		if running(r) && v.stopping[r.ID] {
			state = "stopping"
		}
		parts = []string{"watching " + runName(r), state + " " + v.age(r)}
		switch {
		case r.Question != nil:
			// What it asks stands above the box, as Claude Code's
			// attached session shows its dialog.
			live = v.questionLines(r.Question, max(cols, 10), watchQuestionRows)
			if n := len(r.Question.Choices); n > 0 {
				parts = append(parts, fmt.Sprintf("1-%d answers", n), "words and Enter: no, with them")
			}
		case running(r):
			parts = append(parts, "Ctrl+X stops it")
		default:
			parts = append(parts, "Enter opens it")
		}
	}
	parts = append(parts, "Esc goes back")
	if v.said != "" {
		parts = []string{oneLine(v.said)}
	}
	v.box.SetPlaceholder(watchPlaceholder)
	view := v.box.Render(max(cols, 10))
	crow, ccol = len(live)+view.CursorRow, view.CursorCol
	live = append(append(live, view.Lines...), fit(v.m, th.Quiet.Render(strings.Join(parts, th.Sep)), cols))
	return w.lines, live, crow, ccol
}

// watchQuestionRows is how many rows of a question's text the watch screen
// shows; Esc and Space show it whole in the peek.
const watchQuestionRows = 4

// runAgentView runs the agent view until the person leaves it, and returns
// the exit code.
func runAgentView(o Options, info app.AgentViewInfo) (code int) {
	env := o.Getenv
	if env == nil {
		env = func(string) string { return "" }
	}
	now := o.Now
	if now == nil {
		now = time.Now
	}
	u := &UI{o: o, env: env}
	profile := u.profile()
	th := DefaultTheme(profile)
	if o.Theme != nil {
		th = *o.Theme
	}
	var m text.Metrics
	sigs := make(chan os.Signal, 1)
	hooks := TerminalHooks{OnTerminate: func(sig os.Signal) {
		select {
		case sigs <- sig:
		default:
		}
	}}
	var t Terminal
	var err error
	if o.Open != nil {
		t, err = o.Open(hooks)
	} else {
		t, err = openTerminal(o.In, o.Out, hooks)
	}
	if err != nil {
		fmt.Fprintf(os.Stderr, "gnosh: the agent view could not take the terminal: %v\n", err)
		return 1
	}
	defer t.Close()
	defer t.RestoreOnPanic()
	events := t.Events()
	caps, ahead, _ := term.Probe(context.Background(), t, events, 0)
	if caps.LightBackground && o.Theme == nil {
		th = NewTheme(profile, DefaultPalette(profile).OnLight(profile))
	}
	modes := term.Modes{BracketedPaste: caps.Answered, KittyKeyboard: caps.KittyKeyboard}
	_ = t.SetModes(modes)
	size := func() (int, int) {
		cols, rows, err := t.Size()
		if err != nil || cols < 1 || rows < 1 {
			return 80, 24
		}
		return cols, rows
	}
	cols, rows := size()
	p, err := term.OpenView(t, cols, rows, m.Width, caps.SynchronizedOutput)
	if err != nil {
		fmt.Fprintf(os.Stderr, "gnosh: the agent view could not take the screen: %v\n", err)
		return 1
	}
	defer func() { _, _ = p.Close() }()

	v := newAgentView(th, m, now, info)
	results := make(chan func(*agentView), 16)
	v.async = func(work func() func(*agentView)) {
		go func() {
			defer t.RestoreOnPanic()
			results <- work()
		}()
	}
	v.refresh()
	if info.Focus != "" {
		v.focusOn(info.Focus)
	}
	scr := &agentScreen{p: p}
	for _, ev := range ahead {
		v.input(ev)
	}
	// The title says how many runs wait on the person; the one the
	// terminal had comes back as the view closes.
	shownTitle := ""
	defer func() {
		if shownTitle != "" {
			_, _ = io.WriteString(t, term.TitlePop)
		}
	}()
	tell := func() {
		if title := v.title(); title != shownTitle {
			seq := term.SetTitle(title)
			if shownTitle == "" {
				seq = term.TitlePush + seq
			}
			_, _ = io.WriteString(t, seq)
			shownTitle = title
		}
		for _, note := range v.notes {
			if seq := noteSeq(info.Notify, note, env); seq != "" {
				_, _ = io.WriteString(t, seq)
			}
		}
		v.notes = v.notes[:0]
	}
	tick := time.NewTicker(250 * time.Millisecond)
	defer tick.Stop()
	var err2 error
	for !v.quit {
		if v.clear {
			v.clear = false
			_ = p.Clear()
		}
		_ = scr.draw(v)
		tell()
		select {
		case ev, ok := <-events:
			if !ok {
				return 0
			}
			switch ev := ev.(type) {
			case term.Resize:
				_ = p.Resize(ev.Cols, ev.Rows)
				scr.drawn = nil
			case term.Resume:
				_ = t.SetModes(modes)
				_ = p.Clear()
			default:
				v.input(ev)
			}
		case f := <-results:
			f(v)
		case <-tick.C:
			v.tick()
		case sig := <-sigs:
			if s, ok := sig.(syscall.Signal); ok {
				return 128 + int(s)
			}
			return 1
		}
		if v.suspend || v.open != "" {
			// The terminal goes to the shell (Ctrl+Z), or to gnosh -r on a
			// run that is over, and the view takes it back after.
			_, _ = p.Close()
			if v.suspend {
				v.suspend = false
				if err := t.Suspend(); err != nil {
					v.say("gnosh could not be suspended: %v.", err)
				}
			}
			if id := v.open; id != "" {
				v.open = ""
				_ = t.Release()
				if err := info.Source.Open(id); err != nil {
					v.say("gnosh -r %s: %v.", app.RunShort(id), err)
				}
				_ = t.Resume()
				v.refresh()
			}
			cols, rows = size()
			if p, err2 = term.OpenView(t, cols, rows, m.Width, caps.SynchronizedOutput); err2 != nil {
				return 1
			}
			scr = &agentScreen{p: p}
		}
	}
	return 0
}
