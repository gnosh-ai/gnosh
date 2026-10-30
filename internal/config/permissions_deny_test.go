package config

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// permissions.deny takes Claude Code's deny rules of the two kinds gnosh
// applies: a tool's name takes the tool away, as options.disabled_tools
// does, and Bash(pattern) stops the commands the pattern matches. Any
// other rule is refused at load, so a rule meant to protect something is
// never silently ignored.
func TestPermissionsDeny(t *testing.T) {
	t.Parallel()

	cfg := &Config{Permissions: &Permissions{Deny: []string{"Bash(git push *)", "WebFetch", "bash(rm -rf *)", "sourcegraph"}}}
	require.NoError(t, cfg.ValidateTools())
	require.Equal(t, []string{"git push *", "rm -rf *"}, cfg.Permissions.BashDenyPatterns())
	require.Equal(t, []string{"fetch", "sourcegraph"}, cfg.Options.DisabledTools)
	require.NoError(t, cfg.ValidateTools(), "a reload validates again")
	require.Equal(t, []string{"fetch", "sourcegraph"}, cfg.Options.DisabledTools, "a tool is taken away once")

	for rule, want := range map[string]string{
		"Edit(src/**)":        "neither a built-in tool nor a Bash(...) rule",
		"mcp__github__search": "neither a built-in tool nor a Bash(...) rule",
		"Bsh":                 "neither a built-in tool nor a Bash(...) rule",
		"Bash()":              "names no command",
		"Bash(:*)":            "names no command",
	} {
		c := &Config{Permissions: &Permissions{Deny: []string{rule}}}
		require.ErrorContains(t, c.ValidateTools(), want, rule)
	}

	var none *Permissions
	require.Empty(t, none.BashDenyPatterns())
}
