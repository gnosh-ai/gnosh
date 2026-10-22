package app

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// TestUpdateCheckEnabled: the check asks for upstream Crush's releases, so
// this fork makes it only when asked to.
func TestUpdateCheckEnabled(t *testing.T) {
	for value, want := range map[string]bool{
		"":      false, // unset: no check
		"1":     false,
		"true":  false,
		"0":     true, // asked for
		"false": true,
		"maybe": false,
	} {
		t.Setenv("CRASH_DISABLE_UPDATE_CHECK", value)
		t.Setenv("CRUSH_DISABLE_UPDATE_CHECK", "")
		require.Equal(t, want, updateCheckEnabled(), "CRASH_DISABLE_UPDATE_CHECK=%q", value)
	}
}
