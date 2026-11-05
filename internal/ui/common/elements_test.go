package common

import (
	"strings"
	"testing"

	"github.com/charmbracelet/crush/internal/ui/styles"
	"github.com/charmbracelet/x/ansi"
	"github.com/stretchr/testify/require"
)

func TestFormatTokensAndCostPrefixesEstimatedUsage(t *testing.T) {
	t.Parallel()

	sty := styles.CharmtonePantera()

	rendered := formatTokensAndCost(&sty, 120, 1000, 0, true)
	actual := ansi.Strip(rendered)

	require.Contains(t, actual, "~12%")
	require.Contains(t, actual, "(120)")
	require.Contains(t, actual, "$0.00")
	require.True(t, strings.Contains(rendered, sty.ModelInfo.TokenPercentage.Render("~12%")))
}

func TestFormatTokensAndCostOmitsEstimatedPrefix(t *testing.T) {
	t.Parallel()

	sty := styles.CharmtonePantera()

	actual := ansi.Strip(formatTokensAndCost(&sty, 120, 1000, 0, false))

	require.Contains(t, actual, "12%")
	require.NotContains(t, actual, "~12%")
}

// A section counts one row per status line, so a name longer than the
// section is cut to its width, never left to wrap.
func TestStatusStaysWithinItsWidth(t *testing.T) {
	t.Parallel()

	sty := styles.CharmtonePantera()

	for _, width := range []int{10, 15, 22} {
		got := Status(&sty, StatusOpts{Icon: "●", Title: "authoring-language-pack", Description: "ready"}, width)
		require.LessOrEqual(t, ansi.StringWidth(got), width, "%q at %d columns", ansi.Strip(got), width)
		require.True(t, strings.HasPrefix(ansi.Strip(got), "● author"), "%q", ansi.Strip(got))
	}
	short := ansi.Strip(Status(&sty, StatusOpts{Icon: "●", Title: "jq"}, 15))
	require.Equal(t, "● jq", short)
}
