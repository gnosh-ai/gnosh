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
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/charmbracelet/crush/internal/lock"
)

// memHost is a project's mem on disk. Its directory holds:
//
//	journal.jsonl  every input that changed mem: the source (memjournal.go)
//	store-<gen>/   the realm, built from the journal of generation gen
//	lock           held through each call, so klash processes take turns
//
// A call takes the lock, opens the store and brings it up to the journal,
// runs, journals what it changed, commits that to the store, and closes the
// store again: opening it costs a few milliseconds.
type memHost struct {
	b   *base
	dir string
	// now is the time a call runs at.
	now func() time.Time
	// slice is how long one call may spend bringing the store up to the
	// journal before it says to call again. What it applied is kept.
	slice time.Duration
}

func newMemHost(b *base, dir string) *memHost {
	return &memHost{b: b, dir: dir, now: time.Now, slice: 10 * time.Second}
}

// memBatchOps is about how many Ops one transaction applies when the store
// catches up with the journal. (A variable only so tests can shorten it.)
var memBatchOps = 1000

// memRequest is one input to mem.
type memRequest struct {
	Code string
	// Session is the session the input comes from; "" for none.
	Session string
	// User is set when the user typed it (/mem), not the agent.
	User bool
	// Req is the request's id.
	Req string
}

// memResult is what an input to mem did.
type memResult struct {
	Output string
	Ops    []memOp
	// Seq is the journal entry the input was kept as; 0 for a read.
	Seq int64
	// Notes say what klash did first: rebuilt the store, caught it up.
	Notes []string
}

// call runs one input against mem, under mem's lock, and keeps what it
// changed: first as a journal entry, which is the commit point, then in the
// store. A failed input keeps nothing.
func (h *memHost) call(ctx context.Context, q memRequest) (memResult, error) {
	release, err := h.lock(ctx)
	if err != nil {
		return memResult{}, err
	}
	defer release()
	r, j, notes, err := h.open()
	if err != nil {
		return memResult{Notes: notes}, err
	}
	defer r.close()
	who, caller := "klash", "klash"
	switch {
	case q.User:
		who, caller = "user", "user"
	case q.Session != "":
		who, caller = "agent", q.Session
	}
	call, err := r.call(q.Code, memContext{caller: caller})
	res := memResult{Output: call.Output, Ops: call.Ops, Notes: notes}
	if err != nil || call.batch == nil {
		return res, err
	}
	_, offset := time.Unix(call.At, 0).Zone()
	e := &memEntry{Seq: j.seq + 1, Time: call.At, Offset: offset, Req: q.Req, Who: who, Session: q.Session, Code: q.Code, Ops: call.Ops}
	if err := j.append(e); err != nil {
		res.Ops = nil
		return res, err
	}
	res.Seq = e.Seq
	// The entry is kept. If the store's write fails, the next call
	// catches the store up from the journal.
	return res, r.commit(call, h.stamp(j))
}

// lock takes mem's lock, waiting at most lockWait while another klash
// process holds it.
func (h *memHost) lock(ctx context.Context) (func(), error) {
	if err := mkdirSync(h.dir); err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(ctx, lockWait)
	defer cancel()
	path := filepath.Join(h.dir, "lock")
	for {
		release, err := lock.TryFile(path)
		if err == nil {
			return release, nil
		}
		if !errors.Is(err, lock.ErrContended) {
			return nil, err
		}
		select {
		case <-ctx.Done():
			return nil, &busyError{"mem is in use by another klash process; try again shortly"}
		case <-time.After(20 * time.Millisecond):
		}
	}
}

func (h *memHost) stamp(j *journal) memStamp {
	return memStamp{Layout: memLayout, Gno: stdlibVersion(), Generation: j.generation, Seq: j.seq, End: j.size, Hash: j.lastHash}
}

// open opens mem's store, under the lock, and brings it up to the journal,
// reading only the journal's header and the entries the store lacks. A
// store that is missing, of another layout, gno version or generation, or
// not built from this journal, is built afresh from the whole journal.
// Stores of other generations are deleted.
func (h *memHost) open() (r *memRealm, j *journal, notes []string, err error) {
	path := filepath.Join(h.dir, "journal.jsonl")
	hdr, line, err := readJournalHeader(path)
	if errors.Is(err, os.ErrNotExist) {
		if _, err = createMemJournal(path, h.now()); err != nil {
			return nil, nil, nil, err
		}
		hdr, line, err = readJournalHeader(path)
	}
	if err != nil {
		return nil, nil, nil, fmt.Errorf("mem's journal: %w", err)
	}
	if hdr.Kind != memJournalKind {
		return nil, nil, nil, fmt.Errorf("mem's journal: %w: a %q journal", errBrokenJournal, hdr.Kind)
	}
	dir := filepath.Join(h.dir, "store-"+hdr.Generation)
	if r, err = h.openStore(dir); err != nil {
		return nil, nil, nil, err
	}
	st, ok, err := r.stamp()
	if err == nil && ok {
		if st.Layout > memLayout {
			r.close()
			return nil, nil, nil, &refusal{"a newer klash wrote mem (layout " + strconv.Itoa(st.Layout) + "); restart klash to use it"}
		}
		same := st.Layout == memLayout && st.Gno == stdlibVersion() && st.Generation == hdr.Generation
		if same {
			same, err = lineEndsAt(path, st.End, st.Hash)
		}
		if err == nil && !same {
			r.close()
			if err = os.RemoveAll(dir); err == nil {
				r, err = h.openStore(dir)
			}
			if err != nil {
				return nil, nil, nil, err
			}
			ok = false
			notes = append(notes, "mem's store was rebuilt from its journal")
		}
	}
	if err != nil {
		r.close()
		return nil, nil, nil, err
	}
	j = &journal{path: path, generation: hdr.Generation, gno: hdr.Gno, instance: hdr.Instance, size: int64(len(line)), lastHash: lineHash(line)}
	if ok {
		j.seq, j.size, j.lastHash = st.Seq, st.End, st.Hash
	}
	n, err := h.catchUp(r, j)
	switch {
	case n == 0 || len(notes) > 0:
	case n == 1:
		notes = append(notes, "mem's store caught up with 1 journal entry")
	default:
		notes = append(notes, "mem's store caught up with "+strconv.Itoa(n)+" journal entries")
	}
	if err != nil {
		r.close()
		return nil, nil, notes, err
	}
	h.removeOtherStores(dir)
	return r, j, notes, nil
}

func (h *memHost) openStore(dir string) (*memRealm, error) {
	if err := mkdirSync(dir); err != nil {
		return nil, err
	}
	r, err := openMemRealm(h.b, dir)
	if err != nil {
		return nil, err
	}
	r.clock = h.now
	return r, nil
}

// errCallAgain stops a catch-up whose slice is spent.
var errCallAgain = errors.New("call again")

// readEntry is a journal entry read, with where its line ends and its hash:
// what a store holding it is stamped with.
type readEntry struct {
	memEntry
	end  int64
	hash string
}

// catchUp applies the journal's entries after j's end to r, many to a
// transaction, committing each batch with its stamp, so a catch-up cut
// short at any point resumes from the last batch kept. It stops when its
// slice is spent, saying to call again; it cuts a torn last line, the end
// of an append that did not finish. j ends where the store does. It returns
// how many entries it applied.
func (h *memHost) catchUp(r *memRealm, j *journal) (int, error) {
	deadline := time.Now().Add(h.slice)
	from := j.seq
	var batch []readEntry
	var ops []memOp
	var ats []int64
	applied := 0
	flush := func() error {
		if len(batch) == 0 {
			return nil
		}
		call, err := r.apply(ops, ats)
		if err == nil {
			err = r.commit(call, h.stampAt(j, batch[len(batch)-1]))
		} else {
			// Find the entry that does not fit, keeping those before it.
			err = h.applyEach(r, j, batch)
		}
		if err != nil {
			return err
		}
		e := batch[len(batch)-1]
		j.seq, j.size, j.lastHash = e.Seq, e.end, e.hash
		applied += len(batch)
		batch, ops, ats = batch[:0], ops[:0], ats[:0]
		return nil
	}
	torn, err := readEntriesOf(j.path, j.size, j.seq, func(e memEntry, end int64, hash string) error {
		batch = append(batch, readEntry{e, end, hash})
		for _, op := range e.Ops {
			ops = append(ops, op)
			ats = append(ats, e.Time)
		}
		if len(ops) < memBatchOps {
			return nil
		}
		if err := flush(); err != nil {
			return err
		}
		if time.Now().After(deadline) {
			return errCallAgain
		}
		return nil
	})
	if errors.Is(err, errCallAgain) {
		return applied, &busyError{fmt.Sprintf("mem's store is catching up with its journal (%d entries after entry %d applied); call again", applied, from)}
	}
	if err == nil {
		err = flush()
	}
	if err != nil {
		return applied, err
	}
	if torn {
		if err := j.cutTail(); err != nil {
			return applied, err
		}
	}
	return applied, nil
}

// stampAt is the stamp of a store holding the journal up to entry e.
func (h *memHost) stampAt(j *journal, e readEntry) memStamp {
	st := h.stamp(j)
	st.Seq, st.End, st.Hash = e.Seq, e.end, e.hash
	return st
}

// applyEach applies a batch's entries one at a time, keeping each, and
// fails at the first that does not fit, naming it.
func (h *memHost) applyEach(r *memRealm, j *journal, batch []readEntry) error {
	for _, e := range batch {
		ats := make([]int64, len(e.Ops))
		for i := range ats {
			ats[i] = e.Time
		}
		call, err := r.apply(e.Ops, ats)
		if err != nil {
			return fmt.Errorf("mem's journal entry %d does not apply to its store: %v; /mem repair rebuilds it", e.Seq, err)
		}
		if err := r.commit(call, h.stampAt(j, e)); err != nil {
			return err
		}
		j.seq, j.size, j.lastHash = e.Seq, e.end, e.hash
	}
	return nil
}

// removeOtherStores deletes the stores of other generations: what an
// erasure or a repair left behind.
func (h *memHost) removeOtherStores(keep string) {
	ds, _ := os.ReadDir(h.dir)
	for _, d := range ds {
		if p := filepath.Join(h.dir, d.Name()); d.IsDir() && strings.HasPrefix(d.Name(), "store-") && p != keep {
			_ = os.RemoveAll(p)
		}
	}
}
