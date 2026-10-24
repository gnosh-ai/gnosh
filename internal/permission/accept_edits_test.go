package permission

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

// runsUnasked reports whether s lets tool's edit of path run without
// asking. Nobody answers here, so a request that is asked ends with the
// cancelled wait; one that is let run returns at once.
func runsUnasked(t *testing.T, ctx context.Context, s Service, tool, path string) bool {
	t.Helper()
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	granted, err := s.Request(cancelled, CreatePermissionRequest{
		SessionID: "s1", ToolCallID: "call-1", ToolName: tool, Action: "write",
		Path: filepath.Dir(path), FilePath: path,
	})
	if granted {
		require.NoError(t, err)
		return true
	}
	require.ErrorIs(t, err, context.Canceled, "%s %s was neither let run nor asked about", tool, path)
	return false
}

// As in Claude Code, acceptEdits lets the file edit tools change files in
// the project and in directories added with --add-dir without asking, and
// nothing else: a file elsewhere, or one that runs code or holds settings
// (a git hook, a shell's startup file, Klash's or Claude Code's config),
// is asked about as without the mode, as is anything a symlink leads out
// to. It used to let them write anywhere.
func TestAcceptEditsLetsEditsInTheProjectRun(t *testing.T) {
	t.Parallel()

	project, added, outside := t.TempDir(), t.TempDir(), t.TempDir()
	for _, f := range []string{filepath.Join(project, "a.go"), filepath.Join(outside, "target.go"), filepath.Join(project, ".git", "config")} {
		require.NoError(t, os.MkdirAll(filepath.Dir(f), 0o755))
		require.NoError(t, os.WriteFile(f, []byte("x\n"), 0o644))
	}
	require.NoError(t, os.Symlink(filepath.Join(outside, "target.go"), filepath.Join(project, "out.go")))
	require.NoError(t, os.Symlink(outside, filepath.Join(project, "outdir")))
	require.NoError(t, os.Symlink(filepath.Join(outside, "nothing.go"), filepath.Join(project, "dangling.go")))
	require.NoError(t, os.Symlink(filepath.Join(project, ".git"), filepath.Join(project, "gitdir")))
	require.NoError(t, os.Mkdir(filepath.Join(project, "editor"), 0o755))
	require.NoError(t, os.Symlink(filepath.Join(project, "editor"), filepath.Join(project, ".vscode")))
	s := New(Options{WorkingDir: project, AddedDirs: []string{added}, AcceptEdits: true})

	for path, want := range map[string]bool{
		filepath.Join(project, "a.go"):                        true,
		filepath.Join(project, "new", "dir", "b.go"):          true,
		filepath.Join(added, "c.go"):                          true,
		filepath.Join(outside, "d.go"):                        false,
		filepath.Join(project, ".git", "hooks", "pre-commit"): false,
		filepath.Join(project, "sub", ".git", "config"):       false,
		filepath.Join(project, ".bashrc"):                     false,
		filepath.Join(project, ".zshrc"):                      false,
		filepath.Join(project, "klash.json"):                  false,
		filepath.Join(project, ".klash", "crush.db"):          false,
		filepath.Join(project, ".claude", "settings.json"):    false,
		filepath.Join(project, ".config", "git", "config"):    false,
		filepath.Join(project, "out.go"):                      false,
		filepath.Join(project, "outdir", "e.go"):              false,
		filepath.Join(project, "dangling.go"):                 false,
		filepath.Join(project, "gitdir", "config"):            false,
		filepath.Join(project, ".vscode", "settings.json"):    false,
		"a.go": false,
		"":     false,
	} {
		require.Equalf(t, want, runsUnasked(t, t.Context(), s, "edit", path), "edit %q", path)
	}
	for _, tool := range []string{"write", "multiedit"} {
		require.True(t, runsUnasked(t, t.Context(), s, tool, filepath.Join(project, "a.go")), tool)
	}
	for _, tool := range []string{"bash", "lsp_rename", "lsp_replace_symbol", "download"} {
		require.False(t, runsUnasked(t, t.Context(), s, tool, filepath.Join(project, "a.go")), "%s is no file edit acceptEdits lets run", tool)
	}
	require.False(t, runsUnasked(t, WithHookAsk(t.Context(), "call-1"), s, "edit", filepath.Join(project, "a.go")),
		"a hook's ask still goes to the user")
}

// Without acceptEdits, an edit in the project is asked about.
func TestWithoutAcceptEditsEditsAsk(t *testing.T) {
	t.Parallel()

	project := t.TempDir()
	s := New(Options{WorkingDir: project})
	require.False(t, runsUnasked(t, t.Context(), s, "edit", filepath.Join(project, "a.go")))
}
