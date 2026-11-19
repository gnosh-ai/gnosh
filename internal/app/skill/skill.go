// Package skill reads skills: instructions a person keeps on disk for a
// kind of task, which they run by typing /name and a model runs with the
// Skill tool. The file format, the folders and the fields are Claude
// Code's (spec/cc/slash-commands-and-skills.md, section 5); nothing here
// runs anything or knows about a session.
//
// A skill is a folder with a SKILL.md in it, in a skills directory: the
// folder's name is the skill's, unless the file says another. The older
// form is one Markdown file in a commands directory, named by its path
// with colons for the folders: commands/frontend/component.md is
// frontend:component.
//
// What a skill may ask for is read here in full, so that a skill which
// asks for something this gnosh does not have can be refused with the
// reason (Skill.Unbuilt) and is never run as something else.
package skill

import (
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/gnosh-ai/gnosh/internal/app/agentdef"
)

// Skill is one skill, as its file gives it.
type Skill struct {
	// Name is what is typed after the slash and what the Skill tool is
	// given.
	Name string
	// Description says what the skill does and when to use it: the file's
	// description with its when_to_use after it, or the first line of the
	// text. It is what a model matches a task against.
	Description string
	// ArgumentHint is shown while the name is typed, such as
	// "[issue-number]".
	ArgumentHint string
	// Arguments names the positional arguments, for $name in the text.
	Arguments []string
	// NoModel says only the person may start it (disable-model-invocation),
	// and NoUser that only a model may (user-invocable: false).
	NoModel bool
	NoUser  bool
	// NameOnly says the model is told the skill's name alone, and Off that
	// nobody may start it: the person's settings say so (Claude Code's
	// skillOverrides name-only and off). Over says which of those settings
	// is in force, or "".
	NameOnly, Off bool
	Over          string
	// AllowedTools are permission rules that hold for the turn the skill
	// is run in, as written. DisallowedTools are the tools (or rules) it
	// takes away for that turn: calls they name are refused.
	AllowedTools, DisallowedTools []string
	// Effort is the thinking level the skill asks for, for that turn: one
	// of the model's levels, max or auto.
	Effort string
	// Fork says the skill runs in a sub-agent (context: fork): its text is
	// the sub-agent's task, and only its report comes back. Agent is the
	// kind of sub-agent, empty for the default; Background asks for it to
	// run beside the session (background: true).
	Fork       bool
	Agent      string
	Background bool
	// Model is the model of a forked skill's sub-agent, or empty for the
	// session's. (A model for a skill that is not forked is not built.)
	Model string
	// Paths are patterns of the files the skill is for (paths): the model
	// is told of it once the session works on such a file, and not
	// before.
	Paths []string
	// Hooks is the skill's own hooks block (hooks), as JSON in Claude
	// Code's shape: put in force when the skill is run, for the rest of
	// the session.
	Hooks json.RawMessage
	// Injects says the skill's text runs commands before the model sees
	// it (!`command`, or a block opened with ```!).
	Injects bool
	// Body is the instructions: the file without its settings.
	Body string
	// Dir is the folder that holds the file, Source the file, and Scope
	// where it came from: "user" or "project". Command says it is a file
	// of a commands directory.
	Dir, Source, Scope string
	Command            bool
	// Unbuilt lists what the skill asks for that this gnosh does not
	// have. A skill with any is not run.
	Unbuilt []string
}

// Skipped is a file that holds no usable skill, and why.
type Skipped struct {
	Path, Reason string
}

// Set is the skills that were found.
type Set struct {
	skills  []Skill
	skipped []Skipped
}

// Dirs are the directories of one scope, the ones that win first: each of
// Skills holds a folder for each skill, each of Commands holds Markdown
// files.
type Dirs struct {
	Skills, Commands []string
}

// Load puts the skills together. Where two share a name, the person's own
// wins over the project's, an earlier directory over a later one (which is
// how gnosh's own folders come before Claude Code's), and a skill over a
// file of a commands directory. A directory that is not there holds
// nothing.
func Load(user, project Dirs) *Set {
	s := &Set{}
	have := map[string]bool{}
	add := func(sk Skill, reason, path string) {
		switch {
		case reason != "":
			s.skipped = append(s.skipped, Skipped{path, reason})
		case have[sk.Name]:
			s.skipped = append(s.skipped, Skipped{path, fmt.Sprintf("another skill named %q comes before it, and that one is used", sk.Name)})
		default:
			have[sk.Name] = true
			s.skills = append(s.skills, sk)
		}
	}
	for _, scope := range []struct {
		name string
		dirs Dirs
	}{{"user", user}, {"project", project}} {
		for _, dir := range scope.dirs.Skills {
			entries, _ := os.ReadDir(dir)
			for _, e := range entries {
				// A skill is a folder, or a link to one kept elsewhere.
				if info, err := os.Stat(filepath.Join(dir, e.Name())); err != nil || !info.IsDir() {
					continue
				}
				path := filepath.Join(dir, e.Name(), "SKILL.md")
				data, err := readSmall(path)
				if err != nil {
					if !os.IsNotExist(err) {
						s.skipped = append(s.skipped, Skipped{path, err.Error()})
					}
					continue
				}
				sk, reason := Parse(data, e.Name())
				sk.Dir, sk.Source, sk.Scope = filepath.Dir(path), path, scope.name
				add(sk, reason, path)
			}
		}
		for _, dir := range scope.dirs.Commands {
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
			for _, path := range files {
				data, err := readSmall(path)
				if err != nil {
					s.skipped = append(s.skipped, Skipped{path, err.Error()})
					continue
				}
				rel, _ := filepath.Rel(dir, strings.TrimSuffix(path, ".md"))
				name := strings.ReplaceAll(filepath.ToSlash(rel), "/", ":")
				sk, reason := Parse(data, name)
				// A file of a commands directory is named by its path
				// alone.
				sk.Name, sk.Command = name, true
				if reason == "" {
					reason = badName(name)
				}
				sk.Dir, sk.Source, sk.Scope = filepath.Dir(path), path, scope.name
				add(sk, reason, path)
			}
		}
	}
	sort.SliceStable(s.skills, func(i, j int) bool { return s.skills[i].Name < s.skills[j].Name })
	return s
}

// maxFile is the largest skill file that is read.
const maxFile = 1 << 20

func readSmall(path string) (string, error) {
	info, err := os.Stat(path)
	if err != nil {
		return "", err
	}
	switch {
	case info.IsDir():
		return "", fmt.Errorf("it is a folder, not a file")
	case !info.Mode().IsRegular():
		// (A link a repository holds may lead to /dev/zero, a terminal or
		// a pipe: reading one would never end.)
		return "", fmt.Errorf("it is not a plain file")
	case info.Size() > maxFile:
		return "", fmt.Errorf("it is %d bytes, and a skill file is read up to %d", info.Size(), maxFile)
	}
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, maxFile+1))
	switch {
	case err != nil:
		return "", err
	case len(data) > maxFile:
		return "", fmt.Errorf("it is more than %d bytes, and a skill file is read up to %d", maxFile, maxFile)
	case !utf8.Valid(data):
		return "", fmt.Errorf("it is not text (UTF-8)")
	}
	return string(data), nil
}

// Get returns the skill with that name.
func (s *Set) Get(name string) (Skill, bool) {
	for _, sk := range s.skills {
		if sk.Name == name {
			return sk, true
		}
	}
	return Skill{}, false
}

// All returns the skills, by name.
func (s *Set) All() []Skill { return slices.Clone(s.skills) }

// Override applies the person's settings of how far each skill is seen
// (Claude Code's skillOverrides), by name, to the set: "on" leaves a skill
// as its file says, "name-only" tells the model its name alone,
// "user-invocable-only" keeps it from the model, and "off" from everyone.
// A value only ever takes away.
func (s *Set) Override(over map[string]string) {
	for i := range s.skills {
		sk := &s.skills[i]
		switch sk.Over = over[sk.Name]; sk.Over {
		case "name-only":
			sk.NameOnly = true
		case "user-invocable-only":
			sk.NoModel = true
		case "off":
			sk.Off = true
		}
	}
}

// Skipped returns the files that hold no usable skill.
func (s *Set) Skipped() []Skipped { return slices.Clone(s.skipped) }

// MaxDescription is the most of a description that is kept, in
// characters.
const MaxDescription = 1536

// Parse reads one skill from the text of its file. fallback is its name
// when the file gives none: the name of its folder. reason is empty when
// it could be read, and otherwise says why the file is skipped.
func Parse(text, fallback string) (sk Skill, reason string) {
	fields, body, err := agentdef.Split(text)
	if err != nil {
		// Settings that cannot be read are no settings: the skill loads
		// with none, as Claude Code loads it.
		fields = nil
	}
	sk.Name, sk.Body = fallback, body
	when, context, shell, model := "", "", "", ""
	for _, f := range fields {
		switch f.Key {
		case "name":
			if f.Scalar != "" {
				sk.Name = f.Scalar
			}
		case "description":
			sk.Description = f.Scalar
		case "when_to_use":
			when = f.Scalar
		case "argument-hint":
			sk.ArgumentHint = f.Scalar
		case "arguments":
			if len(f.Items) > 0 {
				sk.Arguments = f.Items
			} else {
				sk.Arguments = strings.Fields(strings.ReplaceAll(f.Scalar, ",", " "))
			}
		case "disable-model-invocation":
			sk.NoModel = truth(f.Scalar)
		case "user-invocable":
			sk.NoUser = f.Scalar != "" && !truth(f.Scalar)
		case "allowed-tools":
			if len(f.Items) > 0 {
				sk.AllowedTools = f.Items
			} else {
				sk.AllowedTools = SplitRules(f.Scalar)
			}
		case "disallowed-tools":
			if len(f.Items) > 0 {
				sk.DisallowedTools = f.Items
			} else {
				sk.DisallowedTools = SplitRules(f.Scalar)
			}
		case "effort":
			sk.Effort = strings.ToLower(strings.TrimSpace(f.Scalar))
		case "context":
			context = f.Scalar
		case "agent":
			sk.Agent = strings.TrimSpace(f.Scalar)
		case "background":
			sk.Background = truth(f.Scalar)
		case "shell":
			shell = f.Scalar
		case "model":
			if f.Scalar != "inherit" {
				model = f.Scalar
			}
		case "paths":
			sk.Paths = f.List()
		case "hooks":
			switch raw, err := agentdef.BlockJSON(f.Block); {
			case !f.Nested && f.Set():
				sk.Unbuilt = append(sk.Unbuilt, "hooks written as something other than a block of events (hooks)")
			case err != nil:
				sk.Unbuilt = append(sk.Unbuilt, "hooks that cannot be read ("+err.Error()+")")
			case f.Nested:
				sk.Hooks = raw
			}
		}
	}
	switch context {
	case "":
	case "fork":
		sk.Fork = true
	default:
		sk.Unbuilt = append(sk.Unbuilt, "a context gnosh does not know (context: "+context+")")
	}
	switch {
	case model == "":
	case sk.Fork:
		sk.Model = model // the sub-agent's
	default:
		sk.Unbuilt = append(sk.Unbuilt, "a model of its own (model)")
	}
	if shell != "" && shell != "bash" {
		sk.Unbuilt = append(sk.Unbuilt, "the shell "+shell)
	}
	sk.Injects = injects(sk.Body)
	if sk.Description == "" {
		for _, line := range strings.Split(sk.Body, "\n") {
			if line = strings.TrimSpace(strings.TrimLeft(strings.TrimSpace(line), "#")); line != "" {
				sk.Description = line
				break
			}
		}
	}
	if when != "" {
		sk.Description = strings.TrimSpace(sk.Description + " " + when)
	}
	if r := []rune(sk.Description); len(r) > MaxDescription {
		sk.Description = string(r[:MaxDescription])
	}
	if why := badName(sk.Name); why != "" {
		return sk, why
	}
	if strings.TrimSpace(sk.Body) == "" {
		return sk, "it holds no instructions"
	}
	return sk, ""
}

// truth reads a setting that is yes or no.
func truth(v string) bool {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "true", "yes", "on", "1":
		return true
	}
	return false
}

// badName says why a name cannot be a skill's, or "".
func badName(name string) string {
	if name == "" {
		return "it has no name"
	}
	if utf8.RuneCountInString(name) > 64 {
		return "its name is longer than 64 characters"
	}
	for i, r := range name {
		ok := r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || (i > 0 && (r == '-' || r == '_' || r == '.' || r == ':'))
		if !ok {
			if unicode.IsControl(r) || unicode.IsSpace(r) || !unicode.IsPrint(r) || r == '<' || r == '>' {
				return "its name holds a character that cannot be typed or seen"
			}
			return fmt.Sprintf("its name %q is not one that can be typed after a slash: letters, digits, and - _ . : after the first", name)
		}
	}
	return ""
}

// SplitRules splits a line of permission rules written with spaces or
// commas between them. What stands in parentheses stays whole:
// "Bash(git add *) Read" is two rules.
func SplitRules(line string) []string {
	var out []string
	var cur strings.Builder
	depth := 0
	flush := func() {
		if r := strings.TrimSpace(cur.String()); r != "" {
			out = append(out, r)
		}
		cur.Reset()
	}
	for _, r := range line {
		switch {
		case r == '(':
			depth++
			cur.WriteRune(r)
		case r == ')':
			if depth > 0 {
				depth--
			}
			cur.WriteRune(r)
		case depth == 0 && (r == ',' || unicode.IsSpace(r)):
			flush()
		default:
			cur.WriteRune(r)
		}
	}
	flush()
	return out
}

// injects reports whether a skill's text asks for a command to be run
// before the model sees it: !`command` at the start of a line or after a
// blank, or a fenced block opened with ```!.
func injects(body string) bool {
	for _, line := range strings.Split(body, "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), "```!") {
			return true
		}
		for i := 0; i+1 < len(line); i++ {
			if line[i] == '!' && line[i+1] == '`' && (i == 0 || line[i-1] == ' ' || line[i-1] == '\t') {
				return true
			}
		}
	}
	return false
}

// Vars are the values a skill's text may name.
type Vars struct {
	// SessionID, SkillDir, ProjectDir and Effort stand for
	// ${CLAUDE_SESSION_ID}, ${CLAUDE_SKILL_DIR}, ${CLAUDE_PROJECT_DIR} and
	// ${CLAUDE_EFFORT}.
	SessionID, SkillDir, ProjectDir, Effort string
}

// Render writes the skill's text as a model is to read it for one run:
// the arguments and the named values put in where the text asks for them.
// args is everything typed after the name.
//
// $ARGUMENTS is all of it; $ARGUMENTS[N] and $N are the Nth word, counted
// from zero, where words are split as a shell splits them; $name is the
// argument a skill named in its settings. A numbered place with no
// argument stays as written, and a named one is empty. A backslash before
// the dollar keeps it as written. What is put in is never read again for
// more places. When arguments were given and the text had no place for
// them, they follow at the end.
func (sk Skill) Render(args string, v Vars) string {
	args = strings.TrimSpace(args)
	words := Words(args)
	named := map[string]int{}
	for i, name := range sk.Arguments {
		if name != "" {
			named[name] = i
		}
	}
	body := sk.Body
	var b strings.Builder
	used := false
	for i := 0; i < len(body); {
		c := body[i]
		if c == '\\' && i+1 < len(body) && body[i+1] == '$' {
			if _, n := placeAt(body[i+1:], named); n > 0 {
				// An escaped place: the dollar and what follows stay.
				b.WriteString(body[i+1 : i+1+n])
				i += 1 + n
				continue
			}
		}
		if c != '$' {
			b.WriteByte(c)
			i++
			continue
		}
		p, n := placeAt(body[i:], named)
		if n == 0 {
			b.WriteByte(c)
			i++
			continue
		}
		switch {
		case p.env != "":
			b.WriteString(map[string]string{
				"CLAUDE_SESSION_ID": v.SessionID, "CLAUDE_SKILL_DIR": v.SkillDir,
				"CLAUDE_PROJECT_DIR": v.ProjectDir, "CLAUDE_EFFORT": v.Effort,
			}[p.env])
		case p.all:
			b.WriteString(args)
			used = true
		case p.named:
			if p.index < len(words) {
				b.WriteString(words[p.index])
			}
			used = true
		case p.index < len(words):
			b.WriteString(words[p.index])
			used = true
		default:
			b.WriteString(body[i : i+n])
		}
		i += n
	}
	out := b.String()
	if args != "" && !used {
		out = strings.TrimRight(out, "\n") + "\n\nARGUMENTS: " + args
	}
	return out
}

// place is one place in a skill's text that a value is put into.
type place struct {
	env   string // one of the ${CLAUDE_...} names
	all   bool   // $ARGUMENTS
	named bool   // $name
	index int
}

// placeAt reads the place that begins at the dollar s starts with, and
// how many bytes it takes. A dollar that begins no place takes none.
func placeAt(s string, named map[string]int) (place, int) {
	rest := s[1:]
	if strings.HasPrefix(rest, "{") {
		for _, name := range []string{"CLAUDE_SESSION_ID", "CLAUDE_SKILL_DIR", "CLAUDE_PROJECT_DIR", "CLAUDE_EFFORT"} {
			if strings.HasPrefix(rest, "{"+name+"}") {
				return place{env: name}, len(name) + 3
			}
		}
		return place{}, 0
	}
	if strings.HasPrefix(rest, "ARGUMENTS") {
		after := rest[len("ARGUMENTS"):]
		if strings.HasPrefix(after, "[") {
			if end := strings.IndexByte(after, ']'); end > 1 {
				if n, err := strconv.Atoi(after[1:end]); err == nil && n >= 0 {
					return place{index: n}, 1 + len("ARGUMENTS") + end + 1
				}
			}
		}
		if after == "" || !wordByte(after[0]) {
			return place{all: true}, 1 + len("ARGUMENTS")
		}
		return place{}, 0
	}
	n := 0
	for n < len(rest) && rest[n] >= '0' && rest[n] <= '9' {
		n++
	}
	if n > 0 && n <= 3 {
		index, _ := strconv.Atoi(rest[:n])
		return place{index: index}, 1 + n
	}
	n = 0
	for n < len(rest) && wordByte(rest[n]) {
		n++
	}
	if index, ok := named[rest[:n]]; ok && n > 0 {
		return place{named: true, index: index}, 1 + n
	}
	return place{}, 0
}

func wordByte(c byte) bool {
	return c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '_'
}

// Words splits what was typed after a skill's name the way a shell splits
// words: at blanks, with single and double quotes keeping blanks inside a
// word, and a backslash keeping the character after it.
func Words(args string) []string {
	var out []string
	var cur strings.Builder
	in := false
	quote := rune(0)
	esc := false
	for _, r := range args {
		switch {
		case esc:
			cur.WriteRune(r)
			esc = false
		case r == '\\' && quote != '\'':
			esc, in = true, true
		case quote != 0:
			if r == quote {
				quote = 0
			} else {
				cur.WriteRune(r)
			}
		case r == '"' || r == '\'':
			quote, in = r, true
		case unicode.IsSpace(r):
			if in {
				out = append(out, cur.String())
				cur.Reset()
				in = false
			}
		default:
			cur.WriteRune(r)
			in = true
		}
	}
	if in {
		out = append(out, cur.String())
	}
	return out
}
