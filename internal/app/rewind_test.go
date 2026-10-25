package app

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/charmbracelet/crush/internal/agent/agenttest"
	"github.com/charmbracelet/crush/internal/agent/tools"
	"github.com/charmbracelet/crush/internal/config"
	"github.com/charmbracelet/crush/internal/db"
	"github.com/charmbracelet/crush/internal/filetracker"
	"github.com/charmbracelet/crush/internal/history"
	"github.com/charmbracelet/crush/internal/message"
	"github.com/charmbracelet/crush/internal/session"
	"github.com/stretchr/testify/require"
)

// toolUse is a call of tool name with input, and its result recording
// meta.
func toolUse(id, name, input string, meta any, isError bool) []message.Message {
	recorded, _ := json.Marshal(meta)
	return []message.Message{
		{Role: message.Assistant, Parts: []message.ContentPart{message.ToolCall{ID: id, Name: name, Input: input, Finished: true}}},
		{Role: message.Tool, Parts: []message.ContentPart{message.ToolResult{ToolCallID: id, Name: name, Metadata: string(recorded), IsError: isError}}},
	}
}

// What a file held before the agent first changed it is what the file
// tool that changed it recorded: an edit's old content, with the Windows
// line endings it read the file with (an edit recorded before the crlf
// flag shows them in what it wrote), and nothing for a file it made; a
// write's old content, unless it made the file, and unknown for one
// recorded before writes kept it; a symbol's replacement's; each file of a
// rename's. A later change of the same file, a call that failed, a call
// with no path and another tool change nothing.
func TestFilesBefore(t *testing.T) {
	t.Parallel()

	var msgs []message.Message
	use := func(id, name, input string, meta any, isError bool) {
		msgs = append(msgs, toolUse(id, name, input, meta, isError)...)
	}
	use("1", tools.EditToolName, `{"file_path":"a.go"}`, tools.EditResponseMetadata{OldContent: "package a\n", NewContent: "package b\n"}, false)
	use("2", tools.EditToolName, `{"file_path":"a.go"}`, tools.EditResponseMetadata{OldContent: "package b\n", NewContent: "package c\n"}, false)
	use("3", tools.EditToolName, `{"file_path":"/abs/new.go","old_string":""}`, tools.EditResponseMetadata{NewContent: "package n\n"}, false)
	use("4", tools.EditToolName, `{"file_path":"win.txt"}`, tools.EditResponseMetadata{OldContent: "a\nb\n", NewContent: "a\r\nc\r\n", CRLF: true}, false)
	use("5", tools.MultiEditToolName, `{"file_path":"old-crlf.txt"}`, map[string]any{"old_content": "x\n", "new_content": "y\r\n"}, false)
	use("6", tools.MultiEditToolName, `{"file_path":"lf.txt"}`, tools.MultiEditResponseMetadata{OldContent: "x\n", NewContent: "y\r\n"}, false)
	use("7", tools.WriteToolName, `{"file_path":"w.txt"}`, tools.WriteResponseMetadata{OldContent: "was\n"}, false)
	use("8", tools.WriteToolName, `{"file_path":"made.txt"}`, tools.WriteResponseMetadata{Created: true}, false)
	use("9", tools.WriteToolName, `{"file_path":"old-write.txt"}`, map[string]any{"diff": "-was\n+is\n"}, false)
	use("10", tools.ReplaceSymbolToolName, `{}`, tools.ReplaceSymbolResponseMetadata{FilePath: "sym.go", OldContent: "func f() {}\n"}, false)
	use("11", tools.RenameToolName, `{}`, tools.RenameResponseMetadata{Files: []tools.RenamedFile{{Path: "/abs/r1.go", OldContent: "r1"}, {Path: "/abs/r2.go", OldContent: "r2"}}}, false)
	use("12", tools.EditToolName, `{"file_path":"denied.go"}`, tools.EditResponseMetadata{OldContent: "d"}, true)
	use("13", tools.BashToolName, `{"command":"rm x.go"}`, nil, false)
	use("14", tools.EditToolName, `{}`, tools.EditResponseMetadata{OldContent: "?"}, false)

	require.Equal(t, []fileBefore{
		{path: "/w/a.go", content: "package a\n", existed: true, known: true},
		{path: "/abs/new.go", known: true},
		{path: "/w/win.txt", content: "a\r\nb\r\n", existed: true, known: true},
		{path: "/w/old-crlf.txt", content: "x\r\n", existed: true, known: true},
		{path: "/w/lf.txt", content: "x\n", existed: true, known: true},
		{path: "/w/w.txt", content: "was\n", existed: true, known: true},
		{path: "/w/made.txt", known: true},
		{path: "/w/old-write.txt"},
		{path: "/w/sym.go", content: "func f() {}\n", existed: true, known: true},
		{path: "/abs/r1.go", content: "r1", existed: true, known: true},
		{path: "/abs/r2.go", content: "r2", existed: true, known: true},
	}, filesBefore(msgs, "/w"))
}

// Restoring puts a changed file back with its mode, removes one the agent
// made, writes back one deleted since, and leaves alone (and does not
// count) one already as it was; a symbolic link, what is not a file now
// and a file whose content before is not known are skipped. A dry run
// says the same and changes nothing.
func TestRestoreFiles(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	path := func(name string) string { return filepath.Join(dir, name) }
	write := func(name, content string, mode os.FileMode) {
		require.NoError(t, os.WriteFile(path(name), []byte(content), mode))
	}
	read := func(name string) string {
		data, err := os.ReadFile(path(name))
		require.NoError(t, err)
		return string(data)
	}
	write("changed.go", "package b\n", 0o600)
	write("made.go", "package m\n", 0o644)
	write("same.go", "package s\n", 0o644)
	require.NoError(t, os.Symlink(path("same.go"), path("link.go")))
	require.NoError(t, os.Mkdir(path("dir"), 0o755))
	files := []fileBefore{
		{path: path("changed.go"), content: "package a\n", existed: true, known: true},
		{path: path("made.go"), known: true},
		{path: path("sub/deleted.go"), content: "package d\n", existed: true, known: true},
		{path: path("same.go"), content: "package s\n", existed: true, known: true},
		{path: path("link.go"), content: "x", existed: true, known: true},
		{path: path("dir"), content: "x", existed: true, known: true},
		{path: path("unknown.go")},
		{path: path("gone.go"), known: true},
	}

	preview, err := restoreFiles(files, true)
	require.NoError(t, err)
	require.Equal(t, []string{path("changed.go"), path("made.go"), path("sub/deleted.go")}, preview.Files)
	require.Equal(t, 2, preview.Additions)
	require.Equal(t, 2, preview.Removals)
	require.Equal(t, []string{
		path("link.go") + ": a symbolic link",
		path("dir") + ": not a file now",
		path("unknown.go") + ": what it held before is not recorded",
	}, preview.Skipped)
	require.Equal(t, "package b\n", read("changed.go"), "a dry run changes nothing")
	require.FileExists(t, path("made.go"))
	require.NoFileExists(t, path("sub/deleted.go"))

	result, err := restoreFiles(files, false)
	require.NoError(t, err)
	require.Equal(t, preview, result)
	require.Equal(t, "package a\n", read("changed.go"))
	info, err := os.Stat(path("changed.go"))
	require.NoError(t, err)
	require.Equal(t, os.FileMode(0o600), info.Mode().Perm())
	require.NoFileExists(t, path("made.go"))
	require.Equal(t, "package d\n", read("sub/deleted.go"))
	target, err := os.Readlink(path("link.go"))
	require.NoError(t, err)
	require.Equal(t, path("same.go"), target)
}

// The todos before a prompt are what the last todos call before it set,
// one that failed set nothing, and with no call there are none.
func TestTodosBefore(t *testing.T) {
	t.Parallel()

	todos := func(id, list string, isError bool) []message.Message {
		return toolUse(id, tools.TodosToolName, `{"todos":`+list+`}`, nil, isError)
	}
	var msgs []message.Message
	msgs = append(msgs, todos("1", `[{"content":"a","status":"pending","active_form":"doing a"}]`, false)...)
	msgs = append(msgs, todos("2", `[{"content":"b","status":"completed"}]`, false)...)
	msgs = append(msgs, todos("3", `[{"content":"refused","status":"pending"}]`, true)...)
	require.Nil(t, todosBefore(nil))
	require.Equal(t, []session.Todo{{Content: "a", Status: session.TodoStatusPending, ActiveForm: "doing a"}}, todosBefore(msgs[:2]))
	require.Equal(t, []session.Todo{{Content: "b", Status: session.TodoStatusCompleted}}, todosBefore(msgs))
}

// busyCoordinator is an agent that is always busy.
type busyCoordinator struct{ agenttest.NopCoordinator }

func (busyCoordinator) IsSessionBusy(string) bool { return true }

// rewoundCoordinator is an agent that records what it is told a rewind
// cut.
type rewoundCoordinator struct {
	agenttest.NopCoordinator
	sessionID string
	removed   []string
}

func (c *rewoundCoordinator) Rewound(sessionID string, removed []string) {
	c.sessionID, c.removed = sessionID, removed
}

// /rewind, as in Claude Code: a dry run says what restoring the code to
// the point before a prompt changes; restoring the code puts back what
// the agent changed since and leaves the conversation as it was;
// restoring the conversation keeps the whole of it in a session of its
// own, then cuts it back to before the prompt, forgetting the reads made
// since and putting back the todos it had then, with the context's size
// unknown until the next answer, and the agent is told what was cut. Only
// a prompt the model still reads can be gone back to, and not while the
// agent works.
func TestRewind(t *testing.T) {
	t.Parallel()

	ctx := t.Context()
	conn, err := db.Connect(ctx, t.TempDir())
	require.NoError(t, err)
	t.Cleanup(func() { _ = conn.Close() })
	q := db.New(conn)
	sessions := session.NewService(q, conn)
	messages := message.NewService(q)
	dir := t.TempDir()
	coordinator := &rewoundCoordinator{}
	a := &App{Sessions: sessions, Messages: messages, FileTracker: filetracker.NewService(q), History: history.NewService(q, conn), AgentCoordinator: coordinator}
	a.config = config.NewTestStoreIn(&config.Config{}, dir)

	src, err := sessions.Create(ctx, "fix-login-bug")
	require.NoError(t, err)
	file := filepath.Join(dir, "a.txt")
	var ids []string
	add := func(role message.MessageRole, parts ...message.ContentPart) {
		msg, err := messages.Create(ctx, src.ID, message.CreateMessageParams{Role: role, Parts: parts})
		require.NoError(t, err)
		ids = append(ids, msg.ID)
	}
	use := func(id, name, input string, meta any) {
		for _, msg := range toolUse(id, name, input, meta, false) {
			add(msg.Role, msg.Parts...)
		}
	}
	add(message.User, message.TextContent{Text: "make a.txt"})
	use("t1", tools.WriteToolName, `{"file_path":"a.txt"}`, tools.WriteResponseMetadata{Created: true})
	use("t2", tools.TodosToolName, `{"todos":[{"content":"change a","status":"pending"}]}`, nil)
	add(message.Assistant, message.TextContent{Text: "Made it."})
	add(message.User, message.TextContent{Text: "change a.txt"})
	use("t3", tools.EditToolName, `{"file_path":"`+file+`"}`, tools.EditResponseMetadata{OldContent: "a\n", NewContent: "a\nb\n"})
	use("t4", tools.TodosToolName, `{"todos":[{"content":"change a","status":"completed"}]}`, nil)
	add(message.Assistant, message.TextContent{Text: "Changed it."})
	prompt := ids[6]
	require.NoError(t, os.WriteFile(file, []byte("a\nb\n"), 0o644))
	src.Todos = []session.Todo{{Content: "change a", Status: session.TodoStatusCompleted}}
	src.PromptTokens, src.CompletionTokens = 1200, 34
	src, err = sessions.Save(ctx, src)
	require.NoError(t, err)
	before := filepath.Join(dir, "before.txt")
	a.FileTracker.RecordRead(ctx, src.ID, before)
	_, err = conn.ExecContext(ctx, "UPDATE read_files SET read_at = read_at - 100 WHERE session_id = ?", src.ID)
	require.NoError(t, err)
	since := filepath.Join(dir, "since.txt")
	a.FileTracker.RecordRead(ctx, src.ID, since)
	conversation := func(sessionID string) []string {
		msgs, err := messages.List(ctx, sessionID)
		require.NoError(t, err)
		texts := make([]string, len(msgs))
		for i, msg := range msgs {
			texts[i] = string(msg.Role) + ":" + msg.Content().Text
		}
		return texts
	}
	whole := conversation(src.ID)
	require.Len(t, whole, 12)

	preview, err := a.Rewind(ctx, src.ID, prompt, RewindOptions{DryRun: true, Conversation: true})
	require.NoError(t, err)
	require.Equal(t, RewindResult{Files: []string{file}, Removals: 1}, preview)
	data, err := os.ReadFile(file)
	require.NoError(t, err)
	require.Equal(t, "a\nb\n", string(data), "a dry run changes nothing")
	require.Equal(t, whole, conversation(src.ID), "not even the conversation")

	_, err = a.Rewind(ctx, src.ID, "no-such-message", RewindOptions{Code: true})
	require.EqualError(t, err, "message not found")
	_, err = a.Rewind(ctx, src.ID, ids[1], RewindOptions{Code: true})
	require.EqualError(t, err, "message not found", "not a prompt")

	result, err := a.Rewind(ctx, src.ID, prompt, RewindOptions{Code: true})
	require.NoError(t, err)
	require.Equal(t, RewindResult{Files: []string{file}, Removals: 1}, result)
	data, err = os.ReadFile(file)
	require.NoError(t, err)
	require.Equal(t, "a\n", string(data))
	require.Equal(t, whole, conversation(src.ID), "the conversation stays")
	require.Nil(t, coordinator.removed, "and nothing is cut")

	result, err = a.Rewind(ctx, src.ID, prompt, RewindOptions{Conversation: true})
	require.NoError(t, err)
	require.Empty(t, result.Files)
	require.Equal(t, "fix-login-bug (before rewind)", result.Kept.Title)
	require.Equal(t, whole, conversation(result.Kept.ID), "the whole conversation is kept")
	require.Equal(t, whole[:6], conversation(src.ID))
	require.Equal(t, src.ID, coordinator.sessionID)
	require.Equal(t, ids[6:], coordinator.removed, "the agent is told what was cut")
	rewound, err := sessions.Get(ctx, src.ID)
	require.NoError(t, err)
	require.Equal(t, []session.Todo{{Content: "change a", Status: session.TodoStatusPending}}, rewound.Todos)
	require.Zero(t, rewound.PromptTokens)
	require.Zero(t, rewound.CompletionTokens)
	require.False(t, a.FileTracker.LastReadTime(ctx, src.ID, before).IsZero(), "a read before the prompt stays")
	require.True(t, a.FileTracker.LastReadTime(ctx, src.ID, since).IsZero(), "a read since is forgotten")
	require.False(t, a.FileTracker.LastReadTime(ctx, result.Kept.ID, since).IsZero(), "the kept conversation keeps it")
	data, err = os.ReadFile(file)
	require.NoError(t, err)
	require.Equal(t, "a\n", string(data), "restoring the conversation leaves the code")

	rewound.SummaryMessageID = ids[5]
	_, err = sessions.Save(ctx, rewound)
	require.NoError(t, err)
	_, err = a.Rewind(ctx, src.ID, ids[0], RewindOptions{Conversation: true})
	require.EqualError(t, err, "that message is no longer in the active context: choose a more recent one")

	a.AgentCoordinator = &busyCoordinator{}
	_, err = a.Rewind(ctx, src.ID, ids[0], RewindOptions{Code: true})
	require.EqualError(t, err, "the agent is busy in this session")
}
