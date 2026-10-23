package config

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// A hook with no timeout gets Claude Code's default for a command hook,
// 600 seconds, and 30 on UserPromptSubmit; one that sets it gets what it
// set, on any event.
func TestHookTimeoutDefaultsToClaudeCodes(t *testing.T) {
	t.Parallel()

	hook := &HookConfig{Command: "true"}
	for _, event := range []string{EventPreToolUse, EventPostToolUse, EventStop, EventSubagentStop, EventSessionStart, EventPreCompact} {
		require.Equal(t, 600*time.Second, hook.TimeoutDuration(event), event)
	}
	require.Equal(t, 30*time.Second, hook.TimeoutDuration(EventUserPromptSubmit))
	set := &HookConfig{Command: "true", Timeout: 5}
	require.Equal(t, 5*time.Second, set.TimeoutDuration(EventStop))
	require.Equal(t, 5*time.Second, set.TimeoutDuration(EventUserPromptSubmit))
}
