package transcript

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"regexp"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gnosh-ai/gnosh/internal/core"
)

// fakeClock tells a time that moves on by a step each time it is asked.
type fakeClock struct {
	mu   sync.Mutex
	t    time.Time
	step time.Duration
}

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(c.step)
	return c.t
}

// newStore makes a store in a temporary directory, with a clock that
// starts on the morning of 13 November 2026 and ticks a millisecond a call.
func newStore(t testing.TB) (*Store, *fakeClock) {
	t.Helper()
	clock := &fakeClock{t: time.Date(2026, 11, 13, 8, 0, 0, 0, time.UTC), step: time.Millisecond}
	return &Store{Dir: t.TempDir(), Clock: clock.Now}, clock
}

var ctx = context.Background()

const cwd = "/work/app"

// mustAppend appends entries and returns them as written.
func mustAppend(t testing.TB, s *Session, entries ...Entry) []Entry {
	t.Helper()
	var out []Entry
	for _, e := range entries {
		w, err := s.Append(e)
		if err != nil {
			t.Fatalf("Append: %v", err)
		}
		out = append(out, w)
	}
	return out
}

// fileLines reads a session file's lines.
func fileLines(t testing.TB, path string) []string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(data) == 0 {
		return nil
	}
	if data[len(data)-1] != '\n' {
		t.Fatalf("%s does not end in a line break", path)
	}
	return strings.Split(strings.TrimSuffix(string(data), "\n"), "\n")
}

// oneOfEach is an entry of every type the format defines.
func oneOfEach() []Entry {
	reply := core.Message{Blocks: []core.Block{
		{Type: core.BlockThinking, Text: "let me look", Signature: "sig"},
		core.Text("I'll read <main.go> & fix it."),
		core.ToolUse("t1", "Read", json.RawMessage(`{"file_path":"/work/app/main.go"}`)),
	}}
	results := core.Message{Blocks: []core.Block{
		core.ToolResultBlock("t1", core.ToolResult{Content: []core.Block{core.Text("package main\n"), {Type: core.BlockImage, MediaType: "image/png", Data: []byte{1, 2, 3}}}}),
	}}
	return []Entry{
		PromptEntry("fix the bug\nin main.go"),
		AssistantEntry(reply, core.StopToolUse),
		UserEntry(results, SourceToolResults),
		PushEntry("fix-bug", "fix the bug", "t1"),
		UserEntry(core.UserText("a hook's context"), SourceHook),
		UserEntry(core.UserText("the background command ended"), SourceNotice),
		PopEntry("fix-bug", "fix the bug", "main.go fixed", "t2"),
		{Type: TypeFrame, Op: OpSlide, Frame: "fix-bug", Anchor: "a", Until: "b"},
		SummaryEntry("what happened so far", "some-uuid"),
		RewindEntry("some-uuid"),
		TitleEntry("Fix the bug", SourceUser),
		MetaEntry("model", "qwen3.8-27b"),
		UsageEntry(core.Usage{InputTokens: 1200, OutputTokens: 38, CacheReadTokens: 900}, "qwen3.8-27b", 4210*time.Millisecond),
		{Type: TypeBranch, FromSession: "0193267a-0000-7000-8000-000000000000", FromUUID: "some-uuid"},
	}
}

func TestCreateAppendReopen(t *testing.T) {
	st, _ := newStore(t)
	s, err := st.Create(cwd + "/")
	if err != nil {
		t.Fatal(err)
	}
	if !validID(s.ID()) || s.ID()[14] != '7' {
		t.Errorf("the session id %q is not a UUIDv7", s.ID())
	}
	if want := filepath.Join(st.Dir, "projects", "-work-app", s.ID()+".jsonl"); s.Path() != want {
		t.Errorf("Path = %q, want %q", s.Path(), want)
	}
	if _, err := os.Stat(s.Path()); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("a session with no entries has a file already (Stat: %v)", err)
	}

	written := mustAppend(t, s, oneOfEach()...)
	stamp := regexp.MustCompile(`^\d{4}-\d\d-\d\dT\d\d:\d\d:\d\d\.\d{9}Z$`)
	lines := fileLines(t, s.Path())
	if len(lines) != len(written) {
		t.Fatalf("the file has %d lines for %d entries", len(lines), len(written))
	}
	for i, e := range written {
		parent := ""
		if i > 0 {
			parent = written[i-1].UUID
		}
		if !validID(e.UUID) || e.ParentUUID != parent || e.SessionID != s.ID() || e.Cwd != cwd || e.V != Version {
			t.Errorf("entry %d was written as %+v", i, e)
		}
		if i > 0 && !(e.UUID > parent) || i > 0 && !e.Timestamp.After(written[i-1].Timestamp) {
			t.Errorf("entry %d does not sort after the one before", i)
		}
		var fields map[string]any
		if err := json.Unmarshal([]byte(lines[i]), &fields); err != nil {
			t.Fatalf("line %d is not JSON: %v", i+1, err)
		}
		for _, name := range []string{"type", "uuid", "parentUuid", "sessionId", "timestamp", "cwd", "v"} {
			if _, ok := fields[name]; !ok {
				t.Errorf("line %d has no %q", i+1, name)
			}
		}
		if ts, _ := fields["timestamp"].(string); !stamp.MatchString(ts) {
			t.Errorf("line %d has the timestamp %q", i+1, ts)
		}
	}
	if strings.Contains(lines[1], `\u003c`) || !strings.Contains(lines[1], "<main.go> & fix") {
		t.Errorf("the reply's text was escaped: %s", lines[1])
	}
	if !reflect.DeepEqual(s.Entries(), written) {
		t.Errorf("Entries differ from what Append returned")
	}
	if err := s.Sync(); err != nil {
		t.Error(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Errorf("closing twice: %v", err)
	}
	if _, err := s.Append(PromptEntry("too late")); !errors.Is(err, ErrClosed) {
		t.Errorf("Append after Close = %v, want ErrClosed", err)
	}

	// Reopen it, by the start of its id, and go on.
	s2, err := st.Open(ctx, s.ID()[:18])
	if err != nil {
		t.Fatal(err)
	}
	defer s2.Close()
	if len(s2.Warnings()) != 0 {
		t.Errorf("Warnings = %v", s2.Warnings())
	}
	if !reflect.DeepEqual(s2.Entries(), written) {
		for i := range written {
			if !reflect.DeepEqual(s2.Entries()[i], written[i]) {
				t.Errorf("entry %d was read back as\n%+v\nwritten as\n%+v", i, s2.Entries()[i], written[i])
			}
		}
		t.Fatalf("the entries read back differ from the ones written")
	}
	if s2.ID() != s.ID() || s2.StartCwd() != cwd || s2.Cwd() != cwd || s2.ReadOnly() {
		t.Errorf("reopened as id %q, cwd %q, read-only %v", s2.ID(), s2.Cwd(), s2.ReadOnly())
	}
	snapshot := s2.Entries()
	s2.SetCwd("/work/app/sub")
	more := mustAppend(t, s2, PromptEntry("and the tests"))
	if more[0].ParentUUID != written[len(written)-1].UUID || more[0].Cwd != "/work/app/sub" {
		t.Errorf("the entry after reopening was written as %+v", more[0])
	}
	if len(snapshot) != len(written) || len(s2.Entries()) != len(written)+1 {
		t.Errorf("a snapshot of %d entries now has %d; the session has %d", len(written), len(snapshot), len(s2.Entries()))
	}
	if got := fileLines(t, s.Path()); len(got) != len(lines)+1 || !reflect.DeepEqual(got[:len(lines)], lines) {
		t.Errorf("appending changed the lines that were there")
	}
}

func TestAppendKeepsWhatTheCallerSet(t *testing.T) {
	st, _ := newStore(t)
	s, _ := st.Create(cwd)
	defer s.Close()
	when := time.Date(2026, 1, 2, 3, 4, 5, 6, time.FixedZone("PST", -8*3600))
	e := PromptEntry("hi")
	e.UUID, e.Timestamp, e.Cwd, e.V = "my-own-uuid", when, "/elsewhere", 7
	e.ParentUUID, e.SessionID = "not this", "nor this"
	got := mustAppend(t, s, e)[0]
	if got.UUID != "my-own-uuid" || !got.Timestamp.Equal(when) || got.Timestamp.Location() != time.UTC || got.Cwd != "/elsewhere" || got.V != 7 {
		t.Errorf("Append changed what was set: %+v", got)
	}
	if got.ParentUUID != "" || got.SessionID != s.ID() {
		t.Errorf("Append kept a parent or session that was not its own: %+v", got)
	}
}

func TestAppendAll(t *testing.T) {
	st, _ := newStore(t)
	s, _ := st.Create(cwd)
	defer s.Close()
	first := mustAppend(t, s, PromptEntry("go"))[0]
	got, err := s.AppendAll(
		AssistantEntry(core.Message{Blocks: []core.Block{core.ToolUse("t1", "mem", nil)}}, core.StopToolUse),
		UserEntry(core.Message{Blocks: []core.Block{core.ToolResultBlock("t1", core.TextResult("in `a`"))}}, SourceToolResults),
		PushEntry("a", "a todo", "t1"),
	)
	if err != nil || len(got) != 3 {
		t.Fatalf("AppendAll = %d entries, %v", len(got), err)
	}
	if got[0].ParentUUID != first.UUID || got[1].ParentUUID != got[0].UUID || got[2].ParentUUID != got[1].UUID {
		t.Errorf("the entries do not follow one another: %+v", got)
	}
	if !reflect.DeepEqual(s.Entries()[1:], got) || len(fileLines(t, s.Path())) != 4 {
		t.Errorf("the session holds %d entries and the file %d lines, want 4 of each", len(s.Entries()), len(fileLines(t, s.Path())))
	}
	if got, err := s.AppendAll(); got != nil || err != nil {
		t.Errorf("AppendAll of nothing = %v, %v", got, err)
	}

	// When one of them cannot be written, none is.
	bad := Entry{Type: "x", Extra: map[string]json.RawMessage{"k": json.RawMessage("{")}}
	if _, err := s.AppendAll(PromptEntry("one"), bad, PromptEntry("three")); err == nil {
		t.Fatalf("an entry that cannot be written was appended")
	}
	if len(s.Entries()) != 4 || len(fileLines(t, s.Path())) != 4 {
		t.Errorf("a failed AppendAll left %d entries and %d lines, want 4 of each", len(s.Entries()), len(fileLines(t, s.Path())))
	}
	next := mustAppend(t, s, PromptEntry("next"))[0]
	if next.ParentUUID != got[2].UUID {
		t.Errorf("after a failed AppendAll the next entry follows %q, want %q", next.ParentUUID, got[2].UUID)
	}
}

func TestEntryJSONRoundTrip(t *testing.T) {
	for _, e := range oneOfEach() {
		e.UUID, e.SessionID, e.Cwd, e.V = "u", "s", cwd, Version
		e.Timestamp = time.Date(2026, 11, 13, 8, 0, 0, 123456789, time.UTC)
		data, err := json.Marshal(e)
		if err != nil {
			t.Fatalf("%s: %v", e.Type, err)
		}
		var back Entry
		if err := json.Unmarshal(data, &back); err != nil {
			t.Fatalf("%s: %v", e.Type, err)
		}
		if !reflect.DeepEqual(back, e) {
			t.Errorf("%s entry\n%+v\ncame back as\n%+v\nfrom %s", e.Type, e, back, data)
		}
	}
	var e Entry
	for _, bad := range []string{`[]`, `42`, `null`, `"text"`, `{"type":7}`, `{"type":"user"`, ``} {
		if err := json.Unmarshal([]byte(bad), &e); err == nil {
			t.Errorf("%q was read as an entry", bad)
		}
	}
	if _, err := json.Marshal(Entry{Type: "x", Extra: map[string]json.RawMessage{"bad": json.RawMessage(`{"a":`)}}); err == nil {
		t.Errorf("an entry with invalid JSON in Extra was written")
	}
	// A value in Extra that spans lines is written on one.
	line, err := (&Entry{Type: "x", Extra: map[string]json.RawMessage{"k": json.RawMessage("{\n  \"a\": 1\n}")}}).line()
	if err != nil || bytes.ContainsRune(line, '\n') || !bytes.Contains(line, []byte(`"k":{"a":1}`)) {
		t.Errorf("line = %s, err = %v", line, err)
	}
}

// foreign is a session file as a newer gnosh might write it: a field this
// version does not know on an entry it does, members it does not know
// inside a message, and an entry of a type it has never heard of.
const foreignID = "0193267a-1111-7000-8000-000000000001"

var foreign = []string{
	`{"type":"user","uuid":"u1","parentUuid":"","sessionId":"` + foreignID + `","timestamp":"2026-11-13T08:00:00.000000001Z","cwd":"/work/app","v":1,"source":"prompt","message":{"role":"user","content":[{"type":"text","text":"hi","cache":{"ttl":"1h"}}],"id":"msg_1"},"future":{"a":[1,2.50,"<x>"]},"flag":true}`,
	`{"type":"hologram","uuid":"u2","parentUuid":"u1","sessionId":"` + foreignID + `","timestamp":"2026-11-13T08:00:00.000000002Z","cwd":"/work/app","v":2,"message":"not a message here","title":{"deep":{"deeper":null}},"n":1.50}`,
	`{"type":"assistant","uuid":"u3","parentUuid":"u2","sessionId":"` + foreignID + `","timestamp":"2026-11-13T08:00:00.000000003Z","cwd":"/work/app","v":1,"stop":"end_turn","message":{"role":"assistant","content":[{"type":"text","text":"hello"}]}}`,
}

// writeSession writes lines as the file of a session started in cwd.
func writeSession(t testing.TB, st *Store, id string, content string) string {
	t.Helper()
	path := filepath.Join(st.ProjectDir(cwd), id+".jsonl")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

// sameJSON reports whether two JSON texts say the same thing, number for
// number.
func sameJSON(t testing.TB, a, b string) bool {
	t.Helper()
	read := func(s string) any {
		dec := json.NewDecoder(strings.NewReader(s))
		dec.UseNumber()
		var v any
		if err := dec.Decode(&v); err != nil {
			t.Fatalf("not JSON: %v: %s", err, s)
		}
		return v
	}
	return reflect.DeepEqual(read(a), read(b))
}

func TestUnknownTypesAndFieldsSurvive(t *testing.T) {
	st, _ := newStore(t)
	path := writeSession(t, st, foreignID, strings.Join(foreign, "\n")+"\n")
	s, err := st.Open(ctx, foreignID)
	if err != nil {
		t.Fatal(err)
	}
	entries := s.Entries()
	if len(entries) != 3 {
		t.Fatalf("read %d entries, want 3", len(entries))
	}
	// One warning: the file holds an entry in a newer format. The unknown
	// type and the unknown fields are not something to warn about.
	if w := s.Warnings(); len(w) != 1 || w[0].Kind != WarnNewer || w[0].Line != 2 {
		t.Errorf("Warnings = %v", w)
	}
	if e := entries[0]; e.Message.Text() != "hi" || string(e.Extra["flag"]) != "true" || len(e.Extra) != 2 {
		t.Errorf("the first entry was read as %+v", e)
	}
	if e := entries[1]; e.Type != "hologram" || e.V != 2 || e.UUID != "u2" || len(e.Extra) != 3 || e.Title != "" || len(e.Message.Blocks) != 0 {
		t.Errorf("the entry of an unknown type was read as %+v", e)
	}

	// Writing each entry again gives back all it held.
	for i, e := range entries {
		data, err := json.Marshal(e)
		if err != nil {
			t.Fatal(err)
		}
		if !sameJSON(t, string(data), foreign[i]) {
			t.Errorf("entry %d was written again as\n%s\nit was\n%s", i, data, foreign[i])
		}
	}

	// The view ignores what it does not know.
	msgs, _ := mustView(t, entries, ViewOptions{})
	if got, want := render(msgs), "U: hi\nA: hello"; got != want {
		t.Errorf("view:\n%s\n--- want ---\n%s", got, want)
	}

	// Appending leaves the lines that were there untouched.
	mustAppend(t, s, PromptEntry("more"))
	s.Close()
	if got := fileLines(t, path); len(got) != 4 || !reflect.DeepEqual(got[:3], foreign) {
		t.Errorf("after an append the file's first lines are\n%s", strings.Join(got, "\n"))
	}

	// Copying the entries into another session keeps everything but what
	// says where an entry is: its session and the entry before it.
	other, _ := st.Create(cwd)
	defer other.Close()
	copied := mustAppend(t, other, entries...)
	lines := fileLines(t, other.Path())
	for i := range foreign {
		want := strings.Replace(foreign[i], foreignID, other.ID(), 1)
		if i == 0 && copied[0].ParentUUID != "" || i > 0 && copied[i].ParentUUID != copied[i-1].UUID {
			t.Errorf("copied entry %d has the parent %q", i, copied[i].ParentUUID)
		}
		if !sameJSON(t, lines[i], want) {
			t.Errorf("entry %d was copied as\n%s\nwant\n%s", i, lines[i], want)
		}
	}
}

func TestFieldsOfTheWrongShapeAreKept(t *testing.T) {
	st, _ := newStore(t)
	const id = "0193267a-2222-7000-8000-000000000002"
	lines := []string{
		`{"type":"user","uuid":"u1","sessionId":"` + id + `","timestamp":12345,"cwd":"/work/app","v":1,"source":"prompt","message":"just a string"}`,
		`{"type":"user","uuid":"u2","sessionId":"` + id + `","timestamp":"2026-11-13T08:00:00Z","cwd":"/work/app","v":1,"source":"prompt","message":{"role":"user","content":[{"type":"text","text":"hi"}]}}`,
	}
	writeSession(t, st, id, strings.Join(lines, "\n")+"\n")
	s, err := st.OpenReadOnly(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	entries, warns := s.Entries(), s.Warnings()
	if len(entries) != 2 || len(warns) != 2 {
		t.Fatalf("%d entries, warnings %v", len(entries), warns)
	}
	for i, field := range []string{"message", "timestamp"} {
		if w := warns[i]; w.Kind != WarnBadField || w.Line != 1 || !strings.Contains(w.Detail, `"`+field+`"`) {
			t.Errorf("warning %d = %+v, want a bad %s on line 1", i, w, field)
		}
	}
	if e := entries[0]; !e.Timestamp.IsZero() || len(e.Message.Blocks) != 0 || len(e.Extra) != 2 {
		t.Errorf("the entry was read as %+v", e)
	}
	data, _ := json.Marshal(entries[0])
	if !sameJSON(t, string(data), strings.Replace(lines[0], `"sessionId"`, `"parentUuid":"","sessionId"`, 1)) {
		t.Errorf("written again as %s", data)
	}
	msgs, _ := mustView(t, entries, ViewOptions{})
	if got := render(msgs); got != "U: hi" {
		t.Errorf("view: %s", got)
	}
}

func TestOneWriter(t *testing.T) {
	st, _ := newStore(t)
	s, _ := st.Create(cwd)
	mustAppend(t, s, PromptEntry("hi"))

	// A second open for writing is refused, in words that say why.
	_, err := st.Open(ctx, s.ID())
	var locked *LockedError
	if !errors.As(err, &locked) || locked.ID != s.ID() || locked.Path != s.Path() {
		t.Fatalf("a second writer got %v, want a *LockedError", err)
	}
	if msg := err.Error(); !strings.Contains(msg, "another gnosh") || !strings.Contains(msg, "read-only") || !strings.Contains(msg, "branch") {
		t.Errorf("the error does not say what to do: %q", msg)
	}

	if !InUse(s.Path()) {
		t.Error("a session open for writing is not in use")
	}
	if InUse(s.Path() + ".none") {
		t.Error("a file that is not there is in use")
	}

	// It can be opened read-only meanwhile, and branched.
	ro, err := st.OpenReadOnly(ctx, s.ID())
	if err != nil {
		t.Fatal(err)
	}
	if !ro.ReadOnly() || len(ro.Entries()) != 1 {
		t.Errorf("read-only: %v, %d entries", ro.ReadOnly(), len(ro.Entries()))
	}
	if _, err := ro.Append(PromptEntry("no")); !errors.Is(err, ErrReadOnly) {
		t.Errorf("Append to a read-only session = %v, want ErrReadOnly", err)
	}
	mustAppend(t, s, AssistantEntry(core.AssistantText("hello"), core.StopEndTurn))
	if len(ro.Entries()) != 1 {
		t.Errorf("a read-only session saw a later append")
	}
	b, err := st.Branch(ro, "")
	if err != nil {
		t.Fatal(err)
	}
	b.Close()
	ro.Close()

	// Once the writer closes, another may open it.
	s.Close()
	if InUse(s.Path()) {
		t.Error("a session nobody writes is in use")
	}
	s2, err := st.Open(ctx, s.ID())
	if err != nil {
		t.Fatalf("after the writer closed: %v", err)
	}
	if len(s2.Entries()) != 2 {
		t.Errorf("reopened with %d entries, want 2", len(s2.Entries()))
	}
	s2.Close()
}

// TestHelperLockHolder is not a test. TestSecondProcess runs the test
// binary again with this as its only test, to be the other gnosh: it opens
// a session for writing and holds it until its standard input closes.
func TestHelperLockHolder(t *testing.T) {
	dir, id := os.Getenv("GNOSH_TEST_LOCK_DIR"), os.Getenv("GNOSH_TEST_LOCK_ID")
	if dir == "" {
		t.Skip("only run by TestSecondProcess")
	}
	s, err := New(dir).Open(ctx, id)
	if err != nil {
		fmt.Println("failed:", err)
		return
	}
	defer s.Close()
	fmt.Println("locked")
	io.Copy(io.Discard, os.Stdin)
}

func TestSecondProcess(t *testing.T) {
	st, _ := newStore(t)
	s, _ := st.Create(cwd)
	mustAppend(t, s, PromptEntry("hi"))
	s.Close()

	cmd := exec.Command(os.Args[0], "-test.run=^TestHelperLockHolder$")
	cmd.Env = append(os.Environ(), "GNOSH_TEST_LOCK_DIR="+st.Dir, "GNOSH_TEST_LOCK_ID="+s.ID())
	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer cmd.Process.Kill()
	held := false
	sc := bufio.NewScanner(stdout)
	for sc.Scan() {
		if sc.Text() == "locked" {
			held = true
			break
		}
		if strings.HasPrefix(sc.Text(), "failed:") {
			t.Fatalf("the other process could not open the session: %s", sc.Text())
		}
	}
	if !held {
		t.Fatalf("the other process ended without opening the session")
	}

	var locked *LockedError
	if _, err := st.Open(ctx, s.ID()); !errors.As(err, &locked) {
		t.Errorf("with another process writing, Open = %v, want a *LockedError", err)
	}
	ro, err := st.OpenReadOnly(ctx, s.ID())
	if err != nil || len(ro.Entries()) != 1 {
		t.Errorf("with another process writing, OpenReadOnly = %v", err)
	}

	stdin.Close()
	io.Copy(io.Discard, stdout)
	if err := cmd.Wait(); err != nil {
		t.Fatalf("the other process: %v", err)
	}
	s2, err := st.Open(ctx, s.ID())
	if err != nil {
		t.Fatalf("after the other process ended: %v", err)
	}
	s2.Close()
}

func TestBranch(t *testing.T) {
	st, _ := newStore(t)
	s, _ := st.Create(cwd)
	w := mustAppend(t, s,
		PromptEntry("one"),
		AssistantEntry(core.AssistantText("answer one"), core.StopEndTurn),
		PromptEntry("two"),
		AssistantEntry(core.AssistantText("answer two"), core.StopEndTurn),
	)
	before := fileLines(t, s.Path())

	b, err := st.Branch(s, w[1].UUID)
	if err != nil {
		t.Fatal(err)
	}
	if b.ID() == s.ID() || filepath.Dir(b.Path()) != filepath.Dir(s.Path()) || b.Inherited() != 2 {
		t.Errorf("the branch is %s at %s, inheriting %d", b.ID(), b.Path(), b.Inherited())
	}
	lines := fileLines(t, b.Path())
	if len(lines) != 1 {
		t.Fatalf("the branch's file has %d lines, want its branch entry", len(lines))
	}
	var first Entry
	if err := json.Unmarshal([]byte(lines[0]), &first); err != nil {
		t.Fatal(err)
	}
	if first.Type != TypeBranch || first.FromSession != s.ID() || first.FromUUID != w[1].UUID || first.ParentUUID != "" || first.SessionID != b.ID() {
		t.Errorf("the branch entry is %+v", first)
	}
	mustAppend(t, b, PromptEntry("two, another way"), AssistantEntry(core.AssistantText("another answer"), core.StopEndTurn))
	const want = "U: one\nA: answer one\nU: two, another way\nA: another answer"
	msgs, _ := b.View(ViewOptions{})
	if got := render(msgs); got != want {
		t.Errorf("the branch's view:\n%s\n--- want ---\n%s", got, want)
	}
	if got := fileLines(t, s.Path()); !reflect.DeepEqual(got, before) {
		t.Errorf("branching changed the session it came from")
	}
	if msgs, _ := s.View(ViewOptions{}); len(msgs) != 4 {
		t.Errorf("the original's view has %d messages, want 4", len(msgs))
	}

	// A branch of the branch, at an entry the first branch inherited: it
	// points straight at the session that holds the entry.
	bb, err := st.Branch(b, w[0].UUID)
	if err != nil {
		t.Fatal(err)
	}
	if e := bb.Entries()[1]; e.Type != TypeBranch || e.FromSession != s.ID() {
		t.Errorf("the second branch points at %+v", e)
	}
	// And one at the first branch's own last entry.
	bbb, err := st.Branch(b, "")
	if err != nil {
		t.Fatal(err)
	}
	mustAppend(t, bbb, PromptEntry("three"))
	inMemory := bbb.Entries()
	b.Close()
	bb.Close()
	bbb.Close()

	// Opened again from disk, a branch has what it had: its own file, and
	// before it what it inherited, through both levels.
	again, err := st.Open(ctx, bbb.ID())
	if err != nil {
		t.Fatal(err)
	}
	defer again.Close()
	if len(again.Warnings()) != 0 || again.Inherited() != 5 || !reflect.DeepEqual(again.Entries(), inMemory) {
		t.Errorf("reopened with warnings %v, %d inherited, %d entries (want 5 and %d)", again.Warnings(), again.Inherited(), len(again.Entries()), len(inMemory))
	}
	msgs, _ = again.View(ViewOptions{})
	if got := render(msgs); got != want+"\nU: three" {
		t.Errorf("the reopened branch's view:\n%s", got)
	}
	// What a branch appends follows its own file's last entry.
	e := mustAppend(t, again, PromptEntry("four"))[0]
	if e.ParentUUID != inMemory[len(inMemory)-1].UUID || e.SessionID != bbb.ID() {
		t.Errorf("the branch's next entry was written as %+v", e)
	}

	// Branching refuses what it cannot point at.
	if _, err := st.Branch(s, "no-such-entry"); err == nil {
		t.Errorf("branched at an entry the session does not hold")
	}
	empty, _ := st.Create(cwd)
	if _, err := st.Branch(empty, ""); err == nil {
		t.Errorf("branched a session with no entries")
	}
}

func TestBranchWhoseParentIsGone(t *testing.T) {
	st, _ := newStore(t)
	s, _ := st.Create(cwd)
	w := mustAppend(t, s, PromptEntry("one"), AssistantEntry(core.AssistantText("answer one"), core.StopEndTurn))
	b, _ := st.Branch(s, "")
	mustAppend(t, b, PromptEntry("two"))
	s.Close()
	b.Close()

	check := func(what string) {
		t.Helper()
		again, err := st.OpenReadOnly(ctx, b.ID())
		if err != nil {
			t.Fatalf("%s: %v", what, err)
		}
		warns := again.Warnings()
		if len(warns) == 0 || warns[len(warns)-1].Kind != WarnBranch || !strings.Contains(warns[len(warns)-1].Detail, s.ID()) {
			t.Errorf("%s: Warnings = %v", what, warns)
		}
		if again.Inherited() != 0 || len(again.Entries()) != 2 {
			t.Errorf("%s: %d inherited, %d entries; want only the branch's own two", what, again.Inherited(), len(again.Entries()))
		}
		msgs, _ := mustView(t, again.Entries(), ViewOptions{})
		if got := render(msgs); got != "U: two" {
			t.Errorf("%s: view: %s", what, got)
		}
	}

	// The parent no longer holds the entry the branch points at.
	if err := os.WriteFile(s.Path(), []byte(fileLines(t, s.Path())[0]+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	_ = w
	check("with the entry gone from the parent")
	if err := os.Remove(s.Path()); err != nil {
		t.Fatal(err)
	}
	check("with the parent gone")
}

func TestTornLastLine(t *testing.T) {
	st, _ := newStore(t)
	s, _ := st.Create(cwd)
	w := mustAppend(t, s, PromptEntry("one"), AssistantEntry(core.AssistantText("answer one"), core.StopEndTurn))
	s.Close()
	// gnosh dies in the middle of writing a third entry.
	whole, _ := (&Entry{Type: TypeUser, UUID: "torn", SessionID: s.ID(), Message: core.UserText("two")}).line()
	f, err := os.OpenFile(s.Path(), os.O_WRONLY|os.O_APPEND, 0)
	if err != nil {
		t.Fatal(err)
	}
	f.Write(whole[:len(whole)/2])
	f.Close()

	s2, err := st.Open(ctx, s.ID())
	if err != nil {
		t.Fatalf("a torn last line stopped the session from opening: %v", err)
	}
	warns := s2.Warnings()
	if len(warns) != 1 || warns[0].Kind != WarnTornTail || warns[0].Line != 3 || warns[0].Path != s.Path() {
		t.Errorf("Warnings = %+v, want one torn tail on line 3", warns)
	}
	if !strings.Contains(warns[0].String(), "line 3") {
		t.Errorf("the warning reads %q", warns[0].String())
	}
	if !reflect.DeepEqual(s2.Entries(), w) {
		t.Errorf("read %d entries, want the two whole ones", len(s2.Entries()))
	}

	// The next entry starts on a line of its own, after the entry before.
	next := mustAppend(t, s2, PromptEntry("two, again"))[0]
	if next.ParentUUID != w[1].UUID {
		t.Errorf("the entry after the tear has the parent %q", next.ParentUUID)
	}
	s2.Close()
	lines := fileLines(t, s.Path())
	if len(lines) != 4 || lines[2] != string(whole[:len(whole)/2]) || !strings.HasPrefix(lines[3], `{"type":"user"`) {
		t.Errorf("the file's lines after the tear:\n%s", strings.Join(lines, "\n"))
	}

	// From now on the torn line is a corrupt line in the middle.
	s3, err := st.OpenReadOnly(ctx, s.ID())
	if err != nil {
		t.Fatal(err)
	}
	warns = s3.Warnings()
	if len(warns) != 1 || warns[0].Kind != WarnCorruptLine || warns[0].Line != 3 || len(s3.Entries()) != 3 {
		t.Errorf("Warnings = %+v with %d entries, want one corrupt line 3 and three entries", warns, len(s3.Entries()))
	}
}

func TestLastLineWholeButUnterminated(t *testing.T) {
	st, _ := newStore(t)
	const id = "0193267a-3333-7000-8000-000000000003"
	one, _ := (&Entry{Type: TypeUser, UUID: "u1", SessionID: id, Cwd: cwd, Source: SourcePrompt, Message: core.UserText("one")}).line()
	path := writeSession(t, st, id, string(one)) // no line break at the end
	s, err := st.Open(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if len(s.Warnings()) != 0 || len(s.Entries()) != 1 {
		t.Errorf("Warnings = %v, %d entries; want the one entry and no warning", s.Warnings(), len(s.Entries()))
	}
	mustAppend(t, s, PromptEntry("two"))
	s.Close()
	if lines := fileLines(t, path); len(lines) != 2 || lines[0] != string(one) {
		t.Errorf("the file's lines:\n%s", strings.Join(lines, "\n"))
	}
}

func TestCorruptMiddleLine(t *testing.T) {
	st, _ := newStore(t)
	const id = "0193267a-4444-7000-8000-000000000004"
	line := func(uuid, text string) string {
		l, _ := (&Entry{Type: TypeUser, UUID: uuid, SessionID: id, Cwd: cwd, Source: SourcePrompt, Message: core.UserText(text)}).line()
		return string(l)
	}
	writeSession(t, st, id, strings.Join([]string{
		line("u1", "one"),
		`{"type":"user","uuid":"u2","message":{"role":"us`, // cut short, mid-file
		"", // a blank line is passed over without a word
		`["not","an","entry"]`,
		line("u5", "five"),
	}, "\n")+"\n")
	s, err := st.OpenReadOnly(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, e := range s.Entries() {
		got = append(got, e.UUID)
	}
	if !reflect.DeepEqual(got, []string{"u1", "u5"}) {
		t.Errorf("read the entries %v, want u1 and u5", got)
	}
	warns := s.Warnings()
	if len(warns) != 2 || warns[0].Kind != WarnCorruptLine || warns[0].Line != 2 || warns[1].Kind != WarnCorruptLine || warns[1].Line != 4 {
		t.Fatalf("Warnings = %+v, want corrupt lines 2 and 4", warns)
	}
	if want := int64(len(line("u1", "one")) + 1); warns[0].Offset != want {
		t.Errorf("the first warning is at byte %d, want %d", warns[0].Offset, want)
	}
}

func TestStreamingRead(t *testing.T) {
	st, _ := newStore(t)
	s, _ := st.Create(cwd)
	written := mustAppend(t, s, oneOfEach()...)
	s.Close()

	var got []Entry
	for e, err := range ReadFile(ctx, s.Path(), nil) {
		if err != nil {
			t.Fatal(err)
		}
		got = append(got, e)
	}
	if !reflect.DeepEqual(got, written) {
		t.Errorf("streaming gave %d entries that differ from the %d written", len(got), len(written))
	}

	// Stopping early is fine, and so is a line far longer than the buffer.
	big := strings.Repeat("0123456789", 100_000)
	data := jsonl(t, []Entry{PromptEntry(big), PromptEntry("small")})
	n := 0
	for e, err := range Read(ctx, bytes.NewReader(data), nil) {
		if err != nil || e.Message.Text() != big {
			t.Fatalf("the long line was read wrong (err %v)", err)
		}
		n++
		break
	}
	if n != 1 {
		t.Errorf("read %d entries before stopping", n)
	}

	// A file that is not there, and a context that is cancelled, end the
	// sequence with the error.
	for _, err := range ReadFile(ctx, filepath.Join(st.Dir, "nope.jsonl"), nil) {
		if !errors.Is(err, fs.ErrNotExist) {
			t.Errorf("reading a missing file: %v", err)
		}
	}
	stopped, cancel := context.WithCancel(ctx)
	cancel()
	n = 0
	for _, err := range ReadFile(stopped, s.Path(), nil) {
		n++
		if !errors.Is(err, context.Canceled) {
			t.Errorf("reading with a cancelled context: %v", err)
		}
	}
	if n != 1 {
		t.Errorf("a cancelled read yielded %d times, want once with the error", n)
	}
	if _, err := st.Open(stopped, s.ID()); !errors.Is(err, context.Canceled) {
		t.Errorf("Open with a cancelled context: %v", err)
	}
	if _, err := st.List(stopped, cwd); !errors.Is(err, context.Canceled) {
		t.Errorf("List with a cancelled context: %v", err)
	}
}

func TestOpenErrors(t *testing.T) {
	st, _ := newStore(t)
	if _, err := st.Open(ctx, "0193267a"); !errors.Is(err, ErrNotFound) {
		t.Errorf("Open in an empty store = %v, want ErrNotFound", err)
	}
	const a, b = "0193267a-aaaa-7000-8000-000000000001", "0193267a-bbbb-7000-8000-000000000002"
	writeSession(t, st, a, "")
	writeSession(t, st, b, "")
	for _, id := range []string{"", "ffff", "../../etc/passwd", "0193267A", a + "0"} {
		if _, err := st.Open(ctx, id); !errors.Is(err, ErrNotFound) {
			t.Errorf("Open(%q) = %v, want ErrNotFound", id, err)
		}
	}
	_, err := st.OpenReadOnly(ctx, "0193267a")
	var amb *AmbiguousError
	if !errors.As(err, &amb) || !reflect.DeepEqual(amb.IDs, []string{a, b}) || !strings.Contains(err.Error(), a) {
		t.Errorf("Open by a start that fits two = %v, want an *AmbiguousError naming both", err)
	}
	if path, err := st.Path("0193267a-b"); err != nil || filepath.Base(path) != b+".jsonl" {
		t.Errorf("Path = %q, %v", path, err)
	}
	s, err := st.Open(ctx, a)
	if err != nil || len(s.Entries()) != 0 {
		t.Fatalf("opening an empty file: %v", err)
	}
	s.Close()

	// OpenPath opens a file wherever it is.
	loose := filepath.Join(t.TempDir(), "saved.jsonl")
	os.WriteFile(loose, jsonl(t, script(t, "prompt hi")), 0o600)
	s, err = st.OpenPath(ctx, loose, true)
	if err != nil || s.ID() != "saved" || len(s.Entries()) != 1 {
		t.Errorf("OpenPath = %v", err)
	}
	if _, err := st.OpenPath(ctx, filepath.Join(st.Dir, "nope.jsonl"), true); !errors.Is(err, ErrNotFound) {
		t.Errorf("OpenPath of a missing file = %v, want ErrNotFound", err)
	}
}

func TestCreate(t *testing.T) {
	st, _ := newStore(t)
	if _, err := st.Create("relative/dir"); err == nil {
		t.Errorf("a session was created in a relative working directory")
	}
	const id = "0193267a-5555-4000-8000-000000000005"
	s, err := st.CreateWithID(cwd, id)
	if err != nil {
		t.Fatal(err)
	}
	if s.ID() != id {
		t.Errorf("ID = %q", s.ID())
	}
	mustAppend(t, s, PromptEntry("hi"))
	s.Close()
	if _, err := st.CreateWithID("/other/dir", id); err == nil || !strings.Contains(err.Error(), "exists already") {
		t.Errorf("creating a session with an id in use = %v", err)
	}
	for _, bad := range []string{"", "abc", strings.ToUpper(id), id + "0", "../" + id} {
		if _, err := st.CreateWithID(cwd, bad); err == nil {
			t.Errorf("CreateWithID(%q) succeeded", bad)
		}
	}
	// Two sessions made with one id before either writes: the second to
	// write is refused, since the file is there by then.
	const twin = "0193267a-6666-4000-8000-000000000006"
	s1, _ := st.CreateWithID(cwd, twin)
	s2, _ := st.CreateWithID(cwd, twin)
	mustAppend(t, s1, PromptEntry("first"))
	if _, err := s2.Append(PromptEntry("second")); !errors.Is(err, fs.ErrExist) {
		t.Errorf("the second writer of a new id got %v, want an error that the file exists", err)
	}
	s1.Close()
}

func TestListAndLatest(t *testing.T) {
	st, clock := newStore(t)
	if list, err := st.List(ctx, cwd); err != nil || len(list) != 0 {
		t.Errorf("List of a directory with no sessions = %v, %v", list, err)
	}
	if _, ok, err := st.Latest(ctx, cwd); ok || err != nil {
		t.Errorf("Latest with no sessions = %v, %v", ok, err)
	}

	// Three sessions in one directory, one in a directory whose slug is
	// the same, one in another directory, and one that never wrote a line.
	a, _ := st.Create("/work/app")
	mustAppend(t, a,
		UserEntry(core.UserText("a hook spoke first"), SourceHook),
		PromptEntry("\n  fix the flaky test  \nit fails one run in ten"),
		AssistantEntry(core.AssistantText("on it"), core.StopEndTurn),
		TitleEntry("Fix flaky test", SourceAuto),
	)
	b, _ := st.Create("/work/app")
	mustAppend(t, b,
		PromptEntry("rename the port flag"),
		TitleEntry("Port flag", SourceAuto),
		TitleEntry("my own name", SourceUser),
		TitleEntry("Renaming a flag", SourceAuto), // a late automatic title
	)
	c, _ := st.Create("/work/app")
	mustAppend(t, c, PromptEntry(strings.Repeat("é", 300)))
	twin, _ := st.Create("/work-app")
	mustAppend(t, twin, PromptEntry("in the other directory with this slug"))
	other, _ := st.Create("/work/other")
	mustAppend(t, other, PromptEntry("elsewhere"))
	unused, _ := st.Create("/work/app")
	// a is written to last of all.
	clock.t = clock.t.Add(time.Hour)
	last := mustAppend(t, a, PromptEntry("and one more thing"))[0]
	for _, s := range []*Session{a, b, c, twin, other, unused} {
		s.Close()
	}
	if filepath.Dir(twin.Path()) != filepath.Dir(a.Path()) {
		t.Fatalf("/work-app and /work/app were meant to share a slug")
	}

	list, err := st.List(ctx, "/work/app/")
	if err != nil {
		t.Fatal(err)
	}
	var ids []string
	for _, info := range list {
		ids = append(ids, info.ID)
	}
	if want := []string{a.ID(), c.ID(), b.ID()}; !reflect.DeepEqual(ids, want) {
		t.Fatalf("List = %v, want the three sessions of /work/app, newest first: %v", ids, want)
	}
	ia, ic, ib := list[0], list[1], list[2]
	if ia.Title != "Fix flaky test" || ia.FirstPrompt != "fix the flaky test" || ia.Entries != 5 || ia.Messages != 4 || ia.Prompts != 2 {
		t.Errorf("a = %+v", ia)
	}
	if ia.Cwd != "/work/app" || ia.Path != a.Path() || !ia.Modified.Equal(last.Timestamp) || !ia.Created.Equal(a.Entries()[0].Timestamp) || ia.Size == 0 {
		t.Errorf("a = %+v", ia)
	}
	if ib.Title != "my own name" {
		t.Errorf("a late automatic title replaced the name the person gave: %q", ib.Title)
	}
	if ic.Title != "" || ic.FirstPrompt != strings.Repeat("é", 256) {
		t.Errorf("c = %+v", ic)
	}

	if list, _ := st.List(ctx, "/work-app"); len(list) != 1 || list[0].ID != twin.ID() {
		t.Errorf("List of /work-app = %+v, want its one session", list)
	}
	latest, ok, err := st.Latest(ctx, "/work/app")
	if err != nil || !ok || latest.ID != a.ID() {
		t.Errorf("Latest = %v, %v, %v; want %s", latest.ID, ok, err, a.ID())
	}

	// A branch says where it came from.
	ro, _ := st.OpenReadOnly(ctx, b.ID())
	br, err := st.Branch(ro, "")
	if err != nil {
		t.Fatal(err)
	}
	br.Close()
	latest, _, _ = st.Latest(ctx, "/work/app")
	if latest.ID != br.ID() || latest.BranchedFrom != b.ID() || latest.Entries != 1 {
		t.Errorf("after a branch, Latest = %+v", latest)
	}
}

func TestSlug(t *testing.T) {
	long := "/" + strings.Repeat("a/", 150) + "x"
	tests := []struct{ cwd, want string }{
		{"/Users/jae/src/gnosh", "-Users-jae-src-gnosh"},
		{"/work/app/", "-work-app"},
		{"/work/./app/../app", "-work-app"},
		{"/", "-"},
		{"/work/my project (v2)", "-work-my-project--v2-"},
		{"/with.dots/.hidden/file_name-1", "-with.dots-.hidden-file_name-1"},
		{"/home/日本語/été", "-home-日本語-été"},
		{"/tmp/tab\there\nnew\x00line", "-tmp-tab-here-new-line"},
		{"/x/\xffy", "-x--y"},
		{"/a/b-c", "-a-b-c"},
		{"/a/b/c", "-a-b-c"}, // the same slug: List tells them apart
		{"", "-."},
		{"..", "-.."},
		{"relative/dir", "relative-dir"},
		{long, strings.Repeat("-a", 91) + "--" + "e3c4b1a2f0a4c1d5"},
	}
	for _, tt := range tests {
		got := Slug(tt.cwd)
		if tt.cwd == long {
			// The hash is whatever SHA-256 says; check the form.
			if len(got) != 200 || got[:183] != strings.Repeat("-a", 91)+"-" || got[183] != '-' || !regexp.MustCompile(`^[0-9a-f]{16}$`).MatchString(got[184:]) {
				t.Errorf("Slug of a 301-byte path = %q (%d bytes)", got, len(got))
			}
			continue
		}
		if got != tt.want {
			t.Errorf("Slug(%q) = %q, want %q", tt.cwd, got, tt.want)
		}
	}
	// Two long paths that begin alike get slugs of their own.
	if Slug(long) == Slug(long+"y") {
		t.Errorf("two long paths share the slug %q", Slug(long))
	}
	// A long path of multi-byte characters is cut between characters.
	if got := Slug("/" + strings.Repeat("日本", 60)); len(got) > 200 || !strings.HasPrefix(got, "-日本") || strings.ContainsRune(got, '\uFFFD') {
		t.Errorf("Slug of a long path of wide characters = %q", got)
	}
	// Whatever the path, the slug is one safe path element.
	for _, cwd := range []string{".", "/..", "///", "\\", "/a\\b", "/tmp/" + strings.Repeat("\xff", 500), strings.Repeat("/", 300), "/é/" + strings.Repeat("é", 250)} {
		got := Slug(cwd)
		if got == "" || got == "." || got == ".." || len(got) > 200 || strings.ContainsAny(got, "/\\\x00") || filepath.Base(got) != got {
			t.Errorf("Slug(%q) = %q is not a safe directory name", cwd, got)
		}
	}
}

func TestDataDir(t *testing.T) {
	env := func(vars map[string]string) func(string) string {
		return func(name string) string { return vars[name] }
	}
	tests := []struct {
		xdg, home, want string
	}{
		{"/data", "/home/jae", "/data/gnosh"},
		{"", "/home/jae", "/home/jae/.local/share/gnosh"},
		{"relative/data", "/home/jae", "/home/jae/.local/share/gnosh"},
		{"/data/", "", "/data/gnosh"},
	}
	for _, tt := range tests {
		got, err := DataDir(env(map[string]string{"XDG_DATA_HOME": tt.xdg}), tt.home)
		if err != nil || got != tt.want {
			t.Errorf("DataDir(XDG_DATA_HOME=%q, home %q) = %q, %v; want %q", tt.xdg, tt.home, got, err, tt.want)
		}
	}
	if _, err := DataDir(env(nil), ""); err == nil {
		t.Errorf("DataDir with nothing to go on gave no error")
	}
	if got, err := DataDir(nil, "/home/jae"); err != nil || got != "/home/jae/.local/share/gnosh" {
		t.Errorf("DataDir with no environment = %q, %v", got, err)
	}
}

func TestIDs(t *testing.T) {
	var g idGen
	at := time.Date(2026, 11, 13, 8, 0, 0, 500_000, time.UTC) // half a millisecond in
	seen := map[string]bool{}
	prev := ""
	for i := range 5000 {
		// The clock stands still, then steps back: the ids still sort.
		now := at
		if i > 2500 {
			now = at.Add(-time.Hour)
		}
		id, err := g.next(now, nil)
		if err != nil {
			t.Fatal(err)
		}
		if !validID(id) || id[14] != '7' || !strings.ContainsRune("89ab", rune(id[19])) {
			t.Fatalf("%q is not a UUIDv7", id)
		}
		if seen[id] || id <= prev {
			t.Fatalf("id %d, %q, does not sort after %q", i, id, prev)
		}
		seen[id], prev = true, id
	}
	first, _ := (&idGen{}).next(at, nil)
	if got, ok := idTime(first); !ok || !got.Equal(at.Truncate(time.Millisecond)) {
		t.Errorf("idTime(%q) = %v, %v; want %v", first, got, ok, at.Truncate(time.Millisecond))
	}
	if first[15:18] != "800" {
		t.Errorf("half a millisecond in, the id's sub-millisecond bits are %q, want 800", first[15:18])
	}
	if _, ok := idTime("0193267a-5555-4000-8000-000000000005"); ok {
		t.Errorf("a version 4 UUID was read as a time")
	}
	if _, err := g.next(at, strings.NewReader("short")); err == nil {
		t.Errorf("an id was made without enough random bytes")
	}
	for _, bad := range []string{"", "0193267a-5555-7000-8000-00000000000", "0193267a-5555-7000-8000-00000000000g", "0193267a55557000800000000000000005xx", "0193267A-5555-7000-8000-000000000005"} {
		if validID(bad) {
			t.Errorf("validID(%q) is true", bad)
		}
	}
}

// Move puts a session with another folder's, as /cd does: its file goes
// under that folder's project directory, a meta entry says so, and the
// session is listed with that folder's sessions from then on, with new
// entries stamped with it. A branch of it is listed there too.
// A session whose file a move put in another folder's directory, cut short
// before the move was written down (gnosh killed in the middle of /cd), is
// listed with the sessions of the folders of that directory: it was listed
// with none (the session store review of 2026-11-19). A session of another
// folder whose name comes to the same directory is still that folder's
// alone.
func TestListAMoveCutShort(t *testing.T) {
	st, _ := newStore(t)
	s, _ := st.Create("/work/app")
	mustAppend(t, s, PromptEntry("fix the flaky test"))
	s.Close()
	to := filepath.Join(st.ProjectDir("/work/lib"), s.ID()+".jsonl")
	if err := os.MkdirAll(filepath.Dir(to), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(s.Path(), to); err != nil {
		t.Fatal(err)
	}
	if Slug("/work/lib") != Slug("/work-lib") {
		t.Fatalf("the test needs two folders of one directory: %s, %s", Slug("/work/lib"), Slug("/work-lib"))
	}
	twin, _ := st.Create("/work-lib")
	mustAppend(t, twin, PromptEntry("the twin's"))
	twin.Close()
	ids := func(dir string) []string {
		list, err := st.List(ctx, dir)
		if err != nil {
			t.Fatal(err)
		}
		var out []string
		for _, info := range list {
			out = append(out, info.ID)
		}
		return out
	}
	if got := ids("/work/lib"); !slices.Equal(got, []string{s.ID()}) {
		t.Errorf("List(/work/lib) = %v, want the session whose move was cut short alone", got)
	}
	if got := ids("/work-lib"); !slices.Contains(got, twin.ID()) {
		t.Errorf("List(/work-lib) = %v, want its own session in it", got)
	}
	if got := ids("/work/app"); len(got) != 0 {
		t.Errorf("List(/work/app) = %v, want none", got)
	}
}

func TestMove(t *testing.T) {
	st, _ := newStore(t)
	s, _ := st.Create("/work/app")
	mustAppend(t, s, PromptEntry("fix the flaky test"))
	// The shell went into a folder below; the session is still the app's.
	s.SetCwd("/work/app/sub")
	mustAppend(t, s, AssistantEntry(core.AssistantText("on it"), core.StopEndTurn))
	old := s.Path()

	if err := s.Move("/work/lib"); err != nil {
		t.Fatal(err)
	}
	if s.Path() == old || filepath.Dir(s.Path()) != st.ProjectDir("/work/lib") {
		t.Errorf("the file is at %s, want it under %s", s.Path(), st.ProjectDir("/work/lib"))
	}
	if _, err := os.Stat(old); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("the file is still at %s: %v", old, err)
	}
	if s.Cwd() != "/work/lib" {
		t.Errorf("new entries are stamped with %s", s.Cwd())
	}
	mustAppend(t, s, PromptEntry("now the lib"))
	es := s.Entries()
	if m := es[len(es)-2]; m.Type != TypeMeta || m.Key != MovedKey || m.Value != "/work/lib" || m.Cwd != "/work/lib" {
		t.Errorf("the move was recorded as %+v", m)
	}
	if got := es[len(es)-1].Cwd; got != "/work/lib" {
		t.Errorf("an entry after the move is stamped with %s", got)
	}

	// Branched now, the copy is one of the lib's sessions too.
	br, err := st.Branch(s, "")
	if err != nil {
		t.Fatal(err)
	}
	s.Close()
	br.Close()
	for dir, want := range map[string]int{"/work/lib": 2, "/work/app": 0, "/work/app/sub": 0} {
		list, err := st.List(ctx, dir)
		if err != nil || len(list) != want {
			t.Errorf("List(%s) = %d sessions (%v), want %d", dir, len(list), err, want)
		}
	}
	// It opens by its id wherever it is, and goes on in the lib.
	again, err := st.Open(ctx, s.ID())
	if err != nil {
		t.Fatal(err)
	}
	if again.Path() != s.Path() || again.Cwd() != "/work/lib" || again.StartCwd() != "/work/app" {
		t.Errorf("opened again: path %s, cwd %s, start %s", again.Path(), again.Cwd(), again.StartCwd())
	}

	// A file of the same id where it would go stops a move, and nothing
	// moves; so does a folder that is not absolute.
	if err := os.MkdirAll(st.ProjectDir("/work/third"), 0o700); err != nil {
		t.Fatal(err)
	}
	clash := filepath.Join(st.ProjectDir("/work/third"), again.ID()+".jsonl")
	if err := os.WriteFile(clash, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := again.Move("/work/third"); err == nil || !strings.Contains(err.Error(), "is there already") {
		t.Errorf("a move onto a file of the same id: %v", err)
	}
	if err := again.Move("work/third"); err == nil {
		t.Error("a move to a relative folder was made")
	}
	if again.Path() != s.Path() || again.Cwd() != "/work/lib" {
		t.Errorf("a refused move changed the session: %s, %s", again.Path(), again.Cwd())
	}
	again.Close()

	// A session that wrote nothing makes its file in the folder it moved to.
	fresh, _ := st.Create("/work/app")
	if err := fresh.Move("/work/lib"); err != nil {
		t.Fatal(err)
	}
	mustAppend(t, fresh, PromptEntry("first words"))
	fresh.Close()
	if filepath.Dir(fresh.Path()) != st.ProjectDir("/work/lib") || fresh.StartCwd() != "/work/lib" {
		t.Errorf("a fresh session moved to %s, starting in %s", fresh.Path(), fresh.StartCwd())
	}
	if list, _ := st.List(ctx, "/work/lib"); len(list) != 3 {
		t.Errorf("the lib lists %d sessions, want 3", len(list))
	}

	// A read-only session does not move.
	ro, err := st.OpenReadOnly(ctx, s.ID())
	if err != nil {
		t.Fatal(err)
	}
	if err := ro.Move("/work/app"); !errors.Is(err, ErrReadOnly) {
		t.Errorf("a read-only session moved: %v", err)
	}
}
