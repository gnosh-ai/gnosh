package agent

import (
	"context"
	"errors"
	"fmt"
	"math/rand/v2"
	"os"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gnosh-ai/gnosh/internal/core"
)

// dice is a random source that many goroutines may roll.
type dice struct {
	mu  sync.Mutex
	rng *rand.Rand
}

func newDice(seed uint64) *dice {
	return &dice{rng: rand.New(rand.NewPCG(seed, seed^0x9e3779b97f4a7c15))}
}

// n returns a number from 0 to max-1.
func (d *dice) n(max int) int {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.rng.IntN(max)
}

// chance reports true about percent times in a hundred.
func (d *dice) chance(percent float64) bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.rng.Float64()*100 < percent
}

// nap sleeps for up to max microseconds.
func (d *dice) nap(max int) {
	if us := d.n(max + 1); us > 0 {
		time.Sleep(time.Duration(us) * time.Microsecond)
	}
}

// Hundreds of rounds with tools that run side by side and finish at
// random moments, replies that fail half way, a gate that refuses and
// ends turns, hooks that send the model back, prompts and notes sent at
// any time, prompts taken back, and the person stopping turns at random.
// Under the race detector. At the end of every turn the log must be a
// conversation a model can be sent, with every tool call answered; no
// prompt may be lost; no request may be longer than the model's window;
// and when it is over no goroutine may be left behind.
func TestStress(t *testing.T) {
	seed := uint64(time.Now().UnixNano())
	if testing.Short() {
		seed = 1
	}
	if v := os.Getenv("GNOSH_STRESS_SEED"); v != "" {
		// (To play a failing run's seed again.)
		n, err := strconv.ParseUint(v, 10, 64)
		if err != nil {
			t.Fatalf("GNOSH_STRESS_SEED: %v", err)
		}
		seed = n
	}
	t.Logf("seed %d", seed)
	d := newDice(seed)

	const window = 12000
	log := &slideLog{memLog: newMemLog()}
	var (
		busy, released atomic.Int64
		turns          atomic.Int64
		badLog         atomic.Value
		callSeq        atomic.Int64
	)

	run := func() func(context.Context, core.ToolCall, *core.ToolEnv) (core.ToolResult, error) {
		return func(ctx context.Context, call core.ToolCall, env *core.ToolEnv) (core.ToolResult, error) {
			env.Progress(core.Progress{Stream: "stdout", Text: "start " + call.ID + "\n"})
			if d.chance(40) {
				select {
				case <-time.After(time.Duration(d.n(400)) * time.Microsecond):
				case <-ctx.Done():
					return core.ToolResult{}, ctx.Err()
				}
			}
			switch {
			case d.chance(1):
				panic("a tool crashed in the stress test")
			case d.chance(3):
				return core.ToolResult{}, errors.New("the sandbox hiccuped")
			case d.chance(6):
				return core.ErrorResult("exit status 1: " + call.ID), nil
			case d.chance(2):
				return core.ToolResult{}, nil // nothing at all
			}
			env.Progress(core.Progress{Stream: "stdout", Text: "done " + call.ID + "\n"})
			return core.TextResult(fmt.Sprintf("%s ok: %s", call.Name, strings.Repeat("line of output\n", d.n(30)))), nil
		}
	}
	tools := []core.Tool{
		look("Read", run()), look("Grep", run()), look("Glob", run()),
		tool("Edit", run()), tool("Bash", run()),
	}
	names := []string{"Read", "Grep", "Glob", "Read", "Read", "Edit", "Bash", "Nope"}

	r := newRig(t, func(o *Options) {
		o.Log = log
		o.Model.Info.ContextWindow = window
		o.System = []string{strings.Repeat("You are gnosh. ", 200)}
		o.Tools = tools
		o.LoopMax = 3
		o.OnPanic = func(string, any, []byte) {}
		o.Gate = GateFunc(func(ctx context.Context, req GateRequest) (Decision, error) {
			if d.chance(10) {
				d.nap(200)
			}
			switch {
			case d.chance(0.4):
				return Decision{Halt: true}, nil
			case d.chance(3):
				return Decision{Reason: "the stress test says no"}, nil
			case d.chance(1):
				return Decision{}, errors.New("the rules could not be read")
			case d.chance(1):
				return Decision{Allow: true, Input: []byte(`{"rewritten":true}`)}, nil
			}
			return Decision{Allow: true}, nil
		})
		o.Drivers = []Driver{{Name: "stop-hook", Every: 2, Decide: func(_ context.Context, end TurnEnd) (Verdict, error) {
			switch {
			case end.Streak < 3 && d.chance(15):
				return Verdict{Action: Continue, Prompt: "<hook>not done yet</hook>", Objection: "not done"}, nil
			case d.chance(1):
				return Verdict{}, errors.New("the hook could not run")
			case d.chance(0.5):
				return Verdict{Action: Halt, Objection: "a hook ended the turn"}, nil
			}
			return Verdict{}, nil
		}}}
		o.Moments.Busy = func(context.Context) func() {
			busy.Add(1)
			return func() { released.Add(1) }
		}
		o.Moments.AfterTool = func(_ context.Context, done ToolDone) (ToolVerdict, error) {
			switch {
			case d.chance(2):
				return ToolVerdict{Context: []Part{{Source: "PostToolUse", Text: "<hook>a formatter ran</hook>"}}}, nil
			case d.chance(0.5):
				return ToolVerdict{}, errors.New("the hook script is missing")
			case d.chance(0.2):
				return ToolVerdict{Halt: true, Reason: "a hook ended the turn"}, nil
			}
			return ToolVerdict{}, nil
		}
		o.Moments.PromptSubmit = func(_ context.Context, p Prompt) (Admission, error) {
			if d.chance(20) {
				d.nap(300)
			}
			if d.chance(3) {
				return Admission{Refused: true, Reason: "the stress test refuses this one"}, nil
			}
			return Admission{Parts: []Part{{Source: "hook", Text: "<hook>seen</hook>"}}}, nil
		}
		o.Moments.PromptLines = []func(context.Context, LineInfo) ([]Part, error){
			func(_ context.Context, info LineInfo) ([]Part, error) {
				if info.Prompt.Hidden {
					return nil, nil
				}
				return []Part{{Source: "mem-line", Text: "<gnosh>use mem</gnosh>"}}, nil
			},
		}
		o.Moments.TurnEnded = func(_ context.Context, res TurnResult) {
			turns.Add(1)
			// At the end of every turn the log is a valid conversation.
			if msg := logTrouble(log.memLog); msg != "" && badLog.Load() == nil {
				badLog.Store(fmt.Sprintf("after a turn that ended %q: %s", res.Reason, msg))
			}
		}
	})
	r.model.honest = true
	r.model.window = window
	r.model.answer(func(req *core.Request, n int) step {
		st := step{}
		piece := func(ev core.Event) { st.evs = append(st.evs, ev) }
		if isSummaryAsk(req) {
			return say("SUMMARY: the stress test read and edited some files.")
		}
		if d.chance(30) {
			piece(core.Event{Type: core.EventThinkingDelta, Text: "thinking about it. "})
		}
		switch {
		case d.chance(5):
			return step{err: &core.ProviderError{Provider: "fake", Kind: core.ErrServer, Status: 500, Message: "a random failure"}}
		case d.chance(3):
			piece(core.Event{Type: core.EventTextDelta, Text: "half an ans"})
			piece(core.Event{Type: core.EventToolUseStart, ID: fmt.Sprintf("cut%d", callSeq.Add(1)), Name: "Edit"})
			st.err = &core.ProviderError{Provider: "fake", Kind: core.ErrNetwork, Message: "connection reset"}
			return st
		case d.chance(3):
			return silence()
		case d.chance(1):
			st.stall = true // only a stop gets it going again
			return st
		}
		if d.chance(70) {
			if d.chance(40) {
				piece(core.Event{Type: core.EventTextDelta, Text: "Let me look. "})
			}
			var calls []core.ToolCall
			for i, k := 0, 1+d.n(8); i < k; i++ {
				input := `{"n":1}`
				if d.chance(2) {
					input = `{"n":1,"n":2}` // a key named twice
				}
				calls = append(calls, tc(fmt.Sprintf("c%d", callSeq.Add(1)), names[d.n(len(names))], input))
			}
			st.evs = append(st.evs, callEvents(calls...)...)
			piece(usageEv(0, 0))
			piece(stopEv(core.StopToolUse))
			return st
		}
		for i, k := 0, 1+d.n(6); i < k; i++ {
			piece(core.Event{Type: core.EventTextDelta, Text: fmt.Sprintf("answer part %d. ", i)})
		}
		piece(usageEv(0, 0))
		piece(stopEv(core.StopEndTurn))
		return st
	})

	// The person: sends prompts and notes, takes some back, stops turns.
	var mu sync.Mutex
	sent := map[string]string{} // id -> text of every prompt that was accepted
	backIDs := map[string]bool{}
	submit := func(p Prompt) {
		id, err := r.a.Submit(p)
		if err != nil {
			t.Errorf("Submit: %v", err)
			return
		}
		mu.Lock()
		sent[id] = p.Text
		mu.Unlock()
	}
	const prompts = 480
	stop := make(chan struct{})
	finished := make(chan struct{})
	stopper := make(chan struct{})
	go func() {
		defer close(finished)
		for i := 0; i < prompts; i++ {
			switch {
			case d.chance(8):
				submit(Prompt{Text: fmt.Sprintf("<note>hidden prompt %d</note>", i), Hidden: true, Source: "task-notification"})
			case d.chance(8):
				if err := r.a.Tell(Note{Text: fmt.Sprintf("<note>job %d ended</note>", i), Wake: d.chance(30)}); err != nil {
					t.Errorf("Tell: %v", err)
				}
			default:
				submit(Prompt{Text: fmt.Sprintf("prompt %d: do some work", i), Later: d.chance(10)})
			}
			if d.chance(6) {
				for _, p := range r.a.TakeQueued() {
					mu.Lock()
					backIDs[p.ID] = true
					mu.Unlock()
					if d.chance(50) {
						p.ID = ""
						submit(p) // sent again after editing
					}
				}
			}
			if i == prompts/2 {
				r.a.SetLoop(true)
			}
			if d.chance(4) {
				go r.a.Side(context.Background(), SideRequest{Purpose: "title", Prompt: "name it", Timeout: 50 * time.Millisecond})
			}
			if d.chance(25) {
				ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
				r.a.Wait(ctx)
				cancel()
			}
			d.nap(1500)
		}
	}()
	go func() {
		defer close(stopper)
		for {
			select {
			case <-stop:
				return
			case <-time.After(time.Duration(2+d.n(25)) * time.Millisecond):
			}
			if d.chance(25) {
				r.a.Interrupt()
			}
			_ = r.a.State()
			_ = r.a.Context()
			_ = r.a.Queued()
			_ = r.a.Usage()
		}
	}()

	// Everything that was sent gets its turn. The stopping goes on until
	// the session is idle, because a reply may stall until it is stopped.
	select {
	case <-finished:
	case <-time.After(90 * time.Second):
		t.Fatalf("the prompts were not all sent; the session is %+v; seed %d", r.a.State(), seed)
	}
	waitUntil := time.Now().Add(90 * time.Second)
	for {
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
		err := r.a.Wait(ctx)
		cancel()
		if err == nil {
			break
		}
		if time.Now().After(waitUntil) {
			t.Fatalf("the session did not go idle; it is %+v; seed %d", r.a.State(), seed)
		}
	}
	close(stop)
	<-stopper
	r.a.SetLoop(false)
	r.idle()
	r.a.Close()
	r.drain()

	if msg := badLog.Load(); msg != nil {
		t.Errorf("the log was not a valid conversation %s (seed %d)", msg, seed)
	}
	checkLogValid(t, log.memLog)
	evs := r.rec.all()
	checkEvents(t, evs)

	// Nothing the person sent was lost: every prompt went out to the
	// model or came back.
	went := map[string]bool{}
	for _, e := range log.of(EntryPrompt) {
		went[e.ID] = true
	}
	for _, e := range eventsOf[*PromptReturnedEvent](r.rec) {
		backIDs[e.ID] = true
	}
	mu.Lock()
	for id, text := range sent {
		if !went[id] && !backIDs[id] {
			t.Errorf("the prompt %q (%s) neither reached the model nor came back (seed %d)", text, id, seed)
		}
		if went[id] && backIDs[id] {
			t.Errorf("the prompt %q (%s) both reached the model and came back (seed %d)", text, id, seed)
		}
	}
	mu.Unlock()

	if b, rel := busy.Load(), released.Load(); b != rel || b == 0 {
		t.Errorf("the session became busy %d times and idle %d times", b, rel)
	}
	refused, longest := r.model.stats()
	if refused != 0 {
		t.Errorf("%d requests were longer than the model's window (seed %d)", refused, seed)
	}
	// How many rounds a run gets through depends on how busy the machine
	// is: the stops come by the clock, and a slow round is stopped more
	// often. So a low count is said and not failed; only a run that made no
	// request at all has tested nothing.
	requests := r.model.count()
	switch {
	case requests == 0:
		t.Errorf("no request was made, so nothing was tested (seed %d)", seed)
	case requests < 200:
		t.Logf("only %d requests were made, where a quiet machine makes hundreds: the machine is busy", requests)
	}
	results := len(log.of(EntryResult))
	ended := map[EndReason]int{}
	for _, e := range eventsOf[*TurnEndEvent](r.rec) {
		ended[e.Result.Reason]++
	}
	t.Logf("%d requests, %d turns (%v), %d tool results, %d events, %d slides, %d summaries, the longest request %d of %d tokens",
		requests, turns.Load(), ended, results, len(evs), len(eventsOf[*RoomEvent](r.rec)), len(log.of(EntrySummary)), longest, window)
	for _, e := range eventsOf[*TurnEndEvent](r.rec) {
		if e.Result.Reason == EndFailed {
			t.Logf("a turn failed: %v", e.Result.Err)
			break
		}
	}
}

// logTrouble says what is wrong with a log as a conversation, or nothing.
func logTrouble(l *memLog) string {
	v, _ := l.View(context.Background())
	if len(v.Messages) > 0 {
		if err := checkView(v.Messages); err != nil {
			return err.Error()
		}
	}
	results := map[string]int{}
	var calls []string
	open := false
	for _, e := range l.all() {
		switch e.Kind {
		case EntryAssistant:
			open = true
			if !e.Silent {
				for _, u := range e.Message.ToolUses() {
					calls = append(calls, u.ID)
				}
			}
		case EntryResult:
			open = true
			results[e.ToolUseID]++
		case EntryPrompt, EntryNote, EntrySummary:
			open = open || e.Turn != ""
		case EntryTurn:
			open = false
		}
	}
	for _, id := range calls {
		if results[id] != 1 {
			return fmt.Sprintf("tool call %s has %d results", id, results[id])
		}
	}
	if open {
		return "it ends in the middle of a turn"
	}
	return ""
}
