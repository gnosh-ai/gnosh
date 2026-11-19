package app

import (
	"bufio"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/gnosh-ai/gnosh/internal/agent"
	"github.com/gnosh-ai/gnosh/internal/cli"
	"github.com/gnosh-ai/gnosh/internal/core"
)

// This file is --input-format stream-json (spec/cc/output-formats.md 5,
// cli.md 4.6): a run that is a conversation. Standard input carries one
// JSON user message a line; each is sent as a prompt, and each turn ends
// with a result line, while the stream goes on. The run ends when standard
// input closes and the turns it asked for have ended, with the last turn's
// exit code. A message's priority says when it goes (next, between the
// rounds of a running turn or as the next turn; later, after the running
// turn; now, stopping it), shouldQuery false keeps it for the next message
// that asks, a blank message ends at once with an empty result, and uuid
// comes back on the result as user_message_uuid. A text that begins with a
// slash is a command, unless client_composed. With --replay-user-messages
// each message read is written back first, as a user line with isReplay.
// Control requests (the SDK's own messages on this channel) are not read:
// a line of another type is passed over, with a warning.

// maxStreamLine is the longest line of standard input that is read.
const maxStreamLine = 268_435_456

// streamUser is one line of standard input.
type streamUser struct {
	Type    string `json:"type"`
	Message struct {
		Role    string          `json:"role"`
		Content json.RawMessage `json:"content"`
	} `json:"message"`
	UUID           string `json:"uuid"`
	Priority       string `json:"priority"`
	ShouldQuery    *bool  `json:"shouldQuery"`
	ClientComposed bool   `json:"client_composed"`
}

// replayLine is a user message written back (--replay-user-messages).
type replayLine struct {
	Type            string          `json:"type"`
	Message         json.RawMessage `json:"message"`
	ParentToolUseID *string         `json:"parent_tool_use_id"`
	UUID            string          `json:"uuid"`
	SessionID       string          `json:"session_id"`
	IsReplay        bool            `json:"isReplay"`
}

// streamIn is what the reader of standard input hands on: a message, or
// why it stopped.
type streamIn struct {
	u      streamUser
	line   []byte
	text   string
	images []core.Block
	err    error
}

// runStream is a run whose prompts come on standard input as JSON lines.
// A message is sent once the one before it has started its turn (or come
// back unsent): two messages that arrive while the session is idle are two
// turns, as in Claude Code, and one that arrives while a turn runs is read
// between its rounds, or after it with priority later.
func (f *printer) runStream(ctx context.Context, c Controls) int {
	f.mu.Lock()
	f.init()
	f.starts = make(chan []string, 64)
	f.mu.Unlock()
	in := make(chan streamIn)
	go f.readStream(ctx, in)

	signals := c.Signals()
	code := cli.ExitOK
	var last agent.TurnResult
	ended := false
	// The prompts sent and not yet answered, with when each went and the
	// uuid its sender gave it; those of them whose turn has not started;
	// and the messages that wait for that.
	type waiting struct {
		since time.Time
		uuid  string
	}
	pending := map[string]waiting{}
	unstarted := map[string]bool{}
	var backlog []streamIn
	var held []string // text kept for the next message that asks

	handle := func(m streamIn) {
		if f.inv.ReplayUserMessages {
			f.mu.Lock()
			f.encode(replayLine{Type: "user", Message: json.RawMessage(m.line), UUID: firstOf(m.u.UUID, newUUID()), SessionID: f.sessionID(), IsReplay: true})
			f.mu.Unlock()
		}
		if m.u.ShouldQuery != nil && !*m.u.ShouldQuery {
			held = append(held, m.text)
			return
		}
		text := strings.Join(append(held, m.text), "\n\n")
		held = nil
		if strings.TrimSpace(text) == "" && len(m.images) == 0 {
			// Nothing to answer: the turn ends at once.
			f.mu.Lock()
			obj := f.result(c, agent.TurnResult{Reason: agent.EndCompleted})
			obj.Subtype, obj.Result, obj.NumTurns, obj.DurationMS, obj.UserMessageUUID = "success", ptr(""), 0, 0, m.u.UUID
			f.encode(obj)
			f.mu.Unlock()
			return
		}
		if !m.u.ClientComposed && strings.HasPrefix(strings.TrimSpace(text), "/") {
			res, err := c.Command(ctx, strings.TrimSpace(text))
			switch {
			case err != nil || res.Handled && res.Prompt == "":
				if f.streamCommand(c, res, err, m.u.UUID) {
					code = cli.ExitFailure
				}
				return
			case res.Handled:
				pending[res.Prompt] = waiting{since: f.p.now(), uuid: m.u.UUID}
				unstarted[res.Prompt] = true
				return
			}
			// Not a command ("/etc/nginx.conf fails to load", or slash
			// commands are off): a prompt like any other, as in the text
			// path. (It waited under no id, and the run never ended.)
		}
		p := agent.Prompt{Text: text, Attachments: m.images, Later: m.u.Priority == "later"}
		if m.u.ClientComposed {
			// Delivered as it is: no @ mention brings a file.
			p.Meta = map[string]any{metaVerbatim: true}
		}
		id, err := c.Submit(p)
		if err != nil {
			f.p.errf("a message could not be sent: %v", err)
			code = cli.ExitFailure
			return
		}
		pending[id] = waiting{since: f.p.now(), uuid: m.u.UUID}
		unstarted[id] = true
	}
	drain := func() {
		for len(unstarted) == 0 && len(backlog) > 0 {
			m := backlog[0]
			backlog = backlog[1:]
			handle(m)
		}
	}

	for {
		if in == nil && len(pending) == 0 && len(backlog) == 0 {
			// Input is over and every message answered: the run waits for
			// the background work its turns started (printwait.go).
			if ended {
				res, sig, st := f.awaitBackground(ctx, c, last, true)
				if sig != nil {
					return f.stoppedBySignal(c, "", sig, st)
				}
				if res.Turn != last.Turn {
					if res.Reason == agent.EndCompleted || res.Reason == agent.EndModelStopped {
						code = cli.ExitOK
					} else {
						code = cli.ExitFailure
					}
				}
				code = c.RunEnd(res, code)
			}
			return code
		}
		select {
		case m, ok := <-in:
			if !ok {
				in = nil
				continue
			}
			if m.err != nil {
				f.p.errf("%v", m.err)
				c.Interrupt()
				return cli.ExitFailure
			}
			if m.u.Priority == "now" && (m.u.ShouldQuery == nil || *m.u.ShouldQuery) && (strings.TrimSpace(m.text) != "" || len(m.images) > 0) {
				// It stops what runs as it arrives. Stopped when its turn
				// came to be sent, after the messages before it, it let the
				// turn it was sent to stop run on, and stopped one that
				// started after it came. The person's messages before it
				// stay in line and are answered.
				c.Interrupt()
			}
			backlog = append(backlog, m)
			drain()
		case ids := <-f.starts:
			for _, id := range ids {
				delete(unstarted, id)
			}
			drain()
		case res := <-f.results:
			var w waiting
			found := false
			for _, id := range res.Prompts {
				delete(unstarted, id) // (one that came back unsent never starts)
				if p, ok := pending[id]; ok {
					if !found {
						w, found = p, true
					}
					delete(pending, id)
				}
			}
			if !found && res.Turn != "" {
				// A turn no message started: the end of background work
				// woke the model. Its result line is written too.
				f.emitResult(c, res)
				last, ended = res, true
			}
			if found {
				f.mu.Lock()
				f.turnStart = w.since
				obj := f.result(c, res)
				obj.UserMessageUUID = w.uuid
				if obj.IsError || res.Reason == agent.EndRefused {
					code = cli.ExitFailure
				} else {
					code = cli.ExitOK
				}
				f.encode(obj)
				f.rounds = 0
				f.mu.Unlock()
				last, ended = res, true
			}
			drain()
		case s := <-signals:
			st := c.State()
			c.Interrupt()
			return f.stoppedBySignal(c, "", s, st)
		case <-ctx.Done():
			return cli.ExitFailure
		}
	}
}

// streamCommand writes what a slash command sent as a message said, as a
// result line, and reports whether it failed.
func (f *printer) streamCommand(c Controls, res CommandResult, err error, uuid string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	obj := resultObject{
		Type: "result", Subtype: "success", UUID: newUUID(), SessionID: c.Info().ID,
		Result: ptr(strings.TrimRight(res.Text, "\n")), ModelUsage: map[string]modelUsage{}, PermissionDenials: []denial{},
		LocalCommand: res.Name, UserMessageUUID: uuid,
	}
	switch {
	case err != nil:
		obj.Subtype, obj.IsError, obj.Result, obj.Errors = "error_during_execution", true, nil, []string{err.Error()}
	case !res.Handled:
		obj.Subtype, obj.IsError, obj.Result, obj.Errors = "error_during_execution", true, nil, []string{"there is no such command"}
	}
	f.encode(obj)
	return obj.IsError
}

// readStream reads standard input a line at a time and hands on each user
// message, until the input ends or a line cannot be read.
func (f *printer) readStream(ctx context.Context, out chan<- streamIn) {
	defer close(out)
	send := func(m streamIn) bool {
		select {
		case out <- m:
			return true
		case <-ctx.Done():
			return false
		}
	}
	stdin := f.p.o.Stdin
	if stdin == nil {
		stdin = os.Stdin
	}
	r := bufio.NewReaderSize(stdin, 1<<20)
	for {
		line, err := readStreamLine(r)
		if len(strings.TrimSpace(string(line))) > 0 {
			var u streamUser
			switch jerr := json.Unmarshal(line, &u); {
			case jerr != nil:
				send(streamIn{err: fmt.Errorf("a line of standard input is not a JSON object (%v), so the run stopped", jerr)})
				return
			case u.Type != "user":
				f.mu.Lock()
				f.stderr("a line of type %q on standard input was passed over: a run reads user messages only", u.Type)
				f.mu.Unlock()
			default:
				text, images, cerr := streamContent(u.Message.Content)
				if cerr != nil {
					send(streamIn{err: fmt.Errorf("a user message on standard input cannot be read: %v, so the run stopped", cerr)})
					return
				}
				if !send(streamIn{u: u, line: append([]byte(nil), streamMessage(line)...), text: text, images: images}) {
					return
				}
			}
		}
		switch {
		case errors.Is(err, errLineTooLong):
			send(streamIn{err: fmt.Errorf("a line of standard input is longer than %d characters, so the run stopped", maxStreamLine)})
			return
		case err != nil:
			return // the input ended
		}
	}
}

// errLineTooLong is a line of standard input over maxStreamLine.
var errLineTooLong = errors.New("line too long")

// readStreamLine reads one line, without its line break.
func readStreamLine(r *bufio.Reader) ([]byte, error) {
	var line []byte
	for {
		chunk, err := r.ReadSlice('\n')
		line = append(line, chunk...)
		if len(line) > maxStreamLine {
			return nil, errLineTooLong
		}
		switch {
		case err == nil:
			return line[:len(line)-1], nil
		case errors.Is(err, bufio.ErrBufferFull):
			continue
		case errors.Is(err, io.EOF) && len(line) > 0:
			return line, io.EOF
		default:
			return nil, err
		}
	}
}

// streamMessage is the "message" of a line, for a replay: the line's own
// bytes of it.
func streamMessage(line []byte) []byte {
	var whole struct {
		Message json.RawMessage `json:"message"`
	}
	if json.Unmarshal(line, &whole) != nil || len(whole.Message) == 0 {
		return []byte("{}")
	}
	return whole.Message
}

// streamContent reads a message's content: a string, or a list of text
// and image blocks.
func streamContent(raw json.RawMessage) (string, []core.Block, error) {
	if len(raw) == 0 || string(raw) == "null" {
		return "", nil, nil
	}
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return s, nil, nil
	}
	var blocks []struct {
		Type   string `json:"type"`
		Text   string `json:"text"`
		Source struct {
			Type      string `json:"type"`
			MediaType string `json:"media_type"`
			Data      string `json:"data"`
		} `json:"source"`
	}
	if err := json.Unmarshal(raw, &blocks); err != nil {
		return "", nil, errors.New("its content is neither a string nor a list of blocks")
	}
	var texts []string
	var images []core.Block
	for _, b := range blocks {
		switch b.Type {
		case "text":
			texts = append(texts, b.Text)
		case "image":
			if b.Source.Type != "base64" {
				return "", nil, fmt.Errorf("an image block whose source is %q, and only base64 is read", b.Source.Type)
			}
			data, err := base64.StdEncoding.DecodeString(b.Source.Data)
			if err != nil {
				return "", nil, fmt.Errorf("an image block whose data is not base64: %v", err)
			}
			images = append(images, core.Block{Type: core.BlockImage, MediaType: b.Source.MediaType, Data: data})
		default:
			return "", nil, fmt.Errorf("a block of type %q, and only text and image blocks are read", b.Type)
		}
	}
	return strings.Join(texts, "\n\n"), images, nil
}
