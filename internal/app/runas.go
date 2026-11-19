package app

import (
	"context"
	"fmt"
	"slices"
	"strings"

	"github.com/gnosh-ai/gnosh/internal/agent"
	"github.com/gnosh-ai/gnosh/internal/config"
	"github.com/gnosh-ai/gnosh/internal/permission"
	"github.com/gnosh-ai/gnosh/internal/transcript"
)

// This file is a session that runs as a kind of agent (spec/cc/
// subagents.md 4): --agent, or the setting "agent", names one of the
// kinds the Agent tool knows, and the whole session is that agent. Its
// prompt stands in place of gnosh's own system prompt (unless
// --system-prompt is given), the tools it does not have are off, as a
// deny rule of a tool's bare name puts one, and its model is the
// session's (unless -m names one), at the thinking level it asks for
// (unless --effort names one). The skills it preloads come with the first
// prompt, after the instruction files, which still load, and a new
// session starts with the kind's initialPrompt as its first turn. The
// session's file records the agent, and a resumed session runs as it
// again unless --agent names another. What a definition asks for that a
// session cannot be (hooks, skills, MCP servers, isolation, memory) stops
// the start, with the reason: it is never run as something else.

// metaAgent is the key of the session file's record of the agent the
// session runs as.
const metaAgent = "agent"

// runAsAgent makes the session the agent it is to run as, if any: its
// prompt and its tools. The model comes after the models are set up
// (agentModel).
func (l *live) runAsAgent() error {
	name, from := l.inv.Agent, "--agent"
	if name == "" && l.source != "startup" {
		if stored := l.storedAgent(); stored != "" {
			name, from = stored, "the session it resumes"
		}
	}
	if name == "" && l.cfg.Agent.Set {
		name, from = l.cfg.Agent.V, "the setting agent in "+l.tilde(l.cfg.Agent.Origin.String())
	}
	if name == "" {
		return nil
	}
	defs := l.agentDefs()
	def, ok := defs.Get(name)
	if !ok {
		return fmt.Errorf("%s names the agent %q, and there is no kind of agent of that name. The kinds are: %s. Nothing was started", from, name, strings.Join(defs.Names(), ", "))
	}
	if len(def.Other) > 0 {
		return fmt.Errorf("the agent %s (%s) asks for %s, which a session cannot have yet, so it is not run as something else. Nothing was started", def.Name, firstOf(def.Source, def.Scope), strings.Join(def.Other, ", "))
	}
	if _, why := l.skillsToPreload(def); why != "" {
		return fmt.Errorf("%s. Nothing was started", why)
	}
	if def.Isolation != "" && !(def.Isolation == "worktree" && l.inv.Worktree) {
		// A session has a work tree of its own only by --worktree.
		return fmt.Errorf("the agent %s (%s) asks for isolation %s, a work tree of its own, which a session has with --worktree: start it with --worktree as well. Nothing was started", def.Name, firstOf(def.Source, def.Scope), def.Isolation)
	}
	if l.inv.SystemPrompt == "" && l.inv.SystemPromptFile == "" && strings.TrimSpace(def.Prompt) != "" {
		l.inv.SystemPrompt = def.Prompt
	}
	var deny []string
	if def.ToolsSet {
		for _, tool := range l.p.toolNames() {
			// (A tool listed by Claude Code's old name, Task for Agent,
			// is listed.)
			if !slices.ContainsFunc(def.Tools, func(t string) bool { return permission.CanonicalTool(t) == tool }) {
				deny = append(deny, tool)
			}
		}
		if !slices.ContainsFunc(def.Tools, func(t string) bool { return strings.HasPrefix(t, "mcp__") }) {
			deny = append(deny, "mcp__*")
		}
	}
	deny = append(deny, def.Disallowed...)
	if len(deny) > 0 {
		err := l.engine.AddLayer(permission.Layer{
			Scope: permission.ScopeCLI, Name: "the agent " + def.Name + " (" + from + ")",
			FromProject: def.Scope == "project", Deny: deny,
		})
		if err != nil {
			return fmt.Errorf("the tools of the agent %s cannot be put in force (%v). Nothing was started", def.Name, err)
		}
	}
	if def.ToolsSet {
		// Agent(worker, researcher) among its tools: only those kinds.
		l.agentKinds = def.AgentTypes
	}
	l.asAgent = &def
	return nil
}

// agentModel makes the model of the agent the session runs as the
// session's, where it names one and -m does not.
func (l *live) agentModel(ctx context.Context) error {
	def := l.asAgent
	if def == nil || def.Model == "" || def.Model == "inherit" || l.inv.Model != "" {
		return nil
	}
	ref := config.ModelRef{ID: def.Model}
	if prov, id, ok := strings.Cut(def.Model, "/"); ok {
		if _, known := l.cfg.Provider(prov); known {
			ref = config.ModelRef{Provider: prov, ID: id}
		}
	}
	ref, err := l.resolveRef(ctx, ref, true, nil)
	if err == nil {
		var mu *modelUse
		if mu, err = l.buildModel(ctx, ref, l.cfg.Large, true); err == nil {
			l.modelMu.Lock()
			l.large = mu
			l.modelMu.Unlock()
			return nil
		}
	}
	return fmt.Errorf("the agent %s asks for the model %s, which cannot be used (%v). Nothing was started", def.Name, def.Model, err)
}

// storedAgent is the agent the session's file says it ran as, or "".
func (l *live) storedAgent() string {
	if l.ts == nil {
		return ""
	}
	name := ""
	for _, e := range l.ts.Entries() {
		if e.Type == transcript.TypeMeta && e.Key == metaAgent {
			name = e.Value
		}
	}
	return name
}

// agentName is the name of the agent the session runs as, or "".
func (l *live) agentName() string {
	if l.asAgent == nil {
		return ""
	}
	return l.asAgent.Name
}

// startInitialPrompt sends the prompt the kind the session runs as starts
// with (initialPrompt) as the first turn of a new session, ahead of
// anything the person types or the command line gives. A resumed session,
// and one that /clear began, do not start with it again.
func (l *live) startInitialPrompt() {
	if l.asAgent == nil || l.asAgent.InitialPrompt == "" || l.source != "startup" || l.agent == nil {
		return
	}
	if _, err := l.agent.Submit(agent.Prompt{Text: l.asAgent.InitialPrompt, Source: "initialPrompt"}); err != nil {
		l.notice(agent.LevelWarning, "agent", fmt.Sprintf("The prompt the agent %s starts with could not be sent: %v.", l.asAgent.Name, err))
	}
}
