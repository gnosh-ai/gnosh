package styles

import (
	"charm.land/glamour/v2/ansi"
	"charm.land/lipgloss/v2"
)

// DefaultThemeName is the theme the UI uses when none is configured.
const DefaultThemeName = "klash-cosmos"

// Klash's own colors: night greys with a little green in them, under an
// aurora green that fades to starlight gold. The Klash Cosmos theme is drawn
// in them, and so is what the CLI prints outside the TUI (help, --version,
// dirs, sessions). The banner, social card and avatar in docs/brand use the
// same colors (scripts/brand.py).
var (
	// Greys, darkest to lightest, at the same steps as Charmtone's.
	KlashNight  = lipgloss.Color("#181F1B")
	KlashDusk   = lipgloss.Color("#232B27")
	KlashShade  = lipgloss.Color("#2F3834")
	KlashSlate  = lipgloss.Color("#414A46")
	KlashMoss   = lipgloss.Color("#555F5A")
	KlashLichen = lipgloss.Color("#7D8680")
	KlashMist   = lipgloss.Color("#B9C0BA")
	KlashMoon   = lipgloss.Color("#E8EBE5")
	KlashStar   = lipgloss.Color("#FCFAF1")
	KlashInk    = lipgloss.Color("#09140F") // text on the bright colors

	// The aurora, green to gold.
	KlashAurora = lipgloss.Color("#1DDC8D")
	KlashMint   = lipgloss.Color("#5CEEC3")
	KlashSpring = lipgloss.Color("#88F6DA")
	KlashFern   = lipgloss.Color("#22B988")
	KlashPine   = lipgloss.Color("#0C5C3C")
	KlashLime   = lipgloss.Color("#B3EE55")
	KlashGold   = lipgloss.Color("#FBD237")
	KlashSun    = lipgloss.Color("#FEE32D")
	KlashCream  = lipgloss.Color("#F2ED94")
	KlashAmber  = lipgloss.Color("#FAAB3F")
	KlashEmber  = lipgloss.Color("#FC9252")

	// Everything else in the sky.
	KlashComet  = lipgloss.Color("#4EBDF7")
	KlashIce    = lipgloss.Color("#82D7FF")
	KlashDeep   = lipgloss.Color("#1479B0")
	KlashCoral  = lipgloss.Color("#FE6270")
	KlashRuby   = lipgloss.Color("#EC4764")
	KlashOrchid = lipgloss.Color("#D17BE8")
)

// KlashCosmos returns Klash's dark theme. It's the default style for the
// UI.
func KlashCosmos() Styles {
	return klashOverrides(quickStyle(klashOpts()))
}

// klashOpts returns the quickStyleOpts for the Klash Cosmos theme.
func klashOpts() quickStyleOpts {
	return quickStyleOpts{
		primary:   KlashAurora,
		secondary: KlashGold,
		accent:    KlashLime,
		keyword:   KlashAmber,

		fgBase:       KlashMoon,
		fgSubtle:     KlashMist,
		fgMoreSubtle: KlashLichen,
		fgMostSubtle: KlashMoss,

		// The brand colors are bright, so what sits on them is dark.
		onPrimary: KlashInk,

		bgBase:         KlashNight,
		bgLeastVisible: KlashDusk,
		bgLessVisible:  KlashShade,
		bgMostVisible:  KlashSlate,

		separator: KlashShade,

		destructive:       KlashCoral,
		error:             KlashRuby,
		warningSubtle:     KlashCream,
		warning:           KlashAmber,
		attention:         KlashEmber,
		busy:              KlashSun,
		info:              KlashComet,
		infoMoreSubtle:    KlashIce,
		infoMostSubtle:    KlashDeep,
		success:           KlashMint,
		successMoreSubtle: KlashSpring,
		successMostSubtle: KlashFern,
		yolo:              KlashCream,
		plan:              KlashAurora,
		planMoreSubtle:    KlashPine,

		diffInsertFg: lipgloss.Color("#5A9E6F"),
		diffDeleteFg: lipgloss.Color("#A85C60"),

		button:         KlashGold,
		buttonSubtle:   KlashShade,
		buttonInactive: KlashSlate,
		buttonHovered:  KlashMist,

		// ANSI 16-color palette for remapping raw terminal output
		// (e.g. bang-mode shell commands) onto the same sky.
		ansiBlack:   KlashDusk,
		ansiRed:     KlashCoral,
		ansiGreen:   KlashFern,
		ansiYellow:  KlashGold,
		ansiBlue:    lipgloss.Color("#5B9DF5"),
		ansiMagenta: KlashOrchid,
		ansiCyan:    lipgloss.Color("#4FD6D6"),
		ansiWhite:   KlashMist,

		ansiBrightBlack:   KlashMoss,
		ansiBrightRed:     lipgloss.Color("#FF8A93"),
		ansiBrightGreen:   KlashAurora,
		ansiBrightYellow:  KlashSun,
		ansiBrightBlue:    lipgloss.Color("#82B8FF"),
		ansiBrightMagenta: lipgloss.Color("#E9A3F5"),
		ansiBrightCyan:    lipgloss.Color("#82E8E8"),
		ansiBrightWhite:   KlashStar,
	}
}

// klashOverrides applies Klash Cosmos tweaks that don't fit the token
// model of [quickStyleOpts].
func klashOverrides(s Styles) Styles {
	// The wordmark runs green to gold, left to right, as on the README
	// banner; quickStyle runs the other way, secondary to primary.
	s.Logo.TitleColorA = KlashAurora
	s.Logo.TitleColorB = KlashGold
	s.Logo.SmallGradFromColor = KlashAurora
	s.Logo.SmallGradToColor = KlashGold
	s.Header.LogoGradFromColor = KlashAurora
	s.Header.LogoGradToColor = KlashGold

	// Headings in gold rather than the generic info blue, in replies and
	// in the plan card. A level-one heading sits on the primary color,
	// which is bright here, so its text is dark.
	s.Markdown.Heading.Color = hex(KlashGold)
	s.Markdown.H1.Color = hex(KlashInk)
	s.PlanMarkdown.Heading.Color = hex(KlashGold)
	for _, h := range []*ansi.StyleBlock{&s.PlanMarkdown.H2, &s.PlanMarkdown.H3, &s.PlanMarkdown.H4, &s.PlanMarkdown.H5} {
		h.Color = hex(KlashGold)
	}
	s.PlanMarkdown.H1.Color = hex(KlashInk)

	// Inline code in mint rather than coral, so it doesn't read as an
	// error.
	s.Markdown.Code.Color = hex(KlashMint)
	s.PlanMarkdown.Code.Color = hex(KlashMint)
	return s
}
