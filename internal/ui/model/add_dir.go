package model

import (
	"context"
	"fmt"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/crush/internal/ui/util"
)

// addDir adds a working directory for the session, as Claude Code's
// /add-dir does, and says so, or why not.
func (m *UI) addDir(path string) tea.Cmd {
	return func() tea.Msg {
		said, added, err := m.com.Workspace.AddDir(context.Background(), path)
		switch {
		case err != nil:
			return util.ReportError(fmt.Errorf("failed to add a working directory: %w", err))()
		case added:
			return util.NewInfoMsg(said)
		default:
			return util.NewWarnMsg(said)
		}
	}
}
