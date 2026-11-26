package app

import (
	"context"
	"encoding/json"
	"strings"

	"github.com/gnosh-ai/gnosh/internal/agent"
	"github.com/gnosh-ai/gnosh/internal/buildinfo"
	"github.com/gnosh-ai/gnosh/internal/core"
	"github.com/gnosh-ai/gnosh/internal/prompt"
)

// This file is Restart, the model's /restart (Jae, 26 November 2026:
// "allow gnosh to restart itself, like when i say 'restart yourself', or
// when it thinks it needs to, but keep a root PID so gnosh can still be
// killed externally"). The call ends the turn, the session closes as
// /restart closes it, and gnosh starts again in it from the program file
// there is now. It starts again in place of this process (Options.Relaunch
// is an exec), so the process id stays the one a terminal's pane, a
// supervisor or a kill from outside holds: nothing runs beside it, and
// nothing is left behind that the id does not reach. The gnosh that
// starts is told it was the model's call (RestartAskedEnv), tells the
// model so, and goes on with the task by itself, as a session
// EnterWorktree moved does. It is an attended session's tool, offered
// where gnosh can start itself again, and never a sub-agent's.

const (
	restartToolName = "Restart"
	// RestartAskedEnv is set for the gnosh the Restart tool starts, to
	// the reason the call gave ("1" for none). As with RestartedEnv,
	// gnosh takes it out of its environment as it starts.
	RestartAskedEnv = "GNOSH_RESTART_ASKED"
	// maxRestartReason is how much of the reason is kept.
	maxRestartReason = 200
)

// restartTool is Restart.
type restartTool struct{ l *live }

func (t *restartTool) Spec() core.ToolSpec {
	return core.ToolSpec{
		Name: restartToolName,
		Description: "Starts gnosh, the program you run in, again in this session, as the user's /restart does: the conversation, the permission mode and the model carry over, and a gnosh installed since this one started is the one that runs. " +
			"Use it when the user asks you to restart yourself, or when gnosh itself must start again for the task to go on: a newer gnosh was installed, or a setting that takes effect only at a start was changed. It does not clear the conversation or stop anything else. " +
			"The call ends this turn; gnosh starts again, tells you so, and the next turn goes on with the task. It is refused while work runs in the background, which a restart would stop.",
		InputSchema: json.RawMessage(`{"type":"object","properties":{"reason":{"type":"string","description":"Why gnosh starts again, in a few words, for the user."}},"additionalProperties":false}`),
	}
}

func (t *restartTool) Describe(call core.ToolCall, env *core.ToolEnv) (core.Action, error) {
	return core.Action{Kind: core.ActionOther, Summary: "Start gnosh again in this session"}, nil
}

func (t *restartTool) Run(ctx context.Context, call core.ToolCall, env *core.ToolEnv) (core.ToolResult, error) {
	l := t.l
	var in struct {
		Reason string `json:"reason"`
	}
	if err := json.Unmarshal(call.Input, &in); err != nil {
		return core.ErrorResult("The input could not be read: " + err.Error()), nil
	}
	if why := l.cannotRestart(); why != "" {
		return core.ErrorResult(why), nil
	}
	args, err := l.goOnWith()
	if err != nil {
		return core.ErrorResult("gnosh cannot start again: " + err.Error() + "."), nil
	}
	reason := oneLine(strings.TrimSpace(in.Reason), maxRestartReason)
	p := l.p
	p.restartMu.Lock()
	p.restart = args
	p.restartAsking = firstOf(reason, "1")
	l.carryTreeLocked()
	p.restartMu.Unlock()
	said := "gnosh starts again in this session, at the model's call"
	if reason != "" {
		said += " (" + reason + ")"
	}
	if p.newBuild() {
		said += ": the newer gnosh installed since this one started is the one that runs"
	}
	l.restartWhy.Store(ptrTo(said + "." + l.loopEndsNote()))
	res := core.TextResult("gnosh starts again in this session as this turn ends. The conversation, the permission mode and the model carry over; once gnosh has started again you are told so, and the next turn goes on with the task by itself.")
	res.Meta = map[string]any{agent.MetaHalt: "gnosh starts again", agent.MetaPlanned: true}
	return res, nil
}

// cannotRestart says why the Restart tool cannot start gnosh again now:
// "" when it can.
func (l *live) cannotRestart() string {
	if l.p.o.Relaunch == nil {
		return "This gnosh cannot start itself again."
	}
	if l.restartedByModel.Load() {
		// A model that restarts at every start would never stop.
		return "gnosh started again at your Restart call already, and the user has said nothing since: it is not started again until they have."
	}
	if work := l.runningWork(); len(work) > 0 {
		return "This runs in the background and would be stopped by the restart: " + strings.Join(work, ", ") + ". Wait for it to end, or stop it, first."
	}
	if n := l.agent.State().Queued; n > 0 {
		return "The user has sent more that waits to be read, which the restart would lose: read it first, and restart after."
	}
	return ""
}

// restartedAtCall tells the model, in a gnosh the Restart tool started,
// that gnosh started again at its call and which build runs now, and
// starts the turn that goes on with the task.
func (l *live) restartedAtCall() {
	p := l.p
	if !p.restartCalled || l.ts == nil || l.agent == nil {
		return
	}
	// Once: a session /clear begins later in this gnosh was not restarted.
	p.restartCalled = false
	l.restartedByModel.Store(true)
	text := "gnosh has started again in this session, at your Restart call"
	if p.restartReason != "" {
		text += " (" + p.restartReason + ")"
	}
	text += ". It runs the build " + firstOf(buildinfo.BuildID, "devel")
	if from := p.restartedFrom; from != "" && !strings.HasPrefix(buildinfo.BuildID, from) && !strings.HasPrefix(from, buildinfo.BuildID) {
		text += ", which took over from " + from
	} else if from != "" {
		text += ", the same build as before"
	}
	text += ". The conversation, the permission mode and the model carried over."
	_ = l.agent.Tell(agent.Note{Source: "restart", Text: prompt.Wrap(text)})
	if _, err := l.agent.Submit(agent.Prompt{
		Text:   "Go on with the task where you left off.",
		Hidden: true,
		Own:    true,
		Source: "restart",
		Notice: "gnosh started again; the model goes on.",
	}); err != nil {
		l.flog.logf("session", "the turn after the restart could not start: %v", err)
	}
}
