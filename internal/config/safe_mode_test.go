package config

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// Claude Code's --safe-mode: the config's hooks, MCP servers, context
// files and skills paths are off, the MCP servers given with --mcp-config
// still load, and the config says safe mode is on; without it nothing
// changes.
func TestApplySafeMode(t *testing.T) {
	t.Parallel()

	configured := func() *Config {
		return &Config{
			Hooks: map[string][]HookConfig{"PreToolUse": {{Command: "check.sh"}}},
			MCP:   MCPs{"github": MCPConfig{Command: "gh-mcp"}},
			Options: &Options{
				ContextPaths:       []string{"AGENTS.md"},
				GlobalContextPaths: []string{"~/.config/AGENTS.md"},
				SkillsPaths:        []string{"~/.config/klash/skills"},
			},
		}
	}

	cfg := configured()
	applyToolOverrides(cfg, RuntimeOverrides{SafeMode: true, MCP: MCPs{"given": MCPConfig{Command: "given-mcp"}}})
	require.True(t, cfg.SafeMode)
	require.Nil(t, cfg.Hooks)
	require.Equal(t, MCPs{"given": MCPConfig{Command: "given-mcp"}}, cfg.MCP, "only --mcp-config's")
	require.Empty(t, cfg.Options.ContextPaths)
	require.Empty(t, cfg.Options.GlobalContextPaths)
	require.Empty(t, cfg.Options.SkillsPaths)

	cfg = configured()
	applyToolOverrides(cfg, RuntimeOverrides{})
	require.Equal(t, configured(), cfg)
}
