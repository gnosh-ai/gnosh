package hooks

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"sync"
	"time"
)

// Options is what a Runner is given about the program and the machine
// around it. Nothing is read from the process's own environment, clock or
// working directory unless a field's comment says so.
type Options struct {
	// Fires lists the events the program really fires. It has no
	// default: a hook configured on an event that is not listed would
	// never run, so Check reports it and NewRunner refuses it, and Run
	// refuses an event that is not listed. Events returns all of them.
	Fires []Event

	// ProjectDir is the project's root, where the session started. Hooks
	// get it as GNOSH_PROJECT_DIR and as CLAUDE_PROJECT_DIR, so that a
	// command written for Claude Code, such as
	// "$CLAUDE_PROJECT_DIR"/.claude/hooks/check.sh, works unchanged. It
	// is required when there are hooks.
	//
	// The shell fills the variable in. gnosh does not paste the directory
	// into the command's text, so a directory's name is never read as
	// shell code.
	ProjectDir string

	// StartDir is the directory the session started in. Hooks start
	// there when the session's working directory no longer exists.
	StartDir string

	// Untrusted says the project's folder is not trusted. A hook of the
	// person's own (Hook.Global) whose command runs a program in the
	// project, as "$CLAUDE_PROJECT_DIR"/.claude/hooks/check.sh does, is
	// then not run: a repository just cloned could have put anything
	// there. It counts as a hook that could not be started, so a guard of
	// that kind blocks what it would have checked.
	Untrusted bool

	// Shell is the shell that runs hook commands: a program that takes
	// -c and a command line. Empty means /bin/bash when that file exists
	// and /bin/sh when it does not.
	Shell string

	// Environ returns the environment hooks start from, and is called
	// for every event. Nil means os.Environ. The variables this package
	// sets are laid over it (see the package comment).
	Environ func() []string

	// Now is the clock a hook's Duration is measured with. Nil means
	// time.Now. It must be safe to call from several goroutines. Timeouts
	// always run on real time.
	Now func() time.Time

	// MemStatus is the path of the file that says what the project's mem
	// holds. Hooks get it as GNOSH_MEM_STATUS. Empty leaves it unset.
	MemStatus string

	// ToolNames lists the tools the program has. It is used only by
	// Check, to warn about a matcher that fits none of them. Empty means
	// no such warning.
	ToolNames []string

	// Renamed maps an older name of a tool to the name it has now. A
	// matcher that uses the older name fits the tool in full. Claude
	// Code's own renames are always known: Task (now Agent), KillShell
	// and KillBash (now TaskStop), BashOutput (now TaskOutput).
	Renamed map[string]string

	// Guards maps a tool's name to the Claude Code tool names whose
	// hooks also guard it, for tools Claude Code has nothing like: a
	// tool that saves a URL to a file is guarded by WebFetch and Write.
	// A hook that fits a call only this way can block it, put it to the
	// user, add text and change the input, but cannot approve it. Edit
	// is always guarded by MultiEdit.
	Guards map[string][]string

	// KillGrace is how long a hook that is being stopped, because its
	// time ran out or the turn was cancelled, has between SIGTERM and
	// SIGKILL. Default: 2 seconds.
	KillGrace time.Duration

	// PipeWait is how long to wait, once a hook's own process has exited,
	// for a process it left behind to let go of the hook's output. After
	// that the hook counts as one that could not evaluate. Default: 10
	// seconds, as in Claude Code.
	PipeWait time.Duration

	// MaxOutput is how many bytes of a hook's stdout are read. A hook
	// that prints more could not be read in full and so could not
	// evaluate. Default: 4 MiB.
	MaxOutput int

	// MaxText is how many characters of one hook's reason, added context
	// or warning are passed on. Longer text is passed on as its start
	// and its end around a line that says how much was left out.
	// Default: 10,000, as in Claude Code.
	MaxText int

	// Background is told what a hook that ran in the background left
	// when it ended, from a goroutine of its own. Nil drops it.
	Background func(Background)

	// MCP calls a tool of a connected MCP server for an mcp_tool hook,
	// and returns the text it answered with, and whether the tool said it
	// failed. Nil: such a hook could not run.
	MCP func(ctx context.Context, server, tool string, input json.RawMessage) (text string, failed bool, err error)

	// Prompt asks the model a prompt hook's question (prompthook.go) and
	// returns its answer; model is the hook's own "model", which the
	// caller may not honor. Nil: such a hook could not run.
	Prompt func(ctx context.Context, model, text string) (string, error)
	// Agent runs an agent hook's task in a sub-agent that may look with
	// its tools, and returns the sub-agent's last message (the app's
	// agenthook.go). Without it an agent hook could not run.
	Agent func(ctx context.Context, model, task string) (string, error)
}

const (
	// stderrKeep is how many bytes of a hook's stderr are kept at each
	// end.
	stderrKeep = 64 << 10
	// killWait is how long to wait, after SIGKILL, for a hook's processes
	// to be gone.
	killWait = 5 * time.Second
	// drainWait is how long to wait for the last of a stopped hook's
	// output, which is only wanted for the record.
	drainWait = 200 * time.Millisecond
)

// Runner runs the hooks of a configuration. It holds no state that
// changes, so one Runner serves a whole process, sub-agents included, and
// Run may be called from several goroutines at once.
type Runner struct {
	hooks      map[Event][]compiled
	fires      []Event
	tools      toolNames
	shell      string
	environ    func() []string
	now        func() time.Time
	projectDir string
	startDir   string
	untrusted  bool
	memStatus  string
	killGrace  time.Duration
	pipeWait   time.Duration
	maxOutput  int
	maxText    int

	// hooksMu guards hooks, to which Add puts more while the runner runs;
	// opts is what Add checks them with, and nextID numbers them.
	hooksMu sync.RWMutex
	opts    Options
	nextID  int

	// The hooks that run in the background: they run under bgCtx, which
	// Close cancels, and bg counts them.
	bgCtx        context.Context
	bgStop       context.CancelFunc
	bg           sync.WaitGroup
	onBackground func(Background)
}

// compiled is a hook with its matcher read.
type compiled struct {
	Hook
	m matcher
	// id tells the hooks Add put in force apart, for Once.
	id int
	// agent is the sub-agent whose events alone the hook takes (AddFor),
	// or "" for a hook that takes every event's.
	agent string
}

// selected is a hook chosen to run on one occurrence of an event.
type selected struct {
	Hook
	// guardOnly says the hook fits the tool only as a guard for another
	// tool's name.
	guardOnly bool
	// asBash says the hook is a Bash hook that guards a Monitor command:
	// it is given the command as a Bash call, and what it changes of that
	// is not Monitor's input.
	asBash bool
	id     int
}

// NewRunner makes a Runner for a configuration. It refuses, with a
// *ConfigError listing every problem, a configuration that Check finds a
// problem with (warnings aside): gnosh then stops at startup instead of
// running with a guard missing.
//
// To run with no hooks at all (safe mode, or "disableAllHooks"), give it
// an empty Config and the same Options.
func NewRunner(cfg Config, opts Options) (*Runner, error) {
	if bad := stopping(Check(cfg, opts)); len(bad) > 0 {
		return nil, &ConfigError{Problems: bad}
	}
	if len(cfg.Hooks) > 0 && opts.ProjectDir == "" {
		return nil, errors.New("hooks cannot be run without the project's directory, which they are given as GNOSH_PROJECT_DIR and CLAUDE_PROJECT_DIR (Options.ProjectDir is empty)")
	}
	r := &Runner{
		hooks:      map[Event][]compiled{},
		fires:      slices.Clone(opts.Fires),
		tools:      newToolNames(opts.Renamed, opts.Guards),
		shell:      opts.Shell,
		environ:    opts.Environ,
		now:        opts.Now,
		projectDir: opts.ProjectDir,
		startDir:   opts.StartDir,
		untrusted:  opts.Untrusted,
		memStatus:  opts.MemStatus,
		killGrace:  opts.KillGrace,
		pipeWait:   opts.PipeWait,
		maxOutput:  opts.MaxOutput,
		maxText:    opts.MaxText,

		onBackground: opts.Background,
		opts:         opts,
	}
	r.bgCtx, r.bgStop = context.WithCancel(context.Background())
	if r.shell == "" {
		r.shell = "/bin/sh"
		if st, err := os.Stat("/bin/bash"); err == nil && !st.IsDir() {
			r.shell = "/bin/bash"
		}
	}
	if r.environ == nil {
		r.environ = os.Environ
	}
	if r.now == nil {
		r.now = time.Now
	}
	if r.killGrace <= 0 {
		r.killGrace = 2 * time.Second
	}
	if r.pipeWait <= 0 {
		r.pipeWait = 10 * time.Second
	}
	if r.maxOutput <= 0 {
		r.maxOutput = 4 << 20
	}
	if r.maxText <= 0 {
		r.maxText = 10000
	}
	for _, h := range cfg.Hooks {
		info, _ := describe(h.Event)
		m, _ := compileMatcher(h.Matcher, info.narrow) // Check has passed it
		r.hooks[h.Event] = append(r.hooks[h.Event], compiled{Hook: h, m: m})
	}
	return r, nil
}

// Has reports whether any hook is configured for the event. The agent
// loop uses it to skip work that only exists for hooks: the timers behind
// Notification, the last message gathered for Stop.
func (r *Runner) Has(event Event) bool {
	r.hooksMu.RLock()
	defer r.hooksMu.RUnlock()
	return len(r.hooks[event]) > 0
}

// Add puts more hooks in force while the runner runs: a skill's, from when
// it is run. They are checked as NewRunner checks its own, and refused,
// all of them, with a *ConfigError, when one would not run.
func (r *Runner) Add(cfg Config) error {
	_, err := r.add(cfg, "")
	return err
}

// AddFor puts hooks in force for the events of one sub-agent alone, an
// agent definition's own (spec/cc/subagents.md 2): they take no event
// whose AgentID is another, a Stop hook among them is that sub-agent's
// SubagentStop, and the function it returns takes them away again, for
// when the sub-agent ends. They are checked as Add checks hooks.
func (r *Runner) AddFor(cfg Config, agentID string) (remove func(), err error) {
	if agentID == "" {
		return nil, errors.New("hooks for a sub-agent need its id (AddFor was given none)")
	}
	cfg.Hooks = slices.Clone(cfg.Hooks)
	for i := range cfg.Hooks {
		if cfg.Hooks[i].Event == Stop {
			cfg.Hooks[i].Event = SubagentStop
		}
	}
	ids, err := r.add(cfg, agentID)
	if err != nil {
		return nil, err
	}
	return func() {
		r.hooksMu.Lock()
		defer r.hooksMu.Unlock()
		for ev, list := range r.hooks {
			r.hooks[ev] = slices.DeleteFunc(list, func(c compiled) bool { return slices.Contains(ids, c.id) })
		}
	}, nil
}

// add checks hooks and puts them in force, for the events of agent alone
// when it is not "", and returns their ids.
func (r *Runner) add(cfg Config, agent string) ([]int, error) {
	if bad := stopping(Check(cfg, r.opts)); len(bad) > 0 {
		return nil, &ConfigError{Problems: bad}
	}
	r.hooksMu.Lock()
	defer r.hooksMu.Unlock()
	var ids []int
	for _, h := range cfg.Hooks {
		info, _ := describe(h.Event)
		m, _ := compileMatcher(h.Matcher, info.narrow) // Check has passed it
		r.nextID++
		ids = append(ids, r.nextID)
		r.hooks[h.Event] = append(r.hooks[h.Event], compiled{Hook: h, m: m, id: r.nextID, agent: agent})
	}
	return ids, nil
}

// dropOnce takes away the hooks with Once whose run did not fail.
func (r *Runner) dropOnce(event Event, sel []selected, records []Record) {
	var gone []int
	for i, h := range sel {
		if h.Once && h.id != 0 && i < len(records) && records[i].Result != ResultCouldNotEvaluate && records[i].Result != ResultCancelled {
			gone = append(gone, h.id)
		}
	}
	if len(gone) == 0 {
		return
	}
	r.hooksMu.Lock()
	defer r.hooksMu.Unlock()
	r.hooks[event] = slices.DeleteFunc(r.hooks[event], func(c compiled) bool { return slices.Contains(gone, c.id) })
}

// Hooks returns the hooks in force for the event, in the order they run
// and report in.
func (r *Runner) Hooks(event Event) []Hook {
	r.hooksMu.RLock()
	defer r.hooksMu.RUnlock()
	out := make([]Hook, 0, len(r.hooks[event]))
	for _, h := range r.hooks[event] {
		out = append(out, h.Hook)
	}
	return out
}

// Run runs the hooks of one occurrence of an event and returns what they
// came to.
//
// The hooks whose matcher fits are chosen; two with the same command run
// once. They are started at the same time, each as a shell command in a
// process group and session of its own with no terminal, with the event
// as JSON on stdin, and all are waited for. A hook that passes its
// timeout is stopped with its whole process group and counts as one that
// could not evaluate. Their answers are then combined in the order the
// hooks are configured (see Outcome).
//
// Run returns an error in two cases, and in both the Outcome it returns
// never lets a tool call or a prompt through (Blocked is set for
// PreToolUse and UserPromptSubmit):
//
//   - ctx was cancelled before every hook had finished. The hooks still
//     running are stopped, with their process groups, before Run returns.
//     The error wraps ctx's. A cancelled turn is not a hook's failure, so
//     nothing is reported as blocked by a hook; the Records say which
//     hooks finished and which were cut short.
//   - the Input is wrong: an event that is not one of gnosh's or that
//     Options.Fires does not list, a tool event with no tool name, a
//     value its event cannot have. These are mistakes in the calling
//     program, and no hook is run.
func (r *Runner) Run(ctx context.Context, in Input) (Outcome, error) {
	info, ok := describe(in.Event)
	switch {
	case !ok:
		return r.refuse(in.Event, nil, errors.New("no hooks were run: "+eventProblem(string(in.Event))))
	case !slices.Contains(r.fires, in.Event):
		return r.refuse(in.Event, nil, fmt.Errorf("the %s hooks were not run: this Runner was told that gnosh does not fire %s (it is not in Options.Fires)", in.Event, in.Event))
	}
	if err := in.check(info); err != nil {
		return r.refuse(in.Event, nil, fmt.Errorf("the %s hooks were not run: %w", in.Event, err))
	}
	sel := r.choose(&in, info)
	if len(sel) == 0 {
		return Outcome{}, nil
	}
	if ctx.Err() != nil {
		return r.refuse(in.Event, nil, stopped(ctx, in.Event))
	}

	base := r.environ()
	dir := r.startIn(in.Cwd, base)
	if in.Cwd == "" {
		in.Cwd = dir
	}
	stdin, err := in.payload(info, in.Cwd)
	if err != nil {
		return r.refuse(in.Event, nil, fmt.Errorf("the %s hooks were not run: %w", in.Event, err))
	}
	env := in.environ(info, base, dir, r.projectDir, r.memStatus)
	// A Bash hook that guards a Monitor command gets it as a Bash call, as
	// in Claude Code: a hook written for Bash reads Bash's input.
	feed := func(selected) ([]byte, []string) { return stdin, env }
	if slices.ContainsFunc(sel, func(h selected) bool { return h.asBash }) {
		asBash := in
		asBash.ToolName, asBash.ToolInput = "Bash", bashOfMonitor(in.ToolInput)
		bashIn, err := asBash.payload(info, in.Cwd)
		if err != nil {
			return r.refuse(in.Event, nil, fmt.Errorf("the %s hooks were not run: %w", in.Event, err))
		}
		bashEnv := asBash.environ(info, base, dir, r.projectDir, r.memStatus)
		feed = func(h selected) ([]byte, []string) {
			if h.asBash {
				return bashIn, bashEnv
			}
			return stdin, env
		}
	}

	// The hooks that run in the background are started, and nothing
	// waits for them.
	var started []Record
	waited := sel[:0:0]
	for _, h := range sel {
		if !h.Async && !h.AsyncRewake {
			waited = append(waited, h)
			continue
		}
		if r.bgCtx.Err() != nil {
			started = append(started, Record{Event: h.Event, Matcher: h.Matcher, Command: h.CommandLine(), Name: h.Name, Source: h.Source,
				ExitCode: -1, Result: ResultCancelled, Background: true, Reason: "the session is ending, and no hook starts in the background now."})
			continue
		}
		hStdin, hEnv := feed(h)
		started = append(started, r.background(&in, info, h, hStdin, hEnv, dir))
	}
	if sel = waited; len(sel) == 0 {
		return Outcome{Records: started}, nil
	}

	procs := make([]proc, len(sel))
	var all sync.WaitGroup
	for i, h := range sel {
		stdin, env := feed(h)
		all.Go(func() {
			if h.URL != "" {
				procs[i] = r.post(ctx, h.Hook, stdin, env, timeoutFor(h.Event, h.Timeout))
				return
			}
			if h.Server != "" {
				procs[i] = r.callMCP(ctx, h.Hook, stdin, timeoutFor(h.Event, h.Timeout))
				return
			}
			if h.Prompt != "" {
				limit := h.Timeout
				switch {
				case limit > 0:
				case h.Agent:
					limit = agentTimeoutDefault
				default:
					limit = promptTimeoutDefault
				}
				procs[i] = r.askModel(ctx, h.Hook, stdin, limit)
				return
			}
			procs[i] = r.exec(ctx, h.Hook, stdin, env, dir, timeoutFor(h.Event, h.Timeout))
		})
	}
	all.Wait()

	out := r.combine(&in, info, sel, procs)
	r.dropOnce(in.Event, sel, out.Records)
	out.Records = append(out.Records, started...)
	if slices.ContainsFunc(procs, func(p proc) bool { return p.cancelled }) {
		return r.refuse(in.Event, out.Records, stopped(ctx, in.Event))
	}
	return out, nil
}

// runsInProject says a hook's command names a program in the project's
// folder: by the variable the project's folder is given in, or by its
// path.
func runsInProject(h Hook, projectDir string) bool {
	for _, w := range append([]string{h.Command}, h.Args...) {
		for _, v := range []string{"$CLAUDE_PROJECT_DIR", "${CLAUDE_PROJECT_DIR", "$GNOSH_PROJECT_DIR", "${GNOSH_PROJECT_DIR"} {
			if strings.Contains(w, v) {
				return true
			}
		}
		if projectDir != "" && strings.Contains(w, strings.TrimSuffix(projectDir, "/")+"/") {
			return true
		}
	}
	return false
}

// sameHandler says two hooks are one handler configured twice: the same
// command, address with its headers, question, or MCP tool with its input,
// run the same way (in the background or not, with the same time). Such a
// hook runs once, as in Claude Code. The line a person reads of a hook
// (CommandLine) cannot tell: it cuts a question short and leaves out an
// MCP tool's input and an address's headers, and two hooks that differ in
// how they run are two guards.
func sameHandler(a, b Hook) bool {
	return a.Command == b.Command && slices.Equal(a.Args, b.Args) && (a.Args == nil) == (b.Args == nil) &&
		a.URL == b.URL && maps.Equal(a.Headers, b.Headers) && slices.Equal(a.AllowedEnvVars, b.AllowedEnvVars) &&
		a.Server == b.Server && a.Tool == b.Tool && bytes.Equal(a.Input, b.Input) &&
		a.Prompt == b.Prompt && a.Model == b.Model && a.Agent == b.Agent && a.ContinueOnBlock == b.ContinueOnBlock &&
		a.Async == b.Async && a.AsyncRewake == b.AsyncRewake && a.Timeout == b.Timeout
}

// monitorTool is the tool whose commands Bash hooks guard.
const monitorTool = "Monitor"

// bashOfMonitor is a Monitor call's input as a Bash call's: its command
// and its description. What cannot be read is passed on as it is.
func bashOfMonitor(input json.RawMessage) json.RawMessage {
	var m struct {
		Command     *string `json:"command"`
		Description *string `json:"description,omitempty"`
	}
	if json.Unmarshal(input, &m) != nil || m.Command == nil {
		return input
	}
	out, err := json.Marshal(m)
	if err != nil {
		return input
	}
	return out
}

// refuse returns what Run returns when it has no answer from the hooks:
// the error, and an Outcome that lets nothing through.
func (r *Runner) refuse(event Event, records []Record, err error) (Outcome, error) {
	out := Outcome{Records: records}
	switch event {
	case PreToolUse:
		out.Blocked, out.PermissionDecision, out.Reason = true, Deny, err.Error()
	case UserPromptSubmit:
		out.Blocked, out.Reason = true, err.Error()
	}
	return out, err
}

// stopped is the error for hooks cut short by a cancelled context. It
// wraps the context's own error and, when the context was given one, the
// cause it was cancelled for.
func stopped(ctx context.Context, event Event) error {
	err, cause := ctx.Err(), context.Cause(ctx)
	if cause == nil || cause == err {
		return fmt.Errorf("the %s hooks were stopped before they finished: %w", event, err)
	}
	return fmt.Errorf("the %s hooks were stopped before they finished: %w: %w", event, err, cause)
}

// choose picks the hooks that run on this occurrence of the event, in the
// order they are configured.
func (r *Runner) choose(in *Input, info eventInfo) []selected {
	var out []selected
	r.hooksMu.RLock()
	hooks := slices.Clone(r.hooks[in.Event])
	r.hooksMu.RUnlock()
	for _, h := range hooks {
		if h.agent != "" && h.agent != in.AgentID {
			// A sub-agent's own hook, and this is not its event.
			continue
		}
		if h.Agent && in.NoAgentHooks {
			continue
		}
		matched, guardOnly := true, false
		switch info.subject {
		case toolName:
			matched, guardOnly = r.tools.fit(h.m, in.ToolName)
		case ownValue:
			matched = h.m.matchesAny(in.subjects())
		}
		if !matched || guardOnly && in.ToolName == monitorTool && in.Event != PreToolUse && in.Event != PermissionRequest {
			// (A Bash hook guards a Monitor command before it runs; after
			// it, as in Claude Code, it has nothing to do with it.)
			continue
		}
		// An "if" filter: on a tool event the hook runs for the calls the
		// filter takes; on any other event never (spec/cc/hooks.md).
		if h.If != "" && (info.subject != toolName || in.IfMatch != nil && !in.IfMatch(h.If)) {
			continue
		}
		// The same handler configured twice runs once. It is a guard
		// only if every hook that brought it here is one.
		asBash := guardOnly && in.ToolName == monitorTool
		if i := slices.IndexFunc(out, func(s selected) bool { return sameHandler(s.Hook, h.Hook) }); i >= 0 {
			out[i].guardOnly = out[i].guardOnly && guardOnly
			out[i].asBash = out[i].asBash && asBash
			continue
		}
		out = append(out, selected{Hook: h.Hook, guardOnly: guardOnly, asBash: asBash, id: h.id})
	}
	return out
}

// startIn returns the directory hooks start in: the session's working
// directory, or, when that is gone, the directory the session started in,
// the project's, the user's home, and at last the system's temporary one.
func (r *Runner) startIn(cwd string, env []string) string {
	lookup := func(name string) string {
		for _, kv := range env {
			if v, ok := strings.CutPrefix(kv, name+"="); ok {
				return v
			}
		}
		return ""
	}
	for _, dir := range []string{cwd, r.startDir, r.projectDir, lookup("HOME"), lookup("TMPDIR"), "/tmp"} {
		if dir == "" {
			continue
		}
		if st, err := os.Stat(dir); err == nil && st.IsDir() {
			return dir
		}
	}
	return "/"
}

// proc is how running one hook's command went.
type proc struct {
	// startErr is why the command could not be started.
	startErr error
	// exit is the exit status, or -1 when there is none: the command
	// never started, was killed, or was stopped by gnosh.
	exit int
	// signal names the signal that killed the command, when one did and
	// gnosh did not send it.
	signal string
	// timedOut and cancelled say that gnosh stopped the command, and
	// why. limit is the time it was given.
	timedOut  bool
	cancelled bool
	limit     time.Duration
	// leftOpen says the command exited but something it started kept its
	// output open past the wait for it.
	leftOpen bool
	// unknown says why gnosh cannot tell how the command ended or what it
	// printed, when it cannot: the system would not say, or reading failed.
	unknown string
	// unstopped says that gnosh stopped the command and could not see all
	// of its processes go.
	unstopped bool
	// stdout is what the command printed, up to the limit; lost counts
	// the bytes past it. stderr is the start and the end of what it wrote
	// to standard error.
	stdout string
	lost   int64
	stderr string
	// took is how long it all took.
	took time.Duration
}

// pipes are the three pipes between gnosh and a hook.
type pipes struct {
	inR, inW   *os.File // the hook's stdin; gnosh writes to inW
	outR, outW *os.File // the hook's stdout; gnosh reads from outR
	errR, errW *os.File // the hook's stderr; gnosh reads from errR
}

// openPipes makes the three pipes.
func openPipes() (*pipes, error) {
	var p pipes
	var err error
	if p.inR, p.inW, err = os.Pipe(); err == nil {
		if p.outR, p.outW, err = os.Pipe(); err == nil {
			if p.errR, p.errW, err = os.Pipe(); err == nil {
				return &p, nil
			}
		}
	}
	p.closeHook()
	p.closeOurs()
	return nil, err
}

// closeHook closes the ends that belong to the hook.
func (p *pipes) closeHook() { closeFiles(p.inR, p.outW, p.errW) }

// closeOurs closes gnosh's own ends. Whoever is reading or writing them
// in another goroutine is let go.
func (p *pipes) closeOurs() { closeFiles(p.inW, p.outR, p.errR) }

func closeFiles(files ...*os.File) {
	for _, f := range files {
		if f != nil {
			f.Close()
		}
	}
}

// exec runs one hook's command and waits for it, for at most limit, or
// until ctx is cancelled. When it stops the hook it stops the hook's whole
// process group, and it does not return before its own goroutines have
// finished (but for the one that waits on a process the system would not
// kill, which ends when that process does).
func (r *Runner) exec(ctx context.Context, h Hook, stdin []byte, env []string, dir string, limit time.Duration) (p proc) {
	p.exit, p.limit = -1, limit
	began := r.now()
	defer func() { p.took = r.now().Sub(began) }()

	if r.untrusted && h.Global && runsInProject(h, r.projectDir) {
		p.startErr = errors.New("its program is in this folder, which is not trusted, and a repository could have put anything there. gnosh trust (or --trust-folder for one run) lets it run")
		return p
	}
	cmd := exec.Command(r.shell, "-c", h.Command)
	if h.Args != nil {
		// The exec form: the program and its arguments as they are, with
		// the project's directory put in for its placeholder.
		fill := strings.NewReplacer("${CLAUDE_PROJECT_DIR}", r.projectDir, "${GNOSH_PROJECT_DIR}", r.projectDir)
		prog, err := program(fill.Replace(h.Command), env, dir)
		if err != nil {
			p.startErr = err
			return p
		}
		args := make([]string, len(h.Args))
		for i, a := range h.Args {
			args[i] = fill.Replace(a)
		}
		cmd = exec.Command(prog, args...)
	}
	pp, err := openPipes()
	if err != nil {
		p.startErr = err
		return p
	}
	cmd.Dir, cmd.Env = dir, env
	cmd.Stdin, cmd.Stdout, cmd.Stderr = pp.inR, pp.outW, pp.errW
	ownGroup(cmd)
	if err := cmd.Start(); err != nil {
		pp.closeHook()
		pp.closeOurs()
		p.startErr = err
		return p
	}
	pp.closeHook() // the hook has its own copies now
	pid := cmd.Process.Pid

	// Three things go on beside the command: the event is written to its
	// stdin, its output is read (and bounded), and its exit is waited
	// for. cmd.Wait returns as soon as the command's own process has
	// exited, because the pipes are ours and not the exec package's.
	exited := make(chan struct{})
	var waitErr error
	go func() {
		waitErr = cmd.Wait()
		close(exited)
	}()
	wrote := make(chan struct{})
	go func() {
		defer close(wrote)
		// A hook that does not read its input, or exits before it has,
		// is not at fault for that.
		pp.inW.Write(stdin)
		pp.inW.Close()
	}()
	stdout := head{max: r.maxOutput}
	stderr := ends{keep: stderrKeep}
	var outErr, errErr error
	read := make(chan struct{})
	go func() {
		defer close(read)
		var both sync.WaitGroup
		both.Go(func() { _, outErr = io.Copy(&stdout, pp.outR) })
		both.Go(func() { _, errErr = io.Copy(&stderr, pp.errR) })
		both.Wait()
	}()

	deadline := time.NewTimer(limit)
	defer deadline.Stop()
	select {
	case <-exited:
		// The hook's own process has exited. Something it started may
		// still hold its output open, and until that lets go the hook
		// may not have said everything.
		wait := time.NewTimer(r.pipeWait)
		select {
		case <-read:
		case <-wait.C:
			p.leftOpen = true
		case <-deadline.C:
			p.timedOut = true
		case <-ctx.Done():
			p.cancelled = true
		}
		wait.Stop()
	case <-deadline.C:
		p.timedOut = true
	case <-ctx.Done():
		p.cancelled = true
	}
	if p.timedOut || p.cancelled {
		p.unstopped = !r.stop(pid, exited)
		// What it printed is no answer now, but the last of it is worth
		// having for the record.
		select {
		case <-read:
		case <-time.After(drainWait):
		}
	}
	pp.closeOurs()
	<-read
	<-wrote

	p.stdout, p.lost = string(stdout.buf), stdout.lost
	p.stderr = stderr.text()
	if p.timedOut || p.cancelled {
		return p
	}
	// The hook ended by itself. If the system will not say how, or its
	// output could not be read to the end, nothing can be made of it: a
	// hook whose answer is not known has not passed.
	p.exit, p.signal = ending(cmd.ProcessState)
	switch {
	case cmd.ProcessState == nil:
		p.unknown = "the system did not say how it ended"
		if waitErr != nil {
			p.unknown += ": " + waitErr.Error()
		}
	case p.leftOpen:
	case outErr != nil:
		p.unknown = "what it printed could not be read: " + outErr.Error()
	case errErr != nil:
		p.unknown = "what it wrote to standard error could not be read: " + errErr.Error()
	}
	return p
}

// stop ends a hook's whole process group. It asks first (SIGTERM), so
// that a hook that started helpers of its own can stop them, and when the
// grace runs out it kills whatever is left (SIGKILL). It reports whether
// everything is known to be gone.
func (r *Runner) stop(pid int, exited <-chan struct{}) bool {
	terminate(pid, isClosed(exited))
	if settled(pid, exited, r.killGrace) {
		return true
	}
	destroy(pid, isClosed(exited))
	return settled(pid, exited, killWait)
}

// settled waits, for at most d, until the hook's own process has been
// waited for and no process is left in its group.
func settled(pid int, exited <-chan struct{}, d time.Duration) bool {
	end := time.NewTimer(d)
	defer end.Stop()
	select {
	case <-exited:
	case <-end.C:
		return false
	}
	for pause := time.Millisecond; ; pause = min(2*pause, 20*time.Millisecond) {
		if groupGone(pid) {
			return true
		}
		select {
		case <-end.C:
			return false
		case <-time.After(pause):
		}
	}
}

// isClosed reports whether ch has been closed.
func isClosed(ch <-chan struct{}) bool {
	select {
	case <-ch:
		return true
	default:
		return false
	}
}

// program finds the program of a hook in the exec form: a name that holds
// a slash is a path, from dir when it is relative, and a bare name is
// looked for on the PATH the hook is given.
func program(name string, env []string, dir string) (string, error) {
	if strings.Contains(name, "/") {
		if !filepath.IsAbs(name) {
			name = filepath.Join(dir, name)
		}
		return name, nil
	}
	path := ""
	for _, kv := range env {
		if v, ok := strings.CutPrefix(kv, "PATH="); ok {
			path = v
		}
	}
	for _, d := range filepath.SplitList(path) {
		if !filepath.IsAbs(d) {
			// An empty or relative entry would be the folder the hook
			// works in, which a repository fills: it is passed over, as Go's
			// own lookup does.
			continue
		}
		p := filepath.Join(d, name)
		if info, err := os.Stat(p); err == nil && info.Mode().IsRegular() && info.Mode()&0o111 != 0 {
			return p, nil
		}
	}
	return "", fmt.Errorf("%s is not a program on the PATH the hook is given", name)
}

// StatusMessages returns the status messages of the hooks that would run,
// and be waited for, on this occurrence of the event, in the order they
// are configured: what a front end shows while they run. It runs nothing.
func (r *Runner) StatusMessages(in Input) []string {
	info, ok := describe(in.Event)
	if !ok || !slices.Contains(r.fires, in.Event) {
		return nil
	}
	var out []string
	for _, h := range r.choose(&in, info) {
		if msg := strings.TrimSpace(h.StatusMessage); msg != "" && !h.Async && !h.AsyncRewake && !slices.Contains(out, msg) {
			out = append(out, msg)
		}
	}
	return out
}

// hookClient sends http hooks' requests. It follows no redirect: an answer
// that sends the event elsewhere is not an answer.
var hookClient = &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}

// post runs an http hook: the event, as JSON, is the body of a POST to its
// address, and the body of the answer is read as a command hook's stdout.
// An answer with another status than 2xx is one gnosh cannot read.
func (r *Runner) post(ctx context.Context, h Hook, body []byte, env []string, limit time.Duration) (p proc) {
	p.exit, p.limit = -1, limit
	began := r.now()
	defer func() { p.took = r.now().Sub(began) }()
	rctx, cancel := context.WithTimeout(ctx, limit)
	defer cancel()
	req, err := http.NewRequestWithContext(rctx, http.MethodPost, h.URL, bytes.NewReader(body))
	if err != nil {
		p.startErr = err
		return p
	}
	req.Header.Set("Content-Type", "application/json")
	vars := map[string]string{}
	for _, kv := range env {
		if name, val, ok := strings.Cut(kv, "="); ok {
			vars[name] = val
		}
	}
	for name, val := range h.Headers {
		req.Header.Set(name, os.Expand(val, func(v string) string {
			if slices.Contains(h.AllowedEnvVars, v) {
				return vars[v]
			}
			return ""
		}))
	}
	stopped := func() bool {
		switch {
		case ctx.Err() != nil:
			p.cancelled = true
		case rctx.Err() != nil:
			p.timedOut = true
		default:
			return false
		}
		return true
	}
	resp, err := hookClient.Do(req)
	if err != nil {
		if !stopped() {
			p.startErr = err
		}
		return p
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, int64(r.maxOutput)+1))
	if err != nil {
		if !stopped() {
			p.unknown = "reading its answer failed (" + err.Error() + ")"
		}
		return p
	}
	if len(data) > r.maxOutput {
		p.lost, data = int64(len(data)-r.maxOutput), data[:r.maxOutput]
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		p.unknown = fmt.Sprintf("its server answered with HTTP status %d", resp.StatusCode)
		p.stderr = string(data)
		return p
	}
	p.exit, p.stdout = 0, string(data)
	return p
}

// callMCP runs an mcp_tool hook: the tool's arguments, with ${field.path}
// in their strings filled in from the event, go to the tool through
// Options.MCP, and the text it answers with is read as a command hook's
// stdout. A tool that says it failed is a hook that could not evaluate.
func (r *Runner) callMCP(ctx context.Context, h Hook, event []byte, limit time.Duration) (p proc) {
	p.exit, p.limit = -1, limit
	began := r.now()
	defer func() { p.took = r.now().Sub(began) }()
	if r.opts.MCP == nil {
		p.startErr = errors.New("this gnosh calls no MCP tool for a hook")
		return p
	}
	input, err := fillInput(h.Input, event)
	if err != nil {
		p.startErr = err
		return p
	}
	cctx, cancel := context.WithTimeout(ctx, limit)
	defer cancel()
	text, failed, err := r.opts.MCP(cctx, h.Server, h.Tool, input)
	switch {
	case ctx.Err() != nil:
		p.cancelled = true
	case cctx.Err() != nil:
		p.timedOut = true
	case err != nil:
		p.startErr = err
	case failed:
		p.unknown = "the tool answered that it failed"
		p.stderr = text
	default:
		p.exit, p.stdout = 0, text
	}
	return p
}

// fillInput puts the fields of the event in for ${field.path} in the
// strings of an mcp_tool hook's arguments ("${tool_input.file_path}"). A
// path the event does not have stands for nothing.
func fillInput(input json.RawMessage, event []byte) (json.RawMessage, error) {
	if len(input) == 0 {
		return json.RawMessage("{}"), nil
	}
	var args, ev any
	if err := json.Unmarshal(input, &args); err != nil {
		return nil, fmt.Errorf("the hook's input cannot be read (%v)", err)
	}
	_ = json.Unmarshal(event, &ev)
	var fill func(v any) any
	fill = func(v any) any {
		switch x := v.(type) {
		case string:
			return placeholder.ReplaceAllStringFunc(x, func(m string) string {
				cur := ev
				for _, key := range strings.Split(m[2:len(m)-1], ".") {
					obj, ok := cur.(map[string]any)
					if !ok {
						return ""
					}
					cur = obj[key]
				}
				switch y := cur.(type) {
				case nil:
					return ""
				case string:
					return y
				default:
					b, _ := json.Marshal(y)
					return string(b)
				}
			})
		case map[string]any:
			for k, e := range x {
				x[k] = fill(e)
			}
		case []any:
			for i, e := range x {
				x[i] = fill(e)
			}
		}
		return v
	}
	return json.Marshal(fill(args))
}

// placeholder finds ${field.path} in an mcp_tool hook's arguments.
var placeholder = regexp.MustCompile(`\$\{[A-Za-z_][A-Za-z0-9_]*(\.[A-Za-z_][A-Za-z0-9_]*)*\}`)
