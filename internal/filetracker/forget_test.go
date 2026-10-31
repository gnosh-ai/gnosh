package filetracker

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/charmbracelet/crush/internal/db"
	"github.com/charmbracelet/crush/internal/session"
	"github.com/stretchr/testify/require"
)

// Forgetting a session's reads by path leaves its other reads, and the
// other session's read of the same file, as they were.
func TestForgetReads(t *testing.T) {
	conn, err := db.Connect(t.Context(), t.TempDir())
	require.NoError(t, err)
	t.Cleanup(func() { _ = conn.Close() })
	q := db.New(conn)
	sessions := session.NewService(q, conn)
	a, err := sessions.Create(t.Context(), "a")
	require.NoError(t, err)
	b, err := sessions.Create(t.Context(), "b")
	require.NoError(t, err)

	dir := t.TempDir()
	one, two := filepath.Join(dir, "one.go"), filepath.Join(dir, "two.go")
	require.NoError(t, os.WriteFile(one, []byte("package one\n"), 0o644))
	require.NoError(t, os.WriteFile(two, []byte("package two\n"), 0o644))

	svc := NewService(q)
	svc.RecordRead(t.Context(), a.ID, one)
	svc.RecordRead(t.Context(), a.ID, two)
	svc.RecordRead(t.Context(), b.ID, one)
	require.True(t, svc.UnchangedSinceRead(t.Context(), a.ID, one))

	require.NoError(t, svc.ForgetReads(t.Context(), a.ID, []string{one, filepath.Join(dir, "never-read.go")}))

	require.True(t, svc.LastReadTime(t.Context(), a.ID, one).IsZero(), "the read is forgotten")
	require.False(t, svc.UnchangedSinceRead(t.Context(), a.ID, one), "and so is what was read")
	require.False(t, svc.LastReadTime(t.Context(), a.ID, two).IsZero(), "the other read stays")
	require.False(t, svc.LastReadTime(t.Context(), b.ID, one).IsZero(), "and the other session's")
}
