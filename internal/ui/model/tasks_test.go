package model

import (
	"context"
	"errors"
	"slices"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/crush/internal/config"
	"github.com/charmbracelet/crush/internal/shell"
	"github.com/charmbracelet/crush/internal/ui/dialog"
	"github.com/charmbracelet/crush/internal/ui/util"
	"github.com/charmbracelet/crush/internal/workspace"
	"github.com/stretchr/testify/require"
)

// tasksWorkspace has background jobs, and records which it was asked to
// stop.
type tasksWorkspace struct {
	workspace.Workspace
	jobs    []shell.JobInfo
	stopped []string
	err     error
}

func (w *tasksWorkspace) BackgroundJobs(context.Context) ([]shell.JobInfo, error) {
	return w.jobs, w.err
}

func (w *tasksWorkspace) KillBackgroundJob(_ context.Context, id string) error {
	if w.err != nil {
		return w.err
	}
	w.stopped = append(w.stopped, id)
	w.jobs = slices.DeleteFunc(w.jobs, func(j shell.JobInfo) bool { return j.ID == id })
	return nil
}

func (w *tasksWorkspace) Config() *config.Config {
	return &config.Config{Options: &config.Options{TUI: &config.TUIOptions{}}}
}

// /tasks, typed or chosen in the palette (or as /bashes), lists the
// workspace's background jobs in the tasks dialog; k there stops the
// chosen one, says so and lists them again; r lists them again; a failure
// is reported.
func TestTasks(t *testing.T) {
	t.Parallel()

	u := newTestUI()
	u.dialog = dialog.NewOverlay()
	ws := &tasksWorkspace{jobs: []shell.JobInfo{{ID: "002", Command: "sleep 99"}, {ID: "001", Command: "make", Done: true}}}
	u.com.Workspace = ws

	msg := u.sendMessage("/tasks")()
	require.Equal(t, tasksMsg{jobs: ws.jobs}, msg)
	require.Equal(t, tasksMsg{jobs: ws.jobs}, u.sendMessage(" /bashes")())
	_, _ = u.Update(msg)
	require.True(t, u.dialog.ContainsDialog(dialog.TasksID))
	opened := u.dialog.DialogLast()

	msgs := messagesOf(u.handleDialogMsg(tea.KeyPressMsg{Code: 'k', Text: "k"}))
	require.Equal(t, []string{"002"}, ws.stopped)
	require.Equal(t, []tea.Msg{tasksMsg{jobs: ws.jobs, done: "Stopped task 002"}}, msgs)
	require.Equal(t, []tea.Msg{util.NewInfoMsg("Stopped task 002")}, messagesOf(u.showTasks(msgs[0].(tasksMsg))))
	require.Same(t, opened, u.dialog.DialogLast(), "the open dialog is updated, not opened again")

	ws.jobs = append(ws.jobs, shell.JobInfo{ID: "003", Command: "npm run dev"})
	require.Equal(t, []tea.Msg{tasksMsg{jobs: ws.jobs}}, messagesOf(u.handleDialogMsg(tea.KeyPressMsg{Code: 'r', Text: "r"})))

	u.dialog.CloseDialog(dialog.TasksID)
	c, err := dialog.NewCommands(u.com, "", false, false, false, nil, nil)
	require.NoError(t, err)
	u.dialog.OpenDialog(c)
	for _, r := range "tasks" {
		u.handleDialogMsg(tea.KeyPressMsg{Code: r, Text: string(r)})
	}
	require.Contains(t, messagesOf(u.handleDialogMsg(tea.KeyPressMsg{Code: tea.KeyEnter})), tea.Msg(tasksMsg{jobs: ws.jobs}))
	require.False(t, u.dialog.ContainsDialog(dialog.CommandsID))

	ws.err = errors.New("workspace not found")
	require.Equal(t, util.InfoTypeError, u.listTasks()().(util.InfoMsg).Type)
	require.Equal(t, util.InfoTypeError, u.stopTask("003")().(util.InfoMsg).Type)
}
