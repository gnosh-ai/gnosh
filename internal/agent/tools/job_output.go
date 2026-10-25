package tools

import (
	"context"
	_ "embed"
	"fmt"
	"strings"
	"time"

	"charm.land/fantasy"
	"github.com/charmbracelet/crush/internal/shell"
)

const (
	JobOutputToolName = "job_output"
)

//go:embed job_output.md
var jobOutputDescription string

type JobOutputParams struct {
	ShellID string `json:"shell_id" description:"The ID of the background shell to retrieve output from"`
	Wait    bool   `json:"wait" description:"If true, block until the background shell completes before returning output"`
}

type JobOutputResponseMetadata struct {
	ShellID          string `json:"shell_id"`
	Command          string `json:"command"`
	Description      string `json:"description"`
	Done             bool   `json:"done"`
	WorkingDirectory string `json:"working_directory"`
}

// maxJobOutputWait bounds how long job_output blocks when wait is true, so a
// background job that never finishes cannot wedge the turn forever. On reaching
// it the tool returns the output gathered so far with a "running" status, and
// the model may choose to wait again. It is a var so tests can shorten it.
var maxJobOutputWait = 5 * time.Minute

// jobNotFound answers job_output or job_kill for an ID that names no
// background job. A model that assumes a command it ran in the foreground
// became a job, or guesses an ID, is told how jobs come about and which
// jobs started in dir there are, as Claude Code's TaskStop names the
// running tasks, rather than left to guess again from a bare "not found".
func jobNotFound(id, dir string) fantasy.ToolResponse {
	var jobs []string
	for _, job := range shell.GetBackgroundShellManager().Jobs(dir) {
		state := "running"
		if job.Done {
			state = fmt.Sprintf("exited %d", job.ExitCode)
		}
		line, _, cut := strings.Cut(job.Command, "\n")
		command := []rune(line)
		if cut || len(command) > 60 {
			command = append([]rune(strings.TrimSpace(string(command[:min(len(command), 60)]))), '…')
		}
		jobs = append(jobs, fmt.Sprintf("%s (%s: %s)", job.ID, state, string(command)))
	}
	there := "There are no background jobs now."
	if len(jobs) > 0 {
		there = "The background jobs are " + strings.Join(jobs, ", ") + "."
	}
	return fantasy.NewTextErrorResponse(fmt.Sprintf("background shell not found: %s. %s "+
		"Only a bash command still running when bash returns becomes a background job, "+
		"with its ID (such as 001) in that bash result; a command that finished in time "+
		"already returned its output there.", id, there))
}

// NewJobOutputTool reads background jobs; one it cannot find, it answers
// with those started in workingDir.
func NewJobOutputTool(workingDir, spillDir string) fantasy.AgentTool {
	return fantasy.NewAgentTool(
		JobOutputToolName,
		jobOutputDescription,
		func(ctx context.Context, params JobOutputParams, call fantasy.ToolCall) (fantasy.ToolResponse, error) {
			if params.ShellID == "" {
				return fantasy.NewTextErrorResponse("missing shell_id"), nil
			}

			bgManager := shell.GetBackgroundShellManager()
			bgShell, ok := bgManager.Get(params.ShellID)
			if !ok {
				return jobNotFound(params.ShellID, workingDir), nil
			}

			if params.Wait {
				waitCtx, cancel := context.WithTimeout(ctx, maxJobOutputWait)
				bgShell.WaitContext(waitCtx)
				cancel()
			}

			stdout, stderr, done, err := bgShell.GetOutput()

			var outputParts []string
			if stdout != "" {
				outputParts = append(outputParts, stdout)
			}
			if stderr != "" {
				outputParts = append(outputParts, stderr)
			}

			status := "running"
			if done {
				status = "completed"
				if err != nil {
					exitCode := shell.ExitCode(err)
					if exitCode != 0 {
						outputParts = append(outputParts, fmt.Sprintf("Exit code %d", exitCode))
					}
				}
			}

			output := strings.Join(outputParts, "\n")
			output = TruncateOutput(output, spillDir)

			metadata := JobOutputResponseMetadata{
				ShellID:          params.ShellID,
				Command:          bgShell.Command,
				Description:      bgShell.Description,
				Done:             done,
				WorkingDirectory: bgShell.WorkingDir,
			}

			if output == "" {
				output = BashNoOutput
			}

			result := fmt.Sprintf("Status: %s\n\n%s", status, output)
			return fantasy.WithResponseMetadata(fantasy.NewTextResponse(result), metadata), nil
		},
	)
}
