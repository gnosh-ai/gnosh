package cmd

import (
	"testing"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
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

// Claude Code users run `claude -p --dangerously-skip-permissions "question"`
// and `claude -p -r <session-id> "question"`; after the -p rewrite these
// must parse on the run command.
func TestRunAcceptsClaudeCodeFlags(t *testing.T) {
	t.Parallel()

	resetRunFlags := func() {
		runCmd.Flags().VisitAll(func(f *pflag.Flag) {
			switch f.Value.Type() {
			case "bool":
				_ = f.Value.Set("false")
			default:
				_ = f.Value.Set(f.DefValue)
			}
		})
	}

	parseRun := func(args ...string) (*cobra.Command, error) {
		resetRunFlags()
		runCmd.SetArgs(args)
		return runCmd, runCmd.ParseFlags(args)
	}

	for _, tc := range []struct {
		name    string
		args    []string
		session string
		yolo    bool
	}{
		{"plain", []string{"hi"}, "", false},
		{"yolo", []string{"--yolo", "hi"}, "", true},
		{"dangerously", []string{"--dangerously-skip-permissions", "hi"}, "", true},
		{"resume", []string{"-r", "abc", "hi"}, "abc", false},
		{"resume-long", []string{"--resume", "abc", "hi"}, "abc", false},
		{"session", []string{"-s", "abc", "hi"}, "abc", false},
		{"resume-and-yolo", []string{"-r", "abc", "--dangerously-skip-permissions", "hi"}, "abc", true},
		{"existing-model", []string{"-m", "qwen", "hi"}, "", false},
		{"existing-quiet", []string{"-q", "hi"}, "", false},
		{"existing-verbose", []string{"-v", "hi"}, "", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cmd, err := parseRun(tc.args...)
			require.NoError(t, err, tc.name)
			require.Equal(t, tc.session, sessionFlag(cmd), tc.name)
			require.Equal(t, tc.yolo, yoloFlag(cmd), tc.name)
		})
	}

	// -r and --continue are mutually exclusive, as -s and --continue are.
	{
		resetRunFlags()
		runCmd.SetArgs([]string{"-r", "abc", "-C", "hi"})
		require.NoError(t, runCmd.ParseFlags([]string{"-r", "abc", "-C", "hi"}))
		require.Error(t, runCmd.ValidateFlagGroups(), "-r and -C are exclusive")
	}
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
