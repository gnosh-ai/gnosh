package agent

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	"charm.land/fantasy"
	"github.com/charmbracelet/crush/internal/agent/tools"
	"github.com/charmbracelet/crush/internal/filetracker"
)

// changedReadFiles wraps a tool, bash, so that a call that changes files
// the session has read says which. The agent's next edit of such a file
// is refused until it views the file again; without being told, it found
// out by having the edit refused, a model round trip each time (runs 13
// and 14, after their own git stash and Python edits). Claude Code tells
// its model of a read file that changed, too. A file the call rewrote as
// it was, as git stash and pop do, is not named.
type changedReadFiles struct {
	fantasy.AgentTool
	files      filetracker.Service
	workingDir string
	// named is what the session has been told changed since it read it.
	// A change is told once, and a file is named again only when it
	// changes after it was named, or after the agent reads it again:
	// reading again records the new content as read, so it is no longer
	// out of date. It is shared by every tool of an agent, as in
	// Claude Code.
	named *namedChanges
}

func (t *changedReadFiles) Unwrap() fantasy.AgentTool { return t.AgentTool }

// namedChanges is the files a session has been told changed since it
// read them. A change is told once, and a file is named again only when
// it changes after it was named, or after the agent reads it again. The
// content at naming is tracked so a second change without an intervening
// re-read is named again. It is keyed by sessionID and shared across
// buildTools calls, so a coordinator rebuild does not reset the dedup
// state. The mutex guards the map: the main agent and a sub-agent can
// run tool calls concurrently, and Go does not allow concurrent map
// access even for different keys.
type namedChanges struct {
	mu        sync.Mutex
	bySession map[string]map[string]*string // sessionID -> path -> content at naming
}

func newNamedChanges() *namedChanges {
	return &namedChanges{bySession: map[string]map[string]*string{}}
}

// isNamed reports whether a file was named and its content has not
// changed since it was named. The entire check is under the lock so a
// concurrent name() in another session cannot overwrite the stored
// content between the pointer read and the file read.
func (n *namedChanges) isNamed(sessionID, path string) bool {
	n.mu.Lock()
	defer n.mu.Unlock()
	stored, ok := n.bySession[sessionID][path]
	if !ok {
		return false
	}
	// A nil stored content means the file was deleted at naming; it is
	// still named until it is re-read.
	if stored == nil {
		return true
	}
	// The content has changed since it was named, so it is not "still
	// named": the new change has not been told.
	data, err := os.ReadFile(path)
	if err == nil && string(data) != *stored {
		return false
	}
	return true
}

// name marks a file as named, recording its content so a second change
// without a re-read is named again.
func (n *namedChanges) name(sessionID, path string) {
	n.mu.Lock()
	defer n.mu.Unlock()
	set := n.bySession[sessionID]
	if set == nil {
		set = map[string]*string{}
		n.bySession[sessionID] = set
	}
	data, err := os.ReadFile(path)
	if err != nil {
		set[path] = nil
		return
	}
	s := string(data)
	set[path] = &s
}

func (n *namedChanges) clear(sessionID, path string) {
	n.mu.Lock()
	defer n.mu.Unlock()
	set := n.bySession[sessionID]
	if set != nil {
		delete(set, path)
	}
}

// fileStamp is what a file's modification time and size were, to tell
// cheaply whether a call touched it.
type fileStamp struct {
	exists  bool
	modTime time.Time
	size    int64
}

func (s fileStamp) same(o fileStamp) bool {
	return s.exists == o.exists && s.size == o.size && s.modTime.Equal(o.modTime)
}

func stampOf(path string) fileStamp {
	info, err := os.Stat(path)
	if err != nil {
		return fileStamp{}
	}
	return fileStamp{exists: true, modTime: info.ModTime(), size: info.Size()}
}

// staleFiles lists the files the session read that are out of date:
// their content is no longer what it was at the last read.
func (t *changedReadFiles) staleFiles(ctx context.Context, sessionID string) []string {
	read, err := t.files.ListReadFiles(ctx, sessionID)
	if err != nil {
		return nil
	}
	var stale []string
	for _, path := range read {
		if !t.files.UnchangedSinceRead(ctx, sessionID, path) {
			stale = append(stale, path)
		}
	}
	return stale
}

func (t *changedReadFiles) Run(ctx context.Context, call fantasy.ToolCall) (fantasy.ToolResponse, error) {
	sessionID := tools.GetSessionFromContext(ctx)
	toolName := call.Name
	// The stamps of the session's read files before the call, to tell
	// cheaply whether the call touched them, when it is bash.
	var before map[string]fileStamp
	if toolName == tools.BashToolName && sessionID != "" && t.files != nil {
		if read, err := t.files.ListReadFiles(ctx, sessionID); err == nil {
			before = make(map[string]fileStamp, len(read))
			for _, path := range read {
				before[path] = stampOf(path)
			}
		}
	}
	// The tool's own result first, so the notes come after it: an
	// earlier message is never changed, and a model server's prompt cache
	// still matches the conversation.
	resp, err := t.AgentTool.Run(ctx, call)
	if sessionID == "" || t.files == nil {
		return resp, err
	}

	// What the call itself changed, when it is bash: its own note for the
	// files it touched, as before.
	var commandChanged []string
	if toolName == tools.BashToolName && err == nil && len(before) > 0 {
		for path, stamp := range before {
			if stampOf(path).same(stamp) || t.files.UnchangedSinceRead(ctx, sessionID, path) {
				continue
			}
			commandChanged = append(commandChanged, path)
		}
	}

	// The file this call writes, as the tool resolves it, so the general
	// note does not name it: the call's own write is not an external
	// change, and the tool records a read of what it wrote.
	var writtenFile string
	if toolName == tools.EditToolName || toolName == tools.MultiEditToolName || toolName == tools.WriteToolName {
		var input struct {
			FilePath string `json:"file_path"`
		}
		if jsonErr := json.Unmarshal([]byte(call.Input), &input); jsonErr == nil && input.FilePath != "" {
			writtenFile = normalizePath(t.workingDir, input.FilePath)
		}
	}

	// The file this call views, as the tool resolves it, so the general
	// note does not name it in the view's own result: the view just
	// re-read the file, so telling the model "view it again" in the very
	// result of the view is noise.
	var viewedFile string
	if toolName == tools.ViewToolName {
		var input struct {
			FilePath string `json:"file_path"`
		}
		if jsonErr := json.Unmarshal([]byte(call.Input), &input); jsonErr == nil && input.FilePath != "" {
			viewedFile = normalizePath(t.workingDir, input.FilePath)
		}
	}

	// A file the session has read that changed by anything other than the
	// agent's own edit, multiedit or write, as the user's editor, a
	// formatter or a background job: told at the end of the result of the
	// next tool call after it changed, whatever the tool. Each change is
	// told once, and reading the file again means it is not told. The
	// stale list is computed regardless of the call's success, so a file
	// is marked as named even when the call errors; the note is only
	// appended when the call succeeds.
	var stale []string
	{
		stale = t.staleFiles(ctx, sessionID)
		commandSet := make(map[string]bool, len(commandChanged))
		for _, p := range commandChanged {
			commandSet[p] = true
		}
		var fresh []string
		for _, path := range stale {
			// A file the call itself wrote is not named: the write is
			// not an external change.
			if normalizePath(t.workingDir, path) == writtenFile {
				continue
			}
			// A file the call itself viewed is not named in the view's
			// own result: the view just re-read the file.
			if normalizePath(t.workingDir, path) == viewedFile {
				continue
			}
			// A file bash's own note names is not named a second time by
			// the general one.
			if commandSet[path] {
				continue
			}
			if t.named.isNamed(sessionID, normalizePath(t.workingDir, path)) {
				continue
			}
			fresh = append(fresh, path)
		}
		stale = fresh
	}

	// Reading the file again means it is not told: the read records the
	// new content as read.
	if toolName == tools.ViewToolName && err == nil && viewedFile != "" {
		t.named.clear(sessionID, viewedFile)
	}

	// The notes, at the end of the tool's result: an error with no result
	// is not a result the note can go at the end of. Files are only
	// marked as named when the note is actually appended, so a file is
	// not marked told if the model never sees the note.
	if err == nil {
		// Mark what the notes name, so a file is not named again until it
		// changes again or the agent reads it again.
		for _, path := range stale {
			t.named.name(sessionID, normalizePath(t.workingDir, path))
		}
		for _, path := range commandChanged {
			t.named.name(sessionID, normalizePath(t.workingDir, path))
		}

		var notes []string
		if len(stale) > 0 {
			notes = append(notes, changedFilesNote(t.workingDir, stale))
		}
		if len(commandChanged) > 0 {
			notes = append(notes, bashChangedNote(t.workingDir, commandChanged))
		}
		for _, note := range notes {
			if resp.Content != "" {
				resp.Content += "\n\n" + note
			} else {
				resp.Content = note
			}
		}
	}
	return resp, err
}

// bashChangedNote is the note for the files a bash call changed, kept
// separate from the general one: a file it named is not named a second
// time by the general note.
func bashChangedNote(workingDir string, changed []string) string {
	slices.Sort(changed)
	return "This command changed files you read before: " + strings.Join(relPaths(workingDir, changed), ", ") +
		". View them again before you edit them."
}

// changedFilesNote is the note for files that changed since the session
// read them.
func changedFilesNote(workingDir string, stale []string) string {
	slices.Sort(stale)
	return "These files changed since you read them: " + strings.Join(relPaths(workingDir, stale), ", ") +
		". View them again before you edit them."
}

// relPaths gives each path relative to the working directory, where it
// is inside it.
func relPaths(workingDir string, paths []string) []string {
	out := make([]string, len(paths))
	for i, path := range paths {
		if rel, err := filepath.Rel(workingDir, path); err == nil && !strings.HasPrefix(rel, "..") {
			out[i] = rel
		} else {
			out[i] = path
		}
	}
	return out
}

// resolvePath resolves a path a tool call carries as the tool resolves
// it.
func resolvePath(workingDir, path string) string {
	if filepath.IsAbs(path) {
		return filepath.Clean(path)
	}
	return filepath.Join(workingDir, path)
}

// normalizePath returns a cleaned absolute path, matching the format
// ListReadFiles produces (filepath.Join(os.Getwd(), relpath)). This
// ensures the named map keys match regardless of how the path was
// originally resolved.
func normalizePath(workingDir, path string) string {
	return filepath.Clean(resolvePath(workingDir, path))
}
