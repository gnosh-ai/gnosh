package tools

import (
	"context"
	"encoding/json"
	"path/filepath"
	"testing"

	"charm.land/fantasy"
	"github.com/charmbracelet/crush/internal/config"
	"github.com/charmbracelet/crush/internal/permission"
	"github.com/stretchr/testify/require"
)

// A file that is not there is answered in Claude Code's words, with the
// working directory and the file meant when one can be guessed: the same
// path inside the working directory, when a path leaves out the
// project's own name, or a file beside it with the same name and another
// extension. A leading dot starts no extension, as in Node's. A path
// inside the working directory is not looked for again inside it.
func TestMissingFileGuessesTheFileMeant(t *testing.T) {
	t.Parallel()

	parent := t.TempDir()
	project := filepath.Join(parent, "proj")
	mkfiles(t, project, "internal/x.go", "main.ts", "README.md", ".env.local", ".gitignore", "data.backup copy", "proj/run.sh")
	note := " Note: your current working directory is " + project + "."

	for path, meant := range map[string]string{
		filepath.Join(parent, "internal", "x.go"): filepath.Join(project, "internal", "x.go"),
		filepath.Join(project, "main.js"):         filepath.Join(project, "main.ts"),
		filepath.Join(project, "README"):          filepath.Join(project, "README.md"),
		filepath.Join(project, ".env"):            filepath.Join(project, ".env.local"),
		filepath.Join(project, ".npmrc"):          "",
		filepath.Join(project, "data"):            "",
		filepath.Join(project, "nothing.go"):      "",
		filepath.Join(project, "run.sh"):          "",
		filepath.Join(parent, "nothing.go"):       "",
	} {
		want := "File does not exist." + note
		if meant != "" {
			want += " Did you mean " + meant + "?"
		}
		require.Equal(t, want, missingFile(path, project), path)
	}
}

// view, edit, multiedit, grep and ls answer a path that is not there as
// Claude Code's tools do: what is missing, the working directory, and
// the path meant, here the same path inside the working directory. A
// path is named as it was given.
func TestToolsSayAPathIsNotThere(t *testing.T) {
	t.Parallel()

	parent := t.TempDir()
	project := filepath.Join(parent, "proj")
	mkfiles(t, project, "internal/x.go")
	perms := permission.NewPermissionService(project, true, nil)
	ctx := context.WithValue(t.Context(), SessionIDContextKey, "s1")
	run := func(tool fantasy.AgentTool, input any) string {
		t.Helper()
		b, err := json.Marshal(input)
		require.NoError(t, err)
		resp, err := tool.Run(ctx, fantasy.ToolCall{ID: "call-1", Name: tool.Info().Name, Input: string(b)})
		require.NoError(t, err)
		require.True(t, resp.IsError, resp.Content)
		return resp.Content
	}

	file := filepath.Join(parent, "internal", "x.go")
	want := "File does not exist. Note: your current working directory is " + project + ". Did you mean " + filepath.Join(project, "internal", "x.go") + "?"
	require.Equal(t, want, run(NewViewTool(nil, perms, mockFileTracker{}, nil, nil, nil, project), ViewParams{FilePath: file}))
	require.Equal(t, want, run(NewEditTool(nil, perms, &mockHistoryService{}, mockFileTracker{}, nil, project), EditParams{FilePath: file, OldString: "x", NewString: "y"}))
	require.Equal(t, want, run(NewMultiEditTool(nil, perms, &mockHistoryService{}, mockFileTracker{}, nil, project), MultiEditParams{FilePath: file, Edits: []MultiEditOperation{{OldString: "x", NewString: "y"}}}))

	dir := filepath.Join(parent, "internal")
	want = "Path does not exist: " + dir + ". Note: your current working directory is " + project + ". Did you mean " + filepath.Join(project, "internal") + "?"
	require.Equal(t, want, run(NewGrepTool(perms, project, config.ToolGrep{}), GrepParams{Pattern: "x", Path: dir}))
	require.Equal(t, want, run(NewLsTool(perms, project, config.ToolLs{}), LSParams{Path: dir}))
	want = "Path does not exist: nope. Note: your current working directory is " + project + "."
	require.Equal(t, want, run(NewGrepTool(perms, project, config.ToolGrep{}), GrepParams{Pattern: "x", Path: "nope"}))
	require.Equal(t, want, run(NewLsTool(perms, project, config.ToolLs{}), LSParams{Path: "nope"}))
}
