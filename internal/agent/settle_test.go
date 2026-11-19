package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"iter"
	"slices"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/gnosh-ai/gnosh/internal/core"
)

// A process that was killed in the middle of a round leaves a reply whose
// calls have no results, a turn with no end, and perhaps a prompt that
// was accepted and never sent. The next process to open the session
// settles all three before anything else happens.
func TestKilledProcessIsSettledOnOpen(t *testing.T) {
	log := newMemLog()
	log.Append(context.Background(),
		Entry{Kind: EntryPrompt, ID: "p1", Turn: "t1", Blocks: []core.Block{core.Text("fix the bug")}},
		Entry{Kind: EntryAssistant, ID: "a1", Turn: "t1", StopReason: core.StopToolUse, Message: core.Message{Role: core.RoleAssistant, Blocks: []core.Block{
			core.Text("Let me look."),
			core.ToolUse("c1", "Read", json.RawMessage(`{"file_path":"/a"}`)),
			core.ToolUse("c2", "Edit", json.RawMessage(`{"file_path":"/a"}`)),
			core.ToolUse("c3", "Bash", json.RawMessage(`{"command":"go test"}`)),
		}}},
		Entry{Kind: EntryResult, ID: "r1", Turn: "t1", ToolUseID: "c1", ToolName: "Read", Result: core.TextResult("package a")},
		Entry{Kind: EntryQueued, ID: "q1", Blocks: []core.Block{core.Text("also update the README"), {Type: core.BlockImage, MediaType: "image/png", Data: []byte{1}}}},
		Entry{Kind: EntryQueued, ID: "q2", Hidden: true, Source: "task-notification", Blocks: []core.Block{core.Text("<note>job ended</note>")}},
		Entry{Kind: EntryQueued, ID: "q3", Blocks: []core.Block{core.Text("taken back before the kill")}},
		Entry{Kind: EntryUnqueued, ID: "u3", Prompt: "q3"},
	)

	r := newRig(t, func(o *Options) { o.Log = log })
	ret := waitEvent[*PromptReturnedEvent](t, r.rec, nil)
	r.drain()

	// The prompt that was accepted and never sent comes back, with its
	// attachment; the one gnosh had queued for itself is only let go.
	if ret.ID != "q1" || ret.Why != ReturnRestart || ret.Prompt.Text != "also update the README" || len(ret.Prompt.Attachments) != 1 {
		t.Errorf("returned prompt = %+v", ret)
	}
	if n := len(eventsOf[*PromptReturnedEvent](r.rec)); n != 1 {
		t.Errorf("%d prompts came back", n)
	}
	n := notices(r.rec, LevelInfo)
	if len(n) != 1 || !strings.Contains(n[0], "gnosh was stopped before this turn finished") || !strings.Contains(n[0], "Edit, Bash") {
		t.Errorf("the person was told %v", n)
	}

	// The calls that were left open have results that say what happened,
	// and the turn has an end.
	var settled []string
	for _, e := range log.of(EntryResult) {
		if e.Interrupted {
			settled = append(settled, e.ToolUseID)
			if !e.Result.IsError || !strings.Contains(core.TextOf(e.Result.Content), "gnosh was stopped before this call finished") {
				t.Errorf("result of %s = %+v", e.ToolUseID, e.Result)
			}
		}
	}
	if !slices.Equal(settled, []string{"c2", "c3"}) {
		t.Errorf("settled calls = %v", settled)
	}
	ends := log.of(EntryTurn)
	if len(ends) != 1 || ends[0].Reason != EndInterrupted {
		t.Errorf("turn ends = %+v", ends)
	}
	var let []string
	for _, e := range log.of(EntryUnqueued) {
		let = append(let, e.Prompt)
	}
	if !slices.Equal(let, []string{"q3", "q1", "q2"}) {
		t.Errorf("unqueued = %v", let)
	}
	if r.model.count() != 0 {
		t.Error("settling called the model")
	}
	checkLogValid(t, log)

	// And the conversation carries on from there.
	r.model.push(say("picking it up again"))
	res := r.run("where were we?")
	if res.Reason != EndCompleted {
		t.Fatalf("result = %+v", res)
	}
	req := r.model.requests()[0]
	if err := checkView(req.Messages); err != nil {
		t.Fatalf("the request after a kill is not valid: %v", err)
	}
	if got, want := userBlocks(req), []string{"result:c1", "result:c2", "result:c3", "where were we?"}; !slices.Equal(got, want) {
		t.Errorf("the request ends with %q, want %q", got, want)
	}
	checkLogValid(t, log)
}

// A log that ended cleanly is left exactly as it was.
func TestHealthyLogIsLeftAlone(t *testing.T) {
	r := newRig(t, nil)
	r.model.push(say("hello"))
	r.run("hi")
	r.a.Compact("") // a summary between turns belongs to no turn
	r.model.push(say("SUMMARY of a greeting"))
	r.idle()
	before := len(r.log.all())

	next := newRig(t, func(o *Options) { o.Log = r.log })
	next.model.push(say("again"))
	if res := next.run("hi again"); res.Reason != EndCompleted {
		t.Fatalf("result = %+v", res)
	}
	entries := r.log.all()
	for _, e := range entries[before:] {
		if e.Kind == EntryTurn && e.Reason == EndInterrupted {
			t.Errorf("a clean log was settled: %+v", e)
		}
	}
	if len(eventsOf[*PromptReturnedEvent](next.rec)) != 0 || len(notices(next.rec, "")) != 0 {
		t.Errorf("opening a clean log said something: %s", typesOf(next.rec.all()))
	}
	// SessionStart is told that the session is not empty.
	checkLogValid(t, r.log)
}

// flakyLog fails to be read the first time.
type flakyLog struct {
	*memLog
	reads atomic.Int32
}

func (l *flakyLog) Recent(ctx context.Context) iter.Seq2[Entry, error] {
	if l.reads.Add(1) == 1 {
		return func(yield func(Entry, error) bool) { yield(Entry{}, errors.New("input/output error")) }
	}
	return l.memLog.Recent(ctx)
}

// A log that cannot be read at open is said to be so, and read again when
// a prompt is sent.
func TestSettleIsTriedAgain(t *testing.T) {
	log := &flakyLog{memLog: newMemLog()}
	log.Append(context.Background(),
		Entry{Kind: EntryPrompt, ID: "p1", Turn: "t1", Blocks: []core.Block{core.Text("old prompt")}},
		Entry{Kind: EntryAssistant, ID: "a1", Turn: "t1", StopReason: core.StopToolUse, Message: core.Message{Role: core.RoleAssistant, Blocks: []core.Block{core.ToolUse("c1", "Read", nil)}}},
	)
	r := &rig{t: t, log: log.memLog, model: &fakeModel{name: "fake"}, rec: &recorder{}}
	a, err := New(Options{
		SessionID: "session-1", Model: Model{Provider: r.model, Info: core.ModelInfo{ID: "big", ContextWindow: 100000}},
		Log: log, Gate: AllowAll, OnEvent: r.rec.on, NewID: testID,
	})
	if err != nil {
		t.Fatal(err)
	}
	r.a = a
	defer a.Close()
	waitEvent(t, r.rec, func(n *NoticeEvent) bool {
		return n.Level == LevelError && strings.Contains(n.Text, "could not read this session's log") && strings.Contains(n.Text, "input/output error")
	})
	r.model.push(say("ok"))
	if res := r.run("go on"); res.Reason != EndCompleted {
		t.Fatalf("result = %+v", res)
	}
	if err := checkView(r.model.requests()[0].Messages); err != nil {
		t.Errorf("the request is not valid: %v", err)
	}
	checkLogValid(t, log.memLog)
}

// A log that cannot be written ends the turn with a plain explanation,
// and never leaves the loop spinning. When it can be written again, what
// the failed turn left open is settled before the next turn builds on it.
func TestLogThatCannotBeWritten(t *testing.T) {
	r := newRig(t, func(o *Options) { o.Tools = []core.Tool{tool("Bash", nil)} })
	r.model.push(use(tc("c", "Bash", `{}`)), say("never reached"))
	r.log.mu.Lock()
	r.log.failOn = func(e Entry) bool { return e.Kind == EntryResult }
	r.log.mu.Unlock()
	res := r.run("run it")
	if res.Reason != EndFailed || res.ErrorKind != "internal" || !strings.Contains(res.Err.Error(), "the session's log could not be written: the disk is full") {
		t.Fatalf("result = %+v", res)
	}
	if r.model.count() != 1 {
		t.Errorf("%d requests after the log failed", r.model.count())
	}

	r.log.mu.Lock()
	r.log.failOn = nil
	r.log.mu.Unlock()
	r.model.mu.Lock()
	r.model.script = nil
	r.model.mu.Unlock()
	r.model.push(say("back on a disk with room"))
	res = r.run("try again")
	if res.Reason != EndCompleted {
		t.Fatalf("after the log could be written again: %+v", res)
	}
	reqs := r.model.requests()
	if err := checkView(reqs[len(reqs)-1].Messages); err != nil {
		t.Errorf("the request after the failure is not valid: %v", err)
	}
	r.checkLog()
}

// A log that crashes as a turn's end is written does not take the session
// down or leave anyone waiting.
func TestLogThatCrashes(t *testing.T) {
	var crashes []string
	r := newRig(t, func(o *Options) {
		o.OnPanic = func(what string, v any, _ []byte) { crashes = append(crashes, what) }
	})
	r.model.push(say("an answer"), say("another"))
	r.log.mu.Lock()
	r.log.panicOn = func(e Entry) bool { return e.Kind == EntryTurn }
	r.log.mu.Unlock()
	res := r.run("ask")
	if res.Reason != EndFailed || res.ErrorKind != "internal" || !strings.Contains(res.Err.Error(), "internal error as the turn ended") {
		t.Fatalf("result = %+v", res)
	}
	r.idle()
	r.log.mu.Lock()
	r.log.panicOn = nil
	r.log.mu.Unlock()
	if res := r.run("ask again"); res.Reason != EndCompleted || res.Text != "another" {
		t.Fatalf("after the crash: %+v", res)
	}
	if len(crashes) != 1 || crashes[0] != "the turn's end" {
		t.Errorf("OnPanic was told %v", crashes)
	}
	// The session says it is idle a moment after the turn's result is
	// handed over.
	r.idle()
	checkEvents(t, r.rec.all())
	r.checkLog()
}

// Every tool call goes by an id of its own, also when the model server
// numbers the calls of every reply from zero, repeats an id within a
// reply, or gives none.
func TestToolCallIDsAreMadeUnique(t *testing.T) {
	r := newRig(t, func(o *Options) { o.Tools = []core.Tool{look("Read", nil)} })
	round := func() step {
		return use(tc("call_0", "Read", `{"n":0}`), tc("call_0", "Read", `{"n":1}`), tc("", "Read", `{"n":2}`))
	}
	r.model.push(round(), round(), say("done"))
	if res := r.run("read"); res.Reason != EndCompleted {
		t.Fatalf("result = %+v", res)
	}
	seen := map[string]bool{}
	for _, e := range r.log.of(EntryAssistant) {
		for _, u := range e.Message.ToolUses() {
			if u.ID == "" || seen[u.ID] {
				t.Errorf("the tool call id %q is empty or used twice", u.ID)
			}
			seen[u.ID] = true
		}
	}
	if len(seen) != 6 || !seen["call_0"] {
		t.Errorf("ids = %v", seen)
	}
	for i, req := range r.model.requests() {
		if err := checkView(req.Messages); err != nil {
			t.Errorf("request %d is not valid: %v", i, err)
		}
	}
	// Each call's arguments stayed with it.
	got, order := toolResults(r.model.requests()[1])
	for i, id := range order {
		if !strings.Contains(got[id], fmt.Sprintf(`{"n":%d}`, i)) {
			t.Errorf("call %s got %q", id, got[id])
		}
	}
	// The events name the calls by the ids they go by.
	inputs := map[string]string{}
	for _, e := range eventsOf[*ToolInputEvent](r.rec) {
		inputs[e.CallID] += e.Text
	}
	for _, e := range eventsOf[*ToolStartEvent](r.rec) {
		if inputs[e.CallID] != string(e.Input) {
			t.Errorf("the streamed arguments of %s are %q, and it started with %s", e.CallID, inputs[e.CallID], e.Input)
		}
	}
	r.checkLog()
}

// A prompt that waits in line while a turn's end cannot be written is not
// taken for one a dead gnosh left behind when the next turn settles the
// log: it was returned to the person ("gnosh was stopped before this
// prompt was sent") while it went out all the same (the agent loop review
// of 2026-11-19).
func TestQueuedPromptStaysAfterAFailedTurnEnd(t *testing.T) {
	reached, release := make(chan struct{}), make(chan struct{})
	r := newRig(t, nil)
	first := say("one")
	first.at, first.wait = once(reached), release
	r.model.push(first, say("two"))
	var failed atomic.Bool
	r.log.mu.Lock()
	r.log.failOn = func(e Entry) bool { return e.Kind == EntryTurn && failed.CompareAndSwap(false, true) }
	r.log.mu.Unlock()
	done1 := make(chan TurnResult, 1)
	go func() { done1 <- r.run("first") }()
	<-reached
	done2 := make(chan TurnResult, 1)
	go func() { done2 <- r.run("second") }()
	waitQueue(t, r, "second")
	close(release)
	<-done1
	if res := <-done2; res.Reason != EndCompleted || res.Text != "two" {
		t.Fatalf("second = %+v", res)
	}
	r.idle()
	if !failed.Load() {
		t.Fatal("the turn's end was written: the test did not do what it says")
	}
	if ret := eventsOf[*PromptReturnedEvent](r.rec); len(ret) != 0 {
		t.Errorf("prompts returned: %+v", ret)
	}
	if n := r.model.count(); n != 2 {
		t.Errorf("%d requests", n)
	}
}
