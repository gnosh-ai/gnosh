package cmd

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"

	"github.com/charmbracelet/crush/internal/agent"
)

var promptCmd = &cobra.Command{
	Use:   "prompt",
	Short: "The system prompt templates, as files you can edit",
	Long: `Crush builds its system prompt from a template. Export the built-in one to
~/.config/crush/templates/<name>, edit it, and Crush uses your file from then on.
Names: coder.md.tpl (the default agent), plan.md.tpl, task.md.tpl, initialize.md.tpl.`,
}

var promptPathCmd = &cobra.Command{
	Use:   "path",
	Short: "Print the directory of user prompt templates",
	RunE: func(cmd *cobra.Command, args []string) error {
		_, err := fmt.Fprintln(cmd.OutOrStdout(), agent.UserTemplateDir())
		return err
	},
}

var promptExportCmd = &cobra.Command{
	Use:   "export [name]",
	Short: "Write the built-in template to the user directory (default coder.md.tpl)",
	Args:  cobra.MaximumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		name := "coder.md.tpl"
		if len(args) == 1 {
			name = args[0]
		}
		builtin, ok := agent.BuiltinTemplate(name)
		if !ok {
			return fmt.Errorf("no built-in template named %q", name)
		}
		path := filepath.Join(agent.UserTemplateDir(), name)
		force, _ := cmd.Flags().GetBool("force")
		if _, err := os.Stat(path); err == nil && !force {
			return fmt.Errorf("%s exists; edit it, or pass --force to overwrite it with the built-in", path)
		}
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			return err
		}
		if err := os.WriteFile(path, builtin, 0o644); err != nil {
			return err
		}
		_, err := fmt.Fprintf(cmd.OutOrStdout(), "Wrote %s (%d bytes). Edit it; Crush uses it from the next start.\n", path, len(builtin))
		return err
	},
}

var promptShowCmd = &cobra.Command{
	Use:   "show [name]",
	Short: "Print the template Crush will use (yours if present, else the built-in)",
	Args:  cobra.MaximumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		name := "coder.md.tpl"
		if len(args) == 1 {
			name = args[0]
		}
		if _, ok := agent.BuiltinTemplate(name); !ok {
			return fmt.Errorf("no template named %q", name)
		}
		text, path, user := agent.EffectiveTemplate(name)
		source := "built-in"
		if user {
			source = path
		}
		fmt.Fprintf(cmd.ErrOrStderr(), "# %s (%s)\n", name, source)
		_, err := fmt.Fprint(cmd.OutOrStdout(), text)
		return err
	},
}

func init() {
	promptExportCmd.Flags().Bool("force", false, "Overwrite an existing file")
	promptCmd.AddCommand(promptPathCmd, promptExportCmd, promptShowCmd)
}
