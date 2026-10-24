package tools

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"charm.land/fantasy"
	"github.com/stretchr/testify/require"
)

// The edit, multiedit and write tools name the file they would change in
// their permission request, as an absolute path, so acceptEdits can tell
// a file in the project from one elsewhere: the request's Path holds only
// the working directory for a file in it.
func TestEditToolsNameTheFileInTheirRequest(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	file := filepath.Join(dir, "sub", "a.go")
	require.NoError(t, os.MkdirAll(filepath.Dir(file), 0o755))
	require.NoError(t, os.WriteFile(file, []byte("package sub\n"), 0o644))
	tracker := realTracker(t, "s1")
	tracker.RecordRead(t.Context(), "s1", file)
	ctx := context.WithValue(t.Context(), SessionIDContextKey, "s1")

	edit := func(input string) func(*capturingPermissions) (fantasy.ToolResponse, error) {
		return func(p *capturingPermissions) (fantasy.ToolResponse, error) {
			return NewEditTool(nil, p, &mockHistoryService{}, tracker, nil, dir).Run(ctx, fantasy.ToolCall{ID: "c1", Name: EditToolName, Input: input})
		}
	}
	multiedit := func(input string) func(*capturingPermissions) (fantasy.ToolResponse, error) {
		return func(p *capturingPermissions) (fantasy.ToolResponse, error) {
			return NewMultiEditTool(nil, p, &mockHistoryService{}, tracker, nil, dir).Run(ctx, fantasy.ToolCall{ID: "c1", Name: MultiEditToolName, Input: input})
		}
	}
	write := func(input string) func(*capturingPermissions) (fantasy.ToolResponse, error) {
		return func(p *capturingPermissions) (fantasy.ToolResponse, error) {
			return NewWriteTool(nil, p, &mockHistoryService{}, tracker, nil, dir).Run(ctx, fantasy.ToolCall{ID: "c1", Name: WriteToolName, Input: input})
		}
	}
	newFile := filepath.Join(dir, "sub", "new.go")
	for _, tc := range []struct {
		name string
		call func(*capturingPermissions) (fantasy.ToolResponse, error)
		want string
	}{
		{"edit", edit(`{"file_path":"sub/a.go","old_string":"package sub","new_string":"package sub2"}`), file},
		{"edit, a deletion", edit(`{"file_path":"sub/a.go","old_string":"package sub\n","new_string":""}`), file},
		{"edit, a new file", edit(`{"file_path":"sub/new.go","old_string":"","new_string":"package sub\n"}`), newFile},
		{"multiedit", multiedit(`{"file_path":"sub/a.go","edits":[{"old_string":"package sub","new_string":"package sub2"}]}`), file},
		{"multiedit, a new file", multiedit(`{"file_path":"sub/new.go","edits":[{"old_string":"","new_string":"package sub\n"}]}`), newFile},
		{"write", write(`{"file_path":"sub/a.go","content":"package sub2\n"}`), file},
	} {
		perms := &capturingPermissions{}
		_, err := tc.call(perms)
		require.NoError(t, err, tc.name)
		require.Equal(t, tc.want, perms.last.FilePath, tc.name)
	}
}
