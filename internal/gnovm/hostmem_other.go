// Copyright (C) 2026 Jae Kwon
//
// This file is part of klash's GnoVM machine subsystem. It is licensed under
// the GNO Network General Public License, version 6, as published by
// NewTendermint, LLC or its successor, or any later version of that License
// that the Licensor accepts as the proxy under its section 14, by a statement
// published in the canonical copy of internal/gnovm/LICENSE.md; together with
// the Additional Terms (Strong Attribution) in that file.

//go:build !linux && !darwin

package gnovm

import "errors"

// probeHostMemory is not implemented here; the budget falls back to its
// configured or default size.
func probeHostMemory() (HostMemory, error) {
	return HostMemory{}, errors.New("host memory probe not supported on this OS")
}
