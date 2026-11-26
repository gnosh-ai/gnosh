//go:build linux

package main

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"syscall"
	"testing"
	"time"

	"golang.org/x/sys/unix"

	"github.com/gnosh-ai/gnosh/internal/app/apptest"
)

// terminal is a real terminal for a test: the far end of a pseudo
// terminal whose near end is gnosh's standard input and output. What the
// test writes is what a person types (the terminal echoes it, and turns
// Ctrl+C into the interrupt signal); what it reads is the screen.
//
// These are the tests of the plain session, the one that reads lines:
// what a person gets where the full interface has no terminal it could
// draw on. The full interface is tried in a terminal emulator, in
// tmux_test.go.
type terminal struct {
	t      *testing.T
	master *os.File
	slave  *os.File
	screen lockedBuffer
	pos    int
	// kind is what TERM says the terminal is.
	kind string
}

// openTerminal makes a pseudo terminal.
func openTerminal(t *testing.T) *terminal {
	t.Helper()
	master, err := os.OpenFile("/dev/ptmx", os.O_RDWR|syscall.O_NOCTTY, 0)
	if err != nil {
		t.Skipf("no pseudo terminal can be made here: %v", err)
	}
	fd := int(master.Fd())
	if err := unix.IoctlSetPointerInt(fd, unix.TIOCSPTLCK, 0); err != nil {
		t.Fatalf("unlocking the pseudo terminal: %v", err)
	}
	n, err := unix.IoctlGetInt(fd, unix.TIOCGPTN)
	if err != nil {
		t.Fatalf("naming the pseudo terminal: %v", err)
	}
	slave, err := os.OpenFile(fmt.Sprintf("/dev/pts/%d", n), os.O_RDWR|syscall.O_NOCTTY, 0)
	if err != nil {
		t.Fatalf("opening the pseudo terminal: %v", err)
	}
	term := &terminal{t: t, master: master, slave: slave, kind: "dumb"}
	go io.Copy(&term.screen, master) // ends when the last process has left the terminal
	t.Cleanup(func() { master.Close() })
	return term
}

// run starts gnosh at the terminal. With piped set, its standard input is
// that text through a pipe, and the terminal is only its screen and the
// place to type.
func (term *terminal) run(w *apptest.World, piped string, args ...string) *process {
	term.t.Helper()
	p := &process{t: term.t, cmd: exec.Command(binary, args...), out: &lockedBuffer{}, err: &lockedBuffer{}, done: make(chan struct{})}
	p.cmd.Dir = w.Dir
	p.cmd.Env = append(w.Env(), "TERM="+term.kind)
	p.cmd.Stdin, p.cmd.Stdout, p.cmd.Stderr = term.slave, term.slave, term.slave
	if piped != "" {
		p.cmd.Stdin = strings.NewReader(piped)
	}
	// The terminal becomes the controlling terminal of a new session, so
	// that Ctrl+C typed at it reaches gnosh and /dev/tty names it.
	p.cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true, Setctty: true, Ctty: 1}
	if err := p.cmd.Start(); err != nil {
		term.t.Fatal(err)
	}
	term.slave.Close()
	go func() {
		p.state = p.cmd.Wait()
		close(p.done)
	}()
	term.t.Cleanup(func() {
		select {
		case <-p.done:
		default:
			p.cmd.Process.Kill()
			<-p.done
		}
	})
	return p
}

// see waits until the screen shows piece after everything seen so far.
func (term *terminal) see(piece string) {
	term.t.Helper()
	deadline := time.Now().Add(time.Minute)
	for {
		screen := term.screen.String()
		if i := strings.Index(screen[term.pos:], piece); i >= 0 {
			term.pos += i + len(piece)
			return
		}
		if time.Now().After(deadline) {
			term.t.Fatalf("the screen never showed %q after what was seen before.\nthe screen:\n%s", piece, screen)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// typeLine types a line and Enter.
func (term *terminal) typeLine(line string) {
	term.t.Helper()
	if _, err := io.WriteString(term.master, line+"\n"); err != nil {
		term.t.Fatalf("typing %q: %v", line, err)
	}
}

// ctrlC presses Ctrl+C.
func (term *terminal) ctrlC() {
	term.t.Helper()
	if _, err := term.master.Write([]byte{3}); err != nil {
		term.t.Fatalf("pressing Ctrl+C: %v", err)
	}
}

// A person at a real terminal that says it is a dumb one, so that the
// full interface declines it and the session is the plain one: a prompt,
// a permission question answered after seeing the change, a slash
// command, Ctrl+C to stop a turn, and Ctrl+C again to leave.
func TestBinaryInteractiveSession(t *testing.T) {
	hold := make(chan struct{})
	defer close(hold)
	w := apptest.NewWorld(t,
		apptest.Reply{Calls: []apptest.Call{{ID: "c1", Name: "Read", Args: `{"file_path":"a.txt"}`}}},
		apptest.Reply{Calls: []apptest.Call{{ID: "c2", Name: "Edit", Args: `{"file_path":"a.txt","old_string":"two plus","new_string":"three plus"}`}}},
		apptest.Reply{Text: "The file is changed."},
		apptest.Reply{Text: "Once upon a time there was a model that never stopped.", Hold: hold})
	w.Write("a.txt", "one\ntwo plus two\nthree\n")
	term := openTerminal(t)
	p := term.run(w, "")
	term.see("model local/test-model")
	term.see("Type a prompt and Enter.")

	term.typeLine("change the file")
	term.see("? Allow Edit?")
	term.see("- two plus two")
	term.see("+ three plus two")
	term.see("1. Yes, this once")
	// (As a person would, after reading it: a line typed as the question
	// came is not taken for its answer.)
	time.Sleep(600 * time.Millisecond)
	term.typeLine("1")
	term.see("The file is changed.")
	term.see("-- done in ")
	if got := w.Read("a.txt"); got != "one\nthree plus two\nthree\n" {
		t.Errorf("a.txt = %q", got)
	}

	term.typeLine("/status")
	term.see("state        idle")

	term.typeLine("tell me a story")
	term.see("Once upon a time")
	term.ctrlC()
	term.see("Stopping the turn.")
	term.see("-- stopped in ")
	term.ctrlC()
	p.wait(30*time.Second).wantCode(t, 130)
	term.see("To resume this session: gnosh -r ")
	checkSession(t, w)
}

// A prompt piped into a session at a terminal is the session's first
// prompt, and the person goes on typing at the terminal itself. The
// terminal is one the full interface would draw on, but standard input is
// not it, so the full interface declines here too and the session is the
// plain one.
func TestBinaryPipedPromptThenTyping(t *testing.T) {
	w := apptest.NewWorld(t,
		apptest.Reply{Text: "The piped report says the build is broken."},
		apptest.Reply{Text: "You are welcome."})
	term := openTerminal(t)
	term.kind = "xterm-256color"
	p := term.run(w, "the build is broken\nsee the log\n")
	term.see("The piped report says the build is broken.")
	term.see("-- done in ")
	term.typeLine("thanks")
	term.see("You are welcome.")
	term.typeLine("/exit")
	p.wait(30*time.Second).wantCode(t, 0)
	first := string(w.Model.Requests()[0].Body)
	if !strings.Contains(first, `the build is broken\nsee the log`) {
		t.Errorf("the piped text is not the first prompt, whole")
	}
	w.Model.Done()
}

// The model's Restart, at a real terminal: gnosh starts again in place of
// itself, so the process id stays the one it was started with, and
// whatever holds it, a terminal's pane, a supervisor or a kill from
// outside, still reaches gnosh (Jae, 26 November 2026: "keep a root PID so
// gnosh can still be killed externally"). A command run before the
// restart and one after it see the same gnosh as their parent, the process
// the test started; the gnosh that started again goes on by itself; and a
// SIGTERM sent to that id from outside still ends it.
func TestBinaryRestartKeepsItsPid(t *testing.T) {
	w := apptest.NewWorld(t,
		apptest.Reply{Calls: []apptest.Call{{ID: "c1", Name: "Bash", Args: `{"command":"echo $PPID > before.pid"}`}}},
		apptest.Reply{Calls: []apptest.Call{{ID: "c2", Name: "Restart", Args: `{"reason":"asked to"}`}}},
		apptest.Reply{Calls: []apptest.Call{{ID: "c3", Name: "Bash", Args: `{"command":"echo $PPID > after.pid"}`}}},
		apptest.Reply{Text: "Back after the restart."},
	)
	term := openTerminal(t)
	p := term.run(w, "", "-y")
	term.see("Type a prompt and Enter.")
	term.typeLine("restart yourself")
	term.see("gnosh starts again in this session, at the model's call (asked to).")
	term.see("Back after the restart.")
	term.see("-- done in ")
	before, after := readPid(t, w, "before.pid"), readPid(t, w, "after.pid")
	if before != p.cmd.Process.Pid || after != p.cmd.Process.Pid {
		t.Errorf("gnosh was started as process %d; its commands saw it as %d before the restart and %d after", p.cmd.Process.Pid, before, after)
	}
	if err := p.cmd.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatalf("SIGTERM to the process gnosh was started as: %v", err)
	}
	p.wait(30*time.Second).wantCode(t, 143)
	checkSession(t, w)
}
