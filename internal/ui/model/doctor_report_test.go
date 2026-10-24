package model

import (
	"context"
	"errors"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/crush/internal/config"
	"github.com/charmbracelet/crush/internal/ui/dialog"
	"github.com/charmbracelet/crush/internal/ui/util"
	"github.com/charmbracelet/crush/internal/workspace"
	"github.com/stretchr/testify/require"
)

// doctorWorkspace answers DoctorReport with report or err, and has a
// config for the palette.
type doctorWorkspace struct {
	workspace.Workspace
	report string
	err    error
}

func (w *doctorWorkspace) DoctorReport(context.Context) (string, error) { return w.report, w.err }

func (w *doctorWorkspace) Config() *config.Config {
	return &config.Config{Options: &config.Options{TUI: &config.TUIOptions{}}}
}

// messagesOf runs cmd and the commands of any batch it returns, and gives
// every message they produce.
func messagesOf(cmd tea.Cmd) []tea.Msg {
	if cmd == nil {
		return nil
	}
	msg := cmd()
	if batch, ok := msg.(tea.BatchMsg); ok {
		var msgs []tea.Msg
		for _, c := range batch {
			msgs = append(msgs, messagesOf(c)...)
		}
		return msgs
	}
	return []tea.Msg{msg}
}

// /doctor, typed or chosen in the palette (or its alias checkup), asks
// the workspace for its check of the setup and shows it, titled; a
// failure is reported.
func TestDoctorReportOpensTheDialog(t *testing.T) {
	t.Parallel()

	u := newTestUI()
	u.dialog = dialog.NewOverlay()
	ws := &doctorWorkspace{report: "## Doctor"}
	u.com.Workspace = ws
	want := reportMsg{title: "Doctor", report: "## Doctor"}

	require.Equal(t, want, u.sendMessage("/doctor")())
	require.Equal(t, want, u.sendMessage(" /checkup")())

	c, err := dialog.NewCommands(u.com, "", false, false, false, nil, nil)
	require.NoError(t, err)
	u.dialog.OpenDialog(c)
	for _, r := range "doctor" {
		u.handleDialogMsg(tea.KeyPressMsg{Code: r, Text: string(r)})
	}
	require.Contains(t, messagesOf(u.handleDialogMsg(tea.KeyPressMsg{Code: tea.KeyEnter})), tea.Msg(want))
	require.False(t, u.dialog.ContainsDialog(dialog.CommandsID))

	_, _ = u.Update(want)
	require.True(t, u.dialog.ContainsDialog(dialog.ReportID))

	ws.err = errors.New("workspace not found")
	info, ok := u.doctorReport()().(util.InfoMsg)
	require.True(t, ok)
	require.Equal(t, util.InfoTypeError, info.Type)
	require.Equal(t, "workspace not found", info.Msg)
}
