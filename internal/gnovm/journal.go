// Copyright (C) 2026 Jae Kwon
//
// This file is part of klash's GnoVM machine subsystem. It is licensed under
// the GNO Network General Public License, version 6, or any later version
// that the Licensor has publicly accepted as the proxy under section 14 of
// that License, with the Additional Terms (Strong Attribution); see
// internal/gnovm/LICENSE.md.

package gnovm

import (
	"bytes"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// journalVersion is the journal format; the header line carries it.
const journalVersion = 1

// maxEntry is the largest journal line klash writes.
const maxEntry = 256 << 10

// journalHeader is a journal's first line. Generation is new whenever the
// journal is created or rewritten (repair, compaction), so a VM built from
// an older generation knows to rebuild rather than catch up.
type journalHeader struct {
	Version    int       `json:"klash_gnovm_journal"`
	Generation string    `json:"generation"`
	Created    time.Time `json:"created"`
	// Gno is the gno version the journal was written under; a replay
	// under another may not give the same state.
	Gno string `json:"gno,omitempty"`
}

// entry is one successful call: the ops it ran, in order.
type entry struct {
	Seq int64 `json:"seq"`
	// Req is the request's id, so a caller that lost the reply (its
	// worker was killed) can ask whether the call was kept.
	Req string `json:"req,omitempty"`
	// Time is when the call ran, in Unix nanoseconds; replay runs the
	// entry at the same time, so time.Now answers the same.
	Time int64 `json:"time"`
	// WallNS is how long the call took; replay budgets from it.
	WallNS int64 `json:"wall_ns"`
	// Gas is the gas the call used.
	Gas int64 `json:"gas"`
	Ops []op  `json:"ops"`
}

// journal is a VM's journal file as last read or written by this process:
// where it ends, and the hash of its last line, to tell whether another
// process changed it since.
type journal struct {
	path       string
	generation string
	gno        string
	size       int64
	lastHash   string
	seq        int64
}

func newGeneration() string {
	var b [8]byte
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
}

func lineHash(line []byte) string {
	sum := sha256.Sum256(line)
	return hex.EncodeToString(sum[:8])
}

// createJournal writes a new journal holding only its header, durably.
func createJournal(path string, now time.Time) (*journal, error) {
	hdr := journalHeader{Version: journalVersion, Generation: newGeneration(), Created: now.UTC(), Gno: stdlibVersion()}
	line, err := json.Marshal(hdr)
	if err != nil {
		return nil, err
	}
	line = append(line, '\n')
	if err := writeFileSync(path, line); err != nil {
		return nil, err
	}
	return &journal{path: path, generation: hdr.Generation, gno: hdr.Gno, size: int64(len(line)), lastHash: lineHash(line)}, nil
}

// errBrokenJournal marks a journal that cannot be read past some line.
var errBrokenJournal = errors.New("journal is broken")

// readJournal reads a whole journal. A torn last line (no newline, or not
// JSON) is reported as torn, to be cut by the caller under the VM's lock;
// any other bad line makes the journal broken.
func readJournal(path string) (j *journal, entries []entry, torn bool, err error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, nil, false, err
	}
	return parseJournal(path, data)
}

func parseJournal(path string, data []byte) (j *journal, entries []entry, torn bool, err error) {
	j = &journal{path: path}
	off := 0
	lineNo := 0
	for off < len(data) {
		nl := bytes.IndexByte(data[off:], '\n')
		if nl < 0 {
			// The last write did not finish.
			return j, entries, true, nil
		}
		line := data[off : off+nl+1]
		lineNo++
		if lineNo == 1 {
			var hdr journalHeader
			if err := json.Unmarshal(line, &hdr); err != nil || hdr.Generation == "" {
				return nil, nil, false, fmt.Errorf("%w: bad header", errBrokenJournal)
			}
			if hdr.Version != journalVersion {
				return nil, nil, false, fmt.Errorf("%w: format %d, want %d", errBrokenJournal, hdr.Version, journalVersion)
			}
			j.generation, j.gno = hdr.Generation, hdr.Gno
		} else {
			var e entry
			if err := json.Unmarshal(line, &e); err != nil {
				if off+nl+1 == len(data) {
					// A torn final line can end in a newline when the
					// write was cut mid-way and a later byte landed.
					return j, entries, true, nil
				}
				return nil, nil, false, fmt.Errorf("%w: line %d: %v", errBrokenJournal, lineNo, err)
			}
			if e.Seq != j.seq+1 {
				return nil, nil, false, fmt.Errorf("%w: line %d has seq %d after %d", errBrokenJournal, lineNo, e.Seq, j.seq)
			}
			j.seq = e.Seq
			entries = append(entries, e)
		}
		off += nl + 1
		j.size = int64(off)
		j.lastHash = lineHash(line)
	}
	if lineNo == 0 {
		return nil, nil, false, fmt.Errorf("%w: empty", errBrokenJournal)
	}
	return j, entries, false, nil
}

// cutTail truncates a journal to the end of its last whole line.
func (j *journal) cutTail() error {
	f, err := os.OpenFile(j.path, os.O_WRONLY, 0)
	if err != nil {
		return err
	}
	defer f.Close()
	if err := f.Truncate(j.size); err != nil {
		return err
	}
	return f.Sync()
}

// errSyncFailed means a journal write may or may not be on disk.
var errSyncFailed = errors.New("journal sync failed")

// append writes one entry durably. If the write fails, the journal is cut
// back to where it was; if the sync fails, nothing is known about what
// reached the disk, and the caller marks the VM broken.
func (j *journal) append(e *entry) error {
	line, err := json.Marshal(e)
	if err != nil {
		return err
	}
	if len(line) > maxEntry {
		return &refusal{fmt.Sprintf("this call would add %d bytes to the journal; the most one call may add is %d", len(line), maxEntry)}
	}
	line = append(line, '\n')
	f, err := os.OpenFile(j.path, os.O_WRONLY, 0)
	if err != nil {
		return err
	}
	defer f.Close()
	if _, err := f.WriteAt(line, j.size); err != nil {
		_ = f.Truncate(j.size)
		_ = f.Sync()
		return err
	}
	if err := f.Sync(); err != nil {
		return fmt.Errorf("%w: %v", errSyncFailed, err)
	}
	j.size += int64(len(line))
	j.lastHash = lineHash(line)
	j.seq = e.Seq
	return nil
}

// changed reports whether the journal on disk is no longer the one this
// process last saw end at j.size: another process appended to it, or
// rewrote it. It reads only the header and, if the size is unchanged, the
// last line.
func (j *journal) changed() (appended, rewritten bool, err error) {
	f, err := os.Open(j.path)
	if err != nil {
		return false, false, err
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return false, false, err
	}
	hdr := make([]byte, min(st.Size(), 4096))
	if _, err := f.ReadAt(hdr, 0); err != nil {
		return false, false, err
	}
	nl := bytes.IndexByte(hdr, '\n')
	if nl < 0 {
		return false, true, nil
	}
	var h journalHeader
	if json.Unmarshal(hdr[:nl+1], &h) != nil || h.Generation != j.generation {
		return false, true, nil
	}
	if st.Size() < j.size {
		return false, true, nil
	}
	// The line that ended at j.size must still be the one this process saw.
	tail := make([]byte, min(j.size, maxEntry+1))
	if _, err := f.ReadAt(tail, j.size-int64(len(tail))); err != nil {
		return false, false, err
	}
	start := bytes.LastIndexByte(tail[:len(tail)-1], '\n') + 1
	if lineHash(tail[start:]) != j.lastHash {
		return false, true, nil
	}
	return st.Size() > j.size, false, nil
}

// tailEntry is an entry past the end this process last saw, with where its
// line ends and the line's hash.
type tailEntry struct {
	entry
	end  int64
	hash string
}

// readFrom reads the entries after the end this process last saw: another
// process's appends, to catch up with. A torn tail is reported as torn.
func (j *journal) readFrom() (tail []tailEntry, torn bool, err error) {
	data, err := os.ReadFile(j.path)
	if err != nil {
		return nil, false, err
	}
	if int64(len(data)) < j.size {
		return nil, false, fmt.Errorf("%w: shrank", errBrokenJournal)
	}
	off := j.size
	seq := j.seq
	for off < int64(len(data)) {
		nl := bytes.IndexByte(data[off:], '\n')
		if nl < 0 {
			return tail, true, nil
		}
		end := off + int64(nl) + 1
		line := data[off:end]
		var e entry
		if err := json.Unmarshal(line, &e); err != nil {
			if end == int64(len(data)) {
				return tail, true, nil
			}
			return nil, false, fmt.Errorf("%w: %v", errBrokenJournal, err)
		}
		if e.Seq != seq+1 {
			return nil, false, fmt.Errorf("%w: seq %d after %d", errBrokenJournal, e.Seq, seq)
		}
		seq = e.Seq
		tail = append(tail, tailEntry{entry: e, end: end, hash: lineHash(line)})
		off = end
	}
	return tail, false, nil
}

// saw records a tail entry as seen.
func (j *journal) saw(t tailEntry) { j.size, j.lastHash, j.seq = t.end, t.hash, t.Seq }

// writeFileSync writes a file atomically and durably: a temporary file,
// synced, renamed over the target, then the directory synced.
func writeFileSync(path string, data []byte) error {
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, ".tmp-"+filepath.Base(path)+"-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmp.Name(), path); err != nil {
		return err
	}
	return syncDir(dir)
}

// mkdirSync makes a directory and its parents, syncing each parent it
// created into, so a crash cannot leave the directory unreachable.
func mkdirSync(dir string) error {
	if _, err := os.Stat(dir); err == nil {
		return nil
	}
	if err := mkdirSync(filepath.Dir(dir)); err != nil {
		return err
	}
	if err := os.Mkdir(dir, 0o700); err != nil && !os.IsExist(err) {
		return err
	}
	return syncDir(filepath.Dir(dir))
}

func syncDir(dir string) error {
	d, err := os.Open(dir)
	if err != nil {
		return err
	}
	defer d.Close()
	return d.Sync()
}
