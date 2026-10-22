package config

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// Disabling a tool is a safety decision. A name that matches nothing
// leaves the tool fully enabled while the config reads as though it is
// off, which is the worst possible way for this to fail.
func TestValidateToolsRejectsUnknownDisabledTool(t *testing.T) {
	t.Parallel()

	for _, bad := range []string{"Bash", "BASH", "bash_tool", "websearch", "web_search", "rm", ""} {
		t.Run(bad, func(t *testing.T) {
			t.Parallel()
			cfg := &Config{Options: &Options{DisabledTools: []string{bad}}}
			err := cfg.ValidateTools()
			require.Error(t, err)
			require.Contains(t, err.Error(), "options.disabled_tools")
			require.Contains(t, err.Error(), "is not a built-in tool")
			require.Contains(t, err.Error(), "bash", "the error must list the names that do work")
		})
	}
}

func TestValidateToolsAcceptsEveryBuiltinAsDisabled(t *testing.T) {
	t.Parallel()

	cfg := &Config{Options: &Options{DisabledTools: KnownToolNames()}}
	require.NoError(t, cfg.ValidateTools(),
		"every name the filter actually matches must be accepted")
}

// A pre-approved tool that matches nothing keeps prompting, which in an
// unattended session means it stalls instead of running.
func TestValidateToolsRejectsUnknownAllowedTool(t *testing.T) {
	t.Parallel()

	for _, bad := range []string{"Bash", "shell", "edit_file", "mcp"} {
		t.Run(bad, func(t *testing.T) {
			t.Parallel()
			cfg := &Config{Permissions: &Permissions{AllowedTools: []string{bad}}}
			err := cfg.ValidateTools()
			require.Error(t, err)
			require.Contains(t, err.Error(), "permissions.allowed_tools")
		})
	}
}

func TestValidateToolsAcceptsBuiltinAndMCPAllowedTools(t *testing.T) {
	t.Parallel()

	cfg := &Config{Permissions: &Permissions{AllowedTools: []string{
		"bash", "view", "edit",
		"mcp_github_get_me",
		"mcp_anything_at_all",
	}}}
	require.NoError(t, cfg.ValidateTools(),
		"MCP tool names are discovered at connect time, so only their shape can be checked")
}

// The MCP escape hatch is for allowed_tools only: disabled_tools filters
// the built-in set, so an mcp_ name there would still be a no-op.
func TestValidateToolsDoesNotAcceptMCPNamesAsDisabled(t *testing.T) {
	t.Parallel()

	cfg := &Config{Options: &Options{DisabledTools: []string{"mcp_github_get_me"}}}
	require.ErrorContains(t, cfg.ValidateTools(), "options.disabled_tools")
}

func TestValidateToolsEmpty(t *testing.T) {
	t.Parallel()

	require.NoError(t, (&Config{}).ValidateTools())
	require.NoError(t, (&Config{Options: &Options{}}).ValidateTools())
	require.NoError(t, (&Config{Permissions: &Permissions{}}).ValidateTools())
	require.NoError(t, (&Config{
		Options:     &Options{DisabledTools: []string{}},
		Permissions: &Permissions{AllowedTools: []string{}},
	}).ValidateTools())
}

func TestIsKnownToolName(t *testing.T) {
	t.Parallel()

	require.True(t, IsKnownToolName("bash"))
	require.True(t, IsKnownToolName("lsp_rename"))
	require.False(t, IsKnownToolName("Bash"))
	require.False(t, IsKnownToolName(""))
}

// The reported list must be the same set the filter uses, or the error
// would send the user to a name that still does nothing.
func TestKnownToolNamesMatchesTheFilterSet(t *testing.T) {
	t.Parallel()

	known := KnownToolNames()
	require.NotEmpty(t, known)
	for _, name := range known {
		remaining := resolveAllowedTools(allToolNames(), []string{name})
		require.NotContains(t, remaining, name,
			"%q is offered as a valid disabled_tools entry, so disabling it must actually remove it", name)
		require.Len(t, remaining, len(known)-1)
	}
}

func TestValidateIncludesTools(t *testing.T) {
	t.Parallel()

	cfg := &Config{Options: &Options{DisabledTools: []string{"nope"}}}
	require.ErrorContains(t, cfg.Validate(), "invalid tool configuration")
}

// A tool named as it was before the rename to Crash is the tool it is now:
// a config written for crush_logs keeps loading, and means crash_logs.
func TestValidateToolsTakesLegacyToolNames(t *testing.T) {
	t.Parallel()

	cfg := &Config{
		Options:     &Options{DisabledTools: []string{"crush_logs"}, DisabledSkills: []string{"crush-config", "jq"}},
		Permissions: &Permissions{AllowedTools: []string{"crush_info", "bash"}},
	}
	require.NoError(t, cfg.ValidateTools())
	require.Equal(t, []string{"crash_logs"}, cfg.Options.DisabledTools)
	require.Equal(t, []string{"crash_info", "bash"}, cfg.Permissions.AllowedTools)
	require.Equal(t, []string{"crash-config", "jq"}, cfg.Options.DisabledSkills, "skills renamed with Crush are the same")
}
