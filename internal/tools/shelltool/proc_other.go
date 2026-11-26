//go:build !linux && !darwin

package shelltool

// sessionProcs cannot list a session's processes on this system. Commands
// are then stopped by their process group alone, which misses a process
// that moved itself to another group.
func sessionProcs(sid int) ([]procInfo, error) {
	return nil, errNoProcList
}

// procArgs cannot read another process's arguments on this system.
func procArgs(pid int) ([]string, error) {
	return nil, errNoProcList
}

// procEnvHas cannot read another process's environment on this system.
func procEnvHas(pid int, entry string) (bool, error) {
	return false, errNoEnviron
}
