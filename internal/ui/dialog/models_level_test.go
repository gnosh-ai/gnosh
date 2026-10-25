package dialog

import (
	"testing"

	"charm.land/catwalk/pkg/catwalk"
	"github.com/charmbracelet/crush/internal/config"
	"github.com/charmbracelet/crush/internal/ui/common"
	"github.com/stretchr/testify/require"
)

func levelItem(t *testing.T, modelID string) *ModelItem {
	t.Helper()
	sty := common.LoadThemeStyles("")
	prov := catwalk.Provider{ID: "p", Name: "P"}
	return NewModelItem(&sty, prov, catwalk.Model{ID: modelID}, ModelTypeLarge, false)
}

// The arrows walk a gpt-oss up and down its effort scale and stop at each
// end rather than wrapping, so holding one settles on a level.
func TestStepLevelWalksTheEffortScaleAndStopsAtTheEnds(t *testing.T) {
	t.Parallel()

	item := levelItem(t, "gpt-oss-120b-abliterated")
	item.SetLevel("medium")

	got, ok := item.StepLevel(1)
	require.True(t, ok)
	require.Equal(t, "high", got)
	require.Equal(t, "high", item.Level())

	_, ok = item.StepLevel(1)
	require.False(t, ok, "the top of the scale must not wrap")
	require.Equal(t, "high", item.Level())

	for range 2 {
		_, ok = item.StepLevel(-1)
		require.True(t, ok)
	}
	require.Equal(t, "low", item.Level())

	_, ok = item.StepLevel(-1)
	require.False(t, ok, "the bottom of the scale must not wrap")
	require.Equal(t, "low", item.Level())
}

func TestStepLevelFlipsTheQwenThinkSwitch(t *testing.T) {
	t.Parallel()

	item := levelItem(t, "qwen3.8-27b")
	item.SetLevel("think")
	require.Equal(t, config.ReasoningThink, item.ReasoningKind())

	got, ok := item.StepLevel(-1)
	require.True(t, ok)
	require.Equal(t, "nothink", got)

	got, ok = item.StepLevel(1)
	require.True(t, ok)
	require.Equal(t, "think", got)
}

// A model with no reasoning axis must ignore the arrows rather than panic
// or invent a level.
func TestStepLevelIsANoOpWithoutAnAxis(t *testing.T) {
	t.Parallel()

	item := levelItem(t, "llama-3.1-8b")
	require.Equal(t, config.ReasoningNone, item.ReasoningKind())
	require.Equal(t, "", item.Level())

	for _, delta := range []int{1, -1} {
		got, ok := item.StepLevel(delta)
		require.False(t, ok)
		require.Equal(t, "", got)
		require.Equal(t, "", item.Level())
	}
}

// Selecting an effort model must carry the level the arrows set, so the
// choice survives into the selected-model config.
func TestSelectedModelCarriesTheChosenEffort(t *testing.T) {
	t.Parallel()

	item := levelItem(t, "gpt-oss-120b-abliterated")
	item.SetLevel("low")
	require.Equal(t, "low", item.SelectedModel().ReasoningEffort)

	// A think model has no effort to carry; the field stays the model's own.
	qwen := levelItem(t, "qwen3.8-27b")
	qwen.SetLevel("nothink")
	require.Equal(t, "", qwen.SelectedModel().ReasoningEffort)
}

// The row shows the level, with arrows on the focused row to say the keys
// move it.
func TestRenderShowsTheLevel(t *testing.T) {
	t.Parallel()

	item := levelItem(t, "gpt-oss-120b-abliterated")
	item.SetLevel("high")

	require.Contains(t, item.Render(60), "high")

	item.SetFocused(true)
	focused := item.Render(60)
	require.Contains(t, focused, "high")
	require.Contains(t, focused, "‹", "the focused row shows the left arrow")
	require.Contains(t, focused, "›", "the focused row shows the right arrow")

	// A model with no axis shows no level and no arrows.
	plain := levelItem(t, "llama-3.1-8b")
	plain.SetFocused(true)
	require.NotContains(t, plain.Render(60), "‹")
}
