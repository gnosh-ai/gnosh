package hooks

import (
	"context"
	"testing"

	"github.com/charmbracelet/crush/internal/config"
	"github.com/stretchr/testify/require"
)

func TestParseDecisionVocabulary(t *testing.T) {
	t.Parallel()

	for word, want := range map[string]Decision{
		"allow":   DecisionAllow,
		"deny":    DecisionDeny,
		"approve": DecisionAllow, // Claude Code top-level
		"block":   DecisionDeny,  // Claude Code top-level
		"ask":     DecisionNone,  // Claude Code permissionDecision: the normal prompt
		"":        DecisionNone,  // no opinion
	} {
		got, err := parseDecision(word)
		require.NoError(t, err, "%q is documented and must be accepted", word)
		require.Equal(t, want, got, "%q", word)
	}
}

// Anything else is an error, not "no opinion" — and matching is exact,
// as it is for event names.
func TestParseDecisionRejectsUnknownWords(t *testing.T) {
	t.Parallel()

	for _, bad := range []string{"Deny", "DENY", "Allow", "denied", "reject", "stop", "no"} {
		got, err := parseDecision(bad)
		require.Error(t, err, "%q must not be read as no opinion", bad)
		require.Equal(t, DecisionNone, got, "a failed hook stays non-blocking")
		require.ErrorContains(t, err, bad)
	}
}

// The compatibility the docs promise: Claude Code hooks say "block".
func TestParseStdoutAcceptsClaudeCodeTopLevelDecisions(t *testing.T) {
	t.Parallel()

	block := parseStdout(`{"decision":"block","reason":"tests are failing"}`)
	require.Equal(t, DecisionDeny, block.Decision)
	require.Equal(t, "tests are failing", block.Reason)
	require.Empty(t, block.Error)

	approve := parseStdout(`{"decision":"approve"}`)
	require.Equal(t, DecisionAllow, approve.Decision)
	require.Empty(t, approve.Error)
}

func TestParseStdoutUnknownDecisionIsAnError(t *testing.T) {
	t.Parallel()

	r := parseStdout(`{"decision":"denied","reason":"meant to block"}`)
	require.Equal(t, DecisionNone, r.Decision)
	require.Contains(t, r.Error, `unknown decision "denied"`)
}

func TestParseClaudeCodeOutputAsk(t *testing.T) {
	t.Parallel()

	r := parseStdout(`{"hookSpecificOutput":{"permissionDecision":"ask"}}`)
	require.Equal(t, DecisionNone, r.Decision, "ask defers to the normal permission prompt")
	require.Empty(t, r.Error, "ask is a real answer, not a failure")

	bad := parseStdout(`{"hookSpecificOutput":{"permissionDecision":"maybe"}}`)
	require.Contains(t, bad.Error, `unknown decision "maybe"`)
}

// End to end: a Claude Code PreToolUse hook that blocks really blocks.
func TestRunnerHonoursClaudeCodeBlock(t *testing.T) {
	t.Parallel()

	r := preRunner(t, config.HookConfig{Command: `echo '{"decision":"block","reason":"no rm -rf"}'`})
	result, err := r.Run(context.Background(), EventPreToolUse, "sess", "bash", `{}`)
	require.NoError(t, err)
	require.Equal(t, DecisionDeny, result.Decision,
		"before this, Crush read Claude Code's block as no opinion and ran the tool")
	require.Equal(t, "no rm -rf", result.Reason)
}

// A misspelt decision reaches the UI as a failure, not as OK.
func TestRunnerSurfacesUnknownDecision(t *testing.T) {
	t.Parallel()

	r := preRunner(t, config.HookConfig{Command: `echo '{"decision":"Deny"}'`})
	result, err := r.Run(context.Background(), EventPreToolUse, "sess", "bash", `{}`)
	require.NoError(t, err)
	require.Equal(t, DecisionNone, result.Decision)
	require.Contains(t, result.Hooks[0].Error, `unknown decision "Deny"`)
}

// And the Stop event: a Claude Code Stop hook keeps the agent working.
func TestRunStopHonoursClaudeCodeBlock(t *testing.T) {
	t.Parallel()

	r := stopRunner(t, `echo '{"decision":"block","reason":"3 tests failing"}'`)
	result, err := r.RunStop(context.Background(), "sess", "done", false)
	require.NoError(t, err)
	require.Equal(t, DecisionDeny, result.Decision)
	require.Equal(t, "3 tests failing", result.Reason)
}
