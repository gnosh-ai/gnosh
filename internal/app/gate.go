package app

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/gnosh-ai/gnosh/internal/agent"
	"github.com/gnosh-ai/gnosh/internal/config"
	"github.com/gnosh-ai/gnosh/internal/core"
	"github.com/gnosh-ai/gnosh/internal/filelock"
	"github.com/gnosh-ai/gnosh/internal/hooks"
	"github.com/gnosh-ai/gnosh/internal/permission"
	"github.com/gnosh-ai/gnosh/internal/prompt"
	"github.com/gnosh-ai/gnosh/internal/shellquote"
	"github.com/gnosh-ai/gnosh/internal/tools/fstools"
	"github.com/gnosh-ai/gnosh/internal/tools/shelltool"
	"github.com/gnosh-ai/gnosh/internal/trust"
)

// gate answers the loop's one question about a tool call: may it run? It
// joins the person's PreToolUse hooks, the permission rules and the person
// in exactly this order:
//
//  1. The PreToolUse hooks, for every call, a sub-agent's included. A
//     block ends it there: the model reads the reason, and no rule is
//     looked at. A hook may also change the call's arguments.
//     Then, for a session or sub-agent with a git work tree of its own,
//     the work tree: a call that would change files or run in a checkout
//     the work tree was made from is refused, whatever the rules allow.
//  2. The permission engine, on the arguments the hooks left, with the
//     hooks' verdict folded in (permission.Decision.WithHook): a deny rule
//     beats a hook's approval, a hook's "ask" asks even when the call
//     would have run, and a hook's approval skips only the question the
//     mode would have asked.
//  3. When that comes to "ask": the PermissionRequest hooks, which may
//     answer in the person's place; and then the person. Where nobody can
//     be asked the call is refused, and the model reads why: a run that
//     nobody attends does not approve its own calls.
//
// A call whose tool says a person must confirm it (core.Action.Confirm:
// it throws away something that cannot be got back) is put to the person
// even when step 2 would let it run, in every mode, skipped permissions
// included. Only the person's own yes runs it: no rule, no hook's
// approval and no PermissionRequest hook stands in, and "don't ask again"
// is not offered. A deny rule still refuses it outright.
//
// A call its tool has already found unable to run is still judged by the
// hooks and the rules, on what it was aimed at, so that a refused path
// does not give away whether it exists; nobody is asked about it.
//
// A call of a feature's tool that reaches beyond what the feature itself
// keeps is judged twice in step 2: as the call of its own tool that it
// is, and as the call of gnosh's own tool that reaches the same thing
// (reach). A call that runs a command on a declared host is also judged
// as the Bash call for that host, so it asks as every command on another
// machine does and the person's deny rules are held against the command
// before it leaves. A call that hands over a file of this machine is also
// judged as the Read of that file, so a file outside the working
// directories asks as reading it does. The stricter answer holds.
type gate struct {
	l *live
}

// critical is how long a person has to answer the question about a
// dangerous removal when permissions are otherwise skipped.
const criticalWait = 2 * time.Minute

// criticalTimeout is the refusal of a call gnosh asks a person about even
// with permissions skipped, when nobody answered in time: why it asked, in
// the engine's words, and what to do. A program whose name is put
// together when the line runs is asked about because it could be rm (box
// 1, 16 November: Qwen built ssh as S=s; S="${S}sh"; $S to slip past the
// refusal of ssh, and was told only that it was a removal).
func criticalTimeout(why string) string {
	s := "This call did not run: gnosh asks a person about it even with permissions skipped, and nobody answered within two minutes."
	if why = strings.TrimSpace(why); why != "" {
		s += " Why it asks: " + why
	}
	if strings.Contains(why, "the program it runs") {
		return s + " Write the program's name out as a plain word: a name put together in the line could be rm, for all gnosh can tell."
	}
	return s + " If it is really wanted, say what you were about to do and why, and let the user run it."
}

// Check decides about one call.
func (g *gate) Check(ctx context.Context, req agent.GateRequest) (dec agent.Decision, err error) {
	l := g.l
	// What a PreToolUse hook added for the model goes with the call's
	// result, whatever is decided.
	var added []agent.Part
	defer func() { dec.Context = append(dec.Context, added...) }()
	call, action, cannot := req.Call, req.Action, req.CannotRun
	cwd := l.cwd()
	if req.Env != nil && req.Env.Cwd != "" {
		cwd = req.Env.Cwd
	}

	// 1. The PreToolUse hooks.
	verdict, hookReason := permission.HookNone, ""
	var changed json.RawMessage
	if l.hooks.has(hooks.PreToolUse) {
		out, err := l.hooks.run(ctx, hooks.Input{
			Event: hooks.PreToolUse, Cwd: cwd,
			ToolName: call.Name, ToolUseID: call.ID, ToolInput: hookToolInput(call, cwd, l.cfg.Paths.Home),
			AgentID: req.AgentID, AgentType: req.AgentType,
			IfMatch: l.hookIf(cwd, call, action),
		})
		if strings.TrimSpace(out.AdditionalContext) != "" {
			added = append(added, agent.Part{Source: "hook:PreToolUse", Text: hookWords(hooks.PreToolUse, call.Name, out.AdditionalContext)})
		}
		switch {
		case err != nil && ctx.Err() != nil:
			return agent.Decision{}, ctx.Err()
		case err != nil:
			return agent.Decision{}, err
		case out.EndTurn:
			return agent.Decision{Halt: true, Reason: "This call did not run: a PreToolUse hook the user set up ended the turn. " + firstOf(out.StopReason, out.Reason)}, nil
		case out.Blocked:
			return agent.Decision{Reason: "This call did not run: a PreToolUse hook the user set up blocked it. " + firstOf(out.Reason, "It gave no reason.")}, nil
		}
		if out.UpdatedInput != nil && cannot == nil {
			// The rules judge the arguments the tool will run with.
			changed = out.UpdatedInput
			call.Input = changed
			if call.Name == askToolName && answersChanged(req.Call.Input, changed) {
				// A hook may answer the model's questions in the
				// person's place; the tool is told that these answers
				// are a hook's. Answers the model wrote and the hook
				// left as they were are not.
				l.hookChanged.Store(call.ID, true)
			}
			act, err := describe(req.Tool, call, req.Env)
			if err != nil {
				return agent.Decision{Reason: fmt.Sprintf("This call did not run: a PreToolUse hook the user set up changed its arguments, and with them the call cannot run (%v).", err)}, nil
			}
			action = act
		}
		switch out.PermissionDecision {
		case hooks.Allow:
			verdict = permission.HookAllow
		case hooks.Ask:
			verdict = permission.HookAsk
		}
		hookReason = out.Reason
	}

	// A session or sub-agent with a git work tree of its own (--worktree,
	// isolation worktree) changes files and runs its commands there, not in
	// the person's checkout it was made from, whatever the rules and the
	// mode allow: that is what the work tree is for.
	if req.Env != nil && req.Env.Tree != nil {
		why := ""
		switch {
		case action.Kind == core.ActionEdit:
			why = outsideTree(*req.Env.Tree, cwd, action.Paths)
		case action.Kind == core.ActionExecute && onThisMachine(call):
			why = l.commandOutsideTree(*req.Env.Tree, cwd, action.Command)
		}
		if why != "" {
			return agent.Decision{Reason: "This call did not run. " + why}, nil
		}
	}

	// 2. The rules and the mode, on the final arguments. A relative path
	// in a command is read from where the shell of the agent that made the
	// call now is: the directory goes with the question, in one step,
	// because the session and its sub-agents share the engine and each has
	// a directory of its own. The engine is given the arguments whole, a
	// shell call's host among them: it judges a call whose host is this
	// machine (left blank, or named "local") as the call for this machine
	// that it is, and any other as a command on another machine, which
	// always asks.
	d := l.reach(cwd, call, action, l.engine.DecideAt(cwd, call.Name, call, action))
	d = l.resumeAsStart(ctx, cwd, call, d).WithHook(verdict, hookReason)
	confirm := action.Confirm && cannot == nil
	if confirm {
		// Asked every time: a kept approval would be the rule that this
		// call must not have.
		d.Suggestions = nil
	}
	switch d.Behavior {
	case permission.Allow:
		if !confirm {
			return agent.Decision{Allow: true, Input: changed}, nil
		}
		d.Reason = "This call throws away something that cannot be got back, so gnosh asks before it runs, whatever the rules and the mode allow."
	case permission.Deny:
		return agent.Decision{Reason: "This call did not run. " + d.Reason + " Do the work another way if there is one, or say what you could not do."}, nil
	}

	// 3. Somebody has to say yes.
	if cannot != nil {
		// The call will not run whatever is answered, so nobody is asked.
		// With a person there the model may read the tool's own account of
		// why; without one it reads no more than it would of any call
		// that needs approval.
		if l.interactive {
			return agent.Decision{Allow: true}, nil
		}
		return agent.Decision{Reason: l.unattended(d), Unattended: true}, nil
	}
	if !confirm && l.hooks.has(hooks.PermissionRequest) {
		out, err := l.hooks.run(ctx, hooks.Input{
			Event: hooks.PermissionRequest, Cwd: cwd,
			ToolName: call.Name, ToolInput: hookToolInput(call, cwd, l.cfg.Paths.Home),
			AgentID: req.AgentID, AgentType: req.AgentType,
			IfMatch: l.hookIf(cwd, call, action),
		})
		switch {
		case err != nil && ctx.Err() != nil:
			return agent.Decision{}, ctx.Err()
		case err != nil:
			l.notice(agent.LevelWarning, "hook:PermissionRequest", fmt.Sprintf("The PermissionRequest hooks could not be run (%v), so the question is put to you.", err))
		case out.PermissionDecision == hooks.Allow && d.Critical:
			// A call that may remove what cannot be got back is the
			// person's alone to approve: no rule and no hook does
			// (permission.Decision.Critical). A hook's refusal stands.
			l.notice(agent.LevelWarning, "hook:PermissionRequest", "A PermissionRequest hook approved a call that may remove what cannot be got back. Only you can approve that, so the question is put to you.")
		case out.PermissionDecision == hooks.Allow:
			return agent.Decision{Allow: true, Input: changed}, nil
		case out.PermissionDecision == hooks.Deny:
			return agent.Decision{Halt: out.EndTurn, Reason: "This call did not run: a PermissionRequest hook the user set up refused it. " + firstOf(out.Reason, out.StopReason)}, nil
		}
	}

	if l.cfg.Permissions.DenyInsteadOfPrompting.V {
		// (permissions.denyInsteadOfPrompting, as Claude Code's: what
		// would ask is refused, and the turn goes on. PermissionRequest
		// hooks have had their say.)
		return agent.Decision{Reason: l.unattended(d), Unattended: true}, nil
	}
	askCtx := ctx
	q := l.permissionQuestion(call, action, d)
	stops := ""
	if req.AgentID != "" {
		// A sub-agent asks: the question says which, and a background
		// one's says what it was started for, so that it is never taken
		// for a question of the conversation the person is in.
		asker := "The sub-agent " + trust.Escape(firstOf(req.AgentType, "of this session"))
		if b, ok := l.backgroundAgent(req.AgentID); ok {
			asker = fmt.Sprintf("The background sub-agent %s (%s)", trust.Escape(b.kind), trust.Escape(oneLine(b.desc, 80)))
			stops = "that sub-agent"
			for i := range q.Choices {
				if q.Choices[i].Key == "no" {
					q.Choices[i].Label = "No (alone it stops that sub-agent; with words after it, it reads them and carries on)"
				}
			}
		}
		q.Title = asker + " asks: " + q.Title
	}
	if changed != nil {
		// A hook changed the call: the question shows what will run, not
		// what the model wrote (its diff, its command).
		act := action
		q.Action = &act
	}
	data := QuestionData{Modes: map[string]string{}, CommentOnYes: true, Input: call.Input, PersonOnly: confirm || d.Critical, Stops: stops}
	for i, s := range d.Suggestions {
		if s.Mode != "" {
			data.Modes["always:"+strconv.Itoa(i)] = string(s.Mode)
		}
	}
	if d.Critical && d.Mode == permission.ModeBypass {
		var cancel context.CancelFunc
		askCtx, cancel = context.WithTimeout(ctx, criticalWait)
		defer cancel()
		data.Until = l.p.now().Add(criticalWait)
	}
	q.Data = data
	ans, err := req.Ask(askCtx, q)
	switch {
	case errors.Is(err, agent.ErrNobodyToAsk):
		return agent.Decision{Reason: l.unattended(d), Unattended: true}, nil
	case err != nil && ctx.Err() == nil && askCtx.Err() != nil:
		return agent.Decision{Reason: criticalTimeout(d.Reason)}, nil
	case err != nil:
		return agent.Decision{}, err
	}
	switch {
	case ans.Choice == "yes":
		yesWith(ctx, call, ans)
		return agent.Decision{Allow: true, Input: changed}, nil
	case strings.HasPrefix(ans.Choice, "always:"):
		i, _ := strconv.Atoi(strings.TrimPrefix(ans.Choice, "always:"))
		if i >= 0 && i < len(d.Suggestions) {
			l.keepApproval(d.Suggestions[i])
		}
		yesWith(ctx, call, ans)
		return agent.Decision{Allow: true, Input: changed}, nil
	}
	// No. With words, the model reads them and carries on; without, the
	// person wants the work to stop, not another way round.
	if comment := strings.TrimSpace(ans.Text); comment != "" {
		return agent.Decision{Reason: "This call did not run: the user refused it, and said: " + comment}, nil
	}
	return agent.Decision{Halt: true, Reason: "This call did not run: the user refused it."}, nil
}

// reach judges what a call of a feature's tool reaches beyond the
// feature's own keeping, and returns the answer that holds for the call:
// own, which is the engine's answer for the call as its own tool's, or
// the engine's answer for the call of gnosh's own tool that reaches the
// same thing, whichever is the stricter.
//
// A feature's tool is known to the engine by its name alone: quiet, or
// asking every time. What a call of it touches is said by the tool
// itself, when it describes the call (core.Describer), and is judged as
// the same reach is judged everywhere else:
//
//   - core.ActionExecute with a command, in a call whose "host" argument
//     names another machine (a host that is left out, left blank or named
//     "local" is this machine, as for Bash), is the Bash call that runs
//     that command on that host: it always asks, when anything asks at
//     all; the person's deny rules are held against the command; and with
//     permissions skipped the declaration of the host is the whole gate.
//   - core.ActionRead with paths is the Read of each of those files: one
//     outside the working directories asks, unless a rule allows reading
//     it, exactly as the model's own Read of it would. (Where Read is
//     switched off altogether there is no such reading to judge by, and
//     the tool's own answer stands.)
//
// Nothing here makes an answer milder than the engine's own.
func (l *live) reach(cwd string, call core.ToolCall, action core.Action, own permission.Decision) permission.Decision {
	if own.Behavior == permission.Deny || !l.featureTool(call.Name) {
		return own
	}
	switch action.Kind {
	case core.ActionExecute:
		if strings.TrimSpace(action.Command) == "" {
			return own
		}
		var fields map[string]json.RawMessage
		if json.Unmarshal(call.Input, &fields) != nil {
			return own
		}
		host, named := fields["host"]
		if !named {
			return own
		}
		var name string
		if json.Unmarshal(host, &name) == nil {
			if name = strings.TrimSpace(name); name == "" || name == shelltool.LocalName {
				return own
			}
		}
		// Whatever else stands in that place is somewhere else, as the
		// engine reads a Bash call's host: it is handed over as written.
		quoted, err := json.Marshal(action.Command)
		if err != nil {
			return own
		}
		input := json.RawMessage(`{"command":` + string(quoted) + `,"host":` + string(host) + `}`)
		own = stricter(own, call.Name+" runs a command on another machine for this call, which is judged as that command is.",
			l.engine.DecideAt(cwd, shelltool.BashName,
				core.ToolCall{ID: call.ID, Name: shelltool.BashName, Input: input},
				core.Action{Kind: core.ActionExecute, Command: action.Command, Summary: action.Summary}))
	case core.ActionRead:
		read := fstools.Defaults().ReadName
		for _, path := range action.Paths {
			if !filepath.IsAbs(path) {
				continue
			}
			quoted, err := json.Marshal(path)
			if err != nil {
				continue
			}
			d := l.engine.DecideAt(cwd, read,
				core.ToolCall{ID: call.ID, Name: read, Input: json.RawMessage(`{"file_path":` + string(quoted) + `}`)},
				core.Action{Kind: core.ActionRead, Paths: []string{path}, Summary: action.Summary})
			if d.Stage == permission.StageTool {
				// Read itself is switched off in this session, which says
				// nothing about this file: the person's deny and ask
				// rules about the path were held against the call
				// already, and the tool's own answer stands.
				continue
			}
			own = stricter(own, call.Name+" hands over a file for this call, which is judged as reading that file is.", d)
		}
	}
	return own
}

// stricter is other, its reason led by why, where other is the stricter
// answer (a refusal over anything, a question over a yes), and own
// otherwise.
func stricter(own permission.Decision, why string, other permission.Decision) permission.Decision {
	if other.Behavior == permission.Allow || own.Behavior == permission.Deny {
		return own
	}
	if other.Behavior == permission.Deny || own.Behavior == permission.Allow {
		other.Reason = why + " " + other.Reason
		return other
	}
	return own
}

// resumeAsStart judges a SendMessage call also as the Agent call that would start
// a sub-agent of the kind it takes up again, and keeps the stricter answer:
// a deny or ask rule about a kind of sub-agent (Agent(name)) covers giving
// one of that kind more to do, as it covers starting one. A call whose
// sub-agent cannot be found is left as it is; it refuses itself when it
// runs.
func (l *live) resumeAsStart(ctx context.Context, cwd string, call core.ToolCall, own permission.Decision) permission.Decision {
	if call.Name != sendMessageName || own.Behavior == permission.Deny {
		return own
	}
	var in sendMessageInput
	if json.Unmarshal(call.Input, &in) != nil {
		return own
	}
	meta, err := subagentMeta(ctx, l.subagentsDir(), strings.TrimSpace(in.To))
	if err != nil {
		return own
	}
	kind := meta[metaAgentType]
	input, err := json.Marshal(agentInput{Description: firstOf(strings.TrimSpace(in.Summary), "more to do"), Prompt: in.Message, Type: kind})
	if err != nil {
		return own
	}
	as := core.ToolCall{ID: call.ID, Name: agentToolName, Input: input}
	act, _ := (&agentTool{l: l}).Describe(as, nil)
	return stricter(own, sendMessageName+" gives a "+kind+" sub-agent more to do, which is judged as starting one is.",
		l.engine.DecideAt(cwd, agentToolName, as, act))
}

// featureTool reports whether a tool is one a feature brought, and not
// one of gnosh's own, which the engine knows for what they are.
func (l *live) featureTool(name string) bool {
	for _, f := range l.p.o.Features {
		for _, t := range f.Tools {
			if t.Name == name {
				return true
			}
		}
	}
	return false
}

// yesWith passes on what the person said with a yes: the call runs, and
// the model reads their words with its result.
func yesWith(ctx context.Context, call core.ToolCall, ans agent.Answer) {
	comment := strings.TrimSpace(ans.Text)
	a := agent.FromContext(ctx)
	if comment == "" || a == nil {
		return
	}
	_ = a.Tell(agent.Note{
		Source: "permission",
		Text:   prompt.Wrap("The user approved your " + call.Name + " call, and said with it: " + comment),
	})
}

// hookIf is the "if" filter of the hooks on one call (hooks.Input.IfMatch):
// a hook runs for the call when a deny rule of the same text would refuse
// it or could apply to it, and when the rule cannot be applied at all, of
// which the person was told when the session started. A filter's mistake
// never skips a hook.
func (l *live) hookIf(dir string, call core.ToolCall, action core.Action) func(rule string) bool {
	return func(rule string) bool {
		takes, err := l.engine.Takes(rule, dir, call.Name, call, action)
		return takes || err != nil
	}
}

// checkHookFilters tells the person, as the session starts, of a hook
// whose "if" filter will not do what it seems to: one on an event that is
// not about a tool call, which never runs, and one whose rule cannot be
// applied, which runs for every call its matcher takes.
func (l *live) checkHookFilters() {
	for _, h := range l.cfg.Hooks {
		if h.If == "" {
			continue
		}
		if !hooks.Event(h.Event).OfTool() {
			l.notice(agent.LevelWarning, "hooks", fmt.Sprintf(
				"The %s hook from %s has an \"if\" filter, which only the hooks of tool events have, so it never runs.", h.Event, h.Origin))
			continue
		}
		if err := l.engine.CheckRule(h.If); err != nil {
			var re *permission.RuleError
			if errors.As(err, &re) {
				err = re.Err
			}
			l.notice(agent.LevelWarning, "hooks", fmt.Sprintf(
				"The %s hook from %s has the \"if\" filter %s, which cannot be applied (%s), so it runs for every call its matcher takes.",
				h.Event, h.Origin, h.If, strings.TrimRight(err.Error(), ".")))
		}
	}
}

// describe asks a tool what a call would do, and survives a tool that
// crashes over it.
func describe(tool core.Tool, call core.ToolCall, env *core.ToolEnv) (act core.Action, err error) {
	d, ok := tool.(core.Describer)
	if !ok {
		return core.Action{Kind: core.ActionOther}, nil
	}
	defer func() {
		if p := recover(); p != nil {
			err = fmt.Errorf("the tool crashed looking at them: %v", p)
		}
	}()
	return d.Describe(call, env)
}

// unattended is what the model reads when a call needs a person's yes and
// nobody is there to give it.
func (l *live) unattended(d permission.Decision) string {
	why := strings.TrimSuffix(d.Reason, ".")
	if l.cfg.Permissions.DenyInsteadOfPrompting.V {
		return "This call did not run: it needs approval, and the user's settings refuse what would ask instead of asking (permissions.denyInsteadOfPrompting) (" + why +
			"). Do the work another way if there is one, or say what you could not do. Do not send this call again."
	}
	text := "This call did not run: it needs a person's approval, and nobody is here to give it (" + why +
		"). Do the work another way if there is one, or say what you could not do."
	if l.inv.PermissionPrompts == "none" {
		text += " Nobody answers permission questions in this run: do not send this call again."
	}
	return text
}

// escapeKeepingTabs is trust.Escape for a line of a command, whose tabs
// stay tabs.
func escapeKeepingTabs(line string) string {
	parts := strings.Split(line, "\t")
	for i, p := range parts {
		parts[i] = trust.Escape(p)
	}
	return strings.Join(parts, "\t")
}

// answersChanged says a hook's rewrite of a question call gave it answers
// the model's call did not have.
func answersChanged(model, hook json.RawMessage) bool {
	var a, b struct {
		Answers json.RawMessage `json:"answers"`
	}
	if json.Unmarshal(hook, &b) != nil || len(b.Answers) == 0 || string(b.Answers) == "null" {
		return false
	}
	_ = json.Unmarshal(model, &a)
	var ca, cb bytes.Buffer
	if json.Compact(&cb, b.Answers) != nil {
		return true
	}
	return json.Compact(&ca, a.Answers) != nil || !bytes.Equal(ca.Bytes(), cb.Bytes())
}

// hookPathKeys are the arguments of the file tools that name a path.
var hookPathKeys = map[string]string{"Read": "file_path", "Write": "file_path", "Edit": "file_path", "MultiEdit": "file_path",
	"NotebookEdit": "notebook_path", "Glob": "path", "Grep": "path"}

// hookToolInput is a call's arguments as hooks are given them: the path a
// file tool is given made absolute and clean, from the call's folder and
// with ~ the home folder, as Claude Code gives them. A hook that guards
// $CLAUDE_PROJECT_DIR/secrets sees "secrets/key" and "~/x" for the files
// they are. (The tool reads the same file from the path as written.)
func hookToolInput(call core.ToolCall, cwd, home string) json.RawMessage {
	key, ok := hookPathKeys[call.Name]
	if !ok {
		return call.Input
	}
	var m map[string]json.RawMessage
	var p string
	if json.Unmarshal(call.Input, &m) != nil || json.Unmarshal(m[key], &p) != nil || p == "" {
		return call.Input
	}
	abs := p
	switch {
	case home != "" && p == "~":
		abs = home
	case home != "" && strings.HasPrefix(p, "~/"):
		abs = filepath.Join(home, p[2:])
	case !filepath.IsAbs(p) && filepath.IsAbs(cwd) && !strings.HasPrefix(p, "~"):
		abs = filepath.Join(cwd, p)
	}
	if !filepath.IsAbs(abs) || filepath.Clean(abs) == p {
		return call.Input
	}
	m[key], _ = json.Marshal(filepath.Clean(abs))
	out, err := json.Marshal(m)
	if err != nil {
		return call.Input
	}
	return out
}

// permissionQuestion writes the question about one call: exactly what
// would run, why gnosh asks, and the answers there are. What the model and
// the files it read brought is shown escaped, never as terminal control
// sequences.
func (l *live) permissionQuestion(call core.ToolCall, action core.Action, d permission.Decision) agent.Question {
	if call.Name == exitPlanName {
		return l.planQuestion(call, d)
	}
	var b strings.Builder
	// The tool's own line about the call, unless it only repeats the
	// command that is shown in full below it.
	summary := strings.TrimSpace(action.Summary)
	if summary != "" && summary != strings.TrimSpace(action.Command) {
		b.WriteString(showLines(summary) + "\n")
	}
	switch {
	case action.Command != "":
		// The command as it is written: the prompt mark on each line where
		// the shell starts reading a command, and the text of a
		// here-document, a line that goes on, or what stands inside quotes
		// across lines set under it; tabs stay tabs (the interface sets
		// them out as spaces), and every other control character is
		// escaped.
		starts := shellquote.CommandStarts(action.Command)
		for i, line := range strings.Split(action.Command, "\n") {
			lead := "    "
			if starts[i] {
				lead = "  $ "
			}
			b.WriteString(lead + escapeKeepingTabs(line) + "\n")
		}
	case len(action.Paths) > 0:
		for i, p := range action.Paths {
			switch {
			case i > 0:
				b.WriteString("  (which really is " + trust.Escape(p) + ")\n")
			case !strings.Contains(summary, p):
				b.WriteString("  " + trust.Escape(p) + "\n")
			}
		}
	case action.URL != "":
		b.WriteString("  " + trust.Escape(action.URL) + "\n")
	default:
		b.WriteString("  " + trust.Escape(oneLine(string(call.Input), 600)) + "\n")
	}
	if d.Reason != "" {
		b.WriteString("Why gnosh asks: " + d.Reason)
	}
	choices := []agent.Choice{{Key: "yes", Label: "Yes, this once"}}
	for i, s := range d.Suggestions {
		label := ""
		switch {
		case s.Mode != "":
			label = fmt.Sprintf("Yes, and switch to %s for the rest of the session", s.Mode)
		case s.Scope == permission.GrantLocal:
			label = fmt.Sprintf("Yes, and don't ask again for %s (kept in %s)", trust.Escape(strings.Join(s.Rules, ", ")), l.cfg.Paths.LocalSettings)
		default:
			label = fmt.Sprintf("Yes, and don't ask again this session for %s", trust.Escape(strings.Join(s.Rules, ", ")))
		}
		choices = append(choices, agent.Choice{Key: "always:" + strconv.Itoa(i), Label: label})
	}
	choices = append(choices, agent.Choice{Key: "no", Label: "No (alone it stops the turn; with words after it, the model reads them and carries on)"})
	return agent.Question{
		Kind:    "permission",
		Title:   "Allow " + call.Name + "?",
		Text:    strings.TrimRight(b.String(), "\n"),
		Choices: choices,
	}
}

// planQuestion is the question ExitPlanMode puts: the plan, and whether to
// carry it out, asking about each edit or accepting them, or to keep
// planning.
func (l *live) planQuestion(call core.ToolCall, d permission.Decision) agent.Question {
	var in struct {
		Plan string `json:"plan"`
	}
	_ = json.Unmarshal(call.Input, &in)
	plan := strings.TrimSpace(in.Plan)
	if plan == "" {
		plan = "(The model gave no plan.)"
	}
	choices := []agent.Choice{{Key: "yes", Label: "Yes, carry it out, asking about each edit"}}
	for i, s := range d.Suggestions {
		if s.Mode == permission.ModeAcceptEdits {
			choices = append(choices, agent.Choice{Key: "always:" + strconv.Itoa(i), Label: "Yes, carry it out, and accept its edits without asking"})
		}
	}
	choices = append(choices, agent.Choice{Key: "no", Label: "No, keep planning (say what to change after it; alone it stops the turn)"})
	return agent.Question{
		Kind:    "permission",
		Title:   "Carry out this plan?",
		Text:    showLines(plan),
		Choices: choices,
	}
}

// showLines escapes text for the screen a line at a time, so that its line
// breaks stay.
func showLines(s string) string {
	lines := strings.Split(s, "\n")
	for i, line := range lines {
		lines[i] = trust.Escape(line)
	}
	return strings.Join(lines, "\n")
}

// keepApproval acts on a "yes, and don't ask again": the suggested rules
// are in force from the next call on, and the ones that are to be kept are
// written to the settings file the config names for that.
func (l *live) keepApproval(s permission.Suggestion) {
	if s.Mode != "" {
		if err := l.setMode(s.Mode); err != nil {
			l.notice(agent.LevelWarning, "permission", fmt.Sprintf("The permission mode could not be changed (%v).", err))
			return
		}
		l.notice(agent.LevelInfo, "permission", fmt.Sprintf("The permission mode is %s for the rest of the session.", s.Mode))
		return
	}
	var kept []string
	for _, rule := range s.Rules {
		if err := l.engine.Grant(rule, s.Scope); err != nil {
			l.notice(agent.LevelWarning, "permission", fmt.Sprintf("The approval could not be kept (%v), so gnosh will ask again.", err))
			continue
		}
		kept = append(kept, rule)
	}
	if s.Scope != permission.GrantLocal || len(kept) == 0 {
		return
	}
	if err := l.saveRules(kept); err != nil {
		l.notice(agent.LevelWarning, "permission", fmt.Sprintf("The approval holds for this session, but it could not be written down (%v), so gnosh will ask again next time.", err))
	}
}

// saveRules adds allow rules to the project's local settings file, the one
// place gnosh itself writes settings. The file as it would then be is
// checked the way every config file is checked before a byte is written,
// so that gnosh can never write a file it would then refuse to load.
//
// The file is read as every config file is, so comments a person put in it
// and a comma after a last item do not stop an approval from being saved;
// but it is written again whole, as plain JSON, and they are not in what
// is written.
func (l *live) saveRules(rules []string) error {
	return l.editLocalRules(func(lists map[string][]string) {
		for _, r := range rules {
			if !slices.Contains(lists["allow"], r) {
				lists["allow"] = append(lists["allow"], r)
			}
		}
	})
}

// editLocalRules changes the permission rules of the project's local
// settings file: change is handed the file's allow, ask and deny lists.
func (l *live) editLocalRules(change func(lists map[string][]string)) error {
	return l.editLocalSettings(func(doc map[string]json.RawMessage) error {
		perms := map[string]json.RawMessage{}
		if raw, ok := doc["permissions"]; ok {
			if err := json.Unmarshal(raw, &perms); err != nil {
				return fmt.Errorf("its \"permissions\" are not an object (%v)", err)
			}
		}
		names := []string{"allow", "ask", "deny"}
		lists := map[string][]string{}
		for _, name := range names {
			if raw, ok := perms[name]; ok {
				var rules []string
				if err := json.Unmarshal(raw, &rules); err != nil {
					return fmt.Errorf("its \"permissions.%s\" is not a list of rules (%v)", name, err)
				}
				lists[name] = rules
			}
		}
		change(lists)
		for _, name := range names {
			rules := lists[name]
			if _, had := perms[name]; len(rules) == 0 && !had {
				continue
			}
			raw, err := json.Marshal(append([]string{}, rules...))
			if err != nil {
				return err
			}
			perms[name] = raw
		}
		raw, err := json.Marshal(perms)
		doc["permissions"] = raw
		return err
	})
}

// editLocalSettings changes the project's local settings file
// (.gnosh/settings.local.json, never committed), which gnosh writes as
// plain JSON, as Claude Code does its own: change is handed the file's
// keys, under the file's lock, and an error from it leaves the file as it
// is. A file gnosh would not load is not written.
func (l *live) editLocalSettings(change func(doc map[string]json.RawMessage) error) error {
	path := l.cfg.Paths.LocalSettings
	dir := filepath.Dir(path)
	_, statErr := os.Stat(dir)
	fresh := errors.Is(statErr, os.ErrNotExist)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	if fresh {
		// The project's data folder is never committed.
		_ = os.WriteFile(filepath.Join(dir, ".gitignore"), []byte("*\n"), 0o600)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	release, err := filelock.File(ctx, path+".lock")
	if err != nil {
		return fmt.Errorf("the settings file is being written by another gnosh: %w", err)
	}
	defer release()

	doc := map[string]json.RawMessage{}
	if data, err := os.ReadFile(path); err == nil && len(strings.TrimSpace(string(data))) > 0 {
		plain, err := config.StandardJSON(data)
		if err == nil {
			if dup := config.DuplicateKey(plain); dup != "" {
				// (Read last-wins, the first of the two would be lost.)
				return fmt.Errorf("%s holds %q twice, which gnosh does not load, so it was left as it is; take one out", path, dup)
			}
			err = json.Unmarshal(plain, &doc)
		}
		if err != nil {
			return fmt.Errorf("%s is not JSON gnosh can read (%v); it was left as it is", path, err)
		}
	} else if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if err := change(doc); err != nil {
		return fmt.Errorf("%s was left as it is: %w", path, err)
	}
	content, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return err
	}
	content = append(content, '\n')
	if _, err := config.CheckWrite(l.st.inputs, path, content); err != nil {
		return fmt.Errorf("gnosh would not load %s with the change in it, so it was not written: %w", path, err)
	}
	if err := config.WriteFileAtomic(path, content, 0o600); err != nil {
		return err
	}
	// (What gnosh writes it has put in force already.)
	l.sawOwnWrite(path)
	return nil
}

// outsideTree says why a session or sub-agent with the work tree t may not change
// paths, when one of them is in the checkout the work tree was made from
// and not in the work tree; "" when none is. A relative path is read from
// cwd.
func outsideTree(t core.WorkTree, cwd string, paths []string) string {
	for _, p := range paths {
		if !filepath.IsAbs(p) {
			p = filepath.Join(cwd, p)
		}
		p = filepath.Clean(p)
		if dotGit(p) {
			return dotGitRefusal(t, p)
		}
		repo, real := checkoutOf(t, p)
		if repo == "" {
			if hardLinked(real) {
				return hardLinkRefusal(t, p)
			}
			continue
		}
		same := "Change the same file under your work tree instead."
		if s, ok := sameIn(t.Path, repo, real); ok {
			same = "The same file in your work tree is " + s + ": change that one."
		}
		return fmt.Sprintf("You work in a git work tree of your own, at %s, so that your changes stay apart from %s, and %s is in it. %s",
			t.Path, checkoutName(t, repo), p, same)
	}
	return ""
}

// hardLinkRefusal says why a session or sub-agent with the work tree t may
// not write the file p, a hard link: under another name it may be a file
// of the checkout, which the write would change too.
func hardLinkRefusal(t core.WorkTree, p string) string {
	return fmt.Sprintf("You work in a git work tree of your own, at %s, and %s is a hard link: under another name it may be a file of %s, which a write here changes too. Remove the link and write a file of its own there.", t.Path, p, checkoutName(t, resolved(t.Repo)))
}

// within reports whether the path p is dir or inside it.
func within(p, dir string) bool {
	return p == dir || strings.HasPrefix(p, strings.TrimSuffix(dir, "/")+"/")
}

// checkoutOf finds the checkout of t that the path p is in, when p is not
// in the work tree itself: the innermost of t.Checkouts that holds it, or
// "". real is p with its symbolic links followed. The checkouts are
// compared with the names macOS takes for one file folded alike
// (permission.PathKey), so that /tmp/x for /private/tmp/x, another letter
// case or /System/Volumes/Data is no way into them; the work tree is
// compared exactly, so that only its own spelling is let through.
func checkoutOf(t core.WorkTree, p string) (repo, real string) {
	real = resolved(p)
	if within(real, resolved(t.Path)) {
		return "", real
	}
	key := permission.PathKey(real)
	for _, c := range t.Checkouts() {
		c = resolved(c)
		if ck := permission.PathKey(c); within(key, ck) {
			return c, real
		}
	}
	return "", real
}

// resolved is the absolute path p with its symbolic links followed, or
// cleaned when that cannot be done.
func resolved(p string) string {
	if r, err := resolvePath(p); err == nil {
		return r
	}
	return filepath.Clean(p)
}

// sameIn is where the path real, in the checkout repo, is in the work tree
// at tree; ok is false when real spells repo another way (letter case, a
// second way into the disk) and it cannot be told letter for letter.
func sameIn(tree, repo, real string) (string, bool) {
	rel, err := filepath.Rel(repo, real)
	if err != nil || rel == ".." || strings.HasPrefix(rel, "../") {
		return "", false
	}
	return filepath.Join(tree, rel), true
}

// checkoutName says which of t's checkouts repo is, for the model: the
// person's own (the last), or a work tree yours was made from.
func checkoutName(t core.WorkTree, repo string) string {
	all := t.Checkouts()
	if resolved(all[len(all)-1]) == repo {
		return "the person's checkout at " + repo
	}
	return "the work tree at " + repo + ", which yours was made from"
}

// commandOutsideTree says why a shell command of a session with the work
// tree t may not run, when it may run in the checkout the work tree was
// made from and not in the work tree, or when where it runs cannot be
// told from its text; "" when it stays in the work tree, or elsewhere.
// Claude Code checks a command's working directory the same way. What a
// command writes into the checkout by the paths it names ("cp a /repo/b")
// is refused as well, as far as the engine can tell those paths.
func (l *live) commandOutsideTree(t core.WorkTree, cwd, command string) string {
	if strings.TrimSpace(command) == "" {
		return ""
	}
	// Told only that a cd must be plain, a model that had written mem's
	// call as a shell command added cds to it, call after call (box 1).
	hint := " Write it as plain shell commands, every cd to a folder of your work tree."
	if strings.Contains(command, "mem.") {
		hint += " (mem is no shell command: it is called with gnovm_run.)"
	}
	dirs, why := l.engine.CommandDirs(cwd, command)
	if why != "" {
		return fmt.Sprintf("You work in a git work tree of your own, at %s, and gnosh cannot tell where this command runs: %s. It could run in %s.%s", t.Path, why, checkoutName(t, resolved(t.Repo)), hint)
	}
	if l.engine.LinkAndMore(command) {
		// What the rest of the line names may lead through the link, which
		// is not there yet as gnosh reads the line.
		return fmt.Sprintf("You work in a git work tree of your own, at %s, and this command makes a link and goes on in the same line: what it names after may lead through the link into %s. Make the link in a command of its own first.", t.Path, checkoutName(t, resolved(t.Repo)))
	}
	for _, d := range dirs {
		repo, real := checkoutOf(t, d)
		if repo == "" {
			continue
		}
		same := ""
		if s, ok := sameIn(t.Path, repo, real); ok {
			same = ": the same folder there is " + s
		}
		return fmt.Sprintf("You work in a git work tree of your own, at %s, and this command would run in %s, in %s. Run it in your work tree instead%s.", t.Path, d, checkoutName(t, repo), same)
	}
	// Nor does it write into the checkout by the files it names: a
	// redirection's target, cp's last word, sed -i's files and the like,
	// as far as they can be told.
	writes, unknown := l.engine.CommandWrites(cwd, command)
	if unknown != "" {
		return fmt.Sprintf("You work in a git work tree of your own, at %s, and gnosh cannot tell where this command writes: %s. It could be in %s. Write the file's path out.", t.Path, unknown, checkoutName(t, resolved(t.Repo)))
	}
	for _, p := range writes {
		if dotGit(p) {
			return dotGitRefusal(t, p)
		}
		repo, real := checkoutOf(t, p)
		if repo == "" {
			if hardLinked(real) {
				return hardLinkRefusal(t, p)
			}
			continue
		}
		same := "under your work tree"
		if s, ok := sameIn(t.Path, repo, real); ok {
			same = "in your work tree, " + s
		}
		return fmt.Sprintf("You work in a git work tree of your own, at %s, and this command writes %s, in %s. Write the same file %s, instead.", t.Path, p, checkoutName(t, repo), same)
	}
	// Nor is git pointed there from the work tree: git -C, --git-dir,
	// --work-tree, GIT_DIR and its kin.
	places, why := l.engine.GitPlaces(cwd, command)
	if why != "" {
		return fmt.Sprintf("You work in a git work tree of your own, at %s, and gnosh cannot tell where this command points git: %s. It could be %s. Run git in your work tree as it is, with its words written out.", t.Path, why, checkoutName(t, resolved(t.Repo)))
	}
	for _, p := range places {
		if repo, _ := checkoutOf(t, p); repo != "" {
			return fmt.Sprintf("You work in a git work tree of your own, at %s, and this command points git at %s, in %s. Run git in your work tree, without -C, --git-dir, --work-tree or GIT_DIR: the work tree is a checkout of its own.", t.Path, p, checkoutName(t, repo))
		}
	}
	// Nor does its git change what the work tree shares with the checkout:
	// the branches and other refs, the git settings, the list of work
	// trees (Claude Code 2.1.326 refuses the like for its sub-agents).
	if what := l.engine.SharedGitChange(cwd, command, treeGit(t)); what != "" {
		return fmt.Sprintf("You work in a git work tree of your own, at %s, and this command may change what your work tree shares with %s: %s. The branches, tags, git settings and list of work trees are the same in both. Make your changes as commits on your own branch, and leave other branches, tags and settings to the person.", t.Path, checkoutName(t, resolved(t.Repo)), what)
	}
	return ""
}

// onThisMachine reports whether a shell call runs here: its host is left
// out, empty, or this machine's own name.
func onThisMachine(call core.ToolCall) bool {
	var fields map[string]json.RawMessage
	if json.Unmarshal(call.Input, &fields) != nil {
		return true
	}
	raw, named := fields["host"]
	if !named {
		return true
	}
	var name string
	if json.Unmarshal(raw, &name) != nil {
		return false
	}
	name = strings.TrimSpace(name)
	return name == "" || name == shelltool.LocalName
}
