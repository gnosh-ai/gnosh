package config

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// /autocompact takes auto, or 100k to 1M tokens with a k or M suffix,
// whole, or in thousands, as Claude Code takes them.
func TestParseAutoCompactWindow(t *testing.T) {
	t.Parallel()

	for arg, want := range map[string]int64{
		"auto":   0,
		" AUTO ": 0,
		"150k":   150_000,
		"150K":   150_000,
		"1M":     1_000_000,
		"0.5m":   500_000,
		"200000": 200_000,
		"200":    200_000,
		"100k":   100_000,
	} {
		got, ok := ParseAutoCompactWindow(arg)
		require.True(t, ok, arg)
		require.Equal(t, want, got, arg)
	}
	for _, arg := range []string{"", "lots", "99k", "50", "2M", "1000001", "-150k", "k"} {
		_, ok := ParseAutoCompactWindow(arg)
		require.False(t, ok, arg)
	}
}
