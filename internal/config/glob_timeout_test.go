package config

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// CLAUDE_CODE_GLOB_TIMEOUT_SECONDS sets how long a glob may search, as in
// Claude Code, over the configured timeout; unset or not a positive
// number, the configured timeout holds, else 30 seconds.
func TestGlobTimeoutFollowsClaudeCodesVariable(t *testing.T) {
	ten := 10 * time.Second
	configured := ToolGlob{Timeout: &ten}

	t.Setenv("CLAUDE_CODE_GLOB_TIMEOUT_SECONDS", "")
	require.Equal(t, 30*time.Second, ToolGlob{}.GetTimeout())
	require.Equal(t, ten, configured.GetTimeout())

	t.Setenv("CLAUDE_CODE_GLOB_TIMEOUT_SECONDS", "45")
	require.Equal(t, 45*time.Second, ToolGlob{}.GetTimeout())
	require.Equal(t, 45*time.Second, configured.GetTimeout(), "the variable wins, as in Claude Code")

	for _, value := range []string{"0", "-3", "soon"} {
		t.Setenv("CLAUDE_CODE_GLOB_TIMEOUT_SECONDS", value)
		require.Equal(t, ten, configured.GetTimeout(), "%q is ignored", value)
	}
}
