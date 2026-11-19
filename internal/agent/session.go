package agent

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/gnosh-ai/gnosh/internal/core"
)

// run is the session's own goroutine. It owns the conversation: nothing
// else starts a turn, appends a reply or decides what happens next. It
// only ever waits on a channel, so with nothing in flight it uses no
// processor at all.
func (a *Agent) run() {
	defer close(a.done)
	if err := a.settleOnce(); err != nil {
		a.emit(&NoticeEvent{Level: LevelError, Source: "loop", Text: fmt.Sprintf(
			"gnosh could not read this session's log to see how it was left (%v). It will try again when a prompt is sent.", err)})
	}
	close(a.ready)
	for a.waitWork() {
		a.setPhase(PhaseStarting, "starting")
		a.emit(&BusyEvent{})
		var release func()
		held := false
		for a.step(&release, &held) {
		}
		a.leaveBusy(release)
	}
}

// workLocked reports whether there is anything for the session's goroutine
// to do. The caller holds the lock.
func (a *Agent) workLocked() bool {
	return len(a.intake) > 0 || a.intaking || len(a.queue) > 0 || len(a.commands) > 0 || a.loop.on
}

// waitWork blocks until there is work, and reports false when the session
// is closing instead. A session that was marked busy gets its busy
// stretch even when the work is already gone (a prompt that its hook
// refused at once), so that busy always ends in idle.
func (a *Agent) waitWork() bool {
	for {
		a.mu.Lock()
		closing, work := a.closing, a.workLocked() || a.busy
		a.mu.Unlock()
		if closing {
			return false
		}
		if work {
			return true
		}
		<-a.wake
	}
}

// hold tells the Busy moment that the session has begun to work, before
// the first turn of a busy stretch, and returns what is to be called when
// the stretch is over. ctx is that turn's own, so that stopping the turn
// stops the wait: the prompt is then kept as a turn that was stopped.
func (a *Agent) hold(ctx context.Context) func() {
	f := a.opts.Moments.Busy
	if f == nil {
		return nil
	}
	a.setPhase(PhaseStarting, "taking the session's turn")

	// If the moment does not return once it has been stopped, the loop
	// goes on without it; what it returns later is then called at once,
	// so that nothing stays held.
	var gone late
	ch := make(chan func(), 1)
	go func() {
		defer func() {
			if p := recover(); p != nil {
				a.panicked("the busy moment", p, nil)
				gone.deliver(func() { ch <- nil })
			}
		}()
		rel := f(ctx)
		if !gone.deliver(func() { ch <- rel }) && rel != nil {
			rel()
		}
	}()
	var release func()
	select {
	case release = <-ch:
	case <-ctx.Done():
		t := time.NewTimer(a.grace)
		select {
		case release = <-ch:
		case <-t.C:
			gone.giveUp()
			select {
			case release = <-ch:
			default:
				a.notice(LevelWarning, "loop", "The busy moment had not stopped "+span(a.grace)+" after the turn was stopped. gnosh went on without it.")
			}
		}
		t.Stop()
	}
	return release
}

// late lets a goroutine that may be given up on hand over its result
// exactly once: either the one who waits takes it, or the goroutine learns
// that nobody waits any more.
type late struct {
	mu   sync.Mutex
	gone bool
}

// deliver runs send unless the goroutine has been given up on, and reports
// whether it ran. send must not block.
func (l *late) deliver(send func()) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.gone {
		return false
	}
	send()
	return true
}

// giveUp says nobody waits any more. What was delivered before it is still
// there to be taken.
func (l *late) giveUp() {
	l.mu.Lock()
	l.gone = true
	l.mu.Unlock()
}

// leaveBusy marks the end of a busy stretch: every queued prompt has had
// its turn, and what the Busy moment returned is called.
func (a *Agent) leaveBusy(release func()) {
	if release != nil {
		func() {
			defer func() {
				if p := recover(); p != nil {
					a.panicked("the idle moment", p, nil)
				}
			}()
			release()
		}()
	}
	a.setPhase(PhaseIdle, "")
	a.emit(&IdleEvent{})
	a.mu.Lock()
	var recorded []Note
	if a.closing || !a.workLocked() {
		a.busy = false
		if a.idle != nil {
			close(a.idle)
			a.idle = nil
		}
		recorded = a.takeRecordedLocked()
	}
	a.mu.Unlock()
	a.writeRecorded(recorded)
}

// step does the next thing there is to do, and reports false when there is
// nothing left. release and held belong to the busy stretch: the Busy
// moment is told once, before the stretch's first turn, and what it
// returns is kept until the session goes idle.
func (a *Agent) step(release *func(), held *bool) bool {
	a.mu.Lock()
	switch {
	case a.closing:
		a.mu.Unlock()
		return false
	case len(a.commands) > 0:
		c := a.commands[0]
		a.commands = a.commands[1:]
		a.mu.Unlock()
		a.runCommand(c)
		return true
	case len(a.queue) > 0:
		subs := a.queue
		a.queue = nil
		t, ctx := a.beginTurnLocked(subs)
		a.mu.Unlock()
		a.emitQueue()
		if !*held {
			*held = true
			*release = a.hold(ctx)
		}
		a.runTurn(ctx, t)
		return true
	case len(a.intake) > 0 || a.intaking:
		// A prompt is still being admitted. Its goroutine says when it is
		// done.
		a.mu.Unlock()
		<-a.wake
		return true
	case a.loop.on:
		a.mu.Unlock()
		func() {
			defer func() {
				if p := recover(); p != nil {
					a.panicked("/loop", p, nil)
					a.loopOff("gnosh hit an internal error")
				}
			}()
			a.loopNext()
		}()
		return true
	}
	a.mu.Unlock()
	return false
}

// beginTurnLocked makes the turn that subs start and registers it as what
// a stop reaches. The caller holds the lock.
func (a *Agent) beginTurnLocked(subs []*submission) (*turn, context.Context) {
	ctx, cancel := context.WithCancel(a.root)
	t := &turn{
		id:      a.newID(),
		subs:    subs,
		started: a.clock.Now(),
		cancel:  cancel,
		drv:     make([]driverState, len(a.opts.Drivers)),
	}
	a.cur = &operation{cancel: cancel, turn: t}
	a.state.Turn = t.id
	a.state.Round = 0
	return t, ctx
}

// Compact asks for a summary of the conversation now, as /compact does,
// with the person's instructions for it. It runs when the turn in hand has
// ended. How it went arrives as SummaryEvents.
func (a *Agent) Compact(instructions string) error {
	a.mu.Lock()
	if a.closing {
		a.mu.Unlock()
		return ErrClosed
	}
	a.commands = append(a.commands, &command{compact: true, instructions: instructions})
	a.markBusyLocked()
	a.mu.Unlock()
	a.kick()
	return nil
}

// runCommand does one thing that is not a turn.
func (a *Agent) runCommand(c *command) {
	ctx, cancel := context.WithCancel(a.root)
	defer cancel()
	a.mu.Lock()
	a.cur = &operation{cancel: cancel}
	a.mu.Unlock()
	defer func() {
		a.mu.Lock()
		a.cur = nil
		a.mu.Unlock()
	}()
	defer func() {
		if p := recover(); p != nil {
			a.panicked("a command", p, nil)
			a.notice(LevelError, "loop", fmt.Sprintf("gnosh hit an internal error while it made a summary (%v). The conversation is as it was.", p))
		}
	}()
	if !c.compact {
		return
	}
	if err := a.settleOnce(); err != nil {
		a.notice(LevelError, "summary", fmt.Sprintf("No summary was made: %v.", err))
		return
	}
	// A summary the person asks for by hand lets automatic ones run again.
	a.summariesOff = false
	err := a.summarize(ctx, nil, "manual", c.instructions)
	switch {
	case err == nil:
	case ctx.Err() != nil:
		a.notice(LevelInfo, "summary", "The summary was stopped. The conversation is as it was.")
	default:
		a.notice(LevelError, "summary", fmt.Sprintf("No summary was made: %v. The conversation is as it was.", err))
	}
}

// admitLoop takes in the prompts that were submitted, one at a time and in
// the order they were sent, however long each one's hooks take. It runs
// beside the session's goroutine, so that a prompt sent while a turn runs
// has its hooks run then and waits in the queue with what they added.
func (a *Agent) admitLoop() {
	defer a.intakeWG.Done()
	<-a.ready
	for {
		a.mu.Lock()
		if len(a.intake) == 0 || a.closing {
			a.intaking = false
			a.mu.Unlock()
			a.kick()
			return
		}
		s := a.intake[0]
		if s.taken {
			a.intake = a.intake[1:]
			a.mu.Unlock()
			continue
		}
		ctx, cancel := context.WithCancel(a.root)
		a.admitting, a.admitStop = s, cancel
		a.mu.Unlock()

		ok, why, reason := a.admit(ctx, s)
		cancel()

		// A prompt that cannot go out at once is written down before it is
		// queued, so that "accepted" means it is on disk: from here on it
		// reaches the model or comes back to the person.
		a.mu.Lock()
		a.admitting, a.admitStop = nil, nil
		wait := ok && !s.taken && ((a.cur != nil && a.cur.turn != nil) || len(a.queue) > 0)
		a.mu.Unlock()
		persisted := false
		if wait {
			err := a.appendLog(Entry{Kind: EntryQueued, ID: s.id, Blocks: s.prompt.blocks(), Hidden: s.prompt.Hidden, Source: s.prompt.Source})
			if err != nil {
				ok, why, reason = false, ReturnUnsaved, err.Error()
			} else {
				persisted = true
			}
		}

		a.mu.Lock()
		removeSub(&a.intake, s)
		taken := s.taken
		if ok && !taken {
			// From here on whoever takes it off the queue lets it go in
			// the log as well.
			s.persisted = persisted
			a.queue = append(a.queue, s)
		}
		a.mu.Unlock()

		switch {
		case taken:
			// Whoever took it has told the person. It may have been
			// written down in the meantime.
			if persisted {
				_ = a.appendLog(Entry{Kind: EntryUnqueued, Prompt: s.id, Text: "taken back"})
			}
			a.emitQueue()
		case !ok && s.note && why == ReturnStopped:
			// A note that was to start a turn, stopped in its check (Esc
			// as the session's start ran for it), is still a note: it goes
			// with the next request, as Tell promises. It came back as a
			// hidden prompt nobody shows, and was lost (the agent loop
			// review of 2026-11-19).
			a.mu.Lock()
			a.notes = append(a.notes, Note{Text: s.prompt.Text, Source: s.prompt.Source, Notice: s.prompt.Notice})
			a.mu.Unlock()
			s.resolve(TurnResult{Prompts: []string{s.id}, Reason: EndWithdrawn, Detail: reason})
		case !ok:
			a.emit(&PromptReturnedEvent{ID: s.id, Prompt: s.prompt, Why: why, Reason: reason})
			end := EndWithdrawn
			if why == ReturnRefused {
				end = EndRefused
			}
			s.resolve(TurnResult{Prompts: []string{s.id}, Reason: end, Detail: reason})
			if why == ReturnRefused && s.prompt.Own && s.prompt.Source == "loop" {
				// Its next prompt would be refused the same way: it sent
				// one at once, again and again, until Esc (the agent loop
				// review of 2026-11-19).
				a.loopOff("its prompt was refused: " + reason)
			}
		case wait:
			// It waits behind a turn or behind other prompts, and is
			// shown as waiting.
			a.emitQueue()
		}
		a.kick()
	}
}

// admit runs the moments a prompt passes on its way in: the session's
// start, once, and then the prompt's own check. It reports whether the
// prompt may go out, and if not, why.
func (a *Agent) admit(ctx context.Context, s *submission) (bool, ReturnReason, string) {
	const stopped = "the session was stopped while this prompt was being checked, so it was not sent"

	a.mu.Lock()
	halted := a.startHalt
	a.mu.Unlock()
	if halted != "" {
		return false, ReturnRefused, halted
	}
	if f := a.opts.Moments.SessionStart; f != nil {
		a.mu.Lock()
		ran, empty, model := a.startRan, !a.sent, a.model.Info.ID
		a.mu.Unlock()
		if !ran {
			source := a.opts.StartSource
			if source == "" {
				source = "startup"
			}
			parts, err, ok := await(ctx, a, "the session-start moment", func(ctx context.Context) ([]Part, error) {
				return f(ctx, StartInfo{Source: source, Model: model, Empty: empty})
			})
			if ctx.Err() != nil || !ok {
				return false, ReturnStopped, stopped
			}
			var h *StopWork
			if errors.As(err, &h) {
				// The start said stop: this prompt and every one after it
				// are refused, with its reason.
				reason := firstNonEmpty(h.Reason, "the session's start stopped it")
				a.mu.Lock()
				a.startRan, a.startHalt = true, reason
				a.mu.Unlock()
				return false, ReturnRefused, reason
			}
			if err != nil {
				a.notice(LevelWarning, "session-start", fmt.Sprintf(
					"The session-start moment could not run (%v). The prompt goes ahead without what it would have added.", err))
			}
			a.mu.Lock()
			a.startRan = true
			a.startAdd = append(a.startAdd, parts...)
			a.mu.Unlock()
		}
	}

	f := a.opts.Moments.PromptSubmit
	if s.prompt.Hidden || f == nil {
		if ctx.Err() != nil {
			return false, ReturnStopped, stopped
		}
		return true, "", ""
	}
	adm, err, ok := await(ctx, a, "the prompt's check", func(ctx context.Context) (Admission, error) {
		return f(ctx, s.prompt)
	})
	switch {
	case ctx.Err() != nil || !ok:
		return false, ReturnStopped, stopped
	case err != nil:
		return false, ReturnRefused, fmt.Sprintf(
			"the prompt could not be checked (%v), and a check that could not run has not passed, so it was not sent", err)
	case adm.Refused:
		reason := adm.Reason
		if reason == "" {
			reason = "a prompt hook refused it without saying why"
		}
		return false, ReturnRefused, reason
	}
	s.added = adm.Parts
	return true, "", ""
}

// settleOnce makes sure the log has been settled before a turn builds on
// it. A log that crashes while it is read is a log that could not be read.
func (a *Agent) settleOnce() (err error) {
	if a.settled {
		return nil
	}
	defer func() {
		if p := recover(); p != nil {
			a.panicked("the log", p, nil)
			err = fmt.Errorf("the session's log could not be read: it crashed (%v)", p)
		}
	}()
	return a.settle()
}

// settle closes what an earlier process left open in the log, so that it
// is a conversation a turn can build on: a tool call without a result gets
// one that says gnosh was stopped, a turn without an end gets one, and a
// prompt that was accepted and never sent comes back to the person.
func (a *Agent) settle() error {
	v, err := a.view()
	if err != nil {
		return err
	}

	// Read back to the end of the turn before last. Everything older is
	// settled: a queued prompt is taken by the next turn at the latest.
	var tail []Entry
	ends := 0
	var readErr error
	a.logMu.Lock()
	for e, err := range a.opts.Log.Recent(a.bg) {
		if err != nil {
			readErr = fmt.Errorf("the session's log could not be read: %w", err)
			break
		}
		if e.Kind == EntryTurn {
			ends++
			if ends == 2 {
				break
			}
		}
		tail = append(tail, e)
	}
	a.unlockLog()
	if readErr != nil {
		return readErr
	}
	slices.Reverse(tail)

	lastEnd := -1
	for i, e := range tail {
		if e.Kind == EntryTurn {
			lastEnd = i
		}
	}

	// The calls of the last reply that have no result, and whether a turn
	// was under way at all. What was stored between turns, such as a
	// summary the person asked for, names no turn and is not one.
	var open []core.Block
	live := false
	for _, e := range tail[lastEnd+1:] {
		switch e.Kind {
		case EntryAssistant:
			live = true
			open = nil
			if !e.Silent {
				open = e.Message.ToolUses()
			}
		case EntryResult:
			live = true
			open = slices.DeleteFunc(open, func(b core.Block) bool { return b.ID == e.ToolUseID })
		case EntryPrompt, EntryNote, EntrySummary:
			live = live || e.Turn != ""
		}
	}

	// The prompts that were accepted and neither sent nor taken back.
	waiting := map[string]Entry{}
	var order []string
	for _, e := range tail {
		switch e.Kind {
		case EntryQueued:
			if _, dup := waiting[e.ID]; !dup {
				order = append(order, e.ID)
			}
			waiting[e.ID] = e
		case EntryPrompt:
			delete(waiting, e.ID)
		case EntryUnqueued:
			delete(waiting, e.Prompt)
		}
	}

	var entries []Entry
	var names []string
	for _, u := range open {
		entries = append(entries, Entry{
			Kind: EntryResult, ToolUseID: u.ID, ToolName: u.Name,
			Result: core.ErrorResult(a.texts.Killed), Interrupted: true,
		})
		names = append(names, u.Name)
	}
	detail := ""
	if live {
		detail = "gnosh was stopped before this turn finished"
		if a.opened {
			// It is this process's own turn, which it could not write
			// the end of at the time.
			detail = "the end of this turn could not be written when it ended"
		}
		if len(names) > 0 {
			detail += "; these calls were left without a result: " + strings.Join(names, ", ")
		}
		entries = append(entries, Entry{Kind: EntryTurn, Reason: EndInterrupted, Text: detail})
	}
	var back []Entry
	if !a.opened {
		// (In a session this process opened, a prompt written down as
		// queued and not sent is still in its line, or in the turn that is
		// starting: only a process that died leaves one behind. Returned
		// here after a turn's end could not be written, it went out all
		// the same, and twice once the person sent it again (the agent
		// loop review of 2026-11-19).)
		for _, id := range order {
			if e, ok := waiting[id]; ok {
				back = append(back, e)
				entries = append(entries, Entry{Kind: EntryUnqueued, Prompt: id, Text: "returned when the session was reopened"})
			}
		}
	}
	if err := a.appendLog(entries...); err != nil {
		return err
	}

	if live && !a.opened {
		a.emit(&NoticeEvent{Level: LevelInfo, Source: "loop", Text: "When this session was last open, " + detail + "."})
	}
	a.opened = true
	// The tool calls the conversation already holds keep their ids to
	// themselves.
	for _, m := range v.Messages {
		for _, u := range m.ToolUses() {
			if a.calls == nil {
				a.calls = map[string]bool{}
			}
			a.calls[u.ID] = true
		}
	}
	for _, e := range back {
		if e.Hidden {
			continue
		}
		a.emit(&PromptReturnedEvent{
			ID: e.ID, Prompt: promptOf(e), Why: ReturnRestart,
			Reason: "gnosh was stopped before this prompt was sent",
		})
	}

	// A prompt went out before when the log holds a reply, a prompt, a
	// turn's end or a summary. Notes alone, written between turns (Record),
	// are not one: the first prompt still carries what a first prompt does.
	before := false
	for _, m := range v.Messages {
		if m.Role == core.RoleAssistant {
			before = true
			break
		}
	}
	for _, e := range tail {
		if before {
			break
		}
		switch e.Kind {
		case EntryPrompt, EntryAssistant, EntryTurn, EntrySummary:
			before = true
		}
	}
	a.mu.Lock()
	a.sent = a.sent || before
	a.mu.Unlock()
	a.settled = true
	return nil
}

// promptOf rebuilds a prompt from the blocks an entry kept of it.
func promptOf(e Entry) Prompt {
	p := Prompt{ID: e.ID, Hidden: e.Hidden, Source: e.Source}
	for i, b := range e.Blocks {
		if i == 0 && b.Type == core.BlockText {
			p.Text = b.Text
			continue
		}
		p.Attachments = append(p.Attachments, b)
	}
	return p
}
