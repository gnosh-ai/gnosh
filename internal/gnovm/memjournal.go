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
	"os"
	"time"
)

// mem's journal is the record of every input that changed mem: what ran,
// who ran it, and the Ops it made. It is the source: the store and the index
// are built from it, and can be rebuilt from it. Its framing is a VM
// journal's (journal.go): a header line, then one line per entry, appended
// and synced before anything else is written. Its header's kind is "mem",
// and it carries mem's instance id, which a rewrite keeps.

const memJournalKind = "mem"

// memEntry is one input that changed mem.
type memEntry struct {
	Seq int64 `json:"seq"`
	// Time is when the input ran, in Unix seconds: the time its Ops stamp
	// items with. Offset is the user's UTC offset then, in seconds.
	Time   int64 `json:"time"`
	Offset int   `json:"offset"`
	// Req is the request's id, so a caller that lost the reply can ask
	// whether the input was kept.
	Req string `json:"req,omitempty"`
	// Who ran it: "agent" (a session's), "user" or "klash"; and the
	// session it came from, if any.
	Who     string `json:"who"`
	Session string `json:"session,omitempty"`
	// Code is the Gno the input ran, and Ops what it changed, in order.
	Code string  `json:"code"`
	Ops  []memOp `json:"ops"`
}

func (e memEntry) seqNo() int64 { return e.Seq }

// createMemJournal writes a new mem journal, for a new mem: a new
// generation and a new instance id.
func createMemJournal(path string, now time.Time) (*journal, error) {
	return writeJournalHeader(path, journalHeader{Generation: newGeneration(), Created: now.UTC(), Kind: memJournalKind, Instance: newGeneration()})
}

// readMemJournal reads a whole mem journal. A torn last line is reported as
// torn, for the caller to cut under mem's lock.
func readMemJournal(path string) (j *journal, entries []memEntry, torn bool, err error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, nil, false, err
	}
	return parseJournalOf[memEntry](path, data, memJournalKind)
}
