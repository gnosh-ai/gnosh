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
	"log/slog"
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
	// Notes say what klash did besides: rebuilt the store, caught it up.
	Notes []string
	// Renamed is the goals an erasure renamed, old name to new.
	Renamed map[string]string
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
	x, err := h.openIndex(j)
	if err != nil {
		return memResult{Notes: notes}, err
	}
	defer x.close()
	who, caller := "klash", "klash"
	switch {
	case q.User:
		who, caller = "user", "user"
	case q.Session != "":
		who, caller = "agent", q.Session
	}
	call, err := r.call(q.Code, memContext{caller: caller, find: x.finder(), holding: h.holding(j)})
	res := memResult{Output: call.Output, Ops: call.Ops, Notes: notes}
	if err != nil || call.batch == nil {
		return res, err
	}
	_, offset := time.Unix(call.At, 0).Zone()
	e := &memEntry{Seq: j.seq + 1, Time: call.At, Offset: offset, Req: q.Req, Who: who, Session: q.Session, Code: q.Code, Ops: call.Ops}
	if er, ok := erasureOf(call.Ops); ok {
		r.close()
		x.close()
		return h.erase(res, j, e, er)
	}
	if err := j.append(e); err != nil {
		res.Ops = nil
		return res, err
	}
	res.Seq = e.Seq
	// The entry is kept. If the store's or the index's write fails, the
	// next call catches it up from the journal.
	if err := r.commit(call, h.stamp(j)); err != nil {
		return res, err
	}
	return res, x.apply([][]memOp{call.Ops}, h.indexStamp(j))
}

// erase keeps an input that redacts or forgets, e: it writes the journal
// again with what e erases gone from every entry, under a new generation,
// as the commit point; deletes the store and the index, which hold the
// text in their own files; and builds them afresh from the new journal, as
// far as the call's slice goes (the next call goes on).
func (h *memHost) erase(res memResult, j *journal, e *memEntry, er memErasure) (memResult, error) {
	_, entries, _, err := readMemJournal(j.path)
	if err != nil {
		res.Ops = nil
		return res, err
	}
	entries, res.Renamed = erase(append(entries, *e), er)
	nj, err := rewriteMemJournal(j.path, j.instance, entries, h.now())
	if err != nil {
		res.Ops = nil
		return res, err
	}
	res.Seq = e.Seq
	res.Notes = append(res.Notes, "mem's journal was written again without what was erased; its store and index are built afresh from it")
	h.removeOtherStores(filepath.Join(h.dir, "store-"+nj.generation))
	r, j, _, err := h.open()
	if err == nil {
		r.close()
		var x *memIndex
		if x, err = h.openIndex(j); err == nil {
			x.close()
		}
	}
	var busy *busyError
	if errors.As(err, &busy) {
		err = nil
	}
	return res, err
}

// holding answers the holding native from mem's journal: the names whose
// Ops ever held text (an item's, fact's or rule's text, or its name, as
// typed or, for a name, in a name's form), with their kinds, and whether the
// statements of a past input held it. ops are the call's own so far. It
// reads the whole journal, for a rare act.
func (h *memHost) holding(j *journal) func(text string, ops []memOp) (names, kinds []string, statements bool) {
	return func(text string, ops []memOp) (names, kinds []string, statements bool) {
		er := memErasure{secrets: []string{text}}
		seen := map[string]bool{}
		see := func(op memOp) {
			target := opTarget(op)
			name := op.Name
			if target == "statements" {
				return
			}
			held := er.holds(op.Old) || er.holds(op.New)
			if target == "item" {
				held = held || er.nameHolds(goalPart(name))
			} else {
				held = held || er.nameHolds(name)
			}
			if held && !seen[target+" "+name] {
				seen[target+" "+name] = true
				names, kinds = append(names, name), append(kinds, target)
			}
		}
		_, err := readEntriesOf(j.path, 0, 0, func(e memEntry, _ int64, _ string) error {
			for _, op := range e.Ops {
				see(op)
			}
			statements = statements || er.holds(e.Code)
			return nil
		})
		if err != nil {
			slog.Warn("mem's journal could not be searched", "error", err)
		}
		for _, op := range ops {
			see(op)
		}
		return names, kinds, statements
	}
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

// removeOtherStores deletes the stores and indexes of other generations:
// what an erasure or a repair left behind.
func (h *memHost) removeOtherStores(keep string) {
	gen := strings.TrimPrefix(filepath.Base(keep), "store-")
	ds, _ := os.ReadDir(h.dir)
	for _, d := range ds {
		name := d.Name()
		other := strings.HasPrefix(name, "store-") && name != "store-"+gen ||
			strings.HasPrefix(name, "index-") && !strings.HasPrefix(name, "index-"+gen+".db")
		if other {
			_ = os.RemoveAll(filepath.Join(h.dir, name))
		}
	}
}

// memIndexLayout is the layout of mem's index: a change to its schema or to
// how Ops become rows is a new number, and an index of another is rebuilt.
const memIndexLayout = 1

func (h *memHost) indexStamp(j *journal) memStamp {
	return memStamp{Layout: memIndexLayout, Generation: j.generation, Seq: j.seq, End: j.size, Hash: j.lastHash}
}

// openIndex opens the index of j's generation and brings it up to j, as
// open does the store: from the entries it lacks, or, if it is of another
// layout or not built from this journal, afresh from the whole journal.
func (h *memHost) openIndex(j *journal) (*memIndex, error) {
	path := filepath.Join(h.dir, "index-"+j.generation+".db")
	x, err := openMemIndex(path)
	if err != nil {
		return nil, err
	}
	st, ok, err := x.stamp()
	if err == nil && ok {
		same := st.Layout == memIndexLayout && st.Generation == j.generation
		if same {
			same, err = lineEndsAt(j.path, st.End, st.Hash)
		}
		if err == nil && !same {
			x.close()
			for _, suffix := range []string{"", "-wal", "-shm"} {
				_ = os.Remove(path + suffix)
			}
			if x, err = openMemIndex(path); err != nil {
				return nil, err
			}
			ok = false
		}
	}
	if err != nil {
		x.close()
		return nil, err
	}
	var off, seq int64
	if ok {
		off, seq = st.End, st.Seq
	}
	var batch [][]memOp
	var last memStamp
	n := 0
	_, err = readEntriesOf(j.path, off, seq, func(e memEntry, end int64, hash string) error {
		if e.Seq > j.seq {
			return nil // past what the store holds
		}
		batch = append(batch, e.Ops)
		n += len(e.Ops)
		last = memStamp{Layout: memIndexLayout, Generation: j.generation, Seq: e.Seq, End: end, Hash: hash}
		if n < memBatchOps {
			return nil
		}
		err := x.apply(batch, last)
		batch, n = nil, 0
		return err
	})
	if err == nil && len(batch) > 0 {
		err = x.apply(batch, last)
	}
	if err != nil {
		x.close()
		return nil, err
	}
	return x, nil
}
