package config

import (
	"slices"
	"testing"

	"github.com/stretchr/testify/require"
)

// The read-only and plan tool lists are hand-copied names. If one drifts
// out of the built-in set — a rename, a removal, a typo — the tool is
// simply filtered away and that agent quietly loses a capability, with
// nothing to show for it. This is the check that turns that silence into
// a failing test.
func TestToolSubsetsAreRealTools(t *testing.T) {
	t.Parallel()

	all := allToolNames()
	for _, subset := range []struct {
		name  string
		names []string
	}{
		{"readOnlyToolNames", readOnlyToolNames()},
		{"planToolNames", planToolNames()},
	} {
		t.Run(subset.name, func(t *testing.T) {
			t.Parallel()
			require.NotEmpty(t, subset.names)
			for _, name := range subset.names {
				require.Contains(t, all, name,
					"%s lists %q, which is not a built-in tool; it would be filtered away silently",
					subset.name, name)
			}
		})
	}
}

// Planning is research plus the ability to delegate and ask, so it must
// never be a smaller set than read-only.
func TestPlanToolsIncludeEveryReadOnlyTool(t *testing.T) {
	t.Parallel()

	plan := planToolNames()
	for _, name := range readOnlyToolNames() {
		require.Contains(t, plan, name,
			"a planning turn should not be able to do less looking around than a read-only one")
	}
}

func TestToolSubsetsHaveNoDuplicates(t *testing.T) {
	t.Parallel()

	for name, names := range map[string][]string{
		"allToolNames":      allToolNames(),
		"readOnlyToolNames": readOnlyToolNames(),
		"planToolNames":     planToolNames(),
	} {
		sorted := slices.Clone(names)
		slices.Sort(sorted)
		require.Equal(t, len(names), len(slices.Compact(sorted)),
			"%s contains a duplicate", name)
	}
}

func TestToolsOnlyAndToolsExceptAreComplementary(t *testing.T) {
	t.Parallel()

	data := []string{"a", "b", "c", "d"}
	mask := []string{"b", "d", "zzz"}

	require.Equal(t, []string{"b", "d"}, toolsOnly(data, mask))
	require.Equal(t, []string{"a", "c"}, toolsExcept(data, mask))

	// Together they account for everything, exactly once.
	combined := append(toolsOnly(data, mask), toolsExcept(data, mask)...)
	slices.Sort(combined)
	require.Equal(t, data, combined)
}

func TestToolsOnlyAndToolsExceptEdgeCases(t *testing.T) {
	t.Parallel()

	require.Empty(t, toolsOnly(nil, []string{"a"}))
	require.Empty(t, toolsExcept(nil, []string{"a"}))
	require.Empty(t, toolsOnly([]string{"a"}, nil), "nothing matches an empty mask")
	require.Equal(t, []string{"a"}, toolsExcept([]string{"a"}, nil), "an empty mask excludes nothing")
}

// The real call sites, phrased as the behaviour a user would notice.
func TestResolveAllowedToolsDropsOnlyWhatIsDisabled(t *testing.T) {
	t.Parallel()

	all := allToolNames()
	got := resolveAllowedTools(all, []string{"bash", "write"})
	require.NotContains(t, got, "bash")
	require.NotContains(t, got, "write")
	require.Contains(t, got, "view")
	require.Len(t, got, len(all)-2)

	require.Equal(t, all, resolveAllowedTools(all, nil),
		"no disabled_tools at all must leave the set untouched")
}

func TestResolveReadOnlyToolsExcludesWriters(t *testing.T) {
	t.Parallel()

	got := resolveReadOnlyTools(allToolNames())
	for _, writer := range []string{"bash", "edit", "multiedit", "write", "download"} {
		require.NotContains(t, got, writer,
			"a read-only agent must not be handed %q", writer)
	}
	require.Contains(t, got, "view")
	require.Contains(t, got, "grep")
}

// Disabling a tool must still take it away from the narrower agents;
// the subsets filter what remains, they do not reintroduce anything.
func TestSubsetsRespectDisabledTools(t *testing.T) {
	t.Parallel()

	remaining := resolveAllowedTools(allToolNames(), []string{"grep", "agent"})
	require.NotContains(t, resolveReadOnlyTools(remaining), "grep")
	require.NotContains(t, resolvePlanTools(remaining), "grep")
	require.NotContains(t, resolvePlanTools(remaining), "agent")
}
