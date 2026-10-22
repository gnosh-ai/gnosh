package hooks

import (
	"context"
	"testing"

	"github.com/charmbracelet/crush/internal/config"
	"github.com/stretchr/testify/require"
)

func promptRunner(t *testing.T, commands ...string) *Runner {
	t.Helper()
	hookCfgs := make([]config.HookConfig, len(commands))
	for i, cmd := range commands {
		hookCfgs[i] = config.HookConfig{Command: cmd}
	}
	cfg := &config.Config{
		Hooks: map[string][]config.HookConfig{EventUserPromptSubmit: hookCfgs},
	}
	require.NoError(t, cfg.ValidateHooks())
	dir := t.TempDir()
	return NewRunner(cfg.Hooks[EventUserPromptSubmit], dir, dir)
}

// As in Claude Code, plain stdout from a UserPromptSubmit hook is context
// for the prompt, joined in config order with what other hooks add.
func TestRunUserPromptSubmitPlainStdoutIsContext(t *testing.T) {
	t.Parallel()

	r := promptRunner(t, `echo "first line"; echo "second line"`, `echo '{"context": "from JSON"}'`)
	result, err := r.RunUserPromptSubmit(context.Background(), "sess", "fix it", nil)
	require.NoError(t, err)
	require.Equal(t, DecisionNone, result.Decision)
	require.Equal(t, "first line\nsecond line\nfrom JSON", result.Context)
}

// For any other event, plain stdout is not read: a PreToolUse hook that
// echoes a log line must not turn it into context.
func TestPlainStdoutIsNotContextForOtherEvents(t *testing.T) {
	t.Parallel()

	r := NewRunner([]config.HookConfig{{Command: `echo "just logging"`}}, t.TempDir(), t.TempDir())
	result, err := r.Run(context.Background(), EventPreToolUse, "sess", "bash", `{"command":"ls"}`)
	require.NoError(t, err)
	require.Empty(t, result.Context)
}

// Exit 2 blocks the prompt with stderr as the reason; exit 49 halts.
func TestRunUserPromptSubmitBlocks(t *testing.T) {
	t.Parallel()

	result, err := promptRunner(t, `echo "no secrets" >&2; exit 2`).RunUserPromptSubmit(context.Background(), "sess", "sk-123", nil)
	require.NoError(t, err)
	require.Equal(t, DecisionDeny, result.Decision)
	require.Equal(t, "no secrets", result.Reason)

	result, err = promptRunner(t, `echo "stop everything" >&2; exit 49`).RunUserPromptSubmit(context.Background(), "sess", "go", nil)
	require.NoError(t, err)
	require.True(t, result.Halt)
}

// There is no tool to match when a prompt is sent, so a matcher on a
// UserPromptSubmit hook is a config error, as on a Stop hook.
func TestUserPromptSubmitRejectsAMatcher(t *testing.T) {
	t.Parallel()

	cfg := &config.Config{Hooks: map[string][]config.HookConfig{
		EventUserPromptSubmit: {{Command: "true", Matcher: "^bash$"}},
	}}
	require.Error(t, cfg.ValidateHooks())
}
