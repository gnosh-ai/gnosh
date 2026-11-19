package app

import (
	"context"
	"fmt"
	"iter"
	"net"
	"net/url"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/gnosh-ai/gnosh/internal/agent"
	"github.com/gnosh-ai/gnosh/internal/config"
	"github.com/gnosh-ai/gnosh/internal/core"
	"github.com/gnosh-ai/gnosh/internal/llm"
)

// providerUse is a configured provider as a session uses it: the address
// that was picked, the client made for it, and the models its server
// listed when it was asked.
type providerUse struct {
	cfg      config.Provider
	endpoint config.Endpoint
	prov     core.Provider

	asked   bool
	models  []core.ModelInfo
	listErr error
}

// modelUse is a model as a session uses it.
type modelUse struct {
	ref   config.ModelRef
	pu    *providerUse
	model agent.Model
}

// dialOnly reports whether something accepts a connection at the address
// of a URL. It opens the connection and closes it, and sends nothing: a
// model server that holds one model at a time must not be disturbed by a
// look.
func dialOnly(ctx context.Context, raw string) error {
	u, err := url.Parse(raw)
	if err != nil {
		return err
	}
	port := u.Port()
	if port == "" {
		port = "80"
		if u.Scheme == "https" {
			port = "443"
		}
	}
	d := net.Dialer{Timeout: 3 * time.Second}
	conn, err := d.DialContext(ctx, "tcp", net.JoinHostPort(u.Hostname(), port))
	if err != nil {
		return err
	}
	return conn.Close()
}

// providerFor returns the client of a configured provider, making it the
// first time it is asked for. With several addresses configured, the first
// that accepts a connection is the one used.
func (l *live) providerFor(ctx context.Context, id string) (*providerUse, error) {
	if pu := l.providerUsed(id); pu != nil {
		return pu, nil
	}
	pc, ok := l.cfg.Provider(id)
	switch {
	case !ok:
		return nil, fmt.Errorf("no provider named %q is configured. The providers are: %s", id, providerList(l.cfg))
	case pc.Disabled:
		return nil, fmt.Errorf("provider %s is disabled in the config (%s), so it cannot be used", id, pc.Origin)
	case pc.LeftOut != "":
		return nil, fmt.Errorf("provider %s cannot be used: %s", id, pc.LeftOut)
	case len(pc.Endpoints) == 0:
		return nil, fmt.Errorf("provider %s has no address", id)
	}
	e, passed, answered := config.SelectEndpoint(ctx, pc, dialOnly)
	switch {
	case !answered:
		e = pc.Endpoints[0]
		l.notice(agent.LevelWarning, "provider", fmt.Sprintf(
			"None of the addresses of provider %s accepted a connection (%s). gnosh goes on with the first, %s; if its server is not running, the first request will say so.",
			id, strings.Join(passed, "; "), e.Display))
	case len(passed) > 0:
		l.notice(agent.LevelInfo, "provider", fmt.Sprintf("Provider %s is used at %s (%s).", id, e.Display, strings.Join(passed, "; ")))
	}
	headers := map[string]string{}
	for _, h := range pc.Headers {
		if !h.Value.Empty() {
			headers[h.Name] = h.Value.Reveal()
		}
	}
	lc := llm.Config{
		Name:    pc.ID,
		Type:    pc.Wire,
		BaseURL: e.URL,
		APIKey:  pc.APIKey.Reveal(),
		Headers: headers,
		Timeouts: llm.Timeouts{
			Connect:    e.Timeouts.Connect,
			FirstToken: e.Timeouts.FirstToken,
			Idle:       e.Timeouts.Idle,
			Total:      e.Timeouts.Total,
		},
		Extra: pc.ExtraBody,
	}
	if pc.Wire == llm.TypeAnthropic {
		// What the model is sent is a view that can change under it: a
		// popped frame and a slide take replies out. Anthropic checks the
		// thinking it is sent back against what it signed, so the
		// thinking of earlier replies is not sent back at all.
		lc.HistoryThinking = "drop"
	}
	prov, err := llm.New(lc)
	if err != nil {
		return nil, fmt.Errorf("provider %s cannot be used: %w", id, err)
	}
	pu := &providerUse{cfg: pc, endpoint: e, prov: prov}
	l.provMu.Lock()
	if had := l.providers[id]; had != nil {
		pu = had // made meanwhile, by another call
	} else {
		l.providers[id] = pu
	}
	l.provMu.Unlock()
	return pu, nil
}

// providerUsed is the client of provider id if one was made, or nil. The
// table is read from more than one goroutine (the server's load is asked
// for beside the requests).
func (l *live) providerUsed(id string) *providerUse {
	l.provMu.Lock()
	defer l.provMu.Unlock()
	return l.providers[id]
}

// providerList names the configured providers for a message.
func providerList(cfg *config.Config) string {
	if len(cfg.Providers) == 0 {
		return "none"
	}
	ids := make([]string, len(cfg.Providers))
	for i, p := range cfg.Providers {
		ids[i] = p.ID
	}
	return strings.Join(ids, ", ")
}

// listModels asks a provider's server which models it has, once. It only
// ever asks for the list: a server that holds one model at a time loads a
// model as soon as a request names it.
func (l *live) listModels(ctx context.Context, pu *providerUse) ([]core.ModelInfo, error) {
	if pu.asked {
		return pu.models, pu.listErr
	}
	pu.asked = true
	lister, ok := pu.prov.(core.ModelLister)
	if !ok {
		return nil, nil
	}
	wait := l.p.o.ModelWait
	if wait <= 0 {
		wait = 10 * time.Second
	}
	ctx, cancel := context.WithTimeout(ctx, wait)
	defer cancel()
	pu.models, pu.listErr = lister.Models(ctx)
	return pu.models, pu.listErr
}

// offered lists the models a provider is known to have: the ones the
// config registers on it, and the ones its server lists when the config
// says to ask.
func (l *live) offered(ctx context.Context, pc config.Provider) (ids []string, note string) {
	for _, m := range l.cfg.Models {
		if m.Provider == pc.ID {
			ids = append(ids, m.ID)
		}
	}
	if !pc.ListModels.V {
		return ids, ""
	}
	pu, err := l.providerFor(ctx, pc.ID)
	if err != nil {
		return ids, err.Error()
	}
	models, err := l.listModels(ctx, pu)
	if err != nil {
		return ids, fmt.Sprintf("the list of models of provider %s at %s could not be read: %v", pc.ID, pu.endpoint.Display, err)
	}
	for _, m := range models {
		if !slices.Contains(ids, m.ID) {
			ids = append(ids, m.ID)
		}
	}
	return ids, ""
}

// errNoModel means no model could be settled on. Its text says why and
// what to do.
type errNoModel struct{ why string }

func (e *errNoModel) Error() string { return e.why }

// resolveRef settles which provider a model named without one is on, and
// with no name at all, which model there is to use. gnosh never guesses
// between several: asked for the choice, pick puts it to the person, and
// is nil where nobody can be asked.
func (l *live) resolveRef(ctx context.Context, ref config.ModelRef, chosen bool, pick func(models []string) (string, bool)) (config.ModelRef, error) {
	if chosen && ref.Provider != "" {
		return ref, nil
	}
	usable := l.cfg.Usable()
	if len(usable) == 0 {
		why := "no model provider can be used, so there is no model to send a prompt to."
		if len(l.cfg.Providers) == 0 {
			why = fmt.Sprintf("no model provider is configured, so there is no model to send a prompt to. Add one to %s, for example: {%s}", l.cfg.Paths.UserSettings, config.ProviderExample)
		}
		for _, p := range l.cfg.Providers {
			switch {
			case p.Disabled:
				why += fmt.Sprintf(" Provider %s is disabled.", p.ID)
			case p.LeftOut != "":
				why += fmt.Sprintf(" Provider %s is left out: %s.", p.ID, p.LeftOut)
			}
		}
		return ref, &errNoModel{why}
	}
	var all, notes []string
	var owners []string
	for _, pc := range usable {
		ids, note := l.offered(ctx, pc)
		if note != "" {
			notes = append(notes, note)
		}
		for _, id := range ids {
			all = append(all, pc.ID+"/"+id)
			if chosen && id == ref.ID {
				owners = append(owners, pc.ID)
			}
		}
	}
	noteText := ""
	if len(notes) > 0 {
		noteText = " (" + strings.Join(notes, "; ") + ")"
	}
	if chosen {
		switch len(owners) {
		case 1:
			return config.ModelRef{Provider: owners[0], ID: ref.ID}, nil
		case 0:
			if len(usable) == 1 {
				return config.ModelRef{Provider: usable[0].ID, ID: ref.ID}, nil
			}
			return ref, &errNoModel{fmt.Sprintf("no provider is known to offer the model %s, and there are several providers, so gnosh cannot tell which was meant. Name it as provider/model.%s", ref.ID, noteText)}
		}
		return ref, &errNoModel{fmt.Sprintf("the model %s is offered by more than one provider (%s). Name it as provider/model.", ref.ID, strings.Join(owners, ", "))}
	}
	switch len(all) {
	case 0:
		return ref, &errNoModel{fmt.Sprintf("no model is chosen and none is known: no provider lists one. Name one with -m provider/model, or register one under \"models\" in gnosh.json.%s", noteText)}
	case 1:
		prov, id, _ := strings.Cut(all[0], "/")
		return config.ModelRef{Provider: prov, ID: id}, nil
	}
	if pick != nil {
		if name, ok := pick(all); ok {
			prov, id, _ := strings.Cut(name, "/")
			return config.ModelRef{Provider: prov, ID: id}, nil
		}
	}
	return ref, &errNoModel{fmt.Sprintf("no model is chosen, and %d are available. gnosh does not guess, because naming the wrong one makes a model server unload the one in use. Choose one with -m provider/model, or with \"model\" in gnosh.json. They are: %s.", len(all), strings.Join(all, ", "))}
}

// buildModel makes a model ready for the loop: its provider's client, its
// window, its thinking levels and the level in force. window says whether
// the model's context window is needed, which it is for the model that
// does the work and not for the one that does the short side jobs.
func (l *live) buildModel(ctx context.Context, ref config.ModelRef, slot config.Slot, window bool) (*modelUse, error) {
	pu, err := l.providerFor(ctx, ref.Provider)
	if err != nil {
		return nil, err
	}
	cm, registered := l.cfg.Model(ref)
	info := core.ModelInfo{ID: ref.ID}
	if registered && cm.ContextWindow.Set {
		info.ContextWindow = cm.ContextWindow.V
	}
	if window && (info.ContextWindow == 0 || pu.cfg.ListModels.V) {
		models, err := l.listModels(ctx, pu)
		if err != nil && info.ContextWindow == 0 {
			l.notice(agent.LevelWarning, "model", fmt.Sprintf(
				"Provider %s at %s did not list its models (%v), so the context window of %s could not be learned from it.",
				ref.Provider, pu.endpoint.Display, err, ref.ID))
		}
		found := false
		var names []string
		for _, mi := range models {
			names = append(names, mi.ID)
			if mi.ID != ref.ID {
				continue
			}
			found = true
			if info.ContextWindow == 0 {
				info.ContextWindow = mi.ContextWindow
			}
			info.MaxOutput = mi.MaxOutput
		}
		if err == nil && len(models) > 0 && !found && !registered {
			l.notice(agent.LevelWarning, "model", fmt.Sprintf(
				"Provider %s does not list a model named %s. It lists: %s. Requests name %s all the same; if the server refuses them, choose one of its own with -m or /model.",
				ref.Provider, ref.ID, strings.Join(names, ", "), ref.ID))
		}
	}

	levels := thinkingLevels(pu.cfg, cm, registered, ref.ID)
	level, note := thinkingLevel(levels, slot.Thinking)
	if note != "" {
		l.notice(agent.LevelWarning, "model", note)
	}
	m := agent.Model{
		Provider: pu.prov,
		Info:     info,
		Address:  pu.endpoint.Display,
		Local:    pu.endpoint.Local,
		Levels:   levels,
		Level:    level,
	}
	switch {
	case registered && cm.MaxOutputTokens.Set:
		m.MaxTokens = cm.MaxOutputTokens.V
	case l.cfg.Limits.MaxOutputTokens.Set:
		m.MaxTokens = l.cfg.Limits.MaxOutputTokens.V
	}
	if lim := l.cfg.Limits.MaxThinkingTokens; lim.Set && lim.V == 0 && slices.Contains(levels, "off") {
		// MAX_THINKING_TOKENS=0 turns thinking off, as in Claude Code.
		m.Level = "off"
	}
	if m.ThinkingBudget = thinkingBudget(pu.cfg, cm, registered, l.cfg.Limits.MaxThinkingTokens); m.ThinkingBudget > 0 {
		m.BudgetNote = "\n\n" + l.texts.ThinkingBudget() + "\n"
	}
	if slot.Temperature.Set {
		t := slot.Temperature.V
		m.Temperature = &t
	}
	l.typeMu.Lock()
	l.types[ref.Provider+"/"+ref.ID] = pu.cfg.Type
	l.typeMu.Unlock()
	return &modelUse{ref: ref, pu: pu, model: m}, nil
}

// fallbacks are the session's fallback models ("fallbackModel",
// --fallback-model), each made the first time a turn needs it, so that a
// fallback whose server is down costs the session nothing until then. One
// that cannot be made is tried again the next time.
func (l *live) fallbacks() []agent.Fallback {
	var out []agent.Fallback
	for _, ref := range l.cfg.Fallback.V {
		var mu sync.Mutex
		var made *agent.Model
		out = append(out, agent.Fallback{Name: ref.String(), Make: func(ctx context.Context) (agent.Model, error) {
			mu.Lock()
			defer mu.Unlock()
			if made != nil {
				return *made, nil
			}
			// (As every other maker of a model: the providers' lists of
			// models are not to be read and filled at once.)
			l.switchMu.Lock()
			defer l.switchMu.Unlock()
			r, err := l.resolveRef(ctx, ref, true, nil)
			if err != nil {
				return agent.Model{}, err
			}
			u, err := l.buildModel(ctx, r, config.Slot{}, true)
			if err != nil {
				return agent.Model{}, err
			}
			made = &u.model
			return u.model, nil
		}})
	}
	return out
}

// defaultThinkingBudget is how many tokens a model on a llama.cpp server
// may think before a reply when nothing says otherwise. Such a model runs
// on the person's own machine, at some 8 to 20 tokens a second: on Jae's
// AI box, with thinking on, Qwen thought for 14,270 tokens, half an hour,
// before one reply, and seven turns before it were stopped by hand with
// nothing to show. 4,096 is some minutes of thinking at most, more than an
// ordinary step of the work takes.
const defaultThinkingBudget = 4096

// thinkingBudget returns how many tokens a model may think before a reply:
// MAX_THINKING_TOKENS, then the model's own thinkingBudget, then gnosh's
// default for a llama.cpp server; 0 means no cap.
func thinkingBudget(pc config.Provider, cm config.Model, registered bool, env config.Value[int]) int {
	switch {
	case env.Set && env.V > 0:
		return env.V
	case registered && cm.ThinkingBudget.Set:
		return cm.ThinkingBudget.V
	case pc.Type == "llamacpp":
		return defaultThinkingBudget
	}
	return 0
}

// thinkingLevels returns a model's thinking levels, the least thinking
// first. The config's own list wins, an empty one saying the model has no
// switch; without one the provider's type and the model's name decide. A
// model with no known switch has none, and the thinking commands then say
// so.
func thinkingLevels(pc config.Provider, cm config.Model, registered bool, id string) []string {
	if registered && cm.ThinkingLevels.Set {
		// What the config says of the model is the last word: its levels,
		// or, with an empty list, that it has no switch.
		if len(cm.ThinkingLevels.V) == 0 {
			return nil
		}
		return slices.Clone(cm.ThinkingLevels.V)
	}
	name := strings.ToLower(id)
	switch {
	case strings.Contains(name, "gpt-oss"):
		return []string{"low", "medium", "high"}
	case pc.Type == "openai":
		for _, prefix := range []string{"o1", "o3", "o4", "gpt-5"} {
			if strings.HasPrefix(name, prefix) {
				return []string{"low", "medium", "high"}
			}
		}
		return nil
	}
	return []string{"off", "on"}
}

// thinkingLevel turns the level the config holds ("on", "off", or one of
// the model's own) into one of the model's levels. "on" is the most
// thinking the model has and "off" the least. When nothing chose a level,
// a model with an on/off switch thinks, and a model with a dial is left at
// its own default.
func thinkingLevel(levels []string, chosen config.Value[string]) (level, note string) {
	if len(levels) == 0 {
		return "", ""
	}
	switch chosen.V {
	case "on", "":
		if !chosen.Set && levels[0] != "off" {
			return "", ""
		}
		return levels[len(levels)-1], ""
	case "off":
		return levels[0], ""
	}
	if slices.Contains(levels, chosen.V) {
		return chosen.V, ""
	}
	return "", fmt.Sprintf("The thinking level %q (%s) is not one of the model's levels (%s), so the model is left at its own default.",
		chosen.V, chosen.Origin, strings.Join(levels, ", "))
}

// noModel is the provider of an interactive session that has no model to
// use. It answers every request with the reason, so that the session still
// opens and /status, /doctor and /model can be used to put it right.
type noModel struct{ why string }

// Name names it in messages.
func (n noModel) Name() string { return "gnosh" }

// Stream fails at once, saying why there is no model.
func (n noModel) Stream(ctx context.Context, req *core.Request) iter.Seq2[core.Event, error] {
	return func(yield func(core.Event, error) bool) {
		yield(core.Event{}, &core.ProviderError{Provider: "gnosh", Kind: core.ErrInvalidRequest, Message: n.why})
	}
}

// setupModels settles the session's two models. In a session nobody
// attends, a model that cannot be settled stops the run. In an
// interactive one the session opens all the same, and says why prompts
// will fail until a model is chosen.
func (l *live) setupModels(ctx context.Context, pick func(models []string) (string, bool)) error {
	ref, err := l.resolveRef(ctx, l.cfg.Large.Model.V, l.cfg.Large.Model.Set, pick)
	var large *modelUse
	if err == nil {
		large, err = l.buildModel(ctx, ref, l.cfg.Large, true)
	}
	if err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if !l.interactive {
			return err
		}
		why := strings.TrimRight(err.Error(), ".") + ". Choose a model with /model, or run /doctor to see what is wrong"
		l.notice(agent.LevelError, "model", "This session has no model to use: "+err.Error())
		large = &modelUse{model: agent.Model{Provider: noModel{why}}}
	}
	l.large = large

	// The short side jobs go to the small model when one is chosen, and to
	// the large one otherwise: a session never names a model the person
	// did not choose.
	if s := l.cfg.Small; s.Model.Set && large.pu != nil {
		sref, err := l.resolveRef(ctx, s.Model.V, true, nil)
		if err == nil && sref != large.ref {
			var small *modelUse
			if small, err = l.buildModel(ctx, sref, s, false); err == nil {
				l.small = small
			}
		}
		if err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			l.notice(agent.LevelWarning, "model", fmt.Sprintf("The small model %s cannot be used (%v), so the session's model does the short side jobs too.", s.Model.V, err))
		}
	}
	return nil
}

// outgoing is the loop's last look at every request before it is sent: the
// conversation is made harmless, and for the servers that take the
// thinking switch in a field of their own, the switch is put there.
func (l *live) outgoing(provider string, req *core.Request) {
	defuseRequest(req)
	l.typeMu.Lock()
	typ := l.types[provider+"/"+req.Model]
	l.typeMu.Unlock()
	if typ == "openai-compatible" {
		// Such a server reads the switch from the chat template's
		// arguments, as llama.cpp's own server does.
		kw := map[string]any{}
		switch {
		case req.Thinking.Effort != "":
			kw["reasoning_effort"] = req.Thinking.Effort
			req.Thinking.Effort = ""
		case req.Thinking.Mode == "on":
			kw["enable_thinking"] = true
		case req.Thinking.Mode == "off":
			kw["enable_thinking"] = false
		}
		if len(kw) > 0 {
			addExtra(req, "chat_template_kwargs", kw)
		}
	}
}

// addExtra sets a field of a request's body, leaving a value the config
// put there for the same field in place when it is an object, where the
// two are merged key by key.
func addExtra(req *core.Request, key string, value any) {
	if req.Extra == nil {
		req.Extra = map[string]any{}
	}
	if old, ok := req.Extra[key].(map[string]any); ok {
		if add, ok := value.(map[string]any); ok {
			merged := map[string]any{}
			for k, v := range add {
				merged[k] = v
			}
			for k, v := range old {
				merged[k] = v
			}
			req.Extra[key] = merged
			return
		}
	}
	req.Extra[key] = value
}
