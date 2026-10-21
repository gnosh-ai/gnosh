package config

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestGetLoopMaxTurns(t *testing.T) {
	t.Parallel()

	// Unset, nil receiver, and nonsense values all mean the default, so a
	// loop always has a positive cap to count down from.
	var nilOpts *Options
	require.Equal(t, DefaultLoopMaxTurns, nilOpts.GetLoopMaxTurns())
	require.Equal(t, DefaultLoopMaxTurns, (&Options{}).GetLoopMaxTurns())
	for _, bad := range []int{0, -1, -100} {
		require.Equal(t, DefaultLoopMaxTurns, (&Options{LoopMaxTurns: &bad}).GetLoopMaxTurns(),
			"a non-positive cap would stop the loop before its first turn")
	}

	n := 200
	require.Equal(t, 200, (&Options{LoopMaxTurns: &n}).GetLoopMaxTurns())
}
