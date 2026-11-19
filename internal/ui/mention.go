package ui

import (
	"context"
	"io/fs"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/gnosh-ai/gnosh/internal/tools/fstools/ignore"
	"github.com/gnosh-ai/gnosh/internal/ui/editor"
)

// The fuzzy @ search: a mention's "@" and a few letters offer the files
// and folders of the project whose paths hold them, wherever they are, as
// Claude Code's @ does, after the names in the session's folder that begin
// with them. The project is listed in the background as the session
// starts and after each turn (the model may have made files), by git where
// the folder is in a repository (the files it tracks and the ones it
// would, so the ignored ones are left out) and by a walk that keeps to the
// ignore rules elsewhere; a list is used for half a minute.

// fileIndex is the project's paths as the fuzzy @ search last listed
// them: relative to dir, folders with a slash after them, only the ones
// that can be offered (not hidden, typed as one word), and each in lower
// case too, for the matching.
type fileIndex struct {
	dir          string
	paths, lower []string
	at           time.Time
	loading      bool
	// stale says the project may have changed since the listing began (a
	// turn ended): it is listed again when next used, even when a listing
	// under way ends after the change.
	stale bool
	// ignored says what git ignores is listed too (respectGitignore off).
	ignored bool
}

// newIndex keeps of the paths of dir the ones that can be offered.
func newIndex(dir string, paths []string, at time.Time) fileIndex {
	ix := fileIndex{dir: dir, at: at}
	for _, p := range paths {
		if strings.ContainsAny(p, " \t\n") || editor.Visible(p) != p || hidden(p) {
			continue
		}
		ix.paths = append(ix.paths, p)
		ix.lower = append(ix.lower, strings.ToLower(p))
	}
	return ix
}

const (
	// indexAge is how long a list of the project is used before it is
	// listed again; indexWait the longest a listing may take, and
	// maxIndexed the most paths it keeps.
	indexAge   = 30 * time.Second
	indexWait  = 3 * time.Second
	maxIndexed = 50000
)

// fuzzyWord reports whether what was typed after an @ is searched for in
// the whole project: a word that is not a path from the home folder, the
// root or a dot.
func fuzzyWord(partial string) bool {
	return partial != "" && !strings.HasPrefix(partial, "/") && !strings.HasPrefix(partial, "~") && !strings.HasPrefix(partial, ".")
}

// indexed returns the project's paths for the fuzzy search, and lists them
// again in the background when the list is old or of another folder.
func (s *session) indexed() *fileIndex {
	if s.listProject == nil || s.cwd == "" {
		return nil
	}
	if ix := &s.index; !ix.loading && (ix.dir != s.cwd || ix.stale || ix.ignored != s.info.ListIgnored || s.now().Sub(ix.at) > indexAge) {
		ix.loading, ix.stale = true, false
		dir, list, now, ignored := s.cwd, s.listProject, s.now, s.info.ListIgnored
		s.host.spawn(func() func() {
			ix := newIndex(dir, list(dir, ignored), now())
			ix.ignored = ignored
			return func() {
				ix.stale = s.index.stale
				s.index = ix
			}
		})
	}
	if s.index.dir != s.cwd {
		return nil
	}
	return &s.index
}

// fuzzyFiles offers the project's paths that match what was typed, best
// first, without the ones already offered.
func (s *session) fuzzyFiles(partial string, start int, sigil string, have []editor.Candidate) []editor.Candidate {
	room := maxCandidates - len(have)
	if room <= 0 {
		return nil
	}
	q := strings.ToLower(partial)
	type hit struct {
		path string
		rank int
	}
	ix := s.indexed()
	if ix == nil {
		return nil
	}
	var hits []hit
	for i, lp := range ix.lower {
		if r := fuzzyRank(lp, q); r >= 0 {
			hits = append(hits, hit{ix.paths[i], r})
		}
	}
	slices.SortFunc(hits, func(a, b hit) int {
		if a.rank != b.rank {
			return a.rank - b.rank
		}
		if len(a.path) != len(b.path) {
			return len(a.path) - len(b.path)
		}
		return strings.Compare(a.path, b.path)
	})
	offered := map[string]bool{}
	for _, c := range have {
		offered[c.Text] = true
	}
	var out []editor.Candidate
	for _, h := range hits {
		text := sigil + h.path
		if offered[text] {
			continue
		}
		offered[text] = true
		out = append(out, editor.Candidate{Text: text, Start: start, Label: h.path})
		if len(out) >= room {
			break
		}
	}
	return out
}

// hidden reports whether a path is in, or is, a file or folder whose name
// begins with a dot.
func hidden(p string) bool {
	for _, seg := range strings.Split(strings.TrimSuffix(p, "/"), "/") {
		if strings.HasPrefix(seg, ".") {
			return true
		}
	}
	return false
}

// fuzzyRank says how well the path lp, in lower case, matches q, typed in
// lower case: 0 for a name that is q, then a name that begins with it, a
// name that holds it, a path that holds it, and last a path that holds q's
// characters in their order, the fewer pieces the better; -1 for no match.
func fuzzyRank(lp, q string) int {
	base := path.Base(strings.TrimSuffix(lp, "/"))
	switch {
	case base == q:
		return 0
	case strings.HasPrefix(base, q):
		return 1
	case strings.Contains(base, q):
		return 2
	case strings.Contains(lp, q):
		return 3
	}
	pieces, j, last := 0, 0, -2
	for i := 0; i < len(lp) && j < len(q); i++ {
		if lp[i] == q[j] {
			if i != last+1 {
				pieces++
			}
			last = i
			j++
		}
	}
	if j < len(q) {
		return -1
	}
	return 3 + pieces
}

// listProject lists the project in dir for the fuzzy @ search: paths
// relative to dir, and the folders they are in with a slash after them.
// In a git repository it is git's list of the files it tracks and the
// ones it would track (so the ignored ones are left out); elsewhere a walk
// by the ignore rules gnosh's search tools go by, without the folders
// whose names begin with a dot. With ignored (the respectGitignore option
// off) what the ignore rules name is listed too. At most maxIndexed
// paths, each way read in at most indexWait.
//
// It runs in a folder that may not be trusted, so git is kept from
// running any program the repository's own settings name: ls-files reads
// the index, and a repository's core.fsmonitor is a command git would run
// for that (the setting is turned off on the command line, which wins
// over the repository's), and nothing else ls-files does runs one.
func listProject(dir string, ignored bool) []string {
	ctx, cancel := context.WithTimeout(context.Background(), indexWait)
	defer cancel()
	args := []string{"-c", "core.fsmonitor=false", "--no-pager", "-C", dir, "ls-files", "--cached", "--others", "-z"}
	if !ignored {
		args = append(args, "--exclude-standard")
	}
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Env = append(os.Environ(), "GIT_OPTIONAL_LOCKS=0")
	var files []string
	if out, err := cmd.Output(); err == nil {
		for _, f := range strings.Split(string(out), "\x00") {
			if f != "" && len(files) < maxIndexed {
				files = append(files, f)
			}
		}
	} else {
		// (A walk of its own time: git's may be what ran out.)
		walk, stop := context.WithTimeout(context.Background(), indexWait)
		files = walkProject(walk, dir, ignored)
		stop()
	}
	folders := map[string]bool{}
	paths := make([]string, 0, len(files))
	for _, f := range files {
		paths = append(paths, f)
		for d := path.Dir(f); d != "." && d != "/" && !folders[d]; d = path.Dir(d) {
			folders[d] = true
		}
	}
	for d := range folders {
		paths = append(paths, d+"/")
	}
	return paths
}

// walkProject lists the files under dir by the ignore rules (or, with
// ignored, without them), for a folder that is not in a git repository.
func walkProject(ctx context.Context, dir string, ignored bool) []string {
	var files []string
	rules := map[string]*ignore.Rules{".": ignore.For(dir, ignore.Options{})}
	_ = filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		switch {
		case ctx.Err() != nil || len(files) >= maxIndexed:
			return fs.SkipAll
		case err != nil && d != nil && d.IsDir() && p != dir:
			return fs.SkipDir
		case err != nil || p == dir:
			return nil
		}
		rel, _ := filepath.Rel(dir, p)
		rel = filepath.ToSlash(rel)
		parent := path.Dir(rel)
		r := rules[parent]
		if r == nil {
			return nil
		}
		name := d.Name()
		if !ignored && r.Ignored(name, d.IsDir()) || strings.HasPrefix(name, ".") {
			if d.IsDir() {
				return fs.SkipDir
			}
			return nil
		}
		if d.IsDir() {
			rules[rel] = r.Enter(name, nil)
			return nil
		}
		files = append(files, rel)
		return nil
	})
	return files
}
