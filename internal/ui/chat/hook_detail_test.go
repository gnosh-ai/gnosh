package chat

import (
	"testing"

	"github.com/charmbracelet/crush/internal/hooks"
	"github.com/charmbracelet/crush/internal/ui/styles"
	"github.com/charmbracelet/x/ansi"
	"github.com/stretchr/testify/require"
)

// plain is a styled string the way a reader sees it, without the escape
// codes, so tests can assert on the words.
func plain(s string) string { return ansi.Strip(s) }

// A hook that never reached a verdict used to fall through to the
// default branch and be drawn as a green "OK" — so a safety hook pointed
// at a missing script vouched for every call while checking nothing.
func TestHookDetail_FailedHookIsNotOK(t *testing.T) {
	t.Parallel()

	sty := styles.CharmtonePantera()
	got := plain(hookDetail(&sty, hooks.HookInfo{
		Name:     "no-rm-rf",
		Decision: "none",
		Error:    "exit 1: open ./hooks/no-rm-rf.sh: no such file or directory",
	}))
	require.Contains(t, got, "Failed")
	require.Contains(t, got, "no such file or directory")
	require.NotContains(t, got, "OK", "a hook that did not run must not say it passed")
}

// The ordinary cases must render exactly as they did before.
func TestHookDetail_UnchangedForRealVerdicts(t *testing.T) {
	t.Parallel()

	sty := styles.CharmtonePantera()
	require.Contains(t, plain(hookDetail(&sty, hooks.HookInfo{Decision: "none"})), "OK",
		"a hook that ran cleanly and had no opinion is genuinely OK")
	require.Contains(t, plain(hookDetail(&sty, hooks.HookInfo{Decision: "allow"})), "OK")

	denied := plain(hookDetail(&sty, hooks.HookInfo{Decision: "deny", Reason: "rm -rf"}))
	require.Contains(t, denied, "Denied")
	require.Contains(t, denied, "rm -rf")
}
