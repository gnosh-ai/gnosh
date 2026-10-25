package server

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/charmbracelet/crush/internal/app"
	"github.com/charmbracelet/crush/internal/backend"
	"github.com/charmbracelet/crush/internal/db"
	"github.com/charmbracelet/crush/internal/filetracker"
	"github.com/charmbracelet/crush/internal/history"
	"github.com/charmbracelet/crush/internal/message"
	"github.com/charmbracelet/crush/internal/proto"
	"github.com/charmbracelet/crush/internal/session"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

// POST .../sessions/{sid}/branch copies the session's conversation into a
// new session and answers with it: titled after the session with no body,
// by the title in the body with one. A body that is not JSON is refused, a
// workspace whose agent has not started has nothing to branch with, and an
// unknown workspace is not found.
func TestPostSessionBranch(t *testing.T) {
	ctx := t.Context()
	conn, err := db.Connect(ctx, t.TempDir())
	require.NoError(t, err)
	t.Cleanup(func() { _ = conn.Close() })
	q := db.New(conn)
	a := &app.App{
		Sessions:    session.NewService(q, conn),
		Messages:    message.NewService(q),
		FileTracker: filetracker.NewService(q),
		History:     history.NewService(q, conn),
	}
	src, err := a.Sessions.Create(ctx, "fix-login-bug")
	require.NoError(t, err)
	_, err = a.Messages.Create(ctx, src.ID, message.CreateMessageParams{Role: message.User, Parts: []message.ContentPart{message.TextContent{Text: "fix it"}}})
	require.NoError(t, err)

	b := backend.New(ctx, nil, nil)
	ws := &backend.Workspace{ID: uuid.New().String(), Path: t.TempDir(), App: a}
	backend.InsertWorkspaceForTest(b, ws)
	backend.SetWorkspaceShutdownFnForTest(ws, func() {})
	bare := uuid.New().String()
	backend.InsertWorkspaceForTest(b, &backend.Workspace{ID: bare, Path: t.TempDir()})
	srv := &Server{backend: b}
	srv.installHandler()
	hs := httptest.NewServer(srv.Handler())
	t.Cleanup(hs.Close)
	post := func(wsID, body string) *http.Response {
		t.Helper()
		var r io.Reader
		if body != "" {
			r = strings.NewReader(body)
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, hs.URL+"/v1/workspaces/"+wsID+"/sessions/"+src.ID+"/branch", r)
		require.NoError(t, err)
		rsp, err := hs.Client().Do(req)
		require.NoError(t, err)
		t.Cleanup(func() { rsp.Body.Close() })
		return rsp
	}
	branched := func(rsp *http.Response) proto.Session {
		t.Helper()
		require.Equal(t, http.StatusOK, rsp.StatusCode)
		var branch proto.Session
		require.NoError(t, json.NewDecoder(rsp.Body).Decode(&branch))
		require.NotEqual(t, src.ID, branch.ID)
		require.Equal(t, int64(1), branch.MessageCount)
		msgs, err := a.Messages.List(ctx, branch.ID)
		require.NoError(t, err)
		require.Len(t, msgs, 1)
		return branch
	}

	require.Equal(t, "fix-login-bug (Branch)", branched(post(ws.ID, "")).Title)
	require.Equal(t, "try-the-cache", branched(post(ws.ID, `{"title":"try-the-cache"}`)).Title)
	require.Equal(t, http.StatusBadRequest, post(ws.ID, `{"title":`).StatusCode)
	require.Equal(t, http.StatusBadRequest, post(bare, "").StatusCode)
	require.Equal(t, http.StatusNotFound, post(uuid.New().String(), "").StatusCode)
}
