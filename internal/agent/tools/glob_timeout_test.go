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
	"github.com/stretchr/testify/require"
)

// A glob cut short by its timeout says so, as grep does: it answered with
// what it had found, "No files found" for files that may be there, and a
// model took the missing file for certain.
func TestGlobSaysWhenItTimedOut(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "a.go"), []byte("package a\n"), 0o644))
	input, err := json.Marshal(GlobParams{Pattern: "**/*.go"})
	require.NoError(t, err)
	ctx := context.WithValue(t.Context(), SessionIDContextKey, "s1")

	tiny := time.Nanosecond
	resp, err := NewGlobTool(&mockPermissionService{}, dir, config.ToolGlob{Timeout: &tiny}).Run(ctx, fantasy.ToolCall{ID: "call-1", Name: GlobToolName, Input: string(input)})
	require.NoError(t, err)
	require.True(t, resp.IsError, resp.Content)
	require.Contains(t, resp.Content, "search timed out after 1ns; try a narrower path or pattern")

	// In time, the same search finds the file.
	resp, err = NewGlobTool(&mockPermissionService{}, dir, config.ToolGlob{}).Run(ctx, fantasy.ToolCall{ID: "call-2", Name: GlobToolName, Input: string(input)})
	require.NoError(t, err)
	require.False(t, resp.IsError, resp.Content)
	require.Contains(t, resp.Content, "a.go")
}
