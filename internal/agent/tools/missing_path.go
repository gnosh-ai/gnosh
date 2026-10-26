package tools

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// A path that is not there is answered in Claude Code's words: the tool
// says so, names the working directory, and guesses the path meant.

// missingFile is what view and the edit tools answer for path, a file
// that does not exist, as Claude Code's Read and Edit do.
func missingFile(path, workingDir string) string {
	msg := "File does not exist. " + workingDirNote(workingDir)
	meant := pathMeant(path, workingDir)
	if meant == "" {
		meant = sameNameFile(path)
	}
	if meant != "" {
		msg += " Did you mean " + meant + "?"
	}
	return msg
}

// missingSearchPath is what grep and ls answer when path, the file or
// directory they were asked to look in (abs is it resolved), does not
// exist, as Claude Code's Grep does. It is "" when the path is there, or
// when what is wrong with it is for the search to report.
func missingSearchPath(path, abs, workingDir string) string {
	if _, err := os.Stat(abs); err == nil || !errors.Is(err, fs.ErrNotExist) {
		return ""
	}
	msg := fmt.Sprintf("Path does not exist: %s. %s", path, workingDirNote(workingDir))
	if meant := pathMeant(abs, workingDir); meant != "" {
		msg += " Did you mean " + meant + "?"
	}
	return msg
}

// workingDirNote is Claude Code's note to a path that is not there.
func workingDirNote(workingDir string) string {
	return "Note: your current working directory is " + workingDir + "."
}

// pathMeant guesses, as Claude Code does, the path meant by abs, one that
// does not exist beside the working directory: the same path inside the
// working directory, as when a model leaves the project's own name out
// of a path. "" when that is not there either.
func pathMeant(abs, workingDir string) string {
	rel, err := filepath.Rel(filepath.Dir(workingDir), abs)
	if err != nil || rel == "." || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return ""
	}
	if within, err := filepath.Rel(workingDir, abs); err == nil && within != ".." && !strings.HasPrefix(within, ".."+string(filepath.Separator)) {
		return ""
	}
	meant := filepath.Join(workingDir, rel)
	if _, err := os.Stat(meant); err != nil {
		return ""
	}
	return meant
}

// extensionRE is what Claude Code takes for another extension in a
// guess: none, or a dot and up to 32 letters, digits or _+~#-.
var extensionRE = regexp.MustCompile(`^(\.[\p{L}\p{N}_+~#-]{1,32})?$`)

// sameNameFile guesses, as Claude Code does, the file meant by path, a
// file that does not exist: the first one beside it with the same name
// and another extension, as main.ts for main.js, or README.md for
// README. "" when there is none.
func sameNameFile(path string) string {
	dir, name := filepath.Split(path)
	stem := strings.TrimSuffix(name, extension(name))
	entries, err := os.ReadDir(dir)
	if err != nil {
		return ""
	}
	for _, entry := range entries {
		ext := extension(entry.Name())
		if entry.Name() != name && strings.TrimSuffix(entry.Name(), ext) == stem && extensionRE.MatchString(ext) {
			return filepath.Join(dir, entry.Name())
		}
	}
	return ""
}

// extension is a file name's extension as Node's path.extname has it,
// as Claude Code's guess does: a leading dot starts none, so .env is all
// name and .env.local is .env with the extension .local.
func extension(name string) string {
	if i := strings.LastIndexByte(name, '.'); i > 0 {
		return name[i:]
	}
	return ""
}

// deniedPath explains a "permission denied" for abs, the path a tool was
// asked for, or "" when that is not what went wrong.
//
// The bare error names the path asked for, which reads as though it is
// there and merely locked: on the AI box, view of /root/renders answered
// "permission denied" because /root is not readable by the user klash runs
// as, and the agent spent its turn hunting for sudo. /root/renders does
// not exist on that machine at all — it is a directory on another host —
// but the kernel cannot say so without first reading /root. Naming the
// directory that actually refused tells the agent what it is up against,
// and that no file of its own is missing.
func deniedPath(abs string) string {
	return deniedPathWith(abs, os.Lstat, os.ReadDir)
}

// deniedPathWith is deniedPath over the given lstat and readDir, so the
// walk can be tested without a directory the test user cannot read: the
// box runs its tests as root, and root is refused nothing.
func deniedPathWith(
	abs string,
	lstat func(string) (fs.FileInfo, error),
	readDir func(string) ([]os.DirEntry, error),
) string {
	if abs == "" {
		return ""
	}
	if _, err := lstat(abs); err == nil || !errors.Is(err, fs.ErrPermission) {
		return ""
	}
	// Walk up to the first directory that can be listed. The one below it
	// is the one that refused.
	blocked := abs
	for parent := filepath.Dir(abs); parent != blocked; parent = filepath.Dir(parent) {
		if _, err := readDir(parent); err == nil {
			break
		}
		blocked = parent
	}
	msg := fmt.Sprintf("Permission denied: %s cannot be read by the user this agent runs as", blocked)
	if blocked != abs {
		msg += fmt.Sprintf(", so nothing under it can be reached, including %s", abs)
	}
	return msg + ". Whether anything is there cannot be told from here, so do not assume the path exists; if it belongs to another machine, reach it the way that machine is reached."
}
