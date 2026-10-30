package agent

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"charm.land/fantasy"
	"github.com/charmbracelet/crush/internal/agent/tools"
	"github.com/charmbracelet/crush/internal/db"
	"github.com/charmbracelet/crush/internal/filetracker"
	"github.com/charmbracelet/crush/internal/session"
	"github.com/stretchr/testify/require"
)

// changedFilesTest is the environment for a test of changedReadFiles: a
// session in its own temporary directory, a filetracker over its own
// database, and helpers to wrap a tool as the coordinator does and to
// run it as the session's agent would.
type changedFilesTest struct {
	workingDir string
	sessionID  string
	files      filetracker.Service
	ctx        context.Context
}

func newChangedFilesTest(t *testing.T) *changedFilesTest {
	t.Helper()
	env := testEnv(t)
	sess, err := env.sessions.Create(t.Context(), "changed read files")
	require.NoError(t, err)
	return &changedFilesTest{
		workingDir: env.workingDir,
		sessionID:  sess.ID,
		files:      env.filetracker,
		ctx:        context.WithValue(t.Context(), tools.SessionIDContextKey, sess.ID),
	}
}

// writeAndRead writes content to a file in the test's working directory
// and records the read, as a view would.
func (c *changedFilesTest) writeAndRead(t *testing.T, name, content string) string {
	t.Helper()
	path := filepath.Join(c.workingDir, name)
	writeFile(t, path, content)
	c.files.RecordRead(t.Context(), c.sessionID, path)
	return path
}

// wrap wraps a tool as the coordinator does for an agent.
func (c *changedFilesTest) wrap(tool fantasy.AgentTool) fantasy.AgentTool {
	return &changedReadFiles{AgentTool: tool, files: c.files, workingDir: c.workingDir, named: newNamedChanges()}
}

// runWith runs a wrapped tool as the session's agent would and returns
// its result's text.
func (c *changedFilesTest) runWith(t *testing.T, tool fantasy.AgentTool, name, input string) string {
	t.Helper()
	resp, err := tool.Run(c.ctx, fantasy.ToolCall{ID: "call", Name: name, Input: input})
	require.NoError(t, err)
	return resp.Content
}

// writeFile writes content to a path, for a test's fake tool.
func writeFile(t *testing.T, path, content string) {
	t.Helper()
	require.NoError(t, os.WriteFile(path, []byte(content), 0o644))
}

// A bash call that changes files the session read says which, so the
// agent views them before an edit of one is refused. A file the call
// rewrote as it was, as git stash and pop do, and one it left alone are
// not named, and a later call that changes nothing says nothing, though
// the changed file still differs from what the agent read.
func TestChangedReadFilesNamesWhatACallChanged(t *testing.T) {
	t.Parallel()
	c := newChangedFilesTest(t)
	changed := c.writeAndRead(t, "changed.go", "package a\n")
	rewritten := c.writeAndRead(t, "rewritten.go", "package b\n")
	c.writeAndRead(t, "untouched.go", "package c\n")

	// The command changes the files, as a real bash command would: the
	// change lands while the command runs, after the before stamps.
	edits := func(context.Context, fantasy.ToolCall) (fantasy.ToolResponse, error) {
		writeFile(t, changed, "package z\n")
		writeFile(t, rewritten, "package b\n")
		return fantasy.NewTextResponse("ran"), nil
	}
	nothing := func(context.Context, fantasy.ToolCall) (fantasy.ToolResponse, error) {
		return fantasy.NewTextResponse("ran"), nil
	}
	// One wrapper, so the named state is shared between calls, as in the
	// coordinator.
	named := newNamedChanges()
	run := func(command func(context.Context, fantasy.ToolCall) (fantasy.ToolResponse, error)) string {
		tool := &changedReadFiles{AgentTool: &funcTool{name: tools.BashToolName, run: command}, files: c.files, workingDir: c.workingDir, named: named}
		return c.runWith(t, tool, tools.BashToolName, "{}")
	}

	require.Equal(t, "ran\n\nThis command changed files you read before: changed.go. View them again before you edit them.", run(edits))
	require.Equal(t, "ran", run(nothing))
}

// A command that rewrites a file at the same size, before the file
// system's clock ticks, leaves its modification time as it was: Linux
// before 6.13 stamps files from a clock a few milliseconds coarse. The
// stamp alone would call the file untouched.
func TestChangedReadFilesSeesASameSizeChangeInOneClockTick(t *testing.T) {
	t.Parallel()
	c := newChangedFilesTest(t)
	path := c.writeAndRead(t, "tick.go", "package a\n")
	info, err := os.Stat(path)
	require.NoError(t, err)

	bash := &funcTool{name: tools.BashToolName, run: func(context.Context, fantasy.ToolCall) (fantasy.ToolResponse, error) {
		writeFile(t, path, "package z\n")
		// The clock has not ticked since the file was last written.
		require.NoError(t, os.Chtimes(path, info.ModTime(), info.ModTime()))
		return fantasy.NewTextResponse("ran"), nil
	}}
	require.Equal(t, "ran\n\nThis command changed files you read before: tick.go. View them again before you edit them.", c.runWith(t, c.wrap(bash), tools.BashToolName, "{}"))
}

// A file the session read, changed on disk by something other than the
// agent's own tools, is named in the result of the next tool call after
// the change, whatever the tool, and at the end of the result.
func TestChangedReadFilesNamesAFileChangedOnDisk(t *testing.T) {
	t.Parallel()
	c := newChangedFilesTest(t)
	file := c.writeAndRead(t, "a.go", "package a\n")
	writeFile(t, file, "package a\n// changed\n")

	other := &funcTool{name: "other", run: func(context.Context, fantasy.ToolCall) (fantasy.ToolResponse, error) {
		return fantasy.NewTextResponse("done"), nil
	}}
	resp := c.runWith(t, c.wrap(other), other.Info().Name, "{}")
	require.Equal(t, "done\n\nThese files changed since you read them: a.go. View them again before you edit them.", resp)
}

// A change is told once: a file named in one tool call's result is not
// named in the next tool call's, while it still differs from what the
// agent read.
func TestChangedReadFilesNamesAChangeOnce(t *testing.T) {
	t.Parallel()
	c := newChangedFilesTest(t)
	file := c.writeAndRead(t, "a.go", "package a\n")
	writeFile(t, file, "package a\n// changed\n")

	other := &funcTool{name: "other", run: func(context.Context, fantasy.ToolCall) (fantasy.ToolResponse, error) {
		return fantasy.NewTextResponse("done"), nil
	}}
	tool := c.wrap(other)
	require.Contains(t, c.runWith(t, tool, other.Info().Name, "{}"), "a.go")
	require.Equal(t, "done", c.runWith(t, tool, other.Info().Name, "{}"))
}

// Reading a file again means it is not named: the read records the new
// content as read, so it is no longer out of date.
func TestChangedReadFilesNotNamedAfterReread(t *testing.T) {
	t.Parallel()
	c := newChangedFilesTest(t)
	file := c.writeAndRead(t, "a.go", "package a\n")
	writeFile(t, file, "package a\n// changed\n")

	view := &funcTool{name: tools.ViewToolName, run: func(context.Context, fantasy.ToolCall) (fantasy.ToolResponse, error) {
		c.files.RecordRead(t.Context(), c.sessionID, file)
		return fantasy.NewTextResponse("viewed"), nil
	}}
	other := &funcTool{name: "other", run: func(context.Context, fantasy.ToolCall) (fantasy.ToolResponse, error) {
		return fantasy.NewTextResponse("done"), nil
	}}
	named := newNamedChanges()
	tool := &changedReadFiles{files: c.files, workingDir: c.workingDir, named: named}
	tool.AgentTool = other
	require.Contains(t, c.runWith(t, tool, other.Info().Name, "{}"), "a.go")
	tool.AgentTool = view
	require.Equal(t, "viewed", c.runWith(t, tool, tools.ViewToolName, `{"file_path":"a.go"}`))
	tool.AgentTool = other
	require.Equal(t, "done", c.runWith(t, tool, other.Info().Name, "{}"))
}

// A file rewritten as it was, as git stash and pop do, is not named.
func TestChangedReadFilesNotNamedWhenRewrittenUnchanged(t *testing.T) {
	t.Parallel()
	c := newChangedFilesTest(t)
	file := c.writeAndRead(t, "a.go", "package a\n")
	// The file's modification time moves but its content is the same, as
	// git stash and pop do.
	require.NoError(t, os.Chtimes(file, time.Now().Add(time.Minute), time.Now().Add(time.Minute)))

	other := &funcTool{name: "other", run: func(context.Context, fantasy.ToolCall) (fantasy.ToolResponse, error) {
		return fantasy.NewTextResponse("done"), nil
	}}
	resp := c.runWith(t, c.wrap(other), other.Info().Name, "{}")
	require.Equal(t, "done", resp)
}

// A session resumed in a later run of Gnosh, which kept no hash of the
// earlier reads, is not told that every file it read changed: only a
// file modified after its read is named.
func TestChangedReadFilesInALaterRun(t *testing.T) {
	t.Parallel()
	conn, err := db.Connect(t.Context(), t.TempDir())
	require.NoError(t, err)
	t.Cleanup(func() { conn.Close() })
	q := db.New(conn)
	sess, err := session.NewService(q, conn).Create(t.Context(), "resumed")
	require.NoError(t, err)

	dir := t.TempDir()
	untouched := filepath.Join(dir, "a.go")
	changed := filepath.Join(dir, "b.go")
	earlier := time.Now().Add(-time.Hour)
	firstRun := filetracker.NewService(q)
	for _, path := range []string{untouched, changed} {
		writeFile(t, path, "package a\n")
		require.NoError(t, os.Chtimes(path, earlier, earlier))
		firstRun.RecordRead(t.Context(), sess.ID, path)
	}
	later := time.Now().Add(time.Hour)
	writeFile(t, changed, "package b\n")
	require.NoError(t, os.Chtimes(changed, later, later))

	other := &funcTool{name: "other", run: func(context.Context, fantasy.ToolCall) (fantasy.ToolResponse, error) {
		return fantasy.NewTextResponse("done"), nil
	}}
	tool := &changedReadFiles{AgentTool: other, files: filetracker.NewService(q), workingDir: dir, named: newNamedChanges()}
	ctx := context.WithValue(t.Context(), tools.SessionIDContextKey, sess.ID)
	resp, err := tool.Run(ctx, fantasy.ToolCall{ID: "call", Name: other.Info().Name, Input: "{}"})
	require.NoError(t, err)
	require.Equal(t, "done\n\nThese files changed since you read them: b.go. View them again before you edit them.", resp.Content)
}

// A file the agent's own edit, multiedit or write just changed is not
// named, because those tools record a read of what they wrote.
func TestChangedReadFilesNotNamedAfterOwnEdit(t *testing.T) {
	t.Parallel()
	c := newChangedFilesTest(t)
	file := c.writeAndRead(t, "a.go", "package a\n")

	edit := &funcTool{name: tools.EditToolName, run: func(context.Context, fantasy.ToolCall) (fantasy.ToolResponse, error) {
		writeFile(t, file, "package a\n// edited\n")
		c.files.RecordRead(t.Context(), c.sessionID, file)
		return fantasy.NewTextResponse("edited"), nil
	}}
	other := &funcTool{name: "other", run: func(context.Context, fantasy.ToolCall) (fantasy.ToolResponse, error) {
		return fantasy.NewTextResponse("done"), nil
	}}
	named := newNamedChanges()
	tool := &changedReadFiles{files: c.files, workingDir: c.workingDir, named: named}
	tool.AgentTool = edit
	require.Equal(t, "edited", c.runWith(t, tool, tools.EditToolName, `{"file_path":"a.go"}`))
	tool.AgentTool = other
	require.Equal(t, "done", c.runWith(t, tool, other.Info().Name, "{}"))
}

// Bash's own note for the files its command changed does not name a file
// a second time, and the general note does not name a file bash already
// named.
func TestChangedReadFilesBashNoteNotDoubled(t *testing.T) {
	t.Parallel()
	c := newChangedFilesTest(t)
	byCommand := c.writeAndRead(t, "a.go", "package a\n")
	byOther := c.writeAndRead(t, "b.go", "package b\n")
	// b.go is changed by something other than the command, before the
	// command runs.
	writeFile(t, byOther, "package b\n// changed\n")

	bash := &funcTool{name: tools.BashToolName, run: func(context.Context, fantasy.ToolCall) (fantasy.ToolResponse, error) {
		// The command changes a.go, as a real bash command would: the
		// change lands while the command runs, after the before stamps.
		writeFile(t, byCommand, "package a\n// changed\n")
		return fantasy.NewTextResponse("ran"), nil
	}}
	resp := c.runWith(t, c.wrap(bash), tools.BashToolName, "{}")
	// a.go is named by bash's note and not a second time by the general
	// one; b.go, changed by something else, is named by the general one.
	require.Equal(t, "ran\n\nThese files changed since you read them: b.go. View them again before you edit them.\n\nThis command changed files you read before: a.go. View them again before you edit them.", resp)
}

// A sub-agent's tool calls are covered for the files its own session
// read, as the main agent's are.
func TestChangedReadFilesCoversSubAgentSessions(t *testing.T) {
	t.Parallel()
	c := newChangedFilesTest(t)
	file := c.writeAndRead(t, "a.go", "package a\n")

	// A sub-agent's session reads the file in its own session.
	subEnv := testEnv(t)
	subSess, err := subEnv.sessions.Create(t.Context(), "sub-agent")
	require.NoError(t, err)
	subEnv.filetracker.RecordRead(t.Context(), subSess.ID, file)
	writeFile(t, file, "package a\n// changed\n")

	other := &funcTool{name: "other", run: func(context.Context, fantasy.ToolCall) (fantasy.ToolResponse, error) {
		return fantasy.NewTextResponse("done"), nil
	}}
	subCtx := context.WithValue(t.Context(), tools.SessionIDContextKey, subSess.ID)
	tool := &changedReadFiles{AgentTool: other, files: subEnv.filetracker, workingDir: subEnv.workingDir, named: newNamedChanges()}
	resp, err := tool.Run(subCtx, fantasy.ToolCall{ID: "call", Name: other.Info().Name, Input: "{}"})
	require.NoError(t, err)
	require.Contains(t, resp.Content, "a.go")
}

// A file the session has not read is not named, and a file outside the
// working directory is named by its full path.
func TestChangedReadFilesNotNamedWhenUnreadAndOutside(t *testing.T) {
	t.Parallel()
	c := newChangedFilesTest(t)
	// A file not read is not named.
	unread := filepath.Join(c.workingDir, "unread.go")
	writeFile(t, unread, "package u\n")
	writeFile(t, unread, "package u\n// changed\n")
	// A file outside the working directory, read, is named by its full
	// path.
	outsideDir := t.TempDir()
	outside := filepath.Join(outsideDir, "out.go")
	writeFile(t, outside, "package o\n")
	c.files.RecordRead(t.Context(), c.sessionID, outside)
	writeFile(t, outside, "package o\n// changed\n")

	other := &funcTool{name: "other", run: func(context.Context, fantasy.ToolCall) (fantasy.ToolResponse, error) {
		return fantasy.NewTextResponse("done"), nil
	}}
	resp := c.runWith(t, c.wrap(other), other.Info().Name, "{}")
	require.Equal(t, "done\n\nThese files changed since you read them: "+outside+". View them again before you edit them.", resp)
}

// The note is at the end of the tool's result, so the result's own text
// is unchanged.
func TestChangedReadFilesNoteAtEndOfResult(t *testing.T) {
	t.Parallel()
	c := newChangedFilesTest(t)
	file := c.writeAndRead(t, "a.go", "package a\n")
	writeFile(t, file, "package a\n// changed\n")

	other := &funcTool{name: "other", run: func(context.Context, fantasy.ToolCall) (fantasy.ToolResponse, error) {
		return fantasy.NewTextResponse("the tool's own output"), nil
	}}
	resp := c.runWith(t, c.wrap(other), other.Info().Name, "{}")
	require.Equal(t, "the tool's own output\n\nThese files changed since you read them: a.go. View them again before you edit them.", resp)
}

// A file changed on disk is named on the next successful tool call after
// the change, even if an earlier call erred: the file is only marked as
// named when the note is actually appended, so an erroring call does not
// consume the naming.
func TestChangedReadFilesNamesAChangeOnAnErringCall(t *testing.T) {
	t.Parallel()
	c := newChangedFilesTest(t)
	file := c.writeAndRead(t, "a.go", "package a\n")
	writeFile(t, file, "package a\n// changed\n")

	other := &funcTool{name: "other", run: func(context.Context, fantasy.ToolCall) (fantasy.ToolResponse, error) {
		return fantasy.NewTextResponse("the tool's output"), errors.New("the tool failed")
	}}
	done := &funcTool{name: "other", run: func(context.Context, fantasy.ToolCall) (fantasy.ToolResponse, error) {
		return fantasy.NewTextResponse("done"), nil
	}}
	// One wrapper, so the named state is shared between the erring call
	// and the next one, as in the coordinator.
	named := newNamedChanges()
	tool := &changedReadFiles{files: c.files, workingDir: c.workingDir, named: named}
	tool.AgentTool = other
	resp, err := tool.Run(c.ctx, fantasy.ToolCall{ID: "call", Name: other.Info().Name, Input: "{}"})
	require.Error(t, err)
	// The note is not appended to an error response.
	require.Equal(t, "the tool's output", resp.Content)
	// The file is not marked named on the erroring call, so it is named
	// on the next successful call.
	tool.AgentTool = done
	resp2 := c.runWith(t, tool, other.Info().Name, "{}")
	require.Contains(t, resp2, "a.go")
	// Named once: not named again on the call after that.
	require.Equal(t, "done", c.runWith(t, tool, other.Info().Name, "{}"))
}

// A file that changes a second time without an intervening re-read is
// named again: the named state tracks the content at naming, so a
// different content is a new change.
func TestChangedReadFilesNamesASecondChange(t *testing.T) {
	t.Parallel()
	c := newChangedFilesTest(t)
	file := c.writeAndRead(t, "a.go", "package a\n")

	other := &funcTool{name: "other", run: func(context.Context, fantasy.ToolCall) (fantasy.ToolResponse, error) {
		return fantasy.NewTextResponse("done"), nil
	}}
	named := newNamedChanges()
	tool := &changedReadFiles{files: c.files, workingDir: c.workingDir, named: named}
	tool.AgentTool = other

	// First change: named on the next call.
	writeFile(t, file, "package a\n// changed 1\n")
	resp := c.runWith(t, tool, other.Info().Name, "{}")
	require.Contains(t, resp, "a.go")

	// Second change, no re-read: named again on the next call.
	writeFile(t, file, "package a\n// changed 2\n")
	resp = c.runWith(t, tool, other.Info().Name, "{}")
	require.Contains(t, resp, "a.go")
}

// An edit of one file names a different stale file in its result: the
// general note is not suppressed for edit calls, only the file the edit
// itself writes is excluded.
func TestChangedReadFilesEditNamesADifferentStaleFile(t *testing.T) {
	t.Parallel()
	c := newChangedFilesTest(t)
	// Two files, both read.
	staleFile := c.writeAndRead(t, "stale.go", "package s\n")
	c.writeAndRead(t, "edit.go", "package e\n")
	// The stale file is changed externally.
	writeFile(t, staleFile, "package s\n// changed\n")

	edit := &funcTool{name: tools.EditToolName, run: func(context.Context, fantasy.ToolCall) (fantasy.ToolResponse, error) {
		return fantasy.NewTextResponse("edited"), nil
	}}
	named := newNamedChanges()
	tool := &changedReadFiles{files: c.files, workingDir: c.workingDir, named: named}
	tool.AgentTool = edit
	resp := c.runWith(t, tool, tools.EditToolName, `{"file_path":"edit.go"}`)
	// The stale file is named in the edit's result.
	require.Contains(t, resp, "stale.go")
}

// A file that is deleted and then recreated with different content is
// named again: the named state tracks the content at naming, and a nil
// (deleted) content does not suppress a subsequent naming.
func TestChangedReadFilesNamesADeletedThenRecreatedFile(t *testing.T) {
	t.Parallel()
	c := newChangedFilesTest(t)
	file := c.writeAndRead(t, "a.go", "package a\n")

	other := &funcTool{name: "other", run: func(context.Context, fantasy.ToolCall) (fantasy.ToolResponse, error) {
		return fantasy.NewTextResponse("done"), nil
	}}
	named := newNamedChanges()
	tool := &changedReadFiles{files: c.files, workingDir: c.workingDir, named: named}
	tool.AgentTool = other

	// First change: named on the next call.
	writeFile(t, file, "package a\n// changed\n")
	resp := c.runWith(t, tool, other.Info().Name, "{}")
	require.Contains(t, resp, "a.go")

	// The file is deleted, then recreated with different content.
	require.NoError(t, os.Remove(file))
	writeFile(t, file, "package a\n// recreated\n")

	// The recreated file is named again on the next call.
	resp = c.runWith(t, tool, other.Info().Name, "{}")
	require.Contains(t, resp, "a.go")
}

// A view of a stale file does not name the file in its own result: the
// view just re-read the file, so telling the model "view it again" in
// the very result of the view is noise.
func TestChangedReadFilesViewDoesNotNameViewedFile(t *testing.T) {
	t.Parallel()
	c := newChangedFilesTest(t)
	file := c.writeAndRead(t, "a.go", "package a\n")
	// The file is changed externally, making it stale.
	writeFile(t, file, "package a\n// changed\n")

	view := &funcTool{name: tools.ViewToolName, run: func(context.Context, fantasy.ToolCall) (fantasy.ToolResponse, error) {
		// The view tool records the read, as the real one does.
		c.files.RecordRead(t.Context(), c.sessionID, file)
		return fantasy.NewTextResponse("viewed"), nil
	}}
	named := newNamedChanges()
	tool := &changedReadFiles{files: c.files, workingDir: c.workingDir, named: named}
	tool.AgentTool = view
	resp := c.runWith(t, tool, tools.ViewToolName, `{"file_path":"a.go"}`)
	// The viewed file is not named in its own result.
	require.NotContains(t, resp, "a.go")
	// The file is not named on the next call either (it was cleared).
	other := &funcTool{name: "other", run: func(context.Context, fantasy.ToolCall) (fantasy.ToolResponse, error) {
		return fantasy.NewTextResponse("done"), nil
	}}
	tool.AgentTool = other
	resp = c.runWith(t, tool, other.Info().Name, "{}")
	require.Equal(t, "done", resp)
}

// Concurrent access to namedChanges from multiple sessions does not
// panic: the mutex guards the map.
func TestNamedChangesConcurrentAccess(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	file := filepath.Join(dir, "a.go")
	writeFile(t, file, "package a\n")

	named := newNamedChanges()
	const sessions = 10
	const iters = 50
	var wg sync.WaitGroup
	for i := 0; i < sessions; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			sid := fmt.Sprintf("session-%d", id)
			for j := 0; j < iters; j++ {
				named.name(sid, file)
				_ = named.isNamed(sid, file)
				named.clear(sid, file)
			}
		}(i)
	}
	wg.Wait()
}

// Concurrent isNamed and name calls in different sessions do not mix up
// their records: each session's file is named with its own content, and
// counts as named until that content changes.
func TestNamedChangesIsNamedNoStaleResult(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	named := newNamedChanges()
	const sessions = 5
	const iters = 100
	var wg sync.WaitGroup
	for i := 0; i < sessions; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			sid := fmt.Sprintf("session-%d", id)
			file := filepath.Join(dir, fmt.Sprintf("a%d.go", id))
			writeFile(t, file, fmt.Sprintf("package a\nv%d\n", id))
			for j := 0; j < iters; j++ {
				// Name the file with the current content.
				named.name(sid, file)
				// The file should be named (content matches).
				if !named.isNamed(sid, file) {
					t.Errorf("session %d: isNamed returned false immediately after name", id)
					return
				}
				// Change the file content.
				writeFile(t, file, fmt.Sprintf("package a\nv%d-%d\n", id, j))
				// Now isNamed should return false (content changed).
				if named.isNamed(sid, file) {
					t.Errorf("session %d: isNamed returned true after content change", id)
					return
				}
			}
		}(i)
	}
	wg.Wait()
}
