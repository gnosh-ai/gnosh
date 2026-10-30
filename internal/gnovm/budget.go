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
	"fmt"
	"strings"
)

// ProbeHostMemory reads how much memory the host has and can give: Linux's
// MemAvailable, bounded by the tightest cgroup v2 limit; on macOS the free,
// speculative and file-backed pages.
func ProbeHostMemory() (HostMemory, error) { return probeHostMemory() }

const (
	// probeFailBudget is the budget when the host's memory cannot be read.
	probeFailBudget = 256 << 20
	// autoBudgetMax caps the automatic budget.
	autoBudgetMax = 1 << 30
)

// declHeapFactor is the Go heap a declaration keeps per byte of its source:
// its syntax tree, preprocessed, with attributes and a map on every node.
// Measured at 300 to 375 across shapes from one function per file to 300,
// and bodies of up to 200 statements (review round 3); statements keep
// nothing once run. TestDeclarationChargeCoversHeap holds the factor to the
// measure.
const declHeapFactor = 400

// vmCharge is what a resident VM is counted as using. A VM's allocator cap
// bounds only the values it holds; the heap also keeps the syntax trees of
// its declarations, its own decoded copies of the standard library objects
// it touched (at most 0.2 MB per imported package, measured; 0.3 charged),
// and the machine itself (1 MB).
func vmCharge(capBytes, declBytes int64, imports int) int64 {
	return capBytes + declHeapFactor*declBytes + int64(imports)*(300<<10) + 1<<20
}

func (h *Host) charge(s *vmState) int64 {
	if s.res == nil {
		return 0
	}
	return vmCharge(s.res.capBytes, s.res.declBytes, len(s.res.imports))
}

// usedBytes is the memory the host counts as in use: the base's measured
// heap, every resident VM's charge, and a reserve for the transient peak of
// the GnoVM's garbage collector. That collector recurses through the values
// it marks, using about 1.3 bytes of Go stack per byte of cap, and Go copies
// a stack when it grows: 3 times the largest resident cap covers it.
func (h *Host) usedBytes() int64 { return h.usedWith(nil) }

// usedWith is usedBytes with s counted as resident too.
func (h *Host) usedWith(s *vmState) int64 {
	var used, largest int64
	if h.base != nil {
		used += h.base.heap
	}
	for _, other := range h.vms {
		if other.res != nil && other != s {
			used += h.charge(other)
			largest = max(largest, other.res.capBytes)
		}
	}
	if s != nil {
		used += vmCharge(int64(s.opts.MemoryMB)<<20, s.cost.declBytes, s.cost.imports)
		largest = max(largest, int64(s.opts.MemoryMB)<<20)
	}
	return used + 3*largest
}

// budgetFor is the memory the VMs may use: the configured budget, or the
// smaller of 1 GB and a tenth of the memory the host could give them, and
// never more than half of that. "Could give" adds what the VMs already use
// back to what is available, so the budget does not shrink as they grow.
func (h *Host) budgetFor(mem HostMemory, probeErr error) int64 {
	if probeErr != nil {
		if h.opts.BudgetBytes > 0 {
			return h.opts.BudgetBytes
		}
		return probeFailBudget
	}
	could := int64(mem.Available) + h.usedBytes()
	budget := h.opts.BudgetBytes
	if budget <= 0 {
		budget = min(autoBudgetMax, could/10)
	}
	return min(budget, could/2)
}

// admit makes room for s to become resident: if the budget cannot hold it,
// it evicts the least recently used resident VMs that are not pinned, until
// it can. A VM evicted keeps its journal and is rebuilt when next used.
func (h *Host) admit(s *vmState) (evicted []string, err error) {
	mem, perr := h.opts.Probe()
	budget := h.budgetFor(mem, perr)
	for h.usedWith(s) > budget {
		var victim *vmState
		for _, other := range h.vms {
			if other == s || other.res == nil || other.opts.Pinned {
				continue
			}
			if victim == nil || other.lastUse < victim.lastUse {
				victim = other
			}
		}
		if victim == nil {
			return evicted, h.noRoom(s, budget)
		}
		victim.res = nil
		evicted = append(evicted, victim.name)
	}
	return evicted, nil
}

func (h *Host) noRoom(s *vmState, budget int64) error {
	var pinned []string
	for _, other := range h.vms {
		if other.res != nil && other.opts.Pinned && other != s {
			pinned = append(pinned, other.name)
		}
	}
	msg := fmt.Sprintf("not enough memory for VM %s: it needs about %d MB; the VMs' budget is %d MB and %d MB of it is in use",
		s.name, vmCharge(int64(s.opts.MemoryMB)<<20, s.cost.declBytes, s.cost.imports)>>20, budget>>20, h.usedBytes()>>20)
	if len(pinned) > 0 {
		msg += " (pinned: " + strings.Join(pinned, ", ") + ")"
	}
	return &refusal{msg + ". Unpin or drop a VM, or use a smaller memory_mb."}
}

// enforceBudget evicts the least recently used resident VMs other than s,
// and not pinned, while the VMs use more than the budget: a VM grows after
// it is admitted, and only admission checked the budget before. It returns
// the names of the VMs it evicted.
func (h *Host) enforceBudget(s *vmState) []string {
	mem, perr := h.opts.Probe()
	budget := h.budgetFor(mem, perr)
	var evicted []string
	for h.usedBytes() > budget {
		var victim *vmState
		for _, other := range h.vms {
			if other == s || other.res == nil || other.opts.Pinned {
				continue
			}
			if victim == nil || other.lastUse < victim.lastUse {
				victim = other
			}
		}
		if victim == nil {
			break
		}
		victim.res = nil
		evicted = append(evicted, victim.name)
	}
	return evicted
}
