package filetracker

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"testing/synctest"
	"time"

	"github.com/charmbracelet/crush/internal/db"
	"github.com/stretchr/testify/require"
)

type testEnv struct {
	ctx context.Context
	q   *db.Queries
	svc Service
}

func setupTest(t *testing.T) *testEnv {
	t.Helper()

	conn, err := db.Connect(t.Context(), t.TempDir())
	require.NoError(t, err)
	t.Cleanup(func() { conn.Close() })

	q := db.New(conn)
	return &testEnv{
		ctx: t.Context(),
		q:   q,
		svc: NewService(q),
	}
}

func (e *testEnv) createSession(t *testing.T, sessionID string) {
	t.Helper()
	_, err := e.q.CreateSession(e.ctx, db.CreateSessionParams{
		ID:    sessionID,
		Title: "Test Session",
	})
	require.NoError(t, err)
}

func TestService_RecordRead(t *testing.T) {
	env := setupTest(t)

	sessionID := "test-session-1"
	path := "/path/to/file.go"
	env.createSession(t, sessionID)

	env.svc.RecordRead(env.ctx, sessionID, path)

	lastRead := env.svc.LastReadTime(env.ctx, sessionID, path)
	require.False(t, lastRead.IsZero(), "expected non-zero time after recording read")
	require.WithinDuration(t, time.Now(), lastRead, 2*time.Second)
}

func TestService_LastReadTime_NotFound(t *testing.T) {
	env := setupTest(t)

	lastRead := env.svc.LastReadTime(env.ctx, "nonexistent-session", "/nonexistent/path")
	require.True(t, lastRead.IsZero(), "expected zero time for unread file")
}

func TestService_RecordRead_UpdatesTimestamp(t *testing.T) {
	env := setupTest(t)

	sessionID := "test-session-2"
	path := "/path/to/file.go"
	env.createSession(t, sessionID)

	env.svc.RecordRead(env.ctx, sessionID, path)
	firstRead := env.svc.LastReadTime(env.ctx, sessionID, path)
	require.False(t, firstRead.IsZero())

	synctest.Test(t, func(t *testing.T) {
		time.Sleep(100 * time.Millisecond)
		synctest.Wait()
		env.svc.RecordRead(env.ctx, sessionID, path)
		secondRead := env.svc.LastReadTime(env.ctx, sessionID, path)

		require.False(t, secondRead.Before(firstRead), "second read time should not be before first")
	})
}

func TestService_RecordRead_DifferentSessions(t *testing.T) {
	env := setupTest(t)

	path := "/shared/file.go"
	session1, session2 := "session-1", "session-2"
	env.createSession(t, session1)
	env.createSession(t, session2)

	env.svc.RecordRead(env.ctx, session1, path)

	lastRead1 := env.svc.LastReadTime(env.ctx, session1, path)
	require.False(t, lastRead1.IsZero())

	lastRead2 := env.svc.LastReadTime(env.ctx, session2, path)
	require.True(t, lastRead2.IsZero(), "session 2 should not see session 1's read")
}

func TestService_RecordRead_DifferentPaths(t *testing.T) {
	env := setupTest(t)

	sessionID := "test-session-3"
	path1, path2 := "/path/to/file1.go", "/path/to/file2.go"
	env.createSession(t, sessionID)

	env.svc.RecordRead(env.ctx, sessionID, path1)

	lastRead1 := env.svc.LastReadTime(env.ctx, sessionID, path1)
	require.False(t, lastRead1.IsZero())

	lastRead2 := env.svc.LastReadTime(env.ctx, sessionID, path2)
	require.True(t, lastRead2.IsZero(), "path2 should not be recorded")
}

// A file written back as it was since its read, as git stash and pop
// do, counts as unchanged whatever its modification time says; a change
// to its content, its removal, or a read in another session does not.
func TestService_UnchangedSinceRead(t *testing.T) {
	env := setupTest(t)
	env.createSession(t, "s")
	path := filepath.Join(t.TempDir(), "a.go")
	require.NoError(t, os.WriteFile(path, []byte("package a\n"), 0o644))

	require.False(t, env.svc.UnchangedSinceRead(env.ctx, "s", path), "never read")
	env.svc.RecordRead(env.ctx, "s", path)
	require.True(t, env.svc.UnchangedSinceRead(env.ctx, "s", path))
	require.False(t, env.svc.UnchangedSinceRead(env.ctx, "other", path), "read in another session")

	later := time.Now().Add(time.Minute)
	require.NoError(t, os.WriteFile(path, []byte("package a\n"), 0o644))
	require.NoError(t, os.Chtimes(path, later, later))
	require.True(t, env.svc.UnchangedSinceRead(env.ctx, "s", path), "written back as it was")

	require.NoError(t, os.WriteFile(path, []byte("package b\n"), 0o644))
	require.False(t, env.svc.UnchangedSinceRead(env.ctx, "s", path), "changed")

	require.NoError(t, os.Remove(path))
	require.False(t, env.svc.UnchangedSinceRead(env.ctx, "s", path), "removed")
}

// A file larger than maxComparedSize is not hashed at its reads, so it
// is judged by its modification time: unchanged until modified in a
// later second than the read, even when it is then written back as it
// was at an earlier read, since what the agent read last was the large
// file.
func TestService_UnchangedSinceReadJudgesLargeFilesByTime(t *testing.T) {
	env := setupTest(t)
	env.createSession(t, "s")
	path := filepath.Join(t.TempDir(), "big.log")
	require.NoError(t, os.WriteFile(path, []byte("small\n"), 0o644))
	env.svc.RecordRead(env.ctx, "s", path)
	require.True(t, env.svc.UnchangedSinceRead(env.ctx, "s", path))

	require.NoError(t, os.Truncate(path, maxComparedSize+1))
	env.svc.RecordRead(env.ctx, "s", path)
	require.False(t, env.svc.LastReadTime(env.ctx, "s", path).IsZero(), "the read is still recorded")
	require.True(t, env.svc.UnchangedSinceRead(env.ctx, "s", path), "not modified since the read")

	later := time.Now().Add(time.Hour)
	require.NoError(t, os.WriteFile(path, []byte("small\n"), 0o644))
	require.NoError(t, os.Chtimes(path, later, later))
	require.False(t, env.svc.UnchangedSinceRead(env.ctx, "s", path), "not compared with the read before the last")
}

// A file read in an earlier run of Klash, a session resumed since, has
// no hash in this run: it counts as unchanged unless modified in a later
// second than the read, as the edit tool judges it. That includes a file
// written and read in the same second, as the write tool does.
func TestService_UnchangedSinceReadInALaterRun(t *testing.T) {
	env := setupTest(t)
	env.createSession(t, "s")
	dir := t.TempDir()
	untouched := filepath.Join(dir, "a.go")
	written := filepath.Join(dir, "b.go")
	changed := filepath.Join(dir, "c.go")
	earlier := time.Now().Add(-time.Hour)
	for _, path := range []string{untouched, written, changed} {
		require.NoError(t, os.WriteFile(path, []byte("package a\n"), 0o644))
		if path != written {
			require.NoError(t, os.Chtimes(path, earlier, earlier))
		}
		env.svc.RecordRead(env.ctx, "s", path)
	}
	later := time.Now().Add(time.Hour)
	require.NoError(t, os.WriteFile(changed, []byte("package c\n"), 0o644))
	require.NoError(t, os.Chtimes(changed, later, later))

	resumed := NewService(env.q)
	require.True(t, resumed.UnchangedSinceRead(env.ctx, "s", untouched))
	require.True(t, resumed.UnchangedSinceRead(env.ctx, "s", written), "written and read in the same second")
	require.False(t, resumed.UnchangedSinceRead(env.ctx, "s", changed))
	require.False(t, resumed.UnchangedSinceRead(env.ctx, "other", untouched), "never read in that session")
	require.NoError(t, os.Remove(untouched))
	require.False(t, resumed.UnchangedSinceRead(env.ctx, "s", untouched), "removed")
}
