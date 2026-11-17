package bridge

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sync"

	"github.com/gnosh-ai/gnosh/internal/core"
	"github.com/gnosh-ai/gnosh/internal/transcript"
)

// Options is what a Server is made from.
type Options struct {
	// Project is the project's directory, an absolute path: the working
	// directory and the one root every call is given.
	Project string
	// Build and Version are what hello reports: the build id the GnoVM
	// worker's handshake compares, and a line naming the build.
	Build   string
	Version string
	// Tools makes the tools the bridge serves. It is called once, when the
	// Server is made, with the way to read a session's stack, for the
	// tools' MemStack.
	Tools func(stack func(session string) []string) []core.Tool
	// Frames reads the frames a call pushed and popped from its result.
	// Nil means calls report none.
	Frames func(res core.ToolResult) []Frame
	// Confirms names the tools some of whose calls need a person's
	// approval, for ToolInfo.Confirm. The calls themselves are judged by
	// core.Describer.
	Confirms map[string]bool
	// Log, when not nil, is told what the bridge has to say for a person:
	// one line each, for standard error.
	Log func(line string)
}

// Server answers one host's requests. It is safe to use from one
// goroutine at a time, which is how Serve uses it.
type Server struct {
	opts   Options
	tools  []core.Tool
	byName map[string]core.Tool

	mu       sync.Mutex
	sessions map[string]*session
	hello    bool
}

// session is what the bridge keeps of one host session: its stack (the
// frame ids it stands in, bottom to top) and the store its calls share.
type session struct {
	stack []string
	store *sync.Map
}

// New makes a Server.
func New(o Options) *Server {
	s := &Server{opts: o, sessions: map[string]*session{}, byName: map[string]core.Tool{}}
	if o.Tools != nil {
		s.tools = o.Tools(s.Stack)
	}
	for _, t := range s.tools {
		s.byName[t.Spec().Name] = t
	}
	return s
}

// Stack is the stack session's mem calls are made from (bridge.md §6.3).
func (s *Server) Stack(id string) []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	if ss := s.sessions[id]; ss != nil {
		return append([]string(nil), ss.stack...)
	}
	return nil
}

func (s *Server) session(id string) *session {
	s.mu.Lock()
	defer s.mu.Unlock()
	ss := s.sessions[id]
	if ss == nil {
		ss = &session{store: &sync.Map{}}
		s.sessions[id] = ss
	}
	return ss
}

// errEnd is what Handle returns, beside its reply, when the bridge must
// end after writing it.
var errEnd = errors.New("the host broke the protocol, so the bridge ends")

// Serve reads requests from r and writes their replies to w, one line
// each, until r ends, ctx is cancelled between requests, or a request
// ends the protocol. It returns nil when r ended.
func (s *Server) Serve(ctx context.Context, r io.Reader, w io.Writer) error {
	in := bufio.NewScanner(r)
	in.Buffer(make([]byte, 0, 64<<10), MaxLine)
	out := json.NewEncoder(w)
	out.SetEscapeHTML(false)
	for in.Scan() {
		if err := ctx.Err(); err != nil {
			return err
		}
		line := in.Bytes()
		if len(line) == 0 {
			continue
		}
		rep, err := s.Handle(ctx, line)
		if werr := out.Encode(rep); werr != nil {
			return fmt.Errorf("writing a reply to the host: %w", werr)
		}
		if err != nil {
			return err
		}
	}
	if err := in.Err(); err != nil {
		if errors.Is(err, bufio.ErrTooLong) {
			_ = out.Encode(fail(nil, CodeBadRequest, fmt.Sprintf("a request line is longer than %d bytes", MaxLine)))
		}
		return fmt.Errorf("reading the host's requests: %w", err)
	}
	return nil
}

// Handle answers one request line. A non-nil error means the bridge must
// end once the reply is written.
func (s *Server) Handle(ctx context.Context, line []byte) (Reply, error) {
	var req Request
	if err := json.Unmarshal(line, &req); err != nil {
		var id struct {
			ID json.RawMessage `json:"id"`
		}
		_ = json.Unmarshal(line, &id)
		if !s.hello {
			return fail(id.ID, CodeProtocol, "the first request must be hello, and this line is not a request: "+err.Error()), errEnd
		}
		return fail(id.ID, CodeBadRequest, "the line is not a request: "+err.Error()), nil
	}
	if !s.hello && req.Op != OpHello {
		return fail(req.ID, CodeProtocol, fmt.Sprintf("the first request must be hello, not %q", req.Op)), errEnd
	}
	switch req.Op {
	case OpHello:
		return s.helloReply(req)
	case OpTools:
		return s.toolsReply(req), nil
	case OpCall:
		return s.call(ctx, req), nil
	case OpView:
		return s.view(req), nil
	case "":
		return fail(req.ID, CodeBadRequest, "the request has no op"), nil
	}
	return fail(req.ID, CodeUnknownOp, fmt.Sprintf("this bridge does not answer %q; it answers %v", req.Op, ops)), nil
}

func (s *Server) helloReply(req Request) (Reply, error) {
	if req.Protocol != Protocol {
		rep := fail(req.ID, CodeProtocol, fmt.Sprintf("this bridge speaks protocol %d, and the host asked for %d", Protocol, req.Protocol))
		rep.Error.Protocols = []int{Protocol}
		return rep, errEnd
	}
	s.hello = true
	if s.opts.Log != nil && req.Client != "" {
		s.opts.Log("gnosh bridge: serving " + req.Client)
	}
	return Reply{OK: true, ID: req.ID, Protocol: Protocol, Build: s.opts.Build, Version: s.opts.Version,
		Project: s.opts.Project, Ops: append([]string(nil), ops...)}, nil
}

func (s *Server) toolsReply(req Request) Reply {
	rep := Reply{OK: true, ID: req.ID, Tools: []ToolInfo{}}
	for _, t := range s.tools {
		spec := t.Spec()
		rep.Tools = append(rep.Tools, ToolInfo{Name: spec.Name, Description: spec.Description,
			InputSchema: spec.InputSchema, Confirm: s.opts.Confirms[spec.Name]})
	}
	return rep
}

func (s *Server) call(ctx context.Context, req Request) Reply {
	switch {
	case req.Session == "":
		return fail(req.ID, CodeBadRequest, "call needs session")
	case req.CallID == "":
		return fail(req.ID, CodeBadRequest, "call needs call_id")
	case req.Name == "":
		return fail(req.ID, CodeBadRequest, "call needs name")
	}
	t := s.byName[req.Name]
	if t == nil {
		return fail(req.ID, CodeUnknownTool, fmt.Sprintf("there is no tool %q; tools lists the ones there are", req.Name))
	}
	input := req.Input
	if len(input) == 0 || string(input) == "null" {
		input = json.RawMessage("{}")
	}
	ss := s.session(req.Session)
	call := core.ToolCall{ID: req.CallID, Name: req.Name, Input: input}
	env := &core.ToolEnv{Cwd: s.opts.Project, Roots: []string{s.opts.Project}, SessionID: req.Session, Store: ss.store}
	if d, ok := t.(core.Describer); ok && !req.Confirmed {
		if a, err := d.Describe(call, env); err == nil && a.Confirm {
			rep := fail(req.ID, CodeConfirm, "a person must approve this call first; send it again with confirmed: true once they have")
			rep.Error.Summary = a.Summary
			return rep
		}
	}
	res, err := t.Run(ctx, call, env)
	if err != nil {
		return fail(req.ID, CodeInternal, fmt.Sprintf("running %s: %v", req.Name, err))
	}
	rep := Reply{OK: true, ID: req.ID, Content: res.Content, IsError: res.IsError}
	if s.opts.Frames != nil {
		for _, f := range s.opts.Frames(res) {
			if f.Session != req.Session {
				continue
			}
			rep.Frames = append(rep.Frames, f)
			s.apply(ss, f)
		}
	}
	if rep.Content == nil {
		rep.Content = []core.Block{}
	}
	return rep
}

// apply moves a session's stack by one frame a call kept.
func (s *Server) apply(ss *session, f Frame) {
	s.mu.Lock()
	defer s.mu.Unlock()
	switch f.Kind {
	case "push":
		ss.stack = append(ss.stack, f.ID)
	case "pop":
		for i := len(ss.stack) - 1; i >= 0; i-- {
			if ss.stack[i] == f.ID {
				ss.stack = ss.stack[:i]
				return
			}
		}
	}
}

func (s *Server) view(req Request) Reply {
	if req.Session == "" {
		return fail(req.ID, CodeBadRequest, "view needs session")
	}
	seen := make(map[string]bool, len(req.Entries))
	for i, e := range req.Entries {
		if e.UUID == "" {
			return fail(req.ID, CodeBadRequest, fmt.Sprintf("entry %d has no uuid", i))
		}
		if seen[e.UUID] {
			return fail(req.ID, CodeBadRequest, fmt.Sprintf("entry %d has the uuid %q of an entry before it", i, e.UUID))
		}
		seen[e.UUID] = true
	}
	msgs, info := transcript.View(req.Entries, transcript.ViewOptions{
		Budget: req.Budget, Room: req.Room, KeepRounds: req.KeepRounds, Prompt: req.Prompt,
		Session: req.Session, Trace: true,
	})
	// The stack the session's next mem calls are made from is the view's,
	// slides included (bridge.md §6.3).
	ss := s.session(req.Session)
	stack := make([]string, len(info.Stack))
	rep := Reply{OK: true, ID: req.ID, Messages: msgs, Sources: info.Sources, Tokens: info.Tokens,
		OverBudget: info.OverBudget, NewSlides: info.NewSlides, Stack: []StackFrame{}}
	for i, f := range info.Stack {
		stack[i] = f.ID
		rep.Stack = append(rep.Stack, StackFrame{ID: f.ID, Label: f.Label, CallID: f.ToolUseID, Slid: f.Slid})
	}
	s.mu.Lock()
	ss.stack = stack
	s.mu.Unlock()
	if rep.Messages == nil {
		rep.Messages = []core.Message{}
	}
	for i := range rep.Sources {
		if rep.Sources[i] == nil {
			rep.Sources[i] = []string{}
		}
	}
	return rep
}

func fail(id json.RawMessage, code, msg string) Reply {
	return Reply{OK: false, ID: id, Error: &Error{Code: code, Message: msg}}
}
