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

// stopRunner builds a Runner for the Stop event through the config-load
// path, so a hook that ValidateHooks would reject can't reach a test.
func stopRunner(t *testing.T, commands ...string) *Runner {
	t.Helper()
	hookCfgs := make([]config.HookConfig, len(commands))
	for i, cmd := range commands {
		hookCfgs[i] = config.HookConfig{Command: cmd}
	}
	cfg := &config.Config{
		Hooks: map[string][]config.HookConfig{EventStop: hookCfgs},
	}
	require.NoError(t, cfg.ValidateHooks())
	dir := t.TempDir()
	return NewRunner(cfg.Hooks[EventStop], dir, dir)
}

func TestRunStopNoHooks(t *testing.T) {
	t.Parallel()

	r := NewRunner(nil, t.TempDir(), t.TempDir())
	result, err := r.RunStop(context.Background(), "sess", "all done", false)
	require.NoError(t, err)
	require.Equal(t, DecisionNone, result.Decision)
	require.False(t, result.Halt)
	require.Zero(t, result.HookCount)
}

// Exit 2 is the block signal: the turn is not allowed to end, and the
// hook's stderr becomes the reason handed back to the model.
func TestRunStopExitTwoBlocksTheStop(t *testing.T) {
	t.Parallel()

	r := stopRunner(t, `echo "tests are still failing" >&2; exit 2`)
	result, err := r.RunStop(context.Background(), "sess", "I'm done!", false)
	require.NoError(t, err)
	require.Equal(t, DecisionDeny, result.Decision)
	require.False(t, result.Halt, "a plain block is not a halt")
	require.Equal(t, "tests are still failing", result.Reason)
}

// Exit 49 means end the turn now, and must not be read as a block.
func TestRunStopHaltEndsTheTurn(t *testing.T) {
	t.Parallel()

	r := stopRunner(t, `echo "giving up" >&2; exit 49`)
	result, err := r.RunStop(context.Background(), "sess", "", false)
	require.NoError(t, err)
	require.True(t, result.Halt)
	require.Equal(t, "giving up", result.Reason)
}

func TestRunStopJSONDecision(t *testing.T) {
	t.Parallel()

	r := stopRunner(t, `echo '{"decision":"deny","reason":"keep going"}'`)
	result, err := r.RunStop(context.Background(), "sess", "", false)
	require.NoError(t, err)
	require.Equal(t, DecisionDeny, result.Decision)
	require.Equal(t, "keep going", result.Reason)
}

func TestRunStopAllowLetsTheTurnEnd(t *testing.T) {
	t.Parallel()

	r := stopRunner(t, `exit 0`)
	result, err := r.RunStop(context.Background(), "sess", "", false)
	require.NoError(t, err)
	require.NotEqual(t, DecisionDeny, result.Decision)
	require.False(t, result.Halt)
}

// Every configured Stop hook runs: there is no tool name to match
// against, so none can be filtered out.
func TestRunStopRunsEveryHookAndJoinsReasons(t *testing.T) {
	t.Parallel()

	r := stopRunner(t,
		`echo "first objection" >&2; exit 2`,
		`echo "second objection" >&2; exit 2`,
	)
	result, err := r.RunStop(context.Background(), "sess", "", false)
	require.NoError(t, err)
	require.Equal(t, DecisionDeny, result.Decision)
	require.Equal(t, 2, result.HookCount)
	require.Contains(t, result.Reason, "first objection")
	require.Contains(t, result.Reason, "second objection")
}

// The payload is the hook's whole view of the turn, so assert its shape
// rather than just the decision it produced.
func TestRunStopPayload(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	out := filepath.Join(dir, "payload.json")
	r := stopRunner(t, "cat > "+out)

	_, err := r.RunStop(context.Background(), "sess-42", "here is my answer", true)
	require.NoError(t, err)

	raw, err := os.ReadFile(out)
	require.NoError(t, err)

	var got Payload
	require.NoError(t, json.Unmarshal(raw, &got))
	require.Equal(t, EventStop, got.Event)
	require.Equal(t, "sess-42", got.SessionID)
	require.Empty(t, got.ToolName, "a Stop hook fires with no tool in play")
	require.JSONEq(t, "{}", string(got.ToolInput))
	require.True(t, got.StopHookActive,
		"a hook must be able to tell it is only running because an earlier block kept the turn alive")
	require.Equal(t, "here is my answer", got.LastAssistantMessage,
		"the hook must see the answer it is judging without reading the database")
}

// stop_hook_active is omitted rather than false-y on a first pass, and
// the environment carries the same signal for hooks that read env vars.
func TestRunStopPayloadFirstPass(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	out := filepath.Join(dir, "payload.json")
	envOut := filepath.Join(dir, "env.txt")
	r := stopRunner(t, "cat > "+out+"; echo \"$CRUSH_STOP_HOOK_ACTIVE|$CRUSH_EVENT|$CRUSH_SESSION_ID\" > "+envOut)

	_, err := r.RunStop(context.Background(), "sess-1", "", false)
	require.NoError(t, err)

	raw, err := os.ReadFile(out)
	require.NoError(t, err)
	var got Payload
	require.NoError(t, json.Unmarshal(raw, &got))
	require.False(t, got.StopHookActive)

	envLine, err := os.ReadFile(envOut)
	require.NoError(t, err)
	require.Equal(t, "0|Stop|sess-1\n", string(envLine))
}

func TestRunStopEnvMarksActive(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	envOut := filepath.Join(dir, "env.txt")
	r := stopRunner(t, `printf '%s|%s' "$KLASH_STOP_HOOK_ACTIVE" "$CRUSH_STOP_HOOK_ACTIVE" > `+envOut)

	_, err := r.RunStop(context.Background(), "sess", "", true)
	require.NoError(t, err)

	envLine, err := os.ReadFile(envOut)
	require.NoError(t, err)
	require.Equal(t, "1|1", string(envLine), "under the KLASH_ name and the CRUSH_ name before it")
}

// updated_input patches a tool's arguments; with no tool call in flight
// there is nothing to patch, so the field must never come back set.
func TestRunStopClearsUpdatedInput(t *testing.T) {
	t.Parallel()

	r := stopRunner(t, `echo '{"decision":"deny","reason":"no","updated_input":{"command":"rm -rf /"}}'`)
	result, err := r.RunStop(context.Background(), "sess", "", false)
	require.NoError(t, err)
	require.Equal(t, DecisionDeny, result.Decision)
	require.Empty(t, result.UpdatedInput)
}
