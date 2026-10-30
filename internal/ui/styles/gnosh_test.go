package styles

import (
	"fmt"
	"image/color"
	"testing"

	"github.com/charmbracelet/crush/internal/ui/fade"
)

func TestGnoshCosmosIsTheDefault(t *testing.T) {
	if !IsBuiltinTheme(DefaultThemeName) {
		t.Fatalf("%q is not a built-in theme", DefaultThemeName)
	}
	for name, s := range map[string]Styles{
		"an empty theme name": must(LoadTheme("")),
		"an unknown theme":    ThemeFromConfig("no-such-theme"),
	} {
		if got, want := *hex(s.Logo.CharmColor), *hex(GnoshGold); got != want {
			t.Errorf("%s gave a mark in %s, want Gnosh's gold %s", name, got, want)
		}
	}
}

func TestGnoshCosmos_KeepsCrushsColors(t *testing.T) {
	// Gnosh adds green and gold to Charmtone; it doesn't recolor it.
	k, c := GnoshCosmos(), CharmtonePantera()
	for _, p := range []struct{ name, gnosh, crush string }{
		{"fields", *hex(k.Logo.FieldColor), *hex(c.Logo.FieldColor)},
		{"background", *hex(k.Background), *hex(c.Background)},
		{"dialog border", *hex(k.Dialog.View.GetBorderTopForeground()), *hex(c.Dialog.View.GetBorderTopForeground())},
		{"selected item", *hex(k.Dialog.SelectedItem.GetBackground()), *hex(c.Dialog.SelectedItem.GetBackground())},
	} {
		if p.gnosh != p.crush {
			t.Errorf("%s is %s, want Charmtone's %s", p.name, p.gnosh, p.crush)
		}
	}
	if k.Logo.FieldColorB == nil {
		t.Error("the logo's fields should fade")
	}
	if c.Logo.FieldColorB != nil {
		t.Error("Charmtone's own logo should stay as upstream draws it")
	}
}

func TestGnoshCosmos_FadesRunThroughTheNebula(t *testing.T) {
	// Every fade starts green and ends gold, with Crush's purple and hot
	// pink between them.
	want := []string{*hex(GnoshGreen), *hex(GnoshPurple), *hex(GnoshPink), *hex(GnoshGold)}
	s := GnoshCosmos()
	for name, ends := range map[string][2]color.Color{
		"wordmark":       {s.Logo.TitleColorA, s.Logo.TitleColorB},
		"small wordmark": {s.Logo.SmallGradFromColor, s.Logo.SmallGradToColor},
		"header":         {s.Header.LogoGradFromColor, s.Header.LogoGradToColor},
		"dialog title":   {s.Dialog.TitleGradFromColor, s.Dialog.TitleGradToColor},
		"working":        {s.WorkingGradFromColor, s.WorkingGradToColor},
	} {
		stops := fade.Stops(ends[0], ends[1])
		got := make([]string, len(stops))
		for i, c := range stops {
			got[i] = *hex(c)
		}
		if fmt.Sprint(got) != fmt.Sprint(want) {
			t.Errorf("the %s fades through %v, want %v", name, got, want)
		}
	}
}

func TestGnoshCosmos_HintsReadable(t *testing.T) {
	// The green and gold sit on the background; they must stay legible.
	const minAA = 4.5
	bg := *hex(GnoshCosmos().Background)
	for name, c := range map[string]string{"gold": *hex(GnoshGold), "green": *hex(GnoshGreen)} {
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
