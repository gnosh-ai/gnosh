package hooks

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/charmbracelet/crush/internal/config"
	"github.com/stretchr/testify/require"
)

// The events Claude Code tells permission_mode carry the mode their
// context gives (WithPermissionMode), read as each hook fires, so a mode
// switched mid-turn shows. SessionStart and PreCompact, which Claude Code
// does not tell it, do not, and no event does without a mode.
func TestPermissionModeOnTheEventsClaudeCodeGivesItTo(t *testing.T) {
	t.Parallel()

	run := map[string]func(ctx context.Context, r *Runner) error{
		EventPreToolUse: func(ctx context.Context, r *Runner) error {
			_, err := r.Run(ctx, EventPreToolUse, "s1", "view", `{"file_path": "a.go"}`)
			return err
		},
		EventPostToolUse: func(ctx context.Context, r *Runner) error {
			_, err := r.RunPostToolUse(ctx, "s1", "view", `{"file_path": "a.go"}`, "package a", "", time.Second)
			return err
		},
		EventPostToolUseFailure: func(ctx context.Context, r *Runner) error {
			_, err := r.RunPostToolUseFailure(ctx, "s1", "view", `{"file_path": "a.go"}`, "no such file", false, time.Second)
			return err
		},
		EventUserPromptSubmit: func(ctx context.Context, r *Runner) error {
			_, err := r.RunUserPromptSubmit(ctx, "s1", "fix the build", nil)
			return err
		},
		EventStop: func(ctx context.Context, r *Runner) error {
			_, err := r.RunStop(ctx, "s1", "Done.", false)
			return err
		},
		EventSubagentStop: func(ctx context.Context, r *Runner) error {
			_, err := r.RunSubagentStop(ctx, "s1", "sub-1", "task", "Done.", false)
			return err
		},
		EventSessionStart: func(ctx context.Context, r *Runner) error {
			_, err := r.RunSessionStart(ctx, "s1", "qwen", SessionStartSourceStartup, "")
			return err
		},
		EventPreCompact: func(ctx context.Context, r *Runner) error {
			_, err := r.RunPreCompact(ctx, "s1", PreCompactTriggerManual, "")
			return err
		},
	}
	told := map[string]bool{
		EventPreToolUse: true, EventPostToolUse: true, EventPostToolUseFailure: true,
		EventUserPromptSubmit: true, EventStop: true, EventSubagentStop: true,
	}
	for event, call := range run {
		t.Run(event, func(t *testing.T) {
			t.Parallel()
			dir := t.TempDir()
			payload := filepath.Join(dir, "payload.json")
			cfg := &config.Config{Hooks: map[string][]config.HookConfig{event: {{Command: `cat > ` + payload}}}}
			require.NoError(t, cfg.ValidateHooks())
			r := NewRunner(cfg.Hooks[event], dir, dir)
			read := func() map[string]any {
				raw, err := os.ReadFile(payload)
				require.NoError(t, err)
				var p map[string]any
				require.NoError(t, json.Unmarshal(raw, &p))
				return p
			}

			mode := config.PermissionModePlan
			ctx := WithPermissionMode(t.Context(), func() string { return mode })
			require.NoError(t, call(ctx, r))
			if told[event] {
				require.Equal(t, config.PermissionModePlan, read()["permission_mode"])
				mode = config.PermissionModeBypass
				require.NoError(t, call(ctx, r))
				require.Equal(t, config.PermissionModeBypass, read()["permission_mode"], "switched since the last hook")
			} else {
				require.NotContains(t, read(), "permission_mode")
			}

			require.NoError(t, call(t.Context(), r))
			require.NotContains(t, read(), "permission_mode", "no mode in the context")
		})
	}
}

// A context that has a permission mode keeps it: a sub-agent's turn,
// which sets its own, works in the mode of the agent that started it.
func TestWithPermissionModeKeepsTheFirst(t *testing.T) {
	t.Parallel()
	ctx := WithPermissionMode(t.Context(), func() string { return config.PermissionModePlan })
	ctx = WithPermissionMode(ctx, func() string { return config.PermissionModeDefault })
	require.Equal(t, config.PermissionModePlan, permissionMode(ctx))
	require.Empty(t, permissionMode(t.Context()))
}
