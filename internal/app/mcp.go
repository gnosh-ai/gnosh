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
	"sort"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/gnosh-ai/gnosh/internal/agent"
	"github.com/gnosh-ai/gnosh/internal/core"
	"github.com/gnosh-ai/gnosh/internal/hooks"
	"github.com/gnosh-ai/gnosh/internal/mcp"
	"github.com/gnosh-ai/gnosh/internal/prompt"
)

// This file joins MCP servers to a session (spec/cc/mcp.md): the servers
// mcp.LoadConfig finds (the user's mcp.json in gnosh's config folder, and in
// a trusted folder the project's .mcp.json) are started or, at a URL,
// reached; their tools are offered to the model as mcp__<server>__<tool>,
// and each call is the server's. The permission engine knows every mcp__
// name as a tool that asks unless a rule allows it (mcp__server,
// mcp__server__*, mcp__server__tool).
//
// In an interactive session the servers connect in the background, and
// their tools are added as each one is ready; a run (-p) waits for them,
// as long as a server may take to start, before its turn. A sub-agent is
// given the tools of the servers connected when it starts, as far as its
// kind's tools and disallowedTools let it have them.

// mcpMaxChars is the most of a tool's result the model is handed, as
// characters; MAX_MCP_OUTPUT_TOKENS may make it less. A longer result is
// written to a file beside the session, which the model may read without
// a question, and the model is told where, with the result's beginning.
const mcpMaxChars = 50_000

// mcpGlimpse is how much of a result that went to a file the model is
// shown, and mcpLargeChars the size from which a result is noted in the
// log as large (about 10,000 tokens).
const (
	mcpGlimpse    = 2000
	mcpLargeChars = 40_000
)

// mcpDescriptionMax is the most of a tool's description the model is sent.
const mcpDescriptionMax = 4096

// mcpServer is one configured server as the session holds it.
type mcpServer struct {
	cfg    mcp.Config
	status string // "pending", "connected", "failed" or "disabled"
	err    string
	client *mcp.Client
	tools  []*mcpTool
	// prompts are its prompts, read when it connected (mcpprompts.go).
	prompts []mcp.Prompt
	since   time.Time
	// gen counts what /mcp has done to the server: a connection made for
	// an earlier one is let go of when it is ready.
	gen int
	// refresh is held while the server's tool list is read again, and
	// redial while a dropped connection is made again.
	refresh, redial sync.Mutex
	// www is what a server that wants a sign-in said in its
	// WWW-Authenticate (mcpauth.go).
	www string
	// own is the kind of sub-agent whose own server this is (agentmcp.go),
	// or empty for one of the session's.
	own string
	// calls are the contexts of its tools' calls in flight, by a mark of
	// each, under callsMu: what the server asks while they run is about
	// them (mcpelicit.go).
	callsMu sync.Mutex
	calls   map[*byte]context.Context
}

// calling notes a call of one of the server's tools, made in ctx, until
// done is called.
func (s *mcpServer) calling(ctx context.Context) (done func()) {
	mark := new(byte)
	s.callsMu.Lock()
	if s.calls == nil {
		s.calls = map[*byte]context.Context{}
	}
	s.calls[mark] = ctx
	s.callsMu.Unlock()
	return func() {
		s.callsMu.Lock()
		delete(s.calls, mark)
		s.callsMu.Unlock()
	}
}

// inFlight is the contexts of the server's calls that run now.
func (s *mcpServer) inFlight() []context.Context {
	s.callsMu.Lock()
	defer s.callsMu.Unlock()
	out := make([]context.Context, 0, len(s.calls))
	for _, c := range s.calls {
		out = append(out, c)
	}
	return out
}

// mcpSet is the session's servers.
type mcpSet struct {
	mu      sync.Mutex
	servers []*mcpServer
	probs   []mcp.Problem
	// logins are the sign-ins to servers at a URL, and flows the sign-ins
	// under way, by server name, under mu (mcpauth.go).
	loginsOnce sync.Once
	logins     *mcp.LoginStore
	flows      map[string]*mcpFlow
}

// loadMCP reads which servers there are: the files', and those of the
// agent the session runs as (agentmcp.go).
func (l *live) loadMCP() *mcpSet {
	// Servers from the command line are the person's word for this run:
	// with --strict-mcp-config they are the only ones, and in safe mode
	// they are all there is too, since safe mode turns off what files set
	// up and not what was typed.
	src := l.mcpSources()
	src.User = filepath.Join(l.cfg.Paths.ConfigDir, "mcp.json")
	src.Project = filepath.Join(l.cfg.Paths.ProjectRoot, ".mcp.json")
	src.Trusted = l.st.trusted
	src.Given, src.GivenFrom = l.inv.MCPConfigs, l.p.o.Cwd
	src.Strict = l.inv.StrictMCPConfig || l.cfg.Switches.SafeMode.V
	cfgs, probs := mcp.LoadConfig(src)
	if l.asAgent != nil {
		own, wrong := l.agentMCPConfigs(*l.asAgent, cfgs)
		probs = append(probs, wrong...)
		for _, c := range own {
			cfgs = slices.DeleteFunc(cfgs, func(have mcp.Config) bool { return have.Name == c.Name })
			cfgs = append(cfgs, c)
		}
	}
	set := &mcpSet{probs: probs}
	for _, c := range cfgs {
		set.servers = append(set.servers, &mcpServer{cfg: c, status: "pending"})
	}
	return set
}

// mcpSources is what reading servers needs of the session: the
// environment for ${VAR}, the project's root, and the variables gnosh's
// own model keys came from, which a server at a URL is not sent either.
func (l *live) mcpSources() mcp.Sources {
	getenv := func(name string) (string, bool) {
		for i := len(l.p.o.Environ) - 1; i >= 0; i-- {
			if k, v, ok := strings.Cut(l.p.o.Environ[i], "="); ok && k == name {
				return v, true
			}
		}
		return "", false
	}
	var keys []string
	for _, p := range l.cfg.Providers {
		if v, ok := strings.CutPrefix(p.KeyFrom, "$"); ok {
			keys = append(keys, v)
		}
	}
	return mcp.Sources{Root: l.cfg.Paths.ProjectRoot, Getenv: getenv, Withheld: keys}
}

// startMCP connects the session's servers: in the background, or, in a
// run, before it returns.
func (l *live) startMCP() {
	if l.mcp == nil {
		return
	}
	for _, p := range l.mcp.probs {
		l.notice(agent.LevelWarning, "mcp", p.String())
	}
	var wg sync.WaitGroup
	for _, s := range l.mcp.servers {
		l.mcp.mu.Lock()
		gen := s.gen
		l.mcp.mu.Unlock()
		wg.Add(1)
		l.bg.Add(1)
		go func() {
			defer l.bg.Done()
			defer wg.Done()
			l.connectMCP(s, gen)
		}()
	}
	if !l.interactive {
		wg.Wait()
	}
}

// connectMCP starts one server and offers its tools. gen is the server's
// generation as the caller marked it pending, under the lock: a disable,
// a reconnect or the end of a sub-agent since then makes another, and this
// connection is then not taken (closing an old connection first can take
// seconds, and the generation read here would be the newer one's).
func (l *live) connectMCP(s *mcpServer, gen int) {
	l.mcp.mu.Lock()
	stale := s.gen != gen || s.status == "disabled"
	l.mcp.mu.Unlock()
	if stale {
		return
	}
	fail := func(why string) {
		l.mcp.mu.Lock()
		if s.gen != gen {
			l.mcp.mu.Unlock()
			return
		}
		s.status, s.err = "failed", why
		l.mcp.mu.Unlock()
		l.flog.logf("mcp", "the MCP server %s did not connect: %s", s.cfg.Name, why)
		if s.own != "" {
			l.notice(agent.LevelWarning, "mcp", fmt.Sprintf("The MCP server %s of the sub-agent %s is not connected: %s. The sub-agent goes without it.", s.cfg.Name, s.own, oneLine(why, 300)))
			return
		}
		l.notice(agent.LevelWarning, "mcp", fmt.Sprintf("The MCP server %s is not connected: %s (/mcp says more)", s.cfg.Name, oneLine(why, 300)))
	}
	timeout := l.cfg.Limits.MCPTimeout.V
	ctx, cancel := context.WithTimeout(l.ctx, timeout)
	defer cancel()
	opt := mcp.Options{ClientName: "gnosh", ClientVersion: firstOf(l.p.o.BuildID, "devel"), StartTimeout: timeout}
	if l.channelNamed(s.cfg.Name) && s.cfg.Type != "http" && s.own == "" {
		// A channel's events (the client hears only a server that declared
		// itself one). A channel runs on this machine: one reached at a URL
		// is not heard (channelsNotice says so).
		opt.OnChannel = func(content string, meta map[string]string) {
			l.channelEvent(s.cfg.Name, content, meta)
		}
		opt.OnChannelVerdict = func(id, behavior string) {
			l.channelVerdict(s.cfg.Name, id, behavior)
		}
		// What the channel is, and its instructions, before its first
		// event (the client takes none until this has been told).
		opt.OnHandshake = func(info mcp.Info) {
			l.channelConnected(s.cfg.Name, info.Channel, info.Instructions)
		}
	}
	// A server that says its tools changed has them read again, once it
	// is connected.
	opt.OnToolsChanged = func() {
		l.mcp.mu.Lock()
		c := s.client
		l.mcp.mu.Unlock()
		if c != nil {
			l.refreshMCP(s, c)
		}
	}
	opt.Auth = l.mcpAuthorizer(s)
	if l.interactive || l.hooks.has(hooks.Elicitation) {
		// (A run has nobody to ask, and declares no elicitation unless a
		// hook may answer.)
		opt.OnElicit = l.mcpElicit(s)
	}
	client, err := mcpConnect(ctx, s.cfg, l.cfg.Paths.ProjectRoot, l.p.o.Environ, l.roots, opt)
	if _, ok := errNeedsAuth(err); ok && s.own != "" {
		// (A sign-in is kept for the session's servers, by name: /mcp
		// login cannot see a sub-agent's own.)
		fail("it wants a sign-in, which only a server of the session's can have: add it to mcp.json, sign in with /mcp login, and name it in the agent's mcpServers")
		return
	}
	if na, ok := errNeedsAuth(err); ok {
		l.mcpNeedsAuth(s, gen, na)
		return
	}
	if err != nil {
		fail(err.Error())
		return
	}
	tools, err := client.ListTools(ctx)
	if na, ok := errNeedsAuth(err); ok {
		_ = client.Close()
		if s.own != "" {
			fail("it wants a sign-in, which only a server of the session's can have: add it to mcp.json, sign in with /mcp login, and name it in the agent's mcpServers")
			return
		}
		l.mcpNeedsAuth(s, gen, na)
		return
	}
	if err != nil {
		_ = client.Close()
		fail("it connected, and its tools could not be listed: " + err.Error())
		return
	}
	made := l.mcpToolsOf(s, tools)
	var prompts []mcp.Prompt
	if client.Info().Prompts {
		// Its prompts, which are typed as commands. A list that cannot be
		// read leaves the server's tools as they are.
		if prompts, err = client.ListPrompts(ctx); err != nil {
			l.flog.logf("mcp", "the prompts of the MCP server %s could not be listed: %v", s.cfg.Name, err)
		}
	}
	l.mcp.mu.Lock()
	if l.closing.Load() || s.gen != gen || s.status == "disabled" || s.client != nil {
		l.mcp.mu.Unlock()
		_ = client.Close()
		return
	}
	s.client, s.tools, s.prompts, s.since = client, made, prompts, l.p.now()
	l.mcp.mu.Unlock()
	// Its tools are offered before it is said to be connected, so that a
	// prompt typed once /mcp says so finds them (a CI run of 19 November
	// typed one between the two, and the model's call of the tool found
	// none of that name).
	l.offerMCPTools()
	l.mcp.mu.Lock()
	connected := s.client == client && s.gen == gen && s.status != "disabled"
	if connected {
		s.status = "connected"
	}
	l.mcp.mu.Unlock()
	if !connected {
		return
	}
	l.flog.logf("mcp", "the MCP server %s is connected (%d tools)", s.cfg.Name, len(made))
	if info := client.Info(); l.channelNamed(s.cfg.Name) && s.cfg.Type != "http" && info.Channel && info.ChannelPermission {
		l.channelRelays(s.cfg.Name)
	}
	// It may have said its tools changed while they were being listed.
	l.refreshMCP(s, client)
}

// redialMCP connects again to a server whose connection old dropped, and
// returns the new connection, or nil when it could not be made. Calls that
// find the same drop at once make one new connection between them.
func (l *live) redialMCP(s *mcpServer, old *mcp.Client) *mcp.Client {
	s.redial.Lock()
	defer s.redial.Unlock()
	l.mcp.mu.Lock()
	if s.client != nil && s.client != old {
		c := s.client
		l.mcp.mu.Unlock()
		return c
	}
	if s.status == "disabled" || s.client != old {
		l.mcp.mu.Unlock()
		return nil
	}
	s.gen++
	gen := s.gen
	s.client, s.tools, s.err, s.status = nil, nil, "", "pending"
	l.mcp.mu.Unlock()
	_ = old.Close()
	l.offerMCPTools()
	l.flog.logf("mcp", "the connection to the MCP server %s dropped; connecting again", s.cfg.Name)
	l.connectMCP(s, gen)
	l.mcp.mu.Lock()
	defer l.mcp.mu.Unlock()
	return s.client
}

// mcpConnect starts or reaches one server and makes the handshake: a
// server gnosh starts runs in the project's root, with CLAUDE_PROJECT_DIR
// and GNOSH_PROJECT_DIR set to it, and is told the session's folders as
// its roots; a server at a URL is told none, since the folders open here
// are nothing it needs to know.
func mcpConnect(ctx context.Context, c mcp.Config, root string, environ, roots []string, opt mcp.Options) (*mcp.Client, error) {
	switch c.Type {
	case "stdio":
		env := map[string]string{"CLAUDE_PROJECT_DIR": root, "GNOSH_PROJECT_DIR": root}
		for k, v := range c.Env {
			env[k] = v
		}
		return mcp.Start(ctx, mcp.Server{Name: c.Name, Command: c.Command, Args: c.Args, Env: env,
			Environ: environ, Dir: root, Roots: append([]string(nil), roots...)}, opt)
	case "http":
		return mcp.StartHTTP(ctx, c.Name, c.URL, c.Headers, opt)
	}
	return nil, fmt.Errorf("gnosh does not speak the %s transport", c.Type)
}

// mcpToolsOf makes the tools the model is offered of a server's list. Two
// that come to one name (listed twice, or "a.b" beside "a_b") are offered
// once, the first: a name twice in the model's list would have every
// offer of tools refused. A name longer than the model's APIs take (64
// characters) is shortened, its end a hash of the whole: OpenAI's refuses
// every request that holds one. The server is called by its own name.
func (l *live) mcpToolsOf(s *mcpServer, tools []mcp.Tool) []*mcpTool {
	var made []*mcpTool
	seen := map[string]bool{}
	var twice, unfit []string
	for _, t := range tools {
		name := mcp.FitToolName(mcp.ToolName(s.cfg.Name, t.Name))
		if seen[name] {
			twice = append(twice, t.Name)
			continue
		}
		schema, ok := objectSchema(t.InputSchema)
		if !ok {
			unfit = append(unfit, t.Name)
			continue
		}
		seen[name] = true
		made = append(made, &mcpTool{l: l, server: s, tool: t, name: name, schema: schema})
	}
	if len(twice) > 0 {
		l.notice(agent.LevelWarning, "mcp", fmt.Sprintf("The MCP server %s lists tools whose names come to one another's (%s): the first of each is offered.", s.cfg.Name, strings.Join(twice, ", ")))
	}
	if len(unfit) > 0 {
		l.notice(agent.LevelWarning, "mcp", fmt.Sprintf("The MCP server %s lists tools whose input is not described as an object (%s), which the model's APIs refuse: they are not offered.", s.cfg.Name, strings.Join(unfit, ", ")))
	}
	return made
}

// objectSchema is a tool's input schema as the model's APIs take it: an
// object schema, with "type": "object" put in where the server left the
// type out. ok is false for a schema that is not an object at all, or of
// another type: every request offering the tool was refused for it, and
// so was the session's every turn (Claude Code 2.1.328 left such a tool
// out the same way).
func objectSchema(raw json.RawMessage) (json.RawMessage, bool) {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 || string(raw) == "null" {
		return json.RawMessage(`{"type":"object"}`), true
	}
	var fields map[string]json.RawMessage
	if json.Unmarshal(raw, &fields) != nil || fields == nil {
		return nil, false
	}
	typ, has := fields["type"]
	if !has {
		fields["type"] = json.RawMessage(`"object"`)
		out, err := json.Marshal(fields)
		return out, err == nil
	}
	return raw, string(bytes.TrimSpace(typ)) == `"object"`
}

// refreshMCP reads a server's tool list again when the server has said it
// changed since it was last read, and offers the model the new list. A
// call that is made while it reads waits for it, so that the model's next
// request has the tools the server has after the call.
func (l *live) refreshMCP(s *mcpServer, client *mcp.Client) {
	s.refresh.Lock()
	defer s.refresh.Unlock()
	if !client.ToolsChanged() || l.closing.Load() {
		return
	}
	ctx, cancel := context.WithTimeout(l.ctx, l.cfg.Limits.MCPTimeout.V)
	defer cancel()
	tools, err := client.ListTools(ctx)
	if err != nil {
		l.flog.logf("mcp", "the MCP server %s said its tools changed, and they could not be listed: %v", s.cfg.Name, err)
		return
	}
	made := l.mcpToolsOf(s, tools)
	l.mcp.mu.Lock()
	if s.client != client {
		l.mcp.mu.Unlock()
		return
	}
	s.tools = made
	l.mcp.mu.Unlock()
	l.flog.logf("mcp", "the MCP server %s changed its tools (%d now)", s.cfg.Name, len(made))
	l.offerMCPTools()
}

// offerMCPTools gives the model the session's own tools and every tool of
// the servers that are connected, as far as the rules let it have them.
// One offer is made at a time, each of the list as it stands then: a
// server marks itself connected before it offers, so the last offer holds
// every server connected by then.
func (l *live) offerMCPTools() {
	if l.agent == nil {
		return
	}
	l.offerMu.Lock()
	defer l.offerMu.Unlock()
	if err := l.agent.SetTools(l.offeredTools()); err != nil {
		l.notice(agent.LevelWarning, "mcp", "The tools of the MCP servers could not be offered to the model: "+err.Error())
	}
}

// closeMCP stops every server.
func (l *live) closeMCP() {
	if l.mcp == nil {
		return
	}
	l.mcp.mu.Lock()
	var clients []*mcp.Client
	for _, s := range l.mcp.servers {
		if s.client != nil {
			clients = append(clients, s.client)
			s.client = nil
		}
	}
	l.mcp.mu.Unlock()
	for _, c := range clients {
		_ = c.Close()
	}
	// The own servers of sub-agents still running (one in the background
	// that did not stop in time) end with the session too.
	l.subMu.Lock()
	var subs []*subSession
	for s := range l.subs {
		subs = append(subs, s)
	}
	l.subMu.Unlock()
	for _, s := range subs {
		l.closeAgentMCP(s)
	}
}

// mcpStatus lists the servers as stream-json's system/init names them.
func (l *live) mcpStatus() []MCPServerStatus {
	out := []MCPServerStatus{}
	if l.mcp == nil {
		return out
	}
	l.mcp.mu.Lock()
	defer l.mcp.mu.Unlock()
	for _, s := range l.mcp.servers {
		out = append(out, MCPServerStatus{Name: s.cfg.Name, Status: s.status})
	}
	return out
}

// slashMCP is /mcp: each server, where it is configured, whether it is
// connected and with what tools, or why not. With reconnect, enable or
// disable and a server's name (or all) after it, it does that first.
func (l *live) slashMCP(ctx context.Context, c *SlashContext) error {
	agents := l.runningAgentServers()
	if l.mcp == nil || len(l.mcp.servers) == 0 && len(l.mcp.probs) == 0 && len(agents) == 0 {
		c.Say("No MCP server is configured. From a shell, gnosh mcp add <name> -- <command> [args...] adds one gnosh starts, and gnosh mcp add -t http <name> <url> one at a URL; they are kept in %s, and a project's in .mcp.json at its root (read once the folder is trusted).", filepath.Join(l.cfg.Paths.ConfigDir, "mcp.json"))
		return nil
	}
	if words := strings.Fields(c.Args); len(words) > 0 {
		return l.mcpAct(c, words)
	}
	l.mcp.mu.Lock()
	defer l.mcp.mu.Unlock()
	for _, s := range l.mcp.servers {
		c.Say("%s", l.mcpLine(s))
	}
	for _, s := range agents {
		// A running sub-agent's own: only it has the tools.
		c.Say("%s (the sub-agent %s's own, while it runs)", l.mcpLine(s), s.own)
	}
	for _, p := range l.mcp.probs {
		c.Say("not in force: %s", p.String())
	}
	c.Say("A tool of a server is mcp__<server>__<tool>; a rule such as mcp__%s allows all of one server's tools. /mcp reconnect, enable or disable, with a server's name or all, does that.", mcpRuleName(l.mcp.servers))
	return nil
}

// mcpLine says how one server stands. It is called with l.mcp.mu held.
func (l *live) mcpLine(s *mcpServer) string {
	where := mcpWhere(s.cfg)
	switch s.status {
	case "connected":
		names := make([]string, 0, len(s.tools))
		for _, t := range s.tools {
			names = append(names, t.tool.Name)
		}
		sort.Strings(names)
		info := s.client.Info()
		line := fmt.Sprintf("%s: connected (%s %s; %s), %s: %s", s.cfg.Name, firstOf(info.Name, "the server"), info.Version, where, countWords(len(names), "tool", "tools"), strings.Join(names, ", "))
		if len(s.prompts) > 0 {
			cmds := make([]string, 0, len(s.prompts))
			for _, p := range s.prompts {
				cmds = append(cmds, "/"+mcp.ToolName(s.cfg.Name, p.Name))
			}
			line += fmt.Sprintf("; %s: %s", countWords(len(cmds), "prompt", "prompts"), strings.Join(cmds, ", "))
		}
		return line
	case "failed":
		return fmt.Sprintf("%s: not connected (%s): %s", s.cfg.Name, where, oneLine(s.err, 400))
	case "needs-auth":
		if s.own != "" {
			// /mcp login signs in the session's servers only.
			return fmt.Sprintf("%s: wants a sign-in (%s), which only a server of the session's can have: add it to mcp.json, sign in with /mcp login, and name it in the agent's mcpServers", s.cfg.Name, where)
		}
		return fmt.Sprintf("%s: wants a sign-in (%s): /mcp login %s", s.cfg.Name, where, s.cfg.Name)
	case "disabled":
		return fmt.Sprintf("%s: disabled for this session (%s); /mcp enable %s connects it", s.cfg.Name, where, s.cfg.Name)
	}
	return fmt.Sprintf("%s: connecting (%s)", s.cfg.Name, where)
}

// mcpAct is /mcp reconnect, enable and disable: each takes a server's name,
// or all. A reconnect or an enable waits for the servers it connects, and
// says how each came out. What disable does lasts for this session.
func (l *live) mcpAct(c *SlashContext, words []string) error {
	verb := words[0]
	if len(l.mcp.servers) == 0 {
		// Only a running sub-agent's own servers, or files that named
		// none gnosh could use: nothing of the session's to act on.
		return errors.New("this session has no MCP server of its own to act on: a running sub-agent's own servers come and go with it, and /mcp alone lists what there is")
	}
	if (verb == "login" || verb == "logout") && len(words) >= 2 {
		for _, s := range l.mcp.servers {
			if s.cfg.Name != words[1] {
				continue
			}
			if verb == "logout" {
				return l.mcpLogout(c, s)
			}
			return l.mcpLogin(l.ctx, c, s, words[2:])
		}
		return fmt.Errorf("there is no MCP server named %s here", words[1])
	}
	if verb != "reconnect" && verb != "enable" && verb != "disable" || len(words) != 2 {
		c.Say("/mcp takes nothing, or reconnect, enable, disable, login or logout with a server's name after it (all, for the first three): /mcp reconnect %s, say.", l.mcp.servers[0].cfg.Name)
		return nil
	}
	var picked []*mcpServer
	var names []string
	for _, s := range l.mcp.servers {
		names = append(names, s.cfg.Name)
		if words[1] == "all" || words[1] == s.cfg.Name {
			picked = append(picked, s)
		}
	}
	if len(picked) == 0 {
		c.Say("There is no MCP server named %s here; the servers are %s.", words[1], strings.Join(names, ", "))
		return nil
	}
	// Each one picked is let go of first: a reconnect starts afresh, and a
	// disable ends there. An enable leaves alone a server that is not
	// disabled, and a reconnect one that is.
	type pending struct {
		s   *mcpServer
		gen int
	}
	var connect []pending
	var gone []*mcp.Client
	l.mcp.mu.Lock()
	for _, s := range picked {
		switch {
		case verb == "enable" && s.status != "disabled", verb == "reconnect" && s.status == "disabled":
			continue
		}
		if s.client != nil {
			gone = append(gone, s.client)
		}
		s.gen++
		s.client, s.tools, s.err = nil, nil, ""
		if verb == "disable" {
			s.status = "disabled"
		} else {
			s.status = "pending"
			connect = append(connect, pending{s, s.gen})
		}
	}
	l.mcp.mu.Unlock()
	for _, cl := range gone {
		_ = cl.Close()
	}
	l.offerMCPTools()
	var wg sync.WaitGroup
	for _, p := range connect {
		wg.Go(func() { l.connectMCP(p.s, p.gen) })
	}
	wg.Wait()
	l.mcp.mu.Lock()
	defer l.mcp.mu.Unlock()
	for _, s := range picked {
		c.Say("%s", l.mcpLine(s))
	}
	return nil
}

// mcpWhere is where a server is configured, and for one at a URL which
// host (the rest of a URL may hold a key).
func mcpWhere(c mcp.Config) string {
	where := c.Scope + ", " + c.Path
	if c.Type == "http" {
		where += ", at " + c.Shown
	}
	return where
}

// mcpRuleName is a server name to show in an example rule.
func mcpRuleName(servers []*mcpServer) string {
	if len(servers) == 0 {
		return "server"
	}
	return strings.TrimSuffix(strings.TrimPrefix(mcp.ToolName(servers[0].cfg.Name, ""), "mcp__"), "__")
}

// mcpTool is one tool of a server, as the model is offered it.
type mcpTool struct {
	l      *live
	server *mcpServer
	tool   mcp.Tool
	name   string
	// schema is the tool's input schema as the model's APIs take it
	// (objectSchema).
	schema json.RawMessage
}

func (t *mcpTool) Spec() core.ToolSpec {
	desc := t.tool.Description
	if desc == "" {
		desc = t.tool.Title
	}
	if utf8.RuneCountInString(desc) > mcpDescriptionMax {
		desc = clipChars(desc, 0, mcpDescriptionMax) + "…"
	}
	// The server's words, not the person's or gnosh's: gnosh's own tags in
	// the description or the schema's reach the model broken, as in what
	// the tool returns. (The mark goes inside JSON strings, where a "<"
	// can stand; the schema stays JSON.)
	return core.ToolSpec{Name: t.name, Description: prompt.Untrusted(desc), InputSchema: json.RawMessage(prompt.Untrusted(string(t.schema)))}
}

// Parallel says a call may run beside others when the server says its
// tool only reads.
func (t *mcpTool) Parallel(core.ToolCall) bool { return t.tool.Annotations.ReadOnly }

func (t *mcpTool) Describe(call core.ToolCall, env *core.ToolEnv) (core.Action, error) {
	return core.Action{Kind: core.ActionOther, Summary: fmt.Sprintf("%s of the MCP server %s", t.tool.Name, t.server.cfg.Name)}, nil
}

func (t *mcpTool) Run(ctx context.Context, call core.ToolCall, env *core.ToolEnv) (core.ToolResult, error) {
	t.l.mcp.mu.Lock()
	client := t.server.client
	t.l.mcp.mu.Unlock()
	if client == nil {
		return core.ErrorResult(fmt.Sprintf("The MCP server %s is not connected now, so %s did not run.", t.server.cfg.Name, t.tool.Name)), nil
	}
	// The server's own timeout, else MCP_TOOL_TIMEOUT, else none.
	limit, whose := t.server.cfg.Timeout, "the timeout its entry gives"
	if limit == 0 {
		limit, whose = t.l.cfg.Limits.MCPToolTimeout.V, "MCP_TOOL_TIMEOUT"
	}
	if limit > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, limit)
		defer cancel()
	}
	defer t.server.calling(ctx)()
	res, err := client.CallTool(ctx, t.tool.Name, call.Input)
	// A server that changed its tools on the way says so before it answers.
	t.l.refreshMCP(t.server, client)
	// A server at a URL that dropped the connection is connected again, as
	// Claude Code does. One that ended the session refused the call, so the
	// call is made again; of one that could not be reached nobody knows
	// whether the call got there, so the model is told, and it decides.
	if t.server.cfg.Type == "http" && ctx.Err() == nil && (errors.Is(err, mcp.ErrSessionEnded) || errors.Is(err, mcp.ErrUnreachable)) {
		fresh := t.l.redialMCP(t.server, client)
		switch {
		case fresh == nil:
			return core.ErrorResult(fmt.Sprintf("%s of the MCP server %s did not run: %v, and connecting to it again failed (/mcp says why).", t.tool.Name, t.server.cfg.Name, err)), nil
		case errors.Is(err, mcp.ErrUnreachable):
			return core.ErrorResult(fmt.Sprintf("The MCP server %s could not be reached with the call of %s (%v), so whether it ran is not known. It is connected again: look before you call it again, if a second run would matter.", t.server.cfg.Name, t.tool.Name, err)), nil
		}
		client = fresh
		res, err = client.CallTool(ctx, t.tool.Name, call.Input)
		t.l.refreshMCP(t.server, client)
	}
	if err != nil {
		if errors.Is(ctx.Err(), context.DeadlineExceeded) && limit > 0 {
			return core.ErrorResult(fmt.Sprintf("%s of the MCP server %s did not answer within %s, %s.", t.tool.Name, t.server.cfg.Name, limit, whose)), nil
		}
		return core.ErrorResult(fmt.Sprintf("%s of the MCP server %s did not run: %v", t.tool.Name, t.server.cfg.Name, err)), nil
	}
	text := mcpText(res)
	if res.IsError {
		if n := utf8.RuneCountInString(text); n > 11_000 {
			text = clipChars(text, 0, 5000) + fmt.Sprintf("\n[... %d characters left out ...]\n", n-10_000) + clipChars(text, n-5000, 5000)
		}
		return core.ErrorResult(firstOf(text, "The tool failed and said nothing.")), nil
	}
	n := utf8.RuneCountInString(text)
	if n > mcpLargeChars {
		t.l.flog.logf("mcp", "%s of the MCP server %s returned a large result: %d characters, about %d tokens", t.tool.Name, t.server.cfg.Name, n, n/4)
	}
	if most := min(mcpMaxChars, t.l.cfg.Limits.MCPMaxOutputTokens.V*4); n > most {
		path := filepath.Join(t.l.spoolDir(), fmt.Sprintf("%s-%s.txt", t.name, plainName(firstOf(call.ID, "call"))))
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err == nil && os.WriteFile(path, []byte(text), 0o600) == nil {
			text = fmt.Sprintf("The result is %d characters long, more than the %d one result may be here, so the whole of it is in %s: read that file, in parts, for it. It begins:\n%s", n, most, path, clipChars(text, 0, min(mcpGlimpse, most/2)))
		} else {
			text = clipChars(text, 0, most) + fmt.Sprintf("\n[The result is %d characters long, and only the first %d are here.]", n, most)
		}
	}
	return core.TextResult(firstOf(text, "(the tool returned nothing)")), nil
}

// plainName keeps the plain characters of a name that goes into a file's
// name and is someone else's word (a call's id is the model's): letters,
// digits, - and _; every other character is a _.
func plainName(s string) string {
	return strings.Map(func(r rune) rune {
		if r < 128 && (r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '_' || r == '-') {
			return r
		}
		return '_'
	}, s)
}

// mcpText writes what a tool returned as text: its text, a line for what is
// not text, and its structured content when there is nothing else.
func mcpText(res mcp.Result) string {
	var parts []string
	for _, c := range res.Content {
		switch c.Type {
		case "text":
			parts = append(parts, c.Text)
		case "image", "audio":
			parts = append(parts, fmt.Sprintf("[%s: %s, %d bytes of base64, not shown]", c.Type, firstOf(c.MimeType, "of no stated type"), len(c.Data)))
		case "resource":
			if c.Resource != nil && c.Resource.Text != "" {
				parts = append(parts, c.Resource.Text)
			} else if c.Resource != nil {
				parts = append(parts, "[a resource: "+c.Resource.URI+"]")
			}
		case "resource_link":
			parts = append(parts, "[a resource link]")
		}
	}
	if len(parts) == 0 && len(res.StructuredContent) > 0 {
		return string(res.StructuredContent)
	}
	return strings.Join(parts, "\n")
}

// mcpHookCall calls a tool of a connected MCP server for an mcp_tool hook:
// the text the tool answers with, and whether it said it failed.
func (l *live) mcpHookCall(ctx context.Context, server, tool string, input json.RawMessage) (string, bool, error) {
	if l.mcp == nil {
		return "", false, fmt.Errorf("this session has no MCP servers, so %s of %s cannot be called", tool, server)
	}
	l.mcp.mu.Lock()
	var client *mcp.Client
	for _, s := range l.mcp.servers {
		if s.cfg.Name == server {
			client = s.client
		}
	}
	l.mcp.mu.Unlock()
	if client == nil {
		return "", false, fmt.Errorf("the MCP server %s is not connected now", server)
	}
	res, err := client.CallTool(ctx, tool, input)
	if err != nil {
		return "", false, err
	}
	return mcpText(res), res.IsError, nil
}
