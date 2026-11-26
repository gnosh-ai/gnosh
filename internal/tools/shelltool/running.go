package shelltool

import (
	"cmp"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/gnosh-ai/gnosh/internal/core"
)

// maxProgramChars is how much of one program's command line is named.
const maxProgramChars = 200

// tellRunning tells fn, while a command runs in the foreground, which
// programs its shell runs just now (core.StreamRunning), each time that
// changes. The command's first line, which is all the call's heading
// names, may be a file being written while the time goes to the line
// after it (Jae, 26 November 2026, of `cat > main.go <<'EOF'` shown for
// minutes while `go run main.go | tail -15` ran: "why is it stuck
// seemingly"). It ends with the shell, or once the command has gone to
// the background.
func (j *job) tellRunning(fn func(core.Progress), callID string) {
	o := &j.m.opt
	t := time.NewTimer(o.RunningAfter)
	defer t.Stop()
	var said, seen string
	for {
		select {
		case <-j.exited:
			return
		case <-j.done:
			return
		case <-t.C:
		}
		if j.sink.Load() == nil {
			return // in the background now, or over
		}
		now := j.runningNow()
		// Named once seen twice, so that a quick program does not flash
		// past; and kept when the shell runs nothing of its own for a
		// moment, between two programs.
		if now != "" && now == seen && now != said {
			said = now
			fn(core.Progress{CallID: callID, Stream: core.StreamRunning, Text: now})
		}
		seen = now
		t.Reset(o.RunningEvery)
	}
}

// runningNow names the programs the command's shell runs just now: the
// live processes of its session whose parent is one of the command's
// shells, each as it was started, in the order they began, as in "go run
// main.go, tail -15". The command's shells are the one gnosh started, the
// copies of it a pipeline or a subshell makes, and what wraps it (a
// sandbox): all of them carry its arguments, among them the file for the
// command's last directory, which nothing else names. A program another
// program started is not named: `go run` is, the program it builds and
// runs is not. It is "" when the shell runs nothing of its own just now,
// and when this system cannot list a session's processes or read their
// arguments.
func (j *job) runningNow() string {
	if j.cwdFile == "" {
		return "" // a command on another machine: here there is only ssh
	}
	procs, err := sessionProcs(j.pid)
	if err != nil {
		return ""
	}
	type proc struct {
		procInfo
		args  []string
		shell bool
	}
	byPid := make(map[int]*proc, len(procs))
	for _, p := range procs {
		args, err := procArgs(p.pid)
		if err != nil || len(args) == 0 {
			continue // gone since, or not to be read
		}
		byPid[p.pid] = &proc{procInfo: p, args: args, shell: slices.Contains(args, j.cwdFile)}
	}
	var programs []*proc
	for _, p := range byPid {
		if parent := byPid[p.ppid]; !p.shell && parent != nil && parent.shell {
			programs = append(programs, p)
		}
	}
	slices.SortFunc(programs, func(a, b *proc) int {
		return cmp.Or(cmp.Compare(a.start, b.start), cmp.Compare(a.pid, b.pid))
	})
	names := make([]string, len(programs))
	for i, p := range programs {
		names[i] = programLine(p.args)
	}
	return strings.Join(names, ", ")
}

// programLine is a program's command line for a person to read: its name
// without the folder it was found in, then its arguments, each cut at its
// first line ("…" where more follows) and in double quotes when it holds
// a space, the whole cut short past maxProgramChars. A script handed over
// as one argument (ssh box "python3 - <<'EOF' ..."), shell-quoted whole,
// took the whole line and read as a run of escaped quotes (Jae, 26 November 2026,
// "is it stuck now?"). It is for reading, not for pasting into a shell.
func programLine(args []string) string {
	words := make([]string, len(args))
	for i, a := range args {
		if i == 0 {
			a = filepath.Base(a)
		}
		if first, _, more := strings.Cut(a, "\n"); more {
			a = strings.TrimRight(first, " \t\r") + " …"
		}
		if a == "" || strings.ContainsAny(a, " \t") {
			a = `"` + a + `"`
		}
		words[i] = a
	}
	line := strings.Join(words, " ")
	if r := []rune(line); len(r) > maxProgramChars {
		line = string(r[:maxProgramChars]) + "…"
	}
	return line
}
