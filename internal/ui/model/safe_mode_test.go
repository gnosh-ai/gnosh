package model

import (
	"testing"

	"github.com/charmbracelet/crush/internal/config"
	"github.com/charmbracelet/crush/internal/ui/util"
	"github.com/charmbracelet/crush/internal/workspace"
	"github.com/stretchr/testify/require"
)

// safeModeWorkspace is a workspace with a config and nothing else.
type safeModeWorkspace struct {
	workspace.Workspace
	cfg *config.Config
}

func (w safeModeWorkspace) Config() *config.Config { return w.cfg }

// Under Claude Code's --safe-mode the TUI says, as it starts, what is off
// and how to turn it back on.
func TestSafeModeNotice(t *testing.T) {
	t.Parallel()

	u := newTestUI()
	u.com.Workspace = safeModeWorkspace{cfg: &config.Config{SafeMode: true}}
	info, ok := u.safeModeNotice()().(util.InfoMsg)
	require.True(t, ok)
	require.Contains(t, info.Msg, "Safe mode: the config's hooks, MCP servers, context files, skills and custom commands are off.")
	require.Contains(t, info.Msg, "Restart without --safe-mode")

	u.com.Workspace = safeModeWorkspace{cfg: &config.Config{}}
	require.Nil(t, u.safeModeNotice())
}
