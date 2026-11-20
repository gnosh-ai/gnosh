package shelltool

import (
	"bytes"
	"fmt"
	"strings"
	"testing"
	"time"
	"unicode/utf8"
)

// feed writes data to a capture in pieces of the given size.
func feed(c *capture, data []byte, piece int) {
	for len(data) > 0 {
		n := min(max(piece, 1), len(data))
		c.Write(data[:n])
		data = data[n:]
	}
}

func TestCaptureKeepsBothEnds(t *testing.T) {
	t.Parallel()
	var data []byte
	for i := range 1000 {
		data = fmt.Appendf(data, "line %04d\n", i)
	}
	// However the stream is cut into pieces, the same bytes are kept.
	for _, piece := range []int{1, 3, 7, 10, 64, 99, 100, 101, 250, 4096, len(data)} {
		c := newCapture(100)
		feed(c, data, piece)
		if c.total != int64(len(data)) || c.lines != 1000 {
			t.Fatalf("piece %d: total %d, lines %d", piece, c.total, c.lines)
		}
		if got := string(c.head); got != string(data[:100]) {
			t.Errorf("piece %d: head = %q", piece, got)
		}
		if got := string(c.last()); got != string(data[len(data)-100:]) {
			t.Errorf("piece %d: last = %q", piece, got)
		}
		if c.whole() {
			t.Errorf("piece %d: a capture that dropped %d bytes says it is whole", piece, len(data)-200)
		}
		// since: what is kept from an offset on, and what is lost before it.
		for _, off := range []int64{0, 50, 100, 5000, int64(len(data)) - 100, int64(len(data)) - 30, int64(len(data))} {
			kept, lost := c.since(off)
			wantLost := max(int64(len(data))-100-max(off, 100), 0)
			var want []byte
			if off < 100 {
				want = append(want, data[off:100]...)
			}
			want = append(want, data[max(off, int64(len(data))-100):]...)
			if lost != wantLost || !bytes.Equal(kept, want) {
				t.Errorf("piece %d: since(%d) = %d bytes kept, %d lost; want %d kept, %d lost", piece, off, len(kept), lost, len(want), wantLost)
			}
		}
	}

	// A stream that fits is kept whole, whether or not it reached the ring.
	for _, n := range []int{0, 1, 99, 100, 101, 150, 199, 200} {
		c := newCapture(100)
		feed(c, data[:n], 13)
		if !c.whole() {
			t.Errorf("%d bytes in a capture that keeps 200: not whole", n)
		}
		if kept, lost := c.since(0); lost != 0 || !bytes.Equal(kept, data[:n]) {
			t.Errorf("%d bytes: since(0) = %d bytes, %d lost", n, len(kept), lost)
		}
	}
	c := newCapture(100)
	feed(c, data[:201], 13)
	if c.whole() {
		t.Error("201 bytes in a capture that keeps 200: whole")
	}
}

func TestCaptureView(t *testing.T) {
	t.Parallel()
	gap := func(lines int64) string { return fmt.Sprintf("<%d lines out>", lines) }
	tests := []struct {
		name  string
		data  string
		keep  int
		limit int
		want  string
		cut   bool
	}{
		{name: "empty", data: "", keep: 100, limit: 10, want: ""},
		{name: "fits", data: "one\ntwo\n", keep: 100, limit: 10, want: "one\ntwo\n"},
		{name: "fits exactly", data: "0123456789", keep: 100, limit: 10, want: "0123456789"},
		{name: "one character too many", data: "0123456789a", keep: 100, limit: 10, want: "01234\n<0 lines out>\n6789a", cut: true},
		{name: "cut between lines", data: "aa\nbb\ncc\ndd\nee\nff\ngg\n", keep: 100, limit: 12, want: "aa\nbb\n<3 lines out>\nff\ngg\n", cut: true},
		{name: "characters are counted, not bytes", data: "ééééé\nééééé\n", keep: 100, limit: 12, want: "ééééé\nééééé\n"},
		{name: "wide characters stay whole", data: strings.Repeat("한", 30), keep: 100, limit: 10, want: "한한한한한\n<0 lines out>\n한한한한한", cut: true},
		{name: "NULs and bad bytes", data: "a\x00b\xff\xfec\n", keep: 100, limit: 10, want: "ab\uFFFDc\n"},
		{name: "man overstrike and SGR colour codes", data: "N\bNA\bAM\bME\bE \x1b[32m_\bo_\bk\x1b[0m\n", keep: 100, limit: 20, want: "NAME ok\n"},
		{name: "only the ends are kept", data: strings.Repeat("0123456789\n", 100), keep: 44, limit: 44,
			want: "0123456789\n0123456789\n<96 lines out>\n0123456789\n0123456789\n", cut: true},
		{name: "characters cut by the edges of what is kept", data: "ab" + strings.Repeat("é", 100) + "c\n", keep: 9, limit: 8,
			want: "abéé\n<0 lines out>\nééc\n", cut: true},
		{name: "a tail that starts a line is not shortened", data: "a\nb\nc\nd\ne\nf\ng\nh\n", keep: 100, limit: 8, want: "a\nb\n<4 lines out>\ng\nh\n", cut: true},
		{name: "a tail that starts inside a line is", data: "aaa\nbbb\nccc\nddd\neee\n", keep: 100, limit: 12, want: "aaa\n<3 lines out>\neee\n", cut: true},
		{name: "but not at the cost of most of it", data: strings.Repeat("x", 50) + "\nEND\n", keep: 100, limit: 20, want: "xxxxxxxxxx\n<0 lines out>\nxxxxx\nEND\n", cut: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			for _, piece := range []int{1, 2, 5, 1 << 20} {
				c := newCapture(tt.keep)
				feed(c, []byte(tt.data), piece)
				got, cut := c.view(tt.limit, gap)
				if got != tt.want || cut != tt.cut {
					t.Errorf("pieces of %d:\n got %q (cut %v)\nwant %q (cut %v)", piece, got, cut, tt.want, tt.cut)
				}
			}
		})
	}
}

func TestCaptureEnd(t *testing.T) {
	t.Parallel()
	c := newCapture(50)
	feed(c, []byte("one\ntwo\nthree\n"), 4)
	if got := c.end(100); got != "one\ntwo\nthree\n" {
		t.Errorf("end of a short stream = %q", got)
	}
	if got := c.end(8); got != "three\n" {
		t.Errorf("end(8) = %q, want it to start at a line", got)
	}
	feed(c, []byte(strings.Repeat("abc\n", 50)+"the last line\n"), 7)
	if got, want := c.end(30), "abc\nabc\nabc\nabc\nthe last line\n"; got != want {
		t.Errorf("end(30) of a long stream = %q, want %q", got, want)
	}
	if got, want := c.end(29), "abc\nabc\nabc\nthe last line\n"; got != want {
		t.Errorf("end(29) of a long stream = %q, want it to drop the line it starts inside: %q", got, want)
	}
	if got := c.end(5); got != "line\n" {
		t.Errorf("end(5) = %q", got)
	}
	// All that is kept of a long stream starts in the middle of a line.
	if got, want := c.end(1000), strings.Repeat("abc\n", 8)+"the last line\n"; got != want {
		t.Errorf("end(1000) of a long stream = %q, want %q", got, want)
	}
}

func TestIncompleteSuffix(t *testing.T) {
	t.Parallel()
	tests := []struct {
		in   string
		want int
	}{
		{"", 0},
		{"abc", 0},
		{"é", 0},
		{"a\xc3", 1},         // the first of two bytes
		{"a\xe2", 1},         // the first of three
		{"a\xe2\x82", 2},     // two of three
		{"a\xe2\x82\xac", 0}, // all three: €
		{"\xf0\x9f", 2},      // two of four
		{"\xf0\x9f\x98", 3},  // three of four
		{"\xf0\x9f\x98\x80", 0},
		{"a\xff", 0},              // can never start a character
		{"a\x80", 0},              // a stray continuation byte
		{"\x80\x80\x80\x80", 0},   // nothing but stray bytes
		{"a\xc3\x28", 0},          // a broken sequence followed by text
		{"ok\xe2\x82\xac\xe2", 1}, // a whole character, then the start of another
	}
	for _, tt := range tests {
		if got := incompleteSuffix([]byte(tt.in)); got != tt.want {
			t.Errorf("incompleteSuffix(%q) = %d, want %d", tt.in, got, tt.want)
		}
	}
	if got := string(dropBrokenStart([]byte("\x82\xacabc"))); got != "abc" {
		t.Errorf("dropBrokenStart = %q", got)
	}
	if got := string(dropBrokenEnd([]byte("abc\xe2\x82"))); got != "abc" {
		t.Errorf("dropBrokenEnd = %q", got)
	}
}

func TestTextHelpers(t *testing.T) {
	t.Parallel()
	for d, want := range map[time.Duration]string{
		0:                         "0 s",
		50 * time.Millisecond:     "0.05 s",
		123456 * time.Microsecond: "0.123 s",
		700 * time.Millisecond:    "0.7 s",
		time.Second:               "1 s",
		3210 * time.Millisecond:   "3.21 s",
		12340 * time.Millisecond:  "12.3 s",
		2 * time.Minute:           "120 s",
		10 * time.Minute:          "600 s",
		99960 * time.Millisecond:  "100 s",
	} {
		if got := seconds(d); got != want {
			t.Errorf("seconds(%v) = %q, want %q", d, got, want)
		}
	}
	for n, want := range map[int64]string{
		0: "0 bytes", 1023: "1023 bytes", 1024: "1 KiB", 1536: "1.5 KiB", 64 << 10: "64 KiB",
		8 << 20: "8 MiB", 5 << 30: "5 GiB", 1<<30 + 1<<29: "1.5 GiB", 3 << 40: "3 TiB", 5000 << 40: "5000 TiB",
	} {
		if got := byteSize(n); got != want {
			t.Errorf("byteSize(%d) = %q, want %q", n, got, want)
		}
	}
	if got := firstLine("\n  \n\tfirst real line  \nsecond", 100); got != "first real line" {
		t.Errorf("firstLine = %q", got)
	}
	if got := firstLine(strings.Repeat("한", 300), 10); utf8.RuneCountInString(got) != 10 || !strings.HasSuffix(got, "…") {
		t.Errorf("firstLine cut to 10 = %q", got)
	}
	if got := oneLine("  a\n\tb   c \n", 100); got != "a b c" {
		t.Errorf("oneLine = %q", got)
	}
	if got := gapLine(3, "/x/out", 10, 10); got != "[3 lines omitted, full output in /x/out]" {
		t.Errorf("gapLine = %q", got)
	}
	if got := gapLine(1, "/x/out", 2048, 4096); got != "[1 line omitted, the first 2 KiB of the output are in /x/out]" {
		t.Errorf("gapLine = %q", got)
	}
	if got := gapLine(0, "", 0, 10); got != "[output omitted here]" {
		t.Errorf("gapLine = %q", got)
	}
}

// FuzzCaptureView checks, for any stream cut into any pieces, that a view
// is text the model can be given and holds no more than it should.
func FuzzCaptureView(f *testing.F) {
	f.Add([]byte("hello\nworld\n"), 5, 8, 3)
	f.Add([]byte(strings.Repeat("0123456789\n", 50)), 40, 30, 7)
	f.Add([]byte("a\x00b\xff\xfe\xe2\x82"), 4, 3, 1)
	f.Add([]byte(strings.Repeat("한글", 40)), 16, 9, 5)
	f.Add([]byte{}, 1, 1, 1)
	f.Fuzz(func(t *testing.T, data []byte, keep, limit, piece int) {
		keep = 1 + abs(keep)%300
		limit = 1 + abs(limit)%200
		// The manager always keeps at least two bytes for each character it
		// may show from each end; see launch.
		keep = max(keep, 2*limit)
		c := newCapture(keep)
		feed(c, data, 1+abs(piece)%64)
		if c.total != int64(len(data)) || c.lines != int64(bytes.Count(data, []byte{'\n'})) {
			t.Fatalf("total %d, lines %d for %d bytes", c.total, c.lines, len(data))
		}
		var left int64 = -1
		view, cut := c.view(limit, func(lines int64) string {
			left = lines
			return "\x01GAP\x01"
		})
		if !utf8.ValidString(view) || strings.ContainsRune(view, 0) {
			t.Fatalf("the view is not clean text: %q", view)
		}
		whole := clean(data)
		if !cut {
			if view != whole || utf8.RuneCountInString(view) > limit {
				t.Fatalf("an uncut view of %q is %q (limit %d)", data, view, limit)
			}
			return
		}
		first, last, ok := strings.Cut(view, "\x01GAP\x01\n")
		if !ok || left < 0 {
			t.Fatalf("a cut view has no line for the gap: %q", view)
		}
		first = strings.TrimSuffix(first, "\n")
		if n := utf8.RuneCountInString(first) + utf8.RuneCountInString(last); n > limit {
			t.Fatalf("a cut view shows %d characters, limit %d: %q", n, limit, view)
		}
		if int64(strings.Count(first, "\n")+strings.Count(last, "\n"))+left > c.lines {
			t.Fatalf("lines shown and lines left out add up to more than there were: %q, %d left out of %d", view, left, c.lines)
		}
		// When nothing was dropped from memory, what is shown is the two
		// ends of the cleaned stream.
		if c.whole() && (!strings.HasPrefix(whole, first) || !strings.HasSuffix(whole, last)) {
			t.Fatalf("the view %q is not the two ends of %q", view, whole)
		}
		if tail := c.end(limit); !utf8.ValidString(tail) || utf8.RuneCountInString(tail) > limit {
			t.Fatalf("end(%d) = %q", limit, tail)
		}
	})
}

// FuzzIncompleteSuffix checks that cutting valid text anywhere and holding
// back the incomplete suffix never loses or splits a character.
func FuzzIncompleteSuffix(f *testing.F) {
	f.Add("héllo € 😀 한글", 3)
	f.Add("plain", 2)
	f.Add("", 0)
	f.Fuzz(func(t *testing.T, s string, at int) {
		b := []byte(s)
		k := incompleteSuffix(b)
		if k < 0 || k > 3 || k > len(b) {
			t.Fatalf("incompleteSuffix(%q) = %d", s, k)
		}
		if !utf8.ValidString(s) {
			return
		}
		at = abs(at) % (len(b) + 1)
		k = incompleteSuffix(b[:at])
		if !utf8.Valid(b[:at-k]) {
			t.Fatalf("%q cut at %d and shortened by %d is not whole characters", s, at, k)
		}
		if k == 0 && !utf8.Valid(b[:at]) {
			t.Fatalf("%q cut at %d splits a character, and no bytes were held back", s, at)
		}
	})
}

func abs(n int) int {
	if n < 0 {
		if n == -n { // the one number with no positive twin
			return 0
		}
		return -n
	}
	return n
}
