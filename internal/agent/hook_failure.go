package agent

import "github.com/charmbracelet/crush/internal/hooks"

// hookFailure says how a hook that did not reach a verdict went wrong,
// for a warning: "could not evaluate: why" for one that could not check,
// which blocks wherever a hook can, or "failed: why" for a non-blocking
// error.
func hookFailure(h hooks.HookInfo) string {
	if h.CouldNotEvaluate {
		return "could not evaluate: " + h.Error
	}
	return "failed: " + h.Error
}
