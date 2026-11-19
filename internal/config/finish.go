package config

import (
	"fmt"
	"maps"
	"net/url"
	"path/filepath"
	"slices"
	"strings"
	"time"
)

// The time limits of a model request when nothing sets them (LM-1, LM-2,
// OQ-13). A server on the user's own machine may take as long as it likes
// to start answering, because a local model reads a long prompt in silence
// for minutes; a service elsewhere gets the usual ten minutes.
const (
	// DefaultConnectTimeout is how long reaching a server may take. The
	// spec names no figure; this is the one it gives for reaching another
	// machine over ssh.
	DefaultConnectTimeout = 20 * time.Second
	// LocalIdleTimeout is how long a local server's answer may go quiet
	// between pieces.
	LocalIdleTimeout = 900 * time.Second
	// RemoteFirstTokenTimeout is how long a service elsewhere may take to
	// start answering.
	RemoteFirstTokenTimeout = 10 * time.Minute
	// RemoteIdleTimeout is how long a service's answer may go quiet
	// between pieces.
	RemoteIdleTimeout = 60 * time.Second
	// firstPieceFactor is how many times the requestTimeout option a
	// server may take before the first piece of its answer.
	firstPieceFactor = 5
)

// The limits that keep context files safe (CFG-20 to CFG-22).
const (
	contextImportDepth    = 4
	contextMaxFileBytes   = 4 << 20
	contextNestedMaxChars = 40000
)

// contextNames are the instruction files read in the working directory and
// in every directory above it, in the order they are read inside one
// directory, and contextLocalNames the person's private ones, read after
// them.
var (
	contextNames      = []string{"AGENTS.md", "GNOSH.md", "CLAUDE.md"}
	contextLocalNames = []string{"CLAUDE.local.md"}
)

// finish applies the environment and the command line on top of the
// files, fills in what nothing set, and returns the Config.
func (ld *loader) finish() *Config {
	in, b := ld.in, ld.b
	cfg := &Config{Paths: ld.paths, Files: ld.files, Trusted: in.Trusted, Held: ld.held, secrets: ld.secrets}

	// Rules and directories given on the command line.
	flags := ld.source("", LayerFlag, "command line", in.Cwd, in.Cwd)
	for _, given := range in.Flags.AllowedTools {
		for _, r := range SplitRules(given) {
			flags.rule("allow", r, flagOrigin("--allowedTools"))
		}
	}
	for _, given := range in.Flags.DisallowedTools {
		for _, r := range SplitRules(given) {
			flags.rule("deny", r, flagOrigin("--disallowedTools"))
		}
	}
	for _, given := range in.Flags.AddDirs {
		dir, o := absPath(given, in.Home, in.Cwd), flagOrigin("--add-dir")
		if info, err := in.FS.Stat(dir); err != nil || !info.IsDir() {
			ld.probs.at(Error, o, "%s is not a directory that is there. Each directory given to --add-dir must exist.", dir)
			continue
		}
		flags.add(op{kind: KindAddDir, text: "additional directory " + dir, origin: o, feature: FeaturePermissions, what: "an additional directory",
			apply: func(b *builder) { addItem(&b.dirs, dir, o) }})
	}
	for _, o := range flags.ops {
		b.run(o)
	}

	cfg.Switches = ld.switches()
	cfg.Limits = ld.limits()
	ld.models()
	ld.providers(cfg.Limits)
	cfg.Permissions = ld.permissions()

	for _, p := range b.providers {
		cfg.Providers = append(cfg.Providers, *p)
	}
	for _, m := range b.models {
		cfg.Models = append(cfg.Models, *m)
	}
	cfg.Large, cfg.Small = b.large, b.small
	cfg.Fallback = b.fallback
	cfg.Options = b.opts
	cfg.Hosts = b.hosts
	ld.probs.list = append(ld.probs.list, checkHosts(cfg.Hosts)...)

	cfg.AllHooks = b.hooks
	cfg.Agent = b.agent
	switch {
	case len(b.hooks) == 0:
	case cfg.Switches.SafeMode.V:
		cfg.HooksOff = "safe mode"
	case b.hooksOff.V:
		cfg.HooksOff = "disableAllHooks in " + b.hooksOff.Origin.String()
	default:
		cfg.Hooks = b.hooks
	}
	if b.statusLine.Set {
		switch {
		case cfg.Switches.SafeMode.V:
			cfg.StatusLineOff = "safe mode"
		case b.hooksOff.V:
			cfg.StatusLineOff = "disableAllHooks in " + b.hooksOff.Origin.String()
		default:
			sl := b.statusLine.V
			cfg.StatusLine = &sl
		}
	}
	cfg.Attribution = b.attribution.V
	if b.fileSuggestion.Set {
		switch {
		case cfg.Switches.SafeMode.V:
			cfg.FileSuggestionOff = "safe mode"
		case b.hooksOff.V:
			cfg.FileSuggestionOff = "disableAllHooks in " + b.hooksOff.Origin.String()
		default:
			fs := b.fileSuggestion.V
			cfg.FileSuggestion = &fs
		}
	}
	for _, name := range slices.Sorted(maps.Keys(b.env)) {
		cfg.Env = append(cfg.Env, b.env[name])
	}
	cfg.SkillOverrides = maps.Clone(b.skillOver)
	for name, v := range b.skillOverProj {
		// The project's file wins as a later file does where the folder is
		// trusted; where it is not, only where it takes away at least as
		// much as the person's own setting (a cloned repository turning a
		// skill the person switched off back on: review, 16 November).
		have, ok := cfg.SkillOverrides[name]
		if !ok || in.Trusted || slices.Index(SkillVisibilities, v.V) >= slices.Index(SkillVisibilities, have.V) {
			cfg.SkillOverrides[name] = v
		}
	}

	cfg.Context = ContextFiles{
		Off:            cfg.Switches.SafeMode.V,
		ProjectNames:   slices.Clone(contextNames),
		LocalNames:     slices.Clone(contextLocalNames),
		Cwd:            in.Cwd,
		ProjectRoot:    in.ProjectRoot,
		Extra:          b.opts.ProjectInstructionFiles,
		Exclude:        b.opts.ClaudeMdExcludes,
		ImportDepth:    contextImportDepth,
		MaxFileBytes:   contextMaxFileBytes,
		NestedMaxChars: contextNestedMaxChars,
	}
	cfg.Context.Global = []ListItem{{V: filepath.Join(ld.paths.ConfigDir, "GNOSH.md")}, {V: ld.agentsFile}}
	cfg.Context.Global = append(cfg.Context.Global, b.opts.UserInstructionFiles...)

	cfg.BadEnv = ld.badEnv
	return cfg
}

// switches reads gnosh's own on/off variables and flags.
func (ld *loader) switches() Switches {
	in, b := ld.in, ld.b
	var s Switches
	for _, name := range []string{"CLAUDE_CODE_SAFE_MODE", "GNOSH_SAFE_MODE"} {
		if on, set := ld.onOff(name); set {
			s.SafeMode.put(on, envOrigin(name))
		}
	}
	if in.Flags.SafeMode {
		s.SafeMode.put(true, flagOrigin("--safe-mode"))
	}
	if in.Flags.Bare {
		s.SafeMode.put(true, flagOrigin("--bare"))
	}
	if on, set := ld.onOff("GNOSH_DEBUG_PAGE"); set {
		s.DebugPage.put(on, envOrigin("GNOSH_DEBUG_PAGE"))
	}
	if in.Flags.Debug {
		b.opts.Debug.put(true, flagOrigin("--debug"))
	}

	return s
}

// heldNote is added to a message about a missing provider when the folder
// has settings that are held, since that is the likely reason.
func (ld *loader) heldNote() string {
	if slices.ContainsFunc(ld.held, func(h Held) bool { return h.Kind == KindProvider || h.Kind == KindModel }) {
		return " This folder's own settings are not in force, because the folder is not trusted; a provider it adds is one of the things held."
	}
	return ""
}

// modelFlag reads -m or --small-model: "provider/model", or a bare model
// name.
func (ld *loader) modelFlag(flag, text string) (ModelRef, bool) {
	b, o := ld.b, flagOrigin(flag)
	text = strings.TrimSpace(text)
	if text == "" {
		ld.probs.at(Error, o, "this flag needs a model after it, as model or provider/model.")
		return ModelRef{}, false
	}
	if len(b.providers) == 0 {
		ld.probs.at(Error, o, "%s was asked for, and no provider is configured, so there is no model to use. Add one to %s, as in {%s}.%s", showName(text), ld.paths.UserSettings, ProviderExample, ld.heldNote())
		return ModelRef{}, false
	}
	if prov, id, ok := strings.Cut(text, "/"); ok && id != "" && b.provider(prov) != nil {
		return ModelRef{Provider: prov, ID: id}, true
	}
	// A bare model name. It belongs to the provider that registers it, or
	// to the only provider there is. Otherwise whoever lists the
	// providers' models settles it.
	var owners []string
	for _, m := range b.models {
		if m.ID == text {
			owners = append(owners, m.Provider)
		}
	}
	if len(owners) > 1 {
		ld.probs.at(Error, o, "the model %s is registered on more than one provider (%s). Say which, as provider/model.", showName(text), list(owners))
		return ModelRef{}, false
	}
	if len(owners) == 1 {
		return ModelRef{Provider: owners[0], ID: text}, true
	}
	var enabled []string
	for _, p := range b.providers {
		if !p.Disabled {
			enabled = append(enabled, p.ID)
		}
	}
	if len(enabled) == 1 {
		return ModelRef{Provider: enabled[0], ID: text}, true
	}
	return ModelRef{ID: text}, true
}

// models settles the two model choices and their thinking levels.
func (ld *loader) models() {
	in, b := ld.in, ld.b
	if in.Flags.Model != "" {
		if ref, ok := ld.modelFlag("--model", in.Flags.Model); ok {
			b.large.Model.put(ref, flagOrigin("--model"))
			// A run that names one model must never send a request to
			// another by default, so the side jobs follow it.
			if in.Flags.SmallModel == "" {
				b.small.Model.put(ref, flagOrigin("--model"))
			}
		}
	}
	if in.Flags.SmallModel != "" {
		if ref, ok := ld.modelFlag("--small-model", in.Flags.SmallModel); ok {
			b.small.Model.put(ref, flagOrigin("--small-model"))
		}
	}
	if level := in.Flags.ReasoningEffort; level != "" {
		if !levelPattern.MatchString(level) {
			ld.probs.at(Error, flagOrigin("--reasoning-effort"), "%s is not a thinking level: a level is a lower-case word such as off, on, low, medium or high.", showName(level))
		} else {
			b.large.Thinking.put(level, flagOrigin("--reasoning-effort"))
		}
	}

	for _, slot := range []struct {
		name   string
		sl     *Slot
		before Value[ModelRef]
	}{{"model", &b.large, b.largeBefore}, {"small model", &b.small, b.smallBefore}} {
		sl := slot.sl
		if sl.Model.Set {
			ref, o := sl.Model.V, sl.Model.Origin
			// A choice saved by gnosh itself may have gone stale. It never
			// stops startup: it is dropped, with a word, and the choice
			// it had outranked counts again.
			stale := func(format string, args ...any) {
				sev := Error
				if o.Layer == LayerState {
					sev = Warning
					sl.Model = slot.before
				}
				ld.probs.at(sev, o, format, args...)
			}
			p := b.provider(ref.Provider)
			switch {
			case ref.Provider == "":
			case p == nil:
				stale("the %s chosen here, %s, is on a provider that is not configured, so it was not used. Choose a model with /model or -m. The providers are: %s.%s", slot.name, ref, b.providerIDs(), ld.heldNote())
			case p.ListModels.Set && !p.ListModels.V && b.model(ref) == nil:
				stale("the %s chosen here, %s, is not one provider %s lists, and that provider is set not to ask its server (\"listModels\": false). Register the model under \"models\", as %q: {}, or choose another.", slot.name, ref, ref.Provider, ref.String())
			}
		}
		if sl.Model.Set {
			if saved, ok := b.stateThinking[sl.Model.V.String()]; ok {
				sl.Thinking.put(saved.V, saved.Origin)
			}
			if m := b.model(sl.Model.V); m != nil && sl.Thinking.Set && len(m.ThinkingLevels.V) > 0 {
				level, levels := sl.Thinking.V, m.ThinkingLevels.V
				if level != "on" && level != "off" && !slices.Contains(levels, level) {
					if sl.Thinking.Origin.Layer == LayerState {
						ld.probs.at(Warning, sl.Thinking.Origin, "the thinking level saved for %s, %s, is not one of that model's levels (%s), so it was not used.", sl.Model.V, level, list(levels))
						sl.Thinking = Value[string]{}
					} else {
						ld.probs.at(Error, sl.Thinking.Origin, "the thinking level %s is not one of the levels of %s, which are: %s. (on and off are always understood, as the top and the bottom.)", showName(level), sl.Model.V, list(levels))
					}
				}
			}
		}
		if !sl.Thinking.Set {
			sl.Thinking.V = "on"
		}
	}
	ld.fallbacks()
}

// fallbacks settles "fallbackModel", which --fallback-model outranks, and
// checks that each model it names is on a provider that is configured.
func (ld *loader) fallbacks() {
	in, b := ld.in, ld.b
	if text := strings.TrimSpace(in.Flags.FallbackModel); text != "" {
		o := flagOrigin("--fallback-model")
		var refs []ModelRef
		ok := true
		for name := range strings.SplitSeq(text, ",") {
			ref, good := ld.modelFlag("--fallback-model", name)
			ok = ok && good
			if good && !slices.Contains(refs, ref) {
				refs = append(refs, ref)
			}
		}
		if len(refs) > MaxFallbacks {
			ld.probs.at(Error, o, "--fallback-model names %d models, and a turn falls back to %d at most. Keep the ones to try first.", len(refs), MaxFallbacks)
			ok = false
		}
		if ok {
			b.fallback.put(refs, o)
		}
	}
	for _, ref := range b.fallback.V {
		p := b.provider(ref.Provider)
		switch {
		case ref.Provider == "":
		case p == nil:
			ld.probs.at(Error, b.fallback.Origin, "the fallback model %s is on a provider that is not configured. The providers are: %s.%s", ref, b.providerIDs(), ld.heldNote())
		case p.ListModels.Set && !p.ListModels.V && b.model(ref) == nil:
			ld.probs.at(Error, b.fallback.Origin, "the fallback model %s is not one provider %s lists, and that provider is set not to ask its server (\"listModels\": false). Register the model under \"models\", as %q: {}, or name another.", ref, ref.Provider, ref.String())
		}
	}
}

// providers completes each provider: its addresses and their time limits,
// a key from the environment, whether to ask the server for its models,
// and whether anything in the configuration stands in its way.
func (ld *loader) providers(limits Limits) {
	b := ld.b
	for _, p := range b.providers {
		t := findProviderType(p.Type)
		if len(p.Endpoints) == 0 && t.url != "" {
			p.Endpoints = []Endpoint{{Given: t.url}}
		}
		allLocal := len(p.Endpoints) > 0
		for i := range p.Endpoints {
			e := &p.Endpoints[i]
			e.URL = apiRoot(e.Given, t)
			e.Display = DisplayURL(e.URL)
			if u, err := url.Parse(e.URL); err == nil {
				e.Local = IsLocal(u.Hostname())
			}
			e.Timeouts = ld.timeouts(e.Local, limits)
			allLocal = allLocal && e.Local
		}
		if p.APIKey.Empty() && t.keyVar != "" {
			if key := ld.env[t.keyVar]; key != "" {
				ld.secrets.add(key)
				p.APIKey, p.KeyFrom = NewSecret(key), "$"+t.keyVar
			}
		}
		if !p.ListModels.Set {
			p.ListModels.V = !slices.ContainsFunc(b.models, func(m *Model) bool { return m.Provider == p.ID })
		}
		at, hint := p.Origin, ""
		switch {
		case p.Disabled:
		case len(b.unset[p.ID]) > 0:
			// A value that could not be filled in is never used as if it
			// had been: the provider waits for the variable.
			for _, field := range []string{"baseUrl", "apiKey", "headers"} {
				u, ok := b.unset[p.ID][field]
				if !ok {
					continue
				}
				which := "variable " + u.names[0] + ", which is not set"
				if len(u.names) > 1 {
					which = "variables " + list(u.names) + ", which are not set"
				}
				p.LeftOut = fmt.Sprintf("its %q is filled in from the environment %s", field, which)
				at, hint = u.origin, fmt.Sprintf(" Set it, or write ${%s:-} for a value that is empty when it is not set.", u.names[0])
				break
			}
		case len(p.Endpoints) == 0:
			p.LeftOut = "no address was given for it: give it a \"baseUrl\""
		case p.APIKey.Empty() && t.keyVar != "" && !allLocal:
			p.LeftOut = fmt.Sprintf("it has no key: give it an \"apiKey\", or set %s in the environment", t.keyVar)
		}
		if p.LeftOut != "" {
			ld.probs.at(Warning, at, "provider %s is left out: %s. gnosh starts without it.%s", p.ID, p.LeftOut, hint)
		}
	}
}

// timeouts works out the time limits of a request to one address.
func (ld *loader) timeouts(local bool, limits Limits) Timeouts {
	t := Timeouts{Connect: DefaultConnectTimeout}
	if local {
		t.Idle = LocalIdleTimeout
	} else {
		t.FirstToken, t.Idle = RemoteFirstTokenTimeout, RemoteIdleTimeout
	}
	// The requestTimeout option sets the quiet time between pieces, and
	// five times it before the first piece; zero waits for ever.
	if rt := ld.b.opts.RequestTimeout; rt.Set {
		t.Idle = time.Duration(rt.V) * time.Second
		t.FirstToken = firstPieceFactor * t.Idle
		t.IdleFrom, t.FirstTokenFrom = rt.Origin, rt.Origin
	}
	// API_TIMEOUT_MS is for a model API that is not local. A local server
	// is never given a limit it did not ask for: that limit is what once
	// ended a ten-minute wait with "provider error".
	if at := limits.APITimeout; at.Set && !local {
		t.FirstToken, t.FirstTokenFrom = at.V, at.Origin
	}
	return t
}

// permissions settles the starting mode and gathers the rules.
func (ld *loader) permissions() Permissions {
	in, b := ld.in, ld.b
	perm := Permissions{Dirs: b.dirs, BypassDisabled: b.bypassDisabled, DenyInsteadOfPrompting: b.denyInstead}
	for _, l := range b.layers {
		if len(l.Allow)+len(l.Ask)+len(l.Deny) > 0 {
			perm.Layers = append(perm.Layers, *l)
		}
	}

	perm.Mode = Value[string]{V: "default"}
	if b.mode.Set {
		perm.Mode = b.mode
		perm.BypassAvailable = b.mode.V == "bypassPermissions" && b.modeFromUser
	}
	asked := perm.Mode.Origin
	if in.Flags.SkipPermissions {
		perm.Mode.put("bypassPermissions", flagOrigin("-y"))
		perm.BypassAvailable, asked = true, flagOrigin("-y")
	}
	if mode := in.Flags.PermissionMode; mode != "" {
		o := flagOrigin("--permission-mode")
		if mode == "manual" {
			mode = "default"
		}
		switch {
		case mode == "auto":
			ld.probs.at(Error, o, "the auto mode is not supported: it needs a second model to review each action, which gnosh does not have. The modes are: %s.", list(permissionModes))
		case !slices.Contains(permissionModes, mode):
			ld.probs.at(Error, o, "%s is not a permission mode. The modes are: %s.", showName(mode), list(permissionModes))
		default:
			perm.Mode.put(mode, o)
			if mode == "bypassPermissions" {
				asked = o
			}
		}
	}
	if in.Flags.AllowSkipPermissions {
		perm.BypassAvailable = true
		if !in.Flags.SkipPermissions && perm.Mode.V != "bypassPermissions" {
			asked = flagOrigin("--allow-dangerously-skip-permissions")
		}
	}
	if perm.Mode.V == "bypassPermissions" {
		perm.BypassAvailable = true
	}
	if b.bypassDisabled.V && perm.BypassAvailable {
		ld.probs.at(Error, asked, "permissions cannot be skipped here: disableBypassPermissionsMode is set in %s. Start without it, or take that setting out.", b.bypassDisabled.Origin)
	}

	if in.Flags.ToolsGiven {
		o := flagOrigin("--tools")
		names := []string{}
		for _, given := range in.Flags.Tools {
			names = append(names, SplitRules(given)...)
		}
		switch {
		case len(names) == 1 && names[0] == "default":
		case in.Tools == nil && len(names) > 0:
			ld.probs.at(Error, o, "this gnosh was started without its list of tool names, so the names given to --tools cannot be checked.")
		default:
			good := true
			for _, name := range names {
				if !slices.Contains(in.Tools, name) {
					ld.probs.at(Error, o, "%s is not a tool gnosh has. The tools are: %s. (--tools \"\" gives the model none, and --tools default gives it all.)", showName(name), list(in.Tools))
					good = false
				}
			}
			if good {
				perm.Tools.put(names, o)
			}
		}
	}
	return perm
}

// HeldFiles returns the project's config files that were read and are
// held because the folder is not trusted: the files the trust question
// names.
func (c *Config) HeldFiles() []string {
	var out []string
	for _, f := range c.Files {
		if f.Held {
			out = append(out, f.Path)
		}
	}
	return out
}

// Usable returns the providers that can be used as far as the
// configuration can tell: not disabled, and not left out.
func (c *Config) Usable() []Provider {
	var out []Provider
	for _, p := range c.Providers {
		if p.Usable() {
			out = append(out, p)
		}
	}
	return out
}

// Provider finds a provider by its ID.
func (c *Config) Provider(id string) (Provider, bool) {
	for _, p := range c.Providers {
		if p.ID == id {
			return p, true
		}
	}
	return Provider{}, false
}

// Model finds a model that was registered by hand, under "models".
func (c *Config) Model(ref ModelRef) (Model, bool) {
	for _, m := range c.Models {
		if m.Provider == ref.Provider && m.ID == ref.ID {
			return m, true
		}
	}
	return Model{}, false
}

// Rules returns every rule of one list ("allow", "ask" or "deny") across
// the layers, weakest layer first.
func (p Permissions) Rules(which string) []Rule {
	var out []Rule
	for _, l := range p.Layers {
		switch which {
		case "allow":
			out = append(out, l.Allow...)
		case "ask":
			out = append(out, l.Ask...)
		case "deny":
			out = append(out, l.Deny...)
		}
	}
	return out
}

// CommandEnv returns the env block in the form a command's environment is
// built from: "NAME=value" sets a variable, and "NAME" alone takes it
// away. It holds the values themselves, so it is for starting commands and
// not for showing.
func (c *Config) CommandEnv() []string {
	out := make([]string, 0, len(c.Env))
	for _, v := range c.Env {
		if v.Value.Empty() {
			out = append(out, v.Name)
		} else {
			out = append(out, v.Name+"="+v.Value.Reveal())
		}
	}
	return out
}
