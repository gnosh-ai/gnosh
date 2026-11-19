package agent

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/gnosh-ai/gnosh/internal/core"
)

// refused is the failure of a model server that is not there.
func refused() step {
	return step{err: &core.ProviderError{Provider: "fake", Kind: core.ErrNetwork, Message: "connection refused"}}
}

// spareOf is a fallback that is the fake model m, with the window given.
func spareOf(m *fakeModel, id string, window int) Fallback {
	return Fallback{Name: m.name + "/" + id, Make: func(context.Context) (Model, error) {
		return Model{Provider: m, Info: core.ModelInfo{ID: id, ContextWindow: window}}, nil
	}}
}

// fellBackEvents are the retry events that moved a turn to a fallback.
func fellBackEvents(r *rig) []*RetryEvent {
	var out []*RetryEvent
	for _, e := range eventsOf[*RetryEvent](r.rec) {
		if e.FellBack != "" {
			out = append(out, e)
		}
	}
	return out
}

// A model server that cannot be reached is not waited for when there is a
// fallback: the turn goes on with it at once, to its end, and the next
// turn asks the session's model first again.
func TestFallbackTakesOverATurn(t *testing.T) {
	spare := &fakeModel{name: "spare"}
	r := newRig(t, func(o *Options) {
		o.Tools = []core.Tool{tool("Bash", nil)}
		o.Fallbacks = []Fallback{spareOf(spare, "small", 100000)}
	})
	r.model.answer(func(*core.Request, int) step { return refused() })
	spare.push(use(tc("a", "Bash", `{}`)), say("from the spare"))
	res := r.run("anyone there?")
	if res.Reason != EndCompleted || res.Text != "from the spare" {
		t.Fatalf("result = %+v", res)
	}
	if n := r.model.count(); n != 1 {
		t.Errorf("the session's model was asked %d times in the turn; once is enough to know it is down", n)
	}
	if n := spare.count(); n != 2 {
		t.Errorf("the fallback was asked %d times, not 2: the turn did not stay with it", n)
	}
	fell := fellBackEvents(r)
	if len(fell) != 1 || fell[0].FellBack != "spare (small)" || fell[0].Wait != 0 {
		t.Fatalf("fell back = %+v", fell)
	}
	for _, part := range []string{"fake (big) at http://203.0.113.7:8080 could not be reached", "connection refused",
		"This turn goes on with the fallback model spare (small)", "the next turn asks fake (big) at http://203.0.113.7:8080 first again"} {
		if !strings.Contains(fell[0].Message, part) {
			t.Errorf("the person is told %q, without %q", fell[0].Message, part)
		}
	}
	if n := len(eventsOf[*RetryEvent](r.rec)); n != 1 {
		t.Errorf("%d retry events: the model that was down was waited for", n)
	}

	// The next turn: the session's model first, and it is back.
	r.model.answer(nil)
	r.model.push(say("back again"))
	if res := r.run("and now?"); res.Reason != EndCompleted || res.Text != "back again" {
		t.Fatalf("the next turn = %+v", res)
	}
	if n := spare.count(); n != 2 {
		t.Errorf("the fallback was asked in the next turn too (%d requests)", n)
	}
	r.checkLog()
}

// A limit on the person's requests is waited out on the session's model:
// another model would not lift it, as Claude Code has it.
func TestNoFallbackForALimitOnRequests(t *testing.T) {
	spare := &fakeModel{name: "spare"}
	r, res := endsWith(t, func(o *Options) { o.Fallbacks = []Fallback{spareOf(spare, "small", 100000)} },
		broken(core.ErrRateLimit, 429), say("ok"))
	if res.Reason != EndCompleted || res.Text != "ok" {
		t.Fatalf("result = %+v", res)
	}
	if spare.count() != 0 || len(fellBackEvents(r)) != 0 {
		t.Errorf("the fallback was asked (%d) for a rate limit", spare.count())
	}
}

// Trouble that may pass (a failure on the server's own side) is retried as
// always, and handed to the fallback only when the wait for it runs out.
func TestFallbackWhenTheWaitRunsOut(t *testing.T) {
	spare := &fakeModel{name: "spare"}
	r := newRig(t, func(o *Options) { o.Fallbacks = []Fallback{spareOf(spare, "small", 100000)} })
	r.model.answer(func(*core.Request, int) step { return broken(core.ErrServer, 500) })
	spare.push(say("covered"))
	res := r.run("hello")
	if res.Reason != EndCompleted || res.Text != "covered" {
		t.Fatalf("result = %+v", res)
	}
	all, fell := eventsOf[*RetryEvent](r.rec), fellBackEvents(r)
	if len(fell) != 1 || len(all) < 3 || all[len(all)-1] != fell[0] {
		t.Fatalf("%d retries, then fell back = %+v", len(all), fell)
	}
	if !strings.Contains(fell[0].Message, "failed on its own side (HTTP 500)") {
		t.Errorf("the person is told %q", fell[0].Message)
	}
}

// The chain is tried in order. A fallback that cannot be made, or whose
// window is smaller than the session model's, is passed over with a word;
// one that fails as well hands the turn on to the next.
func TestFallbackChain(t *testing.T) {
	narrow, down, last := &fakeModel{name: "narrow"}, &fakeModel{name: "down"}, &fakeModel{name: "last"}
	r := newRig(t, func(o *Options) {
		o.Fallbacks = []Fallback{
			spareOf(narrow, "tiny", 16),
			{Name: "gone/model", Make: func(context.Context) (Model, error) { return Model{}, errors.New("no provider named gone") }},
			spareOf(down, "m", 0),
			spareOf(last, "m", 200000),
		}
	})
	r.model.answer(func(*core.Request, int) step { return refused() })
	down.answer(func(*core.Request, int) step { return broken(core.ErrOverloaded, 503) })
	last.push(say("the last one"))
	res := r.run(strings.Repeat("a few words ", 100))
	if res.Reason != EndCompleted || res.Text != "the last one" {
		t.Fatalf("result = %+v", res)
	}
	if narrow.count() != 0 {
		t.Error("a fallback with a smaller window was asked")
	}
	fell := fellBackEvents(r)
	if len(fell) != 2 || fell[0].FellBack != "down (m)" || fell[1].FellBack != "last (m)" {
		t.Fatalf("fell back = %+v", fell)
	}
	if !strings.Contains(fell[1].Message, "down (m) said it is too busy (HTTP 503)") ||
		!strings.Contains(fell[1].Message, "the next turn asks fake (big) at http://203.0.113.7:8080 first.") {
		t.Errorf("the second switch says %q", fell[1].Message)
	}
	warned := strings.Join(notices(r.rec, LevelWarning), "\n")
	for _, part := range []string{"narrow/tiny is passed over: its context window (16 tokens) cannot hold the conversation as it stands", "gone/model is passed over: it cannot be used (no provider named gone)"} {
		if !strings.Contains(warned, part) {
			t.Errorf("the warnings do not say %q:\n%s", part, warned)
		}
	}
}

// A fallback on the same model server of one's own as the model that
// failed is passed over: it would be down or busy as well, and a server
// that holds one model at a time would unload the one in use to load it.
func TestFallbackOnTheSameLocalServer(t *testing.T) {
	other := &fakeModel{name: "spare"}
	r := newRig(t, func(o *Options) {
		o.Model.Local = true
		o.Fallbacks = []Fallback{
			{Name: "fake/other", Make: func(context.Context) (Model, error) {
				return Model{Provider: r0(o), Info: core.ModelInfo{ID: "other"}, Address: "http://203.0.113.7:8080", Local: true}, nil
			}},
			spareOf(other, "small", 0),
		}
	})
	r.model.answer(func(*core.Request, int) step { return broken(core.ErrOverloaded, 503) })
	other.push(say("elsewhere"))
	if res := r.run("hello"); res.Reason != EndCompleted || res.Text != "elsewhere" {
		t.Fatalf("result = %+v", res)
	}
	if n := r.model.count(); n != 1 {
		t.Errorf("the busy server was asked %d times: once for its own model, and never for another", n)
	}
	if warned := strings.Join(notices(r.rec, LevelWarning), "\n"); !strings.Contains(warned, "fake/other is passed over: it is on the same model server of your own as a model that failed in this turn") {
		t.Errorf("warnings:\n%s", warned)
	}
}

// r0 is the session's own provider, as Options names it.
func r0(o *Options) core.Provider { return o.Model.Provider }

// When every fallback fails too, the turn goes back to the session's own
// model and waits for it as it would have without them: a server that has
// answered in the session is waited for as one that restarts, however
// long a fallback that is down took to say so.
func TestEveryFallbackFails(t *testing.T) {
	spare := &fakeModel{name: "spare"}
	r := newRig(t, func(o *Options) { o.Fallbacks = []Fallback{spareOf(spare, "small", 100000)} })
	r.model.answer(func(*core.Request, int) step { return refused() })
	spare.answer(func(*core.Request, int) step { return refused() })
	res := r.run("hello")
	if res.Reason != EndFailed || !strings.Contains(res.Err.Error(), "fake (big) at http://203.0.113.7:8080 is not running") {
		t.Fatalf("result = %+v", res)
	}
	fell := fellBackEvents(r)
	if len(fell) != 2 || fell[1].FellBack != "fake (big) at http://203.0.113.7:8080" ||
		!strings.Contains(fell[1].Message, "No fallback model is left, so this turn waits for fake (big)") {
		t.Fatalf("fell back = %+v", fell)
	}
	r.checkLog()

	// A model that has answered in the session is waited for as one that
	// restarts.
	down := &fakeModel{name: "spare"}
	down.answer(func(*core.Request, int) step { return refused() })
	r2 := newRig(t, func(o *Options) { o.Fallbacks = []Fallback{spareOf(down, "small", 100000)} })
	r2.model.push(say("hello"))
	if res := r2.run("hi"); res.Reason != EndCompleted {
		t.Fatalf("the first turn = %+v", res)
	}
	r2.model.push(refused(), refused(), say("back"))
	if res := r2.run("still there?"); res.Reason != EndCompleted || res.Text != "back" {
		t.Fatalf("the second turn = %+v", res)
	}
	waited := false
	for _, e := range eventsOf[*RetryEvent](r2.rec) {
		waited = waited || e.FellBack == "" && strings.Contains(e.Message, "may be restarting or loading a model")
	}
	if !waited {
		t.Error("the session's own model, which had answered, was not waited for as one that restarts")
	}
}

// A model chosen by hand outranks the fallback a turn went on with.
func TestChosenModelOutranksTheFallback(t *testing.T) {
	spare, chosen := &fakeModel{name: "spare"}, &fakeModel{name: "chosen"}
	r := newRig(t, func(o *Options) {
		o.Tools = []core.Tool{tool("Bash", func(context.Context, core.ToolCall, *core.ToolEnv) (core.ToolResult, error) {
			return core.TextResult("ran"), nil
		})}
		o.Fallbacks = []Fallback{spareOf(spare, "small", 100000)}
	})
	r.model.answer(func(*core.Request, int) step { return refused() })
	st := use(tc("a", "Bash", `{}`))
	st.mid = func() {
		if err := r.a.SetModel(Model{Provider: chosen, Info: core.ModelInfo{ID: "c", ContextWindow: 100000}}); err != nil {
			t.Errorf("SetModel: %v", err)
		}
	}
	spare.push(st)
	chosen.push(say("chosen answers"))
	if res := r.run("hello"); res.Reason != EndCompleted || res.Text != "chosen answers" {
		t.Fatalf("result = %+v", res)
	}
}

// A summary the session's model cannot make is made by the fallback, and
// the turn goes on with it.
func TestFallbackMakesTheSummary(t *testing.T) {
	j := &job{want: 14}
	spare := &fakeModel{name: "spare", honest: true, window: 8000}
	r := newRig(t, func(o *Options) {
		o.Model.Info.ContextWindow = 8000
		o.System = []string{"You are gnosh."}
		o.Tools = []core.Tool{fileTool(600)}
		o.Fallbacks = []Fallback{spareOf(spare, "small", 8000)}
	})
	r.model.honest, r.model.window = true, 8000
	r.model.answer(func(req *core.Request, n int) step {
		if isSummaryAsk(req) {
			return refused()
		}
		return j.respond(req, n)
	})
	spare.answer(j.respond)
	res := r.run("read fourteen files")
	if res.Reason != EndCompleted || res.Text != "done" {
		t.Fatalf("result = %+v", res)
	}
	fell := fellBackEvents(r)
	if len(fell) != 1 || fell[0].Purpose != "summary" {
		t.Fatalf("fell back = %+v", fell)
	}
	summarized := false
	for _, req := range spare.requests() {
		summarized = summarized || isSummaryAsk(req)
	}
	if !summarized || j.summaryCount() == 0 || len(r.log.of(EntrySummary)) == 0 {
		t.Errorf("no summary was made by the fallback (%d summaries)", j.summaryCount())
	}
	if r.model.refused != 0 || spare.refused != 0 {
		t.Errorf("requests over the window: %d and %d", r.model.refused, spare.refused)
	}
	for _, e := range eventsOf[*SummaryEvent](r.rec) {
		if e.Err != nil {
			t.Errorf("a summary handed to the fallback was said to have failed: %v", e.Err)
		}
	}
	r.checkLog()
}

// A fallback whose window nobody knows is taken to hold UnknownWindow
// tokens: it is passed over for a conversation larger than that, which
// would otherwise be cut for good to fit it.
func TestFallbackWithAnUnknownWindow(t *testing.T) {
	unknown, big := &fakeModel{name: "unknown"}, &fakeModel{name: "big"}
	r := newRig(t, func(o *Options) {
		o.Model.Info.ContextWindow = 262144
		o.Fallbacks = []Fallback{spareOf(unknown, "u", 0), spareOf(big, "b", 262144)}
	})
	r.model.answer(func(*core.Request, int) step { return refused() })
	big.push(say("held"))
	res := r.run(strings.Repeat("a long conversation ", 6000))
	if res.Reason != EndCompleted || res.Text != "held" {
		t.Fatalf("result = %+v", res)
	}
	if unknown.count() != 0 {
		t.Error("the fallback with an unknown window was asked")
	}
	if len(r.log.of(EntrySummary)) != 0 {
		t.Error("the conversation was summarized to fit a fallback")
	}
	if warned := strings.Join(notices(r.rec, LevelWarning), "\n"); !strings.Contains(warned, "unknown/u is passed over: its context window (not known, so taken to be 33K tokens) cannot hold the conversation as it stands") {
		t.Errorf("warnings:\n%s", warned)
	}
}

// A model server of one's own that failed is remembered for the turn: a
// fallback on it is passed over even after another, elsewhere, failed in
// between.
func TestFailedLocalServerIsRemembered(t *testing.T) {
	cloud, glm := &fakeModel{name: "cloud"}, 0
	r := newRig(t, func(o *Options) {
		o.Model.Local = true
		o.Fallbacks = []Fallback{
			spareOf(cloud, "x", 0),
			{Name: "fake/glm", Make: func(context.Context) (Model, error) {
				glm++
				return Model{Provider: r0(o), Info: core.ModelInfo{ID: "glm"}, Local: true}, nil
			}},
		}
	})
	cloud.answer(func(*core.Request, int) step { return broken(core.ErrOverloaded, 529) })
	r.model.push(broken(core.ErrOverloaded, 503), broken(core.ErrOverloaded, 503), say("finally"))
	if res := r.run("hello"); res.Reason != EndCompleted || res.Text != "finally" {
		t.Fatalf("result = %+v", res)
	}
	for _, req := range r.model.requests() {
		if req.Model == "glm" {
			t.Error("the session's own server was asked for another of its models")
		}
	}
	if warned := strings.Join(notices(r.rec, LevelWarning), "\n"); !strings.Contains(warned, "fake/glm is passed over: it is on the same model server of your own") {
		t.Errorf("warnings:\n%s", warned)
	}
}

// Esc while a fallback is being made ready stops the turn: it is not a
// failed one.
func TestStopWhileAFallbackIsMade(t *testing.T) {
	making := make(chan struct{})
	var r *rig
	r = newRig(t, func(o *Options) {
		o.Fallbacks = []Fallback{{Name: "slow/m", Make: func(ctx context.Context) (Model, error) {
			close(making)
			<-ctx.Done()
			return Model{}, ctx.Err()
		}}}
	})
	r.model.answer(func(*core.Request, int) step { return refused() })
	go func() {
		<-making
		r.a.Interrupt()
	}()
	if res := r.run("hello"); res.Reason != EndStopped {
		t.Fatalf("result = %+v", res)
	}
	// The failed try is closed as stopped: no request is left out when
	// the turn ends (the stress test once saw one).
	r.idle()
	checkEvents(t, r.rec.all())
}
