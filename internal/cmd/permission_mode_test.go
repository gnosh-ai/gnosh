package cmd

import (
	"testing"

	"github.com/spf13/cobra"
	"github.com/stretchr/testify/require"
)

func permissionModeCmd(t *testing.T, args ...string) *cobra.Command {
	t.Helper()
	cmd := &cobra.Command{Use: "test"}
	addPermissionModeFlag(cmd)
	addYoloFlags(cmd)
	require.NoError(t, cmd.ParseFlags(args))
	return cmd
}

// Claude Code's permission modes that klash has: plan, acceptEdits,
// bypassPermissions and default; dontAsk and auto are refused by name.
func TestPermissionModeFlag(t *testing.T) {
	for _, tc := range []struct {
		args       []string
		want, fail string
	}{
		{nil, "", ""},
		{[]string{"--permission-mode", "default"}, "default", ""},
		{[]string{"--permission-mode", "acceptEdits"}, "acceptEdits", ""},
		{[]string{"--permission-mode", "bypassPermissions"}, "bypassPermissions", ""},
		{[]string{"--permission-mode", "plan"}, "plan", ""},
		{[]string{"--permission-mode", "dontAsk"}, "", "--permission-mode dontAsk is not supported yet"},
		{[]string{"--permission-mode", "auto"}, "", "--permission-mode auto is not supported yet"},
		{[]string{"--permission-mode", "yolo"}, "", `--permission-mode "yolo": expected default, acceptEdits, bypassPermissions or plan`},
	} {
		got, err := permissionModeFlag(permissionModeCmd(t, tc.args...))
		if tc.fail != "" {
			require.ErrorContains(t, err, tc.fail, "%q", tc.args)
			continue
		}
		require.NoError(t, err, "%q", tc.args)
		require.Equal(t, tc.want, got)
	}
}

// --permission-mode bypassPermissions is --yolo by Claude Code's name.
func TestBypassPermissionsIsYolo(t *testing.T) {
	require.True(t, yoloFlag(permissionModeCmd(t, "--permission-mode", "bypassPermissions")))
	require.False(t, yoloFlag(permissionModeCmd(t, "--permission-mode", "plan")))
	require.False(t, yoloFlag(permissionModeCmd(t)))
}

// Both klash and klash run take the flag.
func TestRootAndRunTakePermissionMode(t *testing.T) {
	for _, c := range []*cobra.Command{rootCmd, runCmd} {
		require.NotNil(t, c.Flags().Lookup("permission-mode"), c.Name())
	}
}
