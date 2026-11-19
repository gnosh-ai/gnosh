package transcript

import (
	"bytes"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"sync"

	"github.com/gnosh-ai/gnosh/internal/core"
)

// Session is one conversation: a file of entries that only grows. Its
// methods may be called from several goroutines.
type Session struct {
	st    *Store
	id    string
	path  string
	start string // the working directory the session was started in

	mu        sync.Mutex
	f         *os.File // nil until the first entry is written, and when read-only
	readOnly  bool
	closed    bool
	entries   []Entry // what a branch inherited, then the file's own entries
	inherited int     // how many of entries came from the session branched from
	last      string  // the UUID of the file's last entry
	cwd       string  // the working directory stamped on new entries
	open      bool    // the file does not end in a line break
	warnings  []Warning
}

// ID returns the session's id.
func (s *Session) ID() string { return s.id }

// Path returns the session's file. A new session's file does not exist
// until its first entry is written.
func (s *Session) Path() string { return s.path }

// ReadOnly reports whether the session was opened for reading only.
func (s *Session) ReadOnly() bool { return s.readOnly }

// StartCwd returns the working directory the session was started in.
func (s *Session) StartCwd() string { return s.start }

// Cwd returns the working directory that new entries are stamped with.
func (s *Session) Cwd() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.cwd
}

// SetCwd changes the working directory that new entries are stamped with.
// It writes nothing; the session's file stays where it is.
func (s *Session) SetCwd(dir string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.cwd = dir
}

// MovedKey is the key of the meta entry Move writes; its value is the
// folder the session was moved to.
const MovedKey = "moved"

// Move puts the session in another working directory, as /cd does: its
// file goes under the project directory of cwd, an absolute path, and a
// meta entry (MovedKey) records the folder. From then on the session is
// listed with that folder's sessions (List), and new entries are stamped
// with it. A session with no file yet only takes the folder. A file of the
// session's id already there is an error, as is a read-only session; when
// the entry cannot be written the file is put back.
func (s *Session) Move(cwd string) error {
	if !filepath.IsAbs(cwd) {
		return fmt.Errorf("transcript: a session's working directory must be an absolute path, not %q", cwd)
	}
	cwd = filepath.Clean(cwd)
	s.mu.Lock()
	switch {
	case s.readOnly:
		s.mu.Unlock()
		return fmt.Errorf("transcript: moving session %s: %w", s.id, ErrReadOnly)
	case s.closed:
		s.mu.Unlock()
		return fmt.Errorf("transcript: moving session %s: %w", s.id, ErrClosed)
	}
	from, oldCwd := s.path, s.cwd
	to := filepath.Join(s.st.ProjectDir(cwd), s.id+".jsonl")
	if s.f == nil {
		// Nothing is written yet: the file will be made where it belongs.
		s.path, s.start, s.cwd = to, cwd, cwd
		s.mu.Unlock()
		return nil
	}
	if to != from {
		if _, err := os.Lstat(to); err == nil {
			s.mu.Unlock()
			return fmt.Errorf("transcript: moving session %s: %s is there already", s.id, to)
		}
		if err := os.MkdirAll(filepath.Dir(to), 0o700); err != nil {
			s.mu.Unlock()
			return fmt.Errorf("transcript: moving session %s: %w", s.id, err)
		}
		if err := os.Rename(from, to); err != nil {
			s.mu.Unlock()
			return fmt.Errorf("transcript: moving session %s: %w", s.id, err)
		}
	}
	// (The open file goes on being written where it now is: a rename
	// keeps it, and keeps its writer's lock.)
	s.path, s.cwd = to, cwd
	s.mu.Unlock()
	if _, err := s.Append(MetaEntry(MovedKey, cwd)); err != nil {
		s.mu.Lock()
		if to == from || os.Rename(to, from) == nil {
			s.path, s.cwd = from, oldCwd
		}
		s.mu.Unlock()
		return err
	}
	return nil
}

// Warnings returns what was wrong with the session's file when it was
// opened but could be got past: a torn last line, corrupt lines, a branch
// whose parent is missing.
func (s *Session) Warnings() []Warning {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Clone(s.warnings)
}

// Entries returns the session's entries as they are now: for a branch,
// first the ones it inherited. The result does not change when more are
// appended. It shares memory with the session, so it must not be modified.
func (s *Session) Entries() []Entry {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.entries[:len(s.entries):len(s.entries)]
}

// Inherited returns how many of the session's first entries came from the
// session it was branched from.
func (s *Session) Inherited() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.inherited
}

// View computes what the model is sent from the session's entries. The
// stack is the session's own unless opts.Session names another.
func (s *Session) View(opts ViewOptions) ([]core.Message, ViewInfo) {
	if opts.Session == "" {
		opts.Session = s.id
	}
	return View(s.Entries(), opts)
}

// Stack returns the frames the session stands in, bottom to top.
func (s *Session) Stack() []Frame {
	return Stack(s.Entries(), s.id)
}

// Preview says what the frame entries among next would do if they were
// appended now, as the package's Preview does, for the session's own
// stack. Of opts, Estimate is used.
func (s *Session) Preview(next []Entry, opts ViewOptions) ([]Cut, []Frame) {
	opts.Session = s.id
	return Preview(s.Entries(), next, opts)
}

// Append writes e as the session's next entry and returns it as written.
// It sets ParentUUID and SessionID, and fills UUID, Timestamp, Cwd and V
// when they are empty. The entry reaches the file in one write call. The
// entry's message and maps must not be changed afterwards.
func (s *Session) Append(e Entry) (Entry, error) {
	out, err := s.AppendAll(e)
	if err != nil {
		return Entry{}, err
	}
	return out[0], nil
}

// AppendAll writes entries as the session's next entries, in order, and
// returns them as written. They reach the file together, in one write
// call: when it fails, none of them is part of the session. Each is
// completed as Append completes one.
func (s *Session) AppendAll(entries ...Entry) ([]Entry, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.readOnly {
		return nil, fmt.Errorf("transcript: appending to session %s: %w", s.id, ErrReadOnly)
	}
	if s.closed {
		return nil, fmt.Errorf("transcript: appending to session %s: %w", s.id, ErrClosed)
	}
	if len(entries) == 0 {
		return nil, nil
	}
	out := make([]Entry, len(entries))
	var buf []byte
	if s.open {
		// The file ends in a torn line: start on a line of its own.
		buf = append(buf, '\n')
	}
	last := s.last
	for i, e := range entries {
		if e.UUID == "" {
			id, err := s.st.newID()
			if err != nil {
				return nil, err
			}
			e.UUID = id
		}
		e.ParentUUID, last = last, e.UUID
		e.SessionID = s.id
		if e.Timestamp.IsZero() {
			e.Timestamp = s.st.now()
		}
		e.Timestamp = e.Timestamp.UTC()
		if e.Cwd == "" {
			e.Cwd = s.cwd
		}
		if e.V == 0 {
			e.V = Version
		}
		e.Extra = maps.Clone(e.Extra)
		line, err := e.line()
		if err != nil {
			return nil, err
		}
		if bytes.IndexByte(line, '\n') >= 0 {
			return nil, fmt.Errorf("transcript: appending to session %s: the entry's JSON holds a line break", s.id)
		}
		buf = append(append(buf, line...), '\n')
		out[i] = e
	}
	if s.f == nil {
		if err := s.createFile(); err != nil {
			return nil, err
		}
	}
	before := int64(-1)
	if fi, err := s.f.Stat(); err == nil {
		before = fi.Size()
	}
	n, err := s.f.Write(buf)
	if err != nil {
		if n > 0 && (before < 0 || s.f.Truncate(before) != nil) {
			// Part of it may be in the file, and could not be taken back
			// off (the whole lines of it would come back as entries the
			// session never had): the next write starts on a line of its
			// own, and a load leaves the torn line out.
			s.open = true
		}
		return nil, fmt.Errorf("transcript: appending to session %s: %w (nothing was recorded)", s.id, err)
	}
	s.open = false
	s.entries = append(s.entries, out...)
	s.last = last
	return out, nil
}

// createFile makes the session's file and takes its writer's lock.
func (s *Session) createFile() error {
	if err := os.MkdirAll(filepath.Dir(s.path), 0o700); err != nil {
		return fmt.Errorf("transcript: making the directory for session %s: %w", s.id, err)
	}
	f, err := os.OpenFile(s.path, os.O_RDWR|os.O_APPEND|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return fmt.Errorf("transcript: creating the file of session %s: %w", s.id, err)
	}
	ok, err := lockFile(f)
	if err != nil || !ok {
		f.Close()
		if err != nil {
			return fmt.Errorf("transcript: locking session %s: %w", s.id, err)
		}
		return &LockedError{ID: s.id, Path: s.path}
	}
	s.f = f
	return nil
}

// Sync asks the system to put what has been appended onto the disk.
func (s *Session) Sync() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.f == nil {
		return nil
	}
	if err := s.f.Sync(); err != nil {
		return fmt.Errorf("transcript: syncing session %s: %w", s.id, err)
	}
	return nil
}

// Close ends writing and gives up the writer's lock. The entries stay
// readable. Closing twice is harmless.
func (s *Session) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.closed = true
	if s.f == nil {
		return nil
	}
	err := s.f.Close()
	s.f = nil
	if err != nil {
		return fmt.Errorf("transcript: closing session %s: %w", s.id, err)
	}
	return nil
}
