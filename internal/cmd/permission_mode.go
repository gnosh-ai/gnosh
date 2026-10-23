package cmd

import (
	"fmt"

	"github.com/spf13/cobra"
)

// The --permission-mode values klash takes, Claude Code's names for them.
const (
	permissionModeDefault     = "default"
	permissionModeAcceptEdits = "acceptEdits"
	permissionModeBypass      = "bypassPermissions"
	permissionModePlan        = "plan"
)

// acceptEditsTools are the tools --permission-mode acceptEdits lets run
// without asking, as Claude Code's acceptEdits accepts file edits.
var acceptEditsTools = []string{"edit", "multiedit", "write"}

// addPermissionModeFlag adds Claude Code's --permission-mode; read it with
// permissionModeFlag.
func addPermissionModeFlag(cmd *cobra.Command) {
	cmd.Flags().String("permission-mode", "", "Claude Code's permission mode: default; acceptEdits, file edits run without asking; bypassPermissions, the same as --yolo; or plan, start in plan mode")
}

// permissionModeFlag returns cmd's --permission-mode, "" when it is not
// given. Claude Code's dontAsk and auto have no counterpart here yet, and
// are refused rather than run as something else.
func permissionModeFlag(cmd *cobra.Command) (string, error) {
	switch mode := flagValue(cmd, "permission-mode"); mode {
	case "", permissionModeDefault, permissionModeAcceptEdits, permissionModeBypass, permissionModePlan:
		return mode, nil
	case "dontAsk", "auto":
		return "", fmt.Errorf("--permission-mode %s is not supported yet; use default, acceptEdits, bypassPermissions or plan", mode)
	default:
		return "", fmt.Errorf("--permission-mode %q: expected default, acceptEdits, bypassPermissions or plan", mode)
	}
}
