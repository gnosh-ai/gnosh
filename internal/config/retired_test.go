package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// Files, directories and variables under the name from before the rename
// are reported with the name read in their place; nothing under the klash
// or crush names is.
func TestRetiredNames(t *testing.T) {
	configHome, dataHome, proj := t.TempDir(), t.TempDir(), t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", configHome)
	t.Setenv("XDG_DATA_HOME", dataHome)
	for _, kv := range os.Environ() {
		if name, _, _ := strings.Cut(kv, "="); strings.HasPrefix(name, "CRASH_") {
			t.Setenv(name, "")
			require.NoError(t, os.Unsetenv(name))
		}
	}

	require.NoError(t, os.WriteFile(filepath.Join(proj, "klash.json"), []byte("{}"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(proj, "crushrc"), []byte(""), 0o644))
	require.Empty(t, RetiredNames(proj), "klash and crush names are read, so not reported")

	require.NoError(t, os.WriteFile(filepath.Join(proj, "crash.json"), []byte("{}"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(proj, "crashrc"), []byte(""), 0o644))
	require.NoError(t, os.Mkdir(filepath.Join(proj, ".crash"), 0o755))
	require.NoError(t, os.Mkdir(filepath.Join(configHome, "crash"), 0o755))
	require.NoError(t, os.Mkdir(filepath.Join(dataHome, "crash"), 0o755))
	t.Setenv("CRASH_DISABLE_METRICS", "1")

	got := strings.Join(RetiredNames(proj), "\n")
	require.Contains(t, got, filepath.Join(proj, "crash.json")+" (now klash.json)")
	require.Contains(t, got, filepath.Join(proj, "crashrc")+" (now klashrc)")
	require.Contains(t, got, filepath.Join(proj, ".crash")+" (now .klash)")
	require.Contains(t, got, filepath.Join(configHome, "crash")+" (now "+filepath.Join(configHome, "klash")+")")
	require.Contains(t, got, filepath.Join(dataHome, "crash")+" (now "+filepath.Join(dataHome, "klash")+")")
	require.Contains(t, got, "CRASH_DISABLE_METRICS (now KLASH_DISABLE_METRICS)")
}
