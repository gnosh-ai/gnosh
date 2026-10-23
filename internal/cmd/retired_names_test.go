package cmd

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

// A run says what it does not read since the rename: a crash.json in the
// project, a CRASH_ variable. With KLASH_CLIENT_SERVER=1 it returned
// before saying it; both paths now call reportRetiredNames.
func TestReportRetiredNames(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("XDG_DATA_HOME", filepath.Join(home, ".local", "share"))

	var out bytes.Buffer
	reportRetiredNames(&out, t.TempDir())
	require.Empty(t, out.String(), "nothing to report")

	project := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(project, "crash.json"), []byte("{}"), 0o644))
	t.Setenv("CRASH_DISABLE_METRICS", "1")
	reportRetiredNames(&out, project)
	require.Contains(t, out.String(), "not read since the rename to Klash")
	require.Contains(t, out.String(), "crash.json (now klash.json)")
	require.Contains(t, out.String(), "CRASH_DISABLE_METRICS (now KLASH_DISABLE_METRICS)")
}
