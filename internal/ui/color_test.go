package ui

import (
	"context"
	"strings"
	"testing"

	"github.com/gnosh-ai/gnosh/internal/app"
	"github.com/gnosh-ai/gnosh/internal/text"
)

// /color tints the prompt mark: once the session's info says a colour, the
// box and the prompts sent from then on are drawn in it, and default puts
// the theme's own back. Every colour the app gives has a look here.
func TestPromptColor(t *testing.T) {
	for _, name := range app.PromptColors {
		if _, ok := promptColors[name]; !ok {
			t.Errorf("/color gives %s, and the interface has no colour for it", name)
		}
	}
	g := newRig(t, 100, 24, text.TrueColor, func(c *controls) {
		c.info.Commands = append(c.info.Commands, "color")
		c.command = func(ctx context.Context, line string) (app.CommandResult, error) {
			name := strings.TrimSpace(strings.TrimPrefix(line, "/color"))
			if name == "default" {
				name = ""
			}
			c.mu.Lock()
			c.info.Color = name
			c.mu.Unlock()
			return app.CommandResult{Handled: true, Name: "color", Text: "the colour is " + name}, nil
		}
	})
	own := g.s.th.Editor.PromptStyle.Render(">")
	blue := text.Style{}.In(text.TrueColor).Fg(promptColors["blue"][0]).Bold().Render(">")
	if own == blue {
		t.Fatal("the theme's own prompt mark is already blue")
	}
	g.keys(`"/color blue" <Enter>`)
	if got := g.s.th.Editor.PromptStyle.Render(">"); got != blue {
		t.Errorf("after /color blue the box's mark is %q, want %q", got, blue)
	}
	if got := g.s.th.Prompt.Render(">"); got != blue {
		t.Errorf("after /color blue a sent prompt's mark is %q, want %q", got, blue)
	}
	g.keys(`"/color default" <Enter>`)
	if got := g.s.th.Editor.PromptStyle.Render(">"); got != own {
		t.Errorf("after /color default the box's mark is %q, want the theme's own %q", got, own)
	}
	// With sixteen colours, the terminal's own.
	th := DefaultTheme(text.ANSI16)
	th.tintPrompt("blue")
	want := text.Style{}.In(text.ANSI16).Fg(text.Blue).Bold().Render(">")
	if got := th.Prompt.Render(">"); got != want {
		t.Errorf("blue with sixteen colours is %q, want %q", got, want)
	}
}
