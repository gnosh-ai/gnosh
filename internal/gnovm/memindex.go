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
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"regexp"
	"slices"
	"strconv"
	"strings"
)

// memIndex is mem's index, index-<generation>.db beside the store: each
// item's, fact's and rule's name and current text, searchable by words, and
// an item's status, which recall shows. It is a
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
CREATE VIRTUAL TABLE IF NOT EXISTS docs USING fts5(name, title, result, wait, notes, status UNINDEXED, tokenize='trigram');
INSERT INTO docs(docs, rank) VALUES('secure-delete', 1);
`

// errOldIndex is an index file of an earlier layout, whose tables are not
// this build's: openIndex builds it afresh, and a reader takes it for no
// index yet.
var errOldIndex = errors.New("mem's index is of an earlier layout")

// openMemIndex opens the index at path, making a new one with today's
// tables. The file says its layout in its user_version. An index made
// before it did kept the tables it was made with, when no stamp said
// otherwise, as an empty mem's never was: in Jae's lane every session start
// then read it as today's, and failed with "no such column: d.status"
// (2026-10-30). One of an earlier layout is errOldIndex; of a later one,
// refused.
func openMemIndex(path string) (*memIndex, error) {
	db, err := openSQLite(path, map[string]string{
		"journal_mode":  "WAL",
		"secure_delete": "ON",
		"busy_timeout":  "5000",
	})
	if err != nil {
		return nil, err
	}
	fail := func(err error) (*memIndex, error) {
		db.Close()
		return nil, err
	}
	var layout, tables int
	if err := db.QueryRow(`PRAGMA user_version`).Scan(&layout); err != nil {
		return fail(fmt.Errorf("mem's index: %w", err))
	}
	switch {
	case layout > memIndexLayout:
		return fail(&refusal{"a newer gnosh wrote mem's index (layout " + strconv.Itoa(layout) + "); restart gnosh to use it"})
	case layout == memIndexLayout:
		return &memIndex{db: db}, nil
	}
	if err := db.QueryRow(`SELECT count(*) FROM sqlite_master`).Scan(&tables); err != nil {
		return fail(fmt.Errorf("mem's index: %w", err))
	}
	if tables > 0 {
		return fail(errOldIndex)
	}
	if _, err := db.Exec(memIndexSchema); err != nil {
		return fail(fmt.Errorf("mem's index: %w", err))
	}
	if _, err := db.Exec(fmt.Sprintf(`PRAGMA user_version = %d`, memIndexLayout)); err != nil {
		return fail(fmt.Errorf("mem's index: %w", err))
	}
	return &memIndex{db: db}, nil
}

// removeMemIndex deletes the index at path, with its write-ahead log.
func removeMemIndex(path string) {
	for _, suffix := range []string{"", "-wal", "-shm"} {
		_ = os.Remove(path + suffix)
	}
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

// applyIndexOp changes the rows an Op changes the text or the status of.
// A thought is a row of its own, of kind "thought", named by its address
// (item#name), with its text as its title. Asks and claims are not kept,
// and a Field "statements" redact has no row.
func applyIndexOp(tx *sql.Tx, op memOp) error {
	switch op.Kind {
	case "add":
		return putRow(tx, "item", op.Name, map[string]string{"title": op.New})
	case "unadd":
		return deleteRow(tx, "item", op.Name)
	case "forget":
		if err := deleteRow(tx, opTarget(op), op.Name); err != nil || op.Field != "" {
			return err
		}
		return deleteThoughts(tx, op.Name)
	case "set":
		if op.Field == "result" || op.Field == "wait" || op.Field == "status" {
			return setColumn(tx, "item", op.Name, op.Field, op.New)
		}
	case "note", "unnote":
		if strings.Contains(op.Name, "#") {
			if op.Kind == "note" {
				return putRow(tx, "thought", op.Name, map[string]string{"title": op.New})
			}
			return deleteRow(tx, "thought", op.Name)
		}
		// A note from before thoughts had names: the item's notes column,
		// so the item is still found by its words.
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
		kind := op.Field
		if kind == "item" && strings.Contains(op.Name, "#") {
			kind = "thought"
		}
		for _, col := range []string{"title", "result", "wait", "notes"} {
			s, err := column(tx, kind, op.Name, col)
			if err != nil || !strings.Contains(s, op.Old) {
				if err != nil {
					return err
				}
				continue
			}
			if err := setColumn(tx, kind, op.Name, col, strings.ReplaceAll(s, op.Old, "(redacted)")); err != nil {
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
	_, err = tx.Exec(`INSERT INTO docs(rowid, name, title, result, wait, notes, status) VALUES(?, ?, ?, ?, ?, ?, ?)`,
		id, name, cols["title"], cols["result"], cols["wait"], cols["notes"], cols["status"])
	return err
}

// deleteThoughts deletes the thought rows of item name.
func deleteThoughts(tx *sql.Tx, name string) error {
	rows, err := tx.Query(`SELECT name FROM names WHERE kind = 'thought' AND name LIKE ? ESCAPE '\'`, strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`).Replace(name)+"#%")
	if err != nil {
		return err
	}
	var names []string
	for rows.Next() {
		var n string
		if err := rows.Scan(&n); err != nil {
			rows.Close()
			return err
		}
		names = append(names, n)
	}
	rows.Close()
	for _, n := range names {
		if err := deleteRow(tx, "thought", n); err != nil {
			return err
		}
	}
	return nil
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

// find is the rows that hold words, by name and kind, best first, at most
// max of each: all hold every word of three letters or more, and any, for
// more than one word, hold one of them. Shorter words cannot be matched by
// trigrams: they are left out, unless they are all there is ("CI", "QA"),
// when the rows holding each as a word of its own are found by a scan.
func (x *memIndex) find(words string, max int) (all, any []memRow, err error) {
	var terms, short []string
	for _, w := range strings.Fields(words) {
		if len([]rune(w)) >= 3 {
			terms = append(terms, `"`+strings.ReplaceAll(w, `"`, `""`)+`"`)
		} else {
			short = append(short, w)
		}
	}
	if len(terms) == 0 {
		if len(short) == 0 {
			return nil, nil, nil
		}
		all, err = x.holding(short, max)
		return all, nil, err
	}
	if all, err = x.match(strings.Join(terms, " AND "), max); err != nil || len(terms) == 1 {
		return all, nil, err
	}
	any, err = x.match(strings.Join(terms, " OR "), max)
	return all, any, err
}

// match is the rows an FTS5 query matches, by name and kind, best first.
func (x *memIndex) match(query string, max int) ([]memRow, error) {
	rows, err := x.db.Query(`SELECT n.kind, n.name FROM docs JOIN names n ON n.id = docs.rowid WHERE docs MATCH ? ORDER BY rank LIMIT ?`, query, max)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []memRow
	for rows.Next() {
		var r memRow
		if err := rows.Scan(&r.kind, &r.name); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// holding is the rows that hold each of words as a word of its own, in any
// case, newest first, at most max: a scan, for words too short for
// trigrams, where "CI" must not find "decision".
func (x *memIndex) holding(words []string, max int) ([]memRow, error) {
	var res []*regexp.Regexp
	for _, w := range words {
		res = append(res, regexp.MustCompile(`(?i)(^|[^\pL\pN])`+regexp.QuoteMeta(w)+`($|[^\pL\pN])`))
	}
	rows, err := x.rows()
	if err != nil {
		return nil, err
	}
	var out []memRow
	for i := len(rows) - 1; i >= 0 && len(out) < max; i-- {
		r := rows[i]
		text := r.name + "\n" + r.title + "\n" + r.result + "\n" + r.wait + "\n" + r.notes
		if !slices.ContainsFunc(res, func(re *regexp.Regexp) bool { return !re.MatchString(text) }) {
			out = append(out, memRow{kind: r.kind, name: r.name})
		}
	}
	return out, nil
}

// rows is every row of the index, in the order they were made.
func (x *memIndex) rows() ([]memRow, error) {
	rows, err := x.db.Query(`SELECT n.kind, n.name, d.title, d.result, d.wait, d.notes, d.status FROM names n JOIN docs d ON d.rowid = n.id ORDER BY n.id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []memRow
	for rows.Next() {
		var r memRow
		if err := rows.Scan(&r.kind, &r.name, &r.title, &r.result, &r.wait, &r.notes, &r.status); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}
