package mcp

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

// This file changes the files servers are configured in, for `gnosh mcp
// add` and `remove`: one entry at a time, with the rest of the file, and
// the order of its keys, left as they were.

// Place is where one server's entry is kept.
type Place struct {
	// Path is the file.
	Path string
	// Project, when set, is the project root among whose own servers the
	// entry is (the local scope, under "projects" in the user's file).
	// Empty means the file's own "mcpServers".
	Project string
}

// goodName is what a server's name may be made of, so that its tools'
// names (mcp__<server>__<tool>) are the name as it was typed.
var goodName = regexp.MustCompile(`^[A-Za-z0-9_-]+$`)

// CheckName says what is wrong with a name for a new server, or "".
func CheckName(name string) string {
	if !goodName.MatchString(name) {
		return fmt.Sprintf("a server's name is made of letters, digits, - and _ (up to %d of them), since its tools are named after it", maxServerName)
	}
	return blurredName(name)
}

// Lookup returns a server's entry in a place, compacted, or nil when it
// has none.
func Lookup(pl Place, name string) (json.RawMessage, error) {
	root, err := readPlace(pl.Path)
	if err != nil {
		return nil, err
	}
	chain, _, err := descend(root, pl, false)
	if err != nil || chain == nil {
		return nil, err
	}
	v, ok := chain[len(chain)-1].get(name)
	if !ok {
		return nil, nil
	}
	var b bytes.Buffer
	if err := json.Compact(&b, v); err != nil {
		return nil, err
	}
	return b.Bytes(), nil
}

// Names lists the servers of a place.
func Names(pl Place) ([]string, error) {
	root, err := readPlace(pl.Path)
	if err != nil {
		return nil, err
	}
	chain, _, err := descend(root, pl, false)
	if err != nil || chain == nil {
		return nil, err
	}
	return append([]string(nil), chain[len(chain)-1].keys...), nil
}

// Put adds a server's entry, or replaces it. A file that is not there is
// made with mode (and its folder, readable by its owner alone); a file that
// is there keeps its mode, and when it is a link, the file it leads to is
// the one written.
func Put(pl Place, name string, entry json.RawMessage, mode fs.FileMode) error {
	var compact bytes.Buffer
	if err := json.Compact(&compact, entry); err != nil {
		return fmt.Errorf("the entry is not JSON: %w", err)
	}
	// A file whose servers were written at its top level, beside
	// "mcpServers" instead of inside it, is not added to: one more shape
	// in it would hide that its servers are not read.
	if root, err := readPlace(pl.Path); err == nil {
		if names := strays(root.vals); len(names) > 0 {
			return fmt.Errorf("%s holds %s, outside \"mcpServers\", where no server is read. Move %s inside \"mcpServers\": {...} first; the file was left as it is", pl.Path, strayText(names), itOrThem(len(names)))
		}
	}
	return change(pl, mode, func(servers *object) bool {
		servers.put(name, compact.Bytes())
		return true
	})
}

// Delete takes a server's entry out, and reports whether it was there.
func Delete(pl Place, name string) (bool, error) {
	found := false
	err := change(pl, 0o600, func(servers *object) bool {
		found = servers.remove(name)
		return found
	})
	return found, err
}

// change reads a place's file, lets do change its servers, and writes the
// file back when do says it changed them.
func change(pl Place, mode fs.FileMode, do func(*object) bool) error {
	path := pl.Path
	if real, err := filepath.EvalSymlinks(path); err == nil {
		path = real
	}
	root, err := readPlace(path)
	if err != nil {
		return err
	}
	chain, keys, err := descend(root, pl, true)
	if err != nil {
		return err
	}
	if !do(chain[len(chain)-1]) {
		return nil
	}
	// Back up the tree, each object with its changed child put in it. Under
	// "projects", an object left empty is taken out instead: the project's
	// servers, the project, and "projects" itself.
	for i := len(chain) - 1; i > 0; i-- {
		if pl.Project != "" && len(chain[i].keys) == 0 {
			chain[i-1].remove(keys[i-1])
		} else {
			chain[i-1].put(keys[i-1], chain[i].encode())
		}
	}
	var out bytes.Buffer
	if err := json.Indent(&out, root.encode(), "", "  "); err != nil {
		return err
	}
	out.WriteByte('\n')
	return writeAtomically(path, out.Bytes(), mode)
}

// readPlace reads a file of servers as an object. A file that is not
// there, or that holds nothing but blanks, is an empty object.
func readPlace(path string) (*object, error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return newObject(), nil
	}
	if err != nil {
		return nil, err
	}
	if len(bytes.TrimSpace(data)) == 0 {
		return newObject(), nil
	}
	o, err := readObject(data)
	if err != nil {
		return nil, fmt.Errorf("%s is not a JSON object (%v), so it was left as it is", path, err)
	}
	return o, nil
}

// descend finds the servers object of a place in a file's root object. It
// returns the objects on the way, the root first and the servers last,
// and the key each one after the root is under. With create, an object
// that is missing on the way is made; without it, a missing one means nil.
func descend(root *object, pl Place, create bool) ([]*object, []string, error) {
	keys := []string{"mcpServers"}
	if pl.Project != "" {
		keys = []string{"projects", pl.Project, "mcpServers"}
	}
	chain := []*object{root}
	for _, key := range keys {
		next, err := chain[len(chain)-1].object(key)
		if err != nil {
			return nil, nil, err
		}
		if next == nil {
			if !create {
				return nil, nil, nil
			}
			next = newObject()
		}
		chain = append(chain, next)
	}
	return chain, keys, nil
}

// writeAtomically writes a file by renaming a full copy over it, so that it
// is never seen half written.
func writeAtomically(path string, data []byte, mode fs.FileMode) error {
	if info, err := os.Stat(path); err == nil {
		mode = info.Mode().Perm()
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, "."+filepath.Base(path)+".*")
	if err != nil {
		return err
	}
	name := tmp.Name()
	defer os.Remove(name)
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Chmod(mode); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(name, path)
}

// Entry makes the entry `gnosh mcp add` writes: for a server gnosh starts,
// its command, arguments and environment (env is KEY=value pairs); for one
// at a URL, the URL and headers ("Name: value").
func Entry(transport, target string, args, env, headers []string) (json.RawMessage, error) {
	pairs := func(list []string, sep, what string) (map[string]string, error) {
		if len(list) == 0 {
			return nil, nil
		}
		m := map[string]string{}
		for _, item := range list {
			k, v, ok := strings.Cut(item, sep)
			if k = strings.TrimSpace(k); !ok || k == "" {
				return nil, fmt.Errorf("%q is not %s", item, what)
			}
			if sep == ":" {
				v = strings.TrimSpace(v)
			}
			m[k] = v
		}
		return m, nil
	}
	switch transport {
	case "", "stdio":
		if strings.TrimSpace(target) == "" {
			return nil, errors.New("the command to start the server is empty")
		}
		if len(headers) > 0 {
			return nil, errors.New("-H gives the headers of a server at a URL (-t http); a server gnosh starts takes -e")
		}
		e, err := pairs(env, "=", "KEY=value")
		if err != nil {
			return nil, err
		}
		if args == nil {
			args = []string{}
		}
		return json.Marshal(struct {
			Type    string            `json:"type"`
			Command string            `json:"command"`
			Args    []string          `json:"args"`
			Env     map[string]string `json:"env,omitempty"`
		}{"stdio", target, args, e})
	case "http":
		if len(args) > 0 {
			return nil, fmt.Errorf("a server at a URL takes nothing after its URL, and %q came after it", strings.Join(args, " "))
		}
		if len(env) > 0 {
			return nil, errors.New("-e gives the environment of a server gnosh starts; a server at a URL takes -H")
		}
		if u, err := url.Parse(target); err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
			return nil, fmt.Errorf("%q is not a URL with http:// or https:// in front", target)
		}
		h, err := pairs(headers, ":", `"Name: value"`)
		if err != nil {
			return nil, err
		}
		return json.Marshal(struct {
			Type    string            `json:"type"`
			URL     string            `json:"url"`
			Headers map[string]string `json:"headers,omitempty"`
		}{"http", target, h})
	}
	return nil, fmt.Errorf("%q is not a transport gnosh speaks; it speaks stdio and http", transport)
}

// CheckEntry says what is wrong with an entry, as LoadConfig would see it,
// or returns nil when it would be used. Variables are not looked up: one
// that is not set now may be by the time the server starts.
func CheckEntry(name string, entry json.RawMessage) error {
	var probe map[string]any
	if err := json.Unmarshal(entry, &probe); err != nil {
		return fmt.Errorf("it is not a JSON object: %v", err)
	}
	set := func(string) (string, bool) { return "x", true }
	_, probs := parseConfig("", "", map[string]json.RawMessage{name: entry}, "", set, nil)
	if len(probs) > 0 {
		var msgs []string
		for _, p := range probs {
			msgs = append(msgs, p.Message)
		}
		sort.Strings(msgs)
		return errors.New(strings.Join(msgs, "; "))
	}
	return nil
}

// object is a JSON object whose keys keep their order.
type object struct {
	keys []string
	vals map[string]json.RawMessage
}

func newObject() *object { return &object{vals: map[string]json.RawMessage{}} }

// readObject reads one JSON object, and nothing after it.
func readObject(data []byte) (*object, error) {
	dec := json.NewDecoder(bytes.NewReader(data))
	tok, err := dec.Token()
	if err != nil {
		return nil, err
	}
	if d, ok := tok.(json.Delim); !ok || d != '{' {
		return nil, errors.New("it does not begin with {")
	}
	o := newObject()
	for dec.More() {
		tok, err := dec.Token()
		if err != nil {
			return nil, err
		}
		key, ok := tok.(string)
		if !ok {
			return nil, errors.New("a key is not a string")
		}
		var v json.RawMessage
		if err := dec.Decode(&v); err != nil {
			return nil, err
		}
		o.put(key, v)
	}
	if _, err := dec.Token(); err != nil {
		return nil, err
	}
	if _, err := dec.Token(); err != io.EOF {
		return nil, errors.New("something follows the object")
	}
	return o, nil
}

func (o *object) get(key string) (json.RawMessage, bool) {
	v, ok := o.vals[key]
	return v, ok
}

// object returns the object under key: nil when there is none, and an
// error when what is there is not an object.
func (o *object) object(key string) (*object, error) {
	v, ok := o.vals[key]
	if !ok || string(bytes.TrimSpace(v)) == "null" {
		if !ok {
			return nil, nil
		}
		return newObject(), nil
	}
	inner, err := readObject(v)
	if err != nil {
		return nil, fmt.Errorf("%q is not an object (%v), so the file was left as it is", key, err)
	}
	return inner, nil
}

// put sets key, in its place if it is there and else at the end.
func (o *object) put(key string, v json.RawMessage) {
	if _, ok := o.vals[key]; !ok {
		o.keys = append(o.keys, key)
	}
	o.vals[key] = v
}

func (o *object) remove(key string) bool {
	if _, ok := o.vals[key]; !ok {
		return false
	}
	delete(o.vals, key)
	for i, k := range o.keys {
		if k == key {
			o.keys = append(o.keys[:i], o.keys[i+1:]...)
			break
		}
	}
	return true
}

// encode writes the object compactly.
func (o *object) encode() json.RawMessage {
	var b bytes.Buffer
	b.WriteByte('{')
	for i, k := range o.keys {
		if i > 0 {
			b.WriteByte(',')
		}
		key, _ := json.Marshal(k)
		b.Write(key)
		b.WriteByte(':')
		if err := json.Compact(&b, o.vals[k]); err != nil {
			b.WriteString("null")
		}
	}
	b.WriteByte('}')
	return b.Bytes()
}
