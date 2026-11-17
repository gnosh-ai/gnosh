package ui

import (
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/gnosh-ai/gnosh/internal/app"
	"github.com/gnosh-ai/gnosh/internal/core"
	"github.com/gnosh-ai/gnosh/internal/term/vtest"
	"github.com/gnosh-ai/gnosh/internal/text"
)

// The address of a delivered file, as gnosh serves one: the server's own
// address, a token of 43 characters, and the file's name. It is 72 cells
// long, which fits a row of an 80-column terminal behind two blanks.
const (
	adURL  = "http://127.0.0.1:7777/k3JQm3fK0aP9xR2tV5wY8zB1dE4gH7jL0nQ3sU6vX9y/ad.mp4"
	ad2URL = "http://127.0.0.1:7777/Zx8pW2nM5cT1vB7yK4hQ9rD3fG6jL0sA2uE5iO8tN1q/ad-v2.mp4"
)

// deliverResult is the result of a Deliver call that went well: what the
// model is told, and the deliverable as it then stands.
func deliverResult(d app.Deliverable, replaced bool) core.ToolResult {
	return core.ToolResult{
		Content: []core.Block{core.Text("Recorded \"" + d.Label + "\".")},
		Meta:    map[string]any{app.MetaDeliverable: d, app.MetaReplaced: replaced},
	}
}

// squeezed is a text with every blank and line break taken out: what is
// left of an address when it was wrapped onto several rows.
func squeezed(s string) string { return strings.Join(strings.Fields(s), "") }

// whole fails the test unless the scrollback holds each piece whole:
// every character of it, in order, on however many rows.
func (g *rig) whole(pieces ...string) *rig {
	g.t.Helper()
	for _, p := range pieces {
		if !strings.Contains(squeezed(g.scroll()), squeezed(p)) {
			g.t.Fatalf("the scrollback does not hold all of %q\n%s", p, g.dump())
		}
	}
	return g
}

// deliver hands the session a call of Deliver from its start to its end,
// and then the list as it stands after it.
func (g *rig) deliver(id string, args map[string]any, d app.Deliverable, replaced bool, took time.Duration, list ...app.Deliverable) *rig {
	g.t.Helper()
	return g.ev(toolStart(id, "Deliver", args), toolEnd(id, "Deliver", args, deliverResult(d, replaced), took),
		&app.DeliverablesEvent{List: list})
}

// A deliverable is one entry in the scrollback when it is claimed and one
// when it is delivered, in place of the call's own block: the label, where
// it stands, and the address and the file whole, never cut. The status
// line counts them.
func TestDeliverableLines(t *testing.T) {
	each(t, func(t *testing.T, cols int) {
		g := newRig(t, cols, 30, text.NoColor)
		g.keys(`"render the ad" <Enter>`).start()

		// Claimed: one line, and the count of what is pending.
		claim := map[string]any{"label": "hebrew ad video"}
		pending := app.Deliverable{Label: "hebrew ad video", Pending: true}
		g.ev(toolCall("c1", "Deliver"), replyEnd(core.StopToolUse), toolStart("c1", "Deliver", claim))
		g.liveHas("▸ Deliver hebrew ad video")
		g.ev(toolEnd("c1", "Deliver", claim, deliverResult(pending, false), 0), &app.DeliverablesEvent{List: []app.Deliverable{pending}})
		g.once("✧ hebrew ad video · pending").committedLacks("Deliver", "Recorded")
		if cols >= 80 {
			g.liveHas("1 deliverable pending")
		}

		// Delivered, from another machine: the address and the path, each
		// whole, and the address on a row of its own where a row holds it.
		args := map[string]any{"label": "hebrew ad video", "target": "/root/renders/ad.mp4", "host": "render"}
		done := app.Deliverable{Label: "hebrew ad video", Path: "/root/renders/ad.mp4", Host: "render", URL: adURL}
		g.ev(toolStart("c2", "Deliver", args))
		if cols >= 80 {
			g.liveHas("▸ Deliver hebrew ad video: /root/renders/ad.mp4 on render")
		}
		g.ev(toolEnd("c2", "Deliver", args, deliverResult(done, false), 3*time.Second), &app.DeliverablesEvent{List: []app.Deliverable{done}})
		g.once("✦ hebrew ad video · delivered · 3s").whole(adURL, "/root/renders/ad.mp4 on render")
		if cols >= 80 {
			if !slices.Contains(g.committed, "  "+adURL) || !slices.Contains(g.committed, "  /root/renders/ad.mp4 on render") {
				t.Errorf("the address and the path do not each stand on a row of their own\n%s", g.dump())
			}
			g.liveHas("1 delivered").liveLacks("pending")
		}

		// An address: on the label's row where it fits there.
		link := app.Deliverable{Label: "landing page", URL: "https://example.com/landing"}
		g.deliver("c3", map[string]any{"label": "landing page", "target": link.URL}, link, false, 0, done, link)
		if cols >= 80 {
			g.once("✦ landing page · delivered · https://example.com/landing")
			g.liveHas("2 delivered")
		} else {
			g.once("✦ landing page · delivered").whole(link.URL)
		}

		// A newer version under the same label says so, and a file in the
		// home directory is written the short way.
		again := app.Deliverable{Label: "hebrew ad video", Path: "/home/jae/renders/ad-v2.mp4", URL: ad2URL}
		g.deliver("c4", map[string]any{"label": "hebrew ad video", "target": again.Path}, again, true, 0, link, again)
		g.once("✦ hebrew ad video · new version").whole(ad2URL, "~/renders/ad-v2.mp4")

		// Claimed again: what was delivered stays in sight.
		redo := again
		redo.Pending = true
		g.deliver("c5", claim, redo, false, 0, link, redo)
		g.once("✦ hebrew ad video · a new version is pending")
		if cols >= 80 {
			g.liveHas("1 delivered · 1 pending")
		}

		// Nothing was cut anywhere.
		g.committedLacks("…", "...")

		// A call that did not go well is a failed call like any other, and
		// what the tool said is under it.
		bad := map[string]any{"label": "notes", "target": "/nope/notes.md"}
		g.ev(toolStart("c6", "Deliver", bad), toolEnd("c6", "Deliver", bad,
			core.ErrorResult("Nothing was recorded: there is no file at /nope/notes.md on this machine."), 0))
		g.whole("✗ Deliver notes: /nope/notes.md · failed", "Nothing was recorded: there is no file at /nope/notes.md on this machine.")
		refused := toolEnd("c7", "Deliver", args, core.ErrorResult("This call did not run: the user refused it."), 0)
		refused.Denied = true
		g.ev(toolStart("c7", "Deliver", args), refused)
		g.whole("not run", "This call did not run: the user refused it.")
		g.show("deliverables")
	})
}

// rowOf returns the row of the screen that begins with a piece, as a
// person sees it.
func rowOf(t *testing.T, g *rig, begins string) int {
	t.Helper()
	for row, line := range g.scr.Lines() {
		if strings.HasPrefix(seen(line), begins) {
			return row
		}
	}
	t.Fatalf("no row of the screen begins with %q\n%s", begins, g.dump())
	return -1
}

// The label is in one plain colour, and the mark before it is the one
// touch of colour: green for a file, gold for an address, grey for what is
// not there yet. An address is underlined and is a real link. With no
// colour there is no sequence at all, and the same words.
func TestDeliverableColours(t *testing.T) {
	done := app.Deliverable{Label: "hebrew ad video", Path: "/root/renders/ad.mp4", Host: "render", URL: adURL}
	link := app.Deliverable{Label: "landing page", URL: "https://example.com/landing"}
	pending := app.Deliverable{Label: "demo reel", Pending: true}
	play := func(g *rig) {
		g.keys(`"render the ad" <Enter>`).start()
		g.ev(toolCall("c1", "Deliver"), replyEnd(core.StopToolUse))
		g.deliver("c1", map[string]any{"label": "demo reel"}, pending, false, 0, pending)
		g.deliver("c2", map[string]any{"label": "hebrew ad video", "target": done.Path, "host": "render"}, done, false, 0, pending, done)
		g.deliver("c3", map[string]any{"label": "landing page", "target": link.URL}, link, false, 0, pending, done, link)
	}

	g := newRig(t, 100, 40, text.TrueColor)
	play(g)
	pal := DefaultPalette(text.TrueColor)
	fg := func(c text.Color) string { return strings.ToLower(c.String()) }
	cell := func(begins string, col int) vtest.Cell { return g.scr.Cell(rowOf(t, g, begins), col) }

	for _, tc := range []struct {
		begins string
		mark   text.Color
	}{
		{"✦ hebrew ad video", pal.Green},
		{"✦ landing page", pal.Gold},
		{"✧ demo reel", pal.Grey},
	} {
		if got := cell(tc.begins, 0).Attr.FG.String(); got != fg(tc.mark) {
			t.Errorf("the mark of %q is drawn in %s, want %s", tc.begins, got, fg(tc.mark))
		}
		// Every letter of the label: the terminal's own colour, bold.
		label := strings.SplitN(tc.begins, " ", 2)[1]
		for col := 2; col < 2+len(label); col++ {
			if a := cell(tc.begins, col).Attr; a.FG.String() != "default" || !a.Bold || a.Underline {
				t.Errorf("letter %d of the label %q is drawn %+v, want the terminal's own colour, bold", col-2, label, a)
			}
		}
	}

	// The address of the file stands on its own row: every cell of it is
	// underlined and belongs to a link to that address.
	row := rowOf(t, g, "  http://127.0.0.1:7777/")
	for col := 2; col < 2+len(adURL); col++ {
		if a := g.scr.Cell(row, col).Attr; a.Link != adURL || !a.Underline || a.FG.String() != fg(pal.Purple) {
			t.Fatalf("cell %d of the address is drawn %+v, want an underlined link to %s", col, a, adURL)
		}
	}
	if a := g.scr.Cell(row, 0).Attr; a.Link != "" || a.Underline {
		t.Errorf("the blanks before the address are part of the link: %+v", a)
	}
	// The path beside it is no link, and is not coloured.
	if a := cell("  /root/renders/ad.mp4", 2).Attr; a.Link != "" || a.FG.String() != "default" || a.Underline {
		t.Errorf("the path is drawn %+v, want plain text", a)
	}
	// The address of the page is a link too, on the label's own row.
	at := strings.Index(seen(g.scr.Line(rowOf(t, g, "✦ landing page"))), "https://")
	if a := cell("✦ landing page", text.Width("✦ landing page · delivered · ")).Attr; at < 0 || a.Link != link.URL || !a.Underline {
		t.Errorf("the address of the page is drawn %+v, want an underlined link to %s", a, link.URL)
	}

	// With no colour: not one sequence, the same words.
	plain := newRig(t, 100, 40, text.NoColor)
	play(plain)
	all := strings.Join(plain.writes, "")
	if strings.Contains(all, "\x1b]8;") || sgr.MatchString(all) {
		t.Errorf("with no colour a colour, a style or a link was written")
	}
	plain.once("✧ demo reel · pending", "✦ hebrew ad video · delivered", "  "+adURL, "  /root/renders/ad.mp4 on render", "✦ landing page · delivered · https://example.com/landing")
	// With sixteen colours and with 256 the link is a link as well.
	for _, profile := range []text.Profile{text.ANSI16, text.ANSI256} {
		few := newRig(t, 100, 40, profile)
		play(few)
		if row := rowOf(t, few, "  http://127.0.0.1:7777/"); few.scr.Cell(row, 2).Attr.Link != adURL {
			t.Errorf("with the profile %v the address is not a link", profile)
		}
	}
}

// What an address or a label holds is the model's and a tool's text:
// nothing in it reaches the terminal as a sequence, and nothing in it can
// end the link it stands in.
func TestDeliverableTextIsMadeSafe(t *testing.T) {
	g := newRig(t, 100, 30, text.TrueColor)
	g.keys(`"go" <Enter>`).start()
	evil := app.Deliverable{Label: "a\x1b[31m label\nwith a break", URL: "https://example.com/a\x1b\\\x1b]8;;https://evil.example.com\x07b"}
	g.ev(toolCall("c1", "Deliver"), replyEnd(core.StopToolUse))
	g.deliver("c1", map[string]any{"label": evil.Label, "target": evil.URL}, evil, false, 0, evil)
	row := rowOf(t, g, "✦ a")
	for col := 0; col < 100; col++ {
		if a := g.scr.Cell(row, col).Attr; strings.Contains(a.Link, "evil") && !strings.HasPrefix(a.Link, "https://example.com/a") {
			t.Fatalf("cell %d belongs to a link to %q", col, a.Link)
		}
		if g.scr.Cell(row, col).Attr.FG.String() == "1" {
			t.Fatalf("the label's own colour sequence reached the terminal")
		}
	}
	g.committedHas("with a break")
}

// /deliverables, which is also /artifacts, lists the latest version of
// each thing with its address and its file, the pending ones marked, at
// once and also while a turn runs. The interface draws the list itself,
// from what the session holds. A session that is opened with deliverables
// counts them on its status line from the start.
func TestDeliverablesCommand(t *testing.T) {
	list := []app.Deliverable{
		{Label: "landing page", URL: "https://example.com/landing"},
		{Label: "demo reel", Pending: true},
		{Label: "hebrew ad video", Path: "/root/renders/ad.mp4", Host: "render", URL: adURL},
	}
	const head = "Deliverables, the latest version of each: 2 delivered · 1 pending"
	const hint = "An address at 127.0.0.1:7777 answers on this machine only. From another machine, forward the port first: ssh -L 7777:127.0.0.1:7777 <this machine>"
	each(t, func(t *testing.T, cols int) {
		g := newRig(t, cols, 40, text.NoColor, func(c *controls) {
			c.info.Commands = append(c.info.Commands, "deliverables")
			c.deliv = list
		})
		// In a terminal too narrow for both counts, the promise goes first
		// and the count of what there is to open stays.
		switch {
		case cols >= 120:
			g.liveHas("2 delivered · 1 pending")
		case cols >= 80:
			g.liveHas("2 delivered").liveLacks("pending")
		}

		// The command is in the list a slash opens.
		g.keys(`"/d"`).liveHas("/deliverables")
		g.keys(`"eliverables" <Enter>`)
		g.whole("> /deliverables", head, hint, adURL, "/root/renders/ad.mp4 on render", "https://example.com/landing")
		g.once("✧ demo reel · pending", "✦ hebrew ad video", "✦ landing page").committedLacks("…", "...")
		if cols >= 80 {
			g.once("✦ landing page · https://example.com/landing")
			if !slices.Contains(g.committed, "  "+adURL) {
				t.Errorf("the address does not stand on a row of its own\n%s", g.dump())
			}
		}
		// The order is the session's: the one that changed last is last.
		scroll := flat(g.scroll())
		if a, b, c := strings.Index(scroll, "✦ landing page"), strings.Index(scroll, "✧ demo reel"), strings.Index(scroll, "✦ hebrew ad video"); !(a < b && b < c) {
			t.Errorf("the entries stand at %d, %d and %d, want them in the session's order\n%s", a, b, c, g.dump())
		}

		// The other name, and while a turn runs: at once, not queued, and
		// the session is not asked to run anything.
		g.keys(`"go" <Enter>`).start()
		g.keys(`"/artifacts" <Enter>`)
		g.whole("> /artifacts").liveLacks("runs when the turn ends")
		if n := strings.Count(flat(g.scroll()), "✧ demo reel · pending"); n != 2 {
			t.Errorf("the list was written %d times, want twice\n%s", n, g.dump())
		}
		if got := g.c.commands; len(got) != 0 {
			t.Errorf("the session was asked to run %q", got)
		}
		g.show("the list of deliverables")
	})

	// With nothing claimed or delivered the list says so.
	g := newRig(t, 80, 24, text.NoColor, func(c *controls) { c.info.Commands = append(c.info.Commands, "deliverables") })
	g.liveLacks("delivered", "pending")
	g.keys(`"/deliverables" <Enter>`)
	g.whole("Nothing has been claimed or delivered in this session yet.")

	// A session whose controls have no such command is asked, and answers
	// as it does for any word it does not know.
	g = newRig(t, 80, 24, text.NoColor)
	g.keys(`"/deliverables" <Enter>`)
	g.committedHas("There is no command /deliverables")
}

// The word a person uses for the list finds it: with no command whose
// name begins the way they typed, a command's other name is offered.
func TestOtherNamesAreOfferedWhenNoNameFits(t *testing.T) {
	g := newRig(t, 80, 24, text.NoColor, func(c *controls) {
		c.info.Commands = append(c.info.Commands, "deliverables")
		c.info.CommandList = []app.CommandInfo{
			{Name: "exit", Aliases: []string{"quit"}},
			{Name: "deliverables", Aliases: []string{"artifacts"}},
			{Name: "no_think", Aliases: []string{"nothink", "no-think"}},
		}
	})
	g.keys(`"/art"`).liveHas("/artifacts")
	g.keys(`<Backspace>*4 "/qu"`).liveHas("/quit").liveLacks("/artifacts")
	// A name that fits comes alone: its other names, and those of other
	// commands, stay out of the way.
	g.keys(`<Backspace>*3 "/no"`).liveHas("/no_think", "/noloop").liveLacks("/nothink", "/no-think")
	g.keys(`<Backspace>*3 "/"`).liveLacks("/artifacts", "/quit")
}

// Enter on a command in the list runs it, as in Claude Code, unless what
// must follow its name is still to be typed.
func TestEnterRunsTheCommandChosen(t *testing.T) {
	for _, tc := range []struct{ keys, ran, box string }{
		{`"/comp" <Enter>`, "/compact", ""},
		{`"/backg" <Enter>`, "", "/background "},
		// A skill's hint is in its author's words.
		{`"/fix-is" <Enter>`, "", "/fix-issue "},
		{`"/tid" <Enter>`, "/tidy", ""},
	} {
		g := newRig(t, 80, 24, text.NoColor, func(c *controls) {
			c.info.Commands = append(c.info.Commands, "background")
			c.info.CommandList = []app.CommandInfo{
				{Name: "compact", Args: "[instructions]"},
				{Name: "background", Aliases: []string{"bg"}, Args: "<prompt>"},
			}
			c.info.Skills = []app.CommandInfo{
				{Name: "fix-issue", Args: "issue-number"},
				{Name: "tidy", Args: "[path]"},
				// A skill that has a command's name changes nothing of it.
				{Name: "compact", Args: "<pr>"},
			}
		})
		g.keys(tc.keys)
		if got := strings.Join(g.c.commands, " "); got != tc.ran {
			t.Errorf("%s ran %q, want %q", tc.keys, got, tc.ran)
		}
		if got := g.box(); tc.ran == "" && got != tc.box {
			t.Errorf("%s left %q in the box, want %q", tc.keys, got, tc.box)
		}
	}
}

// When another session comes to stand behind the controls, its
// deliverables are the ones that count.
func TestDeliverablesFollowTheSession(t *testing.T) {
	g := newRig(t, 100, 24, text.NoColor, func(c *controls) {
		c.info.Commands = append(c.info.Commands, "deliverables")
		c.deliv = []app.Deliverable{{Label: "landing page", URL: "https://example.com/landing"}}
	})
	g.liveHas("1 delivered")
	g.c.mu.Lock()
	g.c.deliv = []app.Deliverable{{Label: "demo reel", Pending: true}, {Label: "notes", Pending: true}}
	g.c.mu.Unlock()
	g.keys(`"/clear" <Enter>`)
	g.liveHas("2 deliverables pending").liveLacks("delivered")
}
