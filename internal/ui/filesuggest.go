package ui

import (
	"context"
	"strings"

	"github.com/gnosh-ai/gnosh/internal/agent"
	"github.com/gnosh-ai/gnosh/internal/ui/editor"
)

// The person's own @ search (Claude Code's fileSuggestion): when the
// session has a command for it, what was typed after an @ goes to that
// command in the background, and the paths it printed are what the list
// offers, in its order. Until the answer for what was typed comes, the
// last answer's paths that still hold it are offered, and the list fills
// in when the answer comes. One command runs at a time: what was typed
// meanwhile goes to it next. Answers are kept until a turn ends (a turn
// may make files).

// fileSuggester is what a session's controls may also be: able to run the
// person's fileSuggestion command for what was typed after an @. ok is
// false when there is none.
type fileSuggester interface {
	SuggestFiles(ctx context.Context, query string) (paths []string, ok bool, err error)
}

// suggested is what the fileSuggestion command has answered.
type suggested struct {
	// answers are its paths by what was typed; last is the latest query.
	answers map[string][]string
	last    string
	// running says a command runs now, and next what was typed since,
	// when queued.
	running bool
	next    string
	queued  bool
	// told is the last failure the person was told of, told once.
	told string
}

// maxAnswers is how many answers are kept before they are let go of.
const maxAnswers = 64

// suggestFiles offers what the fileSuggestion command answered for
// partial, and asks it in the background when it has not.
func (s *session) suggestFiles(fs fileSuggester, partial string, start int, sigil string) []editor.Candidate {
	sg := &s.suggest
	paths, ok := sg.answers[partial]
	if !ok {
		s.askSuggest(fs, partial)
		low := strings.ToLower(partial)
		for _, p := range sg.answers[sg.last] {
			if strings.Contains(strings.ToLower(p), low) {
				paths = append(paths, p)
			}
		}
	}
	out := make([]editor.Candidate, 0, len(paths))
	for _, p := range paths {
		out = append(out, editor.Candidate{Text: sigil + p, Start: start, Label: p})
	}
	return out
}

// askSuggest runs the fileSuggestion command for q in the background, or
// queues q when one runs; the list is made again when it answers. A query
// it failed on is not asked again until the turn ends.
func (s *session) askSuggest(fs fileSuggester, q string) {
	sg := &s.suggest
	if sg.running {
		sg.next, sg.queued = q, true
		return
	}
	sg.running = true
	s.host.spawn(func() func() {
		paths, _, err := fs.SuggestFiles(context.Background(), q)
		return func() {
			sg.running = false
			if sg.answers == nil || len(sg.answers) >= maxAnswers {
				sg.answers = map[string][]string{}
			}
			sg.answers[q] = paths
			if err == nil {
				sg.last = q
			} else if msg := err.Error(); msg != sg.told {
				sg.told = msg
				s.notice(agent.LevelWarning, msg)
			}
			if sg.queued {
				sg.queued = false
				if _, have := sg.answers[sg.next]; !have {
					s.askSuggest(fs, sg.next)
				}
			}
			s.ed.Recomplete()
		}
	})
}
