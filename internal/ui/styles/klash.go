package styles

import (
	"image/color"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/exp/charmtone"
)

// DefaultThemeName is the theme the UI uses when none is configured.
const DefaultThemeName = "klash-cosmos"

// Klash keeps Crush's own purple and hot pink, the colors of a nebula, and
// adds green and gold sparingly, as starlight: the klash.pro mark, the
// version, a few stars among the logo's diagonal lines. The banner, social
// card and avatar in docs/brand are drawn in the same colors
// (scripts/brand.py).
var (
	KlashPurple = charmtone.Charple
	KlashPink   = charmtone.Dolly
	KlashGold   = lipgloss.Color("#FBD237")
	KlashGreen  = charmtone.Julep
)

// KlashCosmos returns Klash's dark theme: Charmtone's colors with Klash's
// starlight on top. It's the default style for the UI.
func KlashCosmos() Styles {
	return klashOverrides(quickStyle(klashOpts()))
}

// klashOpts returns the quickStyleOpts for the Klash Cosmos theme, which are
// Charmtone's, kept whole.
func klashOpts() quickStyleOpts {
	return charmtoneOpts()
}

// klashOverrides applies Charmtone's own tweaks, then Klash's green and
// gold, kept to the logo.
func klashOverrides(s Styles) Styles {
	s = charmtoneOverrides(s)

	// The logo's fields fade from purple to hot pink instead of lying flat,
	// with a star here and there.
	s.Logo.FieldColorB = KlashPink
	s.Logo.StarColors = []color.Color{KlashGold, KlashGreen}

	// The mark over the wordmark and the sparkle beside the name are gold,
	// the version green.
	s.Logo.CharmColor = KlashGold
	s.Logo.VersionColor = KlashGreen
	s.Logo.SmallCharm = s.Logo.SmallCharm.Foreground(KlashGold)
	s.Header.Charm = s.Header.Charm.Foreground(KlashGold)
	return s
}
