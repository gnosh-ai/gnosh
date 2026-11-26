package app

import (
	"context"
	"encoding/json"
	"fmt"
	"go/ast"
	"go/parser"
	"os/user"
	"path"
	"regexp"
	"runtime"
	"slices"
	"strings"
	"time"

	"github.com/gnosh-ai/gnosh/internal/agent"
	"github.com/gnosh-ai/gnosh/internal/cli"
	"github.com/gnosh-ai/gnosh/internal/config"
	"github.com/gnosh-ai/gnosh/internal/core"
	"github.com/gnosh-ai/gnosh/internal/hooks"
	"github.com/gnosh-ai/gnosh/internal/tools/fstools"
	"github.com/gnosh-ai/gnosh/internal/tools/shelltool"
)

// looker is a tool whose calls only look at things, so that the loop may
// run several of them side by side (agent.Concurrent). The file tools that
// read are wrapped in it; the tools that change something are not, and run
// alone and in order.
type looker struct {
	core.Tool
}

// Parallel says a call may run beside other calls that only look.
func (looker) Parallel(core.ToolCall) bool { return true }

// Describe hands the call on to the tool's own account of what it would
// do.
func (t looker) Describe(call core.ToolCall, env *core.ToolEnv) (core.Action, error) {
	if d, ok := t.Tool.(core.Describer); ok {
		return d.Describe(call, env)
	}
	return core.Action{Kind: core.ActionOther}, nil
}

// buildTools makes the session's tools: gnosh's own first, then the
// features', in the fixed order the model is told of them. A tool that is
// switched off (--tools left it out, or a deny rule names the whole tool)
// is not in the list at all.
func (l *live) buildTools() error {
	limits := l.cfg.Limits
	fo := fstools.Options{HomeDir: l.cfg.Paths.Home, BeforeChange: l.keepFile, ReadDenied: l.engine.ReadDenied, ReadRoom: l.readRoom, ReadResultMost: l.readResultMost}
	if limits.FileReadMaxTokens.V > 0 {
		// The limit is given in tokens, and the file tools count bytes: a
		// token is about four of them.
		fo.ReadMaxBytes = limits.FileReadMaxTokens.V * 4
	}
	if limits.GlobTimeout.Set {
		fo.SearchTimeout = limits.GlobTimeout.V
	}
	if limits.GlobNoIgnore.Set {
		fo.GlobNoIgnore = limits.GlobNoIgnore.V
	}
	names := fstools.Defaults()
	var all []core.Tool
	for _, t := range fstools.New(fo) {
		switch t.Spec().Name {
		case names.ReadName, names.GlobName, names.GrepName:
			all = append(all, looker{t})
		default:
			all = append(all, t)
		}
	}

	so := shelltool.Options{
		Environ:             func() []string { return slices.Clone(l.p.o.Environ) },
		Env:                 l.cfg.CommandEnv(),
		Scrub:               l.p.scrubbing(),
		PersonSandboxed:     l.cfg.Sandbox.Scrubbed && runtime.GOOS == "linux",
		ScriptCaps:          l.scriptCaps(),
		EnvFile:             l.envFile(),
		SpoolDir:            l.spoolDir(),
		Now:                 l.p.now,
		DefaultTimeout:      limits.BashDefaultTimeout.V,
		MaxTimeout:          limits.BashMaxTimeout.V,
		TimeoutToBackground: true,
		NoBackground:        l.inv.Bare || l.envOn("CLAUDE_CODE_DISABLE_BACKGROUND_TASKS"),
		NoCarryCwd:          l.envOn("CLAUDE_BASH_MAINTAIN_PROJECT_WORKING_DIR"),
		MaxBackground:       l.cfg.Options.MaxBackgroundTasks.V,
		MaxOutputChars:      bashOutputChars(l.cfg),
		NoOutputTool:        true,
		// The machines the config declares, and this one as every result
		// names it. A folder's own hosts are among them only once the
		// folder is trusted: until then the config holds them back.
		Hosts:     shellHosts(l.cfg),
		Local:     l.p.whoAndWhere(),
		ToolNames: l.p.toolNames(),
	}
	if slices.Contains(so.ToolNames, gnoRunTool) {
		so.Misread = misreadGno
	}
	so.Sandbox = l.shellSandbox()
	if l.p.scrubbing() && runtime.GOOS == "linux" {
		so.Isolate = l.pidIsolation()
	}
	if l.interactive || l.inv.Command == cli.CmdPrint {
		// A session somebody attends is told when a background command
		// ends, and so is a run, which waits for the background work its
		// turns started (printwait.go).
		so.OnEnd = l.backgroundEnded
		// What the commands of Monitor calls print is passed on the same
		// way, at the pace the model reads it.
		so.OnMonitor = l.monitorLines
		// The refusal of a foreground command that begins with a long
		// sleep (shelltool.Options.LongSleep, for sessions like this one,
		// which are told when a background command ends) is built and is
		// not switched on here: a test of the interface still runs such a
		// sleep in the foreground. It goes on together with that test.
	}
	l.shell = shelltool.New(so)
	all = append(all, l.wrapStop(l.shell.Tools())...)
	if len(l.cfg.LSPServers) > 0 {
		// (Only with a language server named: lsp.go.)
		l.lspSrv = newLSPServers(l, l.cfg.LSPServers)
		all = append(all, &lspTool{s: l.lspSrv})
	}
	noFetch := l.envOn("CLAUDE_CODE_DISABLE_WEB_FETCH")
	if !noFetch {
		// (CLAUDE_CODE_DISABLE_WEB_FETCH turns it off, as in Claude Code.)
		all = append(all, &webFetchTool{l: l, cacheFor: limits.WebFetchCacheTTL.V, deadline: limits.WebFetchDeadline.V})
	}
	if u := strings.TrimRight(l.cfg.Options.WebSearchURL.V, "/"); u != "" {
		// (Only with a search server named: see websearch.go.)
		all = append(all, &webSearchTool{l: l, base: u, noFetch: noFetch})
	}
	// gnosh's own tools, as a sub-agent starts from them.
	l.own = l.own[:0]
	for _, t := range all {
		if l.engine.ToolEnabled(t.Spec().Name) {
			l.own = append(l.own, t)
		}
	}
	// The tools a sub-agent never has: its own Agent tool is made for it
	// at its depth, only the session the person is in may ask them a
	// question, and plan mode is the session's to switch.
	all = append(all, &agentTool{l: l})
	if l.backgroundOff(nil) == "" && l.engine.ToolEnabled(agentToolName) {
		// A sub-agent that ended is given more to do in the background,
		// where sub-agents may be started at all.
		all = append(all, &sendMessageTool{l: l})
	}
	// A run has nobody to put a question to, whatever its input format
	// (stream-json carries user messages, no answers): a tool that only
	// asks is offered where a person is there, or where a PreToolUse hook
	// the person set up may answer in their place, and plan mode's where a
	// plan can be approved (planApproved). Offered in a run, the three
	// schemas were 3.5K of the 27K characters of tools sent with every
	// request of a FrontierHarness run, and a call of one came back
	// refused (the FrontierHarness session, 2026-11-19).
	deny := l.cfg.Permissions.DenyInsteadOfPrompting.V
	if l.inv.PermissionPrompts != "none" && !deny && (l.interactive || l.hooks.has(hooks.PreToolUse)) {
		// (With --permission-prompts none nobody answers, and a tool that
		// asks a person is no tool.)
		all = append(all, &askTool{l: l})
	}
	if l.planApproved() {
		all = append(all, &enterPlanTool{l: l}, &exitPlanTool{l: l})
	}
	if l.interactive && l.p.o.Relaunch != nil {
		// Moving the session into a work tree of its own, and back: a
		// session somebody attends, in a gnosh that can start itself
		// again (worktreetool.go).
		switch {
		case l.st.tree == nil && l.st.folder.Repo && !l.st.folder.Worktree && l.st.trusted:
			all = append(all, &enterTreeTool{l: l})
		case l.st.tree != nil && l.st.tree.entered:
			all = append(all, &exitTreeTool{l: l})
		}
		// And starting gnosh again in the session, at the model's call
		// (restarttool.go).
		all = append(all, &restartTool{l: l})
	}
	if l.interactive {
		// Calling the person back (pushtool.go).
		all = append(all, &pushTool{l: l})
		// gnokey, with a person to type a key's password, in a project
		// that holds Gno code, where gnokey is installed (gnokeytool.go).
		// (Not the plain front end's: it reads lines typed ahead as
		// prompts, so a password typed early could go to the model.)
		if _, plainFront := l.front.(*plain); !plainFront {
			if bin, err := l.p.lookPath(gnokeyToolName); err == nil && hasGnoCode(l.cfg.Paths.ProjectRoot, l.cfg.Paths.Cwd) {
				if t := newGnokeyTool(l, bin); t != nil {
					all = append(all, t)
				}
			}
		}
	}
	if l.cronOff() == "" {
		// Prompts scheduled for later in the session (crontool.go).
		all = append(all, &cronCreateTool{l: l}, &cronListTool{l: l}, &cronDeleteTool{l: l}, &wakeTool{l: l})
	}
	if len(l.forModel()) > 0 {
		// The Skill tool is there when there is a skill a model may
		// load, as the session starts.
		all = append(all, &skillTool{l: l})
	}

	claimed := map[string]string{}
	for _, f := range l.p.o.Features {
		for _, t := range f.Tools {
			claimed[t.Name] = f.Name
		}
	}
	for _, c := range l.contribs {
		for _, t := range c.c.Tools {
			name := t.Spec().Name
			if claimed[name] != c.f.Name {
				return fmt.Errorf("the feature %q gave this session a tool named %q, which is not in its list of tool names. A tool whose name is in no list could not be named by a rule, a flag or a hook, so gnosh does not start with it", c.f.Name, name)
			}
			all = append(all, t)
		}
	}

	l.tools = l.tools[:0]
	seen := map[string]bool{}
	for _, t := range all {
		name := t.Spec().Name
		if seen[name] {
			return fmt.Errorf("two tools are named %q", name)
		}
		seen[name] = true
		if l.engine.ToolEnabled(name) {
			l.tools = append(l.tools, t)
		}
	}
	return nil
}

// bashOutputChars is how many characters of a command's output the model
// reads: BASH_MAX_OUTPUT_LENGTH when it is set, as the environment
// outranks the files, else the bashOutputMaxChars option.
func bashOutputChars(cfg *config.Config) int {
	if cfg.Limits.BashMaxOutput.Set {
		return cfg.Limits.BashMaxOutput.V
	}
	return cfg.Options.BashOutputMaxChars.V
}

// shellHosts turns the hosts the configuration holds in force into the
// shell tool's own form.
func shellHosts(cfg *config.Config) []shelltool.Host {
	hosts := make([]shelltool.Host, 0, len(cfg.Hosts))
	for _, h := range cfg.Hosts {
		hosts = append(hosts, shelltool.Host{Name: h.Name, Target: h.SSH, Description: h.Description})
	}
	return hosts
}

// whoAndWhere says who gnosh runs as and on which machine, as user@name,
// with whatever of the two is known. It is how every shell result names
// this machine.
func (p *program) whoAndWhere() string {
	who, where := p.userName(), hostName()
	switch {
	case who != "" && where != "":
		return who + "@" + where
	case where != "":
		return where
	}
	return who
}

// userName is the name of the user gnosh runs as: what the environment it
// was given says, or failing that what the system says.
func (p *program) userName() string {
	if name := p.getenv("USER"); name != "" {
		return name
	}
	if u, err := user.Current(); err == nil {
		return u.Username
	}
	return ""
}

// toolNames lists the names of the session's tools, in order.
func (l *live) toolNames() []string {
	all := l.offeredTools()
	names := make([]string, len(all))
	for i, t := range all {
		names[i] = t.Spec().Name
	}
	return names
}

// offeredTools is every tool the model is offered: the session's own, and
// those of the MCP servers that are connected, as far as the rules let it
// have them.
func (l *live) offeredTools() []core.Tool {
	return append(append([]core.Tool(nil), l.tools...), l.allowedMCPTools()...)
}

// allowedMCPTools are the tools of the MCP servers connected now that the
// session may offer, and its sub-agents with it: the session that runs as
// an agent (--agent) has the MCP tools its definition lists, by name or by
// server, and not the ones it takes away; its sub-agents have no others.
func (l *live) allowedMCPTools() []core.Tool {
	var out []core.Tool
	for _, t := range l.mcpTools() {
		if def := l.asAgent; def != nil {
			name := t.Spec().Name
			if def.ToolsSet && strings.HasPrefix(name, "mcp__") && !toolListed(def.Tools, name, false) || toolListed(def.Disallowed, name, true) {
				continue
			}
		}
		out = append(out, t)
	}
	return out
}

// mcpTools are the tools of the MCP servers connected now, as far as the
// rules let the model have them.
func (l *live) mcpTools() []core.Tool {
	if l.mcp == nil {
		return nil
	}
	l.mcp.mu.Lock()
	defer l.mcp.mu.Unlock()
	var out []core.Tool
	resources := false
	// Two servers' tools can come to one name (servers "a.b" and "a_b",
	// whose tools are both mcp__a_b__<tool>): the first server's is
	// offered, since a name twice in the model's list would have every
	// offer of tools refused.
	seen := map[string]bool{}
	for _, s := range l.mcp.servers {
		for _, t := range s.tools {
			if seen[t.name] {
				l.flog.logf("mcp", "the tool %s of the MCP server %s has the name of another server's, which is offered in its place", t.tool.Name, s.cfg.Name)
				continue
			}
			seen[t.name] = true
			if l.engine.ToolEnabled(t.name) {
				out = append(out, t)
			}
		}
		resources = resources || s.client != nil && s.client.Info().Resources
	}
	if resources {
		// A connected server has resources: the two tools that read them.
		for _, t := range l.resourceTools() {
			if l.engine.ToolEnabled(t.Spec().Name) {
				out = append(out, t)
			}
		}
	}
	return out
}

// toolSpecs lists what the model is told about the session's tools.
func (l *live) toolSpecs() []core.ToolSpec {
	all := l.offeredTools()
	specs := make([]core.ToolSpec, len(all))
	for i, t := range all {
		specs[i] = t.Spec()
	}
	return specs
}

// closeShell stops every command the session started, with everything
// those commands started, and waits until they are gone. It is safe to
// call more than once.
func (l *live) closeShell() {
	if l.shell == nil {
		return
	}
	done := make(chan error, 1)
	go func() { done <- l.shell.Close() }()
	select {
	case err := <-done:
		if err != nil {
			l.p.warnf("not everything the session's commands started could be stopped: %v", err)
		}
	case <-time.After(60 * time.Second):
		l.p.warnf("the session's commands had not all stopped after a minute; gnosh goes on without waiting for them.")
		return // (their sandbox's folder is left to them)
	}
	l.closeSandbox()
}

// gnoRunTool is the GnoVM feature's tool that runs Gno, mem's calls among
// it.
const gnoRunTool = "gnovm_run"

// gnoCall finds a call on mem written as Gno: mem.Show(), mem.Todo(...).
var gnoCall = regexp.MustCompile(`\bmem\.[A-Z][A-Za-z0-9_]*\(`)

// rerouteGno is the loop's Reroute: a shell command that is nothing but
// calls on mem, written as Gno (mem.Show(), mem.Todo("x").Push()), runs in
// mem by gnovm_run in its place. Refused with a note, it taught a small
// model nothing: in 19 of 45 FrontierHarness trials on box 2 (2026-11-19)
// Qwen's first mem call went to the shell, and in one 315 of its 320 calls
// did, for the whole half hour. What a command for another machine, or in
// the background, says stays the shell's.
func (l *live) rerouteGno(call core.ToolCall) (core.ToolCall, string, bool) {
	if call.Name != shelltool.BashName || !slices.Contains(l.toolNames(), gnoRunTool) {
		return core.ToolCall{}, "", false
	}
	var in struct {
		Command    string `json:"command"`
		Host       string `json:"host"`
		Background bool   `json:"run_in_background"`
	}
	if json.Unmarshal(call.Input, &in) != nil || in.Host != "" || in.Background {
		return core.ToolCall{}, "", false
	}
	code, ok := memCode(in.Command)
	if !ok {
		return core.ToolCall{}, "", false
	}
	input, err := json.Marshal(map[string]string{"code": code, "name": memVM})
	if err != nil {
		return core.ToolCall{}, "", false
	}
	note := "That was Gno for mem, which no shell runs, so gnosh ran it with " + gnoRunTool + ` (name "` + memVM + `") in its place; this is what it returned. Call ` + gnoRunTool + " for mem from here on."
	return core.ToolCall{ID: call.ID, Name: gnoRunTool, Input: input}, note, true
}

// memCode returns a command that is nothing but calls on mem, each line
// one Gno call, or a chain of them, on mem (mem.Show(),
// mem.Todo("x").Push()), as the Gno to run. A line with anything more is
// the shell's ("mem.Show() && rm -r build").
func memCode(command string) (string, bool) {
	var lines []string
	for _, line := range strings.Split(strings.TrimSpace(command), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		if !memStatement(line) {
			return "", false
		}
		lines = append(lines, line)
	}
	if len(lines) == 0 {
		return "", false
	}
	return strings.Join(lines, "\n"), true
}

// memStatement reports whether a line is one call on mem, or a chain of
// calls that begins with one: read as Go, which Gno is written in.
func memStatement(line string) bool {
	e, err := parser.ParseExpr(strings.TrimSuffix(line, ";"))
	if err != nil {
		return false
	}
	call, ok := e.(*ast.CallExpr)
	for ok {
		sel, isSel := call.Fun.(*ast.SelectorExpr)
		if !isSel {
			return false
		}
		switch x := sel.X.(type) {
		case *ast.Ident:
			return x.Name == memVM && ast.IsExported(sel.Sel.Name)
		case *ast.CallExpr:
			call = x
		default:
			return false
		}
	}
	return false
}

// memVM is the name of mem's VM to gnovm_run (gnovm.MemVM).
const memVM = "mem"

// misreadGno says of a command that it was Gno for mem typed into a shell,
// when it was: mem, one of its calls, or a name made up from it or from the
// tool, run as a program (mem show, mem.Show(), mem_next, gnovm_list), or
// Gno handed to a shell or to gnosh (echo 'mem.Show()' | bash). A model new
// to mem tries these first. The shell tool asks before a command runs,
// about the programs that are not installed, and after one fails.
func misreadGno(command string, programs []string) string {
	for _, p := range programs {
		p = path.Base(p)
		madeUp := p == "mem" || p == "gnovm" || strings.HasPrefix(p, "mem.") || strings.HasPrefix(p, "mem_") || strings.HasPrefix(p, "gnovm_")
		if madeUp || (gnoCall.MatchString(command) && slices.Contains([]string{"bash", "sh", "dash", "zsh", "gnosh"}, p)) {
			return "mem is Gno, which your " + gnoRunTool + " tool runs: it is no program, and no shell runs it. Call " + gnoRunTool + " with the Gno itself (mem.Show(), say) instead of putting it in a command"
		}
	}
	return ""
}

// bytesPerToken is how the loop counts text it has not had counted by a
// server: three bytes a token.
const bytesPerToken = 3

// readRoom is how many bytes of text a Read with allow_large may return:
// about what the calling agent's context has room for before room has to
// be made, as Claude Code sizes allow_large to the room left in the
// context. The calls of one round share that room (agent.TakeRoom), and
// none takes more than one result may hold (readResultMost).
func (l *live) readRoom(ctx context.Context) (int, func(used int)) {
	a := l.callerOf(ctx)
	if a == nil {
		return 0, func(int) {}
	}
	n, give := a.TakeRoom(l.readResultMost(ctx) / bytesPerToken)
	return n * bytesPerToken, func(used int) {
		give(n - (used+bytesPerToken-1)/bytesPerToken)
	}
}

// readResultMost is the most bytes one Read result may hold: a tenth
// under the cuts every tool result gets on its way to the model, to a
// quarter of the window (in the agent loop) and to maxResultBytes, so that
// Read stops before them and what it records as read is what the model is
// shown. The tenth is for Read's own note and the loop's rounding.
func (l *live) readResultMost(ctx context.Context) int {
	most := maxResultBytes
	if a := l.callerOf(ctx); a != nil {
		if t := a.Context().ResultMost(); t > 0 {
			most = min(most, t*bytesPerToken)
		}
	}
	return most * 9 / 10
}

// callerOf is the agent whose tool call ctx belongs to: a sub-agent's own,
// or the session's.
func (l *live) callerOf(ctx context.Context) *agent.Agent {
	if a := agent.FromContext(ctx); a != nil {
		return a
	}
	return l.agent
}
