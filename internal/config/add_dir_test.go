package config

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// AddDir adds to the directories added with --add-dir; a list read
// before is left as it was.
func TestAddDir(t *testing.T) {
	t.Parallel()

	s := NewTestStore(&Config{})
	s.Overrides().AddedDirs = []string{"/src/gno"}
	before := s.AddedDirs()
	s.AddDir("/src/klash")
	require.Equal(t, []string{"/src/gno", "/src/klash"}, s.AddedDirs())
	require.Equal(t, []string{"/src/gno"}, before)
	require.Equal(t, []string{"/src/gno", "/src/klash"}, s.Overrides().AddedDirs)
}
