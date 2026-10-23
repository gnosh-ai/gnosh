package prompt

import (
	"testing"

	"github.com/charmbracelet/crush/internal/config"
	"github.com/stretchr/testify/require"
)

// Text put before and after a prompt is written as given, even where it
// looks like a template action.
func TestBuildPutsTextAroundTheTemplate(t *testing.T) {
	t.Parallel()

	store := config.NewTestStore(&config.Config{Options: &config.Options{}})
	p, err := NewPrompt("test", "Platform: {{.Platform}}",
		WithPlatform("plan9"),
		WithWorkingDir(t.TempDir()),
		WithTextBefore("Mind the {{braces}}.\n"),
		WithTextAfter("Ends with {{.Platform}}."),
	)
	require.NoError(t, err)
	got, err := p.Build(t.Context(), "", "", store)
	require.NoError(t, err)
	require.Equal(t, "Mind the {{braces}}.\nPlatform: plan9\n\nEnds with {{.Platform}}.", got)

	p, err = NewPrompt("test", "Platform: {{.Platform}}", WithPlatform("plan9"), WithWorkingDir(t.TempDir()), WithTextAfter(""))
	require.NoError(t, err)
	got, err = p.Build(t.Context(), "", "", store)
	require.NoError(t, err)
	require.Equal(t, "Platform: plan9", got, "no text after adds no blank line")
}
