package hooks

import (
	"context"
	"encoding/json"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/crush/internal/config"
	"github.com/stretchr/testify/require"
)

// sessionEndRunner builds a Runner holding only SessionEnd hooks. When
// matcher is non-empty it is attached to every hook.
func sessionEndRunner(t *testing.T, matcher string, commands ...string) *Runner {
	t.Helper()
	hookCfgs := make([]config.HookConfig, len(commands))
	for i, cmd := range commands {
		hookCfgs[i] = config.HookConfig{Command: cmd, Matcher: matcher}
	}
	cfg := &config.Config{Hooks: map[string][]config.HookConfig{EventSessionEnd: hookCfgs}}
	require.NoError(t, cfg.ValidateHooks())
	dir := t.TempDir()
	return NewRunner(cfg.Hooks[EventSessionEnd], dir, dir)
}

// As in Claude Code, a SessionEnd hook's payload carries why the
// session ended, and the environment carries the event and the session
// as other hooks' do.
func TestRunSessionEndPayload(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	payloadPath := filepath.Join(dir, "payload.json")
	envPath := filepath.Join(dir, "env")
	r := sessionEndRunner(t, "", `cat > `+payloadPath+`; echo "$KLASH_EVENT $KLASH_SESSION_ID" > `+envPath)
	_, err := r.RunSessionEnd(context.Background(), "sess", SessionEndReasonPromptInputExit)
	require.NoError(t, err)

	data, err := os.ReadFile(payloadPath)
	require.NoError(t, err)
	var payload map[string]any
	require.NoError(t, json.Unmarshal(data, &payload))
	require.Equal(t, "SessionEnd", payload["hook_event_name"])
	require.Equal(t, "SessionEnd", payload["event"])
	require.Equal(t, "sess", payload["session_id"])
	require.Equal(t, r.cwd, payload["cwd"])
	require.Equal(t, "prompt_input_exit", payload["reason"])

	envData, err := os.ReadFile(envPath)
	require.NoError(t, err)
	require.Equal(t, "SessionEnd sess\n", string(envData))
}

// A matcher is tested against the reason, as in Claude Code: a hook
// matched on "clear|other" does not fire on an exit, and a hook with no
// matcher fires on every reason.
func TestRunSessionEndMatcherMatchesTheReason(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		matcher string
		reason  string
	}{
		{"clear|other", SessionEndReasonClear},
		{"clear|other", SessionEndReasonOther},
		{"clear|other", SessionEndReasonPromptInputExit},
		{"^prompt_input_exit$", SessionEndReasonPromptInputExit},
		{"^prompt_input_exit$", SessionEndReasonClear},
		{"", SessionEndReasonClear},
		{"", SessionEndReasonOther},
		{"", SessionEndReasonPromptInputExit},
	} {
		t.Run(tc.matcher+"_"+tc.reason, func(t *testing.T) {
			t.Parallel()
			cfg := &config.Config{Hooks: map[string][]config.HookConfig{
				EventSessionEnd: {{Command: "true", Matcher: tc.matcher}},
			}}
			require.NoError(t, cfg.ValidateHooks())
			runner := NewRunner(cfg.Hooks[EventSessionEnd], t.TempDir(), t.TempDir())
			agg, err := runner.RunSessionEnd(context.Background(), "sess", tc.reason)
			require.NoError(t, err)
			if tc.matcher == "" {
				require.Equal(t, 1, agg.HookCount, "a hook without a matcher fires on every reason")
				return
			}
			want := 0
			switch tc.matcher {
			case "clear|other":
				if tc.reason == SessionEndReasonClear || tc.reason == SessionEndReasonOther {
					want = 1
				}
			case "^prompt_input_exit$":
				if tc.reason == SessionEndReasonPromptInputExit {
					want = 1
				}
			}
			require.Equal(t, want, agg.HookCount, "the hook fires (or not) based on whether the reason matches")
		})
	}
}

// As in Claude Code, a SessionEnd hook cannot stop anything: a block and
// a halt are parsed but the aggregate says the exit goes ahead, and what
// it prints is ignored.
func TestRunSessionEndCannotStopAnything(t *testing.T) {
	t.Parallel()

	result, err := sessionEndRunner(t, "", `echo "not yet" >&2; exit 2`).RunSessionEnd(context.Background(), "sess", SessionEndReasonClear)
	require.NoError(t, err)
	require.Equal(t, DecisionDeny, result.Decision)

	result, err = sessionEndRunner(t, "", `echo "stop everything" >&2; exit 49`).RunSessionEnd(context.Background(), "sess", SessionEndReasonOther)
	require.NoError(t, err)
	require.True(t, result.Halt)

	// What the hook prints is ignored: it is neither context nor a
	// decision.
	result, err = sessionEndRunner(t, "", `echo "ignored output"`).RunSessionEnd(context.Background(), "sess", SessionEndReasonPromptInputExit)
	require.NoError(t, err)
	require.Equal(t, DecisionNone, result.Decision)
	require.Empty(t, result.Context)
}

// A SessionEnd hook that could not run is logged, and the exit goes
// ahead without it. The result says it could not evaluate, which blocks
// wherever a hook can; a SessionEnd hook cannot, and the app goes ahead
// whatever the result says.
func TestRunSessionEndFailedHookIsLogged(t *testing.T) {
	t.Parallel()

	logSwapMu.Lock()
	var buf strings.Builder
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelWarn})))
	t.Cleanup(func() { slog.SetDefault(prev); logSwapMu.Unlock() })

	result, err := sessionEndRunner(t, "", `./hooks/does-not-exist.sh`).RunSessionEnd(context.Background(), "sess", SessionEndReasonClear)
	require.NoError(t, err)
	require.Len(t, result.Hooks, 1)
	require.NotEmpty(t, result.Hooks[0].Error, "the failed hook is named in the result")
	require.True(t, result.Hooks[0].CouldNotEvaluate)
	require.Contains(t, buf.String(), "does-not-exist", "the failure is logged")
}

// SessionEnd's default timeout is Claude Code's 1.5 seconds, and a
// hook's own timeout is honored up to a cap of 60 seconds, so that
// exiting Klash is not held up for long.
func TestSessionEndTimeoutDefaults(t *testing.T) {
	t.Parallel()

	hook := &config.HookConfig{Command: "true"}
	require.Equal(t, 1500*time.Millisecond, hook.TimeoutDuration(EventSessionEnd),
		"the default is Claude Code's 1.5 seconds")
	require.Equal(t, 5*time.Second, (&config.HookConfig{Command: "true", Timeout: 5}).TimeoutDuration(EventSessionEnd),
		"a hook's own timeout is honored")
	require.Equal(t, 60*time.Second, (&config.HookConfig{Command: "true", Timeout: 60}).TimeoutDuration(EventSessionEnd),
		"60 seconds is the cap")
	require.Equal(t, 60*time.Second, (&config.HookConfig{Command: "true", Timeout: 120}).TimeoutDuration(EventSessionEnd),
		"beyond 60 the cap holds")
	// Other events are untouched.
	require.Equal(t, 600*time.Second, hook.TimeoutDuration(EventStop))
	require.Equal(t, 30*time.Second, hook.TimeoutDuration(EventUserPromptSubmit))
}

// reason belongs to SessionEnd: other events' payloads leave it out
// rather than carry it empty.
func TestReasonOnlyInSessionEndPayloads(t *testing.T) {
	t.Parallel()

	var payload map[string]any
	require.NoError(t, json.Unmarshal(BuildPayload(EventPreToolUse, "sess", "/p", "bash", `{"command":"ls"}`), &payload))
	_, has := payload["reason"]
	require.False(t, has)
	require.NoError(t, json.Unmarshal(BuildSessionStartPayload("sess", "/p", "m", SessionStartSourceStartup), &payload))
	_, has = payload["reason"]
	require.False(t, has)

	require.NoError(t, json.Unmarshal(BuildSessionEndPayload("sess", "/p", SessionEndReasonClear), &payload))
	require.Equal(t, "clear", payload["reason"])
}
