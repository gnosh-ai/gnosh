package fstools

import (
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"reflect"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gnosh-ai/gnosh/internal/core"
)

func TestAddSpan(t *testing.T) {
	tests := []struct {
		spans []span
		add   span
		want  []span
	}{
		{nil, span{3, 5}, []span{{3, 5}}},
		{[]span{{1, 2}}, span{5, 6}, []span{{1, 2}, {5, 6}}},
		{[]span{{5, 6}}, span{1, 2}, []span{{1, 2}, {5, 6}}},
		{[]span{{1, 2}}, span{3, 4}, []span{{1, 4}}},
		{[]span{{3, 4}}, span{1, 2}, []span{{1, 4}}},
		{[]span{{1, 5}}, span{2, 3}, []span{{1, 5}}},
		{[]span{{2, 3}}, span{1, 5}, []span{{1, 5}}},
		{[]span{{1, 3}, {7, 9}}, span{4, 6}, []span{{1, 9}}},
		{[]span{{1, 3}, {7, 9}}, span{5, 5}, []span{{1, 3}, {5, 5}, {7, 9}}},
		{[]span{{1, 3}, {7, 9}, {20, 30}}, span{2, 25}, []span{{1, 30}}},
		{[]span{{1, 3}, {7, 9}, {20, 30}}, span{10, 12}, []span{{1, 3}, {7, 12}, {20, 30}}},
		{[]span{{1, 3}}, span{3, 3}, []span{{1, 3}}},
		{[]span{{1, 3}}, span{5, 4}, []span{{1, 3}}},
	}
	for _, tt := range tests {
		if got := addSpan(tt.spans, tt.add); !reflect.DeepEqual(got, tt.want) {
			t.Errorf("addSpan(%v, %v) = %v, want %v", tt.spans, tt.add, got, tt.want)
		}
	}
}

func TestCoversAll(t *testing.T) {
	tests := []struct {
		spans []span
		lines int
		want  bool
	}{
		{nil, 0, true},
		{nil, 1, false},
		{[]span{{1, 10}}, 10, true},
		{[]span{{1, 12}}, 10, true},
		{[]span{{1, 9}}, 10, false},
		{[]span{{2, 10}}, 10, false},
		{[]span{{1, 4}, {6, 10}}, 10, false},
	}
	for _, tt := range tests {
		if got := coversAll(tt.spans, tt.lines); got != tt.want {
			t.Errorf("coversAll(%v, %d) = %v, want %v", tt.spans, tt.lines, got, tt.want)
		}
	}
}

func TestShiftSpans(t *testing.T) {
	tests := []struct {
		name  string
		spans []span
		edits []lineEdit
		want  []span
	}{
		{"no edits", []span{{1, 10}}, nil, []span{{1, 10}}},
		{"a line changed in place", []span{{1, 10}}, []lineEdit{{5, 5, 0}}, []span{{1, 10}}},
		{"lines added inside", []span{{1, 10}}, []lineEdit{{5, 5, 3}}, []span{{1, 13}}},
		{"lines removed inside", []span{{1, 10}}, []lineEdit{{4, 6, -3}}, []span{{1, 7}}},
		{"an edit before moves a later span", []span{{1, 5}, {20, 30}}, []lineEdit{{3, 3, 2}}, []span{{1, 7}, {22, 32}}},
		{"an edit in the later span leaves the earlier one", []span{{1, 5}, {20, 30}}, []lineEdit{{25, 26, -1}}, []span{{1, 5}, {20, 29}}},
		{"two edits", []span{{1, 5}, {20, 30}}, []lineEdit{{2, 2, 1}, {22, 22, 4}}, []span{{1, 6}, {21, 35}}},
		{"a span whose only line is removed", []span{{1, 3}, {7, 7}, {10, 12}}, []lineEdit{{7, 7, -1}}, []span{{1, 3}, {9, 11}}},
		{"an edit on a span's last line", []span{{4, 8}}, []lineEdit{{8, 8, 2}}, []span{{4, 10}}},
		{"an edit on a span's first line", []span{{4, 8}}, []lineEdit{{4, 4, 2}}, []span{{4, 10}}},
		{"a gap stays a gap", []span{{1, 3}, {5, 9}}, []lineEdit{{3, 3, 1}}, []span{{1, 4}, {6, 10}}},
	}
	for _, tt := range tests {
		if got := shiftSpans(tt.spans, tt.edits); !reflect.DeepEqual(got, tt.want) {
			t.Errorf("%s: shiftSpans(%v, %v) = %v, want %v", tt.name, tt.spans, tt.edits, got, tt.want)
		}
	}
}

func TestSeenText(t *testing.T) {
	tests := []struct {
		rec  fileRecord
		want string
	}{
		{fileRecord{all: true}, "all of it"},
		{fileRecord{}, "none of it"},
		{fileRecord{seen: []span{{4, 4}}}, "line 4"},
		{fileRecord{seen: []span{{1, 10}}}, "lines 1-10"},
		{fileRecord{seen: []span{{1, 10}, {12, 12}}}, "lines 1-10 and 12"},
		{fileRecord{seen: []span{{1, 2}, {4, 5}, {7, 8}}}, "lines 1-2, 4-5 and 7-8"},
		{fileRecord{seen: []span{{1, 1}, {3, 3}, {5, 5}, {7, 7}, {9, 9}, {11, 11}, {13, 13}, {15, 15}}}, "lines 1, 3, 5, 7, 9, 11 and 2 more ranges"},
	}
	for _, tt := range tests {
		if got := tt.rec.seenText(); got != tt.want {
			t.Errorf("seenText(%v) = %q, want %q", tt.rec.seen, got, tt.want)
		}
	}
}

func TestStatesListsWhatWasReadAndWritten(t *testing.T) {
	f := newFixture(t, Options{ReadDefaultLines: 3})
	if got := States(f.env); len(got) != 0 {
		t.Fatalf("a new session has states: %+v", got)
	}
	whole := f.file("b-whole.txt", "x\n")
	part := f.file("a-part.txt", manyLines(10))
	written := f.path("c-written.txt")
	f.ok(f.read, in{"file_path": whole})
	f.ok(f.read, in{"file_path": part})
	f.ok(f.write, in{"file_path": written, "content": "new\n"})

	got := States(f.env)
	if len(got) != 3 {
		t.Fatalf("States = %+v, want three files", got)
	}
	want := []struct {
		path          string
		full, written bool
		size          int64
	}{
		{part, false, false, int64(len(manyLines(10)))},
		{whole, true, false, 2},
		{written, true, true, 4},
	}
	for i, w := range want {
		g := got[i]
		if g.Path != w.path || g.Full != w.full || g.Written != w.written || g.Size != w.size {
			t.Errorf("States[%d] = %+v, want %+v", i, g, w)
		}
		info, _ := os.Stat(w.path)
		if !g.ModTime.Equal(info.ModTime()) {
			t.Errorf("States[%d].ModTime = %v, the file's is %v", i, g.ModTime, info.ModTime())
		}
	}
	// An edit keeps the fact that only part was seen, and adds that the
	// session wrote the file.
	f.ok(f.edit, in{"file_path": part, "old_string": "line 2", "new_string": "two"})
	got = States(f.env)
	if got[0].Path != part || got[0].Full || !got[0].Written {
		t.Errorf("after an edit: %+v", got[0])
	}
	// A second session knows nothing of the first.
	other := &core.ToolEnv{Cwd: f.dir, Store: &sync.Map{}}
	if got := States(other); len(got) != 0 {
		t.Errorf("another session sees %+v", got)
	}
	res, err := f.edit.Run(context.Background(), call(in{"file_path": whole, "old_string": "x", "new_string": "y"}), other)
	if err != nil || !res.IsError || !strings.Contains(core.TextOf(res.Content), "has not read") {
		t.Errorf("an edit in another session: %v, %s", err, core.TextOf(res.Content))
	}
	// What States returns is JSON-encodable, for a transcript.
	if _, err := json.Marshal(got); err != nil {
		t.Errorf("States does not encode: %v", err)
	}
}

func TestMarkRead(t *testing.T) {
	f := newFixture(t, Options{})
	p := f.file("shown-elsewhere.txt", "one\ntwo\n")
	// Shown to the model some other way, the file may be changed.
	if err := MarkRead(f.env, p); err != nil {
		t.Fatal(err)
	}
	if st := States(f.env); len(st) != 1 || st[0].Path != p || !st[0].Full || st[0].Written {
		t.Errorf("States = %+v", st)
	}
	f.ok(f.edit, in{"file_path": p, "old_string": "two", "new_string": "2"})

	// A relative path is taken from the working directory.
	f.file("rel.txt", "x\n")
	if err := MarkRead(f.env, "rel.txt"); err != nil {
		t.Fatal(err)
	}
	f.ok(f.write, in{"file_path": f.path("rel.txt"), "content": "y\n"})

	// The mark is of the file as it is now: a later change undoes it.
	q := f.file("changes.txt", "before\n")
	if err := MarkRead(f.env, q); err != nil {
		t.Fatal(err)
	}
	f.setTime(f.file("changes.txt", "after, and longer\n"), 5)
	f.fails(f.edit, in{"file_path": q, "old_string": "after", "new_string": "x"}, "has changed on disk")

	// Through a link it is the target that is marked.
	link := f.symlink("shown-elsewhere.txt", "link")
	if err := MarkRead(f.env, link); err != nil {
		t.Fatal(err)
	}
	if st := States(f.env); len(st) != 3 {
		t.Errorf("a link made a state of its own: %+v", st)
	}

	// What cannot be marked.
	if err := MarkRead(f.env, f.path("missing")); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("a missing file: err = %v", err)
	}
	if err := MarkRead(f.env, f.dir); err == nil || !strings.Contains(err.Error(), "a directory") {
		t.Errorf("a directory: err = %v", err)
	}
	if err := MarkRead(&core.ToolEnv{Store: &sync.Map{}}, "rel.txt"); err == nil || !strings.Contains(err.Error(), "no working directory") {
		t.Errorf("a relative path with no working directory: err = %v", err)
	}
	f.symlink("round", "round")
	if err := MarkRead(f.env, f.path("round")); err == nil {
		t.Error("a looping link was marked")
	}
}

func TestRecordUnchanged(t *testing.T) {
	f := newFixture(t, Options{})
	p := f.file("a.txt", "content\n")
	info, _ := os.Stat(p)
	rec := fileRecord{modTime: info.ModTime(), size: info.Size()}
	if !rec.unchanged(info, nil) || !rec.unchanged(info, []byte("anything")) {
		t.Error("without a hash, the same size and time should count as unchanged")
	}
	later := info.ModTime().Add(time.Second)
	os.Chtimes(p, later, later)
	touched, _ := os.Stat(p)
	if rec.unchanged(touched, nil) {
		t.Error("without a hash, another time should count as changed")
	}
	// With a hash and the bytes in hand, the bytes decide.
	rec.hasSum = true
	rec.sum = [32]byte{0x5a}
	if rec.unchanged(info, []byte("content\n")) {
		t.Error("another hash should count as changed, whatever the time")
	}
	// With a hash but no bytes in hand, size and time decide again.
	if !rec.unchanged(info, nil) || rec.unchanged(touched, nil) {
		t.Error("with a hash and no bytes, size and time should decide")
	}
}

func TestSessionLock(t *testing.T) {
	s := &session{files: map[string]*fileRecord{}, locks: map[string]chan struct{}{}}
	unlock, err := s.lock(context.Background(), "/a")
	if err != nil {
		t.Fatal(err)
	}
	// Another file is free.
	other, err := s.lock(context.Background(), "/b")
	if err != nil {
		t.Fatal(err)
	}
	other()
	// The same file waits, and gives up when its context does.
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if _, err := s.lock(ctx, "/a"); !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("a second lock on a held file: err = %v", err)
	}
	// Once released, it can be taken again.
	got := make(chan struct{})
	go func() {
		u, err := s.lock(context.Background(), "/a")
		if err == nil {
			u()
		}
		close(got)
	}()
	unlock()
	select {
	case <-got:
	case <-time.After(10 * time.Second):
		t.Fatal("the lock was not handed on")
	}
}

// A session that is made to forget a file must read it again before it
// may change it: that is what keeps a model from editing a file from a
// copy it can no longer see.
// Recent lists the files the session read or wrote last, the most recent
// first: a read, a write and an edit each make a file the most recent.
func TestRecent(t *testing.T) {
	f := newFixture(t, Options{})
	a := f.file("a.txt", "one\n")
	b := f.file("b.txt", "two\n")
	c := f.file("c.txt", "three\n")
	for _, p := range []string{a, b, c} {
		f.ok(f.read, in{"file_path": p})
	}
	d := f.path("d.txt")
	f.ok(f.write, in{"file_path": d, "content": "four\n"})
	f.ok(f.edit, in{"file_path": a, "old_string": "one", "new_string": "1"})
	f.ok(f.read, in{"file_path": b})
	paths := func(st []FileState) []string {
		var out []string
		for _, s := range st {
			out = append(out, s.Path)
		}
		return out
	}
	if got, want := paths(Recent(f.env, 3)), []string{b, a, d}; !reflect.DeepEqual(got, want) {
		t.Errorf("Recent(3) = %v, want %v", got, want)
	}
	if got, want := paths(Recent(f.env, 9)), []string{b, a, d, c}; !reflect.DeepEqual(got, want) {
		t.Errorf("Recent(9) = %v, want %v", got, want)
	}
	if st := Recent(f.env, 9); !st[2].Written || st[3].Written {
		t.Errorf("Recent says the wrong files were written: %+v", st)
	}
	ForgetAll(f.env)
	if got := Recent(f.env, 3); len(got) != 0 {
		t.Errorf("Recent after ForgetAll = %v", paths(got))
	}
}

func TestForget(t *testing.T) {
	f := newFixture(t, Options{})
	a := f.file("a.txt", "one\ntwo\n")
	b := f.file("sub/b.txt", "three\n")
	c := f.file("c.txt", "four\n")
	for _, p := range []string{a, b, c} {
		f.ok(f.read, in{"file_path": p})
	}
	// The session wrote d itself.
	d := f.path("d.txt")
	f.ok(f.write, in{"file_path": d, "content": "five\n"})

	// A relative path is taken from the working directory; a path the
	// session knows nothing of, an empty one and one given twice are
	// passed over.
	got := Forget(f.env, "sub/b.txt", a, f.path("never-read.txt"), "", a, d)
	want := []string{a, d, b}
	sort.Strings(want)
	if !reflect.DeepEqual(got, want) {
		t.Errorf("Forget dropped %v, want %v", got, want)
	}
	if st := States(f.env); len(st) != 1 || st[0].Path != c {
		t.Errorf("States after Forget = %+v, want c.txt alone", st)
	}

	// What was forgotten is refused until it is read again; what was not
	// is changed as before.
	f.fails(f.edit, in{"file_path": a, "old_string": "two", "new_string": "2"}, "has not read")
	f.fails(f.write, in{"file_path": d, "content": "six\n"}, "has not read")
	f.ok(f.edit, in{"file_path": c, "old_string": "four", "new_string": "4"})
	f.ok(f.read, in{"file_path": a})
	f.ok(f.edit, in{"file_path": a, "old_string": "two", "new_string": "2"})
	if got := f.content(a); got != "one\n2\n" {
		t.Errorf("a.txt = %q", got)
	}

	// Through a link it is the target that is forgotten.
	link := f.symlink("c.txt", "link-to-c")
	if got := Forget(f.env, link); len(got) != 1 || got[0] != c {
		t.Errorf("Forget through a link dropped %v, want %v", got, c)
	}
	f.fails(f.edit, in{"file_path": c, "old_string": "4", "new_string": "four"}, "has not read")

	// Nothing to forget is nil, and a relative path with no working
	// directory is passed over.
	if got := Forget(f.env); got != nil {
		t.Errorf("Forget of nothing = %v", got)
	}
	if got := Forget(&core.ToolEnv{Store: f.env.Store}, "a.txt"); got != nil {
		t.Errorf("Forget of a relative path with no working directory = %v", got)
	}
}
