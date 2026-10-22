package config

import (
	"os"
	"path/filepath"
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
