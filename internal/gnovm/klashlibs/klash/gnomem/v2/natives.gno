// Copyright (C) 2026 Jae Kwon
//
// This file is part of gnosh's GnoVM machine subsystem. It is licensed under
// the GNO Network General Public License, version 6, as published by
// NewTendermint, LLC or its successor, or any later version of that License
// that the Licensor accepts as the proxy under its section 14, by a statement
// published in the canonical copy of internal/gnovm/LICENSE.md; together with
// the Additional Terms (Strong Attribution) in that file.

package gnomem

// Natives that gnosh provides. Each one that reads or changes mem panics
// unless it runs in a mem call, inside the realm klash.local/r/mem, with
// gnosh's own context, so a Mem the model's code makes or copies for itself
// can do nothing.

// caller is who runs this input: a session's id, "user" or "klash".
func caller() string

// today is the user's local date, "2026-10-29".
func today() string

// bound is the goal the user's /goal binds this input's session to; ""
// for none.
func bound() string

// record hands gnosh an Op of this call.
func record(kind, name, field, old, new, by string)

// pending is the Ops gnosh hands Apply, field by field, each with its
// call's time.
func pending() (kinds, names, fields, olds, news, bys []string, ats []int64)

// find is gnosh's index: the best matches for words, with their kinds
// ("item", "fact" or "rule"). how is "meaning" when it looked by meaning as
// well as by the words, which it does when gnosh has an embedding server;
// "words" when the server did not answer this search; "" otherwise.
func find(words string, max int) (names, kinds []string, how string)

// holding is where the journal, and this call's Ops, ever held text or its
// name form: names with their kinds, and whether statements of past inputs
// did.
func holding(text string) (names, kinds []string, statements bool)

// Two that only format, in Go, since a character at a time is slow in Gno;
// they answer anywhere.

// quote is strconv.Quote.
func quote(s string) string

// jsonString is s as a JSON string, with U+2028 and U+2029 escaped.
func jsonString(s string) string
