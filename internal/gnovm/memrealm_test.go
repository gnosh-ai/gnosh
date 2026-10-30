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
	"crypto/sha256"
	"encoding/hex"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestMemRealmPersists runs a mem input against the realm in a store on
// disk, then opens the store again from a new base, as a new klash process
// would, and finds what the first input made.
// TestMemLayoutPinned fails when the sources that fix how a Mem is stored
// change while memLayout stays the same. Bump memLayout, which rebuilds every
// mem from its journal, and pin the new hash here.
func TestMemLayoutPinned(t *testing.T) {
	h := sha256.New()
	for _, f := range memLayoutFiles {
		b, err := klashlibFS.ReadFile(f)
		require.NoError(t, err)
		h.Write([]byte(f + "\n"))
		h.Write(b)
	}
	h.Write([]byte(memRealmSource))
	got := hex.EncodeToString(h.Sum(nil))[:16]
	pins := map[int]string{
		1: "40f2ee6d42a9774f",
	}
	want, ok := pins[memLayout]
	if !ok {
		t.Fatalf("pin memLayout %d here: %d: %q", memLayout, memLayout, got)
	}
	require.Equal(t, want, got,
		"how a Mem is stored changed: make memLayout %d, and pin %d: %q here", memLayout+1, memLayout+1, got)
}

func TestMemRealmPersists(t *testing.T) {
	dir := t.TempDir()
	r, err := openMemRealm(newBase(), dir)
	require.NoError(t, err)
	call, err := r.run(`mem.Goal("make a pot of tea").Steps("boil water", "pour water")`, "sess-1")
	require.NoError(t, err, call.Output)
	require.Contains(t, call.Output, "added goal `make-pot-tea`")
	require.Contains(t, call.Output, "`make-pot-tea/2` \"pour water\"")
	require.Equal(t, []memOp{
		{Kind: "add", Name: "make-pot-tea", New: "make a pot of tea", By: "sess-1"},
		{Kind: "add", Name: "make-pot-tea/1", New: "boil water", By: "sess-1"},
		{Kind: "add", Name: "make-pot-tea/2", New: "pour water", By: "sess-1"},
	}, call.Ops)
	require.NoError(t, r.close())

	r, err = openMemRealm(newBase(), dir)
	require.NoError(t, err)
	defer r.close()
	call, err = r.run(`println(mem.Show("make-pot-tea"))`, "sess-2")
	require.NoError(t, err, call.Output)
	require.Empty(t, call.Ops)
	require.Contains(t, call.Output, "`make-pot-tea/1` \"boil water\"")
	require.Contains(t, call.Output, "`make-pot-tea/2` \"pour water\"")
}

// TestMemRealmGuards: only mem's own Mem, through its methods, changes mem.
// A Mem the input makes or copies for itself is refused by gnomem's natives,
// so its Ops can never reach the journal; a refusal cannot be recovered;
// and a read writes nothing.
func TestMemRealmGuards(t *testing.T) {
	r, err := openMemRealm(newBase(), t.TempDir())
	require.NoError(t, err)
	defer r.close()
	_, err = r.run(`mem.Goal("ours")`, "sess-1")
	require.NoError(t, err)

	for _, tc := range []struct {
		name, code, err string
		imports         []string
	}{
		{"own", `x := gnomem.New(); x.Goal("theirs")`, "only mem's own Mem", []string{`gnomem "klash/gnomem/v2"`}},
		{"copy", `x := *mem; x.Goal("theirs")`, "only mem's own Mem", nil},
		{"revive", `revive(func() { mem.Goal("theirs"); panic("p") })`, "revive() not enabled", nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := r.run(tc.code, "sess-1", tc.imports...)
			require.ErrorContains(t, err, tc.err)
		})
	}

	call, err := r.run(`println(mem.Show())`, "sess-1")
	require.NoError(t, err, call.Output)
	require.Contains(t, call.Output, "`ours` \"ours\"")
	require.NotContains(t, call.Output, "theirs")
}

// TestMemRealmFailedInputKeepsNothing: a panic after a change leaves the
// store as it was.
func TestMemRealmFailedInputKeepsNothing(t *testing.T) {
	r, err := openMemRealm(newBase(), t.TempDir())
	require.NoError(t, err)
	defer r.close()
	_, err = r.run(`mem.Goal("first"); panic("no")`, "sess-1")
	require.Error(t, err)
	call, err := r.run(`println(mem.Show())`, "sess-1")
	require.NoError(t, err, call.Output)
	require.Contains(t, call.Output, "mem is empty")
}
