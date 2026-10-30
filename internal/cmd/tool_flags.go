package cmd

import (
	"fmt"
	"strings"
	"unicode"

	"github.com/charmbracelet/crush/internal/config"
	"github.com/charmbracelet/crush/internal/tooldefs"
	"github.com/spf13/cobra"
)

// addToolFlags adds --tools, --allowed-tools and --disallowed-tools, with
// Claude Code's spellings of the last two, --allowedTools and
// --disallowedTools; read them with toolFlags.
func addToolFlags(cmd *cobra.Command) {
	cmd.Flags().String("tools", "", `The only built-in tools the agent is given, comma- or space-separated; "" for none, "default" for all; Claude Code's names (Read, Bash) work too`)
	cmd.Flags().StringArray("allowed-tools", nil, `Tools that run without asking permission, comma- or space-separated; Claude Code's names (Read, Bash) work too, and so do its allow rules for commands, such as "Bash(git diff *)"`)
	cmd.Flags().StringArray("allowedTools", nil, "Same as --allowed-tools, by Claude Code's name for it")
	cmd.Flags().StringArray("disallowed-tools", nil, `Tools the agent is not given, comma- or space-separated; Claude Code's names (Edit, Bash) work too, and so do its deny rules for commands, such as "Bash(git push *)"`)
	cmd.Flags().StringArray("disallowedTools", nil, "Same as --disallowed-tools, by Claude Code's name for it")
}

// toolFlags returns the tools cmd's tool flags name, in Gnosh's names, and
// the patterns of the Bash(pattern) rules: the allow rules
// --allowed-tools gives and the deny rules --disallowed-tools gives. A
// name that is not a tool, or a rule gnosh cannot apply, is an error, so
// a typo cannot leave a tool or a command on that was meant to be off. A
// command without the flags names none.
func toolFlags(cmd *cobra.Command) (allowed, allowRules, disallowed, denyRules []string, err error) {
	if allowed, allowRules, err = toolFlagNames(cmd, "allowed-tools", "allowedTools", true, true); err != nil {
		return nil, nil, nil, nil, err
	}
	if disallowed, denyRules, err = toolFlagNames(cmd, "disallowed-tools", "disallowedTools", false, true); err != nil {
		return nil, nil, nil, nil, err
	}
	others, err := toolsFlagOthers(cmd)
	if err != nil {
		return nil, nil, nil, nil, err
	}
	return allowed, allowRules, append(disallowed, others...), denyRules, nil
}

// toolsFlagOthers returns the built-in tools that --tools leaves out, as
// Claude Code's --tools does: every tool but the ones it names, all of
// them for "", none for "default" or when the flag is not given. Naming
// what to keep, rather than everything to turn off, means a tool added
// later stays off too.
func toolsFlagOthers(cmd *cobra.Command) ([]string, error) {
	flag := cmd.Flags().Lookup("tools")
	if flag == nil || !flag.Changed {
		return nil, nil
	}
	value := strings.TrimSpace(flag.Value.String())
	if value == "default" {
		return nil, nil
	}
	keep := map[string]bool{}
	for _, word := range strings.FieldsFunc(value, func(r rune) bool { return r == ',' || unicode.IsSpace(r) }) {
		tool, err := toolFlagName(word, false)
		if err != nil {
			return nil, fmt.Errorf("%w (in --tools)", err)
		}
		keep[tool] = true
	}
	var others []string
	for _, tool := range config.KnownToolNames() {
		if !keep[tool] {
			others = append(others, tool)
		}
	}
	return others, nil
}

// toolFlagNames returns the tools flag and claudeFlag name and, where
// rulesOK, the patterns of the Bash(pattern) rules they give.
func toolFlagNames(cmd *cobra.Command, flag, claudeFlag string, mcpOK, rulesOK bool) (tools, rules []string, err error) {
	var values []string
	for _, name := range []string{flag, claudeFlag} {
		v, err := cmd.Flags().GetStringArray(name)
		if err != nil {
			// The command does not take the flag.
			continue
		}
		values = append(values, v...)
	}
	for _, value := range values {
		// Claude Code takes the tools as separate words of one argument.
		for _, word := range ruleWords(value) {
			if pattern, ok := config.BashRulePattern(word); ok && rulesOK {
				if strings.TrimSpace(strings.TrimSuffix(pattern, ":*")) == "" {
					return nil, nil, fmt.Errorf("%q names no command (in --%s)", word, flag)
				}
				rules = append(rules, pattern)
				continue
			}
			tool, err := toolFlagName(word, mcpOK)
			if err != nil {
				return nil, nil, fmt.Errorf("%w (in --%s)", err, flag)
			}
			tools = append(tools, tool)
		}
	}
	return tools, rules, nil
}

// ruleWords splits value into its tools and rules: at commas and spaces,
// but not inside a rule's parentheses, so "Bash(git push *) Edit" is two.
func ruleWords(value string) []string {
	var words []string
	var word strings.Builder
	depth := 0
	for _, r := range value {
		switch {
		case r == '(':
			depth++
		case r == ')' && depth > 0:
			depth--
		case depth == 0 && (r == ',' || unicode.IsSpace(r)):
			if word.Len() > 0 {
				words = append(words, word.String())
				word.Reset()
			}
			continue
		}
		word.WriteRune(r)
	}
	if word.Len() > 0 {
		words = append(words, word.String())
	}
	return words
}

// toolFlagName is the built-in tool word names: Gnosh's name for it in
// any case, or Claude Code's, or, where mcpOK, an MCP tool's name.
func toolFlagName(word string, mcpOK bool) (string, error) {
	if strings.ContainsAny(word, "()") {
		return "", fmt.Errorf("gnosh takes whole tools here, not a rule like %q; its Bash(...) rules are the ones it applies", word)
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
