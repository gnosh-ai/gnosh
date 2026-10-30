package common

import (
	"cmp"

	"charm.land/catwalk/pkg/catwalk"

	"github.com/charmbracelet/crush/internal/config"
)

// ReasoningSummary is the line the sidebar shows for how hard the current
// model thinks, or "" when the model has no reasoning to describe.
//
// Gnosh settles reasoning two different ways and the line has to follow
// whichever one actually governs the request:
//
//   - A model with a reasoning axis -- gpt-oss, Qwen, or anything given
//     --reasoning-levels -- is driven by the per-model level the /model
//     arrows store. That level is read here the same way the model dialog
//     reads it, so the sidebar and the dialog can never disagree.
//   - A catalog model that only advertises CanReason is still driven by the
//     older per-selection Think and ReasoningEffort fields, which the agent
//     reads for Anthropic and the OpenAI families. It keeps the older
//     wording.
//
// Nothing else reasons in a way we can name, and gets no line. Crucially
// the axis is tried first and is not gated on CanReason: a model added by
// hand in crushrc has no catalog entry, so gating on CanReason left a local
// model's level invisible.
func ReasoningSummary(cfg *config.Config, sel config.SelectedModel, model catwalk.Model) string {
	if kind, _ := config.ReasoningAxis(model); kind != config.ReasoningNone {
		return FormatReasoningLevel(kind, cfg.ReasoningLevelForSelected(sel, model))
	}
	if !model.CanReason {
		return ""
	}
	if len(model.ReasoningLevels) > 0 {
		effort := cmp.Or(sel.ReasoningEffort, model.DefaultReasoningEffort)
		if effort == "" {
			return ""
		}
		return "Reasoning " + FormatReasoningEffort(effort)
	}
	if sel.Think {
		return "Thinking On"
	}
	return "Thinking Off"
}

// FormatReasoningLevel renders one level of a model's reasoning axis for
// display: an effort scale reads "Reasoning High", a think switch reads
// "Thinking On" or "Thinking Off". A model with no axis, or no level,
// renders nothing.
func FormatReasoningLevel(kind config.ReasoningKind, level string) string {
	if kind == config.ReasoningNone || level == "" {
		return ""
	}
	if kind == config.ReasoningThink {
		if level == config.ReasoningNoThink {
			return "Thinking Off"
		}
		return "Thinking On"
	}
	return "Reasoning " + FormatReasoningEffort(level)
}
