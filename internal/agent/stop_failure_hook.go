package agent

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os"

	"charm.land/fantasy"
	"github.com/charmbracelet/crush/internal/hooks"
	"github.com/charmbracelet/crush/internal/message"
)

// stopFailureKind is the kind of API error a turn ended on, in the names
// Claude Code gives its StopFailure hooks, which a matcher is tested
// against. A model server that never answered, refused the connection or
// timed out is server_error: on a box serving its own model that is the
// failure worth an alert.
func stopFailureKind(err error) string {
	var providerErr *fantasy.ProviderError
	if errors.As(err, &providerErr) && providerErr.StatusCode != 0 {
		switch code := providerErr.StatusCode; {
		case code == http.StatusTooManyRequests:
			return "rate_limit"
		case code == http.StatusServiceUnavailable || code == 529:
			return "overloaded"
		case code == http.StatusUnauthorized || code == http.StatusForbidden:
			return "authentication_failed"
		case code == http.StatusPaymentRequired:
			return "billing_error"
		case code == http.StatusNotFound:
			return "model_not_found"
		case code >= 500:
			return "server_error"
		case code >= 400:
			return "invalid_request"
		}
	}
	var timedOut *requestTimeoutError
	if errors.As(err, &timedOut) || errors.As(err, &providerErr) || fantasy.IsTransportError(err) {
		return "server_error"
	}
	return "unknown"
}

// shownError is the error a failed turn's message shows: its title and
// what the API said, "Provider Error: rate limited" say.
func shownError(m *message.Message) string {
	finish := m.FinishPart()
	if finish == nil {
		return ""
	}
	if finish.Message == "" || finish.Details == "" {
		return finish.Message + finish.Details
	}
	return finish.Message + ": " + finish.Details
}

// runStopFailureHooks runs the StopFailure hooks for a turn of the given
// session that ended on err, an error from the model's API, in place of
// its Stop hooks, as Claude Code does: shown is the error as the
// conversation shows it. They are for logging and alerts, so what they
// answer changes nothing, and a hook that fails is logged. Sub-agents run
// none, as they run no Stop hooks.
func (a *sessionAgent) runStopFailureHooks(ctx context.Context, sessionID string, err error, shown string) {
	if a.isSubAgent || a.cfg == nil {
		return
	}
	hookCfgs := a.cfg.Config().Hooks[hooks.EventStopFailure]
	if len(hookCfgs) == 0 {
		return
	}
	workingDir := a.cfg.WorkingDir()
	if workingDir == "" {
		wd, wdErr := os.Getwd()
		if wdErr != nil {
			slog.Warn("StopFailure hooks are configured but no working directory is available; not running them",
				"session_id", sessionID, "error", wdErr)
			return
		}
		workingDir = wd
	}
	runner := hooks.NewRunner(hookCfgs, workingDir, workingDir)
	result, runErr := runner.RunStopFailure(ctx, sessionID, stopFailureKind(err), err.Error(), shown)
	if runErr != nil {
		slog.Warn("StopFailure hook execution error", "session_id", sessionID, "error", runErr)
		return
	}
	for _, h := range result.Hooks {
		if h.Error != "" {
			slog.Warn("StopFailure hook could not evaluate", "session_id", sessionID, "hook", h.Name, "error", h.Error)
		}
	}
}
