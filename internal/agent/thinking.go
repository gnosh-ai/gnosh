package agent

import (
	"charm.land/fantasy"
	"charm.land/fantasy/providers/openaicompat"

	"github.com/charmbracelet/crush/internal/config"
	"github.com/charmbracelet/crush/internal/discover"
)

// applyThinking writes the stored /think or /no_think switch into the request
// options of an OpenAI-compatible provider as llama.cpp's chat-template
// switch, {"chat_template_kwargs": {"enable_thinking": enabled}}, which Qwen
// honours. Other provider kinds, and an unset switch, leave opts untouched.
func applyThinking(opts fantasy.ProviderOptions, providerCfg config.ProviderConfig, cfg *config.Config) {
	enabled, set := cfg.Thinking()
	if !set {
		return
	}
	setEnableThinking(opts, providerCfg, enabled)
}

// disableThinking switches reasoning off for one request, whatever the
// user's /think setting. It is for requests whose answer is a few words,
// such as a session title, where a thinking model would otherwise spend its
// whole small budget reasoning and be cut off before answering.
func disableThinking(opts fantasy.ProviderOptions, providerCfg config.ProviderConfig) {
	setEnableThinking(opts, providerCfg, false)
}

// setEnableThinking writes llama.cpp's chat-template switch into opts for
// OpenAI-compatible providers; other provider kinds are left untouched.
func setEnableThinking(opts fantasy.ProviderOptions, providerCfg config.ProviderConfig, enabled bool) {
	if string(providerCfg.Type) != openaicompat.Name && !discover.IsKnownCustomProvider(string(providerCfg.Type)) {
		return
	}
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
	kwargs["enable_thinking"] = enabled
	po.ExtraBody["chat_template_kwargs"] = kwargs
}
