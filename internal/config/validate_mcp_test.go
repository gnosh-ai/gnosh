package config

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// An MCP server with no type means stdio: that is what the schema
// documents and what the crushrc `mcp add` builtin writes, and it is the
// shape most MCP snippets are copied in with. Defaulting it keeps those
// configs working; the alternative (refusing them) would have made the
// loader disagree with Crush's own config writer.
func TestSetMCPDefaultsFillsStdio(t *testing.T) {
	t.Parallel()

	cfg := &Config{MCP: MCPs{
		"github": {Command: "npx", Args: []string{"-y", "@modelcontextprotocol/server-github"}},
		"remote": {Type: MCPHttp, URL: "https://example.com/mcp"},
		"parked": {Disabled: true},
	}}
	cfg.setMCPDefaults()

	require.Equal(t, MCPStdio, cfg.MCP["github"].Type, "an omitted type means stdio")
	require.Equal(t, MCPHttp, cfg.MCP["remote"].Type, "an explicit type must not be overwritten")
	require.Equal(t, MCPStdio, cfg.MCP["parked"].Type)
	require.NoError(t, cfg.ValidateMCPs())
}

// The default is applied before validation on the real load path, so a
// config omitting type must load rather than be refused.
func TestValidateMCPsAcceptsOmittedTypeAfterDefaults(t *testing.T) {
	t.Parallel()

	cfg := &Config{MCP: MCPs{"github": {Command: "npx"}}}
	cfg.setDefaults(t.TempDir(), t.TempDir())
	require.NoError(t, cfg.Validate())
	require.Equal(t, MCPStdio, cfg.MCP["github"].Type)
}

func TestValidateMCPsRejectsUnknownType(t *testing.T) {
	t.Parallel()

	for _, bad := range []MCPType{"websocket", "Stdio", "STDIO", "grpc", "tcp"} {
		t.Run(string(bad), func(t *testing.T) {
			t.Parallel()
			cfg := &Config{MCP: MCPs{"srv": {Type: bad, URL: "https://example.com"}}}
			err := cfg.ValidateMCPs()
			require.Error(t, err)
			require.Contains(t, err.Error(), "unknown type")
			require.Contains(t, err.Error(), string(bad))
			require.Contains(t, err.Error(), "stdio, sse, http")
		})
	}
}

func TestValidateMCPsAcceptsEveryKnownType(t *testing.T) {
	t.Parallel()

	cfg := &Config{MCP: MCPs{}}
	for i, known := range KnownMCPTypes() {
		cfg.MCP[string(rune('a'+i))] = MCPConfig{Type: known, Command: "x", URL: "https://example.com"}
	}
	require.NoError(t, cfg.ValidateMCPs())
	require.Len(t, cfg.MCP, len(KnownMCPTypes()))
}

// A server that is switched off is never asked to connect, so its
// transport type must not hold up startup.
func TestValidateMCPsSkipsDisabledServers(t *testing.T) {
	t.Parallel()

	cfg := &Config{MCP: MCPs{
		"parked":  {Disabled: true},
		"broken":  {Disabled: true, Type: "websocket"},
		"working": {Type: MCPStdio, Command: "npx"},
	}}
	require.NoError(t, cfg.ValidateMCPs())
}

// Re-enabling a parked server is when the mistake becomes real, and is
// when the user finds out.
func TestValidateMCPsCatchesAReEnabledServer(t *testing.T) {
	t.Parallel()

	cfg := &Config{MCP: MCPs{"parked": {Disabled: true, Type: "websocket"}}}
	require.NoError(t, cfg.ValidateMCPs())

	server := cfg.MCP["parked"]
	server.Disabled = false
	cfg.MCP["parked"] = server
	require.ErrorContains(t, cfg.ValidateMCPs(), "unknown type")
}

func TestValidateMCPsEmpty(t *testing.T) {
	t.Parallel()

	require.NoError(t, (&Config{}).ValidateMCPs())
	require.NoError(t, (&Config{MCP: MCPs{}}).ValidateMCPs())
}

// Two bad servers must produce the same message every run.
func TestValidateMCPsErrorIsDeterministic(t *testing.T) {
	t.Parallel()

	cfg := &Config{MCP: MCPs{
		"zebra": {Type: "websocket"},
		"alpha": {Type: "websocket"},
	}}
	for range 10 {
		require.ErrorContains(t, cfg.ValidateMCPs(), `mcp "alpha"`,
			"map iteration order must not leak into the error message")
	}
}

func TestIsKnownMCPType(t *testing.T) {
	t.Parallel()

	require.True(t, IsKnownMCPType(MCPStdio))
	require.True(t, IsKnownMCPType(MCPSSE))
	require.True(t, IsKnownMCPType(MCPHttp))
	require.False(t, IsKnownMCPType(""))
	require.False(t, IsKnownMCPType("websocket"))
}

func TestKnownMCPTypesIsACopy(t *testing.T) {
	t.Parallel()

	got := KnownMCPTypes()
	got[0] = "tampered"
	require.True(t, IsKnownMCPType(MCPStdio))
	require.False(t, IsKnownMCPType("tampered"))
}

// Every check must be reachable through the single Validate seam.
func TestValidateIncludesMCPs(t *testing.T) {
	t.Parallel()

	cfg := &Config{MCP: MCPs{"srv": {Type: "websocket"}}}
	require.ErrorContains(t, cfg.Validate(), "invalid mcp configuration")
}
