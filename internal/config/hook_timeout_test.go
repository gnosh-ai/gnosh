package config

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// A hook with no timeout gets Claude Code's default for a command hook,
// 600 seconds, 1.5 on SessionEnd, and 30 on UserPromptSubmit; one that
// sets it gets what it set, on any event, capped at 60 on SessionEnd so
// exiting is not held up for long.
func TestHookTimeoutDefaultsToClaudeCodes(t *testing.T) {
	t.Parallel()

	hook := &HookConfig{Command: "true"}
	for _, event := range []string{EventPreToolUse, EventPostToolUse, EventStop, EventSubagentStop, EventSessionStart, EventPreCompact} {
		require.Equal(t, 600*time.Second, hook.TimeoutDuration(event), event)
	}
	require.Equal(t, 30*time.Second, hook.TimeoutDuration(EventUserPromptSubmit))
	require.Equal(t, 1500*time.Millisecond, hook.TimeoutDuration(EventSessionEnd))
	set := &HookConfig{Command: "true", Timeout: 5}
	require.Equal(t, 5*time.Second, set.TimeoutDuration(EventStop))
	require.Equal(t, 5*time.Second, set.TimeoutDuration(EventUserPromptSubmit))
	require.Equal(t, 5*time.Second, set.TimeoutDuration(EventSessionEnd))
	require.Equal(t, 60*time.Second, (&HookConfig{Command: "true", Timeout: 60}).TimeoutDuration(EventSessionEnd))
	require.Equal(t, 60*time.Second, (&HookConfig{Command: "true", Timeout: 120}).TimeoutDuration(EventSessionEnd),
		"beyond 60 the cap holds")
}
