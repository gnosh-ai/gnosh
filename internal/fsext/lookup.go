package fsext

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/charmbracelet/crush/internal/home"
)

// LookupClosestBounded searches for targets starting from dir and walking
// up the directory tree, and returns the full path of the closest one. The
// walk inspects dir, then each ancestor up to and including stopDir, then
// terminates regardless of whether a target was found. Use this when the
// caller wants to avoid adopting matches from outside a project boundary
// (for example a sibling worktree or a parent project).
//
// If stopDir is empty, only dir itself is searched; a stopDir of "/" walks
// to the filesystem root. If stopDir is not an ancestor of dir, the walk
// still terminates at the filesystem root. Whatever stopDir says, a target
// found in the home directory, or owned by someone other than dir's owner,
// ends the search without a match.
//
// Given several targets, each directory is checked for all of them, in
// order, before the walk moves up: the closest directory holding any of
// them wins, and within one directory the earlier target wins. Looking
// for one target all the way up before trying the next would let a
// farther match of the first beat a closer match of the second.
func LookupClosestBounded(dir, stopDir string, targets ...string) (string, bool) {
	var found string

	err := traverseUpBounded(dir, stopDir, func(cwd string, owner int) error {
		for _, target := range targets {
			fpath := filepath.Join(cwd, target)

			err := probeEnt(fpath, owner)
			if errors.Is(err, os.ErrNotExist) {
				continue
			}

			if err != nil {
				return fmt.Errorf("error probing file %s: %w", fpath, err)
			}

			if cwd == home.Dir() {
				return filepath.SkipAll
			}

			found = fpath
			return filepath.SkipAll
		}
		return nil
	})

	return found, err == nil && found != ""
}

// LookupBounded returns the full path of every target found in dir and in
// each ancestor up to and including stopDir, skipping any owned by someone
// other than dir's owner. If stopDir is empty, only dir itself is
// searched; a stopDir of "/" walks to the filesystem root.
func LookupBounded(dir, stopDir string, targets ...string) ([]string, error) {
	if len(targets) == 0 {
		return nil, nil
	}

	var found []string

	err := traverseUpBounded(dir, stopDir, func(cwd string, owner int) error {
		for _, target := range targets {
			fpath := filepath.Join(cwd, target)
			err := probeEnt(fpath, owner)

			// skip to the next file on permission denied
			if errors.Is(err, os.ErrNotExist) ||
				errors.Is(err, os.ErrPermission) {
				continue
			}

			if err != nil {
				return fmt.Errorf("error probing file %s: %w", fpath, err)
			}

			found = append(found, fpath)
		}

		return nil
	})
	if err != nil {
		return nil, err
	}

	return found, nil
}

// ForeignOwnedBounded returns the targets that exist in dir and its
// ancestors up to stopDir, the places LookupBounded searches, but that
// LookupBounded passes over because someone other than dir's owner owns
// them, so a caller can say so instead of ignoring them in silence.
func ForeignOwnedBounded(dir, stopDir string, targets ...string) ([]string, error) {
	var found []string
	err := traverseUpBounded(dir, stopDir, func(cwd string, owner int) error {
		found = append(found, foreignOwnedIn(cwd, owner, targets)...)
		return nil
	})
	if err != nil {
		return nil, err
	}
	return found, nil
}

// foreignOwnedIn returns the targets in dir that exist and are owned by
// someone other than owner. An owner of -1 turns the check off, as it
// does in probeEnt.
func foreignOwnedIn(dir string, owner int, targets []string) []string {
	if owner == -1 {
		return nil
	}
	var found []string
	for _, target := range targets {
		fpath := filepath.Join(dir, target)
		if _, err := os.Stat(fpath); err != nil {
			continue
		}
		if fowner, err := Owner(fpath); err == nil && fowner != owner {
			found = append(found, fpath)
		}
	}
	return found
}

// traverseUpBounded walks up from dir, visiting each ancestor up to and
// including stopDir, then terminates. If stopDir is empty, only dir
// itself is visited; a stopDir of "/" walks to the filesystem root. If
// stopDir is set but is not an ancestor of dir the walk still stops at the
// filesystem root, so callers cannot accidentally produce an infinite walk
// by passing a sibling path.
//
// Boundary comparison is performed against symlink-resolved paths so
// that callers passing logically equivalent paths (a symlinked /var vs
// the underlying /private/var, for example) still terminate at the
// expected directory.
func traverseUpBounded(dir, stopDir string, walkFn func(dir string, owner int) error) error {
	cwd, err := filepath.Abs(dir)
	if err != nil {
		return fmt.Errorf("cannot convert CWD to absolute path: %w", err)
	}

	stop := cwd
	if stopDir != "" {
		stop, err = filepath.Abs(stopDir)
		if err != nil {
			return fmt.Errorf("cannot convert stop dir to absolute path: %w", err)
		}
	}
	canonStop := canonicalize(stop)

	owner, err := Owner(dir)
	if err != nil {
		return fmt.Errorf("cannot get ownership: %w", err)
	}

	for {
		err := walkFn(cwd, owner)
		if err == nil || errors.Is(err, filepath.SkipDir) {
			if canonicalize(cwd) == canonStop {
				return nil
			}

			parent := filepath.Dir(cwd)
			if parent == cwd {
				return nil
			}

			cwd = parent
			continue
		}

		if errors.Is(err, filepath.SkipAll) {
			return nil
		}

		return err
	}
}

// canonicalize resolves any symbolic links in path. If resolution fails
// (typically because path does not exist yet) the original path is
// returned cleaned, so callers can still perform stable equality checks.
func canonicalize(path string) string {
	if resolved, err := filepath.EvalSymlinks(path); err == nil {
		return resolved
	}
	return filepath.Clean(path)
}

// probeEnt checks if entity at given path exists and belongs to given owner
func probeEnt(fspath string, owner int) error {
	_, err := os.Stat(fspath)
	if err != nil {
		return fmt.Errorf("cannot stat %s: %w", fspath, err)
	}

	// special case for ownership check bypass
	if owner == -1 {
		return nil
	}

	fowner, err := Owner(fspath)
	if err != nil {
		return fmt.Errorf("cannot get ownership for %s: %w", fspath, err)
	}

	if fowner != owner {
		return os.ErrPermission
	}

	return nil
}
