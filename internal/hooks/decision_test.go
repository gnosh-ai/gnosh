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

// A misspelt decision is an answer that cannot be read: it blocks, as the
// hook that wrote "Deny" meant to, and reaches the UI as a hook that
// could not evaluate, not as OK.
func TestRunnerSurfacesUnknownDecision(t *testing.T) {
	t.Parallel()

	r := preRunner(t, config.HookConfig{Command: `echo '{"decision":"Deny"}'`})
	result, err := r.Run(context.Background(), EventPreToolUse, "sess", "bash", `{}`)
	require.NoError(t, err)
	require.Equal(t, DecisionDeny, result.Decision)
	require.True(t, result.Hooks[0].CouldNotEvaluate)
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

// A tool-input rewrite that is not an object would be dropped at
// aggregation time with only a log line: the hook set out to rewrite the
// input and looked like one that did not. It must surface as a failed
// hook instead, the same way an unrecognised decision does.
func TestParseStdoutUpdatedInputNonObjectIsAnError(t *testing.T) {
	t.Parallel()

	t.Run("native stringified", func(t *testing.T) {
		t.Parallel()
		r := parseStdout(`{"decision":"allow","updated_input":"just a string"}`)
		require.Equal(t, DecisionNone, r.Decision,
			"an allow given for a rewrite that failed must not approve the call as it stands")
		require.Empty(t, r.UpdatedInput)
		require.Contains(t, r.Error, "updated_input must be a JSON object")
	})

	t.Run("native bare string", func(t *testing.T) {
		t.Parallel()
		r := parseStdout(`{"decision":"allow","updated_input":"not-an-object"}`)
		require.Contains(t, r.Error, "updated_input must be a JSON object")
	})

	t.Run("native array", func(t *testing.T) {
		t.Parallel()
		r := parseStdout(`{"decision":"allow","updated_input":["a","b"]}`)
		require.Contains(t, r.Error, "updated_input must be a JSON object")
	})

	t.Run("native number", func(t *testing.T) {
		t.Parallel()
		r := parseStdout(`{"decision":"allow","updated_input":42}`)
		require.Contains(t, r.Error, "updated_input must be a JSON object")
	})

	t.Run("claude code format", func(t *testing.T) {
		t.Parallel()
		r := parseStdout(`{"hookSpecificOutput":{"permissionDecision":"allow","updatedInput":"just a string"}}`)
		require.Equal(t, DecisionNone, r.Decision)
		require.Empty(t, r.UpdatedInput)
		require.Contains(t, r.Error, "hookSpecificOutput.updatedInput must be a JSON object")
	})

	t.Run("claude code format, an object inside a string", func(t *testing.T) {
		t.Parallel()
		// Claude Code itself does not accept this form.
		r := parseStdout(`{"hookSpecificOutput":{"permissionDecision":"allow","updatedInput":"{\"command\":\"ls\"}"}}`)
		require.Equal(t, DecisionNone, r.Decision)
		require.Empty(t, r.UpdatedInput)
		require.Contains(t, r.Error, "hookSpecificOutput.updatedInput must be a JSON object, got a string")
	})

	t.Run("a deny still holds", func(t *testing.T) {
		t.Parallel()
		r := parseStdout(`{"decision":"deny","reason":"no","updated_input":"oops"}`)
		require.Equal(t, DecisionDeny, r.Decision, "a malformed rewrite must not soften a deny")
		require.Contains(t, r.Error, "updated_input must be a JSON object")
	})

	t.Run("hookSpecificOutput that is not an object", func(t *testing.T) {
		t.Parallel()
		r := parseStdout(`{"hookSpecificOutput":"allow"}`)
		require.Equal(t, DecisionNone, r.Decision)
		require.Contains(t, r.Error, "hookSpecificOutput must be a JSON object, got a string")
	})

	t.Run("unknown decision and bad rewrite both surface", func(t *testing.T) {
		t.Parallel()
		r := parseStdout(`{"decision":"Deny","updated_input":"oops"}`)
		require.Contains(t, r.Error, `unknown decision "Deny"`)
		require.Contains(t, r.Error, "updated_input must be a JSON object")
	})
}

// A hookSpecificOutput field of the wrong type is reported by name, and
// the rest of the output still counts. Decoding the object all at once
// dropped it whole, so a deny next to a malformed additionalContext let
// the call run, and the error blamed the object ("must be a JSON object,
// got an object").
func TestParseClaudeCodeOutputFieldTypes(t *testing.T) {
	t.Parallel()

	r := parseStdout(`{"hookSpecificOutput":{"permissionDecision":"deny","permissionDecisionReason":"no","additionalContext":{"x":1}}}`)
	require.Equal(t, DecisionDeny, r.Decision, "the deny holds")
	require.Equal(t, "no", r.Reason)
	require.Equal(t, "hookSpecificOutput.additionalContext must be a string, got an object", r.Error)

	r = parseStdout(`{"hookSpecificOutput":{"permissionDecision":"deny","permissionDecisionReason":5}}`)
	require.Equal(t, DecisionDeny, r.Decision)
	require.Equal(t, "hookSpecificOutput.permissionDecisionReason must be a string, got a number or boolean", r.Error)

	r = parseStdout(`{"hookSpecificOutput":{"permissionDecision":true}}`)
	require.Equal(t, DecisionNone, r.Decision)
	require.Equal(t, "hookSpecificOutput.permissionDecision must be a string, got a number or boolean", r.Error)

	r = parseStdout(`{"hookSpecificOutput":{"permissionDecision":"deny","additionalContext":["a","","b"]}}`)
	require.Equal(t, DecisionDeny, r.Decision)
	require.Equal(t, "a\nb", r.Context, "a list of context strings is taken, as for the native context field")
	require.Empty(t, r.Error)
}

// Claude Code hooks send top-level fields and hookSpecificOutput together,
// and stop everything with "continue": false. Reading only
// hookSpecificOutput when it was present dropped a PostToolUse or Stop
// hook's top-level block, and continue: false was ignored outright.
func TestParseStdoutClaudeCodeTopLevelFields(t *testing.T) {
	t.Parallel()

	r := parseStdout(`{"decision":"block","reason":"lint failed","hookSpecificOutput":{"hookEventName":"PostToolUse","additionalContext":"see lint.log"}}`)
	require.Equal(t, DecisionDeny, r.Decision, "the top-level block holds beside hookSpecificOutput")
	require.Equal(t, "lint failed", r.Reason)
	require.Equal(t, "see lint.log", r.Context)
	require.Empty(t, r.Error)

	r = parseStdout(`{"continue":false,"stopReason":"Secrets in the diff"}`)
	require.True(t, r.Halt, "continue: false stops everything, as a halt does")
	require.Equal(t, "Secrets in the diff", r.Reason)

	r = parseStdout(`{"continue":true}`)
	require.False(t, r.Halt)
	require.Equal(t, DecisionNone, r.Decision)

	r = parseStdout(`{"decision":"approve","reason":"fine","hookSpecificOutput":{"permissionDecision":"deny","permissionDecisionReason":"no"}}`)
	require.Equal(t, DecisionDeny, r.Decision, "permissionDecision overrides the deprecated top-level decision")
	require.Equal(t, "no", r.Reason)

	r = parseStdout(`{"context":"a","hookSpecificOutput":{"additionalContext":"b"}}`)
	require.Equal(t, "a\nb", r.Context)

	r = parseStdout(`{"updated_input":"oops","hookSpecificOutput":{"permissionDecision":"allow"}}`)
	require.Equal(t, DecisionNone, r.Decision, "an allow must not outlive the malformed rewrite it came with")
	require.Contains(t, r.Error, "updated_input must be a JSON object")
}

// A good rewrite must not be flagged, and both object and stringified
// object forms stay valid.
func TestParseStdoutUpdatedInputObjectHasNoError(t *testing.T) {
	t.Parallel()

	for _, out := range []string{
		`{"decision":"allow","updated_input":{"command":"ls"}}`,
		`{"decision":"allow","updated_input":"{\"command\":\"ls\"}"}`,
		`{"hookSpecificOutput":{"permissionDecision":"allow","updatedInput":{"command":"ls"}}}`,
		`{"hookSpecificOutput":{"permissionDecision":"allow","updatedInput":null}}`,
	} {
		r := parseStdout(out)
		require.Empty(t, r.Error, "%s is a valid answer", out)
	}
}

// End to end: a bad rewrite is an answer that cannot be read, so the call
// is blocked rather than run as it stood, which the hook meant to change.
func TestRunnerSurfacesNonObjectUpdatedInput(t *testing.T) {
	t.Parallel()

	r := preRunner(t, config.HookConfig{
		Command: `echo '{"decision":"allow","updated_input":"oops"}'`,
	})
	result, err := r.Run(context.Background(), EventPreToolUse, "sess", "bash", `{"command":"ls"}`)
	require.NoError(t, err)
	require.Equal(t, DecisionDeny, result.Decision, "the call must not run without the rewrite")
	require.Empty(t, result.UpdatedInput)
	require.True(t, result.Hooks[0].CouldNotEvaluate)
	require.Contains(t, result.Hooks[0].Error, "updated_input must be a JSON object")
}
