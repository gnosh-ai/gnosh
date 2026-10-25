package config

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/tidwall/sjson"
)

// The level is written through sjson under a path built by ReasoningField.
// A model id with a dot in it is the case that breaks a naive path, so the
// round trip is checked the way the config store does it: write the field,
// parse the file back, read the level.
func TestReasoningLevelRoundTripsThroughTheConfigFile(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		provider string
		model    string
		level    string
	}{
		{"a model id with a dot", "qwen", "qwen3.8-27b", "nothink"},
		{"a model id with dashes", "gptoss", "gpt-oss-120b-abliterated", "high"},
		{"a model id with a slash", "openrouter", "meta/llama-3.1", "low"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			data, err := sjson.Set("{}", ReasoningField(tt.provider, tt.model), tt.level)
			require.NoError(t, err)

			var cfg Config
			require.NoError(t, json.Unmarshal([]byte(data), &cfg))

			got, set := cfg.ReasoningLevel(tt.provider, tt.model)
			require.True(t, set, "level not found after round trip; file was %s", data)
			require.Equal(t, tt.level, got)
		})
	}
}

// Two models under one provider must not collide, and writing one must not
// disturb the other.
func TestReasoningLevelsAreStoredPerModel(t *testing.T) {
	t.Parallel()

	data, err := sjson.Set("{}", ReasoningField("qwen", "qwen3.8-27b"), "nothink")
	require.NoError(t, err)
	data, err = sjson.Set(data, ReasoningField("gptoss", "gpt-oss-120b-abliterated"), "low")
	require.NoError(t, err)

	var cfg Config
	require.NoError(t, json.Unmarshal([]byte(data), &cfg))

	qwen, set := cfg.ReasoningLevel("qwen", "qwen3.8-27b")
	require.True(t, set)
	require.Equal(t, "nothink", qwen)

	gptoss, set := cfg.ReasoningLevel("gptoss", "gpt-oss-120b-abliterated")
	require.True(t, set)
	require.Equal(t, "low", gptoss)

	_, set = cfg.ReasoningLevel("qwen", "never-chosen")
	require.False(t, set)
}
