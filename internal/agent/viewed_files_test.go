package agent

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"charm.land/fantasy"
	"github.com/charmbracelet/crush/internal/agent/tools"
	"github.com/stretchr/testify/require"
)

// A view of the same lines of a file the session's context already
// holds, unchanged, is answered with Claude Code's note instead of the
// lines. They are given again for other lines, in another session, when
// the file's size or modification time changes, after a rewind cut the
// view that gave them (one it did not cut stands), and after a summary. A read cut short at the size
// limit is never taken for the lines it was asked for. The note's
// metadata says so, for the chat to show it as a note.
func TestViewGivesAnUnchangedFileOnce(t *testing.T) {
	t.Parallel()

	env := testEnv(t)
	c := &coordinator{sessions: env.sessions, viewed: newViewedFiles(env.sessions)}
	tool := tools.NewViewTool(nil, env.permissions, env.filetracker, nil, nil, c.viewedFiles(), env.workingDir)
	sess, err := env.sessions.Create(t.Context(), "viewing")
	require.NoError(t, err)
	other, err := env.sessions.Create(t.Context(), "another")
	require.NoError(t, err)
	view := func(sessionID, messageID string, params tools.ViewParams) string {
		t.Helper()
		ctx := context.WithValue(context.WithValue(t.Context(), tools.SessionIDContextKey, sessionID), tools.MessageIDContextKey, messageID)
		b, err := json.Marshal(params)
		require.NoError(t, err)
		resp, err := tool.Run(ctx, fantasy.ToolCall{ID: messageID + "-call", Name: tools.ViewToolName, Input: string(b)})
		require.NoError(t, err)
		require.False(t, resp.IsError, resp.Content)
		var meta tools.ViewResponseMetadata
		require.NoError(t, json.Unmarshal([]byte(resp.Metadata), &meta))
		require.Equal(t, strings.HasPrefix(resp.Content, "Wasted call"), meta.Unchanged, resp.Content)
		return resp.Content
	}
	const unchanged = "Wasted call — file unchanged since your last view of it. Refer to that earlier tool result instead."
	file := filepath.Join(env.workingDir, "a.go")
	whole := tools.ViewParams{FilePath: file}
	write := func(content string, modTime time.Time) {
		t.Helper()
		require.NoError(t, os.WriteFile(file, []byte(content), 0o644))
		require.NoError(t, os.Chtimes(file, modTime, modTime))
	}

	first := time.Now().Add(-time.Hour)
	write("package a\n", first)
	require.Contains(t, view(sess.ID, "m1", whole), "package a")
	require.Equal(t, unchanged, view(sess.ID, "m2", whole))
	require.Contains(t, view(sess.ID, "m3", tools.ViewParams{FilePath: file, Limit: 1}), "package a", "other lines")
	require.Contains(t, view(other.ID, "m4", whole), "package a", "another session")

	later := first.Add(time.Minute)
	write("package b\n", later)
	require.Contains(t, view(sess.ID, "m5", whole), "package b", "the same size, modified later")
	write("package bb\n", later)
	require.Contains(t, view(sess.ID, "m6", whole), "package bb", "another size, modified at the same time")
	require.Equal(t, unchanged, view(sess.ID, "m7", whole))
	notes := filepath.Join(env.workingDir, "notes.txt")
	require.NoError(t, os.WriteFile(notes, []byte("the notes\n"), 0o644))
	require.Contains(t, view(sess.ID, "m-notes", tools.ViewParams{FilePath: notes}), "the notes")

	c.Rewound(sess.ID, []string{"m6"})
	require.Contains(t, view(sess.ID, "m8", whole), "package bb", "a rewind cut the view that gave it")
	require.Equal(t, unchanged, view(sess.ID, "m9", whole))
	require.Equal(t, unchanged, view(sess.ID, "m-notes-again", tools.ViewParams{FilePath: notes}), "the rewind did not cut it")

	_, err = env.sessions.UpdateSummary(t.Context(), sess.ID, "summary-1", 0, 0, false)
	require.NoError(t, err)
	require.Contains(t, view(sess.ID, "m10", whole), "package bb", "a summary left it behind")
	require.Equal(t, unchanged, view(sess.ID, "m11", whole))

	big := filepath.Join(env.workingDir, "big.txt")
	require.NoError(t, os.WriteFile(big, []byte(strings.Repeat(strings.Repeat("x", 200)+"\n", tools.DefaultReadLimit)), 0o644))
	for _, id := range []string{"m12", "m13"} {
		require.Contains(t, view(sess.ID, id, tools.ViewParams{FilePath: big}), "one read returns at most", id)
	}
}
