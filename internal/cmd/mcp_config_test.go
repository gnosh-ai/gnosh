package cmd

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/charmbracelet/crush/internal/config"
	"github.com/spf13/cobra"
	"github.com/stretchr/testify/require"
)

// --mcp-config loads MCP servers from files in Claude Code's .mcp.json
// shape, or from that JSON itself, as Claude Code's does: a stdio server
// by its command, an http or sse one by its URL, the type inferred when it
// is left out. Anything else is an error naming the value.
func TestMCPConfigFlags(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	file := filepath.Join(dir, "mcp.json")
	require.NoError(t, os.WriteFile(file, []byte(`{"mcpServers": {
		"files": {"command": "npx", "args": ["-y", "server-files"], "env": {"ROOT": "/tmp"}},
		"docs": {"type": "http", "url": "https://mcp.example/docs", "headers": {"Authorization": "Bearer $TOKEN"}},
		"events": {"type": "sse", "url": "https://mcp.example/events"},
		"plain": {"url": "https://mcp.example/plain"}
	}}`), 0o644))

	parse := func(args ...string) (config.MCPs, bool, error) {
		cmd := &cobra.Command{Use: "test"}
		addMCPConfigFlags(cmd)
		require.NoError(t, cmd.ParseFlags(args))
		return mcpConfigFlags(cmd)
	}

	servers, strict, err := parse("--mcp-config", file)
	require.NoError(t, err)
	require.False(t, strict)
	require.Equal(t, config.MCPs{
		"files":  {Type: config.MCPStdio, Command: "npx", Args: []string{"-y", "server-files"}, Env: map[string]string{"ROOT": "/tmp"}},
		"docs":   {Type: config.MCPHttp, URL: "https://mcp.example/docs", Headers: map[string]string{"Authorization": "Bearer $TOKEN"}},
		"events": {Type: config.MCPSSE, URL: "https://mcp.example/events"},
		"plain":  {Type: config.MCPHttp, URL: "https://mcp.example/plain"},
	}, servers)

	// JSON itself, after a file: the later definition of a name wins, as
	// does the repeated flag's.
	servers, strict, err = parse("--mcp-config", file, "--mcp-config", `{"mcpServers": {"files": {"command": "files-server"}}}`, "--strict-mcp-config")
	require.NoError(t, err)
	require.True(t, strict)
	require.Equal(t, config.MCPConfig{Type: config.MCPStdio, Command: "files-server"}, servers["files"])
	require.Len(t, servers, 4)

	for value, want := range map[string]string{
		filepath.Join(dir, "missing.json"):                       "no such file",
		`{"mcpServers": `:                                        "not JSON",
		`{"servers": {"a": {"command": "x"}}}`:                   `no "mcpServers"`,
		`{"mcpServers": {"a": {"type": "stdio"}}}`:               "needs a command",
		`{"mcpServers": {"a": {"type": "http"}}}`:                "needs a url",
		`{"mcpServers": {"a": {"type": "ws", "url": "ws://x"}}}`: `unknown type "ws"`,
		`{"mcpServers": {"a": {"args": ["x"]}}}`:                 "neither a command nor a url",
	} {
		_, _, err := parse("--mcp-config", value)
		require.ErrorContains(t, err, want, value)
		require.ErrorContains(t, err, "--mcp-config", value)
	}

	_, _, err = parse("--strict-mcp-config")
	require.ErrorContains(t, err, "needs --mcp-config")

	servers, strict, err = parse()
	require.NoError(t, err)
	require.Empty(t, servers)
	require.False(t, strict)

	// A command that does not take the flags loads nothing.
	servers, strict, err = mcpConfigFlags(&cobra.Command{Use: "other"})
	require.NoError(t, err)
	require.Empty(t, servers)
	require.False(t, strict)
}
