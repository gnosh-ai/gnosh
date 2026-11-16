package permission

import (
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"
)

// partKind says what one piece of a shell word is.
type partKind uint8

const (
	// partLit is text the shell passes on as written (after quote removal).
	partLit partKind = iota
	// partVar is a parameter expansion: $name or ${name...}.
	partVar
	// partSubst is a command substitution, a backtick command or a process
	// substitution: the shell runs something to get the text.
	partSubst
	// partArith is an arithmetic expansion: $((...)).
	partArith
)

// wordPart is one piece of a shell word.
type wordPart struct {
	kind partKind
	// lit is the text of a partLit.
	lit string
	// name is the variable a partVar expands.
	name string
	// guarded marks ${name:?} and ${name?}: the shell stops instead of
	// expanding an unset (or empty) variable to nothing.
	guarded bool
	// def is what ${name:-def} (and its kin -, :=, =) stands for when the
	// variable is not set, when that is plain text; hasDef says there is
	// such a text, which may be empty.
	def    string
	hasDef bool
	// sets marks ${name:=word} and ${name=word}: a variable with no
	// value is given the word, for the rest of the line.
	sets bool
	// op is the operator of a ${name<op>word}: one of :- - := = :? ? :+
	// + % %% # ##. It is empty for $name and ${name}.
	op string
	// alt is what ${name:+alt} and ${name+alt} stand for when the
	// variable is set, when that is plain text; hasAlt says there is
	// such a text.
	alt    string
	hasAlt bool
	// body is the word after the operator of the forms that stand for
	// it as it is: - and + and their kin.
	body *word
	// bare is set for a variable that stands outside double quotes. The
	// shell cuts what it stands for into words at its blanks, whatever is
	// quoted beside it in the same word: "mv ${X:-.git /tmp/g} /tmp" is mv
	// .git /tmp/g /tmp where X is not set, and with x holding ". y", "rm
	// -rf $x''" is rm -rf . y.
	bare bool
	// all marks ${name[@]} and ${name[*]}: every word of an array at
	// once, each a word of its own where the shell cuts words.
	all bool
	// pat is the pattern of ${name%pat} and its kin (% %% # ##) where it
	// is plain text (hasPat): the text cut off, if the value ends (or
	// begins) with it.
	pat    string
	hasPat bool
	// arg1 and arg2 are what the forms bash adds are given, where they
	// are plain text: the text to look for and the text to put in its
	// place (ops / and //), or where a piece starts and how long it is
	// (op :sub). See exoticForm.
	arg1, arg2 string
}

// of returns the texts the part may stand for when its variable holds
// val, and whether they are known. A form that cuts a piece off the
// value (${name%pattern}) leaves one of its beginnings, or of its ends:
// which one is the pattern's business, so all of them come back.
func (p wordPart) of(val, home string) ([]string, bool) {
	switch p.op {
	case "", "-", "=", "?", ":?":
		return []string{val}, true
	case ":-", ":=":
		if val != "" {
			return []string{val}, true
		}
		if !p.hasDef {
			return nil, false
		}
		return []string{p.defIn(home)}, true
	case ":+", "+":
		if p.op == ":+" && val == "" {
			return []string{""}, true
		}
		if !p.hasAlt {
			return nil, false
		}
		if home != "" && (p.alt == "~" || strings.HasPrefix(p.alt, "~/")) {
			return []string{home + p.alt[1:]}, true
		}
		return []string{p.alt}, true
	case "~":
		// A form that is not worked out: the value may come through it
		// as it is (and whatever else it may be, nobody knows).
		return []string{val}, true
	case "/", "//":
		// ${name/text/other}: the first of them, or all, put in other
		// words; a value without the text stays as it is.
		n := 1
		if p.op == "//" {
			n = -1
		}
		return []string{strings.Replace(val, p.arg1, p.arg2, n)}, true
	case "^^":
		return []string{strings.ToUpper(val)}, true
	case ",,":
		return []string{strings.ToLower(val)}, true
	case "^", ",":
		if val == "" {
			return []string{""}, true
		}
		first := strings.ToUpper(val[:1])
		if p.op == "," {
			first = strings.ToLower(val[:1])
		}
		return []string{first + val[1:]}, true
	case ":sub":
		// ${name:from:length}: a piece of the value. A from below zero
		// counts from the end, and so does a length below zero.
		from, _ := strconv.Atoi(p.arg1)
		if from < 0 {
			from += len(val)
		}
		if from < 0 || from > len(val) {
			return []string{""}, true
		}
		end := len(val)
		if p.arg2 != "" {
			n, _ := strconv.Atoi(p.arg2)
			if n < 0 {
				end += n
			} else if from+n < end {
				end = from + n
			}
		}
		if end < from {
			return []string{""}, true
		}
		return []string{val[from:end]}, true
	case "%", "%%", "#", "##":
		if p.hasPat {
			// Plain text to cut off: what the shell leaves is known. (A
			// pattern of plain text matches only itself, so the shortest
			// match and the longest are the same.)
			if p.op[0] == '%' {
				return []string{strings.TrimSuffix(val, p.pat)}, true
			}
			return []string{strings.TrimPrefix(val, p.pat)}, true
		}
		if len(val) > 64 {
			return nil, false
		}
		out := make([]string, 0, len(val)+1)
		for i := 0; i <= len(val); i++ {
			if p.op[0] == '%' {
				out = append(out, val[:i])
			} else {
				out = append(out, val[i:])
			}
		}
		return out, true
	}
	return nil, false
}

// plainly reports whether the part stands for its variable's own value
// whenever the variable has one that is not empty.
func (p wordPart) plainly() bool {
	switch p.op {
	case "", "-", "=", "?", ":?", ":-", ":=":
		return true
	}
	return false
}

// withBody returns the word with part i, a ${name:-word} or a
// ${name:+word}, replaced by the word after its operator: what the form
// stands for when the variable is not set, or when it is.
func withBody(w word, i int, home string) word {
	body := *w.parts[i].body
	parts := append([]wordPart(nil), w.parts[:i]...)
	if body.static() {
		text := body.joined()
		if home != "" && (text == "~" || strings.HasPrefix(text, "~/")) {
			text = home + text[1:]
		}
		parts = append(parts, wordPart{lit: text})
	} else {
		parts = append(parts, body.parts...)
	}
	out := w
	out.parts = append(parts, w.parts[i+1:]...)
	out.glob = w.glob || body.glob
	return out
}

// word is one word of a shell command, taken apart far enough to say what
// the shell would do with it.
type word struct {
	// raw is the word as written, with backslash-newline pairs removed.
	raw string
	// parts are the word's pieces in order.
	parts []wordPart
	// quoted is set when a part of the word was quoted or escaped, and
	// every variable and every command substitution in it stands inside
	// double quotes. A word with one of those outside the quotes is cut
	// into words, and matched against file names, like a word that
	// quotes nothing: the shell does that to what the expansion stands
	// for whatever is quoted beside it ("$x''", ""$x, $x\ y), so such a
	// word is read as not quoted. What is quoted of its own text is then
	// read as open to the shell as well, which is more to hold against
	// the rules and never less.
	quoted bool
	// glob is set when the word holds an unquoted *, ? or [.
	glob bool
	// brace is set when the word holds an unquoted { or } and is not the
	// literal "{}": the shell may turn it into several words.
	brace bool
	// tilde is set when the word starts with an unquoted ~.
	tilde bool
	// bareSubst is set when a command substitution stands in the word
	// outside quotes. What the command prints is cut into words at its
	// blanks, so the word may turn into several, with the text beside the
	// substitution on the first and the last of them: "-u $(echo X rm x)m"
	// is -u X rm xm.
	bareSubst bool
	// eqHead is set when the word starts with an unquoted =, which zsh
	// replaces by the path of the command named after it.
	eqHead bool
	// eqTilde is set when the word looks like name=value and an unquoted
	// ~ opens the value or follows a colon in it: bash expands that tilde
	// even when the word is an argument (dd if=~/x).
	eqTilde bool
	// anyOrder is set for a word that was filled in from every word of
	// an array (filledArgs). The words of an array are known as a set:
	// which of them stands last, and how often one stands there, is not
	// known, so a check that goes by an operand's place takes such a
	// word for any of the array's.
	anyOrder bool
}

// static reports whether the word's value is known without running the
// shell: no parameter, command or arithmetic expansion, and no tilde form
// other than ~ and ~/path.
func (w word) static() bool {
	for _, p := range w.parts {
		if p.kind != partLit {
			return false
		}
	}
	if w.tilde {
		l := w.joined()
		if l != "~" && !strings.HasPrefix(l, "~/") {
			return false
		}
	}
	if w.eqTilde {
		_, val, _ := strings.Cut(w.joined(), "=")
		for _, seg := range strings.Split(val, ":") {
			if strings.HasPrefix(seg, "~") && seg != "~" && !strings.HasPrefix(seg, "~/") {
				return false
			}
		}
	}
	return !w.eqHead
}

// counters reports whether the word is static text but for the shell's own
// counters, $? $$ $! and $#. Each stands for a number, or for nothing, and
// no line and no environment can make it stand for anything else, so the
// word stays one word with nothing in it for the shell to match against
// file names: to a command that only prints its arguments (echo "exit:
// $?") it is as harmless as its text.
func (w word) counters() bool {
	if w.glob || w.brace || w.tilde || w.eqHead || w.eqTilde || w.bareSubst {
		return false
	}
	for _, p := range w.parts {
		switch {
		case p.kind == partLit:
		case p.kind == partVar && len(p.name) == 1 && strings.Contains("?$!#", p.name) && p.op == "" && !p.all:
		default:
			return false
		}
	}
	return true
}

// literalParts reports whether every part of the word is text as written:
// no variable, command or arithmetic. What the shell may still do to it
// (zsh's =name, a ~ form, a pattern) is a path, a name or a list of file
// names, which to a command that only prints its arguments ("echo ===",
// which zsh would take for the path of ==) is no more than text. (A
// pattern is a listing, and is read as one: see opsWords.)
func (w word) literalParts() bool {
	for _, p := range w.parts {
		if p.kind != partLit {
			return false
		}
	}
	return !w.brace
}

// plain reports whether the word is static text with nothing for the shell
// to do: no quoting, no pattern, no braces, no tilde.
func (w word) plain() bool {
	return w.static() && !w.quoted && !w.glob && !w.brace && !w.tilde
}

// joined returns the word's literal pieces put together, leaving out
// whatever an expansion would add.
func (w word) joined() string {
	if len(w.parts) == 1 && w.parts[0].kind == partLit {
		return w.parts[0].lit
	}
	var b strings.Builder
	for _, p := range w.parts {
		if p.kind == partLit {
			b.WriteString(p.lit)
		}
	}
	return b.String()
}

// unset returns the word as it is when no variable in it is set and no
// command in it prints a thing: its text, with what each ${name:-text}
// stands for then.
func (w word) unset() string { return w.unsetIn("") }

// unsetIn is unset with the ~ that opens a ${name:-~/text} turned into
// the home directory, as the shell does (when home is known).
func (w word) unsetIn(home string) string {
	var b strings.Builder
	for _, p := range w.parts {
		switch {
		case p.kind == partLit:
			b.WriteString(p.lit)
		case p.kind == partVar && p.hasDef:
			b.WriteString(p.defIn(home))
		}
	}
	return b.String()
}

// defIn is what the part stands for when its variable is not set.
func (p wordPart) defIn(home string) string {
	if home != "" && (p.def == "~" || strings.HasPrefix(p.def, "~/")) {
		return home + p.def[1:]
	}
	return p.def
}

// lit returns the word's value when it is static, and its raw text when
// it is not. A leading ~ is left as it is; see value.
func (w word) lit() string {
	if w.static() {
		return w.joined()
	}
	return w.raw
}

// value returns the static word's value with a leading ~ replaced by
// home, and for a name=value word the tildes bash expands in the value.
func (w word) value(home string) string {
	l := w.joined()
	if w.tilde && home != "" {
		if l == "~" {
			return home
		}
		if strings.HasPrefix(l, "~/") {
			return home + l[1:]
		}
	}
	if w.eqTilde && home != "" {
		name, val, _ := strings.Cut(l, "=")
		segs := strings.Split(val, ":")
		for i, seg := range segs {
			if seg == "~" {
				segs[i] = home
			} else if strings.HasPrefix(seg, "~/") {
				segs[i] = home + seg[1:]
			}
		}
		return name + "=" + strings.Join(segs, ":")
	}
	return l
}

// hasSubst reports whether the word runs a command to get its text.
func (w word) hasSubst() bool {
	for _, p := range w.parts {
		if p.kind == partSubst {
			return true
		}
	}
	return false
}

// onlySubst reports whether the word is nothing but what commands print,
// quoted or not: a substitution, or one that stands in for a variable
// that is not set ("${X:-$(cmd)}").
func (w word) onlySubst() bool {
	n := 0
	for _, p := range w.parts {
		switch p.kind {
		case partSubst:
			n++
		case partVar:
		case partLit:
			if p.lit != "" {
				return false
			}
		default:
			return false
		}
	}
	return n >= 1
}

// braceExpand returns the words bash makes of text that holds braces:
// {a,b} and {1..3}, nested or side by side. It reports false when there
// would be more than a few hundred of them. Text with no braces that
// expand comes back as it is.
func braceExpand(s string) ([]string, bool) {
	const limit = 512
	for i := 0; i < len(s); i++ {
		if s[i] != '{' || i > 0 && s[i-1] == '$' {
			continue
		}
		// The matching }, and the commas at this level.
		depth, end := 0, -1
		var commas []int
		for j := i; j < len(s); j++ {
			switch s[j] {
			case '{':
				depth++
			case '}':
				if depth--; depth == 0 {
					end = j
				}
			case ',':
				if depth == 1 {
					commas = append(commas, j)
				}
			}
			if end >= 0 {
				break
			}
		}
		if end < 0 {
			return []string{s}, true // no closing brace: all of it is text
		}
		var alts []string
		if len(commas) > 0 {
			from := i + 1
			for _, c := range append(commas, end) {
				alts = append(alts, s[from:c])
				from = c + 1
			}
		} else if alts = braceSequence(s[i+1 : end]); alts == nil {
			continue // {x}: text
		}
		rest, ok := braceExpand(s[end+1:])
		if !ok {
			return nil, false
		}
		var out []string
		for _, alt := range alts {
			inner, ok := braceExpand(alt)
			if !ok {
				return nil, false
			}
			for _, in := range inner {
				for _, r := range rest {
					if out = append(out, s[:i]+in+r); len(out) > limit {
						return nil, false
					}
				}
			}
		}
		return out, true
	}
	return []string{s}, true
}

// braceSequence returns what {a..b} or {a..b..step} stands for, or nil
// when the text between the braces is no sequence.
func braceSequence(s string) []string {
	parts := strings.Split(s, "..")
	if len(parts) != 2 && len(parts) != 3 {
		return nil
	}
	step := 1
	if len(parts) == 3 {
		n, err := strconv.Atoi(parts[2])
		if err != nil {
			return nil
		}
		if n < 0 {
			n = -n
		}
		if n > 0 {
			step = n
		}
	}
	a, b := parts[0], parts[1]
	var out []string
	if len(a) == 1 && len(b) == 1 && isNameStart(a[0]) && isNameStart(b[0]) {
		lo, hi := int(a[0]), int(b[0])
		if lo > hi {
			lo, hi = hi, lo
		}
		for c := lo; c <= hi; c += step {
			out = append(out, string(rune(c)))
		}
		return out
	}
	lo, err1 := strconv.Atoi(a)
	hi, err2 := strconv.Atoi(b)
	if err1 != nil || err2 != nil || len(a) > 9 || len(b) > 9 {
		return nil
	}
	if lo > hi {
		lo, hi = hi, lo
	}
	if (hi-lo)/step > 600 {
		// Too many to list: the caller stops at its own limit.
		for n := lo; len(out) <= 600; n += step {
			out = append(out, strconv.Itoa(n))
		}
		return out
	}
	// A number written with a leading zero pads all of them.
	width := 0
	for _, t := range []string{a, b} {
		if d := strings.TrimLeft(t, "+-"); len(d) > 1 && d[0] == '0' {
			width = max(width, len(t))
		}
	}
	for n := lo; n <= hi; n += step {
		t := strconv.Itoa(n)
		for len(t) < width {
			t = "0" + t
		}
		out = append(out, t)
	}
	return out
}

// isNameStart and isNameChar say which bytes make a shell variable name.
func isNameStart(c byte) bool {
	return c == '_' || (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z')
}

func isNameChar(c byte) bool { return isNameStart(c) || (c >= '0' && c <= '9') }

// isWordEnd reports whether c ends an unquoted word.
func isWordEnd(c byte) bool {
	switch c {
	case ' ', '\t', '\n', ';', '&', '|', '<', '>', '(', ')':
		return true
	}
	return false
}

// wordBuilder collects a word while it is read.
type wordBuilder struct {
	w   word
	raw strings.Builder
	lit strings.Builder
}

func (b *wordBuilder) flush() {
	if b.lit.Len() > 0 {
		b.w.parts = append(b.w.parts, wordPart{kind: partLit, lit: b.lit.String()})
		b.lit.Reset()
	}
}

func (b *wordBuilder) add(p wordPart) {
	b.flush()
	b.w.parts = append(b.w.parts, p)
}

func (b *wordBuilder) done() word {
	b.flush()
	b.w.raw = b.raw.String()
	if b.w.quoted {
		// (See word.quoted.)
		for _, p := range b.w.parts {
			if p.kind == partVar && p.bare {
				b.w.quoted = false
			}
		}
		if b.w.bareSubst {
			b.w.quoted = false
		}
	}
	if len(b.w.parts) == 0 {
		b.w.parts = []wordPart{{kind: partLit}}
	}
	if b.w.brace && b.w.raw == "{}" {
		b.w.brace = false
	}
	return b.w
}

// readWord reads the word that starts at p.pos. It reports false when no
// word starts there. On a construct it cannot follow it calls p.fail and
// returns what it has.
func (p *parser) readWord() (word, bool) {
	start := p.pos
	var b wordBuilder
	// prev is the last byte of the word when it was written plainly, and 0
	// after anything quoted or expanded.
	prev := byte(0)
	for p.pos < len(p.src) && !p.failed {
		c := p.src[p.pos]
		last := prev
		prev = 0
		if isWordEnd(c) {
			// <(cmd), >(cmd) and zsh's =(cmd) are words that run a command.
			if c == '(' && p.pos > start && b.raw.String() == "=" && !b.w.quoted {
				p.procSubst(&b)
				continue
			}
			// A process substitution is part of the word it stands in,
			// wherever it stands: "r>()m" is one word, and with nothing in
			// the parentheses it is the word "rm".
			if (c == '<' || c == '>') && p.pos+1 < len(p.src) && p.src[p.pos+1] == '(' {
				b.raw.WriteByte(c)
				p.pos++
				p.procSubst(&b)
				continue
			}
			break
		}
		switch c {
		case '\\':
			if p.pos+1 >= len(p.src) {
				p.fail("the command ends in a backslash")
				return b.done(), true
			}
			n := p.src[p.pos+1]
			if n == '\n' {
				// The shell takes the pair out and reads on in the same
				// word. That is only followed where the word ends anyway.
				if next := p.nextReal(p.pos + 2); next != 0 && strings.IndexByte(" \t\n;&|)", next) < 0 {
					p.fail("a backslash at the end of a line in the middle of a word")
					return b.done(), true
				}
				p.pos += 2
				continue
			}
			p.pos += 2
			b.w.quoted = true
			b.raw.WriteByte('\\')
			b.raw.WriteByte(n)
			b.lit.WriteByte(n)
		case '\'':
			end := strings.IndexByte(p.src[p.pos+1:], '\'')
			if end < 0 {
				p.fail("a single quote is never closed")
				return b.done(), true
			}
			b.w.quoted = true
			b.raw.WriteString(p.src[p.pos : p.pos+end+2])
			b.lit.WriteString(p.src[p.pos+1 : p.pos+1+end])
			p.pos += end + 2
		case '"':
			b.w.quoted = true
			b.raw.WriteByte('"')
			p.pos++
			p.dquote(&b, '"')
		case '`':
			p.backtick(&b, 0)
		case '$':
			p.dollar(&b, false)
		case '*', '?', '[':
			b.w.glob = true
			b.raw.WriteByte(c)
			b.lit.WriteByte(c)
			p.pos++
		case '{', '}':
			b.w.brace = true
			b.raw.WriteByte(c)
			b.lit.WriteByte(c)
			p.pos++
		default:
			if p.pos == start {
				switch c {
				case '~':
					b.w.tilde = true
				case '=':
					b.w.eqHead = true
				}
			} else if c == '~' && (last == '=' || last == ':') {
				if _, ok := assignName(b.raw.String()); ok {
					b.w.eqTilde = true
				}
			}
			if c >= utf8.RuneSelf {
				// Text outside ASCII is an ordinary part of a word, with two
				// exceptions that ask: a character that looks like a blank or
				// cannot be seen (a no-break space is a blank to bash in some
				// locales, and to the person reading the question in all of
				// them), and bytes that are not text at all.
				r, size := utf8.DecodeRuneInString(p.src[p.pos:])
				if r == utf8.RuneError && size <= 1 {
					p.fail("bytes that are not text")
					return b.done(), true
				}
				if oddRune(r) {
					p.fail("a character that looks like a space, or cannot be seen, outside quotes")
					return b.done(), true
				}
				b.raw.WriteString(p.src[p.pos : p.pos+size])
				b.lit.WriteString(p.src[p.pos : p.pos+size])
				p.pos += size
				continue
			}
			prev = c
			b.raw.WriteByte(c)
			b.lit.WriteByte(c)
			p.pos++
		}
	}
	if p.pos == start {
		return word{}, false
	}
	return b.done(), true
}

// oddRune reports whether r is a character that a person takes for a
// blank or does not see at all: Unicode's spaces and line separators, the
// zero-width characters, the marks that change the direction text is
// shown in, and the soft hyphen.
func oddRune(r rune) bool {
	switch {
	case unicode.IsSpace(r), unicode.Is(unicode.Zs, r), unicode.Is(unicode.Zl, r), unicode.Is(unicode.Zp, r):
		return true
	case r == 0x00AD, r == 0x061C, r == 0x180E, r == 0xFEFF:
		return true
	case r >= 0x200B && r <= 0x200F, r >= 0x202A && r <= 0x202E, r >= 0x2060 && r <= 0x2069:
		return true
	}
	return false
}

// dquote reads the inside of a double-quoted string up to and including
// the closing quote. With closer 0 it reads to the end of the text, which
// is how the body of a here-document is scanned.
func (p *parser) dquote(b *wordBuilder, closer byte) {
	for p.pos < len(p.src) && !p.failed {
		c := p.src[p.pos]
		switch {
		case c == closer && closer != 0:
			b.raw.WriteByte(c)
			p.pos++
			return
		case c == '\\':
			if p.pos+1 >= len(p.src) {
				if closer == 0 {
					b.raw.WriteByte(c)
					b.lit.WriteByte(c)
					p.pos++
					return
				}
				p.fail("a double quote is never closed")
				return
			}
			n := p.src[p.pos+1]
			p.pos += 2
			switch {
			case n == '\n':
			case n == '$', n == '`', n == '\\', n == '"' && closer != 0:
				// In a here-document a double quote is an ordinary
				// character, and a backslash before it stays.
				b.raw.WriteByte('\\')
				b.raw.WriteByte(n)
				b.lit.WriteByte(n)
			default:
				b.raw.WriteByte('\\')
				b.raw.WriteByte(n)
				b.lit.WriteByte('\\')
				b.lit.WriteByte(n)
			}
		case c == '`':
			if closer != 0 {
				p.backtick(b, 1)
			} else {
				p.backtick(b, 2)
			}
		case c == '$':
			p.dollar(b, true)
		default:
			b.raw.WriteByte(c)
			b.lit.WriteByte(c)
			p.pos++
		}
	}
	if closer != 0 && !p.failed {
		p.fail("a double quote is never closed")
	}
}

// backtick reads a `command` and parses the command inside it. where
// says what the backticks stand in: 0 for bare text, 1 for double quotes,
// 2 for a here-document. Inside double quotes a backslash before a double
// quote is taken off as well: the command of "`echo \"x\"`" is echo "x".
// Shells do not all do that inside a here-document, so there the command
// is read both ways.
func (p *parser) backtick(b *wordBuilder, where int) {
	p.pos++ // the opening backtick
	var inner, alt strings.Builder
	closed := false
	for p.pos < len(p.src) {
		c := p.src[p.pos]
		if c == '`' {
			p.pos++
			closed = true
			break
		}
		if c == '\\' && p.pos+1 < len(p.src) {
			n := p.src[p.pos+1]
			if n == '`' || n == '\\' || n == '$' {
				inner.WriteByte(n)
				alt.WriteByte(n)
				p.pos += 2
				continue
			}
			if n == '"' && where != 0 {
				inner.WriteByte(n)
				if where == 2 {
					alt.WriteByte(c)
				}
				alt.WriteByte(n)
				p.pos += 2
				continue
			}
		}
		inner.WriteByte(c)
		alt.WriteByte(c)
		p.pos++
	}
	if !closed {
		p.fail("a backtick is never closed")
		return
	}
	b.raw.WriteByte('`')
	b.raw.WriteString(inner.String())
	b.raw.WriteByte('`')
	b.add(wordPart{kind: partSubst})
	if where == 0 {
		b.w.bareSubst = true
	}
	p.sc.subst = true
	p.nested(inner.String(), nestSubst)
	if alt.String() != inner.String() {
		p.nested(alt.String(), nestSubst)
	}
}

// procSubst reads the "(cmd)" of a process substitution; p.pos is at the
// opening parenthesis.
func (p *parser) procSubst(b *wordBuilder) {
	from := p.pos
	p.pos++
	p.inner(nestSubst, ')')
	if p.failed {
		return
	}
	b.raw.WriteString(p.src[from:p.pos])
	b.add(wordPart{kind: partSubst})
	p.sc.subst = true
}

// dollar reads whatever a $ starts: a variable, ${...}, $(...), $((...)),
// $'...' or $"...". inDouble says the $ sits inside double quotes.
func (p *parser) dollar(b *wordBuilder, inDouble bool) {
	from := p.pos
	rest := p.src[p.pos+1:]
	switch {
	case strings.HasPrefix(rest, "\\\n"):
		// The shell takes the pair out first: "$\<newline>(x)" is "$(x)".
		p.fail("a backslash at the end of a line right after a $")
	case strings.HasPrefix(rest, "(("):
		end := arithEnd(p.src, p.pos+3, false)
		if end < 0 {
			p.fail("an arithmetic expansion gnosh cannot follow")
			return
		}
		p.pos = end
		b.raw.WriteString(p.src[from:p.pos])
		b.add(wordPart{kind: partArith})
	case strings.HasPrefix(rest, "("):
		p.pos += 2
		p.inner(nestSubst, ')')
		if p.failed {
			return
		}
		b.raw.WriteString(p.src[from:p.pos])
		b.add(wordPart{kind: partSubst})
		if !inDouble {
			b.w.bareSubst = true
		}
		p.sc.subst = true
	case strings.HasPrefix(rest, "{"):
		p.braceParam(b, inDouble)
	case strings.HasPrefix(rest, "["):
		p.fail("the old $[...] arithmetic form")
	case strings.HasPrefix(rest, "'") && !inDouble:
		p.pos += 2
		s, ok := p.ansiC()
		if !ok {
			return
		}
		b.w.quoted = true
		b.raw.WriteString(p.src[from:p.pos])
		b.lit.WriteString(s)
	case strings.HasPrefix(rest, "\"") && !inDouble:
		// $"..." is looked up in a message catalog, and bash expands what
		// the catalog returns: the text that runs is not the text written.
		p.fail("a $\"...\" string, which the shell may replace with a translation")
	case rest != "" && isNameStart(rest[0]):
		n := 1
		for n < len(rest) && isNameChar(rest[n]) {
			n++
		}
		if strings.HasPrefix(rest[n:], "\\\n") && isNameChar(p.nextReal(p.pos+1+n)) {
			p.fail("a backslash at the end of a line inside a variable's name")
			return
		}
		p.pos += 1 + n
		b.raw.WriteString(p.src[from:p.pos])
		b.add(wordPart{kind: partVar, name: rest[:n], bare: !inDouble})
	case rest != "" && strings.IndexByte("?$!#*@-0123456789", rest[0]) >= 0:
		p.pos += 2
		b.raw.WriteString(p.src[from:p.pos])
		// ($@ and $* are every word the script or the function was
		// handed, like every word of an array.)
		b.add(wordPart{kind: partVar, name: rest[:1], all: rest[0] == '@' || rest[0] == '*', bare: !inDouble})
	default:
		b.raw.WriteByte('$')
		b.lit.WriteByte('$')
		p.pos++
	}
}

// braceParam reads ${...}. Only the plain forms are followed: ${name} and
// ${name<op>word} for the POSIX operators. Anything else (array subscripts,
// indirection, zsh flags, bash's @ operators, substrings) can change what
// runs in ways this package does not model, so it fails the parse.
func (p *parser) braceParam(b *wordBuilder, inDouble bool) {
	from := p.pos
	i := p.pos + 2
	n := i
	for n < len(p.src) && isNameChar(p.src[n]) {
		n++
	}
	if n == i || !isNameStart(p.src[i]) {
		p.exoticParam(b, inDouble)
		return
	}
	name := p.src[i:n]
	if n >= len(p.src) {
		p.fail("a ${ is never closed")
		return
	}
	part := wordPart{kind: partVar, name: name, bare: !inDouble}
	// The same forms on the words of an array, or on one of them, as bash
	// has them: "a=(-R 000 .); chmod "${a[@]=}"" is chmod -R 000 ., and
	// "${a[0]:-x}" is a word of a, or x. Like ${a[@]} itself (see
	// exoticParam), they are left to a person under allow rules.
	if sub, k, ok := subscriptAt(p.src, n); ok && posixOp(p.src[k:]) != "" {
		n, part.all = k, sub == "@" || sub == "*"
		p.sc.exotic = true
	}
	if p.src[n] == '}' {
		p.pos = n + 1
		b.raw.WriteString(p.src[from:p.pos])
		b.add(part)
		return
	}
	op := posixOp(p.src[n:])
	if op == "" {
		p.exoticParam(b, inDouble)
		return
	}
	if op == ":+" || op == "+" {
		// (${a[@]:+word} is the word, once, where a has words.)
		part.all = false
	}
	part.guarded = op == ":?" || op == "?"
	if op == ":=" || op == "=" {
		p.sc.setsVar = true
		part.sets = true
	}
	// The word after the operator may itself expand things, which are
	// followed. Quotes and backslashes inside it are not: shells disagree
	// on how they nest there, and a guess could hide a command.
	p.pos = n + len(op)
	var sub wordBuilder
	closed := false
	for p.pos < len(p.src) && !p.failed && !closed {
		switch c := p.src[p.pos]; c {
		case '}':
			p.pos++
			closed = true
		case '"', '\'', '\\':
			p.fail("quoting inside ${...}")
		case '`':
			if inDouble {
				p.backtick(&sub, 2)
			} else {
				p.backtick(&sub, 0)
			}
		case '$':
			p.dollar(&sub, true)
		case '<', '>':
			if p.pos+1 < len(p.src) && p.src[p.pos+1] == '(' {
				// "${X->(cmd)}": a process substitution runs its command
				// here as anywhere.
				sub.raw.WriteByte(c)
				p.pos++
				p.procSubst(&sub)
				continue
			}
			sub.lit.WriteByte(c)
			p.pos++
		default:
			sub.lit.WriteByte(c)
			p.pos++
		}
	}
	if p.failed {
		return
	}
	if !closed {
		p.fail("a ${ is never closed")
		return
	}
	body := sub.done()
	b.raw.WriteString(p.src[from:p.pos])
	part.op = op
	switch op {
	case ":-", "-", ":=", "=":
		if body.static() {
			// (The shell expands a ~ at the head of that text: defOr does.)
			part.def, part.hasDef = body.joined(), true
		}
		part.body = &body
	case ":+", "+":
		if body.static() {
			part.alt, part.hasAlt = body.joined(), true
		}
		part.body = &body
	case "%", "%%", "#", "##":
		// "a=(-a --delete src/ .x); rsync "${a[@]%x}"" is rsync -a
		// --delete src/ .: a pattern of plain text is cut off as the shell
		// cuts it, and only a real pattern leaves every beginning (or end)
		// of the value as what it may be (oracle seed 106).
		if pat := body.joined(); body.static() && plainPattern(pat) {
			part.pat, part.hasPat = pat, true
		}
	}
	b.add(part)
	if body.hasSubst() {
		b.add(wordPart{kind: partSubst})
	}
}

// exoticParam reads a ${...} that is not one of the plain forms. Its
// value is not known, and no allow rule approves a line that holds one,
// but the line can still be read to its end.
func (p *parser) exoticParam(b *wordBuilder, inDouble bool) {
	end, sets, ok := exoticEnd(p.src, p.pos+2)
	if !ok {
		p.fail("a ${...} form gnosh does not follow")
		return
	}
	b.raw.WriteString(p.src[p.pos:end])
	// ${name[i]}, ${name[@]} and ${name[*]} are a variable's own value,
	// or its values: the name is kept for a line that set them.
	part := wordPart{kind: partVar, bare: !inDouble}
	if name, all, ok := elemName(p.src[p.pos+2 : end-1]); ok {
		part.name, part.all = name, all
	} else if name, op, a, b, all, ok := exoticForm(p.src[p.pos+2 : end-1]); ok {
		part.name, part.op, part.arg1, part.arg2, part.all = name, op, a, b, all
	} else if body := p.src[p.pos+2 : end-1]; body != "" && isNameStart(body[0]) {
		// Some other form of a variable (${name,pattern}, ${name@U},
		// ${name/#a/b}): what it makes of a value is not worked out,
		// and the value as it is stays one thing it may be. On ${a[@]}
		// it is each word of the array, every one a word of its own.
		n := 1
		for n < len(body) && isNameChar(body[n]) {
			n++
		}
		part.name, part.op = body[:n], "~"
		if sub, _, ok := subscriptAt(body, n); ok {
			part.all = sub == "@" || sub == "*"
		}
	}
	p.pos = end
	b.add(part)
	p.sc.exotic = true
	if sets {
		p.sc.setsVar, p.sc.blindSet = true, true
	}
}

// exoticForm reads the body of one of the ${...} forms bash adds to the
// POSIX ones, where what the form makes of a value can be worked out
// from the text alone: ${name/text/other} and ${name//text/other} with
// plain text to look for, ${name:from:length} with numbers, the changes
// of case (^^ ,, ^ ,), and ${!name}, which stands for the variable that
// name's value names (op "!"). A value the line knows is then followed
// through the form: "rm -rf ${HOME/x/y}" is the home directory, whose
// name holds no x, and "${HOME:0:1}" is the root. all says the form is
// on every word of an array (${a[@]/x/y}), each word put through it.
func exoticForm(body string) (name, op, a, b string, all, ok bool) {
	if strings.ContainsAny(body, "$`'\"\\") {
		return "", "", "", "", false, false
	}
	if strings.HasPrefix(body, "!") && isVarName(body[1:]) {
		return body[1:], "!", "", "", false, true
	}
	n := 0
	for n < len(body) && isNameChar(body[n]) {
		n++
	}
	if n == 0 || !isNameStart(body[0]) {
		return "", "", "", "", false, false
	}
	name, rest := body[:n], body[n:]
	if sub, k, ok := subscriptAt(body, n); ok {
		rest, all = body[k:], sub == "@" || sub == "*"
	}
	switch {
	case rest == "^^" || rest == ",," || rest == "^" || rest == ",":
		return name, rest, "", "", all, true
	case strings.HasPrefix(rest, "/"):
		op, rest = "/", rest[1:]
		if strings.HasPrefix(rest, "/") {
			op, rest = "//", rest[1:]
		}
		a, b, _ = strings.Cut(rest, "/")
		// (A # or a % at its head ties the text to one end of the value,
		// and * ? [ make a pattern of it: those are not worked out.)
		if a == "" || strings.ContainsAny(a, "*?[") || a[0] == '#' || a[0] == '%' {
			return "", "", "", "", false, false
		}
		return name, op, a, b, all, true
	case strings.HasPrefix(rest, ":") && !all:
		// (On every word of an array it picks some of the words, which
		// elemName reads.)
		from, length, has := strings.Cut(rest[1:], ":")
		from, length = strings.TrimSpace(from), strings.TrimSpace(length)
		if !isInt(from) || has && !isInt(length) {
			return "", "", "", "", false, false
		}
		return name, ":sub", from, length, false, true
	}
	return "", "", "", "", false, false
}

// plainPattern reports whether a pattern of ${name%pattern} and its kin
// is plain text, which matches only itself: no character that makes a
// pattern (* ? [ and the parentheses of bash's extended ones), and no ~
// at its head, which the shell would make a home directory.
func plainPattern(s string) bool {
	return !strings.ContainsAny(s, "*?[]()\\") && !strings.HasPrefix(s, "~")
}

// posixOp returns the operator of a POSIX ${name<op>word} form that s
// starts with, or "".
func posixOp(s string) string {
	for _, o := range []string{":-", ":=", ":?", ":+", "-", "=", "?", "+", "%%", "%", "##", "#"} {
		if strings.HasPrefix(s, o) {
			return o
		}
	}
	return ""
}

// subscriptAt reads the subscript of an array at s[i], when s[i] opens
// one that is @, * or a number: it returns the subscript and the index
// just past its ].
func subscriptAt(s string, i int) (sub string, end int, ok bool) {
	if i >= len(s) || s[i] != '[' {
		return "", 0, false
	}
	j := strings.IndexByte(s[i:], ']')
	if j < 0 {
		return "", 0, false
	}
	sub = s[i+1 : i+j]
	if sub != "@" && sub != "*" && !isDigits(sub) {
		return "", 0, false
	}
	return sub, i + j + 1, true
}

// elemName returns the variable in the body of a ${name[subscript]}
// whose subscript is a number, @ or *. all says it is one of the last
// two: the form stands for every word of the array.
func elemName(body string) (name string, all, ok bool) {
	i := strings.IndexByte(body, '[')
	// ${name[@]:1} and ${name[*]:1:2} are some of the array's words: read
	// as all of them, which is never fewer.
	if j := strings.Index(body, "]:"); j > i && i > 0 && (body[i+1:j] == "@" || body[i+1:j] == "*") && strings.Trim(body[j+2:], "0123456789: -") == "" {
		body = body[:j+1]
	}
	if i <= 0 || !strings.HasSuffix(body, "]") || !isNameStart(body[0]) {
		return "", false, false
	}
	for k := 1; k < i; k++ {
		if !isNameChar(body[k]) {
			return "", false, false
		}
	}
	sub := body[i+1 : len(body)-1]
	if sub != "@" && sub != "*" && !isDigits(sub) {
		return "", false, false
	}
	return body[:i], sub == "@" || sub == "*", true
}

// exoticEnd returns the index just past the "}" that closes a ${...} whose
// body starts at i, when the body is made only of characters that cannot
// hide a command or a quote: names, digits and the operators of the
// expansion forms shells have (subscripts, substrings, replacements,
// case changes). It reports false for anything else. sets says the body
// holds an =, which in some forms assigns.
func exoticEnd(s string, i int) (end int, sets, ok bool) {
	depth := 1
	for ; i < len(s); i++ {
		c := s[i]
		switch {
		case c == '}':
			depth--
			if depth == 0 {
				return i + 1, sets, true
			}
		case c == '$':
			if i+1 < len(s) && s[i+1] == '{' {
				depth++
				i++
			} else if i+1 >= len(s) || !isNameStart(s[i+1]) {
				return 0, false, false
			}
		case c == '=':
			sets = true
		case isNameChar(c), strings.IndexByte("[]@*#%/:-+?.,^!~ ", c) >= 0:
		default:
			return 0, false, false
		}
	}
	return 0, false, false
}

// arithEnd returns the index just past the "))" that closes the arithmetic
// expansion whose body starts at i, or -1 when the body holds anything but
// plain arithmetic. semis lets the body hold semicolons, as the head of a
// C-style for loop does.
func arithEnd(s string, i int, semis bool) int {
	depth := 0
	for ; i < len(s); i++ {
		c := s[i]
		switch {
		case c == '(':
			depth++
		case c == ')':
			if depth == 0 {
				if i+1 < len(s) && s[i+1] == ')' {
					return i + 2
				}
				return -1
			}
			depth--
		case isNameChar(c), strings.IndexByte(" \t+-*/%<>=!&|^~?:,.[]#@", c) >= 0:
			// (Brackets are array subscripts, which hold arithmetic too;
			// # and @ come with ${#a[@]}.)
		case c == ';' && semis:
		case c == '$' && i+1 < len(s) && isNameStart(s[i+1]):
		case c == '$' && i+1 < len(s) && s[i+1] == '{':
			// ${name}, ${#a[@]}, ${a[i]}: forms that hold no command.
			end, _, ok := exoticEnd(s, i+2)
			if !ok {
				return -1
			}
			i = end - 1
		default:
			return -1
		}
	}
	return -1
}

// ansiC reads the inside of $'...' up to and including the closing quote
// and returns the text it stands for.
func (p *parser) ansiC() (string, bool) {
	var out strings.Builder
	for p.pos < len(p.src) {
		c := p.src[p.pos]
		if c == '\'' {
			p.pos++
			return out.String(), true
		}
		if c != '\\' {
			out.WriteByte(c)
			p.pos++
			continue
		}
		if p.pos+1 >= len(p.src) {
			break
		}
		n := p.src[p.pos+1]
		p.pos += 2
		switch n {
		case 'n':
			out.WriteByte('\n')
		case 't':
			out.WriteByte('\t')
		case 'r':
			out.WriteByte('\r')
		case 'a':
			out.WriteByte(7)
		case 'b':
			out.WriteByte(8)
		case 'f':
			out.WriteByte(12)
		case 'v':
			out.WriteByte(11)
		case 'e', 'E':
			out.WriteByte(27)
		case '\'':
			// A shell with no $'...' (dash, sh on Debian and Ubuntu) reads
			// $ and then a plain '...', which ends at this quote: the rest
			// of the line is commands to it and quoted text to bash (oracle
			// seed 111). Everywhere else the two end the quote together.
			p.fail("a $'...' string with \\' in it, which a shell without $'...' (dash) ends at that quote")
			return "", false
		case '\\', '"', '?':
			out.WriteByte(n)
		case 'x':
			v, k := readDigits(p.src[p.pos:], 16, 2)
			if k == 0 {
				p.fail("a $'...' escape gnosh cannot read")
				return "", false
			}
			p.pos += k
			if v == 0 {
				p.fail("a $'...' escape gnosh cannot read")
				return "", false
			}
			out.WriteByte(byte(v))
		case 'u', 'U':
			max := 4
			if n == 'U' {
				max = 8
			}
			v, k := readDigits(p.src[p.pos:], 16, max)
			if k == 0 || v == 0 || v > 0x10FFFF {
				p.fail("a $'...' escape gnosh cannot read")
				return "", false
			}
			p.pos += k
			out.WriteRune(rune(v))
		case '0', '1', '2', '3', '4', '5', '6', '7':
			v, k := readDigits(p.src[p.pos-1:], 8, 3)
			p.pos += k - 1
			if v == 0 || v > 255 {
				p.fail("a $'...' escape gnosh cannot read")
				return "", false
			}
			out.WriteByte(byte(v))
		default:
			// \cX and anything else: not worth guessing at.
			p.fail("a $'...' escape gnosh cannot read")
			return "", false
		}
	}
	p.fail("a $'...' quote is never closed")
	return "", false
}

// readDigits reads up to max digits of the given base from the start of s
// and returns their value and how many it read.
func readDigits(s string, base, max int) (int, int) {
	v, k := 0, 0
	for k < len(s) && k < max {
		c := s[k]
		d := -1
		switch {
		case c >= '0' && c <= '9':
			d = int(c - '0')
		case c >= 'a' && c <= 'f':
			d = int(c-'a') + 10
		case c >= 'A' && c <= 'F':
			d = int(c-'A') + 10
		}
		if d < 0 || d >= base {
			break
		}
		v = v*base + d
		k++
	}
	return v, k
}
