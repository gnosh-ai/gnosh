package tools

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"charm.land/fantasy"
	"github.com/stretchr/testify/require"
)

type mockFileTrackerService struct{}

func (m mockFileTrackerService) RecordRead(ctx context.Context, sessionID, path string) {}

func (m mockFileTrackerService) LastReadTime(ctx context.Context, sessionID, path string) time.Time {
	return time.Now()
}

func (m mockFileTrackerService) UnchangedSinceRead(ctx context.Context, sessionID, path string) bool {
	return false
}

func (m mockFileTrackerService) ListReadFiles(ctx context.Context, sessionID string) ([]string, error) {
	return nil, nil
}

func (m mockFileTrackerService) CopyReads(ctx context.Context, from, to string) error {
	return nil
}

func TestWriteToolWritesEmptyNewFile(t *testing.T) {
	t.Parallel()

	workingDir := t.TempDir()
	ctx := context.WithValue(context.Background(), SessionIDContextKey, "test-session")

	tool := NewWriteTool(nil, &mockPermissionService{}, &mockHistoryService{}, mockFileTrackerService{}, nil, workingDir)

	input, err := json.Marshal(WriteParams{FilePath: "empty.txt", Content: ""})
	require.NoError(t, err)

	resp, err := tool.Run(ctx, fantasy.ToolCall{
		ID:    "test-call",
		Name:  WriteToolName,
		Input: string(input),
	})
	require.NoError(t, err)
	require.False(t, resp.IsError)

	b, err := os.ReadFile(filepath.Join(workingDir, "empty.txt"))
	require.NoError(t, err)
	require.Equal(t, "", string(b))
}

// An existing file the agent never read is refused for that reason, not
// as one "modified since it was last read" in year 1, and is left alone.
func TestWriteToolRefusesAFileNeverRead(t *testing.T) {
	t.Parallel()

	workingDir := t.TempDir()
	path := filepath.Join(workingDir, "probe_test.go")
	require.NoError(t, os.WriteFile(path, []byte("package probe\n"), 0o644))
	ctx := context.WithValue(context.Background(), SessionIDContextKey, "test-session")
	tool := NewWriteTool(nil, &mockPermissionService{}, &mockHistoryService{}, &mockEditFileTracker{}, nil, workingDir)

	input, err := json.Marshal(WriteParams{FilePath: "probe_test.go", Content: "package other\n"})
	require.NoError(t, err)
	resp, err := tool.Run(ctx, fantasy.ToolCall{ID: "call", Name: WriteToolName, Input: string(input)})
	require.NoError(t, err)
	require.True(t, resp.IsError)
	require.Contains(t, resp.Content, "has not been read in this session")
	require.NotContains(t, resp.Content, "modified since")
	require.NotContains(t, resp.Content, "0001-01-01")

	b, err := os.ReadFile(path)
	require.NoError(t, err)
	require.Equal(t, "package probe\n", string(b))
}

// A file changed after the agent read it is refused with the time of each
// and the likely cause, as the edit tool does, and is left alone.
func TestWriteToolRefusesAFileChangedSinceItWasRead(t *testing.T) {
	t.Parallel()

	workingDir := t.TempDir()
	path := filepath.Join(workingDir, "notes.md")
	require.NoError(t, os.WriteFile(path, []byte("changed by a command\n"), 0o644))
	ctx := context.WithValue(context.Background(), SessionIDContextKey, "test-session")
	tracker := &mockEditFileTracker{lastRead: time.Now().Add(-time.Hour)}
	tool := NewWriteTool(nil, &mockPermissionService{}, &mockHistoryService{}, tracker, nil, workingDir)

	input, err := json.Marshal(WriteParams{FilePath: "notes.md", Content: "new\n"})
	require.NoError(t, err)
	resp, err := tool.Run(ctx, fantasy.ToolCall{ID: "call", Name: WriteToolName, Input: string(input)})
	require.NoError(t, err)
	require.True(t, resp.IsError)
	require.Contains(t, resp.Content, "has been modified since it was last read")
	require.Contains(t, resp.Content, "perhaps by a command you ran")

	b, err := os.ReadFile(path)
	require.NoError(t, err)
	require.Equal(t, "changed by a command\n", string(b))
}

// The write tool, too, writes over a file a command wrote back as it was
// since the agent read it, and refuses one whose content changed.
func TestWriteAfterAFileIsWrittenBackAsItWas(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	path := filepath.Join(dir, "a.go")
	require.NoError(t, os.WriteFile(path, []byte("package a\n"), 0o644))
	tracker := realTracker(t, "session")
	tracker.RecordRead(t.Context(), "session", path)
	ctx := context.WithValue(t.Context(), SessionIDContextKey, "session")
	tool := NewWriteTool(nil, &mockPermissionService{}, &mockHistoryService{}, tracker, nil, dir)
	write := func(content string) fantasy.ToolResponse {
		t.Helper()
		input, err := json.Marshal(WriteParams{FilePath: "a.go", Content: content})
		require.NoError(t, err)
		resp, err := tool.Run(ctx, fantasy.ToolCall{ID: "call", Name: WriteToolName, Input: string(input)})
		require.NoError(t, err)
		return resp
	}

	writeBack(t, path, "package a\n", time.Now().Add(time.Minute))
	resp := write("package b\n")
	require.False(t, resp.IsError, resp.Content)

	writeBack(t, path, "package c\n", time.Now().Add(2*time.Minute))
	resp = write("package d\n")
	require.True(t, resp.IsError)
	require.Contains(t, resp.Content, "modified since it was last read")
}

// A successful write of a new file in a subdirectory ends with the
// subdirectory's own context files, and a failed one adds nothing.
func TestWriteToolAddsTheSubdirectoryContext(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	sub := filepath.Join(dir, "sub")
	require.NoError(t, os.Mkdir(sub, 0o755))
	ctx := context.WithValue(t.Context(), SessionIDContextKey, "s1")
	called := 0
	subctx := func(_ context.Context, sessionID, filePath string) string {
		called++
		return "\n<project_context>\nSUB RULES\n</project_context>\n"
	}

	tool := NewWriteTool(nil, &mockPermissionService{}, &mockHistoryService{}, mockFileTrackerService{}, subctx, dir)

	marshal := func(p WriteParams) string {
		t.Helper()
		b, err := json.Marshal(p)
		require.NoError(t, err)
		return string(b)
	}

	resp, err := tool.Run(ctx, fantasy.ToolCall{
		ID:    "call",
		Name:  WriteToolName,
		Input: marshal(WriteParams{FilePath: "sub/new.go", Content: "package sub\n"}),
	})
	require.NoError(t, err)
	require.False(t, resp.IsError, resp.Content)
	require.Contains(t, resp.Content, "SUB RULES")
	require.Equal(t, 1, called)

	// A write that is refused, as one for a file the session never read,
	// adds nothing.
	require.NoError(t, os.WriteFile(filepath.Join(dir, "sub", "unread.go"), []byte("old\n"), 0o644))
	failedTool := NewWriteTool(nil, &mockPermissionService{}, &mockHistoryService{}, &mockEditFileTracker{}, subctx, dir)
	resp, err = failedTool.Run(ctx, fantasy.ToolCall{
		ID:    "call-failed",
		Name:  WriteToolName,
		Input: marshal(WriteParams{FilePath: "sub/unread.go", Content: "x"}),
	})
	require.NoError(t, err)
	require.True(t, resp.IsError, resp.Content)
	require.NotContains(t, resp.Content, "SUB RULES")
	require.Equal(t, 1, called, "a failed write adds nothing")

	resp, err = tool.Run(ctx, fantasy.ToolCall{
		ID:    "call2",
		Name:  WriteToolName,
		Input: marshal(WriteParams{FilePath: "sub/other.go", Content: "y"}),
	})
	require.NoError(t, err)
	require.False(t, resp.IsError, resp.Content)
	require.Contains(t, resp.Content, "SUB RULES")
	require.Equal(t, 2, called, "a successful write after a failure adds it again")
}
