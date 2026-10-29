// Copyright (C) 2026 Jae Kwon
//
// This file is part of klash's GnoVM machine subsystem. It is licensed under
// the GNO Network General Public License, version 6, or any later version
// that the Licensor has publicly accepted as the proxy under section 14 of
// that License, with the Additional Terms (Strong Attribution); see
// internal/gnovm/LICENSE.md.

//go:build windows

package gnovm

import (
	"fmt"
	"os"
)

// WorkerMain is `klash gnovm-worker`. Windows cannot hand a child the extra
// pipes the protocol needs, so there the VMs run in klash's own process.
func WorkerMain() int {
	fmt.Fprintln(os.Stderr, "gnovm-worker: not used on Windows; VMs run in klash's process")
	return 2
}
