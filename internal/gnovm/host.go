// Copyright (C) 2026 Jae Kwon
//
// This file is part of klash's GnoVM machine subsystem. It is licensed under
// the GNO Network General Public License, version 6, or any later version
// that the Licensor has publicly accepted as the proxy under section 14 of
// that License, with the Additional Terms (Strong Attribution); see
// internal/gnovm/LICENSE.md.

// Package gnovm runs named Gno virtual machines for klash: each with a
// memory cap and a gas limit per call, its state kept as a journal of the
// calls that succeeded, persisted on request, and evicted from memory when
// the budget needs the room. DESIGN.md has the design and its reviews.
package gnovm

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/charmbracelet/crush/internal/lock"
)

const (
	// GoalsVM is the VM that holds the harness's goals; only the goals
	// tool writes to it.
	GoalsVM = "goals"

	scratchPrefix = "scratch-"
	// maxNamedVMs is how many named VMs a project may have; scratch VMs
	// do not count.
	maxNamedVMs = 64
	// maxJournal is the largest journal a VM may grow to.
	maxJournal = 8 << 20

	defaultMemoryMB = 16
	maxMemoryMB     = 64
	defaultGas      = 1_000_000_000
	maxGas          = 100_000_000_000
)

var nameRe = regexp.MustCompile(`^[a-z][a-z0-9_-]{0,31}$`)

// Options configures a Host.
type Options struct {
	// Dir is the data directory, <XDG data>/klash/gnovm.
	Dir string
	// Client names the klash process the host serves, for its memory-only
	// VMs, which live until that process exits; NewClientID makes one.
	Client string
	// BudgetBytes caps the memory the VMs may use; 0 picks it from the
	// host's free memory.
	BudgetBytes int64
	// Defaults for new VMs; zero values mean the built-in defaults.
	DefaultMemoryMB int
	DefaultGas      int64
	NsPerGas        float64
	// Probe reads the host's memory; nil means ProbeHostMemory.
	Probe func() (HostMemory, error)
	// Now is the clock; nil means time.Now.
	Now func() time.Time
}

// NewClientID returns a fresh id for a klash process.
func NewClientID() string {
	var b [4]byte
	_, _ = rand.Read(b[:])
	return strconv.Itoa(os.Getpid()) + "-" + hex.EncodeToString(b[:])
}

// VMOptions are a VM's settings, kept in its machine.json.
type VMOptions struct {
	MemoryMB    int     `json:"memory_mb"`
	Gas         int64   `json:"gas"`
	NsPerGas    float64 `json:"ns_per_gas"`
	Persist     bool    `json:"persist"`
	Pinned      bool    `json:"pinned,omitempty"`
	Description string  `json:"description,omitempty"`
}

type machineFile struct {
	Name    string    `json:"name"`
	Created time.Time `json:"created"`
	VMOptions
}

// Host owns the VMs of one klash process. It runs one operation at a time:
// the GnoVM is single-threaded, and its stores are not safe to share.
type Host struct {
	mu      sync.Mutex
	opts    Options
	base    *base
	vms     map[string]*vmState // by project key + "/" + name
	clock   int64               // for least-recently-used eviction
	release func()              // drops the client lock, if this host holds it
	swept   map[string]bool     // project keys whose dead clients were swept
}

// vmState is what the host knows of one VM.
type vmState struct {
	key     string // project key + "/" + name
	project string // project key
	name    string
	dir     string
	opts    VMOptions
	created time.Time
	j       *journal
	res     *vm
	lastUse int64
	used    time.Time
	broken  error
	// lifetimeGas is the gas of the journal's entries.
	lifetimeGas int64
	// replayDecl and replayImports estimate what replaying the journal
	// keeps in memory (see charge).
	replayDecl    int64
	replayImports int
}

func (s *vmState) persistent() bool { return s.opts.Persist }

// NewHost makes a host. It takes the client's lock, unless the client
// process already holds it (a worker's client does), and sweeps the
// memory-only VMs of klash processes that are gone as it meets each
// project.
func NewHost(opts Options) (*Host, error) {
	if opts.Dir == "" || opts.Client == "" {
		return nil, errors.New("gnovm: Dir and Client are required")
	}
	if opts.DefaultMemoryMB <= 0 {
		opts.DefaultMemoryMB = defaultMemoryMB
	}
	opts.DefaultMemoryMB = min(opts.DefaultMemoryMB, maxMemoryMB)
	if opts.DefaultGas <= 0 {
		opts.DefaultGas = defaultGas
	}
	if opts.NsPerGas <= 0 {
		opts.NsPerGas = 1
	}
	if opts.Probe == nil {
		opts.Probe = ProbeHostMemory
	}
	if opts.Now == nil {
		opts.Now = time.Now
	}
	h := &Host{opts: opts, vms: map[string]*vmState{}, swept: map[string]bool{}}
	if err := mkdirSync(filepath.Join(opts.Dir, ".clients")); err != nil {
		return nil, err
	}
	release, err := lock.TryFile(h.clientLock(opts.Client))
	switch {
	case err == nil:
		h.release = release
	case !errors.Is(err, lock.ErrContended):
		return nil, err
	}
	return h, nil
}

func (h *Host) clientLock(client string) string {
	return filepath.Join(h.opts.Dir, ".clients", client+".lock")
}

// Close drops every VM from memory and deletes this client's memory-only
// VMs.
func (h *Host) Close() error {
	h.mu.Lock()
	defer h.mu.Unlock()
	for key, s := range h.vms {
		s.res = nil
		if !s.persistent() {
			_ = os.RemoveAll(s.dir)
		}
		delete(h.vms, key)
	}
	for project := range h.swept {
		_ = os.RemoveAll(h.ephemeralRoot(project))
	}
	if h.release != nil {
		h.release()
		_ = os.Remove(h.clientLock(h.opts.Client))
		h.release = nil
	}
	return nil
}

// projectKey names a project's directory: its path, made safe, and a hash of
// it, so two projects never share one.
func projectKey(project string) string {
	clean := filepath.Clean(project)
	sum := sha256.Sum256([]byte(clean))
	var b strings.Builder
	for _, r := range strings.Trim(clean, string(filepath.Separator)) {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '.', r == '-', r == '_':
			b.WriteRune(r)
		default:
			b.WriteByte('_')
		}
	}
	s := b.String()
	if len(s) > 48 {
		s = s[len(s)-48:]
	}
	return s + "-" + hex.EncodeToString(sum[:4])
}

// ScratchName is the name of a session's scratch VM.
func ScratchName(session string) string {
	sum := sha256.Sum256([]byte(session))
	return scratchPrefix + hex.EncodeToString(sum[:4])
}

func (h *Host) projectDir(project string) string { return filepath.Join(h.opts.Dir, project) }
func (h *Host) ephemeralRoot(project string) string {
	return filepath.Join(h.projectDir(project), ".ephemeral", h.opts.Client)
}

func (h *Host) persistDir(project, name string) string {
	return filepath.Join(h.projectDir(project), "vms", name)
}

func (h *Host) lockPath(project, name string) string {
	return filepath.Join(h.projectDir(project), "locks", name+".lock")
}

// sweep deletes the memory-only VMs of klash processes that are gone: their
// client lock is free or missing.
func (h *Host) sweep(project string) {
	if h.swept[project] {
		return
	}
	h.swept[project] = true
	root := filepath.Join(h.projectDir(project), ".ephemeral")
	entries, err := os.ReadDir(root)
	if err != nil {
		return
	}
	for _, e := range entries {
		client := e.Name()
		if client == h.opts.Client {
			continue
		}
		path := h.clientLock(client)
		if _, err := os.Stat(path); err == nil {
			release, err := lock.TryFile(path)
			if err != nil {
				continue // alive
			}
			_ = os.RemoveAll(filepath.Join(root, client))
			_ = os.Remove(path)
			release()
			continue
		}
		_ = os.RemoveAll(filepath.Join(root, client))
	}
}

// lookup finds a VM's state: one this host knows, a persistent one on disk,
// or this client's memory-only one on disk (from before a worker restart).
// It returns nil if there is none.
func (h *Host) lookup(project, name string) (*vmState, error) {
	key := project + "/" + name
	if s, ok := h.vms[key]; ok {
		return s, nil
	}
	h.sweep(project)
	for _, dir := range []string{h.persistDir(project, name), filepath.Join(h.ephemeralRoot(project), name)} {
		mf, err := readMachineFile(dir)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return nil, err
		}
		s := &vmState{key: key, project: project, name: name, dir: dir, opts: mf.VMOptions, created: mf.Created}
		h.vms[key] = s
		return s, nil
	}
	return nil, nil
}

func readMachineFile(dir string) (machineFile, error) {
	var mf machineFile
	b, err := os.ReadFile(filepath.Join(dir, "machine.json"))
	if err != nil {
		return mf, err
	}
	if err := json.Unmarshal(b, &mf); err != nil {
		return mf, fmt.Errorf("%s: %w", filepath.Join(dir, "machine.json"), err)
	}
	return mf, nil
}

func (s *vmState) writeMachineFile() error {
	b, err := json.MarshalIndent(machineFile{Name: s.name, Created: s.created, VMOptions: s.opts}, "", "  ")
	if err != nil {
		return err
	}
	return writeFileSync(filepath.Join(s.dir, "machine.json"), append(b, '\n'))
}

// create makes a VM on disk: its directory, machine.json and an empty
// journal.
func (h *Host) create(project, name string, opts VMOptions) (*vmState, error) {
	s := &vmState{key: project + "/" + name, project: project, name: name, opts: opts, created: h.opts.Now().UTC()}
	if opts.Persist {
		s.dir = h.persistDir(project, name)
	} else {
		s.dir = filepath.Join(h.ephemeralRoot(project), name)
	}
	if err := mkdirSync(s.dir); err != nil {
		return nil, err
	}
	j, err := createJournal(filepath.Join(s.dir, "journal.jsonl"), h.opts.Now())
	if err != nil {
		return nil, err
	}
	s.j = j
	if err := s.writeMachineFile(); err != nil {
		return nil, err
	}
	h.vms[s.key] = s
	return s, nil
}

// lockVM takes a persistent VM's cross-process lock, waiting while another
// klash process holds it, until ctx is done. Memory-only VMs belong to one
// klash process and need none.
func (h *Host) lockVM(ctx context.Context, s *vmState) (func(), error) {
	if !s.persistent() {
		return func() {}, nil
	}
	if err := mkdirSync(filepath.Dir(h.lockPath(s.project, s.name))); err != nil {
		return nil, err
	}
	path := h.lockPath(s.project, s.name)
	for {
		release, err := lock.TryFile(path)
		if err == nil {
			_ = os.WriteFile(path, []byte(strconv.Itoa(os.Getpid())+"\n"), 0o600)
			return release, nil
		}
		if !errors.Is(err, lock.ErrContended) {
			return nil, err
		}
		select {
		case <-ctx.Done():
			holder := "another klash process"
			if b, err := os.ReadFile(path); err == nil && len(strings.TrimSpace(string(b))) > 0 {
				holder = "klash process " + strings.TrimSpace(string(b))
			}
			return nil, &busyError{fmt.Sprintf("VM %s is in use by %s; try again shortly", s.name, holder)}
		case <-time.After(50 * time.Millisecond):
		}
	}
}

// busyError is a VM that cannot be used right now; trying again later may
// work.
type busyError struct{ msg string }

func (e *busyError) Error() string { return e.msg }

// sync brings this host's view of a VM up to date with its files, under the
// VM's lock: it reads the journal the first time, cuts a torn tail another
// process left, notices options another process changed, and catches the
// resident VM up with entries another process appended, or drops it if the
// journal was rewritten.
func (h *Host) sync(ctx context.Context, s *vmState) error {
	if s.persistent() {
		mf, err := readMachineFile(s.dir)
		if errors.Is(err, os.ErrNotExist) {
			// Deleted by another process.
			delete(h.vms, s.key)
			return &refusal{fmt.Sprintf("there is no VM named %s; it was deleted", s.name)}
		}
		if err != nil {
			return err
		}
		if mf.MemoryMB != s.opts.MemoryMB || !mf.Created.Equal(s.created) {
			// Its cap changed, or it was deleted and made again.
			s.res, s.j = nil, nil
		}
		s.opts, s.created = mf.VMOptions, mf.Created
	}
	if s.j == nil {
		j, entries, torn, err := readJournal(filepath.Join(s.dir, "journal.jsonl"))
		if err != nil {
			s.broken = err
			return err
		}
		if torn {
			if err := j.cutTail(); err != nil {
				return err
			}
		}
		s.j, s.broken = j, nil
		s.lifetimeGas, s.replayDecl, s.replayImports = journalStats(entries)
		s.res = nil
		return nil
	}
	if !s.persistent() {
		return nil
	}
	appended, rewritten, err := s.j.changed()
	if err != nil {
		return err
	}
	if rewritten {
		s.j, s.res = nil, nil
		return h.sync(ctx, s)
	}
	if !appended {
		return nil
	}
	tail, torn, err := s.j.readFrom()
	if err != nil {
		s.broken = err
		return err
	}
	for _, t := range tail {
		s.lifetimeGas += t.Gas
		if s.res != nil && s.res.seq == t.Seq-1 {
			if _, err := h.replayEntry(ctx, s, &t.entry, time.Time{}); err != nil {
				s.res = nil
			}
		}
		s.j.saw(t)
	}
	if torn {
		return s.j.cutTail()
	}
	return nil
}

// journalStats sums what a journal's entries cost: their gas, the source of
// their declarations and statements, and the packages they import.
func journalStats(entries []entry) (gas, decl int64, imports int) {
	paths := map[string]bool{}
	for _, e := range entries {
		gas += e.Gas
		for _, o := range e.Ops {
			decl += int64(len(o.File) + len(o.Stmts))
			if o.Import != nil {
				paths[o.Import.Path] = true
			}
		}
	}
	return gas, decl, len(paths)
}

// resident returns the VM in memory, rebuilt from its journal if it is not:
// admitted to the budget first, evicting others if need be, then replayed.
// A replay that runs past its budget keeps what it built, so the next call
// carries on from there.
func (h *Host) resident(ctx context.Context, s *vmState) (*vm, []string, error) {
	if s.broken != nil {
		return nil, nil, fmt.Errorf("VM %s is broken: %v", s.name, s.broken)
	}
	var notes []string
	if s.res != nil && s.res.unusable {
		s.res = nil
	}
	if s.res == nil {
		evicted, err := h.admit(s)
		if err != nil {
			return nil, nil, err
		}
		for _, name := range evicted {
			notes = append(notes, "evicted VM "+name+" from memory to make room (it is rebuilt when next used)")
		}
		s.res = newVM(h.base, int64(s.opts.MemoryMB)<<20)
	}
	if s.res.seq == s.j.seq {
		return s.res, notes, nil
	}
	_, entries, _, err := readJournal(s.j.path)
	if err != nil {
		return nil, notes, err
	}
	var todo []entry
	var budget time.Duration
	var paths []string
	for _, e := range entries {
		if e.Seq <= s.res.seq || e.Seq > s.j.seq {
			continue
		}
		todo = append(todo, e)
		budget += time.Duration(e.WallNS) * 3
		for _, o := range e.Ops {
			if o.Import != nil {
				paths = append(paths, o.Import.Path)
			}
		}
	}
	if _, err := h.base.ensure(paths); err != nil {
		s.broken = fmt.Errorf("replay: %w", err)
		s.res = nil
		return nil, notes, s.broken
	}
	start := time.Now()
	deadline := start.Add(budget + 2*time.Second)
	for i := range todo {
		if _, err := h.replayEntry(ctx, s, &todo[i], deadline); err != nil {
			var lim *limitError
			if errors.As(err, &lim) && (lim.kind == "wall" || lim.kind == "cancelled") {
				// Keep what was built; the next call resumes. The
				// entry that was cut short left the VM unusable,
				// so rebuild from the last entry that finished.
				done := s.res.seq
				s.res = nil
				return nil, notes, &busyError{fmt.Sprintf(
					"rebuilding VM %s from its journal took longer than its budget (%d of %d entries done); call again to continue",
					s.name, done, len(todo))}
			}
			s.broken = fmt.Errorf("replaying entry %d: %v", todo[i].Seq, err)
			s.res = nil
			return nil, notes, s.broken
		}
	}
	if len(todo) > 0 {
		notes = append(notes, fmt.Sprintf("rebuilt VM %s from %d journal entries in %s", s.name, len(todo), time.Since(start).Round(time.Millisecond)))
	}
	return s.res, notes, nil
}

// replayEntry applies one journaled entry to the resident VM, without a gas
// limit: it ran within one when it was journaled, and its meaning must not
// change.
func (h *Host) replayEntry(ctx context.Context, s *vmState, e *entry, deadline time.Time) (int64, error) {
	_, used, err := s.res.apply(ctx, e, math.MaxInt64/4, deadline, false)
	if err != nil {
		return used, err
	}
	s.res.seq = e.Seq
	return used, nil
}

func (h *Host) touch(s *vmState) {
	h.clock++
	s.lastUse = h.clock
	s.used = h.opts.Now()
}

func (h *Host) ensureBase() {
	if h.base == nil {
		h.base = newBase()
	}
}

// wallLimit is a call's wall-clock limit: three times what its gas should
// take at the VM's ns per gas, at least 5 s and at most 10 min.
func wallLimit(gas int64, nsPerGas float64) time.Duration {
	d := time.Duration(3 * float64(gas) * nsPerGas)
	return min(max(d, 5*time.Second), 10*time.Minute)
}

// RunRequest is one gnovm_run call.
type RunRequest struct {
	// Project is the project's directory; VMs belong to a project.
	Project string
	// Session is the calling session's id, for its scratch VM.
	Session string
	// Name is the VM; "" means the session's scratch VM, or, for code
	// with a package clause, a fresh VM for just this call.
	Name string
	Code string
	// Gas is the call's gas limit; 0 means the VM's.
	Gas int64
	// Reset starts a memory-only VM over before running.
	Reset bool
	// Req identifies the call, so its outcome can be asked about later.
	Req string
}

// RunResult is what a call did.
type RunResult struct {
	VM        string
	Value     string
	Output    string
	Gas       int64
	GasLimit  int64
	NsPerGas  float64
	Wall      time.Duration
	MemUsed   int64
	MemCap    int64
	Journaled bool
	Seq       int64
	// Program is set when the code ran as a program in a fresh VM that
	// was thrown away after.
	Program bool
	Notes   []string
	// Err is the run's failure; nothing from the call was kept.
	Err error
}

// Run runs code in a VM. The error is for calls that could not be run at
// all (a refusal, a busy or broken VM); a failure of the code itself is in
// RunResult.Err.
func (h *Host) Run(ctx context.Context, req RunRequest) (*RunResult, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.ensureBase()
	project := projectKey(req.Project)
	if req.Gas < 0 || req.Gas > maxGas {
		return nil, &refusal{fmt.Sprintf("gas must be between 1 and %d", int64(maxGas))}
	}

	name := req.Name
	scratch := name == ""
	if scratch {
		prog, err := normalize(req.Code, 1)
		if err != nil {
			return nil, err
		}
		if prog.pkgClause {
			return h.runProgram(ctx, req)
		}
		name = ScratchName(req.Session)
	} else if name == GoalsVM {
		return nil, &refusal{"the goals VM is written only by the goals tool; use goals and goals_list"}
	}

	s, err := h.lookup(project, name)
	if err != nil {
		return nil, err
	}
	var notes []string
	switch {
	case s == nil && scratch:
		s, err = h.create(project, name, h.defaultOptions())
		if err != nil {
			return nil, err
		}
		notes = append(notes, "new scratch VM "+name+": memory only, it ends with this klash process; name a VM with gnovm_new to keep state")
	case s == nil:
		return nil, &refusal{fmt.Sprintf("there is no VM named %s; make it with gnovm_new, or leave out name to use your scratch VM", name)}
	case req.Reset && s.persistent():
		return nil, &refusal{fmt.Sprintf("VM %s is persistent, so reset would destroy saved state; delete it with gnovm_drop first", name)}
	case req.Reset:
		opts := s.opts
		if err := h.deleteVM(s); err != nil {
			return nil, err
		}
		s, err = h.create(project, name, opts)
		if err != nil {
			return nil, err
		}
		notes = append(notes, "reset VM "+name)
	}
	return h.runIn(ctx, s, req, notes)
}

// runIn runs a call in an existing VM, under its lock.
func (h *Host) runIn(ctx context.Context, s *vmState, req RunRequest, notes []string) (*RunResult, error) {
	gas := req.Gas
	if gas == 0 {
		gas = s.opts.Gas
	}
	wall := wallLimit(gas, s.opts.NsPerGas)
	lockCtx, cancel := context.WithTimeout(ctx, wall)
	release, err := h.lockVM(lockCtx, s)
	cancel()
	if err != nil {
		return nil, err
	}
	defer release()
	if err := h.sync(ctx, s); err != nil {
		return nil, err
	}
	if s.j.size >= maxJournal {
		return nil, &refusal{fmt.Sprintf("VM %s's journal is full (%d MB); make a new VM, or delete this one", s.name, maxJournal>>20)}
	}
	prog, err := normalize(req.Code, s.j.seq+1)
	if err != nil {
		return nil, err
	}
	v, rnotes, err := h.resident(ctx, s)
	notes = append(notes, rnotes...)
	if err != nil {
		return nil, err
	}
	h.touch(s)
	ops, err := h.importOps(v, prog)
	if err != nil {
		return nil, err
	}
	res := &RunResult{VM: s.name, GasLimit: gas, NsPerGas: s.opts.NsPerGas, Notes: notes}
	e := &entry{Seq: s.j.seq + 1, Req: req.Req, Time: h.opts.Now().UnixNano(), Ops: ops}
	start := time.Now()
	value, used, runErr := v.apply(ctx, e, gas, start.Add(wall), true)
	res.Wall = time.Since(start)
	res.Gas, res.Value, res.Output = used, value, v.out.String()
	res.MemUsed, res.MemCap = v.memory()
	if runErr != nil {
		// Nothing is journaled, and a VM that ran code is rebuilt.
		if v.unusable {
			s.res = nil
		}
		var ref *refusal
		var perr *parseError
		if errors.As(runErr, &ref) || errors.As(runErr, &perr) {
			return nil, runErr
		}
		res.Err = runErr
		return res, nil
	}
	e.WallNS, e.Gas = int64(res.Wall), used
	e.Ops = journaledOps(ops, prog.evalPure)
	if len(e.Ops) == 0 {
		return res, nil
	}
	if err := s.j.append(e); err != nil {
		// The VM has state its journal lacks: rebuild it.
		s.res = nil
		if errors.Is(err, errSyncFailed) {
			s.broken = err
		}
		res.Err = fmt.Errorf("the call ran, but saving it failed, so it was not kept: %w", err)
		return res, nil
	}
	v.seq = e.Seq
	s.lifetimeGas += used
	for _, o := range e.Ops {
		s.replayDecl += int64(len(o.File) + len(o.Stmts))
	}
	s.replayImports = len(v.imports)
	res.Journaled, res.Seq = true, e.Seq
	return res, nil
}

// importOps prepends the input's imports to its ops, as import ops bound
// package-wide, loading each package into the base first. An import the VM
// already has is left out.
func (h *Host) importOps(v *vm, prog *program) ([]op, error) {
	paths := make([]string, 0, len(prog.imports))
	for _, spec := range prog.imports {
		paths = append(paths, spec.Path)
	}
	names, err := h.base.ensure(paths)
	if err != nil {
		return nil, &refusal{err.Error()}
	}
	var ops []op
	seen := map[string]bool{}
	for _, spec := range prog.imports {
		if spec.Name == "_" {
			continue
		}
		name := spec.Name
		if name == "" {
			name = names[spec.Path]
		}
		if v.imports[name] == spec.Path || seen[name+" "+spec.Path] {
			continue
		}
		seen[name+" "+spec.Path] = true
		ops = append(ops, op{Import: &importOp{Name: name, Path: spec.Path}})
	}
	return append(ops, prog.ops...), nil
}

// journaledOps drops a trailing eval that cannot change state: replaying it
// would change nothing.
func journaledOps(ops []op, evalPure bool) []op {
	if n := len(ops); n > 0 && ops[n-1].Eval != "" && evalPure {
		return ops[:n-1]
	}
	return ops
}

// runProgram runs code that has a package clause in a fresh VM that is
// thrown away after, as `go run` would: a program is a whole, and running
// it again after a fix must not meet its own earlier declarations.
func (h *Host) runProgram(ctx context.Context, req RunRequest) (*RunResult, error) {
	opts := h.defaultOptions()
	gas := req.Gas
	if gas == 0 {
		gas = opts.Gas
	}
	s := &vmState{name: "program", opts: opts}
	if _, err := h.admit(s); err != nil {
		return nil, err
	}
	prog, err := normalize(req.Code, 1)
	if err != nil {
		return nil, err
	}
	v := newVM(h.base, int64(opts.MemoryMB)<<20)
	ops, err := h.importOps(v, prog)
	if err != nil {
		return nil, err
	}
	wall := wallLimit(gas, opts.NsPerGas)
	e := &entry{Seq: 1, Time: h.opts.Now().UnixNano(), Ops: ops}
	start := time.Now()
	value, used, runErr := v.apply(ctx, e, gas, start.Add(wall), true)
	res := &RunResult{VM: "(program)", Program: true, GasLimit: gas, NsPerGas: opts.NsPerGas, Wall: time.Since(start)}
	res.Gas, res.Value, res.Output = used, value, v.out.String()
	res.MemUsed, res.MemCap = v.memory()
	var ref *refusal
	var perr *parseError
	if errors.As(runErr, &ref) || errors.As(runErr, &perr) {
		return nil, runErr
	}
	res.Err = runErr
	return res, nil
}

func (h *Host) defaultOptions() VMOptions {
	return VMOptions{MemoryMB: h.opts.DefaultMemoryMB, Gas: h.opts.DefaultGas, NsPerGas: h.opts.NsPerGas}
}

// NewRequest makes a VM, or changes one. Nil fields are left as they are, or
// take the defaults for a new VM.
type NewRequest struct {
	Project     string
	Name        string
	MemoryMB    *int
	Gas         *int64
	NsPerGas    *float64
	Persist     *bool
	Pinned      *bool
	Description *string
}

// VMInfo describes a VM.
type VMInfo struct {
	Name        string
	Options     VMOptions
	Scratch     bool
	Resident    bool
	MemUsed     int64
	Charge      int64
	Entries     int64
	LifetimeGas int64
	JournalSize int64
	LastUsed    time.Time
	Broken      string
}

// New makes a VM, or changes the settings of one: memory_mb may only grow
// (a smaller cap could not rebuild state that fit the larger one), and a VM
// may become persistent but not stop being so.
func (h *Host) New(ctx context.Context, req NewRequest) (VMInfo, bool, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if !nameRe.MatchString(req.Name) {
		return VMInfo{}, false, &refusal{"a VM name is a lowercase letter, then up to 31 lowercase letters, digits, - or _"}
	}
	if req.Name == GoalsVM || strings.HasPrefix(req.Name, scratchPrefix) {
		return VMInfo{}, false, &refusal{fmt.Sprintf("the name %s is reserved", req.Name)}
	}
	if req.MemoryMB != nil && (*req.MemoryMB < 1 || *req.MemoryMB > maxMemoryMB) {
		return VMInfo{}, false, &refusal{fmt.Sprintf("memory_mb must be between 1 and %d", maxMemoryMB)}
	}
	if req.Gas != nil && (*req.Gas < 1 || *req.Gas > maxGas) {
		return VMInfo{}, false, &refusal{fmt.Sprintf("gas must be between 1 and %d", int64(maxGas))}
	}
	if req.NsPerGas != nil && (*req.NsPerGas < 0.1 || *req.NsPerGas > 100) {
		return VMInfo{}, false, &refusal{"ns_per_gas must be between 0.1 and 100"}
	}
	project := projectKey(req.Project)
	s, err := h.lookup(project, req.Name)
	if err != nil {
		return VMInfo{}, false, err
	}
	if s == nil {
		if n, err := h.countNamed(project); err != nil {
			return VMInfo{}, false, err
		} else if n >= maxNamedVMs {
			return VMInfo{}, false, &refusal{fmt.Sprintf("this project has %d VMs, the most it may have; delete one with gnovm_drop", n)}
		}
		opts := h.defaultOptions()
		applyOptions(&opts, req)
		s, err = h.create(project, req.Name, opts)
		if err != nil {
			return VMInfo{}, false, err
		}
		return h.info(s), true, nil
	}

	lockCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	release, err := h.lockVM(lockCtx, s)
	cancel()
	if err != nil {
		return VMInfo{}, false, err
	}
	defer release()
	if err := h.sync(ctx, s); err != nil {
		return VMInfo{}, false, err
	}
	opts := s.opts
	applyOptions(&opts, req)
	if opts.MemoryMB < s.opts.MemoryMB {
		return VMInfo{}, false, &refusal{fmt.Sprintf("memory_mb can only grow (VM %s has %d); for less, make a new VM", s.name, s.opts.MemoryMB)}
	}
	if s.opts.Persist && !opts.Persist {
		return VMInfo{}, false, &refusal{fmt.Sprintf("VM %s is persistent; to discard its saved state, delete it with gnovm_drop", s.name)}
	}
	if opts == s.opts {
		return h.info(s), false, nil
	}
	if opts.Persist && !s.opts.Persist {
		if err := h.persist(s); err != nil {
			return VMInfo{}, false, err
		}
	}
	if opts.MemoryMB != s.opts.MemoryMB {
		// The allocator's cap is fixed when it is made: rebuild.
		s.res = nil
	}
	s.opts = opts
	if err := s.writeMachineFile(); err != nil {
		return VMInfo{}, false, err
	}
	return h.info(s), false, nil
}

func applyOptions(opts *VMOptions, req NewRequest) {
	if req.MemoryMB != nil {
		opts.MemoryMB = *req.MemoryMB
	}
	if req.Gas != nil {
		opts.Gas = *req.Gas
	}
	if req.NsPerGas != nil {
		opts.NsPerGas = *req.NsPerGas
	}
	if req.Persist != nil {
		opts.Persist = *req.Persist
	}
	if req.Pinned != nil {
		opts.Pinned = *req.Pinned
	}
	if req.Description != nil {
		opts.Description = truncate(*req.Description, 200)
	}
}

// persist moves a memory-only VM's files to the project's persistent VMs.
func (h *Host) persist(s *vmState) error {
	dst := h.persistDir(s.project, s.name)
	if _, err := os.Stat(dst); err == nil {
		return &refusal{fmt.Sprintf("another klash process has a persistent VM named %s; pick another name", s.name)}
	}
	if err := mkdirSync(filepath.Dir(dst)); err != nil {
		return err
	}
	if err := os.Rename(s.dir, dst); err != nil {
		return err
	}
	if err := syncDir(filepath.Dir(dst)); err != nil {
		return err
	}
	s.dir = dst
	if s.j != nil {
		s.j.path = filepath.Join(dst, "journal.jsonl")
	}
	return nil
}

// countNamed counts a project's named VMs: persistent ones on disk and this
// client's memory-only ones.
func (h *Host) countNamed(project string) (int, error) {
	names := map[string]bool{}
	for _, dir := range []string{filepath.Join(h.projectDir(project), "vms"), h.ephemeralRoot(project)} {
		entries, err := os.ReadDir(dir)
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			return 0, err
		}
		for _, e := range entries {
			if e.IsDir() && !strings.HasPrefix(e.Name(), scratchPrefix) && e.Name() != GoalsVM {
				names[e.Name()] = true
			}
		}
	}
	return len(names), nil
}

// Drop evicts a VM from memory (action "evict"), or deletes it ("delete").
// The goals VM is not deleted here.
func (h *Host) Drop(ctx context.Context, projectPath, name, action string) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	project := projectKey(projectPath)
	s, err := h.lookup(project, name)
	if err != nil {
		return err
	}
	if s == nil {
		return &refusal{fmt.Sprintf("there is no VM named %s", name)}
	}
	switch action {
	case "evict":
		s.res = nil
		return nil
	case "delete":
		if name == GoalsVM {
			return &refusal{"the goals VM is not deleted with gnovm_drop; close goals with the goals tool"}
		}
		lockCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
		release, err := h.lockVM(lockCtx, s)
		cancel()
		if err != nil {
			return err
		}
		defer release()
		return h.deleteVM(s)
	}
	return &refusal{fmt.Sprintf("action must be evict or delete, not %q", action)}
}

// deleteVM deletes a VM's files: a persistent VM's go to the project's trash
// (its lock stays where it is, outside), a memory-only VM's are removed.
func (h *Host) deleteVM(s *vmState) error {
	delete(h.vms, s.key)
	s.res = nil
	if !s.persistent() {
		return os.RemoveAll(s.dir)
	}
	trash := filepath.Join(h.projectDir(s.project), ".trash")
	if err := mkdirSync(trash); err != nil {
		return err
	}
	dst := filepath.Join(trash, fmt.Sprintf("%s-%s", s.name, h.opts.Now().UTC().Format("20060102T150405.000000000")))
	if err := os.Rename(s.dir, dst); err != nil {
		return err
	}
	return syncDir(filepath.Dir(s.dir))
}

// ListResult is the state of the host and of a project's VMs.
type ListResult struct {
	Memory    HostMemory
	ProbeErr  string
	Budget    int64
	Used      int64
	BaseBytes int64
	VMs       []VMInfo
}

// List describes the host's memory and a project's VMs: its persistent ones
// on disk, and this client's memory-only ones.
func (h *Host) List(projectPath string) (ListResult, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	project := projectKey(projectPath)
	h.sweep(project)
	var out ListResult
	mem, err := h.opts.Probe()
	if err != nil {
		out.ProbeErr = err.Error()
	}
	out.Memory = mem
	out.Budget = h.budgetFor(mem, err)
	out.Used = h.usedBytes()
	if h.base != nil {
		out.BaseBytes = h.base.heap
	}
	seen := map[string]bool{}
	for _, dir := range []string{filepath.Join(h.projectDir(project), "vms"), h.ephemeralRoot(project)} {
		entries, err := os.ReadDir(dir)
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			return out, err
		}
		for _, e := range entries {
			if !e.IsDir() || seen[e.Name()] {
				continue
			}
			seen[e.Name()] = true
			s, err := h.lookup(project, e.Name())
			if err != nil || s == nil {
				continue
			}
			if s.j == nil && s.broken == nil {
				if j, entries, _, err := readJournal(filepath.Join(s.dir, "journal.jsonl")); err == nil {
					s.j = j
					s.lifetimeGas, s.replayDecl, s.replayImports = journalStats(entries)
				} else {
					s.broken = err
				}
			}
			out.VMs = append(out.VMs, h.info(s))
		}
	}
	sort.Slice(out.VMs, func(i, j int) bool { return out.VMs[i].Name < out.VMs[j].Name })
	return out, nil
}

func (h *Host) info(s *vmState) VMInfo {
	in := VMInfo{
		Name:        s.name,
		Options:     s.opts,
		Scratch:     strings.HasPrefix(s.name, scratchPrefix),
		Resident:    s.res != nil,
		LifetimeGas: s.lifetimeGas,
		LastUsed:    s.used,
		Charge:      h.charge(s),
	}
	if s.res != nil {
		in.MemUsed, _ = s.res.memory()
	}
	if s.j != nil {
		in.Entries, in.JournalSize = s.j.seq, s.j.size
	}
	if s.broken != nil {
		in.Broken = s.broken.Error()
	}
	return in
}

// Kept reports whether the call with request id req was journaled in a VM,
// for a caller that lost the reply.
func (h *Host) Kept(projectPath, name, req string) (bool, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	s, err := h.lookup(projectKey(projectPath), name)
	if err != nil || s == nil {
		return false, err
	}
	_, entries, _, err := readJournal(filepath.Join(s.dir, "journal.jsonl"))
	if err != nil {
		return false, err
	}
	for i := len(entries) - 1; i >= 0; i-- {
		if entries[i].Req == req {
			return true, nil
		}
	}
	return false, nil
}
