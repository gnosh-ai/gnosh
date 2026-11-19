package hooks

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/url"
	"slices"
	"strings"
	"time"

	"github.com/gnosh-ai/gnosh/internal/shellquote"
)

// Hook is one configured hook: a command gnosh runs when Event happens.
type Hook struct {
	// Event is the moment the hook runs at.
	Event Event

	// Matcher narrows the hook to some occurrences of the event: tool
	// names for the tool events, the session's source for SessionStart,
	// and so on. Empty, or "*", means every occurrence. An event with
	// nothing to match (UserPromptSubmit, Stop, SubagentStop) takes no
	// matcher.
	Matcher string

	// Command is a shell command line. A relative path in it is found
	// from the session's working directory, not from the file the hook is
	// written in. When Args is not nil, Command is instead the program to
	// run: Claude Code's exec form.
	Command string

	// Args, when it is not nil, are the program's arguments, each one
	// argument as it is, with no shell to read it: the program is found on
	// the PATH the hook is given, unless it holds a slash.
	// ${CLAUDE_PROJECT_DIR} and ${GNOSH_PROJECT_DIR} in Command and in each
	// argument are the project's directory.
	Args []string

	// Timeout is how long the command may run. Zero means the event's
	// default: 600 seconds, 30 for UserPromptSubmit, 1.5 for SessionEnd
	// (where a hook's own timeout counts up to 60 seconds).
	Timeout time.Duration

	// Name is a name for the hook, for messages, given by code that makes
	// a Hook by hand. A hook read from a settings file has none. Within
	// one event a name belongs to one hook: adding a hook under a name
	// that is taken replaces it.
	Name string

	// Source says where the hook is written, for messages: a file, and a
	// line or a place in it.
	Source string

	// Global marks a hook from the user's own configuration, which
	// applies in every folder. Such a hook's command must not start with
	// a relative path, since there is no one folder it could be relative
	// to.
	Global bool

	// If is the hook's "if" filter: one permission rule, such as
	// "Bash(git push *)". On a tool event the hook runs only for a call
	// the filter takes (Input.IfMatch); on any other event it never runs.
	If string

	// Async runs the hook in the background: nothing waits for it, it
	// decides nothing, and what its answer adds for the model goes out
	// with the next request. AsyncRewake does the same, and an exit with
	// 2 also wakes the model at once (see Background).
	Async, AsyncRewake bool

	// StatusMessage is what the person is shown while the hook runs, in
	// place of the working line's own words (Runner.StatusMessages).
	StatusMessage string

	// Once takes the hook away after its first run that did not fail (a
	// skill's hook, added with Runner.Add; Claude Code honours it in a
	// skill's settings only).
	Once bool

	// URL, when it is set, makes the hook an HTTP hook (Claude Code's
	// "type": "http") in place of a command: the event is POSTed to it as
	// JSON, and the body of the answer is read as a command hook's stdout
	// is. An answer with another status than 2xx, or none, is a hook that
	// could not be evaluated. Headers go with the request; $NAME and
	// ${NAME} in their values are the variables AllowedEnvVars names, and
	// nothing for any other.
	URL            string
	Headers        map[string]string
	AllowedEnvVars []string

	// Server and Tool, when they are set, make the hook a call of a tool
	// of a connected MCP server (Claude Code's "type": "mcp_tool"): Input
	// is its arguments, a JSON object in whose strings ${field.path}
	// stands for that field of the event, and the text the tool returns
	// is read as a command hook's stdout is (Options.MCP calls it).
	Server, Tool string
	Input        json.RawMessage

	// Prompt, when it is set, makes the hook a question to the model
	// (Claude Code's "type": "prompt"): a rule or a condition, in which
	// $ARGUMENTS stands for the event as JSON (with no $ARGUMENTS the
	// event follows it), answered with {"ok": ..., "reason": ...}
	// (prompthook.go). Model names the model it would ask, which gnosh
	// does not pick per hook: it asks the model the session asks its own
	// side questions. ContinueOnBlock makes an "ok": false on PreToolUse
	// and PostToolUse a reason for the model rather than the turn's end.
	Prompt          string
	Model           string
	ContinueOnBlock bool
	// Agent makes the hook an agent hook (Claude Code's "type": "agent"):
	// Prompt is then a task for a sub-agent that looks with its tools
	// before it answers as a prompt hook's model does (agenthook.go in the
	// app). Its "ok": false is read as a prompt hook's with
	// ContinueOnBlock, and "impossible" is not read.
	Agent bool
}

// label is the hook's name in a sentence: the name it was given, or its
// command.
func (h Hook) label() string {
	if h.Name != "" {
		return quote(h.Name)
	}
	return "`" + shorten(h.CommandLine(), 100) + "`"
}

// CommandLine is the hook's command as a person reads it: the shell
// command line, or in the exec form the program and its arguments, each
// argument quoted where a shell would need it.
func (h Hook) CommandLine() string {
	if h.URL != "" {
		return "POST " + h.URL
	}
	if h.Prompt != "" && h.Agent {
		return "agent: " + shorten(h.Prompt, 80)
	}
	if h.Prompt != "" {
		return "prompt: " + shorten(h.Prompt, 80)
	}
	if h.Server != "" {
		return "mcp " + h.Server + "/" + h.Tool
	}
	if h.Args == nil {
		return h.Command
	}
	return shellquote.Words(append([]string{h.Command}, h.Args...))
}

// Spec is a hook as a caller that has already read the settings hands it
// over, before it has been checked: one handler of a settings file, with
// the event's name as the file has it.
type Spec struct {
	// Event is the event's name as written.
	Event string
	// Command is the command line to run, or with Args the program.
	Command string
	// Args are the program's arguments in the exec form; nil is the
	// shell form.
	Args []string
	// Matcher is the matcher of the handler's group, or empty.
	Matcher string
	// Name is a name for the hook, or empty.
	Name string
	// Timeout is how long the command may run. Zero means the event's
	// default.
	Timeout time.Duration
	// Source says where the hook is written, such as
	// "~/.config/gnosh/gnosh.json:12".
	Source string
	// Global is true for a hook of the user's own configuration and false
	// for one of a project's.
	Global bool
	// If is the handler's "if" filter, or empty.
	If string
	// Async and AsyncRewake run the hook in the background.
	Async, AsyncRewake bool
	// StatusMessage is shown while the hook runs.
	StatusMessage string
	// URL, Headers and AllowedEnvVars make an HTTP hook (see Hook).
	URL            string
	Headers        map[string]string
	AllowedEnvVars []string
	// Server, Tool and Input make an MCP tool hook (see Hook).
	Server, Tool string
	Input        json.RawMessage
	// Prompt, Model and ContinueOnBlock make a prompt hook, and Agent
	// with Prompt and Model an agent hook (see Hook).
	Prompt          string
	Model           string
	ContinueOnBlock bool
	Agent           bool
}

// NewHook checks a hook that was read from a settings file by someone else
// and returns it ready to be added to a Config. The error says everything
// that is wrong with it, in plain sentences.
//
// It refuses exactly what ParseConfig refuses in a JSON block, so a hook
// that passes here is one gnosh would load.
func NewHook(s Spec) (Hook, error) {
	h := Hook{
		Event:   Event(s.Event),
		Matcher: s.Matcher,
		Command: s.Command,
		Args:    s.Args,
		Timeout: s.Timeout,
		Name:    s.Name,
		Async:   s.Async,

		AsyncRewake: s.AsyncRewake,

		StatusMessage: s.StatusMessage,

		URL:             s.URL,
		Headers:         s.Headers,
		AllowedEnvVars:  s.AllowedEnvVars,
		Server:          s.Server,
		Tool:            s.Tool,
		Input:           s.Input,
		Prompt:          s.Prompt,
		Model:           s.Model,
		ContinueOnBlock: s.ContinueOnBlock,
		Agent:           s.Agent,
		Source:          s.Source,
		Global:          s.Global,
		If:              strings.TrimSpace(s.If),
	}
	if msgs := hookProblems(h); len(msgs) > 0 {
		problems := make([]Problem, len(msgs))
		for i, m := range msgs {
			problems[i] = Problem{Source: s.Source, Message: m}
		}
		return Hook{}, &ConfigError{Problems: problems}
	}
	return h, nil
}

// seconds turns a number of seconds into a duration. It refuses zero,
// negative numbers, and numbers too large or too odd to be a time.
func seconds(n float64) (time.Duration, bool) {
	if !(n > 0) || n > 1e9 {
		return 0, false
	}
	d := time.Duration(n * float64(time.Second))
	return d, d > 0
}

// timeoutProblem says that a timeout written as text cannot be used.
func timeoutProblem(text string) string {
	return "the timeout " + quote(text) + " cannot be used: a timeout is a number of seconds greater than zero. Leave it out for the event's default (600 seconds for most events)."
}

// noCommand is the problem of a hook that has nothing to run.
const noCommand = "the hook has no command to run."

// httpProblem says what is wrong with an http hook's address, or "".
func httpProblem(raw string) string {
	u, err := url.Parse(raw)
	switch {
	case err != nil:
		return "it cannot be read as an address"
	case u.Scheme != "http" && u.Scheme != "https":
		return "it must start with http:// or https://"
	case u.Host == "":
		return "it names no host"
	}
	return ""
}

// hookProblems says everything that is wrong with h by itself, each as a
// plain sentence. It is the one check every hook passes through, however
// it was written.
func hookProblems(h Hook) []string {
	var msgs []string
	info, known := describe(h.Event)
	if !known {
		msgs = append(msgs, eventProblem(string(h.Event)))
	}
	switch {
	case h.Agent && h.Prompt == "":
		msgs = append(msgs, "the agent hook has no \"prompt\": the task its sub-agent is given.")
	case h.Agent:
		if h.Command != "" || h.Args != nil || h.URL != "" || h.Server != "" || h.Tool != "" || h.Async || h.AsyncRewake {
			msgs = append(msgs, "the hook is both an agent hook and a command, an address or an MCP tool; it is one of them.")
		}
		if h.ContinueOnBlock {
			msgs = append(msgs, "an agent hook takes no \"continueOnBlock\": its \"ok\": false always goes back to the model, and the turn goes on.")
		}
		if known && (!promptEvents[h.Event] || h.Event == PermissionRequest) {
			runs := "command, http and mcp_tool hooks"
			if h.Event == PermissionRequest {
				runs = "command, http, mcp_tool and prompt hooks"
			}
			msgs = append(msgs, fmt.Sprintf("an agent hook runs on PreToolUse, PostToolUse, PostToolUseFailure, PostToolBatch, Stop, SubagentStop, UserPromptSubmit and UserPromptExpansion, as in Claude Code; %s runs %s.", h.Event, runs))
		}
	case h.Prompt != "":
		if h.Command != "" || h.Args != nil || h.URL != "" || h.Server != "" || h.Tool != "" || h.Async || h.AsyncRewake {
			msgs = append(msgs, "the hook is both a prompt and a command, an address or an MCP tool; it is one of them.")
		}
		if known && !promptEvents[h.Event] {
			msgs = append(msgs, fmt.Sprintf("a prompt hook runs on PreToolUse, PostToolUse, PostToolUseFailure, PostToolBatch, Stop, SubagentStop, UserPromptSubmit, UserPromptExpansion and PermissionRequest, as in Claude Code; %s runs command, http and mcp_tool hooks.", h.Event))
		}
	case h.Server != "" || h.Tool != "":
		if h.Server == "" || h.Tool == "" {
			msgs = append(msgs, "an mcp_tool hook names both the \"server\" and the \"tool\" it calls.")
		}
		if h.Command != "" || h.Args != nil || h.URL != "" || h.Async || h.AsyncRewake {
			msgs = append(msgs, "the hook is both a call of an MCP tool and a command or an address; it is one of them.")
		}
		if len(h.Input) > 0 && !bytes.HasPrefix(bytes.TrimSpace(h.Input), []byte("{")) {
			msgs = append(msgs, "an mcp_tool hook's \"input\" is a JSON object of the tool's arguments.")
		}
	case h.URL != "":
		if h.Command != "" || h.Args != nil || h.Async || h.AsyncRewake {
			msgs = append(msgs, "the hook has both an address and a command. An http hook sends the event to its \"url\"; a command hook runs its \"command\"; and only a command hook runs in the background.")
		}
		if why := httpProblem(h.URL); why != "" {
			msgs = append(msgs, "the hook's url cannot be used: "+why+".")
		}
	case strings.TrimSpace(h.Command) == "":
		msgs = append(msgs, noCommand)
	}
	if strings.ContainsRune(h.Command, 0) {
		msgs = append(msgs, "the hook's command holds a NUL character, which no command line can.")
	}
	if slices.ContainsFunc(h.Args, func(a string) bool { return strings.ContainsRune(a, 0) }) {
		msgs = append(msgs, "an argument of the hook holds a NUL character, which no argument can.")
	}
	if h.Timeout < 0 {
		msgs = append(msgs, "the hook's timeout is less than zero. A timeout is a time greater than zero; leave it out for the event's default.")
	}
	if known {
		switch m, err := compileMatcher(h.Matcher, info.narrow); {
		case err != nil:
			msgs = append(msgs, err.Error()+".")
		case info.subject == nothing && !m.all:
			msgs = append(msgs, fmt.Sprintf("a %s hook takes no matcher, because there is nothing to match, but this one has %s. Claude Code would ignore the matcher and run the hook every time; gnosh will not guess. Remove the matcher.", h.Event, quote(h.Matcher)))
		}
	}
	return msgs
}

// Config is a set of configured hooks, in the order they run and report
// in. The zero value is an empty configuration.
//
// A Config also remembers what was wrong with the settings it was read
// from. Check reports those problems together with every other one, and
// NewRunner will not make a Runner from a Config that has any, so a hook
// that could not be read can never be silently left out of a running
// session.
type Config struct {
	// Hooks is every hook, in order.
	Hooks []Hook

	// problems is what ParseConfig found wrong.
	problems []Problem
}

// Add puts h into the configuration. A hook with a name replaces the hook
// of the same event that has that name, where it stands; any other hook
// goes to the end.
func (c *Config) Add(h Hook) {
	if h.Name != "" {
		for i := range c.Hooks {
			if c.Hooks[i].Event == h.Event && c.Hooks[i].Name == h.Name {
				c.Hooks[i] = h
				return
			}
		}
	}
	c.Hooks = append(c.Hooks, h)
}

// Remove takes hooks out of the configuration: the hook of the event that
// has the name, or with an empty name every hook of the event. It returns
// how many it took out. An event name that is not one of gnosh's is an
// error, so that a misspelt name is not read as "nothing to remove".
func (c *Config) Remove(event, name string) (int, error) {
	if msg := eventProblem(event); msg != "" {
		return 0, &ConfigError{Problems: []Problem{{Message: msg}}}
	}
	before := len(c.Hooks)
	c.Hooks = slices.DeleteFunc(c.Hooks, func(h Hook) bool {
		return h.Event == Event(event) && (name == "" || h.Name == name)
	})
	return before - len(c.Hooks), nil
}

// AsGlobal returns the configuration with every hook marked as coming from
// the user's own configuration (see Hook.Global).
func (c Config) AsGlobal() Config {
	out := Config{Hooks: slices.Clone(c.Hooks), problems: slices.Clone(c.problems)}
	for i := range out.Hooks {
		out.Hooks[i].Global = true
	}
	return out
}

// Merge puts the configurations of several layers together. Give the
// layers weakest first: the user's own settings, then a trusted project's,
// then anything given on the command line.
//
// Hooks do not override each other: every layer's hooks are kept, and they
// run and report in the order given, a layer's own order inside it. The one
// exception is a hook with a name: it replaces the earlier hook of the same
// event and name, where that one stood. Two hooks with the same command on
// one event run once (see Runner.Run).
func Merge(layers ...Config) Config {
	var out Config
	for _, layer := range layers {
		for _, h := range layer.Hooks {
			out.Add(h)
		}
		out.problems = append(out.problems, layer.problems...)
	}
	return out
}

// Problem is one thing wrong with a hook configuration.
type Problem struct {
	// Source says where the hook is written.
	Source string
	// Message says what is wrong and what to write instead, in one or two
	// plain sentences.
	Message string
	// Warning is false for a problem that must stop gnosh from starting,
	// and true for one that is only worth knowing.
	Warning bool
}

// String returns the problem as one line: where, then what.
func (p Problem) String() string {
	if p.Source == "" {
		return p.Message
	}
	return p.Source + ": " + p.Message
}

// ConfigError is the error for a hook configuration gnosh will not use. It
// holds every problem found, not only the first.
type ConfigError struct {
	Problems []Problem
}

// Error returns the problems, one to a line.
func (e *ConfigError) Error() string {
	lines := make([]string, len(e.Problems))
	for i, p := range e.Problems {
		lines[i] = p.String()
	}
	return strings.Join(lines, "\n")
}

// Check returns every problem with a configuration at once, each a plain
// sentence. A configuration with a problem that is not a warning must not
// be used: gnosh stops at startup and shows the list. NewRunner makes the
// same check and refuses the same configurations.
//
// The problems are, for every hook however it was written:
//
//   - whatever ParseConfig could not read or does not support;
//   - an event name that is not exactly one of gnosh's;
//   - an event this build of gnosh does not fire (see Options.Fires), since
//     a hook on it would never run;
//   - a matcher on an event that takes none, or one that cannot be read;
//   - no command, or a timeout that is not a time;
//   - in the user's own configuration, a command that starts with a
//     relative path.
//
// And as warnings: a matcher that fits no tool gnosh has (when
// Options.ToolNames is given), or none of the values its event can have.
func Check(cfg Config, opts Options) []Problem {
	out := slices.Clone(cfg.problems)
	tools := newToolNames(opts.Renamed, opts.Guards)
	for _, h := range cfg.Hooks {
		msgs := hookProblems(h)
		for _, m := range msgs {
			out = append(out, Problem{Source: h.Source, Message: m})
		}
		if len(msgs) > 0 {
			continue
		}
		info, _ := describe(h.Event)
		if !slices.Contains(opts.Fires, h.Event) {
			msg := fmt.Sprintf("this build of gnosh does not run %s hooks yet, so the hook %s would never run.", h.Event, h.label())
			if info.class == gate || info.class == answers {
				msg += " A hook on this event can stop or decide things, so gnosh will not start with one it cannot run."
			}
			out = append(out, Problem{Source: h.Source, Message: msg + " Remove it until gnosh has the event."})
			continue
		}
		if h.Global && startsWithRelativePath(h.Command) {
			out = append(out, Problem{Source: h.Source, Message: fmt.Sprintf("the command of the hook %s starts with a relative path. A hook's command is found from the folder a session works in, and this hook applies in every folder. Write the full path, or a command found on PATH.", h.label())})
		}
		m, _ := compileMatcher(h.Matcher, info.narrow)
		if m.all {
			continue
		}
		switch {
		case info.subject == toolName && bareServer(h.Matcher) != "":
			server := bareServer(h.Matcher)
			out = append(out, Problem{Source: h.Source, Warning: true, Message: fmt.Sprintf("the matcher %s of the hook %s names the MCP server %s, and fits none of its tools, which are named mcp__%s__ and the tool's name: %s fits them all.", quote(h.Matcher), h.label(), server, server, quote("mcp__"+server+"__.*"))})
		case info.subject == toolName && len(opts.ToolNames) > 0 && !strings.Contains(h.Matcher, "mcp__"):
			if !slices.ContainsFunc(opts.ToolNames, func(tool string) bool { ok, _ := tools.fit(m, tool); return ok }) {
				out = append(out, Problem{Source: h.Source, Warning: true, Message: fmt.Sprintf("the matcher %s of the hook %s fits no tool gnosh has, so the hook never runs.", quote(h.Matcher), h.label())})
			}
		case info.subject == ownValue && len(info.values) > 0:
			if !m.matchesAny(info.values) {
				out = append(out, Problem{Source: h.Source, Warning: true, Message: fmt.Sprintf("the matcher %s of the hook %s fits none of the values a %s hook is matched against (%s), so the hook never runs.", quote(h.Matcher), h.label(), h.Event, strings.Join(info.values, ", "))})
			}
		}
	}
	return out
}

// stopping returns the problems that are not warnings.
func stopping(problems []Problem) []Problem {
	var out []Problem
	for _, p := range problems {
		if !p.Warning {
			out = append(out, p)
		}
	}
	return out
}

// bareServer returns the server a tool matcher names alone, as
// "mcp__memory", or "": such a matcher fits none of the server's tools.
func bareServer(matcher string) string {
	for _, part := range strings.Split(matcher, "|") {
		part = strings.TrimSpace(part)
		name, ok := strings.CutPrefix(part, "mcp__")
		if ok && name != "" && !strings.Contains(name, "__") && strings.Trim(name, "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789_-") == "" {
			return name
		}
	}
	return ""
}

// startsWithRelativePath reports whether the first word of a command line
// is plainly a path that is not absolute: "./check.sh", "hooks/check.sh".
// A word that starts with a quote, a variable or "~" is left to the shell.
func startsWithRelativePath(command string) bool {
	word := strings.TrimLeft(command, " \t")
	if i := strings.IndexAny(word, " \t\n;|&<>"); i >= 0 {
		word = word[:i]
	}
	if word == "" || !strings.Contains(word, "/") {
		return false
	}
	switch word[0] {
	case '/', '~', '$', '"', '\'', '`', '(', '{':
		return false
	}
	return !strings.Contains(word, "=")
}
