package dialog

import (
	"image"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/crush/internal/shell"
	"github.com/charmbracelet/crush/internal/ui/common"
	"github.com/charmbracelet/crush/internal/ui/styles"
	"github.com/charmbracelet/crush/internal/ui/util"
	uv "github.com/charmbracelet/ultraviolet"
	"github.com/charmbracelet/x/ansi"
	"github.com/stretchr/testify/require"
)

// The tasks dialog lists the jobs with how each stands, and shows the
// chosen one's description, directory and last lines of output; ↓ and ↑
// choose, k stops a running job (an ended one is said to have ended), r
// lists them again, and Esc closes.
func TestTasksDialog(t *testing.T) {
	t.Parallel()

	sty := styles.CharmtonePantera()
	now := time.Date(2026, 10, 24, 23, 0, 0, 0, time.UTC)
	var output strings.Builder
	for i := range 20 {
		output.WriteString("line " + string(rune('a'+i)) + "\n")
	}
	jobs := []shell.JobInfo{
		{ID: "003", Command: "npm run dev", Description: "Starts the dev server", WorkingDir: "/p", StartedAt: now.Add(-2 * time.Minute), Output: strings.TrimRight(output.String(), "\n")},
		{ID: "002", Command: "go test ./...", WorkingDir: "/p", Done: true, ExitCode: 1},
		{ID: "001", Command: "make", WorkingDir: "/p/sub", Done: true, ExitCode: 1, Err: "open make: no such file"},
	}
	d := NewTasks(&common.Common{Styles: &sty}, jobs)
	d.now = func() time.Time { return now }
	draw := func() string {
		scr := uv.NewScreenBuffer(110, 40)
		d.Draw(scr, image.Rect(0, 0, 110, 40))
		return ansi.Strip(scr.Render())
	}
	press := func(k tea.KeyPressMsg) Action { return d.HandleMsg(k) }

	view := draw()
	for _, want := range []string{
		"Background Tasks", "003  running 2m0s  npm run dev", "002  exited 1  go test ./...", "001  failed  make",
		"Starts the dev server", "in /p", "line t", "line i",
	} {
		require.Contains(t, view, want)
	}
	require.NotContains(t, view, "line h", "only the last 12 lines of output")
	require.Equal(t, ActionStopTask{ID: "003"}, press(tea.KeyPressMsg{Code: 'k', Text: "k"}))

	press(tea.KeyPressMsg{Code: tea.KeyDown})
	press(tea.KeyPressMsg{Code: tea.KeyDown})
	press(tea.KeyPressMsg{Code: tea.KeyDown})
	require.Contains(t, draw(), "error: open make: no such file")
	require.Contains(t, draw(), "in /p/sub")
	stop, ok := press(tea.KeyPressMsg{Code: 'k', Text: "k"}).(ActionCmd)
	require.True(t, ok, "an ended job is not stopped")
	require.Equal(t, util.NewInfoMsg("Task 001 has already ended"), stop.Cmd())
	press(tea.KeyPressMsg{Code: tea.KeyUp})
	require.Contains(t, draw(), "(no output yet)")

	require.Equal(t, ActionRefreshTasks{}, press(tea.KeyPressMsg{Code: 'r', Text: "r"}))
	require.Equal(t, ActionClose{}, press(tea.KeyPressMsg{Code: tea.KeyEscape}))

	// Listed again without 003, the chosen 002 stays chosen.
	d.SetJobs(jobs[1:])
	stop, ok = press(tea.KeyPressMsg{Code: 'k', Text: "k"}).(ActionCmd)
	require.True(t, ok)
	require.Equal(t, util.NewInfoMsg("Task 002 has already ended"), stop.Cmd())

	d.SetJobs(nil)
	require.Contains(t, draw(), "No background tasks.")
	require.Nil(t, press(tea.KeyPressMsg{Code: 'k', Text: "k"}))
}
