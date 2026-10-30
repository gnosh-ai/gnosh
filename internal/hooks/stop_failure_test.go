package hooks

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/charmbracelet/crush/internal/config"
	"github.com/stretchr/testify/require"
)

// As in Claude Code, a StopFailure hook's payload carries the kind of
// error the turn ended on, what the API said and the error as shown, its
// env the kind; a matcher is tested against the kind.
func TestRunStopFailure(t *testing.T) {
	t.Parallel()

	runner := func(t *testing.T, matcher string, commands ...string) *Runner {
		t.Helper()
		hookCfgs := make([]config.HookConfig, len(commands))
		for i, cmd := range commands {
			hookCfgs[i] = config.HookConfig{Command: cmd, Matcher: matcher}
		}
		cfg := &config.Config{Hooks: map[string][]config.HookConfig{EventStopFailure: hookCfgs}}
		require.NoError(t, cfg.ValidateHooks())
		dir := t.TempDir()
		return NewRunner(cfg.Hooks[EventStopFailure], dir, dir)
	}

	dir := t.TempDir()
	payloadPath := filepath.Join(dir, "payload.json")
	envPath := filepath.Join(dir, "env")
	r := runner(t, "", `cat > `+payloadPath, `echo "$GNOSH_STOP_FAILURE_ERROR" > `+envPath)
	_, err := r.RunStopFailure(context.Background(), "sess", "rate_limit", "429 Too Many Requests", "Rate limit: slow down")
	require.NoError(t, err)

	data, err := os.ReadFile(payloadPath)
	require.NoError(t, err)
	var payload map[string]any
	require.NoError(t, json.Unmarshal(data, &payload))
	require.Equal(t, "StopFailure", payload["hook_event_name"])
	require.Equal(t, "sess", payload["session_id"])
	require.Equal(t, "rate_limit", payload["error"])
	require.Equal(t, "429 Too Many Requests", payload["error_details"])
	require.Equal(t, "Rate limit: slow down", payload["last_assistant_message"])
	env, err := os.ReadFile(envPath)
	require.NoError(t, err)
	require.Equal(t, "rate_limit\n", string(env))

	matched := runner(t, "server_error", "true")
	result, err := matched.RunStopFailure(context.Background(), "sess", "rate_limit", "", "")
	require.NoError(t, err)
	require.Zero(t, result.HookCount, "a hook matched on server_error does not fire for a rate limit")
	result, err = matched.RunStopFailure(context.Background(), "sess", "server_error", "", "")
	require.NoError(t, err)
	require.Equal(t, 1, result.HookCount)
}
