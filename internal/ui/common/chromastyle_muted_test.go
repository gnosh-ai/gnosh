package common

import (
	"image/color"
	"testing"

	"charm.land/lipgloss/v2"
	"github.com/alecthomas/chroma/v2"
	"github.com/charmbracelet/crush/internal/ui/styles"
	"github.com/stretchr/testify/require"
)

// MutedChromaStyle takes each token colour halfway to the one given, sets
// no background, and leaves the style as it is for no colour.
func TestMutedChromaStyleMixesHalfway(t *testing.T) {
	sty := styles.GnoshCosmos()
	base := ChromaStyle(&sty, nil)
	gray := color.RGBA{R: 0x80, G: 0x80, B: 0x80, A: 0xff}
	muted := MutedChromaStyle(&sty, gray)

	seen := 0
	for _, tt := range []chroma.TokenType{chroma.Keyword, chroma.LiteralString, chroma.NameFunction, chroma.Comment, chroma.LiteralNumber} {
		b, m := base.Get(tt).Colour, muted.Get(tt).Colour
		if !b.IsSet() {
			continue
		}
		seen++
		require.InDelta(t, (float64(b.Red())+0x80)/2, float64(m.Red()), 1, tt.String())
		require.InDelta(t, (float64(b.Green())+0x80)/2, float64(m.Green()), 1, tt.String())
		require.InDelta(t, (float64(b.Blue())+0x80)/2, float64(m.Blue()), 1, tt.String())
		require.False(t, muted.Get(tt).Background.IsSet(), "no background behind %s", tt)
	}
	require.NotZero(t, seen, "the theme colours some of these tokens")
	require.Same(t, muted, MutedChromaStyle(&sty, gray), "memoized")
	require.Same(t, base, MutedChromaStyle(&sty, lipgloss.NoColor{}))
}
