package agent

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// view's allow_large may bring in, in a step, 70% of what is left before
// the point at which the conversation is compacted, as Claude Code allows,
// or before the window's end with compaction off; none past that.
func TestReadRoomIsClaudeCodes(t *testing.T) {
	t.Parallel()

	// A 262,144 window compacts at 242,144.
	require.Equal(t, 99500, readRoom(262144, 0, 100000, false), "70% of the 142,144 left")
	require.Equal(t, 113500, readRoom(262144, 0, 100000, true), "compaction off: 70% of the 162,144 left in the window")
	require.LessOrEqual(t, readRoom(262144, 0, 250000, false), 0, "past the point, no room")
}
