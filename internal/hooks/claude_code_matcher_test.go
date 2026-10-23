package hooks

import (
	"context"
	"testing"

	"github.com/charmbracelet/crush/internal/config"
	"github.com/stretchr/testify/require"
)

// A matcher written for Claude Code matches Klash's tool that does the
// same job: Bash matches bash, Read view, Edit|Write edit and write but
// not multiedit, and an MCP tool by its Claude Code name,
// mcp__<server>__<tool>. Those hooks never fired before: their matchers
// were tested against Klash's names only.
func TestClaudeCodeMatchersMatchKlashTools(t *testing.T) {
	t.Parallel()

	var cfgs []config.HookConfig
	for _, m := range []string{"Bash", "Edit|Write", "Read", "edit", "Task", "WebFetch", "WebSearch", "mcp__srv__.*", "*", "^bash$"} {
		cfgs = append(cfgs, config.HookConfig{Command: m, Matcher: m})
	}
	r := NewRunner(cfgs, t.TempDir(), t.TempDir())
	fired := func(ctx context.Context, tool string) []string {
		var commands []string
		for _, h := range r.matchingHooks(toolNames(ctx, tool)...) {
			commands = append(commands, h.Command)
		}
		return commands
	}

	ctx := t.Context()
	require.ElementsMatch(t, []string{"Bash", "*", "^bash$"}, fired(ctx, "bash"))
	require.ElementsMatch(t, []string{"Edit|Write", "edit", "*"}, fired(ctx, "edit"))
	require.ElementsMatch(t, []string{"Edit|Write", "*"}, fired(ctx, "write"))
	require.ElementsMatch(t, []string{"*"}, fired(ctx, "multiedit"))
	require.ElementsMatch(t, []string{"Read", "*"}, fired(ctx, "view"))
	require.ElementsMatch(t, []string{"Task", "*"}, fired(ctx, "agent"))
	require.ElementsMatch(t, []string{"WebFetch", "*"}, fired(ctx, "fetch"))
	require.ElementsMatch(t, []string{"WebFetch", "WebSearch", "*"}, fired(ctx, "agentic_fetch"))
	require.ElementsMatch(t, []string{"WebFetch", "*"}, fired(ctx, "web_fetch"), "agentic_fetch's sub-agent fetches too")
	require.ElementsMatch(t, []string{"WebSearch", "*"}, fired(ctx, "web_search"))
	require.ElementsMatch(t, []string{"*"}, fired(ctx, "mcp_srv_tool"), "an MCP tool's own name is not Claude Code's")
	require.ElementsMatch(t, []string{"mcp__srv__.*", "*"}, fired(WithToolAliases(ctx, "mcp__srv__tool"), "mcp_srv_tool"))
}
