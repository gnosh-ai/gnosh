package config

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// Matchers are read as Claude Code reads them: "*" or empty for all,
// exact names separated by | or ,, and else an unanchored regular
// expression. A plain name was a regular expression before, so "edit"
// matched multiedit and "*" did not compile.
func TestCompileHookMatcher(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		matcher     string
		match, miss []string
	}{
		{"", []string{"bash", "anything"}, nil},
		{"*", []string{"bash", "anything"}, nil},
		{" * ", []string{"bash"}, nil},
		{"Bash", []string{"Bash"}, []string{"bash", "BashOutput", "MultiBash"}},
		{"Edit|Write", []string{"Edit", "Write"}, []string{"MultiEdit", "edit"}},
		{"Edit, Write", []string{"Edit", "Write"}, []string{"MultiEdit"}},
		{"edit", []string{"edit"}, []string{"multiedit"}},
		{"mcp_github", nil, []string{"mcp_github_create_issue"}},
		{"^Edit$", []string{"Edit"}, []string{"MultiEdit"}},
		{"Edit.*", []string{"Edit", "MultiEdit", "EditNotebook"}, []string{"Write"}},
		{"mcp__github__.*", []string{"mcp__github__create_issue"}, []string{"mcp_github_create_issue"}},
		{"^mcp_github_", []string{"mcp_github_create_issue"}, []string{"mcp__github__create_issue"}},
		{"startup|resume", []string{"startup", "resume"}, []string{"clear"}},
	} {
		m, err := CompileHookMatcher(tc.matcher)
		require.NoError(t, err, tc.matcher)
		for _, name := range tc.match {
			require.Truef(t, m.Matches(name), "%q should match %q", tc.matcher, name)
		}
		for _, name := range tc.miss {
			require.Falsef(t, m.Matches(name), "%q should not match %q", tc.matcher, name)
		}
	}

	_, err := CompileHookMatcher("[invalid")
	require.Error(t, err)
	m, err := CompileHookMatcher("Bash")
	require.NoError(t, err)
	require.True(t, m.Matches("bash", "Bash"), "a tool matches by any of its names")
}

// "*" loads and matches everything, as in Claude Code; it was an invalid
// regular expression, which kept Klash from starting. On an event that
// takes no matcher it counts as none.
func TestValidateHooksAcceptsAStar(t *testing.T) {
	t.Parallel()

	cfg := &Config{Hooks: map[string][]HookConfig{
		EventPreToolUse: {{Command: "true", Matcher: "*"}},
		EventStop:       {{Command: "true", Matcher: "*"}},
	}}
	require.NoError(t, cfg.ValidateHooks())
}

// A Notification hook loads with or without a matcher, as in Claude Code:
// its matcher is tested against the notification type. Before this event
// was known, a config with one stopped Klash at startup with "unknown
// event name".
func TestValidateHooksAcceptsNotification(t *testing.T) {
	t.Parallel()

	for _, matcher := range []string{"", "*", "permission_prompt", "idle_prompt", "permission_prompt|idle_prompt"} {
		cfg := &Config{Hooks: map[string][]HookConfig{
			EventNotification: {{Command: "true", Matcher: matcher}},
		}}
		require.NoError(t, cfg.ValidateHooks(), "matcher %q", matcher)
	}
	require.True(t, IsKnownHookEvent(EventNotification))
	require.True(t, HookEventSupportsMatcher(EventNotification))
}

// An exact matcher name can be a built-in tool's, under any of its names,
// or an MCP tool's whole name; a server's name alone is no tool's.
func TestNamesSomeTool(t *testing.T) {
	t.Parallel()

	for _, name := range []string{"bash", "Bash", "Read", "crush_logs", "klash_logs", "web_fetch", "web_search", "WebSearch", "LSP", "TaskStop", "mcp_github_create_issue", "mcp__github__create_issue"} {
		require.Truef(t, namesSomeTool(name), name)
	}
	for _, name := range []string{"Notebook", "NotebookEdit", "bassh", "mcp_github", "mcp__github", "mcp_", "mcp__github__"} {
		require.Falsef(t, namesSomeTool(name), name)
	}
}
