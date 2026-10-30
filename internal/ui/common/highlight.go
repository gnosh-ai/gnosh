package common

import (
	"bytes"
	"image/color"

	"github.com/alecthomas/chroma/v2"
	"github.com/alecthomas/chroma/v2/formatters"
	"github.com/alecthomas/chroma/v2/lexers"
	"github.com/charmbracelet/crush/internal/ui/styles"
	"github.com/charmbracelet/crush/internal/ui/xchroma"
)

// SyntaxHighlight applies syntax highlighting to the given source code based
// on the file name and background color. It returns the highlighted code as a
// string.
func SyntaxHighlight(st *styles.Styles, source, fileName string, bg color.Color) (string, error) {
	// Determine the language lexer to use. The filename match is memoized
	// (and already coalesced) since it is expensive and stable per name.
	l := xchroma.MatchLexer(fileName)
	if l == nil {
		l = lexers.Analyse(source)
	}
	if l == nil {
		l = lexers.Fallback
	}
	return syntaxHighlight(st, source, l, bg)
}

// SyntaxHighlightLexerName applies syntax highlighting using the lexer
// registered under the given language name. It falls back to the fallback
// lexer when the name is unknown, so callers can safely request languages
// like "bash" without checking for availability.
func SyntaxHighlightLexerName(st *styles.Styles, source, lexerName string, bg color.Color) (string, error) {
	l := lexers.Get(lexerName)
	if l == nil {
		l = lexers.Fallback
	}
	return syntaxHighlight(st, source, l, bg)
}

// SyntaxHighlightMuted is SyntaxHighlightLexerName in MutedChromaStyle's
// colours, taken halfway toward toward: for code shown inside a line of
// text rather than as a block of its own.
func SyntaxHighlightMuted(st *styles.Styles, source, lexerName string, toward color.Color) (string, error) {
	l := lexers.Get(lexerName)
	if l == nil {
		l = lexers.Fallback
	}
	return formatChroma(source, l, MutedChromaStyle(st, toward))
}

// syntaxHighlight formats the given source using the provided lexer and the
// memoized Chroma style for the theme and background.
func syntaxHighlight(st *styles.Styles, source string, l chroma.Lexer, bg color.Color) (string, error) {
	// Memoized: building the style per call is expensive and only depends
	// on the theme and background.
	return formatChroma(source, l, ChromaStyle(st, bg))
}

// formatChroma formats source, tokenised by l, in style s, as 24-bit color.
func formatChroma(source string, l chroma.Lexer, s *chroma.Style) (string, error) {
	f := formatters.Get("terminal16m")
	if f == nil {
		f = formatters.Fallback
	}

	// Tokenize and format
	it, err := l.Tokenise(nil, source)
	if err != nil {
		return "", err
	}

	var buf bytes.Buffer
	err = f.Format(&buf, s, it)
	return buf.String(), err
}
