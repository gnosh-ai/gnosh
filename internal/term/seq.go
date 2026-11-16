package term

import (
	"encoding/base64"
	"strconv"
	"strings"
)

// This file holds every control sequence gnosh writes to a terminal, apart
// from the colour and style codes that internal/text produces, and the few
// of the side panel (panel.go), which takes the alternate screen and the
// mouse only when a person turns it on.
//
// The sequences come from ECMA-48 (cursor movement, erasing, SGR), the
// VT100 and VT220 manuals (DEC private modes), xterm's control sequences
// document (bracketed paste, focus reporting, OSC 2 and OSC 52), the kitty
// keyboard protocol, and the synchronized output specification (mode 2026).

const (
	// HideCursor makes the cursor invisible (DECTCEM reset).
	HideCursor = "\x1b[?25l"
	// ShowCursor makes the cursor visible again (DECTCEM set).
	ShowCursor = "\x1b[?25h"

	// EraseLine blanks the whole line the cursor is on (EL 2). The cursor
	// does not move.
	EraseLine = "\x1b[2K"
	// EraseToLineEnd blanks from the cursor to the end of its line (EL 0).
	EraseToLineEnd = "\x1b[K"
	// EraseDown blanks from the cursor to the end of the screen (ED 0).
	//
	// Never send it with the cursor in the top left corner of the
	// screen: tmux takes that to mean "clear the screen" and, unless its
	// scroll-on-clear option is off, saves the screen in its history
	// instead of erasing it. The renderer does not know which row of the
	// screen the cursor is on, so it does not use this at all; it erases
	// row by row with EraseLine.
	EraseDown = "\x1b[J"

	// SaveCursor remembers where the cursor is (DECSC) and RestoreCursor
	// puts it back (DECRC). The renderer uses the pair to come back from
	// erasing the rows below the cursor, where it cannot count rows
	// because "cursor down" stops at the bottom of the screen.
	SaveCursor    = "\x1b7"
	RestoreCursor = "\x1b8"

	// StyleReset turns off every colour and style (SGR 0).
	StyleReset = "\x1b[m"

	// AutoWrapOff stops text that reaches the right edge from continuing
	// on the next line (DECAWM reset); AutoWrapOn is the normal state. The
	// renderer turns wrapping off only for the length of one update, so a
	// line that is wider than gnosh believed is cut off by the terminal
	// instead of pushing everything below it down a row.
	AutoWrapOff = "\x1b[?7l"
	AutoWrapOn  = "\x1b[?7h"

	// BracketedPasteOn asks the terminal to mark pasted text (mode 2004),
	// so that a paste arrives as one Paste event and not as typing.
	BracketedPasteOn  = "\x1b[?2004h"
	BracketedPasteOff = "\x1b[?2004l"

	// FocusReportingOn asks the terminal to say when its window gains or
	// loses the keyboard (mode 1004).
	FocusReportingOn  = "\x1b[?1004h"
	FocusReportingOff = "\x1b[?1004l"

	// KittyKeyboardPush asks for the first level of the kitty keyboard
	// protocol, "disambiguate escape codes", and nothing more: Esc and
	// keys held with Ctrl, Alt or Shift then arrive in an unmistakable
	// form, so Shift+Enter can be told from Enter. It pushes onto the
	// terminal's stack; KittyKeyboardPop takes it off again. Send the
	// pair only to a terminal that answered QueryKittyKeyboard.
	KittyKeyboardPush = "\x1b[>1u"
	KittyKeyboardPop  = "\x1b[<u"
	// QueryKittyKeyboard asks which kitty keyboard flags are on. A
	// terminal that knows the protocol answers with ReplyKittyKeyboard.
	QueryKittyKeyboard = "\x1b[?u"

	// SyncBegin asks the terminal to hold its display still until SyncEnd
	// (mode 2026), so that an update appears all at once.
	SyncBegin = "\x1b[?2026h"
	SyncEnd   = "\x1b[?2026l"
	// QuerySynchronizedOutput asks whether the terminal knows mode 2026
	// (DECRQM). The answer is a ReplyMode.
	QuerySynchronizedOutput = "\x1b[?2026$p"

	// QueryCursorPosition asks where the cursor is (DSR 6). The answer is
	// a ReplyCursorPosition; tell the decoder to expect it (see
	// Decoder.ExpectCursorPosition and Terminal.QueryCursorPosition).
	QueryCursorPosition = "\x1b[6n"
	// QueryBackground asks the terminal for its background colour (OSC
	// 11). A terminal that tells answers with a ReplyString "11;rgb:...".
	QueryBackground = "\x1b]11;?\x1b\\"

	// QueryDeviceAttributes asks the terminal what it is (DA1). Every
	// terminal answers it, which makes it the way to tell "no" from
	// "not yet" for the questions sent before it.
	QueryDeviceAttributes = "\x1b[c"

	// Bell rings the terminal's bell.
	Bell = "\a"
)

// CursorUp moves the cursor up n lines, stopping at the top of the screen
// (CUU). It is empty when n is not positive.
func CursorUp(n int) string {
	if n <= 0 {
		return ""
	}
	return "\x1b[" + strconv.Itoa(n) + "A"
}

// CursorDown moves the cursor down n lines, stopping at the bottom of the
// screen without scrolling (CUD). It is empty when n is not positive.
func CursorDown(n int) string {
	if n <= 0 {
		return ""
	}
	return "\x1b[" + strconv.Itoa(n) + "B"
}

// CursorColumn moves the cursor to column col of its line, counted from 1
// (CHA).
func CursorColumn(col int) string {
	if col <= 1 {
		return "\x1b[G"
	}
	return "\x1b[" + strconv.Itoa(col) + "G"
}

// DesktopNote raises a desktop notification through the terminal, in the
// way kind names: "osc9" (iTerm2, WezTerm, Ghostty) or "osc99" (kitty).
// Control characters in text are taken out. Any other kind gives "".
func DesktopNote(kind, text string) string {
	text = stripControls(text)
	switch kind {
	case "osc9":
		return "\x1b]9;" + text + "\x1b\\"
	case "osc99":
		return "\x1b]99;;" + text + "\x1b\\"
	}
	return ""
}

// TmuxWrap wraps a sequence for tmux to pass on to the terminal it is
// shown in (tmux's passthrough, which it takes with allow-passthrough on):
// every ESC in it doubled, inside DCS tmux; … ST.
func TmuxWrap(seq string) string {
	return "\x1bPtmux;" + strings.ReplaceAll(seq, "\x1b", "\x1b\x1b") + "\x1b\\"
}

// TitlePush and TitlePop keep the terminal's title on its own stack and
// put it back (XTWINOPS 22 and 23): a program that sets the title pushes
// the old one first and pops it as it leaves. Terminals without the stack
// ignore both.
const (
	TitlePush = "\x1b[22;0t"
	TitlePop  = "\x1b[23;0t"
)

// SetTitle sets the terminal window's title (OSC 2). Control characters in
// the title are left out, so a title cannot end the sequence early and
// smuggle in another.
func SetTitle(title string) string {
	return "\x1b]2;" + stripControls(title) + "\x1b\\"
}

// ClipboardWrite puts text on the system clipboard through the terminal
// (OSC 52). gnosh sends it only when the person has turned it on, since it
// lets whatever is running in the terminal overwrite their clipboard; see
// Options.Clipboard. Apple's Terminal ignores it, and tmux passes it on
// only when its set-clipboard option allows.
func ClipboardWrite(text string) string {
	return "\x1b]52;c;" + base64.StdEncoding.EncodeToString([]byte(text)) + "\x1b\\"
}

// stripControls removes the C0 and C1 control characters and DEL from s.
// Bytes that are not UTF-8 go too: a terminal set to an 8-bit character
// set would read some of them as controls.
func stripControls(s string) string {
	s = strings.ToValidUTF8(s, "")
	control := func(r rune) bool { return r < 0x20 || r == 0x7f || isC1(r) }
	if strings.IndexFunc(s, control) < 0 {
		return s
	}
	return strings.Map(func(r rune) rune {
		if control(r) {
			return -1
		}
		return r
	}, s)
}
