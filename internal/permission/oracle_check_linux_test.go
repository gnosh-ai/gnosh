//go:build linux

package permission

import (
	"fmt"
	"os"
	"path"
	"regexp"
	"sort"
	"strings"
	"sync"
)

// This file holds what a real shell did against what the engine said.
// Three things are checked for every line:
//
//  1. No silent widening. When the engine lets a line run on its rules or
//     on the read-only list, every command the shell ran must be one the
//     engine read in the line, or one it lets run when shown it alone;
//     nothing may be written, and nothing outside the project may be read
//     by a line that ran only because it reads.
//  2. No dodged deny. Whatever the engine said, if the shell ran a command
//     a deny or ask rule names, read or wrote a file one names, or removed
//     something that must never be removed without a question, the engine
//     must not have said yes.
//  3. Agreement of reading. Where the engine says it followed the whole
//     line, the commands it read are the commands the shell ran.

// ruleSet is one configuration the oracle judges every line under.
type ruleSet struct {
	name string
	mode Mode
	r    rules
	// glob gives the engine a way to expand file name patterns.
	glob bool
}

// bare reports whether the set lets every Bash command run.
func (rs ruleSet) bare() bool {
	for _, a := range rs.r.allow {
		if a == "Bash" || a == "Bash(*)" {
			return true
		}
	}
	return false
}

// fixedRuleSets are the realistic configurations of the brief.
func fixedRuleSets() []ruleSet {
	allow := []string{"Bash(git status)", "Bash(git diff *)", "Bash(ls *)", "Bash(go test *)", "Bash(npm run test:*)", "Bash(make build)"}
	deny := []string{"Bash(rm *)", "Bash(curl *)", "Bash(git push *)", "Read(./.env)"}
	ask := []string{"Bash(git commit *)"}
	guards := append([]string{"Read(~/.ssh/**)", "Edit(./build/**)"}, deny...)
	return []ruleSet{
		{"no rules", ModeDefault, rules{}, false},
		{"no rules, patterns expanded", ModeDefault, rules{}, true},
		{"no rules, acceptEdits", ModeAcceptEdits, rules{}, true},
		{"realistic", ModeDefault, rules{allow: allow, deny: deny, ask: ask}, true},
		{"realistic, acceptEdits", ModeAcceptEdits, rules{allow: allow, deny: deny, ask: ask}, true},
		{"realistic, plan", ModePlan, rules{allow: allow, deny: deny, ask: ask}, false},
		{"realistic, dontAsk", ModeDontAsk, rules{allow: allow, deny: deny, ask: ask}, false},
		{"guards only, bypassPermissions", ModeBypass, rules{deny: guards, ask: ask}, true},
		{"every command allowed, guards", ModeDefault, rules{allow: []string{"Bash"}, deny: guards, ask: ask}, true},
	}
}

// fill replaces the jail's placeholders in a line or a rule.
func (j *jail) fill(s string) string {
	return strings.NewReplacer("@WORK@", j.work, "@HOME@", j.home, "@OUT@", j.outside, "@BIN@", j.root+"/bin").Replace(s)
}

// engine builds the engine for a rule set, in this jail. It returns nil
// when a rule of the set cannot be applied.
func (j *jail) engine(rs ruleSet) *Engine {
	fillAll := func(in []string) []string {
		out := make([]string, len(in))
		for i, s := range in {
			out[i] = j.fill(s)
		}
		return out
	}
	o := Options{
		Mode: rs.mode, Cwd: j.work, Home: j.home, Trusted: true, Resolve: realResolve,
		Layers: []Layer{{Scope: ScopeUser, Name: "oracle", Base: j.home + "/.config/gnosh",
			Allow: fillAll(rs.r.allow), Ask: fillAll(rs.r.ask), Deny: fillAll(rs.r.deny)}},
		// gnosh's own folders, as the program names them to the engine.
		Protected: []string{j.home + "/.config/gnosh", j.home + "/.local/share/gnosh"},
	}
	if rs.glob {
		o.Glob = shellGlob
	}
	e, err := New(o)
	if err != nil {
		return nil
	}
	return e
}

// finding is one disagreement between the engine and a shell.
type finding struct {
	kind   string // widen, deny or agree
	set    string
	shell  string
	mode   string
	line   string
	detail string
}

func (f finding) String() string {
	return fmt.Sprintf("[%s] %q\n    rules: %s; shell: %s (%s)\n    %s", f.kind, f.line, f.set, f.shell, f.mode, f.detail)
}

// key groups findings that are the same problem: the kind, what went
// wrong, and the first word of what it went wrong with.
func (f finding) key() string {
	head, rest, _ := strings.Cut(f.detail, ": ")
	if i := strings.IndexAny(rest, " \t\n"); i > 0 {
		rest = rest[:i]
	}
	return f.kind + "/" + head + "/" + baseName(rest)
}

// ranCmd is one command a shell ran, from a stub's record or the trace.
type ranCmd struct {
	name   string
	path   string // the file executed; "" for a command known only from the trace
	args   []string
	cwd    string
	fd     [3]string
	parent string
	// by is startedBy of the programs above it; 2 for a traced command.
	by  int
	raw string // the trace's own text
	// fuzzy marks a command taken from dash's trace, which does not
	// quote: only its name can be trusted.
	fuzzy bool
}

// (Only the end is trimmed: a program named " rm" is not rm.)
func (rc ranCmd) text() string {
	return strings.TrimRight(rc.name+" "+strings.Join(rc.args, " "), " ")
}

// shellBuiltins are commands bash runs itself: a stub of the same name is
// not what the shell uses, so these are taken from the trace.
var shellBuiltins = func() map[string]bool {
	m := map[string]bool{}
	for _, n := range strings.Fields(`. : [ [[ alias bg bind break builtin caller cd command compgen complete compopt
		continue coproc declare dirs disown echo enable eval exec exit export false fc fg getopts hash help history
		jobs kill let local logout mapfile popd printf pushd pwd read readarray readonly return set shift shopt
		source suspend test times trap true type typeset ulimit umask unalias unset wait`) {
		m[n] = true
	}
	return m
}()

// lineShells are the shells themselves; lineWrappers are the programs
// the engine looks through to the command they run.
var (
	lineShells   = map[string]bool{"bash": true, "dash": true, "sh": true}
	lineWrappers = map[string]bool{"env": true, "timeout": true, "nice": true, "nohup": true, "stdbuf": true,
		"setsid": true, "xargs": true, "find": true, "time": true}
)

// startedBy sorts a command by the programs above it: 2 when they are all
// shells (the line's own, or one it handed a script), 1 when a wrapper
// the engine looks through is among them, 0 when some other program
// started it (sed, git, awk: programs whose scripts no rule reads).
func startedBy(chain []string) int {
	n := 2
	for _, p := range chain {
		switch {
		case lineShells[p]:
		case lineWrappers[p]:
			n = 1
		default:
			return 0
		}
	}
	return n
}

// ran lists the commands of a run: every stub, and from the trace the
// commands no stub stands for (builtins, functions, names not found).
func (j *jail) ran(r shellRun) []ranCmd {
	var out []ranCmd
	stubDir := j.root + "/bin/"
	for _, s := range r.stubs {
		rc := ranCmd{name: s.name, path: s.exe, args: s.args, cwd: s.cwd, fd: s.fd, parent: s.parent, by: startedBy(s.chain)}
		if !strings.HasPrefix(s.exe, stubDir) {
			rc.name = s.exe
		}
		out = append(out, rc)
	}
	names := oracleNameSet()
	for _, t := range r.trace {
		w0 := t.words[0]
		if _, ok := assignName(w0); ok {
			continue
		}
		if strings.Contains(t.raw, "+"+traceMark) {
			continue // two processes wrote their trace lines into each other
		}
		switch w0 {
		case "((", "for", "case", "select":
			continue
		}
		if names[baseName(w0)] && !shellBuiltins[w0] {
			continue // a stub wrote this one down, with more detail
		}
		if strings.HasPrefix(w0, "+") || r.shell == "dash" && (strings.Contains(t.raw, traceMark) || strings.HasSuffix(w0, ":")) {
			continue // two processes wrote their trace lines into each other, or into an error message
		}
		if r.shell == "dash" && (strings.ContainsAny(r.line, "|&") || strings.HasPrefix(w0, "-")) {
			continue // dash writes a trace line in pieces, and the commands of a pipeline write theirs at once
		}
		out = append(out, ranCmd{name: w0, args: t.words[1:], raw: t.raw, by: 2, fuzzy: r.shell == "dash"})
	}
	return out
}

// oracleNameSet is oracleNames as a set, made once.
var oracleNameSet = sync.OnceValue(func() map[string]bool {
	set := map[string]bool{}
	for _, n := range oracleNames() {
		set[n] = true
	}
	return set
})

// knownCommand reports whether a name is a stub or a builtin.
func knownCommand(name string) bool {
	return shellBuiltins[name] || oracleNameSet()[name]
}

// known reports whether a word's value is known to the engine ahead of
// time, and returns it.
func (e *Engine) known(w word) (string, bool) {
	switch w.raw {
	case "[", "]", "[[", "]]":
		return w.raw, true // a bracket alone is no pattern
	}
	if !w.static() || w.brace || w.glob {
		return "", false
	}
	return w.value(e.home), true
}

// ruleAllows reports whether an allow rule names the command, as it is
// or behind the wrappers a rule sees through.
func (e *Engine) ruleAllows(ws []word) bool {
	// What xargs adds to a command is the business of a rule that ends
	// in a wildcard, also behind the wrappers such a rule sees through:
	// "xargs timeout 5 git" under Bash(git *). That is how the engine
	// judges it (judgePart), so the word that stands for what xargs adds
	// is taken off before the wrappers are.
	viaXargs := false
	if n := len(ws); n > 1 && xargsWord(ws[n-1]) {
		ws, viaXargs = ws[:n-1], true
	}
	for depth := 0; depth < 8 && len(ws) > 0 && plainName(ws[0]); depth++ {
		if e.allowCommand(ws, viaXargs) != nil {
			return true
		}
		inner, ok := strictStrip(ws)
		if !ok {
			return false
		}
		ws = inner
	}
	return false
}

// unitMatch says how a command the shell ran relates to one the engine
// read: 2 when name and every argument agree, 1 when the name agrees and
// so do the arguments as far as the engine could know them, 0 otherwise.
// A unit whose program is not known ahead of time matches anything at 1.
func (e *Engine) unitMatch(u *unit, rc ranCmd) int {
	ws := u.cmd.words
	if len(ws) == 0 {
		return 0
	}
	name, ok := e.known(ws[0])
	if !ok || magicProg(name) {
		return 1 // (a program named by a number is one nobody knows: /dev/fd/3)
	}
	if rc.fuzzy {
		// dash's trace does not quote: the name alone, or the whole text.
		var lits []string
		for _, w := range ws {
			if v, ok := e.known(w); ok {
				lits = append(lits, v)
			}
		}
		// (A name with blanks or quotes in it, or an empty one, is only to
		// be found at the head of the line dash wrote.)
		if first, _, _ := strings.Cut(strings.Join(lits, " "), "\n"); baseName(name) != baseName(rc.name) && first != rc.raw &&
			strings.TrimSpace(name) != rc.name && rc.raw != name && !strings.HasPrefix(rc.raw, name+" ") {
			return 0
		}
		return 2
	}
	if baseName(name) != baseName(rc.name) {
		return 0
	}
	if name == "[[" {
		return 2
	}
	exact := 2
	for i, w := range ws[1:] {
		v, ok := e.known(w)
		if !ok {
			return 1
		}
		if i >= len(rc.args) {
			return 0
		}
		if rc.args[i] != v {
			// dash does not expand the tilde of "if=~/x", and has no
			// $'...'; the engine reads the word as bash does.
			if rc.args[i] != w.joined() && !strings.Contains(w.raw, "$'") {
				return 0
			}
			exact = 1
		}
	}
	if len(rc.args) != len(ws)-1 {
		// A command the engine dug out of another (the command after
		// xargs, compgen -C, mapfile -C) is given more arguments by the
		// one that runs it.
		if u.derived && len(rc.args) > len(ws)-1 {
			return 1
		}
		return 0
	}
	return exact
}

// runners are commands that run what their arguments say. A rule that
// allows one of them allows what it runs.
var runners = func() map[string]bool {
	m := map[string]bool{"eval": true, "source": true, ".": true, "trap": true, "mapfile": true, "readarray": true,
		"compgen": true, "complete": true, "alias": true, "hash": true, "find": true, "enable": true, "coproc": true}
	for n := range wrappers {
		m[n] = true
	}
	for n := range shellNames {
		m[n] = true
	}
	return m
}()

// ruleRuns reports whether an allow rule names a command of the line
// that runs other commands: then what those are is the rule's business.
func (lc *lineCheck) ruleRuns(e *Engine) bool {
	for _, c := range lc.view.sc.cmds {
		// As the engine does it: through the wrappers it sees through, a
		// rule behind xargs counting only when it ends in a wildcard.
		ws, viaXargs := c.words, false
		for depth := 0; depth < 8 && len(ws) > 0; depth++ {
			name, ok := e.known(ws[0])
			if !ok {
				break
			}
			if (runners[baseName(name)] || magicProg(name)) && e.allowCommand(ws, viaXargs) != nil {
				return true // (/proc/self/exe is a shell: what it runs is the rule's business too)
			}
			inner, ok := strictStrip(ws)
			if !ok {
				break
			}
			viaXargs = viaXargs || name == "xargs"
			ws = inner
		}
	}
	return false
}

// shQuote writes one argument so that a shell (and the engine's reader)
// reads it back as exactly that text.
func shQuote(s string) string {
	if s == "" {
		return "''"
	}
	safe, ansi := s[0] != '~' && s[0] != '#', false
	for i := 0; i < len(s); i++ {
		c := s[i]
		if !(isNameChar(c) || strings.IndexByte("@%+:,./-", c) >= 0) {
			safe = false
		}
		if c < 0x20 && c != '\n' && c != '\t' || c == 0x7f {
			ansi = true
		}
	}
	switch {
	case safe:
		return s
	case ansi:
		var b strings.Builder
		b.WriteString("$'")
		for i := 0; i < len(s); i++ {
			switch c := s[i]; {
			case c == '\\' || c == '\'':
				b.WriteByte('\\')
				b.WriteByte(c)
			case c < 0x20 || c == 0x7f:
				fmt.Fprintf(&b, "\\x%02x", c)
			default:
				b.WriteByte(c)
			}
		}
		b.WriteByte('\'')
		return b.String()
	}
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// render writes a command the shell ran as a line of its own.
func (j *jail) render(rc ranCmd) string {
	if rc.name == "[[" && rc.raw != "" {
		return rc.raw
	}
	parts := []string{rc.name}
	if !plainName(litOf(rc.name)) {
		parts[0] = shQuote(rc.name)
	}
	for _, a := range rc.args {
		if a == "[" || a == "]" {
			parts = append(parts, a)
			continue
		}
		parts = append(parts, shQuote(a))
	}
	line := strings.Join(parts, " ")
	if p := rc.fd[0]; j.treeFile(p) {
		line += " < " + shQuote(p)
	}
	for _, p := range rc.fd[1:] {
		if j.treeFile(p) {
			line += " > " + shQuote(p)
			break
		}
	}
	return line
}

// treeFile reports whether a descriptor's target is a file in the jail's
// tree (and not a pipe, a device or the run's own log).
func (j *jail) treeFile(p string) bool {
	return strings.HasPrefix(p, j.root+"/") && !strings.HasPrefix(p, j.logDir+"/") && !strings.HasSuffix(p, " (deleted)")
}

// lineCheck is everything about one line that does not depend on the
// rule set.
type lineCheck struct {
	j     *jail
	line  string
	view  *bashView // the engine's reading
	plain *Engine
	runs  []shellRun
}

// guardHit says which deny or ask rule names something the run did, or
// "".
func (lc *lineCheck) guardHit(e *Engine, rs ruleSet, r shellRun, rcs []ranCmd) string {
	j := lc.j
	var guards []*crule
	for _, list := range []List{ListDeny, ListAsk} {
		guards = append(guards, e.rules(list)...)
	}
	pathGuard := func(tool string, paths []string) *crule {
		var all []string
		for _, p := range paths {
			all = append(all, p)
			if rp, err := realResolve(p); err == nil && rp != p {
				all = append(all, rp)
			}
		}
		for _, list := range []List{ListDeny, ListAsk} {
			if c := e.pathHit(list, tool, all); c != nil {
				return c
			}
		}
		return nil
	}
	sourced := sourcesFile(rcs) || traceSources(r) || lc.runsFile()
	for _, rc := range rcs {
		if rc.fuzzy || rc.by == 0 || sourced && rc.path == "" {
			continue // started by a program no rule reads, or read from a file that was sourced
		}
		if sourced && rc.by == 2 {
			continue
		}
		texts := []string{rc.text()}
		if b := baseName(rc.name); b != rc.name {
			texts = append(texts, strings.TrimSpace(b+" "+strings.Join(rc.args, " ")))
		}
		for _, c := range guards {
			if c.kind != kindCommand {
				continue
			}
			for _, t := range texts {
				if c.cmd.match(t) {
					return fmt.Sprintf("ran a command the %s rule %s names: %s", c.list, c.Rule, t)
				}
			}
		}
		if rc.path == "" {
			continue
		}
		// Files the command names, by the engine's own account of which
		// arguments are files.
		ws := []word{litOf(baseName(rc.name))}
		for _, a := range rc.args {
			ws = append(ws, litOf(a))
		}
		f := facts(ws)
		var reads, writes []string
		for _, w := range f.reads {
			if abs, ok := cleanAbs(w.joined(), rc.cwd); ok && w.joined() != "-" {
				reads = append(reads, abs)
			}
		}
		for _, w := range f.writes {
			if abs, ok := cleanAbs(w.joined(), rc.cwd); ok {
				writes = append(writes, abs)
			}
		}
		if j.treeFile(rc.fd[0]) {
			reads = append(reads, rc.fd[0])
		}
		for _, p := range rc.fd[1:] {
			if j.treeFile(p) {
				writes = append(writes, p)
			}
		}
		if !lc.letGo(e, rs) {
			if c := pathGuard(toolRead, append(append([]string(nil), reads...), writes...)); c != nil {
				return fmt.Sprintf("touched a file the %s rule %s names: %s", c.list, c.Rule, rc.text())
			}
			if c := pathGuard(toolEdit, writes); c != nil {
				return fmt.Sprintf("changed a file the %s rule %s names: %s", c.list, c.Rule, rc.text())
			}
		}
		// A removal that always asks: of a directory and what is in it.
		// (Letters no rm takes make it stop before it removes a thing.)
		recursive := baseName(rc.name) == "rmdir"
		for _, a := range rc.args {
			if recursiveLong(a) || len(a) > 1 && a[0] == '-' && a[1] != '-' && strings.Trim(a[1:], "rRfvdiIPWx") == "" && strings.ContainsAny(a, "rR") {
				recursive = true
			}
		}
		if b := baseName(rc.name); (b == "rm" || b == "rmdir") && recursive && (b == "rmdir" || lc.writesRecursive(e)) {
			rest := false
			for _, a := range rc.args {
				if a == "--" && !rest {
					rest = true
					continue
				}
				if strings.HasPrefix(a, "-") && !rest {
					continue
				}
				if abs, ok := cleanAbs(a, rc.cwd); ok && e.criticalDir(abs) != "" {
					// A variable in front of a directory at the top that is
					// not one of the usual ones ("$X/b}") is not a removal the
					// engine claims to stop: its check for an empty variable
					// names the usual top-level directories and no others.
					if path.Dir(abs) == "/" && !topLevelNames[path.Base(abs)] && !lc.writes(a) {
						continue
					}
					return "ran a dangerous removal: " + rc.text()
				}
			}
		}
		// The other commands that destroy what has to be kept (mv, chmod
		// -R, git clean -fdx, dd, truncate, shred, rsync --delete): the
		// engine's own judgement of the command as it ran, every word of
		// it known by now, against the yes it gave to the line.
		if b := baseName(rc.name); destroyers[b] && rc.path != "" {
			ws := []word{litOf(b)}
			for _, a := range rc.args {
				ws = append(ws, litOf(a))
			}
			known := &bashView{sc: &script{ok: true}, vars: map[string]*lineVar{}}
			if why := e.removalDanger(known, &simpleCmd{words: ws}, []string{rc.cwd}, nil); why != "" && lc.wroteDestroyer(e, b, rc.args) {
				return "ran a command that destroys what has to be kept: " + rc.text() + " (" + why + ")"
			}
		}
		// Everything in a directory that has to be kept, between the
		// words a command that removes or overwrites was handed. The
		// engine can see what is in a directory only where it was given
		// a way to (Options.Glob), so only there is it held to this.
		if b := baseName(rc.name); rs.glob && rc.path != "" && (b == "rm" || b == "mv" || b == "shred" || b == "truncate") && lc.wroteDestroyer(e, b, rc.args) && lc.allWritten(e, b) {
			if dir := tookAll(e, rc); dir != "" {
				return "ran a command that was handed everything in " + dir + ": " + rc.text()
			}
		}
	}
	for _, ch := range r.changes {
		if r.mode[0] != 'p' || lc.letGo(e, rs) || lc.leavesStubs(rcs) {
			// With the real tools on the path a find or a sed may have made
			// the change, and no path rule claims to follow those; and with
			// permissions skipped a file named in a way that cannot be
			// known ahead of time is let go (see letGo).
			break
		}
		if c := pathGuard(toolRead, []string{ch.path}); c != nil {
			return fmt.Sprintf("changed a file the %s rule %s names: %s was %s", c.list, c.Rule, ch.path, ch.what)
		}
		if c := pathGuard(toolEdit, []string{ch.path}); c != nil {
			return fmt.Sprintf("changed a file the %s rule %s names: %s was %s", c.list, c.Rule, ch.path, ch.what)
		}
		if rs.mode != ModeBypass {
			if why := e.protectedWhy(ch.path); why != "" {
				return fmt.Sprintf("changed a protected file: %s was %s", ch.path, ch.what)
			}
		}
	}
	// A file a Read rule shuts, read by the shell itself into a command
	// line or onto the screen. With the real tools on the path a
	// recursive search may print it, which no rule claims to stop.
	for tok, p := range map[string]string{tokEnv: j.work + "/.env", tokSubEnv: j.work + "/src/.env", tokSSH: j.home + "/.ssh/id_ed25519"} {
		if !e.ReadDenied(p) || lc.letGo(e, rs) {
			continue
		}
		// (Only with stubs on the path: a real grep -r that prints the
		// file into a $(...) read it without being handed its name,
		// which is what no rule claims to stop; a file a real tool is
		// handed by name was held against the rules above.)
		if r.mode[0] != 'p' {
			continue
		}
		seen := strings.Contains(r.out, tok)
		for _, rc := range rcs {
			for _, a := range rc.args {
				seen = seen || strings.Contains(a, tok)
			}
		}
		if seen {
			return "the shell read a file a Read deny rule names: " + p
		}
	}
	return ""
}

// destroyers are the commands, beside rm, rmdir and find, that the
// engine asks about when they would destroy a directory that has to be
// kept.
var destroyers = map[string]bool{"mv": true, "chmod": true, "chown": true, "chgrp": true, "git": true, "dd": true,
	"truncate": true, "shred": true, "rsync": true}

// wroteDestroyer reports whether the line holds the command that ran as
// the engine claims to judge it: a command of that name whose flags, as
// they ran, are all written out in it, and none of whose operands is a
// variable nobody knows. A flag that only running the line produces
// (chmod $(echo -R) 000 .) is a blind spot the engine does not claim to
// see into, like rm's; and a target that is only an unknown variable is
// let go by design (doc.go), so that "mv "$tmp" x" runs unasked.
//
// A command's output without quotes may be several words. Where it
// stands in a place the engine reads as something other than a target
// (the mode of chmod, the place mv moves things to, a flag's value) the
// engine takes it for that one thing, so that "chown -R $(whoami) build"
// runs; words it printed beyond the first are, like a flag, what only
// running the line produces.
//
// Of a line the reader could not follow the engine has only crude pieces,
// and judges those (crudeDanger): the command is written when a piece
// names it and holds every flag it ran with.
func (lc *lineCheck) wroteDestroyer(e *Engine, name string, args []string) bool {
	for _, u := range lc.view.units {
		ws := u.cmd.words
		if len(ws) == 0 {
			continue
		}
		if n, ok := e.progName(ws[0]); !ok || progIsPattern(ws[0]) || progKey(baseName(n)) != name {
			continue
		}
		static := map[string]bool{}
		letGo, spills := false, false
		for _, w := range ws[1:] {
			if w.static() {
				static[w.joined()] = true
				continue
			}
			if _, _, ok := e.filledAll(lc.view, w); !ok && onlyVars(w) {
				letGo = true
			}
			spills = spills || !xargsWord(w) && manyWords(w)
		}
		written := true
		for _, a := range args {
			if strings.HasPrefix(a, "-") && a != "-" && !static[a] {
				written = false
			}
		}
		if spills && len(args) > len(ws)-1 {
			written = false
		}
		if written && !letGo {
			return true
		}
	}
	if !lc.view.sc.ok {
		for _, frag := range lc.view.frags {
			fields := strings.Fields(frag)
			has := map[string]bool{}
			named := false
			for _, f := range fields {
				has[f] = true
				named = named || progKey(baseName(f)) == name
			}
			written := named
			for _, a := range args {
				if strings.HasPrefix(a, "-") && a != "-" && !has[a] {
					written = false
				}
			}
			if written {
				return true
			}
		}
	}
	return false
}

// allWritten reports whether the line holds a command of that name all
// of whose words are written out: the names it is handed, or the
// patterns that make them, are then the line's own, and not what some
// other command printed.
func (lc *lineCheck) allWritten(e *Engine, name string) bool {
	for _, u := range lc.view.units {
		ws := u.cmd.words
		if len(ws) == 0 {
			continue
		}
		if n, ok := e.progName(ws[0]); !ok || progIsPattern(ws[0]) || progKey(baseName(n)) != name {
			continue
		}
		if allStatic(ws) {
			return true
		}
	}
	return false
}

// tookAll returns the directory that has to be kept all of whose entries
// (but those that start with a dot, which a * leaves out) are among the
// words the command was handed, when there is one with two entries or
// more. Every word counts as a name: a file may be named like a flag.
func tookAll(e *Engine, rc ranCmd) string {
	named := map[string]map[string]bool{}
	for _, a := range rc.args {
		abs, ok := cleanAbs(a, rc.cwd)
		if !ok {
			continue
		}
		dir := parentDir(abs)
		if named[dir] == nil {
			named[dir] = map[string]bool{}
		}
		named[dir][abs] = true
	}
	for dir, names := range named {
		if len(names) < 2 || e.criticalGlobDir(dir) == "" {
			continue
		}
		entries, err := os.ReadDir(dir)
		if err != nil {
			continue
		}
		n, all := 0, true
		for _, en := range entries {
			if strings.HasPrefix(en.Name(), ".") {
				continue
			}
			n++
			all = all && names[path.Join(dir, en.Name())]
		}
		if n >= 2 && all {
			return dir
		}
	}
	return ""
}

// writes reports whether some command the engine read in the line holds
// the word as it stands, with nothing in it for the shell to fill in.
func (lc *lineCheck) writes(arg string) bool {
	for _, u := range lc.view.units {
		for _, w := range u.cmd.words {
			if w.static() && w.joined() == arg {
				return true
			}
		}
	}
	return false
}

// writesRecursive reports whether some command the engine read in the
// line is given a recursive flag in so many words. A flag that only
// turns up in what a substitution prints ("rm $(wc -l x -rf)") is not
// one the dangerous-removal check claims to see: that check looks for
// the usual accidents, and says so.
func (lc *lineCheck) writesRecursive(e *Engine) bool {
	for _, u := range lc.view.units {
		if len(u.cmd.words) == 0 {
			continue
		}
		// (The flag must be rm's own: one given to wc and printed by it
		// is the same blind spot.)
		if name, ok := e.progName(u.cmd.words[0]); ok && !progIsPattern(u.cmd.words[0]) && baseName(name) != "rm" {
			continue
		}
		for _, w := range u.cmd.words {
			if !w.static() {
				continue
			}
			if a := w.joined(); recursiveLong(a) || len(a) > 1 && a[0] == '-' && a[1] != '-' && strings.Trim(a[1:], "rRfvdiIPWx") == "" && strings.ContainsAny(a, "rR") {
				return true
			}
		}
	}
	return false
}

// leavesStubs reports whether a run may have started a real program
// although every name on its search path is a stub: "command -p" looks
// in the system's own directories, a name with a slash is a file, and a
// line can move PATH or tell the shell where a name is. What such a
// program writes (git its FETCH_HEAD) is not a file the line names.
func (lc *lineCheck) leavesStubs(rcs []ranCmd) bool {
	for _, s := range []string{"PATH", "hash", "BASH_CMDS", "enable", "exec", "command -p", "command -vp", "command -pv"} {
		if strings.Contains(lc.line, s) {
			return true
		}
	}
	stubDir := lc.j.root + "/bin/"
	for _, rc := range rcs {
		if rc.path == "" && strings.Contains(rc.name, "/") && !strings.HasPrefix(rc.name, stubDir) {
			return true
		}
		if rc.name == "command" && len(rc.args) > 0 && strings.HasPrefix(rc.args[0], "-") && strings.Contains(rc.args[0], "p") {
			return true
		}
	}
	return false
}

// letGo reports the one place where the engine says in so many words
// that it lets a guard go unchecked: with permissions skipped, a command
// that names a file in a way that cannot be known ahead of time is not
// held to the Read and Edit rules (see safety).
func (lc *lineCheck) letGo(e *Engine, rs ruleSet) bool {
	if rs.mode != ModeBypass {
		return false
	}
	req := &request{canon: toolBash, view: e.viewBash(lc.line)}
	e.shellPaths(req)
	return req.pathOpaque != ""
}

// sourcesFile reports whether the run sourced a file. What a file holds
// is the file's business: no rule reads a program's own script.
func sourcesFile(rcs []ranCmd) bool {
	for _, rc := range rcs {
		// A shell started with -l or -i reads its startup files first
		// (/etc/profile runs id): programs of their own, like any file
		// that is sourced.
		if lineShells[baseName(rc.name)] {
			for _, a := range rc.args {
				if a == "--login" || len(a) > 1 && a[0] == '-' && a[1] != '-' && strings.ContainsAny(a, "li") {
					return true
				}
				if !strings.HasPrefix(a, "-") && !strings.HasPrefix(a, "+") {
					break
				}
			}
		}
		words := append([]string{rc.name}, rc.args...)
		// A real shell that is handed a script file, reached past the
		// stubs ("command -p bash notes.md", "/bin/sh x.sh"), runs a
		// program of its own. (A stub named like a shell runs no file.)
		if rc.path == "" && realShellFile(words) {
			return true
		}
		for i, w := range words {
			if (w == "." || w == "source") && (i == 0 || rc.name == "command" || rc.name == "builtin") && i+1 < len(words) {
				if f := words[i+1]; f != "/dev/stdin" && !strings.HasPrefix(f, "/dev/fd/") && !strings.HasPrefix(f, "/proc/self/fd/") {
					return true
				}
			}
		}
	}
	return false
}

// realShellFile reports whether the words are a real shell, reached past
// the directory of stubs, that is handed a script file: "command -p bash
// notes.md", "/bin/sh x.sh", "exec -a x /bin/bash notes.md".
func realShellFile(words []string) bool {
	i, past := 0, false
	if len(words) > 0 && (words[0] == "command" || words[0] == "exec") {
		for i = 1; i < len(words) && strings.HasPrefix(words[i], "-"); i++ {
			if words[0] == "command" && !strings.HasPrefix(words[i], "--") && strings.Contains(words[i], "p") {
				past = true
			}
			if words[0] == "exec" && words[i] == "-a" {
				i++
			}
		}
	}
	if i >= len(words) {
		return false
	}
	if past = past || strings.Contains(words[i], "/"); !past || !lineShells[baseName(words[i])] {
		return false
	}
	for _, a := range words[i+1:] {
		if strings.HasPrefix(a, "-") || strings.HasPrefix(a, "+") {
			if !strings.HasPrefix(a, "--") && strings.Contains(a, "c") {
				return false // the script is the text after -c
			}
			continue
		}
		return true
	}
	return false
}

// runsFile reports whether the line, as the engine read it, starts a
// real shell on a script file (realShellFile): dash's trace does not
// always show the command that did.
func (lc *lineCheck) runsFile() bool {
	for _, u := range lc.view.units {
		if len(u.cmd.words) > 1 && allStatic(u.cmd.words) && realShellFile(litsOf(u.cmd.words)) {
			return true
		}
	}
	return false
}

// traceSources is sourcesFile for a command dash traced with its
// assignments in front, on one line and without quotes ("X= . file"):
// such a line is not taken for a command anywhere else, because "X= a"
// may as well be the assignment X=' a'.
func traceSources(r shellRun) bool {
	if r.shell != "dash" {
		return false
	}
	for _, t := range r.trace {
		ws := t.words
		for len(ws) > 1 {
			if _, ok := assignName(ws[0]); !ok && ws[0] != "command" && ws[0] != "builtin" {
				break
			}
			ws = ws[1:]
		}
		if len(ws) > 1 && len(ws) < len(t.words) && (ws[0] == "." || ws[0] == "source") {
			return true
		}
	}
	return false
}

// widened says how a run went beyond what the engine let through, or "".
// It is only asked about lines the engine allowed on its rules or its
// read-only list.
func (lc *lineCheck) widened(e *Engine, rs ruleSet, d Decision, r shellRun, rcs []ranCmd) string {
	j := lc.j
	runs := d.Stage == StageAllow && lc.ruleRuns(e)
	for _, rc := range rcs {
		if rc.fuzzy {
			continue
		}
		if d.Stage == StageAllow && rc.by == 0 {
			// Started by a program that a rule allowed: what that program
			// runs is the rule's business.
			continue
		}
		covered := false
		for _, u := range lc.view.units {
			// The same command, word for word; or one a rule allowed as
			// written, where what a variable in it turned into is the
			// rule's business.
			m := e.unitMatch(u, rc)
			if m == 2 || m == 1 && e.ruleAllows(u.cmd.words) || m == 1 && r.shell == "dash" && strings.Contains(lc.line, "$'") {
				covered = true
				break
			}
		}
		if rc.path != "" && !runs {
			if p := rc.fd[0]; j.treeFile(p) && !e.readable(cleanPath(p)) {
				return "a command read its input from outside the project: " + rc.text() + " < " + p
			}
			for _, p := range rc.fd[1:] {
				if j.treeFile(p) {
					return "a command wrote to a file: " + rc.text() + " > " + p
				}
			}
		}
		if covered || runs {
			continue
		}
		if rc.cwd != "" && !inside(j.root, rc.cwd) {
			return "ran outside the jail: " + rc.text()
		}
		dirs := []string{rc.cwd}
		if rc.cwd == "" {
			dirs = e.lineDirs(lc.view)
		}
		line := j.render(rc)
		for _, dir := range dirs {
			e2 := j.engine(rs)
			e2.SetCwd(dir)
			if bash(e2, line).Behavior == Allow {
				covered = true
				break
			}
		}
		if !covered {
			return "ran a command the engine neither read in the line nor lets run: " + line
		}
	}
	for _, ch := range r.changes {
		// A change the shell made itself is never let through. One made by
		// a real program is the rule's business when a rule allowed the
		// line, and acceptEdits' when it stays in the project.
		// (A link that was removed or copied is judged where it stands,
		// not where it leads; and what a cp or mv of a whole directory
		// puts down is judged by the directory, whatever is in it.)
		here := inside(j.work, ch.path) && e.inWork(parentDir(ch.path))
		carried := ch.what == "created" && (strings.Contains(lc.line, "cp ") || strings.Contains(lc.line, "mv "))
		ok := r.mode[0] != 'p' && (d.Stage == StageAllow ||
			rs.mode == ModeAcceptEdits && here && (carried || e.protectedWhy(ch.path) == ""))
		if !ok && !runs {
			return fmt.Sprintf("a file was %s without a question: %s", ch.what, ch.path)
		}
	}
	if d.Stage == StageMode {
		// The line ran only because it reads inside the project (or, in
		// acceptEdits, changes files there): nothing from outside it may
		// show up.
		paths := map[string]string{tokOutside: j.outside + "/secret.txt", tokOutDir: j.outside + "/dir/file",
			tokSSH: j.home + "/.ssh/id_ed25519", tokHome: j.home + "/notes.txt", tokPasswd: "/etc/passwd"}
		for _, tok := range outsideTokens {
			if e.readable(paths[tok]) {
				continue
			}
			seen := strings.Contains(r.out, tok)
			for _, rc := range rcs {
				for _, a := range rc.args {
					seen = seen || strings.Contains(a, tok)
				}
			}
			if seen {
				return "a line that ran because it only reads the project showed " + paths[tok]
			}
		}
	}
	return ""
}

// disagreement says how the shell's commands differ from the engine's
// reading of the line, or "". It is asked only where the engine says it
// followed the whole line.
func (lc *lineCheck) disagreement(r shellRun, rcs []ranCmd) string {
	v, e := lc.view, lc.plain
	if !v.sc.ok || v.opaque != "" || v.tooMany {
		return ""
	}
	if sourcesFile(rcs) || traceSources(r) || lc.runsFile() {
		return "" // a script file is a program of its own, which the engine does not read
	}
	if strings.Contains(lc.line, "PS4") || strings.Contains(lc.line, "XTRACE") || strings.Contains(lc.line, "set +") {
		return "" // the line turns the trace off, or makes it unreadable
	}
	if strings.Contains(lc.line, "-C ''") || strings.Contains(lc.line, "-C \"\"") {
		// compgen -C with an empty command: the words bash adds for the
		// callback become a command of their own ("compgen '' ''").
		return ""
	}
	if _, moved := v.vars["HOME"]; moved || strings.Contains(lc.line, "HOME=") {
		// The line moves HOME, and with it what ~ stands for. The engine
		// asks about such a line, and holds both places against its
		// guards; which of the two a word then is, it does not claim.
		return ""
	}
	if r.shell == "dash" && (strings.Contains(lc.line, "function") || dashArg.MatchString(lc.line)) {
		// Forms the engine reads as bash does and refuses to let run:
		// under dash "function f {" is a command, and {name}>file passes
		// {name} on as an argument.
		return ""
	}
	if r.shell == "dash" && (strings.Contains(lc.line, "$'") || strings.Contains(lc.line, "$\"")) {
		// dash has no $'...': it reads a dollar sign and a quoted string.
		// The engine reads it as bash does, which is never the safer of
		// the two (see doc.go).
		return ""
	}
	for _, rc := range rcs {
		if rc.by != 2 {
			continue
		}
		best := 0
		for _, u := range v.units {
			best = max(best, e.unitMatch(u, rc))
			// bash's time takes -p and nothing else: any other word that
			// starts with a dash is the command. The engine reads on past
			// it, to whatever command may follow.
			if ws := u.cmd.words; len(ws) > 1 && ws[0].raw == "time" && strings.HasSuffix(strings.Join(litsOf(ws), " "), rc.text()) {
				best = max(best, 1)
			}
		}
		if best == 0 {
			return "the shell ran a command the engine did not read: " + lc.j.render(rc) + "; the engine read " + unitsText(v)
		}
	}
	// The other way round, for a line of plain commands one after the
	// other: each one the engine read must have run.
	if r.shell != "bash" || r.mode != "p0" || r.timedOut || r.exit > 128 {
		return ""
	}
	sc := v.sc
	if len(v.refs) > 0 {
		// A name reference whose value is no name ("declare -n d; d=~") is
		// an error that ends the line.
		return ""
	}
	// (Every branch of an if, a case or a loop is read, and a group's
	// commands are read whatever runs before them; which of them run is
	// for the shell to say.)
	if sc.subst || sc.subshell || sc.compound || sc.cond || sc.forLoop || sc.group || sc.background || strings.HasPrefix(strings.TrimLeft(lc.line, " \t\n"), "-") {
		// (The shell tool hands the line to eval as it is, and eval takes
		// a line that starts with a dash for an option: nothing runs.)
		return ""
	}
	var want []*simpleCmd
	for _, c := range sc.cmds {
		for _, a := range c.assigns {
			// An assignment to one of the shell's own read-only
			// variables is an error that ends the line.
			switch a.name {
			case "BASHOPTS", "SHELLOPTS", "BASH_VERSINFO", "EUID", "PPID", "UID":
				return ""
			}
			// So is a subscript bash cannot work out as a number
			// ("a[function[[0]=1 pwd"): the command after it does not run.
			if strings.HasPrefix(a.w.raw, a.name+"[") {
				return ""
			}
		}
		if len(c.words) == 0 {
			continue
		}
		if c.sep != sepStart && c.sep != sepSemi {
			return ""
		}
		name, ok := e.known(c.words[0])
		if !ok || !knownCommand(name) {
			// Only commands that are there to run are waited for: a word
			// no shell knows may be refused before it gets that far.
			return ""
		}
		for _, w := range c.words[1:] {
			// An expansion can fail and take the command with it
			// (${!X} with X unset).
			if _, ok := e.known(w); !ok {
				return ""
			}
		}
		switch name {
		case "[[":
			// bash refuses a test it cannot make sense of, and runs
			// nothing.
			return ""
		case "exit", "exec", "return", "kill", "set", "trap", "eval", "command", "builtin", "wait", "time",
			"break", "continue", "shopt", "enable", "alias", "unalias", "hash", "suspend", "logout", "ulimit":
			return ""
		}
		want = append(want, c)
	}
	var got []ranCmd
	for _, t := range r.trace {
		if _, ok := assignName(t.words[0]); !ok {
			got = append(got, ranCmd{name: t.words[0], args: t.words[1:], raw: t.raw})
		}
	}
	i := 0
	for _, c := range want {
		found := false
		for i < len(got) {
			m := e.unitMatch(&unit{cmd: c}, got[i])
			i++
			if m > 0 {
				found = true
				break
			}
		}
		if !found {
			return "the engine read a command the shell did not run: " + strings.Join(litsOf(c.words), " ")
		}
	}
	return ""
}

func unitsText(v *bashView) string {
	var out []string
	for _, u := range v.units {
		if len(u.cmd.words) > 0 {
			out = append(out, "["+strings.Join(litsOf(u.cmd.words), " ")+"]")
		}
	}
	return strings.Join(out, " ")
}

// conditional reports whether what a line runs may depend on how its
// commands exit.
func conditional(line string) bool {
	for _, s := range []string{"&&", "||", "if ", "while ", "until ", "!", "elif "} {
		if strings.Contains(line, s) {
			return true
		}
	}
	return false
}

// mentions reports whether the line holds one of the names as a word.
func mentions(line string, names map[string]bool) bool {
	for _, f := range strings.FieldsFunc(line, func(r rune) bool {
		return !(r == '_' || r == '-' || r == '.' || r >= '0' && r <= '9' || r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z')
	}) {
		if names[f] {
			return true
		}
	}
	return false
}

// dashArg finds {name} before a redirection.
var dashArg = regexp.MustCompile(`\{[A-Za-z_][A-Za-z0-9_]*\}[<>]`)

var (
	realNames = map[string]bool{"sh": true, "bash": true, "dash": true}
	editNames = map[string]bool{"rm": true, "rmdir": true, "mv": true, "cp": true, "mkdir": true, "touch": true, "sed": true}
)

func init() {
	for _, n := range strings.Fields("ls cat head tail wc nl grep which diff stat du sort uniq cut tr basename dirname realpath readlink file whoami uname seq find sed date git env xargs timeout nice nohup stdbuf setsid time strings ps ss sleep") {
		realNames[n] = true
	}
}

// checkLine judges one line under the rule sets and returns every
// disagreement with what the shells did. quick limits the shells and
// modes to the ones named, for shrinking a finding.
func (j *jail) checkLine(line string, sets []ruleSet, only *finding) []finding {
	line = j.fill(line)
	var out []finding
	type judged struct {
		rs ruleSet
		e  *Engine
		d  Decision
	}
	var js []judged
	for _, rs := range sets {
		if only != nil && only.set != rs.name {
			continue
		}
		e := j.engine(rs)
		if e == nil {
			continue
		}
		js = append(js, judged{rs, e, bash(e, line)})
	}
	plain := j.engine(ruleSet{name: "plain", mode: ModeDefault})
	lc := &lineCheck{j: j, line: line, plain: plain, view: plain.viewBash(line)}
	if len(line) > maxCommandLen || strings.IndexByte(line, 0) >= 0 {
		// The engine must ask; no shell is shown the line.
		for _, x := range js {
			if x.d.Behavior == Allow && !x.rs.bare() && x.rs.mode != ModeBypass {
				out = append(out, finding{"widen", x.rs.name, "-", "-", line, "a line gnosh cannot read was allowed"})
			}
		}
		return out
	}
	type pass struct{ shell, mode string }
	passes := []pass{{"bash", "p0"}, {"dash", "p0"}}
	if conditional(line) {
		passes = append(passes, pass{"bash", "p1"}, pass{"dash", "p1"})
	}
	if mentions(line, realNames) {
		passes = append(passes, pass{"bash", "r0"}, pass{"dash", "r0"}, pass{"bash", "q0"})
	}
	if mentions(line, editNames) {
		for _, x := range js {
			if x.rs.mode == ModeAcceptEdits && x.d.Behavior == Allow && x.d.Stage == StageMode {
				passes = append(passes, pass{"bash", "e0"}, pass{"dash", "e0"}, pass{"bash", "f0"})
				break
			}
		}
	}
	for _, p := range passes {
		if only != nil && (only.shell != p.shell || only.mode != p.mode) {
			continue
		}
		r := j.run(p.shell, p.mode, line)
		if r.broken != "" {
			continue
		}
		rcs := j.ran(r)
		if only == nil || only.kind == "agree" {
			if why := lc.disagreement(r, rcs); why != "" {
				out = append(out, finding{"agree", "-", p.shell, p.mode, line, why})
			}
		}
		for _, x := range js {
			if x.d.Behavior != Allow {
				continue
			}
			if only == nil || only.kind == "deny" {
				if why := lc.guardHit(x.e, x.rs, r, rcs); why != "" {
					out = append(out, finding{"deny", x.rs.name, p.shell, p.mode, line, why})
					continue
				}
			}
			if x.rs.bare() || x.rs.mode == ModeBypass {
				continue
			}
			// A file command acceptEdits lets run is only real in the e
			// modes; elsewhere it is a stub, and still must be covered.
			if only == nil || only.kind == "widen" {
				if why := lc.widened(x.e, x.rs, x.d, r, rcs); why != "" {
					out = append(out, finding{"widen", x.rs.name, p.shell, p.mode, line, why})
				}
			}
		}
	}
	return out
}

// shrink makes a finding's line as short as it can while the same kind of
// finding, under the same rules and shell, is still there.
func (j *jail) shrink(f finding, sets []ruleSet) finding {
	tries := 0
	still := func(line string) (finding, bool) {
		if tries++; tries > 300 {
			return finding{}, false
		}
		for _, g := range j.checkLine(line, sets, &f) {
			if g.kind == f.kind && g.key() == f.key() {
				return g, true
			}
		}
		return finding{}, false
	}
	best, line := f, f.line
	sweep := func(size int) bool {
		cut := false
		for at := 0; at+size <= len(line); {
			try := line[:at] + line[at+size:]
			if g, ok := still(try); ok && try != "" {
				line, best, cut = try, g, true
				continue
			}
			at += size
		}
		return cut
	}
	for size := len(line) / 2; size >= 1; size /= 2 {
		sweep(size)
	}
	for i := 0; i < 4 && sweep(1); i++ {
	}
	return best
}

// sortFindings orders findings so that a report reads the same each time.
func sortFindings(fs []finding) {
	sort.SliceStable(fs, func(a, b int) bool {
		if fs[a].kind != fs[b].kind {
			return fs[a].kind > fs[b].kind
		}
		return len(fs[a].line) < len(fs[b].line)
	})
}
