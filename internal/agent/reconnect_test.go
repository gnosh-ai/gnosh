package agent

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"charm.land/fantasy"
	"charm.land/fantasy/providers/openaicompat"
	"github.com/stretchr/testify/require"
)

// scriptedStreams answers successive Stream calls with successive scripts,
// repeating the last one once they run out.
type scriptedStreams struct {
	fakeLanguageModel
	mu      sync.Mutex
	scripts [][]fantasy.StreamPart
	calls   int
}

func (s *scriptedStreams) Stream(context.Context, fantasy.Call) (fantasy.StreamResponse, error) {
	s.mu.Lock()
	parts := s.scripts[min(s.calls, len(s.scripts)-1)]
	s.calls++
	s.mu.Unlock()
	return func(yield func(fantasy.StreamPart) bool) {
		for _, p := range parts {
			if !yield(p) {
				return
			}
		}
	}, nil
}

func (s *scriptedStreams) callCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.calls
}

// fakeTime is a clock that sleep advances, so a test can run past the
// reconnect window without waiting for it.
type fakeTime struct {
	now    time.Time
	sleeps int
}

func (f *fakeTime) clock() time.Time { return f.now }

func (f *fakeTime) sleep(ctx context.Context, d time.Duration) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	f.sleeps++
	f.now = f.now.Add(d)
	return nil
}

func testReconnecting(inner fantasy.LanguageModel, ft *fakeTime) reconnectingModel {
	return reconnectingModel{
		LanguageModel: inner,
		window:        reconnectWindow,
		every:         reconnectEvery,
		now:           ft.clock,
		sleep:         ft.sleep,
	}
}

func collect(t *testing.T, m fantasy.LanguageModel) []fantasy.StreamPart {
	t.Helper()
	stream, err := m.Stream(t.Context(), fantasy.Call{})
	require.NoError(t, err)
	var parts []fantasy.StreamPart
	for p := range stream {
		parts = append(parts, p)
	}
	return parts
}

var (
	loading = fantasy.StreamPart{Type: fantasy.StreamPartTypeError, Error: &fantasy.ProviderError{
		StatusCode: http.StatusServiceUnavailable, Title: "service unavailable", Message: "Loading model",
	}}
	droppedConn = fantasy.StreamPart{Type: fantasy.StreamPartTypeError, Error: &fantasy.ProviderError{
		Title: "stream transport error", Message: "unexpected EOF", TransientError: true, Cause: io.ErrUnexpectedEOF,
	}}
	refused = fantasy.StreamPart{Type: fantasy.StreamPartTypeError, Error: &fantasy.ProviderError{
		Cause: fmt.Errorf("dial tcp 127.0.0.1:8080: connect: %w", syscall.ECONNREFUSED),
	}}
	rejected = fantasy.StreamPart{Type: fantasy.StreamPartTypeError, Error: &fantasy.ProviderError{
		StatusCode: http.StatusBadRequest, Message: "bad request",
	}}
	answer = []fantasy.StreamPart{
		{Type: fantasy.StreamPartTypeTextStart, ID: "t"},
		{Type: fantasy.StreamPartTypeTextDelta, ID: "t", Delta: "hello"},
		{Type: fantasy.StreamPartTypeTextEnd, ID: "t"},
		{Type: fantasy.StreamPartTypeFinish, FinishReason: fantasy.FinishReasonStop},
	}
)

// The restart that cost a sub-agent an hour of work, replayed: the server
// drops the connection, then answers "Loading model", then is back. The
// consumer must see only the answer.
func TestReconnectRidesOutARestart(t *testing.T) {
	t.Parallel()

	inner := &scriptedStreams{scripts: [][]fantasy.StreamPart{
		{droppedConn}, {loading}, {refused}, answer,
	}}
	ft := &fakeTime{now: time.Unix(0, 0)}
	parts := collect(t, testReconnecting(inner, ft))

	require.Equal(t, answer, parts, "no error from the restart may reach the consumer")
	require.Equal(t, 4, inner.callCount())
	require.Equal(t, 3, ft.sleeps)
}

// Once output has flowed, an error is passed on: retrying here would show
// part of the response twice. fantasy's own step retry handles it.
func TestReconnectPassesThroughErrorsAfterOutput(t *testing.T) {
	t.Parallel()

	inner := &scriptedStreams{scripts: [][]fantasy.StreamPart{
		{answer[0], answer[1], loading},
	}}
	ft := &fakeTime{now: time.Unix(0, 0)}
	parts := collect(t, testReconnecting(inner, ft))

	require.Equal(t, []fantasy.StreamPart{answer[0], answer[1], loading}, parts)
	require.Equal(t, 1, inner.callCount())
	require.Zero(t, ft.sleeps)
}

// A server that is up and rejects the request is not restarting.
func TestReconnectPassesThroughRejections(t *testing.T) {
	t.Parallel()

	inner := &scriptedStreams{scripts: [][]fantasy.StreamPart{{rejected}}}
	ft := &fakeTime{now: time.Unix(0, 0)}
	parts := collect(t, testReconnecting(inner, ft))

	require.Equal(t, []fantasy.StreamPart{rejected}, parts)
	require.Equal(t, 1, inner.callCount())
}

// Past the window the server is more likely gone than restarting, and the
// error is shown rather than waited on.
func TestReconnectLetsTheErrorThroughAfterTheWindow(t *testing.T) {
	t.Parallel()

	inner := &scriptedStreams{scripts: [][]fantasy.StreamPart{{loading}}}
	ft := &fakeTime{now: time.Unix(0, 0)}
	parts := collect(t, testReconnecting(inner, ft))

	require.Equal(t, []fantasy.StreamPart{loading}, parts, "the final error must reach the consumer")
	wantAttempts := int(reconnectWindow/reconnectEvery) + 1
	require.Equal(t, wantAttempts, inner.callCount())
}

func TestReconnectStopsWhenCancelled(t *testing.T) {
	t.Parallel()

	inner := &scriptedStreams{scripts: [][]fantasy.StreamPart{{loading}}}
	ft := &fakeTime{now: time.Unix(0, 0)}
	m := testReconnecting(inner, ft)

	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	stream, err := m.Stream(ctx, fantasy.Call{})
	require.NoError(t, err)
	var parts []fantasy.StreamPart
	for p := range stream {
		parts = append(parts, p)
	}
	require.Len(t, parts, 1)
	require.ErrorIs(t, parts[0].Error, context.Canceled)
	require.Equal(t, 1, inner.callCount(), "no reconnect after the user cancelled")
}

// Warnings are produced before connecting; a reconnect must not repeat them.
func TestReconnectDoesNotRepeatWarnings(t *testing.T) {
	t.Parallel()

	warn := fantasy.StreamPart{Type: fantasy.StreamPartTypeWarnings, Warnings: []fantasy.CallWarning{{Message: "w"}}}
	inner := &scriptedStreams{scripts: [][]fantasy.StreamPart{
		{warn, loading},
		append([]fantasy.StreamPart{warn}, answer...),
	}}
	ft := &fakeTime{now: time.Unix(0, 0)}
	parts := collect(t, testReconnecting(inner, ft))

	require.Equal(t, append([]fantasy.StreamPart{warn}, answer...), parts)
}

func TestReconnectReturnsSetupErrorsAsBefore(t *testing.T) {
	t.Parallel()

	setupErr := errors.New("bad params")
	m := newReconnectingModel(&setupFailure{err: setupErr})
	stream, err := m.Stream(t.Context(), fantasy.Call{})
	require.ErrorIs(t, err, setupErr)
	require.Nil(t, stream)
}

type setupFailure struct {
	fakeLanguageModel
	err error
}

func (s *setupFailure) Stream(context.Context, fantasy.Call) (fantasy.StreamResponse, error) {
	return nil, s.err
}

func TestServerNotReady(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name string
		err  error
		want bool
	}{
		{"503 loading", loading.Error, true},
		{"502", &fantasy.ProviderError{StatusCode: http.StatusBadGateway}, true},
		{"504", &fantasy.ProviderError{StatusCode: http.StatusGatewayTimeout}, true},
		{"dropped connection", droppedConn.Error, true},
		{"connection refused", refused.Error, true},
		{"bare EOF", io.EOF, true},
		{"400", rejected.Error, false},
		{"401", &fantasy.ProviderError{StatusCode: http.StatusUnauthorized}, false},
		{"429 is fantasy's to pace", &fantasy.ProviderError{StatusCode: http.StatusTooManyRequests}, false},
		{"500", &fantasy.ProviderError{StatusCode: http.StatusInternalServerError}, false},
		{"cancelled", context.Canceled, false},
		{"deadline", context.DeadlineExceeded, false},
		{"other", errors.New("boom"), false},
		{"nil", nil, false},
	} {
		require.Equal(t, tc.want, serverNotReady(tc.err), tc.name)
	}
}

// End to end against the real OpenAI-compatible provider: a server that
// answers "Loading model" twice, as llama-server does while it restarts,
// then streams a reply. This checks the assumption the wrapper rests on —
// that the provider reports a server that is down as an error inside the
// stream — rather than a fake's idea of it.
func TestReconnectWithTheRealProvider(t *testing.T) {
	t.Parallel()

	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if hits.Add(1) <= 2 {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusServiceUnavailable)
			_, _ = io.WriteString(w, `{"error":{"code":503,"message":"Loading model","type":"unavailable_error"}}`)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		for _, chunk := range []string{
			`{"id":"c","object":"chat.completion.chunk","created":1,"model":"m","choices":[{"index":0,"delta":{"role":"assistant","content":"hello"},"finish_reason":null}]}`,
			`{"id":"c","object":"chat.completion.chunk","created":1,"model":"m","choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}`,
		} {
			_, _ = io.WriteString(w, "data: "+chunk+"\n\n")
		}
		_, _ = io.WriteString(w, "data: [DONE]\n\n")
	}))
	defer srv.Close()

	provider, err := openaicompat.New(openaicompat.WithBaseURL(srv.URL+"/v1"), openaicompat.WithAPIKey("x"))
	require.NoError(t, err)
	inner, err := provider.LanguageModel(t.Context(), "m")
	require.NoError(t, err)

	var sleeps atomic.Int32
	m := reconnectingModel{
		LanguageModel: inner,
		window:        time.Minute,
		every:         time.Millisecond,
		now:           time.Now,
		sleep: func(ctx context.Context, d time.Duration) error {
			sleeps.Add(1)
			return sleepCtx(ctx, d)
		},
	}
	stream, err := m.Stream(t.Context(), fantasy.Call{Prompt: fantasy.Prompt{fantasy.NewUserMessage("hi")}})
	require.NoError(t, err)

	var text strings.Builder
	for p := range stream {
		require.NotEqual(t, fantasy.StreamPartTypeError, p.Type, "no error may reach the consumer: %v", p.Error)
		text.WriteString(p.Delta)
	}
	require.Equal(t, "hello", text.String())
	require.Equal(t, int32(3), hits.Load(), "two refusals, then the reply")
	require.Positive(t, sleeps.Load(), "the wrapper, not the SDK, must have done the waiting")
}
