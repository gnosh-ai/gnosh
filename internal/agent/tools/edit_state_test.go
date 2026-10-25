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

// An edit or a write that went in whole, as given, ends its result with
// Claude Code's note that the agent's context holds the file as it now
// is, so the agent need not read it back. One that matched only with its
// whitespace forgiven, or only some of whose edits were made, does not.
func TestEditResultsSayTheFileStateIsCurrent(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	ctx := context.WithValue(t.Context(), SessionIDContextKey, "session")
	tracker := &mockEditFileTracker{lastRead: time.Now().Add(time.Hour)}
	edit := editContext{ctx: ctx, permissions: &mockPermissionService{}, files: &mockHistoryService{}, filetracker: tracker, workingDir: dir}
	path := filepath.Join(dir, "a.go")
	write := func(content string) {
		t.Helper()
		require.NoError(t, os.WriteFile(path, []byte(content), 0o644))
	}
	run := func(tool fantasy.AgentTool, input any) string {
		t.Helper()
		b, err := json.Marshal(input)
		require.NoError(t, err)
		resp, err := tool.Run(ctx, fantasy.ToolCall{ID: "call", Name: tool.Info().Name, Input: string(b)})
		require.NoError(t, err)
		require.False(t, resp.IsError, resp.Content)
		return resp.Content
	}
	current := func(t *testing.T, content string) {
		t.Helper()
		require.Contains(t, content, fileStateCurrent)
	}

	write("alpha\nbeta\n")
	resp, err := replaceContent(edit, path, "beta", "BETA", false, fantasy.ToolCall{ID: "call"})
	require.NoError(t, err)
	current(t, resp.Content)

	resp, err = deleteContent(edit, path, "BETA\n", false, fantasy.ToolCall{ID: "call"})
	require.NoError(t, err)
	current(t, resp.Content)

	resp, err = createNewFile(edit, filepath.Join(dir, "new.go"), "package a\n", fantasy.ToolCall{ID: "call"})
	require.NoError(t, err)
	current(t, resp.Content)

	write("func a() {\n\treturn 1\n}\n")
	resp, err = replaceContent(edit, path, "func a() {\n    return 1\n}", "func a() {\n    return 2\n}", false, fantasy.ToolCall{ID: "call"})
	require.NoError(t, err)
	require.Contains(t, resp.Content, whitespaceCorrectedNote)
	require.NotContains(t, resp.Content, fileStateCurrent, "its whitespace was forgiven")

	multi := NewMultiEditTool(nil, &mockPermissionService{}, &mockHistoryService{}, tracker, nil, dir)
	write("alpha\nbeta\n")
	current(t, run(multi, MultiEditParams{FilePath: path, Edits: []MultiEditOperation{{OldString: "beta", NewString: "BETA"}}}))
	out := run(multi, MultiEditParams{FilePath: path, Edits: []MultiEditOperation{{OldString: "alpha", NewString: "ALPHA"}, {OldString: "gamma", NewString: "GAMMA"}}})
	require.Contains(t, out, "Applied 1 of 2 edits")
	require.NotContains(t, out, fileStateCurrent, "an edit failed")
	current(t, run(multi, MultiEditParams{FilePath: filepath.Join(dir, "made.go"), Edits: []MultiEditOperation{{OldString: "", NewString: "package b\n"}}}))

	current(t, run(NewWriteTool(nil, &mockPermissionService{}, &mockHistoryService{}, tracker, nil, dir), WriteParams{FilePath: path, Content: "package a\n"}))
}
