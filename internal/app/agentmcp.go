package app

import (
	"encoding/json"
	"fmt"
	"slices"
	"strings"

	"github.com/gnosh-ai/gnosh/internal/agent"
	"github.com/gnosh-ai/gnosh/internal/app/agentdef"
	"github.com/gnosh-ai/gnosh/internal/mcp"
	"github.com/gnosh-ai/gnosh/internal/permission"
)

// This file gives an agent the MCP servers its definition names
// (mcpServers), as Claude Code does. An entry that names a server the
// session has uses the session's connection; a server written in the
// definition itself is the agent's own: a sub-agent's is connected when it
// starts and closed when it ends, and the parent conversation never sees
// its tools; the session's own agent (--agent) has its servers connected at
// startup with the files'. A project's definitions are read only in a
// folder the person has trusted (agentDefs), and so are the servers in
// them. The tools and disallowedTools of the definition take the servers'
// tools as they take the others: mcp__github__* in tools gives every tool
// of github, and mcp__github, mcp__github__* or mcp__* in disallowedTools
// takes them away.

// agentMCPConfigs reads the servers the definition writes itself, and says
// which of the entries name a server the session lacks.
func (l *live) agentMCPConfigs(def agentdef.Def, have []mcp.Config) ([]mcp.Config, []mcp.Problem) {
	own := map[string]json.RawMessage{}
	var probs []mcp.Problem
	where := firstOf(def.Source, "the agent "+def.Name)
	for _, m := range def.MCPServers {
		if m.Inline != nil {
			own[m.Name] = m.Inline
			continue
		}
		known := false
		for _, c := range have {
			known = known || c.Name == m.Name
		}
		if !known {
			probs = append(probs, mcp.Problem{Path: where, Server: m.Name,
				Message: fmt.Sprintf("the agent %s names it, and this session has no MCP server of that name, so the agent goes without it", def.Name)})
		}
	}
	if len(own) == 0 {
		return nil, probs
	}
	if l.inv.StrictMCPConfig || l.cfg.Switches.SafeMode.V {
		if def.Scope != agentdef.ScopeCommandLine {
			// (--agents is the person's word for this run, as --mcp-config
			// is: what a file writes is left out.)
			probs = append(probs, mcp.Problem{Path: where,
				Message: fmt.Sprintf("the agent %s writes MCP servers of its own, which --strict-mcp-config (or safe mode) leaves out, so the agent goes without them", def.Name)})
			return nil, probs
		}
	}
	cfgs, wrong := mcp.ParseServers(where, "agent", own, l.mcpSources())
	return cfgs, append(probs, wrong...)
}

// connectAgentMCP connects the servers a sub-agent's definition writes
// itself, and says what went wrong with any of them; a server that does
// not connect leaves the sub-agent without its tools, as a session's does.
func (l *live) connectAgentMCP(s *subSession) {
	if l.mcp == nil || len(s.def.MCPServers) == 0 {
		return
	}
	l.mcp.mu.Lock()
	var have []mcp.Config
	for _, srv := range l.mcp.servers {
		have = append(have, srv.cfg)
	}
	l.mcp.mu.Unlock()
	cfgs, probs := l.agentMCPConfigs(s.def, have)
	for _, p := range probs {
		l.notice(agent.LevelWarning, "mcp", p.String())
	}
	for _, c := range cfgs {
		if slices.ContainsFunc(have, func(h mcp.Config) bool { return h.Name == c.Name }) {
			// Its tools' names would be the session's server's: the
			// sub-agent has the session's, and this one is not started.
			l.notice(agent.LevelWarning, "mcp", fmt.Sprintf("The sub-agent %s writes an MCP server of its own named %s, the name of one of this session's, so it is not started: the session's %s is the one it has.", s.def.Name, c.Name, c.Name))
			continue
		}
		// It is the sub-agent's before it connects, so that a sub-agent
		// that ends meanwhile closes it (closeAgentMCP), and the
		// connection is then not taken.
		srv := &mcpServer{cfg: c, status: "pending", own: s.def.Name}
		l.subMu.Lock()
		s.ownMCP = append(s.ownMCP, srv)
		l.subMu.Unlock()
		l.connectMCP(srv, 0)
	}
}

// runningAgentServers are the own servers of the sub-agents that run now,
// for /mcp.
func (l *live) runningAgentServers() []*mcpServer {
	l.subMu.Lock()
	defer l.subMu.Unlock()
	var out []*mcpServer
	for s := range l.subs {
		out = append(out, s.ownMCP...)
	}
	slices.SortFunc(out, func(a, b *mcpServer) int { return strings.Compare(a.own+"/"+a.cfg.Name, b.own+"/"+b.cfg.Name) })
	return out
}

// closeAgentMCP closes the servers of a sub-agent's own. (The list is
// taken under subMu, which /mcp reads it under; the two locks are never
// held together.)
func (l *live) closeAgentMCP(s *subSession) {
	l.subMu.Lock()
	servers := s.ownMCP
	s.ownMCP = nil
	l.subMu.Unlock()
	for _, srv := range servers {
		l.mcp.mu.Lock()
		srv.gen++
		c := srv.client
		srv.client, srv.tools, srv.status = nil, nil, "disabled"
		l.mcp.mu.Unlock()
		if c != nil {
			_ = c.Close()
		}
	}
}

// agentMCPTools are the tools of a sub-agent's own servers that the
// session's rules leave on.
func (l *live) agentMCPTools(s *subSession) []*mcpTool {
	if len(s.ownMCP) == 0 {
		return nil
	}
	l.mcp.mu.Lock()
	defer l.mcp.mu.Unlock()
	var out []*mcpTool
	for _, srv := range s.ownMCP {
		for _, t := range srv.tools {
			if l.engine.ToolEnabled(t.name) {
				out = append(out, t)
			}
		}
	}
	return out
}

// toolListed reports whether a definition's list of tools holds the tool
// of that name: by its name, or for an MCP tool by its server
// (mcp__github__*), and, in a list of tools to take away (away), also as
// mcp__github or mcp__*.
func toolListed(list []string, name string, away bool) bool {
	for _, entry := range list {
		switch {
		case permission.CanonicalTool(entry) == name:
			// (Claude Code's old names count: Task is Agent, KillShell
			// is TaskStop.)
			return true
		case !strings.HasPrefix(entry, "mcp__") || !strings.HasPrefix(name, "mcp__"):
		case strings.HasSuffix(entry, "__*") && strings.HasPrefix(name, strings.TrimSuffix(entry, "*")):
			return true
		case away && entry == "mcp__*":
			return true
		case away && !strings.Contains(strings.TrimPrefix(entry, "mcp__"), "__") && strings.HasPrefix(name, entry+"__"):
			return true
		}
	}
	return false
}
