package logo

import (
	"image/color"
	"strings"
	"testing"

	"charm.land/lipgloss/v2"
)

func TestStarsOnlyInTheWideLogo(t *testing.T) {
	gold := color.RGBA{R: 0xfb, G: 0xd2, B: 0x37, A: 0xff}
	o := Opts{
		FieldColor:  color.RGBA{R: 0x6b, G: 0x50, B: 0xff, A: 0xff},
		FieldColorB: color.RGBA{R: 0xff, G: 0x60, B: 0xff, A: 0xff},
		StarColors:  []color.Color{gold},
		Width:       200,
	}
	if wide := Render(lipgloss.NewStyle(), "v1", false, o); !strings.Contains(wide, star) {
		t.Error("the wide logo has no stars among its fields")
	}
	if narrow := Render(lipgloss.NewStyle(), "v1", true, o); strings.Contains(narrow, star) {
		t.Error("the narrow logo has stars; they only clutter it")
	}
}
