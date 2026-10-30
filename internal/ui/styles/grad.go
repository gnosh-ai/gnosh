package styles

import (
	"fmt"
	"image/color"
	"strings"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/crush/internal/ui/fade"
	"github.com/rivo/uniseg"
)

// ForegroundGrad returns a slice of strings representing the input string
// rendered with a horizontal gradient foreground from color1 to color2. Each
// string in the returned slice corresponds to a grapheme cluster in the input
// string. If bold is true, the rendered strings will be bolded.
func ForegroundGrad(base lipgloss.Style, input string, bold bool, color1, color2 color.Color) []string {
	if input == "" {
		return []string{""}
	}
	if len(input) == 1 {
		style := base.Foreground(color1)
		if bold {
			style.Bold(true)
		}
		return []string{style.Render(input)}
	}
	var clusters []string
	gr := uniseg.NewGraphemes(input)
	for gr.Next() {
		clusters = append(clusters, string(gr.Runes()))
	}

	ramp := lipgloss.Blend1D(len(clusters), fade.Stops(color1, color2)...)
	for i, c := range ramp {
		style := base.Foreground(c)
		if bold {
			style.Bold(true)
		}
		clusters[i] = style.Render(clusters[i])
	}
	return clusters
}

// ApplyForegroundGrad renders a given string with a horizontal gradient
// foreground.
func ApplyForegroundGrad(base lipgloss.Style, input string, color1, color2 color.Color) string {
	if input == "" {
		return ""
	}
	var o strings.Builder
	clusters := ForegroundGrad(base, input, false, color1, color2)
	for _, c := range clusters {
		fmt.Fprint(&o, c)
	}
	return o.String()
}

// ApplyBoldWordmark renders the name in bold: with gno set, its first three
// letters, Gno, in that one color and the rest in a fade from color1 to
// color2; with gno nil, all of it in the fade.
func ApplyBoldWordmark(base lipgloss.Style, name string, gno, color1, color2 color.Color) string {
	if gno == nil || len(name) <= 3 {
		return ApplyBoldForegroundGrad(base, name, color1, color2)
	}
	return base.Bold(true).Foreground(gno).Render(name[:3]) + ApplyBoldForegroundGrad(base, name[3:], color1, color2)
}

// ApplyBoldForegroundGrad renders a given string with a horizontal gradient
// foreground.
func ApplyBoldForegroundGrad(base lipgloss.Style, input string, color1, color2 color.Color) string {
	if input == "" {
		return ""
	}
	var o strings.Builder
	clusters := ForegroundGrad(base, input, true, color1, color2)
	for _, c := range clusters {
		fmt.Fprint(&o, c)
	}
	return o.String()
}
