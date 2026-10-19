package sessionweb

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/charmbracelet/crush/internal/message"
	"github.com/charmbracelet/crush/internal/session"
)

type memStore struct {
	sessions []session.Session
	messages map[string][]message.Message
	next     int
}

func (m *memStore) ListSessions(context.Context) ([]session.Session, error) { return m.sessions, nil }
func (m *memStore) ListMessages(_ context.Context, id string) ([]message.Message, error) {
	return m.messages[id], nil
}
func (m *memStore) GetMessage(_ context.Context, id string) (message.Message, error) {
	for _, msgs := range m.messages {
		for _, msg := range msgs {
			if msg.ID == id {
				return msg, nil
			}
		}
	}
	return message.Message{}, ErrNotFound
}
func (m *memStore) UpdateMessage(_ context.Context, msg message.Message) error {
	msgs := m.messages[msg.SessionID]
	for i := range msgs {
		if msgs[i].ID == msg.ID {
			msgs[i] = msg
			return nil
		}
	}
	return ErrNotFound
}
func (m *memStore) DeleteMessage(_ context.Context, id string) error {
	for sid, msgs := range m.messages {
		for i := range msgs {
			if msgs[i].ID == id {
				m.messages[sid] = append(msgs[:i], msgs[i+1:]...)
				return nil
			}
		}
	}
	return ErrNotFound
}
func (m *memStore) CreateMessage(_ context.Context, sid string, p message.CreateMessageParams) (message.Message, error) {
	m.next++
	msg := message.Message{ID: fmt.Sprintf("m%d", m.next), SessionID: sid, Role: p.Role, Parts: p.Parts}
	m.messages[sid] = append(m.messages[sid], msg)
	return msg, nil
}

func newTestServer(t *testing.T) (*httptest.Server, *memStore) {
	t.Helper()
	store := &memStore{
		sessions: []session.Session{{ID: "s1", Title: "first", MessageCount: 2}},
		messages: map[string][]message.Message{"s1": {
			{ID: "u1", SessionID: "s1", Role: message.User, Parts: []message.ContentPart{message.TextContent{Text: "say hi"}}},
			{ID: "a1", SessionID: "s1", Role: message.Assistant, Parts: []message.ContentPart{message.ReasoningContent{Thinking: "short"}, message.TextContent{Text: "hi"}, message.Finish{Reason: message.FinishReasonEndTurn}}},
		}},
	}
	srv := httptest.NewServer(New(store, func(context.Context) (Context, error) {
		return Context{Template: "You are a test.", TemplateSource: "built-in"}, nil
	}))
	t.Cleanup(srv.Close)
	return srv, store
}

func do(t *testing.T, method, url string, body any) (*http.Response, []byte) {
	t.Helper()
	var buf bytes.Buffer
	if body != nil {
		if err := json.NewEncoder(&buf).Encode(body); err != nil {
			t.Fatal(err)
		}
	}
	req, err := http.NewRequest(method, url, &buf)
	if err != nil {
		t.Fatal(err)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var out bytes.Buffer
	_, _ = out.ReadFrom(resp.Body)
	return resp, out.Bytes()
}

func TestSessionWeb(t *testing.T) {
	srv, store := newTestServer(t)

	resp, body := do(t, "GET", srv.URL+"/", nil)
	if resp.StatusCode != 200 || !strings.Contains(string(body), "<title>Crush sessions</title>") {
		t.Fatalf("page: %d %q", resp.StatusCode, body[:40])
	}

	resp, body = do(t, "GET", srv.URL+"/api/sessions", nil)
	if resp.StatusCode != 200 || !strings.Contains(string(body), `"title":"first"`) {
		t.Fatalf("sessions: %d %s", resp.StatusCode, body)
	}

	resp, body = do(t, "GET", srv.URL+"/api/sessions/s1/messages", nil)
	var msgs []messageJSON
	if err := json.Unmarshal(body, &msgs); err != nil || len(msgs) != 2 {
		t.Fatalf("messages: %d %s (%v)", resp.StatusCode, body, err)
	}
	if !strings.Contains(string(msgs[1].Parts), `"thinking":"short"`) {
		t.Fatalf("parts not in the stored shape: %s", msgs[1].Parts)
	}

	// Edit the assistant's text and keep the other parts.
	parts := strings.Replace(string(msgs[1].Parts), `"text":"hi"`, `"text":"hello there"`, 1)
	resp, body = do(t, "PUT", srv.URL+"/api/messages/a1", map[string]json.RawMessage{"parts": json.RawMessage(parts)})
	if resp.StatusCode != 204 {
		t.Fatalf("update: %d %s", resp.StatusCode, body)
	}
	got, _ := store.GetMessage(context.Background(), "a1")
	if len(got.Parts) != 3 || got.Parts[1].(message.TextContent).Text != "hello there" {
		t.Fatalf("edit did not land: %+v", got.Parts)
	}

	resp, body = do(t, "PUT", srv.URL+"/api/messages/a1", map[string]string{"parts": "[]"})
	if resp.StatusCode != 400 {
		t.Fatalf("empty parts must be refused: %d %s", resp.StatusCode, body)
	}

	resp, body = do(t, "POST", srv.URL+"/api/sessions/s1/messages", map[string]string{"role": "user", "text": "and then?"})
	if resp.StatusCode != 201 {
		t.Fatalf("create: %d %s", resp.StatusCode, body)
	}
	if n := len(store.messages["s1"]); n != 3 {
		t.Fatalf("after create: %d messages", n)
	}
	resp, body = do(t, "POST", srv.URL+"/api/sessions/s1/messages", map[string]string{"role": "system", "text": "x"})
	if resp.StatusCode != 400 {
		t.Fatalf("system role must be refused: %d %s", resp.StatusCode, body)
	}

	resp, body = do(t, "DELETE", srv.URL+"/api/messages/u1", nil)
	if resp.StatusCode != 204 || len(store.messages["s1"]) != 2 {
		t.Fatalf("delete: %d %s, left %d", resp.StatusCode, body, len(store.messages["s1"]))
	}

	resp, body = do(t, "GET", srv.URL+"/api/context", nil)
	if resp.StatusCode != 200 || !strings.Contains(string(body), "You are a test.") {
		t.Fatalf("context: %d %s", resp.StatusCode, body)
	}
}
