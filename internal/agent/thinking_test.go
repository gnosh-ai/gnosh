package agent

import (
	"testing"

	"charm.land/catwalk/pkg/catwalk"
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

var (
	qwenModel   = catwalk.Model{ID: "qwen3.8-27b"}
	gptossModel = catwalk.Model{ID: "gpt-oss-120b-abliterated"}
	plainModel  = catwalk.Model{ID: "llama-3.1-8b"}
)

func cfgWith(level map[string]string) *config.Config {
	return &config.Config{Options: &config.Options{Reasoning: level}}
}

// A Qwen takes the boolean switch; a gpt-oss takes an effort level and
// ignores enable_thinking entirely, which is why the old global switch did
// nothing on it.
func TestApplyReasoningWritesTheSpellingTheModelUnderstands(t *testing.T) {
	t.Parallel()

	llamacpp := config.ProviderConfig{ID: "p", Type: "llamacpp"}

	tests := []struct {
		name     string
		model    catwalk.Model
		provider string
		stored   map[string]string
		wantKey  string
		wantVal  any
		wantNone bool
	}{
		{
			name:     "qwen set to nothink",
			model:    qwenModel,
			provider: "qwen",
			stored:   map[string]string{"qwen/qwen3.8-27b": "nothink"},
			wantKey:  "enable_thinking",
			wantVal:  false,
		},
		{
			name:     "qwen set to think",
			model:    qwenModel,
			provider: "qwen",
			stored:   map[string]string{"qwen/qwen3.8-27b": "think"},
			wantKey:  "enable_thinking",
			wantVal:  true,
		},
		{
			name:     "gpt-oss set to low",
			model:    gptossModel,
			provider: "gptoss",
			stored:   map[string]string{"gptoss/gpt-oss-120b-abliterated": "low"},
			wantKey:  "reasoning_effort",
			wantVal:  "low",
		},
		{
			name:     "gpt-oss set to high",
			model:    gptossModel,
			provider: "gptoss",
			stored:   map[string]string{"gptoss/gpt-oss-120b-abliterated": "high"},
			wantKey:  "reasoning_effort",
			wantVal:  "high",
		},
		{
			name:     "an unchosen gpt-oss still carries its default effort",
			model:    gptossModel,
			provider: "gptoss",
			stored:   nil,
			wantKey:  "reasoning_effort",
			wantVal:  "medium",
		},
		{
			name:     "a model with no reasoning axis is left alone",
			model:    plainModel,
			provider: "meta",
			stored:   nil,
			wantNone: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			opts := fantasy.ProviderOptions{}
			applyReasoning(opts, tt.model, tt.provider, llamacpp, cfgWith(tt.stored))
			if tt.wantNone {
				if len(opts) != 0 {
					t.Fatalf("options were added for a model with no axis: %v", opts)
				}
				return
			}
			kw, ok := thinkingKwargs(t, opts)
			if !ok {
				t.Fatalf("no chat_template_kwargs: %v", opts)
			}
			if kw[tt.wantKey] != tt.wantVal {
				t.Fatalf("kwargs = %v, want %s=%v", kw, tt.wantKey, tt.wantVal)
			}
			// Only the key its own kind uses is written.
			other := "enable_thinking"
			if tt.wantKey == other {
				other = "reasoning_effort"
			}
			if _, present := kw[other]; present {
				t.Fatalf("%s was also written: %v", other, kw)
			}
		})
	}
}

func TestApplyReasoningKeepsOtherExtraBody(t *testing.T) {
	t.Parallel()

	opts := fantasy.ProviderOptions{openaicompat.Name: &openaicompat.ProviderOptions{ExtraBody: map[string]any{
		"top_k": 20, "chat_template_kwargs": map[string]any{"other": 1},
	}}}
	applyReasoning(opts, qwenModel, "qwen", config.ProviderConfig{ID: "qwen", Type: "llamacpp"},
		cfgWith(map[string]string{"qwen/qwen3.8-27b": "think"}))

	po := opts[openaicompat.Name].(*openaicompat.ProviderOptions)
	kw, _ := thinkingKwargs(t, opts)
	if po.ExtraBody["top_k"] != 20 || kw["other"] != 1 || kw["enable_thinking"] != true {
		t.Fatalf("extra_body = %v", po.ExtraBody)
	}
}

func TestApplyReasoningLeavesNonOpenAICompatibleProvidersAlone(t *testing.T) {
	t.Parallel()

	opts := fantasy.ProviderOptions{}
	applyReasoning(opts, qwenModel, "qwen", config.ProviderConfig{ID: "anthropic", Type: "anthropic"},
		cfgWith(map[string]string{"qwen/qwen3.8-27b": "nothink"}))
	if len(opts) != 0 {
		t.Fatalf("options were added: %v", opts)
	}
}

// quietReasoning is for requests whose answer is a few words, such as a
// session title. A Qwen can be switched off; a gpt-oss cannot — harmony
// always emits an analysis channel — so it must be lowered to low effort
// rather than sent an enable_thinking it ignores.
func TestQuietReasoning(t *testing.T) {
	t.Parallel()

	for _, typ := range []string{"llamacpp", openaicompat.Name} {
		opts := fantasy.ProviderOptions{}
		quietReasoning(opts, qwenModel, config.ProviderConfig{ID: "p", Type: catwalk.Type(typ)})
		kw, ok := thinkingKwargs(t, opts)
		if !ok || kw["enable_thinking"] != false {
			t.Fatalf("%s: qwen kwargs = %v (present %v)", typ, kw, ok)
		}

		opts = fantasy.ProviderOptions{}
		quietReasoning(opts, gptossModel, config.ProviderConfig{ID: "p", Type: catwalk.Type(typ)})
		kw, ok = thinkingKwargs(t, opts)
		if !ok || kw["reasoning_effort"] != "low" {
			t.Fatalf("%s: gpt-oss kwargs = %v (present %v)", typ, kw, ok)
		}
		if _, present := kw["enable_thinking"]; present {
			t.Fatalf("%s: gpt-oss was sent enable_thinking, which it ignores: %v", typ, kw)
		}
	}

	// A model whose id names no axis still gets the blanket switch: an
	// unrecognised Qwen would otherwise reason through its title budget.
	opts := fantasy.ProviderOptions{}
	quietReasoning(opts, plainModel, config.ProviderConfig{ID: "p", Type: "llamacpp"})
	kw, ok := thinkingKwargs(t, opts)
	if !ok || kw["enable_thinking"] != false {
		t.Fatalf("a model with no known axis must still get enable_thinking false: %v (present %v)", kw, ok)
	}

	opts = fantasy.ProviderOptions{}
	quietReasoning(opts, qwenModel, config.ProviderConfig{ID: "a", Type: "anthropic"})
	if len(opts) != 0 {
		t.Fatalf("options were added for a provider without the switch: %v", opts)
	}
}
