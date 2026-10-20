package agent

import (
	"context"
	"errors"
	"fmt"
	"sync/atomic"
	"time"

	"charm.land/fantasy"
)

// requestTimeoutError reports that an LLM request exhausted its configured
// request_timeout budget. For streaming requests this is an idle timeout:
// it only fires when the provider sends nothing for the whole window, so a
// slow but actively streaming response is never killed. It wraps the
// underlying error so callers can still match [context.DeadlineExceeded]
// through the chain.
type requestTimeoutError struct {
	timeout time.Duration
	idle    bool
	cause   error
}

func (e *requestTimeoutError) Error() string {
	msg := fmt.Sprintf("LLM request timed out after %s", e.timeout)
	if e.idle {
		msg = fmt.Sprintf("LLM stream received no data for %s", e.timeout)
	}
	if e.cause != nil {
		return fmt.Sprintf("%s: %v", msg, e.cause)
	}
	return msg
}

func (e *requestTimeoutError) Unwrap() error { return e.cause }

// userMessage explains the timeout in the UI, including how long the request
// ran before giving up and how to change the limit.
func (e *requestTimeoutError) userMessage() string {
	hint := "Increase the limit with \"option request-timeout SECONDS\" or set it to 0 to disable the timeout."
	if e.idle {
		return fmt.Sprintf("The model stopped sending data for %s. %s", e.timeout, hint)
	}
	return fmt.Sprintf("The model did not respond within %s. %s", e.timeout, hint)
}

// firstTokenTimeoutFactor scales request_timeout into the budget for a
// stream's time-to-first-token phase.
//
// request_timeout is an inter-token idle timeout: it resets on every part, so
// it bounds the silence *between* parts. But the gap before the *first* part
// is different in kind. A provider — especially a local model re-reading a
// long conversation into a cold prompt cache — streams nothing at all while
// it evaluates the prompt, which can take several times a normal inter-token
// gap. Bounding that phase by the same short budget aborts requests that are
// in fact making steady server-side progress. So the first part gets this
// multiple of request_timeout, and every gap after it gets request_timeout
// itself — a true mid-generation stall is still caught quickly, while a slow
// cold start is not mistaken for one. A connection that is genuinely dead
// before the first byte is caught at the larger budget instead.
const firstTokenTimeoutFactor = 5

// requestTimeoutModel wraps a [fantasy.LanguageModel] so requests are
// bounded by the configured request_timeout. Non-streaming calls get a hard
// per-request deadline, applied per call so fantasy's retry loop gives every
// attempt a fresh budget — the same per-request semantics the provider SDKs
// expose. Streams instead get an idle timeout: the budget resets whenever a
// part arrives and only fires when the provider goes silent, so a slow but
// actively streaming response is never aborted. The first part gets a larger
// budget ([requestTimeoutModel.firstTokenTimeout]) because a cold
// prompt-processing phase streams nothing until the first token.
type requestTimeoutModel struct {
	fantasy.LanguageModel
	timeout time.Duration
}

// newRequestTimeoutModel bounds each request to m with the given timeout. A
// timeout of zero or less, or a nil model, returns m unchanged.
func newRequestTimeoutModel(m fantasy.LanguageModel, timeout time.Duration) fantasy.LanguageModel {
	if m == nil || timeout <= 0 {
		return m
	}
	return requestTimeoutModel{LanguageModel: m, timeout: timeout}
}

// firstTokenTimeout is the budget for a stream's time-to-first-token phase:
// [firstTokenTimeoutFactor] times the configured inter-token timeout.
func (m requestTimeoutModel) firstTokenTimeout() time.Duration {
	return m.timeout * firstTokenTimeoutFactor
}

// wrapTimedOut replaces err with the requestTimeoutError that fired this
// model's own timer. Other errors — user cancellation, outer deadlines,
// provider failures — pass through unchanged. Cancellation errors caused by
// our own timer report as [context.DeadlineExceeded] so callers never
// mistake a timeout for a user cancellation.
func wrapTimedOut(ctx context.Context, err error) error {
	if err == nil {
		return err
	}
	var timeoutErr *requestTimeoutError
	if !errors.As(context.Cause(ctx), &timeoutErr) {
		return err
	}
	if errors.Is(err, context.Canceled) {
		timeoutErr.cause = context.DeadlineExceeded
	} else {
		timeoutErr.cause = err
	}
	return timeoutErr
}

// Generate implements [fantasy.LanguageModel]. The request gets a hard
// deadline: there is no incremental progress signal, so the whole call must
// finish within the budget.
func (m requestTimeoutModel) Generate(ctx context.Context, call fantasy.Call) (*fantasy.Response, error) {
	timeoutErr := &requestTimeoutError{timeout: m.timeout}
	ctx, cancel := context.WithTimeoutCause(ctx, m.timeout, timeoutErr)
	defer cancel()
	resp, err := m.LanguageModel.Generate(ctx, call)
	return resp, wrapTimedOut(ctx, err)
}

// Stream implements [fantasy.LanguageModel].
//
// The stream is consumed after Stream returns, so the timer must outlive
// this call. It starts with the time-to-first-token budget
// ([requestTimeoutModel.firstTokenTimeout]); once the first part arrives it
// resets to the configured idle timeout on every part, so it then fires only
// after that much silence between parts. The timer is released when iteration
// ends, whether the stream finishes, breaks, or a timeout aborts it.
func (m requestTimeoutModel) Stream(ctx context.Context, call fantasy.Call) (fantasy.StreamResponse, error) {
	firstTokenErr := &requestTimeoutError{timeout: m.firstTokenTimeout(), idle: true}
	idleErr := &requestTimeoutError{timeout: m.timeout, idle: true}
	// active names the budget in force, so the timer reports the phase that
	// actually elapsed. It is read by the timer goroutine and written by the
	// stream goroutine, so it is swapped atomically.
	var active atomic.Pointer[requestTimeoutError]
	active.Store(firstTokenErr)

	ctx, cancel := context.WithCancelCause(ctx)
	timer := time.AfterFunc(m.firstTokenTimeout(), func() { cancel(active.Load()) })

	inner, err := m.LanguageModel.Stream(ctx, call)
	if err != nil {
		timer.Stop()
		cancel(nil)
		return nil, wrapTimedOut(ctx, err)
	}
	return func(yield func(fantasy.StreamPart) bool) {
		defer timer.Stop()
		defer cancel(nil)
		inner(func(part fantasy.StreamPart) bool {
			active.Store(idleErr)
			timer.Reset(m.timeout)
			part.Error = wrapTimedOut(ctx, part.Error)
			return yield(part)
		})
	}, nil
}
