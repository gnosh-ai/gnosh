package app

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/gnosh-ai/gnosh/internal/app/skill"
	"github.com/gnosh-ai/gnosh/internal/core"
	"github.com/gnosh-ai/gnosh/internal/permission"
	"github.com/gnosh-ai/gnosh/internal/tools/shelltool"
)

// This file is a skill's commands that run before the model sees it
// (spec/cc/slash-commands-and-skills.md 5.4): !`command` at the start of a
// line or after a blank, and a fenced block opened with ```!. Each runs in
// the session's working directory, with stderr in with stdout and two
// minutes to finish, and what it prints takes its place in the skill's
// text, in one pass: what a command prints is not read for more. A command
// never asks: one the rules do not allow outright (a deny, an ask, or no
// rule that lets it run) stops the skill, and so does one that fails. A
// skill's allowed-tools let its commands run.

// The limits of a skill's command: its time, and how much of what it
// prints stands in the skill's text before the rest goes to a file.
const (
	injectTime    = 2 * time.Minute
	injectMost    = 30000
	injectPreview = 2000
)

// injectCommands runs the commands a skill's text asks for and puts what
// each printed in its place.
func (l *live) injectCommands(sk skill.Skill, text string) (string, error) {
	return l.inject(text, true)
}

// checkCommands checks every command a skill's text asks for against the
// rules, as injectCommands does before it runs one, and runs none: a typed
// line of several skills checks all of theirs before any runs.
func (l *live) checkCommands(text string) error {
	_, err := l.inject(text, false)
	return err
}

// inject is injectCommands, and with run false checkCommands.
func (l *live) inject(text string, run bool) (string, error) {
	lines := strings.Split(text, "\n")
	var out []string
	for i := 0; i < len(lines); i++ {
		if strings.HasPrefix(strings.TrimSpace(lines[i]), "```!") {
			j := i + 1
			for j < len(lines) && strings.TrimSpace(lines[j]) != "```" {
				j++
			}
			if j >= len(lines) {
				return "", fmt.Errorf("the block of commands that begins on line %d of the skill's text is not closed with ```", i+1)
			}
			got, err := l.injectOne(strings.Join(lines[i+1:j], "\n"), run)
			if err != nil {
				return "", err
			}
			out = append(out, got)
			i = j
			continue
		}
		line, b := lines[i], strings.Builder{}
		at := 0
		for k := 0; k+1 < len(line); k++ {
			if line[k] != '!' || line[k+1] != '`' || (k > 0 && line[k-1] != ' ' && line[k-1] != '\t') {
				continue
			}
			end := strings.IndexByte(line[k+2:], '`')
			if end < 0 {
				break
			}
			got, err := l.injectOne(line[k+2:k+2+end], run)
			if err != nil {
				return "", err
			}
			b.WriteString(line[at:k])
			b.WriteString(got)
			at = k + 2 + end + 1
			k = at - 1
		}
		b.WriteString(line[at:])
		out = append(out, b.String())
	}
	return strings.Join(out, "\n"), nil
}

// injectOne checks one command against the rules, as a call of the Bash
// tool, runs it when run is set, and returns what it printed.
func (l *live) injectOne(command string, run bool) (string, error) {
	pattern := oneLine(command, 200)
	refused := func(why string) error {
		return fmt.Errorf("Shell command permission check failed for pattern %q: %s", pattern, why)
	}
	var bash core.Tool
	for _, t := range l.tools {
		if t.Spec().Name == shelltool.BashName {
			bash = t
		}
	}
	if bash == nil || l.engine == nil {
		return "", refused("this session has no Bash tool")
	}
	input, err := json.Marshal(map[string]string{"command": command})
	if err != nil {
		return "", refused(err.Error())
	}
	call := core.ToolCall{ID: newCallID(), Name: shelltool.BashName, Input: input}
	action, err := describe(bash, call, l.env())
	if err != nil {
		return "", refused(err.Error())
	}
	cwd := l.cwd()
	if l.agent != nil {
		if t := l.agent.Tree(); t != nil {
			// A session in a git work tree of its own keeps its skills'
			// commands there too, as it keeps its own (review, 16 November).
			if why := l.commandOutsideTree(*t, cwd, command); why != "" {
				return "", refused(why)
			}
		}
	}
	if d := l.reach(cwd, call, action, l.engine.DecideAt(cwd, call.Name, call, action)); d.Behavior != permission.Allow {
		return "", refused(firstOf(strings.TrimRight(d.Reason, "."), "no rule lets it run") + ". A skill's commands never ask: its allowed-tools can let them run")
	}
	if !run {
		return "", nil
	}
	out, code, err := l.runInjected(command, cwd)
	if err == nil && code != 0 && (code != 1 || shelltool.HarmlessExit(command) == "") {
		err = fmt.Errorf("exit code %d", code)
	}
	if err != nil {
		return "", fmt.Errorf("Shell command failed for pattern %q: %v\n%s", pattern, err, oneLine(out, 2000))
	}
	return out, nil
}

// runInjected runs one command and returns what it printed, stderr in
// with stdout, and its exit code.
func (l *live) runInjected(command, dir string) (string, int, error) {
	ctx, cancel := context.WithTimeout(l.ctx, injectTime)
	defer cancel()
	shell := "/bin/sh"
	if st, err := os.Stat("/bin/bash"); err == nil && !st.IsDir() {
		shell = "/bin/bash"
	}
	cmd := exec.CommandContext(ctx, shell, "-c", command)
	cmd.Dir = dir
	// As the Bash tool's commands: what the person's env setting takes
	// away is taken away, and what changes how a shell reads its line
	// (BASH_ENV, SHELLOPTS, PS4, exported functions) is left out. Appended
	// to the whole environment, a taken-away variable stayed (the config
	// review of 2026-11-19).
	cmd.Env = shelltool.CommandEnv(l.p.o.Environ, dir, l.cfg.CommandEnv())
	cmd.WaitDelay = 2 * time.Second
	var buf bytes.Buffer
	cmd.Stdout, cmd.Stderr = &buf, &buf
	err := cmd.Run()
	code := 0
	var exit *exec.ExitError
	switch {
	case ctx.Err() != nil:
		return buf.String(), -1, fmt.Errorf("it did not finish within %s and was stopped", injectTime)
	case errors.As(err, &exit):
		code, err = exit.ExitCode(), nil
	}
	if err != nil {
		return buf.String(), -1, err
	}
	out := strings.TrimRight(buf.String(), "\n")
	if len(out) > injectMost {
		// All of it goes to a file, and its start stands here.
		path := filepath.Join(l.spoolDir(), "skill-command-"+newCallID()+".txt")
		if os.MkdirAll(filepath.Dir(path), 0o700) == nil && os.WriteFile(path, []byte(out), 0o600) == nil {
			out = fmt.Sprintf("[The command printed %d bytes; all of it is in %s. It begins:]\n%s", len(out), path, strings.ToValidUTF8(out[:injectPreview], ""))
		} else {
			out = strings.ToValidUTF8(out[:injectMost], "") + "\n[The rest of what the command printed was cut.]"
		}
	}
	return out, code, nil
}
