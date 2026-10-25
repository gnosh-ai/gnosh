package tools

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf16"
	"unicode/utf8"
)

// When old_string does not appear in a file as written, the edit tools
// look for it as Claude Code's Edit does: with curly quotes taken for
// straight ones, or with \uXXXX escapes taken for the characters they
// stand for, either way round. new_string then goes in in the file's
// style: its quotes curled as the file's are, its characters escaped or
// unescaped as the text it replaces was.

const (
	leftSingleQuote  = '\u2018'
	rightSingleQuote = '\u2019'
	leftDoubleQuote  = '\u201C'
	rightDoubleQuote = '\u201D'
)

// unicodeEscapeRE is a \uXXXX escape.
var unicodeEscapeRE = regexp.MustCompile(`\\u[0-9a-fA-F]{4}`)

// actualOldString is the text in content that old stands for, when old
// does not appear as written: the first place where it does with curly
// quotes taken for straight ones; else old with its \uXXXX escapes made
// the characters they stand for; else old with its characters beyond
// ASCII written as such escapes (see escapedMatch). "" when there is
// none.
func actualOldString(content, old string) string {
	if old == "" {
		return ""
	}
	normContent := straightQuotes(content)
	if i := strings.Index(normContent, straightQuotes(old)); i >= 0 {
		// Straightening keeps one rune for each, so the place, counted
		// in runes, is the same in content.
		start := runeOffset(content, utf8.RuneCountInString(normContent[:i]))
		return content[start : start+runeOffset(content[start:], utf8.RuneCountInString(old))]
	}
	if unicodeEscapeRE.MatchString(old) {
		if decoded := unescapeUnicode(old); decoded != old && strings.Contains(content, decoded) {
			return decoded
		}
	}
	if !isASCII(old) {
		if escapedLen(old) > len(content) || !strings.Contains(content, `\u`) {
			return ""
		}
		return escapedMatch(content, old)
	}
	return ""
}

// styledNewString is new as it goes into the file in place of actual, the
// text old was found as: with its quotes curled as actual's are, then
// with its characters escaped or unescaped as actual's are.
func styledNewString(old, actual, new string) string {
	return escapeStyled(old, actual, quoteStyled(old, actual, new))
}

// straightQuotes is s with its curly quotes straightened, rune for rune.
func straightQuotes(s string) string {
	return strings.Map(func(r rune) rune {
		switch r {
		case leftSingleQuote, rightSingleQuote:
			return '\''
		case leftDoubleQuote, rightDoubleQuote:
			return '"'
		}
		return r
	}, s)
}

// quoteStyled is new with its straight quotes curled where actual has
// curly ones of the kind: an opening quote at the start or after a
// space, a bracket or a dash, a closing one elsewhere, so a single quote
// between two letters is an apostrophe.
func quoteStyled(old, actual, new string) string {
	if old == actual {
		return new
	}
	double := strings.ContainsRune(actual, leftDoubleQuote) || strings.ContainsRune(actual, rightDoubleQuote)
	single := strings.ContainsRune(actual, leftSingleQuote) || strings.ContainsRune(actual, rightSingleQuote)
	if !double && !single {
		return new
	}
	runes := []rune(new)
	curl := func(i int, opening, closing rune) rune {
		if i == 0 || strings.ContainsRune(" \t\n\r([{\u2014\u2013", runes[i-1]) {
			return opening
		}
		return closing
	}
	for i, r := range runes {
		switch {
		case r == '"' && double:
			runes[i] = curl(i, leftDoubleQuote, rightDoubleQuote)
		case r == '\'' && single:
			runes[i] = curl(i, leftSingleQuote, rightSingleQuote)
		}
	}
	return string(runes)
}

// escapeStyled is new written as actual writes old: when actual is old
// with its characters beyond ASCII as \uXXXX escapes, new's are escaped
// too, those old has with the hex digits actual gives them and others in
// the case most of actual's digits are in; when actual is old with its
// escapes unescaped, new's are unescaped.
func escapeStyled(old, actual, new string) string {
	if old == actual {
		return new
	}
	if !isASCII(old) && escapedLen(old) == len(actual) && escapedMatch(actual, old) != "" {
		digits := map[uint16]string{}
		upper, lower := 0, 0
		at := 0
		for _, r := range old {
			if r < utf8.RuneSelf {
				at++
				continue
			}
			for _, unit := range utf16Units(r) {
				d := actual[at+2 : at+6]
				digits[unit] = d
				for _, c := range d {
					switch {
					case c >= 'a' && c <= 'f':
						lower++
					case c >= 'A' && c <= 'F':
						upper++
					}
				}
				at += 6
			}
		}
		var b strings.Builder
		for _, r := range new {
			if r < utf8.RuneSelf {
				b.WriteRune(r)
				continue
			}
			for _, unit := range utf16Units(r) {
				d, ok := digits[unit]
				if !ok {
					d = fmt.Sprintf("%04x", unit)
					if upper > lower {
						d = strings.ToUpper(d)
					}
				}
				b.WriteString(`\u` + d)
			}
		}
		return b.String()
	}
	if unicodeEscapeRE.MatchString(old) && unescapeUnicode(old) == actual {
		return unescapeUnicode(new)
	}
	return new
}

// escapedMatch is the text in content that is old with its characters
// beyond ASCII written as \uXXXX escapes, in either case of hex digit, a
// character beyond the Basic Multilingual Plane as a UTF-16 surrogate
// pair: at the first place where no backslash escapes one of those
// escapes, and only if the same text is not found earlier, where an edit
// would put it. "" when there is none, or when old ends in an escaping
// backslash.
func escapedMatch(content, old string) string {
	if backslashesBefore(old, len(old))%2 == 1 {
		return ""
	}
	var pattern strings.Builder
	var escapes []int // where an escape starts, in the escaped text
	if strings.HasPrefix(old, `\`) {
		escapes = append(escapes, 0)
	}
	at := 0
	for _, r := range old {
		if r < utf8.RuneSelf {
			pattern.WriteString(regexp.QuoteMeta(string(r)))
			at++
			continue
		}
		for _, unit := range utf16Units(r) {
			escapes = append(escapes, at)
			pattern.WriteString(`\\u`)
			for _, d := range fmt.Sprintf("%04x", unit) {
				if d >= 'a' {
					pattern.WriteString("[" + string(d) + strings.ToUpper(string(d)) + "]")
				} else {
					pattern.WriteRune(d)
				}
			}
			at += 6
		}
	}
	re, err := regexp.Compile(pattern.String())
	if err != nil {
		return ""
	}
	for from := 0; from <= len(content); {
		loc := re.FindStringIndex(content[from:])
		if loc == nil {
			return ""
		}
		start, end := from+loc[0], from+loc[1]
		unescaped := true
		for _, a := range escapes {
			if backslashesBefore(content, start+a)%2 != 0 {
				unescaped = false
				break
			}
		}
		if unescaped {
			if match := content[start:end]; strings.Index(content, match) == start {
				return match
			}
			return ""
		}
		from = start + 1
	}
	return ""
}

// unescapeUnicode is s with its \uXXXX escapes made the characters they
// stand for, a surrogate pair one character, and each escaped backslash
// (\\) left as it is.
func unescapeUnicode(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); {
		if strings.HasPrefix(s[i:], `\\`) {
			b.WriteString(`\\`)
			i += 2
			continue
		}
		var units []uint16
		for i+6 <= len(s) && unicodeEscapeRE.MatchString(s[i:i+6]) && s[i] == '\\' {
			n, _ := strconv.ParseUint(s[i+2:i+6], 16, 16)
			units = append(units, uint16(n))
			i += 6
		}
		if len(units) > 0 {
			b.WriteString(string(utf16.Decode(units)))
			continue
		}
		b.WriteByte(s[i])
		i++
	}
	return b.String()
}

// escapedLen is the length of s with its characters beyond ASCII written
// as \uXXXX escapes.
func escapedLen(s string) int {
	n := 0
	for _, r := range s {
		if r < utf8.RuneSelf {
			n++
		} else {
			n += 6 * len(utf16Units(r))
		}
	}
	return n
}

// utf16Units are r's UTF-16 code units: one, or a surrogate pair.
func utf16Units(r rune) []uint16 {
	if r1, r2 := utf16.EncodeRune(r); r1 != unicode.ReplacementChar {
		return []uint16{uint16(r1), uint16(r2)}
	}
	return []uint16{uint16(r)}
}

// backslashesBefore is how many backslashes come right before s[n].
func backslashesBefore(s string, n int) int {
	count := 0
	for n-1-count >= 0 && s[n-1-count] == '\\' {
		count++
	}
	return count
}

// runeOffset is the byte offset of s's nth rune.
func runeOffset(s string, n int) int {
	offset := 0
	for ; n > 0 && offset < len(s); n-- {
		_, size := utf8.DecodeRuneInString(s[offset:])
		offset += size
	}
	return offset
}

// isASCII reports whether s is all ASCII.
func isASCII(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] >= utf8.RuneSelf {
			return false
		}
	}
	return true
}
