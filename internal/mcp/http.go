package mcp

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"strings"
	"sync"
	"time"
)

// httpTransport is a server reached at a URL, by MCP's Streamable HTTP
// transport: each message the client sends is a POST of its own. The
// answer comes back as one JSON message (or a list of them), or as a
// stream of server-sent events, each a message, which may hold the
// server's own requests before the answer. The session id the server
// gives with its answer to the handshake goes with every later request,
// and the session is ended with a DELETE when the client closes.
//
// The client opens no stream of its own with a GET, which the protocol
// leaves to it: a server's notifications between requests (that its tool
// list changed, say) are not heard.
type httpTransport struct {
	c       *Client
	url     string
	headers map[string]string
	client  *http.Client
	// auth gives the requests the person's sign-in, when the server's
	// Authorization header is not configured (auth.go).
	auth *Authorizer
	// base ends with the connection: every request is made in it too.
	base context.Context
	end  context.CancelFunc

	mu      sync.Mutex
	session string
	version string
}

// maxHTTPMessage is the most of one answer that is read.
const maxHTTPMessage = 16 << 20

// StartHTTP connects to a server at a URL and makes the handshake.
// headers go with every request.
func StartHTTP(ctx context.Context, name, url string, headers map[string]string, opt Options) (*Client, error) {
	opt = opt.defaults()
	c := &Client{srv: Server{Name: name}, opt: opt, pending: map[string]chan message{}}
	t := &httpTransport{c: c, url: url, headers: headers}
	if opt.Auth != nil && !hasHeader(headers, "Authorization") {
		t.auth = opt.Auth
	}
	t.base, t.end = context.WithCancel(context.Background())
	t.client = &http.Client{CheckRedirect: func(req *http.Request, via []*http.Request) error {
		// A redirect elsewhere would take the configured headers, which
		// may hold a key, to a host nobody named.
		if req.URL.Host != via[0].URL.Host || req.URL.Scheme != via[0].URL.Scheme {
			return fmt.Errorf("the server sent the request on to %s://%s, which is not where it is configured", req.URL.Scheme, req.URL.Host)
		}
		if len(via) >= 5 {
			return errors.New("the server redirected too many times")
		}
		return nil
	}}
	c.t = t
	return c, c.handshake(ctx)
}

func (t *httpTransport) stderr() string { return "" }

// setVersion notes the protocol version the server answered the handshake
// with, which goes with every later request.
func (t *httpTransport) setVersion(v string) {
	t.mu.Lock()
	t.version = v
	t.mu.Unlock()
}

func (t *httpTransport) send(ctx context.Context, m message) error {
	data, err := json.Marshal(m)
	if err != nil {
		return err
	}
	for renewed := false; ; renewed = true {
		resp, done, token, err := t.post(ctx, data)
		if err != nil {
			return err
		}
		if (resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden) && !hasHeader(t.headers, "Authorization") {
			// The server wants a sign-in, or a better one: a refused token
			// (401) is renewed once, and otherwise the person signs in
			// (/mcp). A 403 (a scope missing, a firewall) renews and
			// forgets nothing. With an Authorization header configured, a
			// refusal is the configured credential's, and a plain failure.
			www := resp.Header.Get("WWW-Authenticate")
			resp.Body.Close()
			done()
			if resp.StatusCode == http.StatusUnauthorized && !renewed && token != "" && t.auth != nil && t.auth.renew(ctx, token) {
				continue
			}
			return &NeedsAuth{Status: resp.StatusCode, WWWAuthenticate: www}
		}
		return t.answered(m, resp, done)
	}
}

// post makes one POST of a message: the answer, how to end the request
// once its answer is read, and the token it carried.
func (t *httpTransport) post(ctx context.Context, data []byte) (*http.Response, func(), string, error) {
	// The request ends with ctx, or with the connection.
	rctx, cancel := context.WithCancel(ctx)
	unhook := context.AfterFunc(t.base, cancel)
	done := func() {
		unhook()
		cancel()
	}
	req, err := http.NewRequestWithContext(rctx, http.MethodPost, t.url, bytes.NewReader(data))
	if err != nil {
		done()
		return nil, nil, "", err
	}
	token := t.header(req)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	resp, err := t.client.Do(req)
	if err != nil {
		done()
		if t.base.Err() != nil {
			return nil, nil, "", ErrClosed
		}
		return nil, nil, "", fmt.Errorf("%w: %w", ErrUnreachable, err)
	}
	return resp, done, token, nil
}

// answered takes the server's answer to the message m. A request's answer
// comes in it, as JSON or in a stream of events: the client opens no
// stream of its own where a later one could come. So a request answered
// with 202, with JSON that holds no answer to it, or with a stream that
// ends before its answer, fails at once, as a server that could not be
// reached does: whether the request did anything is not known.
func (t *httpTransport) answered(m message, resp *http.Response, done func()) error {
	if id := resp.Header.Get("Mcp-Session-Id"); id != "" && m.Method == "initialize" {
		t.mu.Lock()
		t.session = id
		t.mu.Unlock()
	}
	request := m.Method != "" && len(m.ID) > 0
	switch {
	case resp.StatusCode == http.StatusAccepted:
		resp.Body.Close()
		done()
		if request {
			return fmt.Errorf("%w: it took the request (202 Accepted) and sent no answer to it", ErrUnreachable)
		}
		return nil
	case resp.StatusCode == http.StatusNotFound && t.hasSession():
		resp.Body.Close()
		done()
		return fmt.Errorf("%w (it answered 404 Not Found); reconnect to start another", ErrSessionEnded)
	case resp.StatusCode < 200 || resp.StatusCode > 299:
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 2000))
		resp.Body.Close()
		done()
		if text := strings.TrimSpace(string(body)); text != "" {
			return fmt.Errorf("mcp: the server answered %s: %s", resp.Status, text)
		}
		return fmt.Errorf("mcp: the server answered %s", resp.Status)
	}
	media, _, _ := mime.ParseMediaType(resp.Header.Get("Content-Type"))
	if media == "text/event-stream" {
		// The stream may stay open as long as the call takes: it is read
		// beside the request, which waits for its answer as it would over
		// stdio.
		go func() {
			defer done()
			defer resp.Body.Close()
			t.events(resp.Body)
			if request {
				// (A no-op when the answer came: the request no longer
				// waits.)
				t.c.fail(m.ID, fmt.Errorf("%w: the stream its answer was to come in ended first", ErrUnreachable))
			}
		}()
		return nil
	}
	defer done()
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxHTTPMessage))
	if err != nil {
		return fmt.Errorf("mcp: the server's answer could not be read: %w", err)
	}
	answered, err := t.take(body, m.ID)
	if err == nil && request && !answered {
		err = fmt.Errorf("%w: its answer held no answer to the request", ErrUnreachable)
	}
	return err
}

// header puts the configured headers, the session id and the protocol
// version on a request, and the sign-in's token when there is one, which
// it returns.
func (t *httpTransport) header(req *http.Request) (token string) {
	for k, v := range t.headers {
		req.Header.Set(k, v)
	}
	if t.auth != nil {
		if token = t.auth.token(); token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		}
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.session != "" {
		req.Header.Set("Mcp-Session-Id", t.session)
	}
	if t.version != "" {
		req.Header.Set("MCP-Protocol-Version", t.version)
	}
	return token
}

func (t *httpTransport) hasSession() bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.session != ""
}

// take dispatches what a JSON answer holds: one message, or a list. It
// reports whether the answer to the request with the id want was among
// them.
func (t *httpTransport) take(body []byte, want json.RawMessage) (bool, error) {
	body = bytes.TrimSpace(body)
	if len(body) == 0 {
		return false, nil
	}
	var list []message
	if body[0] == '[' {
		if err := json.Unmarshal(body, &list); err != nil {
			return false, fmt.Errorf("mcp: the server's answer is not JSON-RPC: %w", err)
		}
	} else {
		var m message
		if err := json.Unmarshal(body, &m); err != nil {
			return false, fmt.Errorf("mcp: the server's answer is not JSON-RPC: %w", err)
		}
		list = append(list, m)
	}
	got := false
	for _, m := range list {
		if len(want) > 0 && m.Method == "" && strings.Trim(string(m.ID), `"`) == strings.Trim(string(want), `"`) {
			got = true
		}
		t.c.dispatch(m)
	}
	return got, nil
}

// events reads a stream of server-sent events, each event's data a
// message. An event larger than any answer may be ends the stream: what
// waited on it fails.
func (t *httpTransport) events(r io.Reader) {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 64<<10), maxHTTPMessage)
	var data strings.Builder
	flush := func() {
		if data.Len() > 0 {
			_, _ = t.take([]byte(data.String()), nil)
			data.Reset()
		}
	}
	for sc.Scan() {
		line := sc.Text()
		switch {
		case line == "":
			flush()
		case strings.HasPrefix(line, "data:"):
			if data.Len()+len(line) > maxHTTPMessage {
				return
			}
			if data.Len() > 0 {
				data.WriteByte('\n')
			}
			data.WriteString(strings.TrimPrefix(strings.TrimPrefix(line, "data:"), " "))
		}
		// (event:, id: and retry: lines, and comments, carry nothing the
		// client needs.)
	}
	flush()
}

// close ends the session, if the server gave one, with a DELETE, and every
// request still made.
func (t *httpTransport) close() error {
	t.end()
	t.mu.Lock()
	session := t.session
	t.mu.Unlock()
	if session != "" {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if req, err := http.NewRequestWithContext(ctx, http.MethodDelete, t.url, nil); err == nil {
			t.header(req)
			if resp, err := t.client.Do(req); err == nil {
				resp.Body.Close()
			}
		}
	}
	t.client.CloseIdleConnections()
	return nil
}
