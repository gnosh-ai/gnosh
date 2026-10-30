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

// An index of an earlier layout is built afresh, stamped or not: one made
// before the file said its layout kept the tables it was made with when no
// stamp said otherwise, as an empty mem's never was, and every session start
// read it as today's ("no such column: d.status" in Jae's lane, 2026-10-30).
// Readers take it for no index yet; mem's next call builds it.
func TestMemIndexOfAnEarlierLayoutIsBuiltAfresh(t *testing.T) {
	t.Parallel()
	// oldIndex puts at path an index as builds before the status column
	// made it, unstamped.
	oldIndex := func(path string) {
		t.Helper()
		removeMemIndex(path)
		db, err := openSQLite(path, map[string]string{"journal_mode": "WAL"})
		require.NoError(t, err)
		_, err = db.Exec(`CREATE TABLE stamp(id INTEGER PRIMARY KEY CHECK (id = 1), json TEXT NOT NULL);
CREATE TABLE names(id INTEGER PRIMARY KEY, kind TEXT NOT NULL, name TEXT NOT NULL, UNIQUE(kind, name));
CREATE VIRTUAL TABLE docs USING fts5(name, title, result, wait, notes, tokenize='trigram');`)
		require.NoError(t, err)
		require.NoError(t, db.Close())
	}
	for _, empty := range []bool{true, false} {
		dir := t.TempDir()
		h := newTestMemHost(t, dir)
		memOK(t, h, `println(mem.Show())`)
		if !empty {
			memOK(t, h, `mem.Fact("cmd/test", "go test ./...")`)
		}
		hdr, _, err := readJournalHeader(filepath.Join(dir, "journal.jsonl"))
		require.NoError(t, err)
		path := filepath.Join(dir, "index-"+hdr.Generation+".db")
		oldIndex(path)

		_, ok, err := readMemSnapshot(context.Background(), dir)
		require.NoError(t, err, "a reader takes it for no index yet")
		require.False(t, ok)

		memOK(t, h, `println(mem.Show())`)
		s, ok, err := readMemSnapshot(context.Background(), dir)
		require.NoError(t, err)
		require.True(t, ok, "mem's call built it afresh")
		if empty {
			require.Empty(t, s.rows)
		} else {
			require.Len(t, s.rows, 1)
			require.Equal(t, "cmd/test", s.rows[0].name)
		}
		x, err := openMemIndex(path)
		require.NoError(t, err)
		var layout int
		require.NoError(t, x.db.QueryRow(`PRAGMA user_version`).Scan(&layout))
		require.Equal(t, memIndexLayout, layout)
		require.NoError(t, x.close())
	}
}
