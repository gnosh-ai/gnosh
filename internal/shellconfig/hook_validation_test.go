package shellconfig

import (
	"encoding/json"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

// loadScriptErr runs a crushrc script expecting it to be rejected, and
// returns the error.
func loadScriptErr(t *testing.T, script string) error {
	t.Helper()
	path := filepath.Join(t.TempDir(), "crushrc")
	_, err := LoadShellConfig(t.Context(), path, []byte(script))
	require.Error(t, err, "the script should have been rejected")
	return err
}

// The loader refuses to start on an unknown hook event, so writing one
// here would hand the user a crushrc that breaks Crush on next launch —
// a startup failure produced by a command Crush itself offers. Fail on
// the line that is actually wrong instead.
func TestHookAddRejectsUnknownEvent(t *testing.T) {
	t.Parallel()

	for _, bad := range []string{"pre_tool_use", "pretooluse", "PRE_TOOL_USE", "stop", "sessionstart", "banana"} {
		t.Run(bad, func(t *testing.T) {
			t.Parallel()
			err := loadScriptErr(t, `hook add `+bad+` --command "echo hi"`)
			require.ErrorContains(t, err, "unknown event")
			require.ErrorContains(t, err, "PreToolUse")
			require.ErrorContains(t, err, "Stop")
		})
	}
}

func TestHookAddAcceptsKnownEvents(t *testing.T) {
	t.Parallel()

	result := loadScript(t, `hook add PreToolUse --command "echo pre"
hook add PostToolUse --command "echo post" --matcher "^edit$"
hook add PostToolUseFailure --command "echo failed" --matcher "^edit$"
hook add SessionStart --command "echo start"
hook add Stop --command "echo stop"
hook add Notification --command "echo alert" --matcher "^permission_prompt$"`)

	hooks := result["hooks"].(map[string]any)
	require.Len(t, hooks["PreToolUse"].([]any), 1)
	require.Len(t, hooks["PostToolUse"].([]any), 1)
	require.Len(t, hooks["PostToolUseFailure"].([]any), 1)
	require.Len(t, hooks["SessionStart"].([]any), 1)
	require.Len(t, hooks["Stop"].([]any), 1)
	// A matcher on a Notification hook is tested against the
	// notification type, so the builtin takes one, as it does for
	// SessionStart.
	require.Len(t, hooks["Notification"].([]any), 1)
}

// A matcher on a SessionStart hook is tested against its source, so the
// builtin accepts one, as it does for PreCompact.
func TestHookAddAllowsMatcherOnSessionStart(t *testing.T) {
	t.Parallel()

	result := loadScript(t, `hook add SessionStart --command "echo resume" --matcher "^resume$"`)
	arr := result["hooks"].(map[string]any)["SessionStart"].([]any)
	require.Equal(t, "^resume$", arr[0].(map[string]any)["matcher"])
}

// A matcher on a Stop hook matches nothing — there is no tool in play
// when a turn ends — and the loader rejects it, so reject it here.
func TestHookAddRejectsMatcherOnStop(t *testing.T) {
	t.Parallel()

	err := loadScriptErr(t, `hook add Stop --command "echo stop" --matcher "^bash$"`)
	require.ErrorContains(t, err, "--matcher is not supported")
	require.ErrorContains(t, err, "Stop")
}

// "*" matches everything, as no matcher does; the loader takes it on any
// event, as Claude Code does, and so does the builtin.
func TestHookAddTakesAStarOnStop(t *testing.T) {
	t.Parallel()

	result := loadScript(t, `hook add Stop --command "echo stop" --matcher "*"`)
	arr := result["hooks"].(map[string]any)["Stop"].([]any)
	require.Equal(t, "*", arr[0].(map[string]any)["matcher"])
}

func TestHookAddAllowsMatcherOnPreToolUse(t *testing.T) {
	t.Parallel()

	result := loadScript(t, `hook add PreToolUse --command "echo hi" --matcher "^bash$"`)
	arr := result["hooks"].(map[string]any)["PreToolUse"].([]any)
	require.Equal(t, "^bash$", arr[0].(map[string]any)["matcher"])
}

// A rejected line must not leave a half-written hook behind.
func TestHookAddRejectionWritesNothing(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "crushrc")
	out, err := LoadShellConfig(t.Context(), path, []byte(`hook add banana --command "echo hi"`))
	require.Error(t, err)

	if len(out) > 0 {
		var result map[string]any
		require.NoError(t, json.Unmarshal(out, &result))
		hooks, ok := result["hooks"].(map[string]any)
		if ok {
			require.NotContains(t, hooks, "banana")
		}
	}
}

func TestMCPAddRejectsUnknownType(t *testing.T) {
	t.Parallel()

	for _, bad := range []string{"websocket", "grpc", "Stdio", "HTTP"} {
		t.Run(bad, func(t *testing.T) {
			t.Parallel()
			err := loadScriptErr(t, `mcp add srv --type `+bad+` --url https://example.com`)
			require.ErrorContains(t, err, "unknown type")
			require.ErrorContains(t, err, "stdio, sse, http")
		})
	}
}

func TestMCPAddAcceptsKnownTypes(t *testing.T) {
	t.Parallel()

	result := loadScript(t, `mcp add a --type stdio --command npx
mcp add b --type sse --url https://example.com/sse
mcp add c --type http --url https://example.com/mcp`)

	mcp := result["mcp"].(map[string]any)
	require.Equal(t, "stdio", mcp["a"].(map[string]any)["type"])
	require.Equal(t, "sse", mcp["b"].(map[string]any)["type"])
	require.Equal(t, "http", mcp["c"].(map[string]any)["type"])
}

// Omitting --type is fine: the builtin writes stdio, which is what the
// loader now defaults to as well.
func TestMCPAddDefaultsToStdio(t *testing.T) {
	t.Parallel()

	result := loadScript(t, `mcp add srv --command npx`)
	require.Equal(t, "stdio", result["mcp"].(map[string]any)["srv"].(map[string]any)["type"])
}
