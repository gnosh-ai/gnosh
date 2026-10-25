package model

import (
	"context"
	"errors"
	"testing"

	"github.com/charmbracelet/crush/internal/ui/util"
	"github.com/charmbracelet/crush/internal/workspace"
	"github.com/stretchr/testify/require"
)

// addDirWorkspace answers /add-dir as told, recording the paths it was
// asked to add.
type addDirWorkspace struct {
	workspace.Workspace
	said  string
	added bool
	err   error
	asked []string
}

func (w *addDirWorkspace) AddDir(_ context.Context, path string) (string, bool, error) {
	w.asked = append(w.asked, path)
	return w.said, w.added, w.err
}

// /add-dir <path> asks the workspace to add it and says what it said: an
// info when it added it, a warning when it did not, and an error when it
// failed.
func TestAddDirCommand(t *testing.T) {
	t.Parallel()

	u := newTestUI()
	ws := &addDirWorkspace{said: "Added /src/gno as a working directory for this session.", added: true}
	u.com.Workspace = ws
	require.Equal(t, util.NewInfoMsg(ws.said), u.sendMessage("/add-dir ../gno")())
	require.Equal(t, []string{"../gno"}, ws.asked)

	ws.said, ws.added = "Please provide a directory path.", false
	require.Equal(t, util.NewWarnMsg(ws.said), u.sendMessage("/add-dir")())

	ws.err = errors.New("/add-dir cannot reach the server's permissions with KLASH_CLIENT_SERVER=1 yet")
	msg, ok := u.sendMessage("/add-dir x")().(util.InfoMsg)
	require.True(t, ok)
	require.Equal(t, util.InfoTypeError, msg.Type)
	require.Equal(t, "failed to add a working directory: /add-dir cannot reach the server's permissions with KLASH_CLIENT_SERVER=1 yet", msg.Msg)
}
