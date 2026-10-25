package mcp

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/stretchr/testify/require"
)

// pngBytes and jpegBytes begin as those formats do, which is all a type
// sniff reads.
var (
	pngBytes  = []byte{0x89, 'P', 'N', 'G', 0x0d, 0x0a, 0x1a, 0x0a, 1, 2, 3}
	jpegBytes = []byte{0xff, 0xd8, 0xff, 0xe0, 0, 0x10, 'J', 'F', 'I', 'F', 0}
)

func TestToolResultSavesTheImageItShows(t *testing.T) {
	t.Parallel()
	dataDir := t.TempDir()

	got := toolResultFromCall(&mcp.CallToolResult{Content: []mcp.Content{
		&mcp.TextContent{Text: "a screenshot"},
		&mcp.ImageContent{Data: pngBytes, MIMEType: "image/png"},
	}}, "pics", dataDir)

	require.Equal(t, "image", got.Type)
	require.Equal(t, pngBytes, got.Data)
	require.Equal(t, filepath.Join(dataDir, "shell-output"), filepath.Dir(got.ImagePath))
	require.Equal(t, ".png", filepath.Ext(got.ImagePath))
	saved, err := os.ReadFile(got.ImagePath)
	require.NoError(t, err)
	require.Equal(t, pngBytes, saved)
	require.Equal(t, "a screenshot\n[Image: source: "+got.ImagePath+"]", got.Content)
}

func TestToolResultNamesTheSavedImageAlone(t *testing.T) {
	t.Parallel()

	got := toolResultFromCall(&mcp.CallToolResult{Content: []mcp.Content{
		&mcp.ImageContent{Data: pngBytes, MIMEType: "image/png"},
	}}, "pics", t.TempDir())

	require.Equal(t, "[Image: source: "+got.ImagePath+"]", got.Content)
}

func TestToolResultSavesAnImageOfAnotherType(t *testing.T) {
	t.Parallel()
	dataDir := t.TempDir()

	got := toolResultFromCall(&mcp.CallToolResult{Content: []mcp.Content{
		&mcp.ImageContent{Data: []byte("<svg/>"), MIMEType: "image/svg+xml"},
	}}, "pics", dataDir)

	require.Equal(t, "text", got.Type)
	prefix := "[Image from pics] Binary content (image/svg+xml, 6 bytes) saved to "
	require.True(t, strings.HasPrefix(got.Content, prefix), got.Content)
	path := strings.TrimPrefix(got.Content, prefix)
	require.Equal(t, ".svg", filepath.Ext(path))
	saved, err := os.ReadFile(path)
	require.NoError(t, err)
	require.Equal(t, "<svg/>", string(saved))
}

func TestToolResultShowsAMislabeledImage(t *testing.T) {
	t.Parallel()

	got := toolResultFromCall(&mcp.CallToolResult{Content: []mcp.Content{
		&mcp.ImageContent{Data: jpegBytes, MIMEType: "image/jpg"},
	}}, "pics", t.TempDir())

	require.Equal(t, "image", got.Type)
	require.Equal(t, "image/jpeg", got.MediaType)
	require.Equal(t, ".jpg", filepath.Ext(got.ImagePath))
}

func TestToolResultSavesTheImagesItDoesNotShow(t *testing.T) {
	t.Parallel()
	dataDir := t.TempDir()

	got := toolResultFromCall(&mcp.CallToolResult{Content: []mcp.Content{
		&mcp.ImageContent{Data: pngBytes, MIMEType: "image/png"},
		&mcp.ImageContent{Data: jpegBytes, MIMEType: "image/jpeg"},
		&mcp.EmbeddedResource{Resource: &mcp.ResourceContents{URI: "file:///b.png", MIMEType: "image/png", Blob: pngBytes}},
	}}, "pics", dataDir)

	require.Equal(t, pngBytes, got.Data, "the first is shown")
	lines := strings.Split(got.Content, "\n")
	require.Len(t, lines, 3, got.Content)
	require.True(t, strings.HasPrefix(lines[0], "[Image from pics] Binary content (image/jpeg, 11 bytes) saved to "), lines[0])
	require.True(t, strings.HasPrefix(lines[1], "[Resource from pics at file:///b.png] Binary content (image/png, 11 bytes) saved to "), lines[1])
	require.Equal(t, "[Image: source: "+got.ImagePath+"]", lines[2])
	saved, err := os.ReadFile(strings.TrimPrefix(lines[0], "[Image from pics] Binary content (image/jpeg, 11 bytes) saved to "))
	require.NoError(t, err)
	require.Equal(t, jpegBytes, saved)
}

func TestToolResultShowsAnImageItCannotSave(t *testing.T) {
	t.Parallel()
	dataDir := filepath.Join(t.TempDir(), "a-file")
	require.NoError(t, os.WriteFile(dataDir, []byte("x"), 0o600))

	got := toolResultFromCall(&mcp.CallToolResult{Content: []mcp.Content{
		&mcp.TextContent{Text: "a screenshot"},
		&mcp.ImageContent{Data: pngBytes, MIMEType: "image/png"},
	}}, "pics", dataDir)

	require.Equal(t, ToolResult{Type: "image", Content: "a screenshot", Data: pngBytes, MediaType: "image/png"}, got)
}
