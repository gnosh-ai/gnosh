package trust

import (
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// Folder says what "the folder" is for a directory gnosh was started in:
// where its project begins, and which folder the trust record is kept
// under. It is worked out by reading files only. Nothing in the folder is
// run, and git is never asked, because a repository's own git settings can
// name programs that git runs on a mere look.
type Folder struct {
	// Start is the directory gnosh was started in, with links resolved.
	Start string
	// Root is where the project begins: the top folder of the git
	// repository that holds Start, or Start itself outside a repository.
	// It is what the config loader is given as the project root.
	Root string
	// Key is the folder the trust record is kept under. For a repository
	// it is the main checkout, so that worktrees the repository lists
	// share its trust; otherwise it is Root.
	Key string
	// Repo reports that Start is inside a git repository.
	Repo bool
	// Worktree reports that Root is a linked worktree which its repository
	// lists as its own, so that Key is the repository's main checkout.
	Worktree bool
	// SessionOnly reports that Key is the home directory, or a directory
	// that holds it. Such a folder is trusted for one session at a time
	// and is never recorded: a record of it would cover everything the
	// user has.
	SessionOnly bool
	// GitDir is the git directory git would read for Root, and CommonDir
	// the one a linked worktree shares with its repository (the same
	// directory for an ordinary checkout). Both are empty outside a
	// repository.
	GitDir, CommonDir string
	// Notes say, each in a sentence, why Key is not the folder a
	// repository pointed at: a shortcut that was refused.
	Notes []string
}

// note records why a shortcut was refused.
func (f *Folder) note(format string, args ...any) {
	f.Notes = append(f.Notes, fmt.Sprintf(format, args...))
}

// Resolve works out the Folder for the directory start, which must be an
// absolute path. home is the user's home directory, or "" when there is
// none to protect.
func Resolve(start, home string) (Folder, error) {
	if !filepath.IsAbs(start) {
		return Folder{}, fmt.Errorf("trust: the folder %q is not an absolute path", start)
	}
	real, err := filepath.EvalSymlinks(start)
	if err != nil {
		return Folder{}, fmt.Errorf("the folder %s cannot be looked at, so nothing can be said about trusting it: %w", start, err)
	}
	if info, err := os.Stat(real); err != nil || !info.IsDir() {
		return Folder{}, fmt.Errorf("%s is not a folder, so it cannot be trusted", start)
	}
	if home != "" {
		if r, err := filepath.EvalSymlinks(home); err == nil {
			home = r
		} else {
			home = filepath.Clean(home)
		}
	}

	f := Folder{Start: real, Root: real, Key: real}
	for dir := real; ; dir = filepath.Dir(dir) {
		dotgit := filepath.Join(dir, ".git")
		if li, err := os.Lstat(dotgit); err == nil {
			if home != "" && (dir == home || holds(dir, home)) {
				// A repository at or above the home directory (dotfiles
				// kept in git) is not this folder's project.
				if dir != real {
					f.note("the git repository at %s holds the home directory, so it is not taken as this folder's project", dir)
				}
			} else {
				f.git(dir, dotgit, li)
			}
			break
		}
		if dir == filepath.Dir(dir) {
			break
		}
	}
	f.SessionOnly = home != "" && (f.Key == home || holds(f.Key, home))
	return f, nil
}

// holds reports whether path lies under dir, and is not dir itself.
func holds(dir, path string) bool {
	rel, err := filepath.Rel(dir, path)
	return err == nil && rel != "." && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

// git works out Root and Key for a folder that has a .git entry.
func (f *Folder) git(dir, dotgit string, li fs.FileInfo) {
	f.Repo, f.Root, f.Key = true, dir, dir
	switch {
	case li.Mode()&fs.ModeSymlink != 0:
		// A link. Whatever it leads to, this folder borrows nothing
		// through it: a check that followed such a link once let a folder
		// pass as a trusted repository's worktree.
		if target, err := filepath.EvalSymlinks(dotgit); err == nil {
			if ti, err := os.Stat(target); err == nil && ti.IsDir() {
				f.GitDir, f.CommonDir = target, target
			} else if gd, ok := gitDirOf(filepath.Dir(target), target); ok {
				f.GitDir, f.CommonDir = gd, gd
			}
		}
		f.note("the .git in %s is a link, not an ordinary file or folder, so the folder is keyed on itself and shares no other repository's trust", dir)
	case li.IsDir():
		f.GitDir, f.CommonDir = dotgit, dotgit
		if why := workTreeElsewhere(dotgit, dir); why != "" {
			f.Root, f.Key = f.Start, f.Start
			f.note("%s", why)
		}
	case li.Mode().IsRegular():
		f.linked(dir, dotgit)
	default:
		f.note("the .git in %s is neither a file nor a folder, so the folder is keyed on itself", dir)
	}
}

// gitDirOf reads the "gitdir: <path>" line of a .git file and returns the
// git directory it names, with links resolved.
func gitDirOf(dir, dotgit string) (string, bool) {
	text, err := readSmall(dotgit)
	if err != nil {
		return "", false
	}
	gd, ok := strings.CutPrefix(firstLine(text), "gitdir:")
	if !ok {
		return "", false
	}
	gd = strings.TrimSpace(gd)
	if gd == "" {
		return "", false
	}
	if !filepath.IsAbs(gd) {
		gd = filepath.Join(dir, gd)
	}
	real, err := filepath.EvalSymlinks(gd)
	if err != nil {
		return "", false
	}
	return real, true
}

// linked works out Key for a folder whose .git is an ordinary file, as a
// linked worktree's is. The folder shares its repository's trust only when
// the repository itself lists the folder as one of its worktrees: the
// record must sit in the repository's own worktrees folder, and must name
// this folder's .git. A folder cannot borrow a repository's trust by
// naming it in a .git of its own.
func (f *Folder) linked(dir, dotgit string) {
	gd, ok := gitDirOf(dir, dotgit)
	if !ok {
		f.note("the .git file in %s does not name a git directory that is there, so the folder is keyed on itself", dir)
		return
	}
	f.GitDir, f.CommonDir = gd, gd
	if why := workTreeElsewhere(gd, dir); why != "" {
		f.Root, f.Key = f.Start, f.Start
		f.note("%s", why)
		return
	}
	text, err := readSmall(filepath.Join(gd, "commondir"))
	if err != nil {
		// No commondir: a repository of its own whose git directory is
		// kept somewhere else, as a submodule's is. It is keyed on itself.
		return
	}
	common := strings.TrimSpace(firstLine(text))
	if !filepath.IsAbs(common) {
		common = filepath.Join(gd, common)
	}
	common, err = filepath.EvalSymlinks(common)
	if err != nil {
		f.note("the git directory %s names a repository that is not there, so the folder is keyed on itself", gd)
		return
	}
	f.CommonDir = common
	if filepath.Dir(gd) != filepath.Join(common, "worktrees") {
		f.note("the .git file in %s points at %s, which is not in the worktrees folder of the repository it names (%s), so the folder is keyed on itself and does not share that repository's trust", dir, gd, common)
		return
	}
	back, err := readSmall(filepath.Join(gd, "gitdir"))
	named := strings.TrimSpace(firstLine(back))
	if err == nil && named != "" && !filepath.IsAbs(named) {
		named = filepath.Join(gd, named)
	}
	if err != nil || !sameEntry(named, dotgit) {
		f.note("the repository at %s does not list %s as one of its worktrees, so the folder is keyed on itself and does not share that repository's trust", common, dir)
		return
	}
	main := common
	if filepath.Base(common) == ".git" {
		main = filepath.Dir(common)
		if why := workTreeElsewhere(common, main); why != "" {
			f.note("%s", why)
			return
		}
	}
	f.Worktree, f.Key = true, main
}

// sameEntry reports whether the path a repository recorded for a worktree
// is this folder's .git. The directories on the way are resolved; the .git
// itself is not followed.
func sameEntry(named, dotgit string) bool {
	if named == "" || !filepath.IsAbs(named) {
		return false
	}
	parent, err := filepath.EvalSymlinks(filepath.Dir(named))
	if err != nil {
		return false
	}
	return filepath.Join(parent, filepath.Base(named)) == dotgit
}

// workTreeElsewhere reads a git directory's own settings and returns a
// sentence when they place the work tree somewhere other than dir, or
// when that cannot be told. A downloaded repository once used this
// setting to move gnosh's idea of where the project ends, so that the
// project's own settings file counted as outside the project. Such a
// repository is keyed on the folder gnosh was started in.
func workTreeElsewhere(gitDir, dir string) string {
	for _, name := range []string{"config", "config.worktree"} {
		path := filepath.Join(gitDir, name)
		text, err := readSmall(path)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return fmt.Sprintf("the repository's own git settings (%s) could not be read, so where its work tree is cannot be told; the folder is taken to be the one gnosh was started in", path)
		}
		settings, ok := parseGitConfig(text)
		if !ok {
			return fmt.Sprintf("the repository's own git settings (%s) could not be understood, so where its work tree is cannot be told; the folder is taken to be the one gnosh was started in", path)
		}
		for _, s := range settings {
			switch {
			case s.section == "include" || s.section == "includeif":
				return fmt.Sprintf("the repository's own git settings (%s) pull in another file, so where its work tree is cannot be told; the folder is taken to be the one gnosh was started in", path)
			case s.section == "core" && s.sub == "" && s.key == "worktree":
				tree := s.value
				if !filepath.IsAbs(tree) {
					tree = filepath.Join(gitDir, tree)
				}
				if real, err := filepath.EvalSymlinks(tree); err == nil {
					tree = real
				}
				if filepath.Clean(tree) != dir {
					return fmt.Sprintf("the repository's own git settings (%s) place its work tree at %s, so the folder is taken to be the one gnosh was started in", path, Escape(filepath.Clean(tree)))
				}
			}
		}
	}
	return ""
}

// maxSmall is the most that is read of a file that should hold a line or
// two.
const maxSmall = 1 << 20

// readSmall reads a small ordinary file. What is not one is refused before
// it is opened: opening a named pipe waits for a writer, and a repository
// (an archive unpacked) can hold one where its git settings would be, so
// starting gnosh there hung (the config review of 2026-11-19).
func readSmall(path string) (string, error) {
	if info, err := os.Stat(path); err != nil {
		return "", err
	} else if !info.Mode().IsRegular() {
		return "", fmt.Errorf("%s is not an ordinary file", path)
	}
	file, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer file.Close()
	if info, err := file.Stat(); err != nil || !info.Mode().IsRegular() {
		return "", fmt.Errorf("%s is not an ordinary file", path)
	}
	data, err := io.ReadAll(io.LimitReader(file, maxSmall))
	if err != nil {
		return "", err
	}
	return string(data), nil
}

// firstLine returns the first line of text, without its line break.
func firstLine(text string) string {
	line, _, _ := strings.Cut(text, "\n")
	return strings.TrimRight(line, "\r")
}
