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

// A scratch run whose loop showed one line is told to print the terms if
// the answer gives them; one that printed them, one with no loop and a
// named VM's run are not.
func TestLoopNote(t *testing.T) {
	t.Parallel()
	h := newTestHost(t, t.TempDir())
	note := "only the loop's result was printed: if your answer gives its terms or a breakdown, print them too, run it again, and give them as printed"
	res := run(t, h, "", "total := 0\nfor i := 1; i <= 3; i++ {\n\ttotal += i\n}\ntotal")
	require.Equal(t, note, res.Notes[0])
	res = run(t, h, "", "for i := 1; i <= 3; i++ {\n\tprintln(\"term\", i)\n}")
	require.NotContains(t, res.Notes, note)
	res = run(t, h, "", "1 + 2")
	require.NotContains(t, res.Notes, note)

	persist := true
	_, _, err := h.New(context.Background(), NewRequest{Project: "/proj", Name: "sums", Persist: &persist})
	require.NoError(t, err)
	res = run(t, h, "sums", "n := 0\nfor i := 0; i < 3; i++ {\n\tn++\n}\nn")
	require.NotContains(t, res.Notes, note)
}

// Where Gno differs from Go, a failed input is told how, in one line: the
// GnoVM's own words did not say, and Jae's lane spent minutes on a backoff
// sum written in Go (2026-10-30). Each input runs in a scratch VM of its
// own; the shift's are written as the model wrote them, and the hint's two
// fixes run. The hint comes first of the notes, right under the error.
func TestGoHints(t *testing.T) {
	t.Parallel()
	h := newTestHost(t, t.TempDir())
	shift := "Gno types a shift of an untyped constant by a variable wrongly (gnolang/gno#6393): give the constant a type, int(1) << n, or the variable one, var w int = …"
	for i, c := range []struct{ code, hint, out string }{
		{"cap := 30000\nprintln(cap)", "Gno does not let a variable take a builtin's name, as Go does: rename cap", ""},
		{"println(min(3, 2))", "Gno has no min builtin, Go 1.21's: compare with if, or write a func", ""},
		// It types w as an untyped bigint, then refuses an int for it.
		{"capMs := 30000\ntotal := 0\nfor i := 1; i <= 10; i++ {\n\tw := 200 * (1 << (i - 1))\n\tif w > capMs {\n\t\tw = capMs\n\t}\n\ttotal += w\n}\nprintln(total)", shift, ""},
		// Or crashes.
		{"i := 3\nw := 200 * (1 << i)\nprintln(w)", shift, ""},
		{"i := 3\nw := 200 * (int(1) << (i - 1))\nprintln(w)", "", "800\n"},
		{"i := 3\nvar w int = 200 * (1 << (i - 1))\nprintln(w)", "", "800\n"},
	} {
		res, err := h.Run(context.Background(), RunRequest{Project: "/proj", Session: "s" + string(rune('a'+i)), Code: c.code})
		require.NoError(t, err, c.code)
		if c.hint == "" {
			require.NoError(t, res.Err, c.code)
			require.Equal(t, c.out, res.Output)
			continue
		}
		require.Error(t, res.Err, c.code)
		require.NotEmpty(t, res.Notes, c.code)
		require.Equal(t, c.hint, res.Notes[0], "the hint first, right under the error: %q", res.Notes)
	}
}
