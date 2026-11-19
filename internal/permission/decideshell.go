package permission

import (
	"cmp"
	"errors"
	"path"
	"slices"
	"strconv"
	"strings"
)

// lineState follows the shell's directory along one command line.
type lineState struct {
	// dirs are the directories the next command may be running in.
	dirs []string
	// pending is where a cd that just ran would leave the shell, and sure
	// says that it is the shell's own cd, standing by itself: not behind
	// a wrapper (the cd of "nohup cd x" is a program that moves nobody
	// but itself), not one side of a pipe (which runs in a shell of its
	// own), not under a ! (which turns its outcome round) and not after
	// an || (which skips it when what stands before went well: "a || cd
	// d && c" runs c where the line was). Only after such a cd does &&
	// mean that the shell went there; after anything else it may be in
	// either place.
	pending []string
	sure    bool
	// before are the places the line was in ahead of the cds of the &&
	// chain it is in. A cd that fails ends its chain, and what comes
	// after the chain then runs where the line was before it: "cd nope
	// && ls; cat ../x" reads ../x from the directory the line began in.
	before []string
	// moved is set once a cd has come earlier in the line that may have
	// gone somewhere other than start, where the line began: "cd
	// /the/same/folder && git log" goes nowhere, as models often write.
	moved bool
	start string
	// unknown is set when the directory can no longer be worked out.
	unknown bool
	// made are the places an earlier cp or mv of the line puts things,
	// and linked is set once an earlier ln has run. What is there when a
	// later command reads it was not there when the line was judged: a
	// link that was copied or moved in leads wherever it leads.
	made   []string
	linked bool
}

// judgeLooped judges the command at index i of the line, a command in the
// body of a for loop that names the loop's variable once for each value
// the list gives it (filledArgs), every one of them having to pass. A
// command whose redirection names the variable, or whose words cannot be
// filled in, is not judged that way and asks.
func (e *Engine) judgeLooped(v *bashView, i int, c *simpleCmd, st *lineState) partVerdict {
	var names []string
	for _, l := range v.sc.loops {
		if !l.args && l.from <= i && i < l.to && usesVar(c.words, l.name) {
			for _, w := range l.words {
				if !w.plain() && !(w.static() && !w.glob && !w.brace && !w.tilde) {
					// A pattern in the list is file names, any of which may
					// begin with a dash and be a flag where the body puts
					// it ("for f in *; do cp a.txt "$f"x; done" ran cp
					// a.txt -Rx: oracle). Only words as written are
					// filled in.
					return partVerdict{why: "the loop's variable $" + l.name + " takes values that are file names a pattern stands for, or are computed"}
				}
			}
			names = append(names, l.name)
		}
	}
	if len(names) == 0 {
		return e.judgePart(c, st)
	}
	for _, r := range c.redirs {
		for _, n := range names {
			if usesVar([]word{r.target}, n) {
				return partVerdict{why: "a redirection names a file by the loop's variable $" + n}
			}
		}
	}
	alts, _ := e.filledArgs(v, c.words)
	if len(alts) == 0 {
		return partVerdict{why: "the loop's variable $" + names[0] + " is given values gnosh cannot tell"}
	}
	var first partVerdict
	for k, alt := range alts {
		one := *c
		one.words = alt
		pv := e.judgePart(&one, st)
		if !pv.ok {
			// (A rule for the command as written could not cover what it
			// becomes for each value: no rule is offered.)
			pv.exact, pv.prefix = "", ""
			return pv
		}
		switch {
		case k == 0:
			first = pv
		case first.rule == nil && pv.rule != nil:
			// A value an allow rule let through, after values that only
			// read: the loop runs because of the rule, and says so
			// (oracle seed 115: "for f in a.txt <outside>/secret.txt; do
			// strings "$f"; done" was put down to only reading).
			first.rule = pv.rule
		}
	}
	return first
}

// usesVar reports whether a word of ws names the variable name.
func usesVar(ws []word, name string) bool {
	for _, w := range ws {
		for _, p := range w.parts {
			if p.kind == partVar && p.name == name {
				return true
			}
			if p.body != nil && usesVar([]word{*p.body}, name) {
				return true
			}
		}
	}
	return false
}

// changesDir reports whether a command of the line may change the shell's
// directory: cd, pushd or popd, by name or behind builtin or command, or a
// program whose name is computed when the line runs.
func changesDir(v *bashView) bool {
	for _, c := range v.sc.cmds {
		ws := c.words
		for len(ws) > 1 && ws[0].static() && (ws[0].joined() == "builtin" || ws[0].joined() == "command") {
			ws = ws[1:]
		}
		if len(ws) == 0 {
			continue
		}
		if !ws[0].static() {
			return true
		}
		switch baseName(ws[0].joined()) {
		case "cd", "pushd", "popd":
			return true
		}
	}
	return false
}

// copyPlace is a copy of where the line stands, for putting it back.
func (st *lineState) copyPlace() lineState {
	return lineState{
		dirs: slices.Clone(st.dirs), pending: slices.Clone(st.pending), sure: st.sure,
		before: slices.Clone(st.before), moved: st.moved, unknown: st.unknown,
	}
}

// leave puts back where the line stood when a subshell began. What the
// subshell's commands did to the files (made, linked) stays.
func (st *lineState) leave(was lineState) {
	st.dirs, st.pending, st.sure = was.dirs, was.pending, was.sure
	st.before, st.moved, st.unknown = was.before, was.moved, was.unknown
}

// enter is called before judging c: it settles where the shell is.
func (st *lineState) enter(c *simpleCmd) {
	if st.before != nil && c.sep != sepAnd && c.sep != sepPipe {
		// The chain is over, and this command runs whether or not the
		// cds in it went anywhere.
		st.dirs = unionStrings(st.dirs, st.before)
		st.before = nil
	}
	if st.pending == nil {
		return
	}
	if c.sep == sepAnd && st.sure {
		st.before = unionStrings(st.before, st.dirs)
		st.dirs = st.pending
	} else {
		st.dirs = unionStrings(st.dirs, st.pending)
	}
	for _, d := range st.pending {
		if d != st.start {
			st.moved = true
		}
	}
	st.pending = nil
}

func unionStrings(a, b []string) []string {
	out := append([]string(nil), a...)
	for _, s := range b {
		found := false
		for _, o := range out {
			if o == s {
				found = true
				break
			}
		}
		if !found {
			out = append(out, s)
		}
	}
	return out
}

// lineDirs returns every directory a command of the line might run in:
// the shell's current one and wherever a cd in the line leads. It is used
// where being wrong must err on the strict side (deny rules, dangerous
// removals), so order and && are ignored. unknown is set when a cd leads
// somewhere that cannot be told ahead of time.
func (e *Engine) lineDirs(v *bashView) (dirs []string) {
	dirs, _ = e.lineDirsKnown(v)
	return dirs
}

func (e *Engine) lineDirsKnown(v *bashView) (dirs []string, unknown bool) {
	return e.dirsOf(v, true)
}

// dirsOf is lineDirsKnown; withFlags counts the folders programs go to by
// flags of their own (env -C, git -C), where the shell itself never is.
func (e *Engine) dirsOf(v *bashView, withFlags bool) (dirs []string, unknown bool) {
	dirs = []string{e.cur}
	// In the order the line runs them: a cd found inside a command ("command
	// cd sub", eval 'cd sub') where that command stands (oracle seed 137).
	units := slices.Clone(v.units)
	slices.SortStableFunc(units, func(a, b *unit) int { return cmp.Compare(a.origin, b.origin) })
	for _, u := range units {
		if u.cmd.flagCd && !withFlags {
			continue
		}
		ws := u.cmd.words
		if len(ws) == 0 || !ws[0].static() {
			continue
		}
		// (popd goes back to a directory the line was in before, and so
		// does pushd with no directory or with a turn of its stack: +1.)
		pushd := ws[0].joined() == "pushd"
		if ws[0].joined() != "cd" && !pushd {
			continue
		}
		v.moves = true
		// The directory is the word that is not an option. Where there
		// are several (a variable the line gave several values), each
		// one may be it.
		var targets []*word
		for i := range ws[1:] {
			w := &ws[1+i]
			l := w.lit()
			if w.static() && (l == "-L" || l == "-P" || l == "-e" || l == "-@" || l == "--" || pushd && l == "-n") {
				continue
			}
			if pushd && w.static() && len(l) > 1 && (l[0] == '+' || l[0] == '-') && isDigits(l[1:]) {
				continue
			}
			targets = append(targets, w)
			// A variable the line set is where it was set to, each value
			// in its turn.
			vals, _, _ := e.filledAll(v, *w)
			if len(vals) > maxLineDirs || e.tooManyFills(v, *w) {
				v.dirsCapped, vals = true, nil
			}
			for i := range vals {
				dir := litOf(vals[i].text)
				targets = append(targets, &dir)
			}
		}
		switch {
		case len(targets) == 0 && pushd:
		case len(targets) == 0 && len(ws) == 1 && e.home != "":
			dirs = unionStrings(dirs, []string{e.home})
		case len(targets) == 0:
			unknown = true
		}
		if len(targets) > 1 {
			unknown = true
		}
		for _, target := range targets {
			e.lineDir(target, &dirs, &unknown)
		}
	}
	return dirs, unknown
}

// maxLineDirs bounds how many directories a line is followed to. A line
// that goes to more asks where a rule about files is in force.
const maxLineDirs = 32

// lineDir adds the directory a cd or a pushd names to the ones the line
// may be in.
func (e *Engine) lineDir(target *word, into *[]string, lost *bool) {
	dirs := *into
	if rest, ok := dirTilde(*target); ok {
		// A directory the line is in or was in (nothing new), or one
		// under such a directory.
		for _, d := range append([]string(nil), dirs...) {
			if abs, ok := cleanAbs(d+rest, ""); ok && rest != "" && len(dirs) < maxLineDirs {
				dirs = unionStrings(dirs, []string{abs})
			}
		}
		*into = dirs
		return
	}
	if !target.static() {
		// "cd ${X:+/etc}" goes to /etc where X is set.
		for i, p := range target.parts {
			if p.kind == partVar && p.body != nil && len(dirs) < maxLineDirs {
				alt := withBody(*target, i, e.home)
				e.lineDir(&alt, &dirs, lost)
			}
		}
	}
	if !target.static() && !target.quoted && target.unset() == "" && e.home != "" && len(dirs) < maxLineDirs {
		// "cd $X" with nothing in X is a cd with no word after it: it
		// goes home.
		dirs = unionStrings(dirs, []string{e.home})
	}
	if !target.static() || target.joined() == "-" || len(dirs) >= maxLineDirs {
		*lost = true
		// A variable nobody set is empty: "cd /e${x}tc" goes to /etc.
		if l := target.unset(); l != "" && l != "-" && (!target.tilde || l == "~" || strings.HasPrefix(l, "~/")) && len(dirs) < maxLineDirs {
			for _, d := range append([]string(nil), dirs...) {
				if abs, ok := cleanAbs(e.unsetValue(*target), d); ok {
					dirs = unionStrings(dirs, []string{abs})
				}
			}
		}
		*into = dirs
		return
	}
	for _, d := range append([]string(nil), dirs...) {
		if abs, ok := cleanAbs(target.value(e.home), d); ok {
			dirs = unionStrings(dirs, []string{abs})
		}
	}
	*into = dirs
}

// absAll returns w's value as an absolute path for each directory the
// command may run in. ok is false when the word is not a plain path.
func (e *Engine) absAll(w word, dirs []string) (out []string, pattern, ok bool) {
	if !w.static() || w.brace {
		return nil, false, false
	}
	val := w.value(e.home)
	if val == "" {
		return nil, false, false
	}
	// On Linux /proc/self/cwd is the directory of the process that asks:
	// the command's own, wherever the line has taken it.
	for _, proc := range []string{"/proc/self/cwd", "/proc/thread-self/cwd"} {
		if val == proc || strings.HasPrefix(val, proc+"/") {
			val = "." + val[len(proc):]
		}
	}
	for _, d := range dirs {
		abs, ok := cleanAbs(val, d)
		if !ok {
			return nil, false, false
		}
		out = unionStrings(out, []string{abs})
		// The kernel does not read "link/.." the way the text does: it
		// goes to the link's target first and takes the parent of that.
		// Both places count.
		phys, ok := e.physical(val, d)
		if !ok {
			return nil, false, false
		}
		out = unionStrings(out, []string{phys})
	}
	return out, w.glob && hasPathMagic(val), true
}

// dotMatches returns what a pattern names through "." and "..": in dash,
// and in bash before 5.2, a segment such as ".*" matches both, so that
// "ls .*" lists the directory above and "cat .*/x" reads from it. A way
// to expand patterns that lists a directory does not find those two.
// ok is false when a pattern it had to expand on the way could not be.
func (e *Engine) dotMatches(pattern string) (out []string, ok bool) {
	segs := splitPath(pattern)
	ok = true
	for i, seg := range segs {
		if !strings.HasPrefix(seg, ".") || !hasPathMagic(seg) {
			continue
		}
		for _, name := range []string{".", ".."} {
			if !segMatch(seg, name) {
				continue
			}
			rest := append(append(append([]string(nil), segs[:i]...), name), segs[i+1:]...)
			p := cleanPath("/" + strings.Join(rest, "/"))
			if !hasPathMagic(strings.Join(segs[i+1:], "/")) {
				out = append(out, p)
			} else if ms, err := e.expandGlob(p); err == nil {
				out = append(out, ms...)
			} else {
				ok = false
			}
		}
	}
	return out, ok
}

// expandGlob asks Options.Glob what a pattern matches, and asks it only
// what every way of matching patterns reads alike. Shells write "not
// these" as [!abc]; Go's path matching, which is what a Glob is likely
// to be made of, takes that for the characters ! a b c, and both take
// [^abc] for "not these": so that is how it is handed over. A named
// class ([[:alpha:]]), a ] that opens a set, a slash inside one and a
// backslash are read differently by different matchers, and a set that
// never closes is text to a shell and a mistake to others: for those it
// reports an error, and the caller asks. A pattern whose only mark is a
// [ that never closes names one file, itself, and matches nothing else.
func (e *Engine) expandGlob(pattern string) ([]string, error) {
	if e.glob == nil {
		return nil, errors.New("no way to expand patterns")
	}
	var b strings.Builder
	others, open := false, false
	for i := 0; i < len(pattern); i++ {
		c := pattern[i]
		switch c {
		case '\\':
			return nil, errors.New("a backslash in a file name pattern")
		case '*', '?':
			others = true
			b.WriteByte(c)
		case '[':
			j := i + 1
			neg := j < len(pattern) && (pattern[j] == '!' || pattern[j] == '^')
			if neg {
				j++
			}
			end := strings.IndexByte(pattern[j:], ']')
			switch {
			case end < 0:
				open = true
				b.WriteByte(c)
				continue
			case end == 0:
				return nil, errors.New("a ] that opens a set in a file name pattern")
			}
			body := pattern[j : j+end]
			if strings.ContainsAny(body, "[/!^") {
				return nil, errors.New("a set in a file name pattern that matchers read differently")
			}
			b.WriteByte('[')
			if neg {
				b.WriteByte('^')
			}
			b.WriteString(body)
			b.WriteByte(']')
			others = true
			i = j + end
		default:
			b.WriteByte(c)
		}
	}
	switch {
	case open && others:
		return nil, errors.New("a [ that never closes in a file name pattern")
	case open:
		return nil, nil
	}
	return e.glob(b.String())
}

// dotSettings are the names of the settings that make a shell match
// names that start with a dot with * and ?: bash's dotglob (also by way
// of BASHOPTS) and GLOBIGNORE, zsh's GLOB_DOTS, ksh's FIGNORE.
var dotSettings = []string{"dotglob", "globignore", "globdots", "glob_dots", "bashopts", "fignore"}

// namesDotSetting reports whether a text holds one of dotSettings.
func namesDotSetting(text string) bool {
	// (Quotes, backslashes and line breaks may stand inside the name.)
	text = strings.ToLower(strings.NewReplacer("'", "", "\"", "", "\\", "", "\n", "").Replace(text))
	for _, s := range dotSettings {
		if strings.Contains(text, s) {
			return true
		}
	}
	return false
}

// hidesDots reports whether the shell that runs the line is sure to
// leave names that start with a dot out of what a * or a ? matches, as
// bash and sh do until they are told otherwise. Options.Glob may return
// those names (see doc.go), and "cat *" would then be held to a rule
// about .env that it never touches. The answer is yes only for a line
// that was read to its end, runs nothing to build itself, starts no
// shell, reads no file into this one, and names none of those settings
// in any word, however the word is put together.
func (e *Engine) hidesDots(v *bashView) bool {
	if !v.sc.ok || v.opaque != "" || v.tooMany || len(v.unread) > 0 || v.sc.subst {
		return false
	}
	named := func(w word) bool {
		if namesDotSetting(w.raw) || namesDotSetting(w.joined()) || namesDotSetting(w.unset()) {
			return true
		}
		vals, _, _ := e.filledAll(v, w)
		for _, val := range vals {
			if namesDotSetting(val.text) {
				return true
			}
		}
		return false
	}
	for _, a := range v.sc.arith {
		// (( $x = 1 )) sets whatever x names.
		if namesDotSetting(a) || strings.Contains(a, "$") {
			return false
		}
	}
	for _, w := range v.sc.loose {
		if named(w) {
			return false
		}
	}
	for _, l := range v.sc.loops {
		if namesDotSetting(l.name) {
			return false
		}
	}
	// A variable whose value is not known for sure may hold the
	// setting's name, for a builtin or a sum to use.
	for name := range v.vars {
		if !e.closedVar(v, name) {
			return false
		}
	}
	for _, u := range v.units {
		c := u.cmd
		for _, a := range c.assigns {
			if named(a.w) {
				return false
			}
			for _, el := range a.elems {
				if named(el) {
					return false
				}
			}
		}
		for _, r := range c.redirs {
			if named(r.target) || namesDotSetting(r.body) {
				return false
			}
		}
		if len(c.words) == 0 {
			continue
		}
		name, known := e.progName(c.words[0])
		if !known || progIsPattern(c.words[0]) {
			return false
		}
		k := progKey(baseName(name))
		switch {
		case shellNames[k], k == "source", k == ".", k == "shopt", k == "set", k == "setopt", k == "emulate", k == "enable":
			return false
		}
		// A builtin that gives a variable its value, or works one out
		// as a number, may be handed the setting's name in a word that
		// is only known when the line runs.
		sets := evaluators[k] || k == "mapfile" || k == "readarray"
		for _, w := range c.words {
			if named(w) || strings.Contains(w.raw, "$((") || strings.Contains(w.raw, "$[") {
				return false
			}
			if sets && !w.static() && !e.closedWord(v, w, anyTime) {
				return false
			}
		}
	}
	return true
}

// dropHidden takes out of a pattern's matches the names a shell does not
// match: those with a part that starts with a dot where the pattern's
// own part does not. A match that cannot be laid beside the pattern
// part for part is kept.
func dropHidden(pattern string, matches []string) []string {
	pp := strings.Split(pattern, "/")
	var out []string
	for _, m := range matches {
		mp := strings.Split(m, "/")
		hidden := false
		if len(mp) == len(pp) {
			for i := range mp {
				if strings.HasPrefix(mp[i], ".") && !strings.HasPrefix(pp[i], ".") {
					hidden = true
					break
				}
			}
		}
		if !hidden {
			out = append(out, m)
		}
	}
	return out
}

// physical returns the path p names when read from dir the way the
// kernel reads it: each ".." is taken from where the path before it
// really leads, links followed. With no way to follow links it is the
// path with its dots taken out by the text alone. It reports false when
// a link on the way cannot be followed.
func (e *Engine) physical(p, dir string) (string, bool) {
	if !strings.HasPrefix(p, "/") {
		if dir == "" {
			return "", false
		}
		p = dir + "/" + p
	}
	if e.resolve == nil || !strings.Contains(p, "..") {
		return cleanAbs(p, "")
	}
	cur := "/"
	for _, seg := range strings.Split(p, "/") {
		switch seg {
		case "", ".":
		case "..":
			r, err := e.resolve(cur)
			if err != nil || !strings.HasPrefix(r, "/") {
				return "", false
			}
			cur = parentDir(cleanPath(r))
		default:
			if cur == "/" {
				cur += seg
			} else {
				cur += "/" + seg
			}
		}
	}
	return cur, true
}

// climbsAfterMagic reports whether a path climbs with ".." after a part
// that is a pattern: "*/../x", "src/a*/../../y".
func climbsAfterMagic(p string) bool {
	magic := false
	for _, seg := range strings.Split(p, "/") {
		switch {
		case seg == ".." && magic:
			return true
		case hasPathMagic(seg):
			magic = true
		}
	}
	return false
}

// maxClimbPaths is the most paths climbMatches follows.
const maxClimbPaths = 4096

// climbMatches expands a pattern that climbs with ".." after a part that
// is a pattern, as the shell and the kernel read it: each part with magic
// in it is matched against what is there, and each ".." is taken from
// where the path before it really leads, links followed (physical). What
// comes out are cleaned absolute paths. ok is false when a part could not
// be expanded, a link could not be followed, or there were more paths
// than gnosh follows.
func (e *Engine) climbMatches(p, dir string) ([]string, bool) {
	if !strings.HasPrefix(p, "/") {
		if dir == "" {
			return nil, false
		}
		p = dir + "/" + p
	}
	paths := []string{"/"}
	for _, seg := range strings.Split(p, "/") {
		switch {
		case seg == "" || seg == ".":
		case seg == "..":
			for i, q := range paths {
				ph, ok := e.physical(q+"/..", "")
				if !ok {
					return nil, false
				}
				paths[i] = ph
			}
		case hasPathMagic(seg):
			var next []string
			for _, q := range paths {
				ms, err := e.expandGlob(path.Join(q, seg))
				if err != nil {
					return nil, false
				}
				next = append(next, ms...)
			}
			paths = next
		default:
			for i, q := range paths {
				paths[i] = path.Join(q, seg)
			}
		}
		if len(paths) > maxClimbPaths {
			return nil, false
		}
	}
	return paths, true
}

// parentDir returns the directory a cleaned absolute path lies in.
func parentDir(p string) string {
	i := strings.LastIndexByte(p, '/')
	if i <= 0 {
		return "/"
	}
	return p[:i]
}

// shellPaths lists the files a command line names for reading and for
// writing, so that Read and Edit rules and the protected paths can be
// held against them.
func (e *Engine) shellPaths(req *request) {
	v := req.view
	dirs, lost := e.lineDirsKnown(v)
	if lost {
		req.pathOpaque = "it changes directory to a place gnosh cannot tell ahead of time"
	}
	if v.dirsCapped || len(dirs) >= maxLineDirs {
		req.pathCapped = "the line changes directory more often than gnosh follows"
	}
	for _, name := range []string{"CDPATH", "GLOBIGNORE"} {
		// Assigned anywhere in the line, these change where cd goes and
		// what a pattern matches.
		for _, c := range v.sc.cmds {
			for _, a := range c.assigns {
				if a.name == name {
					req.pathOpaque = "it sets " + name + ", which changes what the files it names are"
				}
			}
		}
	}
	v.pwds = dirs
	var reads, writes []string
	hide := e.hidesDots(v)
	// at is where the command whose words are being read stands among
	// the line's own, or noPlace for one found inside another: only for
	// the line's own is it known which assignments come before it.
	at := noPlace
	var add func(w word, into *[]string)
	add = func(w word, into *[]string) {
		if w.static() && (w.joined() == "-" || w.joined() == "/dev/null") {
			return
		}
		if w.brace && w.static() && !w.quoted {
			// Braces are words bash makes before anything else, and they
			// can be worked out: "cat {.env,x}" names .env. (A plain sh
			// keeps the braces, so the word as written is a name too.)
			if alts, ok := braceExpand(w.joined()); ok {
				for _, alt := range append(alts, w.joined()) {
					if alt == "" {
						continue
					}
					part := litOf(alt)
					part.glob, part.tilde = w.glob && hasPathMagic(alt), strings.HasPrefix(alt, "~")
					add(part, into)
				}
				return
			}
		}
		if alts, ok := braceAlts(w); ok {
			// "cat {$F,x}": each word the braces make is read by itself.
			for _, alt := range alts {
				add(alt, into)
			}
			return
		}
		if rest, ok := dirTilde(w); ok {
			// ~+ is the directory the shell is in, ~- the one it was in
			// before and ~2 one that pushd put by: each is one of those
			// the line may be in.
			if w.joined() != "~+"+rest && !v.moves {
				return // no cd on the line: the word stays as it is written
			}
			for _, d := range dirs {
				part := litOf(d + rest)
				part.glob = w.glob && hasPathMagic(rest)
				add(part, into)
			}
			if lost {
				req.pathOpaque = "it names a file by where the line is (" + w.raw + "), and that is not known"
			}
			return
		}
		if lv := v.vars["HOME"]; lv != nil && w.tilde && w.static() {
			// The line moved HOME: ~ is that place now.
			for _, home := range lv.vals {
				if home.text == "" {
					continue
				}
				if a, ok := cleanAbs(home.text+strings.TrimPrefix(w.joined(), "~"), ""); ok {
					*into = append(*into, a)
				}
			}
		}
		if into == &writes && !w.static() && req.writeOpaque == "" && !e.closedWord(v, w, at) {
			// Whatever is made of the text the line computes, the file
			// written cannot be told.
			req.writeOpaque = "it writes to a file named by text computed when it runs (" + w.raw + ")"
		}
		abs, pattern, ok := e.absAll(w, dirs)
		switch {
		case !ok:
			// (Not so where the line itself gave the word's variables all
			// the values they can have: those are tried below, and are
			// all there is to try.)
			if e.tooManyFills(v, w) {
				req.pathCapped = "the line gives " + w.raw + " more values than gnosh follows"
			}
			if !e.closedWord(v, w, at) {
				req.pathOpaque = "it names a file with text computed when the command runs (" + w.raw + ")"
				// The word a ${name:-word} or a ${name:+word} may stand
				// for is a name as well: "cat ${HOME:+.env}" reads .env.
				for i, p := range w.parts {
					if p.kind == partVar && p.body != nil {
						add(withBody(w, i, e.home), into)
					}
				}
			}
			// A variable the line itself set is tried as what it was set
			// to: "x=.env; cat $x". And a variable nobody set is empty, so
			// what is written of the name is a name too: "cat .e${x}nv".
			// Either way the shell then cuts an unquoted value into words
			// and matches the patterns in it: "x='.e*'; cat $x".
			if l := w.unset(); !w.brace && (!w.tilde || l == "~" || strings.HasPrefix(l, "~/")) {
				vals, _, _ := e.filledAll(v, w)
				if e.glob != nil && mixesPattern(v, w) {
					// A variable that went through what a pattern matched,
					// with more text around it: the names are that text
					// around each match ("for f in *.txt; do cp $f
					// new_$f; done" writes new_a.txt), and not what a
					// pattern made of the two would match.
					named, _, ok := e.filledWith(v, w, func(pattern string) ([]string, bool) { return e.matchNames(pattern, dirs, hide) })
					if ok {
						vals = named
					} else {
						req.pathOpaque = "it names files by what a pattern matched earlier in the line (" + w.raw + "), and those could not be worked out"
					}
				}
				known := len(vals)
				if !e.closedWord(v, w, at) {
					// (One of them may still be as it came, or not set.)
					vals = append(vals, lineVal{text: e.unsetValue(w)})
				}
				if known == 0 {
					// One of the line's variables beside one nobody set:
					// "d=.env; cat $X$d" reads .env.
					parts, capped := e.partFills(v, w, at)
					if capped {
						req.pathCapped = "the line gives " + w.raw + " more values than gnosh follows"
					}
					for _, pw := range parts {
						vals = append(vals, lineVal{text: e.unsetValue(pw), glob: pw.glob})
					}
				}
				for i, val := range vals {
					fields := []string{val.text}
					if i < known && !w.quoted {
						fields = e.fields(v, val.text)
					}
					for _, f := range fields {
						if f == "" {
							continue
						}
						part := litOf(f)
						part.glob = hasPathMagic(f) && (w.glob || val.glob || i < known && !w.quoted)
						add(part, into)
					}
				}
			}
		case !pattern:
			*into = append(*into, abs...)
		case e.glob == nil:
			req.pathOpaque = "it names files with a pattern (" + w.raw + ")"
			// A pattern that matches nothing stays as it is written: the
			// text itself is a name too ("echo x >build/[").
			*into = append(*into, abs...)
		default:
			*into = append(*into, abs...)
			for _, a := range abs {
				ms, err := e.expandGlob(a)
				dots, ok := e.dotMatches(a)
				if err != nil || !ok {
					req.pathOpaque = "it names files with a pattern that could not be expanded (" + w.raw + ")"
				}
				if hide {
					ms = dropHidden(a, ms)
				}
				*into = append(*into, ms...)
				*into = append(*into, dots...)
			}
			// The text takes "*/.." out, and the kernel does not: each
			// ".." after a pattern is taken from where each match really
			// leads, so "cat */../x" through a link to a directory outside
			// reads the x beside that directory.
			if val := w.value(e.home); climbsAfterMagic(val) {
				for _, d := range dirs {
					ms, ok := e.climbMatches(val, d)
					if !ok {
						req.pathOpaque = "it names files with a pattern that climbs with .. from what it matched, and that could not be followed (" + w.raw + ")"
					}
					*into = append(*into, ms...)
				}
			}
		}
	}
	for _, u := range v.units {
		at = noPlace
		if !u.derived {
			at = v.place(u.cmd)
		}
		if flag := listFlag(u.cmd.words); flag != "" && req.pathOpaque == "" {
			req.pathOpaque = "it reads the files a list names (" + flag + "), and the list is only read when it runs"
		}
		if why := hiddenWrites(u.cmd.words); why != "" && req.pathOpaque == "" {
			req.pathOpaque = why + ", which are only known when it runs"
		}
		if len(u.cmd.words) > 0 {
			w0 := u.cmd.words[0]
			if _, known := e.progName(w0); !known || progIsPattern(w0) {
				// A program that is only known when the line runs may be
				// cat, and what it would read is not known either. (One
				// the line itself set is read as what it was set to.)
				if name, ok := e.filled(v, w0); !ok || name == "" {
					req.pathOpaque = "the program it runs (" + w0.raw + ") is only known when it runs, and so are the files that program reads"
				}
				// What is not written of the name may be a blank:
				// "cat${s}.env" is then cat .env.
				if alt := spaceReading(u.cmd); alt != nil {
					f := facts(alt.words)
					for _, w := range f.reads {
						add(w, &reads)
					}
					for _, w := range f.writes {
						add(w, &writes)
					}
				}
			}
		}
		f := facts(u.cmd.words)
		for _, w := range f.reads {
			add(w, &reads)
		}
		for _, w := range f.writes {
			add(w, &writes)
		}
		for _, w := range f.lands {
			add(w, &writes)
		}
		if trees, copies := treeOps(u.cmd.words); len(trees)+len(copies) > 0 {
			e.treeGuards(trees, copies, dirs, req, &writes)
		}
		// A file written onto a flag, in a word that is not all written
		// out: "awk -f${X:-.env}", "sort -o$X.env", "tool --config=${X:-.env}".
		// The flag is read as it is when no variable in it is set, and a
		// file it then names is one more the command may read or write;
		// what the variable holds otherwise, nobody knows.
		if alt, changed := e.unsetFlags(u.cmd.words); changed {
			fa := facts(alt)
			if len(fa.reads)+len(fa.writes) > len(f.reads)+len(f.writes) && req.pathOpaque == "" {
				req.pathOpaque = "it names a file in a flag with text computed when the command runs (" + rawJoined(u.cmd.words) + ")"
			}
			for _, w := range fa.reads {
				add(w, &reads)
			}
			for _, w := range fa.writes {
				add(w, &writes)
			}
		}
		// A name that starts with a dash is a flag to the command it is
		// handed to, and the words beside it are then other things than
		// they seem: with a file named -R in the directory, "for f in *;
		// do cp a.txt b.txt $f; done" copies a.txt over b.txt.
		dashed, capped := e.dashReadings(v, u.cmd.words, dirs, hide)
		if capped {
			req.pathCapped = "the line hands " + rawJoined(u.cmd.words[:1]) + " more names that look like flags than gnosh follows"
		}
		for _, alt := range dashed {
			fa := facts(alt)
			for _, w := range fa.reads {
				add(w, &reads)
			}
			for _, w := range fa.writes {
				add(w, &writes)
			}
		}
		for _, r := range u.cmd.redirs {
			switch r.op {
			case "<":
				add(r.target, &reads)
			case ">", ">>", ">|", "&>", "&>>", "<>":
				add(r.target, &writes)
			case ">&":
				if !isFdTarget(r.target) {
					add(r.target, &writes)
					// bash expands this word a second time: what the first
					// expansion leaves is read again, tilde and variables
					// and all ("echo x >&'~/.bashrc'" writes in the home
					// directory).
					if r.target.static() {
						if again, ok := rereadWord(r.target.joined()); ok {
							add(again, &writes)
						} else {
							req.pathOpaque = "it writes to " + r.target.raw + " through >&, a word bash expands twice"
						}
					}
				}
			}
		}
	}
	if v.opaque != "" && req.pathOpaque == "" {
		// Where not every command is known, neither is every file.
		req.pathOpaque = v.opaque
	}
	if !v.sc.ok {
		// A line the reader could not follow may name files it never got
		// to. Its crude pieces are read as commands, which finds too much
		// rather than too little, and what else it names is not known.
		if req.pathOpaque == "" {
			req.pathOpaque = "gnosh could not read the whole command"
		}
		for _, frag := range v.frags {
			var ws []word
			for _, fld := range strings.Fields(frag) {
				ws = append(ws, litOf(fld))
			}
			f := facts(ws)
			for _, w := range f.reads {
				if abs, _, ok := e.absAll(w, dirs); ok {
					reads = append(reads, abs...)
				}
			}
			for _, w := range f.writes {
				if abs, _, ok := e.absAll(w, dirs); ok {
					writes = append(writes, abs...)
				}
			}
		}
	}
	req.reads = e.expand(req, reads)
	req.writes = e.expand(req, writes)
	req.unresolved = false // links matter here only to the checks that allow
}

// dashReadings returns the command once for each name that starts with a
// dash among those one of its words may turn into: a file a pattern
// matches, or a value the line gave a variable (which may itself be a
// pattern, or went through one). Each has that name written out in the
// word's place, where a command takes it for a flag. capped says there
// were more of them than gnosh follows.
func (e *Engine) dashReadings(v *bashView, ws []word, dirs []string, hide bool) (out [][]word, capped bool) {
	const max = 24
	for i, w := range ws {
		if i == 0 || w.quoted && w.static() {
			continue
		}
		var names []string
		expand := func(pattern string) {
			if e.glob == nil {
				return
			}
			if ms, ok := e.matchNames(pattern, dirs, hide); ok {
				names = append(names, ms...)
			}
		}
		switch {
		case w.static() && w.glob && !w.brace:
			expand(w.joined())
		case !w.static():
			vals, _, _ := e.filledAll(v, w)
			if e.glob != nil && mixesPattern(v, w) {
				if named, _, ok := e.filledWith(v, w, func(pattern string) ([]string, bool) { return e.matchNames(pattern, dirs, hide) }); ok {
					vals = named
				}
			}
			for _, val := range vals {
				if val.glob && !w.quoted || val.glob && hasPathMagic(val.text) {
					expand(val.text)
				}
				names = append(names, val.text)
			}
		}
		seen := map[string]bool{}
		for _, name := range names {
			if !strings.HasPrefix(name, "-") || name == "-" || seen[name] {
				continue
			}
			seen[name] = true
			if len(out) >= max {
				return out, true
			}
			alt := append([]word(nil), ws...)
			alt[i] = litOf(name)
			out = append(out, alt)
		}
	}
	return out, false
}

// unsetFlags returns a command's words with each flag that is not all
// written out as it is when no variable in it is set (see word.unset):
// "-rf${X:-.env}" is then -rf.env, and a table that knows where a flag
// keeps its file can find it. changed says some word was read that way.
func (e *Engine) unsetFlags(ws []word) (out []word, changed bool) {
	for i, w := range ws {
		if i > 0 && !w.static() && !w.brace && !w.tilde && allLitOrVar(w) {
			// (Wherever the variable stands in the word: "$x-rf" is -rf.)
			if l := e.unsetValue(w); strings.HasPrefix(l, "-") && l != "-" {
				out = append(out, litOf(l))
				changed = true
				continue
			}
		}
		out = append(out, w)
	}
	return out, changed
}

// matchNames returns the names a pattern matches from each of the
// directories the line may be in, written as the shell writes them: as
// the pattern is, from that directory. It reports false for a pattern
// that could not be expanded, and for one whose matches cannot be
// written back that way (it climbs with "..", or doubles a slash).
func (e *Engine) matchNames(pattern string, dirs []string, hide bool) ([]string, bool) {
	if e.glob == nil || pattern == "" {
		return nil, false
	}
	segs := strings.Split(pattern, "/")
	for i, seg := range segs {
		if seg == "" && i == 0 && len(segs) > 1 {
			continue // it starts at the root
		}
		if seg == "" || seg == "." || seg == ".." {
			return nil, false
		}
	}
	var out []string
	// A part that starts with a dot may match . and .. themselves (bash
	// before 5.2, and every sh): names like any, where it is the last.
	for i, seg := range segs {
		if !strings.HasPrefix(seg, ".") || !hasPathMagic(seg) {
			continue
		}
		for _, name := range []string{".", ".."} {
			if !segMatch(seg, name) {
				continue
			}
			if i != len(segs)-1 {
				return nil, false
			}
			out = unionStrings(out, []string{strings.Join(append(segs[:i:i], name), "/")})
		}
	}
	for _, d := range dirs {
		abs, base := pattern, ""
		if !strings.HasPrefix(pattern, "/") {
			base = strings.TrimSuffix(d, "/") + "/"
			abs = base + pattern
		}
		ms, err := e.expandGlob(abs)
		if err != nil {
			return nil, false
		}
		if hide {
			ms = dropHidden(abs, ms)
		}
		for _, m := range ms {
			rel, ok := strings.CutPrefix(m, base)
			if !ok || rel == "" {
				return nil, false
			}
			out = unionStrings(out, []string{rel})
		}
		if base == "" {
			break // the same from everywhere
		}
	}
	return out, true
}

// unsetValue is the word with no variable in it set (see unset), and a
// leading ~ or ~/ as the home directory.
func (e *Engine) unsetValue(w word) string {
	l := w.unsetIn(e.home)
	if w.tilde && e.home != "" {
		if l == "~" {
			return e.home
		}
		if strings.HasPrefix(l, "~/") {
			return e.home + l[1:]
		}
	}
	return l
}

// rereadWord reads text as one word of a command line: what bash does
// to the word after >& once it has expanded it a first time.
func rereadWord(s string) (word, bool) {
	for i := 0; i < len(s); i++ {
		if c := s[i]; c < 0x20 || c == 0x7f {
			return word{}, false
		}
	}
	p := &parser{src: s, sc: &script{ok: true}}
	w, ok := p.readWord()
	return w, ok && !p.failed && p.pos == len(s)
}

// dirTilde reports whether a word is one of bash's names for a
// directory the shell is or was in (~+, ~-, ~2, ~+1, ~-0), alone or with
// a path after it, and returns that path ("" or "/...").
func dirTilde(w word) (rest string, ok bool) {
	l := w.joined()
	if !w.tilde || !allLit(w) || !strings.HasPrefix(l, "~") {
		return "", false
	}
	i := 1
	if i < len(l) && (l[i] == '+' || l[i] == '-') {
		i++
	}
	for i < len(l) && l[i] >= '0' && l[i] <= '9' {
		i++
	}
	if i == 1 || i < len(l) && l[i] != '/' {
		return "", false
	}
	return l[i:], true
}

// allLit reports whether a word is made of literal text alone.
func allLit(w word) bool {
	for _, p := range w.parts {
		if p.kind != partLit {
			return false
		}
	}
	return true
}

// braceAlts returns the words bash's braces make of a word that also
// holds variables: "{$F,x}" is $F and x, "$@"{,x} is "$@" and "$@"x, and
// ${HOME}{,.old} is ${HOME} and ${HOME}.old, the braces taken apart before
// any variable is looked at. A word whose braces and commas all stand
// outside quotes, with no command or arithmetic in it, is taken apart as
// bash reads it as written (braceExpandRaw): an escaped brace, and those
// of a ${...}, count for nothing.
func braceAlts(w word) ([]word, bool) {
	if !w.brace || w.static() || strings.ContainsAny(w.raw, "`(") || bracesQuoted(w.raw) {
		return nil, false
	}
	alts, ok := braceExpandRaw(w.raw)
	if !ok || len(alts) == 1 && alts[0] == w.raw {
		return nil, false
	}
	var out []word
	for _, alt := range alts {
		if alt == "" {
			continue
		}
		part, ok := rereadWord(alt)
		if !ok || part.brace {
			return nil, false
		}
		out = append(out, part)
	}
	return out, len(out) > 0
}

// bracesQuoted reports whether a brace or a comma stands inside quotes in
// raw, where it counts for nothing to bash's braces, or a quote is left
// open.
func bracesQuoted(raw string) bool {
	var q byte
	for i := 0; i < len(raw); i++ {
		c := raw[i]
		switch {
		case c == '\\' && q != '\'':
			i++ // (escaped, outside quotes or inside double ones)
		case c == '$' && q != '\'' && i+1 < len(raw) && raw[i+1] == '{':
			// A ${...}: its braces are the parameter's, quoted or not.
			close := closingBrace(raw, i+1, true)
			if close < 0 {
				return true
			}
			i = close
		case q != 0 && c == q:
			q = 0
		case q != 0:
			if c == '{' || c == '}' || c == ',' {
				return true
			}
		case c == '\'' || c == '"':
			q = c
		}
	}
	return q != 0
}

// isFdTarget reports whether a redirection target is a descriptor number
// or "-", as in 2>&1 and >&-.
func isFdTarget(w word) bool {
	if !w.plain() {
		return false
	}
	l := strings.TrimSuffix(w.joined(), "-")
	return l == "" || isInt(l) && l[0] != '-' && l[0] != '+'
}

// readable reports whether the cleaned absolute path may be read without
// asking: it lies in a working directory, or an allow rule covers it, as
// written and as resolved.
func (e *Engine) readable(p string) bool {
	check := func(p string) bool {
		for _, d := range e.workDirs() {
			if inside(d, p) {
				return true
			}
		}
		return e.allowsPath(p, false) != nil
	}
	if !check(p) {
		return false
	}
	if e.resolve == nil {
		return true
	}
	r, err := e.resolve(p)
	return err == nil && strings.HasPrefix(r, "/") && check(cleanPath(r))
}

// inWork reports whether the cleaned absolute path lies in a working
// directory, as written and as resolved.
func (e *Engine) inWork(p string) bool {
	check := func(p string) bool {
		for _, d := range e.workDirs() {
			if inside(d, p) {
				return true
			}
		}
		return false
	}
	if !check(p) {
		return false
	}
	if e.resolve == nil {
		return true
	}
	r, err := e.resolve(p)
	return err == nil && strings.HasPrefix(r, "/") && check(cleanPath(r))
}

func cleanPath(p string) string {
	c, _ := cleanAbs(p, "")
	return c
}

// wordsOK reports whether every word names something for which ok holds,
// wherever the command may be running. why says what did not.
func (e *Engine) wordsOK(ws []word, st *lineState, ok func(string) bool, what string) string {
	for _, w := range ws {
		if w.static() && w.joined() == "-" {
			continue
		}
		abs, pattern, good := e.absAll(w, st.dirs)
		if !good {
			return "it names a file gnosh cannot know ahead of time (" + w.raw + ")"
		}
		if st.unknown && !strings.HasPrefix(w.value(e.home), "/") {
			return "it names " + w.raw + " after a change of directory gnosh could not follow"
		}
		if st.linked {
			return "it names " + w.raw + " after an ln earlier in the line, so where that leads cannot be known ahead of time"
		}
		for _, a := range abs {
			for _, m := range st.made {
				if inside(m, a) {
					return "it names " + w.raw + ", which an earlier cp or mv in the line puts there: what it will be cannot be known ahead of time"
				}
			}
		}
		if pattern {
			if e.glob == nil {
				return "it names files with a pattern (" + w.raw + "), which cannot be checked ahead of time"
			}
			var all []string
			for _, a := range abs {
				ms, err := e.expandGlob(a)
				dots, ok := e.dotMatches(a)
				if err != nil || !ok {
					return "the pattern " + w.raw + " could not be expanded"
				}
				all = append(all, ms...)
				all = append(all, dots...)
			}
			// Each ".." after a pattern is taken from where each match
			// really leads (climbMatches): "cat */../x" may read beside a
			// directory outside, through a link.
			if val := w.value(e.home); climbsAfterMagic(val) {
				for _, d := range st.dirs {
					ms, good := e.climbMatches(val, d)
					if !good {
						return "the pattern " + w.raw + " climbs with .. from what it matched, and where it leads could not be followed"
					}
					all = append(all, ms...)
				}
			}
			// A matched name that starts with a dash would reach the
			// command as a flag. The name is the match as the shell writes
			// it: from the directory the command runs in.
			if strings.ContainsAny(w.joined()[:1], "*?[") {
				for _, m := range all {
					for _, d := range st.dirs {
						if rel, ok := strings.CutPrefix(m, strings.TrimSuffix(d, "/")+"/"); ok && strings.HasPrefix(rel, "-") {
							return "the pattern " + w.raw + " matches " + rel + ", which the command would read as a flag"
						}
					}
				}
			}
			// A pattern that matches nothing stays as it is written, and
			// is then a name of its own: "cat /[" reads a file named [.
			abs = append(all, abs...)
		}
		for _, a := range abs {
			if !ok(cleanPath(a)) {
				return a + " is " + what
			}
		}
	}
	return ""
}

// partVerdict is the judgement of one simple command of a line.
type partVerdict struct {
	// ok: the part runs without asking; rule is the allow rule that let
	// it, when one did.
	ok   bool
	rule *crule
	// why says why the part needs approval.
	why string
	// exact and prefix are the rules that would cover the part from now
	// on; both are empty when no rule could.
	exact, prefix string
	// state names the builtin, when the part is one that changes the
	// shell the rest of the line runs in (see stateBuiltins).
	state string
}

// judgePart decides whether one simple command of a flat line runs
// without asking: an allow rule names it, or it only reads, or it is one
// of the file commands acceptEdits lets run.
func (e *Engine) judgePart(c *simpleCmd, st *lineState) partVerdict {
	plan := e.mode == ModePlan
	if len(c.assigns) > 0 {
		return partVerdict{why: "it sets a variable (" + c.assigns[0].w.raw + "), and gnosh asks about every command that does"}
	}
	if c.mixed {
		return partVerdict{why: "it uses a form ([[ ]] with operators, or &>) that a plain sh would run as several commands"}
	}
	staticInput := true
	for _, r := range c.redirs {
		switch r.op {
		case "<<", "<<-":
			staticInput = staticInput && r.bodyStatic
		case "<<<":
			staticInput = staticInput && r.target.static()
		case "<&":
			if !isFdTarget(r.target) {
				return partVerdict{why: "it has a redirection gnosh does not follow (<&" + r.target.raw + ")"}
			}
		case "<":
			if r.target.plain() && r.target.joined() == "/dev/null" {
				continue
			}
			if st.moved && r.target.static() && !strings.HasPrefix(r.target.value(e.home), "/") {
				return partVerdict{why: "it reads " + r.target.raw + " through < after a cd"}
			}
			if r.target.glob {
				return partVerdict{why: "it reads through < from a file name pattern"}
			}
			if why := e.wordsOK([]word{r.target}, st, e.readable, "outside the working directories"); why != "" {
				return partVerdict{why: "its input: " + why}
			}
		default:
			if r.op == ">&" && isFdTarget(r.target) {
				continue
			}
			if r.target.plain() && r.target.joined() == "/dev/null" {
				continue
			}
			return partVerdict{why: "it writes to " + r.target.raw + " with a redirection, and gnosh asks about those"}
		}
	}
	if len(c.words) == 0 {
		return partVerdict{ok: true}
	}

	words := c.words
	why := ""
	viaXargs := false
	// inShell: every wrapper the command was reached through runs it in
	// the shell itself (builtin, command, bash's keyword time), so that a
	// cd there moves the shell. Behind any other (stdbuf, env, nohup,
	// timeout...) a cd is a program of its own, which moves nothing
	// (oracle seed 117).
	inShell := true
	var first cmdFacts
	for depth := 0; ; depth++ {
		f := facts(gitHere(words, st))
		if depth == 0 {
			first = f
		}
		if !f.plain {
			if why == "" {
				why = orDefault(f.why, "the program name is not written as plain text")
			}
			break
		}
		state := ""
		if stateBuiltins[f.name] && (f.name != "printf" || hasLong(words[1:], "-v")) {
			state = f.name
		}
		// After a cd, a program named by a relative path is not the file
		// the rule's author saw: no allow rule names it.
		prog := words[0].joined()
		relProg := strings.Contains(prog, "/") && !strings.HasPrefix(prog, "/")
		if !plan && !((st.moved || st.unknown) && relProg) {
			if r := e.allowCommand(words, viaXargs); r != nil {
				e.afterPartIn(f, words, st, depth == 0 && c.sep != sepPipe && c.sep != sepOr && !c.neg && !c.cond, inShell)
				return partVerdict{ok: true, rule: r, state: state}
			}
		}
		if !viaXargs {
			if w := e.partRuns(c, f, st, depth, staticInput); w == "" {
				e.afterPartIn(f, words, st, depth == 0 && c.sep != sepPipe && c.sep != sepOr && !c.neg && !c.cond, inShell)
				return partVerdict{ok: true}
			} else if why == "" {
				why = w
			}
		}
		if depth == 0 && cutsTime(c) {
			// A redirection stands between bash's keyword time and a word
			// the keyword would have taken for its own: bash runs a program
			// of that word's name ("time>&- -p x" runs -p), which no rule
			// for the command after it covers.
			break
		}
		inner, ok := strictStrip(words)
		if !ok || depth >= 8 {
			break
		}
		viaXargs = viaXargs || words[0].joined() == "xargs"
		if n := words[0].joined(); words[0].quoted || n != "builtin" && n != "command" && n != "time" {
			inShell = false
		}
		words = inner
	}
	e.afterPart(first, c.words, st, c.sep != sepPipe && c.sep != sepOr && !c.neg && !c.cond)
	pv := partVerdict{why: why}
	if plainName(c.words[0]) {
		pv.exact, pv.prefix = e.suggestRules(c, first, st)
	}
	return pv
}

// afterPartIn is afterPart for a command reached through wrappers: one that
// does not run it in the shell itself leaves the shell where it is, whatever
// the command, so a cd there moves nothing.
func (e *Engine) afterPartIn(f cmdFacts, words []word, st *lineState, own, inShell bool) {
	switch f.name {
	case "cd", "pushd", "popd":
		if !inShell {
			return
		}
	}
	e.afterPart(f, words, st, own)
}

// afterPart notes what a part that is going to run does to the shell's
// directory, and to the files the rest of the line may name. own says a
// cd among them is the shell's own, standing by itself (see lineState).
func (e *Engine) afterPart(f cmdFacts, words []word, st *lineState, own bool) {
	switch f.name {
	case "cd":
		if f.cd == nil {
			st.unknown = true
			return
		}
		abs, _, ok := e.absAll(*f.cd, st.dirs)
		if !ok || st.unknown {
			st.unknown = true
			return
		}
		st.pending, st.sure = abs, own
	case "pushd", "popd":
		st.unknown = true
	case "ln":
		st.linked = true
	case "cp", "mv":
		// A plain cp of a file makes a file. A cp of a directory, and any
		// mv, may bring links along.
		brings := f.name == "mv"
		for _, w := range words[1:] {
			if l := w.lit(); strings.HasPrefix(l, "-") && (strings.HasPrefix(l, "--") || strings.ContainsAny(l, "rRadP")) {
				brings = true
			}
		}
		if !brings {
			return
		}
		for _, w := range f.writes {
			if abs, _, ok := e.absAll(w, st.dirs); ok {
				st.made = unionStrings(st.made, abs)
			} else {
				st.linked = true
			}
		}
	}
}

// stateBuiltins change the shell itself: what a name means, where
// programs are looked for, which options are on. Each can be allowed by
// a rule when it is all there is to the line. In a line with other
// commands it asks, because those other commands were judged as if the
// shell were the one the line started in: "alias ls=rm; ls x" is not an
// alias command and an ls command. (eval, source and trap are not here:
// a rule that allows one of those already allows whatever it runs.)
var stateBuiltins = map[string]bool{
	"alias": true, "unalias": true, "hash": true, "enable": true, "shopt": true, "set": true, "export": true,
	"declare": true, "typeset": true, "local": true, "readonly": true, "unset": true, "read": true,
	"mapfile": true, "readarray": true, "let": true, "getopts": true, "umask": true, "ulimit": true,
	"bind": true, "complete": true, "compopt": true, "printf": true, "pushd": true, "popd": true,
}

// allowCommand returns the allow rule that names the command, if any.
// With viaXargs the command is one xargs will add arguments to, so only a
// rule that ends in a wildcard covers what will really run.
func (e *Engine) allowCommand(words []word, viaXargs bool) *crule {
	text := rawJoined(words)
	exactOnly := prefixOnlyExact(words)
	for _, c := range e.rules(ListAllow) {
		if c.kind != kindCommand || exactOnly && !c.cmd.exact() {
			continue
		}
		if viaXargs && (c.cmd.exact() || c.cmd.segs[len(c.cmd.segs)-1] != "") {
			continue
		}
		if c.cmd.match(text) {
			return c
		}
	}
	return nil
}

// gitHere is a git command's words without a -C that names the folder
// the line is in, where git would run anyway: "git -C /their/own/folder
// diff --stat", as models often write, reads the same repository with the
// same settings as "git diff --stat". Only the folder itself, written out
// (absolute, or relative to where the line is) with no .. and no pattern,
// counts: a folder under it may hold a repository of its own, and a link
// or a ~ is not followed.
func gitHere(words []word, st *lineState) []word {
	if len(words) < 3 || st.unknown || len(st.dirs) == 0 || !words[0].static() || words[0].joined() != "git" ||
		!words[1].static() || words[1].joined() != "-C" || !words[2].static() || words[2].tilde || words[2].glob || words[2].brace {
		return words
	}
	// A .. is not taken off as text: git's -C goes into each part as the
	// system does, through a link first, so "git -C lnk/.." runs where lnk
	// points, and above it (a review found it; the shell's patterns are
	// left out for the same reason: what they match is not the text).
	if strings.Contains("/"+words[2].joined()+"/", "/../") {
		return words
	}
	for _, d := range st.dirs {
		if abs, ok := cleanAbs(words[2].joined(), d); !ok || abs != path.Clean(d) {
			return words
		}
	}
	return append([]word{words[0]}, words[3:]...)
}

// partRuns says why the command does not run on the mode alone, or ""
// when it does: it only reads inside the working directories, or the mode
// is acceptEdits and it is a file command that stays inside them.
func (e *Engine) partRuns(c *simpleCmd, f cmdFacts, st *lineState, depth int, staticInput bool) string {
	for _, d := range st.dirs {
		if !e.readable(d) {
			return "it would run in " + d + ", outside the working directories"
		}
	}
	if st.unknown {
		return "it runs after a change of directory gnosh could not follow"
	}
	switch {
	case f.readOnly:
		if !staticInput {
			return "its input is computed when the command runs"
		}
		if f.git && st.moved {
			return "it runs git after a cd, and the other directory's git hooks could run"
		}
		if f.cd != nil {
			return e.wordsOK([]word{*f.cd}, st, e.readable, "outside the working directories")
		}
		if f.name == "diff" && e.resolve != nil {
			// Given a directory, diff reads the files in it and follows
			// the links among them: every entry is held to the project
			// like a file named outright.
			var entries []word
			for _, o := range f.reads {
				if !o.static() || o.joined() == "-" {
					continue
				}
				for _, pat := range []string{"/*", "/.[!.]*", "/..?*"} {
					v := strings.TrimRight(o.joined(), "/") + pat
					entries = append(entries, word{raw: v, parts: []wordPart{{lit: v}}, glob: true, tilde: o.tilde, quoted: true})
				}
			}
			if why := e.wordsOK(entries, st, e.readable, "outside the working directories"); why != "" {
				return why
			}
		}
		return e.wordsOK(f.reads, st, e.readable, "outside the working directories")
	case f.fsEdit && e.mode == ModeAcceptEdits:
		if f.name == "touch" && depth > 0 {
			return "touch behind a wrapper is not one of the commands acceptEdits lets run"
		}
		if why := e.wordsOK(f.reads, st, e.readable, "outside the working directories"); why != "" {
			return why
		}
		// A file that is only put back as it was matters when it is a
		// link in the project that leads out of it: the copy that takes
		// the link's place can then be read like any file here.
		for _, w := range f.rewrites {
			abs, _, good := e.absAll(w, st.dirs)
			if !good {
				continue
			}
			for _, a := range abs {
				p := cleanPath(a)
				for _, d := range e.workDirs() {
					if inside(d, p) && !e.inWork(p) {
						return a + " is a link that leads out of the working directories, and GNU sed would put a copy of what it leads to in its place"
					}
				}
			}
		}
		if why := e.wordsOK(f.writes, st, e.inWork, "outside the working directories"); why != "" {
			return why
		}
		if f.name == "cp" {
			return e.cpLands(f, st)
		}
		return ""
	case f.fsEdit:
		return f.name + " changes files, which asks unless the mode is acceptEdits or a rule allows it"
	}
	return orDefault(f.why, f.name+" is not on the list of commands that only read")
}

// cpLands says why a cp does not run on the mode alone because of where
// its copies would land, or "" when nothing stands against it. Handed a
// directory, cp writes each source under its own name inside it, and
// where a link of that name is already there it writes through the link:
// with dir/notes.txt a link that leads out of the project, "cp notes.txt
// dir" changes the file out there. So every name a copy may get is held to
// the working directories like a file named outright, whether or not the
// place is a directory: a name under a place that is none does not exist,
// and resolves to the place itself.
//
// A copy of a directory with all that is in it (-R, -a) into a place that
// already holds something under its name goes on in the same way for every
// file inside. So what is already there is looked through, and a link
// among it that leads out asks; a place that holds none takes the copy
// without a question, however much is in it.
func (e *Engine) cpLands(f cmdFacts, st *lineState) string {
	if len(f.writes) == 0 || e.resolve == nil {
		// (With no way to follow links, the paths the engine is given are
		// already where they lead: there is no link to write through.)
		return ""
	}
	place := f.writes[0]
	places, pattern, ok := e.absAll(place, st.dirs)
	if !ok || pattern {
		return "" // (wordsOK spoke of the place already)
	}
	within := func(p string) bool {
		for _, d := range e.workDirs() {
			if inside(d, p) {
				return true
			}
		}
		return false
	}
	for _, src := range f.reads {
		abs, pattern, ok := e.absAll(src, st.dirs)
		if !ok {
			return "it names a file gnosh cannot know ahead of time (" + src.raw + ")"
		}
		// The name a copy gets is the last piece of the source as the
		// command is handed it. A source that ends in "." or ".." has no
		// name of its own: what is in it lands in the place itself.
		names := map[string]bool{}
		if pattern {
			for _, a := range abs {
				ms, err := e.expandGlob(a)
				dots, good := e.dotMatches(a)
				if err != nil || !good {
					return "the pattern " + src.raw + " could not be expanded"
				}
				for _, m := range append(ms, dots...) {
					names[lastPiece(m)] = true
				}
			}
		}
		// (A pattern that matches nothing stays as it is written.)
		names[lastPiece(src.value(e.home))] = true
		for name := range names {
			for _, p := range places {
				land := cleanPath(p)
				if name != "" {
					land = cleanPath(strings.TrimRight(p, "/") + "/" + name)
				}
				// Where the name leads cannot be said when the place is no
				// directory: nothing has a name under a file, and the copy
				// is then written to the place itself, which was judged
				// above. It cannot be said either where cp could not get
				// there. Neither writes through anything.
				real, err := e.resolve(land)
				if err != nil {
					continue
				}
				if !strings.HasPrefix(real, "/") || !within(cleanPath(real)) {
					return land + ", where the copy of " + src.raw + " lands when " + place.raw + " is a directory, is a link that leads out of the working directories"
				}
				if !f.tree {
					continue
				}
				if why := e.linkUnder(land, within); why != "" {
					return "cp would copy " + src.raw + " with all that is in it into " + land + ": " + why
				}
			}
		}
	}
	return ""
}

// linkUnder looks through what is already under dir, a place a directory
// is about to be copied into with all that is in it, and says why the copy
// asks, or "" when nothing there stands against it: cp writes through a
// link that is already where one of its files goes, and into a directory
// that a link there stands for. Every entry is held to the working
// directories as resolved, links to directories followed as cp follows
// them. What cannot be listed, and a place that holds more than is looked
// through, asks.
func (e *Engine) linkUnder(dir string, within func(string) bool) string {
	const most = 4096
	seen := 0
	for queue := []string{dir}; len(queue) > 0; queue = queue[1:] {
		d := queue[0]
		if strings.ContainsAny(d, "*?[\\") {
			return "what is already there cannot be listed ahead of time (" + d + " holds a character of file name patterns)"
		}
		for _, under := range []string{"/*", "/.[!.]*", "/..?*"} {
			found, err := e.expandGlob(d + under)
			if err != nil {
				return "what is already there cannot be listed ahead of time"
			}
			for _, m := range found {
				if seen++; seen > most {
					return "it already holds more than " + strconv.Itoa(most) + " files, which is more than gnosh looks through for links"
				}
				real, err := e.resolve(m)
				if err != nil || !strings.HasPrefix(real, "/") || !within(cleanPath(real)) {
					return m + " is already there and is a link that leads out of the working directories, which cp would write through"
				}
				queue = append(queue, m)
			}
		}
	}
	return ""
}

// lastPiece returns the last piece of a path as a command is handed it,
// the name a copy of it gets inside a directory; "" for a path that ends in
// "." or "..", or is the root, which has no name of its own there.
func lastPiece(p string) string {
	p = strings.TrimRight(p, "/")
	if i := strings.LastIndexByte(p, '/'); i >= 0 {
		p = p[i+1:]
	}
	if p == "." || p == ".." {
		return ""
	}
	return p
}

// decideShell applies the allow rules and the mode to a command line that
// no deny rule, ask rule or safety check stopped.
func (e *Engine) decideShell(req *request) Decision {
	v := req.view
	plan := e.mode == ModePlan
	ask := func(reason string) Decision {
		if plan {
			reason = "Plan mode: " + reason
		}
		return Decision{Behavior: Ask, Stage: StageMode, Reason: reason, hookAllow: !plan}
	}
	if !plan {
		if c := e.match(ListAllow, req); c != nil {
			return Decision{Behavior: Allow, Stage: StageAllow, Rule: c.Rule.String(), Source: c.layer.Name,
				Reason: "The allow rule " + c.Rule.String() + " from " + c.layer.Name + " covers every use of the tool."}
		}
	}
	switch {
	case !v.sc.ok:
		return ask("gnosh could not read the whole command (" + v.sc.why + "), so a person decides.")
	case v.tooMany:
		return ask("The command holds more nested commands than gnosh will follow, so a person decides.")
	case len(v.unread) > 0:
		// A script the line will run (the argument of an eval, a
		// subscript a builtin evaluates) that the reader could not
		// follow: no rule for the command that runs it covers what it
		// does.
		return ask("Part of the command is a script gnosh could not read (" + v.opaque + "), so a person decides.")
	case v.sc.subst:
		return ask("The command runs another command to build part of itself ($(...), backticks or <(...)), and gnosh asks about every command that does.")
	case v.sc.compound:
		// (A subshell is read as the commands in it, with its own
		// directory: see the spans below.)
		return ask("The command holds a for loop, a function or another compound form, and gnosh asks about those.")
	case (v.sc.cond || v.sc.forLoop) && changesDir(v):
		// An if, a while or a case is read straight through, every branch
		// judged, which holds as long as where a command runs does not
		// hang on which branch ran.
		return ask("The command holds an if, a loop or a case, and changes directory, so where its commands run hangs on which of them ran, and how often; gnosh asks about those.")
	case v.sc.background:
		return ask("The command starts a background job with &, and gnosh asks about those.")
	case v.sc.exotic:
		return ask("The command holds a ${...} form gnosh does not model, so a person decides.")
	case v.sc.setsVar:
		return ask("The command sets a variable (${name:=value}), and gnosh asks about every command that does.")
	}

	st := &lineState{dirs: []string{e.cur}, start: e.cur}
	var rule *crule
	var whys, exact, prefix []string
	coverable := true
	parts := 0
	for _, c := range v.sc.cmds {
		if len(c.words) > 0 {
			parts++
		}
	}
	// A subshell's cd moves the subshell alone: where the line stands is
	// put back as it was when the subshell began, once its last command
	// is judged. (What else a subshell could change for the commands
	// after it, a variable or the shell's settings, makes a line of more
	// than one part ask in any case.)
	opens, closes := map[int]int{}, map[int]int{}
	for _, sp := range v.sc.subshells {
		opens[sp.from]++
		closes[sp.to]++
	}
	var saved []lineState
	for i, c := range v.sc.cmds {
		for n := closes[i]; n > 0 && len(saved) > 0; n-- {
			st.leave(saved[len(saved)-1])
			saved = saved[:len(saved)-1]
		}
		// Where the shell is is settled first, and that is what a
		// subshell starting here starts from and what is put back after
		// it. Kept before it was settled, a cd still pending before the
		// subshell came back after it as if it had just run: in "cd nope
		// <newline> (cd sub) && cat x", the && then read as cd nope's
		// success, and cat was judged in nope alone, where the shell,
		// cd nope having failed, was where the line began (oracle seed
		// 129).
		st.enter(c)
		for n := opens[i]; n > 0; n-- {
			saved = append(saved, st.copyPlace())
		}
		pv := e.judgeLooped(v, i, c, st)
		if pv.ok && pv.state != "" && parts > 1 {
			whys = append(whys, pv.state+" changes the shell that runs the rest of the line, so the rest cannot be judged ahead of time")
			coverable = false
			continue
		}
		if pv.ok {
			if rule == nil {
				rule = pv.rule
			}
			continue
		}
		whys = append(whys, pv.why)
		if pv.exact == "" {
			coverable = false
			continue
		}
		exact = unionStrings(exact, []string{pv.exact})
		prefix = unionStrings(prefix, []string{orDefault(pv.prefix, pv.exact)})
	}
	if len(whys) == 0 {
		if rule != nil {
			return Decision{Behavior: Allow, Stage: StageAllow, Rule: rule.Rule.String(), Source: rule.layer.Name,
				Reason: "Every part of the command is covered by an allow rule or only reads; the first rule is " + rule.Rule.String() + " from " + rule.layer.Name + "."}
		}
		what := "Every part of the command only reads inside the working directories."
		if e.mode == ModeAcceptEdits {
			what = "Every part of the command only reads, or is a file command acceptEdits lets run, inside the working directories."
		}
		return Decision{Behavior: Allow, Stage: StageMode, Reason: what}
	}
	d := ask("The command needs approval: " + whys[0] + ".")
	if coverable && !plan && len(exact) <= 5 {
		d.Suggestions = append(d.Suggestions, Suggestion{Rules: exact, Scope: GrantLocal})
		if strings.Join(prefix, "\n") != strings.Join(exact, "\n") {
			d.Suggestions = append(d.Suggestions, Suggestion{Rules: prefix, Scope: GrantLocal})
		}
	}
	return d
}

// needsSubcommand lists programs whose first argument picks what they do,
// so that a rule for the bare program would allow far more than the call
// that was approved.
var needsSubcommand = map[string]bool{
	"git": true, "npm": true, "npx": true, "yarn": true, "pnpm": true, "bun": true, "go": true,
	"cargo": true, "docker": true, "kubectl": true, "gh": true, "make": true, "gno": true,
	"gnokey": true, "gnodev": true, "pip": true, "pip3": true, "brew": true, "apt": true,
	"apt-get": true, "dnf": true, "systemctl": true, "terraform": true, "aws": true,
	"gcloud": true, "doctl": true, "helm": true, "poetry": true, "uv": true, "deno": true,
}

// noPrefix lists programs for which only the exact command is ever
// suggested: they run or destroy whatever they are given.
var noPrefix = map[string]bool{
	"rm": true, "rmdir": true, "mv": true, "cp": true, "dd": true, "chmod": true, "chown": true,
	"kill": true, "pkill": true, "killall": true, "sudo": true, "su": true, "doas": true,
	"sh": true, "bash": true, "zsh": true, "dash": true, "ksh": true, "fish": true, "eval": true,
	"exec": true, "env": true, "xargs": true, "find": true, "curl": true, "wget": true,
	"ssh": true, "scp": true, "rsync": true, "nc": true, "python": true, "python3": true,
	"node": true, "ruby": true, "perl": true, "php": true, "lua": true, "awk": true, "sed": true,
	"watch": true, "setsid": true, "ionice": true, "flock": true, "source": true, ".": true,
	"tee": true, "truncate": true, "shred": true, "ln": true, "timeout": true, "nice": true,
	"nohup": true, "time": true, "command": true, "builtin": true, "stdbuf": true, "noglob": true,
	"alias": true, "export": true, "unset": true, "trap": true, "cd": true,
}

// suggestRules returns the rules that would cover a part from now on: the
// exact command, and where it is sensible a prefix rule one step wider.
// For a cd out of the working directories the rule is a Read rule for
// that directory.
func (e *Engine) suggestRules(c *simpleCmd, f cmdFacts, st *lineState) (exact, prefix string) {
	if f.name == "cd" && f.cd != nil {
		if abs, _, ok := e.absAll(*f.cd, st.dirs); ok && len(abs) == 1 {
			return "Read(/" + EscapePath(abs[0]) + "/**)", ""
		}
		return "", ""
	}
	text := rawJoined(c.words)
	if strings.ContainsAny(text, "\n") {
		return "", ""
	}
	// A suggestion is only made when the rule, read back, covers this
	// very command.
	covers := func(rule string) bool {
		r, err := ParseRule(rule)
		if err != nil {
			return false
		}
		cr, err := e.compile(r, ListAllow, e.session)
		return err == nil && cr.kind == kindCommand && cr.cmd.match(text)
	}
	exact = "Bash(" + strings.ReplaceAll(text, "*", "\\*") + ")"
	if !covers(exact) {
		return "", ""
	}
	name := c.words[0].joined()
	if noPrefix[baseName(name)] || strings.Contains(name, "/") || prefixOnlyExact(c.words) || len(c.words) == 1 {
		return exact, ""
	}
	head := name
	if len(c.words) > 1 && subcommandLike(c.words[1]) {
		head += " " + c.words[1].joined()
		if len(c.words) == 2 {
			return exact, ""
		}
	} else if needsSubcommand[name] {
		return exact, ""
	}
	if prefix = "Bash(" + head + " *)"; !covers(prefix) {
		prefix = ""
	}
	return exact, prefix
}

// subcommandLike reports whether a word looks like a subcommand: a short
// plain lower-case word such as "test" or "run-script".
func subcommandLike(w word) bool {
	if !w.plain() {
		return false
	}
	s := w.joined()
	if s == "" || len(s) > 24 || s[0] < 'a' || s[0] > 'z' {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		if !(c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '-' || c == '_' || c == ':') {
			return false
		}
	}
	return true
}
