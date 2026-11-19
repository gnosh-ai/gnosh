package config

import (
	"encoding/json"
	"fmt"
	"maps"
	"slices"
	"strings"
	"time"
)

// String describes the whole configuration, one setting to a line, each
// with where it came from, under the keys a person writes in gnosh.json.
// It is what /status and gnosh doctor can print, and it is safe to log: no
// key, header value, variable value or password is in it.
func (c *Config) String() string {
	if c == nil {
		return "no configuration"
	}
	var b strings.Builder
	line := func(format string, args ...any) {
		fmt.Fprintf(&b, format, args...)
		b.WriteByte('\n')
	}
	from := func(o Origin) string { return "  [" + o.String() + "]" }

	line("directories:")
	line("  config   %s", c.Paths.ConfigDir)
	line("  state    %s", c.Paths.StateDir)
	line("  cache    %s", c.Paths.CacheDir)
	line("  project  %s", c.Paths.ProjectRoot)
	line("  data     %s", c.Paths.DataDir)
	line("files read:")
	for _, f := range c.Files {
		note := ""
		if f.Held {
			note = "  (held: the folder is not trusted)"
		}
		line("  %-8s %-12s %s%s", f.Role, f.Scope, f.Path, note)
	}
	if len(c.Files) == 0 {
		line("  none")
	}

	line("providers:")
	for _, p := range c.Providers {
		state := "usable"
		switch {
		case p.Disabled:
			state = "disabled"
		case p.LeftOut != "":
			state = "left out: " + p.LeftOut
		}
		line("  %s (%s) %q: %s%s", p.ID, p.Type, p.Name, state, from(p.Origin))
		for _, e := range p.Endpoints {
			where := "elsewhere"
			if e.Local {
				where = "local"
			}
			line("    %s (%s; first piece %s, between pieces %s)", e.Display, where, limit(e.Timeouts.FirstToken), limit(e.Timeouts.Idle))
		}
		switch {
		case p.KeyFrom != "":
			line("    key: from %s", p.KeyFrom)
		case !p.APIKey.Empty():
			line("    key: set")
		default:
			line("    key: none")
		}
		for _, h := range p.Headers {
			line("    header %s: %s", h.Name, h.Value)
		}
		if len(p.ExtraBody) > 0 {
			line("    extra body fields: %s", list(slices.Sorted(maps.Keys(p.ExtraBody))))
		}
		line("    listModels: %v", p.ListModels.V)
	}
	if len(c.Providers) == 0 {
		line("  none")
	}

	line("models:")
	for _, m := range c.Models {
		about := "  contextWindow: ask the server"
		if m.ContextWindow.Set {
			about = fmt.Sprintf("  contextWindow: %d", m.ContextWindow.V)
		}
		if m.Name != m.ID {
			about = fmt.Sprintf(" %q", m.Name) + about
		}
		if m.MaxOutputTokens.Set {
			about += fmt.Sprintf("  maxOutputTokens: %d", m.MaxOutputTokens.V)
		}
		if m.ThinkingLevels.Set {
			about += "  thinkingLevels: [" + list(m.ThinkingLevels.V) + "]"
		}
		if m.ThinkingBudget.Set {
			about += fmt.Sprintf("  thinkingBudget: %d", m.ThinkingBudget.V)
		}
		line("  %s%s%s", m.Ref(), about, from(m.Origin))
	}
	if len(c.Models) == 0 {
		line("  none registered")
	}
	for _, sl := range []struct {
		name string
		s    Slot
	}{{"model", c.Large}, {"smallModel", c.Small}} {
		if !sl.s.Model.Set {
			line("%s: not chosen", sl.name)
			continue
		}
		how := fmt.Sprintf("  thinking: %s%s", sl.s.Thinking.V, from(sl.s.Thinking.Origin))
		if sl.s.Temperature.Set {
			how += fmt.Sprintf("  temperature: %v%s", sl.s.Temperature.V, from(sl.s.Temperature.Origin))
		}
		line("%s: %s%s%s", sl.name, sl.s.Model.V, from(sl.s.Model.Origin), how)
	}

	line("options:")
	for _, o := range c.Options.All() {
		line("  %-28s %s%s", o.Key, o.Text, from(o.Origin))
	}

	line("permissions:")
	line("  mode %s%s", c.Permissions.Mode.V, from(c.Permissions.Mode.Origin))
	for _, l := range c.Permissions.Layers {
		for _, set := range []struct {
			name  string
			rules []Rule
		}{{"deny", l.Deny}, {"ask", l.Ask}, {"allow", l.Allow}} {
			for _, r := range set.rules {
				line("  %-5s %s%s", set.name, r.Text, from(r.Origin))
			}
		}
	}
	for _, d := range c.Permissions.Dirs {
		line("  directory %s%s", d.V, from(d.Origin))
	}
	if c.Permissions.Tools.Set {
		line("  only these tools: %s%s", list(c.Permissions.Tools.V), from(c.Permissions.Tools.Origin))
	}

	line("hooks:")
	if c.HooksOff != "" {
		line("  off: %s", c.HooksOff)
	}
	for _, h := range c.AllHooks {
		matcher := ""
		if h.Matcher != "" {
			matcher = " [" + h.Matcher + "]"
		}
		line("  %s%s: %s%s", h.Event, matcher, h.CommandLine(), from(h.Origin))
	}
	if len(c.AllHooks) == 0 {
		line("  none")
	}

	if len(c.Hosts) > 0 {
		line("hosts:")
		for _, h := range c.Hosts {
			about := ""
			if h.Description != "" {
				about = ": " + h.Description
			}
			line("  %s → %s%s%s", h.Name, h.SSH, about, from(h.Origin))
		}
	}
	if len(c.Env) > 0 {
		line("env (values not shown):")
		for _, v := range c.Env {
			line("  %s%s", v.Name, from(v.Origin))
		}
	}

	line("instruction files:")
	if c.Context.Off {
		line("  off (safe mode)")
	} else {
		line("  in each directory down to %s: %s", c.Context.Cwd, list(slices.Concat(c.Context.ProjectNames, c.Context.LocalNames)))
		for _, g := range c.Context.Global {
			line("  global %s", g.V)
		}
		for _, e := range c.Context.Extra {
			line("  also %s%s", e.V, from(e.Origin))
		}
	}

	if !c.Trusted && len(c.Held) > 0 {
		line("held until the folder is trusted:")
		for _, h := range c.Held {
			line("  %s%s", fmt.Sprintf("%q", h.Text), from(h.Origin))
		}
	}
	return c.secrets.scrub(b.String())
}

// limit shows a time limit, or "no limit".
func limit(d time.Duration) string {
	if d == 0 {
		return "no limit"
	}
	return d.String()
}

// OptionValue is one option as /status and /config show it.
type OptionValue struct {
	Key string
	// Text is the value in force, as text; the default when nothing set
	// it.
	Text string
	// Set reports whether something other than the default set it.
	Set    bool
	Origin Origin
	// What the option is for, the value when nothing sets it, and the
	// values it takes, in a few words each.
	What, Default, Takes string
	// List is an option that holds a list, which /config does not change.
	List bool
	// Choices are the values the option takes, where they are few: true
	// and false, or the words of one that takes one of a few words.
	Choices []string
	// JSON is the value in force as a config file would hold it, for an
	// option that holds one value and is set; nil otherwise.
	JSON json.RawMessage
}

// All lists every option with its value in force and where it came from,
// in the order of the table of options.
func (o *Options) All() []OptionValue {
	out := make([]OptionValue, 0, len(optionDefs))
	for i := range optionDefs {
		d := &optionDefs[i]
		v := OptionValue{Key: d.key, What: d.what, Default: d.def, Takes: d.takes(), List: d.list != nil, Choices: d.choices()}
		switch {
		case d.truth != nil:
			x := d.truth(o)
			v.Text, v.Set, v.Origin, v.JSON = fmt.Sprint(x.V), x.Set, x.Origin, jsonOf(x.V)
		case d.num != nil:
			x := d.num(o)
			v.Text, v.Set, v.Origin, v.JSON = fmt.Sprint(x.V), x.Set, x.Origin, jsonOf(x.V)
		case d.text != nil:
			x := d.text(o)
			v.Text, v.Set, v.Origin, v.JSON = x.V, x.Set, x.Origin, jsonOf(x.V)
			if d.kind == oURL && x.Set {
				// An address can carry a password; it is shown without.
				v.Text = DisplayURL(x.V)
			}
		case d.list != nil:
			items := *d.list(o)
			vals := make([]string, len(items))
			for j, it := range items {
				vals[j] = it.V
			}
			v.Text, v.Set = list(vals), len(items) > 0
			if len(items) > 0 {
				v.Origin = items[len(items)-1].Origin
			}
		}
		if !v.Set {
			v.Text, v.JSON = d.def, nil
		}
		out = append(out, v)
	}
	return out
}

// jsonOf is a value as JSON. (A boolean, a number and a string always have
// one.)
func jsonOf(v any) json.RawMessage {
	b, _ := json.Marshal(v)
	return b
}

// HooksJSON returns the hooks in force as a hooks block in Claude Code's
// shape (event, then matcher groups, then command handlers), for a hook
// runner that reads that shape. Timeouts are in seconds.
func (c *Config) HooksJSON() []byte {
	type handler struct {
		Type            string            `json:"type"`
		Command         string            `json:"command,omitempty"`
		Args            *[]string         `json:"args,omitempty"`
		URL             string            `json:"url,omitempty"`
		Headers         map[string]string `json:"headers,omitempty"`
		AllowedEnvVars  []string          `json:"allowedEnvVars,omitempty"`
		Server          string            `json:"server,omitempty"`
		Tool            string            `json:"tool,omitempty"`
		Input           json.RawMessage   `json:"input,omitempty"`
		Prompt          string            `json:"prompt,omitempty"`
		Model           string            `json:"model,omitempty"`
		ContinueOnBlock bool              `json:"continueOnBlock,omitempty"`
		Timeout         float64           `json:"timeout,omitempty"`
	}
	type group struct {
		Matcher string    `json:"matcher,omitempty"`
		Hooks   []handler `json:"hooks"`
	}
	block := map[string][]group{}
	for _, h := range c.Hooks {
		hd := handler{Type: "command", Command: h.Command, Timeout: h.Timeout.Seconds()}
		if h.URL != "" {
			hd = handler{Type: "http", URL: h.URL, Headers: h.Headers, AllowedEnvVars: h.AllowedEnvVars, Timeout: h.Timeout.Seconds()}
		}
		if h.Server != "" {
			hd = handler{Type: "mcp_tool", Server: h.Server, Tool: h.Tool, Input: h.Input, Timeout: h.Timeout.Seconds()}
		}
		if h.Prompt != "" {
			hd = handler{Type: "prompt", Prompt: h.Prompt, Model: h.Model, ContinueOnBlock: h.ContinueOnBlock, Timeout: h.Timeout.Seconds()}
			if h.Agent {
				hd = handler{Type: "agent", Prompt: h.Prompt, Model: h.Model, Timeout: h.Timeout.Seconds()}
			}
		}
		if h.Args != nil {
			// (An empty list is still the exec form.)
			args := h.Args
			hd.Args = &args
		}
		block[h.Event] = append(block[h.Event], group{Matcher: h.Matcher, Hooks: []handler{hd}})
	}
	data, err := json.Marshal(block)
	if err != nil {
		return []byte("{}")
	}
	return data
}
