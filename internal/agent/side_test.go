package agent

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/gnosh-ai/gnosh/internal/core"
)

func ptr[T any](v T) *T { return &v }

// sideRig is a session with a small model beside the large one.
func sideRig(t *testing.T) (*rig, *fakeModel) {
	small := &fakeModel{name: "small"}
	r := newRig(t, func(o *Options) {
		o.Small = &Model{
			Provider: small, Info: core.ModelInfo{ID: "tiny"},
			Levels: []string{"low", "medium", "high"}, Level: "high",
			Extra: map[string]any{"id_slot": 1},
		}
		o.Model.Levels = []string{"off", "on"}
		o.Model.Level = "on"
		o.System = []string{"the session's own system prompt"}
		o.Tools = []core.Tool{tool("Bash", nil)}
	})
	return r, small
}

// A side request is a short question with no tools, a cap on the answer,
// and as little thinking as the model has. The small model is asked
// first, and the answer is read past any thinking tags.
func TestSideRequest(t *testing.T) {
	r, small := sideRig(t)
	small.push(say("<think>\nwhat is this about? a release.\n</think>\n\n release-notes \n"))

	res, err := r.a.Side(context.Background(), SideRequest{
		Purpose: "name", System: "Name the text.", Prompt: "write the release notes",
		MaxTokens: 24, Temperature: ptr(0.0),
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.Text != "release-notes" || res.Fallback || res.Model != "tiny" || res.Provider != "small" {
		t.Errorf("result = %+v", res)
	}
	reqs := small.requests()
	if len(reqs) != 1 || r.model.count() != 0 {
		t.Fatalf("%d requests to the small model, %d to the large", len(reqs), r.model.count())
	}
	req := reqs[0]
	if req.Model != "tiny" || len(req.Tools) != 0 || req.MaxTokens != 24 || req.Temperature == nil || *req.Temperature != 0 {
		t.Errorf("request = %+v", req)
	}
	if len(req.System) != 1 || req.System[0] != "Name the text." {
		t.Errorf("system = %q", req.System)
	}
	if req.Thinking != (core.Thinking{Effort: "low"}) {
		t.Errorf("a side request must think as little as the model can: %+v", req.Thinking)
	}
	if req.Extra["id_slot"] != 1 {
		t.Errorf("the model's extra fields were not sent: %v", req.Extra)
	}
	if len(req.Messages) != 1 || req.Messages[0].Text() != "write the release notes" {
		t.Errorf("messages = %+v", req.Messages)
	}

	// It is counted, so that what the small jobs cost can be watched.
	r.drain()
	u := r.a.Usage()
	if u.Side.Requests != 1 || u.Side.ByPurpose["name"] != 1 || u.ByModel["tiny"].Requests != 1 || u.Requests != 0 {
		t.Errorf("totals = %+v", u)
	}
	evs := eventsOf[*SideEvent](r.rec)
	if len(evs) != 1 || evs[0].Purpose != "name" || evs[0].Err != nil || evs[0].Model != "tiny" {
		t.Errorf("side events = %+v", evs)
	}
}

// When the small model fails, answers with nothing or is cut off, the
// large one is asked.
func TestSideFallsBackToTheLargeModel(t *testing.T) {
	cases := map[string]step{
		"fails":                broken(core.ErrServer, 500),
		"answers with nothing": silence(),
		"only thinks":          say("<think>I wonder"),
		"is cut off":           {evs: []core.Event{{Type: core.EventTextDelta, Text: "release-no"}, usageEv(5, 5), stopEv(core.StopMaxTokens)}},
	}
	for name, st := range cases {
		t.Run(name, func(t *testing.T) {
			r, small := sideRig(t)
			small.push(st)
			r.model.push(say("release-notes"))
			res, err := r.a.Side(context.Background(), SideRequest{Purpose: "name", Prompt: "write the release notes", MaxTokens: 24})
			if err != nil {
				t.Fatal(err)
			}
			if res.Text != "release-notes" || !res.Fallback || res.Model != "big" {
				t.Errorf("result = %+v", res)
			}
			large := r.model.requests()
			if len(large) != 1 || len(large[0].Tools) != 0 || len(large[0].System) != 0 {
				t.Fatalf("the large model was sent %+v", large)
			}
			// The large model's least thinking is "off".
			if large[0].Thinking != (core.Thinking{Mode: "off"}) {
				t.Errorf("thinking = %+v", large[0].Thinking)
			}
			r.drain()
			evs := eventsOf[*SideEvent](r.rec)
			if len(evs) != 2 || evs[0].Err == nil || evs[0].Fallback || evs[1].Err != nil || !evs[1].Fallback {
				t.Errorf("side events = %+v", evs)
			}
			if u := r.a.Usage(); u.Side.Requests != 2 || u.Requests != 0 {
				t.Errorf("totals = %+v", u)
			}
		})
	}
}

// When the large model is cut off too, the caller gets what arrived and
// is told that it was cut.
func TestSideCutOffEverywhere(t *testing.T) {
	r, small := sideRig(t)
	cut := step{evs: []core.Event{{Type: core.EventTextDelta, Text: `{"ok": tr`}, usageEv(5, 5), stopEv(core.StopMaxTokens)}}
	small.push(cut)
	r.model.push(cut)
	res, err := r.a.Side(context.Background(), SideRequest{Purpose: "goal-judge", Prompt: "is it met?", MaxTokens: 5})
	if !errors.Is(err, ErrCutOff) {
		t.Fatalf("err = %v", err)
	}
	if res.Text != `{"ok": tr` || !res.Fallback {
		t.Errorf("result = %+v", res)
	}
}

// With no small model the large one is asked alone, and a model with no
// known thinking switch is still sent the plain "off".
func TestSideWithoutASmallModel(t *testing.T) {
	r := newRig(t, nil)
	r.model.push(say("a title"))
	conversation := []core.Message{core.UserText("fix the flaky test"), core.AssistantText("I fixed it.")}
	res, err := r.a.Side(context.Background(), SideRequest{Messages: conversation, Prompt: "Give this session a title."})
	if err != nil || res.Text != "a title" || res.Fallback {
		t.Fatalf("result = %+v, %v", res, err)
	}
	req := r.model.requests()[0]
	if req.Thinking != (core.Thinking{Mode: "off"}) {
		t.Errorf("thinking = %+v", req.Thinking)
	}
	if len(req.Messages) != 3 || req.Messages[2].Text() != "Give this session a title." {
		t.Errorf("messages = %+v", req.Messages)
	}
	if u := r.a.Usage(); u.Side.ByPurpose["side"] != 1 {
		t.Errorf("totals = %+v", u.Side)
	}
	// A question that follows the user's own words joins them.
	r.model.push(say("yes"))
	if _, err := r.a.Side(context.Background(), SideRequest{Messages: conversation[:1], Prompt: "Is this about tests?"}); err != nil {
		t.Fatal(err)
	}
	if req := r.model.requests()[1]; len(req.Messages) != 1 || len(req.Messages[0].Blocks) != 2 {
		t.Errorf("messages = %+v", req.Messages)
	}
	if _, err := r.a.Side(context.Background(), SideRequest{}); err == nil {
		t.Error("a side request with nothing to ask was accepted")
	}
}

// A model that does not answer in time is given up on, and the other is
// asked.
func TestSideTimeout(t *testing.T) {
	r, small := sideRig(t)
	small.push(step{stall: true})
	r.model.push(say("release-notes"))
	began := time.Now()
	res, err := r.a.Side(context.Background(), SideRequest{Purpose: "name", Prompt: "name it", Timeout: 20 * time.Millisecond})
	if err != nil || res.Text != "release-notes" || !res.Fallback {
		t.Fatalf("result = %+v, %v", res, err)
	}
	if took := time.Since(began); took > 2*time.Second {
		t.Errorf("it took %s", took)
	}
	r.drain()
	evs := eventsOf[*SideEvent](r.rec)
	if len(evs) != 2 || evs[0].Err == nil || !strings.Contains(evs[0].Err.Error(), "did not answer the name request within 20ms") {
		t.Errorf("side events = %+v", evs)
	}
}

// A side request is not sent while the session's own request is being
// answered, so that it cannot push the conversation out of a model
// server's cache; and the caller can give up on it.
func TestSideWaitsForTheMainRequest(t *testing.T) {
	r, small := sideRig(t)
	wait := make(chan struct{})
	reached := make(chan struct{})
	r.model.push(step{at: once(reached), wait: wait, evs: say("the answer").evs})
	small.push(say("a name"))
	done := make(chan TurnResult, 1)
	go func() { done <- r.run("a long question") }()
	<-reached

	// One caller gives up while it waits.
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	_, err := r.a.Side(ctx, SideRequest{Purpose: "name", Prompt: "name it"})
	cancel()
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("err = %v", err)
	}
	if small.count() != 0 {
		t.Fatal("the side request was sent while the main request was out")
	}

	// Another waits, and is sent once the reply is in.
	side := make(chan SideResult, 1)
	go func() {
		res, _ := r.a.Side(context.Background(), SideRequest{Purpose: "name", Prompt: "name it"})
		side <- res
	}()
	time.Sleep(10 * time.Millisecond)
	if small.count() != 0 {
		t.Fatal("the side request was sent while the main request was out")
	}
	close(wait)
	if res := <-done; res.Reason != EndCompleted {
		t.Fatalf("turn = %+v", res)
	}
	select {
	case res := <-side:
		if res.Text != "a name" {
			t.Errorf("side result = %+v", res)
		}
	case <-time.After(bound):
		t.Fatal("the side request never went out")
	}
}

func TestAnswerText(t *testing.T) {
	cases := map[string]string{
		"plain":                                "plain",
		"  spaced \n":                          "spaced",
		"<think>hmm</think>answer":             "answer",
		"<think>a</think><think>b</think>\nok": "ok",
		"reasoning that leaked</think>\n\nok":  "ok",
		"<think>never closed":                  "",
		"":                                     "",
		"keep <b>tags</b> in the answer":       "keep <b>tags</b> in the answer",
	}
	for in, want := range cases {
		if got := AnswerText(in); got != want {
			t.Errorf("AnswerText(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestJSONObject(t *testing.T) {
	cases := []struct {
		in   string
		want string
		ok   bool
	}{
		{`{"ok": true}`, `{"ok": true}`, true},
		{"Here is my verdict:\n```json\n{\"ok\": false, \"reason\": \"no {braces} run\"}\n```\nThanks.", `{"ok": false, "reason": "no {braces} run"}`, true},
		{`no object here`, "", false},
		{`} backwards {`, "", false},
		{`{"cut": tr`, "", false},
		{``, "", false},
	}
	for _, c := range cases {
		got, ok := JSONObject(c.in)
		if got != c.want || ok != c.ok {
			t.Errorf("JSONObject(%q) = %q, %v; want %q, %v", c.in, got, ok, c.want, c.ok)
		}
	}
}

// A side question about the conversation is the session's own next
// request with the question after it: the same system prompt, tools and
// messages, so that a model server that kept the conversation reads only
// the question. The session's own model answers, never the small one, and
// nothing of the exchange is kept.
func TestSideQuestionAboutTheConversation(t *testing.T) {
	r, small := sideRig(t)
	r.model.push(say("The number is 7."))
	if res := r.run("remember the number 7"); res.Reason != EndCompleted {
		t.Fatalf("turn = %+v", res)
	}
	before := len(r.log.all())
	r.model.push(say("<think>easy</think>It was 7."))
	res, err := r.a.Side(context.Background(), SideRequest{Purpose: "btw", Session: true, Prompt: "which number was it?", MaxTokens: 64})
	if err != nil {
		t.Fatal(err)
	}
	if res.Text != "It was 7." || res.Fallback || small.count() != 0 {
		t.Errorf("result = %+v (the small model was asked %d times)", res, small.count())
	}
	reqs := r.model.requests()
	main, side := reqs[0], reqs[len(reqs)-1]
	if len(side.Tools) != len(main.Tools) || len(side.Tools) == 0 || strings.Join(side.System, "|") != strings.Join(main.System, "|") {
		t.Errorf("the side question's request does not begin as the session's: tools %d against %d, system %q against %q", len(side.Tools), len(main.Tools), side.System, main.System)
	}
	if n := len(side.Messages); n != 3 || side.Messages[0].Text() != main.Messages[0].Text() || side.Messages[2].Text() != "which number was it?" {
		t.Errorf("the side question's messages: %+v", side.Messages)
	}
	if side.MaxTokens != 64 || side.Thinking != (core.Thinking{Mode: "off"}) {
		t.Errorf("max tokens %d, thinking %+v", side.MaxTokens, side.Thinking)
	}
	if after := len(r.log.all()); after != before {
		t.Errorf("the side question left %d entries in the log", after-before)
	}
}

// A side question asked while a call runs answers that call as still
// running, so that the conversation reads as one a model can be sent; the
// session's own next request holds the call's real result.
func TestSideQuestionWhileACallRuns(t *testing.T) {
	started, release := make(chan struct{}), make(chan struct{})
	r := newRig(t, func(o *Options) {
		o.Tools = []core.Tool{tool("Bash", func(ctx context.Context, call core.ToolCall, env *core.ToolEnv) (core.ToolResult, error) {
			close(started)
			<-release
			return core.TextResult("done at last"), nil
		})}
	})
	r.model.push(use(tc("c1", "Bash", `{"command":"sleep 9"}`)))
	done := make(chan TurnResult, 1)
	go func() { done <- r.run("run the slow thing") }()
	<-started
	// The side question is asked first, and the session's own request
	// after the call returns.
	r.model.push(say("It is still sleeping."), say("All done."))
	res, err := r.a.Side(context.Background(), SideRequest{Purpose: "btw", Session: true, Prompt: "what is it doing?"})
	if err != nil || res.Text != "It is still sleeping." {
		t.Fatalf("result = %+v, err = %v", res, err)
	}
	reqs := r.model.requests()
	side := reqs[len(reqs)-1]
	last := side.Messages[len(side.Messages)-1]
	if len(last.Blocks) != 2 || last.Blocks[0].Type != core.BlockToolResult || last.Blocks[0].ToolUseID != "c1" ||
		!strings.Contains(core.TextOf(last.Blocks[0].Content), "Still running: nobody interrupted this call") || last.Blocks[1].Text != "what is it doing?" {
		t.Errorf("the side question's last message: %+v", last)
	}
	close(release)
	if res := <-done; res.Reason != EndCompleted {
		t.Fatalf("turn = %+v", res)
	}
	reqs = r.model.requests()
	next := reqs[len(reqs)-1]
	if got := core.TextOf(next.Messages[len(next.Messages)-1].Blocks[0].Content); got != "done at last" {
		t.Errorf("the session's own request after the side question holds %q as the call's result", got)
	}
}

// So too while the call waits on the gate, shown and not yet run: it is
// not interrupted (seen under load: the call joined the running ones only
// once the gate let it, and a side question asked between was told it was
// interrupted).
func TestSideQuestionWhileACallWaitsOnTheGate(t *testing.T) {
	asked, release := make(chan struct{}), make(chan struct{})
	r := newRig(t, func(o *Options) {
		o.Tools = []core.Tool{tool("Bash", func(ctx context.Context, call core.ToolCall, env *core.ToolEnv) (core.ToolResult, error) {
			return core.TextResult("done at last"), nil
		})}
		o.Gate = GateFunc(func(ctx context.Context, req GateRequest) (Decision, error) {
			close(asked)
			<-release
			return Decision{Allow: true}, nil
		})
	})
	r.model.push(use(tc("c1", "Bash", `{"command":"sleep 9"}`)))
	done := make(chan TurnResult, 1)
	go func() { done <- r.run("run the slow thing") }()
	<-asked
	r.model.push(say("It waits for you."), say("All done."))
	res, err := r.a.Side(context.Background(), SideRequest{Purpose: "btw", Session: true, Prompt: "what is it doing?"})
	if err != nil || res.Text != "It waits for you." {
		t.Fatalf("result = %+v, err = %v", res, err)
	}
	reqs := r.model.requests()
	side := reqs[len(reqs)-1]
	last := side.Messages[len(side.Messages)-1]
	if len(last.Blocks) != 2 || !strings.Contains(core.TextOf(last.Blocks[0].Content), "Still running: nobody interrupted this call") {
		t.Errorf("the side question's last message: %+v", last)
	}
	close(release)
	if res := <-done; res.Reason != EndCompleted {
		t.Fatalf("turn = %+v", res)
	}
}

// A side request that gives way (Yield) is stopped when the session's own
// next request wants the model, so a turn never waits behind it; one that
// does not give way is waited for.
func TestYieldingSideRequestGivesWay(t *testing.T) {
	r, _ := sideRig(t)
	reached := make(chan struct{})
	r.model.push(step{at: once(reached), stall: true})
	r.model.push(say("the answer"))
	side := make(chan error, 1)
	go func() {
		_, err := r.a.Side(context.Background(), SideRequest{Purpose: "suggestion", Session: true, Yield: true, Prompt: "what next?", Timeout: time.Minute})
		side <- err
	}()
	<-reached
	began := time.Now()
	res := r.run("a prompt")
	if res.Reason != EndCompleted {
		t.Fatalf("the turn ended %v", res.Reason)
	}
	if took := time.Since(began); took > 5*time.Second {
		t.Errorf("the turn waited %s behind the side request", took)
	}
	if err := <-side; !errors.Is(err, ErrYielded) {
		t.Errorf("the side request ended with %v, want ErrYielded", err)
	}
	r.drain()
	evs := eventsOf[*SideEvent](r.rec)
	if len(evs) != 1 || !errors.Is(evs[0].Err, ErrYielded) {
		t.Errorf("side events = %+v", evs)
	}
}

// droppingModel is a fake model whose server has refused images.
type droppingModel struct{ *fakeModel }

func (droppingModel) LeavesOutImages(string) bool { return true }

// A model whose provider leaves images out is named once in a notice, when
// it first answers: the model may not say it saw no picture.
func TestImagesLeftOutAreTold(t *testing.T) {
	r := newRig(t, func(o *Options) { o.Model.Provider = droppingModel{o.Model.Provider.(*fakeModel)} })
	r.model.push(say("one"))
	r.model.push(say("two"))
	r.run("look at the picture")
	r.run("and again")
	r.drain()
	n := 0
	for _, ev := range eventsOf[*NoticeEvent](r.rec) {
		if strings.Contains(ev.Text, "cannot take images") {
			n++
		}
	}
	if n != 1 {
		t.Errorf("the person was told %d times that the model cannot take images, want once", n)
	}
}

// A side request's time begins once it has the model: one that waits
// behind another (a prompt hook behind a long /btw) is not failed for the
// wait, which has a bound of its own (the side-request review of
// 2026-11-19).
func TestSideTimeBeginsWithTheModel(t *testing.T) {
	r := newRig(t, func(o *Options) {})
	r.a.modelLock <- struct{}{} // another request has the model
	go func() {
		time.Sleep(300 * time.Millisecond)
		<-r.a.modelLock
	}()
	r.model.push(say("Allowed."))
	res, err := r.a.Side(context.Background(), SideRequest{Purpose: "hook", Prompt: "is it allowed?", Timeout: 100 * time.Millisecond})
	if err != nil || res.Text != "Allowed." {
		t.Fatalf("a side request that waited longer than its time for the model: %+v, %v", res, err)
	}
}

// A side request a provider fails in passing (a 429, a model server that
// restarts) is sent again after the session's own pauses, as the
// session's own requests are; one that gives way (a suggestion) is not
// (the side-request review of 2026-11-19).
func TestSideTriesAgainAfterAPassingFailure(t *testing.T) {
	r := newRig(t, func(o *Options) {})
	r.model.push(broken(core.ErrRateLimit, 429), broken(core.ErrServer, 503), say("Judged."))
	res, err := r.a.Side(context.Background(), SideRequest{Purpose: "goal-judge", Prompt: "does it hold?"})
	if err != nil || res.Text != "Judged." {
		t.Fatalf("a judge a provider failed in passing: %+v, %v", res, err)
	}
	r.model.push(broken(core.ErrRateLimit, 429), say("not asked"))
	if _, err := r.a.Side(context.Background(), SideRequest{Purpose: "suggestion", Yield: true, Prompt: "next?"}); err == nil {
		t.Error("a request that gives way was tried again")
	}
}

// A side request asked beside the session's own (the pace check) goes out
// while the session's request is still being answered, where side requests
// have a place of their own on the model server; it carries that place
// (Model.SideExtra), which the session's own requests never do.
func TestSideBesideTheMainRequest(t *testing.T) {
	r := newRig(t, func(o *Options) {
		o.Model.Local = true
		o.Model.SideExtra = map[string]any{"id_slot": 1}
	})
	wait := make(chan struct{})
	reached := make(chan struct{})
	r.model.push(step{at: once(reached), wait: wait, evs: say("the answer").evs})
	r.model.push(say(`{"pace": "ok", "why": "fine", "next": 60}`))
	done := make(chan TurnResult, 1)
	go func() { done <- r.run("a long question") }()
	<-reached
	if !r.a.SidePlace() {
		t.Fatal("a model with SideExtra gives side requests no place of their own")
	}

	ctx, cancel := context.WithTimeout(context.Background(), bound)
	res, err := r.a.Side(ctx, SideRequest{Purpose: "pace", Prompt: "is this the fastest way?", Beside: true})
	cancel()
	if err != nil || !strings.Contains(res.Text, `"ok"`) {
		t.Fatalf("the side request beside the main one: %+v, %v", res, err)
	}
	select {
	case <-done:
		t.Fatal("the main request ended before the side request was answered")
	default:
	}
	close(wait)
	if res := <-done; res.Reason != EndCompleted {
		t.Fatalf("turn = %+v", res)
	}
	reqs := r.model.requests()
	if len(reqs) != 2 {
		t.Fatalf("%d requests, want the main one and the side one", len(reqs))
	}
	if _, ok := reqs[0].Extra["id_slot"]; ok {
		t.Errorf("the session's own request carries the side requests' place: %v", reqs[0].Extra)
	}
	if reqs[1].Extra["id_slot"] != 1 {
		t.Errorf("the side request does not carry its place: %v", reqs[1].Extra)
	}

	// A question about the conversation goes where the conversation is.
	r.model.push(say("it is about the answer"))
	if _, err := r.a.Side(context.Background(), SideRequest{Session: true, Prompt: "what is this about?"}); err != nil {
		t.Fatal(err)
	}
	reqs = r.model.requests()
	if _, ok := reqs[len(reqs)-1].Extra["id_slot"]; ok {
		t.Errorf("a side question about the conversation carries the side requests' place: %v", reqs[len(reqs)-1].Extra)
	}
}

// Where side requests have no place of their own, a side request asked
// beside the session's own waits for it, as any other: it would take the
// conversation's place in the model server's cache.
func TestSideBesideWithoutAPlaceWaits(t *testing.T) {
	r := newRig(t, func(o *Options) { o.Model.Local = true })
	if r.a.SidePlace() {
		t.Fatal("a local model with no SideExtra gives side requests a place of their own")
	}
	wait := make(chan struct{})
	reached := make(chan struct{})
	r.model.push(step{at: once(reached), wait: wait, evs: say("the answer").evs})
	done := make(chan TurnResult, 1)
	go func() { done <- r.run("a long question") }()
	<-reached
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	_, err := r.a.Side(ctx, SideRequest{Purpose: "pace", Prompt: "is this the fastest way?", Beside: true})
	cancel()
	if !errors.Is(err, context.DeadlineExceeded) || r.model.count() != 1 {
		t.Errorf("err = %v, %d requests: the side request went out beside the main one", err, r.model.count())
	}
	close(wait)
	<-done
}

// Side requests have a place of their own with SideExtra, with a small
// model on another server, or with a provider that is not local.
func TestSidePlace(t *testing.T) {
	for _, c := range []struct {
		name string
		mod  func(o *Options)
		want bool
	}{
		{"a local server", func(o *Options) { o.Model.Local = true }, false},
		{"a local server with a slot for side requests", func(o *Options) {
			o.Model.Local = true
			o.Model.SideExtra = map[string]any{"id_slot": 1}
		}, true},
		{"a provider that is not local", nil, true},
		{"a small model on the same server", func(o *Options) {
			o.Model.Local = true
			o.Small = &Model{Provider: &fakeModel{name: "small"}, Info: core.ModelInfo{ID: "tiny"}, Address: o.Model.Address, Local: true}
		}, false},
		{"a small model on another server", func(o *Options) {
			o.Model.Local = true
			o.Small = &Model{Provider: &fakeModel{name: "small"}, Info: core.ModelInfo{ID: "tiny"}, Address: "http://203.0.113.9:8081", Local: true}
		}, true},
	} {
		r := newRig(t, c.mod)
		if got := r.a.SidePlace(); got != c.want {
			t.Errorf("%s: SidePlace = %v, want %v", c.name, got, c.want)
		}
	}
}
