package ui

import (
	"encoding/json"
	"fmt"
	"math/rand/v2"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/gnosh-ai/gnosh/internal/agent"
	"github.com/gnosh-ai/gnosh/internal/app"
	"github.com/gnosh-ai/gnosh/internal/core"
	"github.com/gnosh-ai/gnosh/internal/term"
	"github.com/gnosh-ai/gnosh/internal/text"
)

// story is a whole session in small steps: a prompt, reasoning, an answer
// with typing going on, a command with a question about it, an edit, a
// retry, a second prompt. The tests below run it with something done to
// the terminal between any two steps.
func story() []func(g *rig) {
	bash := map[string]any{"command": "go test ./internal/ui/ -run TestStory"}
	edit := map[string]any{"file_path": "/home/jae/src/proj/a.go"}
	return []func(g *rig){
		func(g *rig) { g.keys(`"fix the flaky test" <Enter>`) },
		func(g *rig) { g.start() },
		func(g *rig) { g.ev(thought("The test shares a directory.\nI should ")) },
		func(g *rig) { g.ev(thought("give each run its own.\n")) },
		func(g *rig) { g.ev(said("I will look at the **test** first.\n\n")) },
		func(g *rig) {
			g.ev(said("The trouble is a temporary directory that every run of the test shares, so two runs at once write over each other. "))
		},
		func(g *rig) { g.keys(`"and then run it twice"`) },
		func(g *rig) { g.ev(said("Here is the fix:\n\n```go\ndir := t.TempDir()\n```\n")) },
		func(g *rig) {
			g.ev(toolCall("c1", "Edit"), toolInput("c1", `{"file_path":"/home/jae/src/proj/a.go","old_`))
		},
		func(g *rig) { g.ev(replyEnd(core.StopToolUse)) },
		func(g *rig) { g.ev(toolStart("c1", "Edit", edit)) },
		func(g *rig) {
			g.ev(toolEnd("c1", "Edit", edit, core.ToolResult{Content: []core.Block{core.Text("ok")},
				Display: &core.Display{Kind: "diff", Path: "/home/jae/src/proj/a.go", Before: "dir := shared\nuse(dir)\n", After: "dir := t.TempDir()\nuse(dir)\n"}}, 0))
		},
		func(g *rig) { g.ev(request(2), said("Now the test.\n")) },
		func(g *rig) { g.ev(toolCall("c2", "Bash"), replyEnd(core.StopToolUse), toolStart("c2", "Bash", bash)) },
		func(g *rig) { g.ev(&agent.QuestionEvent{Question: permission("q1")}) },
		func(g *rig) { g.wait(2 * time.Second) },
		func(g *rig) { g.keys(`<Down>`) },
		func(g *rig) { g.keys(`<Up> <Enter>`) },
		func(g *rig) { g.ev(&agent.AnswerEvent{ID: "q1"}) },
		func(g *rig) { g.ev(progress("c2", "=== RUN   TestStory\n--- PASS: TestStory (0.01s)\n")) },
		func(g *rig) { g.ev(progress("c2", "PASS\nok  \texample.com/ui\t0.012s\n")) },
		func(g *rig) {
			g.ev(toolEnd("c2", "Bash", bash, core.ToolResult{
				Content: []core.Block{core.Text("=== RUN   TestStory\n--- PASS: TestStory (0.01s)\nPASS\nok  \texample.com/ui\t0.012s\n")},
				Meta:    map[string]any{"exitCode": 0}}, 1200*time.Millisecond))
		},
		func(g *rig) {
			g.ev(request(3), &agent.RetryEvent{Purpose: "main", Attempt: 1, Wait: 2 * time.Second, Kind: core.ErrNetwork,
				Message: "local/qwen3.8-27b could not be reached. Trying again in 2s."})
		},
		func(g *rig) { g.wait(2 * time.Second) },
		func(g *rig) { g.ev(request(3), said("The test passes now, twice in a row.\n")) },
		func(g *rig) { g.ev(replyEnd(core.StopEndTurn), turnEnd(agent.EndCompleted), idle()) },
		func(g *rig) { g.keys(`<Enter>`) },
		func(g *rig) { g.start() },
		func(g *rig) {
			g.ev(said("Running it twice.\n"), replyEnd(core.StopEndTurn), turnEnd(agent.EndCompleted), idle())
		},
	}
}

// storyEnd checks what the session committed over the whole story: every
// part of it once.
func storyEnd(t *testing.T, g *rig) {
	t.Helper()
	g.once("fix the flaky test", "thought for", "I will look at the", "Here is the fix:", "Now the test.", "TestStory (0.01s)",
		"The test passes now, twice in a row.", "and then run it twice", "Running it twice.", "Allow Bash? Yes, this once")
	g.committedHas("could not be reached", "answers again", "dir := t.TempDir()")
	if g.cols >= 40 {
		g.committedHas("- dir := shared", "+ dir := t.TempDir()")
	}
	if got := g.c.sent(); len(got) != 2 || got[1] != "and then run it twice" {
		t.Fatalf("submitted %q", got)
	}
}

// The story runs at every width, on tall and short terminals, in a
// terminal that wraps its lines again when it is resized and in one that
// does not.
func TestStory(t *testing.T) {
	for _, size := range [][2]int{{40, 24}, {80, 24}, {120, 40}, {80, 8}, {30, 12}} {
		t.Run(fmt.Sprintf("%dx%d", size[0], size[1]), func(t *testing.T) {
			g := newRig(t, size[0], size[1], text.NoColor)
			for _, step := range story() {
				step(g)
			}
			storyEnd(t, g)
			g.show("the story")
			if g.most > liveBound(size[1]) {
				t.Errorf("the live region grew to %d rows in a terminal of %d", g.most, size[1])
			}
		})
	}
}

// The terminal is resized between any two steps of the story, to every
// kind of size. Whatever is committed afterwards is on the screen once
// and in order, the live region is whole, and the cursor is where it
// belongs.
func TestResizeAtEveryPoint(t *testing.T) {
	sizes := [][2]int{{40, 24}, {120, 30}, {20, 10}, {80, 6}, {200, 50}, {61, 17}}
	steps := story()
	for _, reflow := range []bool{true, false} {
		for at := 0; at <= len(steps); at++ {
			size := sizes[at%len(sizes)]
			t.Run(fmt.Sprintf("reflow=%v/step%d/%dx%d", reflow, at, size[0], size[1]), func(t *testing.T) {
				g := newRig(t, 80, 24, text.NoColor)
				g.scr.SetReflow(reflow)
				g.s.inline.SetReflow(reflow)
				for i, step := range steps {
					if i == at {
						g.resize(size[0], size[1])
					}
					step(g)
				}
				if at == len(steps) {
					g.resize(size[0], size[1])
				}
				storyEnd(t, g)
				// And back again, and through a run of sizes.
				for _, s := range [][2]int{{80, 24}, {33, 9}, {150, 40}, {80, 24}} {
					g.resize(s[0], s[1])
					g.keys(`"x"`)
				}
				if g.box() != "xxxx" {
					t.Fatalf("the box holds %q after the resizes", g.box())
				}
			})
		}
	}
}

// A terminal twenty columns wide: everything still fits, nothing is drawn
// past the edge, and the session can be used.
func TestVeryNarrow(t *testing.T) {
	for _, rows := range []int{24, 10, 4} {
		g := newRig(t, 20, rows, text.NoColor)
		for _, step := range story() {
			step(g)
		}
		storyEnd(t, g)
		g.keys(`"/st" <Tab> <Enter> "?" <Shift+Tab>`)
		g.committedHas("ran /status", "Keys")
		g.liveHas("accept edits")
	}
}

// sgr matches a colour or style sequence.
var sgr = regexp.MustCompile(`\x1b\[[0-9;:]*m`)

// With no colour not one colour or style sequence is written, and the
// screen says the same things. With colour, each profile writes only the
// colours it has.
func TestColourProfiles(t *testing.T) {
	texts := map[text.Profile]string{}
	for _, profile := range []text.Profile{text.NoColor, text.ANSI16, text.ANSI256, text.TrueColor} {
		g := newRig(t, 80, 24, profile, func(c *controls) { c.bypass = true })
		for _, step := range story() {
			step(g)
		}
		storyEnd(t, g)
		g.keys(`<Shift+Tab>*3 "?" "/status" <Enter>`)
		g.liveHas("[BYPASS PERMISSIONS]")
		all := strings.Join(g.writes, "")
		found := sgr.FindAllString(all, -1)
		switch profile {
		case text.NoColor:
			if len(found) > 0 {
				t.Errorf("with no colour %d style sequences were written, the first %q", len(found), found[0])
			}
			if strings.Contains(all, "\x1b]8;") {
				t.Error("with no colour a hyperlink was written")
			}
		case text.ANSI16:
			for _, seq := range found {
				if strings.Contains(seq, "38;") || strings.Contains(seq, "48;") {
					t.Fatalf("with sixteen colours %q was written", seq)
				}
			}
		case text.ANSI256:
			for _, seq := range found {
				if strings.Contains(seq, "38;2;") || strings.Contains(seq, "48;2;") {
					t.Fatalf("with 256 colours %q was written", seq)
				}
			}
		}
		if profile != text.NoColor && len(found) == 0 {
			t.Errorf("profile %v wrote no colours at all", profile)
		}
		texts[profile] = flat(strings.ReplaceAll(g.scroll(), "`", ""))
	}
	// The same words whatever the colours (Markdown keeps the backquotes
	// of code when it has no colour to show it with).
	for profile, got := range texts {
		if got != texts[text.TrueColor] {
			t.Errorf("profile %v says other things than full colour does:\n%s\n---\n%s", profile, got, texts[text.TrueColor])
		}
	}
}

// The wordmark: a fade through all four colours with full colour, plain
// colours with fewer, the letters alone with none.
func TestWordmark(t *testing.T) {
	if got := DefaultTheme(text.NoColor).wordmark(); got != "GNOSH" {
		t.Errorf("with no colour the wordmark is %q", got)
	}
	full := DefaultTheme(text.TrueColor).wordmark()
	if text.Strip(full) != "GNOSH" {
		t.Errorf("the wordmark reads %q", text.Strip(full))
	}
	if n := len(sgr.FindAllString(full, -1)); n < 5 {
		t.Errorf("with full colour the wordmark has %d style sequences, want one for each letter: %q", n, full)
	}
	for _, want := range []string{"38;2;0;255;178", "38;2;251;210;55"} {
		if !strings.Contains(full, want) {
			t.Errorf("the fade does not begin in mint and end in gold: %q lacks %s", full, want)
		}
	}
	for _, p := range []text.Profile{text.ANSI16, text.ANSI256} {
		got := DefaultTheme(p).wordmark()
		if text.Strip(got) != "GNOSH" || strings.Contains(got, "38;2;") {
			t.Errorf("profile %v draws the wordmark as %q", p, got)
		}
		// "GNO" in one green, and the rest from purple to pink.
		seqs := sgr.FindAllString(got, -1)
		var colours []string
		for _, s := range seqs {
			if s != "\x1b[0m" && s != "\x1b[m" {
				colours = append(colours, s)
			}
		}
		if len(colours) != 5 || colours[0] != colours[1] || colours[1] != colours[2] || colours[2] == colours[3] || colours[3] == colours[4] {
			t.Errorf("profile %v colours the letters %q", p, colours)
		}
	}
}

// Every colour the theme writes text in reads on a dark and on a light
// background: a contrast of at least 3.3 against white and 4.5 against
// black. The two brand colours are for the wordmark alone.
func TestPaletteReadsOnBothBackgrounds(t *testing.T) {
	pal := DefaultPalette(text.TrueColor)
	for name, c := range map[string]text.Color{
		"purple": pal.Purple, "pink": pal.Pink, "lilac": pal.Lilac, "green": pal.Green,
		"gold": pal.Gold, "rose": pal.Rose, "grey": pal.Grey,
	} {
		var r, g, b int
		if _, err := fmt.Sscanf(c.String(), "#%02x%02x%02x", &r, &g, &b); err != nil {
			t.Fatalf("%s is %v, not a 24-bit colour", name, c)
		}
		lum := 0.2126*linear(r) + 0.7152*linear(g) + 0.0722*linear(b)
		if white, black := 1.05/(lum+0.05), (lum+0.05)/0.05; white < 3.3 || black < 4.5 {
			t.Errorf("%s (%v) has a contrast of %.2f against white and %.2f against black", name, c, white, black)
		}
	}
}

// linear is one channel of an sRGB colour as light.
func linear(c int) float64 {
	v := float64(c) / 255
	if v <= 0.04045 {
		return v / 12.92
	}
	// (v+0.055)/1.055 to the power 2.4, without the math package's Pow
	// being worth an import: exp and log would do, but a few Newton
	// steps on the fifth root are exact enough for a contrast ratio.
	x := (v + 0.055) / 1.055
	// x^2.4 = x^2 * x^(2/5)
	root := x
	for range 40 {
		root -= (root*root*root*root*root - x*x) / (5 * root * root * root * root)
	}
	return x * x * root
}

// hostile is text that tries everything a terminal can be told: clear the
// screen, move the cursor, set the title, switch screens, turn on the
// mouse, write the clipboard, ring the bell, and the same by way of the
// eight-bit controls.
const hostile = "before \x1b[2J\x1b[H\x1b[1;1Htop \x1b]0;owned\x07 \x1b]52;c;b3duZWQ=\x07 \x1b[?1049h \x1b[?1000h\x1b[?1006h " +
	"\x1bc \x1b[5A \x1b[999D \r overwritten \x08\x08\x08 \x07 \xc2\x9b2J \xc2\x9d0;c1\xc2\x9c \x1bP+q544e\x1b\\ \x1b[6n \x1b[c after"

// An answer, a tool's output, a command, a notice, a question and a
// pasted prompt full of escape sequences: none of it reaches the terminal
// as an instruction. The scrollback is intact and what was hidden in the
// text is shown.
func TestHostileText(t *testing.T) {
	each(t, func(t *testing.T, cols int) {
		for _, profile := range []text.Profile{text.NoColor, text.TrueColor} {
			g := newRig(t, cols, 24, profile)
			g.s.input(term.Paste{Text: "pasted " + hostile})
			g.frame().keys(`<Enter>`).start()
			g.ev(thought("thinking " + hostile + "\n"))
			g.ev(said("An answer: " + hostile + "\n\n```\ncode " + hostile + "\n```\n\n| a | b |\n|---|---|\n| " + hostile + " | x |\n"))
			args := map[string]any{"command": "echo " + hostile, "description": hostile}
			g.ev(toolCall("c1", "Bash\x1b[2J"), toolInput("c1", `{"command":"echo \u001b[2J`), replyEnd(core.StopToolUse))
			g.ev(toolStart("c1", "Bash", args), progress("c1", "live "+hostile+"\n"))
			q := permission("q1")
			q.Title, q.Text = "Allow \x1b[2J?", "  $ echo "+hostile
			q.Choices[0].Label = "Yes \x1b]0;t\x07"
			g.ev(&agent.QuestionEvent{Question: q})
			g.wait(time.Second).keys(`<Enter>`)
			g.ev(&agent.AnswerEvent{ID: "q1"})
			g.ev(toolEnd("c1", "Bash", args, core.ToolResult{Content: []core.Block{core.Text(strings.Repeat("long ", 30000) + "\nout " + hostile + "\n")},
				Meta: map[string]any{"exitCode": 0}}, 0))
			edit := map[string]any{"file_path": "/tmp/\x1b[2Jevil\x07.go"}
			g.ev(toolStart("c2", "Edit", edit), toolEnd("c2", "Edit", edit, core.ToolResult{
				Display: &core.Display{Kind: "diff", Path: "/tmp/\x1b[2Jevil.go", Before: "a " + hostile + "\n", After: "b " + hostile + "\n"}}, 0))
			other := map[string]any{"weird": hostile}
			g.ev(toolStart("c3", "Mystery\x1b[H", other), toolEnd("c3", "Mystery\x1b[H", other, core.ToolResult{Content: []core.Block{core.Text(hostile)}}, 0))
			g.ev(&agent.NoticeEvent{Level: agent.LevelWarning, Text: "notice " + hostile})
			g.ev(hookEvent("PreToolUse\x1b[2J", "", "", "blocked"))
			g.ev(facts{"frame": hostile, "goal": hostile})
			g.ev(&app.TasksEvent{Tasks: []app.Task{{Content: hostile, Status: app.TaskCompleted}, {Content: "x", ActiveForm: hostile, Status: app.TaskInProgress}, {Content: hostile}}})
			g.ev(&agent.TurnEndEvent{Result: agent.TurnResult{Reason: agent.EndFailed, Detail: hostile}}, idle())
			g.keys(`<Ctrl+T> "?"`)

			if got := g.scr.Title(); got != "" {
				t.Errorf("the window's title was set to %q", got)
			}
			if got := g.scr.Clipboard(); len(got) > 0 {
				t.Errorf("the clipboard was written: %q", got)
			}
			if got := g.scr.Bells(); got != 0 {
				t.Errorf("the bell rang %d times", got)
			}
			if got := g.scr.TakeReplies(); len(got) > 0 {
				t.Errorf("the terminal was asked a question: it answered %q", got)
			}
			// The header is still where it was written, above everything.
			if first := g.all()[0]; !strings.HasPrefix(first, "GNOSH") {
				t.Errorf("the first line of the terminal is %q", first)
			}
			g.committedHas("before", "after", "pasted before").once("GNOSH test-build")
			// A carriage return in a command's output begins the line
			// again, as on a terminal, and the block says that it did.
			g.committedHas("overwritten", "(written over 1 earlier state of this line)")
			// What a tool wrote is shown as what it is.
			g.committedHas("^[[2J", "^[]0;owned^G")
		}
	})
}

// partialString reads a string argument out of JSON that is still being
// written exactly as the JSON decoder reads it out of the whole.
func TestPartialString(t *testing.T) {
	cases := []struct{ in, key, want string }{
		{`{"command":"go test`, "command", "go test"},
		{`{"command":"go test ./...","timeout":5}`, "command", "go test ./..."},
		{`{"timeout":5,"command":"ls \"a b\"\n`, "command", "ls \"a b\"\n"},
		{`{"a":{"command":"no"},"command":"yes`, "command", "yes"},
		{`{"a":[1,"x",{"b":"]"}],"command":"y\u00e9\ud83d\ude00`, "command", "y\xc3\xa9\xf0\x9f\x98\x80"},
		{`{"command":"tab\tback\\slash\/`, "command", "tab\tback\\slash/"},
		{`{"comm`, "command", ""},
		{`{"command":`, "command", ""},
		{`{"command":12}`, "command", ""},
		{`[1,2]`, "command", ""},
		{``, "command", ""},
		{`{"command":"half an escape \u00`, "command", "half an escape "},
	}
	for _, tc := range cases {
		if got, _ := partialString([]byte(tc.in), tc.key); got != tc.want {
			t.Errorf("partialString(%q, %q) = %q, want %q", tc.in, tc.key, got, tc.want)
		}
	}
}

// FuzzPartialString holds partialString to the JSON decoder: on a whole
// object it finds the same string, and on any beginning of one it finds a
// beginning of that string and never fails.
func FuzzPartialString(f *testing.F) {
	f.Add(`{"command":"go test ./...","n":1,"o":{"command":"x"},"l":["a","b"]}`, "command")
	f.Add(`{"code":"a\nb\t\"c\" \u00e9 \ud83d\ude00 \\ \/","name":"mem"}`, "code")
	f.Add(`{"a":"\ud83d","b":"\ude00x"}`, "b")
	f.Add(`{"k":"bell \b and feed \f"}`, "k")
	f.Add("{\"k\":\"two bad bytes \xff\xfe\"}", "k")
	f.Add(`{"\u006b":"first","k":"second"}`, "k")
	f.Fuzz(func(t *testing.T, in, key string) {
		got, ok := partialString([]byte(in), key)
		if want, isString, valid := firstString(in, key); valid && isString && (!ok || got != want) {
			t.Fatalf("partialString(%q, %q) = %q, %v; the decoder reads %q", in, key, got, ok, want)
		}
		for cut := 0; cut < len(in); cut++ {
			partialString([]byte(in[:cut]), key)
		}
	})
}

// firstString reads the value of the first key called key in a JSON
// object with the JSON decoder. valid is false when in is not one object.
func firstString(in, key string) (value string, isString, valid bool) {
	if !json.Valid([]byte(in)) {
		return "", false, false
	}
	dec := json.NewDecoder(strings.NewReader(in))
	if tok, err := dec.Token(); err != nil || tok != json.Delim('{') {
		return "", false, false
	}
	found := false
	for dec.More() {
		tok, err := dec.Token()
		name, isName := tok.(string)
		if err != nil || !isName {
			return "", false, false
		}
		var v any
		if dec.Decode(&v) != nil {
			return "", false, false
		}
		if name == key && !found {
			found = true
			value, isString = v.(string)
		}
	}
	return value, isString, true
}

// Under any stream of events and keys the screen keeps its promises:
// what is committed is on the terminal once, in order, and is never drawn
// again; the alternate screen and the mouse are never asked for; the
// cursor is where the prompt box or the question says; and the live
// region stays within its bound. (The rig checks all of that after every
// step.)
func TestRandomStreams(t *testing.T) {
	seeds := 40
	if testing.Short() {
		seeds = 8
	}
	for seed := range seeds {
		t.Run(fmt.Sprint(seed), func(t *testing.T) {
			r := rand.New(rand.NewPCG(uint64(seed), 99))
			cols, rows := 20+r.IntN(120), 4+r.IntN(40)
			g := newRig(t, cols, rows, []text.Profile{text.NoColor, text.TrueColor}[seed%2], func(c *controls) {
				c.bypass, c.info.NotifyHooks = seed%3 == 0, seed%2 == 0
			})
			g.scr.SetReflow(seed%4 < 2)
			g.s.inline.SetReflow(seed%4 < 2)
			for range 400 {
				randomStep(g, r)
				g.s.done = false
				// The clock never makes the loop spin: once the session
				// has seen the time, it asks to be woken later or not at
				// all.
				g.s.tick()
				if at := g.s.wake(); !at.IsZero() && !at.After(g.now) {
					t.Fatalf("the session asks to be woken at %v, which is not after now (%v)", at, g.now)
				}
				// The cursor is where the prompt box says, whenever the
				// box has the keys.
				if g.s.q == nil && g.liveRow >= 0 {
					v := g.s.ed.Render(g.cols)
					if v.CursorCol != g.liveCol {
						t.Fatalf("the box has its cursor at cell %d and the live region at %d\n%s", v.CursorCol, g.liveCol, g.dump())
					}
				}
			}
		})
	}
}

// FuzzSession drives a session from arbitrary bytes: each byte picks a
// step. It finds the streams TestRandomStreams does not think of.
func FuzzSession(f *testing.F) {
	f.Add([]byte("a session of some bytes, any bytes at all"), uint8(80), uint8(24))
	f.Add([]byte{0, 1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15, 200, 201, 202, 250, 251, 252, 253, 254, 255}, uint8(20), uint8(5))
	// A secret asked for in a terminal five rows tall, with a draft in
	// the box: found by the fuzzer.
	f.Add([]byte("a session of Yome by\x00\x00s, any byt000000000"), uint8('P'), uint8(0x18))
	// A terminal resized down to a single cell: found by the fuzzer.
	f.Add([]byte("\x00\x01\x02\x03\x04\x05\xa8\xba\xba\xba\xba\xba\xba\xba\x06\a\b\t\nB\f\r\x0e\x0f\xc8\xc9\xff\xff\x00\x00\xfd\xfe0"), uint8(0x14), uint8(0x05))
	f.Fuzz(func(t *testing.T, data []byte, cols, rows uint8) {
		if len(data) > 300 {
			data = data[:300]
		}
		g := newRig(t, int(cols)%150+1, int(rows)%50+1, text.NoColor)
		var seed [32]byte
		copy(seed[:], data)
		r := rand.New(rand.NewChaCha8(seed))
		for range data {
			randomStep(g, r)
			g.s.done = false
		}
	})
}

// The words a random session is written from: plain ones, Markdown, wide
// and combining characters, and everything a terminal could take for an
// instruction.
var randomWords = []string{
	"word ", "another ", "\n", "\n\n", "# heading\n", "- item\n", "1. one\n", "> quote\n", "```\n", "`code` ", "**bold** ", "*open ",
	"| a | b |\n|---|---|\n", "| 1 | 2 |\n", "[link](https://example.com) ", "a very long word without any spaces in it at all whatsoever-",
	"\xe6\x97\xa5\xe6\x9c\xac\xe8\xaa\x9e ", "e\xcc\x81 ", "\xf0\x9f\x91\x8d ", "\t", "\x1b[2J", "\x1b[H", "\x1b]0;title\x07", "\x1b[?1049h", "\x1b[?1000h",
	"\r", "\x07", "\x00", "\xff", "\xc2\x9b2J", "\xe2\x80\x8b", strings.Repeat("x", 300), "---\n", "<br>", "&amp; ",
}

func randomText(r *rand.Rand) string {
	var b strings.Builder
	for range 1 + r.IntN(6) {
		b.WriteString(randomWords[r.IntN(len(randomWords))])
	}
	return b.String()
}

// randomStep does one random thing to a session: something the app
// reports, in or out of its usual order; a key; a paste; a wait; a change
// of the terminal's size.
func randomStep(g *rig, r *rand.Rand) {
	g.t.Helper()
	id := func() string { return fmt.Sprintf("c%d", r.IntN(4)) }
	tools := []string{"Bash", "Read", "Edit", "Write", "Glob", "Grep", "gnovm_run", "Agent", "Deliver", "Other"}
	args := func() map[string]any {
		return map[string]any{"command": randomText(r), "file_path": "/home/jae/src/proj/" + randomText(r), "pattern": randomText(r),
			"code": randomText(r), "name": "mem", "description": randomText(r),
			"label": randomText(r), "target": randomText(r), "host": []string{"", "local", "render", randomText(r)}[r.IntN(4)]}
	}
	// A deliverable as a tool or a session may hand one over: any text in
	// any of its places.
	deliverable := func() app.Deliverable {
		d := app.Deliverable{Label: randomText(r), Pending: r.IntN(2) == 0}
		if r.IntN(3) > 0 {
			d.URL = []string{"https://example.com/", "http://127.0.0.1:7777/", ""}[r.IntN(3)] + randomText(r)
		}
		if r.IntN(2) == 0 {
			d.Path, d.Host = "/home/jae/"+randomText(r), []string{"", "render", randomText(r)}[r.IntN(3)]
		}
		return d
	}
	switch n := r.IntN(100); {
	case n < 22:
		keys := []term.Event{
			term.Key{Code: term.KeyEnter}, term.Key{Code: term.KeyEscape}, term.Key{Code: term.KeyUp}, term.Key{Code: term.KeyDown},
			term.Key{Code: term.KeyTab}, term.Key{Code: term.KeyTab, Mod: term.ModShift}, term.Key{Code: term.KeyBackspace},
			term.Key{Code: term.KeyLeft}, term.Key{Code: term.KeyRight}, term.Key{Code: term.KeyEnter, Mod: term.ModShift},
			term.Key{Code: term.KeyRune, Rune: 'c', Mod: term.ModCtrl}, term.Key{Code: term.KeyRune, Rune: 'd', Mod: term.ModCtrl},
			// (Ctrl+O, the transcript view, takes a screen of its own: see
			// TestTranscriptView.)
			term.Key{Code: term.KeyRune, Rune: 't', Mod: term.ModCtrl},
			term.Key{Code: term.KeyRune, Rune: 'l', Mod: term.ModCtrl}, term.Key{Code: term.KeyRune, Rune: 'u', Mod: term.ModCtrl},
			term.Key{Code: term.KeyRune, Rune: 'r', Mod: term.ModCtrl}, term.Key{Code: term.KeyRune, Rune: 's', Mod: term.ModCtrl},
			term.Key{Code: term.KeyRune, Rune: 'g', Mod: term.ModCtrl}, term.Key{Code: term.KeyRune, Rune: 'x', Mod: term.ModCtrl},
			term.Key{Code: term.KeyRune, Rune: 'z', Mod: term.ModCtrl}, term.Key{Code: term.KeyRune, Rune: 'b', Mod: term.ModCtrl},
			term.Key{Code: term.KeyRune, Rune: ' ', Text: " "}, term.Focus{In: true}, term.Mouse{}, term.Unknown{Raw: "\x1b[99~"},
			term.Resume{},
		}
		key := keys[r.IntN(len(keys))]
		if _, resumed := key.(term.Resume); resumed {
			// The terminal comes back after the process was stopped from
			// outside: the live region was erased before it was given up.
			g.s.r.Clear()
		}
		g.s.input(key)
	case n < 40:
		for _, c := range []string{"a", "b", " ", "/", "@", "?", "!", "1", "2", "y", "n", "j", "k", "\xe6\x97\xa5", "s", "t"}[r.IntN(16):][:1] {
			g.s.input(term.Key{Code: term.KeyRune, Rune: []rune(c)[0], Text: c})
		}
	case n < 43:
		g.s.input(term.Paste{Text: randomText(r) + strings.Repeat(randomText(r)+"\n", r.IntN(30))})
	case n < 48:
		g.now = g.now.Add(time.Duration(r.IntN(4000)) * time.Millisecond)
		g.s.tick()
	case n < 51:
		g.resize(1+r.IntN(150), 1+r.IntN(50))
		return
	case n < 54:
		g.s.handle(busy())
		g.s.handle(turnStart())
	case n < 56:
		g.s.handle(idle())
	case n < 60:
		g.s.handle(request(1 + r.IntN(3)))
	case n < 72:
		g.s.handle(said(randomText(r)))
	case n < 76:
		g.s.handle(thought(randomText(r)))
	case n < 79:
		g.s.handle(toolCall(id(), tools[r.IntN(len(tools))]))
		g.s.handle(toolInput(id(), `{"command":"`+strings.ReplaceAll(randomText(r), `"`, `'`)))
	case n < 82:
		stops := []core.StopReason{core.StopEndTurn, core.StopToolUse, core.StopCancelled, core.StopError, core.StopMaxTokens}
		g.s.handle(replyEnd(stops[r.IntN(len(stops))], core.Text(randomText(r))))
	case n < 85:
		g.s.handle(toolStart(id(), tools[r.IntN(len(tools))], args()))
	case n < 87:
		g.s.handle(progress(id(), randomText(r)))
	case n < 91:
		res := core.ToolResult{Content: []core.Block{core.Text(randomText(r))}, IsError: r.IntN(4) == 0, Meta: map[string]any{"exitCode": r.IntN(3), "numFiles": r.IntN(9)}}
		if r.IntN(3) == 0 {
			res.Display = &core.Display{Kind: "diff", Path: "/home/jae/src/proj/x.go", Before: randomText(r), After: randomText(r)}
		}
		if r.IntN(4) == 0 {
			res.Meta[app.MetaDeliverable], res.Meta[app.MetaReplaced] = deliverable(), r.IntN(2) == 0
		}
		end := toolEnd(id(), tools[r.IntN(len(tools))], args(), res, time.Duration(r.IntN(3000))*time.Millisecond)
		end.Denied, end.Interrupted, end.Ran = r.IntN(6) == 0, r.IntN(6) == 0, r.IntN(6) != 0
		g.s.handle(end)
	case n < 93:
		q := permission(fmt.Sprintf("q%d", r.IntN(3)))
		switch r.IntN(4) {
		case 0:
			q = agent.Question{ID: q.ID, Kind: "secret", Title: randomText(r)}
		case 1:
			q = agent.Question{ID: q.ID, Kind: "question", Title: randomText(r), Data: app.QuestionForm{Questions: []app.FormQuestion{
				{Question: "a?", Header: "A", Options: []app.FormOption{{Label: "x"}, {Label: "y"}}},
				{Question: "b?", Options: []app.FormOption{{Label: "z"}, {Label: randomText(r)}}, MultiSelect: true},
			}}}
		case 2:
			q.Text, q.Title = randomText(r), randomText(r)
			q.Action.Preview = &core.Display{Kind: "diff", Path: "x.go", Before: randomText(r), After: randomText(r)}
		}
		g.s.handle(&agent.QuestionEvent{Question: q})
	case n < 95:
		g.s.handle(&agent.AnswerEvent{ID: fmt.Sprintf("q%d", r.IntN(3)), Withdrawn: r.IntN(2) == 0})
	case n < 96:
		g.s.handle(&agent.RetryEvent{Purpose: "main", Wait: time.Duration(r.IntN(9)) * time.Second, Kind: core.ErrNetwork, Message: randomText(r), Discarded: r.IntN(2) == 0})
	case n < 97:
		reasons := []agent.EndReason{agent.EndCompleted, agent.EndStopped, agent.EndHalted, agent.EndFailed, agent.EndModelStopped, agent.EndRefused}
		g.s.handle(&agent.TurnEndEvent{Result: agent.TurnResult{Reason: reasons[r.IntN(len(reasons))], Detail: randomText(r), Duration: time.Second}})
	case n < 98:
		g.c.queue = queue(randomText(r), randomText(r))
		g.s.handle(&agent.QueueEvent{Queued: g.c.queue})
	case n < 99:
		levels := []agent.Level{agent.LevelInfo, agent.LevelWarning, agent.LevelError}
		g.s.handle(&agent.NoticeEvent{Level: levels[r.IntN(3)], Text: randomText(r)})
		g.s.handle(hookEvent("PreToolUse", id(), "Bash", "none", "blocked"))
		g.s.handle(facts{"frame": randomText(r)})
		tasks := make([]app.Task, r.IntN(14))
		for i := range tasks {
			tasks[i] = app.Task{Content: randomText(r), ActiveForm: randomText(r), Status: []string{app.TaskPending, app.TaskInProgress, app.TaskCompleted, "?"}[r.IntN(4)]}
		}
		g.c.tasks = tasks
		g.s.handle(&app.TasksEvent{Tasks: tasks})
		list := make([]app.Deliverable, r.IntN(5))
		for i := range list {
			list[i] = deliverable()
		}
		g.c.deliv = list
		g.s.handle(&app.DeliverablesEvent{List: list})
		if r.IntN(2) == 0 {
			g.s.listDeliverables("/artifacts")
		}
	default:
		g.s.handle(&agent.PromptReturnedEvent{ID: "p1", Prompt: agent.Prompt{Text: randomText(r)}, Reason: randomText(r)})
		g.s.handle(&agent.LostEvent{Events: 3})
		g.s.handle(&agent.PromptEvent{ID: "p9", Prompt: agent.Prompt{Text: randomText(r)}})
	}
	g.frame()
}

// Jae's colours: his prompts in white and the model's prose in a phosphor
// green on a dark background, as he asked; on a terminal that says its
// background is light, the terminal's own colour and a green that reads
// on white.
func TestPromptAndReplyColours(t *testing.T) {
	th := DefaultTheme(text.TrueColor)
	if seq := th.PromptText.Sequence(); !strings.Contains(seq, "38;2;255;255;255") {
		t.Errorf("a prompt's text is %q, not white", seq)
	}
	if seq := th.Editor.Text.Sequence(); !strings.Contains(seq, "38;2;255;255;255") {
		t.Errorf("the box's text is %q, not white", seq)
	}
	if seq := th.Markdown.Text.Sequence(); !strings.Contains(seq, "38;2;74;222;128") {
		t.Errorf("an answer's prose is %q, not the green", seq)
	}
	if th.Thinking == th.Markdown.Text {
		t.Error("thinking is drawn as the answer is")
	}
	light := NewTheme(text.TrueColor, DefaultPalette(text.TrueColor).OnLight(text.TrueColor))
	if seq := light.PromptText.Sequence(); strings.Contains(seq, "38;2") {
		t.Errorf("on a light background a prompt's text is %q", seq)
	}
	if seq := light.Markdown.Text.Sequence(); !strings.Contains(seq, "38;2;21;128;61") {
		t.Errorf("on a light background an answer's prose is %q", seq)
	}
	sixteen := DefaultTheme(text.ANSI16)
	if seq := sixteen.PromptText.Sequence(); !strings.Contains(seq, "97") {
		t.Errorf("with sixteen colours a prompt's text is %q, not bright white", seq)
	}
	if seq := sixteen.Markdown.Text.Sequence(); !strings.Contains(seq, "92") {
		t.Errorf("with sixteen colours an answer's prose is %q, not bright green", seq)
	}
}
