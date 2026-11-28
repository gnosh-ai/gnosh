package ui

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode/utf16"
	"unicode/utf8"

	"github.com/gnosh-ai/gnosh/internal/agent"
	"github.com/gnosh-ai/gnosh/internal/app"
	"github.com/gnosh-ai/gnosh/internal/core"
	"github.com/gnosh-ai/gnosh/internal/diff"
	"github.com/gnosh-ai/gnosh/internal/tools/shelltool"
)

// How a tool call is shown.
//
// While the model writes a call and while the call runs, it has a few rows
// in the live region: what it will do, as the model wrote it, and the last
// lines of its output. When it ends it is committed as one compact block:
//
//	$ go test ./internal/ui/           a command, as it was run
//	  ok  example.com/ui  0.4s         the end of its output, as it is
//	  exit 0 · 2.1s
//
//	▸ Read internal/app/frontend.go · 199 lines
//
//	▸ Edit internal/ui/theme.go        an edit, as a diff
//	  internal/ui/theme.go +1 -1
//	  ...
//
// The block is headed by what the call does, not by a name and a spinner.
// Output, code and diffs carry no border, gutter or background of gnosh's
// own, only an indentation, so that what is selected in the terminal is
// the text. Everything in it that came from the model or from a tool is
// made visible first (safe.go).

// The limits of what a block shows while detail is off. With detail on
// (Ctrl+O) a block shows everything, up to detailLines.
const (
	tailLines    = 5    // lines of a command's output
	errorLines   = 6    // lines of a failure
	resultLines  = 3    // lines of any other tool's result
	receiptLines = 12   // lines of a Gno call's result
	commandLines = 6    // lines of a command that has several
	diffLines    = 60   // lines of a diff
	detailLines  = 2000 // the most lines of anything, with detail on
	longLine     = 2000 // a line of output longer than this many bytes is cut
	liveTail     = 3    // rows of live output under a running call
	tailKeep     = 16 << 10
	outputKeep   = 128 << 10 // the end of a command's output that is looked at
)

// call is a tool call from the moment the model begins to write it until
// its result is committed.
type call struct {
	id, name string
	// input is the call's arguments: as much as has streamed while the
	// model writes them, then the arguments the tool is given.
	input []byte
	// started says the call has been put to the gate; before that the
	// model is still writing it.
	started bool
	action  core.Action
	// tail is the end of the call's live output.
	tail tailBuf
	// now is what the call runs just now, in the tool's own words
	// (agent.ToolRunningEvent): for a command, the programs its shell has
	// going. It is "" until the tool says.
	now string
	// hooks are the hooks that ran on the call, one line each.
	hooks []string
	// subs counts the tool calls a sub-agent made under this call, and
	// subLast is the last of them.
	subs    int
	subLast string
}

// tailBuf keeps the end of a stream of output: its last tailKeep bytes,
// and how much there was in all.
type tailBuf struct {
	data  []byte
	total int
}

// add takes the next piece of output.
func (t *tailBuf) add(s string) {
	t.total += len(s)
	if len(s) >= tailKeep {
		t.data = append(t.data[:0], s[len(s)-tailKeep:]...)
		return
	}
	t.data = append(t.data, s...)
	if over := len(t.data) - tailKeep; over > 0 {
		t.data = append(t.data[:0], t.data[over:]...)
	}
}

// last returns the last n lines that are not blank, made safe.
func (t *tailBuf) last(n int) []string {
	return lastLines(string(t.data), n)
}

// lastLines returns the last n lines of s that are not blank, made safe.
// Only the end of a long text is looked at.
func lastLines(s string, n int) []string {
	if len(s) > tailKeep {
		s = s[len(s)-tailKeep:]
		if i := strings.IndexByte(s, '\n'); i >= 0 {
			s = s[i+1:]
		}
	}
	var out []string
	lines := strings.Split(strings.TrimRight(s, "\n"), "\n")
	for i := len(lines) - 1; i >= 0 && len(out) < n; i-- {
		if l := screenLine(lines[i]); strings.TrimSpace(l) != "" {
			out = append(out, l)
		}
	}
	for i, j := 0, len(out)-1; i < j; i, j = i+1, j-1 {
		out[i], out[j] = out[j], out[i]
	}
	return out
}

// screenLine is a line of a command's output as a terminal would have left
// it: a carriage return inside it begins the line again, so a progress
// meter shows as its last state, man-page backspace overstrikes (X\bX and
// _\bX) collapse to the character they mark, and SGR colour codes
// (\x1b[...m) are stripped while all other control sequences remain visible.
// That the line was written over is said after it, so that nothing is
// hidden without a word. It is then made safe.
func screenLine(s string) string {
	s = strings.TrimRight(s, "\r")
	over := ""
	if i := strings.LastIndexByte(s, '\r'); i >= 0 {
		if n := len(strings.FieldsFunc(s[:i], func(r rune) bool { return r == '\r' })); n > 0 {
			over = " (written over " + count(n, "earlier state", "earlier states") + " of this line)"
		}
		s = s[i+1:]
	}
	s = stripSGR(foldOverstrikes(s))
	if len(s) > longLine {
		cut := longLine
		for cut > 0 && !utf8.RuneStart(s[cut]) {
			cut--
		}
		s = s[:cut] + fmt.Sprintf(" (a line of %s bytes, cut here)", grouped(len(s)))
	}
	return safeLine(s) + over
}

// screenLines splits command or tool output into lines and passes each
// through screenLine.
func screenLines(s string) []string {
	s = strings.TrimRight(s, "\n")
	if s == "" {
		return nil
	}
	parts := strings.Split(s, "\n")
	for i, p := range parts {
		parts[i] = screenLine(p)
	}
	return parts
}

// foldOverstrikes collapses man-page bold (X\bX), underline (_\bX or X\b_)
// and bullet (+\bo or o\b+) overstrikes into the character they format,
// leaving arbitrary backspaces untouched so they still render visibly.
func foldOverstrikes(s string) string {
	if strings.IndexByte(s, '\b') < 0 {
		return s
	}
	runes := []rune(s)
	out := make([]rune, 0, len(runes))
	for i := 0; i < len(runes); i++ {
		r := runes[i]
		if r == '\b' && len(out) > 0 && i+1 < len(runes) {
			prev, next := out[len(out)-1], runes[i+1]
			if collapsed, ok := overstrikeRune(prev, next); ok {
				out[len(out)-1] = collapsed
				i++
				continue
			}
		}
		out = append(out, r)
	}
	return string(out)
}

func overstrikeRune(prev, next rune) (rune, bool) {
	if prev <= 0x20 || prev == 0x7f || next <= 0x20 || next == 0x7f {
		return 0, false
	}
	switch {
	case prev == next:
		return next, true
	case prev == '_':
		return next, true
	case next == '_':
		return prev, true
	case (prev == '+' && next == 'o') || (prev == 'o' && next == '+'):
		return '•', true
	default:
		return 0, false
	}
}

// stripSGR removes benign CSI SGR colour/style sequences (\x1b[[0-9;:]*m)
// while keeping all other escape sequences intact so safeLine renders them
// as visible caret notation.
func stripSGR(s string) string {
	if !strings.Contains(s, "\x1b[") {
		return s
	}
	var b strings.Builder
	b.Grow(len(s))
	for i := 0; i < len(s); {
		if i+1 < len(s) && s[i] == '\x1b' && s[i+1] == '[' {
			j := i + 2
			for j < len(s) && ((s[j] >= '0' && s[j] <= '9') || s[j] == ';' || s[j] == ':') {
				j++
			}
			if j < len(s) && s[j] == 'm' {
				i = j + 1
				continue
			}
		}
		b.WriteByte(s[i])
		i++
	}
	return b.String()
}

// head is what a call does, in the parts its heading is made of.
type head struct {
	// shell says the call is a command line, drawn behind the shell mark,
	// and host the machine it runs on when that is not this one.
	shell bool
	host  string
	// name is the tool's name, when the heading shows it.
	name string
	// arg is what the call is about, on one line.
	arg string
	// body is what follows the heading line: the rest of a command of
	// several lines, the code of a Gno call. code says it is code.
	body []string
	code bool
}

// argsInOrder is a call's arguments as key: value, in the order they were
// written, at most n of them: a string quoted and cut short, a number or
// a yes or no as it is, anything else left out. Arguments still being
// written give what has arrived.
func argsInOrder(raw []byte, n int) string {
	dec := json.NewDecoder(bytes.NewReader(raw))
	if t, err := dec.Token(); err != nil || t != json.Delim('{') {
		return ""
	}
	var parts []string
	for dec.More() && len(parts) < n {
		k, err := dec.Token()
		key, ok := k.(string)
		if err != nil || !ok {
			break
		}
		var v any
		if dec.Decode(&v) != nil {
			break
		}
		switch v := v.(type) {
		case string:
			v = oneLine(v)
			if r := []rune(v); len(r) > 60 {
				v = string(r[:60]) + "…"
			}
			parts = append(parts, oneLine(key)+": "+strconv.Quote(v))
		case float64, bool:
			parts = append(parts, fmt.Sprintf("%s: %v", oneLine(key), v))
		}
	}
	return strings.Join(parts, ", ")
}

// argsOf reads a call's arguments, as far as they are a JSON object.
func argsOf(input []byte) map[string]any {
	var m map[string]any
	if json.Unmarshal(input, &m) != nil {
		return nil
	}
	return m
}

// str is the argument called key when it is a string, or "".
func str(args map[string]any, key string) string {
	v, _ := args[key].(string)
	return v
}

// metaInt reads a whole number out of a result's facts, as a tool put it
// there or as JSON gave it back.
func metaInt(meta map[string]any, key string) (int, bool) {
	switch v := meta[key].(type) {
	case int:
		return v, true
	case int64:
		return int(v), true
	case float64:
		return int(v), true
	case json.Number:
		n, err := v.Int64()
		return int(n), err == nil
	}
	return 0, false
}

// partialString reads the string argument called key out of arguments
// that are still being written: as much of it as has arrived. ok is false
// when the key has not come yet or its value is not a string.
func partialString(input []byte, key string) (value string, ok bool) {
	i := 0
	skipSpace := func() {
		for i < len(input) && (input[i] == ' ' || input[i] == '\n' || input[i] == '\t' || input[i] == '\r') {
			i++
		}
	}
	// readString reads the string that begins at i, as far as it goes.
	readString := func() (s string, whole bool) {
		var b strings.Builder
		i++ // the opening quote
		for i < len(input) {
			c := input[i]
			switch {
			case c == '"':
				i++
				return b.String(), true
			case c != '\\':
				b.WriteByte(c)
				i++
			case i+1 >= len(input):
				return b.String(), false
			default:
				esc := input[i+1]
				i += 2
				switch esc {
				case 'n':
					b.WriteByte('\n')
				case 't':
					b.WriteByte('\t')
				case 'r':
					b.WriteByte('\r')
				case 'b':
					b.WriteByte('\b')
				case 'f':
					b.WriteByte('\f')
				case 'u':
					r, ok := hex4(input, i)
					if !ok {
						return b.String(), false
					}
					i += 4
					if utf16.IsSurrogate(r) && i+6 <= len(input) && input[i] == '\\' && input[i+1] == 'u' {
						// A character outside the basic plane is written
						// as two escapes.
						if low, ok := hex4(input, i+2); ok && utf16.DecodeRune(r, low) != utf8.RuneError {
							r = utf16.DecodeRune(r, low)
							i += 6
						}
					}
					b.WriteRune(r)
				default:
					b.WriteByte(esc)
				}
			}
		}
		return b.String(), false
	}
	skipSpace()
	if i >= len(input) || input[i] != '{' {
		return "", false
	}
	i++
	for {
		skipSpace()
		if i >= len(input) || input[i] != '"' {
			return "", false
		}
		name, whole := readString()
		if !whole {
			return "", false
		}
		skipSpace()
		if i >= len(input) || input[i] != ':' {
			return "", false
		}
		i++
		skipSpace()
		if i >= len(input) {
			return "", false
		}
		if input[i] == '"' {
			s, whole := readString()
			if name == key {
				return validUTF8(s), true
			}
			if !whole {
				return "", false
			}
		} else {
			if name == key {
				return "", false
			}
			// Pass over a number, a word, an object or a list.
			depth := 0
			for i < len(input) {
				c := input[i]
				if c == '"' {
					if _, whole := readString(); !whole {
						return "", false
					}
					continue
				}
				if c == '{' || c == '[' {
					depth++
				}
				if c == '}' || c == ']' {
					if depth == 0 {
						break
					}
					depth--
				}
				if c == ',' && depth == 0 {
					break
				}
				i++
			}
		}
		skipSpace()
		if i >= len(input) || input[i] != ',' {
			return "", false
		}
		i++
	}
}

// validUTF8 returns s with every byte that is not UTF-8 replaced by
// U+FFFD, one for each byte, as the JSON decoder does it.
func validUTF8(s string) string {
	if utf8.ValidString(s) {
		return s
	}
	var b strings.Builder
	for _, r := range s {
		b.WriteRune(r)
	}
	return b.String()
}

// hex4 reads the four hex digits of a JSON escape at input[at:].
func hex4(input []byte, at int) (rune, bool) {
	if at+4 > len(input) {
		return 0, false
	}
	var r rune
	for _, c := range input[at : at+4] {
		switch {
		case c >= '0' && c <= '9':
			r = r<<4 | rune(c-'0')
		case c >= 'a' && c <= 'f':
			r = r<<4 | rune(c-'a'+10)
		case c >= 'A' && c <= 'F':
			r = r<<4 | rune(c-'A'+10)
		default:
			return 0, false
		}
	}
	return r, true
}

// headOf says what a call does. args are its arguments when they are
// complete, and nil while they are still being written; raw is then what
// has arrived of them.
func (s *session) headOf(name string, raw []byte, action *core.Action) head {
	return callHead(s.cwd, s.home, name, raw, action)
}

// callHead is headOf for a call made in the folder cwd.
func callHead(cwd, home, name string, raw []byte, action *core.Action) head {
	args := argsOf(raw)
	get := func(key string) string {
		if args != nil {
			return str(args, key)
		}
		v, _ := partialString(raw, key)
		return v
	}
	path := func(key string) string { return showPath(cwd, home, get(key)) }
	// The name is the model's word: on a server that takes any name, it
	// can hold escape sequences, and the agent view drew it as it came
	// (the TUI review of 2026-11-19).
	h := head{name: oneLine(name)}
	switch name {
	case "Bash":
		command := get("command")
		if command == "" && action != nil {
			command = action.Command
		}
		if strings.TrimSpace(command) == "" && args != nil {
			// A call that came whole with no command ("cmd", as a model
			// may write it) is named as any call is, not drawn as a prompt
			// with nothing after it (seen on the devbox's screen).
			break
		}
		h.shell = true
		lines := safeLines(command)
		if len(lines) > 0 {
			h.arg, h.body = lines[0], lines[1:]
		}
		h.name, h.host = "", oneLine(get("host"))
		if h.host == shelltool.LocalName {
			// The model may name this machine; a person needs no label
			// for a command that ran here.
			h.host = ""
		}
	case "Read":
		h.arg = path("file_path")
		off, hasOff := metaInt(args, "offset")
		lim, hasLim := metaInt(args, "limit")
		switch {
		case hasOff && hasLim:
			h.arg += fmt.Sprintf(" (lines %d to %d)", off, off+lim-1)
		case hasOff:
			h.arg += fmt.Sprintf(" (from line %d)", off)
		case hasLim:
			h.arg += fmt.Sprintf(" (first %s)", count(lim, "line", "lines"))
		}
		if p := oneLine(get("pages")); p != "" {
			h.arg += " (pages " + p + ")"
		}
	case "Write", "Edit", "MultiEdit":
		h.arg = path("file_path")
	case "NotebookEdit":
		h.arg = path("notebook_path")
		if id := oneLine(get("cell_id")); id != "" {
			h.arg += " (cell " + id + ")"
		}
	case "Glob", "Grep":
		h.arg = oneLine(get("pattern"))
		if g := get("glob"); g != "" {
			h.arg += " in files named " + oneLine(g)
		}
		if p := get("path"); p != "" {
			h.arg += " under " + showPath(cwd, home, p)
		}
	case "gnovm_run":
		if vm := get("name"); vm != "" && vm != "mem" && vm != "scratch" {
			h.arg = oneLine(vm)
		}
		h.body, h.code = safeLines(get("code")), true
	case "Agent", "Task":
		h.arg = oneLine(get("description"))
		if h.arg == "" {
			h.arg = oneLine(get("prompt"))
		}
	case "LSP":
		// The question, and where: the file, at line:character when one
		// is given.
		h.arg = oneLine(get("operation"))
		if q := get("query"); q != "" {
			h.arg += " " + oneLine(q)
		}
		if p := get("filePath"); p != "" {
			at := showPath(cwd, home, p)
			if n, ok := metaInt(args, "line"); ok && n > 0 {
				at += fmt.Sprintf(":%d", n)
				if c, ok := metaInt(args, "character"); ok && c > 0 {
					at += fmt.Sprintf(":%d", c)
				}
			}
			h.arg += " " + at
		}
	case "WebFetch":
		h.arg = oneLine(get("url"))
	case "WebSearch":
		h.arg = oneLine(get("query"))
	case "ScheduleWakeup":
		// Its result says when the next run is ("Schedule the loop's
		// next run" before it said nothing more).
	case "Skill":
		// The skill and what it was given, as Claude Code heads the call
		// (the action's summary, for the question, names its file whole).
		h.arg = oneLine(strings.TrimPrefix(strings.TrimSpace(get("skill")), "/"))
		if a := oneLine(get("args")); a != "" {
			h.arg += " " + a
		}
	case "TaskOutput", "TaskStop":
		h.arg = oneLine(get("task_id"))
	case app.DeliverToolName:
		// What is being claimed or delivered, while the call is written
		// and while it runs. When it has gone well its block is the
		// deliverable's own line (endBlock).
		h.arg = oneLine(get("label"))
		if target := oneLine(get("target")); target != "" {
			h.arg += ": " + target
			if host := oneLine(get("host")); host != "" && host != shelltool.LocalName {
				h.arg += " on " + host
			}
		}
	default:
		if server, tool, ok := app.MCPToolParts(name); ok {
			// As Claude Code names an MCP tool's call: the server and the
			// tool, then the first arguments as they were written (it read
			// "mcp__echo__echo echo of the MCP server echo").
			h.name = oneLine(server) + " - " + oneLine(tool) + " (MCP)"
			h.arg = argsInOrder(raw, 3)
			break
		}
		if action != nil && strings.TrimSpace(action.Summary) != "" {
			h.arg = oneLine(action.Summary)
			break
		}
		for _, key := range []string{"command", "file_path", "path", "pattern", "url", "query", "task_id", "name", "description", "prompt"} {
			if v := get(key); v != "" {
				h.arg = oneLine(v)
				break
			}
		}
		if h.arg == "" && args != nil && len(args) > 0 {
			h.arg = oneLine(string(raw))
			if len(h.arg) > 300 {
				h.arg = h.arg[:300]
				for !utf8.ValidString(h.arg) {
					h.arg = h.arg[:len(h.arg)-1]
				}
			}
		}
	}
	return h
}

// headLine draws the first line of a call's heading, unwrapped. failed
// swaps the tool mark for the mark of a call that did not go well.
func (s *session) headLine(h head, failed bool) string {
	th := &s.th
	mark := th.Tool.Render(th.ToolMark)
	if failed {
		mark = th.Fail.Render(th.FailMark)
	}
	if h.shell {
		line := th.Tool.Render(strings.TrimRight(th.ShellMark, " "))
		if failed {
			line = th.Fail.Render(th.FailMark) + " " + line
		}
		if h.host != "" {
			// A command on another machine leads with the machine's name.
			line = th.ToolName.Render("["+h.host+"]") + " " + line
		}
		return line + " " + th.ToolArg.Render(h.arg)
	}
	line := mark + " " + th.ToolName.Render(oneLine(h.name))
	if h.arg != "" {
		line += " " + th.ToolArg.Render(h.arg)
	}
	return line
}

// headRows draws a call's heading for the scrollback: the first line
// wrapped to the terminal, with summary after it when there is one, and
// then the body, each line set in by two cells.
func (s *session) headRows(h head, failed bool, summary string) []string {
	th := &s.th
	first := s.headLine(h, failed)
	if summary != "" {
		first += th.Quiet.Render(th.Sep + summary)
	}
	rows := wrap(s.m, first, s.cols, "", "  ")
	body := h.body
	limit, style := s.limit(commandLines), th.ToolArg
	if h.code {
		// A Gno call is headed by its code, whole.
		limit, style = detailLines, th.Code
	}
	left := 0
	if len(body) > limit {
		body, left = body[:limit], len(body)-limit
	}
	for _, l := range body {
		rows = append(rows, wrap(s.m, style.Render(l), s.cols, "  ", "    ")...)
	}
	if left > 0 {
		rows = append(rows, s.note(count(left, "more line", "more lines")+" of it not shown"))
	}
	return rows
}

// note draws a quiet line of gnosh's own under a call, set in like the
// call's body.
func (s *session) note(words string) string {
	return fit(s.m, "  "+s.th.Quiet.Render(words), s.cols)
}

// detailTip says, the first time something is shortened or folded in a
// session, which key shows all of it. Once is enough.
func (s *session) detailTip() string {
	if s.tipped {
		return ""
	}
	s.tipped = true
	if h := s.transcriptHint("", "shows all of it"); h != "" {
		return " (" + h + ")"
	}
	return ""
}

// limit is n while detail is off and detailLines while it is on (the
// verbose option).
func (s *session) limit(n int) int {
	if s.detail {
		return detailLines
	}
	return n
}

// body draws lines of outside text under a call, each set in by two cells
// and wrapped. The lines are safe already.
func (s *session) body(lines []string) []string {
	var rows []string
	for _, l := range lines {
		rows = append(rows, wrap(s.m, s.th.Output.Render(l), s.cols, "  ", "  ")...)
	}
	return rows
}

// exitNote matches the note a command's result ends with when it failed,
// which the block's last line says in its own words.
var exitNote = regexp.MustCompile(`^\[exit code -?\d+\]$`)

// noOutput is what a shell result says to the model in place of output
// when the command printed nothing.
const noOutput = "(no output)"

// liveRows draws a call for the live region: its heading on one row, and
// under it the last rows of its output or of the code being written. It
// takes at most 1+liveTail rows.
func (s *session) liveRows(c *call) []string {
	var action *core.Action
	if c.started {
		action = &c.action
	}
	h := s.headOf(c.name, c.input, action)
	first := s.headLine(h, false)
	var tail []string
	switch {
	case !c.started:
		// The model is still writing the call: show the end of what it
		// has written of a command or of code, and how much there is.
		written := count(len(c.input), "byte", "bytes") + " written"
		if n := len(h.body); n > 0 {
			tail = h.body[max(n-liveTail, 0):]
			written = count(n+1, "line", "lines") + " written"
		}
		first += s.th.Quiet.Render(s.th.Sep + written)
	default:
		tail = c.tail.last(liveTail)
		if c.subs > 0 {
			tail = append(tail[max(len(tail)-liveTail+1, 0):],
				s.th.SubMark+" "+count(c.subs, "tool call", "tool calls")+" so far, the last: "+c.subLast)
		}
	}
	rows := []string{fit(s.m, first, s.cols)}
	for _, l := range tail {
		rows = append(rows, fit(s.m, "  "+s.th.Quiet.Render(l), s.cols))
	}
	return rows
}

// endBlock draws the block a call is committed as when it ends, and says
// whether it is a compact one: a heading and nothing under it.
func (s *session) endBlock(c *call, ev *agent.ToolEndEvent) (rows []string, compact bool) {
	th := &s.th
	input := ev.Input
	if len(input) == 0 {
		input = c.input
	}
	h := s.headOf(ev.Name, input, &c.action)
	res := ev.Result
	if d, replaced, ok := app.DeliverableOf(res.Meta); ok && !ev.Denied && !ev.Interrupted && !res.IsError {
		// A deliverable was claimed or delivered: the call is shown as
		// the deliverable itself, with its address and its file whole.
		words := d.State(replaced)
		if ev.Duration >= time.Second {
			words += th.Sep + span(ev.Duration)
		}
		rows = s.deliverableRows(d, words)
		for _, line := range c.hooks {
			rows = append(rows, wrap(s.m, th.Quiet.Render(line), s.cols, "  ", "    ")...)
		}
		// It is never packed in among the calls around it: it is the
		// thing the person asked for, and stands apart.
		return rows, false
	}
	text := core.TextOf(res.Content)
	if h.shell {
		// A shell result begins with a line that tells the model which
		// machine the command ran on. A person reads the machine in the
		// heading, so the line is not shown again as output.
		_, text = shelltool.SplitStamp(text)
	}
	took := ""
	if ev.Duration >= time.Second {
		took = span(ev.Duration)
	}
	join := func(parts ...string) string {
		var kept []string
		for _, p := range parts {
			if p != "" {
				kept = append(kept, p)
			}
		}
		return strings.Join(kept, th.Sep)
	}

	var under []string // what stands under the heading
	summary := ""      // what stands after it, on its line
	failed := false
	switch {
	case ev.Denied:
		failed = true
		summary = "not run"
		under = s.clipHead(safeLines(text), s.limit(errorLines))
	case ev.Interrupted && !ev.Ran:
		summary = "stopped before it ran"
	case ev.Interrupted:
		summary = join("stopped", took)
		if h.shell {
			under = s.clipTail(outputLines(text), s.limit(tailLines))
		}
	case h.shell:
		lines := outputLines(text)
		if n := len(lines); n > 0 && exitNote.MatchString(lines[n-1]) {
			lines = lines[:n-1]
		}
		if len(lines) == 1 && strings.TrimSpace(lines[0]) == noOutput {
			// The model is told in words that nothing was printed; a
			// person sees that from the heading standing alone.
			lines = nil
		}
		code, hasCode := metaInt(res.Meta, "exitCode")
		id, _ := res.Meta["backgroundTaskId"].(string)
		if id != "" && !res.IsError {
			// Started in the background: what the model is told of it
			// (where to read, that it will be told) is the model's; the
			// person reads where its output goes (seen on the devbox's
			// screen: the model's words, then the same said again).
			lines = nil
			if file, _ := res.Meta["persistedOutputPath"].(string); file != "" {
				lines = []string{"its output goes to " + showPath(s.cwd, s.home, file)}
			}
		}
		under = s.clipTail(lines, s.limit(tailLines))
		last := ""
		switch {
		case id != "":
			last = join("running in the background as "+oneLine(id), took)
		case hasCode:
			last = join(fmt.Sprintf("exit %d", code), took)
		default:
			last = took
		}
		switch {
		case res.IsError:
			failed = true
			line := th.Fail.Render("failed")
			if last != "" {
				line += th.Quiet.Render(th.Sep + last)
			}
			under = append(under, fit(s.m, "  "+line, s.cols))
		case len(under) == 0:
			// Nothing was printed: how it ended fits beside the command.
			summary = last
		case last != "":
			under = append(under, s.note(last))
		}
	case res.IsError:
		failed = true
		summary = join("failed", took)
		if unknown, _ := res.Meta["unknownTool"].(bool); unknown {
			// "There is no tool named ..., so nothing ran.": the list of
			// the tools there are, after it, is the model's.
			if i := strings.Index(text, " so nothing ran."); i > 0 {
				text = text[:i+len(" so nothing ran.")]
			}
		}
		under = s.clipHead(screenLines(text), s.limit(errorLines))
	case res.Display != nil && res.Display.Kind == "diff":
		// An edit is shown as its diff. The diff's own first line names
		// the file and counts the lines, so it is the block's heading.
		d := *res.Display
		d.Path = showPath(s.cwd, s.home, d.Path)
		opts := diff.RenderOptions{
			Width: max(s.cols-2, 4), Profile: th.Profile, Theme: &th.Diff, Metrics: s.m,
			MaxLines: s.limit(diffLines),
		}
		lines := diff.Render(d, opts)
		if len(lines) > 0 {
			h.arg = ""
			first := s.headLine(h, false) + " " + lines[0]
			if took != "" {
				first += th.Quiet.Render(th.Sep + took)
			}
			rows = wrap(s.m, first, s.cols, "", "  ")
			for _, l := range lines[1:] {
				rows = append(rows, "  "+l)
			}
			for _, line := range c.hooks {
				rows = append(rows, wrap(s.m, th.Quiet.Render(line), s.cols, "  ", "    ")...)
			}
			return rows, len(lines) == 1 && len(c.hooks) == 0
		}
		summary = took
	default:
		if ev.Name == "gnovm_run" {
			// The receipt quotes again what the code just above says.
			text = withoutEchoes(text, str(argsOf(input), "code"))
		}
		summary, under = s.resultSummary(ev.Name, res, text)
		summary = join(summary, took)
	}

	rows = s.headRows(h, failed, summary)
	rows = append(rows, under...)
	if c.subs > 0 {
		rows = append(rows, s.note(th.SubMark+" its sub-agent made "+count(c.subs, "tool call", "tool calls")))
	}
	for _, line := range c.hooks {
		rows = append(rows, wrap(s.m, th.Quiet.Render(line), s.cols, "  ", "    ")...)
	}
	// A call that is its heading alone, on however many rows, stands
	// right under the one before it.
	return rows, len(under) == 0 && len(h.body) == 0 && c.subs == 0 && len(c.hooks) == 0
}

// resultSummary says what came of a call that went well and is neither a
// command nor an edit: a count for a search or a read, the receipt of a
// Gno call, and the first lines of anything else.
func (s *session) resultSummary(name string, res core.ToolResult, text string) (summary string, under []string) {
	meta := res.Meta
	switch name {
	case "Read":
		file, _ := meta["file"].(map[string]any)
		if kind, _ := meta["type"].(string); kind == "pdf" {
			// A PDF: which of its pages were read.
			if total, ok := metaInt(file, "numPages"); ok {
				first, _ := metaInt(file, "firstPage")
				last, _ := metaInt(file, "lastPage")
				switch {
				case first <= 1 && last >= total:
					return count(total, "page", "pages") + " of a PDF", nil
				case first >= last:
					return fmt.Sprintf("page %d of %s", first, grouped(total)), nil
				}
				return fmt.Sprintf("pages %d-%d of %s", first, last, grouped(total)), nil
			}
		}
		n, ok := metaInt(file, "numLines")
		if !ok {
			return count(strings.Count(strings.TrimRight(text, "\n"), "\n")+1, "line", "lines"), nil
		}
		summary = count(n, "line", "lines")
		if total, ok := metaInt(file, "totalLines"); ok && total > n {
			summary += " of " + grouped(total)
		}
		return summary, nil
	case "Glob":
		if n, ok := metaInt(meta, "numFiles"); ok {
			summary = count(n, "file", "files")
			if total, ok := metaInt(meta, "totalMatches"); ok && total > n {
				summary += " of " + grouped(total)
			}
			return summary, nil
		}
	case "Grep":
		files, hasFiles := metaInt(meta, "numFiles")
		if n, ok := metaInt(meta, "numMatches"); ok && hasFiles {
			return count(n, "match", "matches") + " in " + count(files, "file", "files"), nil
		}
		if n, ok := metaInt(meta, "numLines"); ok {
			summary = count(n, "line", "lines")
			if total, ok := metaInt(meta, "totalLines"); ok && total > n {
				summary += " of " + grouped(total)
			}
			return summary, nil
		}
		if hasFiles {
			return count(files, "file", "files"), nil
		}
	case "Write":
		kind, _ := meta["type"].(string)
		if unchanged, _ := meta["unchanged"].(bool); unchanged {
			return "already had this content", nil
		}
		if n, ok := metaInt(meta, "lines"); ok && kind != "" {
			return kind + "d, " + count(n, "line", "lines"), nil
		}
	case "gnovm_run":
		return "", s.clipHead(safeLines(text), s.limit(receiptLines))
	case "Monitor":
		// A Monitor started: its id and where its output goes, in the
		// person's words (the result's are the model's: "comes to you as
		// notices ... end your reply to wait").
		if id, _ := meta["taskId"].(string); id != "" {
			summary = "watching as " + oneLine(id)
			if file, _ := meta["persistedOutputPath"].(string); file != "" {
				under = s.body([]string{"its output goes to " + showPath(s.cwd, s.home, file)})
			}
			return summary, under
		}
	case "EnterPlanMode":
		if on, _ := meta["planMode"].(bool); on {
			return "plan mode on", nil
		}
	case "PushNotification":
		// The result's words are the model's ("the user ... reads what
		// you write").
		if why, _ := meta["notSent"].(string); why != "" {
			return "not sent: " + oneLine(why), nil
		}
		if strings.HasPrefix(text, "Notification sent") {
			return "sent", nil
		}
	case "AskUserQuestion":
		// The answers stand in the question's own record just above: the
		// result's words for the model said them again, cut short.
		return "answered", nil
	case "Agent", "Task", "SendMessage":
		// A sub-agent started, or given more to do, in the background:
		// its id, in the person's words (the result's are the model's:
		// "do not report what it found before that notice arrives").
		if status, _ := meta["status"].(string); status == "async_launched" {
			summary = "running in the background"
			if id, _ := meta["agentId"].(string); id != "" {
				summary += " as " + oneLine(id)
			}
			return summary, nil
		}
		// A report ends with the sub-agent's id for SendMessage, a line for
		// the model (seen on the devbox's screen under the report).
		if id, _ := meta["agentId"].(string); id != "" {
			text = strings.TrimRight(strings.TrimSuffix(strings.TrimRight(text, "\n"), app.AgentIDLine(id)), "\n")
		}
	case "ScheduleWakeup":
		// When the loop runs next (the result's "This turn ends here" is
		// the model's); the model's reason is the call's note.
		if when, _ := meta["nextRun"].(string); when != "" {
			summary = "next run at " + oneLine(when)
			if wait, _ := meta["wait"].(string); wait != "" {
				summary += ", in " + oneLine(wait)
			}
			return summary, nil
		}
	case "Skill":
		// The result is the skill's text under a lead written for the
		// model ("Carry them out"), seen on the devbox's screen tags and
		// all. A forked skill's result is its sub-agent's report, and is
		// shown as any other.
		switch loaded, _ := meta["loaded"].(string); loaded {
		case "now":
			return "loaded", nil
		case "again":
			return "loaded already", nil
		}
	case "WebSearch":
		// The results by title and site, as a person scans them: the
		// result lists them for the model, with the search server's
		// address and how to read on.
		if hits := s.searchHits(meta["results"]); len(hits) > 0 {
			return count(len(hits), "result", "results"), s.clipHead(hits, s.limit(resultLines))
		}
	case "WebFetch":
		// What came back, as Claude Code says it ("Received 1.2KB (200
		// OK)"): the result is the smaller model's answer about the
		// page, for the model, which the reply puts in its own words.
		if to, _ := meta["redirect"].(string); to != "" {
			return "redirects to " + oneLine(to) + ", on another host: not followed", nil
		}
		n, hasBytes := metaInt(meta, "bytes")
		code, hasCode := metaInt(meta, "code")
		if !hasBytes || !hasCode {
			break
		}
		got := app.ByteCount(int64(n))
		if kind, _ := meta["contentType"].(string); kind != "" {
			got = oneLine(kind) + ", " + got
		}
		if file, _ := meta["savedTo"].(string); file != "" {
			return "received " + got + " · saved to " + showPath(s.cwd, s.home, file), nil
		}
		status := fmt.Sprint(code)
		if words := oneLine(str(meta, "codeText")); words != "" {
			status += " " + words
		}
		return "received " + got + " (" + status + ")", nil
	case "ExitPlanMode":
		// The plan approved, in the person's words (the result is the
		// model's: "carry the plan out").
		if approved, _ := meta["approved"].(bool); approved {
			mode, _ := meta["mode"].(string)
			name, ok := modeNames[mode]
			if !ok {
				name = oneLine(mode) + " mode"
			}
			summary = "approved · " + name
			if file, _ := meta["planFile"].(string); file != "" {
				under = s.body([]string{"the plan is kept in " + showPath(s.cwd, s.home, file)})
			}
			return summary, under
		}
	}
	lines := nonBlank(safeLines(text))
	switch len(lines) {
	case 0:
		return "done", nil
	case 1:
		if s.m.Width(lines[0]) <= 60 {
			return lines[0], nil
		}
	}
	return "", s.clipHead(lines, s.limit(resultLines))
}

// searchHits is a WebSearch's results as a person reads them, a title and
// its site on each line, from the call's meta: the list as the tool made
// it, or as it reads back from a session's file.
func (s *session) searchHits(v any) []string {
	var hits []string
	add := func(title, address string) {
		line := oneLine(title)
		site := address
		if u, err := url.Parse(address); err == nil && u.Hostname() != "" {
			site = u.Hostname()
		}
		switch site = oneLine(site); {
		case line == "" || title == address:
			line = oneLine(address)
		case site != "":
			line += s.th.Sep + site
		}
		if line != "" {
			hits = append(hits, line)
		}
	}
	switch list := v.(type) {
	case []map[string]string:
		for _, r := range list {
			add(r["title"], r["url"])
		}
	case []any:
		for _, item := range list {
			if r, ok := item.(map[string]any); ok {
				title, _ := r["title"].(string)
				address, _ := r["url"].(string)
				add(title, address)
			}
		}
	}
	return hits
}

// nonBlank drops the blank lines of a result: its first lines under a
// call are a few rows, none to spare for an empty one. The whole is still
// shown, blank lines and all, when more is asked for.
func nonBlank(lines []string) []string {
	var out []string
	for _, l := range lines {
		if strings.TrimSpace(l) != "" {
			out = append(out, l)
		}
	}
	return out
}

// outputLines splits a command's output into lines as a terminal would
// have left them, made safe. Blank lines at its end are dropped.
func outputLines(s string) []string {
	s = strings.TrimRight(s, "\n")
	if s == "" {
		return nil
	}
	if len(s) > outputKeep {
		// A text this long is looked at from its end only.
		s = s[len(s)-outputKeep:]
		if i := strings.IndexByte(s, '\n'); i >= 0 {
			s = s[i+1:]
		}
	}
	lines := strings.Split(s, "\n")
	for i, l := range lines {
		lines[i] = screenLine(l)
	}
	return lines
}

// clipTail draws the last n of lines under a call, with a line before
// them that says how many were left out.
func (s *session) clipTail(lines []string, n int) []string {
	var rows []string
	if left := len(lines) - n; left > 0 {
		rows = append(rows, s.note(count(left, "earlier line", "earlier lines")+" not shown"+s.detailTip()))
		lines = lines[left:]
	}
	return append(rows, s.body(lines)...)
}

// clipHead draws the first n of lines under a call, with a line after
// them that says how many were left out.
func (s *session) clipHead(lines []string, n int) []string {
	left := len(lines) - n
	if left > 0 {
		lines = lines[:n]
	}
	rows := s.body(lines)
	if left > 0 {
		rows = append(rows, s.note(count(left, "more line", "more lines")+" not shown"+s.detailTip()))
	}
	return rows
}
