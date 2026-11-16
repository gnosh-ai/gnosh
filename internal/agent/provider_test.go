package agent

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gnosh-ai/gnosh/internal/core"
)

// The retry policy is a pure decision: what to do about one failure, given
// what came before and what time it is. No count of tries is in it.
func TestRetryPolicy(t *testing.T) {
	p := RetryPolicy{}.fill()
	t0 := time.Date(2026, 11, 13, 9, 0, 0, 0, time.UTC)
	network := &core.ProviderError{Kind: core.ErrNetwork}
	loading := &core.ProviderError{Kind: core.ErrOverloaded, Status: 503}
	server := &core.ProviderError{Kind: core.ErrServer, Status: 500}
	idle := &core.ProviderError{Kind: core.ErrTimeout, Timeout: "idle"}
	connect := &core.ProviderError{Kind: core.ErrTimeout, Timeout: "connect"}
	limited := &core.ProviderError{Kind: core.ErrRateLimit, Status: 429}
	told := &core.ProviderError{Kind: core.ErrRateLimit, Status: 429, RetryAfter: 42 * time.Second}
	busy := &core.ProviderError{Kind: core.ErrOverloaded, Status: 529}

	t.Run("a server that never answered is called not running after a minute and a half", func(t *testing.T) {
		var st retryState
		now := t0
		tries := 0
		for {
			step := p.next(&st, network, false, now)
			if step.giveUp {
				if !step.notRunning {
					t.Error("the step does not say that the server never answered")
				}
				break
			}
			if step.wait != 15*time.Second || step.trouble != troubleUnreachable {
				t.Fatalf("step = %+v", step)
			}
			if !step.until.Equal(t0.Add(90 * time.Second)) {
				t.Fatalf("until = %s", step.until)
			}
			now = now.Add(step.wait)
			tries++
			if tries > 100 {
				t.Fatal("the policy never gave up")
			}
		}
		if got := now.Sub(t0); got != 90*time.Second {
			t.Errorf("gave up after %s, want 1m30s", got)
		}
	})

	t.Run("a server that was answering is waited for five minutes", func(t *testing.T) {
		for _, pe := range []*core.ProviderError{network, loading, connect} {
			var st retryState
			now := t0
			var waits []time.Duration
			for {
				step := p.next(&st, pe, true, now)
				if step.giveUp {
					break
				}
				if step.trouble != troubleUnreachable || step.notRunning {
					t.Fatalf("step = %+v", step)
				}
				waits = append(waits, step.wait)
				now = now.Add(step.wait)
			}
			if got := now.Sub(t0); got != 5*time.Minute {
				t.Errorf("%s: gave up after %s, want 5m", pe.Kind, got)
			}
			if waits[0] != 5*time.Second || waits[1] != 10*time.Second || waits[2] != 20*time.Second || waits[3] != 20*time.Second {
				t.Errorf("%s: pauses %v, want 5s 10s 20s 20s …", pe.Kind, waits[:4])
			}
			if !st.down {
				t.Error("the state does not say the server was down")
			}
		}
	})

	t.Run("other failures are tried again on the same clock", func(t *testing.T) {
		for _, pe := range []*core.ProviderError{server, idle} {
			var st retryState
			step := p.next(&st, pe, true, t0)
			if step.trouble != troubleOther || step.wait != 5*time.Second || !step.until.Equal(t0.Add(5*time.Minute)) {
				t.Errorf("%s: step = %+v", pe.Kind, step)
			}
			// A try that itself takes a quarter of an hour is past the
			// clock when it fails again.
			if step = p.next(&st, pe, true, t0.Add(15*time.Minute)); !step.giveUp {
				t.Errorf("%s: no give-up after the clock ran out: %+v", pe.Kind, step)
			}
		}
	})

	t.Run("a provider that says it is busy is waited for without end", func(t *testing.T) {
		var st retryState
		now := t0
		var waits []time.Duration
		for i := 0; i < 500; i++ {
			pe := limited
			if i%2 == 1 {
				pe = busy
			}
			step := p.next(&st, pe, true, now)
			if step.giveUp || !step.until.IsZero() || step.trouble != troubleBusy {
				t.Fatalf("try %d: step = %+v", i, step)
			}
			waits = append(waits, step.wait)
			now = now.Add(step.wait)
		}
		want := []time.Duration{5 * time.Second, 10 * time.Second, 20 * time.Second, 40 * time.Second, time.Minute, time.Minute}
		for i, w := range want {
			if waits[i] != w {
				t.Errorf("pauses %v, want %v …", waits[:6], want)
				break
			}
		}
		if step := p.next(&st, told, true, now); step.wait != 42*time.Second {
			t.Errorf("the wait the provider asked for was not taken: %+v", step)
		}
	})

	t.Run("the clock starts at the first failure, and a sign of life starts it over", func(t *testing.T) {
		var st retryState
		p.next(&st, network, true, t0)
		step := p.next(&st, network, true, t0.Add(4*time.Minute))
		if step.giveUp || step.wait != 10*time.Second {
			t.Fatalf("step = %+v", step)
		}
		// A piece arrives: the loop clears since. An hour later the next
		// failure has the whole clock again.
		st.since = time.Time{}
		later := t0.Add(time.Hour)
		step = p.next(&st, network, true, later)
		if step.giveUp || !step.until.Equal(later.Add(5*time.Minute)) {
			t.Fatalf("an old failure used up the new one's clock: %+v", step)
		}
	})
}

// A run has nobody at a keyboard: a failure that ends its turn says
// nothing of what a person queued.
func TestProviderFailureWordsInARun(t *testing.T) {
	_, res := endsWith(t, func(o *Options) { o.Interactive = false }, broken(core.ErrAuth, 401))
	if res.Err == nil {
		t.Fatalf("result = %+v", res)
	}
	if msg := res.Err.Error(); !strings.Contains(msg, "The turn has ended; the conversation is kept.") || strings.Contains(msg, "what you queued") {
		t.Errorf("a run's failure says %q", msg)
	}
}

// endsWith runs one prompt against a script and returns how it ended.
func endsWith(t *testing.T, mod func(o *Options), steps ...step) (*rig, TurnResult) {
	t.Helper()
	r := newRig(t, mod)
	r.model.push(steps...)
	res := r.run("go")
	r.idle()
	r.checkLog()
	return r, res
}

func TestProviderFailuresThatEndTheTurn(t *testing.T) {
	cases := []struct {
		name   string
		kind   core.ErrorKind
		status int
		want   string // the kind in the result
		says   string // part of the message
	}{
		{"the key is refused", core.ErrAuth, 401, "authentication_failed", "refused the key"},
		{"the request is refused", core.ErrInvalidRequest, 400, "invalid_request", "refused the request"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			var ended []TurnResult
			var asked int
			r, res := endsWith(t, func(o *Options) {
				o.Moments.TurnEnded = func(_ context.Context, res TurnResult) { ended = append(ended, res) }
				o.Drivers = []Driver{{Name: "stop-hook", Decide: func(context.Context, TurnEnd) (Verdict, error) {
					asked++
					return Verdict{}, nil
				}}}
			}, broken(c.kind, c.status))
			if res.Reason != EndFailed || res.ErrorKind != c.want || res.Status != c.status || res.Err == nil {
				t.Fatalf("result = %+v", res)
			}
			// The message names the provider, the model and the address,
			// and says what happened in plain words.
			msg := res.Err.Error()
			for _, part := range []string{"fake (big) at http://203.0.113.7:8080", c.says, "scripted", "what you queued still runs"} {
				if !strings.Contains(msg, part) {
					t.Errorf("the failure %q does not say %q", msg, part)
				}
			}
			if r.model.count() != 1 {
				t.Errorf("a failure that cannot pass was tried %d times", r.model.count())
			}
			if len(ended) != 1 || ended[0].Reason != EndFailed {
				t.Errorf("TurnEnded was told %+v", ended)
			}
			if asked != 0 {
				t.Error("a turn-end driver was asked about a failed turn")
			}
			if n := notices(r.rec, LevelError); len(n) != 1 || !strings.Contains(n[0], "The turn failed") {
				t.Errorf("error notices = %v", n)
			}
			var pe *core.ProviderError
			if !errors.As(res.Err, &pe) || pe.Kind != c.kind {
				t.Errorf("the failure does not wrap the provider's error: %v", res.Err)
			}
		})
	}
}

func TestProviderFailuresThatPass(t *testing.T) {
	cases := []struct {
		name string
		fail step
		says string
	}{
		{"a rate limit", broken(core.ErrRateLimit, 429), "asked for fewer requests"},
		{"an overloaded provider", broken(core.ErrOverloaded, 529), "too busy"},
		{"a failure on the provider's side", broken(core.ErrServer, 500), "failed on its own side (HTTP 500)"},
		{"an answer that cannot be read", broken(core.ErrBadResponse, 0), "could not be read"},
		{"a reply that went quiet", step{err: &core.ProviderError{Provider: "fake", Kind: core.ErrTimeout, Timeout: "idle", Message: "nothing received for 15m0s"}}, "went quiet"},
		{"a reply that never started", step{err: &core.ProviderError{Provider: "fake", Kind: core.ErrTimeout, Timeout: "first_token"}}, "did not start a reply in time"},
		{"an error that is not a provider's", step{err: errors.New("unexpected EOF")}, "unexpected EOF"},
		{"a reply that ends without a stop", step{evs: []core.Event{{Type: core.EventTextDelta, Text: "half"}}, noStop: true}, "ended without saying that it was complete"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r, res := endsWith(t, nil, c.fail, c.fail, say("there you are"))
			if res.Reason != EndCompleted || res.Text != "there you are" {
				t.Fatalf("result = %+v", res)
			}
			if r.model.count() != 3 {
				t.Errorf("%d requests, want 3", r.model.count())
			}
			retries := eventsOf[*RetryEvent](r.rec)
			if len(retries) != 2 || retries[0].Attempt != 1 || retries[1].Attempt != 2 {
				t.Fatalf("retry events = %+v", retries)
			}
			if retries[0].Wait != time.Millisecond || retries[1].Wait != 2*time.Millisecond {
				t.Errorf("waits = %s, %s", retries[0].Wait, retries[1].Wait)
			}
			for _, part := range []string{"fake (big) at http://203.0.113.7:8080", c.says, "Trying again in", "Esc stops the wait"} {
				if !strings.Contains(retries[0].Message, part) {
					t.Errorf("the person is told %q, which does not say %q", retries[0].Message, part)
				}
			}
			// Only the reply that completed is in the log.
			if as := r.log.of(EntryAssistant); len(as) != 1 || as[0].Message.Text() != "there you are" {
				t.Errorf("assistant entries = %+v", as)
			}
			// The same request went out each time.
			reqs := r.model.requests()
			if len(reqs[0].Messages) != len(reqs[2].Messages) || lastUserText(reqs[0]) != lastUserText(reqs[2]) {
				t.Errorf("a retry sent a different request")
			}
		})
	}
}

// The wait a provider asks for is the wait that is made.
func TestRetryAfterIsHonoured(t *testing.T) {
	r, res := endsWith(t, nil,
		step{err: &core.ProviderError{Provider: "fake", Kind: core.ErrRateLimit, Status: 429, RetryAfter: 7 * time.Millisecond}},
		say("ok"))
	if res.Reason != EndCompleted {
		t.Fatalf("result = %+v", res)
	}
	if retry := eventsOf[*RetryEvent](r.rec); len(retry) != 1 || retry[0].Wait != 7*time.Millisecond || !retry[0].Until.IsZero() {
		t.Errorf("retry = %+v", retry)
	}
}

// A reply that had begun to arrive when the request failed is thrown away:
// the next try writes it afresh, and a tool call cut off in it never runs.
func TestRetryDropsWhatHadArrived(t *testing.T) {
	ran := 0
	r, res := endsWith(t, func(o *Options) {
		o.Tools = []core.Tool{tool("Edit", func(context.Context, core.ToolCall, *core.ToolEnv) (core.ToolResult, error) {
			ran++
			return core.TextResult("edited"), nil
		})}
	},
		step{
			evs: append([]core.Event{{Type: core.EventTextDelta, Text: "I will edit"}},
				core.Event{Type: core.EventToolUseStart, ID: "cut", Name: "Edit"},
				core.Event{Type: core.EventToolUseDelta, ID: "cut", Text: `{"file_`}),
			err: &core.ProviderError{Provider: "fake", Kind: core.ErrNetwork, Message: "connection reset"},
		},
		use(tc("whole", "Edit", `{"file_path":"/x"}`)),
		say("done"),
	)
	if res.Reason != EndCompleted || ran != 1 {
		t.Fatalf("result = %+v, the tool ran %d times", res, ran)
	}
	retry := eventsOf[*RetryEvent](r.rec)
	if len(retry) != 1 || !retry[0].Discarded {
		t.Errorf("retry = %+v", retry)
	}
	for _, e := range r.log.of(EntryAssistant) {
		if strings.Contains(e.Message.Text(), "I will edit") {
			t.Error("the reply that was thrown away is in the log")
		}
		for _, u := range e.Message.ToolUses() {
			if u.ID == "cut" {
				t.Error("the tool call that was cut off is in the log")
			}
		}
	}
	// A server that had answered and then broke off is said to be back.
	if n := notices(r.rec, LevelInfo); len(n) != 1 || !strings.Contains(n[0], "is answering again") {
		t.Errorf("info notices = %v", n)
	}
}

// A model server that is not there is reported soon, by name, as not
// running; it is not waited for as a restarting one is.
func TestServerThatIsNotRunning(t *testing.T) {
	r := newRig(t, nil)
	r.model.answer(func(*core.Request, int) step {
		return step{err: &core.ProviderError{Provider: "fake", Kind: core.ErrNetwork, Message: "connection refused"}}
	})
	began := time.Now()
	res := r.run("anyone there?")
	if res.Reason != EndFailed || res.ErrorKind != "server_error" {
		t.Fatalf("result = %+v", res)
	}
	msg := res.Err.Error()
	for _, part := range []string{"fake (big) at http://203.0.113.7:8080 is not running", "has not answered once", "connection refused", "Start it"} {
		if !strings.Contains(msg, part) {
			t.Errorf("the failure %q does not say %q", msg, part)
		}
	}
	if took := time.Since(began); took > 2*time.Second {
		t.Errorf("it took %s to say so", took)
	}
	retries := eventsOf[*RetryEvent](r.rec)
	if len(retries) < 3 {
		t.Errorf("only %d tries were made", len(retries))
	}
	for _, e := range retries {
		if e.Wait > testRetry.NotRunningPause || e.Until.IsZero() {
			t.Errorf("retry = %+v", e)
		}
	}
	if !strings.Contains(retries[0].Message, "gnosh calls it not running") {
		t.Errorf("the person is told %q", retries[0].Message)
	}
	// The prompt is kept, and the next one still runs.
	r.model.answer(nil)
	r.model.push(say("here now"))
	if res := r.run("and now?"); res.Reason != EndCompleted {
		t.Fatalf("the next prompt = %+v", res)
	}
	r.checkLog()
}

// A model server that restarts in the middle of the work is ridden out:
// the turn waits for it and goes on.
func TestServerRestartIsRiddenOut(t *testing.T) {
	down := &core.ProviderError{Provider: "fake", Kind: core.ErrNetwork, Message: "connection refused"}
	r := newRig(t, func(o *Options) { o.Tools = []core.Tool{tool("Bash", nil)} })
	r.model.push(
		use(tc("a", "Bash", `{}`)),
		step{err: down}, step{err: down}, step{err: down}, step{err: down}, step{err: down},
		say("carried on"),
	)
	res := r.run("work through a restart")
	if res.Reason != EndCompleted || res.Text != "carried on" {
		t.Fatalf("result = %+v", res)
	}
	retries := eventsOf[*RetryEvent](r.rec)
	if len(retries) != 5 {
		t.Fatalf("%d retries", len(retries))
	}
	if !strings.Contains(retries[0].Message, "may be restarting or loading a model") || retries[0].Until.IsZero() {
		t.Errorf("the person is told %q", retries[0].Message)
	}
	if n := notices(r.rec, LevelInfo); len(n) != 1 || !strings.Contains(n[0], "is answering again") {
		t.Errorf("info notices = %v", n)
	}
	r.checkLog()
}

// A server that had been answering and stays gone ends the turn when the
// clock runs out, and the message says it did not come back.
func TestServerThatStaysGone(t *testing.T) {
	r := newRig(t, nil)
	r.model.push(say("hello"))
	if res := r.run("hi"); res.Reason != EndCompleted {
		t.Fatalf("result = %+v", res)
	}
	r.model.answer(func(*core.Request, int) step {
		return step{err: &core.ProviderError{Provider: "fake", Kind: core.ErrNetwork, Message: "connection refused"}}
	})
	res := r.run("still there?")
	if res.Reason != EndFailed {
		t.Fatalf("result = %+v", res)
	}
	if msg := res.Err.Error(); !strings.Contains(msg, "did not come back within") || strings.Contains(msg, "is not running") {
		t.Errorf("failure = %q", msg)
	}
	if n := len(eventsOf[*RetryEvent](r.rec)); n < 5 {
		t.Errorf("only %d tries in the time allowed", n)
	}
	r.checkLog()
}

// Failures an hour apart do not use up each other's clock: each stretch
// of trouble starts its own.
func TestRetryClockIsPerFailure(t *testing.T) {
	clock := &fakeClock{now: time.Date(2026, 11, 13, 9, 0, 0, 0, time.UTC)}
	down := &core.ProviderError{Provider: "fake", Kind: core.ErrServer, Status: 500}
	r := newRig(t, func(o *Options) {
		o.Clock = clock
		o.Retry = RetryPolicy{} // the real waits, on a clock that does not make anyone wait
		o.Tools = []core.Tool{tool("Bash", func(context.Context, core.ToolCall, *core.ToolEnv) (core.ToolResult, error) {
			clock.advance(time.Hour) // a long round
			return core.TextResult("ok"), nil
		})}
	})
	var script []step
	for i := 0; i < 6; i++ {
		// Four minutes of failures, then an answer, then an hour's work.
		for j := 0; j < 12; j++ {
			script = append(script, step{err: down})
		}
		script = append(script, use(tc("c", "Bash", `{}`)))
	}
	script = append(script, say("done"))
	r.model.push(script...)
	res := r.run("a long job on a flaky server")
	if res.Reason != EndCompleted {
		t.Fatalf("result = %+v", res)
	}
	if n := len(eventsOf[*RetryEvent](r.rec)); n != 72 {
		t.Errorf("%d retries, want 72", n)
	}
}

// fakeClock is a clock that a test moves. A wait on it is over at once,
// and moves the clock by its length.
type fakeClock struct {
	mu  sync.Mutex
	now time.Time
}

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *fakeClock) advance(d time.Duration) {
	c.mu.Lock()
	c.now = c.now.Add(d)
	c.mu.Unlock()
}

func (c *fakeClock) After(d time.Duration) <-chan time.Time {
	c.advance(d)
	ch := make(chan time.Time, 1)
	ch <- c.Now()
	return ch
}

// While a request is out, the session can say what it is waiting for, by
// name; and a stream that stalls is stopped at once when the person says
// so.
func TestStalledStream(t *testing.T) {
	reached := make(chan struct{})
	r := newRig(t, nil)
	r.model.push(step{at: once(reached), stall: true})
	done := make(chan TurnResult, 1)
	go func() { done <- r.run("hello?") }()
	<-reached

	deadline := time.Now().Add(bound)
	for r.a.State().Phase != PhaseRequest || !strings.Contains(r.a.State().Waiting, "to start its reply") {
		if time.Now().After(deadline) {
			t.Fatalf("state = %+v", r.a.State())
		}
		time.Sleep(time.Millisecond)
	}
	st := r.a.State()
	if !st.Busy || st.Turn == "" || st.Round != 1 || !strings.Contains(st.Waiting, "fake (big) at http://203.0.113.7:8080") || st.Model != "big" {
		t.Errorf("state = %+v", st)
	}
	began := time.Now()
	r.a.Interrupt()
	res := <-done
	if res.Reason != EndStopped || time.Since(began) > time.Second {
		t.Fatalf("result = %+v after %s", res, time.Since(began))
	}
	r.checkLog()
}

// What the model's thinking level is reaches every request, and a change
// sticks.
func TestThinkingLevels(t *testing.T) {
	r := newRig(t, func(o *Options) {
		o.Model.Levels = []string{"off", "on"}
		o.Model.Level = "on"
		o.Model.MaxTokens = 4096
	})
	r.model.push(say("a"), say("b"), say("c"), say("d"))
	r.run("one")
	if level, ok := r.a.NoThink(); !ok || level != "off" {
		t.Fatalf("NoThink = %q, %v", level, ok)
	}
	r.run("two")
	r.run("three")
	if level, ok := r.a.Think(); !ok || level != "on" {
		t.Fatalf("Think = %q, %v", level, ok)
	}
	if _, err := r.a.SetThinking("high"); err == nil {
		t.Error("a level the model does not have was accepted")
	}
	if level, err := r.a.SetThinking(""); err != nil || level != "" {
		t.Errorf("SetThinking(\"\") = %q, %v", level, err)
	}
	r.run("four")

	reqs := r.model.requests()
	var modes []string
	for _, req := range reqs {
		modes = append(modes, req.Thinking.Mode)
		if req.MaxTokens != 4096 {
			t.Errorf("max tokens = %d", req.MaxTokens)
		}
	}
	if want := []string{"on", "off", "off", ""}; strings.Join(modes, ",") != strings.Join(want, ",") {
		t.Errorf("thinking modes = %v, want %v", modes, want)
	}
	if st := r.a.State(); st.Level != "" || st.Model != "big" {
		t.Errorf("state = %+v", st)
	}

	// A model with an effort dial is sent the level's name; one with no
	// known switch is sent nothing, and says so.
	effort := Model{Provider: r.model, Info: core.ModelInfo{ID: "dial", ContextWindow: 100000}, Levels: []string{"low", "medium", "high"}, Level: "medium"}
	if err := r.a.SetModel(effort); err != nil {
		t.Fatal(err)
	}
	r.model.push(say("e"), say("f"))
	r.run("five")
	last := r.model.requests()[4]
	if last.Model != "dial" || last.Thinking.Effort != "medium" || last.Thinking.Mode != "" {
		t.Errorf("request to a model with a dial = %+v", last.Thinking)
	}
	if err := r.a.SetModel(Model{Provider: r.model, Info: core.ModelInfo{ID: "plain", ContextWindow: 100000}}); err != nil {
		t.Fatal(err)
	}
	if _, ok := r.a.Think(); ok {
		t.Error("Think claims to have switched a model that has no switch")
	}
	r.run("six")
	if th := r.model.requests()[5].Thinking; th != (core.Thinking{}) {
		t.Errorf("request to a model with no switch = %+v", th)
	}
}
