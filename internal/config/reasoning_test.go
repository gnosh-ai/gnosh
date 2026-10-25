package config

import (
	"testing"

	"charm.land/catwalk/pkg/catwalk"
	"github.com/stretchr/testify/require"
)

func TestReasoningAxis(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		model     catwalk.Model
		wantKind  ReasoningKind
		wantLevel []string
	}{
		{
			name:      "gpt-oss takes an effort level",
			model:     catwalk.Model{ID: "gpt-oss-120b-abliterated"},
			wantKind:  ReasoningEffort,
			wantLevel: []string{"low", "medium", "high"},
		},
		{
			name:      "qwen takes the think switch",
			model:     catwalk.Model{ID: "qwen3.8-27b"},
			wantKind:  ReasoningThink,
			wantLevel: []string{"nothink", "think"},
		},
		{
			name:      "a model that says nothing and is named nothing has no axis",
			model:     catwalk.Model{ID: "llama-3.1-8b"},
			wantKind:  ReasoningNone,
			wantLevel: nil,
		},
		{
			name:      "an explicit level list wins over the id",
			model:     catwalk.Model{ID: "qwen3.8-27b", ReasoningLevels: []string{"low", "high"}},
			wantKind:  ReasoningEffort,
			wantLevel: []string{"low", "high"},
		},
		{
			name:      "an explicit think list is a think switch",
			model:     catwalk.Model{ID: "house-blend", ReasoningLevels: []string{"nothink", "think"}},
			wantKind:  ReasoningThink,
			wantLevel: []string{"nothink", "think"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			kind, levels := ReasoningAxis(tt.model)
			require.Equal(t, tt.wantKind, kind)
			require.Equal(t, tt.wantLevel, levels)
		})
	}
}

func TestStepReasoningLevel(t *testing.T) {
	t.Parallel()

	effort := []string{"low", "medium", "high"}
	think := []string{"nothink", "think"}

	tests := []struct {
		name    string
		current string
		delta   int
		kind    ReasoningKind
		levels  []string
		want    string
		wantOK  bool
	}{
		{"right moves up the effort scale", "low", 1, ReasoningEffort, effort, "medium", true},
		{"left moves down the effort scale", "high", -1, ReasoningEffort, effort, "medium", true},
		{"the top of the scale does not wrap", "high", 1, ReasoningEffort, effort, "high", false},
		{"the bottom of the scale does not wrap", "low", -1, ReasoningEffort, effort, "low", false},
		{"the think switch turns on", "nothink", 1, ReasoningThink, think, "think", true},
		{"the think switch turns off", "think", -1, ReasoningThink, think, "nothink", true},
		{"a model with no axis never steps", "", 1, ReasoningNone, nil, "", false},
		{"an unknown current level starts from the default", "bogus", 1, ReasoningEffort, effort, "high", true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, ok := StepReasoningLevel(tt.current, tt.delta, tt.kind, tt.levels)
			require.Equal(t, tt.wantOK, ok)
			require.Equal(t, tt.want, got)
		})
	}
}

func TestReasoningLevelOrDefault(t *testing.T) {
	t.Parallel()

	gptoss := catwalk.Model{ID: "gpt-oss-120b-abliterated"}
	qwen := catwalk.Model{ID: "qwen3.8-27b"}
	plain := catwalk.Model{ID: "llama-3.1-8b"}

	t.Run("an unchosen effort model sits at medium", func(t *testing.T) {
		t.Parallel()
		cfg := &Config{Options: &Options{}}
		require.Equal(t, "medium", cfg.ReasoningLevelOrDefault("gptoss", gptoss))
	})

	t.Run("an unchosen think model thinks, as the server would", func(t *testing.T) {
		t.Parallel()
		cfg := &Config{Options: &Options{}}
		require.Equal(t, "think", cfg.ReasoningLevelOrDefault("qwen", qwen))
	})

	t.Run("a model with no axis has no level", func(t *testing.T) {
		t.Parallel()
		cfg := &Config{Options: &Options{}}
		require.Equal(t, "", cfg.ReasoningLevelOrDefault("meta", plain))
	})

	t.Run("the stored level wins", func(t *testing.T) {
		t.Parallel()
		cfg := &Config{Options: &Options{Reasoning: map[string]string{
			"gptoss/gpt-oss-120b-abliterated": "low",
		}}}
		require.Equal(t, "low", cfg.ReasoningLevelOrDefault("gptoss", gptoss))
	})

	t.Run("a stored level that is not one of the model's own is ignored", func(t *testing.T) {
		t.Parallel()
		cfg := &Config{Options: &Options{Reasoning: map[string]string{
			"gptoss/gpt-oss-120b-abliterated": "nothink",
		}}}
		require.Equal(t, "medium", cfg.ReasoningLevelOrDefault("gptoss", gptoss))
	})

	t.Run("the model's own default is used when nothing is stored", func(t *testing.T) {
		t.Parallel()
		cfg := &Config{Options: &Options{}}
		m := catwalk.Model{ID: "gpt-oss-120b", DefaultReasoningEffort: "high"}
		require.Equal(t, "high", cfg.ReasoningLevelOrDefault("gptoss", m))
	})

	t.Run("a nil config does not panic", func(t *testing.T) {
		t.Parallel()
		var cfg *Config
		require.Equal(t, "medium", cfg.ReasoningLevelOrDefault("gptoss", gptoss))
	})
}

// A model id with a dot in it ("qwen3.8-27b") must stay one key: sjson
// would otherwise read the dot as a path separator and nest the level
// under an object named "qwen3".
func TestReasoningFieldEscapesDotsInTheModelID(t *testing.T) {
	t.Parallel()

	require.Equal(t, `options.reasoning.qwen/qwen3\.8-27b`, ReasoningField("qwen", "qwen3.8-27b"))
	require.Equal(t, "options.reasoning.gptoss/gpt-oss-120b", ReasoningField("gptoss", "gpt-oss-120b"))
	require.Equal(t, "qwen/qwen3.8-27b", ReasoningModelKey("qwen", "qwen3.8-27b"))
}

func TestThinkingLevel(t *testing.T) {
	t.Parallel()

	effort := []string{"low", "medium", "high"}
	think := []string{"nothink", "think"}

	require.Equal(t, "high", ThinkingLevel(true, ReasoningEffort, effort),
		"/think asks for as much reasoning as the model offers")
	require.Equal(t, "low", ThinkingLevel(false, ReasoningEffort, effort),
		"/no_think asks for as little as it offers, which for harmony is not none")
	require.Equal(t, "think", ThinkingLevel(true, ReasoningThink, think))
	require.Equal(t, "nothink", ThinkingLevel(false, ReasoningThink, think))
	require.Equal(t, "", ThinkingLevel(true, ReasoningNone, nil))
}
