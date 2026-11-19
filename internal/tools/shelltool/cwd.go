package shelltool

import (
	"os"
	"path/filepath"
	"strings"

	"github.com/gnosh-ai/gnosh/internal/core"
)

// moved says what the directory a shell ended in means for the session.
type moved struct {
	// to is the session's new working directory, spelled under the root it
	// is in the way the session spells that root. It is empty when the
	// session stays where it is.
	to string
	// outside is the directory the shell ended in when that is outside
	// every root, so the session does not follow it there; checkout says
	// it is in a checkout the session's work tree was made from instead.
	outside  string
	checkout bool
}

// place works out where the session should be after a shell that started
// in env.Cwd ended in final, the physical path the shell reported. The
// session follows the shell only into its roots; with no roots given, the
// directory the command started in stands for them.
//
// Paths are compared with their symbolic links resolved, so a link inside a
// root that leads out of it is seen as outside, and a root that is itself
// reached through a link (as temporary directories are on macOS) still
// holds what is under it.
func place(final string, env *core.ToolEnv) moved {
	if final == "" || final == physical(env.Cwd) {
		return moved{}
	}
	roots := env.Roots
	if len(roots) == 0 {
		roots = []string{env.Cwd}
	}
	var best, bestPhysical string
	for _, root := range roots {
		if p := physical(root); within(final, p) && len(p) > len(bestPhysical) {
			best, bestPhysical = root, p
		}
	}
	if best == "" {
		return moved{outside: final}
	}
	if t := env.Tree; t != nil && !within(final, physical(t.Path)) {
		// A session with a git work tree of its own does not follow its
		// shell into a checkout the work tree was made from, root or not:
		// its calls there would all be refused, its next cd back included.
		for _, c := range t.Checkouts() {
			if within(final, physical(c)) {
				return moved{outside: final, checkout: true}
			}
		}
	}
	rel, err := filepath.Rel(bestPhysical, final)
	if err != nil {
		return moved{outside: final}
	}
	return moved{to: filepath.Join(best, rel)}
}

// physical returns path with its symbolic links resolved, or just cleaned
// when it cannot be resolved (it may be gone).
func physical(path string) string {
	if p, err := filepath.EvalSymlinks(path); err == nil {
		return p
	}
	return filepath.Clean(path)
}

// isDir reports whether path is a directory that is there.
func isDir(path string) bool {
	st, err := os.Stat(path)
	return err == nil && st.IsDir()
}

// nearestStanding is where a command runs when the session's working
// directory has been taken away (a checkout or a clean removed it): the
// nearest folder above it that is there and inside the session's folders
// (its work tree, for a session that has one), or else the first of those
// that is there; "" when none is. Refusing every command there left no
// way out: not even the person's own cd could run.
func nearestStanding(env *core.ToolEnv) string {
	inside := func(d string) bool {
		if t := env.Tree; t != nil {
			return within(d, physical(t.Path))
		}
		for _, r := range env.Roots {
			if within(d, physical(r)) {
				return true
			}
		}
		return len(env.Roots) == 0
	}
	for d := filepath.Dir(env.Cwd); ; d = filepath.Dir(d) {
		if isDir(d) && inside(physical(d)) {
			return d
		}
		if d == filepath.Dir(d) {
			break
		}
	}
	if t := env.Tree; t != nil && isDir(t.Path) {
		return t.Path
	}
	for _, r := range env.Roots {
		if isDir(r) {
			return r
		}
	}
	return ""
}

// within reports whether dir is root or lies under it. Both are clean,
// absolute paths.
func within(dir, root string) bool {
	if root == "" || !filepath.IsAbs(root) {
		return false
	}
	if dir == root {
		return true
	}
	return strings.HasPrefix(dir, strings.TrimSuffix(root, string(filepath.Separator))+string(filepath.Separator))
}
