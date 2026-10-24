package cmd

import (
	"fmt"

	"github.com/charmbracelet/crush/internal/config"
	"github.com/spf13/cobra"
)

// addPermissionModeFlag adds Claude Code's --permission-mode; read it with
// permissionModeFlag.
func addPermissionModeFlag(cmd *cobra.Command) {
	cmd.Flags().String("permission-mode", "", "Claude Code's permission mode: default (or manual); acceptEdits, edits of files in the project run without asking; dontAsk, what would be asked about is denied instead; bypassPermissions, the same as --yolo; or plan, start in plan mode")
}

// applyPermissionMode keeps a --permission-mode in the runtime overrides:
// plan starts the session in the plan agent, and acceptEdits and dontAsk
// go to the permission service (edits in the project run unasked; what
// would be asked about is denied) and to the hooks, which are told the
// mode.
func applyPermissionMode(o *config.RuntimeOverrides, mode string) {
	if mode == config.PermissionModePlan {
		o.MainAgent = config.AgentPlan
	}
	o.AcceptEdits = mode == config.PermissionModeAcceptEdits
	o.DontAsk = mode == config.PermissionModeDontAsk
}

// permissionModeFlag returns cmd's --permission-mode, "" when it is not
// given; manual, Claude Code's other name for default, is default. Claude
// Code's auto has no counterpart here yet, and is refused rather than run
// as something else.
func permissionModeFlag(cmd *cobra.Command) (string, error) {
	switch mode := flagValue(cmd, "permission-mode"); mode {
	case "", config.PermissionModeDefault, config.PermissionModeAcceptEdits, config.PermissionModeBypass, config.PermissionModePlan, config.PermissionModeDontAsk:
		return mode, nil
	case "manual":
		return config.PermissionModeDefault, nil
	case "auto":
		return "", fmt.Errorf("--permission-mode %s is not supported yet; use default, acceptEdits, dontAsk, bypassPermissions or plan", mode)
	default:
		return "", fmt.Errorf("--permission-mode %q: expected default, acceptEdits, dontAsk, bypassPermissions or plan", mode)
	}
}
