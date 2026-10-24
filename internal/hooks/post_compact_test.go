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

// postCompactRunner builds a Runner holding only PostCompact hooks, each
// with the given matcher.
func postCompactRunner(t *testing.T, matcher string, commands ...string) *Runner {
	t.Helper()
	hookCfgs := make([]config.HookConfig, len(commands))
	for i, cmd := range commands {
		hookCfgs[i] = config.HookConfig{Command: cmd, Matcher: matcher}
	}
	cfg := &config.Config{Hooks: map[string][]config.HookConfig{EventPostCompact: hookCfgs}}
	require.NoError(t, cfg.ValidateHooks())
	dir := t.TempDir()
	return NewRunner(cfg.Hooks[EventPostCompact], dir, dir)
}

// As in Claude Code, a PostCompact hook's payload carries the trigger and
// the summary the conversation was compacted to, and its env the
// trigger; a matcher is tested against the trigger.
func TestRunPostCompact(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	payloadPath := filepath.Join(dir, "payload.json")
	envPath := filepath.Join(dir, "env")
	r := postCompactRunner(t, "", `cat > `+payloadPath, `echo "$KLASH_POST_COMPACT_TRIGGER" > `+envPath)
	_, err := r.RunPostCompact(context.Background(), "sess", PreCompactTriggerManual, "what was done")
	require.NoError(t, err)

	data, err := os.ReadFile(payloadPath)
	require.NoError(t, err)
	var payload map[string]any
	require.NoError(t, json.Unmarshal(data, &payload))
	require.Equal(t, "PostCompact", payload["hook_event_name"])
	require.Equal(t, "sess", payload["session_id"])
	require.Equal(t, r.cwd, payload["cwd"])
	require.Equal(t, "manual", payload["trigger"])
	require.Equal(t, "what was done", payload["compact_summary"])
	require.NotContains(t, payload, "custom_instructions", "that is PreCompact's")
	env, err := os.ReadFile(envPath)
	require.NoError(t, err)
	require.Equal(t, "manual\n", string(env))

	matched := postCompactRunner(t, "^manual$", "true")
	result, err := matched.RunPostCompact(context.Background(), "sess", PreCompactTriggerAuto, "summary")
	require.NoError(t, err)
	require.Zero(t, result.HookCount, "a hook matched on manual does not fire on an automatic summary")
	result, err = matched.RunPostCompact(context.Background(), "sess", PreCompactTriggerManual, "summary")
	require.NoError(t, err)
	require.Equal(t, 1, result.HookCount)
}

// compact_summary is in a PostCompact payload only.
func TestCompactSummaryOnlyInPostCompactPayloads(t *testing.T) {
	t.Parallel()

	for _, payload := range [][]byte{
		BuildPreCompactPayload("s", "/w", PreCompactTriggerAuto),
		BuildPayload(EventPreToolUse, "s", "/w", "bash", `{}`),
	} {
		var got map[string]any
		require.NoError(t, json.Unmarshal(payload, &got))
		require.NotContains(t, got, "compact_summary")
	}
}
