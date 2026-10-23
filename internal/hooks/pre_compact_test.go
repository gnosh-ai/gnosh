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

// preCompactRunner builds a Runner holding only PreCompact hooks. When
// matcher is non-empty it is attached to every hook.
func preCompactRunner(t *testing.T, matcher string, commands ...string) *Runner {
	t.Helper()
	hookCfgs := make([]config.HookConfig, len(commands))
	for i, cmd := range commands {
		hookCfgs[i] = config.HookConfig{Command: cmd, Matcher: matcher}
	}
	cfg := &config.Config{Hooks: map[string][]config.HookConfig{EventPreCompact: hookCfgs}}
	require.NoError(t, cfg.ValidateHooks())
	dir := t.TempDir()
	return NewRunner(cfg.Hooks[EventPreCompact], dir, dir)
}

// As in Claude Code, a PreCompact hook's payload carries who asked for
// the summary and the instructions to fold into it. Crash has no
// instructions, so custom_instructions is always the empty string.
func TestRunPreCompactPayload(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	payloadPath := filepath.Join(dir, "payload.json")
	r := preCompactRunner(t, "", `cat > `+payloadPath)
	_, err := r.RunPreCompact(context.Background(), "sess", PreCompactTriggerAuto)
	require.NoError(t, err)

	data, err := os.ReadFile(payloadPath)
	require.NoError(t, err)
	var payload map[string]any
	require.NoError(t, json.Unmarshal(data, &payload))
	require.Equal(t, "PreCompact", payload["hook_event_name"])
	require.Equal(t, "PreCompact", payload["event"])
	require.Equal(t, "sess", payload["session_id"])
	require.Equal(t, r.cwd, payload["cwd"])
	require.Equal(t, "auto", payload["trigger"])
	require.Equal(t, "", payload["custom_instructions"])
}

// A matcher is tested against the trigger, as in Claude Code: a hook
// matched on "manual" does not fire on an automatic summary, and a hook
// with no matcher fires on both.
func TestRunPreCompactMatcherMatchesTheTrigger(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		matcher string
		trigger string
	}{
		{"^manual$", PreCompactTriggerManual},
		{"^manual$", PreCompactTriggerAuto},
		{"^auto$", PreCompactTriggerManual},
		{"^auto$", PreCompactTriggerAuto},
		{"", PreCompactTriggerManual},
		{"", PreCompactTriggerAuto},
	} {
		t.Run(tc.matcher+"_"+tc.trigger, func(t *testing.T) {
			t.Parallel()
			cfg := &config.Config{Hooks: map[string][]config.HookConfig{
				EventPreCompact: {{Command: "true", Matcher: tc.matcher}},
			}}
			require.NoError(t, cfg.ValidateHooks())
			runner := NewRunner(cfg.Hooks[EventPreCompact], t.TempDir(), t.TempDir())
			agg, err := runner.RunPreCompact(context.Background(), "sess", tc.trigger)
			require.NoError(t, err)
			if tc.matcher == "" {
				require.Equal(t, 1, agg.HookCount, "a hook without a matcher fires on both triggers")
				return
			}
			if tc.matcher == "^"+tc.trigger+"$" {
				require.Equal(t, 1, agg.HookCount, "the hook fires when the trigger matches")
			} else {
				require.Equal(t, 0, agg.HookCount, "the hook does not fire when the trigger does not match")
			}
		})
	}
}

// Exit 2 blocks, with stderr as the reason; exit 49 halts. Neither
// stops the summary — that is the agent's decision, made on this
// aggregate.
func TestRunPreCompactBlocks(t *testing.T) {
	t.Parallel()

	result, err := preCompactRunner(t, "", `echo "not yet" >&2; exit 2`).RunPreCompact(context.Background(), "sess", PreCompactTriggerManual)
	require.NoError(t, err)
	require.Equal(t, DecisionDeny, result.Decision)
	require.Equal(t, "not yet", result.Reason)

	result, err = preCompactRunner(t, "", `echo "stop everything" >&2; exit 49`).RunPreCompact(context.Background(), "sess", PreCompactTriggerAuto)
	require.NoError(t, err)
	require.True(t, result.Halt)
}

// The trigger is in the environment too, so a hook can branch on it
// without parsing stdin.
func TestRunPreCompactEnvCarriesTheTrigger(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	out := filepath.Join(dir, "out")
	r := preCompactRunner(t, "", `echo "$CRASH_PRE_COMPACT_TRIGGER" > `+out)
	_, err := r.RunPreCompact(context.Background(), "sess", PreCompactTriggerManual)
	require.NoError(t, err)
	data, err := os.ReadFile(out)
	require.NoError(t, err)
	require.Equal(t, "manual\n", string(data))
}

// custom_instructions belongs to PreCompact: other events' payloads leave
// it out rather than carry it empty.
func TestCustomInstructionsOnlyInPreCompactPayloads(t *testing.T) {
	t.Parallel()

	var payload map[string]any
	require.NoError(t, json.Unmarshal(BuildPayload(EventPreToolUse, "sess", "/p", "bash", `{"command":"ls"}`), &payload))
	_, has := payload["custom_instructions"]
	require.False(t, has)

	require.NoError(t, json.Unmarshal(BuildPreCompactPayload("sess", "/p", PreCompactTriggerAuto), &payload))
	require.Equal(t, "", payload["custom_instructions"])
}
