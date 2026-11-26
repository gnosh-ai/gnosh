package shelltool

import (
	"bytes"
	"os"
	"strconv"
	"syscall"
)

// sessionProcs lists the processes of session sid that are still alive.
// Zombies are left out: they are already dead and only wait to be reaped.
//
// /proc names every process. Which session each is in is asked of the
// kernel directly, which costs next to nothing; the stat file, which costs
// a good deal more to have written out, is read only for the few that are
// in the session, for their process group, start time, name, whether they
// are zombies, and which program they are just now.
func sessionProcs(sid int) ([]procInfo, error) {
	dir, err := os.Open("/proc")
	if err != nil {
		return nil, err
	}
	names, err := dir.Readdirnames(-1)
	dir.Close()
	if err != nil {
		return nil, err
	}
	var out []procInfo
	for _, name := range names {
		pid, err := strconv.Atoi(name)
		if err != nil || pid <= 0 {
			continue // not a process
		}
		if s, _, errno := syscall.RawSyscall(syscall.SYS_GETSID, uintptr(pid), 0, 0); errno != 0 || int(s) != sid {
			continue // gone, or in another session
		}
		data, err := os.ReadFile("/proc/" + name + "/stat")
		if err != nil {
			continue // it ended while we were looking
		}
		// The session is checked again: the pid may have changed hands
		// between the two looks.
		st, ok := parseStat(data)
		if !ok || st.session != sid || st.state == 'Z' || st.state == 'X' {
			continue
		}
		out = append(out, procInfo{
			pid:   pid,
			ppid:  st.ppid,
			pgid:  st.pgid,
			start: st.start,
			name:  st.name,
			image: st.name + "@" + strconv.FormatUint(st.stack, 16),
		})
	}
	return out, nil
}

// procArgs returns the arguments process pid began its program with, the
// program's own name first, as /proc keeps them.
func procArgs(pid int) ([]string, error) {
	data, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/cmdline")
	if err != nil {
		return nil, err
	}
	data = bytes.TrimSuffix(data, []byte{0})
	if len(data) == 0 {
		return nil, nil // a kernel thread, or a process on its way out
	}
	var args []string
	for a := range bytes.SplitSeq(data, []byte{0}) {
		args = append(args, string(a))
	}
	return args, nil
}

// procEnvHas reports whether the environment process pid began its
// program with holds entry ("NAME=value") whole. /proc keeps it as the
// program was handed it.
func procEnvHas(pid int, entry string) (bool, error) {
	data, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/environ")
	if err != nil {
		return false, err
	}
	for _, kv := range bytes.Split(data, []byte{0}) {
		if string(kv) == entry {
			return true, nil
		}
	}
	return false, nil
}
