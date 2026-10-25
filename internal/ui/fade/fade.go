// Package fade lets a color carry the colors a fade passes through on its
// way to it, so a theme can make every two-color fade in the UI a longer
// one without each place that draws a fade knowing about it.
package fade

import "image/color"

// Via is a color that brings company: a fade toward it passes through
// Through first, in order, and a fade away from it passes through them in
// reverse. Anywhere else it is just its Color.
//
// Use a *Via, so the color stays comparable.
type Via struct {
	color.Color
	Through []color.Color
}

// Stops returns the stops of a fade through the given colors, with every
// *Via's company put in: before it when a fade arrives at it, and after it,
// reversed, when a fade leaves it. Colors that are not a *Via pass as they
// are.
func Stops(stops ...color.Color) []color.Color {
	out := make([]color.Color, 0, len(stops))
	for i, c := range stops {
		v, ok := c.(*Via)
		if !ok || v == nil {
			out = append(out, c)
			continue
		}
		if i > 0 {
			out = append(out, v.Through...)
		}
		out = append(out, v.Color)
		if i < len(stops)-1 {
			for j := len(v.Through) - 1; j >= 0; j-- {
				out = append(out, v.Through[j])
			}
		}
	}
	return out
}
