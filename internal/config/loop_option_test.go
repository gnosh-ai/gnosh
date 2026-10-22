package config

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestGetLoopMaxTurns(t *testing.T) {
	t.Parallel()

	// Unset, nil receiver, and nonsense values all mean no cap: by default
	// a loop stops on its own only when the agent says so, never on a count.
	var nilOpts *Options
	require.Zero(t, nilOpts.GetLoopMaxTurns())
	require.Zero(t, (&Options{}).GetLoopMaxTurns())
	for _, bad := range []int{0, -1, -100} {
		require.Zero(t, (&Options{LoopMaxTurns: &bad}).GetLoopMaxTurns(),
			"a non-positive cap would stop the loop before its first turn")
	}

	n := 200
	require.Equal(t, 200, (&Options{LoopMaxTurns: &n}).GetLoopMaxTurns())
}
