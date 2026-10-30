// Copyright (C) 2026 Jae Kwon
//
// This file is part of gnosh's GnoVM machine subsystem. It is licensed under
// the GNO Network General Public License, version 6, as published by
// NewTendermint, LLC or its successor, or any later version of that License
// that the Licensor accepts as the proxy under its section 14, by a statement
// published in the canonical copy of internal/gnovm/LICENSE.md; together with
// the Additional Terms (Strong Attribution) in that file.

package gnovm

import (
	"os"

	"golang.org/x/sys/unix"
)

// probeHostMemory counts free, speculative and file-backed (reclaimable)
// pages, which is roughly what macOS can hand out without swapping.
func probeHostMemory() (HostMemory, error) {
	total, err := unix.SysctlUint64("hw.memsize")
	if err != nil {
		return HostMemory{}, err
	}
	page := uint64(os.Getpagesize())
	var pages uint64
	for _, name := range []string{"vm.page_free_count", "vm.page_speculative_count", "vm.page_pageable_external_count"} {
		n, err := unix.SysctlUint32(name)
		if err != nil {
			return HostMemory{Total: total}, err
		}
		pages += uint64(n)
	}
	return HostMemory{Total: total, Available: pages * page, Source: "free + speculative + file-backed pages"}, nil
}
