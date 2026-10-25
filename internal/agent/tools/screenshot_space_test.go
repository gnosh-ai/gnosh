package tools

import (
	"bytes"
	"context"
	"encoding/json"
	"image"
	"image/png"
	"os"
	"path/filepath"
	"testing"

	"charm.land/fantasy"
	"github.com/stretchr/testify/require"
)

// A screenshot's name gets the other space before AM or PM; other names
// get none.
func TestOtherScreenshotSpace(t *testing.T) {
	t.Parallel()

	typed := "/Users/jae/Desktop/Screenshot 2026-10-25 at 10.47.48 AM.png"
	saved := "/Users/jae/Desktop/Screenshot 2026-10-25 at 10.47.48" + narrowNoBreakSpace + "AM.png"
	require.Equal(t, saved, otherScreenshotSpace(typed))
	require.Equal(t, typed, otherScreenshotSpace(saved))
	require.Equal(t, "/d/Shot 3.04.05"+narrowNoBreakSpace+"PM.png", otherScreenshotSpace("/d/Shot 3.04.05 PM.png"))
	for _, other := range []string{"/d/Shot AM.jpg", "/d/Shot 3.04.05PM.png", "/d/notes.png", "/d/ AM.png"} {
		require.Empty(t, otherScreenshotSpace(other), other)
	}
}

// view reads a screenshot named with a space before AM, as a model types
// it, where macOS saved it with a narrow no-break space, and the other way
// round, as Claude Code's Read does; a file not there either way is not
// there.
func TestViewFindsAScreenshotByItsOtherSpace(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	var pngData bytes.Buffer
	require.NoError(t, png.Encode(&pngData, image.NewGray(image.Rect(0, 0, 1, 1))))
	saved := filepath.Join(dir, "Screenshot 2026-10-25 at 10.47.48"+narrowNoBreakSpace+"AM.png")
	require.NoError(t, os.WriteFile(saved, pngData.Bytes(), 0o644))
	typedSaved := filepath.Join(dir, "Shot 1.02.03 PM.png")
	require.NoError(t, os.WriteFile(typedSaved, pngData.Bytes(), 0o644))

	tool := NewViewTool(nil, &mockPermissionService{}, mockFileTracker{}, nil, nil, nil, dir)
	ctx := context.WithValue(context.WithValue(t.Context(), SessionIDContextKey, "s1"), SupportsImagesContextKey, true)
	view := func(path string) fantasy.ToolResponse {
		t.Helper()
		b, err := json.Marshal(ViewParams{FilePath: path})
		require.NoError(t, err)
		resp, err := tool.Run(ctx, fantasy.ToolCall{ID: "call", Name: ViewToolName, Input: string(b)})
		require.NoError(t, err)
		return resp
	}
	for _, path := range []string{
		filepath.Join(dir, "Screenshot 2026-10-25 at 10.47.48 AM.png"),
		filepath.Join(dir, "Shot 1.02.03"+narrowNoBreakSpace+"PM.png"),
	} {
		resp := view(path)
		require.False(t, resp.IsError, resp.Content)
		require.Equal(t, "image", resp.Type, path)
	}
	resp := view(filepath.Join(dir, "Screenshot 2026-10-25 at 9.00.00 AM.png"))
	require.True(t, resp.IsError)
	require.Contains(t, resp.Content, "File does not exist.")
}
