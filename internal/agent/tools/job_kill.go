package tools

import (
	"context"
	_ "embed"
	"fmt"

	"charm.land/fantasy"
	"github.com/charmbracelet/crush/internal/shell"
)

const (
	JobKillToolName = "job_kill"
)

//go:embed job_kill.md
var jobKillDescription string

type JobKillParams struct {
	ShellID string `json:"shell_id" description:"The ID of the background shell to terminate"`
}

type JobKillResponseMetadata struct {
	ShellID     string `json:"shell_id"`
	Command     string `json:"command"`
	Description string `json:"description"`
}

// NewJobKillTool stops background jobs; one it cannot find, it answers
// with those started in workingDir.
func NewJobKillTool(workingDir string) fantasy.AgentTool {
	return fantasy.NewAgentTool(
		JobKillToolName,
		jobKillDescription,
		func(ctx context.Context, params JobKillParams, call fantasy.ToolCall) (fantasy.ToolResponse, error) {
			if params.ShellID == "" {
				return fantasy.NewTextErrorResponse("missing shell_id"), nil
			}

			bgManager := shell.GetBackgroundShellManager()

			bgShell, ok := bgManager.Get(params.ShellID)
			if !ok {
				return jobNotFound(params.ShellID, workingDir), nil
			}

			metadata := JobKillResponseMetadata{
				ShellID:     params.ShellID,
				Command:     bgShell.Command,
				Description: bgShell.Description,
			}

			// A job that had ended is only removed; saying it was
			// terminated would tell the model it stopped something.
			_, _, done, exitErr := bgShell.GetOutput()
			err := bgManager.Kill(params.ShellID)
			if err != nil {
				return fantasy.NewTextErrorResponse(err.Error()), nil
			}

			result := fmt.Sprintf("Background shell %s terminated successfully", params.ShellID)
			if done {
				result = fmt.Sprintf("Background shell %s had already exited with code %d; it is removed.", params.ShellID, shell.ExitCode(exitErr))
			}
			return fantasy.WithResponseMetadata(fantasy.NewTextResponse(result), metadata), nil
		},
	)
}
