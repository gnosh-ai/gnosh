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
	"database/sql"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
	_ "modernc.org/sqlite"
)

// TestMemIndexFTS5 checks that the SQLite gnosh links has what mem's index
// needs: FTS5 with the trigram tokenizer, so that "flak" finds "flakes",
// and secure-delete, so that a deleted row's text leaves the file.
func TestMemIndexFTS5(t *testing.T) {
	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "index.db"))
	require.NoError(t, err)
	defer db.Close()
	_, err = db.Exec(`CREATE VIRTUAL TABLE docs USING fts5(name UNINDEXED, text, tokenize='trigram')`)
	require.NoError(t, err)
	_, err = db.Exec(`INSERT INTO docs(docs, rank) VALUES('secure-delete', 1)`)
	require.NoError(t, err)
	for name, text := range map[string]string{
		"fix-ci/2":    "TestFoo flakes under -race",
		"cmd/test":    "go test -race ./...",
		"release-v2":  "release v2",
		"gotcha/fill": "never DeepFill a map: gno panics",
	} {
		_, err := db.Exec(`INSERT INTO docs(name, text) VALUES(?, ?)`, name, text)
		require.NoError(t, err)
	}
	match := func(q string) []string {
		rows, err := db.Query(`SELECT name FROM docs WHERE docs MATCH ? ORDER BY rank`, q)
		require.NoError(t, err)
		defer rows.Close()
		var names []string
		for rows.Next() {
			var n string
			require.NoError(t, rows.Scan(&n))
			names = append(names, n)
		}
		return names
	}
	require.Equal(t, []string{"fix-ci/2"}, match(`"flak"`))
	require.Equal(t, []string{"gotcha/fill"}, match(`"deepfill"`))
	require.ElementsMatch(t, []string{"cmd/test", "fix-ci/2"}, match(`"race"`))

	_, err = db.Exec(`DELETE FROM docs WHERE name = 'gotcha/fill'`)
	require.NoError(t, err)
	require.Empty(t, match(`"deepfill"`))
}
