package tools

import (
	"context"
	_ "embed"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"

	"charm.land/fantasy"
	"github.com/charmbracelet/crush/internal/diff"
	"github.com/charmbracelet/crush/internal/filepathext"
	"github.com/charmbracelet/crush/internal/filetracker"
	"github.com/charmbracelet/crush/internal/fsext"
	"github.com/charmbracelet/crush/internal/history"

	"github.com/charmbracelet/crush/internal/lsp"
	"github.com/charmbracelet/crush/internal/permission"
)

//go:embed write.md
var writeDescription string

type WriteParams struct {
	FilePath string `json:"file_path" description:"The path to the file to write"`
	Content  string `json:"content" description:"The content to write to the file"`
}

type WritePermissionsParams struct {
	FilePath   string `json:"file_path"`
	OldContent string `json:"old_content,omitempty"`
	NewContent string `json:"new_content,omitempty"`
}

type WriteResponseMetadata struct {
	Diff      string `json:"diff"`
	Additions int    `json:"additions"`
	Removals  int    `json:"removals"`
	// OldContent is what the file held before the write, and Created
	// whether the write made it: what /rewind puts back.
	OldContent string `json:"old_content,omitempty"`
	Created    bool   `json:"created"`
}

const WriteToolName = "write"

func NewWriteTool(
	lspManager *lsp.Manager,
	permissions permission.Service,
	files history.Service,
	filetracker filetracker.Service,
	subdirectoryContext SubdirectoryContext,
	workingDir string,
) fantasy.AgentTool {
	return fantasy.NewAgentTool(
		WriteToolName,
		writeDescription,
		func(ctx context.Context, params WriteParams, call fantasy.ToolCall) (fantasy.ToolResponse, error) {
			if params.FilePath == "" {
				return fantasy.NewTextErrorResponse("file_path is required"), nil
			}

			sessionID := GetSessionFromContext(ctx)
			if sessionID == "" {
				return fantasy.ToolResponse{}, fmt.Errorf("session_id is required")
			}

			filePath := filepathext.SmartJoin(workingDir, params.FilePath)

			fileInfo, err := os.Stat(filePath)
			if err == nil {
				if fileInfo.IsDir() {
					return fantasy.NewTextErrorResponse(fmt.Sprintf("Path is a directory, not a file: %s", filePath)), nil
				}

				// A file never read in this session used to be refused as
				// "modified since it was last read", last read in year 1:
				// it had been made by a command the agent ran, and the
				// message sent it looking for what else had changed it.
				modTime := fileInfo.ModTime().Truncate(time.Second)
				lastRead := filetracker.LastReadTime(ctx, sessionID, filePath)
				if lastRead.IsZero() {
					return fantasy.NewTextErrorResponse(fmt.Sprintf(
						"file %s already exists and has not been read in this session. "+
							"Read it with the view tool before writing over it, or use edit to change part of it.",
						filePath,
					)), nil
				}
				// Written back as it was since the read (git stash and pop),
				// the file is not stale; see loadExistingFile.
				if modTime.After(lastRead) && !filetracker.UnchangedSinceRead(ctx, sessionID, filePath) {
					return fantasy.NewTextErrorResponse(fmt.Sprintf(
						"file %s has been modified since it was last read (mod time: %s, last read: %s), "+
							"perhaps by a command you ran. Read it again with the view tool before writing over it, "+
							"so that what changed is not lost.",
						filePath, modTime.Format(time.RFC3339), lastRead.Format(time.RFC3339),
					)), nil
				}

				oldContent, readErr := os.ReadFile(filePath)
				if readErr == nil && string(oldContent) == params.Content {
					return fantasy.NewTextErrorResponse(fmt.Sprintf("File %s already contains the exact content. No changes made.", filePath)), nil
				}
			} else if !os.IsNotExist(err) {
				return fantasy.ToolResponse{}, fmt.Errorf("error checking file: %w", err)
			}

			dir := filepath.Dir(filePath)
			if err = os.MkdirAll(dir, 0o755); err != nil {
				return fantasy.ToolResponse{}, fmt.Errorf("error creating directory: %w", err)
			}

			oldContent := ""
			if fileInfo != nil && !fileInfo.IsDir() {
				oldBytes, readErr := os.ReadFile(filePath)
				if readErr == nil {
					oldContent = string(oldBytes)
				}
			}

			diff, additions, removals := diff.GenerateDiff(
				oldContent,
				params.Content,
				strings.TrimPrefix(filePath, workingDir),
			)

			p, err := permissions.Request(
				ctx,
				permission.CreatePermissionRequest{
					SessionID:   sessionID,
					Path:        fsext.PathOrPrefix(filePath, workingDir),
					FilePath:    filePath,
					ToolCallID:  call.ID,
					ToolName:    WriteToolName,
					Action:      "write",
					Description: fmt.Sprintf("Create file %s", filePath),
					Params: WritePermissionsParams{
						FilePath:   filePath,
						OldContent: oldContent,
						NewContent: params.Content,
					},
				},
			)
			if err != nil {
				return fantasy.ToolResponse{}, err
			}
			if !p {
				resp := NewPermissionDeniedResponse()
				resp = fantasy.WithResponseMetadata(resp, WriteResponseMetadata{
					Diff:      diff,
					Additions: additions,
					Removals:  removals,
				})
				return resp, nil
			}

			err = os.WriteFile(filePath, []byte(params.Content), 0o644)
			if err != nil {
				return fantasy.ToolResponse{}, fmt.Errorf("error writing file: %w", err)
			}

			// Check if file exists in history
			file, err := files.GetByPathAndSession(ctx, filePath, sessionID)
			if err != nil {
				_, err = files.Create(ctx, sessionID, filePath, oldContent)
				if err != nil {
					// Log error but don't fail the operation
					return fantasy.ToolResponse{}, fmt.Errorf("error creating file history: %w", err)
				}
			}
			if file.Content != oldContent {
				// User manually changed the content; store an intermediate version
				_, err = files.CreateVersion(ctx, sessionID, filePath, oldContent)
				if err != nil {
					slog.Error("Error creating file history version", "error", err)
				}
			}
			// Store the new version
			_, err = files.CreateVersion(ctx, sessionID, filePath, params.Content)
			if err != nil {
				slog.Error("Error creating file history version", "error", err)
			}

			filetracker.RecordRead(ctx, sessionID, filePath)

			notifyLSPs(ctx, lspManager, params.FilePath)

			result := fmt.Sprintf("File successfully written: %s", filePath)
			result = fmt.Sprintf("<result>\n%s\n</result>", result)
			result += getDiagnostics(filePath, lspManager)
			if subdirectoryContext != nil {
				result += subdirectoryContext(ctx, sessionID, filePath)
			}
			return fantasy.WithResponseMetadata(
				fantasy.NewTextResponse(result),
				WriteResponseMetadata{
					Diff:       diff,
					Additions:  additions,
					Removals:   removals,
					OldContent: oldContent,
					Created:    fileInfo == nil,
				},
			), nil
		},
	)
}
