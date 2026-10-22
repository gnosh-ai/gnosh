package shellconfig

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// A tool name that matches nothing used to be written straight into the
// config, where it silently failed to pre-approve anything. Since the
// loader now refuses such a config outright, writing it would hand the
// user a Crush that will not start.
func TestPermissionsAllowRejectsUnknownTool(t *testing.T) {
	t.Parallel()

	for _, bad := range []string{"Bash", "shell", "edit_file", "websearch"} {
		t.Run(bad, func(t *testing.T) {
			t.Parallel()
			err := loadScriptErr(t, `permissions allow `+bad)
			require.ErrorContains(t, err, "permissions allow")
			require.ErrorContains(t, err, "is not a built-in tool")
			require.ErrorContains(t, err, "bash", "the error must list the names that work")
		})
	}
}

func TestPermissionsAllowAcceptsBuiltinsAndMCPTools(t *testing.T) {
	t.Parallel()

	result := loadScript(t, `permissions allow view edit mcp_github_get_me`)
	allowed := result["permissions"].(map[string]any)["allowed_tools"].([]any)
	require.ElementsMatch(t, []any{"view", "edit", "mcp_github_get_me"}, allowed)
}

// deny writes options.disabled_tools, which filters the built-in set, so
// an MCP name there would do nothing at all.
func TestPermissionsDenyRejectsMCPToolNames(t *testing.T) {
	t.Parallel()

	err := loadScriptErr(t, `permissions deny mcp_github_get_me`)
	require.ErrorContains(t, err, "permissions deny")
	require.ErrorContains(t, err, "is not a built-in tool")
	require.NotContains(t, err.Error(), "is not an MCP tool",
		"deny does not offer the MCP escape hatch, so it must not advertise it")
}

func TestPermissionsDenyRejectsUnknownTool(t *testing.T) {
	t.Parallel()

	err := loadScriptErr(t, `permissions deny Bash`)
	require.ErrorContains(t, err, "permissions deny")
	require.ErrorContains(t, err, `"Bash"`)
}

func TestPermissionsDenyAcceptsBuiltins(t *testing.T) {
	t.Parallel()

	result := loadScript(t, `permissions deny bash write`)
	disabled := result["options"].(map[string]any)["disabled_tools"].([]any)
	require.ElementsMatch(t, []any{"bash", "write"}, disabled)
}

// One bad name in a list must reject the line, not write the good ones
// and drop the bad one.
func TestPermissionsAllowRejectsTheWholeLine(t *testing.T) {
	t.Parallel()

	err := loadScriptErr(t, `permissions allow view nonsense edit`)
	require.ErrorContains(t, err, `"nonsense"`)
}
