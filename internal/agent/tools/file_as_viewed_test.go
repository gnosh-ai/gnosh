package tools

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

// FileAsViewed gives the start of a file as the view tool does: numbered
// lines in a <file> block, and where to read on when it is cut short.
func TestFileAsViewed(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	write := func(name, content string) string {
		path := filepath.Join(dir, name)
		require.NoError(t, os.WriteFile(path, []byte(content), 0o600))
		return path
	}

	text, err := FileAsViewed(write("a.txt", "one\ntwo"), 10, 1000)
	require.NoError(t, err)
	require.Equal(t, "<file>\n     1|one\n     2|two\n</file>\n", text)

	text, err = FileAsViewed(write("long.txt", "1\n2\n3\n4"), 2, 1000)
	require.NoError(t, err)
	require.Equal(t, "<file>\n     1|1\n     2|2\n\n(File has more lines. To read on, use offset 3)\n</file>\n", text)

	text, err = FileAsViewed(write("wide.txt", "12345\n67890\nabcde"), 10, 11)
	require.NoError(t, err)
	require.Equal(t, "<file>\n     1|12345\n     2|67890\n\n(File has more lines. To read on, use offset 3)\n</file>\n", text, "cut at the size limit too")

	_, err = FileAsViewed(filepath.Join(dir, "gone.txt"), 10, 1000)
	require.Error(t, err)
	_, err = FileAsViewed(write("bin", "\xff\xfe"), 10, 1000)
	require.Error(t, err)
	_, err = FileAsViewed(dir, 10, 1000)
	require.Error(t, err, "a directory is not a text file")
}
