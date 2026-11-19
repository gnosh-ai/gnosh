package shelltool

import (
	"bytes"
	"context"
	"fmt"
	"math"
	"os"
	"os/user"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/gnosh-ai/gnosh/internal/core"
	"github.com/gnosh-ai/gnosh/internal/shellquote"
)

// Commands on other machines.
//
// Where a command runs is a fact of the call, not something written into
// the command. A call that names a declared host is run there by the
// system's ssh, and every result, a local one too, begins with a line that
// says which machine it came from and in which directory the command
// started (stamp). This file holds what is particular to the other machine:
// how ssh is found and what it is given, the one line gnosh puts in front
// of the command there, how what ssh itself says is kept apart from what
// the command prints, and how a connection that failed is told from a
// command that failed.

// hostMarker opens the line the other machine sends back, on ssh's standard
// error, when the command starts there: the marker, and then the directory.
const hostMarker = "gnosh-host-cwd:"

// remoteLead returns what stands in front of the command on the other
// machine.
// The account's login shell there is handed one line: this, and then the
// command inside single quotes as the argument of eval. So the command is
// read by that shell exactly as it was written, several lines,
// here-documents and all, and the shell's own messages count the command's
// lines, as they do on this machine.
//
// In order: the shell reports the directory it is in on standard error,
// which is how gnosh knows that the command started and where; from then on
// standard error goes where standard output goes, so that what the command
// prints arrives as one stream in the order written, and whatever comes
// down ssh's standard error after the report is ssh's own word; and the
// command is given the same few variables as a command here (fixedEnv), so
// that a script there can also tell an AI agent is running it. In zsh, the
// login shell of a Mac, a word that begins with "=" stands for a command's
// path, so "[ a == b ]" and "echo ====" failed with "= not found"; that is
// turned off first, as Claude Code 2.1.328 does, and the other shells pass
// over it.
//
// It needs a login shell that reads sh: sh, bash, dash, ksh or zsh.
func remoteLead() string {
	return `[ -n "${ZSH_VERSION-}" ] && setopt no_equals; printf '` + hostMarker + `%s\n' "$PWD" >&2; exec 2>&1; export ` + strings.Join(fixedEnv(), " ") + `; eval `
}

// remoteScript is the one argument ssh is given after the destination: what
// the login shell of the other machine runs.
func remoteScript(command string) string {
	return remoteLead() + shellquote.Quote(command)
}

// sshArgs is what ssh is run with for a command on h.
//
//	-T                               no terminal on the other machine, whatever the person's ssh
//	                                 configuration asks for: output arrives as the command wrote
//	                                 it, and nothing there can stop to ask a question on one
//	-o BatchMode=yes                 ssh never asks: not for a password, not for a key's
//	                                 passphrase, not whether to go on. What would ask fails
//	-o StrictHostKeyChecking=yes     a machine whose key this account's ssh does not already
//	                                 know, or whose key has changed, is not connected to. Which
//	                                 keys are known stays ssh's own business; that an unknown one
//	                                 is refused is gnosh's rule
//	-o ConnectTimeout=20             reaching the machine and logging in may take this long
//	--                               nothing after it is read as an option, whatever it looks like
//	destination                      as the person declared it
//	the command                      one argument, read only by the shell of the other machine
//
// Keys, agents, ports, jump hosts and shared connections are left to the
// person's ssh configuration.
func (m *Manager) sshArgs(h *Host, command string) []string {
	return []string{
		"-T",
		"-o", "BatchMode=yes",
		"-o", "StrictHostKeyChecking=yes",
		"-o", "ConnectTimeout=" + strconv.Itoa(max(int(math.Ceil(m.opt.ConnectTimeout.Seconds())), 1)),
		"--",
		h.Target,
		remoteScript(command),
	}
}

// sshProgram finds the ssh program a command on h is run with. A name is
// looked up on the PATH the command itself would be given, in absolute
// directories only: a program lying in the working directory is never
// taken for ssh. A refusal says that it is not installed.
func (m *Manager) sshProgram(h *Host) (string, error) {
	name := m.opt.SSH
	gone := refusal(fmt.Sprintf("The command was not run: it is for %s, and the program that reaches another machine, %s, is not installed on this machine. Tell the user; do not try to reach the machine another way.", h.Name, name))
	usable := func(path string) bool {
		st, err := os.Stat(path)
		return err == nil && st.Mode().IsRegular() && st.Mode()&0o111 != 0
	}
	if strings.ContainsRune(name, filepath.Separator) {
		if !filepath.IsAbs(name) || !usable(name) {
			return "", gone
		}
		return name, nil
	}
	if path, ok := m.onPath(name); ok {
		return path, nil
	}
	return "", gone
}

// onPath finds a program in the directories of the PATH commands are given,
// the absolute ones only.
func (m *Manager) onPath(name string) (string, bool) {
	usable := func(path string) bool {
		st, err := os.Stat(path)
		return err == nil && st.Mode().IsRegular() && st.Mode()&0o111 != 0
	}
	for _, kv := range commandEnv(m.opt.Environ(), "", m.opt.Env) {
		dirs, ok := strings.CutPrefix(kv, "PATH=")
		if !ok {
			continue
		}
		for _, dir := range filepath.SplitList(dirs) {
			if !filepath.IsAbs(dir) {
				continue
			}
			if path := filepath.Join(dir, name); usable(path) {
				return path, true
			}
		}
	}
	return "", false
}

// usableTarget reports whether a destination can be handed to ssh: it is
// not empty, does not begin with a dash, and holds no blank and nothing a
// reader could not see. The config checks more; this is the last look.
func usableTarget(target string) bool {
	if target == "" || target[0] == '-' || !utf8.ValidString(target) {
		return false
	}
	for _, r := range target {
		if unicode.IsSpace(r) || unicode.IsControl(r) || unicode.Is(unicode.Cf, r) {
			return false
		}
	}
	return true
}

// declareHost tells a model how another machine comes to be one it may
// use: the person declares it, in a file the session watches (box 1, 16
// November: Qwen tried a machine it had just made ten times, as a host and
// with ssh).
const declareHost = `ask the user to declare it as a host in gnosh's config: "hosts": {"NAME": {"ssh": "user@address"}} in gnosh.json, which a running session takes up at once`

// findHost settles which machine a call's host argument names: nil for
// this one. An error is worded for the model.
func (m *Manager) findHost(name string) (*Host, error) {
	name = strings.TrimSpace(name)
	if name == "" || name == LocalName {
		return nil, nil
	}
	hosts, _ := m.hostSet()
	h, ok := hosts[name]
	switch {
	case !ok && len(hosts) == 0:
		return nil, fmt.Errorf("the %s %q was given, and no other machine is declared for this session, so the command was not run. Leave %s out to run it on this machine. To use another machine, %s; do not reach it another way, such as with ssh in a command",
			argHost, name, argHost, declareHost)
	case !ok:
		return nil, fmt.Errorf("the %s %q is not a machine this session may run commands on, so the command was not run. The declared hosts are: %s. Leave %s out to run the command on this machine. For a machine that is not declared, %s; do not reach it another way",
			argHost, name, m.hostList(), argHost, declareHost)
	case !usableTarget(h.Target):
		return nil, fmt.Errorf("the host %s is declared with a destination that cannot be handed to ssh, so the command was not run. Tell the user: in gnosh's config the host needs an ssh destination of the form user@address", h.Name)
	}
	return h, nil
}

// hostList names the declared hosts for a message: "render
// (root@203.0.113.10), web (deploy@203.0.113.11)".
func (m *Manager) hostList() string {
	hosts, names := m.hostSet()
	parts := make([]string, 0, len(names))
	for _, name := range names {
		parts = append(parts, name+" ("+hosts[name].Target+")")
	}
	return strings.Join(parts, ", ")
}

// SplitStamp parts the text of a shell result into the line it begins with,
// which says where the command ran, and the rest. A text that begins with
// no such line comes back whole, as the rest.
func SplitStamp(text string) (stamp, rest string) {
	if !strings.HasPrefix(text, "<host>") {
		return "", text
	}
	stamp, rest, _ = strings.Cut(text, "\n")
	return stamp, rest
}

// whoAndWhere says who the program runs as and on which machine, as
// user@name, with whatever of the two the system can give.
func whoAndWhere() string {
	name := ""
	if u, err := user.Current(); err == nil {
		name = u.Username
	}
	machine, _ := os.Hostname()
	switch {
	case name != "" && machine != "":
		return name + "@" + machine
	case machine != "":
		return machine
	}
	return name
}

// stamp is the line a shell result begins with: the machine the command
// was for, and the directory it started in there. A result from another
// machine can then never be taken for a local one, and a path in the
// output is read beside the machine it belongs to.
//
//	<host>local (dev@box)</host><cwd>/home/dev/project</cwd>
//	<host>render (root@203.0.113.10)</host><cwd>/root</cwd>
//
// h is nil for this machine. dir is empty when the directory is not known:
// the other machine never said, because the command never started there.
func (m *Manager) stamp(h *Host, dir string) string {
	name, machine := LocalName, m.local
	if h != nil {
		name, machine = h.Name, h.Target
	}
	var b strings.Builder
	b.WriteString("<host>" + stampText(name))
	if machine != "" {
		b.WriteString(" (" + stampText(machine) + ")")
	}
	b.WriteString("</host>")
	if dir != "" {
		b.WriteString("<cwd>" + stampText(dir) + "</cwd>")
	}
	return b.String()
}

// stampText makes a value fit to stand in a stamp: whatever could end the
// line or one of its tags, or cannot be seen, is written as an escape. A
// directory's name comes from the machine the command ran on, and nothing
// in it may read as a second stamp.
func stampText(s string) string {
	plain := func(r rune) bool {
		return r != '<' && r != '>' && r != utf8.RuneError && !unicode.IsControl(r) && !unicode.Is(unicode.Cf, r) && r != '\u2028' && r != '\u2029'
	}
	if strings.IndexFunc(s, func(r rune) bool { return !plain(r) }) < 0 {
		return s
	}
	var b strings.Builder
	for _, r := range s {
		switch {
		case plain(r):
			b.WriteRune(r)
		case r == utf8.RuneError:
			b.WriteString(`\ufffd`)
		case r < 0x100:
			fmt.Fprintf(&b, `\x%02x`, r)
		default:
			fmt.Fprintf(&b, `\u%04x`, r)
		}
	}
	return b.String()
}

// sideChannel is what the ssh of a command on a host writes to its own
// standard error. The other machine sends one line there as the command
// starts (hostMarker and the directory); everything else on it is ssh
// speaking: why it could not connect, that the connection broke, a
// warning. None of it is the command's output, which comes down ssh's
// standard output with the command's own standard error in it.
type sideChannel struct {
	r    *os.File      // our end of the pipe
	done chan struct{} // closed when nothing more will be read

	mu      sync.Mutex
	started bool   // the other machine reported that the command started
	dir     string // the directory it started in, or "" if it did not say one that can be used
	before  []byte // what ssh said before that report
	after   []byte // what ssh said after it
	line    []byte // a line whose end has not come
}

// newSideChannel makes the reader of r. The caller starts run.
func newSideChannel(r *os.File) *sideChannel {
	return &sideChannel{r: r, done: make(chan struct{})}
}

// run reads until every process that holds the other end has let go of it,
// or until our end is closed.
func (s *sideChannel) run() {
	defer close(s.done)
	defer s.r.Close()
	buf := make([]byte, 4<<10)
	for {
		n, err := s.r.Read(buf)
		if n > 0 {
			s.take(buf[:n])
		}
		if err != nil {
			s.mu.Lock()
			if len(s.line) > 0 {
				s.keep(s.line)
				s.line = nil
			}
			s.mu.Unlock()
			return
		}
	}
}

// take takes in the next piece, a line at a time.
func (s *sideChannel) take(p []byte) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.line = append(s.line, p...)
	for {
		i := bytes.IndexByte(s.line, '\n')
		if i < 0 {
			break
		}
		s.keep(s.line[:i])
		s.line = s.line[i+1:]
	}
	if len(s.line) > sideKeep {
		// A line this long is not one of ssh's; it is kept as far as
		// there is room and not waited on.
		s.keep(s.line)
		s.line = nil
	}
}

// keep files one line: the report that the command started, or something
// ssh said. It is called with mu held.
func (s *sideChannel) keep(line []byte) {
	line = bytes.TrimRight(line, "\r")
	if !s.started {
		if i := bytes.Index(line, []byte(hostMarker)); i >= 0 {
			s.started = true
			s.dir = reportedDir(line[i+len(hostMarker):])
			if line = line[:i]; len(bytes.TrimSpace(line)) > 0 {
				s.before = appendLine(s.before, line)
			}
			return
		}
		s.before = appendLine(s.before, line)
		return
	}
	s.after = appendLine(s.after, line)
}

// appendLine adds a line to what is kept of ssh's words, while there is
// room for it.
func appendLine(kept, line []byte) []byte {
	if len(bytes.TrimSpace(line)) == 0 || len(kept)+len(line)+1 > sideKeep {
		return kept
	}
	return append(append(kept, line...), '\n')
}

// reportedDir checks the directory the other machine reported. It is that
// machine's word, so it is taken only as what it must be: an absolute path
// of a sane length.
func reportedDir(b []byte) string {
	dir := strings.TrimSpace(clean(b))
	if !strings.HasPrefix(dir, "/") || len(dir) > 4<<10 {
		return ""
	}
	return dir
}

// read returns what the channel has brought so far.
func (s *sideChannel) read() (started bool, dir, before, after string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.started, s.dir, clean(s.before), clean(s.after)
}

// farEnd is how a command for another machine came out, as far as this
// machine can tell.
type farEnd struct {
	// started says the other machine reported that the command started,
	// and dir where.
	started bool
	dir     string
	// reason is ReasonNoConnection when ssh never got the command
	// started, ReasonConnectionLost when the connection broke while it
	// ran, and empty when the exit code ssh ended with is the command's
	// own, or when gnosh itself stopped ssh.
	reason string
	// headline goes above the output, and notes under it.
	headline string
	notes    []string
	// unsure says the command's start was never reported although ssh did
	// not fail: whether it ran as written is not known.
	unsure bool
}

// farEnd works out how the command of j, which was for a host, came out.
// ssh ends with the exit code of the command, and with 255 when ssh itself
// failed; a command can end with 255 too. What settles it is the side
// channel: whether the other machine reported the start, and whether ssh
// had anything to say afterwards.
func (j *job) farEnd(e *ending) farEnd {
	h := j.host
	started, dir, before, after := j.side.read()
	f := farEnd{started: started, dir: dir}
	said := func(text string) {
		if line := oneLine(text, 700); line != "" {
			f.notes = append(f.notes, "ssh on this machine said: "+line)
		}
	}
	switch {
	case e.reason != endExited && !started:
		f.notes = append(f.notes, "as far as this machine can tell, the command had not started on "+h.Name+" when ssh was stopped: ssh was still connecting or logging in")
	case e.reason != endExited:
		f.notes = append(f.notes, "stopping closed the connection, which does not by itself stop the command on "+h.Name+": a program there that is printing ends when it next writes, and one that prints nothing can run on, where gnosh can neither see it nor stop it. Look there with another command if it matters")
	case e.sig != 0 && !started:
		// Something other than gnosh killed the ssh on this machine.
		f.reason = ReasonNoConnection
		f.headline = "not run: the ssh on this machine was killed by " + signalName(e.sig) + " before the command had started on " + h.Name + ", as far as this machine can tell"
	case e.sig != 0:
		f.reason = ReasonConnectionLost
		f.headline = "the ssh on this machine was killed by " + signalName(e.sig) + " while it carried the command to " + h.Name + "; what the command had printed is below"
		f.notes = append(f.notes, "how the command ended on "+h.Name+" is not known: it may have finished, have been cut short, or still be running. Look there with another command before relying on it or running it again")
	case !started && e.code == 255:
		f.reason = ReasonNoConnection
		why, advice := connectTrouble(h, before+after, j.m.opt.ConnectTimeout)
		f.headline = "not run: " + why + ". Nothing was run on " + h.Name
		said(before + after)
		f.notes = append(f.notes, advice)
	case !started:
		f.unsure = true
		said(before + after)
		f.notes = append(f.notes, h.Name+" never reported that the command started, so the line gnosh puts in front of a command did not run there as written. The account's login shell there may not read sh (gnosh needs sh, bash, dash, ksh or zsh), or the account is tied to one fixed command. Whether the command ran is not known; the exit code is the one ssh passed on")
	case e.code == 255 && strings.TrimSpace(after) != "":
		f.reason = ReasonConnectionLost
		f.headline = "the connection to " + h.Name + " was lost while the command ran there; what it had printed is below"
		said(before + after)
		f.notes = append(f.notes, "how the command ended on "+h.Name+" is not known: it may have finished, have been cut short, or still be running. Look there with another command before relying on it or running it again")
	default:
		said(before + after)
	}
	return f
}

// connectTrouble says in plain words why ssh could not get a command
// started on h, going by what ssh itself said, and what to do about it.
func connectTrouble(h *Host, said string, limit time.Duration) (why, advice string) {
	who := h.Name + " (" + h.Target + ")"
	low := strings.ToLower(said)
	has := func(words ...string) bool {
		for _, w := range words {
			if strings.Contains(low, w) {
				return true
			}
		}
		return false
	}
	tell := "Tell the user; do not try to reach the machine another way"
	again := "If it was only away for a moment, the same call will work again; if it keeps failing, tell the user rather than reaching the machine another way"
	switch {
	case has("host identification has changed"):
		return "the key " + who + " presented is not the one this account's ssh has on record for it, so no connection was made",
			"A changed key means the machine was set up afresh, or that something else is answering in its place, and only the user can tell which. " + tell
	case has("host key verification failed", "host key is known"):
		return "this account's ssh has no key on record for " + who + ", and gnosh never accepts a machine's key by itself, so no connection was made",
			"On a first connection anything on the network could answer in the machine's place. The user has to connect once by hand, check the key's fingerprint and accept it (ssh " + h.Target + "). " + tell
	case has("permission denied", "too many authentication failures", "no supported authentication methods"):
		return who + " refused the login: it took none of this account's keys, and gnosh never types a password",
			"Keys and agents are ssh's own business (the user's ~/.ssh/config). " + tell
	case has("connection refused"):
		return who + " refused the connection: nothing answers ssh at its address", again
	case has("timed out"):
		return who + " did not answer within " + seconds(limit), again
	case has("could not resolve hostname", "name or service not known", "nodename nor servname"):
		return "the address of " + who + " could not be looked up", again
	case has("no route to host", "network is unreachable"):
		return "there is no way through the network to " + who + " from this machine", again
	case has("connection closed", "connection reset"):
		return who + " closed the connection before the command could start", again
	}
	return "ssh could not connect to " + who + " or log in", again
}

// HostCheck is what trying a declared host found.
type HostCheck struct {
	// Dir is the directory a command starts in there: the home directory
	// of the account the host was declared with.
	Dir string
	// Problem says why a command cannot be run there just now, in plain
	// words. It is empty when one can.
	Problem string
}

// TryHost logs in to the declared host called name exactly as a command for
// it would, through the same ssh with the same arguments, and runs nothing
// there but the line gnosh puts in front of every command. It is for
// whoever wants to know that a host can be used before a model needs it,
// such as gnosh doctor when it is asked to try the hosts. It reaches out to
// the machine: nothing calls it unasked.
func (m *Manager) TryHost(ctx context.Context, name string) HostCheck {
	h, err := m.findHost(name)
	switch {
	case err != nil:
		return HostCheck{Problem: err.Error()}
	case h == nil:
		return HostCheck{Problem: name + " is this machine, not a declared host"}
	}
	// The colon is the shell's command that does nothing.
	a := bashArgs{command: ":", host: h, limit: m.opt.ConnectTimeout + 10*time.Second}
	j, err := m.start(a, &core.ToolEnv{Cwd: string(filepath.Separator)}, "")
	if err != nil {
		return HostCheck{Problem: strings.TrimPrefix(err.Error(), "shelltool: ")}
	}
	j.attend(ctx, a.limit)
	m.unlist(j)
	e := j.ending()
	far := j.farEnd(e)
	printed, _, _, _ := j.out.view(400)
	os.Remove(j.out.path)
	problem := func(first string) HostCheck {
		parts := append([]string{first}, far.notes...)
		parts = slices.DeleteFunc(parts, func(p string) bool { return strings.TrimSpace(p) == "" })
		return HostCheck{Problem: strings.Join(parts, ". ")}
	}
	switch {
	case e.reason == endTimeout:
		return problem("it did not finish logging in within " + seconds(a.limit) + ", and ssh was stopped")
	case e.reason != endExited:
		return problem("the try was stopped before it finished")
	case far.reason != "" || far.unsure:
		return problem(strings.TrimPrefix(far.headline, "not run: "))
	case e.code != 0:
		return HostCheck{Problem: fmt.Sprintf("ssh logged in, and the line gnosh puts in front of a command ended with exit code %d there: %s", e.code, oneLine(printed, 300))}
	}
	return HostCheck{Dir: far.dir}
}
