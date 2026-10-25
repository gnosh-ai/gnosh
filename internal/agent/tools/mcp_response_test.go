package tools

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"charm.land/fantasy"
	"github.com/charmbracelet/crush/internal/agent/tools/mcp"
	"github.com/stretchr/testify/require"
)

// onePixelPNG is a real 1x1 PNG, 67 bytes.
var onePixelPNG = []byte{
	0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a, 0x00, 0x00, 0x00, 0x0d, 0x49, 0x48, 0x44, 0x52,
	0x00, 0x00, 0x00, 0x01, 0x00, 0x00, 0x00, 0x01, 0x08, 0x00, 0x00, 0x00, 0x00, 0x3a, 0x7e, 0x9b,
	0x55, 0x00, 0x00, 0x00, 0x0a, 0x49, 0x44, 0x41, 0x54, 0x78, 0x9c, 0x63, 0x60, 0x00, 0x00, 0x00,
	0x02, 0x00, 0x01, 0x48, 0xaf, 0xa4, 0x71, 0x00, 0x00, 0x00, 0x00, 0x49, 0x45, 0x4e, 0x44, 0xae,
	0x42, 0x60, 0x82,
}

func TestMCPResponseTellsAModelThatCannotSeeAnImageWhereItIs(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "shot.png")
	require.NoError(t, os.WriteFile(path, onePixelPNG, 0o600))
	ctx := context.WithValue(context.WithValue(t.Context(), SupportsImagesContextKey, false), ModelNameContextKey, "Qwen")

	got := mcpResponse(ctx, mcp.ToolResult{
		Type: "image", Content: "a screenshot\n[Image: source: " + path + "]",
		Data: onePixelPNG, MediaType: "image/png", ImagePath: path,
	})

	require.False(t, got.IsError)
	require.Equal(t, "text", got.Type)
	require.Equal(t, "a screenshot\n[Image: source: "+path+"]\n\n"+unseenImage(path, 67, "Qwen"), got.Content)
	require.Contains(t, got.Content, "is a PNG image of 1x1 pixels, 67 B")

	alone := mcpResponse(ctx, mcp.ToolResult{Type: "image", Data: onePixelPNG, MediaType: "image/png", ImagePath: path})
	require.Equal(t, unseenImage(path, 67, "Qwen"), alone.Content)
}

func TestMCPResponseSaysAnUnsavedImageCannotBeSeen(t *testing.T) {
	t.Parallel()
	ctx := context.WithValue(context.WithValue(t.Context(), SupportsImagesContextKey, false), ModelNameContextKey, "Qwen")

	got := mcpResponse(ctx, mcp.ToolResult{Type: "image", Content: "a screenshot", Data: onePixelPNG, MediaType: "image/png"})
	require.Equal(t, UnsupportedMediaResponse("Qwen"), got)
}

func TestMCPResponseShowsAnImageToAModelThatSees(t *testing.T) {
	t.Parallel()
	ctx := context.WithValue(t.Context(), SupportsImagesContextKey, true)

	got := mcpResponse(ctx, mcp.ToolResult{Type: "image", Content: "a screenshot", Data: onePixelPNG, MediaType: "image/png", ImagePath: "/x.png"})

	require.Equal(t, "image", got.Type)
	require.Equal(t, onePixelPNG, got.Data)
	require.Equal(t, "a screenshot", got.Content)
	require.Equal(t, fantasy.NewTextResponse("plain"), mcpResponse(ctx, mcp.ToolResult{Type: "text", Content: "plain"}))
}
