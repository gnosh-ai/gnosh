package hooks

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/charmbracelet/crush/internal/config"
	"github.com/stretchr/testify/require"
)

// A SubagentStart hook is matched on the sub-agent's type, gets the
// sub-agent's session and type in its env, and its additionalContext is
// the result's context, as in Claude Code.
func TestRunSubagentStart(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	envPath := filepath.Join(dir, "env")
	cfg := &config.Config{Hooks: map[string][]config.HookConfig{EventSubagentStart: {
		{Matcher: "task", Command: `echo "$KLASH_AGENT_ID $KLASH_AGENT_TYPE" > ` + envPath + `; echo '{"hookSpecificOutput":{"hookEventName":"SubagentStart","additionalContext":"be brief"}}'`},
	}}}
	require.NoError(t, cfg.ValidateHooks())
	r := NewRunner(cfg.Hooks[EventSubagentStart], dir, dir)

	result, err := r.RunSubagentStart(context.Background(), "parent", "child", "task")
	require.NoError(t, err)
	require.Equal(t, "be brief", result.Context)
	env, err := os.ReadFile(envPath)
	require.NoError(t, err)
	require.Equal(t, "child task\n", string(env))

	result, err = r.RunSubagentStart(context.Background(), "parent", "child", "agentic_fetch")
	require.NoError(t, err)
	require.Zero(t, result.HookCount, "a hook matched on task does not fire for the fetch helper")
}
