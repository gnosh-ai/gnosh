// Copyright (C) 2026 Jae Kwon
//
// This file is part of klash's GnoVM machine subsystem. It is licensed under
// the GNO Network General Public License, version 6, or any later version
// that the Licensor has publicly accepted as the proxy under section 14 of
// that License, with the Additional Terms (Strong Attribution); see
// internal/gnovm/LICENSE.md.

//go:build unix

package gnovm

import (
	"os/exec"
	"syscall"
)

// setWorkerProcAttr puts the worker in its own process group, so a Ctrl-C at
// klash's terminal does not reach it in the middle of an append.
func setWorkerProcAttr(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
}
