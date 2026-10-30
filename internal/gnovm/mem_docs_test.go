package gnovm

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// GNOMEM.md says an upgrade keeps everything an earlier Gnosh kept in mem,
// and what a Gnosh still running from before the upgrade says. The mem in
// testdata/memupgrade/goals-layout2 is one a layout-2 build left on
// 2026-10-30, after an agent kept GNOMEM's own example in it: the release-v2
// goal, its steps and its question for the user, and the staging-db fact.
func TestDocMemKeepsWhatAnEarlierBuildKept(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	require.NoError(t, os.CopyFS(dir, os.DirFS(filepath.Join("testdata", "memupgrade", "goals-layout2"))))
	h := newTestMemHost(t, dir)

	res := memOK(t, h, `println(mem.Show())`)
	require.Contains(t, res.Notes, "mem's store was rebuilt from its journal")
	for _, want := range []string{
		"[?] `release-v2/3` \"which day suits you for the release?\"",
		"[ ] `release-v2/1` \"bump the version\"  ← next",
		"[ ] `release-v2/2` \"tag v2.0.0\"",
		"`staging-db` = the staging database is db-stg-7",
	} {
		require.Contains(t, res.Output, want)
	}
	require.Contains(t, memOK(t, h, `mem.Find("staging database")`).Output, "fact `staging-db` = the staging database is db-stg-7")
	require.Contains(t, memOK(t, h, `mem.Done("release-v2/1", "now 2.0.0")`).Output,
		"done `release-v2/1` \"bump the version\" → now 2.0.0; next: `release-v2/2` \"tag v2.0.0\"")

	// The store as a later layout leaves it, which the Gnosh still running
	// from before that upgrade finds on its next call.
	stores, err := filepath.Glob(filepath.Join(dir, "store-*"))
	require.NoError(t, err)
	r, err := openMemRealm(newBase(), stores[0])
	require.NoError(t, err)
	st, _, err := r.stamp()
	require.NoError(t, err)
	st.Layout = memLayout + 1
	require.NoError(t, r.setStamp(st))
	require.NoError(t, r.close())
	_, err = h.call(context.Background(), memRequest{Code: `println(mem.Show())`, Session: "s1"})
	var ref *refusal
	require.ErrorAs(t, err, &ref)
	msg := err.Error()
	lp, rp := strings.Index(msg, " ("), strings.Index(msg, ")")
	require.True(t, lp > 0 && rp > lp, msg)
	doc, err := os.ReadFile(filepath.Join("..", "..", "GNOMEM.md"))
	require.NoError(t, err)
	require.Contains(t, strings.Join(strings.Fields(string(doc)), " "), `"`+msg[:lp]+" (…)"+msg[rp+1:]+`"`)
}
