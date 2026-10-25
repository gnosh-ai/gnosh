package tools

import (
	"bytes"
	"cmp"
	"context"
	_ "embed"
	"fmt"
	"html/template"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/charmbracelet/x/ansi"

	"charm.land/fantasy"
	"github.com/charmbracelet/crush/internal/config"
	"github.com/charmbracelet/crush/internal/fsext"
	"github.com/charmbracelet/crush/internal/permission"
	"github.com/charmbracelet/crush/internal/shell"
)

type BashParams struct {
	Description         string `json:"description" description:"A brief description of what the command does, try to keep it under 30 characters or so"`
	Command             string `json:"command" description:"The command to execute"`
	WorkingDir          string `json:"working_dir,omitempty" description:"The working directory to execute the command in (defaults to current directory)"`
	RunInBackground     bool   `json:"run_in_background,omitempty" description:"Set to true (boolean) to run this command in the background. Use job_output to read the output later."`
	AutoBackgroundAfter int    `json:"auto_background_after,omitempty" description:"Seconds to wait before automatically moving the command to a background job (default: 60)"`
	Timeout             int    `json:"timeout,omitempty" description:"Milliseconds to wait for the command before it moves to a background job, at most the most the tool description gives; auto_background_after wins if both are set"`
}

// maxBashTimeoutSeconds bounds a bash call's timeout unless
// BASH_MAX_TIMEOUT_MS says otherwise: Claude Code's 600000 ms.
const maxBashTimeoutSeconds = 600

// timeoutSeconds is a timeout in milliseconds, Claude Code's bash
// parameter, as the whole seconds to wait before a command moves to the
// background: rounded up, at most maxWaitSeconds, 0 for none. A model
// used to Claude Code's Bash gives a long test run timeout 600000, and
// Klash dropped it: the run moved to the background after a minute, to be
// read with job_output, a model round trip each time.
func timeoutSeconds(ms int) int {
	if ms <= 0 {
		return 0
	}
	return min((ms+999)/1000, maxWaitSeconds())
}

// bashEnvInt is the positive whole number the environment variable name
// holds, or def: Claude Code's BASH_* limits, which a klash.json's env
// can set too.
func bashEnvInt(name string, def int) int {
	if n, err := strconv.Atoi(strings.TrimSpace(os.Getenv(name))); err == nil && n > 0 {
		return n
	}
	return def
}

// defaultWaitSeconds is how long a command runs before it moves to the
// background when its call gives no timeout: BASH_DEFAULT_TIMEOUT_MS, as
// in Claude Code, rounded up to whole seconds, or
// DefaultAutoBackgroundAfter. A long job (a test suite, a render) that
// finishes within it costs the model no job_output round trips, each of
// which on a local model means a step over the whole conversation.
func defaultWaitSeconds() int {
	return (bashEnvInt("BASH_DEFAULT_TIMEOUT_MS", DefaultAutoBackgroundAfter*1000) + 999) / 1000
}

// maxWaitSeconds is the longest a call's timeout can ask for:
// BASH_MAX_TIMEOUT_MS, as in Claude Code (maxBashTimeoutSeconds by
// default), or the default wait when that is longer.
func maxWaitSeconds() int {
	return max((bashEnvInt("BASH_MAX_TIMEOUT_MS", maxBashTimeoutSeconds*1000)+999)/1000, defaultWaitSeconds())
}

// maxOutputLength is how many characters of a command's output the model
// reads before the middle is cut: BASH_MAX_OUTPUT_LENGTH, as in Claude
// Code, at most 150000, or MaxOutputLength.
func maxOutputLength() int {
	return min(bashEnvInt("BASH_MAX_OUTPUT_LENGTH", MaxOutputLength), 150_000)
}

type BashPermissionsParams struct {
	Description         string `json:"description"`
	Command             string `json:"command"`
	WorkingDir          string `json:"working_dir"`
	RunInBackground     bool   `json:"run_in_background"`
	AutoBackgroundAfter int    `json:"auto_background_after"`
	Timeout             int    `json:"timeout"`
}

type BashResponseMetadata struct {
	StartTime        int64  `json:"start_time"`
	EndTime          int64  `json:"end_time"`
	Output           string `json:"output"`
	Description      string `json:"description"`
	WorkingDirectory string `json:"working_directory"`
	Background       bool   `json:"background,omitempty"`
	ShellID          string `json:"shell_id,omitempty"`
	// ExitCode is the command's exit status once it has finished; the
	// output text says it too ("Exit code N"), but readers such as the
	// --verbose trace should not have to parse that.
	ExitCode int `json:"exit_code,omitempty"`
}

const (
	BashToolName = "bash"

	DefaultAutoBackgroundAfter = 60 // Commands taking longer automatically become background jobs
	MaxOutputLength            = 30000
	BashNoOutput               = "no output"
)

//go:embed bash.md.tpl
var bashDescriptionTmpl []byte

var bashDescriptionTpl = template.Must(
	template.New("bashDescription").
		Parse(string(bashDescriptionTmpl)),
)

type bashDescriptionData struct {
	BannedCommands  string
	DenyRules       string
	MaxOutputLength int
	DefaultWait     int
	MaxTimeoutMs    int
	Attribution     config.Attribution
	ModelID         string
	RgAvailable     bool
	GhAvailable     bool
	// Notified is set when the agent is told a background command's end,
	// and so may not start a command with a long sleep (BlockedSleep).
	Notified     bool
	BlockedSleep int
}

var bannedCommands = []string{
	// Network/Download tools
	"alias",
	"aria2c",
	"axel",
	"chrome",
	"curl",
	"curlie",
	"firefox",
	"http-prompt",
	"httpie",
	"links",
	"lynx",
	"nc",
	"safari",
	"scp",
	"ssh",
	"telnet",
	"w3m",
	"wget",
	"xh",

	// System administration
	"doas",
	"su",
	"sudo",

	// Package managers
	"apk",
	"apt",
	"apt-cache",
	"apt-get",
	"dnf",
	"dpkg",
	"emerge",
	"home-manager",
	"makepkg",
	"opkg",
	"pacman",
	"paru",
	"pkg",
	"pkg_add",
	"pkg_delete",
	"portage",
	"rpm",
	"yay",
	"yum",
	"zypper",

	// System modification
	"at",
	"batch",
	"chkconfig",
	"crontab",
	"fdisk",
	"mkfs",
	"mount",
	"parted",
	"service",
	"systemctl",
	"umount",

	// Network configuration
	"firewall-cmd",
	"ifconfig",
	"ip",
	"iptables",
	"netstat",
	"pfctl",
	"route",
	"ufw",
}

func bashDescription(attribution *config.Attribution, modelID string, denyRules []string, notified bool) string {
	bannedCommandsStr := strings.Join(bannedCommands, ", ")
	var out bytes.Buffer
	if err := bashDescriptionTpl.Execute(&out, bashDescriptionData{
		BannedCommands:  bannedCommandsStr,
		DenyRules:       strings.Join(denyRules, ", "),
		MaxOutputLength: maxOutputLength(),
		DefaultWait:     defaultWaitSeconds(),
		MaxTimeoutMs:    maxWaitSeconds() * 1000,
		Attribution:     *attribution,
		ModelID:         modelID,
		RgAvailable:     getRg() != "",
		GhAvailable:     ghAvailable,
		Notified:        notified,
		BlockedSleep:    blockedSleep,
	}); err != nil {
		// this should never happen.
		panic("failed to execute bash description template: " + err.Error())
	}
	return out.String()
}

// blockFuncs are the commands the shell refuses to run: the banned ones,
// and those the Bash(pattern) deny rules of the permissions match.
func blockFuncs(denyRules []string) []shell.BlockFunc {
	funcs := []shell.BlockFunc{
		shell.CommandsBlocker(bannedCommands),

		// System package managers
		shell.ArgumentsBlocker("apk", []string{"add"}, nil),
		shell.ArgumentsBlocker("apt", []string{"install"}, nil),
		shell.ArgumentsBlocker("apt-get", []string{"install"}, nil),
		shell.ArgumentsBlocker("dnf", []string{"install"}, nil),
		shell.ArgumentsBlocker("pacman", nil, []string{"-S"}),
		shell.ArgumentsBlocker("pkg", []string{"install"}, nil),
		shell.ArgumentsBlocker("yum", []string{"install"}, nil),
		shell.ArgumentsBlocker("zypper", []string{"install"}, nil),

		// Language-specific package managers
		shell.ArgumentsBlocker("brew", []string{"install"}, nil),
		shell.ArgumentsBlocker("cargo", []string{"install"}, nil),
		shell.ArgumentsBlocker("gem", []string{"install"}, nil),
		shell.ArgumentsBlocker("go", []string{"install"}, nil),
		shell.ArgumentsBlocker("npm", []string{"install"}, []string{"--global"}),
		shell.ArgumentsBlocker("npm", []string{"install"}, []string{"-g"}),
		shell.ArgumentsBlocker("pip", []string{"install"}, []string{"--user"}),
		shell.ArgumentsBlocker("pip3", []string{"install"}, []string{"--user"}),
		shell.ArgumentsBlocker("pnpm", []string{"add"}, []string{"--global"}),
		shell.ArgumentsBlocker("pnpm", []string{"add"}, []string{"-g"}),
		shell.ArgumentsBlocker("yarn", []string{"global", "add"}, nil),

		// `go test -exec` can run arbitrary commands
		shell.ArgumentsBlocker("go", []string{"test"}, []string{"-exec"}),

		// A gno.land key's password belongs in the gnokey tool's masked
		// prompt, never on a command line the model wrote (see
		// gnokey.go). gnokey reads it from standard input only with this
		// flag, so refusing the flag here closes the shell route.
		shell.ArgumentsBlocker("gnokey", nil, []string{"-insecure-password-stdin"}),
		shell.ArgumentsBlocker("gnokey", nil, []string{"--insecure-password-stdin"}),
	}
	for _, pattern := range denyRules {
		funcs = append(funcs, shell.CommandRuleBlocker(pattern))
	}
	return funcs
}

// NewBashTool returns the bash tool. denyRules are the patterns of the
// Bash(pattern) deny rules of the permissions: the shell refuses every
// command one matches, wherever in a command line it is. allowRules are
// the patterns of the Bash(pattern) allow rules: a command line runs
// without asking when each command in it matches one or only reads.
// BackgroundWatcher is told of each command that becomes a background
// job, with the context of the bash call that started it, so the agent
// can be told when the job ends.
type BackgroundWatcher func(ctx context.Context, job *shell.BackgroundShell)

// NewBashTool runs shell commands. onBackground, when not nil, is told of
// each command that becomes a background job, and the result then says
// the agent will be told when it ends.
func NewBashTool(permissions permission.Service, workingDir, spillDir string, attribution *config.Attribution, modelID string, maxBackgroundJobs int, denyRules, allowRules []string, onBackground BackgroundWatcher) fantasy.AgentTool {
	allowed := commandRules(allowRules)
	return fantasy.NewAgentTool(
		BashToolName,
		string(bashDescription(attribution, modelID, denyRules, onBackground != nil)),
		func(ctx context.Context, params BashParams, call fantasy.ToolCall) (fantasy.ToolResponse, error) {
			if params.Command == "" {
				return fantasy.NewTextErrorResponse("missing command"), nil
			}
			// Where the agent is told when a background command ends, a
			// long sleep in the foreground only holds it up, as Claude
			// Code has it.
			if onBackground != nil && !params.RunInBackground {
				if sleep := foregroundSleep(params.Command); sleep != "" {
					return fantasy.NewTextErrorResponse(fmt.Sprintf(sleepBlocked, sleep)), nil
				}
			}

			// Determine working directory
			execWorkingDir := cmp.Or(params.WorkingDir, workingDir)

			dirs := workDirs{start: execWorkingDir, project: workingDir}
			isSafeReadOnly := readOnlyCommand(params.Command, dirs) || allowedCommand(params.Command, dirs, allowed)

			sessionID := GetSessionFromContext(ctx)
			if sessionID == "" {
				return fantasy.ToolResponse{}, fmt.Errorf("session ID is required for executing shell command")
			}
			if !isSafeReadOnly {
				p, err := permissions.Request(
					ctx,
					permission.CreatePermissionRequest{
						SessionID:   sessionID,
						Path:        execWorkingDir,
						ToolCallID:  call.ID,
						ToolName:    BashToolName,
						Action:      "execute",
						Description: fmt.Sprintf("Execute command: %s", params.Command),
						Params:      BashPermissionsParams(params),
					},
				)
				if err != nil {
					return fantasy.ToolResponse{}, err
				}
				if !p {
					return NewPermissionDeniedResponse(), nil
				}
			}

			// If explicitly requested as background, start immediately with detached context
			if params.RunInBackground {
				startTime := time.Now()
				bgManager := shell.GetBackgroundShellManager()
				// Use background context so it continues after tool returns
				bgShell, err := bgManager.StartAfter(context.Background(), execWorkingDir, blockFuncs(denyRules), sessionEnvSetup(ctx), params.Command, params.Description, maxBackgroundJobs)
				if err != nil {
					return fantasy.ToolResponse{}, fmt.Errorf("error starting background shell: %w", err)
				}

				// Wait a short time to detect fast failures (blocked commands, syntax errors, etc.)
				time.Sleep(1 * time.Second)
				stdout, stderr, done, execErr := bgShell.GetOutput()

				if done {
					// Command failed or completed very quickly
					bgManager.Remove(bgShell.ID)

					interrupted := shell.IsInterrupt(execErr)
					exitCode := shell.ExitCode(execErr)
					if exitCode == 0 && !interrupted && execErr != nil {
						return fantasy.ToolResponse{}, fmt.Errorf("[Job %s] error executing command: %w", bgShell.ID, execErr)
					}

					stdout = formatOutput(stdout, stderr, execErr, spillDir)

					metadata := BashResponseMetadata{
						StartTime:        startTime.UnixMilli(),
						EndTime:          time.Now().UnixMilli(),
						Output:           stdout,
						Description:      params.Description,
						Background:       params.RunInBackground,
						WorkingDirectory: bgShell.WorkingDir,
						ExitCode:         exitCode,
					}
					if stdout == "" {
						return fantasy.WithResponseMetadata(fantasy.NewTextResponse(BashNoOutput), metadata), nil
					}
					stdout += fmt.Sprintf("\n\n<cwd>%s</cwd>", normalizeWorkingDir(bgShell.WorkingDir))
					return fantasy.WithResponseMetadata(fantasy.NewTextResponse(stdout), metadata), nil
				}

				// Still running after fast-failure check - return as background job
				metadata := BashResponseMetadata{
					StartTime:        startTime.UnixMilli(),
					EndTime:          time.Now().UnixMilli(),
					Description:      params.Description,
					WorkingDirectory: bgShell.WorkingDir,
					Background:       true,
					ShellID:          bgShell.ID,
				}
				response := fmt.Sprintf("Background shell started with ID: %s\n\n%s", bgShell.ID, backgrounded(ctx, bgShell, onBackground))
				return fantasy.WithResponseMetadata(fantasy.NewTextResponse(response), metadata), nil
			}

			// Start synchronous execution with auto-background support
			startTime := time.Now()

			// Start with detached context so it can survive if moved to background
			bgManager := shell.GetBackgroundShellManager()
			bgShell, err := bgManager.StartAfter(context.Background(), execWorkingDir, blockFuncs(denyRules), sessionEnvSetup(ctx), params.Command, params.Description, maxBackgroundJobs)
			if err != nil {
				return fantasy.ToolResponse{}, fmt.Errorf("error starting shell: %w", err)
			}

			// Wait for either completion, auto-background threshold, or context cancellation
			ticker := time.NewTicker(100 * time.Millisecond)
			defer ticker.Stop()

			autoBackgroundAfter := cmp.Or(params.AutoBackgroundAfter, timeoutSeconds(params.Timeout), defaultWaitSeconds())
			autoBackgroundThreshold := time.Duration(autoBackgroundAfter) * time.Second
			timeout := time.After(autoBackgroundThreshold)

			var stdout, stderr string
			var done bool
			var execErr error

		waitLoop:
			for {
				select {
				case <-ticker.C:
					stdout, stderr, done, execErr = bgShell.GetOutput()
					if done {
						break waitLoop
					}
				case <-timeout:
					stdout, stderr, done, execErr = bgShell.GetOutput()
					break waitLoop
				case <-ctx.Done():
					// Incoming context was cancelled before we moved to background
					// Kill the shell and return error
					bgManager.Kill(bgShell.ID)
					return fantasy.ToolResponse{}, ctx.Err()
				}
			}

			if done {
				// Command completed within threshold - return synchronously
				// Remove from background manager since we're returning directly
				// Don't call Kill() as it cancels the context and corrupts the exit code
				bgManager.Remove(bgShell.ID)

				interrupted := shell.IsInterrupt(execErr)
				exitCode := shell.ExitCode(execErr)
				if exitCode == 0 && !interrupted && execErr != nil {
					return fantasy.ToolResponse{}, fmt.Errorf("[Job %s] error executing command: %w", bgShell.ID, execErr)
				}

				stdout = formatOutput(stdout, stderr, execErr, spillDir)

				metadata := BashResponseMetadata{
					StartTime:        startTime.UnixMilli(),
					EndTime:          time.Now().UnixMilli(),
					Output:           stdout,
					Description:      params.Description,
					Background:       params.RunInBackground,
					WorkingDirectory: bgShell.WorkingDir,
					ExitCode:         exitCode,
				}
				if stdout == "" {
					return fantasy.WithResponseMetadata(fantasy.NewTextResponse(BashNoOutput), metadata), nil
				}
				stdout += fmt.Sprintf("\n\n<cwd>%s</cwd>", normalizeWorkingDir(bgShell.WorkingDir))
				return fantasy.WithResponseMetadata(fantasy.NewTextResponse(stdout), metadata), nil
			}

			// Still running - keep as background job
			metadata := BashResponseMetadata{
				StartTime:        startTime.UnixMilli(),
				EndTime:          time.Now().UnixMilli(),
				Description:      params.Description,
				WorkingDirectory: bgShell.WorkingDir,
				Background:       true,
				ShellID:          bgShell.ID,
			}
			response := fmt.Sprintf("Command is taking longer than expected and has been moved to background.\n\nBackground shell ID: %s\n\n%s", bgShell.ID, backgrounded(ctx, bgShell, onBackground))
			return fantasy.WithResponseMetadata(fantasy.NewTextResponse(response), metadata), nil
		},
	)
}

// backgrounded hands job, a command that became a background job, to
// onBackground, and says what the agent can do with it: with a watcher,
// it is told when the job ends, as Claude Code tells it, so it need not
// poll.
func backgrounded(ctx context.Context, job *shell.BackgroundShell, onBackground BackgroundWatcher) string {
	if onBackground == nil {
		return "Call the job_output tool with this ID to read its output, or the job_kill tool to stop it."
	}
	onBackground(ctx, job)
	return "You will be told when it ends; there is no need to poll it. Call the job_output tool with this ID to read its output, or the job_kill tool to stop it."
}

// sessionEnvSetup is the shell code a bash command runs first: the env
// file of its session (WithSessionEnvFile), which the SessionStart hooks
// write export lines to (Claude Code's CLAUDE_ENV_FILE), when it holds
// anything, as Claude Code runs it before each Bash command. "" when
// there is none.
func sessionEnvSetup(ctx context.Context) string {
	path := SessionEnvFileFromContext(ctx)
	if path == "" {
		return ""
	}
	if info, err := os.Stat(path); err != nil || info.Size() == 0 {
		return ""
	}
	return ". '" + strings.ReplaceAll(path, "'", `'\''`) + "'"
}

// formatOutput formats the output of a completed command with error handling
func formatOutput(stdout, stderr string, execErr error, spillDir string) string {
	interrupted := shell.IsInterrupt(execErr)
	exitCode := shell.ExitCode(execErr)

	stdout = truncateOutput(stdout, spillDir)
	stderr = truncateOutput(stderr, spillDir)

	errorMessage := stderr
	if errorMessage == "" && execErr != nil {
		errorMessage = execErr.Error()
	}

	if interrupted {
		if errorMessage != "" {
			errorMessage += "\n"
		}
		errorMessage += "Command was aborted before completion"
	} else if exitCode != 0 {
		if errorMessage != "" {
			errorMessage += "\n"
		}
		errorMessage += fmt.Sprintf("Exit code %d", exitCode)
	}

	hasBothOutputs := stdout != "" && stderr != ""

	if hasBothOutputs {
		stdout += "\n"
	}

	if errorMessage != "" {
		stdout += "\n" + errorMessage
	}

	return stdout
}

// TruncateOutput keeps the head and tail of oversized tool output. The
// dropped middle is written in full to a file under spillDir so the agent
// can read it back rather than losing it for good.
func TruncateOutput(content, spillDir string) string {
	limit := maxOutputLength()
	runeCount := len([]rune(content))
	if ansi.StringWidth(content) <= limit && runeCount <= limit {
		return content
	}

	halfLength := limit / 2
	var start, end string
	if ansi.StringWidth(content) > limit {
		start = ansi.Truncate(content, halfLength, "")
		end = ansi.TruncateLeft(content, ansi.StringWidth(content)-halfLength, "")
	} else {
		// Oversized by raw length rather than display width: output full of
		// zero-width bytes (a binary printed by cat, say) fills the context
		// window while measuring near nothing on screen, so the width check
		// alone lets it through. Cut on rune boundaries instead.
		r := []rune(content)
		start = string(r[:halfLength])
		end = string(r[len(r)-halfLength:])
	}

	truncatedLinesCount := max(strings.Count(content, "\n")-strings.Count(start, "\n")-strings.Count(end, "\n"), 0)
	marker := fmt.Sprintf("... [%d lines truncated] ...", truncatedLinesCount)
	if path, err := shell.SpillOutput(ansi.Strip(content), spillDir); err == nil {
		marker = fmt.Sprintf("... [%d lines truncated, full output: %s] ...", truncatedLinesCount, path)
	} else {
		slog.Debug("Could not spill tool output", "dir", spillDir, "error", err)
	}
	return fmt.Sprintf("%s\n\n%s\n\n%s", start, marker, end)
}

func truncateOutput(content, spillDir string) string {
	return TruncateOutput(content, spillDir)
}

func normalizeWorkingDir(path string) string {
	if runtime.GOOS == "windows" {
		path = strings.ReplaceAll(path, fsext.WindowsWorkingDirDrive(), "")
	}
	return filepath.ToSlash(path)
}
