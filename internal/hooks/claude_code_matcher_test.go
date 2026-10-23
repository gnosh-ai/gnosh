package hooks

import (
	"context"
	"testing"

	"github.com/charmbracelet/crush/internal/config"
	"github.com/stretchr/testify/require"
)

// A matcher written for Claude Code matches Klash's tool that does the
// same job: Bash matches bash, Read view, Edit|Write edit and write, and
// an MCP tool by its Claude Code name, mcp__<server>__<tool>. Those hooks
// never fired before: their matchers were tested against Klash's names
// only. It also matches the tools Claude Code has nothing like, by the
// name of the one whose job they do: Edit|Write matches lsp_rename and
// download, WebFetch download and x402_fetch. Before, a guard on file
// edits copied from Claude Code was walked around through those tools.
func TestClaudeCodeMatchersMatchKlashTools(t *testing.T) {
	t.Parallel()

	var cfgs []config.HookConfig
	for _, m := range []string{"Bash", "Edit|Write", "Read", "edit", "Task", "WebFetch", "WebSearch", "LSP", "TaskStop", "AskUserQuestion", "mcp__srv__.*", "*", "^bash$"} {
		cfgs = append(cfgs, config.HookConfig{Command: m, Matcher: m})
	}
	r := NewRunner(cfgs, t.TempDir(), t.TempDir())
	fired := func(ctx context.Context, tool string) []string {
		var commands []string
		for _, h := range r.toolHooks(ctx, tool) {
			commands = append(commands, h.Command)
		}
		return commands
	}

	ctx := t.Context()
	require.ElementsMatch(t, []string{"Bash", "*", "^bash$"}, fired(ctx, "bash"))
	require.ElementsMatch(t, []string{"Edit|Write", "edit", "*"}, fired(ctx, "edit"))
	require.ElementsMatch(t, []string{"Edit|Write", "*"}, fired(ctx, "write"))
	require.ElementsMatch(t, []string{"Edit|Write", "*"}, fired(ctx, "multiedit"), "Claude Code has only Edit now")
	require.ElementsMatch(t, []string{"Edit|Write", "*"}, fired(ctx, "lsp_rename"))
	require.ElementsMatch(t, []string{"Edit|Write", "*"}, fired(ctx, "lsp_replace_symbol"))
	require.ElementsMatch(t, []string{"Edit|Write", "WebFetch", "*"}, fired(ctx, "download"))
	require.ElementsMatch(t, []string{"WebFetch", "*"}, fired(ctx, "x402_fetch"))
	require.ElementsMatch(t, []string{"WebSearch", "*"}, fired(ctx, "sourcegraph"))
	require.ElementsMatch(t, []string{"LSP", "*"}, fired(ctx, "lsp_references"))
	require.ElementsMatch(t, []string{"TaskStop", "*"}, fired(ctx, "job_kill"))
	require.ElementsMatch(t, []string{"AskUserQuestion", "*"}, fired(ctx, "question"))
	require.ElementsMatch(t, []string{"*"}, fired(ctx, "gnokey"), "Claude Code has no tool like it")
	require.ElementsMatch(t, []string{"Read", "*"}, fired(ctx, "view"))
	require.ElementsMatch(t, []string{"Task", "*"}, fired(ctx, "agent"))
	require.ElementsMatch(t, []string{"WebFetch", "*"}, fired(ctx, "fetch"))
	require.ElementsMatch(t, []string{"WebFetch", "WebSearch", "*"}, fired(ctx, "agentic_fetch"))
	require.ElementsMatch(t, []string{"WebFetch", "*"}, fired(ctx, "web_fetch"), "agentic_fetch's sub-agent fetches too")
	require.ElementsMatch(t, []string{"WebSearch", "*"}, fired(ctx, "web_search"))
	require.ElementsMatch(t, []string{"*"}, fired(ctx, "mcp_srv_tool"), "an MCP tool's own name is not Claude Code's")
	require.ElementsMatch(t, []string{"mcp__srv__.*", "*"}, fired(WithToolAliases(ctx, "mcp__srv__tool"), "mcp_srv_tool"))
}

// A hook that matches a tool only by the name of a Claude Code tool whose
// job it does with input of another shape guards it but cannot approve
// it: a hook that approves Write calls checked a Write's input, not a
// download's, which also fetches. Its block stands, and a hook that names
// the tool by a name of its own keeps its allow.
func TestAGuardOnlyHookCannotApprove(t *testing.T) {
	t.Parallel()

	const allow = `echo '{"hookSpecificOutput":{"hookEventName":"PreToolUse","permissionDecision":"allow"}}'`
	run := func(t *testing.T, tool string, cfgs ...config.HookConfig) AggregateResult {
		t.Helper()
		r := NewRunner(cfgs, t.TempDir(), t.TempDir())
		result, err := r.Run(t.Context(), EventPreToolUse, "s", tool, `{"url":"https://example.com/a.tgz","file_path":"a.tgz"}`)
		require.NoError(t, err)
		return result
	}

	require.Equal(t, DecisionAllow, run(t, "write", config.HookConfig{Command: allow, Matcher: "Write"}).Decision)
	approved := run(t, "download", config.HookConfig{Command: allow, Matcher: "Write"})
	require.Equal(t, DecisionNone, approved.Decision, "an approval of Write calls is none of a download")
	require.Equal(t, "none", approved.Hooks[0].Decision)
	require.Equal(t, DecisionNone, run(t, "multiedit", config.HookConfig{Command: allow, Matcher: "Edit|Write"}).Decision)

	blocked := run(t, "download", config.HookConfig{Command: `echo "no writes" >&2; exit 2`, Matcher: "Edit|Write"})
	require.Equal(t, DecisionDeny, blocked.Decision, "a guard written for Write stops a download")
	require.Equal(t, "no writes", blocked.Reason)

	require.Equal(t, DecisionAllow, run(t, "download", config.HookConfig{Command: allow, Matcher: "download"}).Decision)
	require.Equal(t, DecisionAllow, run(t, "multiedit", config.HookConfig{Command: allow, Matcher: "MultiEdit"}).Decision, "MultiEdit is its own Claude Code name")
	require.Equal(t, DecisionAllow, run(t, "download",
		config.HookConfig{Command: allow, Matcher: "Write"},
		config.HookConfig{Command: allow, Matcher: "download"},
	).Decision, "the command runs once, and it named the tool too")
}
