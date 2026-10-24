package permission

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
)

// acceptEditsTools are the tools whose edits acceptEdits lets run without
// asking: the file edits of Claude Code's acceptEdits. The names are
// internal/agent/tools', which imports this package. lsp_rename and
// lsp_replace_symbol, which change files too, still ask.
var acceptEditsTools = map[string]bool{"edit": true, "multiedit": true, "write": true}

// protectedDirs and protectedFiles are the paths acceptEdits never lets
// an edit of run unasked, as Claude Code's never does: what they hold
// runs code or sets things up (git hooks and config, a shell's startup
// files, editor and package-manager settings, Claude Code's and Klash's
// own settings), so an edit there is asked about in every mode but yolo.
// A directory counts anywhere in a path; ".config/git" is two names in a
// row.
var (
	protectedDirs = []string{
		".git", ".vscode", ".idea", ".husky", ".cargo", ".devcontainer", ".yarn", ".mvn",
		".claude", ".klash", ".crush",
	}
	protectedFiles = []string{
		".gitconfig", ".gitmodules", ".bashrc", ".bash_profile", ".bash_login", ".profile",
		".zshrc", ".zshenv", ".zprofile", ".zlogin", ".npmrc", ".mcp.json", ".claude.json",
		"klash.json", ".klash.json", "crush.json", ".crush.json",
	}
)

// acceptsEdit reports whether acceptEdits lets an edit of path run
// without asking, as Claude Code's acceptEdits does: path is absolute,
// lies in the working directory or a directory added with --add-dir once
// its symlinks are resolved, and is not, as given or resolved, one of the
// protected paths or in one. Anything else is asked about as without
// acceptEdits. Before, the mode put the edit tools in the allowed tools,
// which let them write anywhere unasked: a shell's startup file, a git
// hook, Klash's own config.
func (s *permissionService) acceptsEdit(path string) bool {
	if !filepath.IsAbs(path) {
		return false
	}
	path = filepath.Clean(path)
	real, ok := resolvedPath(path)
	if !ok || protected(path) || protected(real) {
		return false
	}
	for _, root := range append([]string{s.workingDir}, s.addedDirs...) {
		if root == "" {
			continue
		}
		if realRoot, ok := resolvedPath(root); ok && within(realRoot, real) {
			return true
		}
	}
	return false
}

// resolvedPath is path with every symlink resolved, the part of it that
// does not exist yet (a file to be written, its new directories) taken
// as it is under its nearest existing directory. ok is false for a path
// that ends in a symlink that leads nowhere: writing it would make a file
// wherever the link points.
func resolvedPath(path string) (string, bool) {
	var rest []string
	for dir := path; ; dir = filepath.Dir(dir) {
		real, err := filepath.EvalSymlinks(dir)
		if err == nil {
			return filepath.Join(append([]string{real}, rest...)...), true
		}
		if info, lerr := os.Lstat(dir); lerr == nil && info.Mode()&os.ModeSymlink != 0 {
			return "", false
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", false
		}
		rest = append([]string{filepath.Base(dir)}, rest...)
	}
}

// within reports whether path is root or lies in it.
func within(root, path string) bool {
	rel, err := filepath.Rel(root, path)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

// protected reports whether path is or lies in one of the protected
// paths.
func protected(path string) bool {
	names := strings.Split(filepath.ToSlash(path), "/")
	for i, name := range names {
		if slices.Contains(protectedDirs, name) || (name == ".config" && i+1 < len(names) && names[i+1] == "git") {
			return true
		}
	}
	return slices.Contains(protectedFiles, names[len(names)-1])
}
