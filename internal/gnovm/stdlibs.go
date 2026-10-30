// Copyright (C) 2026 Jae Kwon
//
// This file is part of gnosh's GnoVM machine subsystem. It is licensed under
// the GNO Network General Public License, version 6, or any later version
// that the Licensor has publicly accepted as the proxy under section 14 of
// that License, with the Additional Terms (Strong Attribution); see
// internal/gnovm/LICENSE.md.

package gnovm

import (
	"embed"
	"fmt"
	"io/fs"
	"path"
	"sort"
	"strconv"
	"strings"
	"sync"

	gno "github.com/gnolang/gno/gnovm/pkg/gnolang"
	"github.com/gnolang/gno/tm2/pkg/std"
)

//go:generate go run ./internal/syncstdlibs

// stdlibFS holds the Gno standard library sources, copied from the gno
// version in go.mod by internal/syncstdlibs. A deployed gnosh has no GNOROOT,
// and go:embed cannot reach the module cache.
//
//go:embed stdlibs
var stdlibFS embed.FS

// klashlibFS holds the libraries gnosh adds for its VMs: fmt, since gno has
// none (formatted natively, with Go's fmt: fmtnative.go), and gnosh's own
// packages under gnosh/.
//
//go:embed klashlibs
var klashlibFS embed.FS

// stdlibVersion is the gno version the embedded sources were copied from.
func stdlibVersion() string {
	b, err := stdlibFS.ReadFile("stdlibs/VERSION")
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(b))
}

// stdlibMemPackage builds the production MemPackage for a stdlib path from the
// embedded sources, as gno's ReadMemPackageFromList does for a directory: its
// non-test .gno files, under one package name, sorted. It returns nil, nil
// when no stdlib has that path.
func stdlibMemPackage(pkgPath string) (*std.MemPackage, error) {
	return embeddedMemPackage(stdlibFS, "stdlibs", pkgPath)
}

// libraryMemPackage is stdlibMemPackage over both gnosh's libraries and the
// standard library; gnosh's come first.
func libraryMemPackage(pkgPath string) (*std.MemPackage, error) {
	mpkg, err := embeddedMemPackage(klashlibFS, "klashlibs", pkgPath)
	if mpkg != nil || err != nil {
		return mpkg, err
	}
	return stdlibMemPackage(pkgPath)
}

func embeddedMemPackage(fsys embed.FS, root, pkgPath string) (*std.MemPackage, error) {
	if pkgPath == "" || pkgPath != path.Clean(pkgPath) || strings.HasPrefix(pkgPath, ".") {
		return nil, nil
	}
	dir := path.Join(root, pkgPath)
	entries, err := fs.ReadDir(fsys, dir)
	if err != nil {
		return nil, nil
	}
	mpkg := &std.MemPackage{Type: gno.MPStdlibProd, Path: pkgPath}
	var name gno.Name
	for _, e := range entries {
		fname := e.Name()
		if e.IsDir() || !isProdGno(fname) {
			continue
		}
		body, err := fs.ReadFile(fsys, path.Join(dir, fname))
		if err != nil {
			return nil, err
		}
		pname, err := gno.PackageNameFromFileBody(path.Join(pkgPath, fname), string(body))
		if err != nil {
			return nil, err
		}
		switch {
		case name == "":
			name = pname
		case pname != name:
			return nil, fmt.Errorf("library %s: %s is package %q, want %q", pkgPath, fname, pname, name)
		}
		mpkg.Files = append(mpkg.Files, &std.MemFile{Name: fname, Body: string(body)})
	}
	if len(mpkg.Files) == 0 {
		return nil, nil
	}
	mpkg.Name = string(name)
	mpkg.Sort()
	return mpkg, nil
}

// libraryPaths lists every package path a VM can import, sorted.
func libraryPaths() []string {
	seen := map[string]bool{}
	for _, lib := range []struct {
		fsys embed.FS
		root string
	}{{stdlibFS, "stdlibs"}, {klashlibFS, "klashlibs"}} {
		_ = fs.WalkDir(lib.fsys, lib.root, func(p string, d fs.DirEntry, err error) error {
			if err == nil && !d.IsDir() && isProdGno(d.Name()) {
				seen[strings.TrimPrefix(path.Dir(p), lib.root+"/")] = true
			}
			return nil
		})
	}
	out := make([]string, 0, len(seen))
	for p := range seen {
		out = append(out, p)
	}
	sort.Strings(out)
	return out
}

// ImportablePackages lists the packages a VM may import, besides fmt: the
// standard library less what VMs refuse (see unavailablePackage) and the
// internal packages, and gnosh's own for models (klash/gnomem/v1,
// klash/bptree/v0; klash/gnomem/v2 is mem's realm's alone).
func ImportablePackages() []string {
	var out []string
	for _, p := range libraryPaths() {
		if p == "fmt" || strings.Contains(p, "internal") || p == memLibPath || unavailablePackage(p) != "" {
			continue
		}
		out = append(out, p)
	}
	return out
}

// libraryPackageName is the name the library at pkgPath declares in its
// package clause, or "" if there is no such library.
func libraryPackageName(pkgPath string) string {
	for _, lib := range []struct {
		fsys embed.FS
		root string
	}{{klashlibFS, "klashlibs"}, {stdlibFS, "stdlibs"}} {
		dir := path.Join(lib.root, pkgPath)
		entries, err := fs.ReadDir(lib.fsys, dir)
		if err != nil {
			continue
		}
		for _, e := range entries {
			if e.IsDir() || !isProdGno(e.Name()) {
				continue
			}
			body, err := fs.ReadFile(lib.fsys, path.Join(dir, e.Name()))
			if err != nil {
				return ""
			}
			name, err := gno.PackageNameFromFileBody(path.Join(pkgPath, e.Name()), string(body))
			if err != nil {
				return ""
			}
			return string(name)
		}
	}
	return ""
}

var (
	importNamesOnce sync.Once
	importNames     map[string]string
)

// importByName is the package an input means by name when it uses the name
// without importing it: fmt, or one of ImportablePackages whose package name
// it is. Where two share a name, the newest version is meant
// (klash/gnomem/v2 over v1), and otherwise the shorter path (math/rand, not
// crypto/chacha20/rand); a name that is also a Go package VMs refuse
// (unsafe) is never imported.
func importByName(name string) (string, bool) {
	importNamesOnce.Do(func() {
		importNames = map[string]string{}
		for _, p := range append([]string{"fmt"}, ImportablePackages()...) {
			n := libraryPackageName(p)
			if n == "" || unavailablePackage(n) != "" {
				continue
			}
			if old, ok := importNames[n]; ok && !preferPath(p, old) {
				continue
			}
			importNames[n] = p
		}
	})
	p, ok := importNames[name]
	return p, ok
}

// preferPath reports whether path p is meant over q, two paths of packages
// with the same name: a later version of the same package, or else the
// shorter path.
func preferPath(p, q string) bool {
	pb, pv := splitVersion(p)
	qb, qv := splitVersion(q)
	if pb == qb {
		return pv > qv
	}
	return len(p) < len(q)
}

// splitVersion splits a trailing /vN off a package path: N is 1 for none.
func splitVersion(p string) (string, int) {
	i := strings.LastIndex(p, "/v")
	if i < 0 {
		return p, 1
	}
	n, err := strconv.Atoi(p[i+2:])
	if err != nil || n < 1 {
		return p, 1
	}
	return p[:i], n
}

// isProdGno reports whether a file belongs in a production stdlib package:
// Gno source that is not a test or a filetest.
func isProdGno(name string) bool {
	return strings.HasSuffix(name, ".gno") &&
		!strings.HasSuffix(name, "_test.gno") &&
		!strings.HasSuffix(name, "_filetest.gno")
}
