package agent

import (
	"charm.land/catwalk/pkg/catwalk"
	"charm.land/fantasy"
	"charm.land/fantasy/providers/openaicompat"

	"github.com/charmbracelet/crush/internal/config"
	"github.com/charmbracelet/crush/internal/discover"
)

// applyReasoning writes how hard this model should think into the request
// options of an OpenAI-compatible provider, as llama.cpp's chat-template
// switch.
//
// The spelling depends on the model. A Qwen takes
// {"chat_template_kwargs": {"enable_thinking": bool}}; a gpt-oss takes
// {"chat_template_kwargs": {"reasoning_effort": "low"|"medium"|"high"}} and
// ignores enable_thinking outright, which is why the old global switch did
// nothing on it. A model with no reasoning axis, and a provider that is not
// OpenAI-compatible, are left untouched.
func applyReasoning(opts fantasy.ProviderOptions, model catwalk.Model, provider string, providerCfg config.ProviderConfig, cfg *config.Config) {
	level := cfg.ReasoningLevelOrDefault(provider, model)
	if level == "" {
		return
	}
	kind, _ := config.ReasoningAxis(model)
	setReasoning(opts, providerCfg, kind, level)
}

// quietReasoning asks for as little reasoning as the model allows, whatever
// the user chose. It is for requests whose answer is a few words, such as a
// session title, where a thinking model would otherwise spend its whole
// small budget reasoning and be cut off before answering.
//
// For a Qwen that is thinking off. For a gpt-oss there is no off — harmony
// always emits an analysis channel — so it is the lowest effort instead.
//
// A model whose id names no axis still gets enable_thinking false. That is
// the blanket switch this started as, and it is what protects a thinking
// model we failed to recognise: a server that does not know the key ignores
// it, while an unrecognised Qwen would otherwise reason through its whole
// title budget and leave every session "Untitled Session" again.
func quietReasoning(opts fantasy.ProviderOptions, model catwalk.Model, providerCfg config.ProviderConfig) {
	kind, levels := config.ReasoningAxis(model)
	if kind == config.ReasoningNone || len(levels) == 0 {
		setReasoning(opts, providerCfg, config.ReasoningThink, config.ReasoningNoThink)
		return
	}
	setReasoning(opts, providerCfg, kind, levels[0])
}

// setReasoning writes one level into opts in the spelling its kind calls
// for; other provider kinds are left untouched.
func setReasoning(opts fantasy.ProviderOptions, providerCfg config.ProviderConfig, kind config.ReasoningKind, level string) {
	if kind == config.ReasoningNone || level == "" {
		return
	}
	if string(providerCfg.Type) != openaicompat.Name && !discover.IsKnownCustomProvider(string(providerCfg.Type)) {
		return
	}
	kwargs := chatTemplateKwargs(opts)
	switch kind {
	case config.ReasoningThink:
		kwargs["enable_thinking"] = level == config.ReasoningThinkOn
	case config.ReasoningEffort:
		kwargs["reasoning_effort"] = level
	}
}

// chatTemplateKwargs returns the request's chat_template_kwargs map, making
// it and everything above it as needed, so a caller can write one key
// without disturbing the others already there.
func chatTemplateKwargs(opts fantasy.ProviderOptions) map[string]any {
	po, _ := opts[openaicompat.Name].(*openaicompat.ProviderOptions)
	if po == nil {
		po = &openaicompat.ProviderOptions{}
		opts[openaicompat.Name] = po
	}
	if po.ExtraBody == nil {
		po.ExtraBody = map[string]any{}
	}
	kwargs, _ := po.ExtraBody["chat_template_kwargs"].(map[string]any)
	if kwargs == nil {
		kwargs = map[string]any{}
	}
	po.ExtraBody["chat_template_kwargs"] = kwargs
	return kwargs
}
