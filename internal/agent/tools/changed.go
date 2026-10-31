package tools

import (
	"encoding/json"
	"strings"

	"github.com/charmbracelet/crush/internal/fsext"
)

// FileBefore is what a file held before a file tool's call changed it.
type FileBefore struct {
	// Path is the file as the call named it: the tool's own argument,
	// relative to the working directory when the model wrote it so.
	Path    string
	Content string
	// Existed is false for a file the call made.
	Existed bool
	// Known is false when the change did not record what the file held:
	// a write from before gnosh recorded what it wrote over.
	Known bool
}

// ChangedFiles is what the files a file tool's call changed held before
// it, from what its result recorded: the call's tool name, its input and
// its result's metadata; none for other tools. /rewind restores the code
// from it, and the pop of a frame in mem names the files changed inside
// the frame (FRAMES.md).
func ChangedFiles(toolName, input, metadata string) []FileBefore {
	var params struct {
		FilePath string `json:"file_path"`
	}
	var meta struct {
		FilePath   string        `json:"file_path"`
		OldContent string        `json:"old_content"`
		NewContent string        `json:"new_content"`
		CRLF       *bool         `json:"crlf"`
		Created    *bool         `json:"created"`
		Files      []RenamedFile `json:"files"`
	}
	_ = json.Unmarshal([]byte(input), &params)
	_ = json.Unmarshal([]byte(metadata), &meta)
	switch toolName {
	case EditToolName, MultiEditToolName:
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
		return []FileBefore{{Path: params.FilePath, Content: content, Existed: meta.OldContent != "", Known: true}}
	case WriteToolName:
		known := meta.Created != nil
		return []FileBefore{{Path: params.FilePath, Content: meta.OldContent, Existed: known && !*meta.Created, Known: known}}
	case ReplaceSymbolToolName:
		return []FileBefore{{Path: meta.FilePath, Content: meta.OldContent, Existed: true, Known: true}}
	case RenameToolName:
		files := make([]FileBefore, len(meta.Files))
		for i, f := range meta.Files {
			files[i] = FileBefore{Path: f.Path, Content: f.OldContent, Existed: true, Known: true}
		}
		return files
	}
	return nil
}
