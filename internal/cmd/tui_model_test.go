package cmd

import (
	"testing"

	"github.com/charmbracelet/crush/internal/workspace"
	"github.com/stretchr/testify/require"
)

// The TUI takes Claude Code's --model, as -m too, for its session only.
// Through a server, whose models every client of a workspace shares and
// which can only save a choice, it is refused.
func TestTUIModelFlag(t *testing.T) {
	t.Parallel()

	f := rootCmd.Flags().Lookup("model")
	require.NotNil(t, f)
	require.Equal(t, "m", f.Shorthand)
	err := overrideTUIModel(t.Context(), struct{ workspace.Workspace }{}, "qwen3.8-27b")
	require.EqualError(t, err, "--model is not supported with GNOSH_CLIENT_SERVER=1 yet; pick the model with /model")
}
