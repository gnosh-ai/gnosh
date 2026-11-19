package permission

import (
	"path"
)

// Protected paths hold a repository's state, shell startup files, keys,
// and the settings of gnosh and of other tools. A change to one always
// asks, whatever rule allows the tool: only skipping permissions
// altogether skips the question. Names are compared by their keys
// (guardKey): without regard to letter case, to how an accent is written,
// or to characters and trailing dots that some file systems leave out.

// protectedDirs are directory names that protect everything under them,
// wherever they appear in a path.
var protectedDirs = map[string]bool{
	".git": true, ".vscode": true, ".idea": true, ".husky": true,
	".cargo": true, ".devcontainer": true, ".yarn": true, ".mvn": true,
	".claude": true, ".gnosh": true, ".ssh": true, ".gnupg": true,
}

// protectedFiles are file names that are protected wherever they are.
var protectedFiles = map[string]bool{
	".gitconfig": true, ".gitmodules": true,
	".bashrc": true, ".bash_profile": true, ".bash_login": true,
	".bash_aliases": true, ".bash_logout": true, ".zshrc": true,
	".zprofile": true, ".zshenv": true, ".zlogin": true, ".zlogout": true,
	".profile": true, ".envrc": true,
	".npmrc": true, ".yarnrc": true, ".yarnrc.yml": true, ".pnp.cjs": true,
	".pnp.loader.mjs": true, ".pnpmfile.cjs": true, "bunfig.toml": true,
	".bunfig.toml": true,
	".bazelrc":     true, ".bazelversion": true, ".bazeliskrc": true,
	".pre-commit-config.yaml": true, "lefthook.yml": true,
	"lefthook.yaml": true, ".lefthook.yml": true, ".lefthook.yaml": true,
	"gradle-wrapper.properties": true, "maven-wrapper.properties": true,
	".devcontainer.json": true, ".ripgreprc": true, "pyrightconfig.json": true,
	".mcp.json": true, ".claude.json": true,
	// gnosh's own settings: gnosh.json in the user's config directory and
	// in the project. (The project's settings.local.json lies in .gnosh,
	// which is protected whole.)
	"gnosh.json": true, ".netrc": true,
}

// ProtectsName reports whether a folder of this name protects everything
// under it, wherever it is: .git, .ssh, .gnosh and the others.
func ProtectsName(name string) bool { return protectedDirs[guardKey(name)] }

// protectedWhy says why the cleaned absolute path p is protected, or
// returns "" when it is not.
func (e *Engine) protectedWhy(p string) string {
	low := guardPath(p)
	segs := splitPath(low)
	// Inside a work tree gnosh made for a sub-agent, the path to the work
	// tree (the folder of work trees and the work tree's own name) is not
	// judged: the rest is a checkout of the project. A work tree named as a
	// protected folder (.git) is no such checkout: git would take it for a
	// repository's state.
	tree, trees := 0, ""
	for _, w := range e.workTrees {
		if n := len(splitPath(w)) + 1; inside(w, low) && len(segs) > n && !protectedDirs[segs[n-1]] {
			tree, trees = n, w
		}
	}
	for i, s := range segs {
		switch {
		case i < tree:
		case s == ".claude" && i+1 < len(segs) && segs[i+1] == "worktrees":
			// Claude Code keeps whole checkouts here; they are ordinary
			// project files.
		case protectedDirs[s]:
			return "it is inside " + s + ", which holds a repository's state, keys or a tool's own settings"
		case s == ".config" && i+1 < len(segs) && segs[i+1] == "git":
			return "it is inside .config/git, which holds git's settings"
		}
	}
	if len(segs) > 0 && protectedFiles[segs[len(segs)-1]] {
		return path.Base(p) + " is a settings or startup file that programs run or obey"
	}
	for _, x := range e.protected {
		if inside(x, low) && !(tree > 0 && inside(x, trees)) {
			return "it is one of gnosh's own files"
		}
	}
	return ""
}
