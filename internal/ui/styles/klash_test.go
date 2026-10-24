package styles

import "testing"

func TestKlashCosmosIsTheDefault(t *testing.T) {
	if !IsBuiltinTheme(DefaultThemeName) {
		t.Fatalf("%q is not a built-in theme", DefaultThemeName)
	}
	want := *hex(KlashAurora)
	empty, err := LoadTheme("")
	if err != nil {
		t.Fatalf("load default: %v", err)
	}
	if got := *hex(empty.Logo.TitleColorA); got != want {
		t.Errorf("an empty theme name gave a wordmark starting at %s, want Klash's %s", got, want)
	}
	if got := *hex(ThemeFromConfig("no-such-theme").Logo.TitleColorA); got != want {
		t.Errorf("an unknown theme fell back to a wordmark starting at %s, want Klash's %s", got, want)
	}
}

func TestKlashCosmos_Contrast(t *testing.T) {
	// The brand colors are bright, so text on them must be dark; these
	// pairs are the ones a wrong onPrimary or override would break.
	const minAA = 4.5
	s := KlashCosmos()
	o := klashOpts()
	for _, c := range []struct{ name, fg, bg string }{
		{"inline code", *s.Markdown.Code.Color, *s.Markdown.Code.BackgroundColor},
		{"level-one heading", *s.Markdown.H1.Color, *s.Markdown.H1.BackgroundColor},
		{"level-one heading in a plan", *s.PlanMarkdown.H1.Color, *s.PlanMarkdown.H1.BackgroundColor},
		{"text on primary", *hex(o.onPrimary), *hex(o.primary)},
		{"text on secondary", *hex(o.onPrimary), *hex(o.secondary)},
		{"text on a button", *hex(o.onPrimary), *hex(o.button)},
		{"text on a hovered button", *hex(o.onPrimary), *hex(o.buttonHovered)},
		{"body text", *hex(o.fgBase), *hex(o.bgBase)},
	} {
		if ratio := contrastRatio(c.fg, c.bg); ratio < minAA {
			t.Errorf("%s: %s on %s is %.2f:1, below WCAG AA (%.1f)", c.name, c.fg, c.bg, ratio, minAA)
		}
	}
}
