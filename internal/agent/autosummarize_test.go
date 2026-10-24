package agent

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestAutoSummarize(t *testing.T) {
	t.Parallel()

	// Known small window: compact once within smallContextWindowRatio of full.
	require.False(t, autoSummarize(65536, 40000, false, false), "plenty of room left")
	require.True(t, autoSummarize(65536, 60000, false, false), "within 20% of the window")

	// Known large window: compact within the fixed buffer of full.
	require.False(t, autoSummarize(400000, 300000, false, false))
	require.True(t, autoSummarize(400000, 385000, false, false), "within the 20k buffer")

	// Disabled: never compact, whatever the usage.
	require.False(t, autoSummarize(65536, 65000, true, false))
	require.False(t, autoSummarize(0, 1_000_000, true, false))

	// Unknown window (the bug): a local model whose window could not be
	// discovered used to skip auto-summarize entirely and grow until the
	// provider hard-rejected the request. It must now still auto-compact.
	require.True(t, autoSummarize(0, 60000, false, false), "unknown window must still auto-compact")
	require.False(t, autoSummarize(0, 1000, false, false), "a small session under the fallback budget is fine")
}
