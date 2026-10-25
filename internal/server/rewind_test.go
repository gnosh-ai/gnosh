package server

import (
	"encoding/json"
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

// POST .../sessions/{sid}/rewind rewinds the session as asked and answers
// with the session the whole conversation is kept in; a body that is not
// JSON is refused, and an unknown workspace is not found.
func TestPostSessionRewind(t *testing.T) {
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
	var ids []string
	for _, text := range []string{"one", "two"} {
		msg, err := a.Messages.Create(ctx, src.ID, message.CreateMessageParams{Role: message.User, Parts: []message.ContentPart{message.TextContent{Text: text}}})
		require.NoError(t, err)
		ids = append(ids, msg.ID)
	}

	b := backend.New(ctx, nil, nil)
	ws := &backend.Workspace{ID: uuid.New().String(), Path: t.TempDir(), App: a}
	backend.InsertWorkspaceForTest(b, ws)
	backend.SetWorkspaceShutdownFnForTest(ws, func() {})
	srv := &Server{backend: b}
	srv.installHandler()
	hs := httptest.NewServer(srv.Handler())
	t.Cleanup(hs.Close)
	post := func(wsID, body string) *http.Response {
		t.Helper()
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, hs.URL+"/v1/workspaces/"+wsID+"/sessions/"+src.ID+"/rewind", strings.NewReader(body))
		require.NoError(t, err)
		rsp, err := hs.Client().Do(req)
		require.NoError(t, err)
		t.Cleanup(func() { rsp.Body.Close() })
		return rsp
	}

	rsp := post(ws.ID, `{"message_id":"`+ids[1]+`","conversation":true}`)
	require.Equal(t, http.StatusOK, rsp.StatusCode)
	var result proto.RewindResult
	require.NoError(t, json.NewDecoder(rsp.Body).Decode(&result))
	require.NotNil(t, result.Kept)
	require.Equal(t, "fix-login-bug (before rewind)", result.Kept.Title)
	left, err := a.Messages.List(ctx, src.ID)
	require.NoError(t, err)
	require.Len(t, left, 1)
	require.Equal(t, http.StatusBadRequest, post(ws.ID, `{"message_id":`).StatusCode)
	require.Equal(t, http.StatusNotFound, post(uuid.New().String(), `{"message_id":"x"}`).StatusCode)
}
