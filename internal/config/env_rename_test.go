package config

import (
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/stretchr/testify/require"
)

// Path settings read from the environment answer to their KLASH_ names and
// to the CRUSH_ names from before the rename, the KLASH_ name winning.
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
			t.Setenv("KLASH_"+tc.setting, newer)
			require.Equal(t, tc.want(newer), tc.got(), "the new name wins")
		})
	}
}

// A prefixed variable stands in for the plain one while providers are
// configured; KLASH_ wins over CRUSH_, and the plain value comes back after.
func TestPushPopCrushEnvPrefersKlash(t *testing.T) {
	t.Setenv("PUSHPOP_TEST_KEY", "plain")
	t.Setenv("CRUSH_PUSHPOP_TEST_KEY", "old")
	restore := PushPopCrushEnv()
	require.Equal(t, "old", os.Getenv("PUSHPOP_TEST_KEY"))
	restore()
	require.Equal(t, "plain", os.Getenv("PUSHPOP_TEST_KEY"))

	t.Setenv("KLASH_PUSHPOP_TEST_KEY", "new")
	restore = PushPopCrushEnv()
	require.Equal(t, "new", os.Getenv("PUSHPOP_TEST_KEY"), "KLASH_ wins over CRUSH_")
	restore()
	require.Equal(t, "plain", os.Getenv("PUSHPOP_TEST_KEY"))
}

// Directories and config files take the name Klash, but where only the one
// from before the rename exists, that one is used, so an existing setup and
// its sessions keep working without being moved. Once the new one exists,
// it wins.
func TestPathsPreferKlashAndFallBackToCrush(t *testing.T) {
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
		require.Equal(t, filepath.Join(xdg, "klash", "klash.json"), GlobalConfig(), "a fresh setup gets the new name")

		mkdir(t, xdg, "crush")
		require.Equal(t, filepath.Join(xdg, "crush", "klash.json"), GlobalConfig(), "an existing crush directory is used")
		old := touch(t, xdg, "crush", "crush.json")
		require.Equal(t, old, GlobalConfig(), "and the crush.json in it")
		require.Equal(t, filepath.Join(xdg, "crush", "skills"), GlobalSkillsDirs()[0])

		// A klash directory made for something else must not hide the
		// crush one's config, hooks and skills.
		mkdir(t, xdg, "klash", "commands")
		require.Equal(t, old, GlobalConfig(), "a klash directory without a config does not take over")
		require.Equal(t, filepath.Join(xdg, "crush", "skills"), GlobalSkillsDirs()[0])

		newer := touch(t, xdg, "klash", "klash.json")
		require.Equal(t, newer, GlobalConfig(), "once the klash directory holds a config, it wins")
		require.Equal(t, filepath.Join(xdg, "klash", "skills"), GlobalSkillsDirs()[0])
	})

	t.Run("global config from a shell config alone", func(t *testing.T) {
		xdg := t.TempDir()
		t.Setenv("XDG_CONFIG_HOME", xdg)
		t.Setenv("CRUSH_GLOBAL_CONFIG", "") // set by TestMain for isolation
		mkdir(t, xdg, "crush")
		touch(t, xdg, "crush", "crushrc")
		mkdir(t, xdg, "klash", "templates")
		require.Equal(t, filepath.Join(xdg, "crush", "klash.json"), GlobalConfig(),
			"a crush directory with only a crushrc is in use")
	})

	t.Run("global data", func(t *testing.T) {
		xdg := t.TempDir()
		t.Setenv("XDG_DATA_HOME", xdg)
		t.Setenv("CRUSH_GLOBAL_DATA", "") // set by TestMain for isolation
		require.Equal(t, filepath.Join(xdg, "klash", "klash.json"), GlobalConfigData())
		mkdir(t, xdg, "crush")
		old := touch(t, xdg, "crush", "crush.json")
		require.Equal(t, old, GlobalConfigData(), "an existing setup's data directory is used")
		mkdir(t, xdg, "klash", "logs")
		require.Equal(t, old, GlobalConfigData(), "its OAuth tokens are not hidden by a bare klash directory")
	})

	t.Run("cache", func(t *testing.T) {
		xdg := t.TempDir()
		t.Setenv("XDG_CACHE_HOME", xdg)
		require.Equal(t, filepath.Join(xdg, "klash"), GlobalCacheDir())
		old := mkdir(t, xdg, "crush")
		require.Equal(t, old, GlobalCacheDir())
	})

	t.Run("project data directory", func(t *testing.T) {
		for _, tc := range []struct {
			name   string
			exists []string
			want   string
		}{
			{"fresh project", nil, ".klash"},
			{"project from before the rename", []string{".crush"}, ".crush"},
			{"both", []string{".crush", ".klash"}, ".klash"},
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
		newer := touch(t, cwd, "klash.json")
		paths := lookupConfigs(cwd)
		require.Contains(t, paths, old, "a crush.json is still read")
		require.Contains(t, paths, newer)
		require.Greater(t, slices.Index(paths, newer), slices.Index(paths, old), "klash.json is merged later, so it wins")
	})
}

// A log directory's log is klash.log, or the crush.log from before the
// rename where only that one exists.
func TestLogFile(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	require.Equal(t, filepath.Join(dir, "klash.log"), LogFile(dir))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "crush.log"), nil, 0o644))
	require.Equal(t, filepath.Join(dir, "crush.log"), LogFile(dir), "an existing log keeps growing")
	require.NoError(t, os.WriteFile(filepath.Join(dir, "klash.log"), nil, 0o644))
	require.Equal(t, filepath.Join(dir, "klash.log"), LogFile(dir))
}
