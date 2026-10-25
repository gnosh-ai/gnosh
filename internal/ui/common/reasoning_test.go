package common

import (
	"testing"

	"charm.land/catwalk/pkg/catwalk"
	"github.com/stretchr/testify/require"

	"github.com/charmbracelet/crush/internal/config"
)

// cfgWith returns a config holding the given "provider/model" -> level
// pairs, the way the /model arrows store them.
func cfgWith(levels map[string]string) *config.Config {
	return &config.Config{Options: &config.Options{Reasoning: levels}}
}

func TestReasoningSummary(t *testing.T) {
	t.Parallel()

	// Jae's two models, both added by hand in crushrc: no catalog entry, so
	// CanReason is false and ReasoningLevels is empty. The axis comes from
	// the id alone.
	gptoss := catwalk.Model{ID: "gpt-oss-120b-abliterated", Name: "gpt-oss-120b abliterated"}
	qwen := catwalk.Model{ID: "qwen3.8-27b", Name: "qwen3.8-27b abliterated"}

	tests := []struct {
		name  string
		cfg   *config.Config
		sel   config.SelectedModel
		model catwalk.Model
		want  string
	}{
		{
			name:  "local gpt-oss with no level chosen falls back to its default",
			cfg:   cfgWith(nil),
			sel:   config.SelectedModel{Provider: "gptoss", Model: gptoss.ID},
			model: gptoss,
			want:  "Reasoning Medium",
		},
		{
			name:  "local gpt-oss shows the level the arrows stored",
			cfg:   cfgWith(map[string]string{"gptoss/gpt-oss-120b-abliterated": config.ReasoningLow}),
			sel:   config.SelectedModel{Provider: "gptoss", Model: gptoss.ID},
			model: gptoss,
			want:  "Reasoning Low",
		},
		{
			name:  "local gpt-oss at high",
			cfg:   cfgWith(map[string]string{"gptoss/gpt-oss-120b-abliterated": config.ReasoningHigh}),
			sel:   config.SelectedModel{Provider: "gptoss", Model: gptoss.ID},
			model: gptoss,
			want:  "Reasoning High",
		},
		{
			name:  "local qwen with no level chosen is thinking on",
			cfg:   cfgWith(nil),
			sel:   config.SelectedModel{Provider: "qwen", Model: qwen.ID},
			model: qwen,
			want:  "Thinking On",
		},
		{
			name:  "local qwen set to nothink",
			cfg:   cfgWith(map[string]string{"qwen/qwen3.8-27b": config.ReasoningNoThink}),
			sel:   config.SelectedModel{Provider: "qwen", Model: qwen.ID},
			model: qwen,
			want:  "Thinking Off",
		},
		{
			name:  "local qwen set back to think",
			cfg:   cfgWith(map[string]string{"qwen/qwen3.8-27b": config.ReasoningThinkOn}),
			sel:   config.SelectedModel{Provider: "qwen", Model: qwen.ID},
			model: qwen,
			want:  "Thinking On",
		},
		{
			name:  "a model with no axis and no catalog reasoning says nothing",
			cfg:   cfgWith(nil),
			sel:   config.SelectedModel{Provider: "local", Model: "llama-3-8b"},
			model: catwalk.Model{ID: "llama-3-8b"},
			want:  "",
		},
		{
			name: "an explicit --reasoning-levels model is driven by its own levels",
			cfg: cfgWith(map[string]string{
				"local/house-model": "high",
			}),
			sel: config.SelectedModel{Provider: "local", Model: "house-model"},
			model: catwalk.Model{
				ID:              "house-model",
				ReasoningLevels: []string{"low", "medium", "high"},
			},
			want: "Reasoning High",
		},
		{
			name: "a catalog thinking model keeps the older wording, on",
			cfg:  cfgWith(nil),
			sel:  config.SelectedModel{Provider: "anthropic", Model: "claude", Think: true},
			model: catwalk.Model{
				ID:        "claude",
				CanReason: true,
			},
			want: "Thinking On",
		},
		{
			name: "a catalog thinking model keeps the older wording, off",
			cfg:  cfgWith(nil),
			sel:  config.SelectedModel{Provider: "anthropic", Model: "claude"},
			model: catwalk.Model{
				ID:        "claude",
				CanReason: true,
			},
			want: "Thinking Off",
		},
		{
			name: "a catalog effort model reads the effort on the selection",
			cfg:  cfgWith(nil),
			sel: config.SelectedModel{
				Provider:        "openai",
				Model:           "o3",
				ReasoningEffort: "high",
			},
			model: catwalk.Model{
				ID:              "o3",
				CanReason:       true,
				ReasoningLevels: []string{"low", "medium", "high"},
			},
			want: "Reasoning High",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			require.Equal(t, tt.want, ReasoningSummary(tt.cfg, tt.sel, tt.model))
		})
	}
}

// A model whose id names an axis must report that axis even when the
// catalog says it cannot reason. This is the gate that hid every locally
// added model's level before.
func TestReasoningSummaryIgnoresCanReasonForAxisModels(t *testing.T) {
	t.Parallel()

	model := catwalk.Model{ID: "gpt-oss-120b-abliterated", CanReason: false}
	sel := config.SelectedModel{Provider: "gptoss", Model: model.ID}
	cfg := cfgWith(map[string]string{"gptoss/gpt-oss-120b-abliterated": config.ReasoningHigh})

	require.Equal(t, "Reasoning High", ReasoningSummary(cfg, sel, model),
		"a local model's level must show even though catwalk never heard of it")
}

func TestFormatReasoningLevel(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		kind  config.ReasoningKind
		level string
		want  string
	}{
		{"no axis renders nothing", config.ReasoningNone, "", ""},
		{"no axis ignores a stray level", config.ReasoningNone, "high", ""},
		{"an axis with no level renders nothing", config.ReasoningEffort, "", ""},
		{"effort low", config.ReasoningEffort, config.ReasoningLow, "Reasoning Low"},
		{"effort medium", config.ReasoningEffort, config.ReasoningMedium, "Reasoning Medium"},
		{"effort high", config.ReasoningEffort, config.ReasoningHigh, "Reasoning High"},
		{"effort xhigh keeps its spelling", config.ReasoningEffort, "xhigh", "Reasoning X-High"},
		{"think off", config.ReasoningThink, config.ReasoningNoThink, "Thinking Off"},
		{"think on", config.ReasoningThink, config.ReasoningThinkOn, "Thinking On"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			require.Equal(t, tt.want, FormatReasoningLevel(tt.kind, tt.level))
		})
	}
}
