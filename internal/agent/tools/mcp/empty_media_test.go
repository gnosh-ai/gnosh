package mcp

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/stretchr/testify/require"
)

// A blank payload decodes to nothing, so it reaches the agent as no media
// at all rather than as whitespace posing as image bytes, and nothing is
// saved for it.
func TestBlankMediaDecodesToEmpty(t *testing.T) {
	t.Parallel()

	for _, tt := range []struct {
		name    string
		content mcp.Content
	}{
		{"empty image", &mcp.ImageContent{Data: []byte{}, MIMEType: "image/png"}},
		{"blank image", &mcp.ImageContent{Data: []byte("  "), MIMEType: "image/png"}},
		{"nil image", &mcp.ImageContent{MIMEType: "image/png"}},
		{"empty svg", &mcp.ImageContent{Data: []byte{}, MIMEType: "image/svg+xml"}},
		{"empty audio", &mcp.AudioContent{Data: []byte{}, MIMEType: "audio/wav"}},
		{"blank audio", &mcp.AudioContent{Data: []byte("\n"), MIMEType: "audio/wav"}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			dataDir := t.TempDir()
			got := toolResultFromCall(&mcp.CallToolResult{Content: []mcp.Content{tt.content}}, "srv", dataDir)
			require.Equal(t, ToolResult{Type: "text"}, got)
			require.NoDirExists(t, filepath.Join(dataDir, "shell-output"))
		})
	}
}

func TestEmptyImageKeepsAccompanyingText(t *testing.T) {
	t.Parallel()

	got := toolResultFromCall(&mcp.CallToolResult{
		Content: []mcp.Content{
			&mcp.TextContent{Text: "scrolled to the bottom"},
			&mcp.ImageContent{Data: []byte{}, MIMEType: "image/png"},
		},
	}, "srv", t.TempDir())
	require.Empty(t, got.Data)
	require.Equal(t, "scrolled to the bottom", got.Content)
}

func TestLaterNonEmptyImageWins(t *testing.T) {
	t.Parallel()

	real := []byte{0x89, 'P', 'N', 'G'}
	got := toolResultFromCall(&mcp.CallToolResult{
		Content: []mcp.Content{
			&mcp.ImageContent{Data: []byte{}, MIMEType: "image/gif"},
			&mcp.ImageContent{Data: real, MIMEType: "image/png"},
		},
	}, "srv", t.TempDir())
	require.Equal(t, "image", got.Type)
	require.Equal(t, real, got.Data)
	require.Equal(t, "image/png", got.MediaType)
}

// Audio is saved and named, as Claude Code saves it: most models take none
// in a tool's result.
func TestAudioIsSaved(t *testing.T) {
	t.Parallel()
	dataDir := t.TempDir()

	wav := []byte{0xff, 0xf3, 0x44, 0xc4}
	got := toolResultFromCall(&mcp.CallToolResult{
		Content: []mcp.Content{
			&mcp.TextContent{Text: "a song"},
			&mcp.ImageContent{Data: []byte{}, MIMEType: "image/png"},
			&mcp.AudioContent{Data: wav, MIMEType: "audio/wav"},
		},
	}, "srv", dataDir)

	require.Equal(t, "text", got.Type)
	require.Empty(t, got.Data)
	prefix := "a song\n[Audio from srv] Binary content (audio/wav, 4 bytes) saved to "
	require.True(t, strings.HasPrefix(got.Content, prefix), got.Content)
	path := strings.TrimPrefix(got.Content, prefix)
	require.Equal(t, filepath.Join(dataDir, "shell-output"), filepath.Dir(path))
	require.Equal(t, ".wav", filepath.Ext(path))
	saved, err := os.ReadFile(path)
	require.NoError(t, err)
	require.Equal(t, wav, saved)
}

func TestGoodImageStillPasses(t *testing.T) {
	t.Parallel()

	real := []byte{0x89, 'P', 'N', 'G', 0x0d, 0x0a}
	got := toolResultFromCall(&mcp.CallToolResult{
		Content: []mcp.Content{&mcp.ImageContent{Data: real, MIMEType: "image/png"}},
	}, "srv", t.TempDir())
	require.Equal(t, "image", got.Type)
	require.Equal(t, real, got.Data)
}
