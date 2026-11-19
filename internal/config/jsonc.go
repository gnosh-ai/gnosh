package config

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sort"
	"strconv"
	"strings"
)

// This file is the reader of gnosh's config files. A config file is JSON
// with two allowances for people who edit by hand: // and /* */ comments,
// and a comma after the last member of an object or a list. Nothing else
// is relaxed, and that is so by construction: the comments and the
// trailing commas are blanked out, byte for byte, and what is left is read
// by the standard library's own JSON reader. What that reader refuses,
// gnosh refuses.

// jkind is the kind of a JSON value.
type jkind int

const (
	jObject jkind = iota
	jArray
	jString
	jNumber
	jBool
	jNull
)

// word is the kind in a message: "a list", "a string".
func (k jkind) word() string {
	switch k {
	case jObject:
		return "an object"
	case jArray:
		return "a list"
	case jString:
		return "a string"
	case jNumber:
		return "a number"
	case jBool:
		return "true or false"
	}
	return "null"
}

// jnode is a JSON value read from a config file, with the line it is on.
// Objects keep their keys in the file's order, repeats included, so that a
// key given twice can be reported instead of one copy winning quietly.
type jnode struct {
	kind   jkind
	line   int
	fields []jfield // an object's members
	items  []*jnode // a list's items
	str    string   // a string's value, or a number as written
	b      bool
}

// jfield is one member of a JSON object.
type jfield struct {
	key  string
	line int
	val  *jnode
}

// jsonError is a mistake in the JSON itself, with its line.
type jsonError struct {
	line int
	msg  string
	// limit reports that the text was refused for one of gnosh's own
	// limits (its size, how deep it nests) and not for its grammar.
	limit bool
}

// maxJSONDepth is how deep objects and lists may nest in a config file.
const maxJSONDepth = 64

// maxConfigBytes is the most a config file, or the JSON given to
// --settings, may hold.
const maxConfigBytes = 2 << 20

// StandardJSON returns the text of a config file as standard JSON: every
// comment and every comma after a last member is replaced by blanks, so
// that each byte that is left stands where it stood and on the line it
// stood on. Text that holds neither comes back as it is. It is for the one
// place gnosh rewrites a settings file of its own, which must read what a
// person may have written there. It does not check the JSON: the reader it
// is handed to does.
func StandardJSON(data []byte) ([]byte, error) {
	plain, jerr := plainJSON(data)
	if jerr != nil {
		return nil, fmt.Errorf("line %d: %s", jerr.line, jerr.msg)
	}
	return plain, nil
}

// plainJSON blanks out the comments and the trailing commas of a config
// file's text. It never changes a byte inside a string, and it changes
// nothing at all in text that is already standard JSON.
//
// A comment runs from // to the end of its line, or from /* to the next
// */; it may stand wherever white space may. A trailing comma is a comma
// that follows a value and is followed, white space and comments aside, by
// the bracket that closes its object or list. A comma with no value before
// it (as in [,]) is left where it is, and the JSON reader refuses it.
func plainJSON(data []byte) ([]byte, *jsonError) {
	var out []byte // a copy of data, made at the first byte that changes
	blank := func(i int) {
		if out == nil {
			out = bytes.Clone(data)
		}
		out[i] = ' '
	}
	line := 1
	last := byte(0) // the last byte that counts, outside strings and comments
	comma := -1     // where a comma stands that may turn out to be a trailing one
	for i := 0; i < len(data); {
		c := data[i]
		switch {
		case c == '\n':
			line++
			i++
		case c == ' ' || c == '\t' || c == '\r':
			i++
		case c == '"':
			// A string: everything up to the quote that closes it is the
			// string's own, a backslash and the byte after it together.
			comma = -1
			i++
			for i < len(data) && data[i] != '"' {
				if data[i] == '\\' && i+1 < len(data) {
					i++
				}
				if data[i] == '\n' {
					line++
				}
				i++
			}
			if i < len(data) {
				i++ // the closing quote
			}
			last = '"'
		case c == '/' && i+1 < len(data) && data[i+1] == '/':
			for i < len(data) && data[i] != '\n' {
				blank(i)
				i++
			}
		case c == '/' && i+1 < len(data) && data[i+1] == '*':
			end := bytes.Index(data[i+2:], []byte("*/"))
			if end < 0 {
				return nil, &jsonError{line: line, msg: "a comment that opens with /* on this line is never closed with */"}
			}
			for stop := i + 2 + end + 2; i < stop; i++ {
				if data[i] == '\n' {
					line++
				} else {
					blank(i)
				}
			}
		default:
			if comma >= 0 && (c == ']' || c == '}') {
				blank(comma)
			}
			comma = -1
			if c == ',' && last != 0 && last != '[' && last != '{' && last != ',' && last != ':' {
				comma = i
			}
			last = c
			i++
		}
	}
	if out == nil {
		return data, nil
	}
	return out, nil
}

// parseJSON reads a config file's text into a tree that remembers lines.
func parseJSON(data []byte) (*jnode, *jsonError) {
	if len(data) > maxConfigBytes {
		return nil, &jsonError{line: 1, limit: true, msg: fmt.Sprintf("it is %d bytes, and gnosh reads config of up to %d", len(data), maxConfigBytes)}
	}
	data, jerr := plainJSON(data)
	if jerr != nil {
		return nil, jerr
	}
	var starts []int // the offset at which each line after the first starts
	for i, c := range data {
		if c == '\n' {
			starts = append(starts, i+1)
		}
	}
	lineAt := func(offset int64) int {
		return 1 + sort.Search(len(starts), func(i int) bool { return int64(starts[i]) > offset })
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber()
	here := func() int { return lineAt(max(dec.InputOffset()-1, 0)) }
	fail := func(err error) *jsonError {
		var syn *json.SyntaxError
		switch {
		case errors.As(err, &syn):
			// The offset is just past the offending byte, so the byte
			// before it is the one to name the line of. A line break
			// inside a string is thereby on the line the string is on.
			pos := max(min(syn.Offset, int64(len(data)))-1, 0)
			return &jsonError{line: lineAt(pos), msg: syn.Error()}
		case errors.Is(err, io.EOF), errors.Is(err, io.ErrUnexpectedEOF):
			// The text stops short: the line to look at is the last one
			// that has anything on it.
			pos := len(data) - 1
			for pos > 0 && (data[pos] == ' ' || data[pos] == '\t' || data[pos] == '\n' || data[pos] == '\r') {
				pos--
			}
			return &jsonError{line: lineAt(int64(max(pos, 0))), msg: "it ends before the JSON does"}
		}
		return &jsonError{line: here(), msg: err.Error()}
	}

	var value func(depth int) (*jnode, *jsonError)
	value = func(depth int) (*jnode, *jsonError) {
		tok, err := dec.Token()
		if err != nil {
			return nil, fail(err)
		}
		n := &jnode{line: here()}
		switch t := tok.(type) {
		case json.Delim:
			if depth >= maxJSONDepth {
				return nil, &jsonError{line: n.line, limit: true, msg: fmt.Sprintf("it nests more than %d levels deep", maxJSONDepth)}
			}
			switch t {
			case '{':
				n.kind = jObject
				for dec.More() {
					keyTok, err := dec.Token()
					if err != nil {
						return nil, fail(err)
					}
					key, ok := keyTok.(string)
					if !ok {
						return nil, &jsonError{line: here(), msg: "an object's key is not a string"}
					}
					f := jfield{key: key, line: here()}
					var jerr *jsonError
					if f.val, jerr = value(depth + 1); jerr != nil {
						return nil, jerr
					}
					n.fields = append(n.fields, f)
				}
			case '[':
				n.kind = jArray
				for dec.More() {
					item, jerr := value(depth + 1)
					if jerr != nil {
						return nil, jerr
					}
					n.items = append(n.items, item)
				}
			default:
				return nil, &jsonError{line: n.line, msg: "a bracket closes what was never opened"}
			}
			if _, err := dec.Token(); err != nil { // the closing bracket
				return nil, fail(err)
			}
		case string:
			n.kind, n.str = jString, t
		case json.Number:
			n.kind, n.str = jNumber, t.String()
		case bool:
			n.kind, n.b = jBool, t
		default:
			n.kind = jNull
		}
		return n, nil
	}

	root, jerr := value(0)
	if jerr != nil {
		return nil, jerr
	}
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		return nil, &jsonError{line: here(), msg: "there is more text after the end of the JSON value"}
	}
	return root, nil
}

// strings returns a list's items when every one is a string.
func (n *jnode) strings() ([]*jnode, bool) {
	if n.kind != jArray {
		return nil, false
	}
	for _, it := range n.items {
		if it.kind != jString {
			return nil, false
		}
	}
	return n.items, true
}

// whole returns a number's value when it is a whole number written with
// digits alone, as 900 and -1 are and 9e2 and 900.0 are not.
func (n *jnode) whole() (int, bool) {
	if n.kind != jNumber {
		return 0, false
	}
	v, err := strconv.Atoi(n.str)
	return v, err == nil
}

// marshal writes the node back as JSON, its members in the order they
// were written.
func (n *jnode) marshal() json.RawMessage {
	var b bytes.Buffer
	n.write(&b)
	return b.Bytes()
}

func (n *jnode) write(b *bytes.Buffer) {
	switch n.kind {
	case jObject:
		b.WriteByte('{')
		for i, f := range n.fields {
			if i > 0 {
				b.WriteByte(',')
			}
			key, _ := json.Marshal(f.key)
			b.Write(key)
			b.WriteByte(':')
			f.val.write(b)
		}
		b.WriteByte('}')
	case jArray:
		b.WriteByte('[')
		for i, it := range n.items {
			if i > 0 {
				b.WriteByte(',')
			}
			it.write(b)
		}
		b.WriteByte(']')
	case jString:
		s, _ := json.Marshal(n.str)
		b.Write(s)
	case jNumber:
		b.WriteString(n.str)
	case jBool:
		b.WriteString(strconv.FormatBool(n.b))
	default:
		b.WriteString("null")
	}
}

// DuplicateKey names the first key a JSON text gives twice in one object
// ("permissions.allow"), or is "" when there is none, or when the text is
// no JSON. A settings file that holds one is not loaded, and gnosh must
// not rewrite it either: Go's reader keeps the last of the two, and the
// first would be lost unseen (the config review of 2026-11-19).
func DuplicateKey(data []byte) string {
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber()
	type level struct {
		object  bool
		seen    map[string]bool
		key     string // the key whose value is being read
		wantKey bool
	}
	var stack []*level
	ended := func() { // a value ended: its object waits for a key again
		if n := len(stack); n > 0 && stack[n-1].object {
			stack[n-1].wantKey = true
		}
	}
	for {
		tok, err := dec.Token()
		if err != nil {
			return ""
		}
		if n := len(stack); n > 0 && stack[n-1].object && stack[n-1].wantKey {
			if d, ok := tok.(json.Delim); ok && d == '}' {
				stack = stack[:n-1]
				ended()
				continue
			}
			k, _ := tok.(string)
			top := stack[n-1]
			if top.seen[k] {
				var names []string
				for _, l := range stack[:n-1] {
					if l.object {
						names = append(names, l.key)
					}
				}
				return strings.Join(append(names, k), ".")
			}
			top.seen[k], top.key, top.wantKey = true, k, false
			continue
		}
		switch t := tok.(type) {
		case json.Delim:
			switch t {
			case '{':
				stack = append(stack, &level{object: true, seen: map[string]bool{}, wantKey: true})
			case '[':
				stack = append(stack, &level{})
			case ']':
				stack = stack[:len(stack)-1]
				ended()
			}
		default:
			ended()
		}
	}
}
