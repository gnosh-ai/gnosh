package app

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/user"
	"path/filepath"
	"slices"
	"strconv"
	"strings"

	"github.com/gnosh-ai/gnosh/internal/agent"
	"github.com/gnosh-ai/gnosh/internal/app/agentdef"
	"github.com/gnosh-ai/gnosh/internal/cli"
	"github.com/gnosh-ai/gnosh/internal/config"
	"github.com/gnosh-ai/gnosh/internal/hooks"
	"github.com/gnosh-ai/gnosh/internal/permission"
	"github.com/gnosh-ai/gnosh/internal/tools/fstools"
	"github.com/gnosh-ai/gnosh/internal/tools/shelltool"
	"github.com/gnosh-ai/gnosh/internal/transcript"
	"github.com/gnosh-ai/gnosh/internal/trust"
)

// startup is what is known once a command line's folder, its trust and its
// config are settled: everything a session, doctor or a feature's
// subcommand starts from.
type startup struct {
	p   *program
	inv cli.Invocation

	// attended says a person is there to be asked: the session is an
	// interactive one, and its front end can be typed into.
	attended bool
	// agents are the kinds of sub-agent --agents defines, over the files'
	// and gnosh's own.
	agents []agentdef.Def
	// runState, when not empty, is a folder of the run's own that holds
	// its session in place of the state directory, and goes when the run
	// ends (--no-session-persistence).
	runState string

	// folder is the folder the command works in, and trusted whether its
	// own settings are in force.
	folder  trust.Folder
	trusted bool
	// tree is the git work tree --worktree has the session work in
	// (sessiontree.go).
	tree *sessionTree
	// decision is what the trust record and the command line came to.
	decision trust.Decision
	// gitPrograms is what the repository's own git settings name that git
	// would run on a mere look.
	gitPrograms []trust.Holding
	// held is what the folder's settings would do if it were trusted, as
	// the load that treated it as not trusted found it.
	held []trust.Holding

	// cfg is the configuration in force, or nil when it has mistakes.
	// problems is everything the load noticed, mistakes and warnings.
	cfg      *config.Config
	problems []config.Problem
	// inputs is what the config was loaded from, kept so that a setting
	// gnosh writes itself can be checked the same way before it is
	// written.
	inputs config.Inputs
}

// ownTools are the names of the tools gnosh itself has, in the order a
// model is told of them. It is the first half of the one list of tool
// names; the features' names are the second. A permission rule or a
// --disallowedTools that names a tool not in it stops startup, so every
// tool offeredTools can make is here (TestOwnToolsNameEveryTool): a rule
// as Claude Code writes it, WebFetch(domain:...) say, was refused.
func ownTools() []string {
	d := fstools.Defaults()
	return []string{d.ReadName, d.WriteName, d.EditName, d.GlobName, d.GrepName, shelltool.BashName, shelltool.StopName, shelltool.MonitorName,
		webFetchName, webSearchName, agentToolName, sendMessageName, askToolName, enterPlanName, exitPlanName, skillToolName}
}

// toolNames returns the one list of tool names of this program: gnosh's
// own, then every feature's, in order.
func (p *program) toolNames() []string {
	names := ownTools()
	for _, f := range p.o.Features {
		for _, t := range f.Tools {
			if t.Name != "" && !slices.Contains(names, t.Name) {
				names = append(names, t.Name)
			}
		}
	}
	return names
}

// featureTools sorts the features' tool names for the permission engine
// (the ones that never ask, and the others) and for the hook runner (the
// Claude Code tools whose hooks guard each).
func (p *program) featureTools() (known, quiet []string, guards map[string][]string) {
	guards = map[string][]string{}
	for _, f := range p.o.Features {
		for _, t := range f.Tools {
			if t.Name == "" {
				continue
			}
			if t.Quiet {
				quiet = append(quiet, t.Name)
			} else {
				known = append(known, t.Name)
			}
			if len(t.Guards) > 0 {
				guards[t.Name] = slices.Clone(t.Guards)
			}
		}
	}
	return known, quiet, guards
}

// noLogTools is the set of tools whose arguments and results are never
// written to the log or shown in debugging output (ToolName.NoLogArgs).
func (p *program) noLogTools() map[string]bool {
	out := map[string]bool{}
	for _, f := range p.o.Features {
		for _, t := range f.Tools {
			if t.NoLogArgs && t.Name != "" {
				out[t.Name] = true
			}
		}
	}
	return out
}

// firedEvents lists the hook events this program fires: the loop's own
// moments (those of a sub-agent among them, now that the Agent tool starts
// one), and the two that the wiring fires itself.
func firedEvents() []hooks.Event {
	var out []hooks.Event
	for _, name := range agent.Fires() {
		out = append(out, hooks.Event(name))
	}
	return append(out, hooks.InstructionsLoaded, hooks.Notification, hooks.PostModelSwitch, hooks.DirectoryAdded, hooks.CwdChanged, hooks.UserPromptExpansion)
}

// absFrom makes a path typed on the command line absolute: a relative one
// is under the directory gnosh was started in, and a leading ~ is the home
// directory.
func (p *program) absFrom(path string) string {
	switch {
	case path == "~":
		path = p.home
	case strings.HasPrefix(path, "~/"):
		path = filepath.Join(p.home, path[2:])
	}
	if !filepath.IsAbs(path) {
		path = filepath.Join(p.o.Cwd, path)
	}
	return filepath.Clean(path)
}

// prepare settles the folder a command line works in, its trust and its
// config. ask, when not nil, is how the person is put the trust question;
// it reports yes, and whether the person quit instead of answering.
//
// With a nil result the folder itself could not be used; the reason has
// been printed and the int is the exit code. Otherwise the startup is
// returned even when the config has mistakes: its cfg is then nil and its
// problems say why.
func (p *program) prepare(ctx context.Context, inv cli.Invocation, interactive bool, ask ...func(trust.Question) (yes, quit bool)) (*startup, int) {
	cwd := p.o.Cwd
	if inv.Cwd != "" {
		cwd = p.absFrom(inv.Cwd)
	}
	if info, err := os.Stat(cwd); err != nil || !info.IsDir() {
		p.errf("%s is not a directory that is there, so there is no folder to work in and nothing was started.", cwd)
		return nil, cli.ExitFailure
	}
	folder, err := trust.Resolve(cwd, p.home)
	if err != nil {
		p.errf("%v", err)
		return nil, cli.ExitFailure
	}
	st := &startup{p: p, inv: inv, folder: folder}
	st.gitPrograms = trust.GitPrograms(folder)

	// First as a folder that is not trusted: that load says what waits.
	st.load(false)
	if st.cfg == nil {
		return st, cli.ExitFailure
	}
	for _, h := range st.cfg.Held {
		st.held = append(st.held, trust.Holding{Kind: h.Kind, Text: h.Text, File: h.Origin.File, Line: h.Origin.Line})
	}
	st.held = append(st.held, st.gitPrograms...)

	record := st.trustStore()
	st.decision = trust.Decide(trust.Asked{
		Folder:      folder,
		Verdict:     record.Check(folder),
		TrustFolder: inv.TrustFolder,
		Interactive: interactive && len(ask) > 0,
		Held:        len(st.held),
	})
	trusted := st.decision.Trusted
	if st.decision.Ask {
		yes, quit := ask[0](trust.NewQuestion(folder, st.cfg.HeldFiles(), st.held))
		switch {
		case quit:
			return nil, cli.ExitInterrupted
		case yes:
			trusted = true
			if err := record.Trust(folder); err != nil && !errors.Is(err, trust.ErrSessionOnly) {
				p.warnf("the folder is trusted for this session, but that could not be recorded (%v), so gnosh will ask again next time.", err)
			}
		}
	}
	if trusted {
		st.load(true)
		if st.cfg == nil {
			return st, cli.ExitFailure
		}
	}
	if inv.Worktree {
		if code := st.enterTree(ctx, trusted); code != cli.ExitOK {
			return nil, code
		}
	}
	return st, cli.ExitOK
}

// bareTools are the tools of minimal mode (--bare).
var bareTools = []string{"Bash", "Read", "Edit", "Write"}

// load reads the configuration, treating the folder as trusted or not.
func (st *startup) load(trusted bool) {
	p, inv := st.p, st.inv
	flags := config.Flags{
		Model:           inv.Model,
		SmallModel:      inv.SmallModel,
		ReasoningEffort: inv.Effort,
		PermissionMode:  inv.PermissionMode,
		AllowedTools:    inv.AllowedTools,
		// (-y and its other spellings are folded into the mode by the
		// command line; this one only puts the mode within reach.)
		AllowSkipPermissions: inv.AllowSkipPermissions,
		DisallowedTools:      inv.DisallowedTools,
		ToolsGiven:           inv.ToolsGiven,
		Tools:                inv.Tools,
		SettingSources:       inv.SettingSources,
		SafeMode:             inv.SafeMode,
		Debug:                inv.Debug,
	}
	if inv.Bare {
		// Minimal mode is safe mode with the tools cut down to running
		// commands and reading and changing files, unless --tools names
		// others.
		flags.Bare = true
		if !inv.ToolsGiven {
			flags.ToolsGiven, flags.Tools = true, slices.Clone(bareTools)
		}
	}
	// What was typed as a path is meant from where it was typed, not from
	// the folder --cwd names.
	for _, d := range inv.AddDirs {
		flags.AddDirs = append(flags.AddDirs, p.absFrom(d))
	}
	switch {
	case inv.Settings == "":
	case inv.SettingsInline:
		flags.Settings = inv.Settings
	default:
		flags.Settings = p.absFrom(inv.Settings)
	}

	var env []string
	for _, f := range p.o.Features {
		env = append(env, f.Env...)
	}
	st.trusted = trusted
	st.inputs = config.Inputs{
		FS:            config.OS{},
		Environ:       p.o.Environ,
		Home:          p.home,
		Cwd:           st.folder.Start,
		ProjectRoot:   st.folder.Root,
		Trusted:       trusted,
		KeepUntrusted: trust.Restricts,
		Flags:         flags,
		Built: config.Built{
			Hooks:       true,
			HookEvents:  eventNames(firedEvents()),
			Permissions: true,
			Hosts:       true,
			Env:         true,
		},
		Tools:      p.toolNames(),
		KnownEnv:   env,
		Checks:     []config.Check{st.checkRules, st.checkHooks},
		LookupUser: lookupUser,
	}
	cfg, problems, err := config.Load(st.inputs)
	st.cfg, st.problems = cfg, problems
	if err != nil {
		st.cfg = nil
		var le *config.LoadError
		if !errors.As(err, &le) {
			st.problems = append(st.problems, config.Problem{Severity: config.Error, Message: err.Error()})
		}
	}
}

// eventNames returns hook events as plain words.
func eventNames(events []hooks.Event) []string {
	out := make([]string, len(events))
	for i, e := range events {
		out[i] = string(e)
	}
	return out
}

// lookupUser names a user id for a message.
func lookupUser(uid int) string {
	u, err := user.LookupId(strconv.Itoa(uid))
	if err != nil {
		return ""
	}
	return u.Username
}

// trustStore is the record of trusted folders.
func (st *startup) trustStore() *trust.Store {
	return &trust.Store{Dir: st.cfg.Paths.TrustDir, Home: st.p.home, Now: st.p.now}
}

// sessions is the store the folder's sessions are kept in: gnosh's own
// data directory.
func (st *startup) sessions() *transcript.Store {
	return &transcript.Store{Dir: st.sessionsDir(), Clock: st.p.now}
}

// sessionsDir is the root of the session store.
func (st *startup) sessionsDir() string {
	if st.runState != "" {
		return st.runState
	}
	return st.cfg.Paths.StateDir
}

// dirs lists the directories in use. session is a session's id, or "".
func (st *startup) dirs(session string) Dirs {
	paths := st.cfg.Paths
	d := Dirs{
		Home:        paths.Home,
		Cwd:         paths.Cwd,
		ProjectRoot: paths.ProjectRoot,
		Config:      paths.ConfigDir,
		State:       paths.StateDir,
		Cache:       paths.CacheDir,
		Project:     paths.DataDir,
		Sessions:    st.sessionsDir(),
	}
	if session != "" {
		d.Session = filepath.Join(st.sessions().ProjectDir(paths.Cwd), session)
	}
	return d
}

// printProblems prints everything the config load noticed, mistakes
// first, each on a line of its own. It reports whether any was a mistake.
func (st *startup) printProblems() bool {
	mistakes := config.Errors(st.problems)
	switch len(mistakes) {
	case 0:
	case 1:
		st.p.errf("the configuration has a mistake, so nothing was started:")
	default:
		st.p.errf("the configuration has %d mistakes, so nothing was started:", len(mistakes))
	}
	for _, pr := range mistakes {
		fmt.Fprintln(st.p.stderr, "  "+pr.String())
	}
	for _, pr := range config.Warnings(st.problems) {
		st.p.warnf("%s", pr.String())
	}
	return len(mistakes) > 0
}

// permissionOptions turns the configuration's permission settings into
// what the permission engine is made from. protected are more paths whose
// change always asks.
func (st *startup) permissionOptions(cfg *config.Config, protected []string) permission.Options {
	known, quiet, _ := st.p.featureTools()
	o := permission.Options{
		Mode:            permission.Mode(cfg.Permissions.Mode.V),
		Cwd:             cfg.Paths.Cwd,
		Home:            cfg.Paths.Home,
		Trusted:         cfg.Trusted,
		RestrictTools:   cfg.Permissions.Tools.Set,
		OnlyTools:       cfg.Permissions.Tools.V,
		KnownTools:      known,
		QuietTools:      quiet,
		DisableBypass:   cfg.Permissions.BypassDisabled.V,
		Resolve:         resolvePath,
		Glob:            globPaths,
		GitRunsPrograms: !cfg.Trusted && len(st.gitPrograms) > 0,
	}
	// gnosh's own files are never changed without a question: its config,
	// the data it keeps for the person (sessions among it), and whatever
	// file --settings named.
	o.Protected = append(o.Protected, cfg.Paths.ConfigDir, cfg.Paths.StateDir, cfg.Paths.DataDir)
	if st.runState != "" {
		o.Protected = append(o.Protected, st.runState)
	}
	if s := st.inputs.Flags.Settings; s != "" && !strings.HasPrefix(strings.TrimSpace(s), "{") {
		o.Protected = append(o.Protected, s)
	}
	o.Protected = append(o.Protected, protected...)
	// What is inside the work trees made for sub-agents (worktree.go) is
	// the project's, though they lie in its data folder; and so is a
	// session's own project when it is one of those work trees, kept.
	o.WorkTrees = []string{filepath.Join(cfg.Paths.DataDir, worktreesDir)}
	if outer := outerTrees(cfg.Paths.ProjectRoot, filepath.Base(cfg.Paths.DataDir)); outer != "" {
		o.WorkTrees = append(o.WorkTrees, outer)
	}

	for _, l := range cfg.Permissions.Layers {
		o.Layers = append(o.Layers, ruleLayer(l))
	}
	if len(cfg.Permissions.Dirs) > 0 {
		layer := permission.Layer{Scope: permission.ScopeCLI, Name: "additional directories"}
		for _, d := range cfg.Permissions.Dirs {
			layer.Dirs = append(layer.Dirs, d.V)
		}
		o.Layers = append(o.Layers, layer)
	}
	return o
}

// ruleLayer turns one source of rules of the configuration into the
// permission engine's.
func ruleLayer(l config.RuleLayer) permission.Layer {
	out := permission.Layer{Name: l.File, Base: l.BaseDir}
	switch l.Scope {
	case "user":
		out.Scope = permission.ScopeUser
	case "project":
		out.Scope = permission.ScopeProject
	case "local":
		out.Scope = permission.ScopeLocal
	default:
		out.Scope = permission.ScopeCLI
		if l.File != "" {
			out.Scope = permission.ScopeSettingsFlag
		}
	}
	if out.Name == "" {
		out.Name = "the command line"
	}
	texts := func(rules []config.Rule) []string {
		var s []string
		for _, r := range rules {
			s = append(s, r.Text)
		}
		return s
	}
	out.Allow, out.Ask, out.Deny = texts(l.Allow), texts(l.Ask), texts(l.Deny)
	return out
}

// checkRules is the permission package's own check of the rules, run on
// the finished Config with every other check: a rule the engine cannot
// apply is a mistake that names the rule and the line it is on.
func (st *startup) checkRules(cfg *config.Config) []config.Problem {
	_, err := permission.New(st.permissionOptions(cfg, nil))
	if err == nil {
		return nil
	}
	var out []config.Problem
	for _, e := range unjoin(err) {
		var re *permission.RuleError
		if !errors.As(e, &re) {
			out = append(out, config.Problem{Severity: config.Error, Message: e.Error()})
			continue
		}
		pr := config.Problem{Severity: config.Error, Where: re.Source,
			Message: fmt.Sprintf("the %s rule %q cannot be applied: %v", re.List, re.Rule, re.Err)}
		if o, ok := ruleOrigin(cfg, re); ok {
			pr.File, pr.Line = o.File, o.Line
			if o.File == "" {
				pr.Where = o.Name
			}
		}
		out = append(out, pr)
	}
	return out
}

// unjoin takes apart an error made with errors.Join.
func unjoin(err error) []error {
	if joined, ok := err.(interface{ Unwrap() []error }); ok {
		var out []error
		for _, e := range joined.Unwrap() {
			out = append(out, unjoin(e)...)
		}
		return out
	}
	return []error{err}
}

// ruleOrigin finds where the configuration wrote a rule the engine
// refused.
func ruleOrigin(cfg *config.Config, re *permission.RuleError) (config.Origin, bool) {
	for _, l := range cfg.Permissions.Layers {
		rules := l.Allow
		switch re.List {
		case permission.ListDeny:
			rules = l.Deny
		case permission.ListAsk:
			rules = l.Ask
		}
		for _, r := range rules {
			if r.Text == re.Rule {
				return r.Origin, true
			}
		}
	}
	return config.Origin{}, false
}

// hooksConfig turns the hooks the configuration holds in force into the
// hook runner's own form. A hook the runner would refuse comes back as a
// problem instead.
func hooksConfig(cfg *config.Config) (hooks.Config, []hooks.Problem) {
	var out hooks.Config
	var problems []hooks.Problem
	for _, h := range cfg.Hooks {
		hk, err := hooks.NewHook(hooks.Spec{
			Event:   h.Event,
			Command: h.Command,
			Args:    h.Args,
			Async:   h.Async,
			Matcher: h.Matcher,

			AsyncRewake: h.AsyncRewake,

			StatusMessage: h.StatusMessage,

			URL:            h.URL,
			Headers:        h.Headers,
			AllowedEnvVars: h.AllowedEnvVars,
			Server:         h.Server,
			Tool:           h.Tool,
			Input:          h.Input,
			Timeout:        h.Timeout,
			Source:         h.Origin.String(),
			Global:         h.Scope == "user",
			If:             h.If,
		})
		if err != nil {
			var ce *hooks.ConfigError
			if errors.As(err, &ce) {
				problems = append(problems, ce.Problems...)
			} else {
				problems = append(problems, hooks.Problem{Source: h.Origin.String(), Message: err.Error()})
			}
			continue
		}
		out.Add(hk)
	}
	return out, problems
}

// hookOptions is what the hook runner is told about the program.
func (st *startup) hookOptions(cfg *config.Config) hooks.Options {
	_, _, guards := st.p.featureTools()
	base := st.p.o.Environ
	extra := cfg.CommandEnv()
	return hooks.Options{
		Fires:      firedEvents(),
		ProjectDir: cfg.Paths.ProjectRoot,
		StartDir:   cfg.Paths.Cwd,
		Environ:    func() []string { return overlayEnv(base, extra) },
		Now:        st.p.now,
		ToolNames:  st.p.toolNames(),
		Guards:     guards,
	}
}

// overlayEnv lays the env block of the settings over an environment:
// "NAME=value" sets a variable and "NAME" alone takes it away.
func overlayEnv(base, extra []string) []string {
	if len(extra) == 0 {
		return slices.Clone(base)
	}
	gone := map[string]bool{}
	for _, kv := range extra {
		name, _, _ := strings.Cut(kv, "=")
		gone[name] = true
	}
	out := make([]string, 0, len(base)+len(extra))
	for _, kv := range base {
		if name, _, _ := strings.Cut(kv, "="); !gone[name] {
			out = append(out, kv)
		}
	}
	for _, kv := range extra {
		if strings.Contains(kv, "=") {
			out = append(out, kv)
		}
	}
	return out
}

// checkHooks is the hook package's own check, run on the finished Config
// with every other check: a hook on a moment nobody fires, or one written
// in a way gnosh cannot run, is a mistake.
func (st *startup) checkHooks(cfg *config.Config) []config.Problem {
	hc, problems := hooksConfig(cfg)
	problems = append(problems, hooks.Check(hc, st.hookOptions(cfg))...)
	var out []config.Problem
	for _, hp := range problems {
		sev := config.Error
		if hp.Warning {
			sev = config.Warning
		}
		out = append(out, config.Problem{Severity: sev, Where: hp.Source, Message: hp.Message})
	}
	return out
}

// resolvePath follows the symbolic links of a cleaned absolute path and
// returns where it really leads. For a path that does not exist yet it
// resolves the part that does, and a link that points at nothing is
// followed to where it points: a write through it would land there.
func resolvePath(p string) (string, error) {
	return resolveDepth(filepath.Clean(p), 0)
}

func resolveDepth(p string, depth int) (string, error) {
	if depth > 40 {
		return "", fmt.Errorf("%s leads through too many symbolic links", p)
	}
	rest := ""
	for cur := p; ; {
		real, err := filepath.EvalSymlinks(cur)
		if err == nil {
			return filepath.Join(real, rest), nil
		}
		if !errors.Is(err, fs.ErrNotExist) {
			return "", err
		}
		if li, lerr := os.Lstat(cur); lerr == nil && li.Mode()&fs.ModeSymlink != 0 {
			target, rerr := os.Readlink(cur)
			if rerr != nil {
				return "", rerr
			}
			if !filepath.IsAbs(target) {
				target = filepath.Join(filepath.Dir(cur), target)
			}
			real, err := resolveDepth(filepath.Clean(target), depth+1)
			if err != nil {
				return "", err
			}
			return filepath.Join(real, rest), nil
		}
		parent := filepath.Dir(cur)
		if parent == cur {
			return p, nil
		}
		rest = filepath.Join(filepath.Base(cur), rest)
		cur = parent
	}
}

// globPaths expands a file name pattern the way a shell would, to cleaned
// absolute paths.
func globPaths(pattern string) ([]string, error) {
	matches, err := filepath.Glob(pattern)
	if err != nil {
		return nil, err
	}
	for i, m := range matches {
		matches[i] = filepath.Clean(m)
	}
	return matches, nil
}
