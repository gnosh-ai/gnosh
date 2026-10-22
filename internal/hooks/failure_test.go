package hooks

import (
	"context"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/charmbracelet/crush/internal/config"
	"github.com/stretchr/testify/require"
)

func preRunner(t *testing.T, hook config.HookConfig) *Runner {
	t.Helper()
	dir := t.TempDir()
	return NewRunner([]config.HookConfig{hook}, dir, dir)
}

// The case that motivated this: a safety hook pointed at a script that
// does not exist. It used to come back as "no opinion", which the UI
// drew as a green OK on every call while checking nothing at all.
func TestRunnerReportsCommandNotFound(t *testing.T) {
	t.Parallel()

	r := preRunner(t, config.HookConfig{Command: "./hooks/does-not-exist.sh"})
	result, err := r.Run(context.Background(), EventPreToolUse, "sess", "bash", `{}`)
	require.NoError(t, err)
	require.Equal(t, DecisionNone, result.Decision, "a failed hook is still non-blocking")
	require.Len(t, result.Hooks, 1)
	require.NotEmpty(t, result.Hooks[0].Error, "a hook that never ran must not look like one that approved")
	require.Contains(t, result.Hooks[0].Error, "does-not-exist.sh",
		"the user needs to see which path was wrong")
	require.Contains(t, result.Hooks[0].Error, "no such file or directory",
		"and why it failed — the shell put the reason in its error, not on stderr")
}

func TestRunnerReportsNonBlockingExitWithStderr(t *testing.T) {
	t.Parallel()

	r := preRunner(t, config.HookConfig{Command: `echo "linter crashed" >&2; exit 1`})
	result, err := r.Run(context.Background(), EventPreToolUse, "sess", "bash", `{}`)
	require.NoError(t, err)
	require.Equal(t, DecisionNone, result.Decision)
	require.Equal(t, "exit 1: linter crashed", result.Hooks[0].Error)
}

func TestRunnerReportsTimeout(t *testing.T) {
	t.Parallel()

	r := preRunner(t, config.HookConfig{Command: "sleep 5", Timeout: 1})
	result, err := r.Run(context.Background(), EventPreToolUse, "sess", "bash", `{}`)
	require.NoError(t, err)
	require.Equal(t, DecisionNone, result.Decision)
	require.Contains(t, result.Hooks[0].Error, "timed out")
}

// The distinction has to cut both ways: a hook that ran cleanly and said
// nothing really is OK, and must not be flagged.
func TestRunnerCleanRunHasNoError(t *testing.T) {
	t.Parallel()

	for _, cmd := range []string{"exit 0", `echo '{}'`, `echo '{"decision":"allow"}'`} {
		r := preRunner(t, config.HookConfig{Command: cmd})
		result, err := r.Run(context.Background(), EventPreToolUse, "sess", "bash", `{}`)
		require.NoError(t, err)
		require.Empty(t, result.Hooks[0].Error, "%q ran cleanly", cmd)
	}
}

// Exit 2 and exit 49 are verdicts, not failures.
func TestRunnerDecisionsAreNotErrors(t *testing.T) {
	t.Parallel()

	for _, cmd := range []string{`echo no >&2; exit 2`, `echo stop >&2; exit 49`} {
		r := preRunner(t, config.HookConfig{Command: cmd})
		result, err := r.Run(context.Background(), EventPreToolUse, "sess", "bash", `{}`)
		require.NoError(t, err)
		require.Empty(t, result.Hooks[0].Error, "%q is a decision, not a failure", cmd)
		require.Equal(t, DecisionDeny, result.Decision)
	}
}

// A turn the user cancelled is their doing; the hook did not fail.
func TestRunnerParentCancelIsNotAnError(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	r := preRunner(t, config.HookConfig{Command: "sleep 5"})
	result, err := r.Run(ctx, EventPreToolUse, "sess", "bash", `{}`)
	require.NoError(t, err)
	require.Empty(t, result.Hooks[0].Error)
}

// One failing hook among several must be attributed to the right one.
func TestRunnerAttributesErrorToTheFailingHook(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	r := NewRunner([]config.HookConfig{
		{Name: "good", Command: "exit 0"},
		{Name: "broken", Command: "exit 3"},
	}, dir, dir)
	result, err := r.Run(context.Background(), EventPreToolUse, "sess", "bash", `{}`)
	require.NoError(t, err)
	require.Len(t, result.Hooks, 2)
	byName := map[string]HookInfo{}
	for _, h := range result.Hooks {
		byName[h.Name] = h
	}
	require.Empty(t, byName["good"].Error)
	require.Equal(t, "exit 3", byName["broken"].Error)
}

func TestHookFailureMessage(t *testing.T) {
	t.Parallel()

	require.Equal(t, "exit 1", hookFailureMessage(1, ""))
	require.Equal(t, "exit 127 (command not found): sh: nope: not found",
		hookFailureMessage(127, "sh: nope: not found"))
	require.Equal(t, "exit 126 (not executable)", hookFailureMessage(126, ""))
	require.Equal(t, "exit 1: first line", hookFailureMessage(1, "first line\nsecond line"),
		"only the first line fits on a status line")
}

// A long stderr must be cut by character, never mid-character, or the
// TUI line ends in a broken glyph.
func TestHookFailureMessageTruncatesByRune(t *testing.T) {
	t.Parallel()

	long := strings.Repeat("é", maxHookErrorLen+50)
	msg := hookFailureMessage(1, long)
	require.True(t, utf8.ValidString(msg), "truncation must not split a multi-byte character")
	require.True(t, strings.HasSuffix(msg, "…"))
	require.Equal(t, maxHookErrorLen, utf8.RuneCountInString(strings.TrimSuffix(strings.TrimPrefix(msg, "exit 1: "), "…")))
}
