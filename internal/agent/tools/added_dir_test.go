package tools

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"charm.land/fantasy"
	"github.com/charmbracelet/crush/internal/config"
	"github.com/charmbracelet/crush/internal/permission"
	"github.com/stretchr/testify/require"
)

// view reads, ls lists, and grep and glob search in a directory added
// with --add-dir without asking: the permission service approves them
// there by these tools' names and the actions they ask for, so a renamed
// tool or action would quietly start asking again.
func TestReadToolsNeedNoPermissionInAnAddedDir(t *testing.T) {
	t.Parallel()

	added := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(added, "notes.txt"), []byte("added notes\n"), 0o644))
	perms := permission.NewPermissionService(t.TempDir(), false, nil, added)
	ctx, cancel := context.WithTimeout(context.WithValue(t.Context(), SessionIDContextKey, "s1"), 30*time.Second)
	defer cancel()
	asked := perms.Subscribe(ctx)

	run := func(tool fantasy.AgentTool, input any) string {
		t.Helper()
		b, err := json.Marshal(input)
		require.NoError(t, err)
		resp, err := tool.Run(ctx, fantasy.ToolCall{ID: tool.Info().Name + "-call", Name: tool.Info().Name, Input: string(b)})
		require.NoError(t, err)
		return resp.Content
	}
	require.Contains(t, run(NewViewTool(nil, perms, mockFileTracker{}, nil, nil, t.TempDir()), ViewParams{FilePath: filepath.Join(added, "notes.txt")}), "added notes")
	require.Contains(t, run(NewLsTool(perms, t.TempDir(), config.ToolLs{}), LSParams{Path: added}), "notes.txt")
	require.Contains(t, run(NewGrepTool(perms, t.TempDir(), config.ToolGrep{}), GrepParams{Pattern: "added", Path: added}), "notes.txt")
	require.Contains(t, run(NewGlobTool(perms, t.TempDir(), config.ToolGlob{}), GlobParams{Pattern: "*.txt", Path: added}), "notes.txt")
	select {
	case e := <-asked:
		t.Fatalf("asked for %s %s on %s", e.Payload.ToolName, e.Payload.Action, e.Payload.Path)
	default:
	}
}
