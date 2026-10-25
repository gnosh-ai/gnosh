package styles

import (
	"image/color"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/crush/internal/ui/fade"
	"github.com/charmbracelet/x/exp/charmtone"
)

// DefaultThemeName is the theme the UI uses when none is configured.
const DefaultThemeName = "klash-cosmos"

// Klash keeps Crush's own purple and hot pink, the colors of a nebula, and
// adds green and gold sparingly: every fade starts green and ends gold with
// purple and pink between, and the logo's mark is gold. The banner,
// social card and avatar in docs/brand are drawn in the same colors
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

// KlashNebula returns the ends of Klash's fades: green, and a gold that the
// fade reaches through Crush's purple and hot pink, so those two hold the
// middle and green and gold only the ends.
func KlashNebula() (from, to color.Color) {
	return KlashGreen, &fade.Via{Color: KlashGold, Through: []color.Color{KlashPurple, KlashPink}}
}

// klashOverrides applies Charmtone's own tweaks, then Klash's green and
// gold: in every fade, and in the logo's mark and version.
func klashOverrides(s Styles) Styles {
	s = charmtoneOverrides(s)

	// The wordmark, the small and compact logos, dialog titles and the
	// working animation all fade through the nebula.
	from, to := KlashNebula()
	s.Logo.TitleColorA, s.Logo.TitleColorB = from, to
	s.Logo.SmallGradFromColor, s.Logo.SmallGradToColor = from, to
	s.Header.LogoGradFromColor, s.Header.LogoGradToColor = from, to
	s.Dialog.TitleGradFromColor, s.Dialog.TitleGradToColor = from, to
	s.WorkingGradFromColor, s.WorkingGradToColor = from, to

	// The logo's fields fade from purple to hot pink instead of lying flat.
	s.Logo.FieldColorB = KlashPink

	// The mark over the wordmark is gold, the version green.
	s.Logo.CharmColor = KlashGold
	s.Logo.VersionColor = KlashGreen
	return s
}
