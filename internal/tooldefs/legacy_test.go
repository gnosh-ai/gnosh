package tooldefs

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// The tools renamed with Crush to Gnosh keep their old names as aliases,
// so a config or hook that names one keeps working.
func TestLegacyToolNames(t *testing.T) {
	t.Parallel()

	require.Equal(t, "gnosh_logs", Canonical("crush_logs"))
	require.Equal(t, "gnosh_info", Canonical("crush_info"))
	require.Equal(t, "bash", Canonical("bash"))
	require.Equal(t, "crush_logs", LegacyName("gnosh_logs"))
	require.Empty(t, LegacyName("bash"))

	require.True(t, IsKnown("gnosh_logs"))
	require.True(t, IsKnown("crush_logs"), "the old name is still a known tool")
	require.False(t, IsKnown("crush_banana"))
	require.NotContains(t, Names(), "crush_logs", "only current names are listed")
}
