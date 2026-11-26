package shelltool

import (
	"bytes"
	"errors"
	"strconv"
)

// procInfo is one live process, as far as stopping commands needs to know
// it.
type procInfo struct {
	pid  int
	ppid int // its parent
	pgid int // its process group
	// start is when the process began, in whatever units the system counts
	// in. A pid can be handed out again; a pid with its start time names
	// one process for good.
	start uint64
	name  string // the program's short name, for messages
	// image tells one program the process has been from the next: it
	// changes when the process becomes another program (exec), and not
	// otherwise. It is empty where that cannot be told.
	image string
}

// errNoProcList says that this system has no way, here, to list the
// processes of a session. Callers fall back on the process group alone.
var errNoProcList = errors.New("listing a session's processes is not supported on this system")

// errNoEnviron says that this system does not show one process another's
// environment.
var errNoEnviron = errors.New("this system does not show a process's environment to another")

// statLine is what parseStat reads from a Linux /proc/<pid>/stat file.
type statLine struct {
	name    string
	state   byte // 'R', 'S', 'Z' for a zombie, and so on
	ppid    int
	pgid    int
	session int
	start   uint64
	// stack is the address at which the program's stack starts. The kernel
	// picks it afresh, at random, each time a process becomes a program, so
	// it tells a process that has become another program (or the same one
	// again) since it was last looked at. It is 0 when the kernel does not
	// say.
	stack uint64
}

// parseStat reads the fields of a /proc/<pid>/stat line that matter here.
// The program name sits in parentheses and may itself hold spaces and
// parentheses, so the fields are counted from the last ")".
func parseStat(b []byte) (statLine, bool) {
	open := bytes.IndexByte(b, '(')
	shut := bytes.LastIndexByte(b, ')')
	if open < 0 || shut < open {
		return statLine{}, false
	}
	// After the name: state ppid pgrp session tty_nr tpgid flags minflt
	// cminflt majflt cmajflt utime stime cutime cstime priority nice
	// num_threads itrealvalue starttime vsize rss rsslim startcode endcode
	// startstack ...
	f := bytes.Fields(b[shut+1:])
	if len(f) < 20 || len(f[0]) != 1 {
		return statLine{}, false
	}
	if c := f[0][0] | 0x20; c < 'a' || c > 'z' {
		return statLine{}, false // a state is always one letter
	}
	ppid, err1 := strconv.Atoi(string(f[1]))
	pgid, err2 := strconv.Atoi(string(f[2]))
	session, err3 := strconv.Atoi(string(f[3]))
	start, err4 := strconv.ParseUint(string(f[19]), 10, 64)
	if err1 != nil || err2 != nil || err3 != nil || err4 != nil {
		return statLine{}, false
	}
	st := statLine{
		name:    string(b[open+1 : shut]),
		state:   f[0][0],
		ppid:    ppid,
		pgid:    pgid,
		session: session,
		start:   start,
	}
	if len(f) > 25 {
		st.stack, _ = strconv.ParseUint(string(f[25]), 10, 64)
	}
	return st, true
}

// sameProcess reports whether a process in now is one of those in then:
// the same pid, started at the same moment.
func sameProcess(now, then []procInfo) bool {
	for _, a := range now {
		for _, b := range then {
			if a.pid == b.pid && a.start == b.start {
				return true
			}
		}
	}
	return false
}
