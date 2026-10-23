package cmd

import (
	"fmt"

	"github.com/charmbracelet/crush/internal/config"
	"github.com/spf13/cobra"
)

// acceptEditsTools are the tools --permission-mode acceptEdits lets run
// without asking, as Claude Code's acceptEdits accepts file edits.
var acceptEditsTools = []string{"edit", "multiedit", "write"}

// addPermissionModeFlag adds Claude Code's --permission-mode; read it with
// permissionModeFlag.
func addPermissionModeFlag(cmd *cobra.Command) {
	cmd.Flags().String("permission-mode", "", "Claude Code's permission mode: default; acceptEdits, file edits run without asking; bypassPermissions, the same as --yolo; or plan, start in plan mode")
}

// applyPermissionMode keeps a --permission-mode in the runtime overrides:
// plan starts the session in the plan agent, and acceptEdits, whose edit
// tools go into the allowed tools, is remembered for the hooks, which are
// told the mode.
func applyPermissionMode(o *config.RuntimeOverrides, mode string) {
	if mode == config.PermissionModePlan {
		o.MainAgent = config.AgentPlan
	}
	o.AcceptEdits = mode == config.PermissionModeAcceptEdits
}

// permissionModeFlag returns cmd's --permission-mode, "" when it is not
// given. Claude Code's dontAsk and auto have no counterpart here yet, and
// are refused rather than run as something else.
func permissionModeFlag(cmd *cobra.Command) (string, error) {
	switch mode := flagValue(cmd, "permission-mode"); mode {
	case "", config.PermissionModeDefault, config.PermissionModeAcceptEdits, config.PermissionModeBypass, config.PermissionModePlan:
		return mode, nil
	case "dontAsk", "auto":
		return "", fmt.Errorf("--permission-mode %s is not supported yet; use default, acceptEdits, bypassPermissions or plan", mode)
	default:
		return "", fmt.Errorf("--permission-mode %q: expected default, acceptEdits, bypassPermissions or plan", mode)
	}
}
