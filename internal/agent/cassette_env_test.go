package agent

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/stretchr/testify/require"
)

// A cassette test whose fixed directory is another user's is skipped, with
// the reason, rather than failed. Root can use any directory, so only
// another user sees this.
func TestCassetteEnvSkipsADirectoryItCannotUse(t *testing.T) {
	if runtime.GOOS == "windows" || os.Geteuid() == 0 {
		t.Skip("needs a user that directory permissions apply to")
	}
	locked := filepath.Join("/tmp/crush-test/", t.Name())
	require.NoError(t, os.MkdirAll(locked, 0o755))
	require.NoError(t, os.Chmod(locked, 0o555))
	t.Cleanup(func() {
		os.Chmod(locked, 0o755)
		os.RemoveAll(locked)
	})

	var inner *testing.T
	t.Run("inside", func(t *testing.T) {
		inner = t
		cassetteEnv(t)
		t.Error("cassetteEnv went on in a directory it cannot use")
	})
	require.True(t, inner.Skipped())
}
