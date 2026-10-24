package workspace

import (
	"slices"
	"testing"

	"github.com/charmbracelet/crush/internal/config"
	"github.com/charmbracelet/crush/internal/shell"
	"github.com/stretchr/testify/require"
)

// In process, /tasks lists and stops every job of the process, which hosts
// the one workspace: a job the bash tool started outside the working
// directory is listed too.
func TestAppWorkspaceBackgroundJobs(t *testing.T) {
	dir := t.TempDir()
	m := shell.GetBackgroundShellManager()
	inside, err := m.Start(t.Context(), dir, nil, "sleep 30", "", 50)
	require.NoError(t, err)
	t.Cleanup(func() { _ = m.Kill(inside.ID) })
	outside, err := m.Start(t.Context(), t.TempDir(), nil, "sleep 30", "", 50)
	require.NoError(t, err)
	t.Cleanup(func() { _ = m.Kill(outside.ID) })

	w := &AppWorkspace{store: config.NewTestStoreIn(&config.Config{}, dir)}
	jobs, err := w.BackgroundJobs(t.Context())
	require.NoError(t, err)
	listed := func(id string) bool {
		return slices.ContainsFunc(jobs, func(j shell.JobInfo) bool { return j.ID == id })
	}
	require.True(t, listed(inside.ID))
	require.True(t, listed(outside.ID))
	require.NoError(t, w.KillBackgroundJob(t.Context(), outside.ID))
	require.True(t, outside.IsDone())
	require.Error(t, w.KillBackgroundJob(t.Context(), outside.ID), "stopped already")
}
