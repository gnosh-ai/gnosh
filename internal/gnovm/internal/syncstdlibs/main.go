// Copyright (C) 2026 Jae Kwon
//
// This file is part of klash's GnoVM machine subsystem. It is licensed under
// the GNO Network General Public License, version 6, as published by
// NewTendermint, LLC or its successor, or any later version of that License
// that the Licensor accepts as the proxy under its section 14, by a statement
// published in the canonical copy of internal/gnovm/LICENSE.md; together with
// the Additional Terms (Strong Attribution) in that file.

// Command syncstdlibs copies the Gno standard library sources from the
// github.com/gnolang/gno version in go.mod into internal/gnovm/stdlibs, where
// they are embedded: a deployed klash has no GNOROOT to load them from. It
// also copies gno's ufmt into internal/gnovm/klashlibs/fmt, renamed to fmt.
// Run it with `go generate ./internal/gnovm` after changing the gno version. The
// copy's VERSION file must match go.mod, or TestEmbeddedStdlibsVersion fails,
// since the embedded sources and the native bindings compiled into klash must
// come from the same gno.
package main

import (
	"bytes"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "syncstdlibs:", err)
		os.Exit(1)
	}
}

func run() error {
	out, err := exec.Command("go", "list", "-m", "-f", "{{.Dir}} {{.Version}}", "github.com/gnolang/gno").Output()
	if err != nil {
		return fmt.Errorf("go list github.com/gnolang/gno: %w (run `go mod download` first)", err)
	}
	fields := strings.Fields(string(out))
	if len(fields) != 2 || fields[0] == "" {
		return fmt.Errorf("unexpected go list output %q", out)
	}
	modDir, version := fields[0], fields[1]
	src := filepath.Join(modDir, "gnovm", "stdlibs")

	// go generate runs in the package directory, internal/gnovm.
	dst := "stdlibs"
	if err := os.RemoveAll(dst); err != nil {
		return err
	}
	n := 0
	err = filepath.WalkDir(src, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || !isProdGno(d.Name()) {
			return err
		}
		rel, err := filepath.Rel(src, p)
		if err != nil {
			return err
		}
		body, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		target := filepath.Join(dst, rel)
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return err
		}
		n++
		return os.WriteFile(target, body, 0o644)
	})
	if err != nil {
		return err
	}

	// The copy keeps gno's own license and attribution terms.
	license, err := os.ReadFile(filepath.Join(modDir, "LICENSE.md"))
	if err != nil {
		return err
	}
	notice := []byte("The files in this directory are the Gno standard library, copied unmodified\n" +
		"from github.com/gnolang/gno " + version + " (gnovm/stdlibs) by\n" +
		"internal/gnovm/internal/syncstdlibs. They are copyright NewTendermint, LLC,\n" +
		"under the license below, including its Additional Terms (Strong Attribution).\n\n---\n\n")
	if err := os.WriteFile(filepath.Join(dst, "LICENSE.md"), bytes.Join([][]byte{notice, license}, nil), 0o644); err != nil {
		return err
	}
	// Many stdlib files came from Go and keep Go's BSD license.
	goroot, err := exec.Command("go", "env", "GOROOT").Output()
	if err != nil {
		return fmt.Errorf("go env GOROOT: %w", err)
	}
	goLicense, err := os.ReadFile(filepath.Join(strings.TrimSpace(string(goroot)), "LICENSE"))
	if err != nil {
		return fmt.Errorf("reading Go's LICENSE: %w", err)
	}
	if err := os.WriteFile(filepath.Join(dst, "LICENSE-GO"), goLicense, 0o644); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(dst, "VERSION"), []byte(version+"\n"), 0o644); err != nil {
		return err
	}
	fmt.Printf("syncstdlibs: %d files from gno %s\n", n, version)

	// Gno has no fmt; models write it anyway. klashlibs/fmt is gno's ufmt,
	// with only its package clause renamed. The rest of klashlibs is klash's
	// own and is left alone.
	ufmt := filepath.Join(modDir, "examples", "gno.land", "p", "nt", "ufmt", "v0")
	fmtDst := filepath.Join("klashlibs", "fmt")
	if err := os.RemoveAll(fmtDst); err != nil {
		return err
	}
	if err := os.MkdirAll(fmtDst, 0o755); err != nil {
		return err
	}
	entries, err := os.ReadDir(ufmt)
	if err != nil {
		return err
	}
	for _, e := range entries {
		if e.IsDir() || !isProdGno(e.Name()) {
			continue
		}
		body, err := os.ReadFile(filepath.Join(ufmt, e.Name()))
		if err != nil {
			return err
		}
		renamed := bytes.Replace(body, []byte("\npackage ufmt\n"), []byte("\npackage fmt\n"), 1)
		if bytes.Equal(renamed, body) {
			return fmt.Errorf("%s: no package ufmt clause to rename", e.Name())
		}
		name := strings.Replace(e.Name(), "ufmt", "fmt", 1)
		// The license (section 5(a)) asks a modified file to say so, with
		// a date. The change, and so the date, is the same every run.
		notice := "// Modified by klash on 29 October 2026: the package is renamed from ufmt to\n" +
			"// fmt (and ufmt.gno to fmt.gno). The original is gno's\n" +
			"// examples/gno.land/p/nt/ufmt/v0; see LICENSE.md in this directory.\n\n"
		if err := os.WriteFile(filepath.Join(fmtDst, name), append([]byte(notice), renamed...), 0o644); err != nil {
			return err
		}
	}
	fmtNotice := []byte("The files in this directory are gno's ufmt package\n" +
		"(examples/gno.land/p/nt/ufmt/v0) from github.com/gnolang/gno " + version + ",\n" +
		"copied by internal/gnovm/internal/syncstdlibs with the package renamed to fmt\n" +
		"and ufmt.gno renamed to fmt.gno, and a dated notice of that change added to\n" +
		"each file; nothing else is changed. They are copyright\n" +
		"NewTendermint, LLC, under the license below, including its Additional Terms\n" +
		"(Strong Attribution).\n\n---\n\n")
	if err := os.WriteFile(filepath.Join(fmtDst, "LICENSE.md"), bytes.Join([][]byte{fmtNotice, license}, nil), 0o644); err != nil {
		return err
	}
	fmt.Printf("syncstdlibs: klashlibs/fmt from ufmt v0\n")
	return nil
}

// isProdGno reports whether a file belongs in a production stdlib package:
// Gno source that is not a test or a filetest.
func isProdGno(name string) bool {
	return strings.HasSuffix(name, ".gno") &&
		!strings.HasSuffix(name, "_test.gno") &&
		!strings.HasSuffix(name, "_filetest.gno")
}
