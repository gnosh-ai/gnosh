package mcp

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"
)

// TestMain runs the test binary as a fake MCP server when it is started as
// one: GNOSH_MCP_FAKE says how it behaves.
func TestMain(m *testing.M) {
	if mode := os.Getenv("GNOSH_MCP_FAKE"); mode != "" {
		fakeServer(mode)
		os.Exit(0)
	}
	os.Exit(m.Run())
}

// fakeServer is a small MCP server over stdio.
func fakeServer(mode string) {
	fmt.Fprintln(os.Stderr, "fake server starting")
	if mode == "die" {
		fmt.Fprintln(os.Stderr, "it cannot find its config")
		os.Exit(3)
	}
	if mode == "stubborn" {
		signal.Ignore(syscall.SIGINT)
	}
	out := bufio.NewWriter(os.Stdout)
	write := func(v any) {
		data, _ := json.Marshal(v)
		out.Write(append(data, '\n'))
		out.Flush()
	}
	fmt.Fprintln(out, "this line is not JSON")
	out.Flush()
	in := bufio.NewScanner(os.Stdin)
	in.Buffer(make([]byte, 1<<20), 1<<20)
	pendingRoots := json.RawMessage(nil)
	for in.Scan() {
		var m struct {
			ID     json.RawMessage `json:"id"`
			Method string          `json:"method"`
			Params json.RawMessage `json:"params"`
			Result json.RawMessage `json:"result"`
		}
		if json.Unmarshal(in.Bytes(), &m) != nil {
			continue
		}
		reply := func(result any) { write(map[string]any{"jsonrpc": "2.0", "id": m.ID, "result": result}) }
		switch m.Method {
		case "initialize":
			reply(map[string]any{"protocolVersion": "2025-03-26", "serverInfo": map[string]any{"name": "fake", "version": "1.0"},
				"capabilities": map[string]any{"tools": map[string]any{}, "resources": map[string]any{}, "prompts": map[string]any{}}, "instructions": "Use echo to echo."})
		case "prompts/list":
			reply(map[string]any{"prompts": []map[string]any{{"name": "review", "description": "Reviews a file.",
				"arguments": []map[string]any{{"name": "file", "required": true}, {"name": "focus"}}}}})
		case "prompts/get":
			var p struct {
				Name      string            `json:"name"`
				Arguments map[string]string `json:"arguments"`
			}
			json.Unmarshal(m.Params, &p)
			reply(map[string]any{"messages": []map[string]any{{"role": "user", "content": map[string]any{"type": "text",
				"text": "Review " + p.Arguments["file"] + " for " + p.Arguments["focus"] + "."}}}})
		case "resources/list":
			var p struct {
				Cursor string `json:"cursor"`
			}
			json.Unmarshal(m.Params, &p)
			if p.Cursor == "" {
				reply(map[string]any{"resources": []map[string]any{{"uri": "file:///notes.md", "name": "notes", "mimeType": "text/markdown"}}, "nextCursor": "r2"})
			} else {
				reply(map[string]any{"resources": []map[string]any{{"uri": "db://users", "name": "users", "description": "The users table."}}})
			}
		case "resources/read":
			var p struct {
				URI string `json:"uri"`
			}
			json.Unmarshal(m.Params, &p)
			if p.URI != "file:///notes.md" {
				write(map[string]any{"jsonrpc": "2.0", "id": m.ID, "error": map[string]any{"code": -32002, "message": "no resource " + p.URI}})
				break
			}
			reply(map[string]any{"contents": []map[string]any{{"uri": p.URI, "mimeType": "text/markdown", "text": "# Notes"}, {"uri": p.URI, "blob": "AAEC", "mimeType": "image/png"}}})
		case "tools/list":
			var p struct {
				Cursor string `json:"cursor"`
			}
			json.Unmarshal(m.Params, &p)
			if p.Cursor == "" {
				reply(map[string]any{"tools": []map[string]any{
					{"name": "echo", "description": "Echoes text.", "inputSchema": map[string]any{"type": "object"}},
					{"name": "fail", "description": "Fails.", "annotations": map[string]any{"readOnlyHint": true}},
				}, "nextCursor": "page2"})
			} else {
				reply(map[string]any{"tools": []map[string]any{{"name": "roots", "description": "Asks for the roots."}}})
			}
		case "tools/call":
			var p struct {
				Name      string          `json:"name"`
				Arguments json.RawMessage `json:"arguments"`
			}
			json.Unmarshal(m.Params, &p)
			switch p.Name {
			case "echo":
				var a struct {
					Text string `json:"text"`
				}
				json.Unmarshal(p.Arguments, &a)
				reply(map[string]any{"content": []map[string]any{{"type": "text", "text": "echo: " + a.Text}}})
				write(map[string]any{"jsonrpc": "2.0", "method": "notifications/tools/list_changed"})
			case "fail":
				reply(map[string]any{"content": []map[string]any{{"type": "text", "text": "it went wrong"}}, "isError": true})
			case "slow":
				time.Sleep(3 * time.Second)
				reply(map[string]any{"content": []map[string]any{}})
			case "flood":
				// Two thousand notices that its tools changed, then the answer.
				for range 2000 {
					write(map[string]any{"jsonrpc": "2.0", "method": "notifications/tools/list_changed"})
				}
				reply(map[string]any{"content": []map[string]any{{"type": "text", "text": "flooded"}}})
			case "deaf":
				// It answers, and reads nothing more.
				reply(map[string]any{"content": []map[string]any{{"type": "text", "text": "deaf now"}}})
				time.Sleep(time.Hour)
			case "orphan":
				// It leaves a process of its own outside its group with its
				// output, and exits without an answer.
				cmd := exec.Command("sleep", "8")
				cmd.Stdout = os.Stdout
				cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
				_ = cmd.Start()
				os.Exit(0)
			case "roots":
				// The server asks the client, and answers the call when the
				// client has answered.
				pendingRoots = m.ID
				write(map[string]any{"jsonrpc": "2.0", "id": "srv-1", "method": "roots/list"})
			default:
				write(map[string]any{"jsonrpc": "2.0", "id": m.ID, "error": map[string]any{"code": -32602, "message": "no tool " + p.Name}})
			}
		case "":
			// The client's answer to the server's own request.
			if pendingRoots != nil {
				write(map[string]any{"jsonrpc": "2.0", "id": pendingRoots, "result": map[string]any{
					"content": []map[string]any{{"type": "text", "text": string(m.Result)}}}})
				pendingRoots = nil
			}
		}
	}
	if mode == "stubborn" {
		// Its input is closed and it stays: only a signal ends it.
		time.Sleep(time.Hour)
	}
}

func fake(t *testing.T, mode string) Server {
	t.Helper()
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	return Server{Name: "fake", Command: exe, Environ: append(os.Environ(), "GNOSH_MCP_FAKE="+mode), Roots: []string{"/work/proj"}}
}

func TestClient(t *testing.T) {
	ctx := context.Background()
	c, err := Start(ctx, fake(t, "plain"), Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	if info := c.Info(); info.Name != "fake" || info.Version != "1.0" || info.ProtocolVersion != "2025-03-26" || info.Instructions != "Use echo to echo." {
		t.Errorf("the server's info: %+v", info)
	}
	// Standard error comes in beside the answers, and may come after them.
	for deadline := time.Now().Add(5 * time.Second); !strings.Contains(c.Stderr(), "fake server starting") && time.Now().Before(deadline); {
		time.Sleep(10 * time.Millisecond)
	}
	if !strings.Contains(c.Stderr(), "fake server starting") {
		t.Errorf("the server's standard error was not kept: %q", c.Stderr())
	}

	// Two pages of tools, as one list.
	tools, err := c.ListTools(ctx)
	if err != nil || len(tools) != 3 || tools[0].Name != "echo" || tools[2].Name != "roots" || !tools[1].Annotations.ReadOnly {
		t.Fatalf("the tools: %+v, %v", tools, err)
	}

	res, err := c.CallTool(ctx, "echo", json.RawMessage(`{"text":"hello"}`))
	if err != nil || res.IsError || len(res.Content) != 1 || res.Content[0].Text != "echo: hello" {
		t.Fatalf("echo: %+v, %v", res, err)
	}
	// The call was followed by a notice that the tool list changed.
	deadline := time.Now().Add(5 * time.Second)
	for !c.ToolsChanged() && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if !c.ToolsChanged() {
		t.Error("the server's notice that its tools changed was not taken")
	}
	if _, err := c.ListTools(ctx); err != nil || c.ToolsChanged() {
		t.Errorf("after listing them again the tools still read as changed (%v)", err)
	}

	res, err = c.CallTool(ctx, "fail", nil)
	if err != nil || !res.IsError || res.Content[0].Text != "it went wrong" {
		t.Errorf("a tool that fails: %+v, %v", res, err)
	}
	var rpc *RPCError
	if _, err := c.CallTool(ctx, "nothing", nil); !errors.As(err, &rpc) || rpc.Code != -32602 {
		t.Errorf("a tool that is not there: %v", err)
	}

	// The server asks the client for its roots in the middle of a call.
	res, err = c.CallTool(ctx, "roots", nil)
	if err != nil || len(res.Content) != 1 || !strings.Contains(res.Content[0].Text, "file:///work/proj") {
		t.Errorf("the roots: %+v, %v", res, err)
	}

	// A call that is given up on: the context ends, and the server is
	// still there for the next one.
	short, cancel := context.WithTimeout(ctx, 200*time.Millisecond)
	_, err = c.CallTool(short, "slow", nil)
	cancel()
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("a call past its deadline: %v", err)
	}
	if res, err := c.CallTool(ctx, "echo", json.RawMessage(`{"text":"again"}`)); err != nil || res.Content[0].Text != "echo: again" {
		t.Errorf("a call after one was given up on: %+v, %v", res, err)
	}

	start := time.Now()
	if err := c.Close(); err != nil {
		t.Errorf("Close: %v", err)
	}
	if took := time.Since(start); took > 2*time.Second {
		t.Errorf("a server that stops at the end of its input took %v to stop", took)
	}
	if _, err := c.ListTools(ctx); !errors.Is(err, ErrClosed) {
		t.Errorf("a request after Close: %v", err)
	}
}

func TestStartFailures(t *testing.T) {
	ctx := context.Background()
	if _, err := Start(ctx, Server{Name: "none", Command: "/no/such/program"}, Options{}); err == nil {
		t.Error("a program that is not there started")
	}
	_, err := Start(ctx, fake(t, "die"), Options{StartTimeout: 5 * time.Second})
	if err == nil || !strings.Contains(err.Error(), "it cannot find its config") {
		t.Errorf("a server that dies at once: %v, want what it wrote on its standard error", err)
	}
}

// A flood of notices that the tools changed costs one listing at a time,
// not a goroutine each: OnToolsChanged runs on one goroutine, and once more
// after the notices that came while it ran (the MCP review of 2026-11-19).
func TestToolsChangedFloodIsCoalesced(t *testing.T) {
	var mu sync.Mutex
	calls, running, most := 0, 0, 0
	opt := Options{OnToolsChanged: func() {
		mu.Lock()
		calls++
		running++
		most = max(most, running)
		mu.Unlock()
		time.Sleep(20 * time.Millisecond)
		mu.Lock()
		running--
		mu.Unlock()
	}}
	ctx := context.Background()
	c, err := Start(ctx, fake(t, "ok"), opt)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	if res, err := c.CallTool(ctx, "flood", nil); err != nil || res.Content[0].Text != "flooded" {
		t.Fatalf("flood: %+v, %v", res, err)
	}
	time.Sleep(300 * time.Millisecond)
	mu.Lock()
	defer mu.Unlock()
	if calls == 0 || calls > 100 || most != 1 {
		t.Errorf("2000 notices ran OnToolsChanged %d times, at most %d at once; want a few, one at a time", calls, most)
	}
}

// A server that stops reading its input does not hold a call, and every
// write after it, for ever: the write gives up with the call (the MCP
// review of 2026-11-19; a call larger than the pipe waited for ever).
func TestServerThatStopsReading(t *testing.T) {
	ctx := context.Background()
	c, err := Start(ctx, fake(t, "ok"), Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	if res, err := c.CallTool(ctx, "deaf", nil); err != nil || res.Content[0].Text != "deaf now" {
		t.Fatalf("deaf: %+v, %v", res, err)
	}
	short, cancel := context.WithTimeout(ctx, time.Second)
	defer cancel()
	start := time.Now()
	_, err = c.CallTool(short, "echo", json.RawMessage(`{"text":"`+strings.Repeat("x", 300<<10)+`"}`))
	if took := time.Since(start); err == nil || took > 4*time.Second {
		t.Errorf("a call to a server that reads no more: %v after %v", err, took.Round(time.Millisecond))
	}
}

// A server that exits while a process it left outside its group still
// holds its output: the call that waits fails soon after, and Close ends
// at once, not when that process lets go (the MCP review of 2026-11-19).
func TestServerGoneWhileItsOutputStaysOpen(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 12*time.Second)
	defer cancel()
	c, err := Start(ctx, fake(t, "ok"), Options{})
	if err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	if _, err := c.CallTool(ctx, "orphan", nil); err == nil || time.Since(start) > 6*time.Second {
		t.Errorf("a call to a server that exited: %v after %v", err, time.Since(start).Round(time.Millisecond))
	}
	start = time.Now()
	c.Close()
	if took := time.Since(start); took > 4*time.Second {
		t.Errorf("Close took %v", took.Round(time.Millisecond))
	}
}

// A server that ignores the end of its input and SIGINT is ended by
// SIGTERM, within half a second.
func TestCloseStopsAStubbornServer(t *testing.T) {
	c, err := Start(context.Background(), fake(t, "stubborn"), Options{})
	if err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	if err := c.Close(); err != nil {
		t.Errorf("Close: %v", err)
	}
	if took := time.Since(start); took > 1500*time.Millisecond {
		t.Errorf("Close took %v", took)
	}
}

// A server that offers resources says so in the handshake; its resources
// are listed page by page and read by URI.
func TestResources(t *testing.T) {
	c, err := Start(context.Background(), fake(t, "ok"), Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	if !c.Info().Resources {
		t.Errorf("the server's resources were not noted: %+v", c.Info())
	}
	list, err := c.ListResources(context.Background())
	if err != nil || len(list) != 2 || list[0].URI != "file:///notes.md" || list[1].Description != "The users table." {
		t.Fatalf("ListResources = %+v, %v", list, err)
	}
	parts, err := c.ReadResource(context.Background(), "file:///notes.md")
	if err != nil || len(parts) != 2 || parts[0].Text != "# Notes" || parts[1].Blob != "AAEC" {
		t.Fatalf("ReadResource = %+v, %v", parts, err)
	}
	if _, err := c.ReadResource(context.Background(), "nowhere://x"); err == nil {
		t.Errorf("a resource that is not there was read")
	}
}

// A server that offers prompts says so in the handshake; they are listed,
// and one is got with its arguments filled in.
func TestPrompts(t *testing.T) {
	c, err := Start(context.Background(), fake(t, "ok"), Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	if !c.Info().Prompts {
		t.Errorf("the server's prompts were not noted: %+v", c.Info())
	}
	list, err := c.ListPrompts(context.Background())
	if err != nil || len(list) != 1 || list[0].Name != "review" || len(list[0].Arguments) != 2 || !list[0].Arguments[0].Required {
		t.Fatalf("ListPrompts = %+v, %v", list, err)
	}
	msgs, err := c.GetPrompt(context.Background(), "review", map[string]string{"file": "main.go", "focus": "errors"})
	if err != nil || len(msgs) != 1 || msgs[0].Role != "user" || msgs[0].Content.Text != "Review main.go for errors." {
		t.Fatalf("GetPrompt = %+v, %v", msgs, err)
	}
}
