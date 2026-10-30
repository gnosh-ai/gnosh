// Package logo renders the Gnosh wordmark in a stylized way.
package logo

import (
	"fmt"
	"image/color"
	"strings"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/crush/internal/ui/styles"
	"github.com/charmbracelet/crush/internal/version"
	"github.com/charmbracelet/x/ansi"
)

// letterform represents a letterform. It can be stretched horizontally by
// a given amount via the boolean argument.
type letterform func(bool) string

const diag = `╱`

// Mark is the maker's mark printed above the wordmark, where upstream Crush
// prints Charm™.
const Mark = "gnosh.ai"

// Opts are the options for rendering the Gnosh title art.
type Opts struct {
	FieldColor   color.Color // diagonal lines
	FieldColorB  color.Color // when set, the diagonal lines fade from FieldColor to it
	TitleColorA  color.Color // left gradient ramp point
	TitleColorB  color.Color // right gradient ramp point
	CharmColor   color.Color // maker's mark color
	VersionColor color.Color // version text color
	Width        int         // width of the rendered logo, used for truncation
	Hyper        bool        // whether it is Crush or Hypercrush
}

// Render renders the Crush logo. Set the argument to true to render the narrow
// version, intended for use in a sidebar.
//
// The compact argument determines whether it renders compact for the sidebar
// or wider for the main pane.
func Render(base lipgloss.Style, version string, compact bool, o Opts) string {
	charm := Mark
	if !o.Hyper {
		charm = " " + charm
	}

	fg := func(c color.Color, s string) string {
		return lipgloss.NewStyle().Foreground(c).Render(s)
	}

	// Title.
	const spacing = 1
	var hyperLetterforms []letterform
	if o.Hyper {
		hyperLetterforms = []letterform{
			LetterH,
			LetterYAlt,
			LetterP,
			LetterE,
			LetterR,
		}
	}
	titleLetterforms := []letterform{
		LetterG,
		LetterN,
		LetterO,
		LetterSAlt,
		LetterH,
	}
	if o.Hyper && !compact {
		titleLetterforms = append(hyperLetterforms, titleLetterforms...)
	}

	// No letter is stretched: upstream widened one at random, which made the
	// word look different from one start to the next, and the GitHub banner.
	const stretchIndex = -1
	title := renderWord(spacing, stretchIndex, titleLetterforms...)
	if o.Hyper && compact {
		title = renderWord(spacing, stretchIndex, hyperLetterforms...) + "\n" + title
	}
	titleWidth := lipgloss.Width(title)
	b := new(strings.Builder)
	for r := range strings.SplitSeq(title, "\n") {
		fmt.Fprintln(b, styles.ApplyForegroundGrad(base, r, o.TitleColorA, o.TitleColorB))
	}
	title = b.String()

	// Charm and version.
	metaRowGap := 1
	maxVersionWidth := titleWidth - lipgloss.Width(charm) - metaRowGap
	version = ansi.Truncate(version, maxVersionWidth, "…") // truncate version if too long.
	if o.Hyper && compact {
		version += " "
	}
	gap := max(0, titleWidth-lipgloss.Width(charm)-lipgloss.Width(version))
	metaRow := fg(o.CharmColor, charm) + strings.Repeat(" ", gap) + fg(o.VersionColor, version)

	// The meta row goes under the word.
	title = strings.TrimSuffix(title, "\n") + "\n" + metaRow

	// Narrow version. If this is Hypercrush, this is also a stacked version.
	// The fields run the other way to the title above it and the same way
	// below, so the stack shimmers.
	if compact {
		return strings.Join([]string{
			field(base, o, titleWidth, true),
			field(base, o, titleWidth, false),
			title,
			field(base, o, titleWidth, true),
			"",
		}, "\n")
	}

	fieldHeight := lipgloss.Height(title)

	// Left field.
	const leftWidth = 6
	leftField := new(strings.Builder)
	for range fieldHeight {
		fmt.Fprintln(leftField, field(base, o, leftWidth, false))
	}

	// Right field.
	rightWidth := max(15, o.Width-titleWidth-leftWidth-2) // 2 for the gap.
	const stepDownAt = 0
	rightField := new(strings.Builder)
	for i := range fieldHeight {
		width := rightWidth
		if i >= stepDownAt {
			width = rightWidth - (i - stepDownAt)
		}
		fmt.Fprint(rightField, field(base, o, width, false), "\n")
	}

	// Return the wide version.
	const hGap = " "
	logo := lipgloss.JoinHorizontal(lipgloss.Top, leftField.String(), hGap, title, hGap, rightField.String())
	if o.Width > 0 {
		// Truncate the logo to the specified width.
		lines := strings.Split(logo, "\n")
		for i, line := range lines {
			lines[i] = ansi.Truncate(line, o.Width, "")
		}
		logo = strings.Join(lines, "\n")
	}
	return logo
}

// field draws one row of diagonal lines, width cells long. With
// o.FieldColorB set the row fades from o.FieldColor to it, or the other way
// when reverse is set.
func field(base lipgloss.Style, o Opts, width int, reverse bool) string {
	lines := strings.Repeat(diag, max(width, 0))
	if o.FieldColorB == nil {
		return lipgloss.NewStyle().Foreground(o.FieldColor).Render(lines)
	}
	from, to := o.FieldColor, o.FieldColorB
	if reverse {
		from, to = to, from
	}
	return styles.ApplyForegroundGrad(base, lines, from, to)
}

// SmallRender renders a smaller version of the Crush logo, suitable for
// smaller windows or sidebar usage.
func SmallRender(t *styles.Styles, width int, o Opts) string {
	name := version.Name
	if o.Hyper {
		name = "HYPERCRUSH"
	}
	title := styles.ApplyBoldForegroundGrad(t.Logo.GradCanvas, name, t.Logo.SmallGradFromColor, t.Logo.SmallGradToColor)
	remainingWidth := width - lipgloss.Width(title) - 1 // 1 for the space after the name
	if remainingWidth > 0 {
		lines := strings.Repeat("╱", remainingWidth)
		title = fmt.Sprintf("%s %s", title, t.Logo.SmallDiagonals.Render(lines))
	}
	return title
}
