package styles

import "testing"

func TestKlashCosmosIsTheDefault(t *testing.T) {
	if !IsBuiltinTheme(DefaultThemeName) {
		t.Fatalf("%q is not a built-in theme", DefaultThemeName)
	}
	for name, s := range map[string]Styles{
		"an empty theme name": must(LoadTheme("")),
		"an unknown theme":    ThemeFromConfig("no-such-theme"),
	} {
		if got, want := *hex(s.Logo.CharmColor), *hex(KlashGold); got != want {
			t.Errorf("%s gave a mark in %s, want Klash's gold %s", name, got, want)
		}
	}
}

func TestKlashCosmos_KeepsCrushsColors(t *testing.T) {
	// Klash adds starlight to Charmtone; it doesn't recolor it.
	k, c := KlashCosmos(), CharmtonePantera()
	for _, p := range []struct{ name, klash, crush string }{
		{"wordmark start", *hex(k.Logo.TitleColorA), *hex(c.Logo.TitleColorA)},
		{"wordmark end", *hex(k.Logo.TitleColorB), *hex(c.Logo.TitleColorB)},
		{"fields", *hex(k.Logo.FieldColor), *hex(c.Logo.FieldColor)},
		{"background", *hex(k.Background), *hex(c.Background)},
		{"dialog border", *hex(k.Dialog.View.GetBorderTopForeground()), *hex(c.Dialog.View.GetBorderTopForeground())},
	} {
		if p.klash != p.crush {
			t.Errorf("%s is %s, want Charmtone's %s", p.name, p.klash, p.crush)
		}
	}
	if k.Logo.FieldColorB == nil || len(k.Logo.StarColors) == 0 {
		t.Error("the logo's fields should fade and carry stars")
	}
	if c.Logo.FieldColorB != nil || len(c.Logo.StarColors) != 0 {
		t.Error("Charmtone's own logo should stay as upstream draws it")
	}
}

func TestKlashCosmos_HintsReadable(t *testing.T) {
	// The green and gold sit on the background; they must stay legible.
	const minAA = 4.5
	bg := *hex(KlashCosmos().Background)
	for name, c := range map[string]string{"gold": *hex(KlashGold), "green": *hex(KlashGreen)} {
		if ratio := contrastRatio(c, bg); ratio < minAA {
			t.Errorf("%s %s on %s is %.2f:1, below WCAG AA (%.1f)", name, c, bg, ratio, minAA)
		}
	}
}

func must(s Styles, err error) Styles {
	if err != nil {
		panic(err)
	}
	return s
}
