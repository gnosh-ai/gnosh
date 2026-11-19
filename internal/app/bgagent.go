package app

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync/atomic"
	"time"

	"github.com/gnosh-ai/gnosh/internal/agent"
	"github.com/gnosh-ai/gnosh/internal/core"
	"github.com/gnosh-ai/gnosh/internal/prompt"
	"github.com/gnosh-ai/gnosh/internal/tools/shelltool"
)

// This file runs sub-agents in the background (spec/cc/subagents.md
// section 7). The Agent call returns at once with the sub-agent's id and
// its session file; the sub-agent works on beside the session, and its
// report reaches the model as a notice from gnosh when it ends, through
// the loop's one way in for such news (agent.Tell): a running turn reads
// it before its next request, and an idle session starts a turn for it.
// The person sees its questions as they come, naming it, lists the ones
// running with /tasks and stops one there; the model stops one with
// TaskStop.
//
// Only the session's own agent starts one, and only in a session a person
// attends: a sub-agent waits for the agents it starts, and in a run (-p)
// nothing would read a notice that came after its turn, so there the call
// waits too, and says so. A background sub-agent has neither the Agent
// tool nor anyone to put a question of its own to.

// bgAgent is a sub-agent running in the background.
type bgAgent struct {
	id, desc, kind, output string
	started                time.Time
	cancel                 context.CancelFunc
	done                   chan struct{}
	// stopped says the person or the model stopped it.
	stopped atomic.Bool
}

// backgroundOff says why sub-agents may not run in the background in this
// session, or "". A run (-p) may have them: it waits for them before it
// ends, and the end of each wakes the model (printwait.go).
func (l *live) backgroundOff(owner *subSession) string {
	switch {
	case owner != nil:
		return "a sub-agent waits for the sub-agents it starts"
	case l.p.env["CLAUDE_CODE_DISABLE_BACKGROUND_TASKS"] == "1":
		return "CLAUDE_CODE_DISABLE_BACKGROUND_TASKS is set"
	case l.inv.Bare:
		return "minimal mode (--bare) has no background work"
	}
	return ""
}

// backgroundAgent returns the sub-agent with this id that runs in the
// background, if one does.
func (l *live) backgroundAgent(id string) (*bgAgent, bool) {
	l.bgMu.Lock()
	defer l.bgMu.Unlock()
	for _, b := range l.bgAgents {
		if b.id == id {
			return b, true
		}
	}
	return nil, false
}

// startBackground runs a sub-agent that is ready to start in the
// background, and returns what the Agent call returns: that it started.
func (l *live) startBackground(s *subSession, in agentInput) core.ToolResult {
	ctx, cancel := context.WithCancel(l.ctx)
	b := &bgAgent{id: s.id, desc: firstOf(strings.TrimSpace(in.Description), "a task"), kind: s.def.Name,
		started: l.p.now(), cancel: cancel, done: make(chan struct{})}
	if s.ts != nil {
		b.output = s.ts.Path()
	}
	l.bgMu.Lock()
	l.bgAgents = append(l.bgAgents, b)
	l.bgMu.Unlock()
	l.noteBackground()
	l.bgWG.Add(1)
	go func() {
		defer l.bgWG.Done()
		defer close(b.done)
		defer cancel()
		// (Closed below before its report goes out; this one is for a run
		// that never gets there. Closing is done once.)
		defer s.close()
		res, err := s.agent.Run(ctx, agent.Prompt{Text: in.Prompt})
		var rep core.ToolResult
		var caution string
		if err != nil {
			rep = core.ErrorResult("The sub-agent could not be given its task: " + strings.TrimRight(err.Error(), ".") + ".")
		} else {
			rep, caution = s.report(res, l.p.now().Sub(b.started))
		}
		s.close()
		l.bgMu.Lock()
		for i, x := range l.bgAgents {
			if x == b {
				l.bgAgents = append(l.bgAgents[:i], l.bgAgents[i+1:]...)
				break
			}
		}
		l.bgMu.Unlock()
		l.noteBackground()
		l.backgroundAgentEnded(b, rep, caution, err)
	}()
	l.flog.logf("turn", "sub-agent %s (%s) started in the background: %s", shortID(s.id), s.def.Name, b.desc)
	out := core.TextResult(fmt.Sprintf("The sub-agent is working in the background on: %s. Its id is %s. Its report will come to you as a notice from gnosh when it ends; until then it is still running, so do not report what it found before that notice arrives. Go on with other work meanwhile, or end your turn to wait. TaskStop with its id stops it.", b.desc, b.id))
	out.Meta = map[string]any{"status": "async_launched", "agentId": b.id, "agentType": b.kind, "outputFile": b.output}
	return out
}

// backgroundAgentEnded tells the model that a background sub-agent ended,
// with its report, and the caution report made of it, if any.
func (l *live) backgroundAgentEnded(b *bgAgent, rep core.ToolResult, caution string, runErr error) {
	if l.agent == nil || l.closing.Load() || l.bgUntold.Load() {
		return
	}
	end := prompt.AgentEnd{ID: b.id, Description: b.desc, Output: b.output, Report: prompt.Defuse(core.TextOf(rep.Content)), Caution: caution}
	status, _ := rep.Meta["status"].(string)
	verb := "finished"
	switch {
	case b.stopped.Load() || status == "stopped":
		end.State, verb = prompt.AgentStopped, "was stopped"
		if b.stopped.Load() {
			end.Report, end.Caution = "", ""
		}
	case runErr != nil || rep.IsError || status == "failed" || status == "partial" || status == "halted":
		end.State, verb = prompt.AgentFailed, "stopped before it finished"
	}
	_ = l.agent.Tell(agent.Note{
		Text:   l.texts.AgentEnded(end),
		Source: "task-notification",
		Notice: fmt.Sprintf("A background sub-agent %s: %s", verb, b.desc),
		Wake:   true,
	})
}

// backgroundAgents lists the sub-agents running in the background, in the
// order they started.
func (l *live) backgroundAgents() []*bgAgent {
	l.bgMu.Lock()
	defer l.bgMu.Unlock()
	return append([]*bgAgent(nil), l.bgAgents...)
}

// findBackgroundAgent finds a running background sub-agent by its id, or
// by six or more characters at its start or end that no other one has:
// ids that start close together in time begin alike.
func (l *live) findBackgroundAgent(id string) *bgAgent {
	id = strings.TrimSpace(id)
	if id == "" {
		return nil
	}
	var found []*bgAgent
	for _, b := range l.backgroundAgents() {
		if b.id == id {
			return b
		}
		if len(id) >= 6 && (strings.HasPrefix(b.id, id) || strings.HasSuffix(b.id, id)) {
			found = append(found, b)
		}
	}
	if len(found) == 1 {
		return found[0]
	}
	return nil
}

// stopBackgroundAgent stops one, and waits a little for it to end.
func (l *live) stopBackgroundAgent(b *bgAgent) {
	b.stopped.Store(true)
	b.cancel()
	select {
	case <-b.done:
	case <-time.After(10 * time.Second):
	}
}

// stopBackgroundAgents stops every one, as the session closes, and waits
// for them as long as wait.
func (l *live) stopBackgroundAgents(wait time.Duration) {
	for _, b := range l.backgroundAgents() {
		b.cancel()
	}
	waitGroup(&l.bgWG, wait)
}

// stopAgentTool is TaskStop: the shell's, which stops a background
// command, and which here also stops a background sub-agent by its id.
type stopAgentTool struct {
	l     *live
	shell core.Tool
}

func (t *stopAgentTool) Spec() core.ToolSpec {
	s := t.shell.Spec()
	s.Description = strings.TrimSpace(s.Description) + " It stops a sub-agent running in the background too, by the id its Agent call returned."
	return s
}

// agentOf finds the background sub-agent a call names, if it names one.
func (t *stopAgentTool) agentOf(call core.ToolCall) *bgAgent {
	var in struct {
		TaskID  string `json:"task_id"`
		ShellID string `json:"shell_id"`
	}
	if json.Unmarshal(call.Input, &in) != nil {
		return nil
	}
	return t.l.findBackgroundAgent(firstOf(in.TaskID, in.ShellID))
}

func (t *stopAgentTool) Describe(call core.ToolCall, env *core.ToolEnv) (core.Action, error) {
	if b := t.agentOf(call); b != nil {
		return core.Action{Kind: core.ActionOther, Summary: "Stop background sub-agent " + b.id + ": " + b.desc}, nil
	}
	if d, ok := t.shell.(core.Describer); ok {
		return d.Describe(call, env)
	}
	return core.Action{Kind: core.ActionOther}, nil
}

func (t *stopAgentTool) Run(ctx context.Context, call core.ToolCall, env *core.ToolEnv) (core.ToolResult, error) {
	sub := env != nil && env.ParentSessionID != ""
	if b := t.agentOf(call); b != nil {
		if sub {
			// A sub-agent starts none in the background, so this one is
			// not its to stop.
			return core.ErrorResult(fmt.Sprintf("%s is a sub-agent the session runs in the background, and a sub-agent stops only what it started itself.", b.id)), nil
		}
		t.l.stopBackgroundAgent(b)
		return core.TextResult(fmt.Sprintf("The background sub-agent %s is stopped: %s. Its session file is %s.", b.id, b.desc, b.output)), nil
	}
	if sub && t.l.shell != nil {
		// A sub-agent stops its own background commands, and no other's.
		var in struct {
			TaskID  string `json:"task_id"`
			ShellID string `json:"shell_id"`
		}
		_ = json.Unmarshal(call.Input, &in)
		id := firstOf(in.TaskID, in.ShellID)
		for _, task := range t.l.shell.Tasks() {
			if task.ID == id && task.Owner != env.SessionID {
				return core.ErrorResult(fmt.Sprintf("The background command %s was not started by this sub-agent, and a sub-agent stops only what it started itself.", id)), nil
			}
		}
	}
	return t.shell.Run(ctx, call, env)
}

// wrapStop puts the TaskStop that also stops sub-agents in the place of the
// shell's.
func (l *live) wrapStop(tools []core.Tool) []core.Tool {
	for i, t := range tools {
		if t.Spec().Name == shelltool.StopName {
			tools[i] = &stopAgentTool{l: l, shell: t}
		}
	}
	return tools
}
