package app

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/gnosh-ai/gnosh/internal/agent"
	"github.com/gnosh-ai/gnosh/internal/core"
	"github.com/gnosh-ai/gnosh/internal/prompt"
	"github.com/gnosh-ai/gnosh/internal/transcript"
)

// This file is the rewind: taking a conversation back to before one of
// the person's prompts (spec/cc/sessions.md, section 5; spec/gnosh
// frames.md, 12.5).
//
// The points are the person's own prompts, as the session's file holds
// them after earlier rewinds; one inside a frame that was popped since is
// still a point. A rewind writes one entry into the session's file, which
// says the conversation goes on from the entry before the prompt, and
// then opens the session again from its file behind the same controls,
// exactly as leaving and resuming it would: so everything that is worked
// out from the conversation (what the model is sent, the frames the
// session stands in, what the features hold of it, whether the first
// prompt's facts have been given) is worked out afresh, by the code that
// a resume runs. Nothing is taken out of the file: a rewind that was a
// mistake is itself in the record.
//
// The files: what Write and Edit changed from the prompt on can be put back
// with the conversation ("/rewind N all") or alone ("/rewind N files"), from
// the copies checkpoint.go keeps; "/rewind N" takes the conversation alone,
// and the files stay as they are. What shell commands changed is not kept,
// so it is never put back. Because the session is opened again, the
// commands it had running in the background are stopped, as they are when
// a session ends.

// RewindPoint is one of the person's prompts that the conversation can be
// taken back to.
type RewindPoint struct {
	// ID names the point for Rewind.
	ID string
	// Text is the prompt, and Time when it was sent.
	Text string
	Time time.Time
	// Files are the files Write and Edit changed from this prompt on: what
	// a rewind to before it can put back, as they were before the first of
	// those changes. FilesGone says the copies from before it were let go
	// (only the most recent prompts' are kept), so none can be.
	Files     []string
	FilesGone bool
}

// rewindGroup finds the person's prompts in what is left of the
// conversation, and for each the entry the conversation would go on from
// if it were taken back to before that prompt: the one before the prompt
// and everything that went out with it. That is where the prompt went
// out, not where it was typed: one typed while a turn ran waited in the
// queue, and the files that turn changed meanwhile are the turn's, as its
// conversation is. queued names, by point, the loop's id of a prompt that
// waited so: its record of waiting stays in the conversation, and the
// rewind must say it will not go out.
func (l *live) rewindGroup() (points []RewindPoint, before, queued map[string]string) {
	entries := transcript.Conversation(l.ts.Entries())
	before, queued = map[string]string{}, map[string]string{}
	// first is where each prompt's own entries begin: the parts that rode
	// on it may stand before the prompt itself.
	first := map[string]int{}
	waited := map[string]bool{}
	for i := range entries {
		for _, e := range decode(&entries[i]) {
			id := ""
			switch e.Kind {
			case agent.EntryPrompt:
				id = e.ID
			case agent.EntryQueued:
				waited[e.ID] = true
			case agent.EntryNote:
				id = e.Prompt
			}
			if _, seen := first[id]; id != "" && !seen {
				first[id] = i
			}
		}
	}
	for i := range entries {
		for _, e := range decode(&entries[i]) {
			if e.Kind != agent.EntryPrompt || e.Hidden {
				continue
			}
			text := strings.TrimSpace(core.TextOf(e.Blocks))
			if text == "" {
				continue
			}
			points = append(points, RewindPoint{ID: entries[i].UUID, Text: text, Time: e.Time})
			if at, ok := first[e.ID]; ok && at > 0 {
				before[entries[i].UUID] = entries[at-1].UUID
			}
			if waited[e.ID] {
				queued[entries[i].UUID] = e.ID
			}
		}
	}
	return points, before, queued
}

// rewindPoints is SessionControls.RewindPoints. The session's file is
// read once for all of them.
func (l *live) rewindPoints() []RewindPoint {
	points, _, _ := l.rewindGroup()
	order := l.promptsInOrder()
	for i := range points {
		cps, gone := l.filesSince(order, points[i].ID)
		points[i].Files, points[i].FilesGone = filePaths(cps), gone
	}
	return points
}

// RewindPoints lists the person's prompts of this conversation, the
// oldest first.
func (c *controls) RewindPoints() []RewindPoint { return c.cur().rewindPoints() }

// Rewind takes the conversation back to before the prompt that point
// names, and returns the prompt's text. The session must be idle.
func (c *controls) Rewind(ctx context.Context, point string) (string, error) {
	text, _, err := c.rewindWith(ctx, point, false)
	return text, err
}

// rewindWith is Rewind, which with files puts back what Write and Edit
// changed too, and says how that went. The files are put back only once
// the rewind is in the session's file, and once the session has stopped
// what it ran in the background, which could change them again.
func (c *controls) rewindWith(ctx context.Context, point string, files bool) (text, filesSaid string, err error) {
	old := c.cur()
	if old.busy(ctx) {
		return "", "", errors.New("a turn is running. Stop it first (Esc, or Ctrl+C in the plain session), then go back")
	}
	points, before, queued := old.rewindGroup()
	for _, p := range points {
		if p.ID == point {
			text = p.Text
		}
	}
	to, ok := before[point]
	switch {
	case text == "":
		return "", "", errors.New("that prompt is not in this conversation any more")
	case !ok:
		return "", "", errors.New("that is the very start of this session's file, and there is nothing before it to go back to. /clear starts a fresh session")
	}
	var restore []fileCheckpoint
	if files {
		if restore, err = old.rewindFiles(point); err != nil {
			return "", "", fmt.Errorf("%v. Nothing was changed: /rewind N takes the conversation alone", err)
		}
	}
	if _, err := old.ts.Append(transcript.RewindEntry(to)); err != nil {
		return "", "", fmt.Errorf("the rewind could not be written into the session's file (%v), so nothing was changed", err)
	}
	if id := queued[point]; id != "" {
		// The prompt waited in the queue while a turn ran: its record of
		// waiting is still in the conversation, and must not make the
		// session send it again when it opens.
		if err := old.log.Append(ctx, agent.Entry{Kind: agent.EntryUnqueued, Prompt: id}); err != nil {
			old.flog.logf("turn", "a rewound prompt that had waited in the queue could not be marked as taken back: %v", err)
		}
	}
	id := old.ts.ID()
	old.flog.logf("turn", "the conversation was taken back to before the prompt in entry %s", point)
	old.close("other")
	if files {
		done, failed := old.checks.restore(restore, old.showPath)
		old.flog.logf("turn", "the files were put back as they were before the prompt in entry %s: %d put back, %d not", point, len(done), len(failed))
		filesSaid = restoreSaid(done, failed)
	}
	next, err := c.p.openSession(ctx, c.st, c.front, "rewind", id)
	if err != nil {
		return "", filesSaid, fmt.Errorf("the conversation was taken back, but the session could not be opened again: %w", err)
	}
	c.mu.Lock()
	c.l = next
	c.mu.Unlock()
	return text, filesSaid, nil
}

// rewindNote is what a person is told wherever a rewind is offered.
const rewindNote = "The files that Write and Edit changed since then can be put back with the conversation, or alone; what a file holds when it is put back is kept first, beside the session, and the rewind says where. What shell commands changed in other files is not put back."

// slashRewind is /rewind: with no number it lists the points, and with
// one it asks for the conversation to be taken back to before that
// prompt, which the session's controls then do.
func (l *live) slashRewind(ctx context.Context, c *SlashContext) error {
	points := l.rewindPoints()
	if len(points) == 0 {
		c.Say("There is no prompt of yours in this conversation to go back to.")
		return nil
	}
	args := strings.Fields(c.Args)
	if len(args) == 0 {
		c.Say("Your prompts in this conversation:")
		for i, p := range points {
			files := ""
			switch {
			case p.FilesGone:
				files = "  (the copies of the files from before it were let go)"
			case len(p.Files) > 0:
				files = fmt.Sprintf("  (%s changed since)", countWords(len(p.Files), "file", "files"))
			}
			c.Say("  %d. %s  %s%s", i+1, p.Time.Local().Format("15:04"), oneLine(p.Text, 100), files)
		}
		c.Say("/rewind N takes the conversation back to before prompt N, which you can then send again or change; /rewind N all puts the files back too, and /rewind N files the files alone. %s", rewindNote)
		return nil
	}
	n, err := strconv.Atoi(args[0])
	if err != nil || n < 1 || n > len(points) {
		return fmt.Errorf("%q is not the number of one of your prompts: /rewind lists them, 1 to %d", oneLine(args[0], 40), len(points))
	}
	how := ""
	if len(args) > 1 {
		how = args[1]
	}
	if len(args) > 2 || how != "" && how != "all" && how != "files" {
		return errors.New("/rewind takes the number of one of your prompts, and then all (the files too) or files (the files alone), or nothing")
	}
	if l.busy(ctx) {
		return errors.New("a turn is running. Stop it first, then go back")
	}
	point := points[n-1]
	if how == "files" {
		// The files alone: the conversation stays where it is, and the
		// session is not opened again. A sub-agent in the background could
		// change the files again as they are put back.
		if bg := l.backgroundAgents(); len(bg) > 0 {
			return fmt.Errorf("%s in the background, and could change the files again: wait for it, or stop it (Ctrl+X Ctrl+K), then put the files back", countWords(len(bg), "sub-agent is working", "sub-agents are working"))
		}
		cps, ferr := l.rewindFiles(point.ID)
		if ferr != nil {
			return ferr
		}
		done, failed := l.checks.restore(cps, l.showPath)
		l.flog.logf("turn", "the files were put back as they were before the prompt in entry %s: %d put back, %d not", point.ID, len(done), len(failed))
		if len(done) > 0 {
			c.Say("The files are back as they were before your prompt: %s", oneLine(point.Text, 100))
		}
		c.Say("%s", restoreSaid(done, failed))
		c.Say("The conversation is as it was; the model is told with its next request.")
		_ = l.agent.Tell(agent.Note{Source: "rewind",
			Text: prompt.Wrap("The user put the files that Write and Edit changed since the prompt \"" + oneLine(point.Text, 200) + "\" back as they were before it. " + restoreSaid(done, failed) + " What you know of those files from later in this conversation is out of date: read them again before you change them.")})
		return nil
	}
	l.rewindTo, l.rewindAll = point.ID, how == "all"
	return nil
}

// countWords counts things in words: "1 file", "3 files".
func countWords(n int, one, many string) string {
	if n == 1 {
		return "1 " + one
	}
	return fmt.Sprintf("%d %s", n, many)
}
