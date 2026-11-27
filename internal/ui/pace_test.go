package ui

import (
	"testing"
	"time"

	"github.com/gnosh-ai/gnosh/internal/app"
	"github.com/gnosh-ai/gnosh/internal/text"
)

// What a pace check found (Jae, 2026-11-26: "a little frame that pops up,
// 'is this time efficient?'"): "on pace" for a moment under the status
// line, and never in the conversation; a faster way, or work found stuck,
// kept as a line of the conversation, with the command it moved to the
// background.
func TestPaceShown(t *testing.T) {
	g := newRig(t, 100, 24, text.NoColor)
	g.ev(&app.PaceEvent{Elapsed: time.Minute, Pace: "ok", Why: "counting as asked", Next: time.Minute})
	g.liveHas("pace 1:00 · on pace: counting as asked")
	g.committedLacks("on pace")
	g.ev(&app.PaceEvent{Elapsed: 185 * time.Second, Pace: "stuck", Why: "it waits on nothing", Moved: "Bash: sleep 30"})
	g.committedHas("pace 3:05 · stuck: it waits on nothing (moved to the background: Bash: sleep 30)")
	g.ev(&app.PaceEvent{Elapsed: 4 * time.Minute, Pace: "faster", Why: "run the one failing test"})
	g.committedHas("pace 4:00 · faster: run the one failing test")
}
