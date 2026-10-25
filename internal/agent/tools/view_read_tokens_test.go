package tools

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// viewedPart views the file at path from its start and gives how many
// bytes of it came back and what the model was told.
func viewedPart(t *testing.T, path string) (int, string) {
	t.Helper()
	tool := newViewToolForTest(filepath.Dir(path))
	ctx := context.WithValue(context.Background(), SessionIDContextKey, "test-session")
	resp := runViewTool(t, tool, ctx, ViewParams{FilePath: path})
	require.False(t, resp.IsError, resp.Content)
	var meta ViewResponseMetadata
	require.NoError(t, json.Unmarshal([]byte(resp.Metadata), &meta))
	return len(meta.Content), resp.Content
}

// lines100 writes a file of n lines of 100 bytes each, newline included,
// so k whole lines come back as 100k-1 bytes.
func lines100(t *testing.T, name string, n int) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), name)
	require.NoError(t, os.WriteFile(path, []byte(strings.Repeat(strings.Repeat("x", 99)+"\n", n)), 0o644))
	return path
}

// One read returns at most Claude Code's 25,000 tokens, at its estimate
// of 4 bytes a token and 2 for JSON, cut at a line's end with the offset
// to read on: at 200KB, one read of a log took 40% of a local model's
// 128K context.
func TestViewReadsAtMostClaudeCodesTokens(t *testing.T) {
	t.Parallel()

	size, content := viewedPart(t, lines100(t, "app.log", 1900))
	require.Equal(t, 1000*100-1, size, "the 1000 lines that fit in 100,000 bytes")
	require.Contains(t, content, "(Stopped after line 1000: one read returns at most 25000 tokens. To read on, use offset 1001, "+
		"or grep for a specific section. Do NOT answer from this part alone if the answer may be further in the file. "+
		"To read it anyway, up to what still fits in your context, call view again with allow_large: true, "+
		"only if you genuinely need all of it or the user asked for the whole file.)")

	for _, name := range []string{"events.jsonl", "package.json", "tsconfig.jsonc", "DATA.JSON"} {
		size, content = viewedPart(t, lines100(t, name, 1900))
		require.Equal(t, 500*100-1, size, "%s: the 500 lines that fit in 50,000 bytes", name)
		require.Contains(t, content, "Stopped after line 500: one read returns at most 25000 tokens. To read on, use offset 501", name)
	}

	size, content = viewedPart(t, lines100(t, "small.log", 900))
	require.Equal(t, 900*100, size, "a file within the limit comes back whole, its last newline too")
	require.NotContains(t, content, "Stopped after line")

	require.Contains(t, viewDescription(), "(default 2000 lines, at most 25000 tokens a read)")
}

// CLAUDE_CODE_FILE_READ_MAX_OUTPUT_TOKENS sets how many tokens one read
// returns, as in Claude Code, and the description says so; unset or not
// a positive number, the 25,000 hold.
func TestViewReadTokensFollowClaudeCodesVariable(t *testing.T) {
	t.Setenv("CLAUDE_CODE_FILE_READ_MAX_OUTPUT_TOKENS", "1000")
	size, content := viewedPart(t, lines100(t, "app.log", 1900))
	require.Equal(t, 40*100-1, size, "the 40 lines that fit in 4,000 bytes")
	require.Contains(t, content, "Stopped after line 40: one read returns at most 1000 tokens. To read on, use offset 41")
	size, _ = viewedPart(t, lines100(t, "app.json", 1900))
	require.Equal(t, 20*100-1, size, "the 20 lines that fit in 2,000 bytes")
	require.Contains(t, viewDescription(), "at most 1000 tokens a read")

	for _, value := range []string{"", "0", "-5", "lots"} {
		t.Setenv("CLAUDE_CODE_FILE_READ_MAX_OUTPUT_TOKENS", value)
		size, _ = viewedPart(t, lines100(t, "app.log", 1900))
		require.Equal(t, 1000*100-1, size, "%q is ignored", value)
		require.Contains(t, viewDescription(), "at most 25000 tokens a read", value)
	}
}
