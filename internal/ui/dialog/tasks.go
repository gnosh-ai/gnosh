package dialog

import (
	"fmt"
	"strings"
	"time"

	"charm.land/bubbles/v2/help"
	"charm.land/bubbles/v2/key"
	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/crush/internal/shell"
	"github.com/charmbracelet/crush/internal/ui/common"
	"github.com/charmbracelet/crush/internal/ui/util"
	uv "github.com/charmbracelet/ultraviolet"
	"github.com/charmbracelet/x/ansi"
)

// TasksID is the identifier for the background tasks dialog.
const TasksID = "tasks"

// tasksOutputLines is how many of the chosen job's last lines of output
// the dialog shows.
const tasksOutputLines = 12

// Tasks lists the jobs the agent started in the background, as Claude
// Code's /tasks does: ↑ and ↓ choose one, whose command and last lines of
// output show under the list, k stops it, and r lists them again.
type Tasks struct {
	com      *common.Common
	jobs     []shell.JobInfo
	selected int
	// now is the clock the running times are read from.
	now    func() time.Time
	help   help.Model
	keyMap struct {
		Up, Down, Stop, Refresh, Close key.Binding
	}
}

var _ Dialog = (*Tasks)(nil)

// NewTasks creates the dialog on jobs, the newest first.
func NewTasks(com *common.Common, jobs []shell.JobInfo) *Tasks {
	d := &Tasks{com: com, jobs: jobs, now: time.Now}
	d.help = help.New()
	d.help.Styles = com.Styles.DialogHelpStyles()
	d.keyMap.Up = key.NewBinding(key.WithKeys("up"), key.WithHelp("↑", "up"))
	d.keyMap.Down = key.NewBinding(key.WithKeys("down", "j"), key.WithHelp("↓", "down"))
	d.keyMap.Stop = key.NewBinding(key.WithKeys("k"), key.WithHelp("k", "stop"))
	d.keyMap.Refresh = key.NewBinding(key.WithKeys("r"), key.WithHelp("r", "refresh"))
	d.keyMap.Close = CloseKey
	return d
}

// ID implements [Dialog].
func (*Tasks) ID() string {
	return TasksID
}

// SetJobs shows jobs instead, keeping the chosen job chosen while it is
// still listed.
func (d *Tasks) SetJobs(jobs []shell.JobInfo) {
	id := ""
	if d.selected < len(d.jobs) {
		id = d.jobs[d.selected].ID
	}
	d.jobs = jobs
	d.selected = min(d.selected, max(0, len(jobs)-1))
	for i, j := range jobs {
		if j.ID == id {
			d.selected = i
		}
	}
}

// HandleMsg implements [Dialog].
func (d *Tasks) HandleMsg(msg tea.Msg) Action {
	press, ok := msg.(tea.KeyPressMsg)
	if !ok {
		return nil
	}
	switch {
	case key.Matches(press, d.keyMap.Close):
		return ActionClose{}
	case key.Matches(press, d.keyMap.Up):
		d.selected = max(0, d.selected-1)
	case key.Matches(press, d.keyMap.Down):
		d.selected = min(max(0, len(d.jobs)-1), d.selected+1)
	case key.Matches(press, d.keyMap.Refresh):
		return ActionRefreshTasks{}
	case key.Matches(press, d.keyMap.Stop):
		if d.selected >= len(d.jobs) {
			return nil
		}
		job := d.jobs[d.selected]
		if job.Done {
			return ActionCmd{Cmd: util.ReportInfo("Task " + job.ID + " has already ended")}
		}
		return ActionStopTask{ID: job.ID}
	}
	return nil
}

// Draw implements [Dialog].
func (d *Tasks) Draw(scr uv.Screen, area uv.Rectangle) *tea.Cursor {
	t := d.com.Styles
	width := max(0, min(reportMaxWidth, area.Dx()-t.Dialog.View.GetHorizontalBorderSize()))
	innerWidth := width - t.Dialog.View.GetHorizontalFrameSize()
	rc := NewRenderContext(t, width)
	rc.Title = "Background Tasks"
	if len(d.jobs) == 0 {
		rc.AddPart(t.Dialog.SecondaryText.Width(innerWidth).Render("No background tasks. A task is a command the agent runs in the background."))
	}
	for i, job := range d.jobs {
		style := t.Dialog.NormalItem
		if i == d.selected {
			style = t.Dialog.SelectedItem
		}
		line := fmt.Sprintf("%s  %s  %s", job.ID, d.state(job), strings.ReplaceAll(job.Command, "\n", " "))
		rc.AddPart(style.Width(innerWidth).Render(ansi.Truncate(line, innerWidth-style.GetHorizontalFrameSize(), "…")))
	}
	if d.selected < len(d.jobs) {
		rc.AddPart(t.Dialog.SecondaryText.Width(innerWidth).Render(d.details(d.jobs[d.selected], innerWidth-t.Dialog.SecondaryText.GetHorizontalFrameSize())))
	}
	rc.Help = renderDialogHelp(t, &d.help, d, innerWidth)
	DrawCenter(scr, area, rc.Render())
	return nil
}

// state is how a job stands: running and for how long, or how it ended.
func (d *Tasks) state(job shell.JobInfo) string {
	switch {
	case !job.Done:
		return "running " + d.now().Sub(job.StartedAt).Round(time.Second).String()
	case job.Err != "":
		return "failed"
	case job.ExitCode != 0:
		return fmt.Sprintf("exited %d", job.ExitCode)
	}
	return "done"
}

// details is the chosen job's description, directory and error, and its
// last lines of output, each cut to width.
func (d *Tasks) details(job shell.JobInfo, width int) string {
	lines := []string{""}
	if job.Description != "" {
		lines = append(lines, job.Description)
	}
	lines = append(lines, "in "+job.WorkingDir)
	if job.Err != "" {
		lines = append(lines, "error: "+job.Err)
	}
	if job.Output == "" {
		lines = append(lines, "", "(no output yet)")
	} else {
		output := strings.Split(job.Output, "\n")
		lines = append(lines, "")
		lines = append(lines, output[max(0, len(output)-tasksOutputLines):]...)
	}
	for i, line := range lines {
		lines[i] = ansi.Truncate(ansi.Strip(line), width, "…")
	}
	return strings.Join(lines, "\n")
}

// ShortHelp implements [help.KeyMap].
func (d *Tasks) ShortHelp() []key.Binding {
	return []key.Binding{d.keyMap.Up, d.keyMap.Down, d.keyMap.Stop, d.keyMap.Refresh, d.keyMap.Close}
}

// FullHelp implements [help.KeyMap].
func (d *Tasks) FullHelp() [][]key.Binding {
	return [][]key.Binding{d.ShortHelp()}
}
