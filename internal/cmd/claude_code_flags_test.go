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
		cmd := &cobra.Command{Use: "klash", RunE: func(*cobra.Command, []string) error { return nil }}
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
// must parse on the run command. Each case parses a fresh command: flags
// marked as set by one parse would otherwise leak into the next, and make
// the exclusivity check pass whatever the flags.
func TestRunAcceptsClaudeCodeFlags(t *testing.T) {
	t.Parallel()

	parseRun := func(t *testing.T, args ...string) (*cobra.Command, error) {
		t.Helper()
		cmd := &cobra.Command{Use: "run"}
		addRunFlags(cmd)
		require.NoError(t, cmd.ParseFlags(args))
		return cmd, cmd.ValidateFlagGroups()
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
			t.Parallel()
			cmd, err := parseRun(t, tc.args...)
			require.NoError(t, err)
			require.Equal(t, tc.session, sessionFlag(cmd))
			require.Equal(t, tc.yolo, yoloFlag(cmd))
		})
	}

	_, err := parseRun(t, "-r", "abc", "-C", "hi")
	require.Error(t, err, "-r and -C are exclusive, as -s and -C are")
	_, err = parseRun(t, "-r", "abc", "-s", "def", "hi")
	require.Error(t, err, "-r and -s are exclusive")
}

// klash run accepts the yolo flags for Claude Code's sake but keeps them
// from the workspace: a yolo workspace it created on a shared server would
// hand yolo to a TUI that joined it later. The TUI's own --yolo still
// reaches its workspace.
func TestRunKeepsYoloFromTheWorkspace(t *testing.T) {
	require.NoError(t, runCmd.Flags().Set("dangerously-skip-permissions", "true"))
	t.Cleanup(func() { _ = runCmd.Flags().Set("dangerously-skip-permissions", "false") })
	require.True(t, yoloFlag(runCmd))
	require.False(t, workspaceYolo(runCmd))
	require.Contains(t, runCmd.Flags().Lookup("yolo").Usage, "changes nothing")

	root := &cobra.Command{Use: "klash"}
	addYoloFlags(root)
	require.NoError(t, root.ParseFlags([]string{"--yolo"}))
	require.True(t, workspaceYolo(root))
}

// Claude Code's -p/--print, one question without the TUI, is klash run.
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
