package db

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/charmbracelet/crush/internal/lock"
	"github.com/stretchr/testify/require"
)

func TestConnect_SharesConnectionForSameDataDir(t *testing.T) {
	t.Cleanup(ResetPool)

	dataDir := t.TempDir()

	conn1, err := Connect(context.Background(), dataDir)
	require.NoError(t, err)

	conn2, err := Connect(context.Background(), dataDir)
	require.NoError(t, err)

	require.Same(t, conn1, conn2, "should return the same *sql.DB for the same data dir")

	// Releasing once should not close the connection.
	require.NoError(t, Release(dataDir))
	require.NoError(t, conn1.PingContext(context.Background()), "connection should still be usable after partial release")

	// Releasing again should close it.
	require.NoError(t, Release(dataDir))
	require.Error(t, conn1.PingContext(context.Background()), "connection should be closed after final release")
}

func TestConnect_SeparateConnectionsForDifferentDataDirs(t *testing.T) {
	t.Cleanup(ResetPool)

	dir1 := t.TempDir()
	dir2 := t.TempDir()

	conn1, err := Connect(context.Background(), dir1)
	require.NoError(t, err)

	conn2, err := Connect(context.Background(), dir2)
	require.NoError(t, err)

	require.NotSame(t, conn1, conn2, "different data dirs should get different connections")

	require.NoError(t, Release(dir1))
	require.NoError(t, Release(dir2))
}

func TestRelease_NoopForUnknownDataDir(t *testing.T) {
	t.Cleanup(ResetPool)

	require.NoError(t, Release("/nonexistent/path"), "releasing unknown data dir should not error")
}

// TestConnect_FailsWhenDataDirLocked simulates a second crush process by
// taking the data-dir lock directly via the OS primitive on a separate
// file descriptor and then asserting that Connect surfaces a clean
// ErrDataDirLocked instead of opening the database under contention.
func TestConnect_FailsWhenDataDirLocked(t *testing.T) {
	t.Cleanup(ResetPool)

	dataDir := t.TempDir()
	lockPath := dataDirLockFile(dataDir)

	release, err := lock.TryFile(lockPath)
	require.NoError(t, err, "expected to take the data-dir lock for the first time")
	t.Cleanup(release)

	_, err = Connect(context.Background(), dataDir, WithDataDirLock(true))
	require.Error(t, err, "Connect must refuse to open a locked data dir")
	require.ErrorIs(t, err, ErrDataDirLocked)
}

// TestConnect_SucceedsAfterContenderReleases ensures the lock is purely
// advisory and that a clean release lets the next Connect proceed.
func TestConnect_SucceedsAfterContenderReleases(t *testing.T) {
	t.Cleanup(ResetPool)

	dataDir := t.TempDir()
	lockPath := dataDirLockFile(dataDir)

	release, err := lock.TryFile(lockPath)
	require.NoError(t, err)

	_, err = Connect(context.Background(), dataDir, WithDataDirLock(true))
	require.ErrorIs(t, err, ErrDataDirLocked)

	release()

	conn, err := Connect(context.Background(), dataDir, WithDataDirLock(true))
	require.NoError(t, err, "Connect should succeed once the contender releases the lock")
	require.NoError(t, conn.PingContext(context.Background()))
	require.NoError(t, Release(dataDir))
}

// TestConnect_LockReleasedOnFinalRelease confirms that closing the last
// reference to a pool entry also drops the OS lock, so subsequent
// processes can take the data dir.
func TestConnect_LockReleasedOnFinalRelease(t *testing.T) {
	t.Cleanup(ResetPool)

	dataDir := t.TempDir()
	lockPath := dataDirLockFile(dataDir)

	conn, err := Connect(context.Background(), dataDir, WithDataDirLock(true))
	require.NoError(t, err)
	require.NoError(t, conn.PingContext(context.Background()))

	// Holding the in-process entry must keep the OS lock held so a
	// "second process" (simulated by a fresh lock.TryFile call) is
	// rejected.
	_, lockErr := lock.TryFile(lockPath)
	require.Error(t, lockErr)
	require.True(t, errors.Is(lockErr, lock.ErrContended), "expected contended lock while pool entry is live")

	require.NoError(t, Release(dataDir))

	// After the final release the lock is free again.
	release, err := lock.TryFile(lockPath)
	require.NoError(t, err, "expected lock to be released after final Release")
	release()
}

// TestConnect_SharedPoolDoesNotReacquireLock makes sure that subsequent
// in-process Connect calls reuse the existing OS lock through refcount,
// not by re-acquiring it. The simplest observable signal of correctness
// is that the second Connect does not error and the lock is still held
// after a single Release.
func TestConnect_SharedPoolDoesNotReacquireLock(t *testing.T) {
	t.Cleanup(ResetPool)

	dataDir := t.TempDir()
	lockPath := dataDirLockFile(dataDir)

	_, err := Connect(context.Background(), dataDir, WithDataDirLock(true))
	require.NoError(t, err)

	_, err = Connect(context.Background(), dataDir, WithDataDirLock(true))
	require.NoError(t, err)

	// Drop one reference; lock must still be held.
	require.NoError(t, Release(dataDir))
	_, lockErr := lock.TryFile(lockPath)
	require.ErrorIs(t, lockErr, lock.ErrContended)

	require.NoError(t, Release(dataDir))
}

// TestConnect_SkipLockEnvBypassesAcquisition exercises the escape
// hatch used by users on filesystems where flock is unreliable.
func TestConnect_SkipLockEnvBypassesAcquisition(t *testing.T) {
	t.Cleanup(ResetPool)

	dataDir := t.TempDir()
	lockPath := dataDirLockFile(dataDir)

	release, err := lock.TryFile(lockPath)
	require.NoError(t, err)
	t.Cleanup(release)

	t.Setenv("CRUSH_SKIP_DATADIR_LOCK", "1")

	conn, err := Connect(context.Background(), dataDir, WithDataDirLock(true))
	require.NoError(t, err, "skip-lock env should bypass contention")
	require.NoError(t, conn.PingContext(context.Background()))
	require.NoError(t, Release(dataDir))
}

// TestConnect_DefaultIgnoresContendedLock confirms that without
// WithDataDirLock(true) the lock file is irrelevant: a contender can
// hold lock.TryFile and Connect still succeeds. This pins the
// local-mode default to its pre-lock behavior.
func TestConnect_DefaultIgnoresContendedLock(t *testing.T) {
	t.Cleanup(ResetPool)

	dataDir := t.TempDir()
	lockPath := dataDirLockFile(dataDir)

	release, err := lock.TryFile(lockPath)
	require.NoError(t, err, "expected to take the data-dir lock for the first time")
	t.Cleanup(release)

	conn, err := Connect(context.Background(), dataDir)
	require.NoError(t, err, "default Connect must not take the lock and must succeed under contention")
	require.NoError(t, conn.PingContext(context.Background()))
	require.NoError(t, Release(dataDir))
}

// TestConnect_ServerPathFailsWhenDataDirLocked is the server's
// workspace-bootstrap analogue of TestConnect_FailsWhenDataDirLocked:
// passing WithDataDirLock(true) must surface ErrDataDirLocked when a
// contender already holds the lock.
func TestConnect_ServerPathFailsWhenDataDirLocked(t *testing.T) {
	t.Cleanup(ResetPool)

	dataDir := t.TempDir()
	lockPath := dataDirLockFile(dataDir)

	release, err := lock.TryFile(lockPath)
	require.NoError(t, err, "expected to take the data-dir lock for the first time")
	t.Cleanup(release)

	_, err = Connect(context.Background(), dataDir, WithDataDirLock(true))
	require.Error(t, err, "server-path Connect must refuse to open a locked data dir")
	require.ErrorIs(t, err, ErrDataDirLocked)
}

// A new data directory gets crash.db and crash.lock; one that already
// holds the crush.db from before the rename keeps it, and locks
// crush.lock as an older process sharing it does, so the two cannot open
// it at once.
func TestDataFileNames(t *testing.T) {
	t.Cleanup(ResetPool)

	fresh := t.TempDir()
	conn, err := Connect(context.Background(), fresh)
	require.NoError(t, err)
	require.NoError(t, conn.PingContext(context.Background()))
	require.FileExists(t, filepath.Join(fresh, "crash.db"))
	require.NoFileExists(t, filepath.Join(fresh, "crush.db"))
	require.Equal(t, filepath.Join(fresh, "crash.lock"), dataDirLockFile(fresh))
	require.NoError(t, Release(fresh))

	// An existing database from before the rename is opened, not
	// replaced by a new, empty crash.db beside it.
	old := t.TempDir()
	require.NoError(t, os.Rename(filepath.Join(fresh, "crash.db"), filepath.Join(old, "crush.db")))
	conn, err = Connect(context.Background(), old)
	require.NoError(t, err)
	require.NoError(t, conn.PingContext(context.Background()))
	require.NoFileExists(t, filepath.Join(old, "crash.db"))
	require.Equal(t, filepath.Join(old, "crush.db"), FileName(old))
	require.Equal(t, filepath.Join(old, "crush.lock"), dataDirLockFile(old))
	require.NoError(t, Release(old))
}
