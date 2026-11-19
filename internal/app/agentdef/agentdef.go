// Package agentdef reads the definitions of sub-agents: what kinds of
// sub-agent the Agent tool can start, and what each starts with.
//
// A definition is a Markdown file, in the form Claude Code uses, so that a
// person's own agent files load unchanged: a few settings between two
// lines of "---" at the top (the frontmatter), and below them the
// sub-agent's whole system prompt.
//
//	---
//	name: code-reviewer
//	description: Reviews code for quality and best practices
//	tools: Read, Glob, Grep
//	---
//
//	You are a code reviewer. ...
//
// gnosh's own two kinds, general-purpose and Explore, are definitions of
// the same form, kept in this package (Builtin). A person's file with the
// same name takes the place of one of them.
//
// The package only reads. It runs nothing, and what a definition asks for
// is the caller's to grant or refuse.
package agentdef

import (
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strconv"
	"strings"
	"unicode/utf8"
)

// Def is one kind of sub-agent.
type Def struct {
	// Name is what the Agent tool's subagent_type names, and what hooks
	// see as agent_type. Description says when to hand a task to it.
	Name        string
	Description string
	// Prompt is the body of the file: the sub-agent's whole system prompt.
	Prompt string

	// Tools lists the tools the sub-agent may use, by name, when ToolsSet
	// is true. Without it the sub-agent gets every tool a sub-agent may
	// have. Disallowed lists tools to take away. A name written with
	// something in parentheses after it is kept here as the name alone.
	Tools      []string
	ToolsSet   bool
	Disallowed []string

	// Model is the model the definition asks for: "" or "inherit" for the
	// model of the session that starts the sub-agent.
	Model string
	// PermissionMode is the permission mode the definition asks for, or
	// "" to run in the mode of the session that starts it.
	PermissionMode string
	// MaxTurns is the most replies the sub-agent may write, or zero for
	// no such limit.
	MaxTurns int
	// OmitInstructions says the sub-agent starts without the person's and
	// the project's instruction files (the field omitClaudeMd).
	OmitInstructions bool
	// Background says the definition asks to be run in the background
	// always.
	Background bool
	// AgentTypes are the kinds an Agent(kind, ...) entry of Tools lets the
	// agent start, when it runs as the session (--agent): nil lets it
	// start any. Inside an ordinary sub-agent the list is not used.
	AgentTypes []string
	// Isolation is "worktree" for an agent that works in a git work tree
	// of its own (isolation), or "".
	Isolation string
	// InheritModel says Model was written as inherit: the session's model,
	// whatever else would name one.
	InheritModel bool
	// InitialPrompt is the prompt a session that runs as the agent
	// (--agent) starts with, as its first turn (initialPrompt). A
	// sub-agent has its task instead.
	InitialPrompt string
	// Hooks is the definition's own hooks block (hooks), as JSON in the
	// shape of the settings' hooks: in force while the agent runs, for its
	// events alone.
	Hooks json.RawMessage
	// Skills names the skills whose whole text the agent starts with
	// (skills), and Effort the thinking level it asks for (effort): a
	// level of its model, max, or auto.
	Skills []string
	Effort string
	// MCPServers are the MCP servers the agent is given (mcpServers), in
	// the order written: each a server the session has, by its Name, or
	// one of its own, Inline (an entry in the shape .mcp.json writes one,
	// under Name), connected while the agent runs.
	MCPServers []MCPServer
	// Other lists the fields of the frontmatter that change what the
	// sub-agent is or may do and that this package only notes: memory. A
	// caller that cannot honour one must say so, and not run the agent as
	// if the field were not there.
	Other []string

	// Scope is "built-in", "user" or "project", and Source the file the
	// definition was read from (empty for a built-in).
	Scope  string
	Source string
}

// MCPServer is one entry of an agent's mcpServers: a server of the
// session's, by name, or one of the agent's own, Inline.
type MCPServer struct {
	Name   string
	Inline json.RawMessage
}

// mcpServers reads an agent's mcpServers, as JSON: a list whose entries are
// each a server's name, or an object holding one server under its name.
// The reason says what is wrong, as the end of a sentence about the field.
func mcpServers(raw json.RawMessage) ([]MCPServer, string) {
	var items []json.RawMessage
	if err := json.Unmarshal(raw, &items); err != nil {
		return nil, "is not a list of servers' names and servers"
	}
	var out []MCPServer
	seen := map[string]bool{}
	add := func(m MCPServer) string {
		if seen[m.Name] {
			return fmt.Sprintf("names the server %q twice", m.Name)
		}
		seen[m.Name] = true
		out = append(out, m)
		return ""
	}
	for _, item := range items {
		var name string
		if json.Unmarshal(item, &name) == nil {
			if name = strings.TrimSpace(name); name == "" {
				return nil, "has an entry with no name"
			}
			if strings.ContainsAny(name, ": \t") {
				// (A server of its own written on one line, "- name: x".)
				return nil, fmt.Sprintf("has the entry %q, which is not a server's name: a server of the agent's own is written as its name and, indented under it, its settings", name)
			}
			if why := add(MCPServer{Name: name}); why != "" {
				return nil, why
			}
			continue
		}
		var one map[string]json.RawMessage
		if json.Unmarshal(item, &one) != nil || len(one) != 1 {
			return nil, "has an entry that is neither a server's name nor one server written under its name"
		}
		for name, server := range one {
			var fields map[string]json.RawMessage
			if strings.TrimSpace(name) == "" || json.Unmarshal(server, &fields) != nil || fields == nil {
				return nil, fmt.Sprintf("has the server %q, which is not written as an object of its settings", name)
			}
			if why := add(MCPServer{Name: name, Inline: server}); why != "" {
				return nil, why
			}
		}
	}
	return out, ""
}

// Skipped is a file that holds no usable definition, with the reason.
type Skipped struct {
	Path   string
	Reason string
}

//go:embed builtin/*.md
var builtinFiles embed.FS

// Builtin returns gnosh's own kinds of sub-agent: general-purpose, which
// has every tool, and Explore and Plan, which only read and search: one
// answers a question about the code, the other gathers what a plan
// needs.
func Builtin() []Def {
	var out []Def
	entries, _ := fs.ReadDir(builtinFiles, "builtin")
	for _, e := range entries {
		data, err := fs.ReadFile(builtinFiles, "builtin/"+e.Name())
		if err != nil {
			continue
		}
		d, reason := Parse(string(data))
		if reason != "" {
			panic("agentdef: the built-in definition " + e.Name() + " cannot be read: " + reason)
		}
		d.Scope = "built-in"
		out = append(out, d)
	}
	return out
}

// DefaultType is the kind of sub-agent a call gets that names none.
const DefaultType = "general-purpose"

// Set is the kinds of sub-agent there are for one session.
type Set struct {
	defs    []Def
	skipped []Skipped
}

// WithoutBuiltin returns the set without gnosh's own definitions of the
// names given; a person's or a project's definition of the same name
// stays.
func (s *Set) WithoutBuiltin(names ...string) *Set {
	out := &Set{skipped: s.skipped}
	for _, d := range s.defs {
		if d.Scope == "built-in" && slices.Contains(names, d.Name) {
			continue
		}
		out.defs = append(out.defs, d)
	}
	return out
}

// Load is LoadFrom with one directory for the person's own definitions,
// or none when userDir is empty.
func Load(userDir string, projectDirs []string) *Set {
	if userDir == "" {
		return LoadFrom(nil, projectDirs)
	}
	return LoadFrom([]string{userDir}, projectDirs)
}

// LoadFrom puts the definitions together, the more specific over the more
// general where two share a name: the project's (projectDirs, the
// directory closest to the working directory first), then the person's
// (userDirs), then gnosh's own. Within either list an earlier directory
// wins over a later one, which is how gnosh's own folders come before
// Claude Code's. Each directory is searched with every folder below it; a
// folder is only a way to keep files apart, and the name in the file is
// what counts. A directory that is not there holds nothing. Two files of
// one directory tree with the same name: the first by path is the one
// that is used, and the other is listed as skipped.
func LoadFrom(userDirs, projectDirs []string) *Set {
	s := &Set{}
	have := map[string]bool{}
	add := func(dir, scope string) {
		if dir == "" {
			return
		}
		var files []string
		_ = filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				if d != nil && d.IsDir() {
					return fs.SkipDir
				}
				return nil
			}
			if !d.IsDir() && strings.HasSuffix(d.Name(), ".md") {
				files = append(files, path)
			}
			return nil
		})
		sort.Strings(files)
		inTree := map[string]string{}
		for _, path := range files {
			data, err := readSmall(path)
			if err != nil {
				s.skipped = append(s.skipped, Skipped{path, err.Error()})
				continue
			}
			d, reason := Parse(data)
			if reason != "" {
				s.skipped = append(s.skipped, Skipped{path, reason})
				continue
			}
			if first, dup := inTree[d.Name]; dup {
				s.skipped = append(s.skipped, Skipped{path, fmt.Sprintf("%s also defines an agent named %q, and that one is used", first, d.Name)})
				continue
			}
			inTree[d.Name] = path
			if have[d.Name] {
				continue // a more specific definition has the name
			}
			d.Scope, d.Source = scope, path
			s.defs = append(s.defs, d)
		}
		for name := range inTree {
			have[name] = true
		}
	}
	for _, dir := range projectDirs {
		add(dir, "project")
	}
	for _, dir := range userDirs {
		add(dir, "user")
	}
	for _, d := range Builtin() {
		if !have[d.Name] {
			have[d.Name] = true
			s.defs = append(s.defs, d)
		}
	}
	return s
}

// maxFile is the largest definition file that is read.
const maxFile = 1 << 20

func readSmall(path string) (string, error) {
	info, err := os.Stat(path)
	if err != nil {
		return "", fmt.Errorf("it could not be read (%v)", err)
	}
	if !info.Mode().IsRegular() {
		// (A link a repository holds may lead to /dev/zero, a terminal
		// or a pipe: reading one would never end.)
		return "", fmt.Errorf("it is not a plain file")
	}
	if info.Size() > maxFile {
		return "", fmt.Errorf("it is larger than %d bytes, which no agent definition is", maxFile)
	}
	f, err := os.Open(path)
	if err != nil {
		return "", fmt.Errorf("it could not be read (%v)", err)
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, maxFile+1))
	switch {
	case err != nil:
		return "", fmt.Errorf("it could not be read (%v)", err)
	case len(data) > maxFile:
		return "", fmt.Errorf("it is larger than %d bytes, which no agent definition is", maxFile)
	case !utf8.Valid(data):
		return "", fmt.Errorf("it is not text")
	}
	return string(data), nil
}

// Get returns the definition with that name.
func (s *Set) Get(name string) (Def, bool) {
	for _, d := range s.defs {
		if d.Name == name {
			return d, true
		}
	}
	return Def{}, false
}

// All returns every definition: gnosh's own first (the general kind
// before the other), then the person's and the project's, each group by
// name.
func (s *Set) All() []Def {
	out := slices.Clone(s.defs)
	rank := map[string]int{"built-in": 0, "user": 1, "project": 2, ScopeCommandLine: 3}
	sort.SliceStable(out, func(i, j int) bool {
		if rank[out[i].Scope] != rank[out[j].Scope] {
			return rank[out[i].Scope] < rank[out[j].Scope]
		}
		// The kind a call gets that names none comes first.
		if (out[i].Name == DefaultType) != (out[j].Name == DefaultType) {
			return out[i].Name == DefaultType
		}
		return out[i].Name < out[j].Name
	})
	return out
}

// Names returns the names of every definition, in the order of All.
func (s *Set) Names() []string {
	var out []string
	for _, d := range s.All() {
		out = append(out, d.Name)
	}
	return out
}

// Skipped returns the files that hold no usable definition.
func (s *Set) Skipped() []Skipped { return slices.Clone(s.skipped) }

// knownKeys are the settings of a definition gnosh reads.
var knownKeys = []string{"name", "description", "tools", "disallowedTools", "model", "permissionMode", "maxTurns",
	"omitClaudeMd", "background", "skills", "effort", "initialPrompt", "hooks", "isolation", "mcpServers", "memory"}

// Parse reads one definition from the text of a file. reason is empty
// when it could be read, and otherwise says why the file is skipped.
func Parse(text string) (d Def, reason string) {
	text = strings.TrimPrefix(text, string(rune(0xFEFF))) // a byte order mark, which some editors put first
	text = strings.ReplaceAll(text, "\r\n", "\n")
	lines := strings.Split(text, "\n")
	if len(lines) == 0 || strings.TrimRight(lines[0], " \t") != "---" {
		return d, `its first line is not "---", so it has no settings`
	}
	end := -1
	for i := 1; i < len(lines); i++ {
		if strings.TrimRight(lines[i], " \t") == "---" {
			end = i
			break
		}
	}
	if end < 0 {
		return d, `its settings are not closed by a second line of "---"`
	}
	fields, err := frontmatter(lines[1:end])
	if err != nil {
		return d, "its settings cannot be read: " + err.Error()
	}
	d.Prompt = strings.TrimSpace(strings.Join(lines[end+1:], "\n"))

	for _, f := range fields {
		switch f.key {
		case "name":
			d.Name = f.scalar
		case "description":
			d.Description = f.scalar
		case "tools":
			d.Tools, d.ToolsSet, d.AgentTypes = toolNames(f.list()), true, agentTypes(f.list())
		case "disallowedTools":
			d.Disallowed = toolNames(f.list())
		case "model":
			d.Model = f.scalar
		case "permissionMode":
			d.PermissionMode = f.scalar
		case "maxTurns":
			n, err := strconv.Atoi(f.scalar)
			if err != nil || n < 1 {
				return d, fmt.Sprintf("maxTurns is %q, which is not a number of turns", f.scalar)
			}
			d.MaxTurns = n
		case "omitClaudeMd":
			d.OmitInstructions = f.scalar == "true"
		case "background":
			d.Background = f.scalar == "true"
		case "skills":
			d.Skills = cleanNames(f.list())
		case "effort":
			d.Effort = f.scalar
		case "initialPrompt":
			d.InitialPrompt = strings.TrimSpace(f.scalar)
		case "hooks":
			switch raw, err := BlockJSON(f.block); {
			case !f.nested && (f.scalar != "" || len(f.items) > 0):
				return d, "its hooks are written as something other than a block of events"
			case err != nil:
				return d, "its hooks cannot be read: " + err.Error()
			case f.nested:
				d.Hooks = raw
			}
		case "isolation":
			d.Isolation = f.scalar
		case "mcpServers":
			raw, err := json.Marshal(f.list())
			if f.nested {
				raw, err = BlockJSON(f.block)
			}
			if err != nil {
				return d, "its mcpServers cannot be read: " + err.Error()
			}
			servers, why := mcpServers(raw)
			if why != "" {
				return d, "its mcpServers " + why
			}
			d.MCPServers = servers
		case "memory":
			if f.scalar != "" || len(f.items) > 0 || f.nested {
				d.Other = append(d.Other, f.key)
			}
		default:
			// A setting gnosh knows, in another letter case
			// ("disallowedtools"), would be passed over, and the agent run
			// without what it takes away: the definition is not used.
			// Keys gnosh does not know at all are passed over, as in
			// Claude Code.
			if i := slices.IndexFunc(knownKeys, func(k string) bool { return strings.EqualFold(k, f.key) }); i >= 0 {
				return d, fmt.Sprintf("it has the setting %q, which gnosh reads only as %q (letter case counts), so it is not used rather than used without it", f.key, knownKeys[i])
			}
		}
	}
	if why := d.problem(); why != "" {
		return d, why
	}
	if d.Model == "inherit" {
		d.Model, d.InheritModel = "", true
	}
	return d, ""
}

// cleanNames is a list of names with the blanks around each taken off and
// the empty ones left out.
func cleanNames(list []string) []string {
	var out []string
	for _, n := range list {
		if n = strings.TrimSpace(n); n != "" {
			out = append(out, n)
		}
	}
	return out
}

// problem says why a definition cannot be used, or "": every definition
// has a name an Agent call can give, and a description that says when to
// use it.
func (d Def) problem() string {
	switch {
	case d.Name == "":
		return "it has no name"
	case strings.HasPrefix(d.Name, "-"), strings.Contains(d.Name, ":"), utf8.RuneCountInString(d.Name) > 256:
		return fmt.Sprintf("the name %q cannot be an agent's: a name does not start with a dash, holds no colon, and is at most 256 characters long", clip(d.Name, 80))
	case d.Description == "":
		return fmt.Sprintf("the agent %q has no description, which is what says when to use it", d.Name)
	}
	return ""
}

func clip(s string, n int) string {
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	r := []rune(s)
	return string(r[:n]) + "..."
}

// toolNames turns the entries of a tools list into tool names: what is in
// parentheses after a name is left off.
func toolNames(entries []string) []string {
	var out []string
	for _, e := range entries {
		if i := strings.IndexByte(e, '('); i >= 0 {
			e = e[:i]
		}
		if e = strings.TrimSpace(e); e != "" && !slices.Contains(out, e) {
			out = append(out, e)
		}
	}
	return out
}

// agentTypes reads the kinds the Agent(kind, ...) entries of a tool list
// name. It is nil when the list has no such entry, or has Agent bare,
// which lets any kind be started. (Task is Agent's old name.)
func agentTypes(entries []string) []string {
	var out []string
	for _, e := range entries {
		name, spec, paren := strings.Cut(strings.TrimSpace(e), "(")
		if name = strings.TrimSpace(name); name != "Agent" && name != "Task" {
			continue
		}
		if !paren {
			return nil
		}
		for _, k := range strings.Split(strings.TrimSuffix(strings.TrimSpace(spec), ")"), ",") {
			if k = strings.TrimSpace(k); k != "" && !slices.Contains(out, k) {
				out = append(out, k)
			}
		}
	}
	return out
}

// splitList cuts a list written on one line at its commas, but not at a
// comma inside parentheses: "Read, Agent(worker, researcher)" is two
// entries.
func splitList(s string) []string {
	var out []string
	depth, from := 0, 0
	for i := 0; i < len(s); i++ {
		switch s[i] {
		case '(':
			depth++
		case ')':
			if depth > 0 {
				depth--
			}
		case ',':
			if depth == 0 {
				out = append(out, s[from:i])
				from = i + 1
			}
		}
	}
	return append(out, s[from:])
}

// field is one setting of the frontmatter.
type field struct {
	key    string
	scalar string   // the value written on the key's own line, or a block of text
	items  []string // the entries of a list
	nested bool     // the key holds a map of its own, which is not read
	block  []string // the indented lines under the key, for one that is nested
}

// list returns the field's value as a list: its entries, or the scalar
// split at the commas outside parentheses.
func (f field) list() []string {
	if len(f.items) > 0 {
		return f.items
	}
	var out []string
	for _, part := range splitList(f.scalar) {
		if part = unquote(strings.TrimSpace(part)); part != "" {
			out = append(out, part)
		}
	}
	return out
}

// frontmatter reads the settings: the small part of YAML that agent files
// use. A key at the start of a line with its value after a colon; a list
// as "[a, b]" or as indented lines that begin with "- "; a longer text
// after "|" or ">" on indented lines; and, under a key with no value,
// anything indented, which is taken as a map of that key and not read.
func frontmatter(lines []string) ([]field, error) {
	var out []field
	for i := 0; i < len(lines); i++ {
		line := lines[i]
		if strings.TrimSpace(line) == "" || strings.HasPrefix(strings.TrimSpace(line), "#") {
			continue
		}
		if line[0] == ' ' || line[0] == '\t' {
			return nil, fmt.Errorf("line %d is indented and belongs to no setting", i+2)
		}
		key, value, ok := strings.Cut(line, ":")
		key = strings.TrimSpace(key)
		if !ok || key == "" || strings.ContainsAny(key, " \t\"'{}[]") {
			return nil, fmt.Errorf("line %d is not a setting of the form \"name: value\"", i+2)
		}
		f := field{key: key}
		value = strings.TrimSpace(value)
		// The lines that belong to this key: the indented ones that follow.
		var under []string
		for i+1 < len(lines) && (strings.TrimSpace(lines[i+1]) == "" || lines[i+1][0] == ' ' || lines[i+1][0] == '\t') {
			i++
			under = append(under, lines[i])
		}
		for len(under) > 0 && strings.TrimSpace(under[len(under)-1]) == "" {
			under = under[:len(under)-1]
		}
		switch {
		case value == "|" || value == ">" || strings.HasPrefix(value, "|") && len(value) <= 2 || strings.HasPrefix(value, ">") && len(value) <= 2:
			var parts []string
			for _, u := range under {
				parts = append(parts, strings.TrimSpace(u))
			}
			sep := "\n"
			if value[0] == '>' {
				sep = " "
			}
			f.scalar = strings.TrimSpace(strings.Join(parts, sep))
		case strings.HasPrefix(value, "["):
			if !strings.HasSuffix(value, "]") {
				return nil, fmt.Errorf("the list of %q on line %d is not closed", key, i+2-len(under))
			}
			for _, part := range splitList(value[1 : len(value)-1]) {
				if part = unquote(strings.TrimSpace(part)); part != "" {
					f.items = append(f.items, part)
				}
			}
		case value != "":
			f.scalar = unquote(stripComment(value))
			if len(under) > 0 {
				// A plain value that runs on over indented lines.
				for _, u := range under {
					f.scalar += " " + strings.TrimSpace(u)
				}
				f.scalar = strings.TrimSpace(f.scalar)
			}
		default:
			for _, u := range under {
				t := strings.TrimSpace(u)
				switch {
				case t == "":
				case strings.HasPrefix(t, "- ") || t == "-":
					if item := unquote(strings.TrimSpace(strings.TrimPrefix(t, "-"))); item != "" {
						f.items = append(f.items, item)
					}
				default:
					f.nested = true
				}
			}
			if f.nested {
				f.items, f.block = nil, under
			}
		}
		out = append(out, f)
	}
	return out, nil
}

// stripComment takes a trailing " # ..." off a plain value.
func stripComment(v string) string {
	if strings.HasPrefix(v, `"`) || strings.HasPrefix(v, `'`) {
		return v
	}
	if i := strings.Index(v, " #"); i >= 0 {
		return strings.TrimSpace(v[:i])
	}
	return v
}

// unquote takes the quotes off a quoted value.
func unquote(v string) string {
	if len(v) >= 2 {
		switch {
		case v[0] == '"' && v[len(v)-1] == '"':
			if s, err := strconv.Unquote(v); err == nil {
				return s
			}
			return v[1 : len(v)-1]
		case v[0] == '\'' && v[len(v)-1] == '\'':
			return strings.ReplaceAll(v[1:len(v)-1], "''", "'")
		}
	}
	return v
}

// Field is one setting of a definition file's frontmatter, as Split
// returns it.
type Field struct {
	// Key is the setting's name.
	Key string
	// Scalar is the value written on the key's own line, or a block of
	// text; Items are the entries of a list.
	Scalar string
	Items  []string
	// Nested says the key holds a map of its own, which is not read;
	// Block is the indented lines under the key, which BlockJSON reads.
	Nested bool
	Block  []string
}

// List returns the field's value as a list: its entries, or the scalar
// split at commas.
func (f Field) List() []string { return field{scalar: f.Scalar, items: f.Items}.list() }

// Set reports whether the field says anything at all.
func (f Field) Set() bool { return f.Scalar != "" || len(f.Items) > 0 || f.Nested }

// Split takes a definition file apart: the settings between its two lines
// of "---", and the text after them. A file whose first line is not "---"
// has no settings and is all text. It is the reading that agent
// definitions and skills share. An error says the settings could not be
// read; the text is still returned.
func Split(text string) (fields []Field, body string, err error) {
	text = strings.TrimPrefix(text, string(rune(0xFEFF)))
	text = strings.ReplaceAll(text, "\r\n", "\n")
	lines := strings.Split(text, "\n")
	if len(lines) == 0 || strings.TrimRight(lines[0], " \t") != "---" {
		return nil, strings.TrimSpace(text), nil
	}
	end := -1
	for i := 1; i < len(lines); i++ {
		if strings.TrimRight(lines[i], " \t") == "---" {
			end = i
			break
		}
	}
	if end < 0 {
		return nil, strings.TrimSpace(text), errors.New(`the settings are not closed by a second line of "---"`)
	}
	body = strings.TrimSpace(strings.Join(lines[end+1:], "\n"))
	raw, err := frontmatter(lines[1:end])
	if err != nil {
		return nil, body, err
	}
	for _, f := range raw {
		fields = append(fields, Field{Key: f.key, Scalar: f.scalar, Items: f.items, Nested: f.nested, Block: f.block})
	}
	return fields, body, nil
}
