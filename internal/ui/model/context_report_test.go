package model

import (
	"context"
	"errors"
	"testing"

	"github.com/charmbracelet/crush/internal/session"
	"github.com/charmbracelet/crush/internal/ui/dialog"
	"github.com/charmbracelet/crush/internal/ui/util"
	"github.com/charmbracelet/crush/internal/workspace"
	"github.com/stretchr/testify/require"
)

// contextWorkspace answers ContextReport with report or err, and keeps the
// sessions asked about.
type contextWorkspace struct {
	workspace.Workspace
	report string
	err    error
	asked  []string
}

func (w *contextWorkspace) ContextReport(_ context.Context, sessionID string) (string, error) {
	w.asked = append(w.asked, sessionID)
	return w.report, w.err
}

// /context asks for the current session's report (a new session's before
// there is one) and shows it in its dialog; a failure is reported.
func TestContextReportOpensTheDialog(t *testing.T) {
	t.Parallel()

	u := newTestUI()
	u.dialog = dialog.NewOverlay()
	ws := &contextWorkspace{report: "## Context Usage"}
	u.com.Workspace = ws

	msg := u.contextReport()()
	require.Equal(t, contextReportMsg{report: "## Context Usage"}, msg)
	u.session = &session.Session{ID: "s1"}
	u.contextReport()()
	require.Equal(t, []string{"", "s1"}, ws.asked)

	_, _ = u.Update(msg)
	require.True(t, u.dialog.ContainsDialog(dialog.ContextUsageID))

	ws.err = errors.New("no model configured")
	info, ok := u.contextReport()().(util.InfoMsg)
	require.True(t, ok)
	require.Equal(t, util.InfoTypeError, info.Type)
	require.Contains(t, info.Msg, "no model configured")
}
