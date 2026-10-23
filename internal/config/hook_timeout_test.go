package config

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// A hook with no timeout gets Claude Code's default, 60 seconds; one that
// sets it gets what it set.
func TestHookTimeoutDefaultsToClaudeCodes(t *testing.T) {
	t.Parallel()

	require.Equal(t, 60*time.Second, (&HookConfig{Command: "true"}).TimeoutDuration())
	require.Equal(t, 5*time.Second, (&HookConfig{Command: "true", Timeout: 5}).TimeoutDuration())
}
