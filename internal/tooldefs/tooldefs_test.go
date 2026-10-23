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
// for the fetch tools beyond --allowedTools' one each, and, as a guard
// only, those of the Claude Code tools whose job a tool does with input
// of another shape.
func TestClaudeCodeNamesOf(t *testing.T) {
	t.Parallel()

	require.Equal(t, []string{"Agent", "Task"}, ClaudeCodeNamesOf("agent"))
	require.Equal(t, []string{"WebFetch"}, ClaudeCodeNamesOf("fetch"))
	require.Equal(t, []string{"WebFetch", "WebSearch"}, ClaudeCodeNamesOf("agentic_fetch"))
	require.Equal(t, []string{"WebFetch"}, ClaudeCodeNamesOf("web_fetch"))
	require.Equal(t, []string{"WebSearch"}, ClaudeCodeNamesOf("web_search"))
	require.Equal(t, []string{"KillShell", "TaskStop"}, ClaudeCodeNamesOf("job_kill"))
	require.Equal(t, []string{"BashOutput", "TaskOutput"}, ClaudeCodeNamesOf("job_output"))
	require.Equal(t, []string{"AskUserQuestion"}, ClaudeCodeNamesOf("question"))
	require.Empty(t, ClaudeCodeNamesOf("gnokey"))
	require.Empty(t, ClaudeCodeGuardNamesOf("gnokey"))
	require.Equal(t, "fetch", FromClaudeCode("WebFetch"), "--allowedTools WebFetch is still fetch")
	require.Equal(t, "job_kill", FromClaudeCode("TaskStop"))

	require.Equal(t, []string{"MultiEdit"}, ClaudeCodeNamesOf("multiedit"))
	require.Equal(t, []string{"Edit"}, ClaudeCodeGuardNamesOf("multiedit"))
	require.Empty(t, ClaudeCodeNamesOf("download"))
	require.ElementsMatch(t, []string{"WebFetch", "Write"}, ClaudeCodeGuardNamesOf("download"))
	require.Equal(t, []string{"LSP"}, ClaudeCodeGuardNamesOf("lsp_references"))
	require.Empty(t, ClaudeCodeGuardNamesOf("edit"), "Edit is edit's own name")
	require.Empty(t, FromClaudeCode("LSP"), "--allowedTools takes one tool for each name, and LSP is six")

	for _, name := range []string{"web_fetch", "web_search", "WebSearch", "WebFetch", "Bash", "bash", "crush_logs", "LSP", "TaskStop", "AskUserQuestion"} {
		require.Truef(t, NamesATool(name), name)
	}
	require.False(t, NamesATool("Notebook"))
	require.False(t, NamesATool("NotebookEdit"))
}

// Every Claude Code name stands for a built-in tool, and no tool is
// guarded by a name that is already its own, which would take that
// name's approvals from it.
func TestClaudeCodeNamesNameBuiltIns(t *testing.T) {
	t.Parallel()

	for claudeCode, tool := range claudeCodeNames {
		require.Truef(t, IsKnown(tool), "%s: %s", claudeCode, tool)
	}
	for tool, guards := range claudeCodeGuardedAs {
		require.Truef(t, IsKnown(tool), tool)
		for _, guard := range guards {
			require.NotContainsf(t, ClaudeCodeNamesOf(tool), guard, tool)
		}
	}
}
