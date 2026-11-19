package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"runtime/debug"
	"slices"
	"strings"
	"time"

	"github.com/gnosh-ai/gnosh/internal/core"
)

// SideRequest is a short question to a model, outside the conversation: a
// name for a todo, a verdict on a goal, a review of a close, a title. It
// is sent with no tools and with as little thinking as the model has.
type SideRequest struct {
	// Purpose says what the request is for: "title", "name", "goal-judge",
	// "close-review". Side requests are counted by it.
	Purpose string
	// System, when not empty, is the request's system prompt.
	System string
	// Messages, when not empty, is a conversation to show the model, and
	// Prompt is the question. With both, the question follows the
	// conversation.
	Messages []core.Message
	Prompt   string
	// MaxTokens caps the answer. Leave headroom: a cap that is too tight
	// makes a model that does think look broken.
	MaxTokens int
	// Temperature is sent when not nil. A judging request wants zero.
	Temperature *float64
	// Timeout is how long each model is given to answer, in real time.
	// Zero means two minutes.
	Timeout time.Duration
	// Yield makes the request give way to the session's own: when the
	// session's next request (a turn's, a summary's) wants the model while
	// this one waits for it or is being answered, this one is stopped and
	// Side returns ErrYielded. A prompt suggestion is asked so: it is worth
	// nothing once a turn has begun, by a prompt, a /loop or a hook.
	Yield bool
	// Session makes the request the session's own next one, with Prompt
	// after the conversation: its system prompt, its tools and its
	// messages as they stand, so that a model server that kept the
	// conversation reads only the question. System and Messages are then
	// not used, the session's own model answers (never the small one), a
	// call of the last reply that has no result yet is answered as still
	// running (Texts.AsideRunning), and a tool call in the answer is
	// dropped: nothing runs, and nothing is kept.
	Session bool
}

// SideResult is the answer to a SideRequest.
type SideResult struct {
	// Text is the answer, read past any thinking tags.
	Text string
	// Provider and Model say who answered.
	Provider string
	Model    string
	// Usage is what the provider counted and Duration how long it took.
	Usage    core.Usage
	Duration time.Duration
	// Fallback says the small model failed or was cut off, and the large
	// one answered.
	Fallback bool
}

// ErrCutOff means a side request's answer hit its cap before the model had
// finished. The result then holds what arrived.
var ErrCutOff = errors.New("the answer was cut off at its length limit")

// Side asks a model a short question and returns the answer. The small
// model is asked first, and the large one if the small one fails or is cut
// off; with no small model the large one is asked alone, so that a session
// never names a model the person did not choose.
//
// Side requests are kept off a model server while the session's own
// request is being answered: Side waits for that request to end, and the
// session's next request waits for Side. Every one is counted (Usage) and
// reported as a SideEvent, because on a local model server each is a new
// prompt that may push a saved conversation out of the cache. Keep them
// few.
//
// Side may be called from any goroutine: from inside a tool, a driver or
// a moment, or from the front end.
func (a *Agent) Side(ctx context.Context, sr SideRequest) (SideResult, error) {
	if strings.TrimSpace(sr.Prompt) == "" && len(sr.Messages) == 0 {
		return SideResult{}, errors.New("agent: a side request needs a prompt")
	}
	su := a.setup()
	if sr.Session && strings.TrimSpace(sr.Prompt) == "" {
		return SideResult{}, errors.New("agent: a side question about the conversation needs a question")
	}
	models := []Model{su.model}
	if !sr.Session && su.small != nil && su.small.Provider != nil && modelKey(*su.small) != modelKey(su.model) {
		models = []Model{*su.small, su.model}
	}
	purpose := sr.Purpose
	if purpose == "" || purpose == "main" || purpose == "summary" {
		purpose = "side"
	}
	timeout := sr.Timeout
	if timeout <= 0 {
		timeout = 2 * time.Minute
	}

	var res SideResult
	var err error
	for i, m := range models {
		res, err = a.sideOnce(ctx, sr, purpose, m, timeout)
		res.Fallback = i > 0
		a.emit(&SideEvent{
			Purpose: purpose, Provider: res.Provider, Model: res.Model,
			Usage: res.Usage, Duration: res.Duration, Fallback: res.Fallback, Err: err,
		})
		if err == nil || ctx.Err() != nil || errors.Is(err, ErrYielded) {
			break
		}
	}
	return res, err
}

// giveWay registers a side request that yields (SideRequest.Yield) and
// returns its context and what takes it off again.
func (a *Agent) giveWay(ctx context.Context) (context.Context, func()) {
	yctx, cancel := context.WithCancelCause(ctx)
	a.yieldMu.Lock()
	if a.yielders == nil {
		a.yielders = map[*context.CancelCauseFunc]struct{}{}
	}
	a.yielders[&cancel] = struct{}{}
	a.yieldMu.Unlock()
	return yctx, func() {
		a.yieldMu.Lock()
		delete(a.yielders, &cancel)
		a.yieldMu.Unlock()
		cancel(nil)
	}
}

// takeModel takes the model for a request that does not yield, stopping
// every side request that does, again and again while it waits: one that
// registered after a stop and took the model first is stopped too.
func (a *Agent) takeModel(ctx context.Context) error {
	tick := time.NewTicker(50 * time.Millisecond)
	defer tick.Stop()
	for {
		a.makeWay()
		select {
		case a.modelLock <- struct{}{}:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		case <-tick.C:
		}
	}
}

// makeWay stops every side request that yields: the session's own request
// wants the model.
func (a *Agent) makeWay() {
	a.yieldMu.Lock()
	defer a.yieldMu.Unlock()
	for cancel := range a.yielders {
		(*cancel)(ErrYielded)
	}
}

// sideOnce asks one model.
func (a *Agent) sideOnce(ctx context.Context, sr SideRequest, purpose string, m Model, timeout time.Duration) (SideResult, error) {
	res := SideResult{Provider: m.Provider.Name(), Model: m.Info.ID}
	if sr.Yield {
		yctx, done := a.giveWay(ctx)
		defer done()
		ctx = yctx
	}
	tctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	stop := context.AfterFunc(a.root, cancel) // a closed session asks nothing more
	defer stop()

	fail := func(what string, err error) error {
		switch {
		case errors.Is(context.Cause(ctx), ErrYielded):
			return ErrYielded
		case ctx.Err() != nil:
			return ctx.Err()
		case a.root.Err() != nil:
			return ErrClosed
		case tctx.Err() != nil:
			return fmt.Errorf("%s did not answer the %s request within %s", m.label(), purpose, span(timeout))
		}
		return fmt.Errorf("%s %s the %s request: %w", m.label(), what, purpose, err)
	}

	if sr.Yield {
		select {
		case a.modelLock <- struct{}{}:
		case <-tctx.Done():
			return res, fail("could not be sent", tctx.Err())
		}
	} else if err := a.takeModel(tctx); err != nil {
		// (One that does not give way, /btw say, makes a suggestion give
		// way to it as the session's own requests do.)
		return res, fail("could not be sent", err)
	}
	defer func() { <-a.modelLock }()

	var req *core.Request
	if sr.Session {
		r, err := a.asideRequest(sr, m)
		if err != nil {
			return res, fail("could not be sent", err)
		}
		req = r
	} else {
		msgs := slices.Clone(sr.Messages)
		if sr.Prompt != "" {
			msgs = withQuestion(msgs, core.Text(sr.Prompt))
		}
		req = &core.Request{
			Model:       m.Info.ID,
			Messages:    msgs,
			MaxTokens:   sr.MaxTokens,
			Temperature: sr.Temperature,
			Thinking:    m.lowestThinking(),
			Extra:       maps.Clone(m.Extra),
		}
		if sr.System != "" {
			req.System = []string{sr.System}
		}
	}
	if f := a.opts.Outgoing; f != nil {
		req = cloneRequest(req)
		f(res.Provider, req)
	}
	a.emit(&RequestEvent{Purpose: purpose, Attempt: 1, Provider: res.Provider, Model: res.Model, Messages: len(req.Messages)})

	type answer struct {
		msg   core.Message
		usage core.Usage
		stop  core.StopReason
		err   error
	}
	began := a.clock.Now()
	ch := make(chan answer, 1)
	go func() {
		defer func() {
			if p := recover(); p != nil {
				a.panicked("the provider", p, debug.Stack())
				ch <- answer{err: fmt.Errorf("the provider crashed: %v", p)}
			}
		}()
		msg, usage, stop, err := core.Collect(m.Provider.Stream(tctx, req))
		ch <- answer{msg, usage, stop, err}
	}()
	var ans answer
	select {
	case ans = <-ch:
	case <-tctx.Done():
		// The provider is given a moment to end its answer, and is then
		// left behind.
		t := time.NewTimer(a.grace)
		select {
		case ans = <-ch:
		case <-t.C:
			ans.err = tctx.Err()
		}
		t.Stop()
	}
	res.Usage, res.Duration = ans.usage, a.clock.Now().Sub(began)
	a.mu.Lock()
	a.totals.add(purpose, m, ans.usage, res.Duration)
	a.mu.Unlock()

	if ans.err != nil {
		return res, fail("could not answer", ans.err)
	}
	res.Text = AnswerText(ans.msg.Text())
	var calls []string
	for _, b := range ans.msg.Blocks {
		if b.Type == core.BlockToolUse {
			calls = append(calls, b.Name)
		}
	}
	switch {
	case ans.stop == core.StopMaxTokens:
		return res, fmt.Errorf("%s answering the %s request: %w", m.label(), purpose, ErrCutOff)
	case res.Text == "" && len(calls) > 0:
		return res, fmt.Errorf("%s answered the %s request with a call to %s and no words, and nothing runs for a side request", m.label(), purpose, strings.Join(calls, ", "))
	case res.Text == "":
		return res, fmt.Errorf("%s answered the %s request with nothing", m.label(), purpose)
	}
	return res, nil
}

// withQuestion puts blocks after a conversation as the user's words: into
// its last message when that is the user's, or in a new one.
func withQuestion(msgs []core.Message, blocks ...core.Block) []core.Message {
	if n := len(msgs); n > 0 && msgs[n-1].Role == core.RoleUser {
		last := msgs[n-1]
		last.Blocks = append(slices.Clone(last.Blocks), blocks...)
		msgs[n-1] = last
		return msgs
	}
	return append(msgs, core.Message{Role: core.RoleUser, Blocks: blocks})
}

// asideRequest is the request of a side question about the conversation
// (SideRequest.Session): the session's next request as it stands, with the
// question after the conversation. A call of the last reply that has no
// result yet is answered as still running, so that the conversation reads
// as one a model can be sent; the session's own request, later, holds the
// real result in that place.
func (a *Agent) asideRequest(sr SideRequest, m Model) (*core.Request, error) {
	su := a.setup()
	v, err := a.view()
	if err != nil {
		return nil, err
	}
	msgs := slices.Clone(v.Messages)
	var blocks []core.Block
	if n := len(msgs); n > 0 && msgs[n-1].Role == core.RoleAssistant {
		for _, b := range msgs[n-1].Blocks {
			if b.Type == core.BlockToolUse {
				blocks = append(blocks, core.ToolResultBlock(b.ID, core.TextResult(a.texts.AsideRunning)))
			}
		}
	}
	msgs = withQuestion(msgs, append(blocks, core.Text(sr.Prompt))...)
	if err := checkView(msgs); err != nil {
		return nil, fmt.Errorf("the conversation does not read as one a model can be sent: %w", err)
	}
	req := a.assemble(su, msgs)
	req.Model = m.Info.ID
	req.Thinking = m.lowestThinking()
	if sr.MaxTokens > 0 {
		req.MaxTokens = sr.MaxTokens
	}
	if sr.Temperature != nil {
		req.Temperature = sr.Temperature
	}
	return req, nil
}

// AnswerText returns a model's short answer read past any thinking it
// wrote into the text between <think> tags. A thought that never closes
// leaves no answer.
func AnswerText(s string) string {
	const open, shut = "<think>", "</think>"
	if i := strings.LastIndex(s, shut); i >= 0 {
		s = s[i+len(shut):]
	}
	s = strings.TrimSpace(s)
	if strings.HasPrefix(s, open) {
		return ""
	}
	return s
}

// JSONObject returns the JSON object in a model's answer: everything from
// the first "{" to the last "}", if that is valid JSON. Models wrap a
// verdict in words and code fences; this reads past them.
func JSONObject(s string) (string, bool) {
	i, j := strings.IndexByte(s, '{'), strings.LastIndexByte(s, '}')
	if i < 0 || j < i {
		return "", false
	}
	obj := s[i : j+1]
	if !json.Valid([]byte(obj)) {
		return "", false
	}
	return obj, true
}
