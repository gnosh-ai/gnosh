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
	"testing"

	"github.com/stretchr/testify/require"
)

// TestMemRealmPersists runs a mem input against the realm in a store on
// disk, then opens the store again from a new base, as a new klash process
// would, and finds what the first input made.
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
	call, err = r.run(`println(mem.Show())`, "sess-2")
	require.NoError(t, err, call.Output)
	require.Empty(t, call.Ops)
	require.Equal(t, "[ ] `make-pot-tea` \"make a pot of tea\"\n"+
		"  [ ] `make-pot-tea/1` \"boil water\"\n"+
		"  [ ] `make-pot-tea/2` \"pour water\"\n\n", call.Output)
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
	require.Equal(t, "[ ] `ours` \"ours\"\n\n", call.Output)
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
	require.Equal(t, "mem is empty\n\n", call.Output)
}
