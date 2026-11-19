package ui

import (
	"strings"
	"testing"

	"github.com/gnosh-ai/gnosh/internal/agent"
	"github.com/gnosh-ai/gnosh/internal/core"
	"github.com/gnosh-ai/gnosh/internal/text"
)

// With the showTurnDuration option off, a turn that ended as it should goes
// without the line that says how long it took. A stopped turn still says
// that it was stopped.
func TestTurnDurationHidden(t *testing.T) {
	g := newRig(t, 100, 24, text.NoColor, func(c *controls) { c.info.HideTurnDuration = true })
	g.keys(`"go" <Enter>`).start()
	g.ev(request(1), said("All done.\n"), replyEnd(core.StopEndTurn), turnEnd(agent.EndCompleted), idle())
	g.once("All done.")
	if strings.Contains(flat(g.scroll()), "done in 42s") {
		t.Error("the turn's closing line is shown with showTurnDuration off")
	}
	g.keys(`"again" <Enter>`).start()
	g.ev(request(1), said("Half of it"), turnEnd(agent.EndStopped), idle())
	g.once("stopped by you")
}
