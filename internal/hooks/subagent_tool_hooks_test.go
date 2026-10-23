package hooks

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/crush/internal/config"
	"github.com/stretchr/testify/require"
)

// The hooks around a sub-agent's tool call see, as in Claude Code, the
// session it works for as session_id and the sub-agent as agent_id and
// agent_type, in the payload and the environment; the main agent's
// calls carry neither.
func TestToolHooksOfASubAgentsCallNameTheSubAgent(t *testing.T) {
	t.Parallel()

	run := map[string]func(ctx context.Context, r *Runner) error{
		EventPreToolUse: func(ctx context.Context, r *Runner) error {
			_, err := r.Run(ctx, EventPreToolUse, "sub-1", "view", `{"file_path": "a.go"}`)
			return err
		},
		EventPostToolUse: func(ctx context.Context, r *Runner) error {
			_, err := r.RunPostToolUse(ctx, "sub-1", "view", `{"file_path": "a.go"}`, "package a", "", time.Second)
			return err
		},
		EventPostToolUseFailure: func(ctx context.Context, r *Runner) error {
			_, err := r.RunPostToolUseFailure(ctx, "sub-1", "view", `{"file_path": "a.go"}`, "no such file", false, time.Second)
			return err
		},
	}
	for event, call := range run {
		t.Run(event, func(t *testing.T) {
			t.Parallel()
			dir := t.TempDir()
			payload, envOut := filepath.Join(dir, "payload.json"), filepath.Join(dir, "env.txt")
			cfg := &config.Config{Hooks: map[string][]config.HookConfig{event: {{
				Command: `cat > ` + payload + `; printf '%s|%s|%s' "$KLASH_SESSION_ID" "$KLASH_AGENT_ID" "$KLASH_AGENT_TYPE" > ` + envOut,
			}}}}
			require.NoError(t, cfg.ValidateHooks())
			r := NewRunner(cfg.Hooks[event], dir, dir)

			read := func() (Payload, string) {
				raw, err := os.ReadFile(payload)
				require.NoError(t, err)
				var p Payload
				require.NoError(t, json.Unmarshal(raw, &p))
				env, err := os.ReadFile(envOut)
				require.NoError(t, err)
				return p, string(env)
			}

			require.NoError(t, call(WithSubAgent(t.Context(), "main-1", "task"), r))
			p, env := read()
			require.Equal(t, "main-1", p.SessionID)
			require.Equal(t, "sub-1", p.AgentID)
			require.Equal(t, "task", p.AgentType)
			require.Equal(t, "main-1|sub-1|task", env)

			require.NoError(t, call(t.Context(), r))
			p, env = read()
			require.Equal(t, "sub-1", p.SessionID, "the main agent's call is in its own session")
			require.Empty(t, p.AgentID)
			require.Empty(t, p.AgentType)
			require.Equal(t, "sub-1||", env)
			raw, err := os.ReadFile(payload)
			require.NoError(t, err)
			require.False(t, strings.Contains(string(raw), "agent_id"), "omitted, not empty")
		})
	}
}
