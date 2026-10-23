package agent

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"charm.land/fantasy"
	"github.com/charmbracelet/crush/internal/agent/tools"
	"github.com/charmbracelet/crush/internal/config"
	"github.com/charmbracelet/crush/internal/permission"
	"github.com/stretchr/testify/require"
)

// The fetch agent reads untrusted pages, fetches any URL without asking,
// and works in a session whose permission requests are all approved. Its
// view, grep and glob must read nothing outside the directory holding the
// pages it fetched, or a page could have it read a key and send it off in
// a URL. A relative path is that directory's too, not the process's (the
// project, where a .env would be).
func TestFetchAgentReadsOnlyItsPages(t *testing.T) {
	pages := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(pages, "page-1.md"), []byte("the fetched page\n"), 0o644))
	home := t.TempDir()
	key := filepath.Join(home, "id_ed25519")
	require.NoError(t, os.WriteFile(key, []byte("PRIVATE KEY TOPSECRET\n"), 0o600))
	t.Chdir(home)
	toKey, err := filepath.Rel(pages, key)
	require.NoError(t, err)
	toHome, err := filepath.Rel(pages, home)
	require.NoError(t, err)

	// As agentic_fetch sets up its sub-agent's session.
	perms := permission.NewPermissionService(home, false, nil)
	perms.AutoApproveSession("fetch-session")
	ctx := context.WithValue(t.Context(), tools.SessionIDContextKey, "fetch-session")
	files := &readRecorder{}
	byName := map[string]fantasy.AgentTool{}
	for _, tool := range fetchAgentTools(pages, nil, nil, perms, files, config.Tools{}) {
		byName[tool.Info().Name] = tool
	}
	run := func(t *testing.T, name string, input any) fantasy.ToolResponse {
		t.Helper()
		b, err := json.Marshal(input)
		require.NoError(t, err)
		resp, err := byName[name].Run(ctx, fantasy.ToolCall{ID: name + "-call", Name: name, Input: string(b)})
		require.NoError(t, err)
		return resp
	}

	for _, tc := range []struct {
		name  string
		tool  string
		input any
		leak  string // what the answer would show had it read outside
	}{
		{"view the key by its path", tools.ViewToolName, tools.ViewParams{FilePath: key}, "TOPSECRET"},
		{"view the key by a relative path", tools.ViewToolName, tools.ViewParams{FilePath: toKey}, "TOPSECRET"},
		{"grep the key's directory", tools.GrepToolName, tools.GrepParams{Pattern: "TOPSECRET", Path: home}, "TOPSECRET"},
		{"grep a relative path out", tools.GrepToolName, tools.GrepParams{Pattern: "TOPSECRET", Path: toHome}, "TOPSECRET"},
		{"grep the process's directory", tools.GrepToolName, tools.GrepParams{Pattern: "TOPSECRET", Path: "."}, "TOPSECRET"},
		{"glob the key's directory", tools.GlobToolName, tools.GlobParams{Pattern: "*", Path: home}, "id_ed25519"},
		{"glob a pattern climbing out", tools.GlobToolName, tools.GlobParams{Pattern: toHome + "/*"}, "id_ed25519"},
		{"glob the process's directory", tools.GlobToolName, tools.GlobParams{Pattern: "*", Path: "."}, "id_ed25519"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			resp := run(t, tc.tool, tc.input)
			require.NotContains(t, resp.Content, tc.leak)
		})
	}
	require.NotContains(t, files.reads, key)

	t.Run("its own pages", func(t *testing.T) {
		require.Contains(t, run(t, tools.ViewToolName, tools.ViewParams{FilePath: "page-1.md"}).Content, "the fetched page")
		require.Contains(t, run(t, tools.GrepToolName, tools.GrepParams{Pattern: "fetched"}).Content, "page-1.md")
		require.Contains(t, run(t, tools.GlobToolName, tools.GlobParams{Pattern: "*.md"}).Content, "page-1.md")
	})
}
