package config

import (
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/stretchr/testify/require"
)

// Path settings read from the environment answer to their CRASH_ names and
// to the CRUSH_ names from before the rename, the CRASH_ name winning.
func TestPathSettingsAnswerToBothNames(t *testing.T) {
	inDir := func(d string) string { return filepath.Join(d, appName+".json") }
	asIs := func(d string) string { return d }
	for _, tc := range []struct {
		setting string
		got     func() string
		want    func(dir string) string
	}{
		{"GLOBAL_CONFIG", GlobalConfig, inDir},
		{"GLOBAL_DATA", GlobalConfigData, inDir},
		{"CACHE_DIR", GlobalCacheDir, asIs},
		{"SKILLS_DIR", func() string { return GlobalSkillsDirs()[0] }, asIs},
	} {
		t.Run(tc.setting, func(t *testing.T) {
			old, newer := t.TempDir(), t.TempDir()
			t.Setenv("CRUSH_"+tc.setting, old)
			require.Equal(t, tc.want(old), tc.got(), "the name before the rename still works")
			t.Setenv("CRASH_"+tc.setting, newer)
			require.Equal(t, tc.want(newer), tc.got(), "the new name wins")
		})
	}
}

// A prefixed variable stands in for the plain one while providers are
// configured; CRASH_ wins over CRUSH_, and the plain value comes back after.
func TestPushPopCrushEnvPrefersCrash(t *testing.T) {
	t.Setenv("PUSHPOP_TEST_KEY", "plain")
	t.Setenv("CRUSH_PUSHPOP_TEST_KEY", "old")
	restore := PushPopCrushEnv()
	require.Equal(t, "old", os.Getenv("PUSHPOP_TEST_KEY"))
	restore()
	require.Equal(t, "plain", os.Getenv("PUSHPOP_TEST_KEY"))

	t.Setenv("CRASH_PUSHPOP_TEST_KEY", "new")
	restore = PushPopCrushEnv()
	require.Equal(t, "new", os.Getenv("PUSHPOP_TEST_KEY"), "CRASH_ wins over CRUSH_")
	restore()
	require.Equal(t, "plain", os.Getenv("PUSHPOP_TEST_KEY"))
}

// Directories and config files take the name Crash, but where only the one
// from before the rename exists, that one is used, so an existing setup and
// its sessions keep working without being moved. Once the new one exists,
// it wins.
func TestPathsPreferCrashAndFallBackToCrush(t *testing.T) {
	mkdir := func(t *testing.T, parts ...string) string {
		t.Helper()
		p := filepath.Join(parts...)
		require.NoError(t, os.MkdirAll(p, 0o755))
		return p
	}
	touch := func(t *testing.T, parts ...string) string {
		t.Helper()
		p := filepath.Join(parts...)
		require.NoError(t, os.WriteFile(p, []byte("{}"), 0o644))
		return p
	}

	t.Run("global config", func(t *testing.T) {
		xdg := t.TempDir()
		t.Setenv("XDG_CONFIG_HOME", xdg)
		t.Setenv("CRUSH_GLOBAL_CONFIG", "") // set by TestMain for isolation
		require.Equal(t, filepath.Join(xdg, "crash", "crash.json"), GlobalConfig(), "a fresh setup gets the new name")

		mkdir(t, xdg, "crush")
		require.Equal(t, filepath.Join(xdg, "crush", "crash.json"), GlobalConfig(), "an existing crush directory is used")
		old := touch(t, xdg, "crush", "crush.json")
		require.Equal(t, old, GlobalConfig(), "and the crush.json in it")
		require.Equal(t, filepath.Join(xdg, "crush", "skills"), GlobalSkillsDirs()[0])

		mkdir(t, xdg, "crash")
		require.Equal(t, filepath.Join(xdg, "crash", "crash.json"), GlobalConfig(), "once the crash directory exists, it wins")
	})

	t.Run("global data", func(t *testing.T) {
		xdg := t.TempDir()
		t.Setenv("XDG_DATA_HOME", xdg)
		t.Setenv("CRUSH_GLOBAL_DATA", "") // set by TestMain for isolation
		require.Equal(t, filepath.Join(xdg, "crash", "crash.json"), GlobalConfigData())
		mkdir(t, xdg, "crush")
		old := touch(t, xdg, "crush", "crush.json")
		require.Equal(t, old, GlobalConfigData(), "an existing setup's data directory is used")
	})

	t.Run("cache", func(t *testing.T) {
		xdg := t.TempDir()
		t.Setenv("XDG_CACHE_HOME", xdg)
		require.Equal(t, filepath.Join(xdg, "crash"), GlobalCacheDir())
		old := mkdir(t, xdg, "crush")
		require.Equal(t, old, GlobalCacheDir())
	})

	t.Run("project data directory", func(t *testing.T) {
		for _, tc := range []struct {
			name   string
			exists []string
			want   string
		}{
			{"fresh project", nil, ".crash"},
			{"project from before the rename", []string{".crush"}, ".crush"},
			{"both", []string{".crush", ".crash"}, ".crash"},
		} {
			t.Run(tc.name, func(t *testing.T) {
				workingDir := t.TempDir()
				for _, d := range tc.exists {
					mkdir(t, workingDir, d)
				}
				cfg := &Config{}
				cfg.setDefaults(workingDir, "")
				require.Equal(t, filepath.Join(workingDir, tc.want), cfg.Options.DataDirectory)
			})
		}
	})

	t.Run("project config files", func(t *testing.T) {
		t.Setenv("XDG_CONFIG_HOME", t.TempDir())
		t.Setenv("XDG_DATA_HOME", t.TempDir())
		cwd := t.TempDir()
		old := touch(t, cwd, "crush.json")
		newer := touch(t, cwd, "crash.json")
		paths := lookupConfigs(cwd)
		require.Contains(t, paths, old, "a crush.json is still read")
		require.Contains(t, paths, newer)
		require.Greater(t, slices.Index(paths, newer), slices.Index(paths, old), "crash.json is merged later, so it wins")
	})
}
