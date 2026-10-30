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
	"bytes"
	"errors"
	"fmt"
	"strings"
	"time"

	gno "github.com/gnolang/gno/gnovm/pkg/gnolang"
	"github.com/gnolang/gno/gnovm/pkg/gnomod"
	"github.com/gnolang/gno/tm2/pkg/crypto"
	"github.com/gnolang/gno/tm2/pkg/db/goleveldb"
	"github.com/gnolang/gno/tm2/pkg/std"
	"github.com/gnolang/gno/tm2/pkg/store/cache"
	"github.com/gnolang/gno/tm2/pkg/store/dbadapter"
	storetypes "github.com/gnolang/gno/tm2/pkg/store/types"
)

// memRealmPath is the realm that holds a project's mem: one *gnomem.Mem,
// made when the realm is deployed into a new store.
const memRealmPath = "klash.local/r/mem"

const memRealmSource = `package mem

import gnomem "klash/gnomem/v2"

var M = gnomem.New()
`

// memLibPath is the library the realm's Mem comes from, whose natives
// (memnative.go) answer only inside a mem call.
const memLibPath = "klash/gnomem/v2"

// memLayout is the layout of a mem's store. gnomem v2's types.gno and
// apply.gno, bptree v0 and the realm's source fix how a Mem is stored, and
// TestMemLayoutPinned pins their hash to this number: changing them means a
// new number, and a store of an older one is rebuilt from its journal.
const memLayout = 1

// memLayoutFiles are the library sources memLayout covers.
var memLayoutFiles = []string{
	"klashlibs/klash/gnomem/v2/types.gno",
	"klashlibs/klash/gnomem/v2/apply.gno",
	"klashlibs/klash/bptree/v0/node.gno",
	"klashlibs/klash/bptree/v0/tree.gno",
}

// memRunAddr is the address mem inputs run as. gno gives a realm only to a
// script at the address's run path, as it does for a chain's MsgRun.
var memRunAddr = crypto.AddressFromPreimage([]byte("klash mem"))

// memRealm is a project's mem: the realm in a goleveldb store on disk. The
// libraries it runs on stay in the base, in memory, and are never written
// to the store: a klash upgrade brings new library code.
type memRealm struct {
	b  *base
	db *goleveldb.GoLevelDB
	kv storetypes.CommitStore
	// clock is the time a call runs at: its Ops' time, and the user's
	// local date for days.
	clock func() time.Time
}

// openMemRealm opens the mem in dir, deploying the realm into a new store.
func openMemRealm(b *base, dir string) (*memRealm, error) {
	if _, err := b.ensure([]string{memLibPath}); err != nil {
		return nil, err
	}
	// A mem call's transaction reads the libraries' objects and types from
	// gno's shared byte cache, not from the store it writes to.
	b.store.PopulateStdlibCache(libraryPaths())
	db, err := goleveldb.NewGoLevelDB("mem", dir)
	if err != nil {
		return nil, err
	}
	r := &memRealm{b: b, db: db, kv: dbadapter.StoreConstructor(db, storetypes.StoreOptions{}), clock: time.Now}
	if err := r.deploy(); err != nil {
		db.Close()
		return nil, err
	}
	if err := r.b.preprocessMemRealm(); err != nil {
		db.Close()
		return nil, err
	}
	return r, nil
}

func (r *memRealm) close() error { return r.db.Close() }

// deploy runs the realm's package into the store when it is not there yet.
func (r *memRealm) deploy() (err error) {
	kv := cache.New(r.kv)
	tx := r.b.store.BeginTransaction(kv, kv, nil, nil)
	tx.SetPackageGetter(nil)
	if tx.GetPackage(memRealmPath, false) != nil {
		return nil
	}
	defer func() {
		if p := recover(); p != nil {
			err = fmt.Errorf("deploying mem's realm: %v", p)
		}
	}()
	m := gno.NewMachineWithOptions(gno.MachineOptions{
		Store:   tx,
		Context: memContext{ExecContext: execContext(0, time.Now()), caller: "klash"},
	})
	defer m.Release()
	m.RunMemPackage(memRealmMemPackage(), true)
	kv.Write()
	return nil
}

// preprocessMemRealm puts the realm's block nodes in the base, once: gno
// keeps a package's syntax trees in memory only, so every process rebuilds
// them, as a chain node does at restart, from klash's own copy of the source.
func (b *base) preprocessMemRealm() (err error) {
	if b.memRealmReady {
		return nil
	}
	defer func() {
		if p := recover(); p != nil {
			err = fmt.Errorf("preprocessing mem's realm: %v", p)
		}
	}()
	m := gno.NewMachineWithOptions(gno.MachineOptions{Store: b.store})
	defer m.Release()
	mpkg := memRealmMemPackage()
	fset := m.ParseMemPackage(mpkg)
	pn := gno.NewPackageNode(gno.Name(mpkg.Name), mpkg.Path, fset)
	b.store.SetBlockNode(pn)
	gno.PredefineFileSet(b.store, pn, fset)
	for _, fn := range fset.Files {
		fn = gno.Preprocess(b.store, pn, fn).(*gno.FileNode)
		gno.SaveBlockNodes(b.store, fn)
	}
	if pn.FileSet == nil {
		pn.FileSet = fset
	}
	b.memRealmReady = true
	return nil
}

// memCall is what one mem input did.
type memCall struct {
	Output string
	Ops    []memOp
	// At is the call's time, which its Ops share.
	At int64
}

// run runs code as the body of a mem input's main, by caller, and commits
// what it changed in one batch. A failed input keeps nothing, and an input
// that made no Ops is a read: it must have written nothing either, since
// only gnomem's do changes mem.
func (r *memRealm) run(code, caller string, imports ...string) (memCall, error) {
	return r.call(code, memContext{caller: caller}, imports...)
}

// apply applies ops, made at time at, through gnomem's Apply, which records
// nothing: they are the journal's already.
func (r *memRealm) apply(ops []memOp, at int64) error {
	_, err := r.call("mem.Apply()", memContext{caller: "klash", pending: ops, at: at})
	return err
}

func (r *memRealm) call(code string, ctx memContext, imports ...string) (call memCall, err error) {
	kv := cache.New(r.kv)
	wc := &writeCounter{Store: kv}
	tx := r.b.store.BeginTransaction(wc, wc, nil, nil)
	tx.SetPackageGetter(nil)
	var out bytes.Buffer
	now := r.clock()
	call.At = now.Unix()
	ctx.ExecContext = execContext(0, now)
	ctx.OriginCaller = memRunAddr.Bech32()
	ctx.ops = &call.Ops
	if ctx.today == "" {
		ctx.today = now.Format(time.DateOnly)
	}
	defer func() {
		call.Output = out.String()
		if p := recover(); p != nil {
			// What the input printed before it failed may read as done;
			// say plainly that none of it was kept.
			msg := memPanicText(p)
			if len(call.Ops) > 0 {
				msg += "; nothing was kept"
			}
			call.Ops = nil
			err = errors.New(msg)
		}
	}()
	opts := gno.MachineOptions{Output: &out, Store: tx, Context: ctx, BoundedPanicRender: true}
	m := gno.NewMachineWithOptions(opts)
	defer m.Release()
	_, pv := m.RunMemPackage(memInput(code, imports...), false)
	m2 := gno.NewMachineWithOptions(opts)
	defer m2.Release()
	m2.SetActivePackage(pv)
	m2.RunMain()
	if len(call.Ops) == 0 && ctx.pending == nil {
		if wc.writes > 0 {
			return call, fmt.Errorf("gnomem: an input that made no Ops wrote %d keys", wc.writes)
		}
		return call, nil
	}
	kv.Write()
	return call, nil
}

// memPanicText is a failed mem input's refusal as the model reads it: gno's
// panic text, without its wrapping, on one line.
func memPanicText(p any) string {
	msg := fmt.Sprint(p)
	for _, prefix := range []string{"unhandled panic: ", "panic: "} {
		msg = strings.TrimPrefix(msg, prefix)
	}
	if i := strings.Index(msg, "\n"); i >= 0 {
		msg = msg[:i]
	}
	return msg
}

// writeCounter counts the writes a call makes to its store.
type writeCounter struct {
	storetypes.Store
	writes int
}

func (w *writeCounter) Set(gctx *storetypes.GasContext, key, value []byte) {
	w.writes++
	w.Store.Set(gctx, key, value)
}

func (w *writeCounter) Delete(gctx *storetypes.GasContext, key []byte) {
	w.writes++
	w.Store.Delete(gctx, key)
}

// memInput is a mem input as gno.land runs a MsgRun: a private package at
// the run path, whose main is code, with mem bound to the realm's Mem.
// imports are import specs ("strings", `gnomem "klash/gnomem/v2"`).
func memInput(code string, imports ...string) *std.MemPackage {
	path := "klash.local/e/" + memRunAddr.String() + "/run"
	mod := &gnomod.File{Module: path, Gno: gno.GnoVerLatest, Private: true}
	head := "package main\n\n"
	for _, imp := range imports {
		head += "import " + imp + "\n"
	}
	mpkg := &std.MemPackage{
		Type: gno.MPUserProd,
		Name: "main",
		Path: path,
		Files: []*std.MemFile{
			{Name: "main.gno", Body: head + "\nfunc main() {\n" + code + "\n}\n"},
			{Name: "mem.gno", Body: "package main\n\nimport memr \"" + memRealmPath + "\"\n\nvar mem = memr.M\n"},
		},
	}
	mpkg.SetFile("gnomod.toml", mod.WriteString())
	mpkg.Sort()
	return mpkg
}

func memRealmMemPackage() *std.MemPackage {
	mod := &gnomod.File{Module: memRealmPath, Gno: gno.GnoVerLatest}
	mpkg := &std.MemPackage{
		Type:  gno.MPUserProd,
		Name:  "mem",
		Path:  memRealmPath,
		Files: []*std.MemFile{{Name: "mem.gno", Body: memRealmSource}},
	}
	mpkg.SetFile("gnomod.toml", mod.WriteString())
	mpkg.Sort()
	return mpkg
}
