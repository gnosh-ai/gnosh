package config

import (
	"regexp"
	"slices"
	"strings"
)

// Hook event names. These are the only keys Crush recognises under the
// `hooks` object in crush.json. An unrecognised key is a config error,
// not a silently ignored entry: a hook filed under a name nothing fires
// would look configured and never run.
const (
	// EventPreToolUse fires immediately before a tool call runs. Hooks
	// for it may carry a Matcher, tested against the tool name.
	EventPreToolUse = "PreToolUse"
	// EventPostToolUse fires right after a tool call succeeds. Hooks for
	// it may carry a Matcher, tested against the tool name. A hook that
	// blocks it hands its reason to the agent as feedback on the result;
	// the call has already run, so nothing is undone.
	EventPostToolUse = "PostToolUse"
	// EventPostToolUseFailure fires right after a tool call fails, matched
	// against the tool name like EventPostToolUse; what a hook says is
	// added to the error the agent reads.
	EventPostToolUseFailure = "PostToolUseFailure"
	// EventUserPromptSubmit fires when the user sends a prompt, before
	// the agent sees it. A hook for it can add context the agent reads
	// with the prompt (its stdout, or Claude Code's additionalContext),
	// or block the prompt so it is not sent. As in Claude Code, hooks for
	// it may not carry a Matcher.
	EventUserPromptSubmit = "UserPromptSubmit"
	// EventSessionStart fires before the first prompt a process sends to a
	// session: at startup, on resume, on a new session in a running
	// process, and once after a summary. A hook for it can add context
	// the agent reads with that prompt, but, as in Claude Code, it cannot
	// stop the prompt: a block or a failure is shown as a warning while
	// the prompt goes ahead. Its Matcher is tested against the source.
	EventSessionStart = "SessionStart"
	// EventStop fires when the agent is about to finish its turn. A
	// hook that blocks it sends the agent back to work instead of
	// letting the turn end. There is no tool in play, so hooks for it
	// may not carry a Matcher.
	EventStop = "Stop"
	// EventSubagentStop fires when a sub-agent is about to finish, and
	// works exactly as EventStop does for the main agent: a hook that
	// blocks it sends the sub-agent back to work. It fires only for
	// sub-agents, never for the main agent. As in Claude Code, its
	// payload's session_id is the session the sub-agent works for, and
	// agent_id and agent_type name the sub-agent. There is no tool in
	// play, so hooks for it may not carry a Matcher.
	EventSubagentStop = "SubagentStop"
	// EventStopFailure fires instead of EventStop when the main agent's
	// turn ends on an error from the model's API: a rate limit, a
	// refused key, a server error, a model server that never came back.
	// As in Claude Code it changes nothing (what the hook prints and how
	// it exits are ignored); it is there to log or raise an alert. Its
	// Matcher is tested against the error's kind, Claude Code's names:
	// rate_limit, overloaded, authentication_failed, billing_error,
	// invalid_request, model_not_found, server_error or unknown.
	EventStopFailure = "StopFailure"
	// EventPreCompact fires just before a session's conversation is
	// summarized: when the user asks for the summary, or automatically
	// when the context is nearly full. As in Claude Code, a hook cannot
	// stop the summary — it is told, through a trigger field in its
	// payload, whether it is manual or automatic, and a hook that
	// blocks or fails is shown as a warning while the summary goes
	// ahead. Hooks for it may carry a Matcher, tested against that
	// trigger.
	EventPreCompact = "PreCompact"
	// EventPostCompact fires once a session's conversation has been
	// summarized, with the summary, as in Claude Code: to log it, or to
	// update something outside Klash. It cannot change the summary; a
	// hook that blocks or fails is shown as a warning. Its Matcher is
	// tested against the trigger, as PreCompact's is.
	EventPostCompact = "PostCompact"
	// EventSessionEnd fires when a session ends: when the user starts a
	// new one ("clear"), switches to another ("other"), or the process
	// exits with the session open ("prompt_input_exit" for the TUI,
	// "other" for klash run). As in Claude Code, a hook cannot stop
	// the exit and what it prints is ignored; a hook that fails is
	// logged. Its Matcher is tested against the reason, as a
	// SessionStart's is tested against the source.
	EventSessionEnd = "SessionEnd"
)

// knownHookEvents lists every recognised event name, in the order they
// are reported to the user.
var knownHookEvents = []string{EventPreToolUse, EventPostToolUse, EventPostToolUseFailure, EventUserPromptSubmit, EventSessionStart, EventStop, EventSubagentStop, EventStopFailure, EventPreCompact, EventPostCompact, EventSessionEnd}

// KnownHookEvents returns the recognised hook event names.
func KnownHookEvents() []string {
	return slices.Clone(knownHookEvents)
}

// IsKnownHookEvent reports whether name is an event Crush actually fires.
func IsKnownHookEvent(name string) bool {
	return slices.Contains(knownHookEvents, name)
}

// HookEventSupportsMatcher reports whether hooks for the given event may
// carry a Matcher. Tool events match against the tool name; PreCompact
// and PostCompact against their trigger; StopFailure against the error's kind; SessionStart against its source; SessionEnd
// against its reason.
func HookEventSupportsMatcher(event string) bool {
	return event == EventPreToolUse || event == EventPostToolUse || event == EventPostToolUseFailure || event == EventPreCompact || event == EventPostCompact || event == EventStopFailure || event == EventSessionStart || event == EventSessionEnd
}

// HookMatcher is a hook's matcher read as Claude Code reads one: "*" (or
// "") matches everything; one made only of letters, digits, '_', '-',
// spaces, ',' and '|' is a list of exact names, split at '|' and ',' (so
// "Edit|Write" is two names, and "Edit" does not match "MultiEdit"); and
// anything else is a regular expression, unanchored ("^Edit$",
// "mcp__github__.*"). A nil *HookMatcher matches everything.
type HookMatcher struct {
	names []string       // the exact names, when not a regular expression
	re    *regexp.Regexp // the regular expression, when it is one
}

// exactHookMatcher is what a matcher of exact names is made of.
var exactHookMatcher = regexp.MustCompile(`^[A-Za-z0-9_\- ,|]+$`)

// CompileHookMatcher reads a hook's matcher, as HookMatcher says; nil for
// one that matches everything, and an error for a regular expression that
// does not compile.
func CompileHookMatcher(matcher string) (*HookMatcher, error) {
	if s := strings.TrimSpace(matcher); s == "" || s == "*" {
		return nil, nil
	}
	if exactHookMatcher.MatchString(matcher) {
		var names []string
		for _, name := range strings.FieldsFunc(matcher, func(r rune) bool { return r == '|' || r == ',' }) {
			if name = strings.TrimSpace(name); name != "" {
				names = append(names, name)
			}
		}
		return &HookMatcher{names: names}, nil
	}
	re, err := regexp.Compile(matcher)
	if err != nil {
		return nil, err
	}
	return &HookMatcher{re: re}, nil
}

// Matches reports whether the matcher matches any of names: a tool's
// names, say, its own and Claude Code's for it.
func (m *HookMatcher) Matches(names ...string) bool {
	if m == nil {
		return true
	}
	for _, name := range names {
		switch {
		case name == "":
		case m.re != nil:
			if m.re.MatchString(name) {
				return true
			}
		case slices.Contains(m.names, name):
			return true
		}
	}
	return false
}

// Names are the exact names the matcher is a list of, none for a
// regular expression or one that matches everything.
func (m *HookMatcher) Names() []string {
	if m == nil {
		return nil
	}
	return slices.Clone(m.names)
}
