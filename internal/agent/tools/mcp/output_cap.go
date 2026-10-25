package mcp

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"unicode/utf8"
)

// defaultOutputTokens is how many tokens an MCP tool's text may bring
// into the conversation when MAX_MCP_OUTPUT_TOKENS does not say, as in
// Claude Code.
const defaultOutputTokens = 25000

// outputTokens is how many tokens an MCP tool's text may bring into the
// conversation: MAX_MCP_OUTPUT_TOKENS, as in Claude Code, which a
// klash.json's env can set too, or defaultOutputTokens.
func outputTokens() int {
	if n, err := strconv.Atoi(strings.TrimSpace(os.Getenv("MAX_MCP_OUTPUT_TOKENS"))); err == nil && n > 0 {
		return n
	}
	return defaultOutputTokens
}

// capOutput is an MCP tool's text cut to what it may bring into the
// conversation, at four characters a token, as Claude Code counts it
// when it cannot ask the model, with Claude Code's word that it was cut.
// A server's reply of megabytes otherwise filled the model's context in
// one call.
func capOutput(text string) string {
	limit := outputTokens()
	if len(text) <= limit*4 || utf8.RuneCountInString(text) <= limit*4 {
		return text
	}
	cut := len(text)
	n := 0
	for i := range text {
		if n == limit*4 {
			cut = i
			break
		}
		n++
	}
	return text[:cut] + fmt.Sprintf("\n\n[OUTPUT TRUNCATED - exceeded %d token limit]\n\nThe tool output was truncated. If this MCP server provides pagination or filtering tools, use them to retrieve specific portions of the data. If pagination is not available, inform the user that you are working with truncated output and results may be incomplete.", limit)
}
