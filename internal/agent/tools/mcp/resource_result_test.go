package mcp

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/stretchr/testify/require"
)

func TestToolResultGivesAnEmbeddedResourcesText(t *testing.T) {
	t.Parallel()

	got := toolResultFromCall(&mcp.CallToolResult{Content: []mcp.Content{
		&mcp.TextContent{Text: "before"},
		&mcp.EmbeddedResource{Resource: &mcp.ResourceContents{URI: "file:///notes.md", MIMEType: "text/markdown", Text: "# Notes"}},
	}}, "docs", t.TempDir())

	require.Equal(t, ToolResult{Type: "text", Content: "before\n[Resource from docs at file:///notes.md] # Notes"}, got)
}

func TestToolResultSavesAnEmbeddedBinaryResource(t *testing.T) {
	t.Parallel()
	dataDir := t.TempDir()
	pdf := []byte("%PDF-1.7 not really")

	got := toolResultFromCall(&mcp.CallToolResult{Content: []mcp.Content{
		&mcp.EmbeddedResource{Resource: &mcp.ResourceContents{URI: "file:///r.pdf", MIMEType: "application/pdf", Blob: pdf}},
	}}, "docs", dataDir)

	require.Equal(t, "text", got.Type)
	prefix := "[Resource from docs at file:///r.pdf] Binary content (application/pdf, 19 bytes) saved to "
	require.True(t, strings.HasPrefix(got.Content, prefix), got.Content)
	path := strings.TrimPrefix(got.Content, prefix)
	require.Equal(t, filepath.Join(dataDir, "shell-output"), filepath.Dir(path))
	require.Equal(t, ".pdf", filepath.Ext(path))
	saved, err := os.ReadFile(path)
	require.NoError(t, err)
	require.Equal(t, pdf, saved)
}

func TestToolResultShowsAnEmbeddedImage(t *testing.T) {
	t.Parallel()
	png := []byte{0x89, 'P', 'N', 'G', 1, 2, 3}

	got := toolResultFromCall(&mcp.CallToolResult{Content: []mcp.Content{
		&mcp.EmbeddedResource{Resource: &mcp.ResourceContents{URI: "file:///shot.png", MIMEType: "IMAGE/PNG; q=1", Blob: png}},
	}}, "pics", t.TempDir())

	require.Equal(t, ToolResult{Type: "image", Content: "[Resource from pics at file:///shot.png]", Data: png, MediaType: "image/png"}, got)
}

func TestToolResultSavesAnEmbeddedImageOfAnotherType(t *testing.T) {
	t.Parallel()
	dataDir := t.TempDir()

	got := toolResultFromCall(&mcp.CallToolResult{Content: []mcp.Content{
		&mcp.EmbeddedResource{Resource: &mcp.ResourceContents{URI: "file:///logo.svg", MIMEType: "image/svg+xml", Blob: []byte("<svg/>")}},
	}}, "pics", dataDir)

	require.Equal(t, "text", got.Type)
	require.True(t, strings.HasPrefix(got.Content, "[Resource from pics at file:///logo.svg] Binary content (image/svg+xml, 6 bytes) saved to "), got.Content)
	require.Equal(t, ".svg", filepath.Ext(got.Content))
}

func TestToolResultSaysWhyAnEmbeddedBinaryWasNotSaved(t *testing.T) {
	t.Parallel()
	dataDir := filepath.Join(t.TempDir(), "a-file")
	require.NoError(t, os.WriteFile(dataDir, []byte("x"), 0o600))

	got := toolResultFromCall(&mcp.CallToolResult{Content: []mcp.Content{
		&mcp.EmbeddedResource{Resource: &mcp.ResourceContents{URI: "file:///r.bin", Blob: []byte{0, 1, 2}}},
	}}, "docs", dataDir)

	require.True(t, strings.HasPrefix(got.Content, "[Resource from docs at file:///r.bin] Binary content (unknown type, 3 bytes) could not be saved to disk: "), got.Content)
	require.Contains(t, got.Content, "not a directory")
}

func TestToolResultSkipsAnEmptyEmbeddedResource(t *testing.T) {
	t.Parallel()

	got := toolResultFromCall(&mcp.CallToolResult{Content: []mcp.Content{
		&mcp.TextContent{Text: "only this"},
		&mcp.EmbeddedResource{},
		&mcp.EmbeddedResource{Resource: &mcp.ResourceContents{URI: "file:///empty"}},
	}}, "files", "")

	require.Equal(t, ToolResult{Type: "text", Content: "only this"}, got)
}

func TestToolResultGivesAResourceLink(t *testing.T) {
	t.Parallel()

	got := toolResultFromCall(&mcp.CallToolResult{Content: []mcp.Content{
		&mcp.ResourceLink{URI: "file:///a.txt", Name: "a.txt", Description: "the first file"},
		&mcp.ResourceLink{URI: "file:///b.txt", Name: "b.txt"},
	}}, "files", "")

	require.Equal(t, ToolResult{Type: "text", Content: "[Resource link: a.txt] file:///a.txt (the first file)\n[Resource link: b.txt] file:///b.txt"}, got)
}
