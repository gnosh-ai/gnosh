package config

import (
	"testing"

	"charm.land/catwalk/pkg/catwalk"
	"github.com/stretchr/testify/require"
)

func TestReasoningLevelForSelected(t *testing.T) {
	t.Parallel()

	gptoss := catwalk.Model{ID: "gpt-oss-120b-abliterated"}
	qwen := catwalk.Model{ID: "qwen3.8-27b"}

	tests := []struct {
		name  string
		cfg   *Config
		sel   SelectedModel
		model catwalk.Model
		want  string
	}{
		{
			name:  "no axis returns empty whatever is stored",
			cfg:   &Config{Options: &Options{Reasoning: map[string]string{"local/plain": "high"}}},
			sel:   SelectedModel{Provider: "local", Model: "plain", ReasoningEffort: "high"},
			model: catwalk.Model{ID: "plain"},
			want:  "",
		},
		{
			name:  "the stored level wins over the selection's effort",
			cfg:   &Config{Options: &Options{Reasoning: map[string]string{"gptoss/gpt-oss-120b-abliterated": ReasoningLow}}},
			sel:   SelectedModel{Provider: "gptoss", Model: gptoss.ID, ReasoningEffort: ReasoningHigh},
			model: gptoss,
			want:  ReasoningLow,
		},
		{
			name:  "the selection's effort is used when nothing is stored",
			cfg:   &Config{Options: &Options{}},
			sel:   SelectedModel{Provider: "gptoss", Model: gptoss.ID, ReasoningEffort: ReasoningHigh},
			model: gptoss,
			want:  ReasoningHigh,
		},
		{
			name:  "an effort that is not on the axis is ignored",
			cfg:   &Config{Options: &Options{}},
			sel:   SelectedModel{Provider: "qwen", Model: qwen.ID, ReasoningEffort: ReasoningHigh},
			model: qwen,
			want:  ReasoningThinkOn,
		},
		{
			name:  "a stored level that is not on the axis is ignored",
			cfg:   &Config{Options: &Options{Reasoning: map[string]string{"qwen/qwen3.8-27b": "medium"}}},
			sel:   SelectedModel{Provider: "qwen", Model: qwen.ID},
			model: qwen,
			want:  ReasoningThinkOn,
		},
		{
			name:  "falling all the way through gives the axis default",
			cfg:   &Config{Options: &Options{}},
			sel:   SelectedModel{Provider: "gptoss", Model: gptoss.ID},
			model: gptoss,
			want:  ReasoningMedium,
		},
		{
			name:  "a nil Options is safe",
			cfg:   &Config{},
			sel:   SelectedModel{Provider: "qwen", Model: qwen.ID},
			model: qwen,
			want:  ReasoningThinkOn,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			require.Equal(t, tt.want, tt.cfg.ReasoningLevelForSelected(tt.sel, tt.model))
		})
	}
}
