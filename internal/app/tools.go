package app

import (
	"fmt"
	"os/user"
	"path"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/gnosh-ai/gnosh/internal/cli"
	"github.com/gnosh-ai/gnosh/internal/config"
	"github.com/gnosh-ai/gnosh/internal/core"
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
	fo := fstools.Options{HomeDir: l.cfg.Paths.Home, BeforeChange: l.keepFile, ReadDenied: l.engine.ReadDenied}
	if limits.FileReadMaxTokens.V > 0 {
		// The limit is given in tokens, and the file tools count bytes: a
		// token is about four of them.
		fo.ReadMaxBytes = limits.FileReadMaxTokens.V * 4
	}
	if limits.GlobTimeout.Set {
		fo.SearchTimeout = limits.GlobTimeout.V
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
		EnvFile:             l.envFile(),
		SpoolDir:            l.spoolDir(),
		Now:                 l.p.now,
		DefaultTimeout:      limits.BashDefaultTimeout.V,
		MaxTimeout:          limits.BashMaxTimeout.V,
		TimeoutToBackground: true,
		NoBackground:        l.inv.Bare || l.p.env["CLAUDE_CODE_DISABLE_BACKGROUND_TASKS"] == "1",
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
	all = append(all, &webFetchTool{l: l})
	if u := strings.TrimRight(l.cfg.Options.WebSearchURL.V, "/"); u != "" {
		// (Only with a search server named: see websearch.go.)
		all = append(all, &webSearchTool{l: l, base: u})
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
	deny := l.cfg.Permissions.DenyInsteadOfPrompting.V
	if l.inv.PermissionPrompts != "none" && !deny {
		// (With --permission-prompts none nobody answers, and a tool that
		// asks a person is no tool.)
		all = append(all, &askTool{l: l})
	}
	if !deny {
		// (permissions.denyInsteadOfPrompting takes the plan tools away
		// too, as in Claude Code: a plan is approved by a person.)
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
	}
}

// gnoRunTool is the GnoVM feature's tool that runs Gno, mem's calls among
// it.
const gnoRunTool = "gnovm_run"

// gnoCall finds a call on mem written as Gno: mem.Show(), mem.Todo(...).
var gnoCall = regexp.MustCompile(`\bmem\.[A-Z][A-Za-z0-9_]*\(`)

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
