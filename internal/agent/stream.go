package agent

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"runtime/debug"
	"slices"
	"strings"
	"time"

	"github.com/gnosh-ai/gnosh/internal/core"
)

// RetryPolicy says how long the loop waits on a provider that fails. No
// count of tries is in it: a provider is waited for by the clock, and the
// person can stop the wait at any moment. The zero value gives the
// defaults.
type RetryPolicy struct {
	// Pauses are the waits before a failed request is tried again: the
	// first, the second, and so on. The last one repeats. The default is
	// 5, 10 and 20 seconds. The row starts over when a reply completes.
	Pauses []time.Duration
	// GoneAfter is how long a provider that had been answering may show
	// no sign of life before the turn ends: long enough to ride out a
	// model server that restarts or loads a model. The default is five
	// minutes.
	GoneAfter time.Duration
	// NotRunningPause and NotRunningAfter are the same for a provider that
	// has not answered once since its model was chosen: it is tried every
	// NotRunningPause (15 seconds) and called not running after
	// NotRunningAfter (a minute and a half).
	NotRunningPause time.Duration
	NotRunningAfter time.Duration
	// BusyMax is the longest pause while a provider says it is busy or
	// asks for fewer requests. Such a provider is there, so it is waited
	// for without end. The default is a minute.
	BusyMax time.Duration
}

func (p RetryPolicy) fill() RetryPolicy {
	if len(p.Pauses) == 0 {
		p.Pauses = []time.Duration{5 * time.Second, 10 * time.Second, 20 * time.Second}
	}
	if p.GoneAfter <= 0 {
		p.GoneAfter = 5 * time.Minute
	}
	if p.NotRunningPause <= 0 {
		p.NotRunningPause = 15 * time.Second
	}
	if p.NotRunningAfter <= 0 {
		p.NotRunningAfter = 90 * time.Second
	}
	if p.BusyMax <= 0 {
		p.BusyMax = time.Minute
	}
	return p
}

// retryState is what the loop remembers about a provider's failures.
type retryState struct {
	since time.Time // the first failure since the provider last showed life
	n     int       // failures since the last reply that completed
	down  bool      // the provider could not be reached in this stretch
}

// trouble sorts a failure by what waiting for it means.
type trouble int

const (
	// troubleBusy: the provider is there and asks to be asked later.
	troubleBusy trouble = iota
	// troubleUnreachable: the provider cannot be reached, or says it is
	// not ready: it is not running, restarting, or loading a model.
	troubleUnreachable
	// troubleOther: anything else that may pass: a failure on the
	// provider's side, a reply that could not be read, a time limit.
	troubleOther
)

// sortTrouble says which kind of trouble a retryable failure is.
func sortTrouble(pe *core.ProviderError) trouble {
	gateway := pe.Status == 502 || pe.Status == 503 || pe.Status == 504
	switch {
	case pe.RetryAfter > 0, pe.Kind == core.ErrRateLimit:
		return troubleBusy
	case pe.Kind == core.ErrNetwork, gateway:
		return troubleUnreachable
	case pe.Kind == core.ErrTimeout && pe.Timeout == "connect":
		return troubleUnreachable
	case pe.Kind == core.ErrOverloaded:
		return troubleBusy
	}
	return troubleOther
}

// retryStep is what to do about one failure.
type retryStep struct {
	trouble    trouble
	wait       time.Duration
	until      time.Time // when the loop stops waiting; zero when it never does
	giveUp     bool
	notRunning bool // the provider has never answered
}

// next decides what to do about the failure pe, which happened at now.
// answered says whether the provider has answered at all since its model
// was chosen.
func (p RetryPolicy) next(st *retryState, pe *core.ProviderError, answered bool, now time.Time) retryStep {
	if st.since.IsZero() {
		st.since = now
	}
	st.n++
	pause := p.Pauses[min(st.n, len(p.Pauses))-1]
	step := retryStep{trouble: sortTrouble(pe), wait: pause}
	switch step.trouble {
	case troubleBusy:
		// Past the end of the row the pause doubles, up to BusyMax.
		for i := len(p.Pauses); i < st.n && step.wait < p.BusyMax; i++ {
			step.wait *= 2
		}
		step.wait = min(step.wait, p.BusyMax)
		if pe.RetryAfter > 0 {
			step.wait = pe.RetryAfter
		}
		return step
	case troubleUnreachable:
		st.down = true
		if !answered {
			step.notRunning = true
			step.wait = p.NotRunningPause
			step.until = st.since.Add(p.NotRunningAfter)
		} else {
			step.until = st.since.Add(p.GoneAfter)
		}
	default:
		step.until = st.since.Add(p.GoneAfter)
	}
	if !now.Before(step.until) {
		step.giveUp = true
		return step
	}
	step.wait = min(step.wait, step.until.Sub(now))
	return step
}

// failure is a provider failure that ended a turn, in plain words.
type failure struct {
	msg    string
	kind   string
	status int
	cause  error
}

func (f *failure) Error() string { return f.msg }
func (f *failure) Unwrap() error { return f.cause }

// tooLongError means the provider refused a request as too long for the
// model. The same request must never be sent again.
type tooLongError struct{ cause *core.ProviderError }

func (e *tooLongError) Error() string { return e.cause.Error() }
func (e *tooLongError) Unwrap() error { return e.cause }

// errorKind names a provider failure the way hooks and scripts know it.
func errorKind(k core.ErrorKind) string {
	switch k {
	case core.ErrRateLimit:
		return "rate_limit"
	case core.ErrOverloaded:
		return "overloaded"
	case core.ErrAuth:
		return "authentication_failed"
	case core.ErrInvalidRequest:
		return "invalid_request"
	case core.ErrContextTooLong:
		return "context_too_long"
	}
	return "server_error"
}

// whatHappened says what a provider did, to follow its name: "could not be
// reached (connection refused)".
func whatHappened(pe *core.ProviderError) string {
	var s string
	switch pe.Kind {
	case core.ErrNetwork:
		s = "could not be reached"
	case core.ErrTimeout:
		switch pe.Timeout {
		case "connect":
			s = "did not accept a connection in time"
		case "first_token":
			s = "did not start a reply in time"
		case "idle":
			s = "went quiet in the middle of a reply for longer than the time limit"
		default:
			s = "took longer than the time limit"
		}
	case core.ErrRateLimit:
		s = "asked for fewer requests"
	case core.ErrOverloaded:
		s = "said it is too busy"
	case core.ErrAuth:
		s = "refused the key: it is missing, wrong, or not allowed to do this"
	case core.ErrInvalidRequest:
		s = "refused the request as one it cannot serve"
	case core.ErrContextTooLong:
		s = "refused the request as too long for the model"
	case core.ErrBadResponse:
		s = "answered with something that could not be read"
	default:
		s = "failed on its own side"
	}
	if pe.Status != 0 {
		s += fmt.Sprintf(" (HTTP %d)", pe.Status)
	}
	detail := pe.Message
	if detail == "" && pe.Err != nil {
		detail = pe.Err.Error()
	}
	if detail != "" {
		s += ": " + clip(oneLine(detail), 400)
	}
	return s
}

// asProviderError reads any error from a provider as a ProviderError. An
// error that is not one is taken for a failure on the provider's side,
// which may pass.
func asProviderError(err error, m Model) *core.ProviderError {
	var pe *core.ProviderError
	if errors.As(err, &pe) {
		return pe
	}
	return &core.ProviderError{Provider: m.Provider.Name(), Kind: core.ErrServer, Message: err.Error(), Err: err}
}

// reply is what one request brought back.
type reply struct {
	msg    core.Message
	usage  core.Usage
	stop   core.StopReason
	bad    map[string]string
	model  Model
	dur    time.Duration
	first  time.Duration
	marker string // the stop marker the reply ended on, when it counted
}

// empty reports whether nothing at all arrived.
func (r *reply) empty() bool { return r == nil || len(r.msg.Blocks) == 0 }

// silent reports whether the reply holds no text and no tool call.
func (r *reply) silent() bool {
	return len(r.msg.ToolUses()) == 0 && strings.TrimSpace(r.msg.Text()) == ""
}

// exchange sends req and returns the reply, trying again for as long as
// the retry policy says when the provider fails in a way that may pass.
// What the person sees meanwhile is a RetryEvent for every wait, with the
// reason.
//
// It returns errStopped when the person stopped the turn, a *tooLongError
// when the provider says the request does not fit the model, and a
// *failure when the provider's trouble ended the wait. With an error the
// reply holds what had arrived; it is nil when the turn was stopped
// between two tries, with no request out.
func (a *Agent) exchange(ctx context.Context, t *turn, purpose string, req *core.Request, est int, m Model, show bool) (*reply, error) {
	if a.answeredFor != modelKey(m) {
		a.answered, a.answeredFor = false, modelKey(m)
		a.rs = retryState{}
	}
	round := 0
	if t != nil {
		round = t.requests
	}
	for attempt := 1; ; attempt++ {
		send := req
		if f := a.opts.Outgoing; f != nil {
			send = cloneRequest(req)
			f(send)
		}
		a.emit(&RequestEvent{
			Purpose: purpose, Round: round, Attempt: attempt,
			Provider: m.Provider.Name(), Model: m.Info.ID,
			Messages: len(send.Messages), Estimate: a.predict(est, m),
		})
		rep, err := a.stream(ctx, t, purpose, send, m, show)
		a.count(t, purpose, m, rep)
		if err == nil {
			a.rs = retryState{}
			if purpose == "main" {
				a.counted(est, rep.usage, m)
			}
			return rep, nil
		}
		if ctx.Err() != nil {
			return rep, errStopped
		}

		pe := asProviderError(err, m)
		if pe.Kind == core.ErrContextTooLong {
			return rep, &tooLongError{cause: pe}
		}
		if !pe.Retryable() {
			return rep, &failure{
				msg:  fmt.Sprintf("%s %s.%s", m.label(), whatHappened(pe), a.turnEndedWords()),
				kind: errorKind(pe.Kind), status: pe.Status, cause: pe,
			}
		}

		now := a.clock.Now()
		step := a.retry.next(&a.rs, pe, a.answered, now)
		if step.giveUp {
			waited := now.Sub(a.rs.since)
			a.rs = retryState{}
			msg := fmt.Sprintf("%s did not come back within %s: it %s.", m.label(), span(waited), whatHappened(pe))
			if step.notRunning {
				other := "pick another model with /model"
				if !a.opts.Interactive {
					other = "choose another model with -m"
				}
				msg = fmt.Sprintf("%s is not running: it has not answered once in %s, and it %s. Start it, or %s.",
					m.label(), span(waited), whatHappened(pe), other)
			} else if step.trouble == troubleOther {
				msg = fmt.Sprintf("%s kept failing for %s: it %s.", m.label(), span(waited), whatHappened(pe))
			}
			return rep, &failure{
				msg:  msg + a.turnEndedWords(),
				kind: errorKind(pe.Kind), status: pe.Status, cause: pe,
			}
		}

		msg := fmt.Sprintf("%s %s. Trying again in %s", m.label(), whatHappened(pe), span(step.wait))
		switch {
		case step.notRunning:
			msg += fmt.Sprintf("; if it has not answered by %s from the first failure, gnosh calls it not running.", span(a.retry.NotRunningAfter))
		case step.trouble == troubleUnreachable:
			msg += fmt.Sprintf("; it may be restarting or loading a model, and gnosh waits up to %s for it to come back.", span(a.retry.GoneAfter))
		case step.trouble == troubleBusy:
			msg += "; gnosh keeps asking for as long as it says it is busy."
		default:
			msg += "."
		}
		if a.opts.Interactive {
			// (A run has nobody at a keyboard to press it.)
			msg += " Esc stops the wait."
		}
		a.emit(&RetryEvent{
			Purpose: purpose, Attempt: a.rs.n, Wait: step.wait, Until: step.until,
			Kind: pe.Kind, Status: pe.Status, Message: msg, Discarded: !rep.empty(),
		})
		a.setPhase(PhaseRetry, msg)
		select {
		case <-a.clock.After(step.wait):
		case <-ctx.Done():
			// No request is out, and what had arrived of the failed try
			// was already thrown away: there is no reply to hand back.
			return nil, errStopped
		}
	}
}

// cloneRequest copies req deeply enough that changing the copy's messages
// and blocks leaves the original, and the log's own view, as they were.
func cloneRequest(req *core.Request) *core.Request {
	c := *req
	c.System = slices.Clone(req.System)
	c.Tools = slices.Clone(req.Tools)
	c.Stop = slices.Clone(req.Stop)
	c.Extra = maps.Clone(req.Extra)
	c.Messages = make([]core.Message, len(req.Messages))
	for i, m := range req.Messages {
		c.Messages[i] = core.Message{Role: m.Role, Blocks: cloneBlocks(m.Blocks)}
	}
	return &c
}

func cloneBlocks(blocks []core.Block) []core.Block {
	out := slices.Clone(blocks)
	for i := range out {
		if len(out[i].Content) > 0 {
			out[i].Content = cloneBlocks(out[i].Content)
		}
	}
	return out
}

// count adds what one request cost to the turn and to the session.
func (a *Agent) count(t *turn, purpose string, m Model, rep *reply) {
	if rep == nil {
		return
	}
	if t != nil && purpose == "main" {
		t.api += rep.dur
		t.usage = addUsage(t.usage, rep.usage)
	}
	a.mu.Lock()
	a.totals.add(purpose, m, rep.usage, rep.dur)
	a.mu.Unlock()
}

// alive notes that the provider has sent something: it is there.
func (a *Agent) alive(m Model) {
	a.answered = true
	a.live.Store(modelKey(m))
	if !a.rs.since.IsZero() {
		if a.rs.down {
			a.notice(LevelInfo, "provider", fmt.Sprintf("%s is answering again, %s after it stopped.", m.label(), span(a.clock.Now().Sub(a.rs.since))))
		}
		a.rs.since = time.Time{}
		a.rs.down = false
	}
}

// streamItem is one step of a provider's answer, or its failure.
type streamItem struct {
	ev  core.Event
	err error
}

// stream sends req once and reads the answer as it arrives. The provider
// is read on a goroutine of its own, so that the session's goroutine can
// always be stopped: when the turn is stopped and the provider has not
// ended its answer once the grace is over, stream goes on without it.
func (a *Agent) stream(ctx context.Context, t *turn, purpose string, req *core.Request, m Model, show bool) (*reply, error) {
	rep := &reply{model: m}

	// One request at a time: a side request waits for the main one, and
	// the other way round, so that neither pushes the other out of a
	// model server's cache while it is being answered.
	a.setPhase(PhaseRequest, "waiting for another request to "+m.label()+" to finish")
	select {
	case a.modelLock <- struct{}{}:
	case <-ctx.Done():
		return rep, ctx.Err()
	}
	defer func() { <-a.modelLock }()

	began := a.clock.Now()
	a.setPhase(PhaseRequest, "waiting for "+m.label()+" to start its reply")
	sctx, cancel := context.WithCancel(ctx)
	defer cancel()
	items := make(chan streamItem, 64)
	quit := make(chan struct{})
	go func() {
		defer close(items)
		defer func() {
			if p := recover(); p != nil {
				a.panicked("the provider", p, debug.Stack())
				select {
				case items <- streamItem{err: fmt.Errorf("the provider crashed: %v", p)}:
				case <-quit:
				}
			}
		}()
		for ev, err := range m.Provider.Stream(sctx, req) {
			select {
			case items <- streamItem{ev: ev, err: err}:
			case <-quit:
				return
			}
			if err != nil {
				return
			}
		}
	}()

	var acc core.Accumulator
	renamed := map[string]string{} // the provider's id of a tool call -> the id it goes by
	var hold *holdback
	if show && t != nil && t.cp != nil {
		hold = &holdback{marker: t.cp.marker}
	}
	finish := func() {
		rep.msg, rep.usage, rep.stop, rep.bad = acc.Message(), acc.Usage(), acc.StopReason(), acc.BadInputs()
		rep.msg.Blocks = wordsOnly(rep.msg.Blocks)
		rep.dur = a.clock.Now().Sub(began)
	}
	// flush shows text that was held back and turned out not to be a
	// marker that counts.
	flush := func(counts bool) {
		if hold == nil || hold.held == "" {
			return
		}
		if !counts || !isMarkerLine(hold.held, hold.marker) {
			a.emit(&TextEvent{Text: hold.held})
		}
		hold.held = ""
	}

	stopped := ctx.Done()
	var grace <-chan time.Time
	first := true
	for {
		select {
		case it, ok := <-items:
			if !ok || it.err != nil {
				finish()
				switch {
				case ctx.Err() != nil:
					flush(false)
					return rep, ctx.Err()
				case ok:
					flush(false)
					return rep, it.err
				case rep.stop == "":
					flush(false)
					return rep, &core.ProviderError{
						Provider: m.Provider.Name(), Kind: core.ErrBadResponse,
						Message: "the reply ended without saying that it was complete",
					}
				}
				flush(len(rep.msg.ToolUses()) == 0 && rep.stop != core.StopMaxTokens && rep.stop != core.StopRefusal)
				return rep, nil
			}
			if first {
				first = false
				rep.first = a.clock.Now().Sub(began)
				a.alive(m)
				a.setPhase(PhaseReply, "reading the reply from "+m.label())
			}
			// Every tool call goes by an id nobody else in the session
			// has: results are matched to calls by it, and so is the
			// stretch of conversation a popped frame takes out.
			switch it.ev.Type {
			case core.EventToolUseStart:
				id := a.callID(it.ev.ID)
				renamed[it.ev.ID] = id
				it.ev.ID = id
			case core.EventToolUseDelta, core.EventToolUseStop:
				if id, ok := renamed[it.ev.ID]; ok {
					it.ev.ID = id
				}
			}
			acc.Add(it.ev)
			if show {
				a.piece(it.ev, hold)
			}
		case <-stopped:
			// The person stopped the turn. The provider is told, and given
			// a moment to end its answer.
			stopped = nil
			cancel()
			timer := time.NewTimer(a.grace)
			defer timer.Stop()
			grace = timer.C
		case <-grace:
			close(quit)
			finish()
			flush(false)
			a.notice(LevelWarning, "provider", fmt.Sprintf(
				"%s had not ended its reply %s after the turn was stopped. gnosh went on without it.", m.label(), span(a.grace)))
			return rep, ctx.Err()
		}
	}
}

// callID returns the id a tool call goes by: the provider's own, unless
// it gave none or one that the session has seen before. Some model
// servers number the calls of every reply from zero. With Options.CallID
// set, every call goes by an id the wiring makes.
func (a *Agent) callID(id string) string {
	if a.calls == nil {
		a.calls = map[string]bool{}
	}
	if f := a.opts.CallID; f != nil {
		// An id the session has seen is not taken twice, whatever the
		// wiring's function returns; after a few tries the loop makes one
		// itself rather than ask for ever.
		for range 8 {
			if made := f(); made != "" && !a.calls[made] {
				a.calls[made] = true
				return made
			}
		}
		id = ""
	}
	if id == "" {
		id = "call_" + a.newID()
	}
	for unique := id; ; unique = id + "_" + a.newID() {
		if !a.calls[unique] {
			a.calls[unique] = true
			return unique
		}
	}
}

// wordsOnly drops the text blocks of a reply that hold nothing but blanks.
// A model now and then writes a line end before a tool call; sent back as
// a block of its own it is a blank block, which no message may hold, and
// a provider that refuses it would refuse every later request too.
func wordsOnly(blocks []core.Block) []core.Block {
	out := blocks[:0:0]
	for _, b := range blocks {
		if b.Type == core.BlockText && strings.TrimSpace(b.Text) == "" {
			continue
		}
		out = append(out, b)
	}
	return out
}

// piece tells the consumer of events about one step of an answer.
func (a *Agent) piece(ev core.Event, hold *holdback) {
	switch ev.Type {
	case core.EventTextDelta:
		text := ev.Text
		if hold != nil {
			text = hold.feed(text)
		}
		if text != "" {
			a.emit(&TextEvent{Text: text})
		}
	case core.EventThinkingDelta:
		if ev.Text != "" {
			a.emit(&ThinkingEvent{Text: ev.Text})
		}
	case core.EventToolUseStart:
		a.emit(&ToolCallEvent{CallID: ev.ID, Name: ev.Name})
	case core.EventToolUseDelta:
		if ev.Text != "" {
			a.emit(&ToolInputEvent{CallID: ev.ID, Text: ev.Text})
		}
	}
}

// holdback keeps the end of a reply to a checkpoint from the consumer of
// events for as long as it might be the stop marker, which the person is
// not shown.
type holdback struct {
	marker string
	held   string
}

// feed takes the next piece of the answer and returns what may be shown
// now. What it keeps is the last line that is not blank, with everything
// after it, when that line could still become the marker.
func (h *holdback) feed(s string) string {
	s = h.held + s
	h.held = ""
	body := strings.TrimRight(s, " \t\r\n")
	i := strings.LastIndexByte(body, '\n') + 1
	if couldBeMarker(body[i:], h.marker) {
		h.held = s[i:]
		return s[:i]
	}
	return s
}

// markerDress is what a model may dress a word in: backquotes, asterisks.
const markerDress = "`*"

// isMarkerLine reports whether line is marker and nothing else. Blanks
// around the marker do not matter, and neither does its dress.
func isMarkerLine(line, marker string) bool {
	line = strings.TrimSpace(line)
	return line == marker || strings.TrimSpace(strings.Trim(line, markerDress)) == marker
}

// couldBeMarker reports whether line is the marker or the beginning of
// it, so that more text could still make it the marker.
func couldBeMarker(line, marker string) bool {
	line = strings.TrimSpace(line)
	if line == "" {
		return false
	}
	core := strings.TrimSpace(strings.TrimLeft(line, markerDress))
	switch {
	case core == "":
		return true // only its dress so far
	case strings.HasPrefix(marker, core):
		return true
	case strings.HasPrefix(core, marker):
		return strings.Trim(core[len(marker):], markerDress+" \t") == ""
	}
	return false
}

// cutMarker reports whether the last line of text that is not blank is
// marker, and returns the text before it.
func cutMarker(text, marker string) (string, bool) {
	body := strings.TrimRight(text, " \t\r\n")
	i := strings.LastIndexByte(body, '\n') + 1
	if !isMarkerLine(body[i:], marker) {
		return text, false
	}
	return strings.TrimRight(body[:i], " \t\r\n"), true
}

// hasLine reports whether word stands alone on a line of text. A line
// that only mentions it in a sentence does not count.
func hasLine(text, word string) bool {
	for line := range strings.SplitSeq(text, "\n") {
		if isMarkerLine(line, word) {
			return true
		}
	}
	return false
}

// addUsage adds two token counts.
func addUsage(a, b core.Usage) core.Usage {
	return core.Usage{
		InputTokens:      a.InputTokens + b.InputTokens,
		OutputTokens:     a.OutputTokens + b.OutputTokens,
		CacheReadTokens:  a.CacheReadTokens + b.CacheReadTokens,
		CacheWriteTokens: a.CacheWriteTokens + b.CacheWriteTokens,
	}
}

// Totals is what a session has cost so far.
type Totals struct {
	// Main adds up the session's own main requests, Requests counts them
	// and APITime is how long they took. Last is the most recent main
	// request's own count; it is zero before the first.
	Main     core.Usage
	Requests int
	APITime  time.Duration
	Last     core.Usage
	// ByModel adds up every request made for the session: main requests,
	// summaries, side requests, and the requests of its sub-agents.
	ByModel map[string]ModelTotals
	// Side counts the side requests alone, so that what the small jobs
	// cost a model server can be watched.
	Side SideTotals
}

// ModelTotals is what one model has cost a session.
type ModelTotals struct {
	// Usage adds up the token counts, Requests counts the requests, and
	// APITime is how long they took.
	Usage    core.Usage
	Requests int
	APITime  time.Duration
	// ContextWindow and MaxOutput are the model's, as the session was
	// told.
	ContextWindow int
	MaxOutput     int
}

// SideTotals counts side requests.
type SideTotals struct {
	// Requests counts the side requests, Usage adds up their token counts,
	// and Time is how long they took.
	Requests int
	Usage    core.Usage
	Time     time.Duration
	// ByPurpose counts them by what they were for.
	ByPurpose map[string]int
}

// add counts one request. The caller holds the session's lock.
func (t *Totals) add(purpose string, m Model, u core.Usage, d time.Duration) {
	if t.ByModel == nil {
		t.ByModel = map[string]ModelTotals{}
	}
	mt := t.ByModel[m.Info.ID]
	mt.Usage = addUsage(mt.Usage, u)
	mt.Requests++
	mt.APITime += d
	mt.ContextWindow, mt.MaxOutput = m.Info.ContextWindow, m.Info.MaxOutput
	t.ByModel[m.Info.ID] = mt
	switch purpose {
	case "main":
		t.Main = addUsage(t.Main, u)
		t.Requests++
		t.APITime += d
		t.Last = u
	case "summary":
	default:
		if t.Side.ByPurpose == nil {
			t.Side.ByPurpose = map[string]int{}
		}
		t.Side.Requests++
		t.Side.Usage = addUsage(t.Side.Usage, u)
		t.Side.Time += d
		t.Side.ByPurpose[purpose]++
	}
}

// merge adds a sub-agent's totals to its parent's by-model figures. The
// caller holds the session's lock.
func (t *Totals) merge(sub Totals) {
	if t.ByModel == nil {
		t.ByModel = map[string]ModelTotals{}
	}
	for id, s := range sub.ByModel {
		mt := t.ByModel[id]
		mt.Usage = addUsage(mt.Usage, s.Usage)
		mt.Requests += s.Requests
		mt.APITime += s.APITime
		if mt.ContextWindow == 0 {
			mt.ContextWindow, mt.MaxOutput = s.ContextWindow, s.MaxOutput
		}
		t.ByModel[id] = mt
	}
}

// Usage returns what the session has cost so far.
func (a *Agent) Usage() Totals {
	a.mu.Lock()
	defer a.mu.Unlock()
	out := a.totals
	out.ByModel = make(map[string]ModelTotals, len(a.totals.ByModel))
	for k, v := range a.totals.ByModel {
		out.ByModel[k] = v
	}
	out.Side.ByPurpose = make(map[string]int, len(a.totals.Side.ByPurpose))
	for k, v := range a.totals.Side.ByPurpose {
		out.Side.ByPurpose[k] = v
	}
	return out
}

// turnEndedWords end the words of a turn that failed: what is kept, and
// for a person, that what they queued still runs.
func (a *Agent) turnEndedWords() string {
	if !a.opts.Interactive {
		return " The turn has ended; the conversation is kept."
	}
	return " The turn has ended; the conversation is kept, and what you queued still runs."
}
