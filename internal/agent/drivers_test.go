package agent

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gnosh-ai/gnosh/internal/core"
)

// Each driver may send the model back when a turn is about to end. They
// are asked in order, the first that objects is the only one asked that
// time, and the whole of it is one turn with one end.
func TestDriversSendTheModelBack(t *testing.T) {
	var mu sync.Mutex
	var trace []string
	var ends []TurnEnd
	scripted := func(name string, verdicts ...Verdict) Driver {
		i := 0
		return Driver{Name: name, Decide: func(_ context.Context, end TurnEnd) (Verdict, error) {
			mu.Lock()
			defer mu.Unlock()
			trace = append(trace, name+" on "+end.Text)
			ends = append(ends, end)
			if i >= len(verdicts) {
				return Verdict{}, nil
			}
			i++
			return verdicts[i-1], nil
		}}
	}
	r := newRig(t, func(o *Options) {
		o.Drivers = []Driver{
			scripted("close-check", Verdict{Action: Continue, Prompt: "<own>mem refused to close `x`: do its work.</own>"}),
			scripted("goal",
				Verdict{Action: Continue, Prompt: "<own>The goal is not met: A is missing.</own>"},
				Verdict{Action: Continue, Prompt: "<own>The goal is not met: B is missing.</own>"},
				Verdict{Notice: "Goal met: the tests pass"}),
			scripted("stop-hook", Verdict{Action: Continue, Prompt: "<own>Stop hook: run gofmt.</own>", Objection: "gofmt has not run"}),
		}
	})
	r.model.push(say("done 1"), say("done 2"), say("done 3"), say("done 4"), say("done 5"))

	res := r.run("finish the work")
	if res.Reason != EndCompleted || res.Text != "done 5" || res.Rounds != 5 {
		t.Fatalf("result = %+v", res)
	}
	mu.Lock()
	want := []string{
		"close-check on done 1",
		"close-check on done 2", "goal on done 2",
		"close-check on done 3", "goal on done 3",
		"close-check on done 4", "goal on done 4", "stop-hook on done 4",
		"close-check on done 5", "goal on done 5", "stop-hook on done 5",
	}
	if !slices.Equal(trace, want) {
		t.Errorf("drivers were asked as\n%v\nwant\n%v", trace, want)
	}
	// What a Stop hook needs: whether the turn is only still running
	// because it was sent back, and by whom.
	if ends[0].Streak != 0 || ends[0].SentBack || ends[0].Marker != StopMarker || ends[0].Reply.Text() != "done 1" {
		t.Errorf("the first asking = %+v", ends[0])
	}
	if e := ends[4]; e.Streak != 1 || !e.SentBack { // goal on done 3
		t.Errorf("the goal driver's second asking = %+v", e)
	}
	if e := ends[7]; e.Streak != 0 || !e.SentBack { // stop-hook on done 4
		t.Errorf("the stop hook's first asking = %+v", e)
	}
	if e := ends[10]; e.Streak != 1 { // stop-hook on done 5
		t.Errorf("the stop hook's second asking = %+v", e)
	}
	mu.Unlock()

	var sources []string
	for _, e := range r.log.of(EntryPrompt) {
		if e.Hidden {
			sources = append(sources, e.Source+": "+core.TextOf(e.Blocks))
		}
	}
	wantPrompts := []string{
		"driver:close-check: <own>mem refused to close `x`: do its work.</own>",
		"driver:goal: <own>The goal is not met: A is missing.</own>",
		"driver:goal: <own>The goal is not met: B is missing.</own>",
		"driver:stop-hook: <own>Stop hook: run gofmt.</own>",
	}
	if !slices.Equal(sources, wantPrompts) {
		t.Errorf("the prompts gnosh sent for itself = %q, want %q", sources, wantPrompts)
	}
	if n := len(eventsOf[*TurnEndEvent](r.rec)); n != 1 {
		t.Errorf("%d turn ends, want exactly one", n)
	}
	devs := eventsOf[*DriverEvent](r.rec)
	if len(devs) != 4 || devs[3].Name != "stop-hook" || devs[3].Objection != "gofmt has not run" || devs[3].Action != Continue || devs[2].Streak != 2 {
		t.Errorf("driver events = %+v", devs)
	}
	if w := notices(r.rec, LevelWarning); len(w) != 1 || w[0] != "Goal met: the tests pass" {
		t.Errorf("warnings = %v", w)
	}
	r.checkLog()
}

// No count ends the work. Every so many send-backs in a row the model is
// asked to judge whether there is a real reason to stop; if it says so
// and ends its reply with the marker, the turn ends, and the person is
// shown its reason and what the driver objected to. The marker itself is
// not shown.
func TestCheckpointAndTheModelChoosingToStop(t *testing.T) {
	var checkpoints []bool
	r := newRig(t, func(o *Options) {
		o.Drivers = []Driver{{Name: "stop-hook", Every: 3, Decide: func(_ context.Context, end TurnEnd) (Verdict, error) {
			checkpoints = append(checkpoints, end.Checkpoint)
			return Verdict{Action: Continue, Prompt: "<hook>The tests must pass before you stop.</hook>", Objection: "the tests do not pass"}, nil
		}}}
	})
	r.model.push(
		say("first try"),
		say("still failing"),
		say("still failing, again"),
		sayIn("The tests need a database that is not on this machine.\n\n", "<!-- GNOSH", "_STOP -->", "\n"),
	)
	res := r.run("make the tests pass") // bounded by the rig
	if res.Reason != EndModelStopped {
		t.Fatalf("result = %+v", res)
	}
	if res.Detail != "The tests need a database that is not on this machine." || res.Objection != "the tests do not pass" || res.Driver != "stop-hook" {
		t.Errorf("result = %+v", res)
	}
	if res.Text != "The tests need a database that is not on this machine." {
		t.Errorf("the answer still holds the marker: %q", res.Text)
	}
	if !slices.Equal(checkpoints, []bool{false, false, true}) {
		t.Errorf("the driver was told of checkpoints as %v", checkpoints)
	}

	// The third send-back is the checkpoint: the question first, then
	// what holds the turn open, in the driver's own words.
	reqs := r.model.requests()
	if len(reqs) != 4 {
		t.Fatalf("%d requests", len(reqs))
	}
	blocks := userBlocks(reqs[3])
	cp := blocks[len(blocks)-1]
	if !strings.HasPrefix(cp, "<gnosh>\nDecide this first") || !strings.Contains(cp, StopMarker) ||
		!strings.Contains(cp, "held open 3 times in a row") || !strings.HasSuffix(cp, "<hook>The tests must pass before you stop.</hook>") {
		t.Errorf("the checkpoint = %q", cp)
	}
	if plain := userBlocks(reqs[2]); plain[len(plain)-1] != "<hook>The tests must pass before you stop.</hook>" {
		t.Errorf("an ordinary send-back = %q", plain[len(plain)-1])
	}

	// The marker is not shown, but the model's words are kept as written.
	var shown strings.Builder
	for _, e := range eventsOf[*TextEvent](r.rec) {
		shown.WriteString(e.Text)
	}
	if strings.Contains(shown.String(), "GNOSH") || !strings.Contains(shown.String(), "not on this machine.") {
		t.Errorf("the person was shown %q", shown.String())
	}
	as := r.log.of(EntryAssistant)
	if !strings.Contains(as[len(as)-1].Message.Text(), StopMarker) {
		t.Error("the stored reply lost the marker")
	}
	replies := eventsOf[*ReplyEvent](r.rec)
	if replies[len(replies)-1].Marker != StopMarker {
		t.Errorf("the reply event does not name the marker: %+v", replies[len(replies)-1])
	}
	w := notices(r.rec, LevelWarning)
	if len(w) != 1 || !strings.Contains(w[0], "The model chose to stop") ||
		!strings.Contains(w[0], "a database that is not on this machine") || !strings.Contains(w[0], "the tests do not pass") {
		t.Errorf("warnings = %v", w)
	}
	devs := eventsOf[*DriverEvent](r.rec)
	if len(devs) != 3 || devs[0].Checkpoint || !devs[2].Checkpoint || devs[2].Streak != 3 {
		t.Errorf("driver events = %+v", devs)
	}
	r.checkLog()
}

// The marker counts only in the reply to a checkpoint. At any other time
// the driver stays in charge and the marker is just text.
func TestMarkerOutsideACheckpointIsJustText(t *testing.T) {
	asked := 0
	r := newRig(t, func(o *Options) {
		o.Drivers = []Driver{{Name: "stop-hook", Decide: func(context.Context, TurnEnd) (Verdict, error) {
			asked++
			if asked == 1 {
				return Verdict{Action: Continue, Prompt: "not yet"}, nil
			}
			return Verdict{}, nil
		}}}
	})
	r.model.push(say("I give up.\n"+StopMarker), say("fine, done properly"))
	res := r.run("do it")
	if res.Reason != EndCompleted || res.Text != "fine, done properly" || asked != 2 {
		t.Fatalf("result = %+v, the driver was asked %d times", res, asked)
	}
	var shown strings.Builder
	for _, e := range eventsOf[*TextEvent](r.rec) {
		shown.WriteString(e.Text)
	}
	if !strings.Contains(shown.String(), StopMarker) {
		t.Errorf("text that is not a marker was held back: %q", shown.String())
	}
}

// A model that finds no reason to stop at a checkpoint carries on, and the
// count goes on to the next checkpoint. A marker beside a tool call does
// not count: a reply that asks for a tool is not an ending.
func TestCheckpointDeclined(t *testing.T) {
	asked := 0
	r := newRig(t, func(o *Options) {
		o.Tools = []core.Tool{tool("Bash", nil)}
		o.Drivers = []Driver{{Name: "goal", Every: 2, Marker: "<!-- STOP_GOAL -->", Decide: func(_ context.Context, end TurnEnd) (Verdict, error) {
			asked++
			if asked > 5 {
				return Verdict{}, nil
			}
			if end.Marker != "<!-- STOP_GOAL -->" {
				return Verdict{}, errors.New("the driver was not told its own marker")
			}
			v := Verdict{Action: Continue, Prompt: "the goal is not met"}
			if end.Checkpoint {
				v.Checkpoint = "CHECKPOINT of the goal's own: stop with " + end.Marker + " if there is a reason."
			}
			return v, nil
		}}}
	})
	withMarkerAndCall := use(tc("c1", "Bash", `{}`))
	withMarkerAndCall.evs = append([]core.Event{{Type: core.EventTextDelta, Text: "Let me check once more.\n<!-- STOP_GOAL -->\n"}}, withMarkerAndCall.evs...)
	r.model.push(
		say("try 1"),
		say("try 2"),      // answers the first send-back
		withMarkerAndCall, // answers the checkpoint, and asks for a tool: not an ending
		say("try 3"),
		say("try 4"),                          // answers the third send-back
		say("no reason to stop; carrying on"), // answers the second checkpoint without the marker
		say("try 5"),
	)
	res := r.run("meet the goal")
	if res.Reason != EndCompleted || res.Text != "try 5" {
		t.Fatalf("result = %+v", res)
	}
	var own []string
	for _, e := range r.log.of(EntryPrompt) {
		if e.Hidden {
			own = append(own, core.TextOf(e.Blocks))
		}
	}
	want := []string{
		"the goal is not met",
		"CHECKPOINT of the goal's own: stop with <!-- STOP_GOAL --> if there is a reason.",
		"the goal is not met",
		"CHECKPOINT of the goal's own: stop with <!-- STOP_GOAL --> if there is a reason.",
		"the goal is not met",
	}
	if !slices.Equal(own, want) {
		t.Errorf("send-backs = %q, want %q", own, want)
	}
	r.checkLog()
}

// A turn-end check that could not run has not passed: the model is sent
// back, the person is told, and the checkpoint remains the way out.
func TestDriverThatCannotRunFailsClosed(t *testing.T) {
	asked := 0
	r := newRig(t, func(o *Options) {
		o.Drivers = []Driver{{Name: "stop-hook", Decide: func(context.Context, TurnEnd) (Verdict, error) {
			asked++
			if asked == 1 {
				return Verdict{}, errors.New("ffprobe is not installed")
			}
			return Verdict{}, nil
		}}}
	})
	r.model.push(say("rendered the video"), say("the check cannot run here; the video is at out.mp4"))
	res := r.run("render it")
	if res.Reason != EndCompleted || res.Rounds != 2 {
		t.Fatalf("result = %+v", res)
	}
	sent := ownPrompts(r.log, "driver:stop-hook")
	if len(sent) != 1 || !strings.Contains(sent[0], `The check "stop-hook" could not run: ffprobe is not installed`) || !strings.Contains(sent[0], "has not passed") {
		t.Errorf("the model was sent %q", sent)
	}
	w := notices(r.rec, LevelWarning)
	if len(w) != 1 || !strings.Contains(w[0], "could not run (ffprobe is not installed)") || !strings.Contains(w[0], "The turn goes on") {
		t.Errorf("warnings = %v", w)
	}
}

// A driver that ends the turn ends it whatever a later one would say.
func TestDriverHalt(t *testing.T) {
	later := 0
	r := newRig(t, func(o *Options) {
		o.Drivers = []Driver{
			{Name: "stop-hook", Decide: func(context.Context, TurnEnd) (Verdict, error) {
				return Verdict{Action: Halt, Objection: "a hook ended the turn: the budget is spent"}, nil
			}},
			{Name: "loop-like", Decide: func(context.Context, TurnEnd) (Verdict, error) {
				later++
				return Verdict{Action: Continue, Prompt: "keep going"}, nil
			}},
		}
	})
	r.model.push(say("half done"))
	res := r.run("work")
	if res.Reason != EndHalted || res.Driver != "stop-hook" || res.Detail != "a hook ended the turn: the budget is spent" || later != 0 {
		t.Fatalf("result = %+v, the later driver was asked %d times", res, later)
	}
	if res.Text != "half done" {
		t.Errorf("the answer so far was lost: %q", res.Text)
	}
	r.checkLog()
}

// loopModel answers /loop's prompts from a list of replies, one per turn.
func loopModel(replies ...string) func(*core.Request, int) step {
	var mu sync.Mutex
	i := 0
	return func(*core.Request, int) step {
		mu.Lock()
		defer mu.Unlock()
		if i >= len(replies) {
			return step{err: &core.ProviderError{Provider: "fake", Kind: core.ErrInvalidRequest, Message: "the loop went on past the test's replies"}}
		}
		i++
		return say(replies[i-1])
	}
}

// /loop keeps the session working, turn after turn, until the model shows
// that the work is done. A first claim of being done is questioned; only
// a claim made again in reply to the question ends the loop.
func TestLoopUntilDone(t *testing.T) {
	r := newRig(t, nil)
	r.model.answer(loopModel(
		"working on part one",
		"I will reply LOOP_DONE when the files exist.", // a mention is not a claim
		"all files written\nLOOP_DONE",                 // a first claim: questioned
		"oh, one is missing; writing it",               // no claim in reply to the question: the loop goes on
		"now they are all there\n**LOOP_DONE**",        // a claim again, dressed up: questioned again
		"a.txt b.txt c.txt, each listed above\n  LOOP_DONE  ",
	))
	r.a.SetLoop(true)
	r.idle() // bounded by the rig

	if r.a.Loop() {
		t.Fatal("/loop is still on")
	}
	sent := ownPrompts(r.log, "loop")
	if len(sent) != 6 {
		t.Fatalf("%d prompts were sent by the loop: %q", len(sent), sent)
	}
	kinds := make([]string, len(sent))
	for i, s := range sent {
		switch {
		case strings.Contains(s, "Before that is believed"):
			kinds[i] = "question"
		case strings.Contains(s, "Keep going with the task"):
			kinds[i] = "continue"
		default:
			kinds[i] = "?"
		}
	}
	if want := []string{"continue", "continue", "continue", "question", "continue", "question"}; !slices.Equal(kinds, want) {
		t.Errorf("the loop sent %v, want %v", kinds, want)
	}
	if n := len(eventsOf[*TurnEndEvent](r.rec)); n != 6 {
		t.Errorf("%d turns, want 6: one for each prompt, never two", n)
	}
	evs := eventsOf[*LoopEvent](r.rec)
	if len(evs) != 2 || !evs[0].On || evs[1].On || evs[1].Turns != 6 || !strings.Contains(evs[1].Why, "showed it when asked") {
		t.Errorf("loop events = %+v", evs)
	}
	if b, i := len(eventsOf[*BusyEvent](r.rec)), len(eventsOf[*IdleEvent](r.rec)); b != 1 || i != 1 {
		t.Errorf("%d busy and %d idle events: the loop is one busy stretch", b, i)
	}
	r.checkLog()
}

// Every so many turns the loop checks in instead of giving up: the model
// is asked to judge, and its answer is read from the reply to the
// check-in itself.
func TestLoopCheckIn(t *testing.T) {
	r := newRig(t, func(o *Options) { o.LoopEvery = 3 })
	r.model.answer(loopModel(
		"working",
		"still working; I might say LOOP_STOP later", // not a check-in: just text
		"I am blocked: the deploy key is only on the user's laptop.\nLOOP_STOP",
	))
	r.a.SetLoop(true)
	r.idle()

	sent := ownPrompts(r.log, "loop")
	if len(sent) != 3 || !strings.Contains(sent[2], "is there a genuine reason to stop") || !strings.Contains(sent[2], "turn 3 of the loop") {
		t.Fatalf("the loop sent %q", sent)
	}
	ends := eventsOf[*TurnEndEvent](r.rec)
	last := ends[len(ends)-1].Result
	if last.Reason != EndModelStopped || last.Driver != "loop" || !strings.Contains(last.Detail, "only on the user's laptop") || strings.Contains(last.Text, "LOOP_STOP") {
		t.Errorf("the last turn = %+v", last)
	}
	if r.a.Loop() {
		t.Error("/loop is still on after the model chose to stop")
	}
	found := false
	for _, w := range notices(r.rec, LevelWarning) {
		found = found || strings.Contains(w, "The model chose to stop")
	}
	if !found {
		t.Errorf("the person was not shown that the model chose to stop: %v", notices(r.rec, ""))
	}
}

// Switched on while a turn runs, /loop only arms: that turn's end sends
// the first prompt. An optional cap runs exactly that many turns.
func TestLoopArmedDuringATurnAndCapped(t *testing.T) {
	wait := make(chan struct{})
	r := newRig(t, func(o *Options) { o.LoopMax = 2 })
	r.model.push(step{wait: wait, evs: say("the person's own turn").evs})
	r.model.answer(loopModel("one", "two"))
	if _, err := r.a.Submit(Prompt{Text: "start the work"}); err != nil {
		t.Fatal(err)
	}
	waitEvent[*RequestEvent](t, r.rec, nil)
	r.a.SetLoop(true)
	r.a.SetLoop(true) // twice does not stack
	if len(ownPrompts(r.log, "loop")) != 0 {
		t.Error("a loop prompt went out while the turn was still running")
	}
	close(wait)
	r.idle()

	if n := len(ownPrompts(r.log, "loop")); n != 2 {
		t.Errorf("%d loop prompts, want exactly 2", n)
	}
	evs := eventsOf[*LoopEvent](r.rec)
	if len(evs) != 2 || evs[1].On || !strings.Contains(evs[1].Why, "the 2 turns it was given") {
		t.Errorf("loop events = %+v", evs)
	}
	if r.model.count() != 3 {
		t.Errorf("%d requests", r.model.count())
	}
}

// The loop stops when the person stops a turn, when a turn fails, and
// when it is switched off; and then it sends nothing more.
func TestLoopStops(t *testing.T) {
	t.Run("the person stops the turn", func(t *testing.T) {
		reached := make(chan struct{})
		r := newRig(t, nil)
		r.model.push(say("one"), step{at: once(reached), stall: true})
		r.a.SetLoop(true)
		<-reached
		r.a.Interrupt()
		r.idle()
		if r.a.Loop() || r.model.count() != 2 {
			t.Errorf("loop on = %v after %d requests", r.a.Loop(), r.model.count())
		}
		evs := eventsOf[*LoopEvent](r.rec)
		if len(evs) != 2 || evs[1].On || evs[1].Why != "the turn was stopped" {
			t.Errorf("loop events = %+v", evs)
		}
		r.checkLog()
	})
	t.Run("a turn fails", func(t *testing.T) {
		r := newRig(t, nil)
		r.model.push(say("one"), broken(core.ErrAuth, 401))
		r.a.SetLoop(true)
		r.idle()
		if r.a.Loop() || r.model.count() != 2 {
			t.Errorf("loop on = %v after %d requests", r.a.Loop(), r.model.count())
		}
		evs := eventsOf[*LoopEvent](r.rec)
		if len(evs) != 2 || evs[1].Why != "the turn failed" {
			t.Errorf("loop events = %+v", evs)
		}
	})
	t.Run("it is switched off", func(t *testing.T) {
		wait := make(chan struct{})
		r := newRig(t, nil)
		r.model.push(step{wait: wait, evs: say("one").evs})
		r.a.SetLoop(true)
		waitEvent[*RequestEvent](t, r.rec, nil)
		r.a.SetLoop(false)
		close(wait)
		r.idle()
		if r.model.count() != 1 || len(ownPrompts(r.log, "loop")) != 1 {
			t.Errorf("%d requests and %d loop prompts after the loop was switched off", r.model.count(), len(ownPrompts(r.log, "loop")))
		}
		evs := eventsOf[*LoopEvent](r.rec)
		if len(evs) != 2 || evs[1].Why != "it was switched off" {
			t.Errorf("loop events = %+v", evs)
		}
	})
	t.Run("a prompt of the person's runs first, and the loop goes on after it", func(t *testing.T) {
		wait := make(chan struct{})
		r := newRig(t, func(o *Options) { o.LoopMax = 2 })
		r.model.push(step{wait: wait, evs: say("loop turn one").evs}, say("the person's answer"), say("loop turn two"))
		r.a.SetLoop(true)
		waitEvent[*RequestEvent](t, r.rec, nil)
		r.a.Submit(Prompt{Text: "by the way, use tabs", Later: true})
		waitQueue(t, r, "by the way, use tabs")
		close(wait)
		r.idle()
		var order []string
		for _, e := range r.log.of(EntryPrompt) {
			order = append(order, fmt.Sprintf("%v:%s", e.Hidden, e.Source))
		}
		if want := []string{"true:loop", "false:", "true:loop"}; !slices.Equal(order, want) {
			t.Errorf("prompts went out as %v, want %v", order, want)
		}
	})
}

// A /loop whose prompt is refused (the session's start said stop) goes off
// with the reason: it sent its next prompt at once, refused the same way,
// again and again until Esc (the agent loop review of 2026-11-19).
func TestLoopOffWhenItsPromptIsRefused(t *testing.T) {
	r := newRig(t, func(o *Options) {
		o.Moments.SessionStart = func(context.Context, StartInfo) ([]Part, error) {
			return nil, &StopWork{Reason: "the license has run out"}
		}
	})
	if res := r.run("first"); res.Reason != EndRefused {
		t.Fatalf("first = %+v", res)
	}
	r.a.SetLoop(true)
	off := waitEvent(t, r.rec, func(e *LoopEvent) bool { return !e.On })
	if !strings.Contains(off.Why, "the license has run out") {
		t.Errorf("the loop went off because %q", off.Why)
	}
	time.Sleep(100 * time.Millisecond)
	if n := len(eventsOf[*PromptReturnedEvent](r.rec)); n > 3 {
		t.Errorf("%d prompts were refused", n)
	}
	if n := r.model.count(); n != 0 {
		t.Errorf("the model was asked %d times", n)
	}
}
