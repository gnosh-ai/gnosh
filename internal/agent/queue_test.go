package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gnosh-ai/gnosh/internal/core"
)

// heldTool is a tool that waits until the test lets it go.
func heldTool(name string) (t *fakeTool, started <-chan struct{}, letGo func()) {
	s := make(chan struct{})
	g := make(chan struct{})
	t = tool(name, func(ctx context.Context, _ core.ToolCall, _ *core.ToolEnv) (core.ToolResult, error) {
		once(s)()
		select {
		case <-g:
			return core.TextResult(name + " finished"), nil
		case <-ctx.Done():
			return core.ToolResult{}, ctx.Err()
		}
	})
	return t, s, once(g)
}

// waitQueue waits until the queue is shown holding exactly these prompts,
// all of them through their checks.
func waitQueue(t *testing.T, r *rig, texts ...string) *QueueEvent {
	t.Helper()
	return waitEvent(t, r.rec, func(q *QueueEvent) bool {
		if len(q.Queued) != len(texts) {
			return false
		}
		for i, p := range q.Queued {
			if p.Checking || p.Prompt.Text != texts[i] {
				return false
			}
		}
		return true
	})
}

// userBlocks describes the blocks of the last user message of a request.
func userBlocks(req *core.Request) []string {
	var out []string
	last := req.Messages[len(req.Messages)-1]
	for _, b := range last.Blocks {
		if b.Type == core.BlockToolResult {
			out = append(out, "result:"+b.ToolUseID)
		} else {
			out = append(out, b.Text)
		}
	}
	return out
}

// A prompt sent while a turn runs is queued, shown, and handed to the
// model between two rounds of that turn, with the lines of that moment.
// Its hook runs once, when it is sent.
func TestQueueFoldsIntoRunningTurn(t *testing.T) {
	bash, started, letGo := heldTool("Bash")
	var mu sync.Mutex
	hooked := map[string]int{}
	r := newRig(t, func(o *Options) {
		o.Tools = []core.Tool{bash}
		o.Moments.PromptSubmit = func(_ context.Context, p Prompt) (Admission, error) {
			mu.Lock()
			hooked[p.Text]++
			mu.Unlock()
			return Admission{Parts: []Part{{Source: "hook", Text: "<hook>" + p.Text + "</hook>"}}}, nil
		}
		o.Moments.PromptLines = []func(context.Context, LineInfo) ([]Part, error){
			func(_ context.Context, info LineInfo) ([]Part, error) {
				return []Part{{Source: "line", Text: fmt.Sprintf("<line folded=%v>", info.Folded)}}, nil
			},
		}
	})
	r.model.push(use(tc("b", "Bash", `{}`)), say("both done"))

	id1, err := r.a.Submit(Prompt{Text: "build it"})
	if err != nil {
		t.Fatal(err)
	}
	<-started
	id2, err := r.a.Submit(Prompt{Text: "and test it"})
	if err != nil {
		t.Fatal(err)
	}
	q := waitQueue(t, r, "and test it")
	if q.Queued[0].ID != id2 {
		t.Errorf("queue shown = %+v", q.Queued)
	}
	if got := r.a.Queued(); len(got) != 1 || got[0].ID != id2 {
		t.Errorf("Queued() = %+v", got)
	}
	letGo()
	r.idle()

	if n := len(eventsOf[*TurnStartEvent](r.rec)); n != 1 {
		t.Errorf("%d turns started, want 1", n)
	}
	ends := eventsOf[*TurnEndEvent](r.rec)
	if len(ends) != 1 || !slices.Equal(ends[0].Result.Prompts, []string{id1, id2}) || ends[0].Result.Text != "both done" {
		t.Errorf("turn ends = %+v", ends)
	}
	reqs := r.model.requests()
	if len(reqs) != 2 {
		t.Fatalf("%d requests", len(reqs))
	}
	want := []string{"result:b", "and test it", "<hook>and test it</hook>", "<line folded=true>"}
	if got := userBlocks(reqs[1]); !slices.Equal(got, want) {
		t.Errorf("what the model was handed between the rounds = %q, want %q", got, want)
	}
	if got, want := userBlocks(reqs[0]), []string{"build it", "<hook>build it</hook>", "<line folded=false>"}; !slices.Equal(got, want) {
		t.Errorf("first prompt = %q, want %q", got, want)
	}
	mu.Lock()
	if hooked["build it"] != 1 || hooked["and test it"] != 1 {
		t.Errorf("hooks ran %v times", hooked)
	}
	mu.Unlock()
	prompts := eventsOf[*PromptEvent](r.rec)
	if len(prompts) != 2 || prompts[0].Folded || !prompts[1].Folded || len(prompts[1].Parts) != 2 {
		t.Errorf("prompt events = %+v", prompts)
	}
	// "Accepted" means on disk: the queued prompt was written down before
	// it went out, under the id it then went out with.
	var kinds []string
	for _, e := range r.log.all() {
		if e.ID == id2 {
			kinds = append(kinds, string(e.Kind))
		}
	}
	if !slices.Equal(kinds, []string{"queued", "prompt"}) {
		t.Errorf("the queued prompt's entries = %v", kinds)
	}
	qs := eventsOf[*QueueEvent](r.rec)
	if len(qs[len(qs)-1].Queued) != 0 {
		t.Errorf("the queue is still shown after it went out: %+v", qs[len(qs)-1])
	}
	r.checkLog()
}

// If the turn ends first, the queue starts the next turn, and the session
// stays busy across the hand-over: the busy and idle moments fire exactly
// once each.
func TestQueueStartsNextTurnAndBusyIdle(t *testing.T) {
	var mu sync.Mutex
	var trace []string
	note := func(s string) {
		mu.Lock()
		trace = append(trace, s)
		mu.Unlock()
	}
	wait := make(chan struct{})
	r := newRig(t, func(o *Options) {
		o.Moments.Busy = func(context.Context) func() {
			note("busy")
			return func() { note("released") }
		}
		o.Moments.TurnEnded = func(_ context.Context, res TurnResult) { note("turn ended: " + res.Text) }
	})
	r.model.push(step{wait: wait, evs: say("one").evs}, say("two"))

	if _, err := r.a.Submit(Prompt{Text: "first"}); err != nil {
		t.Fatal(err)
	}
	waitEvent[*RequestEvent](t, r.rec, nil)
	if _, err := r.a.Submit(Prompt{Text: "second"}); err != nil {
		t.Fatal(err)
	}
	waitQueue(t, r, "second")
	close(wait)
	r.idle()

	mu.Lock()
	if want := []string{"busy", "turn ended: one", "turn ended: two", "released"}; !slices.Equal(trace, want) {
		t.Errorf("moments fired as %v, want %v", trace, want)
	}
	mu.Unlock()
	if b, i := len(eventsOf[*BusyEvent](r.rec)), len(eventsOf[*IdleEvent](r.rec)); b != 1 || i != 1 {
		t.Errorf("%d busy and %d idle events, want one each", b, i)
	}
	ends := eventsOf[*TurnEndEvent](r.rec)
	if len(ends) != 2 || ends[0].Turn == ends[1].Turn {
		t.Fatalf("turn ends = %+v", ends)
	}
	reqs := r.model.requests()
	if len(reqs) != 2 || lastUserText(reqs[1]) != "second\n" || len(reqs[1].Messages) != 3 {
		t.Errorf("second turn's request = %+v", reqs[1].Messages)
	}
	inOrder(t, r.rec.all(), "BusyEvent", "TurnStartEvent", "TurnEndEvent", "TurnStartEvent", "TurnEndEvent", "IdleEvent")

	// A prompt sent later starts a new busy stretch.
	r.model.push(say("three"))
	r.run("third")
	r.idle()
	mu.Lock()
	if n := len(trace); n != 7 || trace[4] != "busy" || trace[6] != "released" {
		t.Errorf("after a third prompt the moments are %v", trace)
	}
	mu.Unlock()
	r.checkLog()
}

// Up takes the person's queued prompts back, attachments included, and
// leaves gnosh's own where they are.
func TestTakeQueued(t *testing.T) {
	r := newRig(t, nil)
	if got := r.a.TakeQueued(); got != nil {
		t.Errorf("TakeQueued with nothing queued = %+v", got)
	}
	wait := make(chan struct{})
	r.model.push(step{wait: wait, evs: say("one").evs}, say("noted"))
	if _, err := r.a.Submit(Prompt{Text: "first"}); err != nil {
		t.Fatal(err)
	}
	waitEvent[*RequestEvent](t, r.rec, nil)

	image := core.Block{Type: core.BlockImage, MediaType: "image/png", Data: []byte{1, 2, 3}}
	gone := make(chan TurnResult, 1)
	go func() {
		res, _ := r.a.Run(context.Background(), Prompt{Text: "a", Attachments: []core.Block{image}})
		gone <- res
	}()
	waitQueue(t, r, "a")
	r.a.Submit(Prompt{Text: "b"})
	r.a.Submit(Prompt{Text: "<job>3 ended</job>", Hidden: true, Source: "task-notification"})
	waitQueue(t, r, "a", "b", "<job>3 ended</job>")

	taken := r.a.TakeQueued()
	if len(taken) != 2 || taken[0].Text != "a" || taken[1].Text != "b" || len(taken[0].Attachments) != 1 || taken[0].Attachments[0].MediaType != "image/png" {
		t.Fatalf("taken back = %+v", taken)
	}
	if res := <-gone; res.Reason != EndWithdrawn {
		t.Errorf("a prompt that was taken back ended as %+v", res)
	}
	if q := r.a.Queued(); len(q) != 1 || !q[0].Prompt.Hidden {
		t.Errorf("left in the queue: %+v", q)
	}
	close(wait)
	r.idle()

	reqs := r.model.requests()
	if len(reqs) != 2 || !strings.Contains(lastUserText(reqs[1]), "3 ended") {
		t.Fatalf("requests = %d", len(reqs))
	}
	for _, req := range reqs {
		for _, m := range req.Messages {
			for _, b := range m.Blocks {
				if m.Role == core.RoleUser && (b.Text == "a" || b.Text == "b") {
					t.Errorf("a prompt that was taken back reached the model")
				}
			}
		}
	}
	if q, u := len(r.log.of(EntryQueued)), len(r.log.of(EntryUnqueued)); q != 3 || u != 2 {
		t.Errorf("%d queued and %d unqueued entries, want 3 and 2", q, u)
	}
	r.checkLog()
}

// Stopping a turn drops the prompts gnosh queued for itself, or the turn
// would start itself again; what the person queued stays and goes out
// next.
func TestInterruptKeepsThePersonsQueue(t *testing.T) {
	reached := make(chan struct{})
	r := newRig(t, nil)
	r.model.push(step{at: once(reached), stall: true}, say("ok"))
	first := make(chan TurnResult, 1)
	go func() { first <- r.run("first") }()
	<-reached
	r.a.Submit(Prompt{Text: "next"})
	r.a.Submit(Prompt{Text: "<job>ended</job>", Hidden: true, Own: true, Source: "task-notification"})
	// A hidden prompt the person caused (a !command's output) is theirs:
	// it stays too (the agent loop review of 2026-11-19).
	r.a.Submit(Prompt{Text: "$ npm test: ok", Hidden: true, Source: "shell"})
	waitQueue(t, r, "next", "<job>ended</job>", "$ npm test: ok")
	r.a.Interrupt()
	if res := <-first; res.Reason != EndStopped {
		t.Fatalf("first turn = %+v", res)
	}
	r.idle()

	ret := eventsOf[*PromptReturnedEvent](r.rec)
	if len(ret) != 1 || !ret[0].Prompt.Hidden || ret[0].Why != ReturnStopped {
		t.Errorf("returned prompts = %+v", ret)
	}
	ends := eventsOf[*TurnEndEvent](r.rec)
	if len(ends) != 2 || ends[0].Result.Reason != EndStopped || ends[1].Result.Reason != EndCompleted {
		t.Fatalf("turn ends = %+v", ends)
	}
	var sent strings.Builder
	for _, m := range r.model.requests()[1].Messages {
		if m.Role == core.RoleUser {
			sent.WriteString(m.Text() + "\n")
		}
	}
	if text := sent.String(); !strings.Contains(text, "next") || strings.Contains(text, "ended") || !strings.Contains(text, "npm test: ok") {
		t.Errorf("the second turn was sent %q", text)
	}
	r.checkLog()
}

// What was queued behind a turn that failed still runs.
func TestQueueOutlivesFailure(t *testing.T) {
	wait := make(chan struct{})
	var reasons []EndReason
	var mu sync.Mutex
	r := newRig(t, func(o *Options) {
		o.Moments.TurnEnded = func(_ context.Context, res TurnResult) {
			mu.Lock()
			reasons = append(reasons, res.Reason)
			mu.Unlock()
		}
	})
	r.model.push(step{wait: wait, err: &core.ProviderError{Provider: "fake", Kind: core.ErrAuth, Status: 401, Message: "invalid key"}}, say("still here"))
	r.a.Submit(Prompt{Text: "first"})
	waitEvent[*RequestEvent](t, r.rec, nil)
	second := make(chan TurnResult, 1)
	go func() { second <- r.run("second") }()
	waitQueue(t, r, "second")
	close(wait)
	if res := <-second; res.Reason != EndCompleted || res.Text != "still here" {
		t.Fatalf("the queued prompt's turn = %+v", res)
	}
	r.idle()
	mu.Lock()
	if !slices.Equal(reasons, []EndReason{EndFailed, EndCompleted}) {
		t.Errorf("turns ended as %v", reasons)
	}
	mu.Unlock()
	r.checkLog()
}

// A prompt a hook refuses stores nothing and calls no model; it comes
// back with the reason. One refused while a turn runs leaves that turn
// alone.
func TestPromptRefused(t *testing.T) {
	bash, started, letGo := heldTool("Bash")
	r := newRig(t, func(o *Options) {
		o.Tools = []core.Tool{bash}
		o.Moments.PromptSubmit = func(_ context.Context, p Prompt) (Admission, error) {
			switch {
			case strings.Contains(p.Text, "password"):
				return Admission{Refused: true, Reason: "the prompt holds a password"}, nil
			case strings.Contains(p.Text, "broken"):
				return Admission{}, errors.New("hook script not found")
			}
			return Admission{}, nil
		}
	})

	res := r.run("my password is hunter2")
	if res.Reason != EndRefused || res.Detail != "the prompt holds a password" || res.Turn != "" {
		t.Fatalf("result = %+v", res)
	}
	res = r.run("broken hook")
	if res.Reason != EndRefused || !strings.Contains(res.Detail, "could not be checked (hook script not found)") {
		t.Fatalf("a check that could not run must refuse: %+v", res)
	}
	r.idle()
	if len(r.log.all()) != 0 || r.model.count() != 0 {
		t.Errorf("a refused prompt left %v and %d requests", r.log.kinds(), r.model.count())
	}
	ret := eventsOf[*PromptReturnedEvent](r.rec)
	if len(ret) != 2 || ret[0].Why != ReturnRefused || ret[0].Prompt.Text != "my password is hunter2" || ret[0].Reason != "the prompt holds a password" {
		t.Errorf("returned = %+v", ret)
	}
	if len(eventsOf[*TurnStartEvent](r.rec)) != 0 {
		t.Error("a turn started for a refused prompt")
	}

	// While a turn runs.
	r.model.push(use(tc("b", "Bash", `{}`)), say("built"))
	first := make(chan TurnResult, 1)
	go func() { first <- r.run("build") }()
	<-started
	if res := r.run("the password again"); res.Reason != EndRefused {
		t.Fatalf("result = %+v", res)
	}
	letGo()
	if res := <-first; res.Reason != EndCompleted || res.Text != "built" {
		t.Fatalf("the running turn was disturbed: %+v", res)
	}
	r.checkLog()
}

// Prompts keep the order they were sent in, however long each one's hooks
// take.
func TestPromptsKeepTheirOrder(t *testing.T) {
	var mu sync.Mutex
	var checked []string
	wait := make(chan struct{})
	r := newRig(t, func(o *Options) {
		o.Moments.PromptSubmit = func(_ context.Context, p Prompt) (Admission, error) {
			if p.Text == "slow" {
				time.Sleep(15 * time.Millisecond)
			}
			mu.Lock()
			checked = append(checked, p.Text)
			mu.Unlock()
			return Admission{}, nil
		}
	})
	r.model.push(step{wait: wait, evs: say("one").evs}, say("rest"))
	r.a.Submit(Prompt{Text: "first"})
	waitEvent[*RequestEvent](t, r.rec, nil)
	for _, text := range []string{"slow", "quick", "last"} {
		r.a.Submit(Prompt{Text: text})
	}
	waitQueue(t, r, "slow", "quick", "last")
	var queued []string
	for _, q := range r.a.Queued() {
		queued = append(queued, q.Prompt.Text)
	}
	if !slices.Equal(queued, []string{"slow", "quick", "last"}) {
		t.Errorf("queue = %v", queued)
	}
	close(wait)
	r.idle()
	mu.Lock()
	if !slices.Equal(checked, []string{"first", "slow", "quick", "last"}) {
		t.Errorf("hooks ran as %v", checked)
	}
	mu.Unlock()
	if got := userBlocks(r.model.requests()[1]); !slices.Equal(got, []string{"slow", "quick", "last"}) {
		t.Errorf("the next turn was sent %q", got)
	}
	r.checkLog()
}

// A prompt gnosh sends for itself starts a turn, is stored like any
// prompt, is not shown as typed, and does not go through the prompt hook.
func TestOwnPromptStartsATurn(t *testing.T) {
	hooked := 0
	r := newRig(t, func(o *Options) {
		o.Moments.PromptSubmit = func(context.Context, Prompt) (Admission, error) {
			hooked++
			return Admission{}, nil
		}
	})
	r.model.push(say("I see the job ended"))
	res := r.runPrompt(Prompt{Text: "<note>job 3 ended with exit code 0</note>", Hidden: true, Source: "task-notification", Notice: "Background job 3 ended."})
	if res.Reason != EndCompleted {
		t.Fatalf("result = %+v", res)
	}
	if hooked != 0 {
		t.Error("the prompt hook ran on a prompt of gnosh's own")
	}
	p := r.log.of(EntryPrompt)
	if len(p) != 1 || !p[0].Hidden || p[0].Source != "task-notification" {
		t.Errorf("prompt entry = %+v", p)
	}
	if ev := eventsOf[*PromptEvent](r.rec); len(ev) != 1 || !ev[0].Prompt.Hidden {
		t.Errorf("prompt event = %+v", ev)
	}
	if n := notices(r.rec, LevelInfo); len(n) != 1 || n[0] != "Background job 3 ended." {
		t.Errorf("the quiet line = %v", n)
	}
	if lastUserText(r.model.requests()[0]) != "<note>job 3 ended with exit code 0</note>\n" {
		t.Errorf("sent %q", lastUserText(r.model.requests()[0]))
	}
}

// The session-start moment runs once, before the first prompt's own hook;
// what it adds goes out with the first prompt that is sent, neither lost
// nor sent twice.
func TestSessionStart(t *testing.T) {
	var mu sync.Mutex
	var trace []string
	var info StartInfo
	r := newRig(t, func(o *Options) {
		o.StartSource = "resume"
		o.Moments.SessionStart = func(_ context.Context, i StartInfo) ([]Part, error) {
			mu.Lock()
			trace = append(trace, "start")
			info = i
			mu.Unlock()
			return []Part{{Source: "SessionStart", Text: "<start>context</start>"}}, nil
		}
		o.Moments.PromptSubmit = func(_ context.Context, p Prompt) (Admission, error) {
			mu.Lock()
			trace = append(trace, "submit "+p.Text)
			mu.Unlock()
			if p.Text == "refuse me" {
				return Admission{Refused: true, Reason: "no"}, nil
			}
			return Admission{Parts: []Part{{Source: "hook", Text: "<hook/>"}}}, nil
		}
	})
	if res := r.run("refuse me"); res.Reason != EndRefused {
		t.Fatalf("result = %+v", res)
	}
	r.model.push(say("one"), say("two"))
	r.run("first")
	r.run("second")

	mu.Lock()
	if want := []string{"start", "submit refuse me", "submit first", "submit second"}; !slices.Equal(trace, want) {
		t.Errorf("moments ran as %v, want %v", trace, want)
	}
	if info.Source != "resume" || info.Model != "big" || !info.Empty {
		t.Errorf("SessionStart was told %+v", info)
	}
	mu.Unlock()
	reqs := r.model.requests()
	if got, want := userBlocks(reqs[0]), []string{"first", "<start>context</start>", "<hook/>"}; !slices.Equal(got, want) {
		t.Errorf("first prompt went out as %q, want %q", got, want)
	}
	if got, want := userBlocks(reqs[1]), []string{"second", "<hook/>"}; !slices.Equal(got, want) {
		t.Errorf("second prompt went out as %q, want %q", got, want)
	}
}

func TestSessionStartFailureIsAWarning(t *testing.T) {
	r := newRig(t, func(o *Options) {
		o.Moments.SessionStart = func(context.Context, StartInfo) ([]Part, error) {
			return nil, errors.New("hook timed out")
		}
	})
	r.model.push(say("hello"))
	if res := r.run("hi"); res.Reason != EndCompleted {
		t.Fatalf("result = %+v", res)
	}
	if n := notices(r.rec, LevelWarning); len(n) != 1 || !strings.Contains(n[0], "hook timed out") {
		t.Errorf("warnings = %v", n)
	}
}

// The hidden lines of a prompt go out in a fixed order, are stored once,
// and every request of a session begins with exactly what the request
// before it held: a local model server reads only what is new.
func TestPromptLinesAndStablePrefix(t *testing.T) {
	var firsts []bool
	r := newRig(t, func(o *Options) {
		o.Tools = []core.Tool{look("Read", nil)}
		o.Moments.SessionStart = func(context.Context, StartInfo) ([]Part, error) {
			return []Part{{Source: "SessionStart", Text: "<start/>"}}, nil
		}
		o.Moments.PromptSubmit = func(context.Context, Prompt) (Admission, error) {
			return Admission{Parts: []Part{{Source: "hook", Text: "<hook/>"}}}, nil
		}
		o.Moments.PromptLines = []func(context.Context, LineInfo) ([]Part, error){
			func(_ context.Context, info LineInfo) ([]Part, error) {
				firsts = append(firsts, info.First)
				if info.First {
					return []Part{{Source: "env", Text: "<env>cwd /work, 2026-11-13</env>", Before: true}, {Source: "digest", Text: "<mem>open todos</mem>"}}, nil
				}
				return nil, nil
			},
			func(_ context.Context, info LineInfo) ([]Part, error) {
				if info.Prompt.Hidden {
					return nil, nil
				}
				return []Part{{Source: "mem-line", Text: "<reminder>use mem</reminder>"}, {Text: ""}}, nil
			},
		}
		o.Moments.BeforeRequest = []func(context.Context, RequestInfo) ([]Part, error){
			func(_ context.Context, info RequestInfo) ([]Part, error) {
				if info.Round == 3 {
					return []Part{{Source: "mem-reminder", Text: "<reminder>mem has not been used</reminder>"}}, nil
				}
				return nil, nil
			},
		}
	})
	r.model.push(
		use(tc("a", "Read", `{"file_path":"/a"}`), tc("b", "Read", `{"file_path":"/b"}`)),
		silence(), // an empty reply is asked again
		use(tc("c", "Read", `{"file_path":"/c"}`)),
		say("first answer"),
		say("second answer"),
	)
	r.run("read the files")
	r.run("thanks")

	reqs := r.model.requests()
	if len(reqs) != 5 {
		t.Fatalf("%d requests", len(reqs))
	}
	want := []string{"<env>cwd /work, 2026-11-13</env>", "read the files", "<start/>", "<hook/>", "<mem>open todos</mem>", "<reminder>use mem</reminder>"}
	if got := userBlocks(reqs[0]); !slices.Equal(got, want) {
		t.Errorf("the first prompt went out as %q, want %q", got, want)
	}
	if got, want := userBlocks(reqs[4]), []string{"thanks", "<hook/>", "<reminder>use mem</reminder>"}; !slices.Equal(got, want) {
		t.Errorf("the second prompt went out as %q, want %q", got, want)
	}
	if !slices.Equal(firsts, []bool{true, false}) {
		t.Errorf("First was %v", firsts)
	}
	if got := userBlocks(reqs[2]); len(got) != 4 || !strings.Contains(got[3], "mem has not been used") {
		t.Errorf("the reminder before the third request: %q", got)
	}

	// Each request begins with the bytes of the one before.
	for i := 1; i < len(reqs); i++ {
		prev, next := reqs[i-1].Messages, reqs[i].Messages
		if len(next) < len(prev) {
			t.Fatalf("request %d holds fewer messages than the one before", i)
		}
		for j := range prev {
			a, _ := json.Marshal(prev[j])
			b, _ := json.Marshal(next[j])
			if j < len(prev)-1 {
				if string(a) != string(b) {
					t.Errorf("request %d changed message %d of the request before:\n%s\n%s", i, j, a, b)
				}
				continue
			}
			// The last message may have grown at its end, never changed.
			if len(next[j].Blocks) < len(prev[j].Blocks) {
				t.Errorf("request %d dropped blocks of message %d", i, j)
				continue
			}
			for k := range prev[j].Blocks {
				a, _ := json.Marshal(prev[j].Blocks[k])
				b, _ := json.Marshal(next[j].Blocks[k])
				if string(a) != string(b) {
					t.Errorf("request %d changed block %d of message %d", i, k, j)
				}
			}
		}
		if s1, s2 := strings.Join(reqs[i-1].System, "|"), strings.Join(reqs[i].System, "|"); s1 != s2 {
			t.Errorf("the system prompt changed between requests")
		}
	}
	// What a new process rebuilds from the log is what went out.
	v, _ := r.log.View(context.Background())
	last := reqs[4].Messages
	for j := range last {
		a, _ := json.Marshal(last[j])
		b, _ := json.Marshal(v.Messages[j])
		if string(a) != string(b) {
			t.Errorf("message %d rebuilt from the log differs from what was sent", j)
		}
	}
	r.checkLog()
}

// One prompt of the person's is taken off the queue by its id, and the
// others go out as they would have.
func TestWithdrawOneQueuedPrompt(t *testing.T) {
	r := newRig(t, nil)
	if _, ok := r.a.Withdraw("no-such-prompt"); ok {
		t.Error("Withdraw found a prompt in an empty queue")
	}
	wait := make(chan struct{})
	r.model.push(step{wait: wait, evs: say("one").evs}, say("two"))
	if _, err := r.a.Submit(Prompt{Text: "first"}); err != nil {
		t.Fatal(err)
	}
	waitEvent[*RequestEvent](t, r.rec, nil)
	idA, _ := r.a.Submit(Prompt{Text: "a"})
	idB, _ := r.a.Submit(Prompt{Text: "b"})
	idOwn, _ := r.a.Submit(Prompt{Text: "<job>3 ended</job>", Hidden: true, Source: "task-notification"})
	waitQueue(t, r, "a", "b", "<job>3 ended</job>")

	got, ok := r.a.Withdraw(idA)
	if !ok || got.Text != "a" {
		t.Fatalf("Withdraw(a) = %+v, %v", got, ok)
	}
	if _, ok := r.a.Withdraw(idA); ok {
		t.Error("the same prompt was withdrawn twice")
	}
	if _, ok := r.a.Withdraw(idOwn); ok {
		t.Error("a prompt of gnosh's own was withdrawn as the person's")
	}
	if q := r.a.Queued(); len(q) != 2 || q[0].ID != idB {
		t.Errorf("left in the queue: %+v", q)
	}
	close(wait)
	r.idle()
	for _, req := range r.model.requests() {
		for _, m := range req.Messages {
			for _, b := range m.Blocks {
				if m.Role == core.RoleUser && b.Text == "a" {
					t.Errorf("a prompt that was withdrawn reached the model")
				}
			}
		}
	}
	if last := r.model.requests(); !strings.Contains(lastUserText(last[len(last)-1]), "b") {
		t.Errorf("the prompt that stayed did not go out")
	}
	r.checkLog()
}

// A stop at the end of a round, while a prompt the person sent is still in
// its check and another waits in line, leaves the one in line for a turn
// of its own: it was folded into the stopped turn and never answered (the
// agent loop review of 2026-11-19).
func TestStopBetweenRoundsKeepsTheQueue(t *testing.T) {
	toolRan, finish := make(chan struct{}), make(chan struct{})
	inCheck, release := make(chan struct{}), make(chan struct{})
	r := newRig(t, func(o *Options) {
		o.Tools = []core.Tool{tool("Bash", func(ctx context.Context, _ core.ToolCall, _ *core.ToolEnv) (core.ToolResult, error) {
			close(toolRan)
			<-finish
			return core.TextResult("ran"), nil
		})}
		o.Moments.PromptSubmit = func(ctx context.Context, p Prompt) (Admission, error) {
			if p.Text == "B" {
				close(inCheck)
				select {
				case <-release:
				case <-ctx.Done():
				}
			}
			return Admission{}, nil
		}
	})
	r.model.push(use(tc("c1", "Bash", `{}`)), say("A answered"))
	first := make(chan TurnResult, 1)
	go func() { first <- r.run("first") }()
	<-toolRan
	r.a.Submit(Prompt{Text: "A"})
	waitQueue(t, r, "A")
	r.a.Submit(Prompt{Text: "B"})
	<-inCheck
	close(finish)
	// (The round's end waits up to admitWait for B's check.)
	time.Sleep(300 * time.Millisecond)
	r.a.Interrupt()
	close(release)
	if res := <-first; res.Reason != EndStopped {
		t.Fatalf("the first turn = %+v", res)
	}
	r.idle()
	if n := r.model.count(); n != 2 {
		t.Fatalf("%d requests: the prompt in line was not answered in a turn of its own", n)
	}
	if text := lastUserText(r.model.requests()[1]); !strings.Contains(text, "A") {
		t.Errorf("the next turn was sent %q", text)
	}
	r.checkLog()
}

// The moments after a turn ended (TurnEnded, TurnFailed: the Stop and
// StopFailure hooks) stop at Esc and at End: one that hung held the next
// prompt, Esc did nothing, and End waited for it (the agent loop review of
// 2026-11-19).
func TestHungTurnEndMomentStops(t *testing.T) {
	hangOnce := func(in chan<- struct{}) func(ctx context.Context, _ TurnResult) {
		var calls atomic.Int32
		return func(ctx context.Context, _ TurnResult) {
			if calls.Add(1) == 1 {
				in <- struct{}{}
				<-ctx.Done()
			}
		}
	}
	inMoment := make(chan struct{}, 1)
	r := newRig(t, func(o *Options) { o.Moments.TurnEnded = hangOnce(inMoment) })
	r.model.push(say("one"), say("two"))
	first := make(chan TurnResult, 1)
	go func() { first <- r.run("first") }()
	<-inMoment
	second := make(chan TurnResult, 1)
	go func() { second <- r.run("second") }()
	waitQueue(t, r, "second")
	r.a.Interrupt()
	for _, c := range []struct {
		name string
		ch   chan TurnResult
		want string
	}{{"first", first, "one"}, {"second", second, "two"}} {
		select {
		case res := <-c.ch:
			if res.Reason != EndCompleted || res.Text != c.want {
				t.Errorf("%s = %+v", c.name, res)
			}
		case <-time.After(10 * time.Second):
			t.Fatalf("Esc did not stop the hung moment: %s never ended", c.name)
		}
	}

	// End does not wait for it either.
	inMoment = make(chan struct{}, 1)
	r = newRig(t, func(o *Options) { o.Moments.TurnEnded = hangOnce(inMoment) })
	r.model.push(say("one"))
	go func() { r.run("first") }()
	<-inMoment
	ended := make(chan struct{})
	go func() { r.a.End("other"); close(ended) }()
	select {
	case <-ended:
	case <-time.After(10 * time.Second):
		t.Fatal("End waited for the hung moment")
	}
}

// A prompt taken back while its check still runs is handed back once, and
// is not counted or shown as waiting: a second take handed it back again,
// and the queue showed it until its check ended (the agent loop review of
// 2026-11-19).
func TestTakeBackDuringItsCheck(t *testing.T) {
	inCheck, release := make(chan struct{}), make(chan struct{})
	r := newRig(t, func(o *Options) {
		o.Moments.PromptSubmit = func(ctx context.Context, p Prompt) (Admission, error) {
			if p.Text == "slow" {
				close(inCheck)
				<-release // a check that takes its time, whatever is cancelled
			}
			return Admission{}, nil
		}
	})
	r.a.Submit(Prompt{Text: "slow"})
	<-inCheck
	if got := r.a.TakeQueued(); len(got) != 1 || got[0].Text != "slow" {
		t.Fatalf("the first take = %+v", got)
	}
	if got := r.a.TakeQueued(); len(got) != 0 {
		t.Errorf("a second take handed it back again: %+v", got)
	}
	if st := r.a.State(); st.Queued != 0 {
		t.Errorf("%d prompts counted as waiting", st.Queued)
	}
	if q := r.a.Queued(); len(q) != 0 {
		t.Errorf("shown as waiting: %+v", q)
	}
	close(release)
	r.idle()
	if n := r.model.count(); n != 0 {
		t.Errorf("the prompt taken back went out: %d requests", n)
	}
}

// A prompt End returns that was written down as queued is written off as
// well, so that the session opened again does not return it a second time
// (the agent loop review of 2026-11-19: after /clear, the prompts it
// returned came back again when the old session was resumed).
func TestEndWritesOffWhatItReturns(t *testing.T) {
	reached := make(chan struct{})
	r := newRig(t, nil)
	r.model.push(step{at: once(reached), stall: true})
	go r.run("first")
	<-reached
	r.a.Submit(Prompt{Text: "waiting"})
	q := waitQueue(t, r, "waiting")
	r.a.End("clear")
	off := false
	for _, e := range r.log.all() {
		if e.Kind == EntryUnqueued && e.Prompt == q.Queued[0].ID {
			off = true
		}
	}
	if !off {
		t.Errorf("the prompt End returned was not written off")
	}
	if ret := eventsOf[*PromptReturnedEvent](r.rec); len(ret) != 1 || ret[0].Why != ReturnClosed {
		t.Errorf("returned = %+v", ret)
	}
}

// A note that was to start a turn and is stopped in its check (Esc while
// the session's start runs for it) is kept as a note, and goes with the
// next request: it came back as a hidden prompt nobody shows, and was
// lost (the agent loop review of 2026-11-19).
func TestNoteStoppedInItsCheckIsKept(t *testing.T) {
	inStart := make(chan struct{}, 1)
	var starts atomic.Int32
	r := newRig(t, func(o *Options) {
		o.Moments.SessionStart = func(ctx context.Context, _ StartInfo) ([]Part, error) {
			if starts.Add(1) == 1 {
				inStart <- struct{}{}
				<-ctx.Done()
				return nil, ctx.Err()
			}
			return nil, nil
		}
	})
	r.model.push(say("hello"))
	if err := r.a.Tell(Note{Text: "the build failed", Source: "task-notification", Wake: true}); err != nil {
		t.Fatal(err)
	}
	<-inStart
	r.a.Interrupt()
	r.idle()
	if res := r.run("what happened?"); res.Reason != EndCompleted {
		t.Fatalf("the next turn = %+v", res)
	}
	var sent strings.Builder
	for _, m := range r.model.requests()[0].Messages {
		sent.WriteString(m.Text() + "\n")
	}
	if !strings.Contains(sent.String(), "the build failed") {
		t.Errorf("the note was lost: the request held %q", sent.String())
	}
}

// With two questions open at once (parallel sub-agents), the first answer
// leaves the session waiting on the second, and says which; the last one
// puts back what the session was doing: the first answer put the phase
// back while the second still waited (the agent loop review of
// 2026-11-19).
func TestTwoQuestionsAtOnce(t *testing.T) {
	r := newRig(t, func(o *Options) { o.Interactive = true })
	before := r.a.State().Phase
	done := make(chan error, 2)
	for _, title := range []string{"first?", "second?"} {
		go func() {
			_, err := r.a.Ask(context.Background(), Question{Kind: "confirm", Title: title})
			done <- err
		}()
		waitEvent(t, r.rec, func(e *QuestionEvent) bool { return e.Question.Title == title })
	}
	qs := eventsOf[*QuestionEvent](r.rec)
	if len(qs) != 2 {
		t.Fatalf("%d questions", len(qs))
	}
	if err := r.a.Answer(qs[0].Question.ID, Answer{Choice: "yes"}); err != nil {
		t.Fatal(err)
	}
	<-done
	if st := r.a.State(); st.Phase != PhaseQuestion || !strings.Contains(st.Waiting, "second?") {
		t.Errorf("with one question still open: %+v", st)
	}
	if err := r.a.Answer(qs[1].Question.ID, Answer{Choice: "yes"}); err != nil {
		t.Fatal(err)
	}
	<-done
	if st := r.a.State(); st.Phase != before {
		t.Errorf("after both: phase %v, want %v", st.Phase, before)
	}
}
