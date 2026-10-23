package prompt

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/charmbracelet/crush/internal/config"
	"github.com/stretchr/testify/require"
)

func writeContextFile(t *testing.T, dir, name, content string) string {
	t.Helper()
	path := filepath.Join(dir, name)
	require.NoError(t, os.WriteFile(path, []byte(content), 0o644))
	return path
}

func contextPaths(files []ContextFile) []string {
	var paths []string
	for _, f := range files {
		paths = append(paths, filepath.Base(f.Path))
	}
	return paths
}

// Context files come in the order their paths are configured, every time.
// They used to come in map order, so a rebuilt system prompt could list
// them differently, and a model server's prompt cache then had to read
// the whole conversation again.
func TestLoadContextFilesKeepsTheConfiguredOrder(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	var paths []string
	for _, name := range []string{"CLAUDE.md", "GEMINI.md", "KLASH.md", "CRUSH.md", "AGENTS.md"} {
		paths = append(paths, writeContextFile(t, dir, name, "about "+name))
	}
	store := config.NewTestStore(&config.Config{Options: &config.Options{}})

	for range 20 {
		require.Equal(t,
			[]string{"CLAUDE.md", "GEMINI.md", "KLASH.md", "CRUSH.md", "AGENTS.md"},
			contextPaths(loadContextFiles(paths, store)))
	}
}

// A file reached under two names, as a symlink makes it, is read once.
func TestLoadContextFilesReadsALinkedFileOnce(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	agents := writeContextFile(t, dir, "AGENTS.md", "the rules")
	claude := filepath.Join(dir, "CLAUDE.md")
	require.NoError(t, os.Symlink("AGENTS.md", claude))
	store := config.NewTestStore(&config.Config{Options: &config.Options{}})

	files := loadContextFiles([]string{claude, agents}, store)
	require.Len(t, files, 1, "CLAUDE.md -> AGENTS.md must not put the rules in the prompt twice")
	require.Equal(t, "the rules", files[0].Content)

	// The same path listed twice, in another case, is read once too.
	files = loadContextFiles([]string{agents, filepath.Join(dir, "agents.md")}, store)
	require.Len(t, files, 1)
}
