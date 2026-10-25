package tools

import (
	"context"
	"encoding/json"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

// viewLarge views the file at path from its start, with allow_large when
// asked, under ctx, and gives how many bytes came back and the text.
func viewLarge(t *testing.T, ctx context.Context, path string, allowLarge bool) (int, string) {
	t.Helper()
	tool := newViewToolForTest(filepath.Dir(path))
	resp := runViewTool(t, tool, context.WithValue(ctx, SessionIDContextKey, "test-session"), ViewParams{FilePath: path, AllowLarge: allowLarge})
	require.False(t, resp.IsError, resp.Content)
	var meta ViewResponseMetadata
	require.NoError(t, json.Unmarshal([]byte(resp.Metadata), &meta))
	return len(meta.Content), resp.Content
}

// As Claude Code's Read takes allow_large, a section over the 25,000
// tokens comes back whole when it fits in what is left of the context
// this turn, and what it took is gone for the turn's other large reads.
// A read cut at the limit says allow_large is there.
func TestViewAllowLargeReadsWhatStillFits(t *testing.T) {
	t.Parallel()
	path := lines100(t, "app.log", 1900) // 190,000 bytes: 47,500 tokens

	size, content := viewLarge(t, context.Background(), path, false)
	require.Equal(t, 1000*100-1, size)
	require.Contains(t, content, "call view again with allow_large: true")

	room := NewContextRoom(100000)
	size, content = viewLarge(t, WithContextRoom(context.Background(), room), path, true)
	require.Equal(t, 1900*100, size, "the whole file")
	require.NotContains(t, content, "Stopped after line")
	require.Equal(t, 100000-1900*100/4, room.Left(), "the read took its tokens from the room")

	// A room of 30,000 tokens: cut there.
	room = NewContextRoom(30000)
	size, content = viewLarge(t, WithContextRoom(context.Background(), room), path, true)
	require.Equal(t, 1200*100-1, size, "the 1200 lines that fit in 120,000 bytes")
	require.Contains(t, content, "one read returns at most 30000 tokens")
	require.Contains(t, content, "That is what allow_large had room for this turn.")
	require.Equal(t, 30000-(1200*100-1)/4, room.Left())
}

// allow_large gives no less than the usual limit: with less room left
// than that, or none known, the read is cut at the 25,000 tokens and says
// why, in Claude Code's words.
func TestViewAllowLargeWithoutRoom(t *testing.T) {
	t.Parallel()
	path := lines100(t, "app.log", 1900)

	room := NewContextRoom(10000)
	size, content := viewLarge(t, WithContextRoom(context.Background(), room), path, true)
	require.Equal(t, 1000*100-1, size)
	require.Contains(t, content, "Even with allow_large, this is more than fits in the context you have left this turn")
	require.Equal(t, 10000, room.Left(), "a read at the usual limit takes nothing from the room")

	size, content = viewLarge(t, context.Background(), path, true)
	require.Equal(t, 1000*100-1, size)
	require.Contains(t, content, "allow_large is not available where this read runs; read it in parts with offset and limit.")
}
