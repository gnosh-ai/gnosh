package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"runtime/debug"
	"strings"
	"time"

	"github.com/gnosh-ai/gnosh/internal/core"
)

// callState is one tool call of a reply on its way to a result. It is
// touched by the session's goroutine alone; the goroutine that runs the
// tool is handed copies and reports back over a channel.
type callState struct {
	idx      int
	call     core.ToolCall   // as the model wrote it, through the wiring's filter
	input    json.RawMessage // the arguments it runs with
	changed  bool            // they are not what the model wrote
	cleaned  bool            // its result has been through the wiring's filter
	tool     core.Tool
	parallel bool
	action   core.Action
	cannot   error // the tool found that the call cannot run as asked

	res         core.ToolResult
	done        bool // it has its result
	ran         bool // the tool was started
	denied      bool
	unattended  bool
	interrupted bool
	began       time.Time
	dur         time.Duration
	parts       []Part // what AfterTool added for the model
	gone        *late  // set when the loop gave up waiting for the tool
}

// execDone is what the goroutine that ran a tool reports.
type execDone struct {
	idx         int
	res         core.ToolResult
	dur         time.Duration
	interrupted bool
	verdict     ToolVerdict
	hookErr     error
}

// roundOutcome is how the tool calls of one reply went.
type roundOutcome struct {
	calls   []*callState
	stopped bool   // the person stopped the turn
	halt    string // why the turn must end, or empty
	haltBy  string
	err     error // the log could not be written
}

// round runs the tool calls of one reply. The calls that only look run
// side by side, up to a limit; every other call runs alone, after every
// call before it has finished and is stored, and before any call after it
// starts. Results go into the log in the order the model wrote the calls,
// each as soon as every call before it is done.
type round struct {
	a   *Agent
	t   *turn
	ctx context.Context // the turn's

	rctx   context.Context // ends with the turn, and when the round is halted
	cancel context.CancelFunc
	calls  []*callState
	done   chan execDone

	running int
	flushed int // calls[:flushed] are in the log
	stopped bool
	gaveUp  bool
	halt    string
	haltBy  string
	err     error
	timer   *time.Timer
	grace   <-chan time.Time
}

// runTools runs the tool calls of rep and stores their results.
func (a *Agent) runTools(ctx context.Context, t *turn, rep *reply) roundOutcome {
	su := a.setup()
	uses := rep.msg.ToolUses()
	r := &round{a: a, t: t, ctx: ctx, done: make(chan execDone, len(uses))}
	r.rctx, r.cancel = context.WithCancel(ctx)
	defer r.cancel()
	defer func() {
		if r.timer != nil {
			r.timer.Stop()
		}
	}()

	names := make([]string, 0, len(su.specs))
	for _, s := range su.specs {
		names = append(names, s.Name)
	}
	for i, u := range uses {
		c := &callState{
			idx:   i,
			call:  core.ToolCall{ID: u.ID, Name: u.Name, Input: u.Input},
			input: u.Input,
			tool:  su.index[u.Name],
			gone:  new(late),
		}
		switch raw, bad := rep.bad[u.ID]; {
		case c.tool == nil:
			list := "none"
			if len(names) > 0 {
				list = sortedNames(names)
			}
			c.res = core.ErrorResult(fmt.Sprintf(a.texts.UnknownTool, u.Name, list))
			c.done = true
		case bad:
			c.res = core.ErrorResult(fmt.Sprintf(a.texts.BadInput, clip(raw, 400)))
			c.done = true
		default:
			r.prepare(c)
		}
		r.calls = append(r.calls, c)
	}

	a.setPhase(PhaseTools, "running tool calls")
	for i := 0; i < len(r.calls); {
		j := i + 1
		if r.calls[i].parallel {
			for j < len(r.calls) && r.calls[j].parallel {
				j++
			}
		}
		r.group(r.calls[i:j])
		i = j
	}

	// What AfterTool added for the model follows the results, in the
	// order of the calls, so that the results stay together.
	if r.err == nil {
		var notes []Entry
		for _, c := range r.calls {
			for _, p := range c.parts {
				if strings.TrimSpace(p.Text) != "" {
					notes = append(notes, Entry{Kind: EntryNote, Turn: t.id, Source: firstNonEmpty(p.Source, "after-tool"), Blocks: []core.Block{core.Text(p.Text)}})
				}
			}
		}
		r.err = a.appendLog(notes...)
	}
	return roundOutcome{calls: r.calls, stopped: r.stopped || ctx.Err() != nil, halt: r.halt, haltBy: r.haltBy, err: r.err}
}

// prepare readies a call's arguments for everything that reads them: the
// wiring's filter turns them into what the tool is to act on, and
// arguments that name a key twice are refused. From here on the gate, the
// hooks and the tool all see the same arguments.
func (r *round) prepare(c *callState) {
	a := r.a
	if f := a.opts.CleanInput; f != nil {
		in, ok := cleanInput(f, c.call)
		if !ok {
			c.res = core.ErrorResult(fmt.Sprintf(a.texts.ToolFailed, "its arguments could not be made ready for the tool"))
			c.done = true
			return
		}
		if !bytes.Equal(in, c.call.Input) {
			c.call.Input, c.input, c.changed = in, in, true
		}
	}
	// A guard and the tool must never read different values out of the
	// same arguments, so such a call is refused before either sees it.
	if key, dup := duplicateKey(c.call.Input); dup {
		c.res = core.ErrorResult(fmt.Sprintf(a.texts.DuplicateKey, key))
		c.done = true
		return
	}
	if p, ok := c.tool.(Concurrent); ok {
		c.parallel = safely(func() bool { return p.Parallel(c.call) })
	}
}

// cleanInput passes a call's arguments through the wiring's filter. What
// comes back must be one JSON object; a filter that panics, or returns
// anything else, fails the call.
func cleanInput(f func(core.ToolCall) json.RawMessage, call core.ToolCall) (in json.RawMessage, ok bool) {
	defer func() {
		if recover() != nil {
			in, ok = nil, false
		}
	}()
	return jsonObject(f(call))
}

// safely calls f and returns false if it panics.
func safely(f func() bool) (ok bool) {
	defer func() {
		if recover() != nil {
			ok = false
		}
	}()
	return f()
}

// group runs calls that may run side by side, or one call that runs
// alone. The gate is asked about them one at a time, in order, so that a
// person is put one question at a time and an answer that lasts ("yes, and
// do not ask again") reaches the calls after it.
func (r *round) group(g []*callState) {
	for _, c := range g {
		for r.running >= r.a.parallel {
			r.wait(nil)
		}
		r.begin(c)
		if c.done {
			r.record(c)
			continue
		}
		if why, over := r.over(); over {
			r.skip(c, why)
			continue
		}
		dec := r.ask(c)
		if why, over := r.over(); over {
			r.skip(c, why)
			continue
		}
		if !dec.Allow {
			r.refuse(c, dec)
			continue
		}
		if c.cannot != nil {
			// The gate had nothing against what the call was aimed at, so
			// the model may now read why it cannot run.
			// A tool's own sentence may end in a full stop, and the text
			// around it ends in one too.
			c.res = core.ErrorResult(fmt.Sprintf(r.a.texts.BadDescribe, strings.TrimSuffix(c.cannot.Error(), ".")))
			r.record(c)
			continue
		}
		r.start(c)
	}
	for r.running > 0 {
		r.wait(nil)
	}
}

// over reports whether no more calls may start, and what a call that did
// not run is told.
func (r *round) over() (string, bool) {
	if r.ctx.Err() != nil {
		r.noteStop()
	}
	switch {
	case r.err != nil:
		return fmt.Sprintf(r.a.texts.HaltedBefore, "the session's log could not be written"), true
	case r.stopped:
		return r.a.texts.StoppedBefore, true
	case r.halt != "":
		return fmt.Sprintf(r.a.texts.HaltedBefore, oneLine(r.halt)), true
	}
	return "", false
}

// wait blocks until other is ready, and reports true then. It reports
// false after it has dealt with something else, so that the caller looks
// at the round again: a running call finished, the turn was stopped, or
// the grace for calls that did not stop ran out.
func (r *round) wait(other <-chan struct{}) bool {
	var stopped <-chan struct{}
	if !r.stopped {
		stopped = r.ctx.Done()
	}
	select {
	case d := <-r.done:
		r.finish(d)
		return false
	case <-other:
		return true
	case <-stopped:
		r.noteStop()
		return false
	case <-r.grace:
		r.grace = nil
		r.abandon()
		return false
	}
}

// noteStop records that the person stopped the turn.
func (r *round) noteStop() {
	r.stopped = true
	r.startGrace()
}

// startGrace starts the clock on calls that have been told to stop. When
// it runs out, the round goes on without them.
func (r *round) startGrace() {
	if r.timer == nil {
		r.timer = time.NewTimer(r.a.grace)
		r.grace = r.timer.C
	}
}

// begin works out what the call would do and tells the consumer of events
// that it is about to be put to the gate.
func (r *round) begin(c *callState) {
	a := r.a
	if !c.done {
		if d, ok := c.tool.(core.Describer); ok {
			// Beside an error the tool still says what the call was aimed
			// at. The gate is asked about that before the model reads the
			// error, so that a refused path does not give away whether it
			// exists.
			c.action, c.cannot = describe(d, c.call, a.toolEnv(c.call.ID))
		}
		if c.action.Kind == "" {
			c.action.Kind = core.ActionOther
		}
	}
	a.emit(&ToolStartEvent{CallID: c.call.ID, Name: c.call.Name, Input: c.call.Input, Action: c.action, Parallel: c.parallel})
}

// describe asks a tool what a call would do. A panic is an error.
func describe(d core.Describer, call core.ToolCall, env *core.ToolEnv) (act core.Action, err error) {
	defer func() {
		if p := recover(); p != nil {
			err = fmt.Errorf("the tool crashed looking at them: %v", p)
		}
	}()
	return d.Describe(call, env)
}

// ask puts the call to the gate and waits for the answer, which may take
// as long as a person takes. Calls that are already running go on
// meanwhile, and their results are taken in as they arrive.
func (r *round) ask(c *callState) Decision {
	a := r.a
	req := GateRequest{
		Call:       c.call,
		Tool:       c.tool,
		Action:     c.action,
		Env:        a.toolEnv(c.call.ID),
		Session:    a.session,
		AgentID:    a.agentID,
		AgentType:  a.agentType,
		ParentCall: a.parentCall,
		CannotRun:  c.cannot,
		Ask: func(ctx context.Context, q Question) (Answer, error) {
			if c.cannot != nil {
				return Answer{}, ErrCannotRun
			}
			if q.CallID == "" {
				q.CallID = c.call.ID
			}
			if q.Tool == "" {
				q.Tool = c.call.Name
			}
			if q.Action == nil {
				act := c.action
				q.Action = &act
			}
			return a.Ask(ctx, q)
		},
	}
	var (
		dec  Decision
		err  error
		done = make(chan struct{})
	)
	go func() {
		defer close(done)
		defer func() {
			if p := recover(); p != nil {
				a.panicked("the permission check", p, debug.Stack())
				err = fmt.Errorf("it crashed: %v", p)
			}
		}()
		dec, err = a.opts.Gate.Check(r.rctx, req)
	}()
	for !r.wait(done) {
		if r.gaveUp {
			// The check did not come back after the turn was stopped. Its
			// answer, if it ever comes, is not read.
			return Decision{}
		}
	}
	a.setPhase(PhaseTools, "running tool calls")
	if err != nil {
		return Decision{Reason: fmt.Sprintf(a.texts.GateFailed, err)}
	}
	if dec.Allow && dec.Input != nil && c.cannot == nil {
		in, ok := jsonObject(dec.Input)
		if !ok {
			return Decision{Reason: fmt.Sprintf(a.texts.GateFailed, "it changed the call's arguments into something that is not a JSON object")}
		}
		if key, dup := duplicateKey(in); dup {
			return Decision{Reason: fmt.Sprintf(a.texts.GateFailed, fmt.Sprintf("it changed the call's arguments into an object that names the key %q twice", key))}
		}
		c.input, c.changed = in, true
	}
	return dec
}

// duplicateKey returns a key that one object of a JSON value names twice.
// Two parsers handed such an object may each keep a different one of the
// two values, which is how a guard is shown one thing and a tool does
// another.
func duplicateKey(raw json.RawMessage) (string, bool) {
	type frame struct {
		keys map[string]bool // nil for an array
		key  bool            // the next string is a key
	}
	var stack []*frame
	value := func() {
		if n := len(stack); n > 0 && stack[n-1].keys != nil {
			stack[n-1].key = true
		}
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber() // a number too large for a float must not end the scan
	for {
		tok, err := dec.Token()
		if err != nil {
			return "", false
		}
		switch v := tok.(type) {
		case json.Delim:
			switch v {
			case '{':
				stack = append(stack, &frame{keys: map[string]bool{}, key: true})
			case '[':
				stack = append(stack, &frame{})
			default:
				if len(stack) > 0 {
					stack = stack[:len(stack)-1]
				}
				value()
			}
		case string:
			if n := len(stack); n > 0 && stack[n-1].keys != nil && stack[n-1].key {
				top := stack[n-1]
				if top.keys[v] {
					return v, true
				}
				top.keys[v] = true
				top.key = false
				continue
			}
			value()
		default:
			value()
		}
	}
}

// jsonObject reports whether raw is one JSON object, and returns it
// compact.
func jsonObject(raw json.RawMessage) (json.RawMessage, bool) {
	s := bytes.TrimSpace(raw)
	if len(s) == 0 || s[0] != '{' || !json.Valid(s) {
		return nil, false
	}
	var buf bytes.Buffer
	if err := json.Compact(&buf, s); err != nil {
		return nil, false
	}
	return buf.Bytes(), true
}

// skip gives a call that never started the result that says why.
func (r *round) skip(c *callState, why string) {
	c.res = core.ErrorResult(why)
	c.interrupted = true
	r.record(c)
}

// refuse gives a call the gate refused its result.
func (r *round) refuse(c *callState, dec Decision) {
	a := r.a
	reason := dec.Reason
	if reason == "" {
		reason = a.texts.Denied
		if dec.Unattended {
			reason = a.texts.Unattended
		}
	}
	c.res = core.ErrorResult(reason)
	c.denied, c.unattended = true, dec.Unattended
	r.t.denials = append(r.t.denials, Denial{
		ToolName: c.call.Name, ToolUseID: c.call.ID, Input: c.call.Input,
		Reason: reason, Unattended: dec.Unattended,
	})
	if dec.Halt {
		r.haltNow("the person refused permission for "+c.call.Name, "permission")
	}
	r.record(c)
}

// haltNow ends the turn once this round's results are stored. Calls that
// are running are told to stop, and calls that have not started do not.
func (r *round) haltNow(why, by string) {
	if r.halt == "" {
		r.halt, r.haltBy = why, by
	}
	r.cancel()
	r.startGrace()
}

// start runs the call's tool on a goroutine of its own.
func (r *round) start(c *callState) {
	a := r.a
	c.ran = true
	c.began = a.clock.Now()
	r.running++
	call := core.ToolCall{ID: c.call.ID, Name: c.call.Name, Input: c.input}
	a.mu.Lock()
	a.running = append(a.running, RunningCall{CallID: call.ID, Name: call.Name, Since: c.began})
	a.mu.Unlock()
	go a.exec(r.ctx, r.rctx, r.t.id, c.idx, c.tool, call, a.toolEnv(call.ID), c.began, c.gone, r.done)
}

// exec runs one tool call and reports its result. It runs on its own
// goroutine and shares nothing with the session's but what it is handed.
// ctx is the call's own context; it ends when the person stops the turn
// (stopped ends then too) and when the round is ended for another reason.
func (a *Agent) exec(stopped, ctx context.Context, turn string, idx int, tool core.Tool, call core.ToolCall, env *core.ToolEnv, began time.Time, gone *late, out chan<- execDone) {
	d := execDone{idx: idx}
	res, err, crash, stack := runTool(ctx, tool, call, env)
	switch {
	case crash != nil:
		a.panicked("the tool "+call.Name, crash, stack)
		a.notice(LevelError, "tools", fmt.Sprintf("The tool %s crashed (%v). The model was told; the session goes on.", call.Name, crash))
		res = core.ErrorResult(fmt.Sprintf(a.texts.ToolCrashed, crash))
	case err != nil && stopped.Err() != nil:
		// The tool gave up because the person stopped the turn.
		res = core.ErrorResult(a.texts.StoppedDuring)
		d.interrupted = true
	case err != nil && ctx.Err() != nil:
		// The tool gave up because something else ended the turn.
		res = core.ErrorResult(a.texts.HaltedDuring)
		d.interrupted = true
	case err != nil:
		a.notice(LevelWarning, "tools", fmt.Sprintf("gnosh could not run %s: %v. The model was told.", call.Name, err))
		res = core.ErrorResult(fmt.Sprintf(a.texts.ToolFailed, err))
	}
	if f := a.opts.CleanResult; f != nil {
		res = cleanResult(f, call, res)
	}
	res.Content = sayable(res.Content)
	d.res = res
	d.dur = a.clock.Now().Sub(began)

	if f := a.opts.Moments.AfterTool; f != nil {
		info := ToolDone{
			Call: call, Tool: tool, Result: res, Duration: d.dur, Interrupted: d.interrupted || ctx.Err() != nil,
			Turn: turn, Session: a.session, AgentID: a.agentID, AgentType: a.agentType, ParentCall: a.parentCall,
		}
		d.verdict, d.hookErr = afterTool(ctx, f, info)
		if ctx.Err() != nil {
			d.hookErr = nil
		}
	}

	if !gone.deliver(func() { out <- d }) {
		a.notice(LevelWarning, "tools", fmt.Sprintf(
			"%s finished %s after the turn was stopped. Its result was not recorded, because gnosh had already gone on without it.",
			call.Name, span(a.clock.Now().Sub(began))))
	}
}

// sayable returns a tool result's content as a model can be sent it: no
// text block is empty, and a result with nothing in it says so.
func sayable(content []core.Block) []core.Block {
	out := content[:0:0]
	for _, b := range content {
		if b.Type == core.BlockText && strings.TrimSpace(b.Text) == "" {
			continue
		}
		out = append(out, b)
	}
	if len(out) == 0 {
		return []core.Block{core.Text("(no output)")}
	}
	return out
}

// runTool calls a tool and catches a panic in it.
func runTool(ctx context.Context, tool core.Tool, call core.ToolCall, env *core.ToolEnv) (res core.ToolResult, err error, crash any, stack []byte) {
	defer func() {
		if p := recover(); p != nil {
			crash, stack = p, debug.Stack()
		}
	}()
	res, err = tool.Run(ctx, call, env)
	return res, err, nil, nil
}

// cleanResult passes a result through the wiring's function, and keeps the
// result as it was if that function panics.
func cleanResult(f func(core.ToolCall, core.ToolResult) core.ToolResult, call core.ToolCall, res core.ToolResult) (out core.ToolResult) {
	defer func() {
		if p := recover(); p != nil {
			out = core.ErrorResult(fmt.Sprintf("The result of this call could not be made safe to show (%v), so it is withheld.", p))
		}
	}()
	return f(call, res)
}

// afterTool calls the AfterTool moment and catches a panic in it.
func afterTool(ctx context.Context, f func(context.Context, ToolDone) (ToolVerdict, error), info ToolDone) (v ToolVerdict, err error) {
	defer func() {
		if p := recover(); p != nil {
			err = fmt.Errorf("it crashed: %v", p)
		}
	}()
	return f(ctx, info)
}

// finish takes in what a tool's goroutine reported.
func (r *round) finish(d execDone) {
	a := r.a
	c := r.calls[d.idx]
	if c.done {
		return // It was given up on, and came back after all.
	}
	r.running--
	a.dropRunning(c.call.ID)
	c.res, c.dur, c.interrupted, c.cleaned = d.res, d.dur, d.interrupted, true
	if d.hookErr != nil {
		a.notice(LevelWarning, "after-tool", fmt.Sprintf("A check that runs after tool calls could not run on %s (%v). The model was told.", c.call.Name, d.hookErr))
		c.parts = append(c.parts, Part{Source: "after-tool", Text: a.mark(fmt.Sprintf(a.texts.HookFailed, d.hookErr))})
	}
	c.parts = append(c.parts, d.verdict.Context...)
	if d.verdict.Halt {
		r.haltNow(firstNonEmpty(d.verdict.Reason, "a hook ended the turn after "+c.call.Name), "after-tool")
	}
	r.record(c)
}

// dropRunning takes a call off the list of calls in flight.
func (a *Agent) dropRunning(id string) {
	a.mu.Lock()
	for i, rc := range a.running {
		if rc.CallID == id {
			a.running = append(a.running[:i:i], a.running[i+1:]...)
			break
		}
	}
	a.mu.Unlock()
}

// abandon goes on without the calls that have not stopped since the turn
// was stopped. Each gets a result that says so. A result that was already
// on its way is still taken.
func (r *round) abandon() {
	a := r.a
	r.gaveUp = true
	for _, c := range r.calls {
		if c.ran && !c.done {
			c.gone.giveUp()
		}
	}
	for {
		select {
		case d := <-r.done:
			r.finish(d)
			continue
		default:
		}
		break
	}
	for _, c := range r.calls {
		if !c.ran || c.done {
			continue
		}
		r.running--
		a.dropRunning(c.call.ID)
		c.dur = a.clock.Now().Sub(c.began)
		c.res = core.ErrorResult(fmt.Sprintf(a.texts.DidNotStop, span(a.grace)))
		c.interrupted = true
		a.notice(LevelWarning, "tools", fmt.Sprintf(
			"%s had not stopped %s after the turn was stopped. gnosh went on without it; it may still be running.", c.call.Name, span(a.grace)))
		r.record(c)
	}
}

// record marks a call as having its result, tells the consumer of events,
// and stores what can be stored in order.
func (r *round) record(c *callState) {
	a := r.a
	c.done = true
	if !c.cleaned {
		// What the loop itself answers a call with quotes the model and
		// the gate; it goes through the same filter as a tool's output.
		c.cleaned = true
		if f := a.opts.CleanResult; f != nil {
			c.res = cleanResult(f, core.ToolCall{ID: c.call.ID, Name: c.call.Name, Input: c.input}, c.res)
		}
		c.res.Content = sayable(c.res.Content)
	}
	if c.ran {
		if why, ok := c.res.Meta[MetaHalt].(string); ok && strings.TrimSpace(why) != "" {
			r.haltNow(why, c.call.Name)
		}
	}
	a.emit(&ToolEndEvent{
		CallID: c.call.ID, Name: c.call.Name, Input: c.input, Result: c.res, Duration: c.dur,
		Denied: c.denied, Unattended: c.unattended, Interrupted: c.interrupted, Ran: c.ran,
	})
	r.flush()
}

// flush stores the results of the calls that are done, as far as the order
// the model wrote them in allows.
func (r *round) flush() {
	a := r.a
	for r.err == nil && r.flushed < len(r.calls) && r.calls[r.flushed].done {
		c := r.calls[r.flushed]
		e := Entry{
			Kind: EntryResult, Turn: r.t.id, ToolUseID: c.call.ID, ToolName: c.call.Name,
			Result: c.res, Denied: c.denied, Interrupted: c.interrupted, Duration: c.dur,
		}
		if c.changed {
			e.Input = c.input
		}
		if err := a.appendLog(e); err != nil {
			r.err = err
			r.cancel()
			r.startGrace()
			return
		}
		if c.ran && c.res.Cwd != "" {
			a.mu.Lock()
			a.cwd = c.res.Cwd
			a.mu.Unlock()
		}
		r.t.open = dropCall(r.t.open, c.call.ID)
		r.flushed++
	}
}

// dropCall takes the call with that id off a list of tool calls.
func dropCall(open []core.Block, id string) []core.Block {
	for i, b := range open {
		if b.ID == id {
			return append(open[:i:i], open[i+1:]...)
		}
	}
	return open
}
