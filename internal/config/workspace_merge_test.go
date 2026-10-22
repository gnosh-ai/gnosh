package config

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func baseConfig(t *testing.T) *Config {
	t.Helper()
	cfg := &Config{}
	cfg.setDefaults(t.TempDir(), t.TempDir())
	return cfg
}

func TestMergeWorkspaceConfig_MissingFileIsNotAnError(t *testing.T) {
	t.Parallel()

	cfg := baseConfig(t)
	merged, err := mergeWorkspaceConfig(cfg, filepath.Join(t.TempDir(), "nope.json"), t.TempDir())
	require.NoError(t, err, "a workspace config that was never created is normal")
	require.False(t, merged)
}

func TestMergeWorkspaceConfig_EmptyFileIsNotAnError(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "crush.json")
	require.NoError(t, os.WriteFile(path, nil, 0o600))

	merged, err := mergeWorkspaceConfig(baseConfig(t), path, t.TempDir())
	require.NoError(t, err)
	require.False(t, merged)
}

func TestMergeWorkspaceConfig_MergesAndReports(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "crush.json")
	require.NoError(t, os.WriteFile(path,
		[]byte(`{"hooks":{"Stop":[{"command":"exit 0"}]}}`), 0o600))

	cfg := baseConfig(t)
	merged, err := mergeWorkspaceConfig(cfg, path, t.TempDir())
	require.NoError(t, err)
	require.True(t, merged)
	require.Len(t, cfg.Hooks[EventStop], 1, "the workspace file's contents must land in cfg")
}

func TestMergeWorkspaceConfig_MalformedJSON(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "crush.json")
	require.NoError(t, os.WriteFile(path, []byte(`{"hooks": `), 0o600))

	_, err := mergeWorkspaceConfig(baseConfig(t), path, t.TempDir())
	require.ErrorContains(t, err, "invalid JSON in config file")
	require.ErrorContains(t, err, path, "the error must name the file to fix")
}

// The regression this extraction exists for: valid JSON of the wrong
// shape used to make the merge fail, and the whole workspace file was
// then dropped without a word.
func TestMergeWorkspaceConfig_UnmergeableShapeIsLoud(t *testing.T) {
	t.Parallel()

	for _, body := range []string{
		`{"hooks": "not-an-object"}`,
		`{"options": []}`,
		`[]`,
	} {
		t.Run(body, func(t *testing.T) {
			t.Parallel()
			path := filepath.Join(t.TempDir(), "crush.json")
			require.NoError(t, os.WriteFile(path, []byte(body), 0o600))

			cfg := baseConfig(t)
			merged, err := mergeWorkspaceConfig(cfg, path, t.TempDir())
			require.False(t, merged)
			require.Error(t, err, "a workspace config Crush cannot apply must not be dropped in silence")
			require.ErrorContains(t, err, path, "the error must name the file to fix")
		})
	}
}

// An unreadable file is a different problem from an absent one, and the
// user can only fix it if told.
func TestMergeWorkspaceConfig_UnreadableFileIsLoud(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root ignores file permissions")
	}
	t.Parallel()

	path := filepath.Join(t.TempDir(), "crush.json")
	require.NoError(t, os.WriteFile(path, []byte(`{}`), 0o600))
	require.NoError(t, os.Chmod(path, 0o000))
	t.Cleanup(func() { _ = os.Chmod(path, 0o600) })

	_, err := mergeWorkspaceConfig(baseConfig(t), path, t.TempDir())
	require.ErrorContains(t, err, "failed to read config file")
}

// A failed merge must not leave the caller's config half-rewritten.
func TestMergeWorkspaceConfig_FailureLeavesConfigUntouched(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "crush.json")
	require.NoError(t, os.WriteFile(path, []byte(`{"hooks": "not-an-object"}`), 0o600))

	cfg := baseConfig(t)
	cfg.Hooks = map[string][]HookConfig{EventPreToolUse: {{Command: "exit 0"}}}
	dataDir := cfg.Options.DataDirectory

	_, err := mergeWorkspaceConfig(cfg, path, t.TempDir())
	require.Error(t, err)
	require.Len(t, cfg.Hooks[EventPreToolUse], 1, "the pre-merge config must survive a failed merge")
	require.Equal(t, dataDir, cfg.Options.DataDirectory)
}
