package common

import (
	"image/color"
	"math"
	"sync"

	"charm.land/lipgloss/v2"
	"github.com/alecthomas/chroma/v2"
	chromastyles "github.com/alecthomas/chroma/v2/styles"
	"github.com/charmbracelet/crush/internal/ui/styles"
)

// Building a chroma style from a theme (chroma.MustNewStyle) parses every
// token rule and resolves inheritance, which is expensive. Syntax
// highlighting and diff formatting both did it on every call, so a chat
// full of code views and diffs rebuilt the same style hundreds of times
// per frame on resize. The result depends only on the theme (and, for
// highlighting, an optional background override), so we memoize it.
var (
	chromaStyleMu   sync.Mutex
	chromaStyleFor  *styles.Styles
	chromaStyleBase *chroma.Style
	chromaStyleByBg map[[3]uint8]*chroma.Style
	// chromaStyleMuted is MutedChromaStyle's, for the theme and the colour
	// it mixes in.
	chromaStyleMuted    *chroma.Style
	chromaStyleMutedFor *styles.Styles
	chromaStyleMutedTo  [3]uint8
)

// InvalidateChromaStyleCache drops the memoized chroma style so the next
// call to ChromaStyle rebuilds it from the current theme. Call it when the
// active theme is mutated in place (the *styles.Styles pointer is reused),
// which the pointer-keyed memoization cannot otherwise detect.
func InvalidateChromaStyleCache() {
	chromaStyleMu.Lock()
	defer chromaStyleMu.Unlock()
	chromaStyleFor = nil
	chromaStyleBase = nil
	chromaStyleByBg = nil
	chromaStyleMuted = nil
}

// InvalidateStyleCaches drops every style-derived cache that the pointer
// identity of the active *styles.Styles cannot invalidate on its own when
// the theme is swapped in place. Call it after applying a new theme.
func InvalidateStyleCaches() {
	InvalidateMarkdownRendererCache()
	InvalidateChromaStyleCache()
}

// ChromaStyle returns the chroma style for the given theme, memoized. When
// bg is non-nil the style's background is overridden with it (also
// memoized per color). The cache resets whenever the active theme changes.
func ChromaStyle(st *styles.Styles, bg color.Color) *chroma.Style {
	chromaStyleMu.Lock()
	defer chromaStyleMu.Unlock()

	if chromaStyleFor != st {
		chromaStyleFor = st
		chromaStyleBase = nil
		chromaStyleByBg = nil
	}
	if chromaStyleBase == nil {
		chromaStyleBase = chroma.MustNewStyle("crush", st.ChromaTheme())
	}
	if bg == nil {
		return chromaStyleBase
	}

	r, g, b, _ := bg.RGBA()
	key := [3]uint8{uint8(r >> 8), uint8(g >> 8), uint8(b >> 8)}
	if s, ok := chromaStyleByBg[key]; ok {
		return s
	}

	built, err := chromaStyleBase.Builder().Transform(
		func(t chroma.StyleEntry) chroma.StyleEntry {
			t.Background = chroma.NewColour(key[0], key[1], key[2])
			return t
		},
	).Build()
	if err != nil {
		built = chromastyles.Fallback
	}
	if chromaStyleByBg == nil {
		chromaStyleByBg = map[[3]uint8]*chroma.Style{}
	}
	chromaStyleByBg[key] = built
	return built
}

// mutedMix is how far MutedChromaStyle takes each colour toward the muted
// one.
const mutedMix = 0.5

// MutedChromaStyle is ChromaStyle with every colour taken mutedMix of the
// way toward toward, memoized: code set inside a line of text, such as the
// Gno a gnovm_run call runs in its header, reads as code without outshining
// the line. It sets no background. A toward with no colour leaves the style
// as it is.
func MutedChromaStyle(st *styles.Styles, toward color.Color) *chroma.Style {
	base := ChromaStyle(st, nil)
	if _, none := toward.(lipgloss.NoColor); none || toward == nil {
		return base
	}
	r, g, b, _ := toward.RGBA()
	to := [3]uint8{uint8(r >> 8), uint8(g >> 8), uint8(b >> 8)}

	chromaStyleMu.Lock()
	defer chromaStyleMu.Unlock()
	if chromaStyleMuted != nil && chromaStyleMutedFor == st && chromaStyleMutedTo == to {
		return chromaStyleMuted
	}
	mix := func(x, t uint8) uint8 {
		return uint8(math.Round(float64(x)*(1-mutedMix) + float64(t)*mutedMix))
	}
	built, err := base.Builder().Transform(
		func(e chroma.StyleEntry) chroma.StyleEntry {
			// Transform hands each entry the Background entry's colour
			// too, which would then stand behind every token: code in a
			// line of text has none.
			e.Background = 0
			if e.Colour.IsSet() {
				e.Colour = chroma.NewColour(mix(e.Colour.Red(), to[0]), mix(e.Colour.Green(), to[1]), mix(e.Colour.Blue(), to[2]))
			}
			return e
		},
	).Build()
	if err != nil {
		return base
	}
	chromaStyleMuted, chromaStyleMutedFor, chromaStyleMutedTo = built, st, to
	return built
}
