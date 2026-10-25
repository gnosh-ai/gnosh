package tools

import (
	"cmp"
	"fmt"
	"log/slog"
	"strings"
	"unicode/utf8"

	"github.com/charmbracelet/crush/internal/agent/tools/mcp"
	"github.com/charmbracelet/crush/internal/shell"
)

// resourcePersistChars is how many characters of an MCP resource's text
// come into the conversation whole. Longer text is saved to a file and the
// model is told where, with its start, as Claude Code saves a resource read
// over its 50,000-character ceiling: a server's resource of megabytes
// otherwise filled the context in one read.
const resourcePersistChars = 50000

// resourcePreviewChars is how much of saved text the model sees, as in
// Claude Code.
const resourcePreviewChars = 2000

// resourceText is what the model is told of an MCP resource's contents:
// their text, with each binary part saved to a file under dataDir, and the
// whole saved there instead when it is long, as Claude Code keeps both out
// of the conversation. A binary part came in as its raw bytes.
func resourceText(contents []*mcp.ResourceContents, server, uri, dataDir string) string {
	var parts []string
	for _, content := range contents {
		if content == nil {
			continue
		}
		if content.Text != "" {
			parts = append(parts, content.Text)
			continue
		}
		if len(content.Blob) > 0 {
			parts = append(parts, savedBlob(content, server, cmp.Or(content.URI, uri), dataDir))
			continue
		}
		slog.Debug("MCP resource content missing text/blob", "uri", content.URI)
	}
	return persistedText(strings.Join(parts, "\n"), dataDir)
}

// savedBlob saves a binary part of a resource under dataDir and says where,
// in Claude Code's words.
func savedBlob(content *mcp.ResourceContents, server, uri, dataDir string) string {
	saved, err := mcp.SaveBlob(content.Blob, content.MIMEType, fmt.Sprintf("[Resource from %s at %s] ", server, uri), dataDir)
	if err != nil {
		return "Binary content could not be saved to disk: " + err.Error()
	}
	return saved
}

// persistedText is text, when it is longer than resourcePersistChars, saved
// under dataDir and replaced by Claude Code's word of where, with its start.
// Text that could not be saved is cut there instead.
func persistedText(text, dataDir string) string {
	if utf8.RuneCountInString(text) <= resourcePersistChars {
		return text
	}
	path, err := shell.SpillOutput(text, dataDir)
	if err != nil {
		slog.Debug("Could not save MCP resource text", "dir", dataDir, "error", err)
		kept := previewOf(text, resourcePersistChars)
		return fmt.Sprintf("<truncated-output>\nOutput too large (%s). It could not be saved, so only the first %s are shown; "+
			"the rest was dropped. If the tool can page or filter its results, call it again for the part you need.\n\n%s\n</truncated-output>",
			mcp.ClaudeSize(len(text)), mcp.ClaudeSize(len(kept)), kept)
	}
	return fmt.Sprintf("<persisted-output>\nOutput too large (%s). Full output saved to: %s\n\nPreview (first %s):\n%s\n...\n</persisted-output>",
		mcp.ClaudeSize(len(text)), path, mcp.ClaudeSize(resourcePreviewChars), previewOf(text, resourcePreviewChars))
}

// previewOf is the start of text, at most n characters, ended at a line's
// end when one falls in its second half, as Claude Code cuts a preview.
func previewOf(text string, n int) string {
	cut, count := len(text), 0
	for i := range text {
		if count == n {
			cut = i
			break
		}
		count++
	}
	head := text[:cut]
	if nl := strings.LastIndexByte(head, '\n'); nl >= 0 && utf8.RuneCountInString(head[:nl]) > n/2 {
		return head[:nl]
	}
	return head
}
