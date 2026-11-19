package permission

import (
	"encoding/json"
	"net/url"
	"path"
	"strings"

	"github.com/gnosh-ai/gnosh/internal/core"
)

// Decision is the engine's answer about one tool call.
type Decision struct {
	// Behavior is Allow, Ask or Deny.
	Behavior Behavior
	// Reason is one plain sentence saying why.
	Reason string
	// Rule is the rule that decided, as written, and Source is where it
	// came from. Both are empty when no rule did.
	Rule   string
	Source string
	// Stage is the step of the decision order that settled the call.
	Stage Stage
	// Critical marks a dangerous removal. No rule and no hook approves
	// one; in ModeBypass the caller asks with a time limit, and refuses
	// where nobody can answer.
	Critical bool
	// Suggestions are the choices a "yes, and don't ask again" answer
	// could make, narrowest first. It is empty when no rule could cover
	// the call, and then only a one-time yes is offered.
	Suggestions []Suggestion
	// Mode is the mode the decision was made in.
	Mode Mode

	// hookAllow says a PreToolUse hook's approval would let the call run.
	hookAllow bool
}

// Suggestion is one way to stop being asked about calls like this one.
type Suggestion struct {
	// Rules are allow rules to add, all of them together.
	Rules []string
	// Scope says where the spec keeps such an approval: for the session
	// only, or in the project's local settings.
	Scope GrantScope
	// Mode, when set, means the suggestion is to switch the session to
	// that mode instead of adding rules.
	Mode Mode
}

// HookVerdict is what PreToolUse hooks said about a call.
type HookVerdict string

const (
	// HookNone: no hook had an opinion.
	HookNone HookVerdict = ""
	// HookAllow: a hook approved the call.
	HookAllow HookVerdict = "allow"
	// HookAsk: a hook wants a person to decide.
	HookAsk HookVerdict = "ask"
	// HookDeny: a hook blocked the call.
	HookDeny HookVerdict = "deny"
)

// WithHook folds in what the PreToolUse hooks said. The engine's own
// decision must have been made on the input the hooks left (a hook may
// rewrite it). A hook's block always wins. A hook's "ask" puts the call
// to a person even when it would have run. A hook's approval skips the
// question the mode would have asked, and nothing else: not a deny rule,
// not an ask rule, not a safety check, not plan mode's hold on changes.
func (d Decision) WithHook(v HookVerdict, reason string) Decision {
	switch v {
	case HookDeny:
		return Decision{Behavior: Deny, Reason: orDefault(reason, "A hook blocked this call."), Stage: StageHook, Mode: d.Mode}
	case HookAsk:
		if d.Behavior == Deny {
			return d
		}
		out := Decision{Behavior: Ask, Reason: orDefault(reason, "A hook asked for a person to decide."), Stage: StageHook, Mode: d.Mode, Critical: d.Critical}
		if d.Mode == ModeDontAsk {
			out.Behavior = Deny
		}
		return out
	case HookAllow:
		if d.hookAllow && d.Behavior != Allow {
			return Decision{Behavior: Allow, Reason: orDefault(reason, "A hook approved this call."), Stage: StageHook, Mode: d.Mode}
		}
	}
	return d
}

func orDefault(s, def string) string {
	if strings.TrimSpace(s) == "" {
		return def
	}
	return s
}

// request is one tool call with what was worked out about it.
type request struct {
	canon string
	info  toolInfo
	input map[string]json.RawMessage
	// view is the reading of a shell command.
	view *bashView
	// reads and writes are the cleaned absolute paths the call would
	// read and change, each as written and as resolved.
	reads, writes []string
	// unresolved is set when a path's links could not be followed.
	unresolved bool
	// writeOpaque is not empty when a command names a file to write by
	// text computed when it runs, which cannot be told: it says how.
	writeOpaque string
	// pathOpaque is not empty when a command names a file in a way that
	// cannot be held against the Read and Edit rules. It says how.
	pathOpaque string
	// pathCapped is not empty when the files a command names could have
	// been worked out, and there were more of them than the engine
	// follows. It says of what.
	pathCapped string
	// byNumber is a path of the call that names a file by a number
	// (macOS's /.vol/<disk>/<file>): no rule about paths, and no
	// protected path, can be held against it.
	byNumber string
	// host is the lower-case host of a fetch; "" when the URL has none.
	host string
	// remote is the declared host a shell command would run on.
	remote string
	// problem is set when the call cannot be judged at all.
	problem string
}

// Decide judges one tool call. tool is the tool's name, call is the
// model's request, and action is the tool's own account of what the call
// would do. The answer for a call that needs approval where nobody can
// give it is the caller's: Ask then means refuse.
func (e *Engine) Decide(tool string, call core.ToolCall, action core.Action) Decision {
	e.mu.RLock()
	defer e.mu.RUnlock()
	return e.decideLocked(tool, call, action)
}

// DecideAs judges a call as Decide does, but as if the session were in
// another mode. It is for a sub-agent whose definition gives it a mode of
// its own: the rules, the directories and every approval are the
// session's, only the mode differs. A mode gnosh does not have, or
// bypassPermissions where the settings forbid it, refuses the call.
func (e *Engine) DecideAs(mode Mode, tool string, call core.ToolCall, action core.Action) Decision {
	e.mu.RLock()
	defer e.mu.RUnlock()
	if !mode.valid() || mode == ModeBypass && e.noBypass {
		return Decision{Behavior: Deny, Stage: StageMode, Mode: e.mode,
			Reason: "The call was to be judged in the mode \"" + string(mode) + "\", which is not one this session may use."}
	}
	as := *e
	as.mode = mode
	return as.decideLocked(tool, call, action)
}

// DecideAt judges a call as Decide does, with the shell in dir: relative
// paths in a command are read from there. It is for a program in which
// several agents share one engine and each has a shell of its own, in a
// directory of its own: telling the engine where the shell is (SetCwd) and
// asking it (Decide) are two steps, and between them another agent's call
// may have told it something else, so that "rm -rf *" in the project would
// be judged as the same words in some folder under it. Here the directory
// and the question are one step, and the engine's own directory is left as
// it is. A dir that is not an absolute path changes nothing.
func (e *Engine) DecideAt(dir, tool string, call core.ToolCall, action core.Action) Decision {
	e.mu.RLock()
	defer e.mu.RUnlock()
	if !strings.HasPrefix(dir, "/") {
		return e.decideLocked(tool, call, action)
	}
	at := *e
	at.cur = path.Clean(dir)
	return at.decideLocked(tool, call, action)
}

// CommandDirs is where the shell may be while the parts of a command run,
// for a shell started in dir: dir itself, and wherever a cd or a pushd in
// the line may lead. why is "" when that can be told, and otherwise says
// what keeps it from being told: a line gnosh cannot read to its end, text
// run as a script that gnosh cannot read, a cd to a place only known when
// the line runs.
func (e *Engine) CommandDirs(dir, command string) (dirs []string, why string) {
	e.mu.RLock()
	defer e.mu.RUnlock()
	at := *e
	if strings.HasPrefix(dir, "/") {
		at.cur = path.Clean(dir)
	}
	v := at.viewBash(command)
	if why := unreadable(v); why != "" {
		return nil, why
	}
	// (Where the shell is: a folder a program's own flag sends it to is
	// judged below, and git's by GitPlaces.)
	dirs, unknown := at.dirsOf(v, false)
	switch {
	case unknown:
		return nil, "a cd in it goes to a place only known when it runs"
	case v.dirsCapped:
		return nil, "it goes to more folders than gnosh follows"
	}
	if name := cdChanger(v, command); name != "" {
		return nil, "it sets " + name + ", which changes where a cd goes"
	}
	if prog := chdirFlagUse(v); prog != "" {
		return nil, prog + " in it may run in another folder, which a flag of its own names, and gnosh does not follow what it names from there; cd there first instead"
	}
	return dirs, ""
}

// LinkAndMore reports whether a command line makes a link (ln, link, or a
// cp that links: -l, -s, --link, --symbolic-link) and runs anything else
// besides: what the rest names may lead through the link, which is not
// there yet when gnosh reads the line.
func (e *Engine) LinkAndMore(command string) bool {
	e.mu.RLock()
	defer e.mu.RUnlock()
	v := e.viewBash(command)
	links, others := false, 0
	for _, u := range v.units {
		ws := u.cmd.words
		if len(ws) == 0 || !ws[0].static() {
			others++
			continue
		}
		name := baseName(ws[0].joined())
		if _, wraps := wrappers[name]; wraps {
			continue // what it runs is a unit of its own
		}
		if makesLink(name, ws) {
			links = true
			continue
		}
		others++
	}
	return links && others > 0
}

// makesLink reports whether the command, its program name and its words,
// makes a link.
func makesLink(name string, ws []word) bool {
	switch name {
	case "ln", "link":
		return true
	case "cp":
		for _, w := range ws[1:] {
			l := w.lit()
			if l == "--link" || l == "--symbolic-link" || strings.HasPrefix(l, "-") && !strings.HasPrefix(l, "--") && strings.ContainsAny(l, "ls") {
				return true
			}
		}
	}
	return false
}

// CommandWrites are the files a shell command line names for writing, as
// absolute paths, for a shell started in dir: a redirection's target,
// cp's and mv's last word, sed -i's files and the like, and each with its
// links followed where the engine follows them. A file written by a
// program on its own account is not there. unknown says why a file it
// names for writing cannot be told, by text computed when the line runs
// ("> "$(git rev-parse --git-common-dir)/config""); "" when none is.
func (e *Engine) CommandWrites(dir, command string) (writes []string, unknown string) {
	e.mu.RLock()
	defer e.mu.RUnlock()
	at := *e
	if strings.HasPrefix(dir, "/") {
		at.cur = path.Clean(dir)
	}
	req := &request{canon: toolBash}
	req.view = at.viewBash(command)
	at.shellPaths(req)
	return req.writes, req.writeOpaque
}

// cdChanger names a variable the line may set that changes where its cd,
// or its ~, goes: CDPATH, which a cd to a plain name searches, where the
// line has a cd or a pushd; HOME, where a cd with no folder and ~ lead,
// where it has a cd, a pushd or a ~. "" when it sets neither where that
// matters. text is the line as written.
func cdChanger(v *bashView, text string) string {
	cds := false
	for _, u := range v.units {
		if ws := u.cmd.words; len(ws) > 0 && ws[0].static() {
			if n := baseName(ws[0].joined()); n == "cd" || n == "pushd" {
				cds = true
			}
		}
	}
	tilde := strings.Contains(text, "~")
	for _, name := range []string{"CDPATH", "HOME"} {
		if (cds || name == "HOME" && tilde) && setsVar(v, name) {
			return name
		}
	}
	return ""
}

// setsVar reports whether the line may give the variable name a value: by
// an assignment, or by naming it as the variable a command gives a value
// to (read, printf -v, mapfile, export, declare, unset ...), or in a
// ${name:=word}. A word that is the name alone counts, whatever runs it.
func setsVar(v *bashView, name string) bool {
	for _, u := range v.units {
		for _, a := range u.cmd.assigns {
			if a.name == name {
				return true
			}
		}
		for _, w := range u.cmd.words {
			val := w.raw
			if w.static() {
				val = w.value("")
			}
			if val == name || val == "-v"+name || strings.HasPrefix(val, name+"=") || strings.HasPrefix(val, name+"+=") ||
				strings.Contains(w.raw, "${"+name+":=") || strings.Contains(w.raw, "${"+name+"=") {
				return true
			}
		}
	}
	return false
}

// chdirFlags are the programs that run themselves, or the command they are
// given, in a folder a flag of theirs names: the flag's letter, and its
// long name, which may be given shortened.
var chdirFlags = map[string]struct{ short, long string }{
	"env":   {"C", "chdir"},
	"sudo":  {"D", "chdir"},
	"make":  {"C", "directory"},
	"gmake": {"C", "directory"},
	"tar":   {"C", "directory"},
	"ninja": {"C", ""},
	"go":    {"C", ""},
}

// chdirFlagUse names a program of the line that may be sent to another
// folder by a flag of its own (chdirValues).
func chdirFlagUse(v *bashView) string {
	for _, u := range v.units {
		ws := u.cmd.words
		if len(ws) == 0 || !ws[0].static() {
			continue
		}
		if name := progKey(baseName(ws[0].joined())); len(chdirValues(name, ws[1:])) > 0 {
			return name
		}
	}
	return ""
}

// gitChdirs are the folders git's own -C words name, before its
// subcommand, in their order (each goes on from the one before it: git -C
// a -C b works in a/b), and any word of git's own that is only known when
// the line runs, which may be one. After the subcommand a -C is the
// subcommand's (git log -C finds copies).
func gitChdirs(args []word) (out []word) {
	for i := 0; i < len(args); i++ {
		a := args[i]
		if !a.static() {
			return append(out, a)
		}
		switch l := a.joined(); {
		case l == "-C":
			if i+1 < len(args) {
				i++
				out = append(out, args[i])
			}
		case l == "-c" || l == "--git-dir" || l == "--work-tree" || l == "--namespace" || l == "--config-env" || l == "--attr-source":
			i++ // its value
		case strings.HasPrefix(l, "-"):
		default:
			return out // the subcommand
		}
	}
	return out
}

// gitRuns are the commands that git settings a line gives make git run:
// the value of git's own -c, or one that git config writes, for a key
// whose value is a command ("git -c core.sshCommand='rm -rf ~' fetch"),
// and an alias's: what follows the ! of one that starts with ! is a shell
// command ("git -c alias.z='!rm -rf ~' z"), and any other is a git
// command ("git -c alias.p=push p" pushes).
func gitRuns(args []word) (out []string) {
	setting := func(w word) {
		if !w.static() {
			return
		}
		key, val, ok := strings.Cut(w.joined(), "=")
		switch k := strings.ToLower(key); {
		case !ok || val == "":
		case strings.HasPrefix(k, "alias.") && strings.HasPrefix(val, "!"):
			out = append(out, val[1:])
		case strings.HasPrefix(k, "alias."):
			out = append(out, "git "+val)
		case gitCommandKey(k):
			out = append(out, strings.TrimLeft(val, "!| "))
		}
	}
	i := 0
own:
	for ; i < len(args); i++ {
		switch l := args[i].joined(); {
		case !args[i].static():
			return out
		case l == "-c":
			if i+1 < len(args) {
				i++
				setting(args[i])
			}
		case l == "-C" || l == "--git-dir" || l == "--work-tree" || l == "--namespace" || l == "--config-env" || l == "--attr-source":
			i++
		case strings.HasPrefix(l, "-"):
		default:
			break own // the subcommand
		}
	}
	if i >= len(args) || args[i].joined() != "config" {
		return out
	}
	// git config [options] key value, and git config set key value: the
	// first two words that are no options.
	var words []word
	rest := args[i+1:]
	for j := 0; j < len(rest); j++ {
		switch l := rest[j].joined(); {
		case l == "set" && len(words) == 0:
		case l == "-f" || l == "--file" || l == "--blob" || l == "--default" || l == "--comment" || l == "--value" || l == "--type":
			j++ // its value
		case strings.HasPrefix(l, "-"):
		default:
			words = append(words, rest[j])
		}
	}
	if len(words) >= 2 && words[0].static() {
		w := litOf(words[0].joined() + "=" + words[1].joined())
		if words[1].static() {
			setting(w)
		}
	}
	return out
}

// gitCommandKey reports whether the value of git's setting k (in lower
// case) is a command git runs, or a program.
func gitCommandKey(k string) bool {
	switch k {
	case "core.pager", "core.editor", "core.sshcommand", "core.askpass", "core.fsmonitor", "core.gitproxy", "diff.external",
		"sequence.editor", "interactive.difffilter", "uploadpack.packobjectshook", "web.browser":
		return true
	}
	if strings.HasPrefix(k, "pager.") {
		return true // the pager of one subcommand
	}
	for _, end := range []string{".command", ".textconv", ".driver", ".clean", ".smudge", ".process", ".cmd", ".program", ".helper", ".receivepack", ".uploadpack"} {
		if strings.HasSuffix(k, end) {
			return true
		}
	}
	return false
}

// chdirValues are the folders the program name, given args, may be sent
// to by a flag of its own (chdirFlags), in any of the ways the flag is
// written (-C dir, -Cdir, -sC dir, --directory=dir, --dir dir), and each
// of its words whose text is only known when the line runs, which may be
// that flag or its folder. A wrapper's own words end where the command it
// runs begins (env FOO=1 grep -C 3 x leaves grep's -C to grep). tar's old
// way of writing its letters with no dash, one of them C, takes the folder
// from the words after them, any of which may be it.
func chdirValues(name string, args []word) (out []word) {
	f, ok := chdirFlags[name]
	if !ok {
		return nil
	}
	spec, wraps := wrappers[name]
	if name == "tar" && len(args) > 0 && args[0].static() && !strings.HasPrefix(args[0].value(""), "-") && strings.Contains(args[0].value(""), f.short) {
		return args[1:]
	}
	for i := 0; i < len(args); i++ {
		w := args[i]
		if !w.static() {
			out = append(out, w)
			continue
		}
		s := w.value("")
		value := func(attached string, has bool) {
			switch {
			case has:
				out = append(out, partOf(w, attached))
			case i+1 < len(args):
				i++
				out = append(out, args[i])
			}
		}
		switch {
		case s == "--":
			return out
		case strings.HasPrefix(s, "--"):
			long, val, eq := strings.Cut(s[2:], "=")
			if f.long != "" && len(long) >= 2 && strings.HasPrefix(f.long, long) {
				value(val, eq)
				continue
			}
		case strings.HasPrefix(s, "-") && len(s) > 1:
			if j := strings.Index(s[1:], f.short); j >= 0 {
				rest := s[2+j:]
				value(rest, rest != "")
				continue
			}
		case wraps && strings.Contains(s, "="):
			// env and sudo take NAME=value words before the command.
		case wraps:
			return out // the command it runs
		}
		if wraps && strings.Contains(spec.valued, " "+s+" ") {
			i++ // the flag's value
		}
	}
	return out
}

// unreadable says why a line cannot be read to know what it runs, or "".
func unreadable(v *bashView) string {
	switch {
	case !v.sc.ok:
		return "it cannot be read as a shell command (" + firstNonEmpty(v.sc.why, "the reading stopped") + ")"
	case v.opaque != "":
		return v.opaque
	case v.tooMany:
		return "it holds more commands than gnosh follows"
	}
	return ""
}

// firstNonEmpty is the first of its arguments that is not empty.
func firstNonEmpty(ss ...string) string {
	for _, s := range ss {
		if s != "" {
			return s
		}
	}
	return ""
}

// gitPlaceVars are the variables that point git at a repository or a
// work tree other than the one it runs in.
var gitPlaceVars = []string{"GIT_DIR", "GIT_WORK_TREE", "GIT_COMMON_DIR", "GIT_INDEX_FILE"}

// GitPlaces are the places a command points its git runs at, by what the
// line writes, as absolute paths read from where the line may be: the
// folder of each -C, the paths of --git-dir and --work-tree, and the git
// variables (GIT_DIR, GIT_WORK_TREE ...) set for a command or for the
// line. why is "" when they can be told, and otherwise says why not: the
// line cannot be read, or one of them is only known when it runs.
func (e *Engine) GitPlaces(dir, command string) (places []string, why string) {
	e.mu.RLock()
	defer e.mu.RUnlock()
	at := *e
	if strings.HasPrefix(dir, "/") {
		at.cur = path.Clean(dir)
	}
	v := at.viewBash(command)
	if why := unreadable(v); why != "" {
		return nil, why
	}
	dirs, unknown := at.lineDirsKnown(v)
	ok := true
	add := func(text string, known bool) {
		if !known {
			ok = false
			return
		}
		if text == "" {
			return
		}
		if strings.Contains("/"+text+"/", "/../") {
			// The system goes into each part through a link first, so
			// "lnk/.." is where lnk points, and above it: not the text's
			// place (a review found it).
			ok = false
			return
		}
		if strings.HasPrefix(text, "/") {
			places = unionStrings(places, []string{path.Clean(text)})
			return
		}
		if unknown || v.dirsCapped {
			ok = false
			return
		}
		for _, d := range dirs {
			places = unionStrings(places, []string{path.Join(d, text)})
		}
	}
	varValue := func(w word) (name, text string, isVar, known bool) {
		if !w.static() {
			// A word that may be one of the variables is one.
			for _, n := range gitPlaceVars {
				if strings.HasPrefix(w.raw, n+"=") {
					return n, "", true, false
				}
			}
			return "", "", false, false
		}
		val := w.value(at.home)
		for _, n := range gitPlaceVars {
			if t, found := strings.CutPrefix(val, n+"="); found {
				return n, t, true, true
			}
		}
		return "", "", false, true
	}
	for _, u := range v.units {
		c := u.cmd
		for _, a := range c.assigns {
			if _, text, isVar, known := varValue(a.w); isVar {
				add(text, known)
			}
		}
		// export GIT_DIR=x, env GIT_DIR=x git, declare -x GIT_DIR=x.
		for _, w := range c.words {
			if _, text, isVar, known := varValue(w); isVar {
				add(text, known)
			}
		}
		if len(c.words) > 0 && !c.words[0].static() {
			// A command whose name is only known when the line runs may
			// be git, pointed anywhere ("$CMD -C /repo commit").
			ok = false
			continue
		}
		if len(c.words) == 0 {
			continue
		}
		if c.words[0].glob || c.words[0].brace {
			// "/usr/bin/gi[t] -C /repo": a pattern may name git.
			ok = false
			continue
		}
		if progKey(baseName(c.words[0].joined())) != "git" {
			// (By its key: on a disk that does not tell case apart, GIT
			// runs git.)
			continue
		}
		args := c.words[1:]
		for i := 0; i < len(args); i++ {
			a := args[i]
			if !a.static() {
				// What git reads before its subcommand cannot be told.
				ok = false
				break
			}
			l := a.joined()
			switch {
			case l == "-C" || l == "--git-dir" || l == "--work-tree":
				if i+1 < len(args) {
					i++
					add(args[i].value(at.home), args[i].static())
				}
				continue
			case strings.HasPrefix(l, "--git-dir=") || strings.HasPrefix(l, "--work-tree="):
				_, val, _ := strings.Cut(a.value(at.home), "=")
				add(val, true)
				continue
			case l == "-c" || l == "--namespace" || l == "--config-env":
				i++ // their value
				continue
			case strings.HasPrefix(l, "-"):
				continue
			}
			break // the subcommand: what follows is its own
		}
	}
	if !ok {
		return nil, "a git place or a command's name in it is only known when it runs"
	}
	return places, ""
}

// decideLocked is Decide with the lock already held.
func (e *Engine) decideLocked(tool string, call core.ToolCall, action core.Action) Decision {
	d := e.decide(CanonicalTool(tool), call, action)
	d.Mode = e.mode
	if d.Behavior == Ask && e.mode == ModeDontAsk {
		d.Behavior = Deny
		d.Reason = strings.TrimSuffix(d.Reason, ".") + "; dontAsk mode refuses what would otherwise ask."
		d.Suggestions = nil
	}
	if d.Behavior != Ask {
		d.Suggestions = nil
	}
	return d
}

func (e *Engine) decide(canon string, call core.ToolCall, action core.Action) Decision {
	if off := e.toolOff(canon); off != nil {
		if off.rule != nil {
			return Decision{Behavior: Deny, Stage: StageTool, Rule: off.rule.Rule.String(), Source: off.rule.layer.Name,
				Reason: "The deny rule " + off.rule.Rule.String() + " switches " + canon + " off."}
		}
		return Decision{Behavior: Deny, Stage: StageTool, Reason: canon + " is switched off: " + off.why + "."}
	}
	req := e.newRequest(canon, call, action)

	// 1. Deny rules, from every layer.
	if c := e.match(ListDeny, req); c != nil {
		return Decision{Behavior: Deny, Stage: StageDeny, Rule: c.Rule.String(), Source: c.layer.Name,
			Reason: "The deny rule " + c.Rule.String() + " from " + c.layer.Name + " refuses this."}
	}
	// 2. Ask rules.
	if c := e.match(ListAsk, req); c != nil {
		return Decision{Behavior: Ask, Stage: StageAsk, Rule: c.Rule.String(), Source: c.layer.Name,
			Reason: "The ask rule " + c.Rule.String() + " from " + c.layer.Name + " wants a person to decide."}
	}
	if req.problem != "" {
		return Decision{Behavior: Ask, Stage: StageSafety, Reason: req.problem}
	}

	// 3. Checks no allow rule passes.
	if d, ok := e.safety(req); ok {
		return d
	}
	if req.info.class == classPerson {
		// The tool is itself a question to the person: no mode approves
		// it on the person's behalf, and no rule needs to.
		if e.mode == ModeDontAsk {
			return Decision{Behavior: Deny, Stage: StageMode, Reason: canon + " asks the person a question, and dontAsk mode never asks."}
		}
		return Decision{Behavior: Allow, Stage: StageMode, Reason: canon + " is itself a question to the person."}
	}
	if e.mode == ModeBypass {
		return Decision{Behavior: Allow, Stage: StageMode, Reason: "Permissions are skipped (bypassPermissions)."}
	}

	// 4. Allow rules, then what the mode lets run.
	switch req.info.class {
	case classShell:
		return e.decideShell(req)
	case classFileRead:
		return e.decideRead(req)
	case classFileEdit:
		return e.decideEdit(req)
	case classFetch:
		return e.decideFetch(req)
	case classAgent, classQuiet:
		return Decision{Behavior: Allow, Stage: StageMode, Reason: canon + " never needs approval; what it starts is judged call by call."}
	}
	return e.decideOther(req)
}

// newRequest works out what a call would touch.
func (e *Engine) newRequest(canon string, call core.ToolCall, action core.Action) *request {
	req := &request{canon: canon}
	req.info, _ = e.names.info(canon)
	if len(call.Input) > 0 {
		_ = json.Unmarshal(call.Input, &req.input)
	}
	str := func(key string) string {
		var s string
		if raw, ok := req.input[key]; ok {
			_ = json.Unmarshal(raw, &s)
		}
		return s
	}
	switch req.info.class {
	case classShell:
		cmd := action.Command
		if in := str("command"); cmd == "" {
			cmd = in
		} else if in != "" && in != cmd {
			req.problem = "The command the tool reports is not the command the model sent, so it cannot be judged."
		}
		// A host that is left blank or named "local" is this machine, as
		// the shell tool reads it: the call is judged like any command
		// here. Anything else in that place is somewhere else, a value
		// that is no name at all included.
		if raw, ok := req.input["host"]; ok {
			var host string
			if json.Unmarshal(raw, &host) != nil {
				req.remote = strings.TrimSpace(string(raw))
			} else if host = strings.TrimSpace(host); host != "" && host != localHost {
				req.remote = host
			}
		}
		req.view = e.viewBash(cmd)
		e.shellPaths(req)
		if req.view.sc.ok && len(req.view.sc.cmds) == 0 && len(req.view.sc.arith) == 0 && req.problem == "" {
			req.problem = canon + " was given no command, so there is nothing to judge it by."
		}
	case classFileRead, classFileEdit:
		paths := action.Paths
		if len(paths) == 0 {
			if p := str(req.info.primary); p != "" {
				paths = []string{p}
			} else if canon == toolGrep || canon == toolGlob {
				paths = []string{e.cur}
			}
		}
		if len(paths) == 0 {
			req.problem = canon + " names no file, so there is nothing to judge it by."
		}
		for _, p := range paths {
			if !strings.HasPrefix(p, "/") {
				req.problem = canon + " was given the path " + p + ", which is not absolute."
			}
		}
		if req.info.class == classFileRead {
			req.reads = e.expand(req, paths)
		} else {
			req.writes = e.expand(req, paths)
		}
	case classFetch:
		raw := action.URL
		if raw == "" {
			raw = str("url")
		}
		if u, err := url.Parse(raw); err == nil {
			// A host that cannot be compared with the rules is no host:
			// the call then asks.
			req.host, _ = canonHost(u.Hostname())
		}
	default:
		// Any other tool that says it reads or changes files is held to
		// the Read and Edit deny and ask rules. Nothing here is ever a
		// reason to allow it.
		switch action.Kind {
		case core.ActionRead:
			req.reads = e.expand(req, absOnly(action.Paths))
		case core.ActionEdit:
			req.writes = e.expand(req, absOnly(action.Paths))
		}
	}
	return req
}

// absOnly keeps the absolute paths.
func absOnly(ps []string) []string {
	var out []string
	for _, p := range ps {
		if strings.HasPrefix(p, "/") {
			out = append(out, p)
		}
	}
	return out
}

// expand cleans each absolute path and adds where it really leads, so
// that a rule is held against the path as written and as resolved: a deny
// rule applies when either matches, an allow rule only when both do.
func (e *Engine) expand(req *request, paths []string) []string {
	var out []string
	add := func(p string) {
		for _, o := range out {
			if o == p {
				return
			}
		}
		out = append(out, p)
	}
	for _, p := range paths {
		if !strings.HasPrefix(p, "/") {
			continue
		}
		p = path.Clean(p)
		add(p)
		if byNumber(p) {
			req.byNumber = p
		}
		if e.resolve == nil {
			continue
		}
		r, err := e.resolve(p)
		if err != nil || !strings.HasPrefix(r, "/") {
			req.unresolved = true
			continue
		}
		add(path.Clean(r))
	}
	return out
}

// match returns the first rule of the list that covers the request.
func (e *Engine) match(list List, req *request) *crule {
	for _, c := range e.rules(list) {
		switch c.kind {
		case kindTool:
			if c.canon == req.canon || c.canon == toolBash && req.canon == toolMonitor {
				return c
			}
		case kindNameGlob:
			if globMatch(c.canon, req.canon) {
				return c
			}
		case kindParam:
			if c.canon == req.canon && paramMatch(req.input, c.param, c.value) {
				return c
			}
		case kindCommand:
			if req.view != nil && list != ListAllow && e.commandHit(c, req.view) {
				return c
			}
		case kindDomain:
			if req.info.class == classFetch && req.host != "" && domainMatch(c.domain, req.host) {
				return c
			}
		case kindAgent:
			if req.info.class == classAgent {
				// The kind as the tool reads it: blanks around it left
				// off, and none given (or only blanks) the general-purpose
				// one. Read as written, "" and " Explore" went past
				// Agent(general-purpose) and Agent(Explore).
				typ := ""
				if raw, ok := req.input["subagent_type"]; ok {
					_ = json.Unmarshal(raw, &typ)
				}
				if typ = strings.TrimSpace(typ); typ == "" {
					typ = "general-purpose"
				}
				if typ == c.value {
					return c
				}
			}
		case kindSkill:
			if req.info.class == classSkill && skillMatch(c, req.input, list) {
				return c
			}
		}
	}
	if list != ListAllow {
		// Read rules guard what is read; a Read deny also stops a change
		// to the same path. Edit rules guard what is changed.
		if c := e.pathHit(list, toolRead, append(append([]string(nil), req.reads...), req.writes...)); c != nil {
			if req.canon != toolNotebookEdit || len(req.reads) > 0 {
				return c
			}
		}
		if c := e.pathHit(list, toolEdit, req.writes); c != nil {
			return c
		}
	}
	return nil
}

// commandHit reports whether a deny or ask Bash rule names any command in
// the line: one of its own, one nested in it, or one found inside a
// wrapper or a script.
func (e *Engine) commandHit(c *crule, v *bashView) bool {
	// A guard goes by the program's key as well as by its name as
	// written: RM is rm where the file system takes no notice of case.
	hit := func(t string) bool { return c.cmd.match(t) || c.cmdFold.match(foldFirst(t)) }
	if hit(strings.TrimSpace(v.text)) {
		return true
	}
	for _, u := range v.units {
		if u.texts == nil {
			u.texts = e.denyTexts(u.cmd)
			if t := e.filledText(v, u.cmd); t != "" {
				u.texts = append(u.texts, t)
			}
		}
		for _, t := range u.texts {
			if hit(t) {
				return true
			}
		}
	}
	for _, f := range v.frags {
		if hit(f) {
			return true
		}
	}
	return false
}

// pathHit returns the deny or ask rule for tool (Read or Edit) that
// covers one of the paths. Within one layer a "!" pattern takes its
// matches back out of the working-directory rules listed before it,
// except where a rule shut a whole directory above the file.
func (e *Engine) pathHit(list List, tool string, paths []string) *crule {
	for _, ly := range e.all() {
		rs := ly.deny
		if list == ListAsk {
			rs = ly.ask
		}
		for _, p := range paths {
			var hit *crule
			// Deny and ask rules compare names by their keys; the path's
			// key is worked out once for all of them.
			key := ""
			for _, c := range rs {
				if c.kind != kindPath || c.canon != tool {
					continue
				}
				if key == "" {
					key = guardPath(p)
				}
				if c.negate {
					if ok, _ := c.path.matchKeyed(key); hit != nil && ok {
						hit = nil
					}
					continue
				}
				ok, viaDir := c.path.matchKeyed(key)
				if !ok {
					continue
				}
				if !c.path.relative || viaDir {
					return c
				}
				hit = c
			}
			if hit != nil {
				return hit
			}
		}
	}
	return nil
}

// allowsPath reports whether an allow rule covers reading (or, with
// edit, changing) the path. An Edit allow also grants reading.
func (e *Engine) allowsPath(p string, edit bool) *crule {
	for _, c := range e.rules(ListAllow) {
		if c.kind != kindPath || edit && c.canon != toolEdit {
			continue
		}
		if c.path.match(p) {
			return c
		}
	}
	return nil
}

// paramMatch reports whether the input has the scalar parameter with a
// value the pattern covers.
func paramMatch(input map[string]json.RawMessage, name, pat string) bool {
	raw, ok := input[name]
	if !ok {
		return false
	}
	s := strings.TrimSpace(string(raw))
	if s == "" || s[0] == '{' || s[0] == '[' || s == "null" {
		return false
	}
	if s[0] == '"' {
		var v string
		if json.Unmarshal(raw, &v) != nil {
			return false
		}
		s = v
	}
	return matchValue(pat, s)
}

// skillMatch reports whether a Skill rule covers the call.
func skillMatch(c *crule, input map[string]json.RawMessage, list List) bool {
	var name, args string
	if raw, ok := input["skill"]; ok {
		_ = json.Unmarshal(raw, &name)
	}
	if raw, ok := input["args"]; ok {
		_ = json.Unmarshal(raw, &args)
	}
	if name == "" || strings.TrimPrefix(name, "/") != c.value {
		return false
	}
	// Skill(name) allows the skill with no arguments only. As a deny or
	// ask rule it stops the skill whatever its arguments: a guard must
	// not be dodged by adding a word.
	return c.anyArgs || list != ListAllow || strings.TrimSpace(args) == ""
}

// domainMatch matches a host against a WebFetch pattern. "*" is every
// host; a leading "*." is any subdomain at any depth; any other * stays
// within one label.
func domainMatch(pat, host string) bool {
	if pat == "*" {
		return true
	}
	if rest, ok := strings.CutPrefix(pat, "*."); ok && !strings.Contains(rest, "*") {
		return strings.HasSuffix(host, "."+rest) && len(host) > len(rest)+1
	}
	pl, hl := strings.Split(pat, "."), strings.Split(host, ".")
	if len(pl) != len(hl) {
		return false
	}
	for i := range pl {
		if !globMatch(pl[i], hl[i]) {
			return false
		}
	}
	return true
}
