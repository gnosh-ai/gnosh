package ui

import (
	"testing"

	"github.com/gnosh-ai/gnosh/internal/agent"
	"github.com/gnosh-ai/gnosh/internal/app"
	"github.com/gnosh-ai/gnosh/internal/text"
)

// A bare "no" stops the turn, and the row says so; for a question of a
// sub-agent in the background it stops that sub-agent alone, and the row
// says that instead.
func TestNoSaysWhatItStops(t *testing.T) {
	ask := func(stops string) agent.Question {
		return agent.Question{ID: "q1", Kind: "permission", Title: "Allow Bash?", Text: "  $ touch x",
			Choices: []agent.Choice{{Key: "yes", Label: "Yes, this once"}, {Key: "no", Label: "No"}},
			Data:    app.QuestionData{Stops: stops}}
	}
	g := newRig(t, 100, 24, text.NoColor)
	g.ev(&agent.QuestionEvent{Question: ask("")})
	g.liveHas("the call does not run, and the turn stops")

	g = newRig(t, 100, 24, text.NoColor)
	g.ev(&agent.QuestionEvent{Question: ask("that sub-agent")})
	g.liveHas("the call does not run, and that sub-agent stops").liveLacks("the turn stops")
}
