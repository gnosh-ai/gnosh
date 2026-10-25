// Package logo renders the Klash wordmark in a stylized way.
package logo

import (
	"fmt"
	"image/color"
	"math/rand/v2"
	"strings"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/crush/internal/ui/fade"
	"github.com/charmbracelet/crush/internal/ui/styles"
	"github.com/charmbracelet/crush/internal/version"
	"github.com/charmbracelet/x/ansi"
)

// letterform represents a letterform. It can be stretched horizontally by
// a given amount via the boolean argument.
type letterform func(bool) string

const (
	diag = `╱`
	star = `✦`
)

// Mark is the maker's mark printed above the wordmark, where upstream Crush
// prints Charm™. SmallMark stands beside the name where there is no room
// for it.
const (
	Mark      = "klash.pro"
	SmallMark = "✦"
)

// Opts are the options for rendering the Klash title art.
type Opts struct {
	FieldColor   color.Color   // diagonal lines
	FieldColorB  color.Color   // when set, the diagonal lines fade from FieldColor to it
	StarColors   []color.Color // when set, a few of the diagonal lines are stars, in these colors
	TitleColorA  color.Color   // left gradient ramp point
	TitleColorB  color.Color   // right gradient ramp point
	CharmColor   color.Color   // maker's mark color
	VersionColor color.Color   // version text color
	Width        int           // width of the rendered logo, used for truncation
	Hyper        bool          // whether it is Crush or Hypercrush

	// When true, stretch a random letterform on each render. Has no effect in
	// compact mode. Mainly for testing. In production you will want to cache
	// the stretched letterform to keep the logo from jittering on resize.
	Unstable bool
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
		LetterK,
		LetterL,
		LetterA,
		LetterSAlt,
		LetterH,
	}
	if o.Hyper && !compact {
		titleLetterforms = append(hyperLetterforms, titleLetterforms...)
	}

	stretchIndex := -1 // -1 means no stretching.
	if !compact && !o.Unstable {
		// Always stretch the same letterform, which is picked once at random.
		stretchIndex = cachedRandN(len(titleLetterforms))
	} else if !compact && o.Unstable {
		// Stretch a random letterform on every render.
		stretchIndex = rand.IntN(len(titleLetterforms))
	}
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

	// Join the meta row and big Crush title.
	title = strings.TrimSpace(metaRow + "\n" + title)

	// Narrow version. If this is Hypercrush, this is also a stacked version.
	// The fields run the other way to the title above it and the same way
	// below, so the stack shimmers.
	if compact {
		// The narrow logo is small enough that stars only clutter it.
		plain := o
		plain.StarColors = nil
		return strings.Join([]string{
			field(base, plain, 0, 0, titleWidth, true),
			field(base, plain, 1, 0, titleWidth, false),
			title,
			field(base, plain, 2, 0, titleWidth, true),
			"",
		}, "\n")
	}

	fieldHeight := lipgloss.Height(title)

	// Left field.
	const leftWidth = 6
	leftField := new(strings.Builder)
	for i := range fieldHeight {
		fmt.Fprintln(leftField, field(base, o, i, 0, leftWidth, false))
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
		fmt.Fprint(rightField, field(base, o, i, leftWidth+titleWidth+2, width, false), "\n")
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

// field draws one row of diagonal lines, width cells long, at column col of
// row row of the logo. With o.FieldColorB set the row fades from
// o.FieldColor to it, or the other way when reverse is set. With
// o.StarColors set, about one cell in starEvery is a star instead of a line,
// never two side by side; which ones depends only on the cell's place, so the
// stars stay put from one render to the next.
func field(base lipgloss.Style, o Opts, row, col, width int, reverse bool) string {
	if width <= 0 {
		return ""
	}
	if o.FieldColorB == nil && len(o.StarColors) == 0 {
		return lipgloss.NewStyle().Foreground(o.FieldColor).Render(strings.Repeat(diag, width))
	}
	ramp := make([]color.Color, width)
	for i := range ramp {
		ramp[i] = o.FieldColor
	}
	if o.FieldColorB != nil && width > 1 {
		from, to := o.FieldColor, o.FieldColorB
		if reverse {
			from, to = to, from
		}
		ramp = lipgloss.Blend1D(width, fade.Stops(from, to)...)
	}
	var b strings.Builder
	prevStar := false
	for i, c := range ramp {
		glyph := diag
		if n := len(o.StarColors); n > 0 && !prevStar {
			if h := starHash(row, col+i); h%starEvery == 0 {
				glyph, c = star, o.StarColors[(h/starEvery)%n]
			}
		}
		prevStar = glyph == star
		b.WriteString(base.Foreground(c).Render(glyph))
	}
	return b.String()
}

// starEvery is how sparse the stars among the diagonal lines are.
const starEvery = 29

// starHash scatters the stars over the logo's fields.
func starHash(row, col int) int {
	h := uint32(row)*2654435761 ^ uint32(col)*2246822519
	h ^= h >> 15
	h *= 2654435761
	h ^= h >> 13
	return int(h % 1000003)
}

// SmallRender renders a smaller version of the Crush logo, suitable for
// smaller windows or sidebar usage.
func SmallRender(t *styles.Styles, width int, o Opts) string {
	name := version.Name
	if o.Hyper {
		name = "HYPERCRUSH"
	}
	title := t.Logo.SmallCharm.Render(SmallMark)
	title = fmt.Sprintf("%s %s", title, styles.ApplyBoldForegroundGrad(t.Logo.GradCanvas, name, t.Logo.SmallGradFromColor, t.Logo.SmallGradToColor))
	remainingWidth := width - lipgloss.Width(title) - 1 // 1 for the space after the name
	if remainingWidth > 0 {
		lines := strings.Repeat("╱", remainingWidth)
		title = fmt.Sprintf("%s %s", title, t.Logo.SmallDiagonals.Render(lines))
	}
	return title
}
