package dialog

import (
	"charm.land/catwalk/pkg/catwalk"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/crush/internal/config"
	"github.com/charmbracelet/crush/internal/ui/common"
	"github.com/charmbracelet/crush/internal/ui/list"
	"github.com/charmbracelet/crush/internal/ui/styles"
	"github.com/charmbracelet/x/ansi"
	"github.com/sahilm/fuzzy"
)

// ModelGroup represents a group of model items.
type ModelGroup struct {
	*list.Versioned
	Title      string
	Items      []*ModelItem
	configured bool
	t          *styles.Styles
}

// NewModelGroup creates a new ModelGroup.
func NewModelGroup(t *styles.Styles, title string, configured bool, items ...*ModelItem) ModelGroup {
	return ModelGroup{
		Versioned:  list.NewVersioned(),
		Title:      title,
		Items:      items,
		configured: configured,
		t:          t,
	}
}

// Finished implements list.Item. Model groups are immutable headers.
func (m *ModelGroup) Finished() bool {
	return true
}

// AppendItems appends [ModelItem]s to the group.
func (m *ModelGroup) AppendItems(items ...*ModelItem) {
	m.Items = append(m.Items, items...)
}

// Render implements [list.Item].
func (m *ModelGroup) Render(width int) string {
	var configured string
	if m.configured {
		configuredIcon := m.t.ToolCallSuccess.Render()
		configuredText := m.t.Dialog.Models.ConfiguredText.Render("Configured")
		configured = configuredIcon + " " + configuredText
	}

	title := " " + m.Title + " "
	// Keep the "Configured" badge only when the full title fits beside it
	// (plus a separator). Otherwise drop it and let the title use the whole
	// width, rather than truncating the title to reserve room for a badge
	// that common.Section would then drop anyway, leaving dead space.
	if configured != "" && lipgloss.Width(title)+lipgloss.Width(configured)+3 > width {
		configured = ""
	}
	if configured == "" {
		title = ansi.Truncate(title, max(0, width-1), "…")
	}

	return common.Section(m.t, title, width, configured)
}

// ModelItem represents a list item for a model type.
type ModelItem struct {
	*list.Versioned

	prov      catwalk.Provider
	model     catwalk.Model
	modelType ModelType

	cache        map[int]string
	t            *styles.Styles
	m            fuzzy.Match
	focused      bool
	showProvider bool

	// level is how hard this model thinks, one of the levels its axis
	// offers, or "" for a model with no axis. The left and right arrows
	// move it; see [ModelItem.StepLevel].
	level string
}

// ReasoningKind reports how this model's reasoning is controlled, and
// ReasoningLevels the levels it offers. A model with no axis reports
// [config.ReasoningNone] and no levels.
func (m *ModelItem) ReasoningKind() config.ReasoningKind {
	kind, _ := config.ReasoningAxis(m.model)
	return kind
}

// Level returns the model's current reasoning level, or "" when it has no
// reasoning axis.
func (m *ModelItem) Level() string { return m.level }

// SetLevel sets the model's reasoning level and redraws the row.
func (m *ModelItem) SetLevel(level string) {
	if m.level == level {
		return
	}
	m.cache = nil
	m.level = level
	if m.Versioned != nil {
		m.Bump()
	}
}

// StepLevel moves this model's reasoning one step along its own levels,
// +1 for the right arrow and -1 for the left, and reports the new level.
// ok is false when the model has no axis or the step runs off an end, and
// the level is then unchanged.
func (m *ModelItem) StepLevel(delta int) (string, bool) {
	kind, levels := config.ReasoningAxis(m.model)
	next, ok := config.StepReasoningLevel(m.level, delta, kind, levels)
	if !ok {
		return m.level, false
	}
	m.SetLevel(next)
	return next, true
}

// Finished implements list.Item. Model items are render-stable
// outside of explicit SetFocused / SetMatch.
func (m *ModelItem) Finished() bool {
	return true
}

// SelectedModel returns this model item as a [config.SelectedModel] instance.
// A model whose reasoning is an effort scale carries the chosen level, so
// selecting it keeps what the arrows set.
func (m *ModelItem) SelectedModel() config.SelectedModel {
	effort := m.model.DefaultReasoningEffort
	if m.ReasoningKind() == config.ReasoningEffort && m.level != "" {
		effort = m.level
	}
	return config.SelectedModel{
		Model:           m.model.ID,
		Provider:        string(m.prov.ID),
		ReasoningEffort: effort,
		MaxTokens:       m.model.DefaultMaxTokens,
	}
}

// SelectedModelType returns the type of model represented by this item.
func (m *ModelItem) SelectedModelType() config.SelectedModelType {
	return m.modelType.Config()
}

var _ ListItem = &ModelItem{}

// NewModelItem creates a new ModelItem.
func NewModelItem(t *styles.Styles, prov catwalk.Provider, model catwalk.Model, typ ModelType, showProvider bool) *ModelItem {
	return &ModelItem{
		Versioned:    list.NewVersioned(),
		prov:         prov,
		model:        model,
		modelType:    typ,
		t:            t,
		cache:        make(map[int]string),
		showProvider: showProvider,
	}
}

// Filter implements ListItem.
func (m *ModelItem) Filter() string {
	return m.model.Name
}

// ID implements ListItem.
func (m *ModelItem) ID() string {
	return modelKey(string(m.prov.ID), m.model.ID)
}

// Render implements ListItem.
func (m *ModelItem) Render(width int) string {
	var providerInfo string
	if m.showProvider {
		providerInfo = string(m.prov.Name)
	}
	// The level sits in the same info column as the provider name, with
	// arrows on the focused row to say the left and right keys move it.
	if m.level != "" {
		level := m.level
		if m.focused {
			level = "‹ " + level + " ›"
		}
		if providerInfo != "" {
			providerInfo += "  " + level
		} else {
			providerInfo = level
		}
	}
	styles := ListItemStyles{
		ItemBlurred:     m.t.Dialog.NormalItem,
		ItemFocused:     m.t.Dialog.SelectedItem,
		InfoTextBlurred: m.t.Dialog.ListItem.InfoBlurred,
		InfoTextFocused: m.t.Dialog.ListItem.InfoFocused,
	}
	return renderItem(styles, m.model.Name, providerInfo, m.focused, width, m.cache, &m.m)
}

// SetFocused implements ListItem.
func (m *ModelItem) SetFocused(focused bool) {
	if m.focused == focused {
		return
	}
	m.cache = nil
	m.focused = focused
	if m.Versioned != nil {
		m.Bump()
	}
}

// SetMatch implements ListItem.
func (m *ModelItem) SetMatch(fm fuzzy.Match) {
	if sameFuzzyMatch(m.m, fm) {
		return
	}
	m.cache = nil
	m.m = fm
	if m.Versioned != nil {
		m.Bump()
	}
}
