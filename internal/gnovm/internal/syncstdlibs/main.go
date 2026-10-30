// Copyright (C) 2026 Jae Kwon
//
// This file is part of gnosh's GnoVM machine subsystem. It is licensed under
// the GNO Network General Public License, version 6, as published by
// NewTendermint, LLC or its successor, or any later version of that License
// that the Licensor accepts as the proxy under its section 14, by a statement
// published in the canonical copy of internal/gnovm/LICENSE.md; together with
// the Additional Terms (Strong Attribution) in that file.

// Command syncstdlibs copies the Gno standard library sources from the
// github.com/gnolang/gno version in go.mod into internal/gnovm/stdlibs, where
// they are embedded: a deployed gnosh has no GNOROOT to load them from.
// (klashlibs, fmt included, is gnosh's own and is not generated.)
// Run it with `go generate ./internal/gnovm` after changing the gno version. The
// copy's VERSION file must match go.mod, or TestEmbeddedStdlibsVersion fails,
// since the embedded sources and the native bindings compiled into gnosh must
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
	return nil
}

// isProdGno reports whether a file belongs in a production stdlib package:
// Gno source that is not a test or a filetest.
func isProdGno(name string) bool {
	return strings.HasSuffix(name, ".gno") &&
		!strings.HasSuffix(name, "_test.gno") &&
		!strings.HasSuffix(name, "_filetest.gno")
}
