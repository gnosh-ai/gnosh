package cmd

import (
	"testing"

	"github.com/spf13/cobra"
	"github.com/stretchr/testify/require"
)

// A bare -r or --resume asks for the session list, as Claude Code's
// `claude -r` does; `-r <id>` still names the session.
func TestBareResume(t *testing.T) {
	for _, tc := range []struct {
		args, want []string
	}{
		{[]string{"-r"}, []string{"--resume=" + pickSession}},
		{[]string{"--resume"}, []string{"--resume=" + pickSession}},
		{[]string{"-r", "--yolo"}, []string{"--resume=" + pickSession, "--yolo"}},
		{[]string{"run", "-r"}, []string{"run", "--resume=" + pickSession}},
		{[]string{"-r", "4d6f2a1c"}, []string{"-r", "4d6f2a1c"}},
		{[]string{"--resume", "4d6f2a1c"}, []string{"--resume", "4d6f2a1c"}},
		{[]string{"--resume=4d6f2a1c"}, []string{"--resume=4d6f2a1c"}},
		{[]string{"-C"}, []string{"-C"}},
		{[]string{"run", "--", "-r"}, []string{"run", "--", "-r"}},
		{[]string{"__complete", "-r"}, []string{"__complete", "-r"}},
		{nil, nil},
	} {
		require.Equal(t, tc.want, bareResume(tc.args), "%q", tc.args)
	}
}

// klash run cannot show a list to pick from, so a bare -r there says what
// to give it instead of looking for a session named ":pick:".
func TestRunRefusesABareResume(t *testing.T) {
	cmd := &cobra.Command{Use: "run", RunE: runCmd.RunE}
	addRunFlags(cmd)
	cmd.SetArgs([]string{"--resume=" + pickSession, "hello"})
	cmd.SilenceUsage, cmd.SilenceErrors = true, true
	err := cmd.Execute()
	require.Error(t, err)
	require.Contains(t, err.Error(), "a session ID must follow -r in klash run")
}
