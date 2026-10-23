package tooldefs

import (
	"slices"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestNamesIsACopy(t *testing.T) {
	t.Parallel()

	got := Names()
	got[0] = "tampered"
	require.False(t, IsKnown("tampered"))
	require.True(t, IsKnown("agent"))
}

func TestNamesHasNoDuplicates(t *testing.T) {
	t.Parallel()

	sorted := Names()
	slices.Sort(sorted)
	require.Equal(t, len(Names()), len(slices.Compact(sorted)))
}

func TestIsKnownIsExact(t *testing.T) {
	t.Parallel()

	require.True(t, IsKnown("bash"))
	require.False(t, IsKnown("Bash"), "matching is exact, as the config loader's is")
	require.False(t, IsKnown(""))
	require.False(t, IsKnown("mcp_github_get_me"), "MCP tools are not built-ins")
}

func TestIsMCPToolName(t *testing.T) {
	t.Parallel()

	require.True(t, IsMCPToolName("mcp_github_get_me"))
	require.False(t, IsMCPToolName("mcp_"), "the prefix alone names no tool")
	require.False(t, IsMCPToolName("mcp"))
	require.False(t, IsMCPToolName("bash"))
	require.False(t, IsMCPToolName(""))
}

// A hook matcher can use Claude Code's names for a tool, including those
// for the fetch tools beyond --allowedTools' one each.
func TestClaudeCodeNamesOf(t *testing.T) {
	t.Parallel()

	require.Equal(t, []string{"Agent", "Task"}, ClaudeCodeNamesOf("agent"))
	require.Equal(t, []string{"WebFetch"}, ClaudeCodeNamesOf("fetch"))
	require.Equal(t, []string{"WebFetch", "WebSearch"}, ClaudeCodeNamesOf("agentic_fetch"))
	require.Equal(t, []string{"WebFetch"}, ClaudeCodeNamesOf("web_fetch"))
	require.Equal(t, []string{"WebSearch"}, ClaudeCodeNamesOf("web_search"))
	require.Empty(t, ClaudeCodeNamesOf("gnokey"))
	require.Equal(t, "fetch", FromClaudeCode("WebFetch"), "--allowedTools WebFetch is still fetch")

	for _, name := range []string{"web_fetch", "web_search", "WebSearch", "WebFetch", "Bash", "bash", "crush_logs"} {
		require.Truef(t, NamesATool(name), name)
	}
	require.False(t, NamesATool("Notebook"))
}
