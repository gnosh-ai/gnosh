package shellconfig_test

import (
	"reflect"
	"strings"
	"testing"

	"github.com/charmbracelet/crush/internal/config"
	"github.com/charmbracelet/crush/internal/shellconfig"
	"github.com/stretchr/testify/require"
)

// The crushrc `hook add` builtin spells out the valid event names
// because internal/config imports this package and cannot be imported
// back. This test is what keeps the two copies in step: if config gains
// or renames an event and this list does not follow, `hook add` would
// either refuse a name the loader accepts, or write one it rejects.
//
// It lives in the external test package so it can see both sides
// without creating an import cycle.
func TestKnownHookEventsMatchConfig(t *testing.T) {
	t.Parallel()

	require.ElementsMatch(t, config.KnownHookEvents(), shellconfig.KnownHookEventsForTest(),
		"hook add's accepted events must be exactly the events the loader accepts")
}

func TestEventsSupportingMatcherMatchConfig(t *testing.T) {
	t.Parallel()

	var want []string
	for _, event := range config.KnownHookEvents() {
		if config.HookEventSupportsMatcher(event) {
			want = append(want, event)
		}
	}
	require.ElementsMatch(t, want, shellconfig.EventsSupportingMatcherForTest(),
		"hook add must allow --matcher on exactly the events the loader allows it on")
}

// Same contract for MCP connection types: `mcp add` must accept exactly
// the types the loader can open.
func TestKnownMCPTypesMatchConfig(t *testing.T) {
	t.Parallel()

	var want []string
	for _, mcpType := range config.KnownMCPTypes() {
		want = append(want, string(mcpType))
	}
	require.ElementsMatch(t, want, shellconfig.KnownMCPTypesForTest(),
		"mcp add's accepted types must be exactly the types the loader accepts")
}

// Every option config.Options has can be written in a crushrc. A crushrc
// line naming an option the table lacks is "option: unknown key", which
// stops the whole config from loading, so an option added to config
// without a crushrc name fails here instead of in a user's config.
func TestEveryOptionHasACrushrcName(t *testing.T) {
	t.Parallel()

	// Not written by hand: the TUI keeps each model's reasoning level.
	notInCrushrc := map[string]bool{"reasoning": true}
	settable := map[string]bool{}
	for _, key := range shellconfig.OptionJSONKeysForTest() {
		settable[key] = true
	}
	typ := reflect.TypeFor[config.Options]()
	for i := range typ.NumField() {
		name, _, _ := strings.Cut(typ.Field(i).Tag.Get("json"), ",")
		if name == "" || name == "-" || notInCrushrc[name] {
			continue
		}
		if !settable[name] {
			t.Errorf("options.%s has no crushrc option: add it to optionSpecs in options.go", name)
		}
	}
}
