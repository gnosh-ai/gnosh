package ui

import (
	"time"

	"github.com/gnosh-ai/gnosh/internal/diff"
	"github.com/gnosh-ai/gnosh/internal/markdown"
	"github.com/gnosh-ai/gnosh/internal/text"
	"github.com/gnosh-ai/gnosh/internal/ui/editor"
	"github.com/gnosh-ai/gnosh/internal/ui/menu"
)

// Palette is every colour the interface uses, in one place. The purple and
// the hot pink are gnosh's own and are placeholders until Jae picks the
// final pair (spec/gnosh/harness-open-questions.md, OQ-26): change them
// here, or hand New a Theme made from another Palette, and every part of
// the screen follows, the prompt box, the questions, the Markdown and the
// diffs included.
//
// The first seven are for text. Each is a middle tone, about as far in
// brightness from a black background as from a white one, so that it
// reads on both (theme_test.go holds them to that). The last two are the
// mint and the gold of the brand as the specs give them (UI-50). They are
// too light to read on a white background and are used for nothing but
// the wordmark and the working mark.
type Palette struct {
	// Purple is for structure: the marks gnosh draws itself, links.
	Purple text.Color
	// Pink is the one accent: the prompt mark, what is chosen, headings.
	Pink text.Color
	// Lilac is calm: code, addresses, rules.
	Lilac text.Color
	// Green is what came or went well: inserted lines, a command line.
	Green text.Color
	// Gold is a warning.
	Gold text.Color
	// Rose is what went or went wrong: deleted lines, failures.
	Rose text.Color
	// Grey is everything that is not the text itself: counts, times,
	// notes. Default (the zero Color) means "write it faint instead", for
	// terminals whose palette has no safe grey.
	Grey text.Color

	// Mint and Sun are the brand's green and gold, for the wordmark; the
	// gold is also one of the colours of the working mark.
	Mint, Sun text.Color
	// Aqua is the blue-green of gnosh.ai, for the prompt mark, the pointer
	// and the working mark. Jae chose it for the mark (14 November 2026:
	// "make it one of the blue/green from gnosh.ai site"). Like the
	// brand's two it is light: on a white background it is faint, and
	// nothing but a mark is drawn in it.
	Aqua text.Color

	// Prompt is the text of the person's prompts, in the box and in the
	// conversation, and Reply the prose of the model's answers (not its
	// thinking, not code). Jae chose them (16 November 2026: "let gnosh my
	// prompt be mmm... white, sure, and the response (not thinking) be
	// sorta green, neo"): white, and a phosphor green. They are for a dark
	// background, as the brand's are; on a terminal that says its
	// background is light, OnLight gives them back the terminal's own
	// colour and a green dark enough to read there. Default is the
	// terminal's own colour.
	Prompt, Reply text.Color
}

// OnLight is the palette for a terminal whose background is light: the
// person's prompts in the terminal's own colour, and the answers in a
// green that reads on white.
func (pal Palette) OnLight(p text.Profile) Palette {
	pal.Prompt = text.Default
	pal.Reply = text.RGB(0x15, 0x80, 0x3D)
	if p == text.ANSI16 {
		pal.Reply = text.Green
	}
	return pal
}

// DefaultPalette returns gnosh's own colours for the profile p. With
// sixteen colours they are the terminal's own, which its owner reads every
// day: the nearest of the sixteen to a soft colour is often a grey that
// some palettes make unreadable.
func DefaultPalette(p text.Profile) Palette {
	if p == text.ANSI16 {
		return Palette{
			Purple: text.Magenta, Pink: text.BrightMagenta, Lilac: text.Cyan,
			Green: text.Green, Gold: text.Yellow, Rose: text.Red, Grey: text.Default,
			Mint: text.Green, Sun: text.Yellow, Aqua: text.BrightCyan,
			Prompt: text.BrightWhite, Reply: text.BrightGreen,
		}
	}
	return Palette{
		Purple: text.RGB(0x9D, 0x5C, 0xFF),
		Pink:   text.RGB(0xF0, 0x25, 0x7F),
		Lilac:  text.RGB(0x8B, 0x7F, 0xB8),
		Green:  text.RGB(0x18, 0x90, 0x5C),
		Gold:   text.RGB(0xA9, 0x7C, 0x00),
		Rose:   text.RGB(0xD6, 0x3A, 0x70),
		Grey:   text.RGB(0x7A, 0x7A, 0x7A),
		Mint:   text.RGB(0x00, 0xFF, 0xB2),
		Sun:    text.RGB(0xFB, 0xD2, 0x37),
		Aqua:   text.RGB(0x68, 0xFF, 0xD6),
		Prompt: text.RGB(0xFF, 0xFF, 0xFF),
		Reply:  text.RGB(0x4A, 0xDE, 0x80),
	}
}

// Theme is the look of the whole interface: every mark it draws and every
// style it writes, with the looks of the parts it is made of (the prompt
// box, the questions, Markdown, diffs). Nothing is said by colour alone:
// each kind of line has a mark or a word of its own, so the screen reads
// the same under text.NoColor.
type Theme struct {
	// Profile is how much colour the terminal takes. Every style below is
	// already set to it.
	Profile text.Profile
	// Palette is the colours the styles were made from.
	Palette Palette

	// Name is the wordmark, and WordmarkStops the colours it fades
	// through with full colour. With fewer colours the wordmark is drawn
	// in WordmarkPlain: one style for each of its letters, the last
	// repeated when the name is longer.
	Name          string
	WordmarkStops []text.Color
	WordmarkPlain []text.Style
	// Wordmark is the wordmark's style apart from its colours.
	Wordmark text.Style

	// PromptMark stands before a prompt the person sent, where it is
	// written into the scrollback, and ShellMark before a command line.
	PromptMark, ShellMark string
	// ToolMark stands before a tool call, FailMark before one that
	// failed or was refused, and SubMark before what a sub-agent did.
	ToolMark, FailMark, SubMark string
	// BusyMark stands before the line that says what gnosh is doing, where
	// nothing moves: with no colour, and while a question waits.
	BusyMark string
	// Fall is what moves in the line that says what gnosh is doing: one
	// mark that turns before the words, and colour that falls to the
	// right through the words themselves, more of it, faster and in more
	// colours the deeper the model is in a thought (fall.go). With no
	// colour there is none.
	Fall Fall
	// ThinkMark stands before the model's reasoning.
	ThinkMark string
	// AskMark stands before the record of a question and its answer.
	AskMark string
	// TaskDone, TaskNow and TaskTodo stand before the lines of the task
	// list: a task that is done, the one in hand, one still to do.
	TaskDone, TaskNow, TaskTodo string
	// DeliverMark stands before a deliverable there is something to open
	// of, and PendingMark before one that is claimed and not there yet.
	DeliverMark, PendingMark string
	// Sep separates the parts of the status line and of other one-line
	// summaries.
	Sep string

	// Prompt is the style of PromptMark and PromptText of the prompt's
	// words.
	Prompt, PromptText text.Style
	// Tool is the style of ToolMark, ToolName of the tool's name and
	// ToolArg of what the call is about: the command, the file.
	Tool, ToolName, ToolArg text.Style
	// Output is a line of a command's output and Code a line of code
	// shown as a heading. Neither has a border or a background, so that
	// what is selected is the text itself.
	Output, Code text.Style
	// Fail is the style of FailMark and of the word that says a call
	// failed.
	Fail text.Style
	// Quiet is everything that is not the text itself: counts, times,
	// what was left out, the keys a line names.
	Quiet text.Style
	// Thinking is the model's reasoning.
	Thinking text.Style
	// NoteLabel, WarnLabel and ErrorLabel are the words that lead a
	// notice ("note:", "warning:", "error:"); NoteText is the rest of a
	// quiet notice. A warning's and an error's own words are plain.
	NoteLabel, WarnLabel, ErrorLabel, NoteText text.Style
	// Busy is the style of BusyMark, and Activity of the words after it.
	Busy, Activity text.Style
	// Status is the status line. Mode is the permission mode on it when
	// it is not the default, and Bypass the mode in which nothing asks.
	Status, Mode, Bypass text.Style
	// ModelName is the model's name on the status line.
	ModelName text.Style
	// Full is the context figure when the window is nearly full.
	Full text.Style
	// Hint is the short line under the status line that a key press
	// calls for.
	Hint text.Style
	// Ask is the style of AskMark, and Answer of the answer given.
	Ask, Answer text.Style
	// DeliverFile is the style of DeliverMark for a file, and DeliverLink
	// for an address: the mark is the one touch of colour on a
	// deliverable's line. DeliverLabel is its label, in one plain colour.
	DeliverFile, DeliverLink, DeliverLabel text.Style
	// Link is an address a person can open: underlined, as people expect
	// a link to be.
	Link text.Style

	// Editor is the look of the prompt box, Menu of the questions,
	// Markdown of the model's answers and Diff of changes to files.
	Editor   editor.Theme
	Menu     menu.Theme
	Markdown markdown.Theme
	Diff     diff.Theme
}

// DefaultTheme returns gnosh's own look for the colour profile p.
//
// It is crisp and sparing: prose, commands and output are the terminal's
// own colour; purple marks what gnosh draws itself; the prompt mark and the
// pointer are the site's blue-green; hot pink is the one accent, on
// whatever is chosen; grey is for what is not the text itself. Nothing is
// filled in, blurred or dimmed into a background. Two things have more
// than one colour: the wordmark, once, at the top of a session, and the
// line that says what gnosh is doing while it works, which starts grey
// and pale and takes its colours as the model thinks on.
func DefaultTheme(p text.Profile) Theme {
	return NewTheme(p, DefaultPalette(p))
}

// NewTheme makes the default look in the colours of pal, for the profile
// p. It is how a different purple and pink are tried: every style of every
// part is made from the one palette.
func NewTheme(p text.Profile, pal Palette) Theme {
	p = min(p, text.TrueColor)
	plain := text.Style{}.In(p)
	quiet := plain.Fg(pal.Grey)
	if pal.Grey == text.Default {
		quiet = plain.Dim()
	}
	th := Theme{
		Profile: p,
		Palette: pal,

		Name:          "GNOSH",
		WordmarkStops: []text.Color{pal.Mint, pal.Purple, pal.Pink, pal.Sun},
		WordmarkPlain: []text.Style{
			plain.Fg(pal.Green).Bold(), plain.Fg(pal.Green).Bold(), plain.Fg(pal.Green).Bold(),
			plain.Fg(pal.Purple).Bold(), plain.Fg(pal.Pink).Bold(),
		},
		Wordmark: plain.Bold(),

		PromptMark: "> ",
		ShellMark:  "$ ",
		ToolMark:   "▸",
		FailMark:   "✗",
		SubMark:    "↳",
		BusyMark:   "*",
		ThinkMark:  "~",
		AskMark:    "?",
		TaskDone:   "[x]",
		TaskNow:    "[>]",
		TaskTodo:   "[ ]",
		Sep:        " · ",

		DeliverMark: "✦",
		PendingMark: "✧",

		Prompt:     plain.Fg(pal.Aqua).Bold(),
		PromptText: plain.Fg(pal.Prompt),
		Tool:       plain.Fg(pal.Purple),
		ToolName:   plain.Bold(),
		ToolArg:    plain,
		Output:     plain,
		Code:       plain.Fg(pal.Lilac),
		Fail:       plain.Fg(pal.Rose).Bold(),
		Quiet:      quiet,
		Thinking:   quiet.Italic(),
		NoteLabel:  quiet,
		WarnLabel:  plain.Fg(pal.Gold).Bold(),
		ErrorLabel: plain.Fg(pal.Rose).Bold(),
		NoteText:   quiet,
		Busy:       plain.Fg(pal.Aqua).Bold(),
		Activity:   plain,
		Status:     quiet,
		Mode:       plain.Fg(pal.Purple).Bold(),
		Bypass:     plain.Fg(pal.Rose).Bold().Reverse(),
		ModelName:  plain,
		Full:       plain.Fg(pal.Gold).Bold(),
		Hint:       quiet,
		Ask:        plain.Fg(pal.Purple).Bold(),
		Answer:     plain.Bold(),

		DeliverFile:  plain.Fg(pal.Green),
		DeliverLink:  plain.Fg(pal.Gold),
		DeliverLabel: plain.Bold(),
		Link:         plain.Fg(pal.Purple).Underline(),
	}

	if p != text.NoColor {
		// The fall: a spark that swells and dwindles before the words,
		// the words in a grey lighter than the quiet one, and through
		// them drops of colour: pale while calm (the colour of the
		// person's own words, white on a dark background), and in a deep
		// thought the colours every fade of gnosh's runs through: the
		// site's blue-green, the purple, the hot pink and the gold. A
		// glint is toward the pale colour again.
		th.Fall = Fall{
			Marks: []string{"∙", "⋆", "✧", "✦", "✧", "⋆"},
			Every: 110 * time.Millisecond, EveryDeep: 60 * time.Millisecond,
			Plain: plain,
			Text:  text.Blend(pal.Grey, pal.Prompt, 0.4),
			Quiet: pal.Grey,
			Bland: pal.Prompt,
			Hues:  []text.Color{pal.Aqua, pal.Purple, pal.Pink, pal.Sun},
			Glint: pal.Prompt,
		}
	}

	th.Editor = editor.Theme{
		Prompt:        "> ",
		ShellPrompt:   "! ",
		CommandPrompt: " /",
		MoreAbove:     "↑",
		MoreBelow:     "↓",
		MoreBoth:      "↕",
		Pointer:       ">",

		PromptStyle:  plain.Fg(pal.Aqua).Bold(),
		ShellStyle:   plain.Fg(pal.Gold).Bold(),
		CommandStyle: plain.Fg(pal.Green).Bold(),
		Text:         plain.Fg(pal.Prompt),
		Placeholder:  quiet,
		Paste:        plain.Fg(pal.Purple),
		Control:      plain.Fg(pal.Gold),
		More:         plain.Fg(pal.Purple),
		Locked:       quiet,
		Candidate:    plain,
		Selected:     plain.Fg(pal.Pink).Bold(),
		Detail:       quiet,
		SearchLabel:  plain.Fg(pal.Purple),
		Match:        plain.Fg(pal.Pink).Underline(),
		Note:         quiet,
	}

	field := th.Editor
	field.Prompt, field.PromptStyle = "| ", plain.Fg(pal.Aqua)
	th.Menu = menu.Theme{
		Pointer:   ">",
		MoreAbove: "↑",
		MoreBelow: "↓",
		Checked:   "[x]",
		Unchecked: "[ ]",
		Picked:    "(*)",
		Unpicked:  "( )",
		Answered:  "✓",
		Mask:      "*",

		Title:        plain.Bold(),
		Label:        plain,
		Selected:     plain.Fg(pal.Pink).Bold(),
		PointerStyle: plain.Fg(pal.Aqua).Bold(),
		Description:  quiet,
		Hint:         quiet,
		More:         plain.Fg(pal.Purple),
		Tab:          quiet,
		TabCurrent:   plain.Fg(pal.Pink).Bold(),
		Missing:      plain.Fg(pal.Gold),
		Field:        field,
	}

	md := markdown.DefaultTheme()
	md.Text = plain.Fg(pal.Reply)
	md.Heading = plain.Fg(pal.Pink).Bold()
	md.HeadingMark = plain.Fg(pal.Purple)
	md.Code = plain.Fg(pal.Lilac)
	md.CodeBlock = plain.Fg(pal.Lilac)
	md.CodeMark = plain.Fg(pal.Purple)
	md.Link = plain.Fg(pal.Purple).Underline()
	md.LinkAddress = plain.Fg(pal.Lilac)
	md.HTML = plain.Fg(pal.Lilac)
	md.QuoteMark = plain.Fg(pal.Purple)
	md.ListMark = plain.Fg(pal.Purple)
	md.TaskOpen = plain.Fg(pal.Purple)
	md.TaskDone = plain.Fg(pal.Pink).Bold()
	md.Rule = plain.Fg(pal.Lilac)
	md.TableRule = plain.Fg(pal.Lilac)
	md.Definition = plain.Fg(pal.Lilac)
	th.Markdown = md

	th.Diff = diff.Theme{
		Path:         plain.Bold(),
		Note:         quiet,
		Number:       quiet,
		Deleted:      plain.Fg(pal.Rose),
		Inserted:     plain.Fg(pal.Green),
		DeletedSpan:  plain.Fg(pal.Rose).Underline(),
		InsertedSpan: plain.Fg(pal.Green).Underline(),
		Symbol:       quiet,
	}
	return th
}

// wordmark draws the name: through the fade with full colour, in the
// plain styles with fewer colours, and as the letters alone with none.
func (th Theme) wordmark() string {
	switch {
	case th.Profile == text.NoColor || th.Name == "":
		return th.Name
	case th.Profile == text.TrueColor && len(th.WordmarkStops) > 0:
		return text.GradientOptions{Style: th.Wordmark}.Render(th.Name, th.WordmarkStops...)
	case len(th.WordmarkPlain) == 0:
		return th.Wordmark.Render(th.Name)
	}
	// One plain colour for each letter, and never a rounded fade: with
	// 256 colours or sixteen a fade comes out as two or three bands.
	out, i := "", 0
	for g := range text.Graphemes(th.Name) {
		out += th.WordmarkPlain[min(i, len(th.WordmarkPlain)-1)].Render(g)
		i++
	}
	return out
}

// promptColors are the colours a session's prompt mark may take (/color,
// app.PromptColors), by Claude Code's names: with full colour, middle
// tones that read on a dark and on a light background; with sixteen, the
// terminal's own.
var promptColors = map[string][2]text.Color{
	"red":    {text.RGB(0xE0, 0x4A, 0x4A), text.Red},
	"blue":   {text.RGB(0x4A, 0x8A, 0xF0), text.Blue},
	"green":  {text.RGB(0x2E, 0xB0, 0x60), text.Green},
	"yellow": {text.RGB(0xD9, 0xB0, 0x20), text.Yellow},
	"purple": {text.RGB(0x9D, 0x5C, 0xFF), text.Magenta},
	"orange": {text.RGB(0xF0, 0x84, 0x20), text.BrightRed},
	"pink":   {text.RGB(0xF0, 0x25, 0x7F), text.BrightMagenta},
	"cyan":   {text.RGB(0x20, 0xB8, 0xD0), text.Cyan},
}

// tintPrompt gives the prompt mark, in the box and before each prompt
// sent from now on, the colour of that name; "" or a name it does not know
// is the theme's own blue-green.
func (th *Theme) tintPrompt(name string) {
	c := th.Palette.Aqua
	if pc, ok := promptColors[name]; ok {
		c = pc[0]
		if th.Profile == text.ANSI16 {
			c = pc[1]
		}
	}
	style := text.Style{}.In(th.Profile).Fg(c).Bold()
	th.Prompt = style
	th.Editor.PromptStyle = style
}
