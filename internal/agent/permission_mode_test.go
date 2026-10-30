package agent

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"

	"charm.land/catwalk/pkg/catwalk"
	"github.com/charmbracelet/crush/internal/config"
	"github.com/charmbracelet/crush/internal/hooks"
	"github.com/charmbracelet/crush/internal/message"
	"github.com/charmbracelet/crush/internal/permission"
	"github.com/stretchr/testify/require"
)

// A turn's hooks are told the session's permission mode, as Claude Code
// tells them: plan for the plan agent whatever else is set, else what
// the coordinator says, default with nothing set.
func TestRun_HooksAreToldThePermissionMode(t *testing.T) {
	t.Parallel()
	bypass := func(string) string { return config.PermissionModeBypass }
	for _, tc := range []struct {
		name      string
		agentType string
		mode      func(string) string
		want      string
	}{
		{"nothing set", config.AgentCoder, nil, config.PermissionModeDefault},
		{"the coordinator's", config.AgentCoder, bypass, config.PermissionModeBypass},
		{"the plan agent", config.AgentPlan, bypass, config.PermissionModePlan},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			env := testEnv(t)
			dir := t.TempDir()
			stop, prompt := filepath.Join(dir, "stop.json"), filepath.Join(dir, "prompt.json")
			cfg := &config.Config{Hooks: map[string][]config.HookConfig{
				hooks.EventStop:             {{Command: "cat > " + stop}},
				hooks.EventUserPromptSubmit: {{Command: "cat > " + prompt}},
			}}
			require.NoError(t, cfg.ValidateHooks())
			var turns atomic.Int64
			model := Model{Model: textOnlyModel("Done.", &turns), CatwalkCfg: catwalk.Model{ContextWindow: 200000, DefaultMaxTokens: 1000}}
			a := NewSessionAgent(SessionAgentOptions{
				LargeModel:           model,
				SmallModel:           model,
				SystemPrompt:         "test",
				IsYolo:               true,
				DisableAutoSummarize: true,
				Sessions:             env.sessions,
				Messages:             env.messages,
				Cfg:                  config.NewTestStoreIn(cfg, env.workingDir),
				AgentType:            tc.agentType,
				PermissionMode:       tc.mode,
			}).(*sessionAgent)
			sess, err := env.sessions.Create(t.Context(), "permission mode")
			require.NoError(t, err)
			_, err = env.messages.Create(t.Context(), sess.ID, message.CreateMessageParams{
				Role:  message.User,
				Parts: []message.ContentPart{message.TextContent{Text: "earlier prompt"}},
			})
			require.NoError(t, err)

			_, err = a.Run(t.Context(), SessionAgentCall{SessionID: sess.ID, Prompt: "fix the build"})
			require.NoError(t, err)
			for _, path := range []string{prompt, stop} {
				raw, err := os.ReadFile(path)
				require.NoError(t, err)
				var p map[string]any
				require.NoError(t, json.Unmarshal(raw, &p))
				require.Equal(t, tc.want, p["permission_mode"], filepath.Base(path))
			}
		})
	}
}

// The coordinator's permission mode for a session: bypassPermissions
// when its requests are all granted, by yolo or as a gnosh run's own
// session; acceptEdits under --permission-mode acceptEdits; else
// default.
func TestCoordinatorPermissionMode(t *testing.T) {
	t.Parallel()
	perms := permission.NewPermissionService(t.TempDir(), false, nil)
	store := config.NewTestStore(&config.Config{})
	c := &coordinator{permissions: perms, cfg: store}

	require.Equal(t, config.PermissionModeDefault, c.permissionMode("s1"))
	store.Overrides().AcceptEdits = true
	require.Equal(t, config.PermissionModeAcceptEdits, c.permissionMode("s1"))
	perms.AutoApproveSession("s1")
	require.Equal(t, config.PermissionModeBypass, c.permissionMode("s1"), "a gnosh run's session")
	require.Equal(t, config.PermissionModeAcceptEdits, c.permissionMode("s2"))
	perms.SetSkipRequests(true)
	require.Equal(t, config.PermissionModeBypass, c.permissionMode("s2"), "yolo")
}

// An agent the coordinator builds is told its sessions' permission mode
// by the coordinator: the test environment's permissions grant every
// request, so bypassPermissions.
func TestBuiltAgentsAreToldThePermissionMode(t *testing.T) {
	coord := newGateTestCoordinator(t, true)
	agent, ok := coord.mainAgent.(*sessionAgent)
	require.True(t, ok)
	require.Equal(t, config.PermissionModeBypass, agent.permissionMode("s1"))
}
