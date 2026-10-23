package cmd

import (
	"bytes"
	"errors"
	"testing"

	fang "charm.land/fang/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/spf13/cobra"
	"github.com/stretchr/testify/require"
)

// printError runs a command that fails with msg through fang, as Execute
// does, and returns what was printed, with styling removed.
func printError(t *testing.T, msg string, opts ...fang.Option) string {
	t.Helper()
	var out bytes.Buffer
	cmd := &cobra.Command{Use: "klash", RunE: func(*cobra.Command, []string) error { return errors.New(msg) }}
	cmd.SetArgs(nil)
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	require.Error(t, fang.Execute(t.Context(), cmd, opts...))
	return ansi.Strip(out.String())
}

// Only the first letter of an error is capitalized. fang's own handler
// title-cases the whole first word, so a flag, a quoted name or an
// environment variable at the start of an error came out changed.
func TestErrorHandlerKeepsTheFirstWord(t *testing.T) {
	t.Parallel()

	require.Contains(t, printError(t, "--disallowed-tools: unknown tool"), "--Disallowed-Tools",
		"fang's own handler changes the first word, which is what this fixes")

	for _, tc := range []struct{ err, want string }{
		{"unknown tool \"bsh\" (in --disallowed-tools)", "Unknown tool \"bsh\" (in --disallowed-tools)."},
		{"--disallowed-tools: unknown tool", "--disallowed-tools: unknown tool."},
		{"\"Bash(git:*)\" is a command pattern", "\"Bash(git:*)\" is a command pattern."},
		{"KLASH_CLIENT_SERVER=1 is set", "KLASH_CLIENT_SERVER=1 is set."},
	} {
		require.Contains(t, printError(t, tc.err, fang.WithErrorHandler(errorHandler)), tc.want, tc.err)
	}
}

func TestCapitalizeFirstLetter(t *testing.T) {
	t.Parallel()

	require.Equal(t, "Failed to-load x-y", capitalizeFirstLetter("failed to-load x-y"))
	require.Equal(t, "Éclair", capitalizeFirstLetter("éclair"))
	require.Equal(t, "", capitalizeFirstLetter(""))
	require.Equal(t, " leading space", capitalizeFirstLetter(" leading space"))
}
