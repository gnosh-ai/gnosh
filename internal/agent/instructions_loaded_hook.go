package agent

import (
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/charmbracelet/crush/internal/agent/prompt"
	"github.com/charmbracelet/crush/internal/config"
	"github.com/charmbracelet/crush/internal/hooks"
)

// instructionsLoadedState is what the InstructionsLoaded hooks of a
// process's sessions need to know: which sessions have already had their
// system prompt's context files fired, so the hooks run once per session
// start (and once more after a summary). A coordinator's main agents
// share one, so a session started in code mode counts as started in plan
// mode, and a summary made in either fires "compact" for the next prompt
// in either.
type instructionsLoadedState struct {
	mu sync.Mutex
	// claimed records the sessions that have fired their session_start
	// hooks. A summarized session is re-claimed with "compact".
	claimed map[string]bool
	// summary records the sessions that were summarized since their
	// last fire, so the next claim fires with "compact".
	summary map[string]bool
}

func newInstructionsLoadedState() *instructionsLoadedState {
	return &instructionsLoadedState{
		claimed: make(map[string]bool),
		summary: make(map[string]bool),
	}
}

// markSummarized records that the session was summarized, so the next
// claim fires with load_reason "compact".
func (s *instructionsLoadedState) markSummarized(sessionID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.summary[sessionID] = true
}

// claim atomically decides whether to fire the hooks for the session and
// what load reason to use. It returns the load reason and whether the
// hooks should fire. The check-and-mark is a single lock acquisition so
// that two concurrent callers cannot both pass the "not started" check.
//
// If the session was never started, it marks it started and returns
// "session_start". If it was started and then summarized, it clears the
// summary flag, re-marks started, and returns "compact". If it was
// started and not summarized, it returns false.
func (s *instructionsLoadedState) claim(sessionID string) (loadReason string, shouldFire bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	compacted := s.summary[sessionID]
	s.summary[sessionID] = false
	if !compacted && s.claimed[sessionID] {
		return "", false
	}
	s.claimed[sessionID] = true
	if compacted {
		return hooks.InstructionsLoadedCompact, true
	}
	return hooks.InstructionsLoadedSessionStart, true
}

// promptContextFile is what fireInstructionsLoadedHooks needs of a
// context file: its path, what imported it, and whether it is one of
// the user's global context files (as opposed to a project file).
type promptContextFile struct {
	path   string
	parent string
	global bool
}

// fireInstructionsLoadedHooks runs cfg's InstructionsLoaded hooks for
// the given files, once per file, as in Claude Code: the hooks cannot
// block or change anything, a failing hook is logged, and the caller
// does not wait for them. loadReason is the reason the files were loaded
// (session_start, compact, or nested_traversal); a file with a parent
// gets include regardless. triggerFilePath is the file whose access
// triggered a nested_traversal load.
//
// It runs in a goroutine on a background context: the caller's turn is
// not held up, and a turn cancellation does not kill the hooks. The
// hooks are for observability only.
func fireInstructionsLoadedHooks(cfg *config.ConfigStore, sessionID string, files []promptContextFile, loadReason, triggerFilePath string) {
	if cfg == nil {
		return
	}
	hookCfgs := cfg.Config().Hooks[hooks.EventInstructionsLoaded]
	if len(hookCfgs) == 0 || len(files) == 0 {
		return
	}
	workingDir := cfg.WorkingDir()
	if workingDir == "" {
		wd, err := os.Getwd()
		if err != nil {
			slog.Warn("InstructionsLoaded hooks are configured but no working directory is available; not running them",
				"session_id", sessionID,
				"error", err,
			)
			return
		}
		workingDir = wd
	}
	// The file paths are resolved to absolute before the goroutine so the
	// goroutine does no file IO.
	for i := range files {
		if !filepath.IsAbs(files[i].path) {
			files[i].path = filepath.Join(workingDir, files[i].path)
		}
		if files[i].parent != "" && !filepath.IsAbs(files[i].parent) {
			files[i].parent = filepath.Join(workingDir, files[i].parent)
		}
	}

	go func() {
		runner := hooks.NewRunner(hookCfgs, workingDir, workingDir)
		for _, f := range files {
			memoryType := memoryTypeFor(f.path, f.global)
			reason := loadReason
			if f.parent != "" {
				reason = hooks.InstructionsLoadedInclude
			}
			_, err := runner.RunInstructionsLoaded(
				context.Background(), sessionID, f.path, memoryType, reason,
				triggerFilePath, f.parent,
			)
			if err != nil {
				slog.Warn("InstructionsLoaded hook execution error",
					"session_id", sessionID,
					"file", f.path,
					"reason", reason,
					"error", err,
				)
			}
		}
	}()
}

// memoryTypeFor reports the Claude Code memory type of a context file:
// "User" for one of the user's global context files, "Local" for a
// project .local.md file, "Project" for the rest.
func memoryTypeFor(path string, global bool) string {
	if global {
		return hooks.MemoryTypeUser
	}
	base := filepath.Base(path)
	if strings.HasSuffix(base, ".local.md") {
		return hooks.MemoryTypeLocal
	}
	return hooks.MemoryTypeProject
}

// sessionInstructionsFiles returns the context files that go into the
// system prompt: the user's global context files and the project's, each
// with its import parent, for the InstructionsLoaded hooks to fire on.
// cfg must have non-nil Options; a nil store or a store whose Options
// are nil returns nothing.
func sessionInstructionsFiles(cfg *config.ConfigStore) []promptContextFile {
	if cfg == nil {
		return nil
	}
	cfgData := cfg.Config()
	if cfgData.Options == nil {
		return nil
	}
	var out []promptContextFile
	globalFiles := prompt.LoadContextFiles(cfgData.Options.GlobalContextPaths, cfg, "")
	for _, f := range globalFiles {
		out = append(out, promptContextFile{path: f.Path, parent: f.Parent, global: true})
	}
	projectFiles := prompt.ProjectContextFiles(cfgData.Options.ContextPaths, cfg)
	for _, f := range projectFiles {
		out = append(out, promptContextFile{path: f.Path, parent: f.Parent})
	}
	return out
}

// fireInstructionsLoadedForSession runs the InstructionsLoaded hooks for
// the context files in the system prompt, once per session start, and
// again with load_reason "compact" after a summary. It is called from
// Run, at the same point SessionStart hooks fire, so it runs whether or
// not any SessionStart hooks are configured.
//
// The check-and-mark is a single lock acquisition so that two concurrent
// Run calls for the same session cannot both pass the "not started" check
// and fire the hooks twice.
//
// The hooks run on a background context, not the turn's: the hooks are
// for observability and must not be killed by a turn cancellation (Esc).
// If the turn is cancelled before the hooks finish, the session still
// counts as started, and the hooks are not re-fired on the next prompt.
func (a *sessionAgent) fireInstructionsLoadedForSession(sessionID string) {
	if a.instructionsLoaded == nil || a.isSubAgent || a.cfg == nil {
		return
	}
	if len(a.cfg.Config().Hooks[hooks.EventInstructionsLoaded]) == 0 {
		return
	}
	// Atomically check and mark: if the session was already started and
	// not summarized since, return without firing. If it was summarized,
	// clear the summary flag and fire with "compact". Otherwise mark
	// started and fire with "session_start".
	loadReason, shouldFire := a.instructionsLoaded.claim(sessionID)
	if !shouldFire {
		return
	}
	fireInstructionsLoadedHooks(a.cfg, sessionID, sessionInstructionsFiles(a.cfg), loadReason, "")
}

// markInstructionsLoadedSummarized is called from summarize so the next
// prompt fires the InstructionsLoaded hooks again, with load_reason
// "compact".
func (a *sessionAgent) markInstructionsLoadedSummarized(sessionID string) {
	if a.instructionsLoaded != nil {
		a.instructionsLoaded.markSummarized(sessionID)
	}
}
