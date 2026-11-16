package shellquote

import (
	"os/exec"
	"testing"
)

func TestWord(t *testing.T) {
	for in, want := range map[string]string{
		"gnosh":           "gnosh",
		"--resume=abc-1":  "--resume=abc-1",
		"/a/b.jsonl":      "/a/b.jsonl",
		"":                "''",
		"two words":       "'two words'",
		"it's":            `'it'\''s'`,
		"$HOME":           "'$HOME'",
		"a;b":             "'a;b'",
		`{"a": 1}`:        `'{"a": 1}'`,
		"Bash(git log:*)": "'Bash(git log:*)'",
	} {
		if got := Word(in); got != want {
			t.Errorf("Word(%q) = %q, want %q", in, got, want)
		}
	}
	if got, want := Quote("plain"), "'plain'"; got != want {
		t.Errorf("Quote = %q, want %q", got, want)
	}
	if got, want := Words([]string{"gnosh", "--settings={\"a\": 1}", "-p"}), `gnosh '--settings={"a": 1}' -p`; got != want {
		t.Errorf("Words = %q, want %q", got, want)
	}
}

// A shell reads what Word writes back as the word it was.
func TestWordRoundTrip(t *testing.T) {
	sh, err := exec.LookPath("sh")
	if err != nil {
		t.Skip("no sh here")
	}
	for _, in := range []string{"plain", "", "two words", "it's", `a'b"c`, "$(id)", "`id`", "a\nb", "\\", "*"} {
		out, err := exec.Command(sh, "-c", `printf %s `+Word(in)).Output()
		if err != nil || string(out) != in {
			t.Errorf("sh read %q as %q (%v)", Word(in), out, err)
		}
	}
}
