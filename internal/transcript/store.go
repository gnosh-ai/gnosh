package transcript

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"
	"unicode/utf8"
)

// ErrNotFound is the error, wrapped, when no session matches an id.
var ErrNotFound = errors.New("no such session")

// ErrReadOnly is the error, wrapped, when an entry is appended to a session
// that was opened read-only.
var ErrReadOnly = errors.New("the session is open read-only")

// ErrClosed is the error, wrapped, when an entry is appended to a session
// that has been closed.
var ErrClosed = errors.New("the session is closed")

// LockedError says a session cannot be opened for writing because another
// gnosh process (or another open of it in this one) is writing it. The
// caller can open it read-only instead, or branch it.
type LockedError struct {
	ID   string
	Path string
}

func (e *LockedError) Error() string {
	return fmt.Sprintf("transcript: session %s is open for writing in another gnosh; open it read-only or branch it", e.ID)
}

// AmbiguousError says the start of an id fits more than one session.
type AmbiguousError struct {
	Prefix string
	IDs    []string
}

func (e *AmbiguousError) Error() string {
	return fmt.Sprintf("transcript: %q is the start of %d session ids (%s): give more of it", e.Prefix, len(e.IDs), strings.Join(e.IDs, ", "))
}

// Store is the directory gnosh keeps its sessions in. A session is one
// file, Dir/projects/<slug>/<session-id>.jsonl, where <slug> is Slug of the
// working directory the session was started in. A Store must not be copied
// once it is in use.
type Store struct {
	// Dir is gnosh's data directory (see DataDir).
	Dir string
	// Clock tells the time. Nil means time.Now.
	Clock func() time.Time
	// Rand supplies the random part of ids. Nil means crypto/rand.
	Rand io.Reader

	ids idGen
}

// New returns a Store over the data directory dir, using the real clock.
func New(dir string) *Store {
	return &Store{Dir: dir}
}

func (st *Store) now() time.Time {
	if st.Clock != nil {
		return st.Clock()
	}
	return time.Now()
}

func (st *Store) newID() (string, error) {
	return st.ids.next(st.now(), st.Rand)
}

// NewID makes an id for a session that is to be made later, by a process
// of its own given it (gnosh --bg, as --session-id).
func (st *Store) NewID() (string, error) { return st.newID() }

// projects is the directory that holds one directory per working directory.
func (st *Store) projects() string {
	return filepath.Join(st.Dir, "projects")
}

// ProjectDir returns the directory that holds the sessions started in cwd.
func (st *Store) ProjectDir(cwd string) string {
	return filepath.Join(st.projects(), Slug(cwd))
}

// Holds reports whether path is a session file of the store's own: under
// its folder of projects, with links followed on both sides. A file
// elsewhere (one -r was given by its path) was not written by this store,
// and is copied in before it is gone on with (Import).
func (st *Store) Holds(path string) bool {
	real, err := filepath.EvalSymlinks(path)
	if err != nil {
		return false
	}
	root, err := filepath.EvalSymlinks(st.projects())
	if err != nil {
		return false
	}
	rel, err := filepath.Rel(root, real)
	return err == nil && rel != "." && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

// Import copies the entries of src, a session file that is not the
// store's own, into a new session of the store for the folder cwd: src is
// never written, and nothing beside it is used. clean is given each entry,
// inherited ones first, with the fields this version does not know taken
// out, and returns the entry to copy, or false to leave it out. The copy
// holds every entry itself: it does not point back at src.
func (st *Store) Import(src *Session, cwd string, clean func(Entry) (Entry, bool)) (*Session, error) {
	var out []Entry
	for _, e := range src.Entries() {
		e.Extra, e.kept = nil, nil
		if e, ok := clean(e); ok {
			out = append(out, e)
		}
	}
	s, err := st.Create(cwd)
	if err != nil {
		return nil, err
	}
	if len(out) > 0 {
		if _, err := s.AppendAll(out...); err != nil {
			_ = s.Close()
			return nil, fmt.Errorf("transcript: copying %s into the store: %w", src.Path(), err)
		}
	}
	return s, nil
}

// ProjectDirs are the directories that hold sessions: one for each
// working directory a session was started in.
func (st *Store) ProjectDirs() []string {
	dirs, _ := os.ReadDir(st.projects())
	var out []string
	for _, d := range dirs {
		if d.IsDir() {
			out = append(out, filepath.Join(st.projects(), d.Name()))
		}
	}
	return out
}

// Create starts a new session in the working directory cwd, which must be
// an absolute path. Nothing is written until the first Append, so a session
// that is never used leaves no file.
func (st *Store) Create(cwd string) (*Session, error) {
	id, err := st.newID()
	if err != nil {
		return nil, err
	}
	return st.create(cwd, id)
}

// CreateWithID is Create for a session whose id the caller chose. The id
// must be a UUID in lower case that no session has.
func (st *Store) CreateWithID(cwd, id string) (*Session, error) {
	if !validID(id) {
		return nil, fmt.Errorf("transcript: %q is not a session id: it must be a UUID in lower case", id)
	}
	if _, _, err := st.find(id); err == nil {
		return nil, fmt.Errorf("transcript: session %s exists already", id)
	} else if !errors.Is(err, ErrNotFound) {
		return nil, err
	}
	return st.create(cwd, id)
}

func (st *Store) create(cwd, id string) (*Session, error) {
	if !filepath.IsAbs(cwd) {
		return nil, fmt.Errorf("transcript: a session's working directory must be an absolute path, not %q", cwd)
	}
	cwd = filepath.Clean(cwd)
	return &Session{
		st:    st,
		id:    id,
		path:  filepath.Join(st.ProjectDir(cwd), id+".jsonl"),
		start: cwd,
		cwd:   cwd,
	}, nil
}

// find returns the file and full id of the one session whose id is, or
// starts with, id.
func (st *Store) find(id string) (path, full string, err error) {
	for i := 0; i < len(id); i++ {
		if c := id[i]; !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f' || c == '-') {
			return "", "", fmt.Errorf("transcript: %q is not a session id or the start of one: %w", id, ErrNotFound)
		}
	}
	if id == "" {
		return "", "", fmt.Errorf("transcript: no session id was given: %w", ErrNotFound)
	}
	dirs, err := os.ReadDir(st.projects())
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return "", "", fmt.Errorf("transcript: looking for session %s: %w", id, err)
	}
	var paths, ids []string
	for _, d := range dirs {
		if !d.IsDir() {
			continue
		}
		dir := filepath.Join(st.projects(), d.Name())
		if validID(id) {
			p := filepath.Join(dir, id+".jsonl")
			if fi, err := os.Stat(p); err == nil && fi.Mode().IsRegular() {
				paths, ids = append(paths, p), append(ids, id)
			}
			continue
		}
		files, err := os.ReadDir(dir)
		if err != nil {
			continue
		}
		for _, f := range files {
			name, ok := strings.CutSuffix(f.Name(), ".jsonl")
			if ok && validID(name) && strings.HasPrefix(name, id) && f.Type().IsRegular() {
				paths, ids = append(paths, filepath.Join(dir, f.Name())), append(ids, name)
			}
		}
	}
	if len(paths) == 0 {
		return "", "", fmt.Errorf("transcript: no session matches %q under %s: %w", id, st.projects(), ErrNotFound)
	}
	// One id may be found twice, when a session file was copied into
	// another project's directory: the first, by directory name, is taken.
	first, firstID := paths[0], ids[0]
	slices.Sort(ids)
	if ids = slices.Compact(ids); len(ids) > 1 {
		return "", "", &AmbiguousError{Prefix: id, IDs: ids}
	}
	return first, firstID, nil
}

// Path returns the file of the session whose id is, or starts with, id.
func (st *Store) Path(id string) (string, error) {
	path, _, err := st.find(id)
	return path, err
}

// Open opens the session whose id is, or starts with, id, for reading and
// writing. Only one writer may have a session open: when another has it,
// the error is a *LockedError. What was wrong with the file but could be
// got past is in the session's Warnings.
func (st *Store) Open(ctx context.Context, id string) (*Session, error) {
	path, full, err := st.find(id)
	if err != nil {
		return nil, err
	}
	return st.open(ctx, path, full, true)
}

// OpenReadOnly opens the session whose id is, or starts with, id, for
// reading only. It takes no lock, so it works while another gnosh writes
// the session; it shows the session as it was when it was opened.
func (st *Store) OpenReadOnly(ctx context.Context, id string) (*Session, error) {
	path, full, err := st.find(id)
	if err != nil {
		return nil, err
	}
	return st.open(ctx, path, full, false)
}

// OpenPath opens the session file at path, wherever it is, read-only or
// for writing.
func (st *Store) OpenPath(ctx context.Context, path string, readOnly bool) (*Session, error) {
	id := strings.TrimSuffix(filepath.Base(path), ".jsonl")
	return st.open(ctx, path, id, !readOnly)
}

func (st *Store) open(ctx context.Context, path, id string, write bool) (*Session, error) {
	flag := os.O_RDONLY
	if write {
		flag = os.O_RDWR | os.O_APPEND
	}
	var f *os.File
	for try := 0; ; try++ {
		var err error
		f, err = os.OpenFile(path, flag, 0)
		if err != nil {
			if errors.Is(err, fs.ErrNotExist) {
				return nil, fmt.Errorf("transcript: opening session %s: %w: %w", id, ErrNotFound, err)
			}
			return nil, fmt.Errorf("transcript: opening session %s: %w", id, err)
		}
		if !write {
			break
		}
		ok, err := lockFile(f)
		if err != nil {
			f.Close()
			return nil, fmt.Errorf("transcript: locking session %s: %w", id, err)
		}
		if !ok {
			f.Close()
			return nil, &LockedError{ID: id, Path: path}
		}
		// A redaction puts a new file in the old one's place (redact.go):
		// one that did so between the open and the lock leaves the lock on
		// a file that is no longer there, which may hold what was redacted
		// and loses what is written to it. The one there now is opened.
		held, herr := f.Stat()
		still, serr := os.Stat(path)
		if herr != nil || serr != nil || os.SameFile(held, still) || try >= 4 {
			break
		}
		f.Close()
	}
	ld, err := load(ctx, f, path)
	if err != nil {
		f.Close()
		return nil, err
	}
	s := &Session{st: st, id: id, path: path, readOnly: !write, open: ld.open, warnings: ld.warnings}
	own := ld.entries
	if len(own) > 0 {
		s.start = own[0].Cwd
		s.cwd = own[len(own)-1].Cwd
		s.last = own[len(own)-1].UUID
		if first := &own[0]; first.Type == TypeBranch && first.FromSession != "" {
			seen := map[string]bool{id: true}
			prefix, warns, err := st.inherit(ctx, filepath.Dir(path), first.FromSession, first.FromUUID, seen)
			if err != nil {
				f.Close()
				return nil, err
			}
			s.warnings = append(s.warnings, warns...)
			s.inherited = len(prefix)
			own = append(prefix, own...)
		}
	}
	if s.cwd == "" {
		s.cwd = s.start
	}
	s.entries = own
	if write {
		s.f = f
	} else {
		f.Close()
	}
	return s, nil
}

// inherit returns the entries a branch starts with: those of session id up
// to and including the entry uuid, and before them whatever that session
// itself inherited. dir is where the branch's own file is, which is where
// its parent is looked for first. When the parent cannot be read the
// result is empty and a warning says why.
func (st *Store) inherit(ctx context.Context, dir, id, uuid string, seen map[string]bool) ([]Entry, []Warning, error) {
	fail := func(detail string) ([]Entry, []Warning, error) {
		return nil, []Warning{{Kind: WarnBranch, Detail: "this session was branched from session " + id + ", " + detail + "; it holds only what was written after the branch"}}, nil
	}
	if !validID(id) {
		return fail("which is not a session id")
	}
	if seen[id] {
		return fail("which was itself branched from this one")
	}
	seen[id] = true
	path := filepath.Join(dir, id+".jsonl")
	if _, err := os.Stat(path); err != nil {
		p, _, err := st.find(id)
		if err != nil {
			return fail("which was not found")
		}
		path = p
	}
	var got []Entry
	var warns []Warning
	found := false
	for e, err := range ReadFile(ctx, path, func(w Warning) { warns = append(warns, w) }) {
		if err != nil {
			if ctx.Err() != nil {
				return nil, nil, err
			}
			return fail("which could not be read (" + err.Error() + ")")
		}
		got = append(got, e)
		if e.UUID == uuid {
			found = true
			break
		}
	}
	if !found {
		return fail("at an entry that session does not hold")
	}
	if first := &got[0]; first.Type == TypeBranch && first.FromSession != "" {
		prefix, more, err := st.inherit(ctx, filepath.Dir(path), first.FromSession, first.FromUUID, seen)
		if err != nil {
			return nil, nil, err
		}
		warns = append(warns, more...)
		got = append(prefix, got...)
	}
	return got, warns, nil
}

// Branch starts a new session that continues from the entry atUUID of
// from, or from its last entry when atUUID is empty. The new session's
// file begins with a branch entry that points back at that entry; its
// Entries begin with from's entries up to it, so its view begins with the
// old conversation. from is left as it was and may be read-only.
func (st *Store) Branch(from *Session, atUUID string) (*Session, error) {
	entries := from.Entries()
	at := len(entries) - 1
	if atUUID != "" {
		at = slices.IndexFunc(entries, func(e Entry) bool { return e.UUID == atUUID })
		if at < 0 {
			return nil, fmt.Errorf("transcript: cannot branch session %s at %s: it holds no such entry", from.id, atUUID)
		}
	}
	if at < 0 {
		return nil, fmt.Errorf("transcript: cannot branch session %s: it has no entries yet", from.id)
	}
	if entries[at].UUID == "" {
		return nil, fmt.Errorf("transcript: cannot branch session %s at an entry that has no uuid", from.id)
	}
	id, err := st.newID()
	if err != nil {
		return nil, err
	}
	src := entries[at].SessionID
	if src == "" {
		src = from.id
	}
	// The branch is one of the sessions of the folder the session is
	// listed with: the one it was started in, or the one /cd last moved it
	// to. (Not the folder its shell was in at that entry: a branch stamped
	// with a folder below the project's would be listed nowhere.)
	start := from.start
	if start == "" {
		start = entries[at].Cwd
	}
	for _, e := range entries {
		if e.Type == TypeMeta && e.Key == MovedKey && filepath.IsAbs(e.Value) {
			start = filepath.Clean(e.Value)
		}
	}
	s := &Session{
		st:        st,
		id:        id,
		path:      filepath.Join(filepath.Dir(from.path), id+".jsonl"),
		start:     start,
		cwd:       cmp.Or(entries[at].Cwd, start),
		entries:   entries[: at+1 : at+1],
		inherited: at + 1,
	}
	if filepath.IsAbs(start) {
		s.path = filepath.Join(st.ProjectDir(start), id+".jsonl")
	}
	if _, err := s.Append(Entry{Type: TypeBranch, FromSession: src, FromUUID: entries[at].UUID, Cwd: start}); err != nil {
		return nil, err
	}
	return s, nil
}

// Info describes a session without loading it.
type Info struct {
	// ID is the session's id and Path its file.
	ID   string
	Path string
	// Cwd is the working directory the session was started in, or the
	// one /cd last moved it to (Session.Move).
	Cwd string
	// Title is the session's name: the last one the person gave, or else
	// the last generated one, or empty.
	Title string
	// FirstPrompt is the first line of the first prompt the person typed
	// in this session, cut to 256 characters.
	FirstPrompt string
	// Created is the time of the first entry, and Modified of the last
	// prompt, reply or summary, or else of the last entry: a name given to
	// an old session does not make it the newest, which -c goes on with.
	Created  time.Time
	Modified time.Time
	// Entries counts the file's entries, Messages its user and assistant
	// entries, and Prompts the prompts the person typed.
	Entries  int
	Messages int
	Prompts  int
	// Size is the file's size in bytes.
	Size int64
	// BranchedFrom is the id of the session this one continues from, or
	// empty. The counts and the first prompt of a branch are its own.
	BranchedFrom string
}

// List describes the sessions that were started in the working directory
// cwd, or moved there, the one written to most recently first. Sessions
// that hold no entries are left out.
func (st *Store) List(ctx context.Context, cwd string) ([]Info, error) {
	cwd = filepath.Clean(cwd)
	dir := st.ProjectDir(cwd)
	files, err := os.ReadDir(dir)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("transcript: listing the sessions of %s: %w", cwd, err)
	}
	var out []Info
	for _, f := range files {
		id, ok := strings.CutSuffix(f.Name(), ".jsonl")
		if !ok || !validID(id) || !f.Type().IsRegular() {
			continue
		}
		info, err := scanInfo(ctx, filepath.Join(dir, f.Name()), id)
		if err != nil {
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
			continue
		}
		// A session of another folder whose name comes to the same
		// directory is that folder's. One whose folder belongs in another
		// directory is here because a move (/cd) was cut short between
		// putting its file here and writing down the folder: it is this
		// folder's, or it would be listed nowhere.
		if info.Entries == 0 || info.Cwd != cwd && st.ProjectDir(info.Cwd) == dir {
			continue
		}
		out = append(out, info)
	}
	slices.SortFunc(out, func(a, b Info) int {
		if c := b.Modified.Compare(a.Modified); c != 0 {
			return c
		}
		return cmp.Compare(b.ID, a.ID)
	})
	return out, nil
}

// Latest describes the session started in cwd that was written to most
// recently. It reports false when there is none.
func (st *Store) Latest(ctx context.Context, cwd string) (Info, bool, error) {
	list, err := st.List(ctx, cwd)
	if err != nil || len(list) == 0 {
		return Info{}, false, err
	}
	return list[0], true, nil
}

// liteEntry is the few fields scanInfo needs from each line.
type liteEntry struct {
	Type        EntryType `json:"type"`
	Timestamp   string    `json:"timestamp"`
	Cwd         string    `json:"cwd"`
	Source      Source    `json:"source"`
	Title       string    `json:"title"`
	FromSession string    `json:"fromSession"`
	Key         string    `json:"key"`
	Value       string    `json:"value"`
}

// scanInfo reads the session file at path once and describes it. Lines
// that cannot be read are passed over.
func scanInfo(ctx context.Context, path, id string) (Info, error) {
	info := Info{ID: id, Path: path}
	f, err := os.Open(path)
	if err != nil {
		return info, err
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		return info, err
	}
	info.Size = fi.Size()
	s := newScanner(f, path, nil)
	var autoTitle string
	// last is the time of the last entry, talked of the last prompt,
	// reply or summary.
	var last, talked time.Time
	for {
		if err := ctx.Err(); err != nil {
			return info, err
		}
		line, _, err := s.lr.read()
		if err == io.EOF {
			break
		}
		if err != nil {
			return info, err
		}
		var le liteEntry
		if err := json.Unmarshal(line, &le); err != nil {
			// A field of the wrong shape leaves the rest of the line read:
			// such a line is still an entry, as it is for load. Anything
			// else is not an entry.
			var shape *json.UnmarshalTypeError
			if !errors.As(err, &shape) || shape.Field == "" || shape.Field == "type" {
				continue
			}
		}
		info.Entries++
		t, terr := time.Parse(time.RFC3339Nano, le.Timestamp)
		if terr == nil {
			last = t.UTC()
			switch le.Type {
			case TypeUser, TypeAssistant, TypeSummary:
				talked = last
			}
		}
		if le.Type == TypeMeta && le.Key == MovedKey && filepath.IsAbs(le.Value) {
			info.Cwd = filepath.Clean(le.Value)
		}
		if info.Entries == 1 {
			info.Cwd = le.Cwd
			info.Created = last
			if le.Type == TypeBranch {
				info.BranchedFrom = le.FromSession
			}
		}
		switch le.Type {
		case TypeUser, TypeAssistant:
			info.Messages++
			if le.Type == TypeUser && le.Source == SourcePrompt {
				info.Prompts++
				if info.Prompts == 1 {
					var e Entry
					if e.UnmarshalJSON(line) == nil {
						info.FirstPrompt = firstLine(e.Message.Text())
					}
				}
			}
		case TypeTitle:
			if le.Title == "" {
				break
			}
			if le.Source == SourceUser {
				info.Title = le.Title
			} else {
				autoTitle = le.Title
			}
		}
	}
	if info.Title == "" {
		info.Title = autoTitle
	}
	info.Modified = cmp.Or(talked, last)
	if info.Created.IsZero() {
		if t, ok := idTime(id); ok {
			info.Created = t
		} else {
			info.Created = fi.ModTime().UTC()
		}
	}
	if info.Modified.IsZero() {
		info.Modified = fi.ModTime().UTC()
	}
	return info, nil
}

// firstLine returns the first line of s that is not blank, without the
// space around it, cut to 256 characters.
func firstLine(s string) string {
	for line := range strings.Lines(s) {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		if utf8.RuneCountInString(line) > 256 {
			n := 0
			for i := range line {
				if n == 256 {
					return line[:i]
				}
				n++
			}
		}
		return line
	}
	return ""
}
