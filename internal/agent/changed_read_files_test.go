package agent

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"charm.land/fantasy"
	"github.com/charmbracelet/crush/internal/agent/tools"
	"github.com/stretchr/testify/require"
)

// A bash call that changes files the session read says which, so the
// agent views them before an edit of one is refused. A file the call
// rewrote as it was, as git stash and pop do, and one it left alone are
// not named, and a later call that changes nothing says nothing, though
// the changed file still differs from what the agent read.
func TestChangedReadFilesNamesWhatACallChanged(t *testing.T) {
	t.Parallel()

	env := testEnv(t)
	sess, err := env.sessions.Create(t.Context(), "changed read files")
	require.NoError(t, err)
	write := func(name, content string) string {
		path := filepath.Join(env.workingDir, name)
		require.NoError(t, os.WriteFile(path, []byte(content), 0o644))
		return path
	}
	changed := write("changed.go", "package a\n")
	rewritten := write("rewritten.go", "package b\n")
	write("untouched.go", "package c\n")
	for _, name := range []string{"changed.go", "rewritten.go", "untouched.go"} {
		(*env.filetracker).RecordRead(t.Context(), sess.ID, filepath.Join(env.workingDir, name))
	}

	later := time.Now().Add(time.Minute)
	edits := func(context.Context, fantasy.ToolCall) (fantasy.ToolResponse, error) {
		write("changed.go", "package z\n")
		write("rewritten.go", "package b\n")
		require.NoError(t, os.Chtimes(changed, later, later))
		require.NoError(t, os.Chtimes(rewritten, later, later))
		return fantasy.NewTextResponse("ran"), nil
	}
	nothing := func(context.Context, fantasy.ToolCall) (fantasy.ToolResponse, error) {
		return fantasy.NewTextResponse("ran"), nil
	}
	ctx := context.WithValue(t.Context(), tools.SessionIDContextKey, sess.ID)
	run := func(command func(context.Context, fantasy.ToolCall) (fantasy.ToolResponse, error)) string {
		t.Helper()
		tool := &changedReadFiles{AgentTool: &funcTool{name: tools.BashToolName, run: command}, files: *env.filetracker, workingDir: env.workingDir}
		resp, err := tool.Run(ctx, fantasy.ToolCall{ID: "call", Name: tools.BashToolName, Input: "{}"})
		require.NoError(t, err)
		return resp.Content
	}

	require.Equal(t, "ran\n\nThis command changed files you read before: changed.go. View them again before you edit them.", run(edits))
	require.Equal(t, "ran", run(nothing))
}
