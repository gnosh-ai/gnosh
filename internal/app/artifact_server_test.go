package app

import (
	"testing"

	"github.com/charmbracelet/crush/internal/config"
	"github.com/stretchr/testify/require"
)

// Only a session with a sidebar serves artifacts: a `klash run` is given
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
