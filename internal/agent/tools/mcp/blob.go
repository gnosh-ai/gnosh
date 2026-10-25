package mcp

import (
	"cmp"
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"github.com/charmbracelet/crush/internal/shell"
)

// blobLimit is the largest binary content saved to a file, as in Claude
// Code.
const blobLimit = 100 << 20

// blobExtensions are the extensions Claude Code gives saved binary content
// by its MIME type; any other type is saved as .bin.
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

// SaveBlob saves binary content an MCP server sent to a file under dataDir,
// named by its MIME type's extension, and says where in Claude Code's
// words, after prefix. It refuses content over blobLimit.
func SaveBlob(blob []byte, mimeType, prefix, dataDir string) (string, error) {
	path, err := saveBlob(blob, mimeType, dataDir)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("%sBinary content (%s, %s) saved to %s", prefix, cmp.Or(mimeType, "unknown type"), ClaudeSize(len(blob)), path), nil
}

// saveBlob is SaveBlob's saving: it returns the file's path.
func saveBlob(blob []byte, mimeType, dataDir string) (string, error) {
	if len(blob) > blobLimit {
		return "", fmt.Errorf("content is %d bytes, over the %d byte persist limit", len(blob), blobLimit)
	}
	return shell.SpillBytes(blob, dataDir, blobExtension(mimeType))
}

// savedBinary saves binary content under dataDir and says where, after
// heading, or why it could not, in Claude Code's words.
func savedBinary(blob []byte, mimeType, heading, dataDir string) string {
	saved, err := SaveBlob(blob, mimeType, heading+" ", dataDir)
	if err != nil {
		return fmt.Sprintf("%s Binary content (%s, %d bytes) could not be saved to disk: %s", heading, cmp.Or(mimeType, "unknown type"), len(blob), err)
	}
	return saved
}

// blobExtension is the extension binary content of mimeType is saved
// under, so the view tool knows an image for one.
func blobExtension(mimeType string) string {
	return cmp.Or(blobExtensions[mimeBase(mimeType)], "bin")
}

// modelImageTypes are the image types a model is shown, as in Claude Code;
// an image of another type is saved to a file.
var modelImageTypes = map[string]bool{"image/jpeg": true, "image/png": true, "image/gif": true, "image/webp": true}

// shownImageType is the type an image is shown to a model as: its own when
// a model takes that type, or what its bytes are when those are one, as
// for an image sent as image/jpg; "" when it is neither.
func shownImageType(data []byte, mimeType string) string {
	if t := mimeBase(mimeType); modelImageTypes[t] {
		return t
	}
	if t := mimeBase(http.DetectContentType(data)); modelImageTypes[t] {
		return t
	}
	return ""
}

// mimeBase is mimeType without its parameters, in lower case.
func mimeBase(mimeType string) string {
	base, _, _ := strings.Cut(mimeType, ";")
	return strings.ToLower(strings.TrimSpace(base))
}

// ClaudeSize is a size in bytes as Claude Code writes one: "512 bytes",
// "1.5KB", "2MB".
func ClaudeSize(n int) string {
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
