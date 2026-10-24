package hooks

import (
	"testing"

	"github.com/charmbracelet/crush/internal/config"
	"github.com/stretchr/testify/require"
)

// A PermissionRequest hook answers in Claude Code's decision object:
// behavior allow (with the input to run instead) or deny (with a message,
// and interrupt to stop the turn). Anything else is an answer that cannot
// be read.
func TestParsePermissionRequestDecision(t *testing.T) {
	t.Parallel()

	allow := parseStdout(`{"hookSpecificOutput":{"hookEventName":"PermissionRequest","decision":{"behavior":"allow"}}}`)
	require.Equal(t, DecisionAllow, allow.Decision)
	require.Empty(t, allow.Error)

	rewrite := parseStdout(`{"hookSpecificOutput":{"hookEventName":"PermissionRequest","decision":{"behavior":"allow","updatedInput":{"command":"ls"}}}}`)
	require.Equal(t, DecisionAllow, rewrite.Decision)
	require.JSONEq(t, `{"command":"ls"}`, rewrite.UpdatedInput)

	deny := parseStdout(`{"hookSpecificOutput":{"hookEventName":"PermissionRequest","decision":{"behavior":"deny","message":"not on main"}}}`)
	require.Equal(t, DecisionDeny, deny.Decision)
	require.Equal(t, "not on main", deny.Reason)
	require.False(t, deny.Halt)

	interrupt := parseStdout(`{"hookSpecificOutput":{"hookEventName":"PermissionRequest","decision":{"behavior":"deny","message":"stop","interrupt":true}}}`)
	require.Equal(t, DecisionDeny, interrupt.Decision)
	require.True(t, interrupt.Halt)

	permissions := parseStdout(`{"hookSpecificOutput":{"hookEventName":"PermissionRequest","decision":{"behavior":"allow","updatedPermissions":[{"type":"setMode","mode":"acceptEdits","destination":"session"}]}}}`)
	require.Equal(t, DecisionAllow, permissions.Decision, "updatedPermissions are not applied, and the allow still holds")
	require.Empty(t, permissions.Error)

	for _, bad := range []string{
		`{"hookSpecificOutput":{"decision":{"behavior":"maybe"}}}`,
		`{"hookSpecificOutput":{"decision":{}}}`,
		`{"hookSpecificOutput":{"decision":"allow"}}`,
		`{"hookSpecificOutput":{"decision":{"behavior":"allow","updatedInput":"ls"}}}`,
	} {
		r := parseStdout(bad)
		require.NotEmpty(t, r.Error, bad)
		require.NotEqual(t, DecisionAllow, r.Decision, bad)
	}
}

// Exit 2 blocks a tool call, but for a PermissionRequest hook it leaves
// the permission flow as it was, as in Claude Code: the user is asked.
func TestPermissionRequestExit2IsNoAnswer(t *testing.T) {
	t.Parallel()

	hookCfg := []config.HookConfig{{Command: `echo "not mine to say" >&2; exit 2`}}
	r := NewRunner(hookCfg, t.TempDir(), t.TempDir())

	result, err := r.Run(t.Context(), EventPermissionRequest, "sess", "bash", `{}`)
	require.NoError(t, err)
	require.Equal(t, DecisionNone, result.Decision)
	require.False(t, result.Objected)
	require.Empty(t, result.Reason, "its stderr is not used")

	result, err = r.Run(t.Context(), EventPreToolUse, "sess", "bash", `{}`)
	require.NoError(t, err)
	require.Equal(t, DecisionDeny, result.Decision, "a PreToolUse hook's exit 2 still blocks")
}

// The event is known and takes a tool-name matcher, written as Claude
// Code's tool names are.
func TestPermissionRequestConfig(t *testing.T) {
	t.Parallel()

	require.True(t, config.IsKnownHookEvent(EventPermissionRequest))
	cfg := &config.Config{Hooks: map[string][]config.HookConfig{
		EventPermissionRequest: {{Matcher: "Bash", Command: "true"}, {Command: "exit 0"}},
	}}
	require.NoError(t, cfg.ValidateHooks())

	r := NewRunner(cfg.Hooks[EventPermissionRequest], t.TempDir(), t.TempDir())
	result, err := r.Run(t.Context(), EventPermissionRequest, "sess", "bash", `{}`)
	require.NoError(t, err)
	require.Equal(t, 2, result.HookCount, "Bash matches the bash tool")
	result, err = r.Run(t.Context(), EventPermissionRequest, "sess", "view", `{}`)
	require.NoError(t, err)
	require.Equal(t, 1, result.HookCount)
}
