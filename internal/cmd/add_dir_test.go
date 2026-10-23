package cmd

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/spf13/cobra"
	"github.com/stretchr/testify/require"
)

// --add-dir takes existing directories, made absolute; anything else is
// an error naming it, as in Claude Code.
func TestAddDirFlag(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "notes.txt")
	require.NoError(t, os.WriteFile(file, nil, 0o644))
	t.Chdir(dir)

	parse := func(args ...string) ([]string, error) {
		cmd := &cobra.Command{Use: "test"}
		addAddDirFlag(cmd)
		require.NoError(t, cmd.ParseFlags(args))
		return addDirFlag(cmd)
	}

	got, err := parse("--add-dir", ".", "--add-dir", dir)
	require.NoError(t, err)
	abs, _ := filepath.Abs(".")
	require.Equal(t, []string{abs, dir}, got)

	_, err = parse("--add-dir", filepath.Join(dir, "missing"))
	require.ErrorContains(t, err, "no such directory")
	_, err = parse("--add-dir", file)
	require.ErrorContains(t, err, "not a directory")

	got, err = parse()
	require.NoError(t, err)
	require.Empty(t, got)

	for _, c := range []*cobra.Command{rootCmd, runCmd} {
		require.NotNil(t, c.Flags().Lookup("add-dir"), c.Name())
	}
}
