package app

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/charmbracelet/crush/internal/home"
)

// AddDir adds a working directory for the session, as Claude Code's
// /add-dir does: path, from the working directory, ~ for home, is read
// without asking from now on, as the working directory is, and named in
// the coder's system prompt. It says what it did or why it did
// not, in Claude Code's words, and whether it added the directory.
func (a *App) AddDir(ctx context.Context, path string) (string, bool, error) {
	path = strings.TrimSpace(path)
	if path == "" {
		return "Please provide a directory path.", false, nil
	}
	workingDir := a.config.WorkingDir()
	abs := home.Long(path)
	if !filepath.IsAbs(abs) {
		abs = filepath.Join(workingDir, abs)
	}
	abs = filepath.Clean(abs)
	info, err := os.Stat(abs)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return fmt.Sprintf("Path %s was not found.", abs), false, nil
	case err != nil:
		return fmt.Sprintf("Path %s couldn't be resolved, so it can't be used as a working directory. Check the path.", path), false, nil
	case !info.IsDir():
		return fmt.Sprintf("%s is not a directory. Did you mean to add the parent directory %s?", path, filepath.Dir(abs)), false, nil
	}
	for i, root := range append([]string{workingDir}, a.config.AddedDirs()...) {
		rel, err := filepath.Rel(root, abs)
		if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			continue
		}
		switch {
		case rel == "." && i == 0:
			return fmt.Sprintf("%s is already the current working directory.", path), false, nil
		case rel == ".":
			return fmt.Sprintf("%s is already added as a working directory.", path), false, nil
		case i == 0:
			return fmt.Sprintf("%s is already accessible within the current working directory %s.", path, root), false, nil
		default:
			return fmt.Sprintf("%s is already accessible within the additional working directory %s.", path, root), false, nil
		}
	}
	a.config.AddDir(abs)
	a.Permissions.AddDir(abs)
	if a.AgentCoordinator != nil {
		if err := a.AgentCoordinator.RebuildSystemPrompts(ctx); err != nil {
			return "", false, fmt.Errorf("added %s, but could not tell the agent: %w", abs, err)
		}
	}
	return fmt.Sprintf("Added %s as a working directory for this session.", abs), true, nil
}
