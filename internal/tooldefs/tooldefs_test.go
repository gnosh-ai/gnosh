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
