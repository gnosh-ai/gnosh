package app

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"time"

	"github.com/gnosh-ai/gnosh/internal/agent"
	"github.com/gnosh-ai/gnosh/internal/core"
	"github.com/gnosh-ai/gnosh/internal/prompt"
	"github.com/gnosh-ai/gnosh/internal/tools/shelltool"
	"github.com/gnosh-ai/gnosh/internal/transcript"
)

// shellLine is SessionControls.Shell: a command line the person typed behind "!"
// is run for them, through the same shell tool the model's commands run
// through, so that it has the session's working directory, its
// environment and its limits, and can be moved to the background like any
// other. It is the person's own: no rule is looked at, nothing is asked,
// and the shell tool leaves out what is there to steer a model. A person
// may type ssh.
//
// The front end is shown it as a tool call. When it has ended, the command
// and what it printed go to the model as a prompt of gnosh's own, which
// says whose command it was, and the model answers. One that the person
// stopped is not answered, and with the respondToBashCommands option off
// none is: it is put in the conversation at once, before their next
// prompt.
//
// The command line is also kept in the session's file as a meta entry
// (metaShell), for the prompt history: Up and shell mode's Tab offer it
// again, in this session and the folder's later ones.
func (l *live) shellLine(command string) error {
	command = strings.TrimSpace(command)
	if command == "" {
		return errors.New("there is no command after the !")
	}
	if l.shell == nil || l.closing.Load() {
		return errors.New("the session is closing")
	}
	if shelltool.GnokeyPassword(command) {
		// Caught before anything keeps the line: the session's file, the
		// history Up brings back, and what the model is sent would each
		// hold the password.
		return errors.New("the command gives gnokey a password on its standard input, and what is typed here is kept in the session's file and shown to the model: it was not run, and not kept. Sign in a terminal of your own")
	}
	bash := l.shell.PersonsBash()
	l.shellMu.Lock()
	if l.shellStop != nil {
		l.shellMu.Unlock()
		return errors.New("a command of yours is still running. Wait for it, stop it, or move it to the background")
	}
	ctx, cancel := context.WithCancel(l.ctx)
	l.shellStop = cancel
	l.shellMu.Unlock()
	if l.ts != nil {
		// (A history that lacks a line is no reason to keep the command
		// from running.)
		_, _ = l.ts.Append(transcript.MetaEntry(metaShell, command))
	}

	input, _ := json.Marshal(map[string]string{"command": command})
	call := core.ToolCall{ID: newCallID(), Name: shelltool.BashName, Input: input}
	if call.ID == "" {
		call.ID = "call_shell_" + l.p.now().UTC().Format("150405.000000000")
	}
	// The command is the person's own until it ends; then another may
	// start, before the model has answered this one (the answer is a turn
	// of its own, which can end before this goroutine goes on).
	ended := sync.OnceFunc(func() {
		cancel()
		l.shellMu.Lock()
		l.shellStop = nil
		l.shellMu.Unlock()
	})
	l.bg.Add(1)
	go func() {
		defer l.bg.Done()
		defer ended()
		meta := func() agent.Meta { return agent.Meta{Time: l.p.now(), Session: l.ts.ID()} }
		env := l.env()
		env.Progress = func(p core.Progress) {
			l.deliver(&agent.ToolProgressEvent{Meta: meta(), CallID: call.ID, Stream: p.Stream, Text: p.Text})
		}
		action, _ := describe(bash, call, env)
		started := l.p.now()
		l.deliver(&agent.ToolStartEvent{Meta: meta(), CallID: call.ID, Name: call.Name, Input: input, Action: action})
		res, err := runPersonsCommand(ctx, bash, call, env)
		if err != nil {
			res = core.ErrorResult("The command could not be run: " + strings.TrimRight(err.Error(), ".") + ".")
		}
		res = cleanResult(call, res)
		stopped := ctx.Err() != nil
		if res.Cwd != "" && !stopped && l.agent != nil {
			// Their cd moves the session, as one in the model's own calls
			// does (the shell tool checked it against the session's
			// folders), and the result tells the model so.
			l.agent.SetCwd(res.Cwd)
			l.cwdMoved(l.ctx, res.Cwd)
		}
		ended()
		l.deliver(&agent.ToolEndEvent{
			Meta: meta(), CallID: call.ID, Name: call.Name, Input: input,
			Result: res, Duration: l.p.now().Sub(started), Interrupted: stopped, Ran: true,
		})
		if l.closing.Load() || l.agent == nil {
			return
		}
		// Into the conversation. The command's output is data, like a
		// tool's: it is made harmless on its way in.
		// (Not "go on with what they asked for before": on box 1, in a
		// session with nothing asked before, Qwen went looking for it.)
		text := prompt.Wrap("The user ran this shell command themselves, in this session's working directory, and this is what it printed. It was their own command, not a request to run it again: answer what it shows, in a few words, and if you were in the middle of something for them, go on with it.") +
			"\n\n$ " + prompt.Untrusted(command) + "\n" + prompt.Untrusted(core.TextOf(res.Content))
		// One the model does not answer now is put in the conversation at
		// once (Record), so that it stands before the person's next
		// prompt and is kept if the session ends before that.
		switch {
		case stopped:
			_ = l.agent.Record(agent.Note{Source: "shell", Text: text + "\n" + prompt.Wrap("The user stopped it before it finished.")})
		case !l.respondShell.Load():
			_ = l.agent.Record(agent.Note{Source: "shell", Text: text})
		default:
			_, _ = l.agent.Submit(agent.Prompt{Text: text, Hidden: true, Source: "shell"})
		}
	}()
	return nil
}

// metaShell is the key of the meta entries that hold the command lines
// the person ran behind "!", as typed after it.
const metaShell = "shell"

// runPersonsCommand runs the command and survives a tool that crashes
// over it.
func runPersonsCommand(ctx context.Context, bash core.Tool, call core.ToolCall, env *core.ToolEnv) (res core.ToolResult, err error) {
	defer func() {
		if p := recover(); p != nil {
			res, err = core.ErrorResult("The command could not be run: the shell tool crashed over it."), nil
		}
	}()
	return bash.Run(ctx, call, env)
}

// The most the prompt history reads: so many of the folder's sessions,
// the most recent ones, and so many prompts.
const (
	historySessions = 20
	historyPrompts  = 500
)

// promptHistory is SessionControls.History: the prompts the person sent from
// this folder, in its most recent sessions and in this one, the oldest
// first, and among them the command lines they ran behind "!", with the
// "!". A prompt that a session's file no longer holds (one that was
// erased from it) is not in it.
func (l *live) promptHistory() []string {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	list, err := l.store.List(ctx, l.cfg.Paths.Cwd)
	if err != nil {
		list = nil
	}
	prompts := func(entries []transcript.Entry) []string {
		var out []string
		for i := range entries {
			if te := &entries[i]; te.Type == transcript.TypeMeta {
				if te.Key == metaShell && strings.TrimSpace(te.Value) != "" {
					out = append(out, "!"+te.Value)
				}
				continue
			}
			for _, e := range decode(&entries[i]) {
				if e.Kind != agent.EntryPrompt || e.Hidden {
					continue
				}
				// (A blank at the start stays: " !x" and " /x" were sent
				// as prompts, and must not come back as a shell line or a
				// command.)
				if text := strings.TrimRight(core.TextOf(e.Blocks), " \t\r\n"); strings.TrimSpace(text) != "" {
					out = append(out, text)
				}
			}
		}
		return out
	}
	// The folder's sessions come the most recent first; this one may not
	// be among them yet, when nothing has been written to it.
	var perSession [][]string
	own := false
	for _, info := range list {
		if len(perSession) == historySessions {
			break
		}
		if info.ID == l.ts.ID() {
			own = true
			perSession = append(perSession, prompts(l.ts.Entries()))
			continue
		}
		s, err := l.store.OpenReadOnly(ctx, info.ID)
		if err != nil {
			continue
		}
		perSession = append(perSession, prompts(s.Entries()))
		_ = s.Close()
	}
	var out []string
	for i := len(perSession) - 1; i >= 0; i-- {
		out = append(out, perSession[i]...)
	}
	if !own {
		out = append(out, prompts(l.ts.Entries())...)
	}
	// The same line sent twice in a row is one line of history.
	dedup := out[:0]
	for _, p := range out {
		if len(dedup) == 0 || dedup[len(dedup)-1] != p {
			dedup = append(dedup, p)
		}
	}
	if len(dedup) > historyPrompts {
		dedup = dedup[len(dedup)-historyPrompts:]
	}
	return dedup
}
