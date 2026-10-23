package cmd

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/spf13/cobra"
	"github.com/stretchr/testify/require"
)

func systemPromptCmd(t *testing.T, args ...string) *cobra.Command {
	t.Helper()
	cmd := &cobra.Command{Use: "test", RunE: func(*cobra.Command, []string) error { return nil }}
	addSystemPromptFlags(cmd)
	cmd.SetArgs(args)
	require.NoError(t, cmd.ParseFlags(args))
	return cmd
}

func TestSystemPromptFlags(t *testing.T) {
	dir := t.TempDir()
	instructions := filepath.Join(dir, "instructions.md")
	require.NoError(t, os.WriteFile(instructions, []byte("You review code."), 0o644))
	extra := filepath.Join(dir, "extra.md")
	require.NoError(t, os.WriteFile(extra, []byte("Cite file:line."), 0o644))

	replace, appended, err := systemPromptFlags(systemPromptCmd(t, "--system-prompt", "You answer in haiku.", "--append-system-prompt", "Be brief."))
	require.NoError(t, err)
	require.Equal(t, "You answer in haiku.", replace)
	require.Equal(t, "Be brief.", appended)

	replace, appended, err = systemPromptFlags(systemPromptCmd(t, "--system-prompt-file", instructions, "--append-system-prompt-file", extra))
	require.NoError(t, err)
	require.Equal(t, "You review code.", replace)
	require.Equal(t, "Cite file:line.", appended)

	_, appended, err = systemPromptFlags(systemPromptCmd(t, "--append-system-prompt", "Be brief.", "--append-system-prompt-file", extra))
	require.NoError(t, err)
	require.Equal(t, "Be brief.\n\nCite file:line.", appended, "both append flags are kept, the file's text second")

	missing := filepath.Join(dir, "nope.md")
	_, _, err = systemPromptFlags(systemPromptCmd(t, "--system-prompt-file", missing))
	require.EqualError(t, err, "system prompt file not found: "+missing)
	_, _, err = systemPromptFlags(systemPromptCmd(t, "--append-system-prompt-file", missing))
	require.EqualError(t, err, "append system prompt file not found: "+missing)

	replace, appended, err = systemPromptFlags(systemPromptCmd(t))
	require.NoError(t, err)
	require.Empty(t, replace)
	require.Empty(t, appended)

	// A command without the flags names neither.
	replace, appended, err = systemPromptFlags(&cobra.Command{Use: "bare"})
	require.NoError(t, err)
	require.Empty(t, replace)
	require.Empty(t, appended)
}

func TestSystemPromptAndItsFileAreExclusive(t *testing.T) {
	cmd := &cobra.Command{Use: "test", RunE: func(*cobra.Command, []string) error { return nil }}
	addSystemPromptFlags(cmd)
	cmd.SetArgs([]string{"--system-prompt", "a", "--system-prompt-file", "b"})
	cmd.SilenceUsage, cmd.SilenceErrors = true, true
	err := cmd.Execute()
	require.Error(t, err)
	require.Contains(t, err.Error(), "system-prompt system-prompt-file")
}

// Both klash and klash run take the flags.
func TestRootAndRunTakeTheSystemPromptFlags(t *testing.T) {
	for _, c := range []*cobra.Command{rootCmd, runCmd} {
		for _, name := range []string{"system-prompt", "system-prompt-file", "append-system-prompt", "append-system-prompt-file"} {
			require.NotNil(t, c.Flags().Lookup(name), "%s --%s", c.Name(), name)
		}
	}
}
