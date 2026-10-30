package cmd

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"

	"github.com/charmbracelet/crush/internal/client"
	"github.com/charmbracelet/crush/internal/db"
	"github.com/charmbracelet/crush/internal/proto"
	"github.com/charmbracelet/crush/internal/session"
	"github.com/stretchr/testify/require"
)

// `gnosh run -r <name>`, as Claude Code's `claude -p --resume <title>`:
// the one session with that name, ignoring case; several are an error
// that lists their IDs; none is the ID lookup's error. An ID still
// works, and --session (byName false) takes IDs only.
func TestLocalSessionIDByName(t *testing.T) {
	t.Parallel()

	conn, err := db.Connect(t.Context(), t.TempDir())
	require.NoError(t, err)
	t.Cleanup(func() { _ = conn.Close() })
	svc := session.NewService(db.New(conn), conn)
	create := func(title string) session.Session {
		s, err := svc.Create(t.Context(), title)
		require.NoError(t, err)
		return s
	}
	login := create("fix-login-bug")
	auth1, auth2 := create("add-auth"), create("add-auth")

	id, err := localSessionID(t.Context(), svc, " FIX-login-bug", true)
	require.NoError(t, err)
	require.Equal(t, login.ID, id)

	id, err = localSessionID(t.Context(), svc, login.ID, false)
	require.NoError(t, err)
	require.Equal(t, login.ID, id)

	_, err = localSessionID(t.Context(), svc, "fix-login-bug", false)
	require.ErrorContains(t, err, "session not found: fix-login-bug")

	_, err = localSessionID(t.Context(), svc, "add-auth", true)
	require.ErrorContains(t, err, `--resume "add-auth" matches 2 sessions. Pass one of these session IDs to disambiguate:`)
	require.ErrorContains(t, err, auth1.ID)
	require.ErrorContains(t, err, auth2.ID)

	_, err = localSessionID(t.Context(), svc, "nothing", true)
	require.ErrorContains(t, err, "session not found: nothing")
}

// The same through the server: a name is looked up in its session list
// when it is no session's ID.
func TestClientSessionIDByName(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/workspaces/ws1/sessions" {
			http.NotFound(w, r)
			return
		}
		require.NoError(t, json.NewEncoder(w).Encode([]proto.Session{
			{ID: "s1", Title: "fix-login-bug"},
			{ID: "s2", Title: "add-auth"},
		}))
	}))
	defer srv.Close()
	u, err := url.Parse(srv.URL)
	require.NoError(t, err)
	c, err := client.NewClient(t.TempDir(), "tcp", u.Host)
	require.NoError(t, err)

	id, err := clientSessionID(t.Context(), c, "ws1", "Add-Auth", true)
	require.NoError(t, err)
	require.Equal(t, "s2", id)

	_, err = clientSessionID(t.Context(), c, "ws1", "add-auth", false)
	require.ErrorContains(t, err, "not found")
}
