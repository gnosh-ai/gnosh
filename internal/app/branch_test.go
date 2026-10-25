package app

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/charmbracelet/crush/internal/db"
	"github.com/charmbracelet/crush/internal/filetracker"
	"github.com/charmbracelet/crush/internal/history"
	"github.com/charmbracelet/crush/internal/message"
	"github.com/charmbracelet/crush/internal/session"
	"github.com/stretchr/testify/require"
)

// /branch copies the conversation into a new session: every message in
// order, with its parts (a user message keeps its one finish), times,
// model and summary mark, the steps of the sub-agents it ran, the files
// it read and changed, the summary the session was compacted to, its
// todos and how full its context is; the source stays as it was. Without
// a title the branch is named after the source, "(Branch)" and then
// "(Branch 2)"; with one, by it. A session with no messages has nothing to
// branch, and a branch that cannot be finished is not kept.
func TestBranchSession(t *testing.T) {
	t.Parallel()

	ctx := t.Context()
	conn, err := db.Connect(ctx, t.TempDir())
	require.NoError(t, err)
	t.Cleanup(func() { _ = conn.Close() })
	q := db.New(conn)
	sessions := session.NewService(q, conn)
	messages := message.NewService(q)
	a := &App{Sessions: sessions, Messages: messages, FileTracker: filetracker.NewService(q), History: history.NewService(q, conn)}

	src, err := sessions.Create(ctx, "fix-login-bug")
	require.NoError(t, err)
	create := func(params message.CreateMessageParams) message.Message {
		msg, err := messages.Create(ctx, src.ID, params)
		require.NoError(t, err)
		return msg
	}
	create(message.CreateMessageParams{Role: message.User, Parts: []message.ContentPart{message.TextContent{Text: "fix it"}}})
	asked := create(message.CreateMessageParams{Role: message.Assistant, Model: "qwen3.8-27b", Provider: "standin", Parts: []message.ContentPart{
		message.TextContent{Text: "Fixed."},
		message.ToolCall{ID: "call-1", Name: "agent", Input: `{"prompt":"find the login code"}`, Finished: true},
		message.Finish{Reason: message.FinishReasonEndTurn, Time: 1700000123},
	}})
	sub, err := sessions.CreateTaskSession(ctx, sessions.CreateAgentToolSessionID(asked.ID, "call-1"), src.ID, "find the login code")
	require.NoError(t, err)
	for _, params := range []message.CreateMessageParams{
		{Role: message.User, Parts: []message.ContentPart{message.TextContent{Text: "find the login code"}}},
		{Role: message.Assistant, Parts: []message.ContentPart{message.TextContent{Text: "In auth.go."}}},
	} {
		_, err := messages.Create(ctx, sub.ID, params)
		require.NoError(t, err)
	}
	subBefore, err := messages.List(ctx, sub.ID)
	require.NoError(t, err)
	summary := create(message.CreateMessageParams{Role: message.Assistant, IsSummaryMessage: true, Parts: []message.ContentPart{
		message.TextContent{Text: "We fixed the login bug."}, message.Finish{Reason: message.FinishReasonEndTurn, Time: 1700000200},
	}})
	read := filepath.Join(t.TempDir(), "auth.go")
	require.NoError(t, os.WriteFile(read, []byte("package auth\n"), 0o644))
	a.FileTracker.RecordRead(ctx, src.ID, read)
	_, err = a.History.Create(ctx, src.ID, "login.go", "package login\n")
	require.NoError(t, err)
	_, err = a.History.CreateVersion(ctx, src.ID, "login.go", "package login // fixed\n")
	require.NoError(t, err)
	src.SummaryMessageID = summary.ID
	src.Todos = []session.Todo{{Content: "add a test", Status: "pending"}}
	src.PromptTokens, src.CompletionTokens = 1200, 34
	src, err = sessions.Save(ctx, src)
	require.NoError(t, err)
	before, err := messages.List(ctx, src.ID)
	require.NoError(t, err)
	require.Len(t, before, 3)

	branch, err := a.BranchSession(ctx, src.ID, "")
	require.NoError(t, err)
	require.Equal(t, "fix-login-bug (Branch)", branch.Title)
	require.NotEqual(t, src.ID, branch.ID)
	copied, err := messages.List(ctx, branch.ID)
	require.NoError(t, err)
	require.Len(t, copied, len(before))
	for i, msg := range copied {
		require.NotEqual(t, before[i].ID, msg.ID)
		require.Equal(t, branch.ID, msg.SessionID)
		require.Equal(t, before[i].Role, msg.Role)
		require.Equal(t, before[i].Parts, msg.Parts)
		require.Equal(t, before[i].CreatedAt, msg.CreatedAt, "its time, so the answer's duration holds")
		require.Equal(t, before[i].Model, msg.Model)
		require.Equal(t, before[i].Provider, msg.Provider)
		require.Equal(t, before[i].IsSummaryMessage, msg.IsSummaryMessage)
	}
	row, err := q.GetMessage(ctx, copied[1].ID)
	require.NoError(t, err)
	require.Equal(t, int64(1700000123), row.FinishedAt.Int64, "its finish, for the response times in the stats")
	subCopy, err := sessions.Get(ctx, sessions.CreateAgentToolSessionID(copied[1].ID, "call-1"))
	require.NoError(t, err, "the sub-agent's steps go with its call")
	require.Equal(t, branch.ID, subCopy.ParentSessionID)
	require.Equal(t, "find the login code", subCopy.Title)
	subAfter, err := messages.List(ctx, subCopy.ID)
	require.NoError(t, err)
	require.Len(t, subAfter, len(subBefore))
	for i, msg := range subAfter {
		require.Equal(t, subBefore[i].Parts, msg.Parts)
	}
	require.Equal(t, a.FileTracker.LastReadTime(ctx, src.ID, read), a.FileTracker.LastReadTime(ctx, branch.ID, read), "it has read what it read")
	changed, err := a.History.ListBySession(ctx, src.ID)
	require.NoError(t, err)
	versions, err := a.History.ListBySession(ctx, branch.ID)
	require.NoError(t, err)
	require.Len(t, versions, 2, "what it changed, version by version")
	for i, v := range versions {
		require.NotEqual(t, changed[i].ID, v.ID)
		require.Equal(t, []any{changed[i].Path, changed[i].Content, changed[i].Version, changed[i].CreatedAt, changed[i].UpdatedAt},
			[]any{v.Path, v.Content, v.Version, v.CreatedAt, v.UpdatedAt})
	}
	latest, err := a.History.ListLatestSessionFiles(ctx, branch.ID)
	require.NoError(t, err)
	require.Len(t, latest, 1)
	require.Equal(t, "package login // fixed\n", latest[0].Content)
	require.Equal(t, copied[2].ID, branch.SummaryMessageID)
	stored, err := sessions.Get(ctx, branch.ID)
	require.NoError(t, err)
	require.Equal(t, copied[2].ID, stored.SummaryMessageID)
	require.Equal(t, src.Todos, stored.Todos)
	require.Equal(t, int64(1200), stored.PromptTokens)
	require.Equal(t, int64(34), stored.CompletionTokens)
	require.Equal(t, int64(3), stored.MessageCount)

	after, err := messages.List(ctx, src.ID)
	require.NoError(t, err)
	require.Equal(t, before, after, "the source stays as it was")

	second, err := a.BranchSession(ctx, src.ID, "")
	require.NoError(t, err)
	require.Equal(t, "fix-login-bug (Branch 2)", second.Title)
	named, err := a.BranchSession(ctx, src.ID, "try-the-cache")
	require.NoError(t, err)
	require.Equal(t, "try-the-cache", named.Title)

	empty, err := sessions.Create(ctx, "empty")
	require.NoError(t, err)
	_, err = a.BranchSession(ctx, empty.ID, "")
	require.EqualError(t, err, "no messages to branch")

	a.FileTracker = failingReads{a.FileTracker}
	_, err = a.BranchSession(ctx, src.ID, "doomed")
	require.EqualError(t, err, "copying the conversation: disk full")
	all, err := sessions.List(ctx)
	require.NoError(t, err)
	for _, s := range all {
		require.NotEqual(t, "doomed", s.Title, "half a branch is no branch")
	}
}

// failingReads is a file tracker that cannot copy a session's reads.
type failingReads struct{ filetracker.Service }

func (failingReads) CopyReads(context.Context, string, string) error {
	return errors.New("disk full")
}

// A branch is titled as Claude Code titles one: "(Branch)", then the first
// "(Branch N)" from 2 no session has.
func TestBranchTitle(t *testing.T) {
	t.Parallel()

	require.Equal(t, "a (Branch)", branchTitle("a", nil))
	require.Equal(t, "a (Branch 2)", branchTitle("a", []string{"a (Branch)"}))
	require.Equal(t, "a (Branch 3)", branchTitle("a", []string{"a (Branch)", "a (Branch 2)", "b (Branch 3)", "a (Branch 4)"}))
}
