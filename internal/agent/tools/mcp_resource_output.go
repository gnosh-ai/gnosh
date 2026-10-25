package tools

import (
	"cmp"
	"fmt"
	"log/slog"
	"strconv"
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

// resourceBlobLimit is the largest binary resource that is saved, as in
// Claude Code.
const resourceBlobLimit = 100 << 20

// blobExtensions are the extensions Claude Code gives a saved binary
// resource by its MIME type; any other type is saved as .bin.
var blobExtensions = map[string]string{
	"application/pdf":  "pdf",
	"application/json": "json",
	"text/csv":         "csv",
	"text/plain":       "txt",
	"text/html":        "html",
	"text/markdown":    "md",
	"application/zip":  "zip",
	"application/vnd.openxmlformats-officedocument.wordprocessingml.document":   "docx",
	"application/vnd.openxmlformats-officedocument.spreadsheetml.sheet":         "xlsx",
	"application/vnd.openxmlformats-officedocument.presentationml.presentation": "pptx",
	"application/msword":        "doc",
	"application/vnd.ms-excel":  "xls",
	"audio/mpeg":                "mp3",
	"audio/wav":                 "wav",
	"audio/ogg":                 "ogg",
	"video/mp4":                 "mp4",
	"video/webm":                "webm",
	"image/png":                 "png",
	"image/jpeg":                "jpg",
	"image/gif":                 "gif",
	"image/webp":                "webp",
	"image/svg+xml":             "svg",
	"image/x-icon":              "ico",
	"image/vnd.microsoft.icon":  "ico",
	"text/css":                  "css",
	"text/javascript":           "js",
	"application/javascript":    "js",
	"application/xml":           "xml",
	"text/xml":                  "xml",
	"application/manifest+json": "webmanifest",
	"font/woff2":                "woff2",
	"application/font-woff2":    "woff2",
	"font/woff":                 "woff",
	"application/font-woff":     "woff",
	"font/ttf":                  "ttf",
	"font/otf":                  "otf",
	"application/wasm":          "wasm",
}

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
	if len(content.Blob) > resourceBlobLimit {
		return fmt.Sprintf("Binary content could not be saved to disk: content is %d bytes, over the %d byte persist limit", len(content.Blob), resourceBlobLimit)
	}
	path, err := shell.SpillBytes(content.Blob, dataDir, blobExtension(content.MIMEType))
	if err != nil {
		return "Binary content could not be saved to disk: " + err.Error()
	}
	return fmt.Sprintf("[Resource from %s at %s] Binary content (%s, %s) saved to %s",
		server, uri, cmp.Or(content.MIMEType, "unknown type"), claudeSize(len(content.Blob)), path)
}

// blobExtension is the extension a binary resource of mimeType is saved
// under, so the view tool knows an image for one.
func blobExtension(mimeType string) string {
	base, _, _ := strings.Cut(mimeType, ";")
	return cmp.Or(blobExtensions[strings.ToLower(strings.TrimSpace(base))], "bin")
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
			claudeSize(len(text)), claudeSize(len(kept)), kept)
	}
	return fmt.Sprintf("<persisted-output>\nOutput too large (%s). Full output saved to: %s\n\nPreview (first %s):\n%s\n...\n</persisted-output>",
		claudeSize(len(text)), path, claudeSize(resourcePreviewChars), previewOf(text, resourcePreviewChars))
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

// claudeSize is a size in bytes as Claude Code writes one: "512 bytes",
// "1.5KB", "2MB".
func claudeSize(n int) string {
	unit := func(v float64, suffix string) string {
		return strings.TrimSuffix(strconv.FormatFloat(v, 'f', 1, 64), ".0") + suffix
	}
	kb := float64(n) / 1024
	switch {
	case kb < 1:
		return strconv.Itoa(n) + " bytes"
	case kb < 1024:
		return unit(kb, "KB")
	case kb/1024 < 1024:
		return unit(kb/1024, "MB")
	default:
		return unit(kb/1024/1024, "GB")
	}
}
