package cmd

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"
)

// addAddDirFlag adds Claude Code's --add-dir; read it with addDirFlag.
func addAddDirFlag(cmd *cobra.Command) {
	cmd.Flags().StringSlice("add-dir", nil, "More directories the agent may read without asking, as the working directory (repeatable)")
}

// addDirFlag returns the --add-dir directories as absolute paths, each
// checked to be a directory, as Claude Code checks them.
func addDirFlag(cmd *cobra.Command) ([]string, error) {
	dirs, err := cmd.Flags().GetStringSlice("add-dir")
	if err != nil {
		// The command does not take the flag.
		return nil, nil
	}
	var abs []string
	for _, dir := range dirs {
		path, err := filepath.Abs(dir)
		if err != nil {
			return nil, fmt.Errorf("--add-dir %s: %w", dir, err)
		}
		info, err := os.Stat(path)
		if err != nil {
			return nil, fmt.Errorf("--add-dir %s: no such directory", dir)
		}
		if !info.IsDir() {
			return nil, fmt.Errorf("--add-dir %s: not a directory", dir)
		}
		abs = append(abs, path)
	}
	return abs, nil
}
