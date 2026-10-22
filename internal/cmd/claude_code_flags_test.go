package cmd

import (
	"testing"

	"github.com/spf13/cobra"
	"github.com/stretchr/testify/require"
)

// Jae knows Claude Code best, so its names for these flags work too:
// -r/--resume for --session, and --dangerously-skip-permissions for --yolo.
func TestClaudeCodeFlagNames(t *testing.T) {
	t.Parallel()

	parse := func(args ...string) (*cobra.Command, error) {
		cmd := &cobra.Command{Use: "crash", RunE: func(*cobra.Command, []string) error { return nil }}
		cmd.SilenceErrors, cmd.SilenceUsage = true, true
		addYoloFlags(cmd)
		addSessionFlags(cmd)
		cmd.SetArgs(args)
		return cmd, cmd.Execute()
	}

	for _, tc := range []struct {
		args    []string
		session string
		yolo    bool
	}{
		{nil, "", false},
		{[]string{"--session", "abc"}, "abc", false},
		{[]string{"-s", "abc"}, "abc", false},
		{[]string{"--resume", "abc"}, "abc", false},
		{[]string{"-r", "abc"}, "abc", false},
		{[]string{"--yolo"}, "", true},
		{[]string{"-y"}, "", true},
		{[]string{"--dangerously-skip-permissions"}, "", true},
	} {
		cmd, err := parse(tc.args...)
		require.NoError(t, err, tc.args)
		require.Equal(t, tc.session, sessionFlag(cmd), tc.args)
		require.Equal(t, tc.yolo, yoloFlag(cmd), tc.args)
	}

	_, err := parse("--resume", "abc", "--continue")
	require.Error(t, err, "--resume and --continue are exclusive, as --session and --continue are")
}
