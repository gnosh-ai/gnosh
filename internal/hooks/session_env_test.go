package hooks

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/charmbracelet/crush/internal/config"
	"github.com/stretchr/testify/require"
)

// A SessionStart hook is handed the session's env file as CLAUDE_ENV_FILE,
// as Claude Code hands it, and as KLASH_ENV_FILE and CRUSH_ENV_FILE; with
// no file to hand, none of them is set.
func TestRunSessionStartHandsOverTheEnvFile(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	seen := filepath.Join(dir, "seen")
	cfg := &config.Config{Hooks: map[string][]config.HookConfig{EventSessionStart: {{
		Command: `echo "[${CLAUDE_ENV_FILE-unset}][${KLASH_ENV_FILE-unset}][${CRUSH_ENV_FILE-unset}]" > ` + seen,
	}}}}
	require.NoError(t, cfg.ValidateHooks())
	r := NewRunner(cfg.Hooks[EventSessionStart], dir, dir)

	envFile := filepath.Join(dir, "session-env", "s1.sh")
	_, err := r.RunSessionStart(context.Background(), "s1", "qwen", SessionStartSourceStartup, envFile)
	require.NoError(t, err)
	data, err := os.ReadFile(seen)
	require.NoError(t, err)
	require.Equal(t, "["+envFile+"]["+envFile+"]["+envFile+"]\n", string(data))

	_, err = r.RunSessionStart(context.Background(), "s1", "qwen", SessionStartSourceStartup, "")
	require.NoError(t, err)
	data, err = os.ReadFile(seen)
	require.NoError(t, err)
	require.Equal(t, "[unset][unset][unset]\n", string(data))
}
