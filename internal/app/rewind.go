package app

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/charmbracelet/crush/internal/agent/tools"
	"github.com/charmbracelet/crush/internal/diff"
	"github.com/charmbracelet/crush/internal/filepathext"
	"github.com/charmbracelet/crush/internal/fsext"
	"github.com/charmbracelet/crush/internal/message"
	"github.com/charmbracelet/crush/internal/session"
)

// RewindOptions say what Rewind restores to the point before a prompt:
// the conversation, the code, or both. With DryRun it restores nothing,
// and says what restoring the code would change.
type RewindOptions struct {
	Conversation, Code, DryRun bool
}

// RewindResult is what a rewind restored of the code, or with DryRun
// would restore, and where the whole conversation was kept.
type RewindResult struct {
	// Files are the files restored: put back as they were, or removed
	// where the agent made them.
	Files []string
	// Additions and Removals count the lines restoring them adds and
	// removes.
	Additions, Removals int
	// Skipped are the files the agent changed that are left as they are,
	// each with why.
	Skipped []string
	// Kept is the session the whole conversation is kept in, when the
	// conversation was rewound.
	Kept session.Session
}

// Rewind restores the conversation of session sessionID, its code, or
// both, to the point before its prompt messageID, as Claude Code's
// /rewind does. The prompt must be one the model still reads: after the
// summary of a compacted session.
//
// The code is restored from what the agent's file tools recorded: each
// file they changed after the prompt is put back as it was before the
// first of those changes, or removed where they made it. What commands,
// downloads or the user changed is left as it is.
//
// The conversation is cut back to before the prompt, with the reads of
// files made since, once the whole of it is kept in a session of its own.
// mem, and the todos that show it, stay as they are.
func (a *App) Rewind(ctx context.Context, sessionID, messageID string, opts RewindOptions) (RewindResult, error) {
	if a.AgentCoordinator != nil && a.AgentCoordinator.IsSessionBusy(sessionID) {
		return RewindResult{}, errors.New("the agent is busy in this session")
	}
	if err := a.Messages.FlushAll(ctx); err != nil {
		return RewindResult{}, err
	}
	source, err := a.Sessions.Get(ctx, sessionID)
	if err != nil {
		return RewindResult{}, err
	}
	messages, err := a.Messages.List(ctx, sessionID)
	if err != nil {
		return RewindResult{}, err
	}
	at := slices.IndexFunc(messages, func(msg message.Message) bool { return msg.ID == messageID })
	if at < 0 || messages[at].Role != message.User {
		return RewindResult{}, errors.New("message not found")
	}
	if slices.ContainsFunc(messages[at:], func(msg message.Message) bool { return msg.ID == source.SummaryMessageID }) {
		return RewindResult{}, errors.New("that message is no longer in the active context: choose a more recent one")
	}
	var result RewindResult
	if opts.Code || opts.DryRun {
		result, err = restoreFiles(filesBefore(messages[at:], a.config.WorkingDir()), opts.DryRun)
		if err != nil {
			return result, fmt.Errorf("restoring the code: %w", err)
		}
		if opts.DryRun {
			return result, nil
		}
	}
	if opts.Conversation {
		if result.Kept, err = a.rewindConversation(ctx, source, messages, at); err != nil {
			return result, fmt.Errorf("restoring the conversation: %w", err)
		}
	}
	return result, nil
}

// rewindConversation keeps the whole of source's conversation, messages,
// in a session of its own, then cuts source's back to before the message
// at index at: the messages from there go, and with them the reads of
// files made since and the subdirectories' context given since. The todos
// stay: they show mem, which a rewind leaves as it is. It returns the
// session the whole conversation is kept in.
func (a *App) rewindConversation(ctx context.Context, source session.Session, messages []message.Message, at int) (session.Session, error) {
	titles, err := a.sessionTitles(ctx)
	if err != nil {
		return session.Session{}, err
	}
	kept, err := a.copySession(ctx, source, messages, branchTitle(cmp.Or(source.Title, "Conversation"), "before rewind", titles))
	if err != nil {
		return session.Session{}, err
	}
	removed := make([]string, 0, len(messages)-at)
	for _, msg := range messages[at:] {
		if err := a.Messages.Delete(ctx, msg.ID); err != nil {
			return kept, err
		}
		removed = append(removed, msg.ID)
	}
	if a.AgentCoordinator != nil {
		a.AgentCoordinator.Rewound(source.ID, removed)
	}
	if err := a.FileTracker.ForgetReadsSince(ctx, source.ID, messages[at].CreatedAt); err != nil {
		return kept, err
	}
	current, err := a.Sessions.Get(ctx, source.ID)
	if err != nil {
		return kept, err
	}
	// How full the context is, the next answer says.
	current.PromptTokens, current.CompletionTokens, current.EstimatedUsage = 0, 0, false
	_, err = a.Sessions.Save(ctx, current)
	return kept, err
}

// fileBefore is what a file held before the agent first changed it after
// a prompt.
type fileBefore struct {
	path    string
	content string
	// existed is false for a file the agent made.
	existed bool
	// known is false when the change did not record what the file held:
	// a write from before gnosh recorded what it wrote over.
	known bool
}

// filesBefore says what each file the agent's file tools changed in
// messages held before the first of those changes, in the order of the
// first changes. Relative paths are the tools' own, against workingDir.
func filesBefore(messages []message.Message, workingDir string) []fileBefore {
	calls := make(map[string]message.ToolCall)
	seen := make(map[string]bool)
	var files []fileBefore
	for _, msg := range messages {
		for _, call := range msg.ToolCalls() {
			calls[call.ID] = call
		}
		for _, result := range msg.ToolResults() {
			if result.IsError {
				continue
			}
			for _, f := range changedFiles(calls[result.ToolCallID], result) {
				if f.path == "" {
					continue
				}
				f.path = filepathext.SmartJoin(workingDir, f.path)
				if !seen[f.path] {
					seen[f.path] = true
					files = append(files, f)
				}
			}
		}
	}
	return files
}

// changedFiles is what the files a file tool's call changed held before
// it, from what its result recorded; none for other tools.
func changedFiles(call message.ToolCall, result message.ToolResult) []fileBefore {
	var input struct {
		FilePath string `json:"file_path"`
	}
	var meta struct {
		FilePath   string              `json:"file_path"`
		OldContent string              `json:"old_content"`
		NewContent string              `json:"new_content"`
		CRLF       *bool               `json:"crlf"`
		Created    *bool               `json:"created"`
		Files      []tools.RenamedFile `json:"files"`
	}
	_ = json.Unmarshal([]byte(call.Input), &input)
	_ = json.Unmarshal([]byte(result.Metadata), &meta)
	switch result.Name {
	case tools.EditToolName, tools.MultiEditToolName:
		// An edit that changes a file found what it replaced there: one
		// that found nothing made the file. What it read had its Windows
		// line endings made Unix ones, which an edit recorded before the
		// crlf flag shows in what it wrote.
		content := meta.OldContent
		crlf := strings.Contains(meta.NewContent, "\r\n")
		if meta.CRLF != nil {
			crlf = *meta.CRLF
		}
		if crlf {
			content, _ = fsext.ToWindowsLineEndings(content)
		}
		return []fileBefore{{path: input.FilePath, content: content, existed: meta.OldContent != "", known: true}}
	case tools.WriteToolName:
		known := meta.Created != nil
		return []fileBefore{{path: input.FilePath, content: meta.OldContent, existed: known && !*meta.Created, known: known}}
	case tools.ReplaceSymbolToolName:
		return []fileBefore{{path: meta.FilePath, content: meta.OldContent, existed: true, known: true}}
	case tools.RenameToolName:
		files := make([]fileBefore, len(meta.Files))
		for i, f := range meta.Files {
			files[i] = fileBefore{path: f.Path, content: f.OldContent, existed: true, known: true}
		}
		return files
	}
	return nil
}

// restoreFiles puts each of files back as it was before, keeping its
// mode, or with dryRun only says what that would change. A file that is
// now a symbolic link or not a file, or whose content before is not known,
// is skipped; one already as it was is not counted.
func restoreFiles(files []fileBefore, dryRun bool) (RewindResult, error) {
	var result RewindResult
	for _, f := range files {
		if !f.known {
			result.Skipped = append(result.Skipped, f.path+": what it held before is not recorded")
			continue
		}
		info, err := os.Lstat(f.path)
		exists := err == nil
		switch {
		case err != nil && !errors.Is(err, fs.ErrNotExist):
			return result, err
		case exists && info.Mode()&fs.ModeSymlink != 0:
			result.Skipped = append(result.Skipped, f.path+": a symbolic link")
			continue
		case exists && !info.Mode().IsRegular():
			result.Skipped = append(result.Skipped, f.path+": not a file now")
			continue
		}
		var now []byte
		if exists {
			if now, err = os.ReadFile(f.path); err != nil {
				return result, err
			}
		}
		if exists == f.existed && string(now) == f.content {
			continue
		}
		_, additions, removals := diff.GenerateDiff(string(now), f.content, f.path)
		result.Files = append(result.Files, f.path)
		result.Additions += additions
		result.Removals += removals
		if dryRun {
			continue
		}
		if !f.existed {
			if err := os.Remove(f.path); err != nil && !errors.Is(err, fs.ErrNotExist) {
				return result, err
			}
			continue
		}
		if err := os.MkdirAll(filepath.Dir(f.path), 0o755); err != nil {
			return result, err
		}
		if err := os.WriteFile(f.path, []byte(f.content), 0o644); err != nil {
			return result, err
		}
	}
	return result, nil
}
