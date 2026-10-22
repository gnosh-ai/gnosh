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

// Claude Code's -p/--print, one question without the TUI, is crash run.
func TestPrintAsRun(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		args, want []string
		rewritten  bool
	}{
		{[]string{"-p", "what is this?"}, []string{"run", "what is this?"}, true},
		{[]string{"--print", "--model", "qwen", "hi"}, []string{"run", "--model", "qwen", "hi"}, true},
		{[]string{"-p"}, []string{"run"}, true},
		{[]string{"run", "hi"}, []string{"run", "hi"}, false},
		{[]string{"-s", "abc"}, []string{"-s", "abc"}, false},
		{nil, nil, false},
	} {
		got, ok := printAsRun(tc.args)
		require.Equal(t, tc.rewritten, ok, tc.args)
		require.Equal(t, tc.want, got, tc.args)
	}
}
