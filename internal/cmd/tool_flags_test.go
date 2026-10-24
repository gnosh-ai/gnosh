package cmd

import (
	"testing"

	"github.com/charmbracelet/crush/internal/config"
	"github.com/spf13/cobra"
	"github.com/stretchr/testify/require"
)

// Claude Code's --allowedTools and --disallowedTools work as Claude Code
// users write them: Claude Code's tool names, several tools in one
// argument, and either spelling of the flag.
func TestToolFlags(t *testing.T) {
	t.Parallel()

	parse := func(args ...string) ([]string, []string, error) {
		cmd := &cobra.Command{Use: "klash", RunE: func(*cobra.Command, []string) error { return nil }}
		cmd.SilenceErrors, cmd.SilenceUsage = true, true
		addToolFlags(cmd)
		cmd.SetArgs(args)
		if err := cmd.Execute(); err != nil {
			return nil, nil, err
		}
		allowed, disallowed, rules, err := toolFlags(cmd)
		require.Empty(t, rules)
		return allowed, disallowed, err
	}

	allowed, disallowed, err := parse()
	require.NoError(t, err)
	require.Empty(t, allowed)
	require.Empty(t, disallowed)

	allowed, disallowed, err = parse("--disallowedTools", "Bash Edit", "--disallowed-tools", "write,multiedit", "--allowedTools", "Read,Grep", "--allowed-tools", "mcp_github_search")
	require.NoError(t, err)
	require.Equal(t, []string{"write", "multiedit", "bash", "edit"}, disallowed)
	require.Equal(t, []string{"mcp_github_search", "view", "grep"}, allowed)

	_, disallowed, err = parse("--disallowed-tools", "BASH,crush_logs")
	require.NoError(t, err)
	require.Equal(t, []string{"bash", "klash_logs"}, disallowed, "any case, and the names from before the rename")

	_, _, err = parse("--disallowedTools", "Bsh")
	require.ErrorContains(t, err, `unknown tool "Bsh"`)
	_, _, err = parse("--allowedTools", "Bash(git log:*)")
	require.ErrorContains(t, err, "deny rule goes in --disallowed-tools")
	_, _, err = parse("--disallowedTools", "mcp_github_search")
	require.Error(t, err, "an MCP tool is turned off in its server's config, not here")
}

// A command that does not take the flags names no tools.
func TestToolFlagsWithoutTheFlags(t *testing.T) {
	t.Parallel()

	allowed, disallowed, rules, err := toolFlags(&cobra.Command{Use: "klash"})
	require.NoError(t, err)
	require.Empty(t, allowed)
	require.Empty(t, disallowed)
	require.Empty(t, rules)
}

// --tools names the only built-in tools the agent gets, as Claude Code's
// does: every other one is left out, "" leaves out all of them, and
// "default" (or no flag) leaves the set as it is.
func TestToolsFlag(t *testing.T) {
	t.Parallel()

	parse := func(args ...string) ([]string, error) {
		cmd := &cobra.Command{Use: "klash", RunE: func(*cobra.Command, []string) error { return nil }}
		cmd.SilenceErrors, cmd.SilenceUsage = true, true
		addToolFlags(cmd)
		cmd.SetArgs(args)
		if err := cmd.Execute(); err != nil {
			return nil, err
		}
		_, disallowed, _, err := toolFlags(cmd)
		return disallowed, err
	}

	all := config.KnownToolNames()

	off, err := parse("--tools", "Read Grep,glob ls")
	require.NoError(t, err)
	require.Len(t, off, len(all)-4)
	for _, kept := range []string{"view", "grep", "glob", "ls"} {
		require.NotContains(t, off, kept)
	}
	require.Contains(t, off, "bash")
	require.Contains(t, off, "edit")

	off, err = parse("--tools", "")
	require.NoError(t, err)
	require.ElementsMatch(t, all, off, `"" leaves out every built-in tool`)

	off, err = parse("--tools", "default")
	require.NoError(t, err)
	require.Empty(t, off)

	off, err = parse()
	require.NoError(t, err)
	require.Empty(t, off)

	off, err = parse("--tools", "view,bash", "--disallowedTools", "bash")
	require.NoError(t, err)
	require.Contains(t, off, "bash", "--disallowed-tools still takes away a tool --tools keeps")
	require.NotContains(t, off, "view")

	_, err = parse("--tools", "Bsh")
	require.ErrorContains(t, err, `unknown tool "Bsh"`)
	require.ErrorContains(t, err, "(in --tools)")
}

// --disallowed-tools takes Claude Code's Bash deny rules beside the tools,
// a rule's spaces and commas kept inside its parentheses; any other rule,
// or one in --allowed-tools, is an error.
func TestToolFlagsDenyRules(t *testing.T) {
	t.Parallel()

	parse := func(args ...string) ([]string, []string, error) {
		cmd := &cobra.Command{Use: "klash", RunE: func(*cobra.Command, []string) error { return nil }}
		cmd.SilenceErrors, cmd.SilenceUsage = true, true
		addToolFlags(cmd)
		cmd.SetArgs(args)
		if err := cmd.Execute(); err != nil {
			return nil, nil, err
		}
		_, disallowed, rules, err := toolFlags(cmd)
		return disallowed, rules, err
	}

	disallowed, rules, err := parse("--disallowed-tools", "Bash(rm -rf *),write", "--disallowedTools", "Bash(git push *) Edit Bash(echo a,b)")
	require.NoError(t, err)
	require.Equal(t, []string{"write", "edit"}, disallowed)
	require.Equal(t, []string{"rm -rf *", "git push *", "echo a,b"}, rules)

	_, _, err = parse("--disallowedTools", "Bash()")
	require.ErrorContains(t, err, "names no command")
	_, _, err = parse("--disallowedTools", "Edit(src/**)")
	require.ErrorContains(t, err, `not a rule like "Edit(src/**)"`)
}
