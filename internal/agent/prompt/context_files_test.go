package prompt

import (
	"fmt"
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
			contextPaths(loadContextFiles(paths, store, dir)))
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

	files := loadContextFiles([]string{claude, agents}, store, dir)
	require.Len(t, files, 1, "CLAUDE.md -> AGENTS.md must not put the rules in the prompt twice")
	require.Equal(t, "the rules", files[0].Content)

	// The same path listed twice, in another case, is read once too.
	files = loadContextFiles([]string{agents, filepath.Join(dir, "agents.md")}, store, dir)
	require.Len(t, files, 1)
}

// A context file's @path imports, Claude Code's, come just before it, as
// Claude Code reads them first, each file once: CLAUDE.md saying
// "@AGENTS.md", as Jae's repositories' do, puts AGENTS.md in the prompt
// once, before it. An import is relative to the file it is in, can sit
// inside a sentence, escapes a space with a backslash and ends at the
// first space it does not escape, and an @ in an address or a handle
// that names no file imports nothing.
func TestLoadContextFilesFollowsImports(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	require.NoError(t, os.Mkdir(filepath.Join(dir, "docs"), 0o755))
	require.NoError(t, os.Mkdir(filepath.Join(dir, "Design Docs"), 0o755))
	claude := writeContextFile(t, dir, "CLAUDE.md", "@AGENTS.md\n\nStyle: see @docs/style.md and @Design\\ Docs/look.md for looks. Mail jae@tendermint.com or ping @jaekwon.")
	agents := writeContextFile(t, dir, "AGENTS.md", "the rules")
	writeContextFile(t, dir, "docs/style.md", "tabs, and @naming.md")
	writeContextFile(t, dir, "docs/naming.md", "short names")
	writeContextFile(t, dir, "Design Docs/look.md", "plain")
	store := config.NewTestStore(&config.Config{Options: &config.Options{}})

	files := loadContextFiles([]string{claude, agents}, store, dir)
	require.Equal(t, []string{"AGENTS.md", "naming.md", "style.md", "look.md", "CLAUDE.md"}, contextPaths(files))
	require.Equal(t, "the rules", files[0].Content)
}

// Nothing in a code span or a fenced code block imports, as in Claude Code.
func TestLoadContextFilesImportsNothingFromCode(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	claude := writeContextFile(t, dir, "CLAUDE.md", "Run `@span.md` like so:\n```\n@fenced.md\n```\nand @after.md")
	for _, name := range []string{"span.md", "fenced.md", "after.md"} {
		writeContextFile(t, dir, name, name)
	}
	store := config.NewTestStore(&config.Config{Options: &config.Options{}})

	require.Equal(t, []string{"after.md", "CLAUDE.md"}, contextPaths(loadContextFiles([]string{claude}, store, dir)))
}

// Imports are followed four hops deep, as in Claude Code, and a file that
// imports one that imports it back is read once.
func TestLoadContextFilesImportDepthAndCycles(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	for i := range 8 {
		writeContextFile(t, dir, fmt.Sprintf("f%d.md", i), fmt.Sprintf("@f%d.md", i+1))
	}
	store := config.NewTestStore(&config.Config{Options: &config.Options{}})
	require.Equal(t,
		[]string{"f4.md", "f3.md", "f2.md", "f1.md", "f0.md"},
		contextPaths(loadContextFiles([]string{filepath.Join(dir, "f0.md")}, store, dir)))

	a := writeContextFile(t, dir, "a.md", "@b.md")
	writeContextFile(t, dir, "b.md", "@a.md")
	require.Equal(t, []string{"b.md", "a.md"}, contextPaths(loadContextFiles([]string{a}, store, dir)))
}

// A project's context files import only the project's files: by "..", by
// an absolute path or through a symlink, a file outside it is not read,
// or a repository could put the user's keys in front of the model's
// provider. The user's global context files, with no root, import
// anything.
func TestLoadContextFilesImportsStayInTheProject(t *testing.T) {
	t.Parallel()

	base := t.TempDir()
	secret := writeContextFile(t, base, "secret.md", "the key")
	project := filepath.Join(base, "project")
	require.NoError(t, os.Mkdir(project, 0o755))
	require.NoError(t, os.Symlink(secret, filepath.Join(project, "link.md")))
	store := config.NewTestStore(&config.Config{Options: &config.Options{}})

	for _, ref := range []string{"../secret.md", secret, "link.md"} {
		claude := writeContextFile(t, project, "CLAUDE.md", "@"+ref)
		require.Equal(t, []string{"CLAUDE.md"}, contextPaths(loadContextFiles([]string{claude}, store, project)), ref)
	}

	global := writeContextFile(t, t.TempDir(), "AGENTS.md", "@"+secret)
	files := loadContextFiles([]string{global}, store, "")
	require.Equal(t, []string{"secret.md", "AGENTS.md"}, contextPaths(files))
	require.Equal(t, "the key", files[0].Content)
}
