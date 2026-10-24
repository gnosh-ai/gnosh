package model

import (
	"context"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/crush/internal/shell"
	"github.com/charmbracelet/crush/internal/ui/dialog"
	"github.com/charmbracelet/crush/internal/ui/util"
)

// tasksMsg carries the background jobs for the tasks dialog, and what was
// done to them, if anything, to say.
type tasksMsg struct {
	jobs []shell.JobInfo
	done string
}

// listTasks asks the workspace for its background jobs, as Claude Code's
// /tasks lists what runs in the background, off the update loop: in client
// mode the server is asked.
func (m *UI) listTasks() tea.Cmd {
	return func() tea.Msg {
		jobs, err := m.com.Workspace.BackgroundJobs(context.Background())
		if err != nil {
			return util.ReportError(err)()
		}
		return tasksMsg{jobs: jobs}
	}
}

// stopTask stops background job id, as /tasks does with k, and lists the
// jobs again.
func (m *UI) stopTask(id string) tea.Cmd {
	return func() tea.Msg {
		if err := m.com.Workspace.KillBackgroundJob(context.Background(), id); err != nil {
			return util.ReportError(err)()
		}
		jobs, err := m.com.Workspace.BackgroundJobs(context.Background())
		if err != nil {
			return util.ReportError(err)()
		}
		return tasksMsg{jobs: jobs, done: "Stopped task " + id}
	}
}

// showTasks shows the jobs in the tasks dialog, opening it unless it is
// open already.
func (m *UI) showTasks(msg tasksMsg) tea.Cmd {
	if d, ok := m.dialog.Dialog(dialog.TasksID).(*dialog.Tasks); ok {
		d.SetJobs(msg.jobs)
	} else {
		m.dialog.OpenDialog(dialog.NewTasks(m.com, msg.jobs))
	}
	if msg.done != "" {
		return util.ReportInfo(msg.done)
	}
	return nil
}
