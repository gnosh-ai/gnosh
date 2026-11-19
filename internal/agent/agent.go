package agent

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"runtime/debug"
	"slices"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gnosh-ai/gnosh/internal/core"
)

// Errors the loop's own calls return.
var (
	// ErrClosed means the session has been closed.
	ErrClosed = errors.New("agent: the session is closed")
	// ErrYielded means a side request that gives way (SideRequest.Yield)
	// was stopped because the session's own next request wanted the model.
	ErrYielded = errors.New("agent: the side request gave way to the session's own")
	// ErrBlank means a prompt held neither text nor an attachment.
	ErrBlank = errors.New("agent: the prompt is empty, so nothing was sent")
	// ErrNobodyToAsk means a question was put in a session nobody attends.
	ErrNobodyToAsk = errors.New("agent: nobody is here to answer a question")
	// ErrNoQuestion means an answer named a question that is not waiting.
	ErrNoQuestion = errors.New("agent: no question with that id is waiting for an answer")
	// ErrCannotRun means a gate tried to ask a person about a call that
	// its tool has already found unable to run.
	ErrCannotRun = errors.New("agent: the call cannot run as asked, so nobody is asked about it")
)

// Options is everything a session is made of. Model, Log and Gate are
// required; the rest has a default or may be left out.
type Options struct {
	// SessionID names the session. Tools, the gate and events are given
	// it.
	SessionID string
	// Cwd is the session's working directory, an absolute path, and Roots
	// are the directories it works in.
	Cwd   string
	Roots []string
	// Tree is the git work tree the session works in apart from the
	// person's checkout (--worktree), or nil: its tools are given it
	// (core.ToolEnv.Tree), and so are its sub-agents'.
	Tree *core.WorkTree
	// Interactive says a person is there to answer questions. Without it
	// every question is answered with ErrNobodyToAsk at once.
	Interactive bool
	// Relay says permission questions go to the front end even so: a run
	// in the background, whose questions a person answers from the agent
	// view (app's runasks.go). Other questions still have nobody to ask.
	Relay bool
	// StartSource is how the session came to be, for the SessionStart
	// moment: "startup", "resume", "clear", "fork".
	StartSource string

	// Model is the model main requests go to. Small is the model for side
	// requests; nil means Model does those too, so that a session never
	// names a model the person did not choose.
	Model Model
	Small *Model
	// Fallbacks are the models a turn goes on with, in this order, when
	// Model cannot answer (Claude Code's fallbackModel): its server cannot
	// be reached or is not ready, says it is too busy, does not have the
	// model, or keeps failing until the wait for it runs out. The switch
	// holds for the rest of the turn; the next turn asks Model first
	// again. One that cannot hold the conversation as it stands, or is on
	// a model server of one's own that failed in the turn, is passed over;
	// with none left the turn waits for Model as without them (fallback.go).
	// Sub-agents are given the same list.
	Fallbacks []Fallback
	// System is the system prompt, in parts ordered from the part that
	// changes least to the part that changes most.
	System []string
	// Tools are what the model may call, in the order the model is told of
	// them. The order is kept, so that the fixed part of every request is
	// the same bytes.
	Tools []core.Tool

	// Log is the session's transcript.
	Log Log
	// Gate decides whether a tool call may run. AllowAll lets everything
	// run.
	Gate Gate
	// Moments are the points at which other code is consulted or told.
	Moments Moments
	// Drivers are asked, in this order, whether a turn may end.
	Drivers []Driver

	// OnEvent, when not nil, is given every event, in order, on one
	// goroutine of the session's. It may take its time: events wait for
	// it, and the loop does not. When it is nil, Events returns a channel
	// instead.
	OnEvent func(Event)
	// EventBacklog is how many bytes of live output may wait for a slow
	// consumer before the oldest pieces are thrown away and a LostEvent
	// says so. Zero means 32 MB.
	EventBacklog int

	// Clock is the time. Nil means the machine's clock.
	Clock Clock
	// Estimate guesses how many tokens a text is. Nil means a third of its
	// bytes, which errs on the side of too many.
	Estimate func(string) int
	// EstimateMessage, when not nil, guesses how many tokens one message
	// of the conversation is. Nil means the loop adds Estimate up over
	// the message's blocks. Either way the guesses are tied to the
	// provider's own counts as soon as it reports one.
	EstimateMessage func(core.Message) int
	// NewID makes the ids of turns, prompts, entries and questions. Nil
	// means random ones.
	NewID func() string

	// Retry says how long the loop waits on a provider that fails.
	Retry RetryPolicy
	// Repeat tunes the guard that tells a model it is repeating itself.
	Repeat RepeatPolicy

	// CompactAt, when above zero, is where room is made, in tokens. It can
	// only make that earlier than the default, never later.
	CompactAt int
	// NoAutoCompact turns off making room by itself: no slide and no
	// summary. A request that cannot be sent then ends the turn.
	NoAutoCompact bool

	// MaxParallel is how many tool calls that only look may run at once.
	// Zero means five.
	MaxParallel int
	// StopGrace is how long, after the person stops a turn, the loop waits
	// for a tool, a hook or a model request that has not stopped before it
	// goes on without it. It is real time, whatever Clock says. Zero means
	// ten seconds.
	StopGrace time.Duration

	// LoopEvery is how many /loop turns come between check-ins (zero means
	// 25), and LoopMax, when above zero, is how many turns a /loop runs.
	LoopEvery int
	LoopMax   int

	// Texts are the loop's own words to the model, and Mark wraps them the
	// way gnosh marks its own words on the user's side of a conversation.
	// Nil Mark puts them between <gnosh> and </gnosh>.
	Texts Texts
	Mark  func(string) string

	// CleanInput, when not nil, is given every tool call as the model
	// wrote it, and returns the arguments the call goes on with: the
	// gate, the hooks and the tool all see what it returns, never what
	// the model wrote. It is where what was made harmless in text the
	// model read is turned back before a tool acts on it. It must return
	// one JSON object.
	CleanInput func(call core.ToolCall) json.RawMessage
	// CleanResult, when not nil, is given every tool result before it is
	// stored for the model or shown: what a tool returned, and what the
	// loop itself answers a call with. It is where output is capped and
	// made harmless. It is never given gnosh's own lines, which are not
	// results. The loop itself never reads a tool's output as
	// instructions.
	CleanResult func(call core.ToolCall, res core.ToolResult) core.ToolResult
	// Outgoing, when not nil, is given every request just before it is
	// sent, main, summary and side alike, with the name of the provider it
	// goes to. It may change the request, which is the loop's own copy.
	Outgoing func(provider string, req *core.Request)

	// OnPanic, when not nil, is told when a tool, a port or the loop
	// itself panicked, with the stack. The loop carries on either way.
	OnPanic func(what string, v any, stack []byte)

	// Store, when not nil, is the Store in which the session's tools keep
	// their own state (core.ToolEnv.Store). Nil makes a new one. The
	// wiring gives its own when code other than a tool call has to reach
	// that state, such as what a session has read. A sub-agent always
	// gets a Store of its own.
	Store *sync.Map

	// CallID, when not nil, makes the id every tool call goes by, in place
	// of the id the provider gave it. The wiring gives one when the ids
	// must be unique beyond this session: a model server may number the
	// calls of every reply from zero, and what one session calls "call_0"
	// another does too. It must return an id it has not returned before,
	// of letters, digits, "_" and "-", at most 40 characters long: that is
	// what every provider takes back. Nil keeps the provider's own id
	// wherever this session has not seen it before.
	CallID func() string
}

// Agent is one session: a conversation with a model that is kept going by
// one goroutine of its own. Every method may be called from any goroutine,
// and none waits for a turn except Run, Wait and Close.
type Agent struct {
	opts      Options
	clock     Clock
	texts     Texts
	mark      func(string) string
	est       func(string) int
	newID     func() string
	grace     time.Duration
	retry     RetryPolicy
	parallel  int
	loopEvery int

	// A sub-agent has a parent and no sink of its own.
	parent     *Agent
	sink       *sink
	agentID    string
	agentType  string
	parentCall string
	session    string
	depth      int

	root   context.Context // cancelled when the session is closed
	cancel context.CancelFunc
	bg     context.Context // root's values without its end: for writes that settle a stopped turn
	done   chan struct{}   // closed when the session's goroutine has returned
	ready  chan struct{}   // closed when the log has been looked at for the first time
	wake   chan struct{}   // holds a token when the session's goroutine has something to look at

	modelLock  chan struct{} // holds a token while a request to a model is in flight
	imagesTold sync.Map      // the models the person was told cannot take images
	yieldMu    sync.Mutex
	yielders   map[*context.CancelCauseFunc]struct{} // side requests that give way to the session's own
	logMu      sync.Mutex                            // serialises calls to the Log; let go with unlockLog
	lateMu     sync.Mutex                            // guards late
	late       []Entry                               // notices raised while the Log was busy, stored when it is free
	store      *sync.Map                             // the tools' own state for the session
	intakeWG   sync.WaitGroup
	closeOnce  sync.Once
	turnNow    atomic.Value // the running turn's id, a string
	live       atomic.Value // the model that last sent something, as modelKey names it

	mu        sync.Mutex
	closing   bool
	busy      bool
	idle      chan struct{} // closed when the session next goes idle
	intake    []*submission // sent, not yet admitted
	intaking  bool
	admitting *submission
	admitStop context.CancelFunc
	// endStop cancels the moments that run after a turn ended (TurnFailed,
	// TurnEnded) while they run: Esc and End stop them.
	endStop    context.CancelFunc
	queue      []*submission // admitted, waiting for their turn
	notes      []Note        // told to the model with the next request
	queueShown bool          // the consumer was last told of a queue that was not empty
	commands   []*command
	cur        *operation
	startRan   bool
	startAdd   []Part
	startHalt  string // the session's start said stop (Halt): every prompt is refused with it
	sent       bool   // a prompt has gone out in this session, now or before
	model      Model
	small      *Model
	fell       *Model          // the fallback the turn goes on with, or nil (fallback.go)
	fellName   string          // its name in Options.Fallbacks
	fellOut    map[string]bool // the models and fallbacks that failed in this turn
	tools      []core.Tool
	index      map[string]core.Tool
	specs      []core.ToolSpec
	system     []string
	compactAt  int
	noAuto     bool
	cwd        string
	roots      []string
	tree       *core.WorkTree // its work tree (Options.Tree, SubOptions.Tree), or its parent's
	loop       loopState
	questions  map[string]chan Answer
	// asking is what each open question waits for, in words, and
	// beforeAsk the state before the first of them was put: with two open
	// (parallel sub-agents), the first answer put the phase back while the
	// second still waited (the agent loop review of 2026-11-19).
	asking    map[string]string
	beforeAsk State
	state     State
	running   []RunningCall
	totals    Totals
	cachePrev cachePrint // the last main request, for Totals.Cache
	context   ContextStatus

	// The rest belongs to the session's goroutine alone.
	settled      bool
	opened       bool            // the log has been settled once: what follows is this process's own
	calls        map[string]bool // the ids of the tool calls the session has seen
	anchor       anchor
	calib        float64
	learned      int // a window learned the hard way, and the model it was learned for
	learnedFor   string
	held         int // the longest request the provider has taken, by its own count, and for which model
	heldFor      string
	warnedWindow string
	summariesOff bool
	// answeredBy are the models that have answered in this session, so
	// that one that answered and is restarting is waited for as such,
	// also after a turn went on with a fallback; rsFor is the model rs is
	// about.
	answeredBy map[string]bool
	rsFor      string
	rs         retryState
}

// operation is what the session's goroutine is doing that a stop can
// reach: a turn, or a summary the person asked for.
type operation struct {
	cancel context.CancelFunc
	turn   *turn
}

// submission is a prompt on its way through the session.
type submission struct {
	id        string
	prompt    Prompt
	added     []Part // what its PromptSubmit moment added
	persisted bool   // an EntryQueued for it is in the log
	taken     bool   // taken back or dropped while it was being admitted
	checkin   bool   // a /loop check-in
	note      bool   // a note that was to start a turn: stopping keeps it as a note
	once      sync.Once
	result    chan TurnResult
}

// own reports whether gnosh queued the prompt for itself: one stopping a
// turn drops (dropOwnLocked).
func (s *submission) own() bool { return s.prompt.Own || s.note }

// resolve says how the prompt ended. Only the first call counts.
func (s *submission) resolve(r TurnResult) {
	s.once.Do(func() { s.result <- r })
}

// command is something the session does between turns that is not a turn.
type command struct {
	compact      bool
	instructions string
}

type ctxKey struct{}

// FromContext returns the session a tool call, a gate check or a moment is
// running in, or nil. A tool that starts a sub-agent, asks the person or
// makes a side request gets its session this way.
func FromContext(ctx context.Context) *Agent {
	a, _ := ctx.Value(ctxKey{}).(*Agent)
	return a
}

// New makes a session and starts its goroutine. Nothing is sent to a model
// until a prompt is submitted.
func New(opts Options) (*Agent, error) {
	return newAgent(opts, nil, SubOptions{})
}

func newAgent(opts Options, parent *Agent, sub SubOptions) (*Agent, error) {
	if opts.Model.Provider == nil {
		return nil, errors.New("agent: Options.Model has no Provider")
	}
	if opts.Log == nil {
		return nil, errors.New("agent: Options.Log is nil")
	}
	if opts.Gate == nil {
		return nil, errors.New("agent: Options.Gate is nil; a session that may act without asking says so with AllowAll")
	}
	if err := checkLevel(opts.Model); err != nil {
		return nil, err
	}
	for i, d := range opts.Drivers {
		if d.Name == "" || d.Decide == nil {
			return nil, fmt.Errorf("agent: Options.Drivers[%d] needs a Name and a Decide function", i)
		}
	}

	a := &Agent{
		opts:      opts,
		clock:     opts.Clock,
		texts:     opts.Texts.fill(),
		mark:      opts.Mark,
		est:       opts.Estimate,
		newID:     opts.NewID,
		grace:     opts.StopGrace,
		retry:     opts.Retry.fill(),
		parallel:  opts.MaxParallel,
		loopEvery: opts.LoopEvery,
		parent:    parent,
		session:   opts.SessionID,
		done:      make(chan struct{}),
		ready:     make(chan struct{}),
		wake:      make(chan struct{}, 1),
		modelLock: make(chan struct{}, 1),
		store:     opts.Store,
		model:     opts.Model,
		small:     opts.Small,
		system:    slices.Clone(opts.System),
		compactAt: opts.CompactAt,
		noAuto:    opts.NoAutoCompact,
		cwd:       opts.Cwd,
		roots:     slices.Clone(opts.Roots),
		tree:      opts.Tree,
		questions: make(map[string]chan Answer),
		asking:    make(map[string]string),
		calib:     1,
	}
	if a.store == nil {
		a.store = new(sync.Map)
	}
	if a.clock == nil {
		a.clock = systemClock{}
	}
	if a.mark == nil {
		a.mark = defaultMark
	}
	if a.est == nil {
		a.est = defaultEstimate
	}
	if a.newID == nil {
		a.newID = randomID
	}
	if a.grace <= 0 {
		a.grace = 10 * time.Second
	}
	if a.parallel <= 0 {
		a.parallel = 5
	}
	if a.loopEvery <= 0 {
		a.loopEvery = 25
	}
	if err := a.setTools(opts.Tools); err != nil {
		return nil, err
	}
	a.state.Phase = PhaseIdle
	a.state.Since = a.clock.Now()

	base := context.Background()
	if parent != nil {
		a.agentID = sub.ID
		a.agentType = sub.Type
		a.parentCall = sub.CallID
		// A sub-agent of a sub-agent with a work tree of its own works in
		// that work tree too, unless it has one of its own.
		a.tree = sub.Tree
		if a.tree == nil {
			a.tree = parent.tree
		}
		a.session = parent.session
		a.depth = parent.depth + 1
		if a.agentID == "" {
			a.agentID = a.newID()
		}
		// A provider that has answered the parent is known to be there, so
		// a restart of it is ridden out for the sub-agent as for the
		// parent.
		if k, _ := parent.live.Load().(string); k != "" && k == modelKey(opts.Model) {
			a.answeredBy = map[string]bool{k: true}
		}
	} else {
		a.sink = newSink(opts.SessionID, a.clock, opts.OnEvent, opts.EventBacklog)
		a.sink.start()
	}
	a.root, a.cancel = context.WithCancel(context.WithValue(base, ctxKey{}, a))
	a.bg = context.WithoutCancel(a.root)

	go a.run()
	return a, nil
}

// checkLevel reports whether m's thinking level is one of its levels.
func checkLevel(m Model) error {
	if m.Level == "" || slices.Contains(m.Levels, m.Level) {
		return nil
	}
	return fmt.Errorf("agent: thinking level %q is not one of the model's levels %v", m.Level, m.Levels)
}

// setTools replaces the session's tools. The caller holds no lock.
func (a *Agent) setTools(tools []core.Tool) error {
	index := make(map[string]core.Tool, len(tools))
	specs := make([]core.ToolSpec, 0, len(tools))
	for i, t := range tools {
		if t == nil {
			return fmt.Errorf("agent: tool %d is nil", i)
		}
		spec := t.Spec()
		if spec.Name == "" {
			return fmt.Errorf("agent: tool %d has no name", i)
		}
		if _, dup := index[spec.Name]; dup {
			return fmt.Errorf("agent: two tools are named %q", spec.Name)
		}
		index[spec.Name] = t
		specs = append(specs, spec)
	}
	a.mu.Lock()
	a.tools = slices.Clone(tools)
	a.index = index
	a.specs = specs
	a.mu.Unlock()
	return nil
}

// randomID makes an id nobody else has.
func randomID() string {
	var b [12]byte
	if _, err := rand.Read(b[:]); err != nil {
		// The system's random source does not fail on the machines gnosh
		// runs on; if it ever does, say so rather than hand out ids that
		// repeat.
		panic("agent: no random bytes for an id: " + err.Error())
	}
	return hex.EncodeToString(b[:])
}

// defaultEstimate guesses a text's tokens from its bytes.
func defaultEstimate(s string) int {
	return (len(s) + 2) / 3
}

// Events returns the channel on which every event of the session arrives,
// in order. It is closed once the session is closed and its last event has
// been read. Events is nil when Options.OnEvent was given, and for a
// sub-agent, whose events go to its parent's consumer.
//
// The loop never waits for the consumer. Events that are not read wait in
// memory; see LostEvent for what happens when too many do.
func (a *Agent) Events() <-chan Event {
	if a.sink == nil {
		return nil
	}
	return a.sink.ch
}

// emit sends ev to the session's consumer. It never blocks.
func (a *Agent) emit(ev Event) {
	m := ev.EventMeta()
	if m.Time.IsZero() {
		m.Time = a.clock.Now()
	}
	if m.Turn == "" {
		if id, _ := a.turnNow.Load().(string); id != "" {
			m.Turn = id
		}
	}
	a.forward(ev)
}

// forward hands ev up to the session that owns the consumer, marking it on
// the way as a sub-agent's when it is one.
func (a *Agent) forward(ev Event) {
	if a.parent == nil {
		a.sink.emit(ev)
		return
	}
	m := ev.EventMeta()
	if m.Agent == "" {
		m.Agent = a.agentID
		m.Parent = a.parentCall
	}
	a.parent.forward(ev)
}

// Notice puts a line in front of the person and keeps it in the log. Other
// code uses it for what the person must not miss: "popped x: about 44K
// tokens out of the model's context", a goal's verdict.
func (a *Agent) Notice(level Level, source, text string) {
	a.notice(level, source, text)
}

func (a *Agent) notice(level Level, source, text string) {
	a.noticeAs(&NoticeEvent{Level: level, Source: source, Text: text})
}

// noticeAs is notice, for the event as it is given (a turn's end marks
// its own: NoticeEvent.TurnEnd).
func (a *Agent) noticeAs(ev *NoticeEvent) {
	level, source, text := ev.Level, ev.Source, ev.Text
	if text == "" {
		return
	}
	a.emit(ev)
	turn, _ := a.turnNow.Load().(string)
	// The person has been shown the line; what is left is to keep it in
	// the log. A notice may be raised by code that the log itself is
	// calling (the log's owner telling a feature of an entry it has just
	// stored, or of a slide), and the log is then busy on this very
	// goroutine: waiting for it would wait for ever. So the entry is put
	// on a list, and stored at once if the log is free, or by whoever
	// holds the log when they let go of it. The list keeps the order the
	// notices were raised in.
	a.lateMu.Lock()
	a.late = append(a.late, Entry{
		Kind: EntryNotice, ID: a.newID(), Time: a.clock.Now(), Turn: turn,
		Level: level, Source: source, Text: text,
	})
	a.lateMu.Unlock()
	if a.logMu.TryLock() {
		a.unlockLog()
	}
}

// unlockLog lets go of the Log, and first stores the notices that were
// raised while it was held. Everything that takes logMu lets go with it.
func (a *Agent) unlockLog() {
	for {
		a.storeLate()
		a.logMu.Unlock()
		// One that was raised between the last look and the unlock is
		// stored now, unless somebody else has taken the Log: they store
		// it when they let go.
		a.lateMu.Lock()
		waiting := len(a.late) > 0
		a.lateMu.Unlock()
		if !waiting || !a.logMu.TryLock() {
			return
		}
	}
}

// panicked reports a panic that the loop caught.
func (a *Agent) panicked(what string, v any, stack []byte) {
	if f := a.opts.OnPanic; f != nil {
		func() {
			defer func() { _ = recover() }()
			f(what, v, stack)
		}()
	}
}

// storeLate stores the notices that wait. The caller holds logMu. A notice
// that cannot be stored has still been shown, so neither an error nor a
// crash of the log over one is passed on: whatever happens here, the
// caller goes on to let go of the log.
func (a *Agent) storeLate() {
	defer func() { _ = recover() }()
	for {
		a.lateMu.Lock()
		late := a.late
		a.late = nil
		a.lateMu.Unlock()
		if len(late) == 0 {
			return
		}
		_ = a.opts.Log.Append(a.bg, late...)
	}
}

// appendLog stores entries, giving each an id and a time if it has none.
func (a *Agent) appendLog(entries ...Entry) error {
	if len(entries) == 0 {
		return nil
	}
	now := a.clock.Now()
	for i := range entries {
		if entries[i].Time.IsZero() {
			entries[i].Time = now
		}
		if entries[i].ID == "" {
			entries[i].ID = a.newID()
		}
	}
	a.logMu.Lock()
	defer a.unlockLog()
	if err := a.opts.Log.Append(a.bg, entries...); err != nil {
		return fmt.Errorf("the session's log could not be written: %w", err)
	}
	return nil
}

// view asks the log for what the model is to read.
func (a *Agent) view() (View, error) {
	a.logMu.Lock()
	defer a.unlockLog()
	v, err := a.opts.Log.View(a.bg)
	if err != nil {
		return View{}, fmt.Errorf("the session's log could not be read: %w", err)
	}
	return v, nil
}

// await runs f on a goroutine of its own and waits for it. When ctx is
// cancelled and f has still not returned once the grace is over, await
// goes on without it and reports false; whatever f returns later is thrown
// away. A panic in f comes back as an error.
func await[T any](ctx context.Context, a *Agent, what string, f func(context.Context) (T, error)) (T, error, bool) {
	type result struct {
		v   T
		err error
	}
	ch := make(chan result, 1)
	go func() {
		defer func() {
			if p := recover(); p != nil {
				a.panicked(what, p, debug.Stack())
				var zero T
				ch <- result{zero, fmt.Errorf("%s crashed: %v", what, p)}
			}
		}()
		v, err := f(ctx)
		ch <- result{v, err}
	}()
	select {
	case r := <-ch:
		return r.v, r.err, true
	case <-ctx.Done():
	}
	t := time.NewTimer(a.grace)
	defer t.Stop()
	select {
	case r := <-ch:
		return r.v, r.err, true
	case <-t.C:
	}
	// The person is told, but nothing is written: what would not stop may
	// be the log itself.
	a.emit(&NoticeEvent{Level: LevelWarning, Source: "loop", Text: fmt.Sprintf(
		"%s had not stopped %s after the turn was stopped. gnosh went on without it.", what, span(a.grace))})
	var zero T
	return zero, ctx.Err(), false
}

// kick tells the session's goroutine to look at its state again.
func (a *Agent) kick() {
	select {
	case a.wake <- struct{}{}:
	default:
	}
}

// setPhase records what the session is doing, for State.
func (a *Agent) setPhase(p Phase, waiting string) {
	a.mu.Lock()
	a.state.Phase = p
	a.state.Waiting = waiting
	a.state.Since = a.clock.Now()
	a.mu.Unlock()
}

// State says what the session is doing right now, and what it is waiting
// for. It never blocks on the loop, so a session that seems stuck can
// always be asked.
func (a *Agent) State() State {
	a.mu.Lock()
	defer a.mu.Unlock()
	s := a.state
	s.Busy = a.busy
	s.Running = slices.Clone(a.running)
	s.Queued = len(a.queue) + len(a.intake) - takenLocked(a.intake)
	s.Notes = len(a.notes)
	s.Loop = a.loop.on
	s.Model = a.model.Info.ID
	s.Level = a.model.Level
	return s
}

// Context gives the size of the conversation against the model's window,
// as of the last count.
func (a *Agent) Context() ContextStatus {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.context
}

// SetCwd moves the session's working directory, as a command's cd does in
// the model's own calls: for a command the person ran themselves. dir is
// where the command ended, which the tool that ran it has checked against
// the session's folders.
func (a *Agent) SetCwd(dir string) {
	if dir == "" {
		return
	}
	a.mu.Lock()
	a.cwd = dir
	a.mu.Unlock()
}

// Cwd returns the session's working directory.
func (a *Agent) Cwd() string {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.cwd
}

// SessionID returns the id of the session the person is in. For a
// sub-agent that is its parent's.
func (a *Agent) SessionID() string { return a.session }

// AgentID returns the sub-agent's id, or the empty string for a main
// agent.
func (a *Agent) AgentID() string { return a.agentID }

// Depth returns how many sub-agents deep this session is: zero for a main
// agent.
func (a *Agent) Depth() int { return a.depth }

// Model returns the model main requests go to.
func (a *Agent) Model() Model {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.model
}

// SetModel changes the model main requests go to, from the next request
// on. If the new model's window is smaller, the conversation is brought
// under it before that request goes out.
func (a *Agent) SetModel(m Model) error {
	if m.Provider == nil {
		return errors.New("agent: the model has no Provider")
	}
	if err := checkLevel(m); err != nil {
		return err
	}
	a.mu.Lock()
	// A model chosen by hand outranks a fallback the turn went on with.
	a.model, a.fell = m, nil
	a.mu.Unlock()
	return nil
}

// SetSmallModel changes the model side requests go to first. Nil makes
// them go to the main model.
func (a *Agent) SetSmallModel(m *Model) {
	a.mu.Lock()
	a.small = m
	a.mu.Unlock()
}

// SetThinking sets the thinking level of every later main request to one
// of the model's levels, or to the model's own default with "". It returns
// the level now in force. Keeping the choice for future sessions is the
// caller's business.
func (a *Agent) SetThinking(level string) (string, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	m := a.model
	m.Level = level
	if err := checkLevel(m); err != nil {
		return a.model.Level, err
	}
	a.model.Level = level
	return level, nil
}

// Think moves the model to the most thinking it has, and NoThink to the
// least. Each returns the level that is, or false when the loop knows of
// no thinking switch for the model.
func (a *Agent) Think() (string, bool) { return a.thinkEnd(true) }

// NoThink moves the model to the least thinking it has. See Think.
func (a *Agent) NoThink() (string, bool) { return a.thinkEnd(false) }

func (a *Agent) thinkEnd(most bool) (string, bool) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if len(a.model.Levels) == 0 {
		return "", false
	}
	level := a.model.Levels[0]
	if most {
		level = a.model.Levels[len(a.model.Levels)-1]
	}
	a.model.Level = level
	return level, true
}

// SetTools replaces the tools the model may call, from the next request
// on.
func (a *Agent) SetTools(tools []core.Tool) error { return a.setTools(tools) }

// SetSystem replaces the system prompt, from the next request on.
func (a *Agent) SetSystem(parts []string) {
	a.mu.Lock()
	a.system = slices.Clone(parts)
	a.mu.Unlock()
}

// Tree is the git work tree the session works in apart from the person's
// checkout, its own or its parent's, or nil. It is set when the agent is
// made and never changes.
func (a *Agent) Tree() *core.WorkTree { return a.tree }

// SetRoots replaces the directories the session works in.
func (a *Agent) SetRoots(roots []string) {
	a.mu.Lock()
	a.roots = slices.Clone(roots)
	a.mu.Unlock()
}

// SetCompactAt moves the point at which room is made to tokens, or back to
// the default with zero. It can only make that earlier than the default.
func (a *Agent) SetCompactAt(tokens int) {
	a.mu.Lock()
	a.compactAt = tokens
	a.mu.Unlock()
}

// SetAutoCompact turns making room by itself on or off.
func (a *Agent) SetAutoCompact(on bool) {
	a.mu.Lock()
	a.noAuto = !on
	a.mu.Unlock()
}

// setup is what a request is built from, as it stands at one moment.
type setup struct {
	model     Model
	small     *Model
	tools     []core.Tool
	index     map[string]core.Tool
	specs     []core.ToolSpec
	system    []string
	compactAt int
	noAuto    bool
}

func (a *Agent) setup() setup {
	a.mu.Lock()
	defer a.mu.Unlock()
	model := a.model
	if a.fell != nil {
		model = *a.fell
	}
	return setup{
		model:     model,
		small:     a.small,
		tools:     a.tools,
		index:     a.index,
		specs:     a.specs,
		system:    a.system,
		compactAt: a.compactAt,
		noAuto:    a.noAuto,
	}
}

// toolEnv makes what one tool call is given to work with. A sub-agent's
// calls are given the sub-agent's own id as their session, and the id of
// the session that started it beside it.
func (a *Agent) toolEnv(callID string) *core.ToolEnv {
	a.mu.Lock()
	cwd, roots := a.cwd, slices.Clone(a.roots)
	a.mu.Unlock()
	session, parent := a.session, ""
	if a.parent != nil {
		session, parent = a.agentID, a.parent.agentID
		if parent == "" {
			parent = a.parent.session
		}
	}
	return &core.ToolEnv{
		Cwd:             cwd,
		Roots:           roots,
		SessionID:       session,
		ParentSessionID: parent,
		Store:           a.store,
		Tree:            a.tree,
		Progress: func(p core.Progress) {
			if p.CallID == "" {
				p.CallID = callID
			}
			a.emit(&ToolProgressEvent{CallID: p.CallID, Stream: p.Stream, Text: p.Text})
		},
	}
}

// Ask puts a question to the person and waits for the answer, which the
// front end gives with Answer. It returns ErrNobodyToAsk at once in a
// session nobody attends, and ctx's error when ctx ends first. A
// sub-agent's question is put to the person through its parent.
func (a *Agent) Ask(ctx context.Context, q Question) (Answer, error) {
	turn, _ := a.turnNow.Load().(string)
	return a.ask(ctx, q, Meta{Agent: a.agentID, Parent: a.parentCall, Turn: turn})
}

// ask puts q to the person. who says which session asks, so that the
// events of a sub-agent's question are marked as that sub-agent's.
func (a *Agent) ask(ctx context.Context, q Question, who Meta) (Answer, error) {
	if a.parent != nil {
		return a.parent.ask(ctx, q, who)
	}
	if !a.opts.Interactive && !(a.opts.Relay && q.Kind == "permission") {
		return Answer{}, ErrNobodyToAsk
	}
	q.ID = a.newID()
	ch := make(chan Answer, 1)
	a.mu.Lock()
	if a.closing {
		a.mu.Unlock()
		return Answer{}, ErrClosed
	}
	if len(a.questions) == 0 {
		a.beforeAsk = a.state
	}
	a.questions[q.ID] = ch
	waiting := "waiting for the person to answer: " + oneLine(firstNonEmpty(q.Title, q.Text, q.Kind))
	a.asking[q.ID] = waiting
	a.state.Phase = PhaseQuestion
	a.state.Waiting = waiting
	a.state.Since = a.clock.Now()
	a.mu.Unlock()

	a.emit(&QuestionEvent{Meta: who, Question: q})

	var ans Answer
	var err error
	select {
	case ans = <-ch:
	case <-ctx.Done():
		err = ctx.Err()
	case <-a.root.Done():
		err = ErrClosed
	}

	a.mu.Lock()
	delete(a.questions, q.ID)
	delete(a.asking, q.ID)
	if a.state.Phase == PhaseQuestion {
		if len(a.questions) == 0 {
			a.state.Phase, a.state.Waiting, a.state.Since = a.beforeAsk.Phase, a.beforeAsk.Waiting, a.clock.Now()
		} else {
			for _, w := range a.asking {
				a.state.Waiting = w // one that still waits
				break
			}
		}
	}
	a.mu.Unlock()
	a.emit(&AnswerEvent{Meta: who, ID: q.ID, Answer: ans, Withdrawn: err != nil})
	return ans, err
}

func firstNonEmpty(ss ...string) string {
	for _, s := range ss {
		if s != "" {
			return s
		}
	}
	return ""
}

// Answer gives the person's answer to the question with that id.
func (a *Agent) Answer(id string, ans Answer) error {
	if a.parent != nil {
		return a.parent.Answer(id, ans)
	}
	a.mu.Lock()
	ch, ok := a.questions[id]
	if ok {
		delete(a.questions, id)
	}
	a.mu.Unlock()
	if !ok {
		return ErrNoQuestion
	}
	ch <- ans
	return nil
}

// Tell hands the model a note: something the harness has to say that is
// not a prompt, such as the end of a background command. It is the one way
// in for such notices, and may be called at any time, from any goroutine.
//
// While a turn runs, the note is put in front of the model with the next
// request. In an idle session it starts nothing: it waits, and goes out
// with the next request there is. With Wake set it starts a turn of its
// own in an idle session instead. Stopping a turn never loses a note: one
// that was to start a turn then waits like any other.
func (a *Agent) Tell(n Note) error {
	if (Prompt{Text: n.Text}).blank() {
		return ErrBlank
	}
	if n.Wake {
		_, err := a.submit(Prompt{Text: n.Text, Hidden: true, Source: n.Source, Notice: n.Notice}, func(s *submission) { s.note = true })
		return err
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.closing {
		return ErrClosed
	}
	a.notes = append(a.notes, n)
	return nil
}

// Record puts a note into the conversation without starting a turn. When
// no turn runs, it is written to the log at once, between turns: it stands
// before the prompt that comes next, and is kept if the session ends
// first. While a turn runs it waits for the turn's next request, as a
// note told with Tell does. Wake is not read.
func (a *Agent) Record(n Note) error {
	if (Prompt{Text: n.Text}).blank() {
		return ErrBlank
	}
	n.Wake = false
	a.mu.Lock()
	if a.closing {
		a.mu.Unlock()
		return ErrClosed
	}
	if a.busy {
		// The turn's next request takes it; one that makes none leaves it
		// to be written when the turn ends (leaveBusy), or the session.
		n.recorded = true
		a.notes = append(a.notes, n)
		a.mu.Unlock()
		return nil
	}
	a.mu.Unlock()
	// (A prompt sent in between may be written first; the note then
	// waits in the log for that turn's request, which reads it.)
	if err := a.writeNote(n); err != nil {
		return a.Tell(n)
	}
	return nil
}

// writeNote writes a note to the log as an entry of its own, between
// turns, and says so.
func (a *Agent) writeNote(n Note) error {
	entry := Entry{Kind: EntryNote, Source: firstNonEmpty(n.Source, "note"), Blocks: []core.Block{core.Text(n.Text)}}
	if err := a.appendLog(entry); err != nil {
		return err
	}
	a.emit(&NoteEvent{Note: n})
	if n.Notice != "" {
		a.notice(LevelInfo, firstNonEmpty(n.Source, "note"), n.Notice)
	}
	return nil
}

// takeRecordedLocked takes out of the notes that wait the ones Record put
// there while a turn ran. The caller holds a.mu.
func (a *Agent) takeRecordedLocked() []Note {
	var keep, took []Note
	for _, n := range a.notes {
		if n.recorded {
			took = append(took, n)
		} else {
			keep = append(keep, n)
		}
	}
	if len(took) > 0 {
		a.notes = keep
	}
	return took
}

// writeRecorded writes the notes Record left while a turn ran that no
// request took, now that the turn is over; one that cannot be written
// waits for the next request, as a told one does.
func (a *Agent) writeRecorded(notes []Note) {
	for _, n := range notes {
		if a.writeNote(n) != nil {
			n.recorded = false
			a.mu.Lock()
			a.notes = append(a.notes, n)
			a.mu.Unlock()
		}
	}
}

// Submit hands a prompt to the session and returns its id at once. The
// turn runs on the session's own goroutine. A prompt sent while a turn
// runs is queued: it is given to the model between two rounds of that
// turn, or starts the next turn if that one ends first. What becomes of
// the prompt arrives as events.
func (a *Agent) Submit(p Prompt) (string, error) {
	s, err := a.submit(p, nil)
	if err != nil {
		return "", err
	}
	return s.id, nil
}

// submit puts a prompt in line. mark, when not nil, sets what kind of
// prompt it is before anyone else can see it.
func (a *Agent) submit(p Prompt, mark func(*submission)) (*submission, error) {
	if p.blank() {
		return nil, ErrBlank
	}
	s := &submission{id: p.ID, prompt: p, result: make(chan TurnResult, 1)}
	if s.id == "" {
		s.id = a.newID()
		s.prompt.ID = s.id
	}
	if mark != nil {
		mark(s)
	}
	a.mu.Lock()
	if a.closing {
		a.mu.Unlock()
		return nil, ErrClosed
	}
	a.intake = append(a.intake, s)
	a.markBusyLocked()
	if !a.intaking {
		a.intaking = true
		a.intakeWG.Add(1)
		go a.admitLoop()
	}
	a.mu.Unlock()
	a.kick()
	return s, nil
}

// markBusyLocked notes that the session has work. The caller holds the
// lock.
func (a *Agent) markBusyLocked() {
	if !a.busy {
		a.busy = true
		a.idle = make(chan struct{})
	}
}

// Run submits a prompt and waits for the turn that answers it to end. It
// is the plain way to drive a run that nobody attends, and a sub-agent.
// When ctx ends first, the prompt is taken back if it has not gone out and
// the turn is stopped if it has, and Run still returns how it ended.
func (a *Agent) Run(ctx context.Context, p Prompt) (TurnResult, error) {
	s, err := a.submit(p, nil)
	if err != nil {
		return TurnResult{}, err
	}
	select {
	case r := <-s.result:
		return r, nil
	case <-ctx.Done():
	}
	a.withdraw(s)
	return <-s.result, nil
}

// withdraw takes s back if it has not gone out, and stops its turn if it
// has.
func (a *Agent) withdraw(s *submission) {
	a.mu.Lock()
	var unq bool
	switch {
	case a.admitting == s:
		s.taken = true
		if a.admitStop != nil {
			a.admitStop()
		}
	case removeSub(&a.intake, s):
		s.taken = true
	case removeSub(&a.queue, s):
		s.taken = true
		unq = s.persisted
	default:
		if a.cur != nil && a.cur.turn != nil && slices.Contains(a.cur.turn.subs, s) {
			a.cur.cancel()
		}
		a.mu.Unlock()
		return
	}
	a.mu.Unlock()
	if unq {
		_ = a.appendLog(Entry{Kind: EntryUnqueued, Prompt: s.id, Text: "withdrawn"})
	}
	a.emitQueue()
	s.resolve(TurnResult{Prompts: []string{s.id}, Reason: EndWithdrawn, Detail: "the prompt was taken back before it went out"})
	a.kick()
}

// removeSub takes s out of list and reports whether it was there.
func removeSub(list *[]*submission, s *submission) bool {
	i := slices.Index(*list, s)
	if i < 0 {
		return false
	}
	*list = slices.Delete(*list, i, i+1)
	return true
}

// Queued lists the prompts that wait for their turn, in the order they
// will go out.
func (a *Agent) Queued() []QueuedPrompt {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.queuedLocked()
}

func (a *Agent) queuedLocked() []QueuedPrompt {
	out := make([]QueuedPrompt, 0, len(a.queue)+len(a.intake))
	for _, s := range a.queue {
		out = append(out, QueuedPrompt{ID: s.id, Prompt: s.prompt})
	}
	for _, s := range a.intake {
		if s.taken {
			continue // taken back while its check runs: not in line
		}
		out = append(out, QueuedPrompt{ID: s.id, Prompt: s.prompt, Checking: true})
	}
	return out
}

// takenLocked counts the prompts in intake that were taken back while
// their check ran, which wait there only for the admitting goroutine to
// drop them. They were counted and listed as waiting, and a second take
// handed one back again (the agent loop review of 2026-11-19).
func takenLocked(intake []*submission) int {
	n := 0
	for _, s := range intake {
		if s.taken {
			n++
		}
	}
	return n
}

// emitQueue tells the consumer of events which prompts wait for their
// turn. A prompt that goes out at once never waits, so an empty list is
// only told when a list that was not empty was told before.
func (a *Agent) emitQueue() {
	a.mu.Lock()
	q := a.queuedLocked()
	if len(q) == 0 && !a.queueShown {
		a.mu.Unlock()
		return
	}
	a.queueShown = len(q) > 0
	a.mu.Unlock()
	a.emit(&QueueEvent{Queued: q})
}

// TakeQueued takes every prompt the person has waiting off the queue and
// returns them, in order, for editing: it is what Up does in an empty
// input box. Prompts gnosh queued for itself stay where they are.
func (a *Agent) TakeQueued() []Prompt { return a.takeQueued("") }

// Withdraw takes one prompt of the person's off the queue, the one with
// that id, and returns it. It reports false when no such prompt waits: it
// has gone out, or was never the person's.
func (a *Agent) Withdraw(id string) (Prompt, bool) {
	if id == "" {
		return Prompt{}, false
	}
	taken := a.takeQueued(id)
	if len(taken) == 0 {
		return Prompt{}, false
	}
	return taken[0], true
}

// takeQueued takes the person's prompts off the queue: all of them, or
// with only set, the one with that id.
func (a *Agent) takeQueued(only string) []Prompt {
	a.mu.Lock()
	var taken []*submission
	keep := a.queue[:0:0]
	for _, s := range a.queue {
		if s.prompt.Hidden || (only != "" && s.id != only) {
			keep = append(keep, s)
			continue
		}
		s.taken = true
		taken = append(taken, s)
	}
	a.queue = keep
	keepIn := a.intake[:0:0]
	for _, s := range a.intake {
		if s.taken || s.prompt.Hidden || (only != "" && s.id != only) {
			keepIn = append(keepIn, s)
			continue
		}
		s.taken = true
		taken = append(taken, s)
		if a.admitting == s {
			// Its check is still running: stop that, and leave it in line
			// for the admitting goroutine to drop.
			keepIn = append(keepIn, s)
			if a.admitStop != nil {
				a.admitStop()
			}
		}
	}
	a.intake = keepIn
	// Which of them are written down as queued is read under the lock,
	// like everything else about a prompt on its way.
	written := make([]bool, len(taken))
	for i, s := range taken {
		written[i] = s.persisted
	}
	a.mu.Unlock()

	if len(taken) == 0 {
		return nil
	}
	out := make([]Prompt, 0, len(taken))
	for i, s := range taken {
		if written[i] {
			_ = a.appendLog(Entry{Kind: EntryUnqueued, Prompt: s.id, Text: "taken back"})
		}
		out = append(out, s.prompt)
		s.resolve(TurnResult{Prompts: []string{s.id}, Reason: EndWithdrawn, Detail: "the prompt was taken back for editing"})
	}
	a.emitQueue()
	a.kick()
	return out
}

// Interrupt stops the turn that is running, all of it and at once: the
// request to the model, every tool with what it started, every hook, and
// a question that waits for the person. Prompts gnosh had queued for
// itself are dropped and /loop is switched off, or the turn would start
// itself again. Prompts the person queued stay, and go out next.
//
// Interrupt returns at once. The turn's end arrives as a TurnEndEvent
// whose reason is EndStopped.
func (a *Agent) Interrupt() {
	a.mu.Lock()
	dropped := a.dropOwnLocked(func(*submission) bool { return true })
	if a.admitStop != nil {
		// A prompt stopped in its hooks is a stopped prompt: it comes back
		// to the person.
		a.admitStop()
	}
	if a.cur != nil {
		a.cur.cancel()
	}
	if a.endStop != nil {
		a.endStop()
	}
	loopOn := a.loop.on
	turns := a.loop.turns
	a.loop = loopState{}
	a.mu.Unlock()

	a.returnDropped(dropped, "dropped when the turn was stopped")
	if loopOn {
		a.emit(&LoopEvent{On: false, Turns: turns, Why: "the turn was stopped"})
	}
	a.kick()
}

// Wait blocks until the session is idle: no turn running, nothing queued.
// It returns ctx's error when ctx ends first.
func (a *Agent) Wait(ctx context.Context) error {
	for {
		a.mu.Lock()
		if !a.busy {
			a.mu.Unlock()
			return nil
		}
		ch := a.idle
		a.mu.Unlock()
		select {
		case <-ch:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
}

// Drain waits until every event the session has made so far has been
// handed to its consumer, or until ctx ends. A front end calls it when it
// has to know that it has been told everything before it goes on, such as
// before it takes the next line of a script. It must not be called from
// inside the OnEvent function, which it would wait for.
func (a *Agent) Drain(ctx context.Context) error {
	if a.parent != nil {
		return a.parent.Drain(ctx)
	}
	return a.sink.drain(ctx)
}

// Close ends the session with the reason "other". See End.
func (a *Agent) Close() error { return a.End("other") }

// End closes the session. A running turn is stopped and recorded as
// stopped, so that the log is one a new process can pick up; prompts that
// had not gone out come back as PromptReturnedEvents; the SessionEnd
// moment is told reason, if the session ever had a message; and the stream
// of events ends. End waits for all of that except a consumer that reads
// Events from a channel; it must therefore not be called from inside the
// OnEvent function, which it would wait for. Calling it again does
// nothing.
func (a *Agent) End(reason string) error {
	a.closeOnce.Do(func() {
		a.mu.Lock()
		a.closing = true
		if a.cur != nil {
			a.cur.cancel()
		}
		if a.admitStop != nil {
			a.admitStop()
		}
		if a.endStop != nil {
			a.endStop()
		}
		a.mu.Unlock()
		a.kick()

		<-a.done
		a.intakeWG.Wait()

		// Nothing more will run: whoever waits for idle is let go.
		a.mu.Lock()
		a.busy = false
		if a.idle != nil {
			close(a.idle)
			a.idle = nil
		}
		a.mu.Unlock()

		// What never went out comes back.
		a.mu.Lock()
		left := append(slices.Clone(a.queue), a.intake...)
		a.queue, a.intake = nil, nil
		written := make([]bool, len(left))
		for i, s := range left {
			written[i] = s.persisted
		}
		had := a.sent
		recorded := a.takeRecordedLocked()
		a.mu.Unlock()
		// (What Record kept for a request that never came is in the log.)
		for _, n := range recorded {
			_ = a.writeNote(n)
		}
		for i, s := range left {
			if written[i] {
				// Written down as queued, it is written off too: a session
				// opened again (one /clear left) returned it a second time
				// (the agent loop review of 2026-11-19).
				_ = a.appendLog(Entry{Kind: EntryUnqueued, Prompt: s.id, Text: "the session was closed before it went out"})
			}
			a.emit(&PromptReturnedEvent{ID: s.id, Prompt: s.prompt, Why: ReturnClosed, Reason: "the session was closed before this prompt went out"})
			s.resolve(TurnResult{Prompts: []string{s.id}, Reason: EndWithdrawn, Detail: "the session was closed before the prompt went out"})
		}

		if f := a.opts.Moments.SessionEnd; f != nil && had {
			ctx, cancel := context.WithTimeout(a.bg, a.grace)
			_, _, _ = await(ctx, a, "the session-end moment", func(ctx context.Context) (struct{}, error) {
				f(ctx, EndInfo{Reason: reason})
				return struct{}{}, nil
			})
			cancel()
		}
		a.cancel()
		if a.parent != nil {
			a.parent.adopt(a)
		} else {
			a.sink.close()
		}
	})
	return nil
}
