// Package mcp is a client of MCP servers (the Model Context Protocol):
// outside programs that offer tools. It speaks JSON-RPC 2.0 to a server
// in one of two ways (spec/cc/mcp.md): to a server it starts, over the
// server's standard input and output, one message to a line (the stdio
// transport, stdio.go); or to a server at a URL, a POST to a message
// (the Streamable HTTP transport, http.go).
//
// A Client makes the initialize handshake, lists the server's tools and
// calls them. It answers what a server may ask of a client: ping, and
// roots/list with the folders it was given; anything else is answered
// "method not found". Stopping a started server closes its input and then,
// as Claude Code does, sends SIGINT, SIGTERM 100 ms later and SIGKILL
// 400 ms after that, to the server's whole process group.
package mcp

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// ProtocolVersion is the version of MCP the client asks for. A server
// answers with the version it speaks, which the client accepts.
const ProtocolVersion = "2025-06-18"

// Server is how to start one server.
type Server struct {
	// Name is the server's name in the configuration.
	Name string
	// Command, Args and Env start it: Env is added to Environ.
	Command string
	Args    []string
	Env     map[string]string
	// Environ is the environment the server starts from; nil means none
	// at all but Env.
	Environ []string
	// Dir is the folder it starts in.
	Dir string
	// Roots are the folders roots/list names, as file URLs are made of
	// them.
	Roots []string
}

// Options are the client's own settings.
type Options struct {
	// ClientName and ClientVersion are what the handshake says the client
	// is. Default "gnosh" and "devel".
	ClientName, ClientVersion string
	// StartTimeout bounds starting the server and the handshake. Default
	// 30 seconds.
	StartTimeout time.Duration
	// MaxStderr is how much of the server's standard error is kept, for
	// the person to read when it fails. Default 16 KiB.
	MaxStderr int
	// MaxLine is the longest message the server may send. Default 32 MiB.
	MaxLine int
	// OnToolsChanged, when set, is called (in a goroutine of its own) each
	// time the server says its tool list changed.
	OnToolsChanged func()
	// OnChannel, when set, is given each event the server pushes as a
	// channel, Claude Code's notifications/claude/channel: its content,
	// and its meta with only the keys that are letters, digits and
	// underscores, and only string values. It is called in the order the
	// events come, on the goroutine that reads the server, so it must not
	// wait long.
	OnChannel func(content string, meta map[string]string)
	// OnChannelVerdict, when set, is given each answer to a permission
	// question the server sends as a channel that relays them, Claude
	// Code's notifications/claude/channel/permission: the request's id,
	// five lowercase letters, and "allow" or "deny". Anything else is
	// dropped, as is a verdict from a server that did not declare
	// claude/channel/permission. It is called as OnChannel is.
	OnChannelVerdict func(requestID, behavior string)
	// Auth, for a server reached at a URL, gives its requests the
	// person's sign-in to it (auth.go); nil sends none.
	Auth *Authorizer
	// OnElicit asks the person what a server asks for in the middle of a
	// call (elicit.go), and returns their answer; it may wait as long as
	// they take. With it set the client declares the elicitation
	// capability; nil declares none, and a server's request is refused.
	OnElicit func(ctx context.Context, e Elicitation) (ElicitResult, error)
	// OnHandshake, when set, is told what the server said of itself as
	// soon as the handshake is done, before any notification of the
	// server's is taken: what a channel is goes in front of its first
	// event.
	OnHandshake func(Info)
}

// Tool is one tool a server offers.
type Tool struct {
	Name        string          `json:"name"`
	Title       string          `json:"title,omitempty"`
	Description string          `json:"description,omitempty"`
	InputSchema json.RawMessage `json:"inputSchema,omitempty"`
	Annotations struct {
		ReadOnly bool `json:"readOnlyHint,omitempty"`
	} `json:"annotations"`
}

// Content is one piece of what a tool call came to.
type Content struct {
	Type     string `json:"type"`
	Text     string `json:"text,omitempty"`
	MimeType string `json:"mimeType,omitempty"`
	Data     string `json:"data,omitempty"`
	Resource *struct {
		URI      string `json:"uri"`
		Text     string `json:"text,omitempty"`
		MimeType string `json:"mimeType,omitempty"`
	} `json:"resource,omitempty"`
}

// Result is what a tool call came to.
type Result struct {
	Content           []Content       `json:"content"`
	IsError           bool            `json:"isError,omitempty"`
	StructuredContent json.RawMessage `json:"structuredContent,omitempty"`
}

// Info is what the server said of itself in the handshake.
type Info struct {
	Name            string
	Version         string
	ProtocolVersion string
	Instructions    string
	// Resources says it offers resources, and Prompts that it offers
	// prompts (its capabilities name them).
	Resources, Prompts bool
	// Channel says it declared itself a channel (the experimental
	// capability claude/channel): it pushes events into the session.
	// ChannelPermission says it also takes permission prompts to relay
	// (claude/channel/permission, declared and not false).
	Channel, ChannelPermission bool
}

// Prompt is one prompt a server offers: a message it writes for the user
// to send, from the arguments given.
type Prompt struct {
	Name        string           `json:"name"`
	Title       string           `json:"title,omitempty"`
	Description string           `json:"description,omitempty"`
	Arguments   []PromptArgument `json:"arguments,omitempty"`
}

// PromptArgument is one argument a prompt takes.
type PromptArgument struct {
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	Required    bool   `json:"required,omitempty"`
}

// PromptMessage is one message a prompt comes to.
type PromptMessage struct {
	Role    string  `json:"role"`
	Content Content `json:"content"`
}

// Resource is one resource a server lists: data it makes readable by URI.
type Resource struct {
	URI         string `json:"uri"`
	Name        string `json:"name"`
	Title       string `json:"title,omitempty"`
	Description string `json:"description,omitempty"`
	MimeType    string `json:"mimeType,omitempty"`
}

// ResourceContents is one part of what a resource holds: text, or a blob
// in base64.
type ResourceContents struct {
	URI      string `json:"uri"`
	MimeType string `json:"mimeType,omitempty"`
	Text     string `json:"text,omitempty"`
	Blob     string `json:"blob,omitempty"`
}

// RPCError is an error a server answered a request with.
type RPCError struct {
	Code    int             `json:"code"`
	Message string          `json:"message"`
	Data    json.RawMessage `json:"data,omitempty"`
}

func (e *RPCError) Error() string {
	return fmt.Sprintf("the server answered with an error (%d): %s", e.Code, e.Message)
}

// ErrClosed is returned for a request made, or pending, when the server's
// connection is gone.
var ErrClosed = errors.New("mcp: the server is not running")

// ErrSessionEnded is returned when a server at a URL answers that the
// session the request carried is over: the request was refused, and did
// nothing.
var ErrSessionEnded = errors.New("mcp: the server has ended this session")

// ErrUnreachable is returned when a server at a URL could not be reached
// with a request; whether the request got there is not known.
var ErrUnreachable = errors.New("mcp: the server could not be reached")

// message is one JSON-RPC message, either way.
type message struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method,omitempty"`
	Params  json.RawMessage `json:"params,omitempty"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   *RPCError       `json:"error,omitempty"`

	// failed is set by the transport, never sent: the answer to the
	// request will not come, and why (Client.fail).
	failed error
}

// Client is the connection to one server.
type Client struct {
	srv  Server
	opt  Options
	t    transport
	info Info

	mu      sync.Mutex
	next    int64
	pending map[string]chan message
	closed  bool
	err     error // why the connection ended
	// changed is set when the server says its tool list changed.
	changed bool
	// channel says the server declared itself a channel (Info.Channel),
	// and relay that it takes permission questions too
	// (Info.ChannelPermission), for the goroutine that reads it.
	channel, relay atomic.Bool
	// serving holds the requests of the server's that are being answered,
	// by their ids: the server may take one back (notifications/cancelled),
	// and the connection's end takes them all back. (An elicitation waits
	// for the person.)
	serving map[string]*served
	// toolsRunning says OnToolsChanged runs now, and toolsAgain that a
	// notice came while it ran: it runs once more, however many came.
	toolsRunning, toolsAgain bool
}

// maxServing is how many of the server's own requests are answered at
// once; one more is refused, so that a flood of them (pings) does not
// grow without end.
const maxServing = 32

// served is a request of the server's that is being answered.
type served struct{ cancel context.CancelFunc }

// serve notes that the server's request with the id is being answered,
// and gives the context its answer is made in, which ends when the server
// takes the request back or the connection ends; done forgets it. It is
// called before the next message is read, so that a request taken back
// at once is found.
func (c *Client) serve(id json.RawMessage) (ctx context.Context, done func()) {
	ctx, cancel := context.WithCancel(context.Background())
	s, key := &served{cancel: cancel}, idKey(id)
	c.mu.Lock()
	if c.closed || c.err != nil {
		cancel()
	} else {
		if c.serving == nil {
			c.serving = map[string]*served{}
		}
		c.serving[key] = s
	}
	c.mu.Unlock()
	return ctx, func() {
		c.mu.Lock()
		if c.serving[key] == s {
			delete(c.serving, key)
		}
		c.mu.Unlock()
		cancel()
	}
}

// transport carries messages to a server and brings its own back, which
// it hands to the client's dispatch.
type transport interface {
	// send hands one message to the server.
	send(ctx context.Context, m message) error
	// stderr is the end of what a started server wrote on its standard
	// error, or "".
	stderr() string
	// close ends the connection, and with a started server the server.
	close() error
}

// Start starts the server and makes the handshake.
func Start(ctx context.Context, srv Server, opt Options) (*Client, error) {
	opt = opt.defaults()
	if strings.TrimSpace(srv.Command) == "" {
		return nil, errors.New("mcp: the server has no command")
	}
	c := &Client{srv: srv, opt: opt, pending: map[string]chan message{}}
	st, err := startStdio(c, srv, opt)
	if err != nil {
		return nil, err
	}
	c.t = st
	return c, c.handshake(ctx)
}

// defaults fills in the options that were left at zero.
func (opt Options) defaults() Options {
	if opt.ClientName == "" {
		opt.ClientName = "gnosh"
	}
	if opt.ClientVersion == "" {
		opt.ClientVersion = "devel"
	}
	if opt.StartTimeout <= 0 {
		opt.StartTimeout = 30 * time.Second
	}
	if opt.MaxStderr <= 0 {
		opt.MaxStderr = 16 << 10
	}
	if opt.MaxLine <= 0 {
		opt.MaxLine = 32 << 20
	}
	return opt
}

// handshake makes the initialize handshake. On failure the connection is
// closed.
func (c *Client) handshake(ctx context.Context) error {
	opt := c.opt
	hctx, cancel := context.WithTimeout(ctx, opt.StartTimeout)
	defer cancel()
	caps := map[string]any{"roots": map[string]any{"listChanged": false}}
	if opt.OnElicit != nil {
		caps["elicitation"] = map[string]any{}
	}
	raw, err := c.request(hctx, "initialize", map[string]any{
		"protocolVersion": ProtocolVersion,
		"capabilities":    caps,
		"clientInfo":      map[string]any{"name": opt.ClientName, "version": opt.ClientVersion},
	})
	if err != nil {
		c.Close()
		return c.failure("the handshake failed", err)
	}
	var hello struct {
		ProtocolVersion string `json:"protocolVersion"`
		ServerInfo      struct {
			Name    string `json:"name"`
			Version string `json:"version"`
		} `json:"serverInfo"`
		Instructions string                     `json:"instructions"`
		Capabilities map[string]json.RawMessage `json:"capabilities"`
	}
	if err := json.Unmarshal(raw, &hello); err != nil {
		c.Close()
		return fmt.Errorf("mcp: the server's answer to the handshake cannot be read: %w", err)
	}
	_, resources := hello.Capabilities["resources"]
	_, prompts := hello.Capabilities["prompts"]
	var experimental map[string]json.RawMessage
	_ = json.Unmarshal(hello.Capabilities["experimental"], &experimental)
	_, channel := experimental["claude/channel"]
	relay := strings.TrimSpace(string(experimental["claude/channel/permission"]))
	c.info = Info{Name: hello.ServerInfo.Name, Version: hello.ServerInfo.Version, ProtocolVersion: hello.ProtocolVersion, Instructions: hello.Instructions,
		Resources: resources, Prompts: prompts, Channel: channel, ChannelPermission: relay != "" && relay != "false" && relay != "null"}
	// (Read by the goroutine that reads the server: its events are taken
	// from now on, the handshake done.)
	if c.opt.OnHandshake != nil {
		c.opt.OnHandshake(c.info)
	}
	c.channel.Store(channel)
	c.relay.Store(channel && c.info.ChannelPermission)
	if h, ok := c.t.(*httpTransport); ok {
		h.setVersion(hello.ProtocolVersion)
	}
	if err := c.notify("notifications/initialized", nil); err != nil {
		c.Close()
		return c.failure("the handshake could not be finished", err)
	}
	return nil
}

// failure says why the server did not answer, with what it wrote on its
// standard error.
func (c *Client) failure(what string, err error) error {
	if tail := strings.TrimSpace(c.t.stderr()); tail != "" {
		return fmt.Errorf("mcp: %s: %w. What the server wrote: %s", what, err, tail)
	}
	return fmt.Errorf("mcp: %s: %w", what, err)
}

// Info returns what the server said of itself.
func (c *Client) Info() Info { return c.info }

// Stderr returns the end of what the server wrote on its standard error.
func (c *Client) Stderr() string { return c.t.stderr() }

// ToolsChanged reports whether the server said its tool list changed since
// the last call of ListTools.
func (c *Client) ToolsChanged() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.changed
}

// ListTools lists the server's tools, every page of them.
func (c *Client) ListTools(ctx context.Context) ([]Tool, error) {
	c.mu.Lock()
	c.changed = false
	c.mu.Unlock()
	var all []Tool
	cursor := ""
	for page := 0; page < 100; page++ {
		params := map[string]any{}
		if cursor != "" {
			params["cursor"] = cursor
		}
		raw, err := c.request(ctx, "tools/list", params)
		if err != nil {
			return nil, err
		}
		var res struct {
			Tools      []Tool `json:"tools"`
			NextCursor string `json:"nextCursor"`
		}
		if err := json.Unmarshal(raw, &res); err != nil {
			return nil, fmt.Errorf("mcp: the tool list cannot be read: %w", err)
		}
		all = append(all, res.Tools...)
		if res.NextCursor == "" {
			return all, nil
		}
		cursor = res.NextCursor
	}
	return nil, errors.New("mcp: the server's tool list never ends")
}

// ListResources lists the server's resources, every page of them.
func (c *Client) ListResources(ctx context.Context) ([]Resource, error) {
	var all []Resource
	cursor := ""
	for page := 0; page < 100; page++ {
		params := map[string]any{}
		if cursor != "" {
			params["cursor"] = cursor
		}
		raw, err := c.request(ctx, "resources/list", params)
		if err != nil {
			return nil, err
		}
		var res struct {
			Resources  []Resource `json:"resources"`
			NextCursor string     `json:"nextCursor"`
		}
		if err := json.Unmarshal(raw, &res); err != nil {
			return nil, fmt.Errorf("mcp: the resource list cannot be read: %w", err)
		}
		all = append(all, res.Resources...)
		if res.NextCursor == "" {
			return all, nil
		}
		cursor = res.NextCursor
	}
	return nil, errors.New("mcp: the server's resource list never ends")
}

// ReadResource reads one resource by its URI.
func (c *Client) ReadResource(ctx context.Context, uri string) ([]ResourceContents, error) {
	raw, err := c.request(ctx, "resources/read", map[string]any{"uri": uri})
	if err != nil {
		return nil, err
	}
	var res struct {
		Contents []ResourceContents `json:"contents"`
	}
	if err := json.Unmarshal(raw, &res); err != nil {
		return nil, fmt.Errorf("mcp: the resource cannot be read: %w", err)
	}
	return res.Contents, nil
}

// ListPrompts lists the server's prompts, every page of them.
func (c *Client) ListPrompts(ctx context.Context) ([]Prompt, error) {
	var all []Prompt
	cursor := ""
	for page := 0; page < 100; page++ {
		params := map[string]any{}
		if cursor != "" {
			params["cursor"] = cursor
		}
		raw, err := c.request(ctx, "prompts/list", params)
		if err != nil {
			return nil, err
		}
		var res struct {
			Prompts    []Prompt `json:"prompts"`
			NextCursor string   `json:"nextCursor"`
		}
		if err := json.Unmarshal(raw, &res); err != nil {
			return nil, fmt.Errorf("mcp: the prompt list cannot be read: %w", err)
		}
		all = append(all, res.Prompts...)
		if res.NextCursor == "" {
			return all, nil
		}
		cursor = res.NextCursor
	}
	return nil, errors.New("mcp: the server's prompt list never ends")
}

// GetPrompt gets the messages a prompt comes to with the arguments given.
func (c *Client) GetPrompt(ctx context.Context, name string, args map[string]string) ([]PromptMessage, error) {
	if args == nil {
		args = map[string]string{}
	}
	raw, err := c.request(ctx, "prompts/get", map[string]any{"name": name, "arguments": args})
	if err != nil {
		return nil, err
	}
	var res struct {
		Messages []PromptMessage `json:"messages"`
	}
	if err := json.Unmarshal(raw, &res); err != nil {
		return nil, fmt.Errorf("mcp: the prompt's messages cannot be read: %w", err)
	}
	return res.Messages, nil
}

// CallTool calls one tool with its arguments, a JSON object.
func (c *Client) CallTool(ctx context.Context, name string, args json.RawMessage) (Result, error) {
	if len(bytes.TrimSpace(args)) == 0 {
		args = json.RawMessage(`{}`)
	}
	raw, err := c.request(ctx, "tools/call", map[string]any{"name": name, "arguments": args})
	if err != nil {
		return Result{}, err
	}
	var res Result
	if err := json.Unmarshal(raw, &res); err != nil {
		return Result{}, fmt.Errorf("mcp: the tool's result cannot be read: %w", err)
	}
	return res, nil
}

// Close ends the connection. A started server's input is closed, and then
// it is sent SIGINT, SIGTERM 100 ms later and SIGKILL 400 ms after that, as
// long as it runs; an http server's session is ended.
func (c *Client) Close() error {
	c.mu.Lock()
	already := c.closed
	c.closed = true
	c.mu.Unlock()
	if already || c.t == nil {
		return nil
	}
	err := c.t.close()
	c.lost(nil)
	return err
}

// request sends a request and waits for its answer.
func (c *Client) request(ctx context.Context, method string, params any) (json.RawMessage, error) {
	c.mu.Lock()
	if c.closed || c.err != nil {
		err := c.err
		c.mu.Unlock()
		if err == nil {
			err = ErrClosed
		}
		return nil, err
	}
	c.next++
	id := fmt.Sprintf("%d", c.next)
	ch := make(chan message, 1)
	c.pending[id] = ch
	c.mu.Unlock()
	defer func() {
		c.mu.Lock()
		delete(c.pending, id)
		c.mu.Unlock()
	}()
	// The server is told a request that is given up.
	giveUp := func() error {
		_ = c.notify("notifications/cancelled", map[string]any{"requestId": json.RawMessage(id), "reason": "the client stopped waiting"})
		return ctx.Err()
	}
	// (Over http the answer may come back with the request itself: send
	// then returns once it has been handed to the channel.)
	if err := c.t.send(ctx, message{JSONRPC: "2.0", ID: json.RawMessage(id), Method: method, Params: mustJSON(params)}); err != nil {
		if ctx.Err() != nil {
			return nil, giveUp()
		}
		return nil, err
	}
	select {
	case m, ok := <-ch:
		if !ok {
			return nil, c.ended()
		}
		if m.failed != nil {
			return nil, m.failed
		}
		if m.Error != nil {
			return nil, m.Error
		}
		return m.Result, nil
	case <-ctx.Done():
		return nil, giveUp()
	}
}

// toolsChanged runs OnToolsChanged for a notice that the server's tools
// changed: on one goroutine at a time, and once more after it if notices
// came while it ran, however many. A server that sends a flood of them
// costs one listing at a time, not a goroutine each.
func (c *Client) toolsChanged() {
	c.mu.Lock()
	if c.toolsRunning {
		c.toolsAgain = true
		c.mu.Unlock()
		return
	}
	c.toolsRunning = true
	c.mu.Unlock()
	go func() {
		for {
			c.opt.OnToolsChanged()
			c.mu.Lock()
			if !c.toolsAgain {
				c.toolsRunning = false
				c.mu.Unlock()
				return
			}
			c.toolsAgain = false
			c.mu.Unlock()
		}
	}()
}

// ended is why pending requests can no longer be answered.
func (c *Client) ended() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.err != nil {
		return c.err
	}
	return ErrClosed
}

func (c *Client) notify(method string, params any) error {
	return c.send(message{JSONRPC: "2.0", Method: method, Params: mustJSON(params)})
}

// send sends what needs no answer: a notification, or the client's answer
// to a server's request.
func (c *Client) send(m message) error {
	ctx, cancel := context.WithTimeout(context.Background(), c.opt.StartTimeout)
	defer cancel()
	return c.t.send(ctx, m)
}

// dispatch takes one message from the server: an answer goes to the request
// that waits for it, a request of the server's is answered, and a
// notification is noted.
func (c *Client) dispatch(m message) {
	switch {
	case m.Method != "" && len(m.ID) > 0:
		c.mu.Lock()
		busy := len(c.serving) >= maxServing
		c.mu.Unlock()
		if busy {
			_ = c.send(message{JSONRPC: "2.0", ID: m.ID, Error: &RPCError{Code: -32000, Message: "gnosh is answering too many of the server's requests at once"}})
			return
		}
		ctx, done := c.serve(m.ID)
		go func() {
			defer done()
			c.answer(ctx, m)
		}()
	case m.Method != "":
		c.notified(m)
	case len(m.ID) > 0:
		// (Handed over under the lock, which lost holds as it closes the
		// channels: an answer that comes as the connection ends is not
		// sent on a closed one.)
		c.mu.Lock()
		if ch := c.pending[strings.Trim(string(m.ID), `"`)]; ch != nil {
			select {
			case ch <- m:
			default:
			}
		}
		c.mu.Unlock()
	}
}

// fail ends the wait of the request with that id, if it still waits, with
// err: the transport knows its answer will not come (a server at a URL
// whose answer to it ended without it).
func (c *Client) fail(id json.RawMessage, err error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if ch := c.pending[strings.Trim(string(id), `"`)]; ch != nil {
		select {
		case ch <- message{ID: id, failed: err}:
		default:
		}
	}
}

// lost ends the connection for every request that waits: they fail with
// err, or ErrClosed.
func (c *Client) lost(err error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.err == nil {
		c.err = err
		if c.err == nil {
			c.err = ErrClosed
		}
	}
	for id, ch := range c.pending {
		close(ch)
		delete(c.pending, id)
	}
	for id, s := range c.serving {
		s.cancel()
		delete(c.serving, id)
	}
}

// answer answers a request the server made of the client, unless the
// server takes it back (ctx ends) first.
func (c *Client) answer(ctx context.Context, m message) {
	reply := message{JSONRPC: "2.0", ID: m.ID}
	switch m.Method {
	case "ping":
		reply.Result = json.RawMessage(`{}`)
	case "roots/list":
		roots := make([]map[string]string, 0, len(c.srv.Roots))
		for _, r := range c.srv.Roots {
			roots = append(roots, map[string]string{"uri": "file://" + r, "name": r})
		}
		reply.Result = mustJSON(map[string]any{"roots": roots})
	case "elicitation/create":
		if c.opt.OnElicit == nil {
			reply.Error = &RPCError{Code: -32601, Message: "gnosh does not answer " + m.Method + " here: nobody is there to ask"}
			break
		}
		reply = c.elicited(ctx, m)
	default:
		reply.Error = &RPCError{Code: -32601, Message: "gnosh does not answer " + m.Method}
	}
	if ctx.Err() != nil {
		// Taken back, or the connection ended: nothing is sent.
		return
	}
	_ = c.send(reply)
}

// notified takes a notification from the server.
func (c *Client) notified(m message) {
	switch m.Method {
	case "notifications/cancelled":
		// The server takes back a request it made: it is answered no more.
		var p struct {
			RequestID json.RawMessage `json:"requestId"`
		}
		if json.Unmarshal(m.Params, &p) != nil || len(p.RequestID) == 0 {
			return
		}
		c.mu.Lock()
		s := c.serving[idKey(p.RequestID)]
		c.mu.Unlock()
		if s != nil {
			s.cancel()
		}
	case "notifications/tools/list_changed":
		c.mu.Lock()
		c.changed = true
		c.mu.Unlock()
		if c.opt.OnToolsChanged != nil {
			c.toolsChanged()
		}
	case "notifications/claude/channel":
		if c.opt.OnChannel == nil || !c.channel.Load() {
			// (Only a server that declared itself a channel is heard.)
			return
		}
		var p struct {
			Content string         `json:"content"`
			Meta    map[string]any `json:"meta"`
		}
		if json.Unmarshal(m.Params, &p) != nil {
			return
		}
		meta := map[string]string{}
		for k, v := range p.Meta {
			if s, ok := v.(string); ok && metaKey.MatchString(k) {
				meta[k] = s
			}
		}
		c.opt.OnChannel(p.Content, meta)
	case "notifications/claude/channel/permission":
		if c.opt.OnChannelVerdict == nil || !c.relay.Load() {
			return
		}
		var p struct {
			RequestID string `json:"request_id"`
			Behavior  string `json:"behavior"`
		}
		if json.Unmarshal(m.Params, &p) != nil || !requestID.MatchString(p.RequestID) || (p.Behavior != "allow" && p.Behavior != "deny") {
			return
		}
		c.opt.OnChannelVerdict(p.RequestID, p.Behavior)
	}
}

// requestID is what the id of a permission question relayed to a channel
// is: five lowercase letters, without l (Claude Code's alphabet, which a
// phone's keyboard cannot mistake for a 1 or an I).
var requestID = regexp.MustCompile(`^[a-km-z]{5}$`)

// RelayPermission puts a permission question to a server that declared
// itself a channel that relays them (Info.ChannelPermission), Claude
// Code's notifications/claude/channel/permission_request: the request's
// id, the tool's name, what the call does, and its input as display text.
// The server answers, if it does, with a verdict (Options.OnChannelVerdict).
func (c *Client) RelayPermission(id, tool, description, preview string) error {
	if !c.relay.Load() {
		return errors.New("mcp: the server takes no permission questions")
	}
	return c.notify("notifications/claude/channel/permission_request", map[string]string{
		"request_id": id, "tool_name": tool, "description": description, "input_preview": preview,
	})
}

// metaKey is what a key of a channel event's meta may be: letters, digits
// and underscores, as an attribute of the tag the event is shown in.
var metaKey = regexp.MustCompile(`^[A-Za-z0-9_]+$`)

func mustJSON(v any) json.RawMessage {
	if v == nil {
		return nil
	}
	data, err := json.Marshal(v)
	if err != nil {
		return nil
	}
	return data
}

// capped keeps the last max bytes written to it.
type capped struct {
	mu  sync.Mutex
	max int
	buf []byte
}

func (c *capped) Write(p []byte) (int, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.buf = append(c.buf, p...)
	if len(c.buf) > c.max {
		c.buf = append([]byte(nil), c.buf[len(c.buf)-c.max:]...)
	}
	return len(p), nil
}

func (c *capped) String() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return string(c.buf)
}
