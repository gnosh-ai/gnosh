package agent

import (
	"context"
	"errors"
	"fmt"
	"slices"

	"github.com/gnosh-ai/gnosh/internal/core"
)

// A Fallback is a model a turn goes on with when the session's model
// cannot answer (Options.Fallbacks). Name is how it is written in messages
// before it is made. Make makes it ready, its provider's client and, where
// it can be learned, its window; it is called only once the fallback is
// needed, so that a fallback whose server is down costs nothing until then.
// A Make that fails passes the fallback over for the rest of the turn.
type Fallback struct {
	Name string
	Make func(ctx context.Context) (Model, error)
}

// errFellBack says a request went unanswered and the turn now goes on with
// another model: the request is to be made again, for that model.
var errFellBack = errors.New("agent: the turn goes on with a fallback model")

// fallsBack says whether a failure is one a fallback model is for: the
// model's server cannot be reached, says it is not ready or too busy, or
// does not have the model. A limit on the person's requests, a key that is
// refused, and a request that is too long or that no model would take are
// not: another model would meet them too, or they are the person's to
// settle. gaveUp says the wait for the model ran out, which hands any other
// failure on as well.
func fallsBack(pe *core.ProviderError, gaveUp bool) bool {
	switch {
	case pe.Kind == core.ErrRateLimit, pe.Kind == core.ErrAuth, pe.Kind == core.ErrContextTooLong:
		return false
	case pe.Kind == core.ErrInvalidRequest:
		return pe.Status == 404 // no such model on that server
	case pe.Kind == core.ErrOverloaded, sortTrouble(pe) == troubleUnreachable:
		return true
	}
	return gaveUp
}

// fellBack moves the turn to another model when the failure pe of the
// model m is one a fallback is for (gaveUp: the wait for m ran out), and
// says so with a RetryEvent that names it. est is the size of the request
// that failed. With no fallback left, a turn that went on with one goes
// back to the session's own model, which is then waited for as it would
// be without fallbacks. It reports whether the turn moved.
func (a *Agent) fellBack(ctx context.Context, t *turn, purpose string, m Model, pe *core.ProviderError, gaveUp bool, rep *reply, est int) bool {
	if t == nil || len(a.opts.Fallbacks) == 0 || !fallsBack(pe, gaveUp) {
		return false
	}
	a.mu.Lock()
	primary := a.model
	a.mu.Unlock()
	to, ok := a.fallTo(ctx, m, est, purpose == "summary")
	if ctx.Err() != nil {
		return false
	}
	what := fmt.Sprintf("%s %s.", m.label(), whatHappened(pe))
	var msg string
	switch {
	case ok:
		next := "the next turn asks " + primary.label() + " first again"
		if modelKey(m) != modelKey(primary) {
			next = "the next turn asks " + primary.label() + " first"
		}
		msg = fmt.Sprintf("%s This turn goes on with the fallback model %s; %s.", what, to.label(), next)
	case modelKey(m) != modelKey(primary):
		// Every fallback has failed: the session's own model is waited for,
		// as it would have been without them.
		to = primary
		a.mu.Lock()
		a.fell, a.fellName = nil, ""
		a.mu.Unlock()
		msg = fmt.Sprintf("%s No fallback model is left, so this turn waits for %s, as it would without them.", what, primary.label())
	default:
		return false
	}
	a.emit(&RetryEvent{
		Purpose: purpose, Attempt: 1, Kind: pe.Kind, Status: pe.Status, Discarded: !rep.empty(),
		FellBack: to.label(), Message: msg,
	})
	return true
}

// fallTo moves the turn to the first fallback that is not a model that
// failed in this turn, is not on a model server of one's own that failed
// in it (no help when that server is down or busy, and one that holds a
// model at a time would unload the one in use to load it), can hold the
// conversation as it stands without cutting it (est tokens, with a window
// nobody knows taken to be UnknownWindow), and can be made; it returns it.
// For a summary, which is there to cut the conversation, it is enough
// that the fallback can take the request.
func (a *Agent) fallTo(ctx context.Context, from Model, est int, summary bool) (Model, bool) {
	a.mu.Lock()
	if a.fellOut == nil {
		a.fellOut = map[string]bool{}
	}
	a.fellOut[modelKey(from)] = true
	if from.Local {
		a.fellOut[serverKey(from)] = true
		if from.Address != "" {
			a.fellOut[addressKey(from)] = true
		}
	}
	if a.fell != nil && modelKey(*a.fell) == modelKey(from) {
		a.fellOut[a.fellName] = true
	}
	a.mu.Unlock()
	out := func(keys ...string) bool {
		a.mu.Lock()
		defer a.mu.Unlock()
		return slices.ContainsFunc(keys, func(k string) bool { return k != "" && a.fellOut[k] })
	}
	passOver := func(name, why string) {
		a.mu.Lock()
		a.fellOut[name] = true
		a.mu.Unlock()
		a.notice(LevelWarning, "provider", fmt.Sprintf("The fallback model %s is passed over: %s.", name, why))
	}
	for _, fb := range a.opts.Fallbacks {
		if fb.Make == nil || out(fb.Name) {
			continue
		}
		a.setPhase(PhaseRetry, "getting the fallback model "+fb.Name+" ready")
		m, err := fb.Make(ctx)
		if err == nil && m.Provider == nil {
			err = errors.New("it has no provider")
		}
		if ctx.Err() != nil {
			return Model{}, false
		}
		if err != nil {
			passOver(fb.Name, fmt.Sprintf("it cannot be used (%v)", err))
			continue
		}
		addr := ""
		if m.Address != "" {
			addr = addressKey(m)
		}
		switch {
		case out(modelKey(m)):
			continue
		case out(serverKey(m), addr):
			passOver(fb.Name, "it is on the same model server of your own as a model that failed in this turn, which would be no help and could unload the model in use")
			continue
		}
		su := a.setup()
		su.model = m
		lim, need := a.limits(su), a.predict(est, m)
		limit := lim.point
		if summary {
			limit = lim.hard
		}
		if need > limit {
			window := tokens(lim.window) + " tokens"
			if !lim.known {
				window = "not known, so taken to be " + window
			}
			passOver(fb.Name, fmt.Sprintf("its context window (%s) cannot hold the conversation as it stands (about %s tokens) without cutting it", window, tokens(need)))
			continue
		}
		// The level of thinking chosen for the session holds where the
		// fallback has it.
		a.mu.Lock()
		level := a.model.Level
		a.mu.Unlock()
		if level != "" && slices.Contains(m.Levels, level) {
			m.Level = level
		}
		if checkLevel(m) != nil {
			m.Level = ""
		}
		a.mu.Lock()
		a.fell, a.fellName = &m, fb.Name
		a.mu.Unlock()
		return m, true
	}
	return Model{}, false
}

// serverKey and addressKey name a model's server, by its provider and by
// its address, among the things that failed in a turn.
func serverKey(m Model) string {
	if m.Provider == nil {
		return ""
	}
	return "server\x00" + m.Provider.Name()
}

func addressKey(m Model) string { return "address\x00" + m.Address }

// fellBackEnds puts the session's own model back in force, at the end of a
// turn.
func (a *Agent) fellBackEnds() {
	a.mu.Lock()
	a.fell, a.fellName, a.fellOut = nil, "", nil
	a.mu.Unlock()
}
