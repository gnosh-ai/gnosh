package permission

import (
	"errors"
	"fmt"
	"path"
	"slices"
	"strings"
	"sync"
)

// Layer is one source of rules: a settings file, a flag, a skill.
type Layer struct {
	// Scope says what kind of source this is.
	Scope Scope
	// Name is how the source is shown to a person: a file's path, or
	// "--allowedTools". It becomes Decision.Source.
	Name string
	// Base is the directory a rule's /path is read from: the directory of
	// the settings file. Left empty, project, local, command-line, skill
	// and session layers use the working directory; a user or --settings
	// layer with no Base cannot hold /path rules.
	Base string
	// FromProject marks a layer the project folder supplied. A
	// ScopeProject layer always counts as one; a local settings file that
	// is tracked in git or reached through a link must set it.
	FromProject bool
	// Allow, Ask and Deny are the rules, as written.
	Allow, Ask, Deny []string
	// Dirs are additional working directories (--add-dir, or the
	// settings key additionalDirectories): cleaned absolute paths.
	Dirs []string
	// DefaultMode is the settings key defaultMode, or "".
	DefaultMode string
}

// Options is everything the engine is told. It reads nothing by itself.
type Options struct {
	// Mode is the mode asked for on the command line. Empty means: take
	// defaultMode from the layers, and ModeDefault when none sets it.
	Mode Mode
	// Layers are the rule sources, in any order.
	Layers []Layer
	// Cwd is the primary working directory: a cleaned absolute path with
	// links resolved.
	Cwd string
	// Home is the user's home directory, for ~/ in rules and commands.
	Home string
	// Trusted says whether the folder is trusted. While it is not, the
	// layers the project supplied can only take away: their deny and ask
	// rules apply, their allow rules, directories and mode do not.
	Trusted bool
	// RestrictTools and OnlyTools are --tools: when RestrictTools is set,
	// only the built-in tools named in OnlyTools exist. MCP tools are
	// not affected.
	RestrictTools bool
	OnlyTools     []string
	// KnownTools names the program's tools that this package has no entry
	// for (they ask unless a rule allows them). QuietTools names those of
	// them that never need asking.
	KnownTools []string
	QuietTools []string
	// Protected lists more paths whose change always asks: gnosh's own
	// config, state and data directories, a file given to --settings,
	// and what a feature keeps for itself (the mem store). Each covers
	// itself and everything under it.
	Protected []string
	// WorkTrees are the folders the git work trees gnosh makes for
	// sub-agents are in: the project's data folder's worktrees, and, for a
	// session that runs in such a work tree (one that was kept), the
	// folder that one lies in. What is inside one of those work trees is a
	// checkout of the project and is judged as the project's files are:
	// the protected folder the work trees lie in does not protect it,
	// while a protected name inside the work tree (its .git, its own
	// .gnosh) still does.
	WorkTrees []string
	// DisableBypass forbids ModeBypass (the settings key
	// disableBypassPermissionsMode).
	DisableBypass bool
	// Resolve follows symbolic links in a cleaned absolute path and
	// returns where it really leads; for a path that does not exist yet
	// it resolves the part that does. Nil means the paths the engine is
	// given are already resolved. An error means the path cannot be
	// resolved, and nothing about it is allowed without asking.
	Resolve func(path string) (string, error)
	// Glob expands a file name pattern to cleaned absolute paths: every
	// path it matches, the names that start with a dot among them, as
	// filepath.Glob returns them (the engine leaves those out itself
	// where the shell would; see "What the engine is told" in doc.go).
	// Nil means patterns cannot be checked, so a read-only command with
	// one asks.
	Glob func(pattern string) ([]string, error)
	// GitRunsPrograms says the repository's own git settings name
	// programs that git would run (a file-system monitor, filters, diff
	// drivers) and the folder is not trusted: git commands then ask.
	GitRunsPrograms bool
	// RepoOf returns the git directory of the repository a folder is in
	// (its .git, or where a .git file points), cleaned and with its links
	// followed, or "" for none. With it, git commands ask while the
	// shell's folder is in a repository that is not one of the working
	// directories' own, nor a work tree or submodule of one (whose git
	// directories lie inside theirs): that repository's settings could
	// name programs git runs, even for git status. Nil tells no
	// repository apart.
	RepoOf func(dir string) string
}

// layer is a Layer with its rules made ready.
type layer struct {
	Layer
	base string
	// held is set for a project layer in a folder that is not trusted:
	// whatever it would grant waits.
	held             bool
	deny, ask, allow []*crule
}

// Engine decides, for each tool call, whether it runs, is refused, or a
// person is asked first. It is safe for use from several goroutines.
type Engine struct {
	mu        *sync.RWMutex
	mode      Mode
	cwd       string // the primary working directory
	cur       string // the shell's current directory
	home      string
	trusted   bool
	noBypass  bool
	names     names
	restrict  bool
	only      map[string]bool
	protected []string
	workTrees []string // Options.WorkTrees as given and with its links followed, as guardPath keys them
	resolve   func(string) (string, error)
	glob      func(string) ([]string, error)
	gitRuns   bool
	repoOf    func(string) string
	layers    []*layer
	session   *layer
	saved     *layer
	notes     []string
}

// RuleError is a rule that cannot be applied. It names the rule and where
// it was written.
type RuleError struct {
	// Rule is the rule as written.
	Rule string
	// Source is the layer's name.
	Source string
	// List is the list the rule was in.
	List List
	// Err says what is wrong and what to write instead.
	Err error
}

// Error names the rule, the source and list it was in, and what is wrong.
func (e *RuleError) Error() string {
	return fmt.Sprintf("permission rule %q in %s (%s list): %v", e.Rule, e.Source, e.List, e.Err)
}

// Unwrap returns what is wrong with the rule.
func (e *RuleError) Unwrap() error { return e.Err }

// New builds an engine. Every rule that cannot be applied is reported, all
// of them at once, and no engine is returned: a rule that looks configured
// is in force, or startup stops.
func New(o Options) (*Engine, error) {
	if !strings.HasPrefix(o.Cwd, "/") {
		return nil, fmt.Errorf("permission: the working directory %q is not an absolute path", o.Cwd)
	}
	e := &Engine{
		mu:       &sync.RWMutex{},
		cwd:      path.Clean(o.Cwd),
		home:     o.Home,
		trusted:  o.Trusted,
		noBypass: o.DisableBypass,
		names:    newNames(o.KnownTools, o.QuietTools),
		restrict: o.RestrictTools,
		only:     map[string]bool{},
		resolve:  o.Resolve,
		glob:     o.Glob,
		gitRuns:  o.GitRunsPrograms,
		repoOf:   o.RepoOf,
	}
	e.cur = e.cwd
	if e.home != "" {
		e.home = path.Clean(e.home)
	}
	for _, t := range o.OnlyTools {
		e.only[CanonicalTool(t)] = true
	}
	for _, p := range o.Protected {
		if !strings.HasPrefix(p, "/") {
			continue
		}
		p = path.Clean(p)
		e.protected = append(e.protected, guardPath(p))
		// A protected file or folder that is a link (a settings file
		// linked from a dotfiles repository, a data folder linked
		// elsewhere) protects what it leads to as well: that is the file
		// the settings are read from.
		if o.Resolve != nil {
			if r, err := o.Resolve(p); err == nil && r != p {
				e.protected = append(e.protected, guardPath(r))
			}
		}
	}
	for _, trees := range o.WorkTrees {
		if trees = path.Clean(trees); !strings.HasPrefix(trees, "/") {
			continue
		}
		// A folder of work trees counts only where it is what it says: in
		// a data folder that has no other protected folder's name (.git),
		// and not itself a link, which would carry the exemption somewhere
		// else (a repository can ship ".gnosh/worktrees -> ..").
		data := path.Base(path.Dir(trees))
		ok, real := data == ".gnosh" || !ProtectsName(data), trees
		if ok && o.Resolve != nil {
			parent, err1 := o.Resolve(path.Dir(trees))
			r, err2 := o.Resolve(trees)
			ok, real = err1 == nil && err2 == nil && r == path.Join(parent, path.Base(trees)), r
		}
		if !ok {
			continue
		}
		for _, g := range []string{guardPath(trees), guardPath(real)} {
			if !slices.Contains(e.workTrees, g) {
				e.workTrees = append(e.workTrees, g)
			}
		}
	}

	var errs []error
	for i := range o.Layers {
		ly, err := e.newLayer(o.Layers[i])
		if err != nil {
			errs = append(errs, err)
		}
		e.layers = append(e.layers, ly)
	}
	e.session = &layer{Layer: Layer{Scope: ScopeSession, Name: SessionLayer}, base: e.cwd}
	e.saved = &layer{Layer: Layer{Scope: ScopeLocal, Name: KeptLayer}, base: e.cwd}
	if len(errs) > 0 {
		return nil, errors.Join(errs...)
	}

	mode, err := e.startMode(o.Mode)
	if err != nil {
		return nil, err
	}
	e.mode = mode
	return e, nil
}

// newLayer compiles one layer's rules.
func (e *Engine) newLayer(l Layer) (*layer, error) {
	ly := &layer{Layer: l, base: l.Base}
	if l.Name == "" {
		ly.Name = string(l.Scope)
	}
	if l.Scope == ScopeProject {
		ly.FromProject = true
	}
	ly.held = ly.FromProject && !e.trusted
	if ly.base == "" {
		switch l.Scope {
		case ScopeUser, ScopeSettingsFlag:
		default:
			ly.base = e.cwd
		}
	} else {
		ly.base = path.Clean(ly.base)
	}
	var errs []error
	for _, d := range l.Dirs {
		if !strings.HasPrefix(d, "/") {
			errs = append(errs, fmt.Errorf("additional directory %q in %s is not an absolute path", d, ly.Name))
		}
	}
	add := func(list List, rules []string, into *[]*crule) {
		seen := map[string]bool{}
		for _, s := range rules {
			if seen[s] {
				continue
			}
			seen[s] = true
			r, err := ParseRule(s)
			var c *crule
			if err == nil {
				c, err = e.compile(r, list, ly)
			}
			if err != nil {
				errs = append(errs, &RuleError{Rule: s, Source: ly.Name, List: list, Err: err})
				continue
			}
			*into = append(*into, c)
		}
	}
	add(ListDeny, l.Deny, &ly.deny)
	add(ListAsk, l.Ask, &ly.ask)
	add(ListAllow, l.Allow, &ly.allow)
	return ly, errors.Join(errs...)
}

// startMode picks the mode a session starts in: the one asked for on the
// command line, else defaultMode from the settings by their precedence,
// else ModeDefault. A project can never switch prompts off.
func (e *Engine) startMode(asked Mode) (Mode, error) {
	if asked != "" {
		m, fell, err := ParseMode(string(asked))
		if err != nil {
			return "", err
		}
		if fell {
			e.notes = append(e.notes, "gnosh has no \"auto\" mode; the session starts in \"default\"")
		}
		if m == ModeBypass && e.noBypass {
			return "", errors.New("bypassPermissions is switched off by the setting disableBypassPermissionsMode")
		}
		return m, nil
	}
	for _, sc := range []Scope{ScopeSettingsFlag, ScopeLocal, ScopeProject, ScopeUser} {
		for _, ly := range e.layers {
			if ly.Scope != sc || ly.DefaultMode == "" {
				continue
			}
			if ly.held {
				e.notes = append(e.notes, fmt.Sprintf("defaultMode in %s waits until the folder is trusted", ly.Name))
				continue
			}
			m, fell, err := ParseMode(ly.DefaultMode)
			if err != nil {
				return "", fmt.Errorf("defaultMode in %s: %w", ly.Name, err)
			}
			repo := ly.FromProject || sc == ScopeLocal || sc == ScopeProject
			switch {
			case fell:
				e.notes = append(e.notes, fmt.Sprintf("defaultMode \"auto\" in %s: gnosh has no such mode; the session starts in \"default\"", ly.Name))
			case m == ModeBypass && repo:
				e.notes = append(e.notes, fmt.Sprintf("defaultMode \"bypassPermissions\" in %s is ignored: a project's settings cannot switch prompts off", ly.Name))
				continue
			case m == ModeBypass && e.noBypass:
				e.notes = append(e.notes, fmt.Sprintf("defaultMode \"bypassPermissions\" in %s is ignored: disableBypassPermissionsMode is set", ly.Name))
				continue
			}
			return m, nil
		}
	}
	return ModeDefault, nil
}

// Mode returns the current mode.
func (e *Engine) Mode() Mode {
	e.mu.RLock()
	defer e.mu.RUnlock()
	return e.mode
}

// SetMode changes the mode. It refuses a mode gnosh does not have, and
// ModeBypass when the settings forbid it. Whether the session may enter
// bypass at all (it must have been started with it available) is the
// caller's to check.
func (e *Engine) SetMode(m Mode) error {
	if !m.valid() {
		_, _, err := ParseMode(string(m))
		if err == nil {
			err = fmt.Errorf("permission mode %q cannot be set directly", m)
		}
		return err
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	if m == ModeBypass && e.noBypass {
		return errors.New("bypassPermissions is switched off by the setting disableBypassPermissionsMode")
	}
	e.mode = m
	return nil
}

// SetCwd tells the engine the shell's current directory, which relative
// paths in a command are read from. Rules keep their anchor at the primary
// working directory.
func (e *Engine) SetCwd(dir string) {
	if !strings.HasPrefix(dir, "/") {
		return
	}
	e.mu.Lock()
	e.cur = path.Clean(dir)
	e.mu.Unlock()
}

// AddDir adds a working directory for the rest of the session (/add-dir).
func (e *Engine) AddDir(dir string) error {
	if !strings.HasPrefix(dir, "/") {
		return fmt.Errorf("additional directory %q is not an absolute path", dir)
	}
	e.mu.Lock()
	e.session.Dirs = append(e.session.Dirs, path.Clean(dir))
	e.mu.Unlock()
	return nil
}

// SetTrusted records that the folder has been trusted (or no longer is).
// The project's allow rules and directories start or stop applying with
// the next call.
func (e *Engine) SetTrusted(trusted bool) {
	e.mu.Lock()
	e.trusted = trusted
	for _, ly := range e.layers {
		ly.held = ly.FromProject && !trusted
	}
	e.mu.Unlock()
}

// Grant adds an allow rule that a person approved at a prompt. With
// GrantSession it lasts until the session ends; with GrantLocal the caller
// also writes it to the project's local settings file. The rule must be
// one an allow list accepts.
func (e *Engine) Grant(rule string, scope GrantScope) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	ly := e.session
	switch scope {
	case GrantSession:
	case GrantLocal:
		ly = e.saved
	default:
		return fmt.Errorf("permission: %q is not a way to keep an approval", scope)
	}
	_, err := e.keepIn(ly, rule, ListAllow)
	return err
}

// The names of the session's own layers, as Decision.Source and Explain
// give them: the approvals for this session alone, and the rules kept for
// the project while it runs (GrantLocal, Keep).
const (
	SessionLayer = "approved for this session"
	KeptLayer    = "approved at a prompt and kept"
)

// Keep puts rule on list (allow, ask or deny) among the rules kept for
// the project, where an approval kept at a prompt goes (GrantLocal): in
// force at once. added is false when it was there already. The caller
// writes it down.
func (e *Engine) Keep(rule string, list List) (added bool, err error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.keepIn(e.saved, rule, list)
}

// Unkeep takes back a rule Keep added, from the kept rules alone.
func (e *Engine) Unkeep(rule string, list List) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.saved.drop(rule, list)
}

// keepIn adds rule to the list of the layer ly, once; added is false when
// it was there already.
func (e *Engine) keepIn(ly *layer, rule string, list List) (added bool, err error) {
	cs, texts := ly.list(list)
	if cs == nil {
		return false, fmt.Errorf("permission: %q is not a list of rules", list)
	}
	r, err := ParseRule(rule)
	if err == nil {
		for _, c := range *cs {
			if c.Rule.String() == r.String() {
				return false, nil
			}
		}
	}
	var c *crule
	if err == nil {
		c, err = e.compile(r, list, ly)
	}
	if err != nil {
		return false, &RuleError{Rule: rule, Source: ly.Name, List: list, Err: err}
	}
	*cs = append(*cs, c)
	*texts = append(*texts, rule)
	return true, nil
}

// Drop takes rule off list in the session's own layers (approved for the
// session, kept at a prompt) and in every layer whose Name is one of
// sources, and reports from how many places it was taken.
func (e *Engine) Drop(rule string, list List, sources ...string) int {
	e.mu.Lock()
	defer e.mu.Unlock()
	n := 0
	for _, ly := range append(slices.Clone(e.layers), e.session, e.saved) {
		if ly != e.session && ly != e.saved && !slices.Contains(sources, ly.Name) {
			continue
		}
		n += ly.drop(rule, list)
	}
	return n
}

// drop takes rule off the layer's list, and says how many times it was
// there.
func (ly *layer) drop(rule string, list List) int {
	want := rule
	if r, err := ParseRule(rule); err == nil {
		want = r.String()
	}
	cs, texts := ly.list(list)
	if cs == nil {
		return 0
	}
	n := 0
	*cs = slices.DeleteFunc(*cs, func(c *crule) bool {
		if c.Rule.String() == want {
			n++
			return true
		}
		return false
	})
	*texts = slices.DeleteFunc(*texts, func(s string) bool {
		r, err := ParseRule(s)
		return err == nil && r.String() == want
	})
	return n
}

// list is the layer's rules on one list, compiled and as written.
func (ly *layer) list(list List) (*[]*crule, *[]string) {
	switch list {
	case ListAllow:
		return &ly.allow, &ly.Allow
	case ListAsk:
		return &ly.ask, &ly.Ask
	case ListDeny:
		return &ly.deny, &ly.Deny
	}
	return nil, nil
}

// AddLayer adds a source of rules while the session runs: a skill's
// allowed-tools for the turn that runs it, a settings file that appeared.
// Its rules are held to the same standard as New's.
func (e *Engine) AddLayer(l Layer) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	ly, err := e.newLayer(l)
	if err != nil {
		return err
	}
	e.layers = append(e.layers, ly)
	return nil
}

// RemoveLayers drops every layer of the given scope that was passed to
// New or AddLayer, as when the turn a skill's grants were for is over.
// Approvals given at a prompt are not layers of this kind and stay.
func (e *Engine) RemoveLayers(scope Scope) {
	e.mu.Lock()
	defer e.mu.Unlock()
	kept := e.layers[:0:0]
	for _, ly := range e.layers {
		if ly.Scope != scope {
			kept = append(kept, ly)
		}
	}
	e.layers = kept
}

// ReadDenied reports whether a Read deny rule covers the cleaned absolute
// path, as written or as resolved. File discovery and search results
// leave such files out.
func (e *Engine) ReadDenied(p string) bool {
	e.mu.RLock()
	defer e.mu.RUnlock()
	req := &request{}
	return e.pathHit(ListDeny, toolRead, e.expand(req, []string{p})) != nil
}

// all returns every layer, the session's own included.
func (e *Engine) all() []*layer {
	out := make([]*layer, 0, len(e.layers)+2)
	out = append(out, e.layers...)
	return append(out, e.saved, e.session)
}

// rules returns the rules of one list that are in force.
func (e *Engine) rules(list List) []*crule {
	var out []*crule
	for _, ly := range e.all() {
		switch list {
		case ListDeny:
			out = append(out, ly.deny...)
		case ListAsk:
			out = append(out, ly.ask...)
		case ListAllow:
			if !ly.held {
				out = append(out, ly.allow...)
			}
		}
	}
	return out
}

// workDirs returns the working directories: the primary one first, then
// the additional ones that are in force.
func (e *Engine) workDirs() []string {
	out := []string{e.cwd}
	for _, ly := range e.all() {
		if ly.held {
			continue
		}
		for _, d := range ly.Dirs {
			if strings.HasPrefix(d, "/") {
				out = append(out, path.Clean(d))
			}
		}
	}
	return out
}

// Dirs returns the working directories in force: the primary one first,
// then the additional ones.
func (e *Engine) Dirs() []string {
	e.mu.RLock()
	defer e.mu.RUnlock()
	return e.workDirs()
}

// ToolEnabled reports whether the model should be offered the tool at
// all. It is false when --tools left the tool out, or a deny rule names
// the whole tool (Bash, mcp__*, *).
func (e *Engine) ToolEnabled(name string) bool {
	e.mu.RLock()
	defer e.mu.RUnlock()
	return e.toolOff(CanonicalTool(name)) == nil
}

// offReason says why a tool is switched off.
type offReason struct {
	rule *crule
	why  string
}

func (e *Engine) toolOff(canon string) *offReason {
	if e.restrict && !isMCP(canon) && !e.only[canon] {
		return &offReason{why: "--tools does not list " + canon}
	}
	family := canon
	if canon == toolMonitor {
		family = toolBash
	}
	for _, c := range e.rules(ListDeny) {
		switch c.kind {
		case kindTool:
			if c.canon == canon || c.canon == family {
				return &offReason{rule: c}
			}
		case kindNameGlob:
			if globMatch(c.canon, canon) {
				return &offReason{rule: c}
			}
		}
	}
	return nil
}

// RuleInfo is one rule as Explain reports it.
type RuleInfo struct {
	// Rule is the rule as written.
	Rule string
	// List is the list it is in.
	List List
	// Source and Scope say where it came from.
	Source string
	Scope  Scope
	// Held is set when the rule waits for the folder to be trusted.
	Held bool
}

// Explanation is everything in force, for a /permissions view and for
// doctor.
type Explanation struct {
	// Mode is the current mode.
	Mode Mode
	// Trusted says whether the folder is trusted.
	Trusted bool
	// Dirs are the working directories, the primary one first.
	Dirs []string
	// HeldDirs are additional directories that wait for trust.
	HeldDirs []string
	// Rules lists every rule: deny first, then ask, then allow, each in
	// the order of the layers.
	Rules []RuleInfo
	// Notes are things worth telling the person once: a mode that was
	// replaced, a rule that looks wider than its author meant.
	Notes []string
}

// Explain reports the mode, the working directories and every rule with
// its source.
func (e *Engine) Explain() Explanation {
	e.mu.RLock()
	defer e.mu.RUnlock()
	x := Explanation{Mode: e.mode, Trusted: e.trusted, Dirs: e.workDirs(), Notes: append([]string(nil), e.notes...)}
	for _, list := range []List{ListDeny, ListAsk, ListAllow} {
		for _, ly := range e.all() {
			var rs []*crule
			switch list {
			case ListDeny:
				rs = ly.deny
			case ListAsk:
				rs = ly.ask
			default:
				rs = ly.allow
			}
			for _, c := range rs {
				x.Rules = append(x.Rules, RuleInfo{
					Rule: c.Rule.String(), List: list, Source: ly.Name, Scope: ly.Scope,
					Held: ly.held && list == ListAllow,
				})
				if list == ListAllow && c.kind == kindCommand && !c.cmd.exact() && (len(c.cmd.segs) > 2 || c.cmd.segs[1] != "") {
					x.Notes = append(x.Notes, fmt.Sprintf("allow rule %s in %s has a * before its end, so it lets through more commands than a prefix rule would", c.Rule, ly.Name))
				}
			}
		}
	}
	for _, ly := range e.layers {
		if ly.held {
			x.HeldDirs = append(x.HeldDirs, ly.Dirs...)
		}
	}
	return x
}
