// Package filetracker provides functionality to track file reads in sessions.
package filetracker

import (
	"context"
	"crypto/sha256"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"time"

	"github.com/charmbracelet/crush/internal/csync"
	"github.com/charmbracelet/crush/internal/db"
)

// Service defines the interface for tracking file reads in sessions.
type Service interface {
	// RecordRead records when a file was read.
	RecordRead(ctx context.Context, sessionID, path string)

	// LastReadTime returns when a file was last read.
	// Returns zero time if never read.
	LastReadTime(ctx context.Context, sessionID, path string) time.Time

	// UnchangedSinceRead reports whether a file holds what it held when
	// it was last read in the session, whatever its modification time
	// says. False when that is not known: the read was before this
	// process started, or the file is too large to compare.
	UnchangedSinceRead(ctx context.Context, sessionID, path string) bool

	// ListReadFiles returns the paths of all files read in a session.
	ListReadFiles(ctx context.Context, sessionID string) ([]string, error)

	// CopyReads records the reads of session from as reads of session to,
	// at the same times and of the same content, as /branch copies a
	// conversation: the branch has read what its conversation says it
	// read, and can edit those files without reading them again.
	CopyReads(ctx context.Context, from, to string) error

	// ForgetReadsSince forgets the reads of session sessionID made at or
	// after since (Unix seconds), as /rewind forgets those of the
	// conversation it cuts: the agent reads those files again before it
	// changes them.
	ForgetReadsSince(ctx context.Context, sessionID string, since int64) error
}

type service struct {
	q *db.Queries
	// sums holds each file's content hash at its last read in a session.
	sums *csync.Map[readKey, [sha256.Size]byte]
}

type readKey struct{ sessionID, path string }

// maxComparedSize bounds the files whose content is hashed at each read
// to compare later; a larger one is judged by its modification time.
const maxComparedSize = 16 << 20

// NewService creates a new file tracker service.
func NewService(q *db.Queries) Service {
	return &service{q: q, sums: csync.NewMap[readKey, [sha256.Size]byte]()}
}

// RecordRead records when a file was read.
func (s *service) RecordRead(ctx context.Context, sessionID, path string) {
	key := readKey{sessionID, relpath(path)}
	if err := s.q.RecordFileRead(ctx, db.RecordFileReadParams{
		SessionID: sessionID,
		Path:      key.path,
	}); err != nil {
		slog.Error("Error recording file read", "error", err, "file", path)
	}
	if sum, ok := contentSum(path); ok {
		s.sums.Set(key, sum)
	} else {
		s.sums.Del(key)
	}
}

// UnchangedSinceRead reports whether a file holds what it held when it
// was last read in the session. Where this process kept no hash of what
// was read, because the read was in an earlier run of Gnosh (a resumed
// session) or the file is larger than maxComparedSize, the file is
// judged by its modification time, as the edit and write tools judge
// every file: unchanged unless modified in a later second than the read,
// the read time being whole seconds. Without that, every file a resumed
// session had read counted as changed.
func (s *service) UnchangedSinceRead(ctx context.Context, sessionID, path string) bool {
	then, ok := s.sums.Get(readKey{sessionID, relpath(path)})
	if !ok {
		info, err := os.Stat(path)
		if err != nil {
			return false
		}
		// Never read, the read time is zero, and any file is later.
		return !info.ModTime().Truncate(time.Second).After(s.LastReadTime(ctx, sessionID, path))
	}
	now, ok := contentSum(path)
	return ok && now == then
}

// contentSum hashes a file's content, unless it is not a regular file
// that can be read or is larger than maxComparedSize.
func contentSum(path string) ([sha256.Size]byte, bool) {
	info, err := os.Stat(path)
	if err != nil || !info.Mode().IsRegular() || info.Size() > maxComparedSize {
		return [sha256.Size]byte{}, false
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return [sha256.Size]byte{}, false
	}
	return sha256.Sum256(data), true
}

// LastReadTime returns when a file was last read.
// Returns zero time if never read.
func (s *service) LastReadTime(ctx context.Context, sessionID, path string) time.Time {
	readFile, err := s.q.GetFileRead(ctx, db.GetFileReadParams{
		SessionID: sessionID,
		Path:      relpath(path),
	})
	if err != nil {
		return time.Time{}
	}

	return time.Unix(readFile.ReadAt, 0)
}

func relpath(path string) string {
	path = filepath.Clean(path)
	basepath, err := os.Getwd()
	if err != nil {
		slog.Warn("Error getting basepath", "error", err)
		return path
	}
	relpath, err := filepath.Rel(basepath, path)
	if err != nil {
		slog.Warn("Error getting relpath", "error", err)
		return path
	}
	return relpath
}

// CopyReads records the reads of session from as reads of session to.
func (s *service) CopyReads(ctx context.Context, from, to string) error {
	if err := s.q.CopySessionReadFiles(ctx, db.CopySessionReadFilesParams{
		ToSessionID:   to,
		FromSessionID: from,
	}); err != nil {
		return fmt.Errorf("copying file reads: %w", err)
	}
	for key, sum := range s.sums.Seq2() {
		if key.sessionID == from {
			s.sums.Set(readKey{to, key.path}, sum)
		}
	}
	return nil
}

// ForgetReadsSince forgets the reads of a session made since a time.
func (s *service) ForgetReadsSince(ctx context.Context, sessionID string, since int64) error {
	reads, err := s.q.ListSessionReadFiles(ctx, sessionID)
	if err != nil {
		return fmt.Errorf("listing read files: %w", err)
	}
	for _, read := range reads {
		if read.ReadAt >= since {
			s.sums.Del(readKey{sessionID, read.Path})
		}
	}
	if err := s.q.DeleteSessionReadFilesSince(ctx, db.DeleteSessionReadFilesSinceParams{
		SessionID: sessionID,
		ReadAt:    since,
	}); err != nil {
		return fmt.Errorf("forgetting file reads: %w", err)
	}
	return nil
}

// ListReadFiles returns the paths of all files read in a session.
func (s *service) ListReadFiles(ctx context.Context, sessionID string) ([]string, error) {
	readFiles, err := s.q.ListSessionReadFiles(ctx, sessionID)
	if err != nil {
		return nil, fmt.Errorf("listing read files: %w", err)
	}

	basepath, err := os.Getwd()
	if err != nil {
		return nil, fmt.Errorf("getting working directory: %w", err)
	}

	paths := make([]string, 0, len(readFiles))
	for _, rf := range readFiles {
		paths = append(paths, filepath.Join(basepath, rf.Path))
	}
	return paths, nil
}
