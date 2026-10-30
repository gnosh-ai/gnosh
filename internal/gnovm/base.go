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
	"fmt"
	"io"
	"runtime"
	"sort"
	"strings"

	gno "github.com/gnolang/gno/gnovm/pkg/gnolang"
	"github.com/gnolang/gno/gnovm/stdlibs"
	"github.com/gnolang/gno/tm2/pkg/db/memdb"
	"github.com/gnolang/gno/tm2/pkg/store/dbadapter"
	storetypes "github.com/gnolang/gno/tm2/pkg/store/types"
)

// base is the gno store that every VM's overlay reads through. It holds the
// standard library and klash's libraries, each loaded once per process, on
// demand: an empty VM costs ~43 KB, but a store per VM that loads its own
// strings, strconv and time costs ~88 MB.
//
// Every import must be loaded here before any VM looks the package up: an
// overlay's cache remembers a miss, so a package loaded after a VM missed it
// would stay missing in that VM. ensure does the loading; callers pass it
// every import of an input, or of a whole journal, before running anything.
type base struct {
	kv    storetypes.CommitStore
	store gno.Store

	// names maps each loaded package path to its package name.
	names map[string]string
	// heap is the heap growth measured across loads, charged to the budget.
	heap int64
}

func newBase() *base {
	kv := dbadapter.StoreConstructor(memdb.NewMemDB(), storetypes.StoreOptions{})
	b := &base{kv: kv, names: map[string]string{}}
	b.store = gno.NewStore(nil, kv, kv)
	b.store.SetNativeResolver(nativeResolver)
	b.store.SetPackageGetter(b.getPackage)
	return b
}

// nativeResolver is gno's, with chain.emit doing nothing: a VM's events
// would collect in Go heap outside its memory cap for as long as the call
// runs (gigabytes at high gas, measured), nothing on the VM's side can read
// them, and dropping them changes no state, so replay is unchanged.
func nativeResolver(pkgPath string, name gno.Name) func(*gno.Machine) {
	switch {
	case pkgPath == "chain" && name == "emit":
		return func(*gno.Machine) {}
	case pkgPath == "fmt":
		return fmtNative(name)
	}
	return stdlibs.NativeResolver(pkgPath, name)
}

// getPackage is the base store's package getter: it runs a library package
// from the embedded sources into the store, as gno's test store does.
func (b *base) getPackage(pkgPath string, store gno.Store) (*gno.PackageNode, *gno.PackageValue) {
	mpkg, err := libraryMemPackage(pkgPath)
	if err != nil {
		panic(err)
	}
	if mpkg == nil {
		return nil, nil
	}
	m := gno.NewMachineWithOptions(gno.MachineOptions{
		PkgPath:       pkgPath,
		Output:        io.Discard,
		Store:         store,
		ReviveEnabled: true,
		SkipPackage:   true,
	})
	return m.RunMemPackageWithOverrides(mpkg, true)
}

// ensure loads the packages at paths into the base, with whatever they
// import, and returns their package names by path. It measures the heap the
// loads add (the process runs one VM operation at a time, so the measure is
// clean). Loading is not metered: it is the same work for every VM, done
// once.
func (b *base) ensure(paths []string) (map[string]string, error) {
	names := make(map[string]string, len(paths))
	var missing []string
	for _, p := range paths {
		if name, ok := b.names[p]; ok {
			names[p] = name
		} else {
			missing = append(missing, p)
		}
	}
	if len(missing) == 0 {
		return names, nil
	}
	sort.Strings(missing)
	before := heapAlloc()
	defer func() { b.heap += max(int64(heapAlloc())-int64(before), 0) }()
	for _, p := range missing {
		name, err := b.load(p)
		if err != nil {
			return nil, err
		}
		names[p] = name
	}
	return names, nil
}

func (b *base) load(pkgPath string) (name string, err error) {
	if hint := unavailablePackage(pkgPath); hint != "" {
		return "", fmt.Errorf("package %q is not available in klash VMs: %s", pkgPath, hint)
	}
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("loading package %q: %v", pkgPath, r)
		}
	}()
	pv := b.store.GetPackage(pkgPath, true)
	if pv == nil {
		return "", fmt.Errorf("package %q is not available in klash VMs: %s", pkgPath, unknownPackageHint(pkgPath))
	}
	b.names[pkgPath] = string(pv.PkgName)
	return string(pv.PkgName), nil
}

// unavailablePackage explains why a well-known Go package has no Gno
// counterpart, or returns "" if it is not one of those.
func unavailablePackage(pkgPath string) string {
	switch pkgPath {
	case "os", "os/exec", "io/ioutil", "io/fs", "path/filepath", "syscall", "net", "net/http", "log":
		return "VMs have no files, network or processes; print with println or fmt.Println"
	case "sync", "sync/atomic", "context":
		return "VMs run one goroutine, so there is nothing to synchronize"
	case "encoding/json", "encoding/xml":
		return "there is no JSON package; build strings with strconv and strings, or use encoding/csv, encoding/hex or encoding/base64"
	case "slices", "maps", "container/list", "container/heap":
		return "write the loop by hand, or use sort"
	case "reflect", "unsafe":
		return "Gno has no reflection"
	case "chain/banker", "chain/params", "sys/params":
		return "VMs run on no chain, so there are no coins or chain parameters"
	case "testing":
		return "check values with if and panic"
	}
	return ""
}

func unknownPackageHint(pkgPath string) string {
	if strings.Contains(strings.SplitN(pkgPath, "/", 2)[0], ".") {
		return "on-chain packages are not available; VMs have the Gno standard library and fmt"
	}
	return "VMs have the Gno standard library and fmt (gnovm_list shows the packages)"
}

// heapAlloc is the live heap after a collection.
func heapAlloc() uint64 {
	runtime.GC()
	var ms runtime.MemStats
	runtime.ReadMemStats(&ms)
	return ms.HeapAlloc
}
