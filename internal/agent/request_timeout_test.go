package agent

import (
	"context"
	"errors"
	"runtime"
	"testing"
	"time"

	"charm.land/fantasy"
	"charm.land/x/vcr"
	"github.com/charmbracelet/crush/internal/message"
	"github.com/stretchr/testify/require"
)

// fakeLanguageModel is a [fantasy.LanguageModel] stub that records the
// context its methods were called with and can be configured with a custom
// stream body.
type fakeLanguageModel struct {
	generateCtx context.Context
	streamCtx   context.Context
	stream      func(yield func(fantasy.StreamPart) bool)
}

func (f *fakeLanguageModel) Generate(ctx context.Context, _ fantasy.Call) (*fantasy.Response, error) {
	f.generateCtx = ctx
	return &fantasy.Response{}, nil
}

func (f *fakeLanguageModel) Stream(ctx context.Context, _ fantasy.Call) (fantasy.StreamResponse, error) {
	f.streamCtx = ctx
	if f.stream == nil {
		return func(yield func(fantasy.StreamPart) bool) {
			yield(fantasy.StreamPart{})
		}, nil
	}
	return f.stream, nil
}

func (f *fakeLanguageModel) GenerateObject(context.Context, fantasy.ObjectCall) (*fantasy.ObjectResponse, error) {
	return &fantasy.ObjectResponse{}, nil
}

func (f *fakeLanguageModel) StreamObject(context.Context, fantasy.ObjectCall) (fantasy.ObjectStreamResponse, error) {
	return nil, nil
}

func (f *fakeLanguageModel) Provider() string { return "fake" }
func (f *fakeLanguageModel) Model() string    { return "fake-model" }

func TestNewRequestTimeoutModel_Disabled(t *testing.T) {
	t.Parallel()

	inner := &fakeLanguageModel{}
	require.Same(t, inner, newRequestTimeoutModel(inner, 0))
	require.Same(t, inner, newRequestTimeoutModel(inner, -time.Second))
}

func TestRequestTimeoutModel_GenerateDeadline(t *testing.T) {
	t.Parallel()

	inner := &fakeLanguageModel{}
	m := newRequestTimeoutModel(inner, 5*time.Minute)

	_, err := m.Generate(t.Context(), fantasy.Call{})
	require.NoError(t, err)

	_, ok := inner.generateCtx.Deadline()
	require.True(t, ok, "Generate should run under a deadline")
}

func TestRequestTimeoutModel_StreamDeadlineOutlivesCall(t *testing.T) {
	t.Parallel()

	inner := &fakeLanguageModel{}
	m := newRequestTimeoutModel(inner, 5*time.Minute)

	stream, err := m.Stream(t.Context(), fantasy.Call{})
	require.NoError(t, err)

	require.NoError(t, inner.streamCtx.Err(), "the idle timer must not fire while the stream is being consumed")

	for range stream {
	}

	require.ErrorIs(t, inner.streamCtx.Err(), context.Canceled, "the stream context should be released after the stream ends")
}

func TestRequestTimeoutModel_StreamAbortsWhenIdle(t *testing.T) {
	t.Parallel()

	inner := &fakeLanguageModel{}
	// A stream that outlives the idle window: it waits for the context to
	// be done and reports what it observed.
	streamObserved := make(chan error, 1)
	inner.stream = func(yield func(fantasy.StreamPart) bool) {
		<-inner.streamCtx.Done()
		streamObserved <- inner.streamCtx.Err()
	}
	m := newRequestTimeoutModel(inner, 10*time.Millisecond)
	stream, err := m.Stream(t.Context(), fantasy.Call{})
	require.NoError(t, err)

	done := make(chan struct{})
	go func() {
		defer close(done)
		for range stream {
		}
	}()

	select {
	case err := <-streamObserved:
		require.ErrorIs(t, err, context.Canceled)
	case <-time.After(5 * time.Second):
		t.Fatal("stream was not aborted by the idle timeout")
	}
	<-done
}

func TestRequestTimeoutModel_ActiveStreamSurvives(t *testing.T) {
	t.Parallel()

	inner := &fakeLanguageModel{}
	// A stream that keeps sending data: total runtime exceeds the timeout,
	// but every gap is shorter than the idle window, so it must finish.
	inner.stream = func(yield func(fantasy.StreamPart) bool) {
		for range 6 {
			time.Sleep(10 * time.Millisecond)
			if !yield(fantasy.StreamPart{}) {
				return
			}
		}
	}
	m := newRequestTimeoutModel(inner, 25*time.Millisecond)
	stream, err := m.Stream(t.Context(), fantasy.Call{})
	require.NoError(t, err)

	parts := 0
	for part := range stream {
		require.NoError(t, part.Error)
		parts++
	}
	require.Equal(t, 6, parts)
}

// blockingModel blocks until the context is done and then returns the
// context error, the way a hung provider request would.
type blockingModel struct {
	fakeLanguageModel
}

func (b *blockingModel) Generate(ctx context.Context, _ fantasy.Call) (*fantasy.Response, error) {
	<-ctx.Done()
	return nil, ctx.Err()
}

func TestRequestTimeoutModel_GenerateReportsTimeout(t *testing.T) {
	t.Parallel()

	m := newRequestTimeoutModel(&blockingModel{}, 10*time.Millisecond)

	_, err := m.Generate(t.Context(), fantasy.Call{})
	require.Error(t, err)

	var timeoutErr *requestTimeoutError
	require.ErrorAs(t, err, &timeoutErr)
	require.Equal(t, 10*time.Millisecond, timeoutErr.timeout)
	require.ErrorIs(t, err, context.DeadlineExceeded, "the deadline must stay detectable through the chain")
	require.Contains(t, err.Error(), "timed out after 10ms")
}

func TestRequestTimeoutModel_StreamReportsTimeout(t *testing.T) {
	t.Parallel()

	inner := &fakeLanguageModel{}
	// A provider stream that fails with the context error once the deadline
	// fires, mirroring how SDKs surface mid-stream aborts.
	inner.stream = func(yield func(fantasy.StreamPart) bool) {
		<-inner.streamCtx.Done()
		yield(fantasy.StreamPart{Error: inner.streamCtx.Err()})
	}
	m := newRequestTimeoutModel(inner, 10*time.Millisecond)
	stream, err := m.Stream(t.Context(), fantasy.Call{})
	require.NoError(t, err)

	var got error
	for part := range stream {
		if part.Error != nil {
			got = part.Error
		}
	}

	var timeoutErr *requestTimeoutError
	require.ErrorAs(t, got, &timeoutErr)
	require.ErrorIs(t, got, context.DeadlineExceeded)
}

func TestRequestTimeoutModel_ParentCancelPassesThrough(t *testing.T) {
	t.Parallel()

	m := newRequestTimeoutModel(&blockingModel{}, 5*time.Minute)

	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	go func() {
		time.Sleep(10 * time.Millisecond)
		cancel()
	}()

	_, err := m.Generate(ctx, fantasy.Call{})
	require.ErrorIs(t, err, context.Canceled)

	var timeoutErr *requestTimeoutError
	require.NotErrorAs(t, err, &timeoutErr, "user cancellation must not be reported as a timeout")
}

func TestRequestTimeoutErrorMessages(t *testing.T) {
	t.Parallel()

	err := &requestTimeoutError{timeout: time.Second}
	require.Equal(t, "LLM request timed out after 1s", err.Error())
	require.Contains(t, err.userMessage(), "1s")
	require.Contains(t, err.userMessage(), "request-timeout")

	err.cause = context.DeadlineExceeded
	require.Equal(t, "LLM request timed out after 1s: context deadline exceeded", err.Error())

	idle := &requestTimeoutError{timeout: 2 * time.Second, idle: true}
	require.Equal(t, "LLM stream received no data for 2s", idle.Error())
	require.Contains(t, idle.userMessage(), "stopped sending data for 2s")
	require.Contains(t, idle.userMessage(), "request-timeout")
}

// timeoutOnlyModel streams a single error part shaped exactly like the one
// requestTimeoutModel produces when its deadline fires.
type timeoutOnlyModel struct {
	fakeLanguageModel
}

func (m *timeoutOnlyModel) Stream(context.Context, fantasy.Call) (fantasy.StreamResponse, error) {
	timeoutErr := &requestTimeoutError{timeout: time.Second, idle: true, cause: context.DeadlineExceeded}
	return func(yield func(fantasy.StreamPart) bool) {
		yield(fantasy.StreamPart{Type: fantasy.StreamPartTypeError, Error: timeoutErr})
	}, nil
}

// TestRequestTimeoutRunFinishMessage pins what the user sees when a request
// exhausts its timeout: a "Request timed out" finish that names the elapsed
// budget and how to change it, instead of a bare provider error.
func TestRequestTimeoutRunFinishMessage(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("skipping on windows for now")
	}

	env := testEnv(t)
	model := &timeoutOnlyModel{}
	agent, err := coderAgent(vcr.NewRecorder(t), env, model, model)
	require.NoError(t, err)

	session, err := env.sessions.Create(t.Context(), "timeout session")
	require.NoError(t, err)

	_, err = agent.Run(t.Context(), SessionAgentCall{
		Prompt:          "Hello",
		SessionID:       session.ID,
		MaxOutputTokens: 10000,
	})
	require.Error(t, err)

	msgs, err := env.messages.List(t.Context(), session.ID)
	require.NoError(t, err)

	var finish *message.Finish
	for _, msg := range msgs {
		if msg.Role != message.Assistant {
			continue
		}
		if part := msg.FinishPart(); part != nil {
			finish = part
		}
	}
	require.NotNil(t, finish, "the assistant message should carry a finish part")
	require.Equal(t, message.FinishReasonError, finish.Reason)
	require.Equal(t, "Request timed out", finish.Message)
	require.Contains(t, finish.Details, "stopped sending data for 1s")
	require.Contains(t, finish.Details, "request-timeout")
}

func TestRequestTimeoutModel_SlowFirstTokenSurvives(t *testing.T) {
	t.Parallel()

	inner := &fakeLanguageModel{}
	// The first part arrives after a gap longer than the inter-token idle
	// timeout but within the time-to-first-token budget (idle *
	// firstTokenTimeoutFactor), the way a cold prompt-processing phase
	// streams nothing until the first token. It must survive; bounding the
	// first part by the plain idle timeout would abort it.
	inner.stream = func(yield func(fantasy.StreamPart) bool) {
		select {
		case <-time.After(150 * time.Millisecond):
		case <-inner.streamCtx.Done():
			yield(fantasy.StreamPart{Error: inner.streamCtx.Err()})
			return
		}
		yield(fantasy.StreamPart{})
	}
	m := newRequestTimeoutModel(inner, 50*time.Millisecond) // first-token budget 250ms
	stream, err := m.Stream(t.Context(), fantasy.Call{})
	require.NoError(t, err)

	parts := 0
	for part := range stream {
		require.NoError(t, part.Error)
		parts++
	}
	require.Equal(t, 1, parts, "a slow first token within the time-to-first-token budget must not be aborted")
}

func TestRequestTimeoutModel_FirstTokenTimeoutReportsItsBudget(t *testing.T) {
	t.Parallel()

	inner := &fakeLanguageModel{}
	// Nothing ever streams, so the time-to-first-token budget is what fires.
	inner.stream = func(yield func(fantasy.StreamPart) bool) {
		<-inner.streamCtx.Done()
		yield(fantasy.StreamPart{Error: inner.streamCtx.Err()})
	}
	m := newRequestTimeoutModel(inner, 10*time.Millisecond)
	stream, err := m.Stream(t.Context(), fantasy.Call{})
	require.NoError(t, err)

	var got error
	for part := range stream {
		if part.Error != nil {
			got = part.Error
		}
	}

	var timeoutErr *requestTimeoutError
	require.ErrorAs(t, got, &timeoutErr)
	require.ErrorIs(t, got, context.DeadlineExceeded)
	require.Equal(t, 10*time.Millisecond*firstTokenTimeoutFactor, timeoutErr.timeout,
		"a timeout before the first part must report the time-to-first-token budget, not the idle timeout")
}

// unwrapDepth walks err to the end of its Unwrap chain, giving up after cap
// links. It reports the number of links walked, so a chain that loops is a
// number the caller can fail on instead of a test that never returns.
func unwrapDepth(err error, cap int) int {
	n := 0
	for err != nil && n < cap {
		u, ok := err.(interface{ Unwrap() error })
		if !ok {
			break
		}
		err = u.Unwrap()
		n++
	}
	return n
}

// TestWrapTimedOutCannotPointAtItself pins the bug that wedged a live
// session for two hours at 100% of a core on 2026-10-30. wrapTimedOut used
// to fill in the cause of the single *requestTimeoutError held as the
// context cause and hand that same error back. Anything that ran an
// already-wrapped error through it a second time — a stream yielding another
// error part, a retry re-reporting the one it got — then recorded that error
// as its own cause, and errors.Is followed Unwrap round the loop forever.
func TestWrapTimedOutCannotPointAtItself(t *testing.T) {
	t.Parallel()

	fired := &requestTimeoutError{timeout: time.Second, idle: true}
	ctx, cancel := context.WithCancelCause(context.Background())
	cancel(fired)
	defer cancel(nil)

	first := wrapTimedOut(ctx, errors.New("connection reset by peer"))
	second := wrapTimedOut(ctx, first)

	require.Less(t, unwrapDepth(second, 100), 100,
		"the error chain does not terminate: it points back at itself")

	// The symptom the lane actually died of: a routine question about the
	// error never came back. Asked on another goroutine so a regression
	// fails the test instead of hanging the suite.
	answered := make(chan bool, 1)
	go func() { answered <- errors.Is(second, context.Canceled) }()
	select {
	case got := <-answered:
		require.False(t, got, "a timeout must not report as a user cancellation")
	case <-time.After(10 * time.Second):
		t.Fatal("errors.Is never returned: the Unwrap chain loops")
	}
}

// TestWrapTimedOutNeverReturnsTheHeldError states the invariant that keeps
// the loop above impossible: the error handed back is never the one the
// context holds, so nothing a caller does with it can reach that object and
// make it its own ancestor.
func TestWrapTimedOutNeverReturnsTheHeldError(t *testing.T) {
	t.Parallel()

	fired := &requestTimeoutError{timeout: time.Second, idle: true}
	ctx, cancel := context.WithCancelCause(context.Background())
	cancel(fired)
	defer cancel(nil)

	got := wrapTimedOut(ctx, errors.New("boom"))
	require.NotSame(t, fired, got, "wrapTimedOut must not hand back the error the context holds")
	require.Nil(t, fired.cause, "wrapTimedOut must not write to the error the context holds")
}

// TestWrapTimedOutReportsTheBudgetThatFired checks the fresh error still
// describes the timer that actually fired, since it is now a copy rather
// than the held error itself.
func TestWrapTimedOutReportsTheBudgetThatFired(t *testing.T) {
	t.Parallel()

	for _, fired := range []*requestTimeoutError{
		{timeout: 90 * time.Second, idle: true},
		{timeout: 30 * time.Second, idle: false},
	} {
		ctx, cancel := context.WithCancelCause(context.Background())
		cancel(fired)

		var got *requestTimeoutError
		require.ErrorAs(t, wrapTimedOut(ctx, errors.New("boom")), &got)
		require.Equal(t, fired.timeout, got.timeout)
		require.Equal(t, fired.idle, got.idle)
		require.Equal(t, fired.Error(), got.Error()[:len(fired.Error())])
		cancel(nil)
	}
}

// TestWrapTimedOutDoesNotStackTimeouts checks an error that already reports
// this timeout is passed through, so a stream yielding many parts cannot
// grow an ever-longer chain of identical timeout errors.
func TestWrapTimedOutDoesNotStackTimeouts(t *testing.T) {
	t.Parallel()

	fired := &requestTimeoutError{timeout: time.Second, idle: true}
	ctx, cancel := context.WithCancelCause(context.Background())
	cancel(fired)
	defer cancel(nil)

	err := wrapTimedOut(ctx, errors.New("connection reset by peer"))
	depth := unwrapDepth(err, 100)

	// Re-wrapped on another goroutine: wrapTimedOut asks errors.Is about the
	// error it was handed, so a version that loops hangs here rather than
	// returning. The watchdog turns that into a failure in seconds instead
	// of a test binary that spins until the suite timeout.
	rewrapped := make(chan error, 1)
	go func() {
		e := err
		for range 20 {
			e = wrapTimedOut(ctx, e)
		}
		rewrapped <- e
	}()
	select {
	case e := <-rewrapped:
		require.Equal(t, depth, unwrapDepth(e, 100), "re-wrapping must not lengthen the chain")
	case <-time.After(10 * time.Second):
		t.Fatal("wrapTimedOut never returned: it looped on the error it was given")
	}
}
