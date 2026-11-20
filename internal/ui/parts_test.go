package ui

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gnosh-ai/gnosh/internal/text"
)

// The small pieces every line is written with.
func TestWords(t *testing.T) {
	for d, want := range map[time.Duration]string{
		0: "0s", 40 * time.Millisecond: "0s", 300 * time.Millisecond: "0.3s", 999 * time.Millisecond: "1.0s",
		time.Second: "1s", 59 * time.Second: "59s", 65 * time.Second: "1m 05s",
		time.Hour + 2*time.Minute: "1h 02m", -time.Second: "0s",
	} {
		if got := span(d); got != want {
			t.Errorf("span(%v) = %q, want %q", d, got, want)
		}
	}
	for n, want := range map[int]string{0: "0", 812: "812", 1000: "1k", 1234: "1.2k", 9999: "10k", 14200: "14k", 262144: "262k", 1_200_000: "1.2M"} {
		if got := tokens(n); got != want {
			t.Errorf("tokens(%d) = %q, want %q", n, got, want)
		}
	}
	if got := tokenCount(1); got != "1 token" {
		t.Errorf("tokenCount(1) = %q", got)
	}
	for n, want := range map[int]string{0: "0", 999: "999", 1000: "1,000", 1234567: "1,234,567", -4321: "-4,321"} {
		if got := grouped(n); got != want {
			t.Errorf("grouped(%d) = %q, want %q", n, got, want)
		}
	}
	if got := count(1, "line", "lines") + ", " + count(2000, "line", "lines"); got != "1 line, 2,000 lines" {
		t.Errorf("count gave %q", got)
	}
	paths := []struct{ cwd, home, p, want string }{
		{"/home/jae/src/proj", "/home/jae", "/home/jae/src/proj/a/b.go", "a/b.go"},
		{"/home/jae/src/proj", "/home/jae", "/home/jae/src/proj", "."},
		{"/home/jae/src/proj", "/home/jae", "/home/jae/src/other/x.go", "~/src/other/x.go"},
		{"/home/jae/src/proj", "/home/jae", "/etc/hosts", "/etc/hosts"},
		{"/home/jae/src/proj", "/home/jae", "relative/x.go", "relative/x.go"},
		{"", "/home/jae", "/home/jae", "~"},
		{"/a", "", "/a/b\x1b[2J", "b^[[2J"},
	}
	for _, tc := range paths {
		if got := showPath(tc.cwd, tc.home, tc.p); got != tc.want {
			t.Errorf("showPath(%q, %q, %q) = %q, want %q", tc.cwd, tc.home, tc.p, got, tc.want)
		}
	}
}

// Outside text is made safe to draw: nothing in it is left for a terminal
// to act on, and nothing is hidden without a word.
func TestSafeLines(t *testing.T) {
	cases := []struct{ in, want string }{
		{"plain", "plain"},
		{"a\tb", "a       b"},
		{"1234567\tb", "1234567 b"},
		{"esc \x1b[2J here", "esc ^[[2J here"},
		{"bell \x07 nul \x00 del \x7f", "bell ^G nul ^@ del ^?"},
		{"trailing return\r", "trailing return"},
		{"wide \xe6\x97\xa5\tx", "wide \xe6\x97\xa5 x"},
	}
	for _, tc := range cases {
		if got := safeLine(tc.in); got != tc.want {
			t.Errorf("safeLine(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
	if got := safeLines("one\ntwo\r\n\nfour\n\n\n"); strings.Join(got, "|") != "one|two||four" {
		t.Errorf("safeLines gave %q", got)
	}
	if got := oneLine("  several\n\twords \x1b[H here  "); got != "several words ^[[H here" {
		t.Errorf("oneLine gave %q", got)
	}
	// A line of a command's output that was written over shows as its
	// last state, and says that it was.
	if got := screenLine("10%\r50%\r100% done\r"); got != "100% done (written over 2 earlier states of this line)" {
		t.Errorf("screenLine gave %q", got)
	}
	if got := screenLine("N\bNA\bAM\bME\bE — _\bd_\bi_\bf_\bf"); got != "NAME — diff" {
		t.Errorf("screenLine overstrike gave %q", got)
	}
	if got := screenLine("\x1b[31merror\x1b[0m and \x1b[2J"); got != "error and ^[[2J" {
		t.Errorf("screenLine SGR gave %q", got)
	}
	if got := screenLine("no return"); got != "no return" {
		t.Errorf("screenLine gave %q", got)
	}
	long := screenLine(strings.Repeat("\xe6\x97\xa5", 5000))
	if !strings.Contains(long, "a line of 15,000 bytes, cut here") || !strings.HasPrefix(long, "\xe6\x97\xa5") || len(long) > longLine+64 {
		t.Errorf("a long line came out as %d bytes ending %q", len(long), long[len(long)-40:])
	}
	if got := lastLines("a\n\n  \nb\nc\n\n", 2); strings.Join(got, "|") != "b|c" {
		t.Errorf("lastLines gave %q", got)
	}
	var tail tailBuf
	for i := 0; i < 10000; i++ {
		tail.add("line of output\n")
	}
	tail.add("the end\n")
	if len(tail.data) > tailKeep || tail.total != 10000*15+8 {
		t.Errorf("the tail keeps %d bytes of %d", len(tail.data), tail.total)
	}
	if got := tail.last(2); strings.Join(got, "|") != "line of output|the end" {
		t.Errorf("the tail's last lines are %q", got)
	}
}

// The files completion offers are the ones the project's ignore rules
// leave in, in name order, with directories marked; the branch is read
// from the repository's own file, and nothing is run.
func TestFilesAndBranch(t *testing.T) {
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	write := func(name, content string) {
		t.Helper()
		path := filepath.Join(root, name)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write(".git/HEAD", "ref: refs/heads/feature/x\n")
	write(".gitignore", "*.log\nbuild/\n")
	write("main.go", "package main\n")
	write("Makefile", "all:\n")
	write("debug.log", "noise\n")
	write("build/out.bin", "x")
	write("internal/ui/a.go", "package ui\n")
	write("internal/ui/skip.log", "noise\n")
	write(".env", "SECRET=1\n")
	if err := os.Symlink(filepath.Join(root, "internal"), filepath.Join(root, "linked")); err != nil {
		t.Fatal(err)
	}

	var names []string
	for _, e := range listDir(root) {
		name := e.name
		if e.dir {
			name += "/"
		}
		names = append(names, name)
	}
	if got, want := strings.Join(names, " "), ".env .gitignore internal/ linked/ main.go Makefile"; got != want {
		t.Errorf("the folder lists as %q, want %q", got, want)
	}
	if got := listDir(filepath.Join(root, "internal", "ui")); len(got) != 1 || got[0].name != "a.go" {
		t.Errorf("a folder below lists as %+v", got)
	}
	if got := listDir(filepath.Join(root, "nowhere")); got != nil {
		t.Errorf("a folder that is not there lists as %+v", got)
	}

	if got := gitBranch(filepath.Join(root, "internal", "ui")); got != "feature/x" {
		t.Errorf("the branch is %q", got)
	}
	write(".git/HEAD", "0123456789abcdef0123456789abcdef01234567\n")
	if got := gitBranch(root); got != "0123456" {
		t.Errorf("a detached head reads as %q", got)
	}
	// A worktree: its .git is a file that names the real directory.
	write("wt/.git", "gitdir: ../.git/worktrees/wt\n")
	write(".git/worktrees/wt/HEAD", "ref: refs/heads/side\n")
	if got := gitBranch(filepath.Join(root, "wt")); got != "side" {
		t.Errorf("a worktree's branch is %q", got)
	}
	write(".git/HEAD", "ref: refs/heads/evil\x1b[2J\n")
	if got := gitBranch(root); got != "evil^[[2J" {
		t.Errorf("a branch with an escape sequence in its name reads as %q", got)
	}
	if got := gitBranch(t.TempDir()); got != "" {
		t.Errorf("outside a repository the branch is %q", got)
	}
}

// One palette makes the whole look: change a colour in it and the prompt
// box, the questions, the Markdown and the diffs all follow.
func TestOnePalette(t *testing.T) {
	pal := DefaultPalette(text.TrueColor)
	pal.Purple = text.RGB(1, 2, 3)
	pal.Pink = text.RGB(4, 5, 6)
	pal.Green = text.RGB(7, 8, 9)
	pal.Rose = text.RGB(10, 11, 12)
	pal.Aqua = text.RGB(13, 14, 15)
	th := NewTheme(text.TrueColor, pal)
	for what, seq := range map[string]string{
		"the tool mark":                 th.Tool.Sequence(),
		"the box's paste":               th.Editor.Paste.Sequence(),
		"a question's hidden rows":      th.Menu.More.Sequence(),
		"Markdown's list marks":         th.Markdown.ListMark.Sequence(),
		"Markdown's links":              th.Markdown.Link.Sequence(),
		"the wordmark with few colours": th.WordmarkPlain[3].Sequence(),
		"the working mark's second":     th.Fall.Plain.Fg(th.Fall.Hues[1]).Sequence(),
	} {
		if !strings.Contains(seq, "38;2;1;2;3") {
			t.Errorf("%s is not in the palette's purple: %q", what, seq)
		}
	}
	for what, seq := range map[string]string{
		"a question's chosen row":  th.Menu.Selected.Sequence(),
		"Markdown's headings":      th.Markdown.Heading.Sequence(),
		"the working mark's third": th.Fall.Plain.Fg(th.Fall.Hues[2]).Sequence(),
	} {
		if !strings.Contains(seq, "38;2;4;5;6") {
			t.Errorf("%s is not in the palette's pink: %q", what, seq)
		}
	}
	for what, seq := range map[string]string{
		"the prompt mark":           th.Prompt.Sequence(),
		"the box's prompt mark":     th.Editor.PromptStyle.Sequence(),
		"a question's pointer":      th.Menu.PointerStyle.Sequence(),
		"the line that says busy":   th.Busy.Sequence(),
		"the working mark's first":  th.Fall.Plain.Fg(th.Fall.Hues[0]).Sequence(),
		"a question's field prompt": th.Menu.Field.PromptStyle.Sequence(),
	} {
		if !strings.Contains(seq, "38;2;13;14;15") {
			t.Errorf("%s is not in the palette's blue-green: %q", what, seq)
		}
	}
	if !strings.Contains(th.Diff.Inserted.Sequence(), "38;2;7;8;9") || !strings.Contains(th.Diff.Deleted.Sequence(), "38;2;10;11;12") {
		t.Errorf("the diff's colours are not the palette's: %q %q", th.Diff.Inserted.Sequence(), th.Diff.Deleted.Sequence())
	}
	// With no colour every style writes nothing at all.
	plain := NewTheme(text.NoColor, pal)
	for what, st := range map[string]text.Style{
		"the prompt mark": plain.Prompt, "quiet words": plain.Quiet, "the mode in which nothing asks": plain.Bypass,
		"the box's prompt": plain.Editor.PromptStyle, "a question's title": plain.Menu.Title,
		"a heading": plain.Markdown.Heading, "a deleted line": plain.Diff.Deleted,
	} {
		if got := st.Render("x"); got != "x" {
			t.Errorf("with no colour %s is written as %q", what, got)
		}
	}
}
