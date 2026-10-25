package mcp

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestSaveBlob(t *testing.T) {
	t.Parallel()
	dataDir := t.TempDir()

	got, err := SaveBlob([]byte("a,b"), "text/csv", "[X] ", dataDir)
	require.NoError(t, err)
	prefix := "[X] Binary content (text/csv, 3 bytes) saved to "
	require.True(t, strings.HasPrefix(got, prefix), got)
	path := strings.TrimPrefix(got, prefix)
	require.Equal(t, filepath.Join(dataDir, "shell-output"), filepath.Dir(path))
	require.Equal(t, ".csv", filepath.Ext(path))
	saved, err := os.ReadFile(path)
	require.NoError(t, err)
	require.Equal(t, "a,b", string(saved))

	_, err = SaveBlob(make([]byte, blobLimit+1), "", "", dataDir)
	require.EqualError(t, err, "content is 104857601 bytes, over the 104857600 byte persist limit")
}

func TestBlobExtension(t *testing.T) {
	t.Parallel()
	for mimeType, want := range map[string]string{
		"image/png":                  "png",
		" IMAGE/JPEG ; quality=high": "jpg",
		"application/pdf":            "pdf",
		"image/vnd.microsoft.icon":   "ico",
		"application/x-unknown":      "bin",
		"":                           "bin",
	} {
		require.Equal(t, want, blobExtension(mimeType), mimeType)
	}
}

func TestClaudeSize(t *testing.T) {
	t.Parallel()
	for n, want := range map[int]string{
		0:                     "0 bytes",
		1023:                  "1023 bytes",
		1024:                  "1KB",
		1536:                  "1.5KB",
		2000:                  "2KB",
		1 << 20:               "1MB",
		5<<20 + 1<<19 + 1<<17: "5.6MB",
		1 << 30:               "1GB",
		3<<30 + 1<<29:         "3.5GB",
	} {
		require.Equal(t, want, ClaudeSize(n), n)
	}
}
