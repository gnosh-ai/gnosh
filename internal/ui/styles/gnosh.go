package styles

import (
	"image/color"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/crush/internal/ui/fade"
	"github.com/charmbracelet/x/exp/charmtone"
)

// DefaultThemeName is the theme the UI uses when none is configured.
const DefaultThemeName = "gnosh-cosmos"

// Gnosh keeps Crush's own purple and hot pink, the colors of a nebula, and
// adds green and gold sparingly: every fade starts green and ends gold with
// purple and pink between, and the logo's mark is gold. The banner,
// social card and avatar in docs/brand are drawn in the same colors
// (scripts/brand.py).
var (
	GnoshPurple = charmtone.Charple
	GnoshPink   = charmtone.Dolly
	GnoshGold   = lipgloss.Color("#FBD237")
	GnoshGreen  = charmtone.Julep
)

// GnoshCosmos returns Gnosh's dark theme: Charmtone's colors with Gnosh's
// starlight on top. It's the default style for the UI.
func GnoshCosmos() Styles {
	return gnoshOverrides(quickStyle(gnoshOpts()))
}

// gnoshOpts returns the quickStyleOpts for the Gnosh Cosmos theme, which are
// Charmtone's, kept whole.
func gnoshOpts() quickStyleOpts {
	return charmtoneOpts()
}

// GnoshNebula returns the ends of Gnosh's fades: green, and a gold that the
// fade reaches through Crush's purple and hot pink, so those two hold the
// middle and green and gold only the ends.
func GnoshNebula() (from, to color.Color) {
	return GnoshGreen, &fade.Via{Color: GnoshGold, Through: []color.Color{GnoshPurple, GnoshPink}}
}

// gnoshOverrides applies Charmtone's own tweaks, then Gnosh's green and
// gold: in every fade, and in the logo's mark and version.
func gnoshOverrides(s Styles) Styles {
	s = charmtoneOverrides(s)

	// The wordmark, the small and compact logos, dialog titles and the
	// working animation all fade through the nebula.
	from, to := GnoshNebula()
	s.Logo.TitleColorA, s.Logo.TitleColorB = from, to
	s.Logo.SmallGradFromColor, s.Logo.SmallGradToColor = from, to
	s.Header.LogoGradFromColor, s.Header.LogoGradToColor = from, to
	s.Dialog.TitleGradFromColor, s.Dialog.TitleGradToColor = from, to
	s.WorkingGradFromColor, s.WorkingGradToColor = from, to

	// A terminal without 24-bit color, as Apple's Terminal is, bands a fade
	// over the whole name into stripes, so there the wordmarks draw Gno in
	// one green and fade the rest from Crush's purple to hot pink. Jae,
	// 2026-10-30: "at least make 'gno' one color, green i suppose"; later
	// he said that was meant for such terminals, and that Ghostty keeps the
	// fade through the nebula, which looked better.
	s.Logo.GnoColor = GnoshGreen
	s.Logo.GnoRestColorA, s.Logo.GnoRestColorB = GnoshPurple, GnoshPink

	// The logo's fields fade from purple to hot pink instead of lying flat.
	s.Logo.FieldColorB = GnoshPink

	// The mark over the wordmark is gold, the version green.
	s.Logo.CharmColor = GnoshGold
	s.Logo.VersionColor = GnoshGreen
	return s
}
