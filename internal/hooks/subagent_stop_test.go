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

// subagentStopRunner builds a Runner for the SubagentStop event through
// the config-load path, so a hook that ValidateHooks would reject
// can't reach a test.
func subagentStopRunner(t *testing.T, commands ...string) *Runner {
	t.Helper()
	hookCfgs := make([]config.HookConfig, len(commands))
	for i, cmd := range commands {
		hookCfgs[i] = config.HookConfig{Command: cmd}
	}
	cfg := &config.Config{
		Hooks: map[string][]config.HookConfig{EventSubagentStop: hookCfgs},
	}
	require.NoError(t, cfg.ValidateHooks())
	dir := t.TempDir()
	return NewRunner(cfg.Hooks[EventSubagentStop], dir, dir)
}

func TestRunSubagentStopNoHooks(t *testing.T) {
	t.Parallel()

	r := NewRunner(nil, t.TempDir(), t.TempDir())
	result, err := r.RunSubagentStop(context.Background(), "main", "sub", "task", "all done", false)
	require.NoError(t, err)
	require.Equal(t, DecisionNone, result.Decision)
	require.False(t, result.Halt)
	require.Zero(t, result.HookCount)
}

// Exit 2 is the block signal: the sub-agent is not allowed to finish,
// and the hook's stderr becomes the reason handed back to the model.
func TestRunSubagentStopExitTwoBlocksTheStop(t *testing.T) {
	t.Parallel()

	r := subagentStopRunner(t, `echo "tests are still failing" >&2; exit 2`)
	result, err := r.RunSubagentStop(context.Background(), "main", "sub", "task", "I'm done!", false)
	require.NoError(t, err)
	require.Equal(t, DecisionDeny, result.Decision)
	require.False(t, result.Halt, "a plain block is not a halt")
	require.Equal(t, "tests are still failing", result.Reason)
}

// Exit 49 means end the sub-agent's turn now, and must not be read as
// a block.
func TestRunSubagentStopHaltEndsTheTurn(t *testing.T) {
	t.Parallel()

	r := subagentStopRunner(t, `echo "giving up" >&2; exit 49`)
	result, err := r.RunSubagentStop(context.Background(), "main", "sub", "task", "", false)
	require.NoError(t, err)
	require.True(t, result.Halt)
	require.Equal(t, "giving up", result.Reason)
}

func TestRunSubagentStopAllowLetsTheSubAgentFinish(t *testing.T) {
	t.Parallel()

	r := subagentStopRunner(t, `exit 0`)
	result, err := r.RunSubagentStop(context.Background(), "main", "sub", "task", "", false)
	require.NoError(t, err)
	require.NotEqual(t, DecisionDeny, result.Decision)
	require.False(t, result.Halt)
}

// The payload is the hook's whole view of the sub-agent, so assert its
// shape rather than just the decision it produced: the event name, the
// session the sub-agent works for, the sub-agent's own ID and type as
// Claude Code names them, the active flag and the answer it is judging.
func TestRunSubagentStopPayload(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	out := filepath.Join(dir, "payload.json")
	r := subagentStopRunner(t, "cat > "+out)

	_, err := r.RunSubagentStop(context.Background(), "main-7", "sub-42", "task", "here is my answer", true)
	require.NoError(t, err)

	raw, err := os.ReadFile(out)
	require.NoError(t, err)

	var got Payload
	require.NoError(t, json.Unmarshal(raw, &got))
	require.Equal(t, EventSubagentStop, got.Event)
	require.Equal(t, EventSubagentStop, got.HookEventName,
		"Claude Code hooks read hook_event_name to tell events apart")
	require.Equal(t, "main-7", got.SessionID, "the session the sub-agent works for, as in Claude Code")
	require.Equal(t, "sub-42", got.AgentID, "the sub-agent's own session")
	require.Equal(t, "task", got.AgentType)
	require.Empty(t, got.ToolName, "a SubagentStop hook fires with no tool in play")
	require.JSONEq(t, "{}", string(got.ToolInput))
	require.True(t, got.StopHookActive,
		"a hook must be able to tell it is only running because an earlier block kept the sub-agent working")
	require.Equal(t, "here is my answer", got.LastAssistantMessage)
}

// stop_hook_active is omitted rather than false-y on a first pass, and
// the environment carries the same signal for hooks that read env vars,
// beside the sub-agent's ID and type.
func TestRunSubagentStopPayloadFirstPass(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	out := filepath.Join(dir, "payload.json")
	envOut := filepath.Join(dir, "env.txt")
	r := subagentStopRunner(t,
		"cat > "+out+
			"; printf '%s|%s|%s|%s|%s' \"$CRUSH_EVENT\" \"$CRUSH_STOP_HOOK_ACTIVE\" \"$KLASH_SESSION_ID\" \"$KLASH_AGENT_ID\" \"$KLASH_AGENT_TYPE\" > "+envOut)

	_, err := r.RunSubagentStop(context.Background(), "main-1", "sub-1", "task", "", false)
	require.NoError(t, err)

	raw, err := os.ReadFile(out)
	require.NoError(t, err)
	var got Payload
	require.NoError(t, json.Unmarshal(raw, &got))
	require.False(t, got.StopHookActive)

	envLine, err := os.ReadFile(envOut)
	require.NoError(t, err)
	require.Equal(t, "SubagentStop|0|main-1|sub-1|task", string(envLine))
}

func TestRunSubagentStopEnvMarksActive(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	envOut := filepath.Join(dir, "env.txt")
	r := subagentStopRunner(t, `printf '%s|%s' "$KLASH_STOP_HOOK_ACTIVE" "$CRUSH_STOP_HOOK_ACTIVE" > `+envOut)

	_, err := r.RunSubagentStop(context.Background(), "main", "sub", "task", "", true)
	require.NoError(t, err)

	envLine, err := os.ReadFile(envOut)
	require.NoError(t, err)
	require.Equal(t, "1|1", string(envLine), "under the KLASH_ name and the CRUSH_ name before it")
}

// updated_input patches a tool's arguments; with no tool call in flight
// there is nothing to patch, so the field must never come back set.
func TestRunSubagentStopClearsUpdatedInput(t *testing.T) {
	t.Parallel()

	r := subagentStopRunner(t, `echo '{"decision":"deny","reason":"no","updated_input":{"command":"rm -rf /"}}'`)
	result, err := r.RunSubagentStop(context.Background(), "main", "sub", "task", "", false)
	require.NoError(t, err)
	require.Equal(t, DecisionDeny, result.Decision)
	require.Empty(t, result.UpdatedInput)
}

// A matcher on a SubagentStop hook could never match anything, as on a
// Stop hook: rejecting it keeps it from becoming a silently dead
// setting.
func TestValidateHooksRejectsMatcherOnSubagentStop(t *testing.T) {
	t.Parallel()

	cfg := &config.Config{
		Hooks: map[string][]config.HookConfig{
			EventSubagentStop: {{Command: "true", Matcher: "^bash$"}},
		},
	}
	err := cfg.ValidateHooks()
	require.Error(t, err)
	require.Contains(t, err.Error(), "matcher is not supported")
	require.Contains(t, err.Error(), EventSubagentStop)
}
