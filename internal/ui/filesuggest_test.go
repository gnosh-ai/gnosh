package ui

import (
	"errors"
	"slices"
	"testing"

	"github.com/gnosh-ai/gnosh/internal/text"
)

// The person's own @ search (fileSuggestion): what was typed after the @
// goes to their command in the background, and once it answers its paths
// are what the list offers, in its order, without another key; gnosh's
// own search offers nothing then. What was typed while a command ran goes
// to it next, the latest only. A failure is told once.
func TestFileSuggestionInTheBox(t *testing.T) {
	var asked []string
	g := newRig(t, 100, 24, text.NoColor, func(c *controls) {
		c.info.FileSuggestion = true
		c.suggest = func(q string) ([]string, error) {
			asked = append(asked, q)
			if q == "bad" {
				return nil, errors.New("the fileSuggestion command (files.sh) failed: exit status 3")
			}
			return []string{"zeta/" + q + ".go", "alpha/" + q + "_test.go"}, nil
		}
	})
	g.files["/home/jae/src/proj"] = []fileEntry{{name: "main.go"}, {name: "map.go"}}
	g.s.listProject = func(string, bool) []string { return []string{"main.go", "map.go"} }
	g.hold = true
	g.keys(`"look at @ma"`)
	g.release().release()
	g.liveHas("zeta/ma.go", "alpha/ma_test.go").liveLacks("main.go", "map.go")
	if !slices.Equal(asked, []string{"", "ma"}) {
		t.Errorf("the command was asked %q, want what was typed first and then the latest", asked)
	}
	if got := g.s.complete("look at @ma", 11); len(got) != 2 || got[0].Text != "@zeta/ma.go" {
		t.Errorf("the list is %v, want the command's paths in its order", got)
	}
	// Asked once more for the same words, it is not run again.
	g.keys(`<Backspace> "a"`)
	g.release()
	if len(asked) != 3 || asked[2] != "m" {
		t.Errorf("the command was asked %q", asked)
	}
	g.keys(`<Ctrl+U> "@bad"`)
	g.release().release()
	g.once("the fileSuggestion command (files.sh) failed")
}
