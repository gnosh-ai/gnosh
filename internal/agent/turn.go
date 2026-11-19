package agent

import (
	"context"
	"errors"
	"fmt"
	"runtime/debug"
	"slices"
	"strings"
	"time"

	"github.com/gnosh-ai/gnosh/internal/core"
)

// turn is everything from a prompt until the session has nothing more to
// do about it. It is touched by the session's goroutine alone, except for
// subs and cancel, which a stop reads under the session's lock.
type turn struct {
	id      string
	subs    []*submission // the prompts it answers, those folded in included
	started time.Time
	cancel  context.CancelFunc

	requests int           // main requests made, for RequestInfo and events
	rounds   int           // replies the model finished
	api      time.Duration // time spent waiting on the model
	usage    core.Usage
	denials  []Denial

	last   *reply       // the last reply stored
	open   []core.Block // tool calls of the last reply that have no stored result yet
	newest string       // the id of the turn's newest prompt
	// sent is the id of the newest prompt of the turn that came through the
	// queue (the person's, or a /loop's), not one of gnosh's own: a slide
	// keeps it and all after it.
	sent    string
	empties int // empty replies in a row
	// textCalls counts replies in a row that were a tool call written out
	// as text.
	textCalls int
	sentBack  bool // a driver has sent the model back
	drv       []driverState
	cp        *checkpoint // a checkpoint whose reply is awaited
	chosen    *stopChoice // the model's choice to stop, made in reply to a checkpoint
	guard     repeatGuard
	ended     bool // its TurnEndEvent has been sent
	// halt is why a moment ended the turn where it stood (a summary's end
	// said stop, Halt).
	halt string
}

// outcome is how a turn's body ended.
type outcome struct {
	reason    EndReason
	detail    string
	driver    string
	objection string
	err       error
	kind      string
	status    int
}

func stoppedOutcome() *outcome {
	return &outcome{reason: EndStopped, detail: "the user stopped the turn"}
}

// failedOutcome turns an error into the end of a turn, in plain words.
func failedOutcome(err error) *outcome {
	o := &outcome{reason: EndFailed, err: err, kind: "internal"}
	var f *failure
	var fit *fitError
	switch {
	case errors.As(err, &f):
		o.kind, o.status = f.kind, f.status
	case errors.As(err, &fit):
		o.kind = "context_too_long"
	}
	return o
}

// errStopped means the person stopped the turn. It never reaches the
// person as text: a stopped turn is shown as stopped.
var errStopped = errors.New("the user stopped the turn")

// runTurn runs one turn from its prompts to its end.
func (a *Agent) runTurn(ctx context.Context, t *turn) {
	defer t.cancel()
	defer func() {
		// The turn's own work is guarded below. What can still crash is
		// the writing of its end, which is the log's doing. Nobody who
		// waits for the turn is left waiting, and the log is settled
		// before the next turn builds on it.
		if p := recover(); p != nil {
			a.panicked("the turn's end", p, debug.Stack())
			a.fellBackEnds()
			a.settled = false
			res := TurnResult{
				Turn: t.id, Reason: EndFailed, ErrorKind: "internal",
				Err:     fmt.Errorf("gnosh hit an internal error as the turn ended (%v)", p),
				Started: t.started, Ended: a.clock.Now(),
			}
			res.Detail = res.Err.Error()
			a.mu.Lock()
			subs := slices.Clone(t.subs)
			a.cur = nil
			a.state.Turn = ""
			a.mu.Unlock()
			for _, s := range subs {
				res.Prompts = append(res.Prompts, s.id)
			}
			if !t.ended {
				a.emit(&TurnEndEvent{Result: res})
			}
			a.turnNow.Store("")
			for _, s := range subs {
				s.resolve(res)
			}
			a.loopOff("gnosh hit an internal error")
		}
	}()
	a.turnNow.Store(t.id)
	ids := make([]string, len(t.subs))
	for i, s := range t.subs {
		ids[i] = s.id
		if s.checkin {
			// The first reply of a /loop check-in is read for the word
			// that stops the loop.
			t.cp = &checkpoint{driver: "loop", marker: LoopStop, objection: "/loop is on, and the task has not been reported done"}
		}
	}
	a.emit(&TurnStartEvent{Prompts: ids})

	out := a.guarded(ctx, t)
	res := a.finishTurn(t, out)
	a.fellBackEnds()

	a.turnNow.Store("")
	a.mu.Lock()
	a.cur = nil
	a.state.Turn = ""
	a.state.Round = 0
	if a.loop.on {
		if res.Reason == EndCompleted {
			a.loop.last = &res
		}
	}
	a.mu.Unlock()
	if res.Reason != EndCompleted {
		a.loopOff("the turn " + endWords(res.Reason))
	}
}

// endWords says how a turn ended, to follow "the turn".
func endWords(r EndReason) string {
	switch r {
	case EndStopped:
		return "was stopped"
	case EndHalted:
		return "was ended"
	case EndFailed:
		return "failed"
	case EndModelStopped:
		return "ended because the model chose to stop"
	}
	return "ended"
}

// guarded runs the turn's body and turns a panic into a failed turn, so
// that one bad moment does not take the session down.
func (a *Agent) guarded(ctx context.Context, t *turn) (out *outcome) {
	defer func() {
		if p := recover(); p != nil {
			a.panicked("the turn", p, debug.Stack())
			out = failedOutcome(fmt.Errorf("gnosh hit an internal error in the middle of the turn (%v). "+
				"The turn has ended; the conversation so far is kept, and the next prompt carries on from it", p))
		}
	}()
	return a.body(ctx, t)
}

// body is the turn: the prompts go out, and then round follows round until
// the model answers without asking for a tool and nothing sends it back.
func (a *Agent) body(ctx context.Context, t *turn) *outcome {
	if err := a.settleOnce(); err != nil {
		return failedOutcome(err)
	}

	// Before the prompt goes out, the conversation is brought under what
	// the model can hold. It matters after a switch to a model with a
	// smaller window, and in a resumed session.
	// What goes out with the prompt (an @-mentioned file, the
	// environment) is worked out first and counted too: counted after, it
	// made the first round's summary take the new prompt with the rest.
	ready := a.preparePrompts(ctx, t, t.subs, false)
	extra, typed := 0, false
	for _, r := range ready {
		extra += a.estBlocks(r.s.prompt.blocks(), true)
		for _, p := range r.parts {
			extra += a.est(p.Text) + a.estBlocks(p.Images, true)
		}
		typed = typed || !r.s.prompt.Hidden
	}
	moment := RoomBeforeOwnPrompt
	if typed {
		moment = RoomBeforePrompt
	}
	_, fitErr := a.fit(ctx, t, moment, extra)

	// The prompt is kept whatever happened: stopped here, it is a stopped
	// turn, and a summary that failed sends it as it is.
	if err := a.appendPrompts(t, ready, false); err != nil {
		return failedOutcome(err)
	}
	if ctx.Err() != nil {
		return stoppedOutcome()
	}
	if fitErr != nil && !errors.Is(fitErr, errStopped) {
		return failedOutcome(fitErr)
	}
	if t.halt != "" {
		return &outcome{reason: EndHalted, detail: t.halt, driver: "after-summary"}
	}

	for {
		if ctx.Err() != nil {
			return stoppedOutcome()
		}
		rep, out := a.request(ctx, t)
		if out != nil {
			return out
		}

		if len(rep.msg.ToolUses()) > 0 {
			ro := a.runTools(ctx, t, rep)
			switch {
			case ro.err != nil:
				return failedOutcome(ro.err)
			case ro.stopped:
				return stoppedOutcome()
			case ro.halt != "":
				return &outcome{reason: EndHalted, detail: ro.halt, driver: ro.haltBy}
			}
			if ctx.Err() != nil {
				// Stopped as the round's tools ended: the turn ends as
				// stopped, and what the person sent meanwhile stays in line
				// for the next turn instead of going into this one (the
				// agent loop review of 2026-11-19).
				return stoppedOutcome()
			}
			if f := a.opts.Moments.MoreRounds; f != nil {
				if more, why := f(t.id, t.requests); !more {
					return &outcome{reason: EndHalted, detail: why, driver: "max-turns"}
				}
			}
			// The model will be asked again, so this is where it is told
			// that it repeats itself, and where what the person sent
			// meanwhile is handed to it.
			if err := a.checkRepeat(t, ro); err != nil {
				return failedOutcome(err)
			}
			if err := a.foldQueued(ctx, t); err != nil {
				return failedOutcome(err)
			}
			continue
		}

		// The reply asks for no tool: the turn is about to end, unless
		// something sends the model back to work.
		if rep.silent() && rep.stop != core.StopMaxTokens && rep.stop != core.StopRefusal {
			if err := a.nudgeEmpty(t); err != nil {
				return failedOutcome(err)
			}
			continue
		}
		t.empties = 0
		if rep.callAsText() && rep.stop != core.StopMaxTokens && rep.stop != core.StopRefusal {
			if err := a.nudgeTextCall(t); err != nil {
				return failedOutcome(err)
			}
			continue
		}
		t.textCalls = 0
		a.noteCutReply(rep)

		if c := t.chosen; c != nil {
			return &outcome{reason: EndModelStopped, detail: c.reason, driver: c.cp.driver, objection: c.cp.objection}
		}
		back, out := a.askDrivers(ctx, t, rep)
		if out != nil {
			return out
		}
		if back {
			continue
		}
		return &outcome{reason: EndCompleted}
	}
}

// noteCutReply tells the person when the reply a turn ends on is not the
// model's own ending.
func (a *Agent) noteCutReply(rep *reply) {
	switch rep.stop {
	case core.StopMaxTokens:
		a.notice(LevelWarning, "loop", fmt.Sprintf(
			"The reply was cut off at the length limit (%s tokens written). What arrived is kept.", tokens(rep.usage.OutputTokens)))
	case core.StopRefusal:
		a.notice(LevelWarning, "loop", "The provider withheld the model's answer.")
	}
}

// request makes one main request and stores its reply. It returns the
// reply, or how the turn ended instead.
func (a *Agent) request(ctx context.Context, t *turn) (*reply, *outcome) {
	t.requests++
	a.mu.Lock()
	a.state.Round = t.requests
	a.mu.Unlock()

	if err := a.tellNotes(t); err != nil {
		return nil, failedOutcome(err)
	}
	if err := a.remind(ctx, t); err != nil {
		return nil, failedOutcome(err)
	}
	for {
		fr, err := a.fit(ctx, t, RoomMidTurn, 0)
		switch {
		case errors.Is(err, errStopped):
			return nil, stoppedOutcome()
		case err != nil:
			return nil, failedOutcome(err)
		case t.halt != "":
			return nil, &outcome{reason: EndHalted, detail: t.halt, driver: "after-summary"}
		}

		rep, err := a.exchange(ctx, t, "main", fr.req, fr.est, fr.su.model, true)
		var long *tooLongError
		switch {
		case err == nil:
			a.cacheSeen(fr.req, rep.usage, a.clock.Now())
		case errors.Is(err, errFellBack):
			// Asked again of the fallback model, for which the request is
			// made afresh. What arrived of the failed try is not kept.
			continue
		case errors.Is(err, errStopped):
			if serr := a.storePartial(t, rep, fr.su.model, core.StopCancelled); serr != nil {
				return nil, failedOutcome(serr)
			}
			return nil, stoppedOutcome()
		case errors.As(err, &long):
			// The provider says the request is too long for the model. It
			// is never sent again as it is: room is made first.
			a.emit(&RetryEvent{Purpose: "main", Attempt: 1, Kind: core.ErrContextTooLong, Status: long.cause.Status,
				Message: fr.su.model.label() + " refused the request as too long for the model. gnosh makes room and asks again."})
			a.refusedAsTooLong(fr)
			continue
		default:
			if serr := a.storePartial(t, rep, fr.su.model, core.StopError); serr != nil {
				return nil, failedOutcome(serr)
			}
			return nil, failedOutcome(err)
		}

		// A local server whose slot is smaller than the window it was
		// said to have cuts the reply off when the slot is full. That is
		// the same trouble as a request that is too long, found later.
		if a.cutByWindow(fr, rep) {
			a.emit(&RetryEvent{Purpose: "main", Attempt: 1, Kind: core.ErrContextTooLong, Discarded: !rep.empty(),
				Message: "The reply was cut off because the model's context filled up. gnosh makes room and asks again."})
			continue
		}

		if err := a.storeReply(t, rep); err != nil {
			return nil, failedOutcome(err)
		}
		return rep, nil
	}
}

// refusedAsTooLong is called when the provider refused a request as too
// long for the model, though the loop had taken it to fit. Either the
// loop's estimate was too low or the model's window is smaller than the
// loop was told. With no count of the provider's own to go by, the
// estimate is the likelier culprit, and it is scaled up until this request
// counts as too long; otherwise the window is taken to be smaller. Either
// way the next fit must make the request shorter.
func (a *Agent) refusedAsTooLong(fr fitted) {
	m := fr.su.model
	if !(a.anchor.ok && a.anchor.model == modelKey(m)) && fr.est > 0 {
		c := min(max(float64(fr.lim.window+1)/float64(fr.est), a.calib), 3)
		if scale(fr.est, c) > fr.lim.hard {
			a.calib = c
			a.notice(LevelWarning, "context", fmt.Sprintf(
				"%s refused a request as too long for its context, though gnosh took it for about %s tokens against a window of %s. "+
					"gnosh's estimates were too low; it now counts %.0f%% more, and makes room.",
				m.label(), tokens(fr.predicted), tokens(fr.lim.window), (c-1)*100))
			return
		}
	}
	// The real window lies between the longest request the provider has
	// taken and this one. Halving the gap finds it in a few refusals,
	// which cost a model server nothing.
	n := fr.predicted - 1
	if a.heldFor == modelKey(m) && a.held > 0 && a.held < n {
		n = (a.held + n) / 2
	}
	a.learnWindow(m, n, fmt.Sprintf(
		"%s refused a request of about %s tokens as too long for its context", m.label(), tokens(fr.predicted)))
}

// cutByWindow reports whether rep was cut off by a window smaller than the
// one the loop believed in, and if so makes the loop believe in the
// smaller one.
func (a *Agent) cutByWindow(fr fitted, rep *reply) bool {
	m := fr.su.model
	if !m.Local || rep.stop != core.StopMaxTokens {
		return false
	}
	if m.MaxTokens > 0 && rep.usage.OutputTokens >= m.MaxTokens {
		return false // The reply hit the cap the person set.
	}
	total := rep.usage.PromptTokens() + rep.usage.OutputTokens
	if rep.usage.PromptTokens() == 0 || total >= fr.lim.window {
		return false
	}
	a.learnWindow(m, total, fmt.Sprintf(
		"%s cut its reply off after %s tokens in all, though its window was taken to be %s",
		m.label(), tokens(total), tokens(fr.lim.window)))
	return true
}

// tellNotes puts the notes that wait in front of the model: what the
// harness was told to pass on since the last request.
func (a *Agent) tellNotes(t *turn) error {
	a.mu.Lock()
	notes := a.notes
	a.notes = nil
	a.mu.Unlock()
	if len(notes) == 0 {
		return nil
	}
	entries := make([]Entry, 0, len(notes))
	for _, n := range notes {
		entries = append(entries, Entry{Kind: EntryNote, Turn: t.id, Source: firstNonEmpty(n.Source, "note"), Blocks: []core.Block{core.Text(n.Text)}})
	}
	if err := a.appendLog(entries...); err != nil {
		// They were not told: they wait on.
		a.mu.Lock()
		a.notes = append(notes, a.notes...)
		a.mu.Unlock()
		return err
	}
	for _, n := range notes {
		a.emit(&NoteEvent{Note: n})
		if n.Notice != "" {
			a.notice(LevelInfo, firstNonEmpty(n.Source, "note"), n.Notice)
		}
	}
	return nil
}

// remind asks the BeforeRequest moments for what to put in front of the
// model, and stores it.
func (a *Agent) remind(ctx context.Context, t *turn) error {
	if len(a.opts.Moments.BeforeRequest) == 0 {
		return nil
	}
	a.mu.Lock()
	info := RequestInfo{Turn: t.id, Round: t.requests, Used: a.context.Used, Point: a.context.Point}
	a.mu.Unlock()
	var entries []Entry
	for _, f := range a.opts.Moments.BeforeRequest {
		parts, err, ok := await(ctx, a, "a reminder", func(ctx context.Context) ([]Part, error) { return f(ctx, info) })
		if ctx.Err() != nil || !ok {
			break
		}
		if err != nil {
			a.notice(LevelWarning, "reminder", fmt.Sprintf("A reminder for the model could not be made (%v). The request goes out without it.", err))
			continue
		}
		for _, p := range parts {
			if strings.TrimSpace(p.Text) == "" {
				continue
			}
			entries = append(entries, Entry{Kind: EntryNote, Turn: t.id, Source: p.Source, Blocks: []core.Block{core.Text(p.Text)}})
		}
	}
	return a.appendLog(entries...)
}

// sendPrompts puts prompts into the conversation, each with its hidden
// lines, which are worked out now: a prompt that waited in the queue tells
// of things as they stand when it goes out.
func (a *Agent) sendPrompts(ctx context.Context, t *turn, subs []*submission, folded bool) error {
	return a.appendPrompts(t, a.preparePrompts(ctx, t, subs, folded), folded)
}

// readyPrompt is a prompt with the hidden lines that go out with it.
type readyPrompt struct {
	s     *submission
	parts []Part
}

// preparePrompts works out the hidden lines of prompts about to go out:
// what the session's start added, what the prompt's check added, and the
// lines of the moment (an @-mentioned file, the environment).
func (a *Agent) preparePrompts(ctx context.Context, t *turn, subs []*submission, folded bool) []readyPrompt {
	var out []readyPrompt
	for _, s := range subs {
		a.mu.Lock()
		first := !a.sent
		parts := a.startAdd
		a.startAdd = nil
		a.mu.Unlock()
		parts = append(slices.Clone(parts), s.added...)

		info := LineInfo{Prompt: s.prompt, ID: s.id, First: first, Folded: folded, Turn: t.id}
		for _, f := range a.opts.Moments.PromptLines {
			if ctx.Err() != nil {
				break
			}
			more, err, ok := await(ctx, a, "a prompt's lines", func(ctx context.Context) ([]Part, error) { return f(ctx, info) })
			if !ok || ctx.Err() != nil {
				break
			}
			if err != nil {
				a.notice(LevelWarning, "prompt-lines", fmt.Sprintf("A line that goes with every prompt could not be made (%v). The prompt goes out without it.", err))
				continue
			}
			parts = append(parts, more...)
		}
		out = append(out, readyPrompt{s: s, parts: parts})
	}
	return out
}

// appendPrompts puts prompts made ready into the conversation.
func (a *Agent) appendPrompts(t *turn, ready []readyPrompt, folded bool) error {
	for _, r := range ready {
		s, parts := r.s, r.parts
		var entries []Entry
		var sentParts []Part
		note := func(p Part) {
			if strings.TrimSpace(p.Text) == "" {
				return // no message may hold a blank block
			}
			entries = append(entries, Entry{Kind: EntryNote, Turn: t.id, Source: p.Source, Prompt: s.id, Blocks: append([]core.Block{core.Text(p.Text)}, p.Images...)})
			sentParts = append(sentParts, p)
		}
		for _, p := range parts {
			if p.Before {
				note(p)
			}
		}
		entries = append(entries, Entry{
			Kind: EntryPrompt, ID: s.id, Turn: t.id, Blocks: s.prompt.blocks(),
			Hidden: s.prompt.Hidden, Source: s.prompt.Source,
		})
		for _, p := range parts {
			if !p.Before {
				note(p)
			}
		}
		if err := a.appendLog(entries...); err != nil {
			return err
		}
		a.mu.Lock()
		a.sent = true
		a.mu.Unlock()
		t.newest, t.sent = s.id, s.id
		a.emit(&PromptEvent{ID: s.id, Prompt: s.prompt, Folded: folded, Parts: sentParts})
		if s.prompt.Hidden && s.prompt.Notice != "" {
			a.notice(LevelInfo, firstNonEmpty(s.prompt.Source, "loop"), s.prompt.Notice)
		}
	}
	return nil
}

// foldQueued hands the model what was sent while the turn ran, at the
// boundary between two rounds. Whoever takes a prompt off the queue does
// it under the session's lock, so a prompt is never both folded in and
// taken back.
func (a *Agent) foldQueued(ctx context.Context, t *turn) error {
	a.waitAdmitting(ctx)
	a.mu.Lock()
	if ctx.Err() != nil {
		// Stopped while it waited (Interrupt cancels under this lock): the
		// queue is the next turn's.
		a.mu.Unlock()
		return nil
	}
	var take []*submission
	keep := a.queue[:0:0]
	for _, s := range a.queue {
		if s.prompt.Later {
			keep = append(keep, s)
			continue
		}
		take = append(take, s)
	}
	if len(take) == 0 {
		a.mu.Unlock()
		return nil
	}
	a.queue = keep
	t.subs = append(t.subs, take...)
	a.mu.Unlock()
	a.emitQueue()
	return a.sendPrompts(ctx, t, take, true)
}

// admitWait is how long the boundary between two rounds waits for a
// prompt the person sent that is still being taken in.
const admitWait = 2 * time.Second

// waitAdmitting waits, up to admitWait, for what was sent and is still
// being taken in (its hooks run, it is written down) to reach the queue,
// so that a prompt the person sent before the round ended goes with the
// next request rather than a turn of its own: sending at once moves the
// commands aside and ends the round at once, while the prompt may still
// be on its way (on a busy machine, writing it down takes a while). It
// waits for what the person sent alone, hidden or not (a !command's
// output, a /skill): the notes gnosh tells the model (Tell: a channel's
// event, a background task's end), which may come often, never hold a
// round up.
func (a *Agent) waitAdmitting(ctx context.Context) {
	deadline := time.Now().Add(admitWait)
	for ctx.Err() == nil && time.Now().Before(deadline) {
		a.mu.Lock()
		pending := slices.ContainsFunc(a.intake, func(s *submission) bool { return !s.taken && !s.prompt.Later && !s.note })
		a.mu.Unlock()
		if !pending {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// ownPrompt sends the model a prompt of gnosh's own in the middle of a
// turn: a nudge, a driver's objection, a checkpoint. It is stored like any
// prompt, so that the conversation is rebuilt the same, and it is never
// shown as something the person typed.
func (a *Agent) ownPrompt(t *turn, source, text string) error {
	id := a.newID()
	err := a.appendLog(Entry{
		Kind: EntryPrompt, ID: id, Turn: t.id, Hidden: true, Source: source,
		Blocks: []core.Block{core.Text(text)},
	})
	if err != nil {
		return err
	}
	t.newest = id
	a.emit(&PromptEvent{ID: id, Prompt: Prompt{ID: id, Text: text, Hidden: true, Source: source}})
	return nil
}

// nudgeEmpty asks a model that answered with nothing to answer again.
// There is no count at which this gives up: at every third empty reply in
// a row the person is told what is happening and what usually fixes it.
func (a *Agent) nudgeEmpty(t *turn) error {
	t.empties++
	if t.empties%3 == 0 {
		a.notice(LevelWarning, "empty-reply", fmt.Sprintf(a.texts.EmptyNotice, t.empties))
	}
	return a.ownPrompt(t, "nudge:empty", a.mark(a.texts.EmptyNudge))
}

// nudgeTextCall tells a model whose reply was a tool call written out as
// text that nothing ran, and asks it again. As with an empty reply, no
// count gives up: at every third in a row the person is told.
func (a *Agent) nudgeTextCall(t *turn) error {
	t.textCalls++
	if t.textCalls%3 == 0 {
		a.notice(LevelWarning, "text-call", fmt.Sprintf(a.texts.TextCallNotice, t.textCalls))
	}
	return a.ownPrompt(t, "nudge:text-call", a.mark(a.texts.TextCallNudge))
}

// storeReply puts a finished reply into the log and counts it.
func (a *Agent) storeReply(t *turn, rep *reply) error {
	silent := rep.silent()
	err := a.appendLog(Entry{
		Kind: EntryAssistant, Turn: t.id, Message: rep.msg,
		Model: rep.model.Info.ID, Provider: rep.model.Provider.Name(),
		Usage: rep.usage, StopReason: rep.stop,
		Silent: silent, BadInputs: rep.bad, Duration: rep.dur,
	})
	if err != nil {
		return err
	}
	t.rounds++
	t.last = rep
	t.open = nil
	if !silent {
		t.open = rep.msg.ToolUses()
	}

	// The marker that stops a turn counts only in the reply to a
	// checkpoint, and only in a reply the model finished without asking
	// for a tool. An empty reply is asked again, and the next one is then
	// the reply to the checkpoint.
	if cp := t.cp; cp != nil && !silent {
		t.cp = nil
		if len(t.open) == 0 && rep.stop != core.StopMaxTokens && rep.stop != core.StopRefusal {
			if reason, ok := cutMarker(rep.msg.Text(), cp.marker); ok {
				t.chosen = &stopChoice{cp: cp, reason: reason}
				rep.marker = cp.marker
			}
		}
	}

	a.emit(&ReplyEvent{
		Purpose: "main", Round: t.requests, Message: rep.msg,
		StopReason: rep.stop, Silent: silent, BadInputs: rep.bad, Marker: rep.marker,
		Provider: rep.model.Provider.Name(), Model: rep.model.Info.ID,
		Usage: rep.usage, Duration: rep.dur, FirstPiece: rep.first,
	})
	return nil
}

// storePartial keeps what arrived of a reply that was cut short, so that
// the person still has it, under the reason it was cut: core.StopCancelled
// when the person stopped the turn, core.StopError when the provider
// failed. It is stored even when nothing had arrived, because a reply that
// was cancelled or failed is what ends a turn. Its tool calls never ran;
// each gets a result that says so, which keeps the conversation one a
// model can be sent.
//
// rep is nil when the turn was stopped while the loop waited to try a
// failed request again. No request was out then, so the consumer of events
// is told of no reply; the entry is stored all the same.
func (a *Agent) storePartial(t *turn, rep *reply, m Model, why core.StopReason) error {
	out := rep != nil
	if rep == nil {
		rep = &reply{model: m}
	}
	rep.stop = why
	rep.msg.Role = core.RoleAssistant
	silent := rep.silent()
	entries := []Entry{{
		Kind: EntryAssistant, Turn: t.id, Message: rep.msg,
		Model: rep.model.Info.ID, Provider: rep.model.Provider.Name(),
		Usage: rep.usage, StopReason: why,
		Silent: silent, BadInputs: rep.bad, Duration: rep.dur,
	}}
	if !silent {
		for _, u := range rep.msg.ToolUses() {
			entries = append(entries, Entry{
				Kind: EntryResult, Turn: t.id, ToolUseID: u.ID, ToolName: u.Name,
				Result: core.ErrorResult(a.texts.ReplyCut), Interrupted: true,
			})
		}
	}
	if err := a.appendLog(entries...); err != nil {
		return err
	}
	if !rep.empty() {
		t.last = rep
	}
	t.open = nil
	if !out {
		return nil
	}
	a.emit(&ReplyEvent{
		Purpose: "main", Round: t.requests, Message: rep.msg,
		StopReason: why, Silent: silent, BadInputs: rep.bad,
		Provider: rep.model.Provider.Name(), Model: rep.model.Info.ID,
		Usage: rep.usage, Duration: rep.dur, FirstPiece: rep.first,
	})
	return nil
}

// finishTurn writes the turn's end into the log and tells everyone who
// waits for it. Whatever path the turn took, no tool call is left without
// a result.
func (a *Agent) finishTurn(t *turn, out *outcome) TurnResult {
	now := a.clock.Now()
	res := TurnResult{
		Turn:        t.id,
		Reason:      out.reason,
		Detail:      out.detail,
		Driver:      out.driver,
		Objection:   out.objection,
		Err:         out.err,
		ErrorKind:   out.kind,
		Status:      out.status,
		Rounds:      t.rounds,
		Usage:       t.usage,
		Duration:    now.Sub(t.started),
		APIDuration: t.api,
		Denials:     t.denials,
		Started:     t.started,
		Ended:       now,
	}
	a.mu.Lock()
	for _, s := range t.subs {
		res.Prompts = append(res.Prompts, s.id)
	}
	closing := a.closing
	a.mu.Unlock()
	if res.Reason == EndStopped && closing {
		// It was not the person's Esc: the session itself was closed.
		res.Detail = "the session was closed while the turn ran"
	}
	if t.last != nil {
		res.StopReason = t.last.stop
		res.Text = t.last.msg.Text()
		if t.last.marker != "" {
			res.Text, _ = cutMarker(res.Text, t.last.marker)
		}
	}
	if out.err != nil && res.Detail == "" {
		res.Detail = out.err.Error()
	}

	var entries []Entry
	for _, u := range t.open {
		text := a.texts.StoppedBefore
		if out.reason != EndStopped {
			text = fmt.Sprintf(a.texts.HaltedBefore, firstNonEmpty(oneLine(res.Detail), "the turn failed"))
		}
		entries = append(entries, Entry{
			Kind: EntryResult, Turn: t.id, ToolUseID: u.ID, ToolName: u.Name,
			Result: core.ErrorResult(text), Interrupted: true,
		})
	}
	t.open = nil
	entries = append(entries, Entry{Kind: EntryTurn, Turn: t.id, Reason: out.reason, Text: res.Detail})
	if err := a.appendLog(entries...); err != nil {
		// The turn's end is not in the log. It is settled before the next
		// turn builds on the log, when the log can be written again.
		a.settled = false
		if res.Reason != EndFailed {
			res.Reason, res.Err, res.ErrorKind, res.Detail = EndFailed, err, "internal", err.Error()
		}
	}

	// Every way a turn can end is visible.
	switch res.Reason {
	case EndFailed:
		a.noticeAs(&NoticeEvent{TurnEnd: true, Level: LevelError, Source: "loop", Text: strings.TrimSpace("The turn failed. " + res.Detail)})
	case EndHalted:
		a.noticeAs(&NoticeEvent{TurnEnd: true, Level: LevelWarning, Source: firstNonEmpty(res.Driver, "loop"),
			Text: "The turn was ended: " + firstNonEmpty(res.Detail, "no reason was given") + "."})
	case EndModelStopped:
		a.noticeAs(&NoticeEvent{TurnEnd: true, Level: LevelWarning, Source: "driver:" + res.Driver, Text: fmt.Sprintf(
			"The model chose to stop. Its reason: %s\nWhat held the turn open: %s",
			firstNonEmpty(strings.TrimSpace(res.Detail), "(it gave none)"), firstNonEmpty(res.Objection, "(nothing was recorded)"))})
	}

	a.emit(&TurnEndEvent{Result: res})
	t.ended = true
	// The moments after a turn ended run under a context of their own,
	// which Esc and End cancel: a hook that hung there (a StopFailure
	// webhook, ten minutes by default) held the next prompt, Esc did
	// nothing, and /exit waited for it (the agent loop review of
	// 2026-11-19).
	mctx, mstop := context.WithCancel(a.root)
	a.mu.Lock()
	a.endStop = mstop
	a.mu.Unlock()
	if f := a.opts.Moments.TurnFailed; f != nil && res.Reason == EndFailed {
		_, _, _ = await(mctx, a, "the turn-failed moment", func(ctx context.Context) (struct{}, error) {
			f(ctx, res)
			return struct{}{}, nil
		})
	}
	if f := a.opts.Moments.TurnEnded; f != nil {
		_, _, _ = await(mctx, a, "the turn-ended moment", func(ctx context.Context) (struct{}, error) {
			f(ctx, res)
			return struct{}{}, nil
		})
	}
	a.mu.Lock()
	a.endStop = nil
	a.mu.Unlock()
	mstop()
	a.mu.Lock()
	subs := slices.Clone(t.subs)
	a.mu.Unlock()
	for _, s := range subs {
		s.resolve(res)
	}
	return res
}
