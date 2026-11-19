package config

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"reflect"
	"regexp"
	"strings"
	"testing"
)

// plainOf turns what the reader gave into the plain Go value the standard
// library gives for the same text: an object as a map in which the last
// of two equal keys wins, a list as a slice, a number as it is written.
func plainOf(n *jnode) any {
	switch n.kind {
	case jObject:
		m := make(map[string]any, len(n.fields))
		for _, f := range n.fields {
			m[f.key] = plainOf(f.val)
		}
		return m
	case jArray:
		out := make([]any, len(n.items))
		for i, it := range n.items {
			out[i] = plainOf(it)
		}
		return out
	case jString:
		return n.str
	case jNumber:
		return json.Number(n.str)
	case jBool:
		return n.b
	}
	return nil
}

// standard reads text with the standard library alone: one JSON value and
// nothing after it, numbers kept as written.
func standard(data []byte) (any, bool) {
	if !json.Valid(data) {
		return nil, false
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber()
	var v any
	if err := dec.Decode(&v); err != nil {
		return nil, false
	}
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		return nil, false
	}
	return v, true
}

// depthOf is how deep the objects and lists of a value nest.
func depthOf(v any) int {
	deepest := 0
	switch x := v.(type) {
	case map[string]any:
		for _, e := range x {
			deepest = max(deepest, depthOf(e))
		}
		return deepest + 1
	case []any:
		for _, e := range x {
			deepest = max(deepest, depthOf(e))
		}
		return deepest + 1
	}
	return 0
}

// The two allowances: comments, and a comma after the last member. Each
// text reads as the standard JSON beside it.
func TestReaderAllowsCommentsAndTrailingCommas(t *testing.T) {
	tests := []struct{ name, text, same string }{
		{"a line comment", "{\n  // why\n  \"a\": 1\n}", `{"a": 1}`},
		{"a comment after a value", "{\"a\": 1 // one\n}", `{"a": 1}`},
		{"a block comment", `{"a": /* the number */ 1}`, `{"a": 1}`},
		{"a block comment over lines", "{\n/* one\n   two */\n\"a\": 1}", `{"a": 1}`},
		{"a comment before the value", "// the file\n{}", `{}`},
		{"a comment after the value", "{}\n// the end\n", `{}`},
		{"a comment with no line end", "{} // the end", `{}`},
		{"a comment between a key and its colon", `{"a" /* key */ : 1}`, `{"a": 1}`},
		{"a trailing comma in an object", `{"a": 1, "b": 2,}`, `{"a": 1, "b": 2}`},
		{"a trailing comma in a list", `[1, 2, 3,]`, `[1, 2, 3]`},
		{"nested trailing commas", `{"a": [1, {"b": 2,},],}`, `{"a": [1, {"b": 2}]}`},
		{"a trailing comma after a string", `["a",]`, `["a"]`},
		{"a trailing comma after a word", `[true, null,]`, `[true, null]`},
		{"a trailing comma, then white space", "[1,\n\t ]", `[1]`},
		{"a trailing comma, then a line comment", "[1, // one\n]", `[1]`},
		{"a trailing comma, then a block comment", `[1, /* one */ ]`, `[1]`},
		{"a comment between a value and its comma", `[1 /* one */ ,]`, `[1]`},
		{"comment marks inside a string are the string's", `{"url": "http://x/*y*/z // w"}`, `{"url": "http://x/*y*/z // w"}`},
		{"a comma and a bracket inside a string", `{"a": ",]", "b": ",}"}`, `{"a": ",]", "b": ",}"}`},
		{"an escaped quote before a comment mark", `{"a": "\"// not a comment"}`, `{"a": "\"// not a comment"}`},
		{"a string that ends in a backslash", `{"a": "\\"} // c`, `{"a": "\\"}`},
		{"windows line ends", "{\r\n// c\r\n\"a\": 1,\r\n}\r\n", `{"a": 1}`},
		{"a block comment that begins /*/", `[1 /*/ 2 */ ]`, `[1]`},
		{"stars in a block comment", `[/* ** */ 1]`, `[1]`},
		{"a line comment holding a block opener", "[1 // /* not open\n]", `[1]`},
		{"a block comment holding a line opener", "[1 /* // */ ,]", `[1]`},
		{"a comment that holds quotes", "[1 // \"x\n, \"y\" /* \" */ ]", `[1, "y"]`},
		{"everything at once", "{ // a\n\"p\": {\"q\": [1, /* b */ 2,], /* c */ },\n}// d", `{"p": {"q": [1, 2]}}`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			root, jerr := parseJSON([]byte(tt.text))
			if jerr != nil {
				t.Fatalf("refused on line %d: %s", jerr.line, jerr.msg)
			}
			want, ok := standard([]byte(tt.same))
			if !ok {
				t.Fatalf("the test's own JSON is not JSON: %s", tt.same)
			}
			if got := plainOf(root); !reflect.DeepEqual(got, want) {
				t.Errorf("read %#v, want %#v", got, want)
			}
		})
	}
}

// Nothing else is relaxed: what the standard library refuses once the
// comments and trailing commas are out, gnosh refuses, on the line it is
// on.
func TestReaderRefusesWhatIsNotJSON(t *testing.T) {
	tests := []struct {
		name, text string
		line       int
		piece      string
	}{
		{"single quotes", "{\n'a': 1}", 2, "invalid character"},
		{"a key without quotes", "{\na: 1}", 2, "invalid character"},
		{"a comma with nothing before it", "[\n,]", 2, "invalid character"},
		{"a comma with nothing before it, in an object", "{,}", 1, "invalid character"},
		{"two commas", "[1,,]", 1, "invalid character"},
		{"a comma before the first member", `{,"a": 1}`, 1, "invalid character"},
		{"a comma after a colon", `{"a":,}`, 1, "invalid character"},
		{"a comma at the top", "{},", 1, ""},
		{"only a comma", ",", 1, ""},
		{"a hash comment", "{\n# no\n}", 2, "invalid character"},
		{"a comment that is never closed", "{\n/* open\n\"a\": 1}", 2, "never closed"},
		{"a block comment closed twice", "[1 /* a */ */ ]", 1, "invalid character"},
		{"one slash", `{"a": 1 / 2}`, 1, "invalid character"},
		{"a comment inside a number", "[12/**/34]", 1, ""},
		{"a comment inside a word", "[tr/**/ue]", 1, ""},
		{"two values", "{}\n{}", 2, "more text after the end"},
		{"two values, a comment between", "{} /* and */ []", 1, "more text after the end"},
		{"nothing at all", "", 1, "ends before"},
		{"only a comment", "// nothing\n", 1, "ends before"},
		{"an object left open", "{\n\"a\": 1,\n", 2, ""},
		{"NaN", "[NaN]", 1, "invalid character"},
		{"a hex number", "[0x10]", 1, "invalid character"},
		{"a leading plus", "[+1]", 1, "invalid character"},
		{"a leading zero", "[01]", 1, "invalid character"},
		{"a tab inside a string", "[\"a\tb\"]", 1, "invalid character"},
		{"a line break inside a string", "[\"a\nb\"]", 1, "invalid character"},
		{"an escape JSON does not have", `["\x41"]`, 1, "invalid escape"},
		{"a byte order mark", "\xef\xbb\xbf{}", 1, "invalid character"},
		{"a word JSON does not have", "[undefined]", 1, "invalid character"},
		{"a missing colon", `{"a" 1}`, 1, "invalid character"},
		{"a missing comma", `[1 2]`, 1, "invalid character"},
		{"brackets that do not match", `[1}`, 1, "invalid character"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			root, jerr := parseJSON([]byte(tt.text))
			if jerr == nil {
				t.Fatalf("it was read, as %#v", plainOf(root))
			}
			if jerr.line != tt.line || !strings.Contains(jerr.msg, tt.piece) || jerr.limit {
				t.Errorf("refused on line %d with %q (limit %v); want line %d and %q", jerr.line, jerr.msg, jerr.limit, tt.line, tt.piece)
			}
			// The standard library agrees about what is left.
			if plain, err := StandardJSON([]byte(tt.text)); err == nil && json.Valid(plain) {
				t.Errorf("the standard library reads what is left of it: %q", plain)
			}
		})
	}
}

// Every value keeps the line it is on, comments and all.
func TestReaderLines(t *testing.T) {
	text := "{\n  // one\n  \"a\": [\n    1, /* two\n    lines */ 2,\n  ],\n  \"b\": {\"c\": true}, \"d\":\n    \"x\"\n}"
	root, jerr := parseJSON([]byte(text))
	if jerr != nil {
		t.Fatalf("refused on line %d: %s", jerr.line, jerr.msg)
	}
	a, b, d := root.fields[0], root.fields[1], root.fields[2]
	got := []int{root.line, a.line, a.val.line, a.val.items[0].line, a.val.items[1].line, b.line, b.val.fields[0].line, d.line, d.val.line}
	want := []int{1, 3, 3, 4, 5, 7, 7, 7, 8}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("lines %v, want %v", got, want)
	}
}

// How deep a file may nest and how much it may hold: past either it is
// refused in words, at once, however large or deep it is.
func TestReaderLimits(t *testing.T) {
	nested := func(n int) []byte { return []byte(strings.Repeat("[", n) + strings.Repeat("]", n)) }
	if _, jerr := parseJSON(nested(maxJSONDepth)); jerr != nil {
		t.Errorf("%d levels are refused: %s", maxJSONDepth, jerr.msg)
	}
	for _, text := range [][]byte{
		nested(maxJSONDepth + 1),
		[]byte(strings.Repeat(`{"a":`, maxJSONDepth+1) + "1" + strings.Repeat("}", maxJSONDepth+1)),
		[]byte(strings.Repeat("[", 1<<20)),
		[]byte(strings.Repeat(`{"a":[`, 300000)),
	} {
		_, jerr := parseJSON(text)
		if jerr == nil || !jerr.limit || !strings.Contains(jerr.msg, "nests more than 64 levels deep") {
			t.Errorf("text %d levels deep: %+v", bytes.Count(text, []byte("["))+bytes.Count(text, []byte("{")), jerr)
		}
	}
	big := append(bytes.Repeat([]byte(" "), maxConfigBytes), []byte("{}")...)
	if _, jerr := parseJSON(big); jerr == nil || !jerr.limit || !strings.Contains(jerr.msg, "bytes") {
		t.Errorf("a text of %d bytes: %+v", len(big), jerr)
	}
	if _, jerr := parseJSON(big[2:]); jerr != nil {
		t.Errorf("a text of exactly %d bytes is refused: %s", maxConfigBytes, jerr.msg)
	}
	// Through Load: the JSON given to --settings is held to the same size.
	in := inputs(newFS())
	in.Flags.Settings = "{" + strings.Repeat(" ", maxConfigBytes) + "}"
	_, problems := load(t, in)
	wantProblem(t, problems, Error, "--settings", 1, "bytes", "none of it is in force")
	in.Flags.Settings = strings.Repeat(`{"env":`, 100) + "{}" + strings.Repeat("}", 100)
	_, problems = load(t, in)
	wantProblem(t, problems, Error, "--settings", 1, "nests more than 64 levels deep")
}

// StandardJSON blanks the comments and the trailing commas and moves
// nothing: what is left stands where it stood.
func TestStandardJSON(t *testing.T) {
	plain := []byte(`{"a": [1, 2], "b": "// not a comment"}`)
	got, err := StandardJSON(plain)
	if err != nil || &got[0] != &plain[0] {
		t.Errorf("standard JSON comes back as it is: %q, %v", got, err)
	}
	text := "{\n  // why\n  \"a\": 1, /* one\n  more */\n  \"b\": [2,],\n}"
	got, err = StandardJSON([]byte(text))
	want := "{\n        \n  \"a\": 1,       \n         \n  \"b\": [2 ] \n}"
	if err != nil || string(got) != want {
		t.Errorf("StandardJSON:\n got %q\nwant %q (%v)", got, want, err)
	}
	if !json.Valid(got) {
		t.Errorf("what is left is not standard JSON: %q", got)
	}
	if _, err := StandardJSON([]byte("{\n\n /* never closed")); err == nil || !strings.Contains(err.Error(), "line 3") {
		t.Errorf("a comment left open: %v", err)
	}
}

// trailingComma finds a comma that only white space parts from a closing
// bracket, wherever it stands.
var trailingComma = regexp.MustCompile(`,[ \t\r\n]*[\]}]`)

// FuzzReader holds the reader to the standard library's. Whatever the
// text:
//
//   - reading it never panics, and a refusal names a line of the text;
//   - what the standard library reads, gnosh reads to the same values,
//     unless it nests deeper than gnosh allows, which gnosh says;
//   - what gnosh reads and the standard library does not holds a comment
//     or a trailing comma, and with those blanked out it is standard JSON
//     with the same values. So text without either is read by the one
//     exactly when it is read by the other;
//   - blanking moves nothing: the text keeps its length and its lines, and
//     standard JSON is not changed at all.
func FuzzReader(f *testing.F) {
	for _, seed := range []string{
		`{}`, `[]`, `"x"`, `null`, `true`, `-1.5e3`, ``, `{`, `{"a":`, "{\n\"env\": {},\n}", `[1,]`, `[,]`, `{,}`, `[1,,]`,
		"{ // c\n\"a\": [1, /* b */ 2,], }", `{"a": "//", "b": "/* */", "c": ",]"}`, `/* open`, `/**/`, `//`, `/`, `/*/`, `[1 /*/ 2 */ ]`,
		`{"a": 1, "a": 2}`, `{"a": {"b": 1}, "a": {"c": 2}}`, "\"a\\u0000\\ud800\\udc00\\ud800\"", "\"\xff\xfe\"", "{\"\xff\": 1}",
		`[1e999999, -0, 0.0, 1E+2]`, `[01]`, `[+1]`, `[.5]`, `[1.]`, `'a'`, `{a: 1}`, "\xef\xbb\xbf{}", "[\"a\tb\"]", "[\"a\\\nb\"]",
		strings.Repeat("[", 70) + strings.Repeat("]", 70), strings.Repeat(`{"a":`, 70) + "1" + strings.Repeat("}", 70),
		strings.Repeat("[", 64) + strings.Repeat("]", 64), `{"a": "\"// x", "b": "\\"} // c`, "[1,\r\n]\r\n", `[1 , /* a */ // b` + "\n]",
		`{"providers": {"p": {"type": "llamacpp", "baseUrl": ["http://127.0.0.1:1",],},}, "model": "p/m", /* x */ }`,
		" \t\r\n", `[] []`, `{} ,`, `[tr/**/ue]`, `[1/**/2]`, `"a" // "b"`, `"/*" "*/"`, `["\\", "\\\\", "\\\""]`, `[ "a\\" , ]`, `["a\\", ]`,
	} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, text string) {
		data := []byte(text)
		lines := strings.Count(text, "\n") + 1

		root, jerr := parseJSON(data)
		if jerr != nil && (jerr.line < 1 || jerr.line > lines || strings.TrimSpace(jerr.msg) == "") {
			t.Fatalf("a refusal without its line or its words: %+v\nfor %q", jerr, text)
		}
		want, valid := standard(data)
		switch {
		case valid && depthOf(want) <= maxJSONDepth:
			if jerr != nil {
				t.Fatalf("the standard library reads it and gnosh refuses it (line %d: %s):\n%q", jerr.line, jerr.msg, text)
			}
			if got := plainOf(root); !reflect.DeepEqual(got, want) {
				t.Fatalf("gnosh read %#v and the standard library %#v\nfor %q", got, want, text)
			}
		case valid:
			if jerr == nil || !jerr.limit {
				t.Fatalf("text %d levels deep was not refused for its depth: %+v", depthOf(want), jerr)
			}
		case jerr == nil:
			if !strings.Contains(text, "//") && !strings.Contains(text, "/*") && !trailingComma.MatchString(text) {
				t.Fatalf("gnosh read what the standard library refuses, and it holds neither a comment nor a trailing comma:\n%q", text)
			}
			left, err := StandardJSON(data)
			if err != nil {
				t.Fatalf("gnosh read it and StandardJSON refuses it: %v\nfor %q", err, text)
			}
			same, ok := standard(left)
			if !ok {
				t.Fatalf("gnosh read it, and with its comments and trailing commas blanked it is not standard JSON:\n%q\n%q", text, left)
			}
			if got := plainOf(root); !reflect.DeepEqual(got, same) {
				t.Fatalf("gnosh read %#v, and the standard library reads what is left as %#v\nfor %q", got, same, text)
			}
		}

		left, perr := plainJSON(data)
		if perr != nil {
			if perr.line < 1 || perr.line > lines || !strings.Contains(text, "/*") {
				t.Fatalf("blanking refused text with no comment left open: %+v\nfor %q", perr, text)
			}
			return
		}
		if len(left) != len(data) {
			t.Fatalf("blanking changed the length from %d to %d\nfor %q", len(data), len(left), text)
		}
		for i := range data {
			if left[i] != data[i] && (left[i] != ' ' || data[i] == '\n') {
				t.Fatalf("blanking changed byte %d from %q to %q\nfor %q", i, data[i], left[i], text)
			}
		}
		if json.Valid(data) && !bytes.Equal(left, data) {
			t.Fatalf("blanking changed standard JSON:\n%q\n%q", text, left)
		}
	})
}

// DuplicateKey names the first key given twice in one object, with the
// keys of the objects around it; keys alike in different objects are no
// duplicate (the config review of 2026-11-19: gnosh's rewrite of the local
// settings file read such a file last-wins, and lost the first).
func TestDuplicateKey(t *testing.T) {
	for text, want := range map[string]string{
		`{"a": 1, "a": 2}`: "a",
		`{"permissions": {"allow": ["A"], "deny": [], "allow": ["B"]}}`: "permissions.allow",
		`{"a": [{"x": 1}, {"x": 2}], "b": {"x": 1}, "c": {"x": 2}}`:     "",
		`{"a": [{"x": 1, "x": 2}]}`:                                     "a.x",
		`{"a": {"b": {"c": 1}}, "a2": {"b": {"c": 1, "c": 1}}}`:         "a2.b.c",
		`not json`: "",
		`{}`:       "",
	} {
		if got := DuplicateKey([]byte(text)); got != want {
			t.Errorf("DuplicateKey(%s) = %q, want %q", text, got, want)
		}
	}
}
