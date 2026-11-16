package permission

import (
	"strings"
)

// This file is gnosh's own reader of shell command lines. It is not a
// shell: it reads just enough of the POSIX grammar (and the bash and zsh
// additions people really type) to say which programs a command line would
// run and what else it would do on the way. Whatever it cannot follow it
// says so, and the caller then asks a person instead of guessing.

// nesting says what a simple command sits inside.
type nesting uint8

const (
	// nestSubshell: inside ( ... ).
	nestSubshell nesting = 1 << iota
	// nestSubst: inside $( ... ), backticks, or <( ... ).
	nestSubst
	// nestCompound: inside a loop, an if, a case, a { ...; } group or a
	// function body.
	nestCompound
	// nestScript: inside text handed to another shell, to eval or to a
	// program that runs its arguments. The parser never sets it; the
	// walk over wrappers does.
	nestScript
)

// sepKind says what joined a simple command to the one before it.
type sepKind uint8

const (
	// sepStart: the first command of its list.
	sepStart sepKind = iota
	// sepSemi: after ; or a newline.
	sepSemi
	// sepAnd: after &&.
	sepAnd
	// sepOr: after ||.
	sepOr
	// sepPipe: after | or |&.
	sepPipe
	// sepBg: after a command that was put in the background with &.
	sepBg
)

// redir is one redirection of a simple command.
type redir struct {
	// op is the operator: < > >> >| <> &> &>> >& <& << <<- <<<.
	op string
	// fd is the descriptor number written before the operator, or "".
	fd string
	// target is the word after the operator: a file, a descriptor, a
	// here-document's delimiter or a here-string.
	target word
	// body is a here-document's text; bodyStatic says the shell passes it
	// on unchanged (the delimiter was quoted, or nothing in it expands).
	body       string
	bodyStatic bool
}

// assign is a variable assignment written before a command's name.
type assign struct {
	name string
	// w is the whole word, name and = included.
	w word
	// fromSubst is set when the value comes from a command substitution.
	fromSubst bool
	// array is set for name=(word ...); elems are the words.
	array bool
	elems []word
}

// loopVar is the variable of a for loop and the words it goes through.
type loopVar struct {
	name  string
	words []word
	// args is set for a loop with no list: it goes through "$@".
	args bool
	// from and to bound the commands of the loop's body in script.cmds.
	from, to int
}

// simpleCmd is one program invocation: assignments, words, redirections.
type simpleCmd struct {
	assigns []assign
	words   []word
	redirs  []redir
	sep     sepKind
	nest    nesting
	// bg is set when the list the command ends was put in the background.
	bg bool
	// neg is set when the pipeline the command is part of stands under a
	// !: what comes after && then runs when the pipeline failed.
	neg bool
	// timeCut is set for a command that starts with bash's keyword time
	// when a redirection stands among the words the keyword takes for its
	// own (-p, --, a second time): it is how many words stood before the
	// redirection. bash takes those words for the keyword's only where
	// they follow it at once, and reads what follows a redirection as the
	// command, whatever it looks like: "time>&- -p x" runs a program
	// named -p. To a plain sh time is a program, which reads the words as
	// its own. Only the reader sets it: a command made of another's words
	// has none.
	timeCut int
	// pieces and altRedirs are how a shell that has no [[ ]] (dash, and
	// every plain sh) would read a [[ ]] test that holds && || | ( ) < >
	// or a line break: as several commands, and as redirections. mixed is
	// set when one of those commands would be more than a test operator
	// that no program is named after. A command with an &> in its middle
	// is cut into pieces in the same way, and one with a {name} before a
	// redirection has a single piece: itself, with the {name} that a
	// plain sh takes for an argument among its words.
	pieces    [][]word
	altRedirs []redir
	mixed     bool
	// nameGlob is set for a command read out of (( )) whose first word
	// holds * ? or [: to bash it is arithmetic (total[i]), to a plain sh
	// a file name pattern that names the program to run.
	nameGlob bool
	// aliased are the aliases this command was read through (useAlias):
	// a name is taken for its alias once.
	aliased []string
}

// script is everything read from one command line.
type script struct {
	// cmds holds every simple command, nested ones included, in the order
	// their text ends.
	cmds []*simpleCmd
	// ok is false when the line was not followed to its end; why says
	// what stopped the reader.
	ok  bool
	why string
	// subshells are the ( ... ) of the line, each as the commands inside
	// it, cmds[from:to]: a cd among them moves only the subshell.
	subshells []cmdSpan
	// What the line holds anywhere, nested or not.
	subst      bool // a command or process substitution
	subshell   bool // ( ... )
	cond       bool // an if, a while, an until or a case
	forLoop    bool // a for loop over a list of words
	group      bool // a { ...; } group
	compound   bool // a for loop, a function, (( )) and the other forms not read through
	background bool // a command followed by &
	piped      bool // a | anywhere
	setsVar    bool // a ${name:=value} expansion or a {name}> redirection, which assign
	blindSet   bool // a variable is set and the reader cannot say which: ${!ref:=x}
	funcs      bool // a function is defined
	funcNames  []string
	exotic     bool // a ${...} form that is followed but not modelled
	comments   []string
	// arith holds the text of every (( ... )) command. A shell without
	// that form (dash) runs it as commands in nested subshells.
	arith []string
	// loose holds words that belong to no command: the list of a for
	// loop, the word and patterns of a case.
	loose []word
	// loops holds every for loop's variable and list.
	loops []loopVar
}

// cmdSpan is a run of a script's commands: cmds[from:to].
type cmdSpan struct{ from, to int }

// maxShellDepth bounds how deeply substitutions and groups may nest before
// the reader stops and reports the line as one it cannot follow.
const maxShellDepth = 64

// pendingDoc is a here-document whose body has not been reached yet.
type pendingDoc struct {
	cmd   *simpleCmd
	idx   int
	delim string
	tabs  bool
	lit   bool
	depth int
}

// parser reads one piece of shell text.
type parser struct {
	src    string
	pos    int
	sc     *script
	nest   nesting
	sep    sepKind
	depth  int
	failed bool
	docs   []pendingDoc
}

// parseScript reads a command line.
func parseScript(src string) *script {
	sc := &script{ok: true}
	for i := 0; i < len(src); i++ {
		if c := src[i]; c < 0x20 && c != '\n' && c != '\t' || c == 0x7f {
			sc.ok = false
			sc.why = "the command holds a control character"
			return sc
		}
	}
	p := &parser{src: src, sc: sc}
	p.run()
	return sc
}

// run reads p.src to its end.
func (p *parser) run() {
	p.list(stopper{})
	if p.failed {
		return
	}
	if p.pos < len(p.src) {
		p.fail("an unexpected " + describeByte(p.src[p.pos]))
		return
	}
	if len(p.docs) > 0 {
		p.fail("a here-document has no body")
	}
}

func describeByte(c byte) string {
	if c == '\n' {
		return "line break"
	}
	return "\"" + string(c) + "\""
}

// fail records that the line could not be followed. The first reason is
// the one kept.
func (p *parser) fail(why string) {
	p.failed = true
	if p.sc.ok {
		p.sc.ok = false
		p.sc.why = why
	}
}

// nested reads text that sits inside the line being read (the inside of
// backticks) as a script of its own.
func (p *parser) nested(text string, n nesting) {
	if p.depth >= maxShellDepth {
		p.fail("commands nested too deeply")
		return
	}
	q := &parser{src: text, sc: p.sc, nest: p.nest | n, depth: p.depth + 1}
	q.run()
	if q.failed {
		p.failed = true
	}
}

// inner reads a list in place up to the closing byte, which it consumes.
func (p *parser) inner(n nesting, closer byte) {
	if p.depth >= maxShellDepth {
		p.fail("commands nested too deeply")
		return
	}
	nest, sep := p.nest, p.sep
	p.nest |= n
	p.sep = sepStart
	p.depth++
	p.list(stopper{paren: true})
	p.depth--
	p.nest, p.sep = nest, sep
	if p.failed {
		return
	}
	if p.pos >= len(p.src) || p.src[p.pos] != closer {
		p.fail("a parenthesis is never closed")
		return
	}
	p.pos++
}

// blanks skips spaces, tabs, backslash-newline pairs and comments. It is
// only called between tokens, which is where a # starts a comment.
func (p *parser) blanks() {
	for p.pos < len(p.src) {
		c := p.src[p.pos]
		switch {
		case c == ' ' || c == '\t':
			p.pos++
		case c == '\\' && p.pos+1 < len(p.src) && p.src[p.pos+1] == '\n':
			if !p.joinOK(p.pos) {
				p.fail("a backslash at the end of a line that joins two pieces of the command")
				return
			}
			p.pos += 2
		case c == '#':
			end := strings.IndexByte(p.src[p.pos:], '\n')
			if end < 0 {
				end = len(p.src) - p.pos
			}
			text := p.src[p.pos+1 : p.pos+end]
			p.sc.comments = append(p.sc.comments, text)
			// The shells gnosh runs (bash and sh, never at a prompt) end a
			// comment at the end of its line whatever it holds. Inside a
			// substitution an older bash looks for the closing parenthesis
			// or quote without knowing what a comment is, so there a
			// comment that holds anything a shell could act on is refused.
			if p.nest&nestSubst != 0 && strings.ContainsAny(text, ";&|<>()$`\\\"'{}") {
				p.fail("a comment that holds shell operators, inside a substitution")
			}
			p.pos += end
		default:
			return
		}
	}
}

// nextReal returns the first byte at or after i that is not part of a
// backslash-newline pair, or 0 at the end of the text.
func (p *parser) nextReal(i int) byte {
	for i+1 < len(p.src) && p.src[i] == '\\' && p.src[i+1] == '\n' {
		i += 2
	}
	if i >= len(p.src) {
		return 0
	}
	return p.src[i]
}

// joinOK reports whether the backslash-newline pair at i sits where it
// can only be white space. A shell takes the pair out before it reads
// anything, so "&\<newline>&" is "&&" and "$\<newline>(x)" is "$(x)":
// two pieces of text become one token. The reader does not rebuild the
// joined text. It accepts the pair where a blank stands on one side of
// it, or where a list operator stands before it and no operator after
// it, and refuses the line everywhere else.
func (p *parser) joinOK(i int) bool {
	next := p.nextReal(i + 2)
	if i == 0 || next == 0 || next == ' ' || next == '\t' || next == '\n' {
		return true
	}
	switch p.src[i-1] {
	case ' ', '\t', '\n':
		return true
	case ';', '&', '|', '(', ')':
		return strings.IndexByte(";&|<>()", next) < 0
	}
	return false
}

// newlines skips blanks and line breaks, reading the bodies of any
// here-documents that a line break starts.
func (p *parser) newlines() {
	for {
		p.blanks()
		if p.failed || p.pos >= len(p.src) || p.src[p.pos] != '\n' {
			return
		}
		p.newline()
	}
}

// newline consumes one line break and the here-document bodies after it.
func (p *parser) newline() {
	p.pos++
	if len(p.docs) == 0 {
		return
	}
	docs := p.docs
	p.docs = nil
	for _, d := range docs {
		if d.depth != p.depth {
			p.fail("a here-document that starts inside a substitution")
			return
		}
		var body strings.Builder
		closed := false
		for p.pos < len(p.src) {
			end := strings.IndexByte(p.src[p.pos:], '\n')
			line := p.src[p.pos:]
			next := len(p.src)
			if end >= 0 {
				line = p.src[p.pos : p.pos+end]
				next = p.pos + end + 1
			}
			p.pos = next
			cmp := line
			if d.tabs {
				cmp = strings.TrimLeft(line, "\t")
			}
			if cmp == d.delim {
				closed = true
				break
			}
			body.WriteString(line)
			body.WriteByte('\n')
		}
		if !closed {
			p.fail("a here-document is never closed")
			return
		}
		r := &d.cmd.redirs[d.idx]
		r.body = body.String()
		r.bodyStatic = true
		if !d.lit && joinMovesEnd(r.body, d.delim, d.tabs) {
			// With an unquoted delimiter the shell joins a line that ends
			// in a backslash to the next one before it looks for the
			// delimiter, and shells do not agree on the details. Where the
			// body ends is then not known.
			p.fail("a here-document in which a backslash at the end of a line may move where it ends")
			return
		}
		if !d.lit {
			// An unquoted delimiter: the shell expands the body.
			q := &parser{src: r.body, sc: p.sc, nest: p.nest, depth: p.depth + 1}
			var wb wordBuilder
			q.dquote(&wb, 0)
			if q.failed {
				p.failed = true
				return
			}
			w := wb.done()
			r.bodyStatic = w.static()
			if r.bodyStatic {
				r.body = w.joined()
			}
		}
	}
}

// joinMovesEnd reports whether a backslash at the end of a line could
// change where a here-document with an unquoted delimiter ends. body is
// what stands before the first line that is the delimiter. Shells join a
// line that ends in a backslash to the one after it, and do not agree on
// whether they look for the delimiter before or after joining, so two
// things make the end uncertain: a line joined to the delimiter line
// itself, and lines that spell the delimiter once they are joined. With
// <<-, where tabs are taken off as well, any join does.
func joinMovesEnd(body, delim string, tabs bool) bool {
	lines := strings.Split(strings.TrimSuffix(body, "\n"), "\n")
	joins := func(line string) bool {
		n := 0
		for n < len(line) && line[len(line)-1-n] == '\\' {
			n++
		}
		return n%2 == 1
	}
	run := ""
	for i, line := range lines {
		if !joins(line) {
			if run != "" && run+line == delim {
				return true
			}
			run = ""
			continue
		}
		if tabs || i == len(lines)-1 {
			return true
		}
		run += line[:len(line)-1]
	}
	return false
}

// stopper says where a list ends besides the end of the text.
type stopper struct {
	// paren: at a closing parenthesis.
	paren bool
	// words: at any of these reserved words.
	words []string
	// dsemi: at ;; ;& or ;;& (the end of a case branch).
	dsemi bool
}

func (p *parser) atStop(st stopper) bool {
	if p.pos >= len(p.src) {
		return true
	}
	if st.paren && p.src[p.pos] == ')' {
		return true
	}
	if st.dsemi && (strings.HasPrefix(p.src[p.pos:], ";;") || strings.HasPrefix(p.src[p.pos:], ";&")) {
		return true
	}
	if len(st.words) > 0 {
		if kw := p.reserved(); kw != "" {
			for _, w := range st.words {
				if w == kw {
					return true
				}
			}
		}
	}
	return false
}

// list reads commands joined by ; & and line breaks until st says stop.
func (p *parser) list(st stopper) {
	p.sep = sepStart
	for !p.failed {
		p.newlines()
		if p.failed || p.atStop(st) {
			return
		}
		first := len(p.sc.cmds)
		p.andOr()
		if p.failed {
			return
		}
		p.blanks()
		if p.pos >= len(p.src) {
			return
		}
		rest := p.src[p.pos:]
		switch {
		case st.dsemi && (strings.HasPrefix(rest, ";;") || strings.HasPrefix(rest, ";&")):
			return
		case rest[0] == ';':
			p.pos++
			p.sep = sepSemi
		case rest[0] == '&':
			p.pos++
			p.sc.background = true
			for _, c := range p.sc.cmds[first:] {
				c.bg = true
			}
			p.sep = sepBg
		case rest[0] == '\n':
			p.newline()
			p.sep = sepSemi
		default:
			return
		}
	}
}

// andOr reads pipelines joined by && and ||.
func (p *parser) andOr() {
	p.pipeline()
	for !p.failed {
		p.blanks()
		rest := p.src[p.pos:]
		switch {
		case strings.HasPrefix(rest, "&&"):
			p.sep = sepAnd
		case strings.HasPrefix(rest, "||"):
			p.sep = sepOr
		default:
			return
		}
		p.pos += 2
		p.newlines()
		p.pipeline()
	}
}

// pipeline reads commands joined by | and |&.
func (p *parser) pipeline() {
	p.blanks()
	neg := false
	if p.reserved() == "!" {
		p.pos++
		neg = true
	}
	first := len(p.sc.cmds)
	defer func() {
		if neg {
			for _, c := range p.sc.cmds[first:] {
				c.neg = true
			}
		}
	}()
	if p.keywordAfterTime() {
		// To bash, "time" opens a pipeline, and what stands after it is
		// read as one, keywords and all: "time coproc rm x" runs rm, and
		// "time if a; then b; fi" is an if. A reader that took time for a
		// program and the keyword for its first argument would be reading
		// another line.
		p.fail("a shell keyword after time")
		return
	}
	p.command()
	for !p.failed {
		p.blanks()
		rest := p.src[p.pos:]
		if !strings.HasPrefix(rest, "|") || strings.HasPrefix(rest, "||") {
			return
		}
		p.pos++
		if strings.HasPrefix(rest, "|&") {
			p.pos++
		}
		p.sc.piped = true
		p.sep = sepPipe
		p.newlines()
		p.command()
	}
}

// keywordAfterTime reports whether the pipeline at p.pos opens with
// bash's "time" (and its -p, --, a ! or another time) followed by a word
// of the shell's own.
func (p *parser) keywordAfterTime() bool {
	i, sawTime := p.pos, false
	for {
		for i < len(p.src) {
			if p.src[i] == ' ' || p.src[i] == '\t' {
				i++
			} else if strings.HasPrefix(p.src[i:], "\\\n") {
				i += 2
			} else {
				break
			}
		}
		// The shell takes a backslash-newline pair out before it reads a
		// word, so "time\<newline> coproc" opens with time as well.
		j := i
		for j < len(p.src) {
			if strings.HasPrefix(p.src[j:], "\\\n") {
				j += 2
				continue
			}
			if isWordEnd(p.src[j]) {
				break
			}
			j++
		}
		switch tok := strings.ReplaceAll(p.src[i:j], "\\\n", ""); {
		case tok == "time":
			sawTime = true
		case sawTime && (tok == "-p" || tok == "--" || tok == "!"):
		default:
			if !sawTime {
				return false
			}
			save := p.pos
			p.pos = i
			kw := p.reserved()
			p.pos = save
			return kw != "" && kw != "!"
		}
		i = j
	}
}

// reservedWords are the words that mean something to the shell when they
// stand where a command name would.
var reservedWords = []string{
	"!", "{", "}", "if", "then", "elif", "else", "fi", "while", "until",
	"do", "done", "for", "case", "esac", "function", "select", "coproc", "[[",
}

// reserved returns the reserved word that starts at p.pos, or "".
func (p *parser) reserved() string {
	rest := p.src[p.pos:]
	for _, kw := range reservedWords {
		if !strings.HasPrefix(rest, kw) {
			continue
		}
		// A backslash-newline pair after the word is taken out by the
		// shell before it reads the word.
		c := p.nextReal(p.pos + len(kw))
		if c == 0 {
			return kw
		}
		switch kw {
		case "{":
			if c == ' ' || c == '\t' || c == '\n' {
				return kw
			}
		default:
			// "!" too: "!<file cmd" and "!(cmd)" negate.
			if isWordEnd(c) {
				return kw
			}
		}
	}
	return ""
}

// expect consumes the reserved word kw or fails.
func (p *parser) expect(kw string) {
	if p.failed {
		return
	}
	p.newlines()
	if p.reserved() != kw {
		p.fail("a missing \"" + kw + "\"")
		return
	}
	p.pos += len(kw)
}

// body reads a list that sits inside a compound command.
func (p *parser) body(words ...string) {
	if p.failed {
		return
	}
	if p.depth >= maxShellDepth {
		p.fail("commands nested too deeply")
		return
	}
	nest := p.nest
	p.nest |= nestCompound
	p.depth++
	p.list(stopper{words: words})
	p.depth--
	p.nest = nest
}

// command reads one command: a simple one or a compound one.
func (p *parser) command() {
	p.blanks()
	if p.failed {
		return
	}
	if p.pos >= len(p.src) {
		p.fail("an operator with no command after it")
		return
	}
	c := p.src[p.pos]
	if c == '(' {
		if strings.HasPrefix(p.src[p.pos:], "((") {
			// (( expression )): arithmetic as a command. It is followed
			// only when it is plain arithmetic, and counts as a compound
			// form, so no allow rule approves the line.
			end := arithEnd(p.src, p.pos+2, false)
			if end < 0 {
				p.fail("an arithmetic command gnosh cannot follow")
				return
			}
			p.sc.arith = append(p.sc.arith, p.src[p.pos+2:end-2])
			p.pos = end
			p.sc.compound = true
			p.trailingRedirs()
			return
		}
		p.pos++
		p.sc.subshell = true
		from := len(p.sc.cmds)
		p.inner(nestSubshell, ')')
		if to := len(p.sc.cmds); to > from && !p.failed {
			p.sc.subshells = append(p.sc.subshells, cmdSpan{from, to})
		}
		p.trailingRedirs()
		return
	}
	if c == ')' || c == ';' || c == '&' || c == '|' || c == '\n' {
		p.fail("an operator with no command before it")
		return
	}
	kw := p.reserved()
	switch kw {
	case "":
		p.simple()
		return
	case "{":
		// A group runs in the shell itself, one command after the other,
		// as a line does: read straight through, it is what runs. A
		// redirection after it is opened before any cd in it has run,
		// and so is not read that way: such a group asks.
		p.pos++
		p.sc.group = true
		p.body("}")
		p.expect("}")
		mark := len(p.sc.cmds)
		p.trailingRedirs()
		if len(p.sc.cmds) > mark {
			p.sc.compound = true
		}
		return
	case "if":
		p.pos += 2
		p.sc.cond = true
		p.body("then")
		p.expect("then")
		p.body("elif", "else", "fi")
		for !p.failed && p.reserved() == "elif" {
			p.pos += 4
			p.body("then")
			p.expect("then")
			p.body("elif", "else", "fi")
		}
		if !p.failed && p.reserved() == "else" {
			p.pos += 4
			p.body("fi")
		}
		p.expect("fi")
	case "while", "until":
		p.pos += 5
		p.sc.cond = true
		p.body("do")
		p.expect("do")
		p.body("done")
		p.expect("done")
	case "for":
		p.pos += 3
		loops := len(p.sc.loops)
		p.forHead()
		if len(p.sc.loops) > loops && !p.sc.loops[loops].args {
			// A loop over a list of words: its body is judged once for
			// each value (decideShell).
			p.sc.forLoop = true
		} else {
			// for ((...)), and a loop over the words a script was handed.
			p.sc.compound = true
		}
		p.expect("do")
		from := len(p.sc.cmds)
		p.body("done")
		if len(p.sc.loops) > loops {
			p.sc.loops[loops].from, p.sc.loops[loops].to = from, len(p.sc.cmds)
		}
		p.expect("done")
	case "case":
		p.pos += 4
		p.sc.cond = true
		p.caseBody()
	case "[[":
		p.testCommand()
	case "function":
		p.pos += 8
		p.blanks()
		name, ok := p.readWord()
		if !ok {
			p.fail("a function with no name")
			return
		}
		p.sc.funcNames = append(p.sc.funcNames, name.joined())
		p.blanks()
		if strings.HasPrefix(p.src[p.pos:], "(") {
			p.pos++
			p.blanks()
			if !strings.HasPrefix(p.src[p.pos:], ")") {
				p.fail("a parenthesis gnosh cannot place")
				return
			}
			p.pos++
		}
		p.funcBody()
		return
	default:
		p.fail("\"" + kw + "\" where a command should start")
		return
	}
	p.trailingRedirs()
}

// funcBody reads the command that is a function's body.
func (p *parser) funcBody() {
	p.sc.compound, p.sc.funcs = true, true
	p.newlines()
	if p.failed {
		return
	}
	nest := p.nest
	p.nest |= nestCompound
	p.command()
	p.nest = nest
}

// forHead reads "name [in word...]" and the separator after it.
func (p *parser) forHead() {
	p.blanks()
	if strings.HasPrefix(p.src[p.pos:], "((") {
		// for ((init; test; step)): followed when it is plain arithmetic.
		end := arithEnd(p.src, p.pos+2, true)
		if end < 0 {
			p.fail("a for loop gnosh cannot follow")
			return
		}
		p.pos = end
		p.blanks()
		if strings.HasPrefix(p.src[p.pos:], ";") && !strings.HasPrefix(p.src[p.pos:], ";;") {
			p.pos++
		}
		return
	}
	name, ok := p.readWord()
	if !ok || !name.plain() {
		p.fail("a for loop gnosh cannot follow")
		return
	}
	p.newlines()
	if p.failed {
		return
	}
	rest := p.src[p.pos:]
	loop := loopVar{name: name.joined(), args: true}
	if strings.HasPrefix(rest, "in") && (len(rest) == 2 || isWordEnd(rest[2])) {
		p.pos += 2
		loop.args = false
		for !p.failed {
			p.blanks()
			w, ok := p.readWord()
			if !ok {
				break
			}
			p.sc.loose = append(p.sc.loose, w)
			loop.words = append(loop.words, w)
		}
	}
	p.sc.loops = append(p.sc.loops, loop)
	p.blanks()
	if strings.HasPrefix(p.src[p.pos:], ";") && !strings.HasPrefix(p.src[p.pos:], ";;") {
		p.pos++
	}
}

// caseBody reads "word in pattern) list ;; ... esac".
func (p *parser) caseBody() {
	p.blanks()
	subject, ok := p.readWord()
	if !ok {
		p.fail("a case with nothing to match")
		return
	}
	p.sc.loose = append(p.sc.loose, subject)
	p.newlines()
	rest := p.src[p.pos:]
	if p.failed || !strings.HasPrefix(rest, "in") || (len(rest) > 2 && !isWordEnd(rest[2])) {
		p.fail("a missing \"in\"")
		return
	}
	p.pos += 2
	for !p.failed {
		p.newlines()
		if p.failed {
			return
		}
		if p.reserved() == "esac" {
			p.pos += 4
			return
		}
		if strings.HasPrefix(p.src[p.pos:], "(") {
			p.pos++
		}
		for !p.failed {
			p.blanks()
			pat, ok := p.readWord()
			if !ok {
				p.fail("a case pattern gnosh cannot follow")
				return
			}
			p.sc.loose = append(p.sc.loose, pat)
			p.blanks()
			rest := p.src[p.pos:]
			if strings.HasPrefix(rest, "|") {
				p.pos++
				continue
			}
			if strings.HasPrefix(rest, ")") {
				p.pos++
				break
			}
			p.fail("a case pattern gnosh cannot follow")
			return
		}
		if p.depth >= maxShellDepth {
			p.fail("commands nested too deeply")
			return
		}
		nest := p.nest
		p.nest |= nestCompound
		p.depth++
		p.list(stopper{dsemi: true, words: []string{"esac"}})
		p.depth--
		p.nest = nest
		if p.failed {
			return
		}
		p.blanks()
		for _, end := range []string{";;&", ";;", ";&"} {
			if strings.HasPrefix(p.src[p.pos:], end) {
				p.pos += len(end)
				break
			}
		}
	}
}

// testCommand reads [[ ... ]]. Inside it && || ( ) < > and | are the
// test's own operators, not the shell's; everything else is read as
// words, so that a substitution among them is still seen.
//
// That is how bash reads it. A shell with no [[ ]] (dash, which is sh on
// many systems) reads the same text as ordinary commands: "[[ -f a ||
// rm x ]]" runs rm there, and "[[ a > b ]]" empties the file b. So the
// reader also keeps that second reading: the words cut at every operator
// and line break (pieces), and the words after < and > (altRedirs).
func (p *parser) testCommand() {
	cmd := &simpleCmd{sep: p.sep, nest: p.nest}
	open, _ := p.readWord()
	cmd.words = append(cmd.words, open)
	piece := []word{open}
	cut := func() {
		if len(piece) > 0 {
			cmd.pieces = append(cmd.pieces, piece)
		}
		piece = nil
	}
	// afterOp is set between an && or || and the word after it. If that
	// word is a test operator (-f, !), the other reading runs a program
	// named "-f", which there is none of; any other word is a program.
	afterOp, pendingRedir := false, ""
	// lastEnd is where the last word read ended: a number that stands
	// right before a < or > is the descriptor to a plain sh, not a word.
	lastEnd := -1
	for !p.failed {
		p.blanks()
		if p.failed {
			return
		}
		if p.pos < len(p.src) && p.src[p.pos] == '\n' {
			p.newline()
			cut()
			cmd.mixed = true
			continue
		}
		rest := p.src[p.pos:]
		switch {
		case rest == "":
			p.fail("a [[ is never closed")
		case strings.HasPrefix(rest, "]]") && (len(rest) == 2 || isWordEnd(rest[2])):
			p.pos += 2
			closer := word{raw: "]]", parts: []wordPart{{lit: "]]"}}}
			cmd.words = append(cmd.words, closer)
			if pendingRedir != "" {
				// "[[ a >]]": to a plain sh the ]] is the file.
				cmd.altRedirs = append(cmd.altRedirs, redir{op: pendingRedir, target: closer})
			} else {
				piece = append(piece, closer)
			}
			if len(cmd.pieces) > 0 {
				cut()
			}
			p.sc.cmds = append(p.sc.cmds, cmd)
			return
		case strings.HasPrefix(rest, "&&"), strings.HasPrefix(rest, "||"):
			p.pos += 2
			cut()
			afterOp = true
		case strings.HasPrefix(rest, "<("), strings.HasPrefix(rest, ">("):
			// A process substitution runs a command even here.
			w, _ := p.readWord()
			cmd.words = append(cmd.words, w)
			piece = append(piece, w)
		case rest[0] == '<' || rest[0] == '>':
			if n := len(piece); n > 0 && lastEnd == p.pos && pendingRedir == "" && isDigits(piece[n-1].raw) {
				piece = piece[:n-1]
			}
			p.pos++
			pendingRedir = rest[:1]
			cmd.mixed = true
		case strings.IndexByte("()|", rest[0]) >= 0:
			p.pos++
			cut()
			cmd.mixed = true
		case rest[0] == ';' || rest[0] == '&':
			p.fail("an operator inside [[ ]]")
		default:
			w, ok := p.readWord()
			if !ok {
				p.fail("a [[ ]] gnosh cannot follow")
				return
			}
			cmd.words = append(cmd.words, w)
			lastEnd = p.pos
			if pendingRedir != "" {
				cmd.altRedirs = append(cmd.altRedirs, redir{op: pendingRedir, target: w})
				pendingRedir = ""
				lastEnd = -1
				continue
			}
			if afterOp {
				if w.raw != "!" {
					afterOp = false
					if !strings.HasPrefix(w.raw, "-") {
						cmd.mixed = true
					}
				}
			}
			piece = append(piece, w)
		}
	}
	if len(cmd.words) > 0 {
		p.sc.cmds = append(p.sc.cmds, cmd)
	}
}

// trailingRedirs reads the redirections written after a compound command
// and keeps them as a command with no words, so that what they write is
// judged like any other redirection.
func (p *parser) trailingRedirs() {
	if p.failed {
		return
	}
	cmd := &simpleCmd{sep: p.sep, nest: p.nest}
	for !p.failed {
		p.blanks()
		if !p.redirAt() {
			break
		}
		p.redir(cmd)
	}
	if len(cmd.redirs) > 0 {
		p.sc.cmds = append(p.sc.cmds, cmd)
	}
}

// simple reads one simple command.
func (p *parser) simple() {
	cmd := &simpleCmd{sep: p.sep, nest: p.nest}
	// ampAts is how many words stood before each &> redirection.
	var ampAts []int
	// shWords are the command's words as a shell that knows no {name}>file
	// has them: the {name} is one of them there. named says there is one.
	var shWords []word
	named := false
	for !p.failed {
		p.blanks()
		if p.pos >= len(p.src) {
			break
		}
		if p.redirAt() {
			amp := strings.HasPrefix(p.src[p.pos:], "&>")
			p.redir(cmd)
			if amp {
				ampAts = append(ampAts, len(shWords))
			}
			continue
		}
		c := p.src[p.pos]
		if c == '(' {
			if len(cmd.words) == 1 && len(cmd.assigns) == 0 && len(cmd.redirs) == 0 && cmd.words[0].plain() {
				// name() body: a function definition.
				p.sc.funcNames = append(p.sc.funcNames, cmd.words[0].joined())
				p.pos++
				p.blanks()
				if !strings.HasPrefix(p.src[p.pos:], ")") {
					p.fail("a parenthesis gnosh cannot place")
					return
				}
				p.pos++
				p.funcBody()
				return
			}
			p.fail("a parenthesis gnosh cannot place")
			return
		}
		procSubst := (c == '<' || c == '>') && strings.HasPrefix(p.src[p.pos+1:], "(")
		if isWordEnd(c) && !procSubst {
			break
		}
		w, ok := p.readWord()
		if !ok || p.failed {
			break
		}
		if isBraceName(w.raw) && p.pos < len(p.src) && (p.src[p.pos] == '<' || p.src[p.pos] == '>') {
			// {name}>file: bash opens the file on a descriptor of its
			// choosing and puts the number in the variable. The word is not
			// an argument, and a variable is set (PATH, say). To a plain
			// sh it is an argument like any, with a redirection after it:
			// "mv . {fd}>log" moves the directory to a place named {fd}.
			p.sc.setsVar = true
			p.sc.loose = append(p.sc.loose, w)
			shWords, named = append(shWords, w), true
			continue
		}
		if len(cmd.words) == 0 {
			if name, ok := assignName(w.raw); ok {
				a := assign{name: name, w: w, fromSubst: w.hasSubst()}
				switch {
				case strings.HasPrefix(p.src[p.pos:], "(") && arrayStart(w.raw, name):
					// name=(word ...): an array.
					a.array = true
					p.pos++
					for !p.failed {
						p.newlines()
						if strings.HasPrefix(p.src[p.pos:], ")") {
							p.pos++
							if p.pos < len(p.src) && !isWordEnd(p.src[p.pos]) {
								p.fail("text right after an array assignment")
							}
							break
						}
						v, ok := p.readWord()
						if !ok {
							p.fail("an array assignment gnosh cannot follow")
							break
						}
						if strings.HasPrefix(v.raw, "[") && !strings.Contains(v.raw, "]") {
							// bash reads a subscript on to its ], past the
							// parenthesis and to the end of the line if it
							// must: "a=([x); b" is not the array it looks
							// like, and without a ] it is no line at all.
							p.fail("an array element that opens a [ with no ] in it")
							break
						}
						a.fromSubst = a.fromSubst || v.hasSubst()
						a.elems = append(a.elems, v)
					}
				case strings.HasPrefix(p.src[p.pos:], "("):
					// "a==(1)", "a=b(1)": a value, and a parenthesis right
					// after it, which the shells refuse.
					p.fail("a parenthesis right after an assignment's value")
				}
				cmd.assigns = append(cmd.assigns, a)
				continue
			}
		}
		cmd.words = append(cmd.words, w)
		shWords = append(shWords, w)
	}
	if p.failed {
		// Keep what was read: a deny rule still applies to it.
		if len(cmd.words) > 0 || len(cmd.assigns) > 0 || len(cmd.redirs) > 0 {
			p.sc.cmds = append(p.sc.cmds, cmd)
		}
		return
	}
	if len(cmd.words) == 0 && len(cmd.assigns) == 0 && len(cmd.redirs) == 0 {
		p.fail("an operator with no command before it")
		return
	}
	switch {
	case len(ampAts) > 0 && ampAts[0] < len(shWords):
		// "a &>file b": to bash, a with the argument b and its output in
		// file. To a shell with no &> (sh, dash) it is a in the
		// background, and then the command b; and each further &> ends
		// a command in the same way ("a &>f b &>g c" is a, b and c).
		from := 0
		for _, at := range append(ampAts, len(shWords)) {
			if at > from || from == 0 {
				cmd.pieces = append(cmd.pieces, shWords[from:at:at])
			}
			from = at
		}
		cmd.mixed = true
	case named:
		// The command as a plain sh has it, the {name} among its words.
		cmd.pieces = append(cmd.pieces, shWords)
	}
	p.sc.cmds = append(p.sc.cmds, cmd)
}

// isDigits reports whether s is made of digits and nothing else.
func isDigits(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return false
		}
	}
	return s != ""
}

// isBraceName reports whether raw is {name}, the word bash takes before a
// redirection as the variable to keep the descriptor's number in.
func isBraceName(raw string) bool {
	if len(raw) < 3 || raw[0] != '{' || raw[len(raw)-1] != '}' || !isNameStart(raw[1]) {
		return false
	}
	for i := 2; i < len(raw)-1; i++ {
		if !isNameChar(raw[i]) {
			return false
		}
	}
	return true
}

// assignName reports whether raw starts like an assignment (name=, name+=
// or name[...]=) and returns the variable's name.
// arrayStart reports whether an assignment word, of the variable name,
// opens an array when a parenthesis follows it: nothing stands after its =
// (name=, name+=, name[sub]=).
func arrayStart(raw, name string) bool {
	rest := raw[len(name):]
	if strings.HasPrefix(rest, "[") {
		end := strings.IndexByte(rest, ']')
		if end < 0 {
			return false
		}
		rest = rest[end+1:]
	}
	return rest == "=" || rest == "+="
}

func assignName(raw string) (string, bool) {
	if raw == "" || !isNameStart(raw[0]) {
		return "", false
	}
	i := 1
	for i < len(raw) && isNameChar(raw[i]) {
		i++
	}
	name := raw[:i]
	if i < len(raw) && raw[i] == '[' {
		end := strings.IndexByte(raw[i:], ']')
		if end < 0 {
			return "", false
		}
		i += end + 1
	}
	if strings.HasPrefix(raw[i:], "=") || strings.HasPrefix(raw[i:], "+=") {
		return name, true
	}
	return "", false
}

// redirAt reports whether a redirection starts at p.pos.
func (p *parser) redirAt() bool {
	i := p.pos
	for i < len(p.src) && p.src[i] >= '0' && p.src[i] <= '9' {
		i++
	}
	if i >= len(p.src) {
		return false
	}
	c := p.src[i]
	if c == '<' || c == '>' {
		// <(cmd) and >(cmd) with nothing before them are words.
		if i == p.pos && i+1 < len(p.src) && p.src[i+1] == '(' {
			return false
		}
		return true
	}
	return i == p.pos && strings.HasPrefix(p.src[i:], "&>")
}

// redirOps are the redirection operators, longest first.
var redirOps = []string{"<<<", "<<-", "<<", "<>", "<&", "<", ">>|", ">>", ">&", ">|", ">", "&>>", "&>"}

// timeWord reports whether a word is one bash's keyword time takes for its
// own when it follows the keyword at once.
func timeWord(w word) bool {
	if !w.static() || w.quoted {
		return false
	}
	switch w.joined() {
	case "-p", "--", "time", "!":
		return true
	}
	return false
}

// timeWords reports whether ws are bash's keyword time and nothing but
// words the keyword takes for its own.
func timeWords(ws []word) bool {
	if len(ws) == 0 || !ws[0].static() || ws[0].quoted || ws[0].joined() != "time" {
		return false
	}
	for _, w := range ws[1:] {
		if !timeWord(w) {
			return false
		}
	}
	return true
}

// cutsTime reports whether bash runs a program named by one of the
// keyword's own words in c, because a redirection stands before it (see
// simpleCmd.timeCut).
func cutsTime(c *simpleCmd) bool {
	return c.timeCut > 0 && c.timeCut < len(c.words) && timeWord(c.words[c.timeCut])
}

// redir reads the redirection at p.pos into cmd.
func (p *parser) redir(cmd *simpleCmd) {
	if cmd.timeCut == 0 && timeWords(cmd.words) {
		cmd.timeCut = len(cmd.words)
	}
	start := p.pos
	for p.pos < len(p.src) && p.src[p.pos] >= '0' && p.src[p.pos] <= '9' {
		p.pos++
	}
	r := redir{fd: p.src[start:p.pos]}
	if len(r.fd) > 1 {
		// bash takes 10>&1 as descriptor ten; dash and other plain shells
		// take the 10 as an argument and redirect descriptor one.
		p.fail("a redirection whose descriptor has more than one digit, which shells read differently")
		return
	}
	for _, op := range redirOps {
		if strings.HasPrefix(p.src[p.pos:], op) {
			r.op = op
			break
		}
	}
	p.pos += len(r.op)
	if r.op == ">>|" {
		// zsh's "append, noclobber or not". To bash and sh it is >> with
		// nothing after it, and the line does not run.
		p.fail("the >>| redirection, which only zsh has")
		return
	}
	p.blanks()
	if p.failed {
		return
	}
	if (r.op == ">&" || r.op == "<&") && p.pos < len(p.src) && p.src[p.pos] == '-' {
		// >&- and <&- close a descriptor. To bash the dash is a token of
		// its own, whatever follows it: "git>&-push" is git push with its
		// output closed.
		r.target = litOf("-")
		p.pos++
		cmd.redirs = append(cmd.redirs, r)
		return
	}
	p.blanks()
	t, ok := p.readWord()
	if p.failed {
		return
	}
	if !ok {
		p.fail("a redirection with no target")
		return
	}
	r.target = t
	if (isInt(t.raw) || isBraceName(t.raw)) && p.pos < len(p.src) && (p.src[p.pos] == '<' || p.src[p.pos] == '>') {
		// "date>2>x": the 2 is the descriptor of the next redirection, and
		// this one has no target. bash refuses the line. "{fd}>{fd}>x" is
		// the same with a name for the descriptor.
		p.fail("a redirection with no target")
		return
	}
	switch r.op {
	case "<<", "<<-":
		if !t.static() || t.glob && !t.quoted {
			p.fail("a here-document delimiter gnosh cannot follow")
			return
		}
		cmd.redirs = append(cmd.redirs, r)
		p.docs = append(p.docs, pendingDoc{
			cmd: cmd, idx: len(cmd.redirs) - 1, delim: t.joined(),
			tabs: r.op == "<<-", lit: t.quoted, depth: p.depth,
		})
		return
	case ">", ">>", ">|", "&>", "&>>":
		if t.raw == "!" {
			// zsh reads >! file as "overwrite file"; bash writes a file
			// named "!". Nobody can say which without knowing the shell.
			p.fail("a redirection to \"!\"")
			return
		}
	}
	cmd.redirs = append(cmd.redirs, r)
}

// crudeFragments cuts a command line at everything that could start a new
// command, without regard to quoting. It is used only on lines the reader
// could not follow, and only to look for what deny and ask rules name: it
// finds too much rather than too little.
func crudeFragments(src string) []string {
	if joined := strings.ReplaceAll(src, "\\\n", ""); joined != src {
		// A backslash-newline pair joins two lines, except in a comment or
		// in single quotes, where the line break still ends a command.
		// Both readings are cut.
		return append(crudeCut(joined), crudeCut(src)...)
	}
	return crudeCut(src)
}

func crudeCut(src string) []string {
	var out []string
	add := func(s string) {
		for {
			s = strings.TrimLeft(s, " \t\r")
			cut := false
			for _, kw := range []string{"do", "then", "else", "elif", "if", "while", "until", "time", "function", "!", "in", "coproc", "-p", "--"} {
				if strings.HasPrefix(s, kw+" ") || strings.HasPrefix(s, kw+"\t") {
					s = s[len(kw)+1:]
					cut = true
				}
			}
			if !cut {
				break
			}
		}
		s = strings.TrimRight(s, " \t\r\\")
		if s == "" {
			return
		}
		out = append(out, s)
		if bare := strings.NewReplacer("'", "", "\"", "", "\\", "").Replace(s); bare != s && bare != "" {
			out = append(out, bare)
		}
	}
	start := 0
	for i := 0; i < len(src); i++ {
		// A ${...} is part of the word it stands in, not a place to cut:
		// "rsync -a --${x}delete src/ ." is one piece (oracle seed 100).
		if src[i] == '$' && i+1 < len(src) && src[i+1] == '{' {
			if end := strings.IndexByte(src[i+2:], '}'); end >= 0 && !strings.ContainsAny(src[i+2:i+2+end], "\n;&|`") {
				i += 2 + end
				continue
			}
		}
		switch src[i] {
		case '\n', ';', '&', '|', '(', ')', '`', '{', '}', '<', '>':
			add(src[start:i])
			start = i + 1
		}
	}
	add(src[start:])
	// A fragment may open with assignments; rules about the command after
	// them must still be found.
	for _, s := range out {
		rest := s
		for {
			sp := strings.IndexAny(rest, " \t")
			if sp < 0 {
				break
			}
			if _, ok := assignName(rest[:sp]); !ok {
				break
			}
			rest = strings.TrimLeft(rest[sp:], " \t")
			if rest != "" {
				out = append(out, rest)
			}
		}
	}
	return out
}
