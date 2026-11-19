package config

import (
	"slices"
)

// builder holds the configuration while the ops of each file are put in
// force, weakest file first. A mistake that only shows against what the
// files before set (a model on a provider that does not exist) is reported
// from here, at the place that made it.
type builder struct {
	in    *Inputs
	probs *problems

	providers []*Provider
	// unset holds, for each provider and each of its fields that is filled
	// in from the environment, the variables that are not set. A provider
	// with any is left out, and says which.
	unset  map[string]map[string]unsetVars
	models []*Model
	// large is the session's model ("model"), and small the one for the
	// short side jobs ("smallModel").
	large Slot
	small Slot
	// stateThinking is the thinking level the state file holds for each
	// model, keyed by provider/id.
	stateThinking map[string]Value[string]
	// largeBefore and smallBefore are the model choices as they stood
	// before the state file's were put on top, to fall back on when the
	// state file's choice turns out to be stale.
	largeBefore, smallBefore Value[ModelRef]
	// fallback is "fallbackModel" or --fallback-model.
	fallback Value[[]ModelRef]

	hosts    []Host
	hooks    []Hook
	hooksOff Value[bool]
	// agent is the kind of agent the session runs as.
	agent Value[string]
	// statusLine is the status line command; a later file wins.
	statusLine Value[StatusLine]
	// fileSuggestion is the @ file suggestion command; a later file wins.
	fileSuggestion Value[FileSuggestion]
	// attribution is what commits and pull requests are signed with; a
	// later file wins.
	attribution Value[Attribution]

	layers []*RuleLayer
	mode   Value[string]
	// modeFromUser reports that the mode in force came from the user's own
	// settings file, which is what makes bypassPermissions available.
	modeFromUser   bool
	bypassDisabled Value[bool]
	denyInstead    Value[bool]
	dirs           []ListItem

	env           map[string]EnvVar
	skillOver     map[string]Value[string] // the person's own files
	skillOverProj map[string]Value[string] // the project's
	opts          Options
}

// newBuilder returns a builder holding the defaults.
func newBuilder(in *Inputs, probs *problems) *builder {
	return &builder{
		in:            in,
		probs:         probs,
		unset:         map[string]map[string]unsetVars{},
		stateThinking: map[string]Value[string]{},
		env:           map[string]EnvVar{},
		skillOver:     map[string]Value[string]{},
		skillOverProj: map[string]Value[string]{},
		opts:          defaultOptions(),
	}
}

// run puts one op in force. An op for a part of gnosh the binary does not
// have is a mistake: the setting would look configured and do nothing.
func (b *builder) run(o op) {
	if o.feature != "" && !b.in.Built.has(o.feature) {
		words := featureWords[o.feature]
		b.probs.at(Error, o.origin, "%s is configured here, and this gnosh has no %s yet, so the setting would look in force and do nothing. gnosh does not start with a setting like that: take it out, or use a gnosh that has %s.", o.what, words, words)
	}
	o.apply(b)
}

// provider finds a provider by ID.
func (b *builder) provider(id string) *Provider {
	for _, p := range b.providers {
		if p.ID == id {
			return p
		}
	}
	return nil
}

// providerIDs lists the providers so far, for a message.
func (b *builder) providerIDs() string {
	if len(b.providers) == 0 {
		return "none"
	}
	ids := make([]string, len(b.providers))
	for i, p := range b.providers {
		ids[i] = p.ID
	}
	return list(ids)
}

// addProvider declares a provider, or changes the one a weaker file
// declared under the same ID: what this file gives replaces what was
// there, field by field, and what it leaves out stays.
func (b *builder) addProvider(id string, ch providerChange, o Origin) {
	p := b.provider(id)
	if p == nil {
		if ch.typ == nil {
			b.probs.at(Error, o, "the provider %s has no \"type\", and no file read before this one declares it. A provider needs its \"type\" where it is first declared, so that gnosh knows how to talk to it. The types are: %s.", id, list(providerTypeNames()))
			return
		}
		p = &Provider{ID: id, Name: id}
		b.providers = append(b.providers, p)
	}
	if ch.typ != nil {
		p.Type, p.Wire = ch.typ.name, ch.typ.wire
	}
	if ch.name != nil {
		if p.Name = *ch.name; p.Name == "" {
			p.Name = id
		}
	}
	if ch.hasURLs {
		p.Endpoints = nil
		for _, u := range ch.urls {
			p.Endpoints = append(p.Endpoints, Endpoint{Given: u})
		}
	}
	if ch.key != nil {
		p.APIKey, p.KeyFrom = *ch.key, ch.keyFrom
	}
	if ch.noListModels {
		p.ListModels = Value[bool]{}
	}
	if ch.listModels != nil {
		p.ListModels.put(*ch.listModels, ch.listModelsAt)
	}
	if ch.hasExtraBody {
		p.ExtraBody = ch.extraBody
	}
	if ch.hasHeaders {
		p.Headers = ch.headers
	}
	if ch.disabled != nil {
		p.Disabled = *ch.disabled
	}
	// A field this file gives stands for what a weaker file gave, and so
	// do the variables it could not fill in.
	for _, field := range ch.filled {
		delete(b.unset[id], field)
	}
	for field, vars := range ch.unset {
		if b.unset[id] == nil {
			b.unset[id] = map[string]unsetVars{}
		}
		b.unset[id][field] = vars
	}
	p.Origin = o
}

// removeProvider takes away a provider, the models registered on it, and
// any model choice that named it.
func (b *builder) removeProvider(id string, o Origin) {
	if b.provider(id) == nil {
		b.probs.at(Error, o, "\"providers\" takes %s away with null, and no file read before this one declares a provider of that name, so there is nothing to take away. The providers so far are: %s.", id, b.providerIDs())
		return
	}
	b.providers = slices.DeleteFunc(b.providers, func(p *Provider) bool { return p.ID == id })
	b.models = slices.DeleteFunc(b.models, func(m *Model) bool { return m.Provider == id })
	delete(b.unset, id)
	for _, sl := range []*Slot{&b.large, &b.small} {
		if sl.Model.Set && sl.Model.V.Provider == id {
			sl.Model = Value[ModelRef]{}
		}
	}
}

// model finds a registered model.
func (b *builder) model(ref ModelRef) *Model {
	for _, m := range b.models {
		if m.Provider == ref.Provider && m.ID == ref.ID {
			return m
		}
	}
	return nil
}

// addModel registers a model on an existing provider, or changes the one a
// weaker file registered under the same name, field by field.
func (b *builder) addModel(ref ModelRef, ch modelChange, o Origin) {
	if b.provider(ref.Provider) == nil {
		b.probs.at(Error, o, "\"models\" registers %s, and there is no provider named %s. Declare it under \"providers\", in this file or in one read before it. The providers so far are: %s.", ref, ref.Provider, b.providerIDs())
		return
	}
	m := b.model(ref)
	if m == nil {
		m = &Model{Provider: ref.Provider, ID: ref.ID, Name: ref.ID}
		b.models = append(b.models, m)
	}
	if ch.name != nil {
		if m.Name = *ch.name; m.Name == "" {
			m.Name = ref.ID
		}
	}
	if ch.noWindow {
		m.ContextWindow = Value[int]{}
	}
	if ch.window != nil {
		m.ContextWindow.put(*ch.window, ch.windowAt)
	}
	if ch.noMax {
		m.MaxOutputTokens = Value[int]{}
	}
	if ch.maxOutput != nil {
		m.MaxOutputTokens.put(*ch.maxOutput, ch.maxAt)
	}
	if ch.noBudget {
		m.ThinkingBudget = Value[int]{}
	}
	if ch.budget != nil {
		m.ThinkingBudget.put(*ch.budget, ch.budgetAt)
	}
	if ch.noLevels {
		m.ThinkingLevels = Value[[]string]{}
	}
	if ch.hasLevels {
		m.ThinkingLevels.put(ch.levels, ch.levelsAt)
	}
	m.Origin = o
}

// removeModel takes a registered model away.
func (b *builder) removeModel(ref ModelRef, o Origin) {
	if b.model(ref) == nil {
		b.probs.at(Error, o, "\"models\" takes %s away with null, and no file read before this one registers a model of that name, so there is nothing to take away.", ref)
		return
	}
	b.models = slices.DeleteFunc(b.models, func(m *Model) bool { return m.Provider == ref.Provider && m.ID == ref.ID })
}

// setSlot puts in force what a file says of "model" or "smallModel".
func (b *builder) setSlot(which string, ch slotChange, o Origin) {
	sl := &b.large
	if which == "smallModel" {
		sl = &b.small
	}
	if ch.all {
		*sl = Slot{}
		return
	}
	if ch.noRef {
		sl.Model = Value[ModelRef]{}
	}
	if ch.ref != nil {
		if b.provider(ch.ref.Provider) == nil {
			b.probs.at(Error, ch.refAt, "%q names %s, and there is no provider named %s. Declare it under \"providers\", in this file or in one read before it. The providers so far are: %s.", which, ch.ref, ch.ref.Provider, b.providerIDs())
			return
		}
		sl.Model.put(*ch.ref, ch.refAt)
	}
	if ch.noThinking {
		sl.Thinking = Value[string]{}
	}
	if ch.thinking != nil {
		sl.Thinking.put(*ch.thinking, ch.thinkingAt)
	}
	if ch.noTemperature {
		sl.Temperature = Value[float64]{}
	}
	if ch.temperature != nil {
		sl.Temperature.put(*ch.temperature, ch.temperatureAt)
	}
}

// addHost takes in the declaration of a machine. Every declaration is kept,
// in the order it was made: what is wrong with them, a name declared twice
// among it, is found on the hosts as they stand when every file has been
// read (checkHosts), so that it is found whichever file said it.
func (b *builder) addHost(h Host) {
	b.hosts = append(b.hosts, h)
}

// removeHost takes a declared machine away.
func (b *builder) removeHost(name string, o Origin) {
	if !slices.ContainsFunc(b.hosts, func(h Host) bool { return h.Name == name }) {
		b.probs.at(Error, o, "\"hosts\" takes %s away with null, and no file read before this one declares a host of that name, so there is nothing to take away.", showName(name))
		return
	}
	b.hosts = slices.DeleteFunc(b.hosts, func(h Host) bool { return h.Name == name })
}

// addHook adds a hook. Hooks add up: every file's hooks are kept, in the
// order they were read.
func (b *builder) addHook(h Hook) {
	if b.in.Built.Hooks && b.in.Built.HookEvents != nil && !slices.Contains(b.in.Built.HookEvents, h.Event) {
		b.probs.at(Error, h.Origin, "a hook is configured on %s, and this gnosh does not fire that event yet, so the hook would never run. gnosh does not start with a hook like that: take it out, or use a gnosh that fires %s. This one fires: %s.", h.Event, h.Event, list(b.in.Built.HookEvents))
	}
	b.hooks = append(b.hooks, h)
}

// addRule adds a permission rule to the layer of the file or flag it came
// from.
func (b *builder) addRule(which string, r Rule, scope, file, dir string) {
	var layer *RuleLayer
	for _, l := range b.layers {
		if l.Scope == scope && l.File == file {
			layer = l
		}
	}
	if layer == nil {
		layer = &RuleLayer{Scope: scope, File: file, BaseDir: dir}
		b.layers = append(b.layers, layer)
	}
	rules := &layer.Allow
	switch which {
	case "ask":
		rules = &layer.Ask
	case "deny":
		rules = &layer.Deny
	}
	if !slices.ContainsFunc(*rules, func(have Rule) bool { return have.Text == r.Text }) {
		*rules = append(*rules, r)
	}
}

// setEnv sets a variable of the env block. A later file wins.
func (b *builder) setEnv(v EnvVar) {
	if have, ok := b.env[v.Name]; ok && v.Origin.Layer < have.Origin.Layer {
		return
	}
	b.env[v.Name] = v
}

// setSkillOverride sets how far the skill name is seen. A later file wins;
// a project's file is kept apart, since in a folder that is not trusted it
// may only take away (finish).
func (b *builder) setSkillOverride(name string, v Value[string]) {
	m := b.skillOver
	if v.Origin.Layer == LayerProject {
		m = b.skillOverProj
	}
	if have, ok := m[name]; ok && v.Origin.Layer < have.Origin.Layer {
		return
	}
	m[name] = v
}

// sweep takes out of the builder everything a project's file put there
// that an untrusted folder may not contribute. Ops of a held file are never
// applied in the first place; this runs after every merge all the same, so
// that no later step can put back what is held. It reports what it found,
// which should be nothing.
func (b *builder) sweep(keep func(kind string) bool, projectFiles []string) (removed []string) {
	ok := func(kind string) bool { return keep != nil && keep(kind) }
	from := func(o Origin) bool { return o.File != "" && slices.Contains(projectFiles, o.File) }
	note := func(what string) { removed = append(removed, what) }

	if !ok(KindProvider) {
		b.providers = slices.DeleteFunc(b.providers, func(p *Provider) bool {
			if from(p.Origin) {
				note("provider " + p.ID)
				delete(b.unset, p.ID)
				return true
			}
			return false
		})
		for _, p := range b.providers {
			if p.ListModels.Set && from(p.ListModels.Origin) {
				note("listModels of provider " + p.ID)
				p.ListModels = Value[bool]{}
			}
		}
	}
	if !ok(KindModel) {
		b.models = slices.DeleteFunc(b.models, func(m *Model) bool {
			if from(m.Origin) {
				note("model " + m.Ref())
				return true
			}
			return false
		})
		for _, m := range b.models {
			if m.ContextWindow.Set && from(m.ContextWindow.Origin) {
				note("contextWindow of model " + m.Ref())
				m.ContextWindow = Value[int]{}
			}
			if m.MaxOutputTokens.Set && from(m.MaxOutputTokens.Origin) {
				note("maxOutputTokens of model " + m.Ref())
				m.MaxOutputTokens = Value[int]{}
			}
			if m.ThinkingLevels.Set && from(m.ThinkingLevels.Origin) {
				note("thinkingLevels of model " + m.Ref())
				m.ThinkingLevels = Value[[]string]{}
			}
			if m.ThinkingBudget.Set && from(m.ThinkingBudget.Origin) {
				note("thinkingBudget of model " + m.Ref())
				m.ThinkingBudget = Value[int]{}
			}
		}
		for _, slot := range []struct {
			name string
			sl   *Slot
		}{{"model", &b.large}, {"smallModel", &b.small}} {
			sl := slot.sl
			if sl.Model.Set && from(sl.Model.Origin) {
				note(slot.name)
				sl.Model = Value[ModelRef]{}
			}
			if sl.Thinking.Set && from(sl.Thinking.Origin) {
				note("thinking of " + slot.name)
				sl.Thinking = Value[string]{}
			}
			if sl.Temperature.Set && from(sl.Temperature.Origin) {
				note("temperature of " + slot.name)
				sl.Temperature = Value[float64]{}
			}
		}
	}
	if !ok(KindHost) {
		b.hosts = slices.DeleteFunc(b.hosts, func(h Host) bool {
			if from(h.Origin) {
				note("host " + h.Name)
				return true
			}
			return false
		})
	}
	if !ok(KindHook) {
		b.hooks = slices.DeleteFunc(b.hooks, func(h Hook) bool {
			if from(h.Origin) {
				note("hook " + h.Event)
				return true
			}
			return false
		})
	}
	if !ok(KindHookOff) && b.hooksOff.Set && from(b.hooksOff.Origin) {
		note("disableAllHooks")
		b.hooksOff = Value[bool]{}
	}
	if !ok(KindFileSuggest) && b.fileSuggestion.Set && from(b.fileSuggestion.Origin) {
		note("fileSuggestion")
		b.fileSuggestion = Value[FileSuggestion]{}
	}
	if !ok(KindStatusLine) && b.statusLine.Set && from(b.statusLine.Origin) {
		note("statusLine")
		b.statusLine = Value[StatusLine]{}
	}
	for _, l := range b.layers {
		if !slices.Contains(projectFiles, l.File) {
			continue
		}
		if !ok(KindAllowRule) && len(l.Allow) > 0 {
			note("allow rules")
			l.Allow = nil
		}
		if !ok(KindAskRule) && len(l.Ask) > 0 {
			note("ask rules")
			l.Ask = nil
		}
		if !ok(KindDenyRule) && len(l.Deny) > 0 {
			note("deny rules")
			l.Deny = nil
		}
	}
	if !ok(KindMode) {
		if b.mode.Set && from(b.mode.Origin) {
			note("defaultMode")
			b.mode = Value[string]{}
		}
		if b.denyInstead.Set && from(b.denyInstead.Origin) {
			note("denyInsteadOfPrompting")
			b.denyInstead = Value[bool]{}
		}
	}
	if !ok(KindRestriction) && b.bypassDisabled.Set && from(b.bypassDisabled.Origin) {
		note("disableBypassPermissionsMode")
		b.bypassDisabled = Value[bool]{}
	}
	if !ok(KindAddDir) {
		b.dirs = slices.DeleteFunc(b.dirs, func(d ListItem) bool {
			if from(d.Origin) {
				note("additional directory")
				return true
			}
			return false
		})
	}
	if !ok(KindEnv) {
		for name, v := range b.env {
			if from(v.Origin) {
				note("env " + name)
				delete(b.env, name)
			}
		}
	}
	def := defaultOptions()
	for i := range optionDefs {
		d := &optionDefs[i]
		if ok(d.trustKind()) {
			continue
		}
		var origin Origin
		switch {
		case d.truth != nil:
			origin = d.truth(&b.opts).Origin
		case d.num != nil:
			origin = d.num(&b.opts).Origin
		case d.text != nil:
			origin = d.text(&b.opts).Origin
		case d.list != nil:
			items := d.list(&b.opts)
			before := len(*items)
			*items = slices.DeleteFunc(*items, func(it ListItem) bool { return from(it.Origin) })
			if len(*items) != before {
				note("options." + d.key)
			}
			continue
		}
		if from(origin) {
			note("options." + d.key)
			switch {
			case d.truth != nil:
				*d.truth(&b.opts) = *d.truth(&def)
			case d.num != nil:
				*d.num(&b.opts) = *d.num(&def)
			case d.text != nil:
				*d.text(&b.opts) = *d.text(&def)
			}
		}
	}
	slices.Sort(removed)
	return removed
}
