package tools

import (
	"bytes"
	"cmp"
	"context"
	_ "embed"
	"errors"
	"fmt"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	"charm.land/fantasy"
	"github.com/charmbracelet/crush/internal/permission"
	"github.com/charmbracelet/crush/internal/question"
	"mvdan.cc/sh/v3/shell"
)

const GnokeyToolName = "gnokey"

//go:embed gnokey.md
var gnokeyDescription string

// GnokeyParams are the gnokey tool's inputs. Their JSON names are the bash
// tool's, so the chat and the permission dialog show a gnokey call the way
// they show a bash one.
type GnokeyParams struct {
	Command     string `json:"command" description:"The gnokey command line as you would type it, with or without the leading gnokey, the key name last, e.g. maketx addpkg -pkgpath gno.land/r/dev/counter -pkgdir . -gas-fee 1000000ugnot -gas-wanted 20000000 -chainid dev -remote 127.0.0.1:26657 devtest"`
	Description string `json:"description" description:"One line telling the user what this does, e.g. Deploy counter to the local gnodev chain"`
	WorkingDir  string `json:"working_dir,omitempty" description:"Directory to run in; -pkgdir and -tx-path resolve against it (default: the project directory)"`
}

// gnokeyTimeout bounds one gnokey run. Broadcasting waits for the
// transaction to be committed, which takes seconds; a remote that does not
// answer is reported by gnokey itself long before this.
const gnokeyTimeout = 5 * time.Minute

// passwordStdinFlag is gnokey's flag for reading the key's password from
// standard input. The tool adds it itself when a command signs; a model
// must not.
const passwordStdinFlag = "-insecure-password-stdin"

// gnokeyCommand is a parsed gnokey command line.
type gnokeyCommand struct {
	// args is the command line without the leading "gnokey".
	args []string
	// action names the subcommand for the permission dialog: "maketx
	// addpkg", "sign", "query".
	action string
	// signs is whether gnokey will need the key's password.
	signs bool
	// key is the key named in a signing command, for the prompt, or ""
	// when it cannot be told.
	key string
	// insertAt is where passwordStdinFlag goes: after the subcommand (and
	// maketx's own), before its flags.
	insertAt int
}

// parseGnokeyCommand splits a gnokey command line and decides whether it
// signs. Key management subcommands are refused: they ask their own
// questions on a terminal (a new password twice, a mnemonic) and belong in
// the user's hands.
func parseGnokeyCommand(command string) (*gnokeyCommand, error) {
	fields, err := shell.Fields(command, nil)
	if err != nil {
		return nil, fmt.Errorf("cannot parse the command: %w", err)
	}
	if len(fields) > 0 && filepath.Base(fields[0]) == "gnokey" {
		fields = fields[1:]
	}
	if len(fields) == 0 {
		return nil, errors.New("the command is empty: give a gnokey subcommand with its flags, the key name last")
	}
	for _, f := range fields {
		if name, _, _ := strings.Cut(strings.TrimLeft(f, "-"), "="); name == strings.TrimLeft(passwordStdinFlag, "-") {
			return nil, fmt.Errorf("do not pass %s: the tool asks the user for the password itself", passwordStdinFlag)
		}
	}
	c := &gnokeyCommand{args: fields, action: fields[0], insertAt: 1}
	switch fields[0] {
	case "maketx":
		if len(fields) < 2 || strings.HasPrefix(fields[1], "-") {
			return nil, errors.New("maketx needs a subcommand: addpkg, call, send, run, session, enablepkg or rejectpkg")
		}
		c.action = "maketx " + fields[1]
		c.insertAt = 2
		c.signs = !flagIsFalse(fields, "broadcast")
	case "sign":
		c.signs = true
	case "broadcast", "query", "list", "verify", "version", "multisign":
	case "add", "delete", "rotate", "import", "export", "generate":
		return nil, fmt.Errorf("gnokey %s manages keys through its own prompts on a terminal; ask the user to run it themselves", fields[0])
	default:
		return nil, fmt.Errorf("unknown gnokey subcommand %q", fields[0])
	}
	if c.signs {
		if last := fields[len(fields)-1]; len(fields) > c.insertAt && !strings.HasPrefix(last, "-") {
			c.key = last
		}
	}
	return c, nil
}

// flagIsFalse reports whether the command line sets the boolean flag name
// to false, as in -broadcast=false. Go's flag parsing, which gnokey uses,
// takes a boolean's value only in that form.
func flagIsFalse(fields []string, name string) bool {
	for _, f := range fields {
		if k, v, ok := strings.Cut(strings.TrimLeft(f, "-"), "="); ok && k == name {
			b, err := strconv.ParseBool(v)
			return err == nil && !b
		}
	}
	return false
}

// argv is what gnokey runs: the command line with the password flag added
// when it signs.
func (c *gnokeyCommand) argv() []string {
	if !c.signs {
		return slices.Clone(c.args)
	}
	return slices.Concat(c.args[:c.insertAt], []string{passwordStdinFlag}, c.args[c.insertAt:])
}

// display is the command line as the user reads it, quoted again where an
// argument has spaces or quotes.
func (c *gnokeyCommand) display() string {
	parts := make([]string, 0, len(c.args)+1)
	parts = append(parts, "gnokey")
	for _, a := range c.args {
		parts = append(parts, shellQuote(a))
	}
	return strings.Join(parts, " ")
}

func shellQuote(s string) string {
	if s != "" && !strings.ContainsAny(s, " \t\n'\"\\$`") {
		return s
	}
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// mainnet reports whether the command targets gno.land's mainnet, where a
// transaction spends real GNOT.
func (c *gnokeyCommand) mainnet() bool {
	for _, a := range c.args {
		if a == "gnoland-1" || strings.Contains(a, "rpc.gno.land") {
			return true
		}
	}
	return false
}

// NewGnokeyTool makes the tool that runs gnokey for the user. A command that
// signs needs the key's password: the tool asks the user for it in a masked
// prompt (question.Question.Secret) and writes it to gnokey's standard
// input, so it never reaches the model, the transcript or a log. questions
// is nil when there is no user to ask, as in a non-interactive run; the
// tool then refuses to sign.
func NewGnokeyTool(permissions permission.Service, questions question.Service, workingDir string) fantasy.AgentTool {
	return fantasy.NewAgentTool(
		GnokeyToolName,
		gnokeyDescription,
		func(ctx context.Context, params GnokeyParams, call fantasy.ToolCall) (fantasy.ToolResponse, error) {
			cmd, err := parseGnokeyCommand(params.Command)
			if err != nil {
				return fantasy.NewTextErrorResponse(err.Error()), nil
			}
			if _, err := exec.LookPath("gnokey"); err != nil {
				return fantasy.NewTextErrorResponse("gnokey is not installed or not on PATH; see https://docs.gno.land/builders/install"), nil
			}
			sessionID := GetSessionFromContext(ctx)
			if sessionID == "" {
				return fantasy.ToolResponse{}, errors.New("session ID is required for running gnokey")
			}
			dir := cmp.Or(params.WorkingDir, workingDir)
			shown := cmd.display()
			granted, err := permissions.Request(ctx, permission.CreatePermissionRequest{
				SessionID:   sessionID,
				Path:        dir,
				ToolCallID:  call.ID,
				ToolName:    GnokeyToolName,
				Action:      cmd.action,
				Description: cmp.Or(params.Description, "Run "+shown),
				Params:      BashPermissionsParams{Command: shown, Description: params.Description, WorkingDir: dir},
			})
			if err != nil {
				return fantasy.ToolResponse{}, err
			}
			if !granted {
				return NewPermissionDeniedResponse(), nil
			}

			var stdin bytes.Buffer
			var password string
			if cmd.signs {
				if questions == nil {
					return fantasy.NewTextErrorResponse("signing needs the key's password typed by the user, and this run has no one to ask; give the user this command to run in a terminal: " + shown), nil
				}
				password, err = askGnokeyPassword(ctx, questions, sessionID, call.ID, cmd)
				if errors.Is(err, question.ErrCancelled) {
					resp := fantasy.NewTextErrorResponse("The user cancelled signing; ask them how to proceed instead of retrying")
					resp.StopTurn = true
					return resp, nil
				}
				if err != nil {
					return fantasy.NewTextErrorResponse(err.Error()), nil
				}
				stdin.WriteString(password + "\n")
			}

			start := time.Now()
			output, exitCode, err := runGnokey(ctx, dir, cmd.argv(), &stdin)
			if password != "" {
				// gnokey never echoes the password; this makes sure of it.
				output = strings.ReplaceAll(output, password, "[password]")
			}
			meta := BashResponseMetadata{
				StartTime:        start.UnixMilli(),
				EndTime:          time.Now().UnixMilli(),
				Output:           output,
				Description:      params.Description,
				WorkingDirectory: dir,
				ExitCode:         exitCode,
			}
			text := strings.TrimSpace(output)
			switch {
			case err != nil:
				return fantasy.WithResponseMetadata(fantasy.NewTextErrorResponse(fmt.Sprintf("gnokey did not finish: %v\n%s", err, text)), meta), nil
			case exitCode != 0:
				return fantasy.WithResponseMetadata(fantasy.NewTextErrorResponse(fmt.Sprintf("gnokey exited with code %d\n%s", exitCode, text)), meta), nil
			case text == "":
				text = BashNoOutput
			}
			return fantasy.WithResponseMetadata(fantasy.NewTextResponse(text), meta), nil
		},
	)
}

// askGnokeyPassword asks the user for the key's password through the
// question service, in a masked prompt that shows the command the password
// will sign.
func askGnokeyPassword(ctx context.Context, questions question.Service, sessionID, callID string, cmd *gnokeyCommand) (string, error) {
	text := "Password to sign with"
	if cmd.key != "" {
		text = "Password for key " + cmd.key
	}
	note := "The password goes to gnokey's standard input and nowhere else; the model never sees it. Esc cancels."
	if cmd.mainnet() {
		note = "**Mainnet (gnoland-1): this spends real GNOT.**\n\n" + note
	}
	// The permission dialog showed the whole command; here a very long one
	// loses its tail to the description limit.
	const frame = "Signs and runs:\n\n`%s`\n\n%s"
	shown := cmd.display()
	if room := question.MaxDescriptionLength - len(frame) - len(note); len(shown) > room {
		shown = shown[:room-3] + "..."
	}
	answers, err := questions.Ask(ctx, question.Request{
		SessionID:  sessionID,
		ToolCallID: callID,
		Questions: []question.Question{{
			Type:        question.TypeFreeText,
			Secret:      true,
			Label:       "Password",
			Text:        text,
			Description: fmt.Sprintf(frame, shown, note),
		}},
	})
	if err != nil {
		return "", err
	}
	if len(answers) == 0 {
		return "", errors.New("no password was given")
	}
	return answers[0].FillInText, nil
}

// runGnokey runs gnokey in dir with stdin on its standard input and returns
// everything it printed, in order, with its exit code. The error is for a
// run that did not finish: gnokey could not start, or the context ended.
func runGnokey(ctx context.Context, dir string, argv []string, stdin *bytes.Buffer) (string, int, error) {
	ctx, cancel := context.WithTimeout(ctx, gnokeyTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, "gnokey", argv...)
	cmd.Dir = dir
	cmd.Stdin = stdin
	var out bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &out
	err := cmd.Run()
	var exitErr *exec.ExitError
	switch {
	case err == nil:
		return out.String(), 0, nil
	case ctx.Err() != nil:
		return out.String(), -1, ctx.Err()
	case errors.As(err, &exitErr):
		return out.String(), exitErr.ExitCode(), nil
	default:
		return out.String(), -1, err
	}
}
