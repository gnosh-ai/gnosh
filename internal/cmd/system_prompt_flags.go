package cmd

import (
	"errors"
	"fmt"
	"io/fs"
	"os"

	"github.com/spf13/cobra"
)

// addSystemPromptFlags adds Claude Code's --system-prompt and
// --append-system-prompt, each with a -file form; read them with
// systemPromptFlags.
func addSystemPromptFlags(cmd *cobra.Command) {
	cmd.Flags().String("system-prompt", "", "Instructions to use in place of Crash's own; the environment, skills and context files (CRASH.md, AGENTS.md) are kept")
	cmd.Flags().String("system-prompt-file", "", "Read the --system-prompt instructions from a file")
	cmd.Flags().String("append-system-prompt", "", "Text to add at the end of the system prompt")
	cmd.Flags().String("append-system-prompt-file", "", "Read text to add at the end of the system prompt from a file")
	cmd.MarkFlagsMutuallyExclusive("system-prompt", "system-prompt-file")
}

// systemPromptFlags returns the instructions that replace Crash's own and
// the text to add at the end of the system prompt, from cmd's system
// prompt flags. Both append flags may be given; the file's text comes
// second. A command without the flags names neither.
func systemPromptFlags(cmd *cobra.Command) (replace, appended string, err error) {
	replace = flagValue(cmd, "system-prompt")
	if replace == "" {
		if replace, err = readFlagFile(cmd, "system-prompt-file", "system prompt file"); err != nil {
			return "", "", err
		}
	}
	appended = flagValue(cmd, "append-system-prompt")
	file, err := readFlagFile(cmd, "append-system-prompt-file", "append system prompt file")
	if err != nil {
		return "", "", err
	}
	if appended != "" && file != "" {
		appended += "\n\n"
	}
	return replace, appended + file, nil
}

// readFlagFile returns the content of the file the named flag names, or
// "" when the flag is not given.
func readFlagFile(cmd *cobra.Command, name, what string) (string, error) {
	path := flagValue(cmd, name)
	if path == "" {
		return "", nil
	}
	b, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return "", fmt.Errorf("%s not found: %s", what, path)
	}
	if err != nil {
		return "", fmt.Errorf("reading %s: %w", what, err)
	}
	return string(b), nil
}

// flagValue returns the named flag's value, or "" for a command without
// the flag.
func flagValue(cmd *cobra.Command, name string) string {
	flag := cmd.Flags().Lookup(name)
	if flag == nil {
		return ""
	}
	return flag.Value.String()
}
