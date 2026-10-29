// Copyright (C) 2026 Jae Kwon
//
// This file is part of klash's GnoVM machine subsystem. It is licensed under
// the GNO Network General Public License, version 6, or any later version
// that the Licensor has publicly accepted as the proxy under section 14 of
// that License, with the Additional Terms (Strong Attribution); see
// internal/gnovm/LICENSE.md.

package gnovm

import (
	"errors"
	"os"
)

// probeHostMemory reads MemAvailable from /proc/meminfo, bounded by the
// tightest cgroup v2 limit on the process.
func probeHostMemory() (HostMemory, error) {
	data, err := os.ReadFile("/proc/meminfo")
	if err != nil {
		return HostMemory{}, err
	}
	total, avail, ok := parseMeminfo(data)
	if !ok {
		return HostMemory{}, errors.New("/proc/meminfo has no MemAvailable")
	}
	hm := HostMemory{Total: total, Available: avail, Source: "MemAvailable"}
	if self, err := os.ReadFile("/proc/self/cgroup"); err == nil {
		if p := cgroupV2Path(self); p != "" {
			if limit, headroom, ok := cgroupHeadroom("/sys/fs/cgroup", p); ok {
				hm.CgroupLimit = limit
				if headroom < hm.Available {
					hm.Available, hm.Source = headroom, "cgroup memory.max - memory.current"
				}
			}
		}
	}
	return hm, nil
}
