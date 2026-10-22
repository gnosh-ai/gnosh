package agent

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"syscall"
	"time"

	"charm.land/fantasy"
)

// Defaults for reconnectingModel.
const (
	// reconnectWindow is how long a stream keeps reconnecting to a model
	// server that is not up yet before the error is let through. It covers
	// a local server restarting and reloading its model (about 25 seconds
	// for the AI box's 27B model) with room to spare; past it, the server
	// is more likely gone or misconfigured than restarting, and the error
	// is better shown to the user than waited on.
	reconnectWindow = 5 * time.Minute
	// reconnectEvery is the pause between attempts.
	reconnectEvery = 3 * time.Second
)

// reconnectingModel wraps a [fantasy.LanguageModel] so that a stream whose
// very first event is "the server is not up yet" waits and reconnects,
// instead of surfacing that as a failure.
//
// fantasy retries a failed step only three times, and it counts every
// failure across the step's whole life against that budget, without
// resetting it after a retry succeeds. A long step that rode out one model
// server restart therefore had no budget left for the next one: on the AI
// box a sub-agent lost about an hour of work that way, when llama-server
// was restarted twenty minutes into one of its requests. Absorbing the
// reconnect phase here keeps those failures off fantasy's budget, which is
// then spent only on streams that break after output has started.
//
// Only failures that arrive before any output are absorbed. Once a stream
// has produced anything, its errors pass through unchanged, so a consumer
// never sees part of a response twice.
type reconnectingModel struct {
	fantasy.LanguageModel
	window time.Duration
	every  time.Duration
	now    func() time.Time
	sleep  func(context.Context, time.Duration) error
}

// newReconnectingModel wraps m; a nil model is returned unchanged.
func newReconnectingModel(m fantasy.LanguageModel) fantasy.LanguageModel {
	if m == nil {
		return m
	}
	return reconnectingModel{
		LanguageModel: m,
		window:        reconnectWindow,
		every:         reconnectEvery,
		now:           time.Now,
		sleep:         sleepCtx,
	}
}

func sleepCtx(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-t.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// serverNotReady reports whether err means the model server is not up yet
// (restarting, still loading its model, or not listening) rather than that
// it rejected the request.
func serverNotReady(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return false
	}
	var pe *fantasy.ProviderError
	if errors.As(err, &pe) && pe.StatusCode != 0 {
		switch pe.StatusCode {
		case http.StatusBadGateway, http.StatusServiceUnavailable, http.StatusGatewayTimeout:
			return true
		}
		return false
	}
	if errors.Is(err, syscall.ECONNREFUSED) || errors.Is(err, syscall.ECONNRESET) ||
		errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
		return true
	}
	if pe != nil && pe.TransientError {
		return true
	}
	return fantasy.IsTransportError(err)
}

// Stream implements [fantasy.LanguageModel]. A setup error from the inner
// model is returned as before; reconnecting happens while the stream is
// consumed, which is when providers actually connect.
func (m reconnectingModel) Stream(ctx context.Context, call fantasy.Call) (fantasy.StreamResponse, error) {
	first, err := m.LanguageModel.Stream(ctx, call)
	if err != nil {
		return nil, err
	}
	return func(yield func(fantasy.StreamPart) bool) {
		start := m.now()
		stream := first
		for attempt := 1; ; attempt++ {
			var (
				held    []fantasy.StreamPart // warnings seen before the first real event
				started bool                 // a real event has been passed on
				retry   bool
			)
			stream(func(part fantasy.StreamPart) bool {
				if started {
					return yield(part)
				}
				switch {
				case part.Type == fantasy.StreamPartTypeWarnings:
					// Warnings are produced locally, before connecting; hold
					// them so a reconnect does not repeat them.
					held = append(held, part)
					return true
				case part.Type == fantasy.StreamPartTypeError &&
					serverNotReady(part.Error) && m.now().Sub(start) < m.window:
					retry = true
					return false
				default:
					started = true
					for _, h := range held {
						if !yield(h) {
							return false
						}
					}
					return yield(part)
				}
			})
			if !retry {
				if !started {
					for _, h := range held {
						if !yield(h) {
							return
						}
					}
				}
				if attempt > 1 && started {
					slog.Info("Reconnected to the model server", "attempts", attempt)
				}
				return
			}
			if attempt == 1 {
				slog.Warn("Model server not ready; reconnecting", "window", m.window)
			}
			if err := m.sleep(ctx, m.every); err != nil {
				yield(fantasy.StreamPart{Type: fantasy.StreamPartTypeError, Error: err})
				return
			}
			next, err := m.LanguageModel.Stream(ctx, call)
			if err != nil {
				yield(fantasy.StreamPart{Type: fantasy.StreamPartTypeError, Error: err})
				return
			}
			stream = next
		}
	}, nil
}
