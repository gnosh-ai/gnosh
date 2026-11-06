package db

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/charmbracelet/crush/internal/lock"
	"github.com/stretchr/testify/require"
	"go.uber.org/goleak"
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

// A data directory removed while gnosh runs (a project generator that
// wants an empty folder) is written back from the open database, and
// what is written after goes to the restored file.
func TestConnect_RestoresRemovedDataDir(t *testing.T) {
	warnings := captureWarnings(t)
	dataDir := filepath.Join(t.TempDir(), ".gnosh")
	t.Cleanup(ResetPool)
	conn, err := Connect(t.Context(), dataDir, WithRestore(true), checkOften)
	require.NoError(t, err)
	q := New(conn)
	createSession := func(id string) {
		t.Helper()
		_, err := q.CreateSession(t.Context(), CreateSessionParams{ID: id, Title: id})
		require.NoError(t, err)
	}

	createSession("before-removal")
	require.NoError(t, os.RemoveAll(dataDir))
	createSession("after-removal")

	dbPath := filepath.Join(dataDir, "gnosh.db")
	require.Eventually(t, func() bool {
		ids, err := sessionIDsIn(t.Context(), dbPath)
		return err == nil && len(ids) == 2
	}, 5*time.Second, 10*time.Millisecond, "the removed database is not back at its path")

	// Lands in the restored file only if the pool let go of its
	// connection to the removed one.
	createSession("after-restore")
	ids, err := sessionIDsIn(t.Context(), dbPath)
	require.NoError(t, err)
	require.ElementsMatch(t, []string{"before-removal", "after-removal", "after-restore"}, ids)

	// Without it, git add -A picks up the restored database.
	gitIgnore, err := os.ReadFile(filepath.Join(dataDir, ".gitignore"))
	require.NoError(t, err)
	require.Equal(t, "*\n", string(gitIgnore))

	require.Eventually(t, func() bool {
		return strings.Contains(warnings(), "Data directory was removed while in use; restored the database")
	}, 5*time.Second, 10*time.Millisecond, warnings())
}

// A data directory removed again after a restore, with no query in
// between, is restored again.
func TestConnect_RestoresADataDirRemovedTwice(t *testing.T) {
	dataDir := filepath.Join(t.TempDir(), ".gnosh")
	t.Cleanup(ResetPool)
	conn, err := Connect(t.Context(), dataDir, WithRestore(true), checkOften)
	require.NoError(t, err)
	_, err = New(conn).CreateSession(t.Context(), CreateSessionParams{ID: "kept", Title: "kept"})
	require.NoError(t, err)

	dbPath := filepath.Join(dataDir, "gnosh.db")
	want := []string{"kept"}
	holds := func() bool {
		ids, err := sessionIDsIn(t.Context(), dbPath)
		return err == nil && slices.Equal(slices.Sorted(slices.Values(ids)), slices.Sorted(slices.Values(want)))
	}
	for i := range 2 {
		require.NoError(t, os.RemoveAll(dataDir))
		require.Eventually(t, holds, 5*time.Second, 10*time.Millisecond, "removal %d is not restored", i+1)
		// What gnosh writes next lands in the restored file. Once it is
		// there the pool holds that file, as it held the removed one, and
		// the next removal is restored from it. (A removal before the pool
		// reopens the file finds nothing holding it: that gap is the
		// restore's, not this test's.)
		id := fmt.Sprintf("after-%d", i+1)
		_, err := New(conn).CreateSession(t.Context(), CreateSessionParams{ID: id, Title: id})
		require.NoError(t, err)
		want = append(want, id)
		require.Eventually(t, holds, 5*time.Second, 10*time.Millisecond, "a write after restore %d is not in the restored file", i+1)
	}
}

// A data directory removed again once a restore has put the database
// back at its path, before the pool holds the restored file, is restored
// again.
func TestConnect_RestoresARemovalDuringTheRestore(t *testing.T) {
	for name, remove := range map[string]func(dataDir string) error{
		"data directory": os.RemoveAll,
		// As rm -r midway: the file is gone, its directory not yet.
		"database file": func(dataDir string) error {
			return os.Remove(filepath.Join(dataDir, "gnosh.db"))
		},
	} {
		t.Run(name, func(t *testing.T) {
			warnings := captureWarnings(t)
			dataDir := filepath.Join(t.TempDir(), ".gnosh")
			t.Cleanup(ResetPool)
			removedAgain := false
			removeAgain := withRestoredHook(func() {
				if !removedAgain {
					removedAgain = true
					require.NoError(t, remove(dataDir))
				}
			})
			conn, err := Connect(t.Context(), dataDir, WithRestore(true), checkRarely, removeAgain)
			require.NoError(t, err)
			_, err = New(conn).CreateSession(t.Context(), CreateSessionParams{ID: "kept", Title: "kept"})
			require.NoError(t, err)
			require.NoError(t, os.RemoveAll(dataDir))

			// As two checks would: the first restores and sees its file
			// removed again, the second finds it gone.
			entry := pool[poolKey(dataDir)]
			entry.checkFile(t.Context())
			require.True(t, removedAgain, "the restore never put the file back")
			require.Contains(t, warnings(), "Failed to restore the removed database")
			entry.checkFile(t.Context())

			ids, err := sessionIDsIn(t.Context(), filepath.Join(dataDir, "gnosh.db"))
			require.NoError(t, err, "the second removal is not restored")
			require.Equal(t, []string{"kept"}, ids)
		})
	}
}

// The same, while a query waits for the pool's connection, whichever
// connection the query gets.
func TestConnect_RestoresARemovalDuringTheRestoreWhileAQueryWaits(t *testing.T) {
	warnings := captureWarnings(t)
	dataDir := filepath.Join(t.TempDir(), ".gnosh")
	t.Cleanup(ResetPool)
	entry := restoreWhileAQueryWaits(t, dataDir, func() {
		require.NoError(t, os.RemoveAll(dataDir))
	})
	require.Contains(t, warnings(), "Failed to restore the removed database")
	entry.checkFile(t.Context())

	ids, err := sessionIDsIn(t.Context(), filepath.Join(dataDir, "gnosh.db"))
	require.NoError(t, err, "the pool lost the database")
	require.Equal(t, []string{"kept"}, ids)
}

// A file put at the database path during a restore, while a query waits
// for the pool's connection, leaves the pool on the removed file.
func TestConnect_KeepsTheDatabaseWhenAFileTakesThePathDuringTheRestore(t *testing.T) {
	warnings := captureWarnings(t)
	dataDir := filepath.Join(t.TempDir(), ".gnosh")
	t.Cleanup(ResetPool)
	entry := restoreWhileAQueryWaits(t, dataDir, func() {
		other := filepath.Join(dataDir, "other.db")
		require.NoError(t, os.WriteFile(other, nil, 0o600))
		require.NoError(t, os.Rename(other, filepath.Join(dataDir, "gnosh.db")))
	})
	require.Contains(t, warnings(), "another file took the restored database's path")

	sessions, err := New(entry.db).ListSessions(t.Context())
	require.NoError(t, err, "the pool lost the database")
	require.Len(t, sessions, 1)
}

// restoreWhileAQueryWaits opens a restoring pool on dataDir with a session
// in it, removes the directory and runs a check. Once the restore has put
// the database back at its path, a query waits for the pool's connection
// and during runs. It returns when the query is done; whether the query
// fails depends on the connection it gets.
func restoreWhileAQueryWaits(t *testing.T, dataDir string, during func()) *connEntry {
	t.Helper()
	var conn *sql.DB
	queried := make(chan struct{})
	ran := false
	hook := withRestoredHook(func() {
		if ran {
			return
		}
		ran = true
		waits := conn.Stats().WaitCount
		go func() {
			defer close(queried)
			_, _ = New(conn).ListSessions(t.Context())
		}()
		require.Eventually(t, func() bool {
			return conn.Stats().WaitCount > waits
		}, 5*time.Second, time.Millisecond, "the query never waited for the pool")
		during()
	})
	var err error
	conn, err = Connect(t.Context(), dataDir, WithRestore(true), checkRarely, hook)
	require.NoError(t, err)
	_, err = New(conn).CreateSession(t.Context(), CreateSessionParams{ID: "kept", Title: "kept"})
	require.NoError(t, err)
	require.NoError(t, os.RemoveAll(dataDir))

	entry := pool[poolKey(dataDir)]
	entry.checkFile(t.Context())
	require.True(t, ran, "the restore never put the file back")
	select {
	case <-queried:
	case <-time.After(5 * time.Second):
		t.Fatal("the waiting query never got a connection")
	}
	return entry
}

// A restore whose check is canceled, as by a Release, once the database is
// back at its path still moves the pool onto the restored file.
func TestConnect_RestoreCanceledAfterTheCopyMovesThePool(t *testing.T) {
	dataDir := filepath.Join(t.TempDir(), ".gnosh")
	t.Cleanup(ResetPool)
	ctx, cancel := context.WithCancel(t.Context())
	conn, err := Connect(t.Context(), dataDir, WithRestore(true), checkRarely, withRestoredHook(cancel))
	require.NoError(t, err)
	require.NoError(t, os.RemoveAll(dataDir))

	pool[poolKey(dataDir)].checkFile(ctx)

	_, err = New(conn).CreateSession(t.Context(), CreateSessionParams{ID: "after-restore", Title: "after-restore"})
	require.NoError(t, err)
	ids, err := sessionIDsIn(t.Context(), filepath.Join(dataDir, "gnosh.db"))
	require.NoError(t, err)
	require.Equal(t, []string{"after-restore"}, ids, "the pool stayed on the removed file")
}

// A data directory removed since the last check, just before gnosh exits,
// is restored before the pool closes the removed file.
func TestRelease_RestoresARemovalSinceTheLastCheck(t *testing.T) {
	dataDir := filepath.Join(t.TempDir(), ".gnosh")
	t.Cleanup(ResetPool)
	conn, err := Connect(t.Context(), dataDir, WithRestore(true), checkRarely)
	require.NoError(t, err)
	_, err = New(conn).CreateSession(t.Context(), CreateSessionParams{ID: "kept", Title: "kept"})
	require.NoError(t, err)
	require.NoError(t, os.RemoveAll(dataDir))

	require.NoError(t, Release(dataDir))
	ids, err := sessionIDsIn(t.Context(), filepath.Join(dataDir, "gnosh.db"))
	require.NoError(t, err)
	require.Equal(t, []string{"kept"}, ids)
}

// A restored data directory is locked again when it was locked before, at
// the lock named for its database.
func TestConnect_LocksRestoredDataDir(t *testing.T) {
	for name, db := range map[string]string{
		"gnosh.db":                "",
		"from before the renames": "crush.db",
	} {
		t.Run(name, func(t *testing.T) {
			dataDir := filepath.Join(t.TempDir(), ".gnosh")
			t.Cleanup(ResetPool)
			lockName := "gnosh.lock"
			if db != "" {
				require.NoError(t, os.Mkdir(dataDir, 0o700))
				require.NoError(t, os.WriteFile(filepath.Join(dataDir, db), nil, 0o600))
				lockName = "crush.lock"
			}
			_, err := Connect(t.Context(), dataDir, WithDataDirLock(true), WithRestore(true), checkOften)
			require.NoError(t, err)
			require.NoError(t, os.RemoveAll(dataDir))

			// The owner info is written once the lock is held;
			// trying the lock before then could take it from the
			// restore.
			lockPath := filepath.Join(dataDir, lockName)
			require.Eventually(t, func() bool {
				return readOwnerInfo(lockPath).PID == os.Getpid()
			}, 5*time.Second, 10*time.Millisecond, "the restored data directory is not locked")
			_, err = lock.TryFile(lockPath)
			require.ErrorIs(t, err, lock.ErrContended)
		})
	}
}

// A removed data directory that another gnosh made again and locked is
// left to it: the open database is not copied into it.
func TestConnect_LeavesARemovedDataDirAnotherLocked(t *testing.T) {
	dataDir := filepath.Join(t.TempDir(), ".gnosh")
	t.Cleanup(ResetPool)
	_, err := Connect(t.Context(), dataDir, WithDataDirLock(true), WithRestore(true), checkRarely)
	require.NoError(t, err)
	require.NoError(t, os.RemoveAll(dataDir))

	require.NoError(t, os.Mkdir(dataDir, 0o700))
	release, err := lock.TryFile(filepath.Join(dataDir, "gnosh.lock"))
	require.NoError(t, err)
	defer release()

	require.NoError(t, Release(dataDir))
	require.NoFileExists(t, filepath.Join(dataDir, "gnosh.db"))
}

// Another file put at the database path while gnosh runs is never written
// over. An empty one, since VACUUM INTO itself refuses a file with
// anything in it.
func TestConnect_LeavesAnotherFileAtTheDatabasePath(t *testing.T) {
	warnings := captureWarnings(t)
	dataDir := t.TempDir()
	t.Cleanup(ResetPool)
	conn, err := Connect(t.Context(), dataDir, WithRestore(true), checkOften)
	require.NoError(t, err)
	_, err = New(conn).CreateSession(t.Context(), CreateSessionParams{ID: "ours", Title: "ours"})
	require.NoError(t, err)

	// Renamed over the database, so the path is never missing.
	dbPath := filepath.Join(dataDir, "gnosh.db")
	other := filepath.Join(dataDir, "other.db")
	require.NoError(t, os.WriteFile(other, nil, 0o600))
	require.NoError(t, os.Rename(other, dbPath))

	require.Never(t, func() bool {
		info, err := os.Stat(dbPath)
		return err != nil || info.Size() != 0
	}, 300*time.Millisecond, 10*time.Millisecond, "the other file at the database path was changed")
	require.Contains(t, warnings(), "Another file is at the database path")
}

// A file put at the database path after the check found it missing is
// left as it is, and the restore leaves nothing else behind.
func TestConnect_RestoreLeavesAFileThatAppearsAtThePath(t *testing.T) {
	dataDir := t.TempDir()
	t.Cleanup(ResetPool)
	_, err := Connect(t.Context(), dataDir, WithRestore(true), checkRarely)
	require.NoError(t, err)
	require.NoError(t, os.RemoveAll(dataDir))
	require.NoError(t, os.Mkdir(dataDir, 0o700))
	dbPath := filepath.Join(dataDir, "gnosh.db")
	require.NoError(t, os.WriteFile(dbPath, nil, 0o600))

	// As a check that found the path free just before the file came.
	pool[poolKey(dataDir)].restore(t.Context())

	info, err := os.Stat(dbPath)
	require.NoError(t, err)
	require.Zero(t, info.Size(), "the file at the database path was written over")
	entries, err := os.ReadDir(dataDir)
	require.NoError(t, err)
	names := make([]string, 0, len(entries))
	for _, entry := range entries {
		names = append(names, entry.Name())
	}
	require.ElementsMatch(t, []string{".gitignore", "gnosh.db"}, names)
}

// Dropping an entry stops the check of its file with it.
func TestConnect_StopsCheckingWhenDropped(t *testing.T) {
	for name, drop := range map[string]func(t *testing.T, dataDir string){
		"release":    func(t *testing.T, dataDir string) { require.NoError(t, Release(dataDir)) },
		"reset pool": func(*testing.T, string) { ResetPool() },
	} {
		t.Run(name, func(t *testing.T) {
			ignore := goleak.IgnoreCurrent()
			dataDir := t.TempDir()
			t.Cleanup(ResetPool)
			_, err := Connect(t.Context(), dataDir, WithRestore(true))
			require.NoError(t, err)

			drop(t, dataDir)
			goleak.VerifyNone(t, ignore)
		})
	}
}

// Without WithRestore, Connect starts no check of the database file, so a
// test that never calls Release leaves none running.
func TestConnect_ChecksNoFileWithoutRestore(t *testing.T) {
	ignore := goleak.IgnoreCurrent()
	t.Cleanup(ResetPool)

	_, err := Connect(t.Context(), t.TempDir())
	require.NoError(t, err)
	goleak.VerifyNone(t, ignore, goleak.IgnoreTopFunction("database/sql.(*DB).connectionOpener"))
}

// A pool is found by its data directory, also once the directory was
// removed with a database from before the renames in it, which FileName
// can then no longer name: Connect joins the pool instead of opening a new
// gnosh.db, and Release stops the pool's check.
func TestConnect_FindsARemovedDataDirByItsPath(t *testing.T) {
	ignore := goleak.IgnoreCurrent()
	dataDir := t.TempDir()
	t.Cleanup(ResetPool)
	require.NoError(t, os.WriteFile(filepath.Join(dataDir, "crush.db"), nil, 0o600))
	conn, err := Connect(t.Context(), dataDir, WithRestore(true), checkRarely)
	require.NoError(t, err)
	require.NoError(t, os.RemoveAll(dataDir))

	again, err := Connect(t.Context(), dataDir, WithRestore(true), checkRarely)
	require.NoError(t, err)
	require.Same(t, conn, again, "a second pool on the removed directory")
	require.NoFileExists(t, filepath.Join(dataDir, "gnosh.db"))

	require.NoError(t, Release(dataDir))
	require.NoError(t, Release(dataDir))
	goleak.VerifyNone(t, ignore)
}

// Each new trouble with the database file is logged, though an earlier
// one was: a failed restore after another file stood at the path.
func TestConnect_WarnsOfEachNewTrouble(t *testing.T) {
	warnings := captureWarnings(t)
	dataDir := filepath.Join(t.TempDir(), ".gnosh")
	t.Cleanup(ResetPool)
	_, err := Connect(t.Context(), dataDir, WithDataDirLock(true), WithRestore(true), checkRarely)
	require.NoError(t, err)
	entry := pool[poolKey(dataDir)]

	other := filepath.Join(dataDir, "other.db")
	require.NoError(t, os.WriteFile(other, nil, 0o600))
	require.NoError(t, os.Rename(other, filepath.Join(dataDir, "gnosh.db")))
	entry.checkFile(t.Context())
	entry.checkFile(t.Context())
	require.Equal(t, 1, strings.Count(warnings(), "Another file is at the database path"), warnings())

	require.NoError(t, os.RemoveAll(dataDir))
	require.NoError(t, os.Mkdir(dataDir, 0o700))
	release, err := lock.TryFile(filepath.Join(dataDir, "gnosh.lock"))
	require.NoError(t, err)
	defer release()
	entry.checkFile(t.Context())
	require.Contains(t, warnings(), "Failed to restore the removed database")
}

// captureWarnings logs warnings to a buffer for the rest of the test, and
// returns what was logged so far.
func captureWarnings(t *testing.T) func() string {
	t.Helper()
	var buf logBuffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelWarn})))
	t.Cleanup(func() { slog.SetDefault(prev) })
	return buf.String
}

// logBuffer is written by a pool's check while the test reads it.
type logBuffer struct {
	mu  sync.Mutex
	buf strings.Builder
}

func (b *logBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *logBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// checkOften has a restoring pool check its file every few milliseconds.
// A test that uses it creates its data directory before it registers
// ResetPool, so the check stops before t.TempDir's folder is removed.
var checkOften = withFileCheckInterval(10 * time.Millisecond)

// checkRarely keeps a restoring pool's check from running during a test.
var checkRarely = withFileCheckInterval(time.Hour)

// sessionIDsIn reads the database file at dbPath on a connection of its
// own, not the pool's.
func sessionIDsIn(ctx context.Context, dbPath string) ([]string, error) {
	conn, err := ConnectReadOnly(ctx, dbPath)
	if err != nil {
		return nil, err
	}
	defer conn.Close()
	sessions, err := New(conn).ListSessions(ctx)
	if err != nil {
		return nil, err
	}
	ids := make([]string, 0, len(sessions))
	for _, s := range sessions {
		ids = append(ids, s.ID)
	}
	return ids, nil
}

// A new data directory gets gnosh.db and gnosh.lock; one that already
// holds the crush.db from before the rename keeps it, and locks
// crush.lock as an older process sharing it does, so the two cannot open
// it at once.
func TestDataFileNames(t *testing.T) {
	t.Cleanup(ResetPool)

	fresh := t.TempDir()
	conn, err := Connect(context.Background(), fresh)
	require.NoError(t, err)
	require.NoError(t, conn.PingContext(context.Background()))
	require.FileExists(t, filepath.Join(fresh, "gnosh.db"))
	require.NoFileExists(t, filepath.Join(fresh, "crush.db"))
	require.Equal(t, filepath.Join(fresh, "gnosh.lock"), dataDirLockFile(fresh))
	require.NoError(t, Release(fresh))

	// An existing database from before the rename is opened, not
	// replaced by a new, empty gnosh.db beside it.
	old := t.TempDir()
	require.NoError(t, os.Rename(filepath.Join(fresh, "gnosh.db"), filepath.Join(old, "crush.db")))
	conn, err = Connect(context.Background(), old)
	require.NoError(t, err)
	require.NoError(t, conn.PingContext(context.Background()))
	require.NoFileExists(t, filepath.Join(old, "gnosh.db"))
	require.Equal(t, filepath.Join(old, "crush.db"), FileName(old))
	require.Equal(t, filepath.Join(old, "crush.lock"), dataDirLockFile(old))
	require.NoError(t, Release(old))
}

// A data directory's .gitignore keeps git off everything in it; one
// already there is left as it is.
func TestWriteGitIgnore(t *testing.T) {
	cases := map[string]struct {
		existing string
		want     string
	}{
		"none yet":      {want: "*\n"},
		"already there": {existing: "gnosh.db\n", want: "gnosh.db\n"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			dataDir := t.TempDir()
			path := filepath.Join(dataDir, ".gitignore")
			if tc.existing != "" {
				require.NoError(t, os.WriteFile(path, []byte(tc.existing), 0o644))
			}

			require.NoError(t, WriteGitIgnore(dataDir))
			got, err := os.ReadFile(path)
			require.NoError(t, err)
			require.Equal(t, tc.want, string(got))
		})
	}
}
