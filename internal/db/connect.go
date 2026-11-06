package db

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"embed"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	crushlog "github.com/charmbracelet/crush/internal/log"
	"github.com/pressly/goose/v3"
)

var (
	pragmas = map[string]string{
		"foreign_keys":  "ON",
		"journal_mode":  "WAL",
		"page_size":     "4096",
		"temp_store":    "MEMORY",
		"cache_size":    "-8000",
		"synchronous":   "NORMAL",
		"secure_delete": "ON",
		"busy_timeout":  "30000",
	}
	gooseInitOnce sync.Once
	gooseInitErr  error
)

//go:embed migrations/*.sql
var FS embed.FS

func init() {
	goose.SetBaseFS(FS)

	if testing.Testing() {
		goose.SetLogger(goose.NopLogger())
	}
}

// connEntry holds a shared database connection, its reference count,
// and the data-directory lock that gates access to this entry. The
// lock is acquired when the entry is created, again in a restored data
// directory, and released when the last reference is dropped, which lets
// the same process open the same data directory concurrently while still
// blocking a second crush process from racing the storage.
type connEntry struct {
	db       *sql.DB
	refCount int
	// watchFile owns lock, file and warned, and may replace lock, until
	// stopWatch returns.
	lock *dataDirLock
	// path is the database file and file the one the pool has open there;
	// they and stopWatch are set only WithRestore.
	path string
	file os.FileInfo
	// warned is the last warning logged, so a new one is logged once.
	warned       string
	stopWatch    func()
	restoredHook func()
}

var (
	pool   = make(map[string]*connEntry)
	poolMu sync.Mutex
)

// dbFileCheckInterval is how often a pooled database checks that its file
// is still at its path.
const dbFileCheckInterval = 2 * time.Second

// ConnectOption configures a Connect call. Options are applied in
// order; later options override earlier ones for the same field.
type ConnectOption func(*connectOptions)

// connectOptions holds the resolved configuration for a Connect call.
type connectOptions struct {
	lockDataDir  bool
	restore      bool
	checkEvery   time.Duration
	restoredHook func()
}

// WithDataDirLock toggles acquisition of the per-data-directory lock
// for this Connect call. The lock is off by default so local-mode
// invocations do not regress today's behavior; the server's
// workspace-bootstrap path opts in. GNOSH_SKIP_DATADIR_LOCK still
// bypasses acquisition even when this option is set.
func WithDataDirLock(enable bool) ConnectOption {
	return func(o *connectOptions) { o.lockDataDir = enable }
}

// WithRestore toggles the check that writes the database back to its path
// when its file is removed while open, as by a model deleting .gnosh/. A
// user's removal is undone too: erasing the data directory takes
// releasing the database first. It is off by default. As with the lock,
// the Connect that opens the database decides.
func WithRestore(enable bool) ConnectOption {
	return func(o *connectOptions) { o.restore = enable }
}

// withFileCheckInterval sets how often a restoring pool checks its file.
func withFileCheckInterval(every time.Duration) ConnectOption {
	return func(o *connectOptions) { o.checkEvery = every }
}

// withRestoredHook has a restoring pool call fn each time a restore has put
// the database back at its path, before the pool holds the restored file.
func withRestoredHook(fn func()) ConnectOption {
	return func(o *connectOptions) { o.restoredHook = fn }
}

// Connect opens a SQLite database connection for the given data
// directory and runs migrations. If a connection to the same database
// file already exists, the existing connection is returned with its
// reference count incremented. Callers must pair each Connect with a
// [Release] when they no longer need the connection.
func Connect(ctx context.Context, dataDir string, opts ...ConnectOption) (*sql.DB, error) {
	if dataDir == "" {
		return nil, fmt.Errorf("data.dir is not set")
	}

	cfg := connectOptions{checkEvery: dbFileCheckInterval}
	for _, opt := range opts {
		opt(&cfg)
	}

	key := poolKey(dataDir)

	poolMu.Lock()
	defer poolMu.Unlock()

	if entry, ok := pool[key]; ok {
		entry.refCount++
		return entry.db, nil
	}

	// Take the per-data-directory lock before opening the database so
	// we fail fast and with a clear error rather than racing another
	// crush process on the same SQLite file. The lock is released when
	// the matching Release call drops the refcount to zero. Ensuring
	// the data directory exists is required because the lock file
	// lives inside it. Locking is opt-in via WithDataDirLock so that
	// local-mode invocations do not refuse a second crush against the
	// same data dir until client/server becomes the default.
	if err := os.MkdirAll(dataDir, 0o700); err != nil {
		return nil, fmt.Errorf("failed to create data directory %q: %w", dataDir, err)
	}
	var lock *dataDirLock
	if cfg.lockDataDir && !skipDataDirLock() {
		var err error
		lock, err = acquireDataDirLock(dataDir)
		if err != nil {
			return nil, err
		}
	}

	dbPath := FileName(dataDir)
	// A restoring pool never creates its file, made here for a new
	// database: a connection that did, opened while the file is removed,
	// would leave an empty database the check takes for another file.
	if cfg.restore {
		f, err := os.OpenFile(dbPath, os.O_RDONLY|os.O_CREATE, 0o644)
		if err == nil {
			err = f.Close()
		}
		if err != nil {
			if lock != nil {
				lock.release()
			}
			return nil, fmt.Errorf("failed to create database: %w", err)
		}
	}
	conn, err := openDB(dbPath, cfg.restore)
	if err != nil {
		if lock != nil {
			lock.release()
		}
		return nil, err
	}

	// Serialize all access through a single connection. SQLite
	// serializes writes at the file level anyway, and allowing multiple
	// pool connections to interleave writes/checkpoints (especially
	// under concurrent sub-agents) has caused WAL/header desync
	// resulting in SQLITE_NOTADB (26) on the next open.
	conn.SetMaxOpenConns(1)

	releaseLock := func() {
		if lock != nil {
			lock.release()
		}
	}

	if err = conn.PingContext(ctx); err != nil {
		conn.Close()
		releaseLock()
		return nil, fmt.Errorf("failed to connect to database: %w", err)
	}

	if err := initGoose(); err != nil {
		conn.Close()
		releaseLock()
		slog.Error("Failed to initialize goose", "error", err)
		return nil, fmt.Errorf("failed to initialize goose: %w", err)
	}

	if err := goose.Up(conn, "migrations"); err != nil {
		conn.Close()
		releaseLock()
		slog.Error("Failed to apply migrations", "error", err)
		return nil, fmt.Errorf("failed to apply migrations: %w", err)
	}

	entry := &connEntry{db: conn, refCount: 1, lock: lock}
	if cfg.restore {
		info, err := os.Stat(dbPath)
		if err != nil {
			conn.Close()
			releaseLock()
			return nil, fmt.Errorf("failed to stat database: %w", err)
		}
		entry.path, entry.file, entry.restoredHook = dbPath, info, cfg.restoredHook
		watchCtx, cancel := context.WithCancel(context.Background())
		done := make(chan struct{})
		go entry.watchFile(watchCtx, cfg.checkEvery, done)
		entry.stopWatch = func() {
			cancel()
			<-done
		}
	}
	pool[key] = entry
	return conn, nil
}

// poolKey is the pool's key for dataDir: its absolute path, so different
// relative paths share a connection, and a directory whose database
// FileName can no longer name, once removed, is still found.
func poolKey(dataDir string) string {
	abs, err := filepath.Abs(dataDir)
	if err != nil {
		return dataDir
	}
	return abs
}

// watchFile checks the database file every interval until ctx ends, and
// once more then, so a removal since the last check is restored before
// the pool closes the removed file.
func (e *connEntry) watchFile(ctx context.Context, interval time.Duration, done chan<- struct{}) {
	defer close(done)
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			e.checkFile(context.WithoutCancel(ctx))
			return
		case <-ticker.C:
			e.checkFile(ctx)
		}
	}
}

// checkFile restores the database when its file is gone, and leaves a
// different file at its path as it is.
func (e *connEntry) checkFile(ctx context.Context) {
	info, err := os.Stat(e.path)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		e.restore(ctx)
	case err != nil:
		e.warnOnce("Failed to check the database file", "path", e.path, "error", err)
	case !os.SameFile(info, e.file):
		e.warnOnce("Another file is at the database path; leaving it, the open database is no longer on disk", "path", e.path)
	}
}

// restore writes the open database back to its path, then moves the pool,
// the data-directory lock and the log file onto the restored directory.
func (e *connEntry) restore(ctx context.Context) {
	lock, err := e.restoreFile(ctx)
	if err != nil {
		e.warnOnce("Failed to restore the removed database", "path", e.path, "error", err)
		return
	}
	if lock != nil {
		e.lock.release()
		e.lock = lock
	}
	e.warned = ""
	if err := crushlog.ReopenFile(); err != nil {
		slog.Warn("Failed to reopen the removed log file", "error", err)
	}
	slog.Warn("Data directory was removed while in use; restored the database", "path", e.path)
}

// restoreFile writes the open database back to its path. When the pool
// holds a lock, it returns the restored directory's, taken before the
// copy: a gnosh that opens the directory meanwhile finds it locked, and
// one that locked it first keeps it.
func (e *connEntry) restoreFile(ctx context.Context) (*dataDirLock, error) {
	// The pool's one connection: while it is held, no write can land in
	// the removed file after the copy.
	conn, err := e.db.Conn(ctx)
	if err != nil {
		return nil, err
	}
	defer conn.Close()

	dataDir := filepath.Dir(e.path)
	if err := os.Mkdir(dataDir, 0o700); err != nil && !errors.Is(err, fs.ErrExist) {
		return nil, err
	}
	var lock *dataDirLock
	if e.lock != nil {
		lock, err = acquireDataDirLockAt(dataDir, lockFileOf(e.path))
		if err != nil {
			return nil, err
		}
	}
	if err := e.copyFile(ctx, conn, dataDir); err != nil {
		if lock != nil {
			lock.release()
		}
		return nil, err
	}
	return lock, nil
}

// copyFile copies the database open on conn to its path in dataDir, then
// moves the pool onto the copy.
func (e *connEntry) copyFile(ctx context.Context, conn *sql.Conn, dataDir string) error {
	if err := WriteGitIgnore(dataDir); err != nil {
		slog.Warn("Failed to restore the data directory's .gitignore", "error", err)
	}
	// Copied under a new name, then linked: a link, unlike a rename,
	// fails when the path is taken, and a copy cut short never reaches
	// the path.
	tmp, err := os.CreateTemp(dataDir, filepath.Base(e.path)+".restore-*")
	if err != nil {
		return err
	}
	defer func() { _ = os.Remove(tmp.Name()) }()
	if err := tmp.Close(); err != nil {
		return err
	}
	// Not ctx: a Release meanwhile waits for the copy to finish.
	if _, err := conn.ExecContext(context.WithoutCancel(ctx), "VACUUM INTO ?", tmp.Name()); err != nil {
		return err
	}
	if err := os.Link(tmp.Name(), e.path); err != nil {
		return err
	}
	info, err := os.Stat(e.path)
	if err != nil {
		return err
	}
	if e.restoredHook != nil {
		e.restoredHook()
	}
	if err := e.moveOnto(ctx, conn, info); err != nil {
		return err
	}
	e.file = info
	return nil
}

// moveOnto moves the pool from conn, open on the removed file, onto the
// restored file at info. It lets conn go only once another connection has
// read the restored file, since a connection whose first read comes after
// its files are unlinked cannot read them; on an error the pool keeps conn.
func (e *connEntry) moveOnto(ctx context.Context, conn *sql.Conn, info os.FileInfo) error {
	// As for the copy, a Release meanwhile waits for the move to finish.
	ctx = context.WithoutCancel(ctx)
	e.db.SetMaxOpenConns(2)
	restored, err := e.db.Conn(ctx)
	if err != nil {
		// The failed open let a waiting query open a connection; handed
		// back at 1 with that one open, conn would be closed.
		_ = conn.Close()
		e.db.SetMaxOpenConns(1)
		return err
	}
	// Lowered while both are held: at 2, a connection let go would let a
	// waiting query open another, and conn, handed back after it, be closed.
	e.db.SetMaxOpenConns(1)
	err = restored.QueryRowContext(ctx, "SELECT count(*) FROM sqlite_schema").Scan(new(int))
	var now os.FileInfo
	if err == nil {
		now, err = os.Stat(e.path)
	}
	if err == nil && !os.SameFile(now, info) {
		err = errors.New("another file took the restored database's path")
	}
	// database/sql closes a connection that reports ErrBadConn instead of
	// pooling it.
	if err != nil {
		_ = restored.Raw(func(any) error { return driver.ErrBadConn })
		return err
	}
	_ = conn.Raw(func(any) error { return driver.ErrBadConn })
	return restored.Close()
}

func (e *connEntry) warnOnce(msg string, args ...any) {
	if e.warned == msg {
		return
	}
	e.warned = msg
	slog.Warn(msg, args...)
}

// Release decrements the reference count for the database at the given
// data directory. When the count reaches zero the underlying connection
// is closed and removed from the pool.
func Release(dataDir string) error {
	key := poolKey(dataDir)

	poolMu.Lock()
	defer poolMu.Unlock()

	entry, ok := pool[key]
	if !ok {
		return nil
	}

	entry.refCount--
	if entry.refCount > 0 {
		return nil
	}

	delete(pool, key)
	return entry.close()
}

// ResetPool closes all pooled connections and clears the pool. This is
// intended for use in tests to ensure a clean state between test cases.
func ResetPool() {
	poolMu.Lock()
	defer poolMu.Unlock()
	for key, entry := range pool {
		_ = entry.close()
		delete(pool, key)
	}
}

// close stops the check of the database file, which may restore it first,
// then closes the pool and releases the lock.
func (e *connEntry) close() error {
	if e.stopWatch != nil {
		e.stopWatch()
	}
	err := e.db.Close()
	if e.lock != nil {
		e.lock.release()
	}
	return err
}

// ConnectReadOnly opens a read-only SQLite database connection without running
// migrations. Used for aggregating stats across multiple project databases.
func ConnectReadOnly(ctx context.Context, dbPath string) (*sql.DB, error) {
	if dbPath == "" {
		return nil, fmt.Errorf("database path is empty")
	}

	db, err := openDBReadOnly(dbPath)
	if err != nil {
		return nil, err
	}

	db.SetMaxOpenConns(1)

	if err = db.PingContext(ctx); err != nil {
		db.Close()
		return nil, fmt.Errorf("failed to connect to database: %w", err)
	}

	return db, nil
}

func initGoose() error {
	gooseInitOnce.Do(func() {
		goose.SetBaseFS(FS)
		gooseInitErr = goose.SetDialect("sqlite3")
	})

	return gooseInitErr
}

// FileName returns the database file in dataDir: gnosh.db, or the klash.db
// or crush.db from before the renames where only that one exists, so that an
// existing data directory keeps its sessions.
func FileName(dataDir string) string {
	for _, name := range []string{"gnosh.db", "klash.db", "crush.db"} {
		if _, err := os.Stat(filepath.Join(dataDir, name)); err == nil {
			return filepath.Join(dataDir, name)
		}
	}
	return filepath.Join(dataDir, "gnosh.db")
}

// WriteGitIgnore writes the .gitignore that keeps git off everything in
// dataDir, unless one is already there.
func WriteGitIgnore(dataDir string) error {
	path := filepath.Join(dataDir, ".gitignore")
	if _, err := os.Stat(path); !errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err := os.WriteFile(path, []byte("*\n"), 0o644); err != nil {
		return fmt.Errorf("failed to create .gitignore file: %q %w", path, err)
	}
	return nil
}
