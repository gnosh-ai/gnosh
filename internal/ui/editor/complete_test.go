package editor

import (
	"fmt"
	"strings"
	"testing"
)

// hook stands in for the application's Complete function: commands for a
// "/" word at the start, files for an "@" word anywhere, and earlier shell
// commands for a "!" line. It keeps a record of what it was asked.
type hook struct {
	calls []string
}

var hookCommands = []struct{ name, about string }{
	{"/clear", "Start a fresh session"},
	{"/compact", "Summarize the conversation now"},
	{"/context", "Show what fills the context"},
	{"/copy", "Copy the last response"},
	{"/mem", "The plan and the findings"},
	{"/mode", "Switch the permission mode"},
	{"/model", "Pick the model and its thinking level"},
	{"/no_think", "Thinking off"},
	{"/think", "Thinking on"},
}

var hookFiles = []string{"README.md", "spec/", "src/", "src/main.go", "src/main_test.go", "src/util/", "src/util/x.go"}

var hookShell = []string{"git diff", "git status", "make test"}

func (h *hook) complete(text string, cursor int) []Candidate {
	h.calls = append(h.calls, fmt.Sprintf("%s|%d", text, cursor))
	const spaces = " \n\t"
	start, end := cursor, cursor
	for start > 0 && !strings.ContainsRune(spaces, rune(text[start-1])) {
		start--
	}
	for end < len(text) && !strings.ContainsRune(spaces, rune(text[end])) {
		end++
	}
	word := text[start:cursor]
	var out []Candidate
	switch {
	case start == 0 && strings.HasPrefix(word, "/"):
		for _, c := range hookCommands {
			if strings.HasPrefix(c.name, word) {
				out = append(out, Candidate{Text: c.name + " ", Start: start, End: end, Label: c.name, Detail: c.about, Run: true})
			}
		}
	case strings.HasPrefix(word, "@"):
		path := word[1:]
		dir := path[:strings.LastIndex(path, "/")+1]
		for _, f := range hookFiles {
			parent := f[:strings.LastIndex(strings.TrimSuffix(f, "/"), "/")+1]
			if parent != dir || !strings.HasPrefix(f, path) {
				continue
			}
			c := Candidate{Text: "@" + f, Start: start, Label: f}
			if !strings.HasSuffix(f, "/") {
				c.Text += " "
			}
			out = append(out, c)
		}
	case strings.HasPrefix(text, "!"):
		for _, c := range hookShell {
			if strings.HasPrefix(c, text[1:cursor]) {
				out = append(out, Candidate{Text: "!" + c, Start: 0, End: len(text)})
			}
		}
	}
	return out
}

// completing makes a rig whose editor completes with a hook.
func completing(t *testing.T, width int, o Options) (*rig, *hook) {
	h := &hook{}
	o.Complete = h.complete
	return newRig(t, width, o), h
}

// open reports whether the list of candidates is showing.
func (g *rig) open(want bool) *rig {
	g.t.Helper()
	if got := g.e.MenuOpen(); got != want {
		g.t.Fatalf("list of candidates open: %v, want %v (text %q)", got, want, g.e.Text())
	}
	return g
}

// A slash opens the list of commands, which narrows as letters are typed.
func TestCompletionOpensAndNarrows(t *testing.T) {
	g, h := completing(t, 60, Options{})
	g.open(false)
	g.do(`"/"`).open(true).screen(0, 2,
		" /",
		"  > /clear     Start a fresh session",
		"    /compact   Summarize the conversation now",
		"    /context   Show what fills the context",
		"    /copy      Copy the last response",
		"    /mem       The plan and the findings",
		"↓   /mode      Switch the permission mode")
	g.do(`"mo"`).open(true).screen(0, 4,
		" /mo",
		"  > /mode   Switch the permission mode",
		"    /model  Pick the model and its thinking level")
	// Deleting widens it again.
	g.do(`<Backspace>`).open(true).screen(0, 3,
		" /m",
		"  > /mem    The plan and the findings",
		"    /mode   Switch the permission mode",
		"    /model  Pick the model and its thinking level")
	// Nothing fits: the list goes.
	g.do(`"zz"`).open(false).screen(0, 5, " /mzz")
	if want := []string{"/|1", "/m|2", "/mo|3", "/m|2", "/mz|3", "/mzz|4"}; fmt.Sprint(h.calls) != fmt.Sprint(want) {
		t.Errorf("the hook was asked %q, want %q", h.calls, want)
	}
}

// Tab and Enter take the candidate chosen; Up and Down choose, going
// round.
func TestCompletionAccept(t *testing.T) {
	g, _ := completing(t, 60, Options{})
	g.do(`"/mo" <Tab>`).text("/mode ").cursor(6).open(false).screen(0, 7, " /mode")
	if g.last.Action != ActionNone || !g.last.Changed {
		t.Errorf("Tab on a candidate: %+v", g.last)
	}
	// It is one step of the undo history.
	g.do(`<Ctrl+_>`).text("/mo")

	// Enter takes a command and runs it; one that does not run is only
	// taken, for what must follow it to be typed.
	g, _ = completing(t, 60, Options{})
	g.do(`"/mo" <Enter>`).submitted("/mode ", ModeCommand).open(false)
	g, _ = completing(t, 60, Options{})
	g.do(`"/mo" <Down> <Enter>`).submitted("/model ", ModeCommand)
	g = newRig(t, 60, Options{Complete: func(text string, cursor int) []Candidate {
		return []Candidate{{Text: "/background "}}
	}})
	g.do(`"/ba" <Enter>`).action(ActionNone).text("/background ")
	g, _ = completing(t, 60, Options{})
	g.do(`"/mo" <Down> <Down> <Tab>`).text("/mode ")
	g, _ = completing(t, 60, Options{})
	g.do(`"/c" <Up> <Tab>`).text("/copy ")
	g, _ = completing(t, 60, Options{})
	g.do(`"/c" <Ctrl+N> <Ctrl+N> <Ctrl+P> <Tab>`).text("/compact ")
	g, _ = completing(t, 60, Options{MenuHeight: 2})
	g.do(`"/" <PgDn> <PgDn> <PgDn> <Tab>`).text("/model ")
	g, _ = completing(t, 60, Options{MenuHeight: 2})
	g.do(`"/" <PgDn>*9 <PgUp> <Tab>`).text("/model ")
	g, _ = completing(t, 60, Options{MenuHeight: 2})
	g.do(`"/" <PgUp> <Tab>`).text("/clear ")
}

// A command typed in full and Enter runs it: an exact name always wins
// over one that only begins the same way.
func TestCompletionEnterOnExactSends(t *testing.T) {
	g, _ := completing(t, 60, Options{})
	g.do(`"/mode"`).open(true).screen(0, 6,
		" /mode",
		"  > /mode   Switch the permission mode",
		"    /model  Pick the model and its thinking level")
	g.do(`<Enter>`).submitted("/mode", ModeCommand).open(false)

	g, _ = completing(t, 60, Options{})
	g.do(`"/model" <Enter>`).submitted("/model", ModeCommand)

	// Choosing another candidate first runs that one instead.
	g, _ = completing(t, 60, Options{})
	g.do(`"/mode" <Down> <Enter>`).submitted("/model ", ModeCommand)

	// A file named in full is sent with the prompt.
	g, _ = completing(t, 60, Options{})
	g.do(`"see @README.md" <Enter>`).submitted("see @README.md", ModePrompt)

	// With the cursor inside the word, the whole word counts.
	g, _ = completing(t, 60, Options{})
	g.do(`"/model" <Left>*3`).open(true).cursor(3)
	g.do(`<Enter>`).submitted("/model", ModeCommand)
	g, _ = completing(t, 60, Options{})
	g.do(`"/model" <Left>*3 <Up> <Tab>`).text("/mode ").cursor(6)
}

// When what was typed stops being an exact name, the choice the person
// made is kept; when it becomes one, the exact name is chosen.
func TestCompletionChoice(t *testing.T) {
	g, _ := completing(t, 60, Options{})
	g.do(`"/c" <Down> <Down> "o"`).screen(0, 4,
		" /co",
		"    /compact  Summarize the conversation now",
		"  > /context  Show what fills the context",
		"    /copy     Copy the last response")
	g.do(`"p"`).screen(0, 5, " /cop", "  > /copy  Copy the last response")

	// A hook that offers near matches too, the wrong one first.
	loose := func(text string, cursor int) []Candidate {
		if !strings.HasPrefix(text, "/mo") {
			return nil
		}
		return []Candidate{{Text: "/model ", Label: "/model"}, {Text: "/mode ", Label: "/mode"}}
	}
	g = newRig(t, 60, Options{Complete: loose})
	g.do(`"/mod"`).screen(0, 5, " /mod", "  > /model", "    /mode")
	g.do(`"e"`).screen(0, 6, " /mode", "    /model", "  > /mode")
	g.do(`"l"`).screen(0, 7, " /model", "  > /model", "    /mode")
	g.do(`<Backspace>`).screen(0, 6, " /mode", "    /model", "  > /mode")
	g.do(`<Enter>`).submitted("/mode", ModeCommand)
}

// Esc closes the list and nothing else, and it stays closed for that
// word.
func TestCompletionDismiss(t *testing.T) {
	g, _ := completing(t, 60, Options{})
	g.do(`"/mo" <Esc>`).open(false).text("/mo")
	if !g.last.Handled || g.last.Hint != HintNone || g.e.Hint() != HintNone {
		t.Errorf("Esc on the list: %+v", g.last)
	}
	g.do(`"d"`).open(false)
	g.do(`<Backspace>`).open(false)
	// The next Esc is the ordinary first Esc.
	g.do(`<Esc>`).text("/mo")
	if g.last.Hint != HintEscClear {
		t.Errorf("Esc after the list closed: %+v", g.last)
	}
	// Tab asks for it again.
	g.do(`<Tab>`).open(true)
	g.do(`<Tab>`).text("/mode ").open(false)
	// Another word opens by itself.
	g.do(`"x @s"`).open(true).screen(0, 11, " /mode x @s", "  > spec/", "    src/")
	g.do(`<Esc> "r"`).open(false)
	g.do(`" @R"`).open(true)

	// Stopping what runs comes after closing the list.
	g, _ = completing(t, 60, Options{})
	g.e.SetBusy(true)
	g.do(`"/mo" <Esc>`).action(ActionNone).open(false)
	g.do(`<Esc>`).action(ActionInterrupt)
}

// Tab asks for candidates where typing does not: a lone one is taken at
// once, several open the list.
func TestCompletionOnTab(t *testing.T) {
	g, h := completing(t, 60, Options{})
	g.do(`"!gi"`).open(false)
	if len(h.calls) != 0 {
		t.Errorf("typing a shell line asked the hook: %q", h.calls)
	}
	g.do(`<Tab>`).open(true).screen(0, 4, "! gi", "  > !git diff", "    !git status")
	g.do(`<Down> <Enter>`).text("!git status").open(false)

	g, _ = completing(t, 60, Options{})
	g.do(`"!m" <Tab>`).text("!make test").cursor(10).open(false)
	// Nothing to offer: nothing happens.
	g.do(`<Ctrl+U> "zz" <Tab>`).text("!zz").open(false)
	// A lone candidate that is already there, letter for letter.
	g.do(`<Ctrl+U> "make test" <Tab>`).text("!make test").open(false)

	// A word in the middle of a prompt.
	g, _ = completing(t, 60, Options{})
	g.do(`"a/b"`).open(false)
	g.do(`<Tab>`).open(false).text("a/b")
}

// Taking a directory opens the list again with what is inside it.
func TestCompletionGoesOn(t *testing.T) {
	g, _ := completing(t, 60, Options{})
	g.do(`"see @sr"`).open(true).screen(0, 9, "> see @sr", "  > src/")
	g.do(`<Tab>`).text("see @src/").open(true).screen(0, 11,
		"> see @src/",
		"  > src/main.go",
		"    src/main_test.go",
		"    src/util/")
	g.do(`<Down> <Tab>`).text("see @src/main_test.go ").open(false)
	g.do(`<Enter>`).submitted("see @src/main_test.go ", ModePrompt)

	// A file taken in full does not open a list of itself.
	g, _ = completing(t, 60, Options{})
	g.do(`"@READ" <Tab>`).text("@README.md ").open(false)
}

// What moves the cursor out of the word closes the list, and the list
// takes Up and Down from the history.
func TestCompletionAndOtherKeys(t *testing.T) {
	g, _ := completing(t, 60, Options{})
	g.e.SetHistory(drafts("old"))
	g.do(`"/c" <Up>`).text("/c").open(true)
	g.do(`<Down> <Down>`).text("/c").open(true)

	g, _ = completing(t, 60, Options{})
	g.do(`"x @sr"`).open(true)
	g.do(`<Left>`).open(true)
	g.do(`<Left>*2`).open(false).cursor(2)
	g.do(`<Right>`).open(false)

	g, _ = completing(t, 60, Options{})
	g.do(`"/mo" <Ctrl+J>`).open(false).text("/mo\n")
	g, _ = completing(t, 60, Options{})
	g.do(`"/mo" <Ctrl+C>`).open(false).text("")
	g, _ = completing(t, 60, Options{})
	g.do(`"/mo" <Ctrl+W>`).open(true).text("/")
	g, _ = completing(t, 60, Options{})
	g.do(`"/" <Paste "mo">`).open(true).screen(0, 4,
		" /mo",
		"  > /mode   Switch the permission mode",
		"    /model  Pick the model and its thinking level")
	// Shift+Tab is never the box's: it changes the permission mode.
	if r := g.e.Handle(key("Shift+Tab")); r.Handled {
		t.Errorf("Shift+Tab with the list open: %+v", r)
	}
	g.open(true)
	// Ctrl+R closes the list and opens the search.
	g.do(`<Ctrl+R>`)
	if !g.e.Searching() {
		t.Error("Ctrl+R with the list open did not open the search")
	}
	// Loading text closes it.
	g, _ = completing(t, 60, Options{})
	g.do(`"/mo"`)
	g.e.SetText("other")
	g.open(false)
}

// A candidate takes the place of exactly the part of the text it names,
// and one that names a part that is not there is left out.
func TestCandidateRanges(t *testing.T) {
	var offer []Candidate
	g := newRig(t, 60, Options{Complete: func(string, int) []Candidate { return offer }})
	g.do(`"ab 世 cd" <Left>*3`).cursor(6)

	// From Start to the cursor.
	offer = []Candidate{{Text: "XY", Start: 3}}
	g.do(`<Tab>`).text("ab XY cd").cursor(5)
	// From Start to End, when End is past the cursor.
	offer = []Candidate{{Text: "Z", Start: 3, End: 8}}
	g.do(`<Tab>`).text("ab Z").cursor(4)
	// An End before the cursor is the cursor.
	offer = []Candidate{{Text: "!", Start: 4, End: 1}}
	g.do(`<Tab>`).text("ab Z!")
	// Several lines.
	offer = []Candidate{{Text: "one\ntwo", Start: 0}}
	g.do(`<Tab>`).text("one\ntwo").cursor(7)

	// Left out: before the text, after the cursor, past the end, inside
	// a character, inside a collapsed paste.
	g.e.SetDraft(Draft{Text: "世PASTEDx", Pastes: []Span{{3, 9}}})
	offer = []Candidate{
		{Text: "a", Start: -1}, {Text: "b", Start: 11}, {Text: "c", Start: 0, End: 99},
		{Text: "d", Start: 1}, {Text: "e", Start: 5}, {Text: "f", Start: 0, End: 0},
	}
	g.do(`<Tab>`).text("f").open(false)
	g.e.SetDraft(Draft{Text: "世PASTEDx", Pastes: []Span{{3, 9}}})
	offer = offer[:5]
	g.do(`<Tab>`).text("世PASTEDx").open(false)
}

// The list scrolls to keep the choice in view and marks what is hidden.
func TestCompletionView(t *testing.T) {
	g, _ := completing(t, 60, Options{MenuHeight: 3})
	g.do(`"/"`).screen(0, 2,
		" /",
		"  > /clear     Start a fresh session",
		"    /compact   Summarize the conversation now",
		"↓   /context   Show what fills the context")
	g.do(`<Down>*3`).screen(0, 2,
		" /",
		"↑   /compact   Summarize the conversation now",
		"    /context   Show what fills the context",
		"↓ > /copy      Copy the last response")
	g.do(`<Up>*4`).screen(0, 2,
		" /",
		"↑   /model     Pick the model and its thinking level",
		"    /no_think  Thinking off",
		"  > /think     Thinking on")
	g.do(`<Down>`).screen(0, 2,
		" /",
		"  > /clear     Start a fresh session",
		"    /compact   Summarize the conversation now",
		"↓   /context   Show what fills the context")
	// One row: both marks at once.
	g, _ = completing(t, 60, Options{MenuHeight: 1})
	g.do(`"/" <Down>`).screen(0, 2, " /", "↕ > /compact   Summarize the conversation now")

	// The few words beside a candidate are cut to fit, and left out
	// when there is no room for them.
	g, _ = completing(t, 40, Options{})
	g.do(`"/mo"`).screen(0, 4,
		" /mo",
		"  > /mode   Switch the permission mode",
		"    /model  Pick the model and its thin…")
	g, _ = completing(t, 20, Options{})
	g.do(`"/mo"`).screen(0, 4, " /mo", "  > /mode", "    /model")
	// No room for a list at all: the box is still sound.
	g, _ = completing(t, 5, Options{})
	g.do(`"/mo"`).open(true).screen(0, 2, "mo")

	// A label too long for the row keeps its end, where candidates
	// differ. Control characters in what the application passes are
	// drawn harmless.
	offer := []Candidate{
		{Text: "x", Label: "very/long/path/to/some/file.go"},
		{Text: "y", Label: "a\x1b[31mb", Detail: "c\nd \u009b0m"},
	}
	g = newRig(t, 20, Options{Complete: func(string, int) []Candidate { return offer }})
	g.do(`"q" <Tab>`).screen(0, 3, "> q", "  > …to/some/file.go", "    a^[[31mb")
	g = newRig(t, 60, Options{Complete: func(string, int) []Candidate { return offer }})
	g.do(`"q" <Tab> <Down>`).screen(0, 3,
		"> q",
		"    very/long/path/to/some/file.go",
		"  > a^[[31mb"+strings.Repeat(" ", 24)+"c^Jd <U+009B>0m")
}

// The characters that open the list as a word is typed are the
// application's to choose.
func TestCompletionTriggers(t *testing.T) {
	offer := func(text string, cursor int) []Candidate {
		return []Candidate{{Text: "#issue ", Start: strings.LastIndex(text[:cursor], "#")}}
	}
	g := newRig(t, 40, Options{Complete: offer, Triggers: "#"})
	g.do(`"/x "`).open(false)
	g.do(`"#i"`).open(true)
	g.do(`<Tab>`).text("/x #issue ")
}

// Tab moves on when the candidate chosen is already in the box, letter
// for letter.
func TestCompletionTabMovesOn(t *testing.T) {
	offer := func(text string, cursor int) []Candidate {
		if text == "@alpha" {
			return []Candidate{{Text: "@alpha"}, {Text: "@alphabet"}}
		}
		return nil
	}
	g := newRig(t, 40, Options{Complete: offer})
	g.do(`"@alpha"`).screen(0, 8, "> @alpha", "  > @alpha", "    @alphabet")
	g.do(`<Tab>`).text("@alpha").screen(0, 8, "> @alpha", "    @alpha", "  > @alphabet")
	g.do(`<Tab>`).text("@alphabet").open(false)
	// With one candidate and nothing to add, Tab closes the list.
	one := func(text string, cursor int) []Candidate { return []Candidate{{Text: "@a"}} }
	g = newRig(t, 40, Options{Complete: one})
	g.do(`"@a"`).open(true)
	g.do(`<Tab>`).open(false).text("@a")
}

// After the name of a command the list opens by itself for what the
// command takes, when the application has something to offer there.
func TestCompletionOfCommandArguments(t *testing.T) {
	models := func(text string, cursor int) []Candidate {
		rest, ok := strings.CutPrefix(text[:cursor], "/model ")
		if !ok {
			return nil
		}
		var out []Candidate
		for _, m := range []string{"opus", "qwen-27b", "qwen-coder"} {
			if strings.HasPrefix(m, rest) {
				out = append(out, Candidate{Text: m, Start: len("/model ")})
			}
		}
		return out
	}
	g := newRig(t, 40, Options{Complete: models})
	g.do(`"/model"`).open(false)
	g.do(`" "`).open(true).screen(0, 8, " /model", "  > opus", "    qwen-27b", "    qwen-coder")
	g.do(`"q"`).screen(0, 9, " /model q", "  > qwen-27b", "    qwen-coder")
	g.do(`<Down> <Enter>`).text("/model qwen-coder").open(false)
	g.do(`<Enter>`).submitted("/model qwen-coder", ModeCommand)
	// A name typed in full is sent by Enter.
	g.do(`"/model opus" <Enter>`).submitted("/model opus", ModeCommand)
	// On a prompt, words after the first open nothing by themselves.
	g.do(`"model q"`).open(false)
}

// A candidate that only resembles what was typed never takes its place
// on Enter: what is sent is what was typed. (The earlier gnosh once ran
// "Thinking On" for the word "nothink".)
func TestCompletionNeverSwapsUnasked(t *testing.T) {
	fuzzy := func(text string, cursor int) []Candidate {
		if strings.HasPrefix(text, "/n") {
			return []Candidate{{Text: "/think "}, {Text: "/no_think "}}
		}
		return nil
	}
	g := newRig(t, 40, Options{Complete: fuzzy})
	g.do(`"/nothink"`).open(true).screen(0, 9, " /nothink", "  > /think", "    /no_think")
	g.do(`<Enter>`).submitted("/nothink", ModeCommand)
	// Chosen with the arrows, it is taken.
	g.do(`"/nothink" <Down> <Enter>`).action(ActionNone).text("/no_think ")
	g.do(`<Enter>`).submitted("/no_think ", ModeCommand)
	// Tab asks for it, so Tab takes it.
	g.do(`"/nothink" <Tab>`).text("/think ")
	g.do(`<Ctrl+U>*2`)
	// A candidate that begins with what was typed, in any case, is
	// taken by Enter.
	caseless := func(text string, cursor int) []Candidate { return []Candidate{{Text: "/No_Think "}} }
	g = newRig(t, 40, Options{Complete: caseless})
	g.do(`"/no_t" <Enter>`).action(ActionNone).text("/No_Think ")
}
