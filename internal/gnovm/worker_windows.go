// Copyright (C) 2026 Jae Kwon
//
// This file is part of gnosh's GnoVM machine subsystem. It is licensed under
// the GNO Network General Public License, version 6, as published by
// NewTendermint, LLC or its successor, or any later version of that License
// that the Licensor accepts as the proxy under its section 14, by a statement
// published in the canonical copy of internal/gnovm/LICENSE.md; together with
// the Additional Terms (Strong Attribution) in that file.

//go:build windows

package gnovm

import (
	"fmt"
	"os"
)

// WorkerMain is `gnosh gnovm-worker`. Windows cannot hand a child the extra
// pipes the protocol needs, so there the VMs run in gnosh's own process.
func WorkerMain() int {
	fmt.Fprintln(os.Stderr, "gnovm-worker: not used on Windows; VMs run in gnosh's process")
	return 2
}
