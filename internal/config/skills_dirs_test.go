package config

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

// Skills are found under gnosh's name as under klash's and crush's: in
// ~/.config/gnosh/skills, ~/.config/klash/skills and ~/.config/crush/skills,
// the directory the config is in first, and in a project's .gnosh/skills
// beside its .klash/skills and .crush/skills.
func TestSkillsDirsUnderEveryName(t *testing.T) {
	xdg := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", xdg)
	t.Setenv("CRUSH_GLOBAL_CONFIG", "") // set by TestMain for isolation
	gnosh, klash, crush := filepath.Join(xdg, "gnosh", "skills"), filepath.Join(xdg, "klash", "skills"), filepath.Join(xdg, "crush", "skills")
	require.Equal(t, []string{gnosh, klash, crush}, GlobalSkillsDirs()[:3])
	require.NoError(t, os.MkdirAll(filepath.Join(xdg, "crush"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(xdg, "crush", "crush.json"), []byte("{}"), 0o644))
	require.Equal(t, []string{crush, gnosh, klash}, GlobalSkillsDirs()[:3], "the config's directory first")

	proj := t.TempDir()
	dirs := ProjectSkillsDir(proj)
	require.Contains(t, dirs, filepath.Join(proj, ".gnosh", "skills"))
	require.Contains(t, dirs, filepath.Join(proj, ".klash", "skills"))
	require.Contains(t, dirs, filepath.Join(proj, ".crush", "skills"))
}
