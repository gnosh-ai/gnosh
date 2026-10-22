package cmd

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"charm.land/fantasy"
	"github.com/spf13/cobra"

	"github.com/charmbracelet/crush/internal/agent"
	"github.com/charmbracelet/crush/internal/version"
	"github.com/charmbracelet/crush/internal/workspace"
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
		_, err := fmt.Fprintf(cmd.OutOrStdout(), "Wrote %s (%d bytes). Edit it; "+version.Name+" uses it from the next start.\n", path, len(builtin))
		return err
	},
}

var promptShowCmd = &cobra.Command{
	Use:   "show [name]",
	Short: "Print the template " + version.Name + " will use (yours if present, else the built-in)",
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

var promptToolsCmd = &cobra.Command{
	Use:   "tools",
	Short: "Print what the model is told about each tool (the rest of every request)",
	Long: `Every request carries, besides the prompt template, a description of each
tool the agent may call: its name, what it does and its parameters. This prints
them as the coder agent builds them in the current directory; --json prints the
exact objects.`,
	RunE: func(cmd *cobra.Command, args []string) error {
		ws, cleanup, err := setupLocalWorkspace(cmd)
		if err != nil {
			return err
		}
		defer cleanup()
		appWs, ok := ws.(*workspace.AppWorkspace)
		if !ok {
			return fmt.Errorf("unexpected workspace type %T", ws)
		}
		infos, err := appWs.App().AgentCoordinator.ToolInfos(cmd.Context())
		if err != nil {
			return err
		}
		sort.Slice(infos, func(i, j int) bool { return infos[i].Name < infos[j].Name })
		out := cmd.OutOrStdout()
		if export, _ := cmd.Flags().GetBool("export"); export {
			return exportToolDescriptions(cmd, infos)
		}
		if asJSON, _ := cmd.Flags().GetBool("json"); asJSON {
			enc := json.NewEncoder(out)
			enc.SetIndent("", "  ")
			return enc.Encode(infos)
		}
		total := 0
		for _, t := range infos {
			b, _ := json.Marshal(t)
			total += len(b)
			source := ""
			if path, used := agent.UserToolDescription(t.Name); used {
				source = "  (description from " + path + ")"
			}
			fmt.Fprintf(out, "## %s  (%d bytes)%s\n%s\n", t.Name, len(b), source, strings.TrimSpace(t.Description))
			names := make([]string, 0, len(t.Parameters))
			for name := range t.Parameters {
				names = append(names, name)
			}
			sort.Strings(names)
			for _, name := range names {
				desc := ""
				if m, ok := t.Parameters[name].(map[string]any); ok {
					desc, _ = m["description"].(string)
				}
				req := ""
				for _, r := range t.Required {
					if r == name {
						req = ", required"
					}
				}
				fmt.Fprintf(out, "  - %s%s: %s\n", name, req, strings.TrimSpace(desc))
			}
			fmt.Fprintln(out)
		}
		fmt.Fprintf(out, "%d tools, %d bytes of JSON (roughly %d tokens)\n", len(infos), total, total/4)
		fmt.Fprintf(out, "Descriptions can be edited as %s/<name>.md and tools left out via %s (crush prompt tools --export writes them).\n", agent.UserToolsDir(), agent.DisabledToolsFile())
		return nil
	},
}

// exportToolDescriptions writes each tool's current description to the user
// tools directory (existing files are kept) and a commented disabled.txt.
func exportToolDescriptions(cmd *cobra.Command, infos []fantasy.ToolInfo) error {
	dir := agent.UserToolsDir()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	written := 0
	for _, t := range infos {
		path := filepath.Join(dir, t.Name+".md")
		if _, err := os.Stat(path); err == nil {
			continue
		}
		if err := os.WriteFile(path, []byte(agent.ExportedDescriptionFile(t.Description)), 0o644); err != nil {
			return err
		}
		written++
	}
	disabled := agent.DisabledToolsFile()
	if _, err := os.Stat(disabled); err != nil {
		header := "# One tool name per line; these are not offered to the model. Names: crush prompt tools\n"
		if err := os.WriteFile(disabled, []byte(header), 0o644); err != nil {
			return err
		}
	}
	_, err := fmt.Fprintf(cmd.OutOrStdout(), "Wrote %d description files to %s (existing ones kept); edit them, or list tools to drop in %s. A file is used from the next start once you edit it; until then the current built-in description is.\n", written, dir, disabled)
	return err
}

func init() {
	promptExportCmd.Flags().Bool("force", false, "Overwrite an existing file")
	promptToolsCmd.Flags().Bool("json", false, "Print the exact JSON objects")
	promptToolsCmd.Flags().Bool("export", false, "Write each description to ~/.config/crush/tools/<name>.md for editing")
	promptCmd.AddCommand(promptPathCmd, promptExportCmd, promptShowCmd, promptToolsCmd)
}
