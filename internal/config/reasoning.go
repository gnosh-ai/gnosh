package config

import (
	"slices"
	"strings"

	"charm.land/catwalk/pkg/catwalk"
)

// ReasoningKey is the config field the per-model reasoning levels are stored
// under: an object keyed by "provider/model", each holding one of the
// model's own levels.
const ReasoningKey = "options.reasoning"

// ReasoningKind says how a model's reasoning is asked for on the wire.
// Servers disagree: Qwen's chat template takes a boolean switch, gpt-oss
// (OpenAI's harmony format) takes an effort level and cannot be silenced at
// all. One axis in the UI, two spellings in the request.
type ReasoningKind int

const (
	// ReasoningNone is a model with no reasoning axis. The arrows in the
	// model dialog do nothing and no reasoning key is sent.
	ReasoningNone ReasoningKind = iota
	// ReasoningEffort is a harmony-format model (gpt-oss): levels low,
	// medium and high, sent as chat_template_kwargs.reasoning_effort.
	// Such a model always reasons; low is as quiet as it gets.
	ReasoningEffort
	// ReasoningThink is a Qwen-style model: levels nothink and think,
	// sent as chat_template_kwargs.enable_thinking.
	ReasoningThink
)

// Reasoning level names. The effort names are harmony's own; the think
// names match what /think and /no_think used to spell.
const (
	ReasoningLow     = "low"
	ReasoningMedium  = "medium"
	ReasoningHigh    = "high"
	ReasoningNoThink = "nothink"
	ReasoningThinkOn = "think"
)

var (
	effortLevels = []string{ReasoningLow, ReasoningMedium, ReasoningHigh}
	thinkLevels  = []string{ReasoningNoThink, ReasoningThinkOn}
)

// ReasoningAxis reports how a model's reasoning is controlled and the levels
// it offers, lowest first.
//
// A model says so itself through catwalk's ReasoningLevels, which `model add
// --reasoning-levels` writes. With none set the id decides: a harmony model
// (gpt-oss) takes an effort level, a Qwen takes the think switch, and
// anything else has no axis. Guessing from the id is what keeps an existing
// crushrc working without being edited.
func ReasoningAxis(model catwalk.Model) (ReasoningKind, []string) {
	if len(model.ReasoningLevels) > 0 {
		return kindOfLevels(model.ReasoningLevels), slices.Clone(model.ReasoningLevels)
	}
	id := strings.ToLower(model.ID)
	switch {
	case strings.Contains(id, "gpt-oss"), strings.Contains(id, "harmony"):
		return ReasoningEffort, slices.Clone(effortLevels)
	case strings.Contains(id, "qwen"):
		return ReasoningThink, slices.Clone(thinkLevels)
	}
	return ReasoningNone, nil
}

// kindOfLevels reads the kind out of an explicit level list: the think
// switch is named by its two levels, anything else is an effort scale.
func kindOfLevels(levels []string) ReasoningKind {
	if slices.Contains(levels, ReasoningThinkOn) || slices.Contains(levels, ReasoningNoThink) {
		return ReasoningThink
	}
	return ReasoningEffort
}

// ReasoningModelKey is the key a model's level is stored under, as
// "provider/model".
func ReasoningModelKey(provider, model string) string {
	return provider + "/" + model
}

// ReasoningField is the full config path for one model's level, with the
// dots in the key escaped so sjson treats "qwen3.8-27b" as one name rather
// than a nested object.
func ReasoningField(provider, model string) string {
	return ReasoningKey + "." + strings.ReplaceAll(ReasoningModelKey(provider, model), ".", `\.`)
}

// ReasoningLevel returns the level stored for a model. set is false when the
// user never chose one, and the model's own default then stands.
func (c *Config) ReasoningLevel(provider, model string) (level string, set bool) {
	if c == nil || c.Options == nil || c.Options.Reasoning == nil {
		return "", false
	}
	level, set = c.Options.Reasoning[ReasoningModelKey(provider, model)]
	if level == "" {
		return "", false
	}
	return level, set
}

// ReasoningLevelOrDefault returns the level in force for a model: the one
// the user chose, else the model's own default, else the middle of its
// levels. It returns "" for a model with no reasoning axis.
func (c *Config) ReasoningLevelOrDefault(provider string, model catwalk.Model) string {
	kind, levels := ReasoningAxis(model)
	if kind == ReasoningNone {
		return ""
	}
	if level, set := c.ReasoningLevel(provider, model.ID); set && slices.Contains(levels, level) {
		return level
	}
	if d := model.DefaultReasoningEffort; d != "" && slices.Contains(levels, d) {
		return d
	}
	return defaultLevel(kind, levels)
}

// ReasoningLevelForSelected returns the level in force for the model a
// selection points at, which is what the sidebar reports.
//
// It prefers the per-model level the /model arrows store, so the sidebar
// and the dialog can never disagree. Failing that it takes the effort left
// on the selection itself, which is what the older reasoning-effort dialog
// writes and what [ModelItem.SelectedModel] carries over when a model is
// picked. Only then does it fall back to the model's own default. It
// returns "" for a model with no reasoning axis.
func (c *Config) ReasoningLevelForSelected(sel SelectedModel, model catwalk.Model) string {
	kind, levels := ReasoningAxis(model)
	if kind == ReasoningNone {
		return ""
	}
	if level, set := c.ReasoningLevel(sel.Provider, model.ID); set && slices.Contains(levels, level) {
		return level
	}
	if slices.Contains(levels, sel.ReasoningEffort) {
		return sel.ReasoningEffort
	}
	return c.ReasoningLevelOrDefault(sel.Provider, model)
}

// defaultLevel is where a model's axis sits before anything is chosen: the
// middle of an effort scale (harmony's own default is medium), and thinking
// on for a think switch, which is the server's default too.
func defaultLevel(kind ReasoningKind, levels []string) string {
	if len(levels) == 0 {
		return ""
	}
	if kind == ReasoningThink {
		return ReasoningThinkOn
	}
	return levels[len(levels)/2]
}

// StepReasoningLevel moves one step along a model's levels and returns the
// new one. delta is +1 for the right arrow and -1 for the left. It stops at
// each end rather than wrapping, so holding an arrow settles on a level
// instead of cycling past it. ok is false for a model with no axis, or when
// the step would run off an end.
func StepReasoningLevel(current string, delta int, kind ReasoningKind, levels []string) (string, bool) {
	if kind == ReasoningNone || len(levels) == 0 {
		return "", false
	}
	i := slices.Index(levels, current)
	if i < 0 {
		i = slices.Index(levels, defaultLevel(kind, levels))
		if i < 0 {
			i = 0
		}
	}
	next := i + delta
	if next < 0 || next >= len(levels) {
		return levels[i], false
	}
	return levels[next], true
}
