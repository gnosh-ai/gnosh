package ui

import (
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/gnosh-ai/gnosh/internal/tools/fstools/ignore"
	"github.com/gnosh-ai/gnosh/internal/ui/editor"
)

// What the prompt box offers to complete.
//
// The first word of a line that begins with a slash is completed from the
// session's list of commands. A word that begins with "@" is completed as
// a path to mention, and so, on Tab, is any word that reads as a path.
// Paths are completed a directory at a time, by the start of a name; the
// files a project's ignore rules leave out are not offered, and neither
// are names that begin with a dot until a dot is typed. A line of shell
// mode, which begins with "!", is completed as shellcomplete.go says.

// fileEntry is one name in a directory, as completion offers it.
type fileEntry struct {
	name string
	dir  bool
}

// The most names read from one directory and offered for one word.
const (
	maxDirEntries = 4000
	maxCandidates = 50
)

// complete is the prompt box's Complete: the ways to complete the word
// the cursor is in.
func (s *session) complete(text string, cursor int) []editor.Candidate {
	cursor = min(max(cursor, 0), len(text))
	before := text[:cursor]
	if strings.HasPrefix(text, "!") && cursor > 0 {
		return s.completeShell(text, cursor)
	}
	if strings.HasPrefix(text, "/") && !strings.ContainsAny(before, " \t\n") {
		return s.completeCommand(before, 0)
	}
	start := strings.LastIndexAny(before, " \t\n") + 1
	word := before[start:]
	if folderCommand(before) {
		// /cd and /add-dir take a folder: what is typed after them is
		// one, slash or no slash, and only folders are offered.
		return s.completeFolder(word, start)
	}
	if strings.HasPrefix(word, "/") && !strings.Contains(word[1:], "/") && !strings.HasPrefix(text, "/") {
		// A command named partway through a prompt ("run the tests, then
		// /com"), as Claude Code completes it: only names that begin
		// with what was typed, so that a path such as /tmp, which no
		// command's name begins with, is completed as a path. A bare /
		// lists every command. The command does not run from there; the
		// model reads its name.
		if c := s.completeCommand(word, start); len(c) > 0 {
			return c
		}
	}
	switch {
	case strings.HasPrefix(word, "@"):
		return append(s.completeFile(word[1:], start, "@"), s.completeAgent(word[1:], start)...)
	case strings.ContainsRune(word, '/') || strings.HasPrefix(word, "~"):
		return s.completeFile(word, start, "")
	}
	return nil
}

// folderCommand reports whether the line so far is a command that takes a
// folder (/cd, /add-dir) with its first argument begun.
func folderCommand(before string) bool {
	name, rest, ok := strings.Cut(before, " ")
	return ok && (name == "/cd" || name == "/add-dir") && !strings.ContainsAny(strings.TrimLeft(rest, " "), " \t\n")
}

// completeFolder is completeFile with only the folders.
func (s *session) completeFolder(partial string, start int) []editor.Candidate {
	return s.completePath(partial, start, "", true)
}

// agentLister is what a session's controls may also be: able to name the
// kinds of sub-agent a prompt may ask for with @agent-.
type agentLister interface {
	AgentKinds() []string
}

// completeAgent offers "@agent-" and a kind of sub-agent, for what was
// typed after an @ that "agent-" and the kind's name begin with.
func (s *session) completeAgent(typed string, start int) []editor.Candidate {
	al, ok := s.c.(agentLister)
	if !ok || typed == "" || !strings.HasPrefix("agent-", typed) && !strings.HasPrefix(typed, "agent-") {
		return nil
	}
	var out []editor.Candidate
	for _, name := range al.AgentKinds() {
		if text := "@agent-" + name; strings.HasPrefix(text, "@"+typed) {
			out = append(out, editor.Candidate{Text: text + " ", Start: start, Label: text, Detail: "a kind of sub-agent"})
		}
	}
	return out
}

// completeCommand offers the session's commands whose names begin with
// what was typed, in the order /help lists them, and after them the
// person's skills. When no name begins that way, the commands' other
// names that do are offered. start is where the typed word begins.
func (s *session) completeCommand(typed string, start int) []editor.Candidate {
	want := strings.TrimPrefix(typed, "/")
	var out []editor.Candidate
	for _, name := range s.info.Commands {
		if strings.HasPrefix(name, strings.ToLower(want)) {
			out = append(out, editor.Candidate{Text: "/" + name, Start: start})
		}
	}
	for _, sk := range s.info.Skills {
		if strings.HasPrefix(sk.Name, want) && !slices.Contains(s.info.Commands, sk.Name) {
			out = append(out, editor.Candidate{Text: "/" + sk.Name, Start: start})
		}
	}
	if len(out) == 0 && want != "" {
		// Nothing begins that way by its own name. A command's other
		// name is offered then, so that the word a person uses for a
		// thing finds it: /artifacts for /deliverables, /quit for /exit.
		for _, c := range s.info.CommandList {
			for _, alias := range c.Aliases {
				if strings.HasPrefix(alias, strings.ToLower(want)) {
					out = append(out, editor.Candidate{Text: "/" + alias, Start: start})
				}
			}
		}
	}
	return out
}

// completeFile offers the entries of the directory that partial names so
// far whose names begin with its last part. sigil is what stands before
// the path in the box ("@"), and start where the word begins. After a
// mention's "@", the project's files and folders that match what was typed
// anywhere in their paths follow (mention.go), as Claude Code's @ finds
// them: "@focus" offers internal/ui/focus.go.
func (s *session) completeFile(partial string, start int, sigil string) []editor.Candidate {
	if fs, ok := s.c.(fileSuggester); ok && sigil == "@" && s.info.FileSuggestion {
		// The person's own command offers the paths (filesuggest.go).
		return s.suggestFiles(fs, partial, start, sigil)
	}
	out := s.completePath(partial, start, sigil, false)
	if sigil == "@" && fuzzyWord(partial) && (len(out) == 0 || !strings.Contains(partial, "/")) {
		out = append(out, s.fuzzyFiles(partial, start, sigil, out)...)
	}
	return out
}

// completePath is completeFile, or with folders completeFolder.
func (s *session) completePath(partial string, start int, sigil string, folders bool) []editor.Candidate {
	if s.listDir == nil {
		return nil
	}
	dirPart, base := "", partial
	if i := strings.LastIndexByte(partial, '/'); i >= 0 {
		dirPart, base = partial[:i+1], partial[i+1:]
	}
	dir := dirPart
	switch {
	case dir == "~/" || strings.HasPrefix(dir, "~/"):
		if s.home == "" {
			return nil
		}
		dir = filepath.Join(s.home, dir[2:])
	case filepath.IsAbs(dir):
	default:
		if s.cwd == "" {
			return nil
		}
		dir = filepath.Join(s.cwd, dir)
	}
	var out []editor.Candidate
	lower := strings.ToLower(base)
	for _, e := range s.listDir(dir) {
		switch {
		case folders && !e.dir:
			continue
		case !strings.HasPrefix(strings.ToLower(e.name), lower):
			continue
		case strings.HasPrefix(e.name, ".") && !strings.HasPrefix(base, "."):
			continue
		case strings.ContainsAny(e.name, " \t\n") || editor.Visible(e.name) != e.name:
			// A name that could not be typed as one word.
			continue
		}
		name := e.name
		if e.dir {
			name += "/"
		}
		if folders && e.name == base {
			// The folder typed in full is a candidate as it was typed, and
			// first: an exact name wins, so Enter runs the command (/cd
			// ../lib) and does not take ../lib/ in its place, while Tab
			// goes on to ../lib/ and into the folder.
			out = append([]editor.Candidate{{Text: sigil + dirPart + base, Start: start, Label: base}}, out...)
		}
		out = append(out, editor.Candidate{Text: sigil + dirPart + name, Start: start, Label: name})
		if len(out) >= maxCandidates {
			break
		}
	}
	return out
}

// listDir lists a directory for completion: its entries in name order,
// without the ones the ignore rules in force there leave out (the same
// rules gnosh's own search tools go by) and without the repository's own
// folder.
func listDir(dir string) []fileEntry {
	f, err := os.Open(dir)
	if err != nil {
		return nil
	}
	ents, _ := f.ReadDir(maxDirEntries)
	f.Close()
	if len(ents) == 0 {
		return nil
	}
	rules := ignore.For(dir, ignore.Options{})
	out := make([]fileEntry, 0, len(ents))
	for _, e := range ents {
		name := e.Name()
		if name == ".git" {
			continue
		}
		isDir := e.IsDir()
		if e.Type()&os.ModeSymlink != 0 {
			if st, err := os.Stat(filepath.Join(dir, name)); err == nil {
				isDir = st.IsDir()
			}
		}
		if rules.Ignored(name, isDir) {
			continue
		}
		out = append(out, fileEntry{name: name, dir: isDir})
	}
	slices.SortFunc(out, byName)
	return out
}

// byName orders two entries by name without regard to case, and by case
// when the names are otherwise the same.
func byName(a, b fileEntry) int {
	if c := strings.Compare(strings.ToLower(a.name), strings.ToLower(b.name)); c != 0 {
		return c
	}
	return strings.Compare(a.name, b.name)
}

// gitBranch returns the branch that is checked out in the git repository
// dir lies in, the start of the commit's name when no branch is, and ""
// when dir is in no repository. It reads the repository's HEAD file and
// runs nothing.
func gitBranch(dir string) string {
	for d := filepath.Clean(dir); ; {
		dotGit := filepath.Join(d, ".git")
		if fi, err := os.Lstat(dotGit); err == nil {
			gitDir := dotGit
			if !fi.IsDir() {
				// A worktree or a submodule: the file names the real
				// directory.
				rest, ok := strings.CutPrefix(strings.TrimSpace(readStart(dotGit)), "gitdir:")
				if !ok {
					return ""
				}
				gitDir = strings.TrimSpace(rest)
				if !filepath.IsAbs(gitDir) {
					gitDir = filepath.Join(d, gitDir)
				}
			}
			head := strings.TrimSpace(readStart(filepath.Join(gitDir, "HEAD")))
			if ref, ok := strings.CutPrefix(head, "ref: refs/heads/"); ok {
				return oneLine(ref)
			}
			if ref, ok := strings.CutPrefix(head, "ref: "); ok {
				return oneLine(ref)
			}
			if len(head) >= 7 {
				return oneLine(head[:7])
			}
			return ""
		}
		parent := filepath.Dir(d)
		if parent == d {
			return ""
		}
		d = parent
	}
}

// readStart reads the first few kilobytes of a file, or "" when it cannot
// be read.
func readStart(path string) string {
	f, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer f.Close()
	data, _ := io.ReadAll(io.LimitReader(f, 4096))
	return string(data)
}
