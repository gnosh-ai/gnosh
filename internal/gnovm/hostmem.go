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
	"bufio"
	"bytes"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// HostMemory is what the host klash runs on can give it.
type HostMemory struct {
	Total       uint64 // bytes; 0 when unknown
	Available   uint64 // bytes obtainable without swapping; 0 when unknown
	CgroupLimit uint64 // tightest cgroup memory.max, in bytes; 0 when none
	Source      string // how Available was found, shown by gnovm_list
}

// parseMeminfo reads MemTotal and MemAvailable (in bytes) from the contents
// of /proc/meminfo. ok is false if MemAvailable is missing.
func parseMeminfo(data []byte) (total, available uint64, ok bool) {
	sc := bufio.NewScanner(bytes.NewReader(data))
	for sc.Scan() {
		key, rest, found := strings.Cut(sc.Text(), ":")
		if !found {
			continue
		}
		fields := strings.Fields(rest)
		if len(fields) == 0 {
			continue
		}
		n, err := strconv.ParseUint(fields[0], 10, 64)
		if err != nil {
			continue
		}
		if len(fields) > 1 && fields[1] == "kB" {
			n *= 1024
		}
		switch key {
		case "MemTotal":
			total = n
		case "MemAvailable":
			available, ok = n, true
		}
	}
	return total, available, ok
}

// cgroupV2Path returns the cgroup v2 path of the process from the contents of
// /proc/self/cgroup (the "0::<path>" line), or "" if there is none.
func cgroupV2Path(data []byte) string {
	for line := range strings.SplitSeq(string(data), "\n") {
		if p, ok := strings.CutPrefix(line, "0::"); ok {
			return strings.TrimSpace(p)
		}
	}
	return ""
}

// cgroupHeadroom walks from the process's cgroup up to the root of a cgroup v2
// hierarchy mounted at root, and returns the smallest headroom found and the
// limit it came from. At each level the limit is the lower of memory.max and
// memory.high (past memory.high the kernel throttles and reclaims), and the
// usage is memory.current less the inactive page cache in memory.stat, which
// the kernel takes back before it runs out: counting the cache as used would
// leave a busy lane with no room at all. ok is false when no level sets a
// limit.
func cgroupHeadroom(root, cgPath string) (limit, headroom uint64, ok bool) {
	dir := filepath.Join(root, filepath.FromSlash(cgPath))
	for {
		lim, err := readCgroupUint(filepath.Join(dir, "memory.max"))
		if high, herr := readCgroupUint(filepath.Join(dir, "memory.high")); herr == nil && (err != nil || high < lim) {
			lim, err = high, nil
		}
		if err == nil {
			cur, _ := readCgroupUint(filepath.Join(dir, "memory.current"))
			if cache, cerr := readCgroupStat(filepath.Join(dir, "memory.stat"), "inactive_file"); cerr == nil {
				cur -= min(cache, cur)
			}
			h := uint64(0)
			if lim > cur {
				h = lim - cur
			}
			if !ok || h < headroom {
				limit, headroom, ok = lim, h, true
			}
		}
		if dir == root || !strings.HasPrefix(dir, root) {
			break
		}
		dir = filepath.Dir(dir)
	}
	return limit, headroom, ok
}

// readCgroupStat reads one key of a cgroup "key value" file such as
// memory.stat.
func readCgroupStat(path, key string) (uint64, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return 0, err
	}
	for line := range strings.SplitSeq(string(b), "\n") {
		if v, found := strings.CutPrefix(line, key+" "); found {
			return strconv.ParseUint(strings.TrimSpace(v), 10, 64)
		}
	}
	return 0, os.ErrNotExist
}

// readCgroupUint reads a cgroup file holding one number. A "max" (no limit)
// or unreadable file is an error.
func readCgroupUint(path string) (uint64, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return 0, err
	}
	return strconv.ParseUint(strings.TrimSpace(string(b)), 10, 64)
}
