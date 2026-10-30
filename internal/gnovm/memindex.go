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
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
)

// memIndex is mem's index, index-<generation>.db beside the store: each
// item's, fact's and rule's current text, searchable by words. It is a
// projection of the journal's Ops, kept by applying each Op as the store
// does, so it needs nothing from the store, and is rebuilt from the journal
// the same way. FTS5 with trigrams finds "flak" in "flakes"; secure-delete
// overwrites the text of a deleted or replaced row, so erased text leaves
// the file.
type memIndex struct {
	db *sql.DB
}

const memIndexSchema = `
CREATE TABLE IF NOT EXISTS stamp(id INTEGER PRIMARY KEY CHECK (id = 1), json TEXT NOT NULL);
CREATE TABLE IF NOT EXISTS names(id INTEGER PRIMARY KEY, kind TEXT NOT NULL, name TEXT NOT NULL, UNIQUE(kind, name));
CREATE VIRTUAL TABLE IF NOT EXISTS docs USING fts5(title, result, wait, notes, tokenize='trigram');
INSERT INTO docs(docs, rank) VALUES('secure-delete', 1);
`

func openMemIndex(path string) (*memIndex, error) {
	db, err := openSQLite(path, map[string]string{
		"journal_mode":  "WAL",
		"secure_delete": "ON",
		"busy_timeout":  "5000",
	})
	if err != nil {
		return nil, err
	}
	if _, err := db.Exec(memIndexSchema); err != nil {
		db.Close()
		return nil, fmt.Errorf("mem's index: %w", err)
	}
	return &memIndex{db: db}, nil
}

func (x *memIndex) close() error { return x.db.Close() }

// stamp is what the index holds, as a store's stamp says; ok is false for
// a new index.
func (x *memIndex) stamp() (st memStamp, ok bool, err error) {
	var b string
	err = x.db.QueryRow(`SELECT json FROM stamp WHERE id = 1`).Scan(&b)
	if errors.Is(err, sql.ErrNoRows) {
		return memStamp{}, false, nil
	}
	if err != nil {
		return st, false, err
	}
	return st, true, json.Unmarshal([]byte(b), &st)
}

// apply applies the Ops of entries, in one transaction, and stamps the
// index with st.
func (x *memIndex) apply(entries [][]memOp, st memStamp) error {
	tx, err := x.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, ops := range entries {
		for _, op := range ops {
			if err := applyIndexOp(tx, op); err != nil {
				return fmt.Errorf("mem's index, %s %s: %w", op.Kind, op.Name, err)
			}
		}
	}
	b, err := json.Marshal(st)
	if err != nil {
		return err
	}
	if _, err := tx.Exec(`INSERT INTO stamp(id, json) VALUES(1, ?) ON CONFLICT(id) DO UPDATE SET json = excluded.json`, string(b)); err != nil {
		return err
	}
	return tx.Commit()
}

// applyIndexOp changes the rows an Op changes the text of. Status, wait-free
// fields and claims are not text, and a Field "statements" redact has no row.
func applyIndexOp(tx *sql.Tx, op memOp) error {
	switch op.Kind {
	case "add":
		return putRow(tx, "item", op.Name, map[string]string{"title": op.New})
	case "unadd", "forget":
		return deleteRow(tx, "item", op.Name)
	case "set":
		if op.Field == "result" || op.Field == "wait" {
			return setColumn(tx, "item", op.Name, op.Field, op.New)
		}
	case "note", "unnote":
		notes, err := column(tx, "item", op.Name, "notes")
		if err != nil {
			return err
		}
		if op.Kind == "note" {
			notes = strings.TrimPrefix(notes+"\n"+op.New, "\n")
		} else {
			notes = strings.TrimSuffix(strings.TrimSuffix(notes, op.Old), "\n")
		}
		return setColumn(tx, "item", op.Name, "notes", notes)
	case "fact", "rule":
		if op.New == "" {
			return deleteRow(tx, op.Kind, op.Name)
		}
		return putRow(tx, op.Kind, op.Name, map[string]string{"title": op.New})
	case "redact":
		if op.Field == "statements" {
			return nil
		}
		for _, col := range []string{"title", "result", "wait", "notes"} {
			s, err := column(tx, op.Field, op.Name, col)
			if err != nil || !strings.Contains(s, op.Old) {
				if err != nil {
					return err
				}
				continue
			}
			if err := setColumn(tx, op.Field, op.Name, col, strings.ReplaceAll(s, op.Old, "(redacted)")); err != nil {
				return err
			}
		}
	}
	return nil
}

// rowID is the row of kind and name; 0 for none.
func rowID(tx *sql.Tx, kind, name string) (int64, error) {
	var id int64
	err := tx.QueryRow(`SELECT id FROM names WHERE kind = ? AND name = ?`, kind, name).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, nil
	}
	return id, err
}

// putRow sets a row's columns, making the row if it is new; the columns not
// given are emptied.
func putRow(tx *sql.Tx, kind, name string, cols map[string]string) error {
	id, err := rowID(tx, kind, name)
	if err != nil {
		return err
	}
	if id == 0 {
		res, err := tx.Exec(`INSERT INTO names(kind, name) VALUES(?, ?)`, kind, name)
		if err != nil {
			return err
		}
		if id, err = res.LastInsertId(); err != nil {
			return err
		}
	} else if _, err := tx.Exec(`DELETE FROM docs WHERE rowid = ?`, id); err != nil {
		return err
	}
	_, err = tx.Exec(`INSERT INTO docs(rowid, title, result, wait, notes) VALUES(?, ?, ?, ?, ?)`,
		id, cols["title"], cols["result"], cols["wait"], cols["notes"])
	return err
}

func deleteRow(tx *sql.Tx, kind, name string) error {
	id, err := rowID(tx, kind, name)
	if err != nil || id == 0 {
		return err
	}
	if _, err := tx.Exec(`DELETE FROM docs WHERE rowid = ?`, id); err != nil {
		return err
	}
	_, err = tx.Exec(`DELETE FROM names WHERE id = ?`, id)
	return err
}

// column is one column of a row; "" for a row that is not there.
func column(tx *sql.Tx, kind, name, col string) (string, error) {
	id, err := rowID(tx, kind, name)
	if err != nil || id == 0 {
		return "", err
	}
	var s string
	err = tx.QueryRow(`SELECT `+col+` FROM docs WHERE rowid = ?`, id).Scan(&s)
	return s, err
}

func setColumn(tx *sql.Tx, kind, name, col, value string) error {
	id, err := rowID(tx, kind, name)
	if err != nil || id == 0 {
		return err
	}
	_, err = tx.Exec(`UPDATE docs SET `+col+` = ? WHERE rowid = ?`, value, id)
	return err
}

// finder is find as the find native asks it. An index that cannot answer
// finds nothing, and the error is logged.
func (x *memIndex) finder() func(words string, max int) (names, kinds []string) {
	return func(words string, max int) ([]string, []string) {
		names, kinds, err := x.find(words, max)
		if err != nil {
			slog.Warn("mem's index could not search", "error", err)
			return nil, nil
		}
		return names, kinds
	}
}

// find is the rows that match words, best first, at most max: each word of
// three letters or more must be in the row; if no row has them all, any one
// will do. Shorter words cannot be matched by trigrams, and are left out.
func (x *memIndex) find(words string, max int) (names, kinds []string, err error) {
	var terms []string
	for _, w := range strings.Fields(words) {
		if len([]rune(w)) >= 3 {
			terms = append(terms, `"`+strings.ReplaceAll(w, `"`, `""`)+`"`)
		}
	}
	if len(terms) == 0 {
		return nil, nil, nil
	}
	for _, join := range []string{" AND ", " OR "} {
		rows, err := x.db.Query(`SELECT n.name, n.kind FROM docs JOIN names n ON n.id = docs.rowid WHERE docs MATCH ? ORDER BY rank LIMIT ?`,
			strings.Join(terms, join), max)
		if err != nil {
			return nil, nil, err
		}
		for rows.Next() {
			var n, k string
			if err := rows.Scan(&n, &k); err != nil {
				rows.Close()
				return nil, nil, err
			}
			names, kinds = append(names, n), append(kinds, k)
		}
		if err := rows.Close(); err != nil {
			return nil, nil, err
		}
		if len(names) > 0 || len(terms) == 1 {
			break
		}
	}
	return names, kinds, nil
}
