package cmd

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// A prompt may follow klash's flags, as Claude Code's `claude "query"`
// does; a first word naming a subcommand is that subcommand.
func TestRootTakesAPrompt(t *testing.T) {
	t.Parallel()

	require.NoError(t, rootCmd.ValidateArgs([]string{"explain this project"}))
	found, args, err := rootCmd.Find([]string{"explain this project"})
	require.NoError(t, err)
	require.Equal(t, rootCmd, found)
	require.Equal(t, []string{"explain this project"}, args)

	found, _, err = rootCmd.Find([]string{"run", "hi"})
	require.NoError(t, err)
	require.Equal(t, runCmd, found)
}
