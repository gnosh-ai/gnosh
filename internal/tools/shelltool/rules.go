package shelltool

import (
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"path"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/gnosh-ai/gnosh/internal/core"
	"github.com/gnosh-ai/gnosh/internal/shellquote"
)

// The few rules about commands that are the shell tool's own, and the notes
// it puts under a failure. They are not permission rules: a permission rule
// decides whether a person is asked, and these say that a command is not
// the way to do a thing, whatever the person allows. A full path or a
// script gets past every one of them, and that is fine: each is there to
// tell a model, at the moment it matters, what to do instead.
//
// They read a command only as far as it takes to see what it begins with:
// its first line that is not blank or a comment, cut into the commands a
// shell would run one after another. Later lines are left alone, so that a
// file written with a here-document may say anything.

// piece is one command of a line, with the operator that ended it: ";",
// "&&", "||", "|", "&", "(", ")", a backquote, or "" at the end of the
// line.
type piece struct {
	text string
	sep  string
}

// firstCommandLine returns the first line of a command that is neither
// blank nor a comment, and what follows it.
func firstCommandLine(command string) (line, rest string) {
	for command != "" {
		line, rest, _ = strings.Cut(command, "\n")
		if t := strings.TrimSpace(line); t != "" && !strings.HasPrefix(t, "#") {
			return line, rest
		}
		command = rest
	}
	return "", ""
}

// pieces cuts one line into its commands, at the operators that stand
// outside quotes.
func pieces(line string) []piece {
	var out []piece
	start := 0
	var quote byte
	cut := func(end int, sep string, next int) {
		out = append(out, piece{text: line[start:end], sep: sep})
		start = next
	}
	for i := 0; i < len(line); i++ {
		c := line[i]
		switch {
		case quote == '\'':
			if c == '\'' {
				quote = 0
			}
		case c == '\\':
			i++
		case quote == '"':
			if c == '"' {
				quote = 0
			}
		case c == '\'' || c == '"':
			quote = c
		case c == '&' && (i > 0 && (line[i-1] == '>' || line[i-1] == '<') || i+1 < len(line) && line[i+1] == '>'):
			// Part of a redirection (2>&1, &>file), not the end of a
			// command.
		case c == ';' || c == '&' || c == '|':
			sep, width := string(c), 1
			if i+1 < len(line) {
				switch two := line[i : i+2]; two {
				case "&&", "||":
					sep, width = two, 2
				case ";;", "|&":
					width = 2
				}
			}
			cut(i, sep, i+width)
			i += width - 1
		case c == '(' || c == ')' || c == '`':
			cut(i, string(c), i+1)
		}
	}
	return append(out, piece{text: line[start:]})
}

// Words that stand in front of a command without being it: the shell's own
// (keywords), and the programs that run the command they are given
// (wrappers).
var (
	keywords = []string{"do", "then", "else", "elif", "if", "while", "until", "!", "{", "time"}
	wrappers = []string{"env", "command", "exec", "nohup", "sudo", "nice", "timeout"}
)

// programOf returns the program a command runs, as it was written but
// without its quotes: the first word that is not a variable setting, a
// keyword of the shell, or a wrapper with its options. It returns "" when
// there is none, and for a command that only looks a program up
// (command -v).
func programOf(text string) string {
	words := shellWords(text)
	for len(words) > 0 {
		w := words[0]
		switch {
		case isAssignment(w):
			words = words[1:]
		case !w.quoted && slices.Contains(keywords, w.text):
			words = words[1:]
		case slices.Contains(wrappers, path.Base(w.text)):
			words = words[1:]
			if path.Base(w.text) == "command" && len(words) > 0 && (words[0].text == "-v" || words[0].text == "-V") {
				return ""
			}
			// Its own options, the variables env sets, and a number such
			// as the time timeout is given.
			for len(words) > 0 && (strings.HasPrefix(words[0].text, "-") || isAssignment(words[0]) || isAmount(words[0].text)) {
				words = words[1:]
			}
		default:
			return w.text
		}
	}
	return ""
}

// isAmount reports whether a word is a number, with or without one letter
// after it: 5, 2.5, 10s.
func isAmount(s string) bool {
	_, ok := amount(s)
	return ok
}

// amount reads a length of time as sleep and timeout take it: a number,
// and after it s, m, h or d, or nothing, which means seconds.
func amount(s string) (time.Duration, bool) {
	unit := time.Second
	if s != "" {
		switch s[len(s)-1] {
		case 's':
			s = s[:len(s)-1]
		case 'm':
			unit, s = time.Minute, s[:len(s)-1]
		case 'h':
			unit, s = time.Hour, s[:len(s)-1]
		case 'd':
			unit, s = 24*time.Hour, s[:len(s)-1]
		}
	}
	n, err := strconv.ParseFloat(s, 64)
	if err != nil || n < 0 || n != n {
		return 0, false
	}
	// A year is more than any limit here, and keeps the sum from
	// overflowing.
	const year = 365 * 24 * time.Hour
	if n*float64(unit) >= float64(year) {
		return year, true
	}
	return time.Duration(n * float64(unit)), true
}

// reaches returns "ssh" or "scp" when a command begins with a line that
// runs one of them, in any of its commands, and "" otherwise.
func reaches(command string) string {
	// Every line of it, not only the first (box 1, 16 November: ssh on the
	// second line, under an assignment and a comment, ran); the text of a
	// here-document is no command.
	for _, line := range commandLines(command) {
		for _, p := range pieces(line) {
			if p.sep == "(" {
				// What stands before a parenthesis is a function's name, a
				// dollar sign or a pattern, not a command.
				continue
			}
			if prog := path.Base(programOf(p.text)); prog == "ssh" || prog == "scp" {
				return prog
			}
		}
	}
	return ""
}

// gnokeyPassword reports whether a command hands gnokey a password on its
// standard input: gnokey's -insecure-password-stdin, in either spelling and
// wherever it stands (behind a pipe, in quotes, split by them), which is
// the one way gnokey takes a password without a terminal. Only gnosh's
// gnokey tool uses it, for a password the user typed at its masked prompt
// (spec/gnosh/harness-tools.md, TOOL-41).
// GnokeyPassword reports whether a command gives gnokey a password on its
// standard input (-insecure-password-stdin), which no command here may
// do: for the person's own commands, which are kept in the session's file
// and shown to the model, it is caught before either.
func GnokeyPassword(command string) bool { return gnokeyPassword(command) }

func gnokeyPassword(command string) bool {
	plain := strings.NewReplacer(`'`, "", `"`, "", `\`, "").Replace(command)
	return strings.Contains(plain, "insecure-password-stdin")
}

// commandLines are the lines of a command where the shell starts reading
// a command (shellquote.CommandStarts: not the text of a here-document or
// a line inside quotes), each with the lines it goes on into by a
// backslash at its end, and that are not blank or comments.
func commandLines(command string) []string {
	var out []string
	lines := strings.Split(command, "\n")
	starts := shellquote.CommandStarts(command)
	for i := 0; i < len(lines); i++ {
		if !starts[i] {
			continue
		}
		line := lines[i]
		for i+1 < len(lines) && !starts[i+1] && continues(line) {
			i++
			line = line[:len(line)-1] + lines[i]
		}
		if t := strings.TrimSpace(line); t == "" || strings.HasPrefix(t, "#") {
			continue
		}
		out = append(out, line)
	}
	return out
}

// continues reports whether a line ends with a backslash that is not
// itself escaped: the shell reads on into the next line.
func continues(line string) bool {
	return (len(line)-len(strings.TrimRight(line, `\`)))%2 == 1
}

// leadingSleep returns how long a command only sleeps before it does
// anything else: the sleeps it begins with, one after another, added up.
func leadingSleep(command string) time.Duration {
	var total time.Duration
	for {
		line, rest := firstCommandLine(command)
		if line == "" {
			return total
		}
		for _, p := range pieces(line) {
			words := shellWords(p.text)
			if len(words) == 0 && (p.sep == ";" || p.sep == "&&" || p.sep == "") {
				continue
			}
			if len(words) < 2 || words[0].text != "sleep" || p.sep == "&" {
				return total
			}
			times := 0
			for _, w := range words[1:] {
				d, ok := amount(w.text)
				if !ok {
					// A redirection, or something only the shell knows.
					break
				}
				total += d
				times++
			}
			if times == 0 || (p.sep != ";" && p.sep != "&&" && p.sep != "") {
				return total
			}
		}
		command = rest
	}
}

// bashArgNames are the arguments Bash takes, in the order a message lists
// them.
var bashArgNames = []string{argCommand, argDescription, argTimeout, argBackground, argHost}

// unknownArg refuses an argument Bash does not take. A made-up argument
// that was passed over in silence would leave the model believing the
// command ran the way it asked: in another directory, say.
func unknownArg(fields map[string]json.RawMessage) error {
	for _, name := range slices.Sorted(maps.Keys(fields)) {
		if slices.Contains(bashArgNames, name) || name == argNoSandbox {
			continue
		}
		text := fmt.Sprintf("%s is not an argument of %s, so the command was not run. %s takes: %s", strconv.Quote(name), BashName, BashName, strings.Join(bashArgNames, ", "))
		if slices.Contains([]string{"working_dir", "working_directory", "workdir", "cwd", "dir", "directory", "path"}, strings.ToLower(name)) {
			text += ". There is no argument for a directory: a command starts in the session's working directory (on a host, in the account's home directory there), and a cd in the command goes elsewhere"
		}
		return fmt.Errorf("%s", text)
	}
	return nil
}

// rules refuses a call that is not the way to do what it is after. person
// says the command is the person's own, typed into the session: the rules
// that steer a model are not for them. An error is worded for the model.
func (m *Manager) rules(a bashArgs, env *core.ToolEnv, person bool) error {
	if h := a.host; h != nil {
		if a.background {
			return fmt.Errorf("%s cannot be used with %s: a command on another machine runs only for as long as this call holds the connection to it, and gnosh cannot stop it there afterwards. For something long on %s, start it there so that it outlives the connection, with its output in a file (nohup COMMAND > FILE 2>&1 < /dev/null &), and read the file with later commands",
				argBackground, argHost, h.Name)
		}
		if n := len(remoteScript(a.command)); n > maxRemoteCommand {
			return fmt.Errorf("the command is %s long, and a command for another machine goes to ssh as one argument, of at most %s. Send it in parts: a long file can be written on %s by several commands that each add to it (cat >> FILE <<'EOF' ... EOF)",
				byteSize(int64(len(a.command))), byteSize(maxRemoteCommand), h.Name)
		}
	}
	if gnokeyPassword(a.command) {
		if person {
			return errors.New("the command gives gnokey a password on its standard input, and a command typed here is kept in the session's file with the password in it. Sign in a terminal of your own")
		}
		return errors.New("the command gives gnokey a password on its standard input (-insecure-password-stdin), which no command here may do: a key's password is typed by the user, at a masked prompt that only the gnokey tool opens. Call that tool with the gnokey command instead, without the flag; where there is no such tool (nobody is at the terminal), give the user the exact command to run in their own terminal, and stop. Never ask for a password, and do not look for another way to give one")
	}
	if person {
		return nil
	}
	if hosts, _ := m.hostSet(); len(hosts) > 0 {
		switch prog := reaches(a.command); prog {
		case "ssh":
			// (Box 1, 16 November: told this, Qwen built the name as
			// S=s; S="${S}sh" and decoded it from base64. Such a line is
			// held for a person by the permission rules, and nobody was
			// there.)
			return fmt.Errorf("the command runs ssh, and in this session another machine is reached by naming it as %s in the call, never with ssh in a command: that is what lets every result say which machine it came from. The declared hosts are: %s. Give the command for the other machine alone, with %s set to the machine's name. For a machine that is not declared, %s. Do not spell ssh another way (put together in a variable, or decoded): a program whose name is not written out is held for the user to approve, and the turn waits on it",
				argHost, m.hostList(), argHost, declareHost)
		case "scp":
			return fmt.Errorf("the command runs scp, and in this session another machine is reached by naming it as %s in the call, never with scp or ssh in a command: that is what lets every result say which machine it came from. The declared hosts are: %s. To move a file, read it on one machine with a command and write it on the other (the file tools here; cat with a here-document there), or tell the user what needs copying",
				argHost, m.hostList())
		}
	}
	if a.host == nil {
		if note := m.misreadBefore(a.command); note != "" {
			return errors.New(note)
		}
	}
	main := env == nil || env.ParentSessionID == ""
	if limit := m.opt.LongSleep; limit > 0 && a.host == nil && !a.background && main {
		if d := leadingSleep(a.command); d >= limit {
			return fmt.Errorf("the command begins by sleeping for %s, and a foreground command that only waits holds up the whole turn. To wait for something to happen, run a loop that checks for it with %s set to true: you are told when it ends. To wait for a command, run that command with %s. Several shorter sleeps one after another are not the way either",
				seconds(d), argBackground, argBackground)
		}
	}
	return nil
}

// misreadBefore says, before a command runs, what was meant by one that
// cannot run as written: one that runs a session tool's name, or code for a
// tool (Options.Misread), as a program, where no program of that name is
// installed. Without it the person is asked about a command that only
// fails, and the model learns why after it has (box 1 and box 2, 16
// November: one command of Qwen's in twenty was mem.Show() or gnovm_run
// typed into the shell). Only the first line is read, as the other rules
// read it: a later one may be inside a quoted text that runs on (a commit
// message about mem.Show(), say).
func (m *Manager) misreadBefore(command string) string {
	line, _ := firstCommandLine(command)
	for _, p := range pieces(line) {
		prog := programOf(p.text)
		if prog == "" || strings.ContainsAny(prog, "/$`") {
			continue
		}
		if _, ok := m.onPath(prog); ok {
			continue
		}
		if slices.Contains(m.opt.ToolNames, prog) {
			return toolNotProgram(prog)
		}
		if m.opt.Misread != nil {
			if note := m.opt.Misread(command, []string{prog}); note != "" {
				return note
			}
		}
	}
	return ""
}

// toolNotProgram is the note for a tool's name run as a program.
func toolNotProgram(name string) string {
	return name + " is one of your tools, not a program: call it as a tool, with its arguments, instead of typing it into a command"
}

// lookedUp returns the program a command looked for, when looking a
// program up is all the command does: which NAME, command -v NAME.
func lookedUp(command string) string {
	line, rest := firstCommandLine(command)
	if strings.TrimSpace(rest) != "" {
		return ""
	}
	ps := pieces(line)
	if len(ps) != 1 {
		return ""
	}
	words := shellWords(ps[0].text)
	switch {
	case len(words) == 2 && words[0].text == "which":
		return words[1].text
	case len(words) == 3 && words[0].text == "command" && (words[1].text == "-v" || words[1].text == "-V"):
		return words[2].text
	}
	return ""
}

// onlyChangesDirectory reports whether a command is a cd and nothing else.
func onlyChangesDirectory(command string) bool {
	line, rest := firstCommandLine(command)
	if strings.TrimSpace(rest) != "" {
		return false
	}
	ps := pieces(line)
	if len(ps) != 1 {
		return false
	}
	words := shellWords(ps[0].text)
	return len(words) > 0 && words[0].text == "cd"
}

// hints are the notes that say what a failure means and what to do about
// it, at the moment a model reads the failure: a program that is not
// installed, a lookup that found nothing, a tool typed as a command, a
// directory changed on a machine where nothing carries over. e is how the
// command ended, body what it printed, and own says the exit code is the
// command's own.
func (m *Manager) hints(a bashArgs, e *ending, body string, own bool) []string {
	if e.reason != endExited || !own {
		return nil
	}
	where := "on this machine"
	if a.host != nil {
		where = "on " + a.host.Name
	}
	const instead = "Do it another way, or ask the user where the program is; do not search the disk for it"
	if e.code != 0 && a.host == nil && m.opt.Misread != nil {
		line, _ := firstCommandLine(a.command)
		var programs []string
		for _, p := range pieces(line) {
			programs = append(programs, programOf(p.text))
		}
		if note := m.opt.Misread(a.command, programs); note != "" {
			return []string{note}
		}
	}
	var notes []string
	switch name := lookedUp(a.command); {
	case name != "" && e.code != 0 && strings.TrimSpace(body) == "":
		notes = append(notes, "nothing was found: "+oneLine(name, 80)+" is not installed "+where+", or is not on the PATH there. "+instead)
	case e.code == 127:
		line, _ := firstCommandLine(a.command)
		first := ""
		if ps := pieces(line); len(ps) > 0 {
			first = programOf(ps[0].text)
		}
		if a.host == nil && first != "" && slices.Contains(m.opt.ToolNames, first) {
			notes = append(notes, toolNotProgram(first))
		} else {
			notes = append(notes, "exit code 127 means a program the command names is not installed "+where+". "+instead)
		}
	case e.code == 0 && a.host != nil && onlyChangesDirectory(a.command):
		notes = append(notes, "a change of directory does not carry over on a host: the next command on "+a.host.Name+" starts in the account's home directory again. Put the cd and the work in one command")
	case e.code != 0 && strings.Contains(a.command, "gnokey") && strings.Contains(body, "inappropriate ioctl for device"):
		// (gnokey asked for a password at a terminal: the old fork's
		// Qwen, told only that it could not sign, spent its whole window
		// in the shell looking for another way, TOOL-41.)
		notes = append(notes, "gnokey asked for the key's password, and a command here has no terminal to type it in, nor may it be given one: signing goes through the gnokey tool, which asks the user for the password at a masked prompt. If you have that tool, call it with this gnokey command; if not, give the user the exact command to run in their own terminal, and stop. Do not look for another way")
	}
	return notes
}
