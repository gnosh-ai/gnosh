package fade

import (
	"image/color"
	"testing"
)

var (
	green  = color.RGBA{G: 0xff, A: 0xff}
	purple = color.RGBA{R: 0x6b, G: 0x50, B: 0xff, A: 0xff}
	pink   = color.RGBA{R: 0xff, G: 0x60, B: 0xff, A: 0xff}
	gold   = color.RGBA{R: 0xfb, G: 0xd2, B: 0x37, A: 0xff}
)

func TestStops(t *testing.T) {
	end := &Via{Color: gold, Through: []color.Color{purple, pink}}
	for _, tc := range []struct {
		name  string
		stops []color.Color
		want  []color.Color
	}{
		{"plain colors pass as they are", []color.Color{green, gold}, []color.Color{green, gold}},
		{"arriving passes through in order", []color.Color{green, end}, []color.Color{green, purple, pink, gold}},
		{"leaving passes through in reverse", []color.Color{end, green}, []color.Color{gold, pink, purple, green}},
		{"in the middle, both ways", []color.Color{green, end, green}, []color.Color{green, purple, pink, gold, pink, purple, green}},
		{"alone, just its color", []color.Color{end}, []color.Color{gold}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := Stops(tc.stops...)
			if len(got) != len(tc.want) {
				t.Fatalf("got %d stops %v, want %d %v", len(got), got, len(tc.want), tc.want)
			}
			for i := range got {
				if !same(got[i], tc.want[i]) {
					t.Errorf("stop %d is %v, want %v", i, got[i], tc.want[i])
				}
			}
		})
	}
}

func TestViaIsItsColor(t *testing.T) {
	v := &Via{Color: gold, Through: []color.Color{purple}}
	if !same(v, gold) {
		t.Errorf("a *Via reads as %v, want its own color %v", v, gold)
	}
	// Comparing colors must not panic, as it would for a Via held by value.
	var a, b color.Color = v, &Via{Color: gold}
	if a == b {
		t.Error("two different *Via compared equal")
	}
}

func same(a, b color.Color) bool {
	r1, g1, b1, a1 := a.RGBA()
	r2, g2, b2, a2 := b.RGBA()
	return r1 == r2 && g1 == g2 && b1 == b2 && a1 == a2
}
