package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeHTTP is a small MCP server over Streamable HTTP. It answers the
// handshake with a session id; tools/list over a stream of events that
// first pings the client; tools/call with JSON, or with a list of
// messages when the tool is "batch". It notes every request's headers.
type fakeHTTP struct {
	mu       sync.Mutex
	seen     []http.Header
	methods  []string
	answers  []string // the client's answers to the server's requests
	deleted  bool
	gone     bool // the session has ended: requests with it get 404
	statusOf map[string]int
}

func (f *fakeHTTP) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	f.seen = append(f.seen, r.Header.Clone())
	gone := f.gone
	f.mu.Unlock()
	if r.Method == http.MethodDelete {
		f.mu.Lock()
		f.deleted = r.Header.Get("Mcp-Session-Id") == "s-42"
		f.mu.Unlock()
		w.WriteHeader(http.StatusNoContent)
		return
	}
	if gone && r.Header.Get("Mcp-Session-Id") != "" {
		http.NotFound(w, r)
		return
	}
	body, _ := io.ReadAll(r.Body)
	var m message
	_ = json.Unmarshal(body, &m)
	f.mu.Lock()
	f.methods = append(f.methods, m.Method)
	if m.Method == "" {
		f.answers = append(f.answers, string(body))
	}
	code := f.statusOf[m.Method]
	f.mu.Unlock()
	if code != 0 {
		http.Error(w, "nope", code)
		return
	}
	id := string(m.ID)
	switch m.Method {
	case "initialize":
		w.Header().Set("Mcp-Session-Id", "s-42")
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"jsonrpc":"2.0","id":%s,"result":{"protocolVersion":"2025-03-26","serverInfo":{"name":"far","version":"2"}}}`, id)
	case "tools/list":
		w.Header().Set("Content-Type", "text/event-stream")
		fl := w.(http.Flusher)
		fmt.Fprint(w, ": a comment\n\nevent: message\ndata: {\"jsonrpc\":\"2.0\",\"id\":\"srv-1\",\"method\":\"ping\"}\n\n")
		fl.Flush()
		// The answer comes in two data lines, which join with a newline.
		fmt.Fprintf(w, "id: 7\ndata: {\"jsonrpc\":\"2.0\",\"id\":%s,\n", id)
		fmt.Fprint(w, "data: \"result\":{\"tools\":[{\"name\":\"far_tool\",\"description\":\"Far away.\"}]}}\n\n")
		fl.Flush()
	case "tools/call":
		var p struct {
			Name string `json:"name"`
		}
		_ = json.Unmarshal(m.Params, &p)
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		if p.Name == "batch" {
			fmt.Fprintf(w, `[{"jsonrpc":"2.0","method":"notifications/tools/list_changed"},{"jsonrpc":"2.0","id":%s,"result":{"content":[{"type":"text","text":"in a list"}]}}]`, id)
			return
		}
		if p.Name == "slow" {
			select {
			case <-r.Context().Done():
			case <-time.After(10 * time.Second):
			}
			return
		}
		// Answers in which the answer never comes.
		switch p.Name {
		case "accepted":
			w.WriteHeader(http.StatusAccepted)
			return
		case "wrongid":
			fmt.Fprintf(w, `{"jsonrpc":"2.0","id":"not-%s","result":{"content":[]}}`, strings.Trim(id, `"`))
			return
		case "cutstream":
			w.Header().Set("Content-Type", "text/event-stream")
			fmt.Fprint(w, "data: {\"jsonrpc\":\"2.0\",\"method\":\"notifications/message\"}\n\n")
			return
		case "bigevent":
			w.Header().Set("Content-Type", "text/event-stream")
			line := "data: " + strings.Repeat("x", 1<<20) + "\n"
			for range 20 {
				if _, err := fmt.Fprint(w, line); err != nil {
					return
				}
			}
			<-r.Context().Done()
			return
		}
		fmt.Fprintf(w, `{"jsonrpc":"2.0","id":%s,"result":{"content":[{"type":"text","text":"called %s"}]}}`, id, p.Name)
	default:
		// Notifications and the client's answers.
		w.WriteHeader(http.StatusAccepted)
	}
}

func TestHTTPClient(t *testing.T) {
	f := &fakeHTTP{}
	srv := httptest.NewServer(f)
	defer srv.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	c, err := StartHTTP(ctx, "far", srv.URL+"/mcp", map[string]string{"Authorization": "Bearer k1"}, Options{ClientVersion: "t"})
	if err != nil {
		t.Fatal(err)
	}
	if info := c.Info(); info.Name != "far" || info.Version != "2" || info.ProtocolVersion != "2025-03-26" {
		t.Errorf("info: %+v", info)
	}
	tools, err := c.ListTools(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(tools) != 1 || tools[0].Name != "far_tool" {
		t.Errorf("tools: %+v", tools)
	}
	res, err := c.CallTool(ctx, "echo", nil)
	if err != nil || len(res.Content) != 1 || res.Content[0].Text != "called echo" {
		t.Errorf("call: %+v, %v", res, err)
	}
	// An answer in a list, after a notification.
	res, err = c.CallTool(ctx, "batch", json.RawMessage(`{}`))
	if err != nil || res.Content[0].Text != "in a list" || !c.ToolsChanged() {
		t.Errorf("batch: %+v, %v, changed %v", res, err, c.ToolsChanged())
	}
	// A call given up is given up, and the server told.
	short, stop := context.WithTimeout(ctx, 200*time.Millisecond)
	_, err = c.CallTool(short, "slow", nil)
	stop()
	if err != context.DeadlineExceeded {
		t.Errorf("a call given up: %v", err)
	}
	if err := c.Close(); err != nil {
		t.Error(err)
	}
	if _, err := c.CallTool(ctx, "echo", nil); err == nil {
		t.Error("a call after Close was made")
	}

	f.mu.Lock()
	defer f.mu.Unlock()
	if !f.deleted {
		t.Error("the session was not ended with a DELETE")
	}
	// The client answered the server's ping.
	if len(f.answers) != 1 || !strings.Contains(f.answers[0], `"id":"srv-1"`) || !strings.Contains(f.answers[0], `"result":{}`) {
		t.Errorf("the client's answers: %q", f.answers)
	}
	// (The client's answer to the ping, which has no method, goes when it
	// is ready, beside the rest.)
	sent := slices.DeleteFunc(slices.Clone(f.methods), func(m string) bool { return m == "" })
	if strings.Join(sent, " ") != "initialize notifications/initialized tools/list tools/call tools/call tools/call notifications/cancelled" {
		t.Errorf("what the server was sent: %q", f.methods)
	}
	for i, h := range f.seen {
		if h.Get("Authorization") != "Bearer k1" {
			t.Errorf("request %d had no configured header", i)
		}
		if i == 0 {
			if h.Get("Mcp-Session-Id") != "" || h.Get("Mcp-Protocol-Version") != "" {
				t.Errorf("the handshake carried %v", h)
			}
			if !strings.Contains(h.Get("Accept"), "text/event-stream") || !strings.Contains(h.Get("Accept"), "application/json") {
				t.Errorf("the handshake accepts %q", h.Get("Accept"))
			}
			continue
		}
		if h.Get("Mcp-Session-Id") != "s-42" {
			t.Errorf("request %d had session %q", i, h.Get("Mcp-Session-Id"))
		}
		// The version the server answered with, not the one asked for.
		if h.Get("Mcp-Protocol-Version") != "2025-03-26" {
			t.Errorf("request %d had version %q", i, h.Get("Mcp-Protocol-Version"))
		}
	}
}

// A request whose answer will not come fails at once, as a server that
// could not be reached does: answered with 202, with JSON that holds no
// answer to it, with a stream that ends before the answer, or with an
// event larger than any answer may be. Each waited for ever before, with
// no timeout set (the MCP review of 2026-11-19).
func TestHTTPAnswersThatNeverCome(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	srv := httptest.NewServer(&fakeHTTP{})
	defer srv.Close()
	c, err := StartHTTP(ctx, "x", srv.URL, nil, Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	for _, name := range []string{"accepted", "wrongid", "cutstream", "bigevent"} {
		callCtx, stop := context.WithTimeout(ctx, 10*time.Second)
		start := time.Now()
		_, err := c.CallTool(callCtx, name, nil)
		stop()
		if took := time.Since(start); !errors.Is(err, ErrUnreachable) || took > 5*time.Second {
			t.Errorf("%s: %v after %v, want it unreachable at once", name, err, took.Round(time.Millisecond))
		}
	}
	if res, err := c.CallTool(ctx, "fine", nil); err != nil || len(res.Content) != 1 || res.Content[0].Text != "called fine" {
		t.Errorf("a call after those: %+v, %v", res, err)
	}
}

func TestHTTPFailures(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	// A server that refuses the handshake wants a sign-in, unless the
	// configured Authorization header is what it refused: then it says why.
	f := &fakeHTTP{statusOf: map[string]int{"initialize": http.StatusUnauthorized}}
	srv := httptest.NewServer(f)
	defer srv.Close()
	if _, err := StartHTTP(ctx, "x", srv.URL, nil, Options{}); !errors.Is(err, ErrNeedsAuth) {
		t.Errorf("a refused handshake: %v", err)
	}
	if _, err := StartHTTP(ctx, "x", srv.URL, map[string]string{"Authorization": "Bearer k"}, Options{}); err == nil || errors.Is(err, ErrNeedsAuth) || !strings.Contains(err.Error(), "401 Unauthorized: nope") {
		t.Errorf("a refused handshake with a configured Authorization header: %v", err)
	}

	// Nobody there.
	dead := httptest.NewServer(http.NotFoundHandler())
	url := dead.URL
	dead.Close()
	if _, err := StartHTTP(ctx, "x", url, nil, Options{}); !errors.Is(err, ErrUnreachable) || !strings.Contains(err.Error(), "could not be reached") {
		t.Errorf("nobody there: %v", err)
	}

	// A session the server has ended.
	f = &fakeHTTP{}
	srv2 := httptest.NewServer(f)
	defer srv2.Close()
	c, err := StartHTTP(ctx, "x", srv2.URL, nil, Options{})
	if err != nil {
		t.Fatal(err)
	}
	f.mu.Lock()
	f.gone = true
	f.mu.Unlock()
	if _, err := c.ListTools(ctx); !errors.Is(err, ErrSessionEnded) || !strings.Contains(err.Error(), "ended this session") {
		t.Errorf("an ended session: %v", err)
	}
	c.Close()

	// A redirect to another host is not followed.
	other := httptest.NewServer(&fakeHTTP{})
	defer other.Close()
	away := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, other.URL, http.StatusTemporaryRedirect)
	}))
	defer away.Close()
	if _, err := StartHTTP(ctx, "x", away.URL, map[string]string{"X-Key": "secret"}, Options{}); err == nil || !strings.Contains(err.Error(), "not where it is configured") {
		t.Errorf("a redirect elsewhere: %v", err)
	}
}
