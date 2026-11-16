package ui

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/gnosh-ai/gnosh/internal/term"
	"github.com/gnosh-ai/gnosh/internal/text"
)

// Keystrokes are read as Claude Code writes them, in any case, and come
// out as the keys the terminal reports.
func TestParseStroke(t *testing.T) {
	for in, want := range map[string]term.Key{
		"ctrl+e":       ctrlKey('e'),
		"CTRL+E":       ctrlKey('e'),
		"control+k":    ctrlKey('k'),
		"meta+p":       altKey('p'),
		"opt+t":        altKey('t'),
		"shift+tab":    namedKey(term.KeyTab, term.ModShift),
		"escape":       namedKey(term.KeyEscape, 0),
		"esc":          namedKey(term.KeyEscape, 0),
		"return":       namedKey(term.KeyEnter, 0),
		"ctrl+enter":   namedKey(term.KeyEnter, term.ModCtrl),
		"pageup":       namedKey(term.KeyPageUp, 0),
		"f5":           namedKey(term.KeyF1+4, 0),
		"space":        {Code: term.KeyRune, Rune: ' '},
		"x":            {Code: term.KeyRune, Rune: 'x'},
		"K":            {Code: term.KeyRune, Rune: 'k'},
		"shift+k":      {Code: term.KeyRune, Rune: 'K'},
		"ctrl+shift+c": {Code: term.KeyRune, Rune: 'C', Mod: term.ModCtrl},
		"ctrl+_":       ctrlKey('_'),
		"cmd+k":        {Code: term.KeyRune, Rune: 'k', Mod: term.ModSuper},
	} {
		got, err := parseStroke(in)
		if err != nil || got != want {
			t.Errorf("parseStroke(%q) = %v, %v; want %v", in, got, err, want)
		}
	}
	for _, bad := range []string{"ctl+k", "ctrl+", "", "f99", "hyperdrive", "ctrl+ k"} {
		if k, err := parseStroke(bad); err == nil {
			t.Errorf("parseStroke(%q) = %v, want an error", bad, k)
		}
	}
	// A key pressed is looked up as a binding names it.
	if got := canonKey(term.Key{Code: term.KeyRune, Rune: 'k', Mod: term.ModShift | term.ModCapsLock, Text: "K"}); got != (term.Key{Code: term.KeyRune, Rune: 'K'}) {
		t.Errorf("Shift+k is looked up as %v", got)
	}
}

// What in the file cannot be used is said and left out; the rest is in
// force.
func TestParseKeyBindings(t *testing.T) {
	b, problems := parseKeyBindings([]byte(`{
		"bindings": [
			{"context": "Chat", "bindings": {"ctrl+e": "chat:externalEditor", "ctrl+s": null, "ctrl+y": "command:compact",
				"ctrl+q": "chat:nope", "ctrl+c": "chat:stash", "ctl+k": "chat:stash", "ctrl+k ctrl+s": "chat:stash", "a b c": "chat:stash", "ctrl+w": 3}},
			{"context": "Task", "bindings": {"ctrl+f": "task:background"}},
			{"context": "Settings", "bindings": {"x": "select:accept"}}
		]}`))
	chat := b.byCtx["Chat"]
	if chat[ctrlKey('e')] != "chat:externalEditor" || chat[ctrlKey('y')] != "command:compact" || chat[ctrlKey('f')] != "task:background" {
		t.Errorf("the bindings in force: %v", chat)
	}
	if action, ok := chat[ctrlKey('s')]; !ok || action != "" {
		t.Errorf("ctrl+s is not taken away: %q %v", action, ok)
	}
	if len(chat) != 4 {
		t.Errorf("%d bindings in force in Chat, want 4: %v", len(chat), chat)
	}
	if b.chords["Chat"][[2]term.Key{ctrlKey('k'), ctrlKey('s')}] != "chat:stash" {
		t.Errorf("the chords in force: %v", b.chords["Chat"])
	}
	all := strings.Join(problems, "\n")
	for _, want := range []string{`"chat:nope" is not an action gnosh carries out`, "ctrl+c cannot be bound", `"ctl" is no modifier`, "a binding is one key, or a chord of two", "ctrl+w is neither an action's name nor null", `the context "Settings" is not one gnosh takes keys for`} {
		if !strings.Contains(all, want) {
			t.Errorf("the problems do not say %q:\n%s", want, all)
		}
	}
	if _, problems := parseKeyBindings([]byte("{not json")); len(problems) != 1 || !strings.Contains(problems[0], "no key is changed") {
		t.Errorf("a file that is not JSON: %q", problems)
	}
}

// In the box a bound key does its action, null takes a key away, and
// command: runs a command; the file is read again when it changes, and a
// problem in it is said.
func TestKeyBindingsInTheBox(t *testing.T) {
	path := filepath.Join(t.TempDir(), "keybindings.json")
	write := func(text string) {
		t.Helper()
		if err := os.WriteFile(path, []byte(text), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write(`{"bindings": [{"context": "Chat", "bindings": {"ctrl+k": "chat:stash", "ctrl+s": null, "ctrl+y": "command:compact"}}]}`)
	g := newRig(t, 100, 24, text.NoColor, func(c *controls) {
		c.info.Keybindings = path
		c.info.Commands = append(c.info.Commands, "compact")
	})
	g.keys(`"abc" <Ctrl+K>`)
	if got := g.box(); got != "" {
		t.Fatalf("Ctrl+K bound to chat:stash left %q in the box", got)
	}
	g.keys(`<Ctrl+S>`)
	if got := g.box(); got != "" {
		t.Fatalf("Ctrl+S, taken away, put %q in the box", got)
	}
	g.keys(`<Ctrl+K>`)
	if got := g.box(); got != "abc" {
		t.Fatalf("Ctrl+K again brought back %q, want abc", got)
	}
	g.keys(`<Ctrl+U> <Ctrl+Y>`)
	if !slices.Contains(g.c.commands, "/compact") {
		t.Fatalf("Ctrl+Y bound to command:compact ran %q", g.c.commands)
	}
	// Changed, the file is read again: Ctrl+K takes back nothing, and a
	// context gnosh has no keys for is said.
	write(`{"bindings": [{"context": "Chat", "bindings": {"ctrl+k": null}}, {"context": "Nope", "bindings": {}}]}`)
	g.wait(2 * time.Second).keys(`"xyz" <Ctrl+A> <Ctrl+K>`)
	if got := g.box(); got != "xyz" {
		t.Fatalf("Ctrl+K taken away still cut, leaving %q", got)
	}
	g.committedHas(`the context "Nope" is not one gnosh takes keys for`)
	// With the file gone every key is its own again.
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	g.wait(2 * time.Second).keys(`<Ctrl+A> <Ctrl+K>`)
	if got := g.box(); got != "" {
		t.Fatalf("without the file Ctrl+K left %q", got)
	}
}

// A chord of the person's waits for its second key: the pair does the
// action, another key lets the first one through, and a chord left
// unfinished does nothing.
func TestKeyBindingChords(t *testing.T) {
	path := filepath.Join(t.TempDir(), "keybindings.json")
	if err := os.WriteFile(path, []byte(`{"bindings": [{"context": "Chat", "bindings": {"ctrl+k ctrl+s": "command:compact"}}]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	g := newRig(t, 100, 24, text.NoColor, func(c *controls) {
		c.info.Keybindings = path
		c.info.Commands = append(c.info.Commands, "compact")
	})
	g.keys(`<Ctrl+K> <Ctrl+S>`)
	if !slices.Contains(g.c.commands, "/compact") {
		t.Fatalf("Ctrl+K Ctrl+S ran %q", g.c.commands)
	}
	g.keys(`"abc" <Ctrl+A> <Ctrl+K> "z"`)
	if got := g.box(); got != "z" {
		t.Fatalf("Ctrl+K then z left %q in the box, want z (Ctrl+K cut the line, then z was typed)", got)
	}
	g.keys(`<Ctrl+K>`).wait(4 * time.Second).keys(`<Ctrl+S>`)
	if got := g.box(); got != "" {
		t.Fatalf("Ctrl+S after a chord left unfinished did not stash: %q", got)
	}
	if n := strings.Count(strings.Join(g.c.commands, " "), "/compact"); n != 1 {
		t.Fatalf("/compact ran %d times", n)
	}
}

// Whatever the file holds, reading it never panics, and a key it binds is
// already in the form keys are looked up by.
func FuzzKeyBindings(f *testing.F) {
	for _, seed := range []string{
		`{"bindings": [{"context": "Chat", "bindings": {"ctrl+e": "chat:externalEditor", "ctrl+s": null}}]}`,
		`{"bindings": [{"context": "Global", "bindings": {"ctrl+k ctrl+s": "command:compact", "shift+K": "app:redraw"}}]}`,
		`{"bindings": [{"context": "Transcript", "bindings": {"q": "transcript:exit", "f12": null, "ctrl++": "chat:undo"}}]}`,
		`{"bindings": null}`, `[]`, `{`, ``,
	} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, data string) {
		b, _ := parseKeyBindings([]byte(data))
		for ctx, keys := range b.byCtx {
			for k := range keys {
				if canonKey(k) != k {
					t.Fatalf("%s: %v is kept as it is not looked up", ctx, k)
				}
			}
		}
		for _, stroke := range strings.Fields(data) {
			if k, err := parseStroke(stroke); err == nil && canonKey(k) != k {
				t.Fatalf("parseStroke(%q) = %v, which is not looked up as it is", stroke, k)
			}
		}
	})
}

// A problem in the file is said as the session starts, before any key.
func TestKeyBindingsProblemsAtStart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "keybindings.json")
	if err := os.WriteFile(path, []byte(`{"bindings": [{"context": "Chat", "bindings": {"ctl+k": "chat:stash"}}]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	g := newRig(t, 100, 24, text.NoColor, func(c *controls) { c.info.Keybindings = path })
	g.committedHas(`"ctl" is no modifier`)
}
