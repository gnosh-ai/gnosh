package prompt

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
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

// A context file's block-level HTML comments are notes for the people who
// keep it: the model does not get them, as in Claude Code, and an import
// one names is not followed. A comment in a fenced code block, one within
// a line of text, and one indented as code stay.
func TestLoadContextFilesStripHTMLComments(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	writeContextFile(t, dir, "notes.md", "the maintainers' notes")
	claude := writeContextFile(t, dir, "CLAUDE.md", strings.Join([]string{
		"# Rules",
		"<!-- keep this list short -->",
		"Use tabs.",
		"  <!--",
		"  old rule: @notes.md",
		"  -->",
		"Say why <!-- inline --> in commits.",
		"```html",
		"<!-- an example comment -->",
		"```",
		"    <!-- indented as code -->",
		"Done.",
	}, "\n"))
	store := config.NewTestStore(&config.Config{Options: &config.Options{}})

	files := LoadContextFiles([]string{claude}, store, dir)
	require.Equal(t, []string{"CLAUDE.md"}, contextPaths(files), "the commented-out import was followed")
	require.Equal(t, strings.Join([]string{
		"# Rules",
		"Use tabs.",
		"Say why <!-- inline --> in commits.",
		"```html",
		"<!-- an example comment -->",
		"```",
		"    <!-- indented as code -->",
		"Done.",
	}, "\n"), files[0].Content)

	require.Equal(t, "no comments here", stripHTMLComments("no comments here"))
	require.Equal(t, "a\nb", stripHTMLComments("a\n<!-->\nb"), "an empty comment ends on its own line")
}

// A context file over 4 MiB is not read at all, as Claude Code skips one.
func TestLoadContextFilesSkipAFileOver4MiB(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	big := writeContextFile(t, dir, "CLAUDE.md", "")
	require.NoError(t, os.Truncate(big, maxContextFileSize+1))
	exact := writeContextFile(t, dir, "AGENTS.md", "")
	require.NoError(t, os.Truncate(exact, maxContextFileSize))
	store := config.NewTestStore(&config.Config{Options: &config.Options{}})

	require.Equal(t, []string{"AGENTS.md"}, contextPaths(LoadContextFiles([]string{big, exact}, store, dir)))
}

// Context files come in the order their paths are configured, every time.
// They used to come in map order, so a rebuilt system prompt could list
// them differently, and a model server's prompt cache then had to read
// the whole conversation again.
func TestLoadContextFilesKeepsTheConfiguredOrder(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	var paths []string
	for _, name := range []string{"CLAUDE.md", "GEMINI.md", "GNOSH.md", "CRUSH.md", "AGENTS.md"} {
		paths = append(paths, writeContextFile(t, dir, name, "about "+name))
	}
	store := config.NewTestStore(&config.Config{Options: &config.Options{}})

	for range 20 {
		require.Equal(t,
			[]string{"CLAUDE.md", "GEMINI.md", "GNOSH.md", "CRUSH.md", "AGENTS.md"},
			contextPaths(LoadContextFiles(paths, store, dir)))
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

	files := LoadContextFiles([]string{claude, agents}, store, dir)
	require.Len(t, files, 1, "CLAUDE.md -> AGENTS.md must not put the rules in the prompt twice")
	require.Equal(t, "the rules", files[0].Content)

	// The same path listed twice, in another case, is read once too.
	files = LoadContextFiles([]string{agents, filepath.Join(dir, "agents.md")}, store, dir)
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

	files := LoadContextFiles([]string{claude, agents}, store, dir)
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

	require.Equal(t, []string{"after.md", "CLAUDE.md"}, contextPaths(LoadContextFiles([]string{claude}, store, dir)))
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
		contextPaths(LoadContextFiles([]string{filepath.Join(dir, "f0.md")}, store, dir)))

	a := writeContextFile(t, dir, "a.md", "@b.md")
	writeContextFile(t, dir, "b.md", "@a.md")
	require.Equal(t, []string{"b.md", "a.md"}, contextPaths(LoadContextFiles([]string{a}, store, dir)))
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
		require.Equal(t, []string{"CLAUDE.md"}, contextPaths(LoadContextFiles([]string{claude}, store, project)), ref)
	}

	global := writeContextFile(t, t.TempDir(), "AGENTS.md", "@"+secret)
	files := LoadContextFiles([]string{global}, store, "")
	require.Equal(t, []string{"secret.md", "AGENTS.md"}, contextPaths(files))
	require.Equal(t, "the key", files[0].Content)
}

// filesUnder returns the paths, relative to dir, of the files that are in
// dir: the directories above a test's own are the machine's, and may hold
// context files of their own.
func filesUnder(files []ContextFile, dir string) []string {
	var out []string
	for _, f := range files {
		if rel, err := filepath.Rel(dir, f.Path); err == nil && !strings.HasPrefix(rel, "..") {
			out = append(out, filepath.ToSlash(rel))
		}
	}
	return out
}

// Started in a subdirectory, gnosh reads the context files of the
// directories above it too, the outermost first and the working
// directory's last, as Claude Code reads CLAUDE.md files. An import in a
// directory above stays inside that directory.
func TestProjectContextFilesReadTheDirectoriesAbove(t *testing.T) {
	t.Parallel()

	base := t.TempDir()
	repo := filepath.Join(base, "repo")
	work := filepath.Join(repo, "sub")
	require.NoError(t, os.MkdirAll(filepath.Join(repo, "docs"), 0o755))
	require.NoError(t, os.MkdirAll(work, 0o755))
	writeContextFile(t, base, "outside.md", "outside")
	writeContextFile(t, base, "CLAUDE.md", "the outer rules")
	writeContextFile(t, repo, "AGENTS.md", "@docs/style.md @../outside.md")
	writeContextFile(t, filepath.Join(repo, "docs"), "style.md", "the style")
	writeContextFile(t, work, "CLAUDE.md", "the sub rules")
	store := config.NewTestStoreIn(&config.Config{Options: &config.Options{}}, work)

	files := ProjectContextFiles([]string{"CLAUDE.md", "AGENTS.md"}, store)
	require.Equal(t, []string{"CLAUDE.md", "repo/docs/style.md", "repo/AGENTS.md", "repo/sub/CLAUDE.md"}, filesUnder(files, base))
}

// A directory above gives only files the user or root owns, so a shared
// one such as /tmp cannot slip instructions into a session. The working
// directory's own files are read whoever owns them, as before.
func TestProjectContextFilesTrustOnlyTheUserOrRootAbove(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("giving a file to another user takes root")
	}
	t.Parallel()

	base := t.TempDir()
	work := filepath.Join(base, "work")
	require.NoError(t, os.Mkdir(work, 0o755))
	planted := writeContextFile(t, base, "AGENTS.md", "planted")
	own := writeContextFile(t, work, "AGENTS.md", "the rules")
	require.NoError(t, os.Chown(planted, 4242, 4242))
	require.NoError(t, os.Chown(own, 4242, 4242))
	store := config.NewTestStoreIn(&config.Config{Options: &config.Options{}}, work)

	require.Equal(t, []string{"work/AGENTS.md"}, filesUnder(ProjectContextFiles([]string{"AGENTS.md"}, store), base))
}

// A file in a subdirectory of the working directory brings the context
// files of the subdirectories down to it, the outermost first, as Claude
// Code loads a subdirectory's CLAUDE.md once it reads a file there; the
// working directory's own are the system prompt's. A subdirectory's
// imports stay inside it, and a file in the working directory, or
// outside it, brings none.
func TestSubdirectoryContextFiles(t *testing.T) {
	t.Parallel()

	work := t.TempDir()
	e2e := filepath.Join(work, "misc", "gnoe2e")
	require.NoError(t, os.MkdirAll(e2e, 0o755))
	writeContextFile(t, work, "AGENTS.md", "the project's")
	writeContextFile(t, filepath.Join(work, "misc"), "AGENTS.md", "misc's")
	writeContextFile(t, e2e, "CLAUDE.md", "@notes.md @../../AGENTS.md")
	writeContextFile(t, e2e, "notes.md", "the e2e notes")
	store := config.NewTestStoreIn(&config.Config{Options: &config.Options{}}, work)
	paths := []string{"CLAUDE.md", "AGENTS.md"}

	files := SubdirectoryContextFiles(paths, store, filepath.Join(e2e, "main_test.go"))
	require.Equal(t, []string{"misc/AGENTS.md", "misc/gnoe2e/notes.md", "misc/gnoe2e/CLAUDE.md"}, filesUnder(files, work))
	require.Empty(t, SubdirectoryContextFiles(paths, store, filepath.Join(work, "main.go")))
	require.Empty(t, SubdirectoryContextFiles(paths, store, filepath.Join(t.TempDir(), "main.go")))
}
