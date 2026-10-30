package app

import (
	"net"
	"net/http"
	"strings"
	"testing"

	"github.com/charmbracelet/crush/internal/config"
	"github.com/stretchr/testify/require"
)

// Only a session with a sidebar serves artifacts: a `gnosh run` is given
// no artifact tool, so it would be a port open for nothing.
func TestArtifactServerFor(t *testing.T) {
	opts := &config.Options{ArtifactServer: "127.0.0.1:0", DataDirectory: t.TempDir()}

	srv := artifactServerFor(true, opts)
	require.NotNil(t, srv, "an interactive session serves what it records")
	t.Cleanup(func() { _ = srv.Close() })
	require.Contains(t, srv.BaseURL(), "127.0.0.1")

	require.Nil(t, artifactServerFor(false, opts), "a run has no sidebar")
	require.Nil(t, artifactServerFor(true, &config.Options{ArtifactServer: "off", DataDirectory: t.TempDir()}),
		`"off" serves nothing`)
}

// A `gnosh run` gives the port back. app.New initializes the coder agent
// interactively, and only then does a run re-initialize it headless, so
// the server made by that first init has to be closed — otherwise every
// headless run sits on the artifact port, and the next session with a
// sidebar finds it taken, falls back to a random port, and its URLs stop
// outliving it.
func TestARunGivesUpTheArtifactPort(t *testing.T) {
	opts := &config.Options{ArtifactServer: "127.0.0.1:0", DataDirectory: t.TempDir()}
	app := &App{}

	app.setArtifactServer(true, opts)
	require.NotNil(t, app.artifacts, "a session with a sidebar serves what it records")
	addr := strings.TrimPrefix(app.artifacts.BaseURL(), "http://")

	app.setArtifactServer(true, opts)
	require.Equal(t, addr, strings.TrimPrefix(app.artifacts.BaseURL(), "http://"), "a second interactive init keeps the one server")

	app.setArtifactServer(false, opts)
	require.Nil(t, app.artifacts, "a run must not hold the port a sidebar session opened")

	ln, err := net.Listen("tcp", addr)
	require.NoError(t, err, "the port must be free again, not held by a run")
	require.NoError(t, ln.Close())

	_, err = http.Get("http://" + addr + "/") //nolint:noctx // a loopback request in a test
	require.Error(t, err, "nothing answers there any more")
	require.True(t, strings.Contains(err.Error(), "refused") || strings.Contains(err.Error(), "reset"),
		"expected the connection to be refused, got %v", err)
}
