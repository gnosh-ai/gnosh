// Copyright (C) 2026 Jae Kwon
//
// This file is part of klash's GnoVM machine subsystem. It is licensed under
// the GNO Network General Public License, version 6, or any later version
// that the Licensor has publicly accepted as the proxy under section 14 of
// that License, with the Additional Terms (Strong Attribution); see
// internal/gnovm/LICENSE.md.

//go:build windows

package gnovm

import "os/exec"

// setWorkerProcAttr does nothing: on Windows the VMs run in-process.
func setWorkerProcAttr(*exec.Cmd) {}
