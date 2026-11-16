package app

import (
	"context"
	"fmt"
	"math/rand/v2"
	"slices"
	"strings"

	"github.com/gnosh-ai/gnosh/internal/transcript"
)

// This file is /color, as Claude Code has it: the session's prompt mark
// takes a colour of its own, so that one session can be told from another
// at a glance (one on each box, say). The colour is kept in the session's
// file, so a resumed session has it again, and a run can set it for later.

// PromptColors are the colours /color gives, by Claude Code's names. The
// interface makes each its own way.
var PromptColors = []string{"red", "blue", "green", "yellow", "purple", "orange", "pink", "cyan"}

// metaColor is the key of the session's meta entries that hold its colour,
// "" for the interface's own. The last one is in force.
const metaColor = "color"

// sessionColor is the colour of the session's prompt mark, read from its
// file once.
func (l *live) sessionColor() string {
	l.colorMu.Lock()
	defer l.colorMu.Unlock()
	if !l.colorRead && l.ts != nil {
		l.colorRead = true
		entries := l.ts.Entries()
		for i := len(entries) - 1; i >= 0; i-- {
			if e := entries[i]; e.Type == transcript.TypeMeta && e.Key == metaColor {
				l.color = e.Value
				break
			}
		}
	}
	return l.color
}

// slashColor is /color [colour|default]: with nothing after it, a colour
// at random.
func (l *live) slashColor(ctx context.Context, c *SlashContext) error {
	name := strings.ToLower(strings.TrimSpace(c.Args))
	switch {
	case name == "":
		name = PromptColors[rand.IntN(len(PromptColors))]
	case name == "default":
		name = ""
	case !slices.Contains(PromptColors, name):
		return fmt.Errorf("%q is no colour /color gives: %s, or default for the interface's own", oneLine(name, 40), strings.Join(PromptColors, ", "))
	}
	l.sessionColor()
	if _, err := l.ts.Append(transcript.MetaEntry(metaColor, name)); err != nil {
		return fmt.Errorf("the colour could not be kept in the session's file: %v", err)
	}
	l.colorMu.Lock()
	l.color = name
	l.colorMu.Unlock()
	if name == "" {
		c.Say("This session's prompt mark has the interface's own colour again.")
		return nil
	}
	c.Say("This session's prompt mark is %s from now on, here and when the session is opened again.", name)
	return nil
}
