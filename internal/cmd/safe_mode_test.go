package cmd

import (
	"path/filepath"
	"testing"

	"github.com/spf13/cobra"
	"github.com/stretchr/testify/require"
)

// Claude Code's --safe-mode is asked for by the flag, or by
// CLAUDE_CODE_SAFE_MODE or KLASH_SAFE_MODE set true.
func TestSafeModeOn(t *testing.T) {
	parse := func(args ...string) *cobra.Command {
		t.Helper()
		cmd := &cobra.Command{Use: "klash"}
		addSafeModeFlag(cmd)
		require.NoError(t, cmd.ParseFlags(args))
		return cmd
	}
	require.False(t, safeModeOn(parse()))
	require.True(t, safeModeOn(parse("--safe-mode")))
	t.Setenv("CLAUDE_CODE_SAFE_MODE", "1")
	require.True(t, safeModeOn(parse()))
	t.Setenv("CLAUDE_CODE_SAFE_MODE", "0")
	require.False(t, safeModeOn(parse()))
	t.Setenv("KLASH_SAFE_MODE", "true")
	require.True(t, safeModeOn(parse()))
}

// `klash run --safe-mode`, as Claude Code's: the project's hooks do not
// run, as its other customizations are off; without it they do. (Its
// refusal through a server is checked on the real binary: a test that got
// past it would start a server from the test binary.)
func TestRunSafeMode(t *testing.T) {
	ran := filepath.Join(t.TempDir(), "ran")
	standInRunProject(t, `"hooks": {"SessionStart": [{"command": "touch `+ran+`"}]}`)
	runSessionID(t, "--safe-mode", "list the files here")
	require.NoFileExists(t, ran, "no hook runs in safe mode")
	runSessionID(t, "list the files here")
	require.FileExists(t, ran)
}
