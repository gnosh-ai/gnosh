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
	// EventStop fires when the agent is about to finish its turn. A
	// hook that blocks it sends the agent back to work instead of
	// letting the turn end. There is no tool in play, so hooks for it
	// may not carry a Matcher.
	EventStop = "Stop"
)

// knownHookEvents lists every recognised event name, in the order they
// are reported to the user.
var knownHookEvents = []string{EventPreToolUse, EventStop}

// KnownHookEvents returns the recognised hook event names.
func KnownHookEvents() []string {
	return slices.Clone(knownHookEvents)
}

// IsKnownHookEvent reports whether name is an event Crush actually fires.
func IsKnownHookEvent(name string) bool {
	return slices.Contains(knownHookEvents, name)
}

// HookEventSupportsMatcher reports whether hooks for the given event may
// carry a Matcher. Only events that fire against a named tool do.
func HookEventSupportsMatcher(event string) bool {
	return event == EventPreToolUse
}
