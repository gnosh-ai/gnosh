// Copyright (C) 2026 Jae Kwon
//
// This file is part of klash's GnoVM machine subsystem. It is licensed under
// the GNO Network General Public License, version 6, as published by
// NewTendermint, LLC or its successor, or any later version of that License
// that the Licensor accepts as the proxy under its section 14, by a statement
// published in the canonical copy of internal/gnovm/LICENSE.md; together with
// the Additional Terms (Strong Attribution) in that file.

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
					hm.Available, hm.Source = headroom, "cgroup limit less its usage net of reclaimable cache"
				}
			}
		}
	}
	return hm, nil
}
