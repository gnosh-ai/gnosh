// Copyright (C) 2026 Jae Kwon
//
// This file is part of gnosh's GnoVM machine subsystem. It is licensed under
// the GNO Network General Public License, version 6, or any later version
// that the Licensor has publicly accepted as the proxy under section 14 of
// that License, with the Additional Terms (Strong Attribution); see
// internal/gnovm/LICENSE.md.

package gnovm

import (
	"io/fs"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	gno "github.com/gnolang/gno/gnovm/pkg/gnolang"
	"github.com/stretchr/testify/require"
)

// embeddedStdlibPaths lists every stdlib package path with embedded sources.
func embeddedStdlibPaths(t *testing.T) []string {
	t.Helper()
	seen := map[string]bool{}
	err := fs.WalkDir(stdlibFS, "stdlibs", func(p string, d fs.DirEntry, err error) error {
		if err == nil && !d.IsDir() && isProdGno(d.Name()) {
			seen[strings.TrimPrefix(path.Dir(p), "stdlibs/")] = true
		}
		return err
	})
	require.NoError(t, err)
	var out []string
	for p := range seen {
		out = append(out, p)
	}
	return out
}

// The embedded sources and the native bindings compiled into gnosh must come
// from the same gno, or a native call can disagree with its declaration.
// After bumping gno in go.mod, run `go generate ./internal/gnovm`.
func TestEmbeddedStdlibsVersion(t *testing.T) {
	t.Parallel()
	gomod, err := os.ReadFile(filepath.Join("..", "..", "go.mod"))
	require.NoError(t, err)
	m := regexp.MustCompile(`(?m)^\s*github\.com/gnolang/gno (\S+)`).FindSubmatch(gomod)
	require.NotNil(t, m, "go.mod requires github.com/gnolang/gno")
	require.Equal(t, string(m[1]), stdlibVersion(),
		"internal/gnovm/stdlibs is from another gno version; run `go generate ./internal/gnovm`")
}

// stdlibMemPackage must build the same package as gno's own directory reader
// on the module's copy of each stdlib.
func TestStdlibMemPackageMatchesGno(t *testing.T) {
	t.Parallel()
	out, err := exec.Command("go", "list", "-m", "-f", "{{.Dir}}", "github.com/gnolang/gno").Output()
	modDir := strings.TrimSpace(string(out))
	if err != nil || modDir == "" {
		t.Skip("gno module source not available:", err)
	}
	paths := embeddedStdlibPaths(t)
	require.GreaterOrEqual(t, len(paths), 40, "the stdlib set looks truncated")
	for _, p := range paths {
		got, err := stdlibMemPackage(p)
		require.NoError(t, err, p)
		require.NotNil(t, got, p)

		dir := filepath.Join(modDir, "gnovm", "stdlibs", filepath.FromSlash(p))
		entries, err := os.ReadDir(dir)
		require.NoError(t, err, p)
		var files []string
		for _, e := range entries {
			if !e.IsDir() && strings.HasSuffix(e.Name(), ".gno") {
				files = append(files, filepath.Join(dir, e.Name()))
			}
		}
		want, err := gno.ReadMemPackageFromList(files, p, gno.MPStdlibProd)
		require.NoError(t, err, p)

		require.Equal(t, want.Name, got.Name, p)
		require.Equal(t, want.Path, got.Path, p)
		require.Equal(t, want.Type, got.Type, p)
		require.Equal(t, len(want.Files), len(got.Files), p)
		for i := range want.Files {
			require.Equal(t, want.Files[i].Name, got.Files[i].Name, p)
			require.Equal(t, want.Files[i].Body, got.Files[i].Body, p+"/"+want.Files[i].Name)
		}
	}
}

func TestStdlibMemPackageUnknownPath(t *testing.T) {
	t.Parallel()
	mpkg, err := stdlibMemPackage("gno.land/p/nope")
	require.NoError(t, err)
	require.Nil(t, mpkg)
}
