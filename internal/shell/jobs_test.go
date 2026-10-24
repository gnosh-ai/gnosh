package shell

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// /tasks lists the jobs started in the workspace's directory or below it,
// the newest first, each with how it ended and the end of its output;
// another directory's jobs are not listed.
func TestJobs(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	sub := filepath.Join(dir, "sub")
	require.NoError(t, os.Mkdir(sub, 0o755))
	m := newBackgroundShellManager()
	start := func(in, command string) *BackgroundShell {
		t.Helper()
		s, err := m.Start(t.Context(), in, nil, command, "about "+command, testMaxJobs)
		require.NoError(t, err)
		return s
	}
	failed := start(dir, "echo out; echo err >&2; exit 3")
	failed.Wait()
	time.Sleep(10 * time.Millisecond)
	running := start(sub, "echo started; sleep 30")
	t.Cleanup(func() { _ = m.Kill(running.ID) })
	other := start(t.TempDir(), "sleep 30")
	t.Cleanup(func() { _ = m.Kill(other.ID) })
	require.Eventually(t, func() bool {
		stdout, _, _, _ := running.GetOutput()
		return stdout != ""
	}, 5*time.Second, 10*time.Millisecond)

	jobs := m.Jobs(dir)
	require.Len(t, jobs, 2)
	require.Equal(t, running.ID, jobs[0].ID, "the newest first")
	require.False(t, jobs[0].Done)
	require.Equal(t, "started", jobs[0].Output)
	require.Equal(t, sub, jobs[0].WorkingDir)
	require.WithinDuration(t, time.Now(), jobs[0].StartedAt, time.Minute)
	require.Equal(t, JobInfo{
		ID: failed.ID, Command: "echo out; echo err >&2; exit 3", Description: "about echo out; echo err >&2; exit 3",
		WorkingDir: dir, StartedAt: failed.StartedAt, Done: true, ExitCode: 3, Output: "out\nerr",
	}, jobs[1])
}

// A workspace stops only its own jobs: one started elsewhere is not found.
func TestKillIn(t *testing.T) {
	t.Parallel()

	dir, elsewhere := t.TempDir(), t.TempDir()
	require.NoError(t, os.Mkdir(filepath.Join(dir, "sub"), 0o755))
	m := newBackgroundShellManager()
	mine, err := m.Start(t.Context(), filepath.Join(dir, "sub"), nil, "sleep 30", "", testMaxJobs)
	require.NoError(t, err)
	theirs, err := m.Start(t.Context(), elsewhere, nil, "sleep 30", "", testMaxJobs)
	require.NoError(t, err)
	t.Cleanup(func() { _ = m.Kill(theirs.ID) })

	require.EqualError(t, m.KillIn(dir, theirs.ID), "background shell not found: "+theirs.ID)
	require.False(t, theirs.IsDone())
	require.NoError(t, m.KillIn(dir, mine.ID))
	require.True(t, mine.IsDone())
	require.Empty(t, m.Jobs(dir))
	require.False(t, inDir(dir, dir+"-other"), "a sibling that shares the name's start is not inside")

	// With no directory, every job is listed and any can be stopped.
	require.Len(t, m.Jobs(""), 1)
	require.NoError(t, m.KillIn("", theirs.ID))
	require.True(t, theirs.IsDone())
}

// The output kept is its end, from the start of a line, stdout before
// stderr.
func TestOutputTail(t *testing.T) {
	t.Parallel()

	require.Equal(t, "a\nb", outputTail("a\n", "b\n"))
	require.Equal(t, "b", outputTail("", "b"))
	long := strings.Repeat("0123456789\n", 1000)
	tail := outputTail(long, "")
	require.LessOrEqual(t, len(tail), jobOutputTail)
	require.True(t, strings.HasPrefix(tail, "0123456789\n"), "starts at a line")
	require.True(t, strings.HasSuffix(tail, "\n0123456789"))
}
