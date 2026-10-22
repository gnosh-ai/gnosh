package config

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// A `models` key Crush never reads would sit in the config looking
// configured while Crush quietly used its default model, so it is a load
// error rather than a silent fallback.
func TestValidateModelsRejectsUnknownTypes(t *testing.T) {
	t.Parallel()

	for _, bad := range []SelectedModelType{"medium", "Large", "LARGE", "huge", "", "default"} {
		t.Run(string(bad), func(t *testing.T) {
			t.Parallel()
			cfg := &Config{
				Models: map[SelectedModelType]SelectedModel{
					bad: {Provider: "openai", Model: "gpt-4o"},
				},
			}
			err := cfg.ValidateModels()
			require.Error(t, err)
			require.Contains(t, err.Error(), "unknown model type")
			require.Contains(t, err.Error(), "large")
			require.Contains(t, err.Error(), "small")
		})
	}
}

func TestValidateModelsAcceptsKnownTypes(t *testing.T) {
	t.Parallel()

	cfg := &Config{Models: map[SelectedModelType]SelectedModel{}}
	for _, known := range KnownModelTypes() {
		cfg.Models[known] = SelectedModel{Provider: "openai", Model: "gpt-4o"}
	}
	require.NoError(t, cfg.ValidateModels())
	require.Len(t, cfg.Models, len(KnownModelTypes()))
}

func TestValidateModelsEmpty(t *testing.T) {
	t.Parallel()

	require.NoError(t, (&Config{}).ValidateModels())
	require.NoError(t, (&Config{Models: map[SelectedModelType]SelectedModel{}}).ValidateModels())
}

// recent_models lives in the data directory and is written by Crush, not
// hand-edited, so a stale entry there must not block startup.
func TestValidateModelsIgnoresRecentModels(t *testing.T) {
	t.Parallel()

	cfg := &Config{
		Models: map[SelectedModelType]SelectedModel{
			SelectedModelTypeLarge: {Provider: "openai", Model: "gpt-4o"},
		},
		RecentModels: map[SelectedModelType][]SelectedModel{
			"medium": {{Provider: "openai", Model: "gpt-4o"}},
		},
	}
	require.NoError(t, cfg.ValidateModels())
}

// Two bad keys must produce the same message every run.
func TestValidateModelsErrorIsDeterministic(t *testing.T) {
	t.Parallel()

	cfg := &Config{
		Models: map[SelectedModelType]SelectedModel{
			"zebra": {Provider: "openai", Model: "gpt-4o"},
			"alpha": {Provider: "openai", Model: "gpt-4o"},
		},
	}
	for range 10 {
		err := cfg.ValidateModels()
		require.Error(t, err)
		require.Contains(t, err.Error(), `models "alpha"`,
			"map iteration order must not leak into the error message")
	}
}

func TestIsKnownModelType(t *testing.T) {
	t.Parallel()

	require.True(t, IsKnownModelType(SelectedModelTypeLarge))
	require.True(t, IsKnownModelType(SelectedModelTypeSmall))
	require.False(t, IsKnownModelType("medium"))
	require.False(t, IsKnownModelType(""))
}

// KnownModelTypes hands out a copy; a caller mutating it must not be
// able to change what Crush accepts.
func TestKnownModelTypesIsACopy(t *testing.T) {
	t.Parallel()

	got := KnownModelTypes()
	got[0] = "tampered"
	require.True(t, IsKnownModelType(SelectedModelTypeLarge))
	require.False(t, IsKnownModelType("tampered"))
}
