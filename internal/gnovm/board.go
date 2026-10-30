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
	"context"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// summaryFile is where a mem VM keeps the first line of its board's
// outline ("board: 2 open, 5 done · next: 3 ..."), rewritten after every
// change, so that klash can tell a new session what is open without
// starting a worker or rebuilding the VM.
const summaryFile = "summary.txt"

// writeBoardSummary rewrites a mem VM's summary from its board, after the
// change at time t was journaled. It is best effort: a session that misses
// it calls mem.Show() itself.
func (h *Host) writeBoardSummary(ctx context.Context, s *vmState, v *vm, t int64) {
	if !v.isBoard(MemVM) {
		return // an older VM named mem, whose Show might change it
	}
	e := &entry{Seq: s.j.seq + 1, Time: t, Ops: []op{{Eval: MemVM + ".Show()"}}}
	value, _, err := v.apply(ctx, e, 50_000_000, time.Now().Add(5*time.Second), true)
	if err != nil {
		if v.unusable {
			s.res = nil
		}
		return
	}
	if raw, err := strconv.Unquote(value); err == nil {
		value = raw
	}
	header, _, _ := strings.Cut(value, "\n")
	tmp := filepath.Join(s.dir, "."+summaryFile)
	if os.WriteFile(tmp, []byte(header+"\n"), 0o600) == nil {
		_ = os.Rename(tmp, filepath.Join(s.dir, summaryFile))
	}
}

// BoardSummary is what a new session in project should know of its board,
// from the summary its mem VM last wrote in dir: "" unless an item is open.
// The summary file's time is when the board last changed.
func BoardSummary(dir, project string) string {
	path := filepath.Join(dir, projectKey(project), "vms", MemVM, summaryFile)
	b, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	info, err := os.Stat(path)
	if err != nil {
		return ""
	}
	header := strings.TrimSpace(string(b))
	if !strings.HasPrefix(header, "board: ") || strings.HasPrefix(header, "board: 0 open") {
		return ""
	}
	return "<gno-board>\nYour Gno board mem, from earlier sessions (last changed " + info.ModTime().UTC().Format("2006-01-02 15:04") + " UTC): " +
		header + ". `mem.Show()` (gnovm_run name \"mem\") shows it; work on it only if this task is part of it.\n</gno-board>"
}

// BoardSummary is BoardSummary for this Lazy's directory. It reads a file:
// no worker is started.
func (l *Lazy) BoardSummary(project string) string { return BoardSummary(l.opts.Dir, project) }
