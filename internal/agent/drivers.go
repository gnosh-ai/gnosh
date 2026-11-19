package agent

import (
	"context"
	"fmt"
)

// driverState is what a turn remembers about one driver.
type driverState struct {
	streak int // how many times in a row it has sent the model back
}

// checkpoint is a question the model has been put: is there a real reason
// to stop? The reply that follows it is the only one in which the marker
// counts.
type checkpoint struct {
	driver    string
	marker    string
	objection string
}

// stopChoice is the model's answer to a checkpoint when it chose to stop.
type stopChoice struct {
	cp     *checkpoint
	reason string
}

// askDrivers asks each driver, in order, whether the turn may end. The
// first that sends the model back does so with a hidden prompt, and the
// rest are not asked this time. It reports whether the model was sent
// back, or how the turn ended instead.
func (a *Agent) askDrivers(ctx context.Context, t *turn, rep *reply) (bool, *outcome) {
	if len(a.opts.Drivers) == 0 {
		return false, nil
	}
	a.setPhase(PhaseEnding, "asking whether the turn may end")
	text := rep.msg.Text()
	for i, d := range a.opts.Drivers {
		st := &t.drv[i]
		every := d.Every
		if every <= 0 {
			every = 10
		}
		marker := d.Marker
		if marker == "" {
			marker = StopMarker
		}
		end := TurnEnd{
			Turn: t.id, Reply: rep.msg, Text: text,
			Streak: st.streak, SentBack: t.sentBack,
			Checkpoint: (st.streak+1)%every == 0, Marker: marker,
		}
		v, err, ok := await(ctx, a, "the turn-end check "+d.Name, func(ctx context.Context) (Verdict, error) {
			return d.Decide(ctx, end)
		})
		if ctx.Err() != nil || !ok {
			return false, stoppedOutcome()
		}
		if err != nil {
			// A check that could not run has not passed. The checkpoint is
			// the way out, so this can never hold a turn for ever.
			a.notice(LevelWarning, "driver:"+d.Name, fmt.Sprintf(
				"The turn-end check %q could not run (%v). The turn goes on, because a check that could not run has not passed.", d.Name, err))
			v = Verdict{
				Action:    Continue,
				Prompt:    a.mark(fmt.Sprintf(a.texts.DriverFailed, d.Name, err)),
				Objection: fmt.Sprintf("the check %q could not run: %v", d.Name, err),
			}
		}
		if v.Notice != "" {
			a.notice(LevelWarning, "driver:"+d.Name, v.Notice)
		}

		switch v.Action {
		case End:
			st.streak = 0
		case Halt:
			why := firstNonEmpty(v.Objection, v.Prompt, d.Name+" ended the turn")
			a.emit(&DriverEvent{Name: d.Name, Action: Halt, Objection: why, Streak: st.streak})
			return false, &outcome{reason: EndHalted, detail: why, driver: d.Name}
		case Continue:
			st.streak++
			t.sentBack = true
			// A send-back with no words would be an empty message, which
			// no model may be sent.
			if v.Prompt == "" {
				v.Prompt = a.mark(firstNonEmpty(v.Objection, "The turn may not end yet: the check "+d.Name+" objects."))
			}
			objection := firstNonEmpty(v.Objection, clip(oneLine(v.Prompt), 400))
			prompt := v.Prompt
			if end.Checkpoint {
				// The question comes first, so that the model answers it
				// rather than carrying on.
				if v.Checkpoint != "" {
					prompt = v.Checkpoint
				} else {
					prompt = a.mark(fmt.Sprintf(a.texts.Checkpoint, marker, st.streak)) + "\n\n" + v.Prompt
				}
				t.cp = &checkpoint{driver: d.Name, marker: marker, objection: objection}
			}
			a.emit(&DriverEvent{Name: d.Name, Action: Continue, Objection: objection, Streak: st.streak, Checkpoint: end.Checkpoint})
			if err := a.ownPrompt(t, "driver:"+d.Name, prompt); err != nil {
				return false, failedOutcome(err)
			}
			return true, nil
		default:
			a.notice(LevelWarning, "driver:"+d.Name, fmt.Sprintf("The turn-end check %q gave an answer gnosh does not know (%d). It is taken as letting the turn end.", d.Name, v.Action))
		}
	}
	return false, nil
}

// loopState is /loop: keep the session working on its task, turn after
// turn, until the model shows that the work is done. It is guarded by the
// session's lock.
type loopState struct {
	on         bool
	turns      int         // continuations sent since it was switched on
	questioned bool        // the last continuation questioned a claim of being done
	last       *TurnResult // how the turn before ended, when it ended well
}

// SetLoop switches /loop on or off. Switched on while a turn runs, it only
// arms: that turn's end sends the first "keep going". Switched on in an
// idle session, it sends one at once. It never sends two for one turn.
//
// The loop goes off by itself when the model reports the work done and,
// asked to show it, says so again; when the model stops at a check-in;
// when the person stops a turn; and when a turn is halted or fails.
func (a *Agent) SetLoop(on bool) {
	a.mu.Lock()
	if a.closing || a.loop.on == on {
		a.mu.Unlock()
		return
	}
	turns := a.loop.turns
	a.loop = loopState{on: on}
	var dropped []*submission
	if on {
		a.markBusyLocked()
	} else {
		// A "keep going" that is still waiting must not start one more
		// turn.
		dropped = a.dropOwnLocked(func(s *submission) bool { return s.prompt.Source == "loop" })
	}
	a.mu.Unlock()
	why := ""
	if !on {
		why = "it was switched off"
	}
	a.returnDropped(dropped, "/loop was switched off")
	a.emit(&LoopEvent{On: on, Turns: turns, Why: why})
	a.kick()
}

// dropOwnLocked takes the prompts gnosh queued for itself (Prompt.Own, and
// notes that were to start a turn) that match off the queue and returns
// them. One that is being admitted right now stays for the admitting
// goroutine to deal with. The caller holds the lock.
func (a *Agent) dropOwnLocked(match func(*submission) bool) []*submission {
	var dropped []*submission
	keep := a.queue[:0:0]
	for _, s := range a.queue {
		if s.own() && match(s) {
			s.taken = true
			dropped = append(dropped, s)
			continue
		}
		keep = append(keep, s)
	}
	a.queue = keep
	keepIn := a.intake[:0:0]
	for _, s := range a.intake {
		if s.own() && a.admitting != s && match(s) {
			s.taken = true
			dropped = append(dropped, s)
			continue
		}
		keepIn = append(keepIn, s)
	}
	a.intake = keepIn
	return dropped
}

// returnDropped tells everyone that prompts of gnosh's own will not go
// out.
func (a *Agent) returnDropped(dropped []*submission, why string) {
	for _, s := range dropped {
		a.mu.Lock()
		written, note := s.persisted, s.note
		a.mu.Unlock()
		if written {
			_ = a.appendLog(Entry{Kind: EntryUnqueued, Prompt: s.id, Text: why})
		}
		a.mu.Lock()
		if note {
			// A note that was to start a turn is not lost: it waits for
			// the next request like any other.
			a.notes = append(a.notes, Note{Text: s.prompt.Text, Source: s.prompt.Source, Notice: s.prompt.Notice})
		}
		a.mu.Unlock()
		if !note {
			a.emit(&PromptReturnedEvent{ID: s.id, Prompt: s.prompt, Why: ReturnStopped, Reason: why})
		}
		s.resolve(TurnResult{Prompts: []string{s.id}, Reason: EndWithdrawn, Detail: why})
	}
	if len(dropped) > 0 {
		a.emitQueue()
	}
}

// Loop reports whether /loop is on.
func (a *Agent) Loop() bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.loop.on
}

// loopOff switches /loop off and says why. It does nothing when the loop
// is not on.
func (a *Agent) loopOff(why string) {
	a.mu.Lock()
	if !a.loop.on {
		a.mu.Unlock()
		return
	}
	turns := a.loop.turns
	a.loop = loopState{}
	a.mu.Unlock()
	a.emit(&LoopEvent{On: false, Turns: turns, Why: why})
	a.notice(LevelInfo, "loop", "/loop is off: "+why+".")
}

// loopNext is called when the session has nothing left to do and /loop is
// on. It reads how the last turn ended and either sends the next prompt or
// switches the loop off.
func (a *Agent) loopNext() {
	a.mu.Lock()
	st := a.loop
	a.loop.last = nil
	a.mu.Unlock()
	if !st.on {
		return
	}

	// "Done" is the word alone on a line of the reply, and a first claim
	// is questioned: only a claim made again in reply to the question
	// ends the loop. The reply of a turn that ran before the loop's first
	// prompt knows nothing of the word and is not read for it.
	text := a.texts.LoopContinue
	questioned := false
	if st.turns > 0 && st.last != nil {
		claims := hasLine(st.last.Text, LoopDone)
		switch {
		case st.questioned && claims:
			a.loopOff("the model reported the work done, and showed it when asked")
			return
		case claims:
			text, questioned = a.texts.LoopQuestion, true
		}
	}
	if limit := a.opts.LoopMax; limit > 0 && st.turns >= limit {
		a.loopOff(fmt.Sprintf("it has run the %d turns it was given", limit))
		return
	}

	turns := st.turns + 1
	checkin := !questioned && turns%a.loopEvery == 0
	if checkin {
		// Every so many turns the model is asked to judge whether there is
		// a real reason to stop. No count ends the loop.
		text = fmt.Sprintf(a.texts.LoopCheckin, LoopStop, turns)
	}
	a.mu.Lock()
	if !a.loop.on {
		a.mu.Unlock()
		return
	}
	a.loop.turns = turns
	a.loop.questioned = questioned
	a.mu.Unlock()

	_, err := a.submit(Prompt{Text: a.mark(text), Hidden: true, Source: "loop", Own: true}, func(s *submission) { s.checkin = checkin })
	if err != nil {
		a.loopOff("the next prompt could not be sent")
	}
}
