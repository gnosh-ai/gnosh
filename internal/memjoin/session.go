package memjoin

import (
	"context"
	"sync"
	"time"

	"github.com/gnosh-ai/gnosh/internal/agent"
	"github.com/gnosh-ai/gnosh/internal/app"
	"github.com/gnosh-ai/gnosh/internal/core"
	"github.com/gnosh-ai/gnosh/internal/gnovm"
	gnovmtools "github.com/gnosh-ai/gnosh/internal/gnovm/tools"
	"github.com/gnosh-ai/gnosh/internal/hooks"
	"github.com/gnosh-ai/gnosh/internal/prompt"
	"github.com/gnosh-ai/gnosh/internal/transcript"
)

// stopGrace is how long a stopped turn waits for a gnovm_run call to come
// back. A call that mem kept before the stop reached it still hands back
// the frames it pushed and popped, and those must reach the session's log
// with the call's result: a push or a pop that is lost leaves the
// session's stack and mem's disagreeing.
const stopGrace = 30 * time.Second

// join is the join for one session: everything the harness does for mem
// and frames in that session hangs off it.
type join struct {
	p   *process
	s   *app.Session
	vms *gnovm.Lazy

	// dir is the gnovm data directory, project the session's project
	// directory (what its machines and its mem belong to), and id the
	// session's id: the id of its log, which is also what mem knows the
	// session by, so that the frames mem reports and the frame entries in
	// the log carry the same id.
	dir     string
	project string
	id      string
	ts      *transcript.Session
	texts   *prompt.Set

	// main says a person is in this session: it is not a sub-agent's.
	// run says gnovm_run is among its tools.
	main bool
	run  bool

	mu sync.Mutex
	// kept holds, by tool call id, what the frames of a kept mem call
	// did, from the moment mem hands them back until the call's result is
	// in the log. inflight names the gnovm_run calls the model has made
	// whose result is not in the log yet.
	kept     map[string]*keptCall
	inflight map[string]bool
	// agent is the session's agent once it exists.
	agent *agent.Agent
	// noticeDay is the day the person was last told what is due in mem.
	noticeDay string

	goal   goalState
	remind reminderState
	tasks  taskState
	names  nameCache
	review reviewState
	turn   turnState
}

// session is Feature.Session: it is called once for every session, when
// its facts are known and before its agent exists; and again for every
// sub-agent the session starts, with the sub-agent's own facts.
func (p *process) session(ctx context.Context, s *app.Session) (*app.Contribution, error) {
	dir := DataDir(s.Dirs.State)
	embeddings := ""
	if s.Config != nil {
		embeddings = s.Config.Options.MemEmbeddingsURL.V
	}
	j := &join{
		p:        p,
		s:        s,
		vms:      p.client(dir, embeddings),
		dir:      dir,
		project:  s.Dirs.ProjectRoot,
		id:       s.ID,
		ts:       s.Transcript,
		texts:    s.Texts,
		main:     !s.SubAgent,
		kept:     map[string]*keptCall{},
		inflight: map[string]bool{},
	}
	if j.project == "" {
		j.project = s.Dirs.Cwd
	}
	if j.ts != nil && j.ts.ID() != "" {
		j.id = j.ts.ID()
	}
	if j.texts == nil {
		j.texts = prompt.Builtin()
	}
	j.load()

	tools := j.tools()
	if !j.main && len(tools) == 0 {
		// A sub-agent that is given no gnovm tool has nothing of the join:
		// no machine, no mem, none of mem's context, and no turn lock,
		// since it can hold no todo.
		return &app.Contribution{}, nil
	}
	c := &app.Contribution{
		Tools:       tools,
		Protected:   []string{dir},
		Hooks:       func(o *hooks.Options) { o.MemStatus = j.vms.MemStatusPath(j.project) },
		Stored:      j.onStored,
		AfterStored: j.afterStored,
		Slid:        j.slid,
		Busy:        j.busy,
		StopGrace:   stopGrace,
		Ready:       j.ready,
		Close:       j.close,
	}
	if j.run {
		// Whoever has gnovm_run can close a todo, and a turn that would
		// end on a refused close is sent back to it, once: a sub-agent's
		// too, whose report would otherwise claim what mem refused.
		c.Drivers = []agent.Driver{j.closeDriver()}
	}
	if j.main {
		// A sub-agent gets none of what follows: no digest, no mem line,
		// no frame line, no reminder, no goal and no task list.
		c.PromptLines = []func(context.Context, agent.LineInfo) ([]agent.Part, error){j.promptLines}
		c.BeforeRequest = []func(context.Context, agent.RequestInfo) ([]agent.Part, error){j.beforeRequest}
		c.AfterSummary = j.afterSummary
		c.TurnEnded = j.turnEnded
		c.RunEnd = j.runEnd
		c.RunPrompt = j.runPrompt
		c.Slash = j.slash()
		if j.run {
			// The order is the contract's: a refused close sends the
			// model back first, then the goal; the person's Stop hooks
			// come after both.
			c.Drivers = append(c.Drivers, j.goalDriver())
			c.RepeatKey = j.repeatKey
			c.RepeatNudge = j.repeatNudge
		}
	}
	p.add(j)
	if p.opts.joined != nil {
		p.opts.joined(c, j)
	}
	return c, nil
}

// tools makes the session's gnovm tools, with every slot filled, and
// returns the ones this session is given (given, in plan.go).
func (j *join) tools() []core.Tool {
	set := gnovmtools.New(gnovmtools.Options{
		Client:      j.client(),
		Project:     j.project,
		Session:     func(context.Context, *core.ToolEnv) string { return j.id },
		SubAgent:    func(context.Context, string) bool { return !j.main },
		MemStack:    j.memStack,
		BoundGoal:   j.boundGoal,
		MemFresh:    j.memFresh,
		Namer:       j.namer,
		CheckCloses: j.checkCloses,
		ClosesKept:  j.closesKept,
		OnMemFrames: j.onMemFrames,
		OnMemGoals:  j.onMemGoals,
		OnMemErased: j.onMemErased,
		OnMemRules:  j.onMemRules,
	})
	tools, run := j.given(set)
	j.run = run
	return tools
}

// client is the tools' way to the GnoVM: the process's client, acquired
// when a tool is first called.
func (j *join) client() func() (gnovmtools.Client, error) {
	get := gnovmtools.Lazy(j.vms)
	watch := j.p.opts.watch
	if watch == nil {
		return get
	}
	return func() (gnovmtools.Client, error) {
		c, err := get()
		if err != nil {
			return nil, err
		}
		return watched{Client: c, watch: watch}, nil
	}
}

// watched is a client whose runs a test is shown.
type watched struct {
	gnovmtools.Client
	watch func(gnovm.RunRequest)
}

// Run shows the request and passes it on.
func (w watched) Run(ctx context.Context, req gnovm.RunRequest) (*gnovm.RunResult, error) {
	w.watch(req)
	return w.Client.Run(ctx, req)
}

// ready is Contribution.Ready: the session's agent exists, and no prompt
// has gone out yet. The person is told what is due in mem and what waits
// on them, whether or not the model brings it up, and the front end is
// handed where the session stands: a session that is opened again stands
// where its file says.
func (j *join) ready(a *agent.Agent) {
	j.mu.Lock()
	j.agent = a
	j.mu.Unlock()
	if j.main {
		j.memNotice()
		j.armGoalFlag()
		j.show()
	}
}

// busy is Contribution.Busy: a prompt has made the idle session busy. The
// session's turn lock is held until it goes idle again, so that other
// sessions working the same plan see this one working. A sub-agent's
// session holds its own. A person's session that can run mem also joins
// the plan's peers (gnovm.Lazy.JoinPeers), so that sessions started
// together are each told of the others from their first look at the plan.
func (j *join) busy(ctx context.Context) func() {
	release := j.vms.HoldTurn(ctx, j.id)
	if j.main && j.run {
		j.vms.JoinPeers(j.project, j.id)
	}
	j.mu.Lock()
	j.turn.began = j.now()
	j.mu.Unlock()
	return release
}

// close is Contribution.Close: the session is ending.
func (j *join) close(context.Context) {
	j.p.remove(j)
}

// tell puts a line in front of the person: in the conversation of an
// interactive session, on stderr in a run. It never waits. The wiring
// shows the line at once and keeps it in the session's file as soon as
// the log is free, so a line may be told from anywhere, the callbacks
// that run while the loop is storing something included. It is not to be
// called with the join's lock held: the line is shown to the join again
// when it is kept (onStored).
func (j *join) tell(level agent.Level, source, text string) {
	if text == "" || j.s.Notice == nil {
		return
	}
	j.s.Notice(level, source, text)
}

// show hands the front end where the session stands: for its status
// line, the frame the session is in (nothing at the root) and the goal it
// is bound to; and the task list, whose line in progress reads the frame.
// It is called whenever one of them may have changed: when the result of
// a call on a machine is in the log, when a goal is bound or let go, and
// when a session starts or is opened again. What has not changed is not
// sent again: the wiring compares.
func (j *join) show() {
	if !j.main {
		return
	}
	stack := j.ts.Stack()
	if j.s.Status != nil {
		frame := ""
		if n := len(stack); n > 0 {
			frame = stack[n-1].ID
		}
		j.s.Status("frame", frame)
	}
	j.showGoal()
	j.showTasks(stack)
}

// showGoal hands the front end the name of the goal the session is bound
// to, or nothing.
func (j *join) showGoal() {
	if !j.main || j.s.Status == nil {
		return
	}
	name := ""
	if b, _, ok := j.binding(); ok {
		name = b.Name
	}
	j.s.Status("goal", name)
}

// now reads the clock the program was given.
func (j *join) now() time.Time {
	if j.s.Now != nil {
		return j.s.Now()
	}
	return time.Now()
}

// env is what a tool call of the session is given to work with, as it
// stands now.
func (j *join) env() *core.ToolEnv {
	if j.s.Env != nil {
		if env := j.s.Env(); env != nil {
			return env
		}
	}
	return &core.ToolEnv{Cwd: j.s.Dirs.Cwd, SessionID: j.id, Store: new(sync.Map)}
}

// theAgent returns the session's agent, or nil before it exists.
func (j *join) theAgent() *agent.Agent {
	j.mu.Lock()
	a := j.agent
	j.mu.Unlock()
	if a == nil && j.s.Agent != nil {
		a = j.s.Agent()
	}
	return a
}

// memStack is the tools' MemStack slot: the session's stack of frames,
// bottom to top, as it is derived from the session's own log. mem is told
// it before every one of its calls, and checks every statement against
// it.
func (j *join) memStack(context.Context, string) []string {
	stack := j.ts.Stack()
	ids := make([]string, len(stack))
	for i, f := range stack {
		ids[i] = f.ID
	}
	return ids
}
