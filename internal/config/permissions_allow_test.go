package config

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// permissions.allow takes Claude Code's allow rules of the two kinds gnosh
// applies: a tool's name lets the tool run without asking, as
// permissions.allowed_tools does, and Bash(pattern) lets the commands the
// pattern matches run without asking; Bash(*) is Bash. Any other rule is
// refused at load, so a rule is never silently ignored.
func TestPermissionsAllow(t *testing.T) {
	t.Parallel()

	cfg := &Config{Permissions: &Permissions{Allow: []string{"Bash(git diff *)", "Read", "bash(npm run:*)", "glob", "mcp_github_search"}}}
	require.NoError(t, cfg.ValidateTools())
	require.Equal(t, []string{"git diff *", "npm run:*"}, cfg.Permissions.BashAllowPatterns())
	require.Equal(t, []string{"view", "glob", "mcp_github_search"}, cfg.Permissions.AllowedTools)
	require.NoError(t, cfg.ValidateTools(), "a reload validates again")
	require.Equal(t, []string{"view", "glob", "mcp_github_search"}, cfg.Permissions.AllowedTools, "a tool is allowed once")

	all := &Config{Permissions: &Permissions{Allow: []string{"Bash(*)"}}}
	require.NoError(t, all.ValidateTools())
	require.Equal(t, []string{"bash"}, all.Permissions.AllowedTools, "Bash(*) is Bash")
	require.Empty(t, all.Permissions.BashAllowPatterns())

	for rule, want := range map[string]string{
		"Edit(src/**)":                 "neither a tool's name nor a Bash(...) rule",
		"WebFetch(domain:example.com)": "neither a tool's name nor a Bash(...) rule",
		"Bsh":                          "neither a tool's name nor a Bash(...) rule",
		"Bash()":                       "names no command",
		"Bash(:*)":                     "names no command",
	} {
		c := &Config{Permissions: &Permissions{Allow: []string{rule}}}
		require.ErrorContains(t, c.ValidateTools(), want, rule)
	}

	var none *Permissions
	require.Empty(t, none.BashAllowPatterns())
}
