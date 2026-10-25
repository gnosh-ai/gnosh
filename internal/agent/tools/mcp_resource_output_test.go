package tools

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/charmbracelet/crush/internal/agent/tools/mcp"
	"github.com/stretchr/testify/require"
)

// fakePNG is a binary part of a resource: 24 bytes that are not UTF-8.
var fakePNG = append([]byte{0x89}, "PNG not really an image"...)

func TestResourceTextSavesABinaryPart(t *testing.T) {
	t.Parallel()
	dataDir := t.TempDir()

	got := resourceText([]*mcp.ResourceContents{{URI: "res://shot.png", MIMEType: "image/png", Blob: fakePNG}}, "pics", "res://asked", dataDir)

	prefix := "[Resource from pics at res://shot.png] Binary content (image/png, 24 bytes) saved to "
	require.True(t, strings.HasPrefix(got, prefix), got)
	path := strings.TrimPrefix(got, prefix)
	require.Equal(t, filepath.Join(dataDir, "shell-output"), filepath.Dir(path))
	require.Equal(t, ".png", filepath.Ext(path))
	saved, err := os.ReadFile(path)
	require.NoError(t, err)
	require.Equal(t, fakePNG, saved)
}

func TestResourceTextNamesABinaryPartByTheURIAsked(t *testing.T) {
	t.Parallel()
	dataDir := t.TempDir()

	got := resourceText([]*mcp.ResourceContents{{Blob: fakePNG}}, "pics", "res://asked", dataDir)

	prefix := "[Resource from pics at res://asked] Binary content (unknown type, 24 bytes) saved to "
	require.True(t, strings.HasPrefix(got, prefix), got)
	require.Equal(t, ".bin", filepath.Ext(got))
}

func TestResourceTextJoinsItsParts(t *testing.T) {
	t.Parallel()
	dataDir := t.TempDir()

	got := resourceText([]*mcp.ResourceContents{{Text: "one"}, nil, {URI: "res://empty"}, {Blob: fakePNG}, {Text: "two"}}, "pics", "res://asked", dataDir)

	lines := strings.Split(got, "\n")
	require.Len(t, lines, 3, got)
	require.Equal(t, "one", lines[0])
	require.True(t, strings.HasPrefix(lines[1], "[Resource from pics at res://asked] Binary content"), lines[1])
	require.Equal(t, "two", lines[2])
	require.Empty(t, resourceText(nil, "pics", "res://asked", dataDir))
}

func TestResourceTextSaysWhyABinaryPartWasNotSaved(t *testing.T) {
	t.Parallel()
	dataDir := filepath.Join(t.TempDir(), "a-file")
	require.NoError(t, os.WriteFile(dataDir, []byte("x"), 0o600))

	got := resourceText([]*mcp.ResourceContents{{Blob: fakePNG}}, "pics", "res://asked", dataDir)
	require.True(t, strings.HasPrefix(got, "Binary content could not be saved to disk: "), got)
	require.Contains(t, got, "not a directory")

	tooBig := make([]byte, resourceBlobLimit+1)
	got = resourceText([]*mcp.ResourceContents{{Blob: tooBig}}, "pics", "res://asked", t.TempDir())
	require.Equal(t, "Binary content could not be saved to disk: content is 104857601 bytes, over the 104857600 byte persist limit", got)
}

func TestResourceTextKeepsFiftyThousandCharacters(t *testing.T) {
	t.Parallel()
	dataDir := t.TempDir()

	// Two bytes each: the limit counts characters, not bytes.
	text := strings.Repeat(string(rune(0xe9)), resourcePersistChars)
	require.Equal(t, text, resourceText([]*mcp.ResourceContents{{Text: text}}, "srv", "res://big", dataDir))
	require.NoDirExists(t, filepath.Join(dataDir, "shell-output"))

	longer := text + string(rune(0xe9))
	require.True(t, strings.HasPrefix(resourceText([]*mcp.ResourceContents{{Text: longer}}, "srv", "res://big", dataDir), "<persisted-output>\n"))
}

// numberedLines is 5455 lines of "line 00000" and a newline: 60,005 bytes.
func numberedLines() string {
	var b strings.Builder
	for i := 0; b.Len() < 60000; i++ {
		fmt.Fprintf(&b, "line %05d\n", i)
	}
	return b.String()
}

func TestResourceTextSavesLongText(t *testing.T) {
	t.Parallel()
	dataDir := t.TempDir()
	text := numberedLines()

	got := resourceText([]*mcp.ResourceContents{{Text: text}}, "srv", "res://big", dataDir)

	prefix := "<persisted-output>\nOutput too large (58.6KB). Full output saved to: "
	require.True(t, strings.HasPrefix(got, prefix), got[:200])
	path, _, _ := strings.Cut(strings.TrimPrefix(got, prefix), "\n")
	require.Equal(t, filepath.Join(dataDir, "shell-output"), filepath.Dir(path))
	saved, err := os.ReadFile(path)
	require.NoError(t, err)
	require.Equal(t, text, string(saved))

	// The preview ends at the last line's end in its first 2000 characters.
	want := prefix + path + "\n\nPreview (first 2KB):\n" + text[:1990] + "\n...\n</persisted-output>"
	require.Equal(t, want, got)
	require.True(t, strings.HasSuffix(text[:1990], "line 00180"))
}

func TestResourceTextCutsTextItCannotSave(t *testing.T) {
	t.Parallel()
	dataDir := filepath.Join(t.TempDir(), "a-file")
	require.NoError(t, os.WriteFile(dataDir, []byte("x"), 0o600))
	text := numberedLines()

	got := resourceText([]*mcp.ResourceContents{{Text: text}}, "srv", "res://big", dataDir)

	want := "<truncated-output>\nOutput too large (58.6KB). It could not be saved, so only the first 48.8KB are shown; " +
		"the rest was dropped. If the tool can page or filter its results, call it again for the part you need.\n\n" +
		text[:49994] + "\n</truncated-output>"
	require.Equal(t, want, got)
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
		require.Equal(t, want, claudeSize(n), n)
	}
}

func TestPreviewOf(t *testing.T) {
	t.Parallel()
	require.Equal(t, "ab\ncd", previewOf("ab\ncd", 10), "shorter than n: whole")
	require.Equal(t, "aaaa\nbbbb", previewOf("aaaa\nbbbb\ncccc", 12), "a line's end in the second half")
	require.Equal(t, "a\nbbbbbb", previewOf("a\nbbbbbbbbbb", 8), "a line's end only in the first half")
	require.Equal(t, "abcde\nfghi", previewOf("abcde\nfghij", 10), "a line's end at the half is not past it")
	require.Equal(t, strings.Repeat(string(rune(0xe9)), 3), previewOf(strings.Repeat(string(rune(0xe9)), 5), 3), "n counts characters")
}
