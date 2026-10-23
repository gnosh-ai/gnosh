package agent

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/charmbracelet/crush/internal/agent/prompt"
	"github.com/charmbracelet/crush/internal/config"
	"github.com/stretchr/testify/require"
)

// The coordinator applies --system-prompt and --append-system-prompt (in
// RuntimeOverrides) when it builds its top-level agents: the coder's own
// instructions give way to the user's, its environment and the project's
// KLASH.md stay, and the appended text ends the coder's and the plan
// agent's prompts.
func TestNewCoordinatorUsesTheSystemPromptFlags(t *testing.T) {
	env := testEnv(t)
	t.Setenv("CRUSH_GLOBAL_CONFIG", filepath.Join(t.TempDir(), "crush.json"))
	crushJSON := `{
  "options": {"disable_default_providers": true, "disable_provider_auto_update": true},
  "providers": {"mock": {"id": "mock", "name": "Mock", "type": "openai",
    "base_url": "http://127.0.0.1:9/v1", "api_key": "test-key",
    "models": [{"id": "mock-model", "name": "Mock", "context_window": 8192, "default_max_tokens": 128}]}},
  "models": {"large": {"provider": "mock", "model": "mock-model"},
             "small": {"provider": "mock", "model": "mock-model"}}
}`
	require.NoError(t, os.WriteFile(filepath.Join(env.workingDir, "crush.json"), []byte(crushJSON), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(env.workingDir, "KLASH.md"), []byte("Run the linter before every commit."), 0o644))

	prompts := func(replace, appended string) (coder, plan string) {
		cfg, err := config.Init(env.workingDir, "", false)
		require.NoError(t, err)
		cfg.SetupAgents()
		cfg.Overrides().SystemPrompt = replace
		cfg.Overrides().AppendSystemPrompt = appended
		c, err := NewCoordinator(t.Context(), CoordinatorOptions{
			Config:      cfg,
			Sessions:    env.sessions,
			Messages:    env.messages,
			Permissions: env.permissions,
			History:     env.history,
			FileTracker: *env.filetracker,
		})
		require.NoError(t, err)
		coord := c.(*coordinator)
		require.NoError(t, coord.readyWg.Wait())
		return coord.agents[config.AgentCoder].(*sessionAgent).systemPrompt.Get(),
			coord.agents[config.AgentPlan].(*sessionAgent).systemPrompt.Get()
	}

	coder, plan := prompts("", "")
	require.True(t, strings.HasPrefix(coder, "You are Klash, a powerful AI Assistant"), "without the flags the built-in instructions are used")
	require.Contains(t, coder, "Run the linter before every commit.")
	plainPlan := plan

	coder, plan = prompts("You answer only in haiku.", "End every answer with DONE.")
	require.True(t, strings.HasPrefix(coder, "You answer only in haiku.\n\n<env>\nWorking directory: "+env.workingDir), "the user's instructions come first, then the environment: %.120q", coder)
	require.NotContains(t, coder, "You are Klash, a powerful AI Assistant", "--system-prompt replaces the built-in instructions")
	require.Contains(t, coder, "Run the linter before every commit.", "the project's KLASH.md is kept, as Claude Code keeps CLAUDE.md")
	require.True(t, strings.HasSuffix(coder, "\n\nEnd every answer with DONE."), "--append-system-prompt ends the coder's prompt")
	require.Equal(t, plainPlan+"\n\nEnd every answer with DONE.", plan, "the plan agent keeps its own instructions and gets the appended text")
}

// --system-prompt keeps the built-in template from its <env> line on, so
// the line must be there.
func TestBuiltinCoderTemplateHasAnEnvLine(t *testing.T) {
	require.True(t, strings.HasPrefix(builtinCoderContext(), "<env>\n"))
	require.Contains(t, builtinCoderContext(), "{{range .ContextFiles}}")
}

// --permission-mode plan (RuntimeOverrides.MainAgent) starts the session
// with the plan agent, in the TUI and in klash run alike.
func TestNewCoordinatorStartsWithThePlanAgent(t *testing.T) {
	env := testEnv(t)
	t.Setenv("CRUSH_GLOBAL_CONFIG", filepath.Join(t.TempDir(), "crush.json"))
	crushJSON := `{
  "options": {"disable_default_providers": true, "disable_provider_auto_update": true},
  "providers": {"mock": {"id": "mock", "name": "Mock", "type": "openai",
    "base_url": "http://127.0.0.1:9/v1", "api_key": "test-key",
    "models": [{"id": "mock-model", "name": "Mock", "context_window": 8192, "default_max_tokens": 128}]}},
  "models": {"large": {"provider": "mock", "model": "mock-model"},
             "small": {"provider": "mock", "model": "mock-model"}}
}`
	require.NoError(t, os.WriteFile(filepath.Join(env.workingDir, "crush.json"), []byte(crushJSON), 0o644))
	for _, start := range []string{"", config.AgentPlan} {
		cfg, err := config.Init(env.workingDir, "", false)
		require.NoError(t, err)
		cfg.SetupAgents()
		cfg.Overrides().MainAgent = start
		c, err := NewCoordinator(t.Context(), CoordinatorOptions{
			Config:      cfg,
			Sessions:    env.sessions,
			Messages:    env.messages,
			Permissions: env.permissions,
			History:     env.history,
			FileTracker: *env.filetracker,
		})
		require.NoError(t, err)
		coord := c.(*coordinator)
		agent, name := coord.activeAgent()
		want := config.AgentCoder
		if start == config.AgentPlan {
			want = config.AgentPlan
		}
		require.Equal(t, want, name)
		require.Same(t, coord.agents[want], agent)
	}
}

// Directories added with --add-dir are named in the coder's environment,
// so the model knows it may work there, as Claude Code's prompt names them.
func TestCoderPromptNamesTheAddedDirs(t *testing.T) {
	store := config.NewTestStore(&config.Config{Options: &config.Options{}})
	p, err := coderPrompt(prompt.WithWorkingDir(t.TempDir()))
	require.NoError(t, err)

	plain, err := p.Build(t.Context(), "", "", store)
	require.NoError(t, err)
	require.NotContains(t, plain, "Additional working directories")

	store.Overrides().AddedDirs = []string{"/src/gno", "/src/bubblerumble"}
	withDirs, err := p.Build(t.Context(), "", "", store)
	require.NoError(t, err)
	require.Contains(t, withDirs, "Working directory: ")
	require.Contains(t, withDirs, "\nAdditional working directories: /src/gno, /src/bubblerumble\nIs directory a git repo:")
}
