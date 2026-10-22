package config

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

// Tools given on the command line apply to the agents and the permission
// list, and a reload from disk, which rebuilds the config from its files,
// keeps them.
func TestToolOverridesSurviveAReload(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("CRUSH_GLOBAL_CONFIG", dir)
	t.Setenv("CRUSH_GLOBAL_DATA", dir)
	resetProviderState()
	t.Cleanup(resetProviderState)
	require.NoError(t, os.WriteFile(filepath.Join(dir, "crush.json"), []byte(twoProviderConfig("openai", "gpt-4")), 0o600))

	store, err := Load(dir, dir, false)
	require.NoError(t, err)
	store.Overrides().DisabledTools = []string{"bash"}
	store.Overrides().AllowedTools = []string{"view"}
	store.SetupAgents()

	check := func(when string) {
		t.Helper()
		cfg := store.Config()
		require.NotContains(t, cfg.Agents[AgentCoder].AllowedTools, "bash", when)
		require.Contains(t, cfg.Agents[AgentCoder].AllowedTools, "edit", when)
		require.Contains(t, cfg.Permissions.AllowedTools, "view", when)
	}
	check("after the flags are applied")

	require.NoError(t, store.ReloadFromDisk(t.Context()))
	check("after a reload from disk")
}
