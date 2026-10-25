package tools

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"charm.land/fantasy"
	"github.com/charmbracelet/crush/internal/db"
	"github.com/charmbracelet/crush/internal/filetracker"
	"github.com/stretchr/testify/require"
)

type mockEditFileTracker struct {
	lastRead  time.Time
	reads     []string
	unchanged bool
}

func (m *mockEditFileTracker) RecordRead(ctx context.Context, sessionID, path string) {
	m.reads = append(m.reads, path)
}

func (m *mockEditFileTracker) LastReadTime(ctx context.Context, sessionID, path string) time.Time {
	return m.lastRead
}

func (m *mockEditFileTracker) UnchangedSinceRead(ctx context.Context, sessionID, path string) bool {
	return m.unchanged
}

func (m *mockEditFileTracker) ListReadFiles(ctx context.Context, sessionID string) ([]string, error) {
	return m.reads, nil
}

func (m *mockEditFileTracker) CopyReads(ctx context.Context, from, to string) error {
	return nil
}

func (m *mockEditFileTracker) ForgetReadsSince(ctx context.Context, sessionID string, since int64) error {
	return nil
}

func TestReplaceContentPreservesCRLFAndMetadata(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	filePath := filepath.Join(dir, "test.txt")
	require.NoError(t, os.WriteFile(filePath, []byte("alpha\r\nbeta\r\n"), 0o644))

	tracker := &mockEditFileTracker{lastRead: time.Now().Add(time.Second)}
	edit := editContext{
		ctx:         context.WithValue(t.Context(), SessionIDContextKey, "session"),
		permissions: &mockPermissionService{},
		files:       &mockHistoryService{},
		filetracker: tracker,
		workingDir:  dir,
	}

	resp, err := replaceContent(edit, filePath, "beta", "BETA", false, fantasy.ToolCall{ID: "call"})
	require.NoError(t, err)
	require.False(t, resp.IsError)
	require.Equal(t, "Content replaced in file: "+filePath+fileStateCurrent, resp.Content)

	content, err := os.ReadFile(filePath)
	require.NoError(t, err)
	require.Equal(t, "alpha\r\nBETA\r\n", string(content))
	require.Equal(t, []string{filePath}, tracker.reads)

	var meta EditResponseMetadata
	require.NoError(t, json.Unmarshal([]byte(resp.Metadata), &meta))
	require.Equal(t, "alpha\nbeta\n", meta.OldContent)
	require.Equal(t, "alpha\r\nBETA\r\n", meta.NewContent)
	require.True(t, meta.CRLF, "for /rewind to put the line endings back")
}

func TestDeleteContentRejectsMultipleMatchesWithoutReplaceAll(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	filePath := filepath.Join(dir, "test.txt")
	require.NoError(t, os.WriteFile(filePath, []byte("alpha\nbeta\nalpha\n"), 0o644))

	edit := editContext{
		ctx:         context.WithValue(t.Context(), SessionIDContextKey, "session"),
		permissions: &mockPermissionService{},
		files:       &mockHistoryService{},
		filetracker: &mockEditFileTracker{lastRead: time.Now().Add(time.Second)},
		workingDir:  dir,
	}

	resp, err := deleteContent(edit, filePath, "alpha\n", false, fantasy.ToolCall{ID: "call"})
	require.NoError(t, err)
	require.True(t, resp.IsError)
	require.Contains(t, resp.Content, "old_string appears multiple times")

	content, err := os.ReadFile(filePath)
	require.NoError(t, err)
	require.Equal(t, "alpha\nbeta\nalpha\n", string(content))
}

// A file changed since the agent last read it, often by a command the agent
// itself ran, is edited only where the edit matches it as it is now exactly
// and once, as in Claude Code, and the result says the file holds other
// changes. A looser match, whitespace-forgiving or one of several, is
// refused, saying what to do; so is any edit of such a file outside the
// working directory, which the agent may not read without asking.
func TestReplaceContentInAFileChangedSinceItWasRead(t *testing.T) {
	t.Parallel()

	stale := func(t *testing.T, content, workingDir string) (editContext, string) {
		dir := t.TempDir()
		filePath := filepath.Join(dir, "test.txt")
		require.NoError(t, os.WriteFile(filePath, []byte(content), 0o644))
		if workingDir == "" {
			workingDir = dir
		}
		return editContext{
			ctx:         context.WithValue(t.Context(), SessionIDContextKey, "session"),
			permissions: &mockPermissionService{},
			files:       &mockHistoryService{},
			filetracker: &mockEditFileTracker{lastRead: time.Now().Add(-time.Hour)},
			workingDir:  workingDir,
		}, filePath
	}
	refused := func(t *testing.T, resp fantasy.ToolResponse, filePath, content string) {
		t.Helper()
		require.True(t, resp.IsError)
		require.Contains(t, resp.Content, "has been modified since it was last read")
		require.Contains(t, resp.Content, "Read it again with the view tool before editing it")
		got, err := os.ReadFile(filePath)
		require.NoError(t, err)
		require.Equal(t, content, string(got), "the file is left as it is")
	}

	t.Run("exact and once", func(t *testing.T) {
		edit, filePath := stale(t, "alpha\nbeta\n", "")
		resp, err := replaceContent(edit, filePath, "alpha", "ALPHA", false, fantasy.ToolCall{ID: "call"})
		require.NoError(t, err)
		require.False(t, resp.IsError, resp.Content)
		require.Equal(t, "Content replaced in file: "+filePath+"\n"+staleEditNote, resp.Content)
		got, err := os.ReadFile(filePath)
		require.NoError(t, err)
		require.Equal(t, "ALPHA\nbeta\n", string(got))
		require.Empty(t, edit.filetracker.(*mockEditFileTracker).reads,
			"the file holds changes the agent has not seen, so it is not read: a write of it is still refused")
	})
	t.Run("replace_all", func(t *testing.T) {
		edit, filePath := stale(t, "alpha\nalpha\n", "")
		resp, err := replaceContent(edit, filePath, "alpha", "ALPHA", true, fantasy.ToolCall{ID: "call"})
		require.NoError(t, err)
		require.False(t, resp.IsError, resp.Content)
		require.Contains(t, resp.Content, staleEditNote)
	})
	t.Run("deleted exactly", func(t *testing.T) {
		edit, filePath := stale(t, "alpha\nbeta\n", "")
		resp, err := deleteContent(edit, filePath, "beta\n", false, fantasy.ToolCall{ID: "call"})
		require.NoError(t, err)
		require.False(t, resp.IsError, resp.Content)
		require.Contains(t, resp.Content, staleEditNote)
	})
	t.Run("only after whitespace", func(t *testing.T) {
		edit, filePath := stale(t, "\tif x {\n\t\treturn\n\t}\n", "")
		resp, err := replaceContent(edit, filePath, "  if x {\n    return\n  }", "  if y {\n    return\n  }", false, fantasy.ToolCall{ID: "call"})
		require.NoError(t, err)
		refused(t, resp, filePath, "\tif x {\n\t\treturn\n\t}\n")
	})
	t.Run("more than once", func(t *testing.T) {
		edit, filePath := stale(t, "alpha\nalpha\n", "")
		resp, err := replaceContent(edit, filePath, "alpha", "ALPHA", false, fantasy.ToolCall{ID: "call"})
		require.NoError(t, err)
		refused(t, resp, filePath, "alpha\nalpha\n")
	})
	t.Run("not there", func(t *testing.T) {
		edit, filePath := stale(t, "alpha\n", "")
		resp, err := replaceContent(edit, filePath, "gamma", "GAMMA", false, fantasy.ToolCall{ID: "call"})
		require.NoError(t, err)
		refused(t, resp, filePath, "alpha\n")
	})
	t.Run("outside the working directory", func(t *testing.T) {
		edit, filePath := stale(t, "alpha\n", t.TempDir())
		resp, err := replaceContent(edit, filePath, "alpha", "ALPHA", false, fantasy.ToolCall{ID: "call"})
		require.NoError(t, err)
		refused(t, resp, filePath, "alpha\n")
	})
}

// A multi-edit of a file changed since the agent read it is made only if
// every edit matches the file as it is now exactly; if one does not, none
// is made, rather than some edits of a file the agent has not seen.
func TestMultiEditInAFileChangedSinceItWasRead(t *testing.T) {
	t.Parallel()

	var tracker *mockEditFileTracker
	run := func(t *testing.T, edits ...MultiEditOperation) (fantasy.ToolResponse, string) {
		dir := t.TempDir()
		filePath := filepath.Join(dir, "test.txt")
		require.NoError(t, os.WriteFile(filePath, []byte("alpha\nbeta\n"), 0o644))
		tracker = &mockEditFileTracker{lastRead: time.Now().Add(-time.Hour)}
		edit := editContext{
			ctx:         context.WithValue(t.Context(), SessionIDContextKey, "session"),
			permissions: &mockPermissionService{},
			files:       &mockHistoryService{},
			filetracker: tracker,
			workingDir:  dir,
		}
		resp, err := processMultiEditExistingFile(edit, MultiEditParams{FilePath: filePath, Edits: edits}, fantasy.ToolCall{ID: "call"})
		require.NoError(t, err)
		got, err := os.ReadFile(filePath)
		require.NoError(t, err)
		return resp, string(got)
	}

	resp, got := run(t, MultiEditOperation{OldString: "alpha", NewString: "ALPHA"}, MultiEditOperation{OldString: "beta", NewString: "BETA"})
	require.False(t, resp.IsError, resp.Content)
	require.Empty(t, tracker.reads, "the file holds changes the agent has not seen, so it is not read")
	require.Contains(t, resp.Content, staleEditNote)
	require.Equal(t, "ALPHA\nBETA\n", got)

	resp, got = run(t, MultiEditOperation{OldString: "alpha", NewString: "ALPHA"}, MultiEditOperation{OldString: "gamma", NewString: "GAMMA"})
	require.True(t, resp.IsError)
	require.Contains(t, resp.Content, "has been modified since it was last read")
	require.Equal(t, "alpha\nbeta\n", got, "no edit is made")
}

// realTracker is the file tracker the app uses, over a fresh database
// holding the session to record reads in.
func realTracker(t *testing.T, sessionID string) filetracker.Service {
	t.Helper()
	conn, err := db.Connect(t.Context(), t.TempDir())
	require.NoError(t, err)
	t.Cleanup(func() { conn.Close() })
	q := db.New(conn)
	_, err = q.CreateSession(t.Context(), db.CreateSessionParams{ID: sessionID, Title: "test"})
	require.NoError(t, err)
	return filetracker.NewService(q)
}

// writeBack writes content to path with a modification time after every
// read so far, as a command does that rewrites the file.
func writeBack(t *testing.T, path, content string, at time.Time) {
	t.Helper()
	require.NoError(t, os.WriteFile(path, []byte(content), 0o644))
	require.NoError(t, os.Chtimes(path, at, at))
}

// A file a command wrote back as it was since the agent read it, as git
// stash and pop do, can be edited as usual; one whose content changed is
// edited only where the edit matches it exactly, with a note, and an edit
// written against the text the agent read is refused as stale. Run 13 had
// three edits refused, and read the files again, after a git stash and
// pop of its own.
func TestEditAfterAFileIsWrittenBackAsItWas(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	path := filepath.Join(dir, "a.go")
	require.NoError(t, os.WriteFile(path, []byte("package a\n\nvar x = 1\n"), 0o644))
	tracker := realTracker(t, "session")
	tracker.RecordRead(t.Context(), "session", path)
	edit := editContext{
		ctx:         context.WithValue(t.Context(), SessionIDContextKey, "session"),
		permissions: &mockPermissionService{},
		files:       &mockHistoryService{},
		filetracker: tracker,
		workingDir:  dir,
	}

	writeBack(t, path, "package a\n\nvar x = 1\n", time.Now().Add(time.Minute))
	resp, err := replaceContent(edit, path, "x = 1", "x = 2", false, fantasy.ToolCall{ID: "c1"})
	require.NoError(t, err)
	require.False(t, resp.IsError, resp.Content)
	require.NotContains(t, resp.Content, staleEditNote)

	writeBack(t, path, "package a\n\nvar x = 3\n", time.Now().Add(2*time.Minute))
	resp, err = replaceContent(edit, path, "x = 2", "x = 4", false, fantasy.ToolCall{ID: "c2"})
	require.NoError(t, err)
	require.True(t, resp.IsError)
	require.Contains(t, resp.Content, "modified since it was last read")

	resp, err = replaceContent(edit, path, "x = 3", "x = 4", false, fantasy.ToolCall{ID: "c3"})
	require.NoError(t, err)
	require.False(t, resp.IsError, resp.Content)
	require.Contains(t, resp.Content, staleEditNote)
	require.False(t, tracker.UnchangedSinceRead(t.Context(), "session", path),
		"after an edit of a changed file it still counts as changed since the agent read it")
}

// The edit tool gives a session the subdirectory's context files once it
// edits a file there; a failed edit adds nothing.
func TestEditToolAddsTheSubdirectoryContext(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	sub := filepath.Join(dir, "sub")
	require.NoError(t, os.Mkdir(sub, 0o755))
	path := filepath.Join(sub, "a.go")
	require.NoError(t, os.WriteFile(path, []byte("package sub\n"), 0o644))
	tracker := realTracker(t, "s1")
	tracker.RecordRead(t.Context(), "s1", path)
	ctx := context.WithValue(t.Context(), SessionIDContextKey, "s1")
	called := 0
	subctx := func(_ context.Context, sessionID, filePath string) string {
		called++
		return "\n<project_context>\nSUB RULES\n</project_context>\n"
	}
	tool := NewEditTool(nil, &mockPermissionService{}, &mockHistoryService{}, tracker, subctx, dir)

	marshal := func(p EditParams) string {
		t.Helper()
		b, err := json.Marshal(p)
		require.NoError(t, err)
		return string(b)
	}

	resp, err := tool.Run(ctx, fantasy.ToolCall{
		ID:    "call",
		Name:  EditToolName,
		Input: marshal(EditParams{FilePath: "sub/a.go", OldString: "package sub", NewString: "package sub2"}),
	})
	require.NoError(t, err)
	require.False(t, resp.IsError, resp.Content)
	require.Contains(t, resp.Content, "SUB RULES")
	require.Equal(t, 1, called)

	// A failed edit (no match) adds nothing.
	resp, err = tool.Run(ctx, fantasy.ToolCall{
		ID:    "call-failed",
		Name:  EditToolName,
		Input: marshal(EditParams{FilePath: "sub/a.go", OldString: "no such text", NewString: "x"}),
	})
	require.NoError(t, err)
	require.True(t, resp.IsError, resp.Content)
	require.NotContains(t, resp.Content, "SUB RULES")
	require.Equal(t, 1, called, "a failed edit adds nothing")
}

// An edit that deletes text from a file with Windows line endings, and a
// multiedit of one, say so beside the old content they record, which has
// Unix ones, for /rewind to put them back; an edit of a file with Unix
// line endings says it had none.
func TestEditsRecordWindowsLineEndings(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	edit := editContext{
		ctx:         context.WithValue(t.Context(), SessionIDContextKey, "session"),
		permissions: &mockPermissionService{},
		files:       &mockHistoryService{},
		filetracker: &mockEditFileTracker{lastRead: time.Now().Add(time.Second)},
		workingDir:  dir,
	}
	file := func(name, content string) string {
		path := filepath.Join(dir, name)
		require.NoError(t, os.WriteFile(path, []byte(content), 0o644))
		return path
	}
	meta := func(resp fantasy.ToolResponse) EditResponseMetadata {
		require.False(t, resp.IsError, resp.Content)
		var meta EditResponseMetadata
		require.NoError(t, json.Unmarshal([]byte(resp.Metadata), &meta))
		return meta
	}

	resp, err := deleteContent(edit, file("del.txt", "alpha\r\nbeta\r\n"), "beta", false, fantasy.ToolCall{ID: "call"})
	require.NoError(t, err)
	deleted := meta(resp)
	require.True(t, deleted.CRLF)
	require.Equal(t, "alpha\nbeta\n", deleted.OldContent)

	resp, err = processMultiEditExistingFile(edit, MultiEditParams{
		FilePath: file("multi.txt", "one\r\ntwo\r\n"),
		Edits:    []MultiEditOperation{{OldString: "two", NewString: "TWO"}},
	}, fantasy.ToolCall{ID: "call"})
	require.NoError(t, err)
	multi := meta(resp)
	require.True(t, multi.CRLF)
	require.Equal(t, "one\ntwo\n", multi.OldContent)

	resp, err = replaceContent(edit, file("unix.txt", "alpha\nbeta\n"), "beta", "BETA", false, fantasy.ToolCall{ID: "call"})
	require.NoError(t, err)
	require.False(t, meta(resp).CRLF)
}
