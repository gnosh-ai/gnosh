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

type pickSessionWorkspace struct {
	workspace.Workspace
	sessions []session.Session
	err      error
}

func (w *pickSessionWorkspace) ListSessions(context.Context) ([]session.Session, error) {
	return w.sessions, w.err
}

// A bare `crash -r` opens the sessions list as the UI starts, as Claude
// Code's `claude -r` opens its picker; with no session to pick, it says so
// instead of opening an empty list.
func TestStartupSessionPicker(t *testing.T) {
	t.Parallel()

	start := func(ws *pickSessionWorkspace, pick bool) any {
		u := newTestUI()
		u.com.Workspace = ws
		u.state = uiLanding
		if pick {
			u.PickSessionAtStart()
		}
		cmd := u.startupSessionPicker()
		if cmd == nil {
			return nil
		}
		return cmd()
	}

	ws := &pickSessionWorkspace{sessions: []session.Session{{ID: "s1", Title: "Fix the build"}}}
	require.Equal(t, dialog.ActionOpenDialog{DialogID: dialog.SessionsID}, start(ws, true))
	require.Nil(t, start(ws, false), "without -r the UI starts as before")

	msg, ok := start(&pickSessionWorkspace{}, true).(util.InfoMsg)
	require.True(t, ok)
	require.Equal(t, util.InfoTypeInfo, msg.Type)
	require.Contains(t, msg.Msg, "No sessions to resume")

	msg, ok = start(&pickSessionWorkspace{err: errors.New("database is locked")}, true).(util.InfoMsg)
	require.True(t, ok)
	require.Equal(t, util.InfoTypeError, msg.Type)
	require.Contains(t, msg.Msg, "database is locked")
}

// Before onboarding is done there is no session to go back to.
func TestStartupSessionPickerWaitsOutOnboarding(t *testing.T) {
	t.Parallel()

	u := newTestUI()
	u.com.Workspace = &pickSessionWorkspace{sessions: []session.Session{{ID: "s1"}}}
	u.state = uiOnboarding
	u.PickSessionAtStart()
	require.Nil(t, u.startupSessionPicker())
}
