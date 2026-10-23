package config

import "slices"

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
	// EventPreCompact fires just before a session's conversation is
	// summarized: when the user asks for the summary, or automatically
	// when the context is nearly full. As in Claude Code, a hook cannot
	// stop the summary — it is told, through a trigger field in its
	// payload, whether it is manual or automatic, and a hook that
	// blocks or fails is shown as a warning while the summary goes
	// ahead. Hooks for it may carry a Matcher, tested against that
	// trigger.
	EventPreCompact = "PreCompact"
)

// knownHookEvents lists every recognised event name, in the order they
// are reported to the user.
var knownHookEvents = []string{EventPreToolUse, EventPostToolUse, EventPostToolUseFailure, EventUserPromptSubmit, EventSessionStart, EventStop, EventSubagentStop, EventPreCompact}

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
// against its trigger; SessionStart against its source.
func HookEventSupportsMatcher(event string) bool {
	return event == EventPreToolUse || event == EventPostToolUse || event == EventPostToolUseFailure || event == EventPreCompact || event == EventSessionStart
}
