package app

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/gnosh-ai/gnosh/internal/agent"
	"github.com/gnosh-ai/gnosh/internal/core"
	"github.com/gnosh-ai/gnosh/internal/prompt"
	"github.com/gnosh-ai/gnosh/internal/tools/shelltool"
)

// This file is the pace check (the paceCheck option; Jae, 26 November
// 2026: "when a task/prompt/todo takes too long, every minute there should
// be a little frame that pops up, 'is this time efficient?' ... some
// things will require every 10 seconds... can it handle even every
// second? ... the idea is, should look briefly to see if it's the time
// efficient way to do something. of course you can't dwell on that either
// so i guess it should be its own lane? and when running bash scripts or
// whatever i guess, if it doesn't complete in 3 seconds is it worth
// thinking 'is this working?'").
//
// While a turn runs, a look is taken beside it: a short side request,
// shown the task, the time so far, the steps with how long each took and
// what the agent does now, asks whether this is the time-efficient way.
// The first look comes once the turn has run for the option's time; each
// look says when the next one comes, soon when something is urgent and
// late for a long, steady job, within paceMin and paceMax; a Bash command
// that has printed nothing for paceQuiet is looked at at once. The work
// never waits for a look: the look goes to the side requests' own place
// on the model server (sideExtraBody, a small model on another server, a
// server that is not local), and where there is none it is not taken at
// all, since it would push the conversation out of the server's cache.
// "ok" is shown to the person for a moment. A faster way, or work found
// stuck, is shown and told to the model with its next request, to weigh
// for a moment and no more; a command found stuck goes to the background,
// so that the model has the turn back at once.

// How the pace check looks.
const (
	paceMin    = time.Second      // the least time between two looks
	paceMax    = 10 * time.Minute // the most
	paceQuiet  = 3 * time.Second  // a Bash command silent this long is looked at
	paceSteps  = 30               // the newest steps a look is shown
	paceTokens = 400              // room for the answer, thinking included
	paceWait   = 2 * time.Minute  // how long a look may take
	paceBefore = 5                // the earlier looks a look is shown
)

// PaceEvent is what a pace check found, for the interface. Pace is "ok",
// "faster" or "stuck", Why what it saw, Elapsed how long the turn had run
// then, Next when it looks again, and Moved the command it found stuck and
// moved to the background, if it did.
type PaceEvent struct {
	Elapsed time.Duration
	Pace    string
	Why     string
	Next    time.Duration
	Moved   string
}

// paceState is what the pace check knows of the turn it watches. It is
// guarded by its lock.
type paceState struct {
	mu sync.Mutex
	// turn is the turn watched, "" when none runs; began is when it
	// began, and ask the prompt it works on.
	turn  string
	began time.Time
	ask   string
	// steps are its finished calls, the newest paceSteps of them, and
	// left how many earlier ones were let go.
	steps []prompt.PaceStep
	left  int
	// calls are the calls in flight, by id.
	calls map[string]*paceCall
	// next is when the next look is due, looking says one is under way,
	// and before is what the turn's earlier looks said.
	next    time.Time
	looking bool
	before  []string
	// placeless says a look was due where side requests have no place of
	// their own, and the person was told the check is off for that.
	placeless bool
}

// paceCall is a call in flight: its tool, what it was given, when it
// began and when it last printed, and whether a look was taken at its
// present silence.
type paceCall struct {
	name, what   string
	since, heard time.Time
	looked       bool
}

// paceEvery is the paceCheck option, in a session a person attends; zero
// for never.
func (l *live) paceEvery() time.Duration {
	if !l.interactive || l.cfg == nil {
		return 0
	}
	return time.Duration(max(l.cfg.Options.PaceCheck.V, 0)) * time.Second
}

// paceObserve takes in what the session reports, for the look's account of
// the turn. Sub-agents' events are their own.
func (l *live) paceObserve(ev any) {
	if l.paceEvery() <= 0 {
		return
	}
	p := &l.pace
	switch ev := ev.(type) {
	case *agent.TurnStartEvent:
		if ev.Agent != "" {
			return
		}
		p.mu.Lock()
		p.turn, p.began, p.ask = ev.Turn, ev.Time, ""
		p.steps, p.left, p.before = nil, 0, nil
		p.calls = map[string]*paceCall{}
		p.next = ev.Time.Add(l.paceEvery())
		p.mu.Unlock()
	case *agent.PromptEvent:
		if ev.Agent != "" {
			return
		}
		p.mu.Lock()
		if p.turn != "" && p.ask == "" {
			p.ask = ev.Prompt.Text
		}
		p.mu.Unlock()
	case *agent.ToolStartEvent:
		if ev.Agent != "" {
			return
		}
		p.mu.Lock()
		if p.turn != "" {
			p.calls[ev.CallID] = &paceCall{name: ev.Name, what: paceWhat(ev.Name, ev.Input), since: ev.Time, heard: ev.Time}
		}
		p.mu.Unlock()
	case *agent.ToolProgressEvent:
		if ev.Agent != "" {
			return
		}
		p.mu.Lock()
		if c := p.calls[ev.CallID]; c != nil && ev.Text != "" {
			c.heard, c.looked = ev.Time, false
		}
		p.mu.Unlock()
	case *agent.ToolEndEvent:
		if ev.Agent != "" {
			return
		}
		p.mu.Lock()
		if c := p.calls[ev.CallID]; c != nil {
			delete(p.calls, ev.CallID)
			p.steps = append(p.steps, prompt.PaceStep{At: c.since.Sub(p.began), Took: ev.Duration, What: c.what, Outcome: paceOutcome(ev)})
			if over := len(p.steps) - paceSteps; over > 0 {
				p.steps = append(p.steps[:0], p.steps[over:]...)
				p.left += over
			}
		}
		p.mu.Unlock()
	case *agent.TurnEndEvent:
		if ev.Agent != "" {
			return
		}
		p.mu.Lock()
		p.turn, p.calls = "", nil
		p.mu.Unlock()
	}
}

// paceLoop looks at the clock every second while the session lives, and
// takes a look when one is due.
func (l *live) paceLoop(ctx context.Context) {
	t := time.NewTicker(time.Second)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			l.paceTick()
		}
	}
}

// paceTick takes a look when one is due: the turn has run until the time
// set for the next look, or a Bash command has printed nothing for
// paceQuiet. It does not wait for the look.
func (l *live) paceTick() {
	if l.paceEvery() <= 0 {
		return
	}
	a := l.agentReady.Load()
	if a == nil {
		return
	}
	now := l.p.now()
	p := &l.pace
	p.mu.Lock()
	if p.turn == "" || p.looking {
		p.mu.Unlock()
		return
	}
	quiet := ""
	for id, c := range p.calls {
		if c.name == shelltool.BashName && !c.looked && now.Sub(c.since) >= paceQuiet && now.Sub(c.heard) >= paceQuiet {
			quiet = id
			break
		}
	}
	if quiet == "" && now.Before(p.next) {
		p.mu.Unlock()
		return
	}
	if !a.SidePlace() {
		// No place of its own: a look would take the conversation's place
		// in the model server's cache. Said once a session.
		p.next = now.Add(paceMax)
		told := p.placeless
		p.placeless = true
		p.mu.Unlock()
		if !told && l.cfg.Options.PaceCheck.Set {
			l.notice(agent.LevelInfo, "pace", "The pace check is off here: side requests have no place of their own on the model server, and a look would push the conversation out of its cache. A provider's sideExtraBody gives them one, such as {\"id_slot\": 1} for llama.cpp.")
		}
		return
	}
	if quiet != "" {
		p.calls[quiet].looked = true
	}
	in := l.paceInput(now, a)
	turn, elapsed := p.turn, now.Sub(p.began)
	moved := ""
	if quiet != "" {
		moved = p.calls[quiet].what
	}
	p.looking = true
	p.mu.Unlock()
	l.background(func(ctx context.Context) { l.paceLook(ctx, a, turn, elapsed, in, quiet, moved) })
}

// paceInput is the look's account of the turn as it stands at now. The
// lock is held.
func (l *live) paceInput(now time.Time, a *agent.Agent) prompt.PaceInput {
	p := &l.pace
	in := prompt.PaceInput{
		Ask:     p.ask,
		Elapsed: now.Sub(p.began),
		Steps:   append([]prompt.PaceStep(nil), p.steps...),
		LeftOut: p.left,
		Before:  append([]string(nil), p.before...),
	}
	in.Todo = l.statusFacts()["frame"]
	var longest *paceCall
	for _, c := range p.calls {
		if longest == nil || c.since.Before(longest.since) {
			longest = c
		}
	}
	switch st := a.State(); {
	case longest != nil:
		in.Now = fmt.Sprintf("running %s for %s", longest.what, prompt.PaceClock(now.Sub(longest.since)))
		if silent := now.Sub(longest.heard); silent >= paceQuiet {
			in.Now += fmt.Sprintf(", which has printed nothing for %s", prompt.PaceClock(silent))
		}
	case st.Phase == agent.PhaseReply || st.Phase == agent.PhaseRequest:
		in.Now = fmt.Sprintf("the model has been writing its reply for %s", prompt.PaceClock(now.Sub(st.Since)))
	case st.Phase == agent.PhaseQuestion:
		in.Now = fmt.Sprintf("waiting for the user to answer a question, for %s", prompt.PaceClock(now.Sub(st.Since)))
	case st.Waiting != "":
		in.Now = st.Waiting
	}
	return in
}

// paceLook takes one look, and acts on what it found. turn is the turn it
// looks at, elapsed how long that had run, and quiet the Bash call it was
// taken for, if one, whose command is moved.
func (l *live) paceLook(ctx context.Context, a *agent.Agent, turn string, elapsed time.Duration, in prompt.PaceInput, quiet, moved string) {
	req := l.texts.PaceCheck(in)
	zero := 0.0
	res, err := a.Side(ctx, agent.SideRequest{
		Purpose:     "pace",
		System:      req.System,
		Prompt:      req.Prompt,
		MaxTokens:   paceTokens,
		Temperature: &zero,
		Timeout:     paceWait,
		Beside:      true,
	})
	pace, why, next := "", "", l.paceEvery()
	if err == nil {
		pace, why, next = parsePace(res.Text, l.paceEvery())
	}
	p := &l.pace
	p.mu.Lock()
	p.looking = false
	if p.turn != turn {
		// The turn ended meanwhile: what the look found is of no use.
		p.mu.Unlock()
		return
	}
	p.next = l.p.now().Add(next)
	if pace == "" {
		p.mu.Unlock()
		return
	}
	p.before = append(p.before, prompt.PaceClock(elapsed)+" "+pace+": "+why)
	if over := len(p.before) - paceBefore; over > 0 {
		p.before = append(p.before[:0], p.before[over:]...)
	}
	_, running := p.calls[quiet]
	p.mu.Unlock()

	ev := &PaceEvent{Elapsed: elapsed, Pace: pace, Why: why, Next: next}
	if pace == "ok" {
		l.deliver(ev)
		return
	}
	note := prompt.PaceNoteInput{Elapsed: elapsed, Stuck: pace == "stuck", Why: why}
	if pace == "stuck" && quiet != "" && running && l.shell != nil && l.shell.BackgroundSoon(quiet) {
		// The command it found stuck goes to the background, as Ctrl+B
		// sends it, so that the model has the turn back now.
		ev.Moved, note.Command = moved, moved
	}
	l.deliver(ev)
	_ = a.Tell(agent.Note{Text: l.texts.PaceNote(note), Source: "pace"})
}

// parsePace reads a look's answer: {"pace": …, "why": …, "next": seconds}.
// What cannot be read is no finding, and the next look comes after every.
func parsePace(text string, every time.Duration) (pace, why string, next time.Duration) {
	next = every
	text = strings.TrimSpace(text)
	if i := strings.Index(text, "{"); i >= 0 {
		text = text[i:]
	}
	if j := strings.LastIndex(text, "}"); j >= 0 {
		text = text[:j+1]
	}
	var ans struct {
		Pace string  `json:"pace"`
		Why  string  `json:"why"`
		Next float64 `json:"next"`
	}
	if json.Unmarshal([]byte(text), &ans) != nil {
		return "", "", next
	}
	switch pace = strings.ToLower(strings.TrimSpace(ans.Pace)); pace {
	case "ok", "faster", "stuck":
	default:
		return "", "", next
	}
	why = oneLineText(ans.Why)
	if ans.Next > 0 {
		next = time.Duration(ans.Next * float64(time.Second))
	}
	return pace, why, min(max(next, paceMin), paceMax)
}

// paceWhat is a call in a few words, for a look: the tool, and what it was
// given.
func paceWhat(name string, input json.RawMessage) string {
	var args map[string]any
	_ = json.Unmarshal(input, &args)
	pick := func(keys ...string) string {
		for _, k := range keys {
			if s, ok := args[k].(string); ok && strings.TrimSpace(s) != "" {
				return s
			}
		}
		return ""
	}
	what := pick("command", "code", "file_path", "notebook_path", "path", "pattern", "url", "query", "description", "prompt")
	if what == "" && len(input) > 0 {
		what = string(input)
	}
	what = oneLineText(what)
	if r := []rune(what); len(r) > 200 {
		what = string(r[:200]) + "…"
	}
	if what == "" {
		return name
	}
	return name + ": " + what
}

// paceOutcome says in a few words how a call ended.
func paceOutcome(ev *agent.ToolEndEvent) string {
	switch {
	case ev.Denied:
		return "refused"
	case ev.Interrupted:
		return "stopped"
	}
	if id, _ := ev.Result.Meta["backgroundTaskId"].(string); id != "" {
		return "moved to the background as " + id
	}
	text := core.TextOf(ev.Result.Content)
	if ev.Name == shelltool.BashName {
		_, text = shelltool.SplitStamp(text)
	}
	first := oneLineText(firstLineOf(text))
	if r := []rune(first); len(r) > 120 {
		first = string(r[:120]) + "…"
	}
	head := "ok"
	if ev.Result.IsError {
		head = "failed"
	}
	if code, ok := metaNumber(ev.Result.Meta, "exitCode"); ok && ev.Name == shelltool.BashName {
		head = fmt.Sprintf("exit %d", code)
	}
	if first == "" {
		return head
	}
	return head + ": " + first
}

// firstLineOf is the first line of s that is not blank.
func firstLineOf(s string) string {
	for line := range strings.SplitSeq(s, "\n") {
		if strings.TrimSpace(line) != "" {
			return line
		}
	}
	return ""
}

// oneLineText is s on one line, with its runs of spaces as one.
func oneLineText(s string) string { return strings.Join(strings.Fields(s), " ") }

// metaNumber reads a whole number out of a result's facts, as a tool put
// it there or as JSON gave it back.
func metaNumber(meta map[string]any, key string) (int, bool) {
	switch v := meta[key].(type) {
	case int:
		return v, true
	case int64:
		return int(v), true
	case float64:
		return int(v), true
	case json.Number:
		n, err := v.Int64()
		return int(n), err == nil
	}
	return 0, false
}

// paceLine is what a pace check found, in a line for the person: "pace
// 3:05 · faster: run the one failing test", with the command it moved to
// the background.
func paceLine(ev *PaceEvent) string {
	what := "on pace"
	switch ev.Pace {
	case "faster":
		what = "faster"
	case "stuck":
		what = "stuck"
	}
	line := "pace " + prompt.PaceClock(ev.Elapsed) + " · " + what
	if ev.Why != "" {
		line += ": " + ev.Why
	}
	if ev.Moved != "" {
		line += " (moved to the background: " + ev.Moved + ")"
	}
	return line
}

// Line is the event as the interface shows it (paceLine).
func (ev *PaceEvent) Line() string { return paceLine(ev) }
