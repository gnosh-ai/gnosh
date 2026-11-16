package term

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/gnosh-ai/gnosh/internal/term/vtest"
)

// answering is an emulated terminal wired up the way a real one is: what
// is written to it reaches the screen, and the screen's answers come back
// through a decoder as events.
type answering struct {
	scr    *vtest.Screen
	dec    Decoder
	events chan Event
}

func newAnswering(a vtest.Answers) *answering {
	scr := vtest.New(40, 10)
	scr.SetAnswers(a)
	return &answering{scr: scr, events: make(chan Event, 64)}
}

func (a *answering) Write(p []byte) (int, error) {
	n, err := a.scr.Write(p)
	for _, ev := range a.dec.Feed(a.scr.TakeReplies()) {
		a.events <- ev
	}
	return n, err
}

func TestProbe(t *testing.T) {
	cases := []struct {
		name    string
		answers vtest.Answers
		want    Capabilities
	}{
		{"a terminal that knows everything", vtest.AllAnswers,
			Capabilities{KittyKeyboard: true, SynchronizedOutput: true, Answered: true}},
		{"kitty keyboard only", vtest.Answers{DeviceAttributes: true, Modes: true, Kitty: true},
			Capabilities{KittyKeyboard: true, Answered: true}},
		{"synchronized output only", vtest.Answers{DeviceAttributes: true, Modes: true, SynchronizedOutput: true},
			Capabilities{SynchronizedOutput: true, Answered: true}},
		{"neither, and no mode reports (Apple's Terminal, old tmux)", vtest.Answers{DeviceAttributes: true, CursorPosition: true},
			Capabilities{Answered: true}},
		{"mode reports, but the mode is unknown", vtest.Answers{DeviceAttributes: true, Modes: true},
			Capabilities{Answered: true}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			term := newAnswering(c.answers)
			start := time.Now()
			caps, others, err := Probe(context.Background(), term, term.events, 5*time.Second)
			if err != nil {
				t.Fatal(err)
			}
			if caps != c.want {
				t.Errorf("capabilities %+v, want %+v", caps, c.want)
			}
			if len(others) != 0 {
				t.Errorf("events left over: %s", describe(others))
			}
			if time.Since(start) > time.Second {
				t.Errorf("the probe waited %v although the terminal answered at once", time.Since(start))
			}
			if u := term.scr.Unhandled(); len(u) > 0 {
				t.Errorf("the probe wrote sequences the terminal does not know: %q", u)
			}
			if row, col := term.scr.Cursor(); row != 0 || col != 0 || len(term.scr.AllLines()) != 0 {
				t.Errorf("the probe moved the cursor or printed something:\n%s", term.scr.Dump())
			}
		})
	}
}

// A terminal that answers nothing gets the safe defaults after the wait.
func TestProbeNoAnswer(t *testing.T) {
	term := newAnswering(vtest.Answers{})
	start := time.Now()
	caps, others, err := Probe(context.Background(), term, term.events, 20*time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	if caps != (Capabilities{}) || len(others) != 0 {
		t.Errorf("capabilities %+v, others %s; want nothing", caps, describe(others))
	}
	if waited := time.Since(start); waited < 20*time.Millisecond || waited > 2*time.Second {
		t.Errorf("waited %v, want about 20ms", waited)
	}
}

// Keys typed while the probe waits are kept, in order.
func TestProbeKeepsTyping(t *testing.T) {
	term := newAnswering(vtest.AllAnswers)
	term.events <- txt('l')
	term.events <- txt('s')
	caps, others, err := Probe(context.Background(), term, term.events, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if !caps.Answered || !caps.KittyKeyboard {
		t.Errorf("capabilities %+v", caps)
	}
	if want := []Event{txt('l'), txt('s')}; !reflect.DeepEqual(others, want) {
		t.Errorf("typing came back as %s, want %s", describe(others), describe(want))
	}
}

func TestProbeStops(t *testing.T) {
	// The context ends the wait.
	term := newAnswering(vtest.Answers{})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, _, err := Probe(ctx, term, term.events, time.Minute); !errors.Is(err, context.Canceled) {
		t.Errorf("error %v, want the context's", err)
	}
	// So does the end of the events.
	term = newAnswering(vtest.Answers{})
	close(term.events)
	if caps, _, err := Probe(context.Background(), term, term.events, time.Minute); err != nil || caps.Answered {
		t.Errorf("capabilities %+v, error %v", caps, err)
	}
	// A failed write is reported.
	if _, _, err := Probe(context.Background(), failingWriter{}, nil, time.Minute); err == nil {
		t.Error("no error when the questions could not be written")
	}
}

type failingWriter struct{}

func (failingWriter) Write([]byte) (int, error) { return 0, errors.New("broken pipe") }

// Answers that arrive after the wait still count when handed to Note.
func TestCapabilitiesNote(t *testing.T) {
	var caps Capabilities
	for _, ev := range decodeAll("\x1b[?1u\x1b[?2026;2$y\x1b[?62c") {
		if !caps.Note(ev) {
			t.Errorf("Note did not take %s", ev)
		}
	}
	if want := (Capabilities{KittyKeyboard: true, SynchronizedOutput: true, Answered: true}); caps != want {
		t.Errorf("capabilities %+v, want %+v", caps, want)
	}
	caps = Capabilities{}
	for _, in := range []string{"\x1b[?2026;0$y", "\x1b[?2026;4$y", "\x1b[?2004;1$y", "\x1b[2026;1$y", "a", "\x1b[5;5R"} {
		for _, ev := range decodeAll(in) {
			caps.Note(ev)
		}
	}
	if caps != (Capabilities{}) {
		t.Errorf("capabilities %+v from answers that grant nothing", caps)
	}
}

// The sequences gnosh writes are understood by the emulated terminal and
// do what their names say.
func TestSequences(t *testing.T) {
	scr := vtest.New(20, 5)
	scr.WriteString("one\r\ntwo\r\nthree" + CursorUp(2) + CursorColumn(2) + "X" + CursorDown(1) + CursorColumn(1) + "Y")
	if got, want := scr.Lines(), []string{"oXe", "Ywo", "three", "", ""}; !reflect.DeepEqual(got, want) {
		t.Errorf("cursor movement: screen %q, want %q", got, want)
	}
	scr.WriteString(CursorColumn(2) + EraseToLineEnd + CursorUp(1) + EraseLine + CursorDown(1) + CursorDown(0) + CursorUp(-3) + "\r\n" + CursorColumn(3) + EraseDown)
	if got, want := scr.Lines(), []string{"", "Y", "th", "", ""}; !reflect.DeepEqual(got, want) {
		t.Errorf("erasing: screen %q, want %q", got, want)
	}
	scr.WriteString(HideCursor)
	if scr.CursorVisible() {
		t.Error("HideCursor did not hide the cursor")
	}
	scr.WriteString(ShowCursor + BracketedPasteOn + FocusReportingOn + SyncBegin + AutoWrapOff)
	if !scr.CursorVisible() || !scr.Mode(2004) || !scr.Mode(1004) || !scr.Mode(2026) || scr.Mode(7) {
		t.Error("ShowCursor, BracketedPasteOn, FocusReportingOn, SyncBegin or AutoWrapOff did not set its mode")
	}
	scr.WriteString(BracketedPasteOff + FocusReportingOff + SyncEnd + AutoWrapOn)
	if scr.Mode(2004) || scr.Mode(1004) || scr.Mode(2026) || !scr.Mode(7) {
		t.Error("a mode was not turned off again")
	}
	scr.WriteString(KittyKeyboardPush)
	if flags, depth := scr.KittyFlags(); flags != 1 || depth != 1 {
		t.Errorf("after KittyKeyboardPush: flags %d, depth %d; want only \"disambiguate\" (1), depth 1", flags, depth)
	}
	scr.WriteString(KittyKeyboardPop)
	if flags, depth := scr.KittyFlags(); flags != 0 || depth != 0 {
		t.Errorf("after KittyKeyboardPop: flags %d, depth %d", flags, depth)
	}
	scr.WriteString("\x1b[1;31mx" + StyleReset + "y" + SaveCursor + CursorUp(2) + RestoreCursor + "z" + Bell)
	if a := scr.Cell(2, 3).Attr; a != (vtest.Attr{}) || !scr.Cell(2, 2).Attr.Bold {
		t.Errorf("StyleReset left %+v", a)
	}
	if scr.Line(2) != "thxyz" || scr.Bells() != 1 {
		t.Errorf("SaveCursor and RestoreCursor or Bell: line %q, bells %d", scr.Line(2), scr.Bells())
	}

	scr.WriteString(SetTitle("gnosh: \x1b]0;evil\x07 \u009b title\n"))
	if got := scr.Title(); got != "gnosh: ]0;evil  title" {
		t.Errorf("title %q: control characters were not removed", got)
	}
	scr.WriteString(ClipboardWrite("copied text"))
	if got := scr.Clipboard(); len(got) != 1 || got[0] != "copied text" {
		t.Errorf("clipboard %q", got)
	}

	scr.TakeReplies()
	scr.WriteString(QueryCursorPosition + QueryDeviceAttributes + QueryKittyKeyboard + QuerySynchronizedOutput)
	var d Decoder
	d.ExpectCursorPosition()
	got := d.Feed(scr.TakeReplies())
	want := []Event{
		Reply{Kind: ReplyCursorPosition, Row: 3, Col: 6},
		Reply{Kind: ReplyDeviceAttributes, Params: []int{62, 22}},
		Reply{Kind: ReplyKittyKeyboard, Flags: 0},
		Reply{Kind: ReplyMode, Mode: 2026, Private: true, State: ModeReset},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("answers %s, want %s", describe(got), describe(want))
	}
	if u := scr.Unhandled(); len(u) > 0 {
		t.Errorf("sequences the terminal does not know: %q", u)
	}
	if scr.AltScreenEver() || scr.MouseEver() {
		t.Error("the alternate screen or mouse reporting was turned on")
	}
}

// A desktop notification is OSC 9 or OSC 99, with the text's control
// characters taken out, and nothing for a terminal that takes neither.
func TestDesktopNote(t *testing.T) {
	for _, tc := range []struct{ kind, text, want string }{
		{"osc9", "gnosh waits\x07\x1b]0;evil", "\x1b]9;gnosh waits]0;evil\x1b\\"},
		{"osc99", "done", "\x1b]99;;done\x1b\\"},
		{"", "done", ""},
		{"bell", "done", ""},
	} {
		if got := DesktopNote(tc.kind, tc.text); got != tc.want {
			t.Errorf("DesktopNote(%q, %q) = %q, want %q", tc.kind, tc.text, got, tc.want)
		}
	}
}

// A terminal that tells its background colour (OSC 11) says whether it is
// light; one that does not, or says it in a form gnosh cannot read, leaves
// it unknown.
func TestProbeBackground(t *testing.T) {
	for spec, want := range map[string][2]bool{
		"rgb:ffff/ffff/ffff": {true, true},
		"rgb:f0/f0/f0":       {true, true},
		"rgb:1e1e/1e1e/1e1e": {true, false},
		"rgb:0/0/0":          {true, false},
		"rgb:ffff/ffff":      {false, false},
		"rgb:zz/00/00":       {false, false},
		"#ffffff":            {false, false},
	} {
		var c Capabilities
		if !c.Note(Reply{Kind: ReplyString, Intro: ']', Text: "11;" + spec}) {
			t.Errorf("%s: the answer was not taken as one", spec)
		}
		if got := [2]bool{c.BackgroundKnown, c.LightBackground}; got != want {
			t.Errorf("%s: known, light = %v, want %v", spec, got, want)
		}
	}
	if !strings.Contains(probeQuery, QueryBackground) || !strings.HasSuffix(probeQuery, QueryDeviceAttributes) {
		t.Errorf("the probe asks %q", probeQuery)
	}
}
