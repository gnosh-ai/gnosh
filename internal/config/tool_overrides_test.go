package config

import (
	"encoding/json"
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
	store.Overrides().DenyRules = []string{"git push *"}
	store.SetupAgents()

	check := func(when string) {
		t.Helper()
		cfg := store.Config()
		require.NotContains(t, cfg.Agents[AgentCoder].AllowedTools, "bash", when)
		require.Contains(t, cfg.Agents[AgentCoder].AllowedTools, "edit", when)
		require.Contains(t, cfg.Permissions.AllowedTools, "view", when)
		require.Equal(t, []string{"git push *"}, cfg.Permissions.BashDenyPatterns(), when)
	}
	check("after the flags are applied")

	require.NoError(t, store.ReloadFromDisk(t.Context()))
	check("after a reload from disk")
}

// MCP servers loaded with --mcp-config join the configured ones, each in
// place of a configured server of the same name, or with
// --strict-mcp-config are the only ones; a reload from disk keeps them.
func TestMCPOverridesSurviveAReload(t *testing.T) {
	for _, strict := range []bool{false, true} {
		dir := t.TempDir()
		t.Setenv("CRUSH_GLOBAL_CONFIG", dir)
		t.Setenv("CRUSH_GLOBAL_DATA", dir)
		resetProviderState()
		t.Cleanup(resetProviderState)
		var doc map[string]any
		require.NoError(t, json.Unmarshal([]byte(twoProviderConfig("openai", "gpt-4")), &doc))
		doc["mcp"] = map[string]any{
			"configured": map[string]any{"type": "stdio", "command": "configured-server"},
			"shared":     map[string]any{"type": "stdio", "command": "old-server"},
		}
		data, err := json.Marshal(doc)
		require.NoError(t, err)
		require.NoError(t, os.WriteFile(filepath.Join(dir, "crush.json"), data, 0o600))

		store, err := Load(dir, dir, false)
		require.NoError(t, err)
		store.Overrides().MCP = MCPs{
			"shared": {Type: MCPStdio, Command: "new-server"},
			"flag":   {Type: MCPHttp, URL: "https://mcp.example"},
		}
		store.Overrides().StrictMCP = strict
		store.SetupAgents()

		check := func(when string) {
			t.Helper()
			mcp := store.Config().MCP
			require.Equal(t, "new-server", mcp["shared"].Command, when)
			require.Equal(t, "https://mcp.example", mcp["flag"].URL, when)
			if strict {
				require.NotContains(t, mcp, "configured", when)
			} else {
				require.Equal(t, "configured-server", mcp["configured"].Command, when)
			}
		}
		check("after the flags are applied")
		require.NoError(t, store.ReloadFromDisk(t.Context()))
		check("after a reload from disk")
	}
}
