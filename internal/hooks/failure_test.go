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
// drew as a green OK on every call while checking nothing at all, and
// then as a non-blocking failure, which let every call through. It
// checked nothing, so it blocks.
func TestRunnerReportsCommandNotFound(t *testing.T) {
	t.Parallel()

	r := preRunner(t, config.HookConfig{Command: "./hooks/does-not-exist.sh"})
	result, err := r.Run(context.Background(), EventPreToolUse, "sess", "bash", `{}`)
	require.NoError(t, err)
	require.Equal(t, DecisionDeny, result.Decision, "a hook that could not run must not let the call through")
	require.False(t, result.Objected, "it could not check; it did not object")
	require.Len(t, result.Hooks, 1)
	require.True(t, result.Hooks[0].CouldNotEvaluate)
	require.Contains(t, result.Reason, `hook "./hooks/does-not-exist.sh" could not run (did not start: `)
	require.NotEmpty(t, result.Hooks[0].Error, "a hook that never ran must not look like one that approved")
	require.Contains(t, result.Hooks[0].Error, "does-not-exist.sh",
		"the user needs to see which path was wrong")
	require.Contains(t, result.Hooks[0].Error, "no such file or directory",
		"and why it failed — the shell put the reason in its error, not on stderr")
}

// An error that says why on stderr, a check that crashed or a tool it
// ran that failed, did not check: it blocks, where Claude Code lets it
// pass as a non-blocking error.
func TestRunnerErrorThatSaysWhyBlocks(t *testing.T) {
	t.Parallel()

	r := preRunner(t, config.HookConfig{Name: "lint", Command: `echo "linter crashed" >&2; exit 1`})
	result, err := r.Run(context.Background(), EventPreToolUse, "sess", "bash", `{}`)
	require.NoError(t, err)
	require.Equal(t, DecisionDeny, result.Decision)
	require.True(t, result.Hooks[0].CouldNotEvaluate)
	require.Equal(t, "exit 1: linter crashed", result.Hooks[0].Error)
	require.Equal(t, `hook "lint" failed (exit 1: linter crashed), so it did not check this. A check that could not run does not pass.`, result.Reason)
}

// A silent non-zero exit is a test that came out false, as a hook ending
// in [[ $f == *.go ]] && gofmt -w "$f" exits 1 on every other file: no
// opinion, and not a failure to show either.
func TestRunnerSilentExitIsNoOpinion(t *testing.T) {
	t.Parallel()

	for _, cmd := range []string{`[ -f /no/such/file/for/hook/tests ] && echo found`, "exit 4"} {
		r := preRunner(t, config.HookConfig{Command: cmd})
		result, err := r.Run(context.Background(), EventPreToolUse, "sess", "bash", `{}`)
		require.NoError(t, err)
		require.Equal(t, DecisionNone, result.Decision, cmd)
		require.Empty(t, result.Hooks[0].Error, cmd)
		require.False(t, result.Hooks[0].CouldNotEvaluate, cmd)
	}
}

// A hook killed by a signal, the kernel's out-of-memory killer say, says
// nothing on stderr and checked nothing: it blocks.
func TestRunnerKilledHookBlocks(t *testing.T) {
	t.Parallel()

	r := preRunner(t, config.HookConfig{Command: `sh -c 'kill -9 $$'`})
	result, err := r.Run(context.Background(), EventPreToolUse, "sess", "bash", `{}`)
	require.NoError(t, err)
	require.Equal(t, DecisionDeny, result.Decision)
	require.True(t, result.Hooks[0].CouldNotEvaluate)
	require.Equal(t, "exit 137 (killed by signal 9)", result.Hooks[0].Error)
}

// A hook warns the user, blocking nothing, with Claude Code's
// systemMessage. One that is not a string is an answer that cannot be
// read.
func TestRunnerSystemMessage(t *testing.T) {
	t.Parallel()

	r := preRunner(t, config.HookConfig{Command: `echo '{"systemMessage":"lint found 3 warnings"}'`})
	result, err := r.Run(context.Background(), EventPreToolUse, "sess", "bash", `{}`)
	require.NoError(t, err)
	require.Equal(t, DecisionNone, result.Decision)
	require.Equal(t, "lint found 3 warnings", result.Hooks[0].Message)
	require.Empty(t, result.Hooks[0].Error)

	r = preRunner(t, config.HookConfig{Command: `echo '{"systemMessage":3}'`})
	result, err = r.Run(context.Background(), EventPreToolUse, "sess", "bash", `{}`)
	require.NoError(t, err)
	require.Equal(t, DecisionDeny, result.Decision)
	require.Contains(t, result.Hooks[0].Error, "systemMessage must be a string, got a number or boolean")
}

func TestRunnerReportsTimeout(t *testing.T) {
	t.Parallel()

	r := preRunner(t, config.HookConfig{Command: "sleep 5", Timeout: 1})
	result, err := r.Run(context.Background(), EventPreToolUse, "sess", "bash", `{}`)
	require.NoError(t, err)
	require.Equal(t, DecisionDeny, result.Decision, "a check that did not finish has not passed")
	require.True(t, result.Hooks[0].CouldNotEvaluate)
	require.Contains(t, result.Hooks[0].Error, "timed out")
	require.Contains(t, result.Reason, "could not run (timed out after 1s). A check that could not run does not pass.")
}

// A hook that could not check says so with exit 3, and Klash fails
// closed: the call is blocked, the reason is the hook's own, and it reads
// as could not check, not as a fault found. Without stderr it still
// blocks.
func TestRunnerCouldNotEvaluate(t *testing.T) {
	t.Parallel()

	r := preRunner(t, config.HookConfig{Name: "sound", Command: `echo "ffprobe is not installed, so the audio was not checked" >&2; exit 3`})
	result, err := r.Run(context.Background(), EventPreToolUse, "sess", "bash", `{}`)
	require.NoError(t, err)
	require.Equal(t, DecisionDeny, result.Decision)
	require.False(t, result.Objected)
	require.Equal(t, `hook "sound" could not check this: ffprobe is not installed, so the audio was not checked. A check that could not run does not pass.`, result.Reason)
	require.Equal(t, HookInfo{
		Name:             "sound",
		Event:            EventPreToolUse,
		Decision:         "deny",
		Reason:           result.Reason,
		Error:            "ffprobe is not installed, so the audio was not checked",
		CouldNotEvaluate: true,
	}, result.Hooks[0])

	r = preRunner(t, config.HookConfig{Name: "quiet", Command: "exit 3"})
	result, err = r.Run(context.Background(), EventPreToolUse, "sess", "bash", `{}`)
	require.NoError(t, err)
	require.Equal(t, DecisionDeny, result.Decision)
	require.Contains(t, result.Reason, `hook "quiet" could not check this: it gave no reason.`)
}

// A command the hook runs that is missing or cannot be run (127, 126)
// means the hook checked nothing, as when the hook itself is missing.
func TestRunnerCommandItRunsIsMissing(t *testing.T) {
	t.Parallel()

	for _, cmd := range []string{"no_such_checker_for_hook_tests --audio x.mp4", "exit 126"} {
		r := preRunner(t, config.HookConfig{Command: cmd})
		result, err := r.Run(context.Background(), EventPreToolUse, "sess", "bash", `{}`)
		require.NoError(t, err)
		require.Equal(t, DecisionDeny, result.Decision, cmd)
		require.True(t, result.Hooks[0].CouldNotEvaluate, cmd)
	}
}

// An answer Klash cannot read is no answer: a hook that wrote "Deny"
// meant to deny, and it blocks with its reason kept. "ask" is an answer.
func TestRunnerUnreadableAnswerBlocks(t *testing.T) {
	t.Parallel()

	r := preRunner(t, config.HookConfig{Name: "no-rm", Command: `echo '{"decision":"Deny","reason":"rm is not allowed"}'`})
	result, err := r.Run(context.Background(), EventPreToolUse, "sess", "bash", `{}`)
	require.NoError(t, err)
	require.Equal(t, DecisionDeny, result.Decision)
	require.True(t, result.Hooks[0].CouldNotEvaluate)
	require.Contains(t, result.Reason, `hook "no-rm" gave an answer Klash could not read (unknown decision "Deny"`)
	require.Contains(t, result.Reason, "its reason: rm is not allowed")

	r = preRunner(t, config.HookConfig{Command: `echo '{"hookSpecificOutput":{"permissionDecision":"ask"}}'`})
	result, err = r.Run(context.Background(), EventPreToolUse, "sess", "bash", `{}`)
	require.NoError(t, err)
	require.Equal(t, DecisionNone, result.Decision)
	require.False(t, result.Hooks[0].CouldNotEvaluate)
}

// Objected is a verdict of a hook's own, which is what an event no hook
// can block reports as an objection.
func TestAggregateObjected(t *testing.T) {
	t.Parallel()

	require.False(t, aggregate([]HookResult{{Decision: DecisionDeny, CouldNotEvaluate: true}, {Decision: DecisionAllow}}, "").Objected)
	require.True(t, aggregate([]HookResult{{Decision: DecisionDeny, CouldNotEvaluate: true}, {Decision: DecisionDeny}}, "").Objected)
	require.True(t, aggregate([]HookResult{{Halt: true}}, "").Objected)
	require.False(t, aggregate([]HookResult{{Decision: DecisionNone, Error: "exit 1"}}, "").Objected)
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
	require.Equal(t, DecisionNone, result.Decision, "the user's cancel is not a hook that could not run")
}

// One failing hook among several must be attributed to the right one.
func TestRunnerAttributesErrorToTheFailingHook(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	r := NewRunner([]config.HookConfig{
		{Name: "good", Command: "exit 0"},
		{Name: "broken", Command: `echo "lint config is missing" >&2; exit 4`},
	}, dir, dir)
	result, err := r.Run(context.Background(), EventPreToolUse, "sess", "bash", `{}`)
	require.NoError(t, err)
	require.Len(t, result.Hooks, 2)
	byName := map[string]HookInfo{}
	for _, h := range result.Hooks {
		byName[h.Name] = h
	}
	require.Empty(t, byName["good"].Error)
	require.Equal(t, "exit 4: lint config is missing", byName["broken"].Error)
	require.Equal(t, DecisionDeny, result.Decision, "an error that says why blocks")
}

func TestHookFailureMessage(t *testing.T) {
	t.Parallel()

	require.Equal(t, "exit 1", hookFailureMessage(1, ""))
	require.Equal(t, "exit 127 (command not found): sh: nope: not found",
		hookFailureMessage(127, "sh: nope: not found"))
	require.Equal(t, "exit 126 (not executable)", hookFailureMessage(126, ""))
	require.Equal(t, "exit 137 (killed by signal 9)", hookFailureMessage(137, ""))
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
