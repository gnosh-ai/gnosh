// Copyright (C) 2026 Jae Kwon
//
// This file is part of gnosh's GnoVM machine subsystem. It is licensed under
// the GNO Network General Public License, version 6, as published by
// NewTendermint, LLC or its successor, or any later version of that License
// that the Licensor accepts as the proxy under its section 14, by a statement
// published in the canonical copy of internal/gnovm/LICENSE.md; together with
// the Additional Terms (Strong Attribution) in that file.

package gnovm

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

// Scrub takes an erasure out of a copy of mem's text: the lines naming
// what was forgotten go, a goal renamed for a secret in its name is named
// anew, and each erased text, as typed or Go-quoted, gives way; a text too
// short to be told from others is left.
func TestMemErasureScrub(t *testing.T) {
	t.Parallel()
	e := MemErasure{
		Texts:   map[string]string{"plan the surprise party": "(forgotten)", "sk-live-4f\"9c": "(redacted)", "tea": "(forgotten)"},
		Names:   []string{"plan-surprise-party"},
		Renamed: map[string]string{"call-555-0199": "forgotten-2"},
	}
	block := "<mem>\n- `plan-surprise-party` \"plan the surprise party\" (0/2 done)\n  next: `plan-surprise-party/1` \"book it\"\n- `call-555-0199` \"call (redacted)\"\n- `make-tea` \"make tea\"\n</mem>"
	require.Equal(t, "<mem>\n- `forgotten-2` \"call (redacted)\"\n- `make-tea` \"make tea\"\n</mem>", e.Scrub(block))
	require.Equal(t, "the key is (redacted) and (redacted)", e.Scrub("the key is sk-live-4f\"9c and sk-live-4f\\\"9c"))
	require.Equal(t, "(forgotten), first", e.Scrub("plan the surprise party, first"))
	require.Equal(t, []string{"sk-live-4f\"9c"}, e.Secrets())
}

// A forget and a redact say what they erased, for gnosh's own copies.
func TestMemHostSaysWhatItErased(t *testing.T) {
	t.Parallel()
	h := newTestMemHost(t, t.TempDir())
	memOK(t, h, `mem.Goal("plan the surprise party").Steps("book Moonlight Hall")`)
	memOK(t, h, `mem.Note("plan-surprise-party/1", "the deposit is 200")`)
	memOK(t, h, `mem.Drop("plan-surprise-party", "called off")`)
	memOK(t, h, `mem.Fact("phone/alice", "555-0142 ext 9")`)
	res := memOK(t, h, `mem.Forget("plan-surprise-party")`)
	require.NotNil(t, res.Erased)
	require.Equal(t, map[string]string{"plan-surprise-party": "forgotten-1"}, res.Erased.Renamed)
	require.ElementsMatch(t, []string{"plan-surprise-party", "plan-surprise-party/1"}, res.Erased.Names)
	for _, text := range []string{"plan the surprise party", "book Moonlight Hall", "the deposit is 200", "called off"} {
		require.Equal(t, "(forgotten)", res.Erased.Texts[text], text)
	}
	res = memOK(t, h, `mem.Forget("phone/alice")`)
	require.Equal(t, []string{"phone/alice"}, res.Erased.Names)
	require.Equal(t, "(forgotten)", res.Erased.Texts["555-0142 ext 9"])

	memOK(t, h, `mem.Fact("deploy", "use the key sk-live-4f9a2c")`)
	res, err := h.call(context.Background(), memRequest{Code: `mem.Redact("sk-live-4f9a2c")`, User: true})
	require.NoError(t, err)
	require.Equal(t, map[string]string{"sk-live-4f9a2c": "(redacted)"}, res.Erased.Texts)
	require.Nil(t, memOK(t, h, `mem.Show()`).Erased, "a read erases nothing")
}
