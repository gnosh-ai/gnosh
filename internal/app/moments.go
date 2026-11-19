package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/gnosh-ai/gnosh/internal/agent"
	"github.com/gnosh-ai/gnosh/internal/cli"
	"github.com/gnosh-ai/gnosh/internal/core"
	"github.com/gnosh-ai/gnosh/internal/hooks"
	"github.com/gnosh-ai/gnosh/internal/instructions"
	"github.com/gnosh-ai/gnosh/internal/prompt"
	"github.com/gnosh-ai/gnosh/internal/tools/fstools"
)

// hookRunner runs the person's hooks with the session's facts filled in,
// and shows what every hook that ran did. Every event the hook package has
// is fired from here, at the moment of the loop that the event names:
//
//	PreToolUse          the gate, before the permission rules
//	PermissionRequest   the gate, when a person is about to be asked
//	PostToolUse,        the loop's AfterTool moment
//	PostToolUseFailure
//	UserPromptSubmit    the loop's PromptSubmit moment
//	SessionStart        the loop's SessionStart moment, and after a summary
//	Stop                a turn-end driver
//	StopFailure         the loop's TurnFailed moment
//	PreCompact,         the loop's BeforeSummary and AfterSummary moments
//	PostCompact
//	SessionEnd          the loop's SessionEnd moment
//	InstructionsLoaded  when instruction files are loaded
//	Notification        when the front end says a question or an idle
//	                    session has waited
//	PreModelSwitch,     /model, before and after the switch
//	PostModelSwitch
//
//	SubagentStart,      when the Agent tool starts a sub-agent, and when
//	SubagentStop        the sub-agent is about to finish (subagent.go)
//	PostToolBatch       the loop's AfterRound moment
//	ConfigChange        before a request, when a settings file changed
//	                    (settingswatch.go)
type hookRunner struct {
	l *live
	r *hooks.Runner
}

// has reports whether any hook is configured for the event.
func (h *hookRunner) has(e hooks.Event) bool { return h != nil && h.r != nil && h.r.Has(e) }

// run runs the hooks of one occurrence of an event and shows what each
// did. The session's own facts are filled in here, so that every event
// tells the hooks the same ones.
func (h *hookRunner) run(ctx context.Context, in hooks.Input) (hooks.Outcome, error) {
	l := h.l
	if l.isHookAgent(in.AgentID) {
		// An agent hook's own sub-agent (agenthook.go).
		if in.Event == hooks.SubagentStart || in.Event == hooks.SubagentStop {
			return hooks.Outcome{}, nil
		}
		in.NoAgentHooks = true
	}
	in.SessionID = l.ts.ID()
	in.TranscriptPath = l.ts.Path()
	if in.Cwd == "" {
		in.Cwd = l.cwd()
	}
	in.PermissionMode = string(l.engine.Mode())
	status := strings.Join(h.r.StatusMessages(in), "; ")
	if status != "" {
		l.deliver(&HookStatusEvent{Event: in.Event, Status: status})
	}
	out, err := h.r.Run(ctx, in)
	if status != "" {
		l.deliver(&HookStatusEvent{Event: in.Event})
	}
	l.showHooks(in, out)
	return out, err
}

// showHooks tells the front end what the hooks of one moment did, and puts
// what the person must not miss in front of them as a warning: a hook's
// own warning, and gnosh's sentence about a hook that objected or could
// not check where nothing can be blocked.
func (l *live) showHooks(in hooks.Input, out hooks.Outcome) {
	if len(out.Records) > 0 {
		l.inOrder()
		l.deliver(&HookEvent{Event: in.Event, CallID: in.ToolUseID, Tool: in.ToolName, Records: out.Records})
	}
	source := "hook:" + string(in.Event)
	for _, m := range out.SystemMessages {
		l.notice(agent.LevelWarning, source, m)
	}
	for _, w := range out.Warnings {
		l.notice(agent.LevelWarning, source, w)
	}
}

// backgroundHook takes what a hook that ran in the background left when it
// ended (spec/cc/hooks.md 7): what it adds for the model goes out with the
// next request, an "asyncRewake" hook's exit with 2 wakes the model at
// once, and one that could not be evaluated is a warning for the person.
func (l *live) backgroundHook(b hooks.Background) {
	a := l.agentReady.Load()
	if l.closing.Load() || a == nil {
		return
	}
	if b.Wake == "" && b.Warning == "" {
		// (A hook that wakes the model or could not be evaluated is said
		// once, below, and not also as its record.)
		l.inOrder()
		l.deliver(&HookEvent{Event: b.Event, Records: []hooks.Record{b.Record}})
	}
	source := "hook:" + string(b.Event)
	if b.Warning != "" {
		l.notice(agent.LevelWarning, source, b.Warning)
	}
	lead := "A " + string(b.Event) + " hook the user set up, which ran in the background, "
	if len(b.Context) > 0 {
		_ = a.Tell(agent.Note{Source: source, Text: prompt.Wrap(lead + "adds this:\n" + strings.TrimSpace(strings.Join(b.Context, "\n")))})
	}
	if b.Wake != "" {
		_ = a.Tell(agent.Note{Source: source, Wake: true, Text: prompt.Wrap(lead + "exited with 2 and says this:\n" + b.Wake),
			Notice: fmt.Sprintf("The %s hook %s, in the background, exited with 2: %s", b.Event, hookWho(b.Record), oneLine(b.Wake, 200))})
	}
}

// inOrder waits, a moment at most, for the events the agent has made so far
// to reach the front end, so that a line of gnosh's own (what a hook did)
// is not shown in the middle of a reply that is still on its way: the
// agent's events wait in a queue, and gnosh's own go straight to the front
// end. It is never called from inside the agent's OnEvent, which it would
// wait for; the bound keeps a mistake from holding anything up for long.
func (l *live) inOrder() {
	a := l.agentReady.Load()
	if a == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	_ = a.Drain(ctx)
}

// hookWho names a hook by its record: its name, or its command.
func hookWho(r hooks.Record) string {
	if r.Name != "" {
		return strconv.Quote(r.Name)
	}
	return "`" + oneLine(r.Command, 60) + "`"
}

// hookWords marks what a hook said as the words of a hook the user set
// up, so that the model can tell them from what the user typed and from
// what gnosh itself says.
func hookWords(event hooks.Event, tool, text string) string {
	lead := "A " + string(event) + " hook the user set up adds this:"
	if tool != "" {
		lead = "A " + string(event) + " hook the user set up says this about the " + tool + " call:"
	}
	return prompt.Wrap(lead + "\n" + strings.TrimSpace(text))
}

// moments joins the loop's moments to the hooks, to gnosh's own effects
// and to what the features brought, in the order written at the top of
// feature.go.
func (l *live) moments() agent.Moments {
	m := agent.Moments{
		SessionStart:  l.sessionStart,
		PromptSubmit:  l.promptSubmit,
		AfterTool:     l.afterTool,
		AfterRound:    l.afterRound,
		BeforeSummary: l.beforeSummary,
		AfterSummary:  l.afterSummary,
		TurnFailed:    l.turnFailed,
		TurnEnded:     l.turnEnded,
		SessionEnd:    l.sessionEnd,
	}
	// (The paths a prompt mentions come before what the features add, so
	// that mem's line stays the last a prompt carries.)
	m.PromptLines = append(m.PromptLines, l.firstPromptLines, l.mentionLines)
	m.BeforeRequest = append(m.BeforeRequest, l.turnCap, l.settingsChanged)
	m.MoreRounds = l.moreRounds
	var busy []func(context.Context) func()
	for _, c := range l.contribs {
		m.PromptLines = append(m.PromptLines, c.c.PromptLines...)
		m.BeforeRequest = append(m.BeforeRequest, c.c.BeforeRequest...)
		if c.c.Busy != nil {
			busy = append(busy, c.c.Busy)
		}
	}
	if len(busy) > 0 {
		m.Busy = func(ctx context.Context) func() {
			var releases []func()
			for _, hold := range busy {
				if release := hold(ctx); release != nil {
					releases = append(releases, release)
				}
			}
			return func() {
				for i := len(releases) - 1; i >= 0; i-- {
					releases[i]()
				}
			}
		}
	}
	return m
}

// firstPromptLines puts the instruction files and the environment block
// ahead of the first prompt that ever goes out in a session. The files
// come first: they are the same for every session of a project, and the
// environment block holds the date.
func (l *live) firstPromptLines(ctx context.Context, info agent.LineInfo) ([]agent.Part, error) {
	if !info.First {
		return nil, nil
	}
	var parts []agent.Part
	if text := l.startInstructions(); text != "" {
		parts = append(parts, agent.Part{Source: "instructions", Text: text, Before: true})
	}
	if text := l.startAgentTypes(); text != "" {
		parts = append(parts, agent.Part{Source: "agents", Text: text, Before: true})
	}
	if text := l.skillsLine(); text != "" {
		parts = append(parts, agent.Part{Source: "skills", Text: text, Before: true})
	}
	pre, err := l.agentPreloaded()
	if err != nil {
		return nil, err
	}
	for _, p := range pre {
		p.Before = true
		parts = append(parts, p)
	}
	return append(parts, agent.Part{Source: "environment", Text: l.environment(), Before: true}), nil
}

// startAgentTypes is the line that names the kinds of sub-agent a person
// or the project defined, for a session that has the Agent tool.
func (l *live) startAgentTypes() string {
	if !slices.Contains(l.toolNames(), agentToolName) {
		return ""
	}
	return l.agentTypesLine()
}

// turnCap stops a run when the model has written as many replies as
// --max-turns gave it. It is asked before every request, so the request
// that would be one too many is never sent.
func (l *live) turnCap(ctx context.Context, info agent.RequestInfo) ([]agent.Part, error) {
	if l.maxTurns <= 0 {
		return nil, nil
	}
	l.capMu.Lock()
	before, key := l.roundsBefore, ""
	if l.inv.InputFormat == cli.FormatStreamJSON {
		// Each message of a stream starts a turn with a fresh count, as in
		// Claude Code: after one turn reached the cap, every later message
		// was stopped before its first request.
		before, key = 0, info.Turn
	}
	over := before+info.Round > l.maxTurns
	first := over && (!l.capped || l.capKey != key)
	if over {
		l.capped, l.capKey = true, key
	}
	l.capMu.Unlock()
	if first {
		l.deliver(&CapEvent{Turns: l.maxTurns, Turn: info.Turn})
	}
	if over {
		l.agent.Interrupt()
	}
	return nil, nil
}

// turnsSpent reports whether the run has had the turns of the model that
// --max-turns gave it, so that a turn started now would be stopped before
// its first request. With stream-json input each turn has a fresh count,
// and none is spent ahead of it.
func (l *live) turnsSpent() bool {
	if l.maxTurns <= 0 || l.inv.InputFormat == cli.FormatStreamJSON {
		return false
	}
	l.capMu.Lock()
	defer l.capMu.Unlock()
	return l.capped || l.roundsBefore >= l.maxTurns
}

// moreRounds is turnCap asked after a round, before what the person queued
// meanwhile is handed to the turn: a turn that has had its replies ends
// there, and the queued words go out as a turn of their own (with stream-
// json input, with a fresh count) instead of into a turn the cap stops.
func (l *live) moreRounds(turn string, rounds int) (bool, string) {
	if l.maxTurns <= 0 {
		return true, ""
	}
	l.capMu.Lock()
	before, key := l.roundsBefore, ""
	if l.inv.InputFormat == cli.FormatStreamJSON {
		before, key = 0, turn
	}
	over := before+rounds >= l.maxTurns
	first := over && (!l.capped || l.capKey != key)
	if over {
		l.capped, l.capKey = true, key
	}
	l.capMu.Unlock()
	if first {
		l.deliver(&CapEvent{Turns: l.maxTurns, Turn: turn})
	}
	if over {
		return false, fmt.Sprintf("the run has had its %d turns of the model (--max-turns)", l.maxTurns)
	}
	return true, ""
}

// sessionStart runs the SessionStart hooks, once, before the first prompt
// this process sends to the session. What they print goes out with that
// prompt.
func (l *live) sessionStart(ctx context.Context, info agent.StartInfo) ([]agent.Part, error) {
	if !l.hooks.has(hooks.SessionStart) {
		return nil, nil
	}
	out, err := l.hooks.run(ctx, hooks.Input{Event: hooks.SessionStart, Source: info.Source, Model: info.Model, EnvFile: l.envFile()})
	if err != nil {
		return nil, err
	}
	l.setWatched(out)
	if out.EndTurn {
		// "continue": false: the session takes no prompt (Claude Code:
		// processing stops). What the hook said is the reason.
		return hookParts(hooks.SessionStart, out), &agent.StopWork{Reason: hookHalt(hooks.SessionStart, "this session takes no prompt", out.StopReason)}
	}
	return hookParts(hooks.SessionStart, out), nil
}

// hookHalt is what a person and a model are told when a hook's
// "continue": false stopped something.
func hookHalt(event hooks.Event, what, said string) string {
	text := fmt.Sprintf("A %s hook the user set up said to stop, so %s.", event, what)
	if said = strings.TrimSpace(said); said != "" {
		text += " It said: " + said
	}
	return text
}

// beforeModelSwitch runs the PreModelSwitch hooks before /model makes a
// switch. A refusal, or a hook that could not check, keeps the model as it
// is. "ask" puts the switch to the person, and is a refusal where nobody
// can answer (a run), as in Claude Code.
func (l *live) beforeModelSwitch(ctx context.Context, c *SlashContext, from, to, requested string) error {
	if !l.hooks.has(hooks.PreModelSwitch) {
		return nil
	}
	out, err := l.hooks.run(ctx, hooks.Input{Event: hooks.PreModelSwitch, FromModel: from, ToModel: to, RequestedModel: requested,
		Source: "command", ContextTokens: c.Agent.Context().Used})
	switch {
	case err != nil:
		return fmt.Errorf("the model was not changed: the PreModelSwitch hooks could not be run (%w)", err)
	case out.Blocked:
		return fmt.Errorf("the model was not changed: a PreModelSwitch hook the user set up refused the switch to %s. %s", to, firstOf(out.Reason, "It gave no reason."))
	case out.PermissionDecision != hooks.Ask:
		return nil
	}
	ans, err := c.Agent.Ask(ctx, agent.Question{
		Kind:  "model-switch",
		Title: "Switch the model to " + to + "? A PreModelSwitch hook asks.",
		Text:  out.Reason,
		Choices: []agent.Choice{
			{Key: "yes", Label: "Yes, switch to " + to},
			{Key: "no", Label: "No, keep " + from},
		},
	})
	switch {
	case errors.Is(err, agent.ErrNobodyToAsk):
		return fmt.Errorf("the model was not changed: a PreModelSwitch hook the user set up wants the switch to %s confirmed, and nobody is here to confirm it. %s", to, out.Reason)
	case err != nil || ans.Choice != "yes":
		return fmt.Errorf("the model was not changed: it is still %s", from)
	}
	return nil
}

// modelSwitched runs the PostModelSwitch hooks once /model has switched
// the model: what they add goes to the model with the next request.
func (l *live) modelSwitched(ctx context.Context, from, to, requested string) {
	if !l.hooks.has(hooks.PostModelSwitch) {
		return
	}
	out, err := l.hooks.run(ctx, hooks.Input{Event: hooks.PostModelSwitch, FromModel: from, ToModel: to, RequestedModel: requested,
		Source: "command", ContextTokens: l.agent.Context().Used})
	if err != nil || strings.TrimSpace(out.AdditionalContext) == "" {
		return
	}
	_ = l.agent.Tell(agent.Note{Source: "hook:PostModelSwitch", Text: hookWords(hooks.PostModelSwitch, "", out.AdditionalContext)})
}

// directoryAdded runs the DirectoryAdded hooks once /add-dir has added a
// working directory: a message they give goes to the model with the next
// turn.
func (l *live) directoryAdded(ctx context.Context, dir string) {
	if !l.hooks.has(hooks.DirectoryAdded) {
		return
	}
	out, err := l.hooks.run(ctx, hooks.Input{Event: hooks.DirectoryAdded, Directory: dir, Source: "slash_command"})
	if err != nil || len(out.SystemMessages) == 0 {
		return
	}
	_ = l.agent.Tell(agent.Note{Source: "hook:DirectoryAdded", Text: hookWords(hooks.DirectoryAdded, "", strings.Join(out.SystemMessages, "\n"))})
}

// hookParts turns the text hooks added into hidden parts for the model.
func hookParts(event hooks.Event, out hooks.Outcome) []agent.Part {
	if strings.TrimSpace(out.AdditionalContext) == "" {
		return nil
	}
	return []agent.Part{{Source: "hook:" + string(event), Text: hookWords(event, "", out.AdditionalContext)}}
}

// promptSubmit runs the UserPromptSubmit hooks on a prompt a person sent,
// once, when it is sent. A block sends the prompt back with the reason.
func (l *live) promptSubmit(ctx context.Context, p agent.Prompt) (agent.Admission, error) {
	// (A suggestion still being made would hold this prompt's request up.)
	l.suggest.stop()
	if !l.hooks.has(hooks.UserPromptSubmit) {
		return agent.Admission{}, nil
	}
	// The hook is told the prompt as the model will read it: what was
	// typed, then the text of anything pasted or attached as text.
	text := p.Text
	for _, b := range p.Attachments {
		if b.Type == core.BlockText {
			text += "\n\n" + b.Text
		}
	}
	out, err := l.hooks.run(ctx, hooks.Input{Event: hooks.UserPromptSubmit, Prompt: text})
	switch {
	case err != nil && ctx.Err() != nil:
		return agent.Admission{}, ctx.Err()
	case err != nil:
		return agent.Admission{}, err
	case out.EndTurn:
		return agent.Admission{Refused: true, Reason: firstOf(out.StopReason, out.Reason, "a UserPromptSubmit hook ended the turn before the prompt was sent")}, nil
	case out.Blocked:
		return agent.Admission{Refused: true, Reason: firstOf(out.Reason, "a UserPromptSubmit hook refused the prompt without saying why")}, nil
	}
	return agent.Admission{Parts: hookParts(hooks.UserPromptSubmit, out)}, nil
}

// firstOf returns the first of its arguments that is not blank.
func firstOf(ss ...string) string {
	for _, s := range ss {
		if strings.TrimSpace(s) != "" {
			return s
		}
	}
	return ""
}

// afterTool is what happens after every tool call that ran: gnosh's own
// effects, then the features', then the person's PostToolUse hooks.
func (l *live) afterTool(ctx context.Context, done agent.ToolDone) (agent.ToolVerdict, error) {
	v, err := l.afterToolWith(ctx, done, l.instr, l.contribs, l.env)
	if done.Result.Cwd != "" {
		l.cwdMoved(ctx, done.Result.Cwd)
	}
	return v, err
}

// afterRound runs the PostToolBatch hooks once every tool call of a reply
// has finished, before the model is asked again: what they add goes to the
// model after the results, and a block (or "continue": false) ends the
// turn there, its reason kept in the conversation for the model.
func (l *live) afterRound(ctx context.Context, done agent.RoundDone) (agent.ToolVerdict, error) {
	if !l.hooks.has(hooks.PostToolBatch) {
		return agent.ToolVerdict{}, nil
	}
	in := hooks.Input{Event: hooks.PostToolBatch, Cwd: l.subStart(done.AgentID), AgentID: done.AgentID, AgentType: done.AgentType}
	for _, c := range done.Calls {
		in.ToolCalls = append(in.ToolCalls, hooks.BatchCall{ToolName: c.Call.Name, ToolUseID: c.Call.ID, ToolInput: c.Call.Input, ToolResponse: core.TextOf(c.Result.Content)})
	}
	out, err := l.hooks.run(ctx, in)
	if err != nil {
		if ctx.Err() != nil {
			return agent.ToolVerdict{}, nil
		}
		return agent.ToolVerdict{}, err
	}
	var v agent.ToolVerdict
	if strings.TrimSpace(out.AdditionalContext) != "" {
		v.Context = append(v.Context, agent.Part{Source: "hook:PostToolBatch", Text: hookWords(hooks.PostToolBatch, "", out.AdditionalContext)})
	}
	stop := ""
	switch {
	case out.Blocked:
		stop = firstOf(out.Reason, "It gave no reason.")
	case out.EndTurn:
		stop = firstOf(out.StopReason, "It gave no reason.")
	}
	if stop != "" {
		v.Context = append(v.Context, agent.Part{Source: "hook:PostToolBatch", Text: prompt.Wrap("A PostToolBatch hook the user set up stopped the turn after these tool calls, and says this:\n" + strings.TrimSpace(stop))})
		v.Halt, v.Reason = true, "a PostToolBatch hook stopped the turn: "+oneLine(stop, 300)
	}
	return v, nil
}

// cwdMoved runs the CwdChanged hooks when a call of the session's own has
// left the shell in another directory than the last one seen. They may
// write export lines to the session's environment file.
func (l *live) cwdMoved(ctx context.Context, dir string) {
	l.cwdMu.Lock()
	old := l.lastCwd
	if old == "" {
		old = l.cfg.Paths.Cwd
	}
	if dir == old {
		l.cwdMu.Unlock()
		return
	}
	l.lastCwd = dir
	l.cwdMu.Unlock()
	if l.hooks.has(hooks.CwdChanged) {
		if out, err := l.hooks.run(ctx, hooks.Input{Event: hooks.CwdChanged, OldCwd: old, NewCwd: dir, EnvFile: l.envFile()}); err == nil {
			l.setWatched(out)
		}
	}
}

// afterToolWith is afterTool for one session: the person's own, or a
// sub-agent's, each with its own reading of the instruction files, its
// own part of the features and its own tool environment.
func (l *live) afterToolWith(ctx context.Context, done agent.ToolDone, instr *instructions.Set, contribs []joined, env func() *core.ToolEnv) (agent.ToolVerdict, error) {
	var v agent.ToolVerdict
	var errs []error
	v.Context = append(v.Context, l.lateInstructionsFor(ctx, instr, done)...)
	v.Context = append(v.Context, l.skillsForFile(done)...)
	for _, c := range contribs {
		if c.c.AfterTool == nil {
			continue
		}
		fv, err := c.c.AfterTool(ctx, done)
		if err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", c.f.Name, err))
			continue
		}
		v.Context = append(v.Context, fv.Context...)
		if fv.Halt && !v.Halt {
			v.Halt, v.Reason = true, fv.Reason
		}
	}
	hv, err := l.postToolHooks(ctx, done, env)
	if err != nil {
		errs = append(errs, err)
	}
	v.Context = append(v.Context, hv.Context...)
	v.Replace = hv.Replace
	if hv.Halt && !v.Halt {
		v.Halt, v.Reason = true, hv.Reason
	}
	return v, errors.Join(errs...)
}

// withheld is a call's result with its output withheld, for the reason
// given.
func withheld(res core.ToolResult, why string) *core.ToolResult {
	res.Content = []core.Block{core.Text("[The output of this call is withheld: " + why + ".]")}
	return &res
}

// postToolHooks runs the PostToolUse hooks on a call that succeeded, or
// the PostToolUseFailure hooks on one that returned an error. The call has
// run, so a block is feedback: its reason goes to the model after the
// tool's output.
func (l *live) postToolHooks(ctx context.Context, done agent.ToolDone, env func() *core.ToolEnv) (agent.ToolVerdict, error) {
	event := hooks.PostToolUse
	if done.Result.IsError {
		event = hooks.PostToolUseFailure
	}
	if !l.hooks.has(event) {
		return agent.ToolVerdict{}, nil
	}
	dir := l.cwd()
	if e := env(); e != nil && e.Cwd != "" {
		dir = e.Cwd
	}
	in := hooks.Input{
		Event:     event,
		Cwd:       dir,
		ToolName:  done.Call.Name,
		ToolUseID: done.Call.ID,
		ToolInput: hookToolInput(done.Call, dir, l.cfg.Paths.Home),
		Duration:  done.Duration,
		AgentID:   done.AgentID,
		AgentType: done.AgentType,
	}
	if done.Tool != nil {
		// The "if" filters look at what the call did, which the tool is
		// asked once, and only when a hook has a filter.
		var match func(string) bool
		in.IfMatch = func(rule string) bool {
			if match == nil {
				e := env()
				dir := l.cwd()
				if e != nil && e.Cwd != "" {
					dir = e.Cwd
				}
				act, err := describe(done.Tool, done.Call, e)
				if err != nil {
					match = func(string) bool { return true }
				} else {
					match = l.hookIf(dir, done.Call, act)
				}
			}
			return match(rule)
		}
	}
	text := core.TextOf(done.Result.Content)
	if done.Result.IsError {
		in.Error, in.IsInterrupt = text, done.Interrupted
	} else {
		in.ToolResponse = toolResponse(done.Result, text)
	}

	// A hook that reformats the file the model just wrote would make the
	// model's next edit of it fail as "changed since it was read". So the
	// file is looked at before and after the hooks.
	path := ""
	var before os.FileInfo
	names := fstools.Defaults()
	if !done.Result.IsError && (done.Call.Name == names.WriteName || done.Call.Name == names.EditName) {
		if path = touchedPath(done); path != "" {
			before, _ = os.Stat(path)
		}
	}

	out, err := l.hooks.run(ctx, in)
	if err != nil {
		// The hooks did not finish: what the call returned is withheld,
		// since one of them may have been there to hide part of it.
		v := agent.ToolVerdict{Replace: withheld(done.Result, "the "+string(event)+" hooks the user set up were stopped before they finished, and one of them may have been there to hide part of it")}
		if ctx.Err() != nil {
			return v, nil
		}
		return v, err
	}
	var v agent.ToolVerdict
	if out.CutShort && out.UpdatedOutput == nil && !out.OutputClash {
		// So with a hook that ran out of time or was killed: a slip of
		// one that hides a key must not show the key.
		v.Replace = withheld(done.Result, "a "+string(event)+" hook the user set up did not finish, and it may have been there to hide part of it")
	}
	if out.UpdatedOutput != nil || out.OutputClash {
		// What the model reads in place of what the tool returned. One
		// that cannot be read, or two that differ, withhold the output:
		// a hook that meant to hide part of it (a key, say) must not
		// have it shown for a slip. (Claude Code shows the original.)
		r := done.Result
		text, ok := outputText(out.UpdatedOutput)
		switch {
		case out.OutputClash:
			text = "[The output of this call is withheld: two PostToolUse hooks the user set up each gave one to show in its place, and they differ.]"
		case !ok:
			text = "[The output of this call is withheld: a PostToolUse hook the user set up gave one to show in its place that gnosh could not read as text.]"
		}
		r.Content = []core.Block{core.Text(text)}
		v.Replace = &r
	}
	if out.Blocked && strings.TrimSpace(out.Reason) != "" {
		v.Context = append(v.Context, agent.Part{Source: "hook:" + string(event), Text: hookWords(event, done.Call.Name, out.Reason)})
	}
	if strings.TrimSpace(out.AdditionalContext) != "" {
		v.Context = append(v.Context, agent.Part{Source: "hook:" + string(event), Text: hookWords(event, done.Call.Name, out.AdditionalContext)})
	}
	if out.EndTurn {
		v.Halt, v.Reason = true, firstOf(out.StopReason, "a "+string(event)+" hook ended the turn after "+done.Call.Name)
	}
	if before != nil {
		if after, err := os.Stat(path); err == nil && (after.Size() != before.Size() || !after.ModTime().Equal(before.ModTime())) {
			_ = fstools.MarkRead(env(), path)
			v.Context = append(v.Context, agent.Part{Source: "hook:" + string(event), Text: prompt.Wrap(
				"A PostToolUse hook the user set up changed " + path + " after your " + done.Call.Name + " call. Read the file again before you edit near what it changed.")})
		}
	}
	return v, nil
}

// outputText reads a PostToolUse hook's replacement for a tool's output
// as the text the model reads: a string; gnosh's own shape, an object
// with "text"; Bash's in Claude Code, an object with "stdout" and
// "stderr"; an object with "content" as a string; or MCP's, content
// blocks (alone, or as an object's "content"), of which the text ones
// are taken. Anything else cannot be read.
func outputText(raw json.RawMessage) (string, bool) {
	if raw == nil {
		return "", false
	}
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return s, true
	}
	blocks := func(raw json.RawMessage) (string, bool) {
		var bs []struct{ Type, Text string }
		if json.Unmarshal(raw, &bs) != nil {
			return "", false
		}
		var parts []string
		for _, b := range bs {
			if b.Type == "text" {
				parts = append(parts, b.Text)
			}
		}
		return strings.Join(parts, "\n"), len(parts) > 0 || len(bs) == 0
	}
	var obj map[string]json.RawMessage
	if json.Unmarshal(raw, &obj) != nil {
		return blocks(raw)
	}
	str := func(key string) (string, bool) {
		var v string
		field, ok := obj[key]
		return v, ok && json.Unmarshal(field, &v) == nil
	}
	if t, ok := str("text"); ok {
		return t, true
	}
	stdout, hasOut := str("stdout")
	stderr, hasErr := str("stderr")
	if hasOut || hasErr {
		var parts []string
		for _, p := range []string{stdout, stderr} {
			if p = strings.TrimRight(p, "\n"); p != "" {
				parts = append(parts, p)
			}
		}
		return strings.Join(parts, "\n"), true
	}
	if c, ok := str("content"); ok {
		return c, true
	}
	if c, ok := obj["content"]; ok {
		return blocks(c)
	}
	return "", false
}

// toolResponse is what a PostToolUse hook is told a tool returned: the
// facts of the call under the names the tools give them, and always the
// text the model reads.
func toolResponse(res core.ToolResult, text string) json.RawMessage {
	obj := map[string]any{}
	for k, v := range res.Meta {
		obj[k] = v
	}
	obj["text"] = text
	raw, err := json.Marshal(obj)
	if err != nil {
		raw, _ = json.Marshal(map[string]any{"text": text})
	}
	return raw
}

// beforeSummary runs the PreCompact hooks. They cannot stop the summary;
// what they print is added to the instructions for it.
func (l *live) beforeSummary(ctx context.Context, info agent.SummaryInfo) (string, error) {
	var parts []string
	for _, c := range l.contribs {
		if c.c.BeforeSummary == nil {
			continue
		}
		if s, err := c.c.BeforeSummary(ctx, info); err == nil && strings.TrimSpace(s) != "" {
			parts = append(parts, s)
		}
	}
	if l.hooks.has(hooks.PreCompact) {
		out, err := l.hooks.run(ctx, hooks.Input{Event: hooks.PreCompact, Trigger: info.Trigger, CustomInstructions: info.Instructions})
		if err != nil {
			return strings.Join(parts, "\n"), err
		}
		if strings.TrimSpace(out.AdditionalContext) != "" {
			parts = append(parts, out.AdditionalContext)
		}
	}
	return strings.Join(parts, "\n"), nil
}

// afterSummary says what the session is given back once a summary stands
// in place of the conversation: the instruction files given during the
// session are forgotten, so that each is given again when the model next
// works where it applies; the background commands still running are
// named; the features hand on what is theirs; and the PostCompact hooks
// and then the SessionStart hooks run, the latter with the source
// "compact".
func (l *live) afterSummary(ctx context.Context, info agent.SummaryInfo) ([]agent.Part, error) {
	var parts []agent.Part
	// The model no longer sees any file it read: Edit and Write refuse to
	// change one until Read has shown it again, as after a slide, so that
	// no file is written from a copy the model can no longer see. The ones
	// it read or changed last are read again and given back below.
	recent := fstools.Recent(l.env(), backFiles)
	fstools.ForgetAll(l.env())
	if l.instr != nil {
		// The files given along the way are forgotten, so that each is
		// given again when the model next works where it applies; the
		// ones in effect from the start are given back now.
		l.instr.Forget()
		if text := l.startInstructions(); text != "" {
			parts = append(parts, agent.Part{Source: "instructions", Text: text})
		}
	}
	if text := l.startAgentTypes(); text != "" {
		parts = append(parts, agent.Part{Source: "agents", Text: text})
	}
	if text := l.skillsLine(); text != "" {
		parts = append(parts, agent.Part{Source: "skills", Text: text})
	}
	if text := l.skillsAfterSummary(); text != "" {
		parts = append(parts, agent.Part{Source: "skills", Text: text})
	}
	if pre, err := l.agentPreloaded(); err != nil {
		l.notice(agent.LevelWarning, "agent", "The skills the kind "+l.agentName()+" preloads could not be given back after the summary: "+err.Error())
	} else {
		parts = append(parts, pre...)
	}
	back, given := l.filesBack(ctx, recent, info.Window)
	parts = append(parts, back...)
	defer func() {
		if ctx.Err() != nil {
			// Stopped: what this gives back may not reach the model (a
			// moment that does not finish in time is gone on without), so
			// the files it read again do not count as read.
			fstools.Forget(l.env(), given...)
		}
	}()
	if l.shell != nil {
		var running []string
		for _, t := range l.shell.Tasks() {
			if t.Running {
				what := firstOf(t.Description, firstLine(t.Command))
				running = append(running, fmt.Sprintf("- %s (%s), output in %s", t.ID, what, t.OutputFile))
			}
		}
		if len(running) > 0 {
			parts = append(parts, agent.Part{Source: "background", Text: prompt.Wrap(
				"These background commands are still running:\n" + strings.Join(running, "\n"))})
		}
	}
	var errs []error
	for _, c := range l.contribs {
		if c.c.AfterSummary == nil {
			continue
		}
		more, err := c.c.AfterSummary(ctx, info)
		if err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", c.f.Name, err))
			continue
		}
		parts = append(parts, more...)
	}
	var halt *agent.StopWork
	if l.hooks.has(hooks.PostCompact) {
		out, err := l.hooks.run(ctx, hooks.Input{Event: hooks.PostCompact, Trigger: info.Trigger, CompactSummary: info.Summary})
		switch {
		case err != nil:
			errs = append(errs, err)
		case out.EndTurn:
			halt = &agent.StopWork{Reason: hookHalt(hooks.PostCompact, "the turn ended after the summary", out.StopReason)}
		}
	}
	if l.hooks.has(hooks.SessionStart) {
		out, err := l.hooks.run(ctx, hooks.Input{Event: hooks.SessionStart, Source: "compact", Model: l.agent.Model().Info.ID, EnvFile: l.envFile()})
		switch {
		case err != nil:
			errs = append(errs, err)
		default:
			parts = append(parts, hookParts(hooks.SessionStart, out)...)
			if out.EndTurn && halt == nil {
				halt = &agent.StopWork{Reason: hookHalt(hooks.SessionStart, "the turn ended after the summary", out.StopReason)}
			}
		}
	}
	if halt != nil {
		if err := errors.Join(errs...); err != nil {
			l.notice(agent.LevelWarning, "summary", fmt.Sprintf("What the session is given back after a summary could not all be put together (%v).", err))
		}
		return parts, halt
	}
	return parts, errors.Join(errs...)
}

// firstLine returns the first line of s.
func firstLine(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	return s
}

// failureKind names a failed turn's kind the way StopFailure hooks know
// it.
func failureKind(kind string) string {
	switch kind {
	case "rate_limit", "overloaded", "authentication_failed", "invalid_request", "server_error":
		return kind
	case "context_too_long":
		return "invalid_request"
	}
	return "unknown"
}

// turnFailed runs the StopFailure hooks when a turn ends failed. Nothing
// they say is read.
func (l *live) turnFailed(ctx context.Context, res agent.TurnResult) {
	if !l.hooks.has(hooks.StopFailure) {
		return
	}
	_, _ = l.hooks.run(ctx, hooks.Input{
		Event:                hooks.StopFailure,
		Error:                failureKind(res.ErrorKind),
		ErrorDetails:         res.Detail,
		LastAssistantMessage: firstOf(res.Text, res.Detail),
	})
}

// turnEnded is told how every turn ended, and tells the features. That
// it has is recorded whatever becomes of them: the end of a run waits for
// it (RunEnd).
func (l *live) turnEnded(ctx context.Context, res agent.TurnResult) {
	defer l.markTold(res.Turn)
	l.capMu.Lock()
	l.roundsBefore += res.Rounds
	l.capMu.Unlock()
	for _, c := range l.contribs {
		if c.c.TurnEnded != nil {
			c.c.TurnEnded(ctx, res)
		}
	}
	l.tellNewBuild()
	l.loopTurnEnded()
	l.suggestNext(res)
}

// sessionEnd is what happens when the session closes, in the order the
// specs give: every command the session started is stopped, with what
// those commands started, and then the SessionEnd hooks run.
func (l *live) sessionEnd(ctx context.Context, info agent.EndInfo) {
	l.closeShell()
	if !l.hooks.has(hooks.SessionEnd) {
		return
	}
	reason := info.Reason
	switch reason {
	case "clear", "other", "prompt_input_exit", "resume", "logout":
	default:
		reason = "other"
	}
	_, _ = l.hooks.run(ctx, hooks.Input{Event: hooks.SessionEnd, Reason: reason})
}

// drivers lists what may send the model back to work when a turn is about
// to end, in the order they are asked: the features' drivers, the person's
// Stop hooks, the features' late drivers. /loop comes after all of them,
// in the loop itself.
func (l *live) drivers() []agent.Driver {
	var out []agent.Driver
	for _, c := range l.contribs {
		out = append(out, c.c.Drivers...)
	}
	out = append(out, agent.Driver{Name: "stop-hook", Every: 10, Marker: prompt.StopMarker, Decide: l.stopHooks})
	for _, c := range l.contribs {
		out = append(out, c.c.LateDrivers...)
	}
	return out
}

// stopHooks runs the person's Stop hooks when the turn is about to end. A
// block keeps the turn alive, with the hook's reason as the model's next
// instruction, for as long as the hook objects: no count ends it. Every
// tenth block in a row the loop asks the model, in place of the plain
// send-back, to judge whether there is a real reason to stop.
func (l *live) stopHooks(ctx context.Context, end agent.TurnEnd) (agent.Verdict, error) {
	if !l.hooks.has(hooks.Stop) {
		return agent.Verdict{Action: agent.End}, nil
	}
	out, err := l.hooks.run(ctx, hooks.Input{
		Event:                hooks.Stop,
		StopHookActive:       end.Streak > 0,
		LastAssistantMessage: end.Text,
	})
	switch {
	case err != nil:
		return agent.Verdict{}, err
	case out.EndTurn:
		return agent.Verdict{Action: agent.Halt, Objection: firstOf(out.StopReason, "a Stop hook ended the turn")}, nil
	case out.Blocked:
		v := agent.Verdict{
			Action:    agent.Continue,
			Prompt:    l.texts.StopHook(out.Reason),
			Objection: oneLine(firstOf(out.Reason, "a Stop hook objects"), 400),
		}
		if out.CouldNotEvaluate {
			v.Notice = "A Stop hook could not check, and a check that could not run has not passed, so the turn goes on. What it said: " + oneLine(out.Reason, 400)
		}
		return v, nil
	case strings.TrimSpace(out.AdditionalContext) != "":
		// Text a Stop hook adds for the model keeps the turn going with
		// it, as guidance.
		return agent.Verdict{
			Action:    agent.Continue,
			Prompt:    hookWords(hooks.Stop, "", out.AdditionalContext),
			Objection: oneLine(out.AdditionalContext, 400),
		}, nil
	}
	return agent.Verdict{Action: agent.End}, nil
}

// oneLine puts s on one line and cuts it to about max bytes.
func oneLine(s string, max int) string {
	s = strings.Join(strings.Fields(s), " ")
	if len(s) <= max {
		return s
	}
	cut := max
	for cut > 0 && s[cut]&0xC0 == 0x80 {
		cut--
	}
	return s[:cut] + "…"
}
