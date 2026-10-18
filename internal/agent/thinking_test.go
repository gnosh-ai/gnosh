package agent

import (
	"testing"

	"charm.land/fantasy"
	"charm.land/fantasy/providers/openaicompat"

	"github.com/charmbracelet/crush/internal/config"
)

func thinkingKwargs(t *testing.T, opts fantasy.ProviderOptions) (map[string]any, bool) {
	t.Helper()
	po, ok := opts[openaicompat.Name].(*openaicompat.ProviderOptions)
	if !ok || po.ExtraBody == nil {
		return nil, false
	}
	kw, ok := po.ExtraBody["chat_template_kwargs"].(map[string]any)
	return kw, ok
}

func TestApplyThinking(t *testing.T) {
	t.Parallel()
	off, on := false, true
	llamacpp := config.ProviderConfig{ID: "qwen", Type: "llamacpp"}

	t.Run("unset leaves the request alone", func(t *testing.T) {
		opts := fantasy.ProviderOptions{}
		applyThinking(opts, llamacpp, &config.Config{})
		if len(opts) != 0 {
			t.Fatalf("options were added: %v", opts)
		}
	})
	t.Run("off becomes enable_thinking false", func(t *testing.T) {
		opts := fantasy.ProviderOptions{}
		applyThinking(opts, llamacpp, &config.Config{Options: &config.Options{Thinking: &off}})
		kw, ok := thinkingKwargs(t, opts)
		if !ok || kw["enable_thinking"] != false {
			t.Fatalf("kwargs = %v (present %v)", kw, ok)
		}
	})
	t.Run("on keeps other kwargs and existing extra_body", func(t *testing.T) {
		opts := fantasy.ProviderOptions{openaicompat.Name: &openaicompat.ProviderOptions{ExtraBody: map[string]any{
			"top_k": 20, "chat_template_kwargs": map[string]any{"other": 1},
		}}}
		applyThinking(opts, llamacpp, &config.Config{Options: &config.Options{Thinking: &on}})
		po := opts[openaicompat.Name].(*openaicompat.ProviderOptions)
		kw, _ := thinkingKwargs(t, opts)
		if po.ExtraBody["top_k"] != 20 || kw["other"] != 1 || kw["enable_thinking"] != true {
			t.Fatalf("extra_body = %v", po.ExtraBody)
		}
	})
	t.Run("a non OpenAI-compatible provider is untouched", func(t *testing.T) {
		opts := fantasy.ProviderOptions{}
		applyThinking(opts, config.ProviderConfig{ID: "anthropic", Type: "anthropic"}, &config.Config{Options: &config.Options{Thinking: &off}})
		if len(opts) != 0 {
			t.Fatalf("options were added: %v", opts)
		}
	})
}
