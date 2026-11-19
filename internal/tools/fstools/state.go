package fstools

import (
	"context"
	"crypto/sha256"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/gnosh-ai/gnosh/internal/core"
)

// sessionKey is the key under which a session's file state is kept in
// core.ToolEnv.Store.
type sessionKey struct{}

// session is what one session has read and written. It is shared by every
// call of the session, which may run at the same time.
type session struct {
	mu sync.Mutex
	// files is keyed by the real path of a file: links followed.
	files map[string]*fileRecord
	// locks holds, per real path, a one-slot channel that a call fills
	// while it changes the file.
	locks map[string]chan struct{}
	// ticks counts the looks and writes, for each record's order.
	ticks uint64
}

// span is a run of line numbers, first and last included. Lines count from
// one.
type span struct {
	first, last int
}

// fileRecord is what the session knows about one file.
type fileRecord struct {
	// modTime and size are the file's when it was last read or written by
	// the session.
	modTime time.Time
	size    int64
	// sum is the SHA-256 of the whole file at that moment, when the
	// session had all of it in hand; hasSum says whether it did.
	sum    [sha256.Size]byte
	hasSum bool
	// all says that the session has been shown every line of the file as
	// it then was. When it is false, seen lists the lines it was shown, in
	// order, with no two spans touching.
	all  bool
	seen []span
	// lines is how many lines the file has, or -1 when that is not known.
	lines int
	// written says that the session itself has written the file.
	written bool
	// order is when the session last read or wrote the file: the higher,
	// the more recent.
	order uint64
}

// sessionOf returns the file state of the session env belongs to, making
// it on first use.
func sessionOf(env *core.ToolEnv) *session {
	if v, ok := env.Store.Load(sessionKey{}); ok {
		return v.(*session)
	}
	v, _ := env.Store.LoadOrStore(sessionKey{}, &session{
		files: make(map[string]*fileRecord),
		locks: make(map[string]chan struct{}),
	})
	return v.(*session)
}

// record returns a copy of what the session knows about the file at the
// real path, and whether it knows anything.
func (s *session) record(real string) (fileRecord, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	rec, ok := s.files[real]
	if !ok {
		return fileRecord{}, false
	}
	out := *rec
	out.seen = append([]span(nil), rec.seen...)
	return out, true
}

// sighting is one look at a file: what a Read call showed.
type sighting struct {
	info fs.FileInfo
	// sum is the hash of the whole file, or nil when the call did not read
	// all of it.
	sum *[sha256.Size]byte
	// shown is the lines the call showed; the zero span means none.
	shown span
	// lines is the file's line count when the call learnt it, else -1.
	lines int
	// whole says that the call showed all there is to see of the file even
	// though it showed no lines: the file is empty, or a picture, or
	// binary and only described.
	whole bool
}

// saw records a look at a file. Looks at the same state of the file add
// up: paging through a file ends with all of it seen. A look at a file
// that has changed since the last one starts over.
func (s *session) saw(real string, v sighting) {
	s.mu.Lock()
	defer s.mu.Unlock()
	old, known := s.files[real]
	rec := fileRecord{modTime: v.info.ModTime(), size: v.info.Size(), lines: -1}
	if known {
		rec.written = old.written
		same := old.size == rec.size && old.modTime.Equal(rec.modTime)
		if same && old.hasSum && v.sum != nil && old.sum != *v.sum {
			same = false
		}
		if same {
			rec = *old
			rec.seen = append([]span(nil), old.seen...)
		}
	}
	if v.sum != nil {
		rec.sum, rec.hasSum = *v.sum, true
	}
	if v.lines >= 0 {
		rec.lines = v.lines
	}
	if v.whole {
		rec.all = true
	} else if v.shown.first > 0 && !rec.all {
		rec.seen = addSpan(rec.seen, v.shown)
	}
	if !rec.all && rec.lines >= 0 && coversAll(rec.seen, rec.lines) {
		rec.all = true
	}
	if rec.all {
		rec.seen = nil
	}
	if !rec.all && len(rec.seen) == 0 {
		// Nothing of the file as it now is has been shown. Whatever was
		// known is about an older state of it and is no longer true.
		if known {
			delete(s.files, real)
			if rec.written {
				s.files[real] = &fileRecord{modTime: rec.modTime, size: rec.size, lines: rec.lines, written: true, sum: rec.sum, hasSum: rec.hasSum, order: s.tick()}
			}
		}
		return
	}
	rec.order = s.tick()
	s.files[real] = &rec
}

// tick returns the next order for a record. s.mu is held.
func (s *session) tick() uint64 {
	s.ticks++
	return s.ticks
}

// wrote records that the session has just written the file. info and sum
// are those of the new content, lines is how many lines it has, and all
// and seen say which of them the session has seen.
func (s *session) wrote(real string, info fs.FileInfo, sum [sha256.Size]byte, all bool, seen []span, lines int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	rec := &fileRecord{
		modTime: info.ModTime(),
		size:    info.Size(),
		sum:     sum,
		hasSum:  true,
		all:     all,
		lines:   lines,
		written: true,
		order:   s.tick(),
	}
	if !all {
		rec.seen = append([]span(nil), seen...)
		if lines >= 0 && coversAll(rec.seen, lines) {
			rec.all, rec.seen = true, nil
		}
	}
	s.files[real] = rec
}

// lock waits for the turn to change the file at the real path, so that two
// calls on one file run one after the other and neither loses the other's
// change. It returns the function that gives the turn up.
func (s *session) lock(ctx context.Context, real string) (unlock func(), err error) {
	s.mu.Lock()
	ch, ok := s.locks[real]
	if !ok {
		ch = make(chan struct{}, 1)
		s.locks[real] = ch
	}
	s.mu.Unlock()
	if ctx.Err() != nil {
		return nil, context.Cause(ctx)
	}
	select {
	case ch <- struct{}{}:
		return func() { <-ch }, nil
	case <-ctx.Done():
		return nil, context.Cause(ctx)
	}
}

// unchanged reports whether the file on disk is still as rec describes it.
// content, when not nil, is the file's bytes as just read; with them and a
// recorded hash the answer is exact. Otherwise it goes by size and
// modification time.
func (rec *fileRecord) unchanged(info fs.FileInfo, content []byte) bool {
	if rec.hasSum && content != nil {
		return sha256.Sum256(content) == rec.sum
	}
	return info.Size() == rec.size && info.ModTime().Equal(rec.modTime)
}

// sees reports whether every line of the span has been shown.
func (rec *fileRecord) sees(want span) bool {
	if rec.all {
		return true
	}
	for _, sp := range rec.seen {
		if sp.first <= want.first && want.last <= sp.last {
			return true
		}
	}
	return false
}

// seenText lists the lines shown, for messages: "lines 1-200 and 950-1000".
func (rec *fileRecord) seenText() string {
	if rec.all {
		return "all of it"
	}
	if len(rec.seen) == 0 {
		return "none of it"
	}
	parts := make([]string, 0, len(rec.seen))
	for i, sp := range rec.seen {
		if i == 6 {
			parts = append(parts, fmt.Sprintf("%d more ranges", len(rec.seen)-i))
			break
		}
		parts = append(parts, spanText(sp))
	}
	word := "lines "
	if len(rec.seen) == 1 && rec.seen[0].first == rec.seen[0].last {
		word = "line "
	}
	return word + joinAnd(parts)
}

func spanText(sp span) string {
	if sp.first == sp.last {
		return fmt.Sprint(sp.first)
	}
	return fmt.Sprintf("%d-%d", sp.first, sp.last)
}

// joinAnd joins words the way a sentence lists them: "a", "a and b",
// "a, b and c".
func joinAnd(parts []string) string {
	switch len(parts) {
	case 0:
		return ""
	case 1:
		return parts[0]
	}
	return strings.Join(parts[:len(parts)-1], ", ") + " and " + parts[len(parts)-1]
}

// addSpan adds a span to a sorted list of spans, merging any it overlaps
// or touches.
func addSpan(spans []span, add span) []span {
	if add.last < add.first {
		return spans
	}
	out := make([]span, 0, len(spans)+1)
	placed := false
	for _, sp := range spans {
		switch {
		case sp.last+1 < add.first:
			out = append(out, sp)
		case add.last+1 < sp.first:
			if !placed {
				out = append(out, add)
				placed = true
			}
			out = append(out, sp)
		default:
			add.first = min(add.first, sp.first)
			add.last = max(add.last, sp.last)
		}
	}
	if !placed {
		out = append(out, add)
	}
	return out
}

// coversAll reports whether the spans take in every line of a file of the
// given length.
func coversAll(spans []span, lines int) bool {
	if lines == 0 {
		return true
	}
	return len(spans) == 1 && spans[0].first <= 1 && spans[0].last >= lines
}

// lineEdit is one replacement an edit made, in lines of the old file.
type lineEdit struct {
	// first and last are the old lines the replaced text touched.
	first, last int
	// delta is how many lines the file grew by (or, negative, shrank).
	delta int
}

// shiftSpans works out which lines of the new file have been seen, given
// the lines seen of the old one and the replacements made, in order. Each
// replacement lies wholly inside one seen span, since Edit refuses to
// touch lines that were not shown; that span grows or shrinks with it, and
// the spans after it move.
func shiftSpans(spans []span, edits []lineEdit) []span {
	out := make([]span, 0, len(spans))
	for _, sp := range spans {
		first, last := sp.first, sp.last
		for _, e := range edits {
			if e.last < sp.first {
				first += e.delta
			}
			if e.first <= sp.last {
				last += e.delta
			}
		}
		if last >= first {
			out = addSpan(out, span{first, last})
		}
	}
	return out
}

// FileState is what a session knows about one file it has read or
// written.
type FileState struct {
	// Path is the file's path with every symbolic link followed.
	Path string
	// ModTime and Size are the file's as the session last saw it.
	ModTime time.Time
	Size    int64
	// Full says that the session has been shown every line of the file.
	Full bool
	// Written says that the session itself has written the file.
	Written bool
}

// States lists the files the session has read or written, sorted by path.
// It is for a caller that wants to show or record them; the tools
// themselves do not need it.
func States(env *core.ToolEnv) []FileState {
	s := sessionOf(env)
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]FileState, 0, len(s.files))
	for path, rec := range s.files {
		out = append(out, FileState{Path: path, ModTime: rec.modTime, Size: rec.size, Full: rec.all, Written: rec.written})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Path < out[j].Path })
	return out
}

// Recent lists the files the session read or wrote last, the most recent
// first, at most n of them: for a caller that gives the model back the
// files it was working on (after a summary, which takes them out of its
// view).
func Recent(env *core.ToolEnv, n int) []FileState {
	s := sessionOf(env)
	s.mu.Lock()
	type ordered struct {
		st    FileState
		order uint64
	}
	all := make([]ordered, 0, len(s.files))
	for path, rec := range s.files {
		all = append(all, ordered{FileState{Path: path, ModTime: rec.modTime, Size: rec.size, Full: rec.all, Written: rec.written}, rec.order})
	}
	s.mu.Unlock()
	sort.Slice(all, func(i, j int) bool { return all[i].order > all[j].order })
	out := make([]FileState, 0, min(n, len(all)))
	for _, o := range all[:min(n, len(all))] {
		out = append(out, o.st)
	}
	return out
}

// MarkRead records that the model has seen the whole of the file at path
// as it is on disk now, as if Read had just shown it. It is for a caller
// that showed the file to the model some other way: a shell command that
// printed it, a file the person attached. A relative path is taken from
// env.Cwd. It fails when the path does not lead to a regular file.
func MarkRead(env *core.ToolEnv, path string) error {
	if !filepath.IsAbs(path) {
		if env.Cwd == "" {
			return fmt.Errorf("mark %s as read: the path is relative and the session has no working directory", path)
		}
		path = filepath.Join(env.Cwd, path)
	}
	real, exists, err := followLinks(filepath.Clean(path))
	if err != nil {
		return fmt.Errorf("mark %s as read: %w", path, err)
	}
	if !exists {
		return fmt.Errorf("mark %s as read: %w", path, fs.ErrNotExist)
	}
	info, err := os.Lstat(real)
	if err != nil {
		return fmt.Errorf("mark %s as read: %w", path, underlying(err))
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("mark %s as read: it is %s, not a file", path, kindOf(info.Mode()))
	}
	sessionOf(env).saw(real, sighting{info: info, lines: -1, whole: true})
	return nil
}

// Forget makes the session forget what it has seen of the files at paths,
// as if it had never read them: Edit and Write then refuse to change such
// a file until Read has shown it again. It is for a caller that takes out
// of the model's context the part of the conversation in which the files
// were read (a finished frame, the oldest rounds when room was made): the
// model must not change a file from a copy it can no longer see. That the
// session itself wrote a file is forgotten with the rest, since what it
// wrote is no longer in front of it either.
//
// A relative path is taken from env.Cwd. A path the session knows nothing
// of, or that cannot be followed, is passed over. Forget returns the files
// whose record it dropped, each as the path its links lead to, sorted and
// each once; nil when there were none.
func Forget(env *core.ToolEnv, paths ...string) []string {
	s := sessionOf(env)
	var dropped []string
	for _, path := range paths {
		if path == "" {
			continue
		}
		if !filepath.IsAbs(path) {
			if env.Cwd == "" {
				continue
			}
			path = filepath.Join(env.Cwd, path)
		}
		real, _, err := followLinks(filepath.Clean(path))
		if err != nil {
			continue
		}
		s.mu.Lock()
		if _, known := s.files[real]; known {
			delete(s.files, real)
			dropped = append(dropped, real)
		}
		s.mu.Unlock()
	}
	sort.Strings(dropped)
	return dropped
}

// ForgetAll forgets every file the session has read or written, as Forget
// forgets some: for a summary, which takes the whole conversation out of
// the model's view, with every copy of a file it held. It returns the
// files whose record it dropped, sorted.
func ForgetAll(env *core.ToolEnv) []string {
	s := sessionOf(env)
	s.mu.Lock()
	dropped := make([]string, 0, len(s.files))
	for real := range s.files {
		dropped = append(dropped, real)
	}
	clear(s.files)
	s.mu.Unlock()
	sort.Strings(dropped)
	return dropped
}
