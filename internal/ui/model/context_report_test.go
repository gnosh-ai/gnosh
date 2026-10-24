package model

import (
	"context"
	"errors"
	"testing"

	"github.com/charmbracelet/crush/internal/config"
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
	require.Equal(t, reportMsg{title: "Context Usage", report: "## Context Usage"}, msg)
	u.session = &session.Session{ID: "s1"}
	u.contextReport()()
	require.Equal(t, []string{"", "s1"}, ws.asked)

	_, _ = u.Update(msg)
	require.True(t, u.dialog.ContainsDialog(dialog.ReportID))

	ws.err = errors.New("no model configured")
	info, ok := u.contextReport()().(util.InfoMsg)
	require.True(t, ok)
	require.Equal(t, util.InfoTypeError, info.Type)
	require.Contains(t, info.Msg, "no model configured")
}

// hooksWorkspace has a config with hooks.
type hooksWorkspace struct {
	workspace.Workspace
	cfg *config.Config
}

func (w *hooksWorkspace) Config() *config.Config { return w.cfg }

// /hooks opens the report dialog on the configured hooks.
func TestOpenHooksReport(t *testing.T) {
	t.Parallel()

	u := newTestUI()
	u.dialog = dialog.NewOverlay()
	u.com.Workspace = &hooksWorkspace{cfg: &config.Config{Hooks: map[string][]config.HookConfig{
		config.EventStop: {{Command: "stop-check.sh"}},
	}}}
	u.openHooksReport()
	require.True(t, u.dialog.ContainsDialog(dialog.ReportID))
}

// permissionsWorkspace answers PermissionsReport with report or err.
type permissionsWorkspace struct {
	workspace.Workspace
	report string
	err    error
}

func (w *permissionsWorkspace) PermissionsReport(context.Context) (string, error) {
	return w.report, w.err
}

// /permissions asks the workspace for its report and shows it, titled; a
// failure is reported.
func TestPermissionsReportOpensTheDialog(t *testing.T) {
	t.Parallel()

	u := newTestUI()
	u.dialog = dialog.NewOverlay()
	ws := &permissionsWorkspace{report: "## Permissions"}
	u.com.Workspace = ws

	msg := u.permissionsReport()()
	require.Equal(t, reportMsg{title: "Permissions", report: "## Permissions"}, msg)
	_, _ = u.Update(msg)
	require.True(t, u.dialog.ContainsDialog(dialog.ReportID))

	ws.err = errors.New("agent coordinator not initialized")
	info, ok := u.permissionsReport()().(util.InfoMsg)
	require.True(t, ok)
	require.Equal(t, util.InfoTypeError, info.Type)
}
