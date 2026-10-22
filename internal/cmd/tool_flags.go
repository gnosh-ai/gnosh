package cmd

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/crush/internal/config"
	"github.com/charmbracelet/crush/internal/tooldefs"
	"github.com/spf13/cobra"
)

// addToolFlags adds --allowed-tools and --disallowed-tools, with Claude
// Code's spellings of them, --allowedTools and --disallowedTools; read
// them with toolFlags.
func addToolFlags(cmd *cobra.Command) {
	cmd.Flags().StringSlice("allowed-tools", nil, "Tools that run without asking permission, comma- or space-separated; Claude Code's names (Read, Bash) work too")
	cmd.Flags().StringSlice("allowedTools", nil, "Same as --allowed-tools, by Claude Code's name for it")
	cmd.Flags().StringSlice("disallowed-tools", nil, "Tools the agent is not given, comma- or space-separated; Claude Code's names (Edit, Bash) work too")
	cmd.Flags().StringSlice("disallowedTools", nil, "Same as --disallowed-tools, by Claude Code's name for it")
}

// toolFlags returns the tools cmd's tool flags name, in Crash's names. A
// name that is not a tool is an error, so a typo cannot leave a tool on
// that was meant to be off. A command without the flags names none.
func toolFlags(cmd *cobra.Command) (allowed, disallowed []string, err error) {
	if allowed, err = toolFlagNames(cmd, "allowed-tools", "allowedTools", true); err != nil {
		return nil, nil, err
	}
	if disallowed, err = toolFlagNames(cmd, "disallowed-tools", "disallowedTools", false); err != nil {
		return nil, nil, err
	}
	return allowed, disallowed, nil
}

func toolFlagNames(cmd *cobra.Command, flag, claudeFlag string, mcpOK bool) ([]string, error) {
	var values []string
	for _, name := range []string{flag, claudeFlag} {
		v, err := cmd.Flags().GetStringSlice(name)
		if err != nil {
			// The command does not take the flag.
			continue
		}
		values = append(values, v...)
	}
	var tools []string
	for _, value := range values {
		// Claude Code takes the tools as separate words of one argument,
		// so split on spaces as well as the commas StringSlice splits on.
		for _, word := range strings.Fields(value) {
			tool, err := toolFlagName(word, mcpOK)
			if err != nil {
				return nil, fmt.Errorf("%w (in --%s)", err, flag)
			}
			tools = append(tools, tool)
		}
	}
	return tools, nil
}

// toolFlagName is the built-in tool word names: Crash's name for it in
// any case, or Claude Code's, or, where mcpOK, an MCP tool's name.
func toolFlagName(word string, mcpOK bool) (string, error) {
	if strings.ContainsAny(word, "()") {
		return "", fmt.Errorf("crash takes whole tools only, not a command pattern like %q", word)
	}
	if tool := tooldefs.FromClaudeCode(word); tool != "" {
		return tool, nil
	}
	if mcpOK && tooldefs.IsMCPToolName(word) {
		return word, nil
	}
	tool := tooldefs.Canonical(strings.ToLower(word))
	if !config.IsKnownToolName(tool) {
		return "", fmt.Errorf("unknown tool %q; known tools are %s", word, strings.Join(config.KnownToolNames(), ", "))
	}
	return tool, nil
}
