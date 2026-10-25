package mcp

import (
	"fmt"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/stretchr/testify/require"
)

// truncatedNote opens Claude Code's word that an MCP tool's text was cut.
func truncatedNote(limit int) string {
	return fmt.Sprintf("\n\n[OUTPUT TRUNCATED - exceeded %d token limit]\n\nThe tool output was truncated.", limit)
}

// An MCP tool's text that would bring more than 25,000 tokens into the
// conversation, at four characters a token, is cut there, between
// characters, with Claude Code's word that it was. MAX_MCP_OUTPUT_TOKENS
// sets another limit; a value that is no positive number leaves the
// default.
func TestCapOutput(t *testing.T) {
	t.Setenv("MAX_MCP_OUTPUT_TOKENS", "")
	exact := strings.Repeat("a", 100_000)
	require.Equal(t, exact, capOutput(exact))
	over := exact + "b"
	got := capOutput(over)
	require.True(t, strings.HasPrefix(got, exact+truncatedNote(25000)), got[len(got)-300:])
	require.Contains(t, got, "If this MCP server provides pagination or filtering tools, use them")

	hebrew := strings.Repeat("\u05e9", 100_000)
	require.Equal(t, hebrew, capOutput(hebrew), "200,000 bytes, but 100,000 characters")
	got = capOutput(hebrew + "\u05dc")
	require.True(t, utf8.ValidString(got))
	require.True(t, strings.HasPrefix(got, hebrew+truncatedNote(25000)))

	t.Setenv("MAX_MCP_OUTPUT_TOKENS", "10")
	require.True(t, strings.HasPrefix(capOutput(strings.Repeat("a", 41)), strings.Repeat("a", 40)+truncatedNote(10)))
	for _, notANumber := range []string{"0", "-5", "lots"} {
		t.Setenv("MAX_MCP_OUTPUT_TOKENS", notANumber)
		require.Equal(t, exact, capOutput(exact), notANumber)
		require.True(t, strings.HasPrefix(capOutput(over), exact+truncatedNote(25000)), notANumber)
	}
}

// The text of an MCP tool's reply is cut before it becomes the tool's
// result.
func TestToolResultFromCallCapsItsText(t *testing.T) {
	t.Setenv("MAX_MCP_OUTPUT_TOKENS", "5")
	got := toolResultFromCall(&mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: strings.Repeat("x", 30)}}})
	require.True(t, strings.HasPrefix(got.Content, strings.Repeat("x", 20)+truncatedNote(5)), got.Content)
}
