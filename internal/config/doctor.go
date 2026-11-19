package config

import (
	"context"
	"fmt"
	"path/filepath"
	"slices"
	"strings"
)

// Level says how much a doctor finding matters.
type Level int

const (
	// Fine: the check passed.
	Fine Level = iota
	// Note: worth knowing; a turn can still run.
	Note
	// Fail: a turn cannot run until this is fixed. gnosh doctor exits 1.
	Fail
)

// String is "ok", "note" or "FAIL".
func (l Level) String() string {
	switch l {
	case Note:
		return "note"
	case Fail:
		return "FAIL"
	}
	return "ok"
}

// Finding is one line of gnosh doctor's report.
type Finding struct {
	// Area is what was checked: config, providers, models, hooks, tools,
	// hosts, trust or environment.
	Area  string
	Level Level
	// Text is the finding, in one line.
	Text string
}

// String is the finding as doctor prints it.
func (f Finding) String() string {
	return fmt.Sprintf("%-4s %-11s %s", f.Level, f.Area, f.Text)
}

// Probes are the ways Doctor looks at the machine. The caller supplies
// them, so that Doctor itself never opens a connection, starts a program or
// reads a file. A nil probe means that part is not checked, and the report
// says so.
//
// Doctor must not disturb a running model server (CFG-61, LM-16). A server
// that holds one model at a time loads a model as soon as a request names
// it, and throws out the one in use. So the two probes that reach a server
// are held to exactly this:
//
//   - Connect opens a connection to the address and closes it. It sends
//     nothing at all.
//   - ListModels sends one request, a GET of the address's models list
//     (Endpoint.URL + "/models"), and reads the answer. It never asks about
//     one model by name (no "/models/<id>"), never asks for a server's
//     slots or properties ("/slots", "/props"), and never sends a chat,
//     completion, tokenizing or embedding request, because each of those
//     names a model.
type Probes struct {
	// Connect reports whether something accepts a connection at the
	// address of url. Nothing is sent.
	Connect func(ctx context.Context, url string) error
	// ListModels returns the names of the models the server at e lists.
	ListModels func(ctx context.Context, p Provider, e Endpoint) ([]string, error)
	// LookPath finds a program by name the way it will be run, as
	// exec.LookPath does.
	LookPath func(name string) (string, error)
	// Exists reports whether a file is there.
	Exists func(path string) bool
	// TryHost logs in to a declared host and runs nothing there. It
	// returns the directory a command would start in, or, when a command
	// could not be run there, why not in plain words. It reaches another
	// machine, so it is given only when the person asked for the hosts to
	// be tried; with nil, Doctor lists the hosts and reaches none of them.
	TryHost func(ctx context.Context, h Host) (dir, problem string)
}

// CanRun reports whether the findings leave a turn able to run: none of
// them is a Fail.
func CanRun(findings []Finding) bool {
	return !slices.ContainsFunc(findings, func(f Finding) bool { return f.Level == Fail })
}

// Doctor checks that a turn could run with this configuration, and returns
// one line for each thing it looked at. Pass what Load returned: the Config
// (nil when Load refused the configuration) and the problems. With a nil
// Config the report is the mistakes themselves.
func Doctor(ctx context.Context, cfg *Config, problems []Problem, pr Probes) []Finding {
	var out []Finding
	add := func(area string, level Level, format string, args ...any) {
		text := strings.Join(strings.Fields(fmt.Sprintf(format, args...)), " ")
		out = append(out, Finding{Area: area, Level: level, Text: text})
	}

	if cfg == nil {
		for _, p := range Errors(problems) {
			add("config", Fail, "%s", p)
		}
		for _, p := range Warnings(problems) {
			add("config", Note, "%s", p)
		}
		if len(Errors(problems)) == 0 {
			add("config", Fail, "the configuration could not be loaded")
		}
		return out
	}

	// 1. Config files.
	for _, f := range cfg.Files {
		switch {
		case f.Held:
			add("config", Note, "held: %s (the project's %s). The folder is not trusted, so only what restricts the agent was taken from it.", f.Path, f.Role)
		default:
			add("config", Fine, "read %s (%s %s)", f.Path, f.Scope, f.Role)
		}
	}
	if len(cfg.Files) == 0 {
		add("config", Note, "no config file was found, so gnosh runs on its defaults. Your own settings belong in %s.", cfg.Paths.UserSettings)
	}
	for _, p := range Warnings(problems) {
		if slices.Contains(cfg.BadEnv, p) || strings.HasPrefix(p.Message, "provider ") && strings.Contains(p.Message, " is left out: ") {
			continue
		}
		add("config", Note, "%s", p)
	}
	if cfg.Switches.SafeMode.V {
		add("config", Note, "safe mode is on (%s): hooks, instruction files, skills on disk and custom commands are off.", cfg.Switches.SafeMode.Origin)
	}

	// 2. Providers. offers holds, for each provider that answered, the
	// models it is known to have; known says whether that list is whole.
	offers := map[string][]string{}
	known := map[string]bool{}
	answering := 0
	for _, p := range cfg.Providers {
		switch {
		case p.Disabled:
			add("providers", Note, "%s is disabled (%s).", p.ID, p.Origin)
			continue
		case p.LeftOut != "":
			add("providers", Note, "%s is left out: %s.", p.ID, p.LeftOut)
			continue
		}
		var listed []string
		for _, m := range cfg.Models {
			if m.Provider == p.ID {
				listed = append(listed, m.ID)
			}
		}
		if pr.Connect == nil {
			answering++
			offers[p.ID], known[p.ID] = listed, !p.ListModels.V
			add("providers", Note, "%s at %s: %d models registered. Whether its server answers was not checked.", p.ID, p.Endpoints[0].Display, len(listed))
			continue
		}
		var chosen *Endpoint
		var passed []string
		for i := range p.Endpoints {
			if err := pr.Connect(ctx, p.Endpoints[i].URL); err != nil {
				passed = append(passed, p.Endpoints[i].Display)
				continue
			}
			chosen = &p.Endpoints[i]
			break
		}
		if chosen == nil {
			add("providers", Note, "%s is left out: its server did not answer at %s.", p.ID, list(passed))
			continue
		}
		answering++
		which := ""
		if len(p.Endpoints) > 1 {
			which = fmt.Sprintf(" It is address %d of %d", len(passed)+1, len(p.Endpoints))
			if len(passed) > 0 {
				which += "; " + list(passed) + " did not answer"
			}
			which += "."
		}
		offers[p.ID], known[p.ID] = listed, true
		if p.ListModels.V && pr.ListModels != nil {
			found, err := pr.ListModels(ctx, p, *chosen)
			if err != nil {
				known[p.ID] = false
				add("providers", Note, "%s at %s answers, but its list of models could not be read (%v).%s", p.ID, chosen.Display, err, which)
				continue
			}
			for _, id := range found {
				if !slices.Contains(offers[p.ID], id) {
					offers[p.ID] = append(offers[p.ID], id)
				}
			}
		} else if p.ListModels.V {
			known[p.ID] = false
		}
		add("providers", Fine, "%s at %s: %d models.%s", p.ID, chosen.Display, len(offers[p.ID]), which)
	}
	switch {
	case len(cfg.Providers) == 0:
		add("providers", Fail, "no provider is configured, so a turn cannot run. Add one to %s, for example: {%s}", cfg.Paths.UserSettings, ProviderExample)
	case answering == 0:
		add("providers", Fail, "none of the configured providers can be used, so a turn cannot run.")
	}

	// 3. Models.
	var all []string
	for _, p := range cfg.Providers {
		for _, id := range offers[p.ID] {
			all = append(all, p.ID+"/"+id)
		}
	}
	// The session's model must be there for a turn to run; the small one
	// is only worth a note, since the session's model does its work when
	// it is missing.
	for _, slot := range []struct {
		name  string
		s     Slot
		small bool
	}{{"model", cfg.Large, false}, {"small model", cfg.Small, true}} {
		miss := Fail
		if slot.small {
			miss = Note
		}
		if !slot.s.Model.Set {
			switch {
			case slot.small:
				add("models", Fine, "small model: not chosen, so the session's model does the side jobs.")
			case len(all) == 1:
				add("models", Fine, "model: not chosen; %s is the only model there is, so it is used.", all[0])
			case len(all) > 1:
				add("models", Fail, "model: not chosen, and %d models are available. gnosh does not guess: choose one with -m, with /model, or with \"model\" in gnosh.json. They are: %s.", len(all), list(all))
			case answering > 0:
				add("models", Fail, "model: not chosen, and no model is known. Name one with -m, or register one under \"models\" in gnosh.json.")
			}
			continue
		}
		ref := slot.s.Model.V
		if ref.Provider == "" {
			var owners []string
			for _, p := range cfg.Providers {
				if slices.Contains(offers[p.ID], ref.ID) {
					owners = append(owners, p.ID)
				}
			}
			switch len(owners) {
			case 0:
				add("models", miss, "%s: no provider is known to offer %s. Name it as provider/model.", slot.name, ref.ID)
			case 1:
				add("models", Fine, "%s: %s/%s (%s), thinking %s.", slot.name, owners[0], ref.ID, slot.s.Model.Origin, slot.s.Thinking.V)
			default:
				add("models", miss, "%s: %s is offered by more than one provider (%s). Name it as provider/model.", slot.name, ref.ID, list(owners))
			}
			continue
		}
		_, answers := offers[ref.Provider]
		switch {
		case !answers:
			add("models", miss, "%s: %s is on a provider that cannot be used right now.", slot.name, ref)
		case known[ref.Provider] && !slices.Contains(offers[ref.Provider], ref.ID):
			add("models", miss, "%s: provider %s does not offer %s. It has: %s.", slot.name, ref.Provider, ref.ID, list(offers[ref.Provider]))
		default:
			window := ""
			if m, ok := cfg.Model(ref); !ok || !m.ContextWindow.Set {
				window = " Its context window is not configured: the server is asked, and 32768 tokens is assumed if it cannot say."
			}
			add("models", Fine, "%s: %s (%s), thinking %s.%s", slot.name, ref, slot.s.Model.Origin, slot.s.Thinking.V, window)
		}
	}

	// 4. Hooks.
	if cfg.HooksOff != "" {
		add("hooks", Note, "%d hooks are configured and none runs: %s.", len(cfg.AllHooks), cfg.HooksOff)
	}
	for _, h := range cfg.AllHooks {
		e := findHookEvent(h.Event)
		why := commandTrouble(cfg, h.Command, pr)
		if h.URL != "" || h.Server != "" {
			why = "" // (an address or a server is only known to answer when it does)
		} else if h.Args != nil {
			// The exec form names the program alone, and no shell reads it.
			root := cfg.Paths.ProjectRoot
			why = programTrouble(cfg, strings.NewReplacer("${CLAUDE_PROJECT_DIR}", root, "${GNOSH_PROJECT_DIR}", root).Replace(h.Command), pr)
		}
		matcher := ""
		if h.Matcher != "" {
			matcher = " [" + h.Matcher + "]"
		}
		command := h.CommandLine()
		switch {
		case why == "":
			add("hooks", Fine, "%s%s: %s (%s)", h.Event, matcher, command, h.Origin)
		case h.Event == "PermissionRequest":
			add("hooks", Note, "%s%s: %s cannot run (%s), so every question it matches is put to you instead. (%s)", h.Event, matcher, command, why, h.Origin)
		case e != nil && e.blocks && cfg.HooksOff == "":
			add("hooks", Fail, "%s%s: %s cannot run (%s). A check that cannot run does not pass, so this would hold up %s. (%s)", h.Event, matcher, command, why, blocksWhat(h.Event), h.Origin)
		default:
			add("hooks", Note, "%s%s: %s cannot run (%s). (%s)", h.Event, matcher, command, why, h.Origin)
		}
	}

	// 5. Tools, and the servers the options give them. A server is only
	// connected to, as a provider's is, and is sent nothing: an embedding
	// server holds a model, which nothing of doctor's may disturb.
	add("tools", Fine, "file search (Glob, Grep) is gnosh's own and needs no outside program.")
	for _, s := range []struct {
		name, down string
		v          Value[string]
	}{
		{"WebSearch's search server", "every search fails until it does", cfg.Options.WebSearchURL},
		{"mem's embedding server", "mem's searches match words only until it does", cfg.Options.MemEmbeddingsURL},
	} {
		if s.v.V == "" {
			continue
		}
		at := DisplayURL(s.v.V)
		if pr.Connect == nil {
			add("tools", Note, "%s is %s (%s). Whether it answers was not checked.", s.name, at, s.v.Origin)
		} else if err := pr.Connect(ctx, s.v.V); err != nil {
			add("tools", Note, "%s at %s did not answer (%v): %s. (%s)", s.name, at, err, s.down, s.v.Origin)
		} else {
			add("tools", Fine, "%s at %s answers. (%s)", s.name, at, s.v.Origin)
		}
	}

	// 6. Hosts. A host that was not tried is never called fine: nothing
	// reads as checked that was not.
	sshMissing := false
	if len(cfg.Hosts) > 0 && pr.LookPath != nil {
		if _, err := pr.LookPath("ssh"); err != nil {
			sshMissing = true
		}
	}
	for _, h := range cfg.Hosts {
		what := h.Name + " → " + h.SSH
		if h.Description != "" {
			what += " (" + h.Description + ")"
		}
		switch {
		case sshMissing:
			add("hosts", Note, "%s cannot be used: the program ssh is not installed (it was not found on the PATH), and it is what runs a command on another machine. (%s)", what, h.Origin)
		case pr.TryHost == nil:
			add("hosts", Note, "%s is declared (%s). It was not tried: gnosh doctor --hosts logs in to each host with ssh and runs nothing there.", what, h.Origin)
		default:
			dir, problem := pr.TryHost(ctx, h)
			if problem != "" {
				add("hosts", Note, "%s cannot be used right now: %s (%s)", what, strings.TrimRight(problem, ". ")+".", h.Origin)
			} else {
				add("hosts", Fine, "%s answers: a command there starts in %s. (%s)", what, firstNonEmpty(dir, "the account's home directory"), h.Origin)
			}
		}
	}

	// 7. Trust.
	project := slices.ContainsFunc(cfg.Files, func(f File) bool { return f.Scope == "project" || f.Scope == "local" })
	switch {
	case !project:
		add("trust", Fine, "this folder has no settings of its own.")
	case cfg.Trusted:
		add("trust", Fine, "this folder is trusted, and its own settings are in force.")
	case len(cfg.Held) == 0:
		add("trust", Fine, "this folder is not trusted, and none of its settings waits for trust.")
	default:
		var some []string
		for _, h := range cfg.Held[:min(len(cfg.Held), 5)] {
			some = append(some, fmt.Sprintf("%q", h.Text))
		}
		more := ""
		if len(cfg.Held) > len(some) {
			more = fmt.Sprintf(" and %d more", len(cfg.Held)-len(some))
		}
		add("trust", Note, "this folder is not trusted, so %d of its settings are held: %s%s. To use them: gnosh trust, or --trust-folder for one run.", len(cfg.Held), list(some), more)
	}

	// 8. Environment.
	for _, p := range cfg.BadEnv {
		add("environment", Note, "%s", p)
	}
	if len(cfg.BadEnv) == 0 {
		add("environment", Fine, "every variable gnosh reads has a value it can use.")
	}
	for i := range out {
		out[i].Text = cfg.secrets.scrub(out[i].Text)
	}
	return out
}

// firstNonEmpty returns a, or b when a is empty.
func firstNonEmpty(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

// blocksWhat says what a hook that cannot run holds up, by its event.
func blocksWhat(event string) string {
	switch event {
	case "PreToolUse":
		return "every tool call it matches"
	case "UserPromptSubmit":
		return "every prompt"
	case "UserPromptExpansion":
		return "every typed command it matches that expands into a prompt"
	case "PreModelSwitch":
		return "every switch to a model it matches"
	case "ConfigChange":
		return "every change to the settings files it matches, which then is not taken in"
	case "PostToolBatch":
		return "every turn at its first round of tool calls"
	case "SubagentStop":
		return "the end of every sub-agent"
	}
	return "the end of every turn"
}

// shellOnly are first words of a command that the shell handles itself, so
// there is no program to look for.
var shellOnly = []string{"[", "[[", "test", "if", "for", "while", "case", "cd", "echo", "printf", "exit", "true", "false", ":", "{", "(", "!", "export", "set", "command", "exec", "eval", "source", "."}

// interpreters are programs whose first plain argument is a script.
var interpreters = []string{"sh", "bash", "zsh", "dash", "ksh", "python", "python3", "node", "ruby", "perl", "deno", "bun", "php", "lua"}

// commandTrouble says why a hook's command line cannot run, or "". It
// looks only at the program the command starts with and at a script that
// program is handed; the rest of the line is the shell's.
func commandTrouble(cfg *Config, command string, pr Probes) string {
	words := commandWords(cfg, command)
	for len(words) > 0 {
		if _, ok := assignmentName(words[0]); !ok {
			break
		}
		words = words[1:]
	}
	if len(words) == 0 {
		return "the command is empty"
	}
	if slices.Contains(shellOnly, words[0]) || strings.ContainsAny(words[0], "$`*?") {
		return ""
	}
	if why := programTrouble(cfg, words[0], pr); why != "" {
		return why
	}
	if !slices.Contains(interpreters, filepath.Base(words[0])) || pr.Exists == nil {
		return ""
	}
	for _, w := range words[1:] {
		if strings.HasPrefix(w, "-") {
			continue
		}
		if strings.ContainsAny(w, "$`*?;|&<>") {
			break
		}
		if path := fromCwd(cfg, w); !pr.Exists(path) {
			return "the script " + path + " is not there"
		}
		break
	}
	return ""
}

// programTrouble says why a program cannot be started, or "". A name with
// a slash is a path, looked for from the working directory; a bare name is
// looked for the way the shell would.
func programTrouble(cfg *Config, program string, pr Probes) string {
	if program == "" {
		return "no program is named"
	}
	if strings.Contains(program, "/") {
		if pr.Exists == nil {
			return ""
		}
		if path := fromCwd(cfg, program); !pr.Exists(path) {
			return "the file " + path + " is not there"
		}
		return ""
	}
	if pr.LookPath == nil {
		return ""
	}
	if _, err := pr.LookPath(program); err != nil {
		return "the program " + program + " is not installed (it was not found on the PATH)"
	}
	return ""
}

// fromCwd makes a path absolute as a command started in the working
// directory would read it.
func fromCwd(cfg *Config, path string) string {
	return absPath(path, cfg.Paths.Home, cfg.Paths.Cwd)
}

// commandWords splits a command line into words the way a shell would for
// its simplest cases: spaces separate, quotes group. The project
// directory variables hooks are given are filled in first.
func commandWords(cfg *Config, command string) []string {
	root := cfg.Paths.ProjectRoot
	command = strings.NewReplacer(
		"${CLAUDE_PROJECT_DIR}", root, "$CLAUDE_PROJECT_DIR", root,
		"${GNOSH_PROJECT_DIR}", root, "$GNOSH_PROJECT_DIR", root,
	).Replace(command)
	var words []string
	var cur strings.Builder
	in, quote := false, byte(0)
	for i := 0; i < len(command); i++ {
		c := command[i]
		switch {
		case quote != 0:
			if c == quote {
				quote = 0
			} else {
				cur.WriteByte(c)
			}
		case c == '\'' || c == '"':
			quote, in = c, true
		case c == ' ' || c == '\t' || c == '\n':
			if in {
				words = append(words, cur.String())
				cur.Reset()
				in = false
			}
		default:
			cur.WriteByte(c)
			in = true
		}
	}
	if in {
		words = append(words, cur.String())
	}
	return words
}
