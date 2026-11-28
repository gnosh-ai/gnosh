package shelltool

import (
	"bytes"
	"strings"
	"unicode/utf8"
)

// capture keeps the first and the last bytes of a stream and counts what
// goes by, so that a view of bounded size can be made of output of any
// size. Its memory never grows past twice keep. Make one with newCapture.
type capture struct {
	keep  int    // how many bytes are kept at each end
	head  []byte // the first bytes of the stream: at most keep of them
	ring  []byte // the latest bytes that did not fit in head: a ring of keep bytes
	next  int    // where the next byte goes in ring
	full  bool   // ring has wrapped, so all of it holds data
	total int64  // bytes seen
	lines int64  // newlines seen
}

// newCapture makes a capture that keeps keep bytes at each end.
func newCapture(keep int) *capture {
	return &capture{keep: max(keep, 1)}
}

// Write takes the next piece of the stream. It never fails.
func (c *capture) Write(p []byte) (int, error) {
	n := len(p)
	c.total += int64(n)
	c.lines += int64(bytes.Count(p, []byte{'\n'}))
	if room := c.keep - len(c.head); room > 0 {
		k := min(room, len(p))
		c.head = append(c.head, p[:k]...)
		p = p[k:]
	}
	if len(p) == 0 {
		return n, nil
	}
	if c.ring == nil {
		c.ring = make([]byte, c.keep)
	}
	if len(p) >= c.keep {
		copy(c.ring, p[len(p)-c.keep:])
		c.next, c.full = 0, true
		return n, nil
	}
	if k := copy(c.ring[c.next:], p); k < len(p) {
		copy(c.ring, p[k:])
	}
	c.next += len(p)
	if c.next >= c.keep {
		c.next -= c.keep
		c.full = true
	}
	return n, nil
}

// last returns the bytes in the ring, oldest first. The result may share
// the ring's memory, so it is only good until the next Write.
func (c *capture) last() []byte {
	if !c.full {
		return c.ring[:c.next]
	}
	out := make([]byte, 0, c.keep)
	out = append(out, c.ring[c.next:]...)
	return append(out, c.ring[:c.next]...)
}

// whole reports whether every byte of the stream is still kept.
func (c *capture) whole() bool {
	if c.full {
		return c.total == int64(len(c.head)+c.keep)
	}
	return true
}

// since returns what is still kept of the stream from offset off on, and
// how many of the bytes from off on are not kept any more.
func (c *capture) since(off int64) (kept []byte, lost int64) {
	off = min(max(off, 0), c.total)
	if headEnd := int64(len(c.head)); off < headEnd {
		kept = append(kept, c.head[off:]...)
		off = headEnd
	}
	last := c.last()
	if start := c.total - int64(len(last)); off < start {
		lost = start - off
		off = start
	}
	return append(kept, last[int64(len(last))-(c.total-off):]...), lost
}

// view returns the stream as the model is shown it: all of it when it is
// at most limit characters long, and otherwise its start and its end,
// limit characters together, around one line that gap makes from the number
// of lines left out. It reports whether anything was left out.
func (c *capture) view(limit int, gap func(lines int64) string) (text string, cut bool) {
	var first, last string
	if c.whole() {
		all := clean(append(c.head[:len(c.head):len(c.head)], c.last()...))
		if utf8.RuneCountInString(all) <= limit {
			return all, false
		}
		first, last = firstRunes(all, limit/2), tailOf(all, limit-limit/2, true)
	} else {
		first = firstRunes(clean(dropBrokenEnd(c.head)), limit/2)
		last = tailOf(clean(dropBrokenStart(c.last())), limit-limit/2, false)
	}
	// End the first part with a whole line where that does not cost much
	// of what is shown.
	if i := strings.LastIndexByte(first, '\n'); i >= 0 && i >= len(first)/2 {
		first = first[:i+1]
	}
	left := c.lines - int64(strings.Count(first, "\n")) - int64(strings.Count(last, "\n"))
	var b strings.Builder
	b.Grow(len(first) + len(last) + 160)
	b.WriteString(first)
	if first != "" && !strings.HasSuffix(first, "\n") {
		b.WriteByte('\n')
	}
	b.WriteString(gap(max(left, 0)))
	b.WriteByte('\n')
	b.WriteString(last)
	return b.String(), true
}

// end returns the last limit characters of the stream, starting at the
// start of a line where it can.
func (c *capture) end(limit int) string {
	if c.whole() {
		return tailOf(clean(append(c.head[:len(c.head):len(c.head)], c.last()...)), limit, true)
	}
	return tailOf(clean(dropBrokenStart(c.last())), limit, false)
}

// tailOf returns the last limit characters of src, made to start at the
// start of a line where that does not cost much of them. atStart says that
// src begins where the stream itself begins, so that its first character
// starts a line.
func tailOf(src string, limit int, atStart bool) string {
	last := lastRunes(src, limit)
	switch before := len(src) - len(last); {
	case before == 0 && atStart:
		return last // all of the stream
	case before > 0 && src[before-1] == '\n':
		return last // it starts a line as it is
	}
	if i := strings.IndexByte(last, '\n'); i >= 0 && i < len(last)/2 {
		last = last[i+1:]
	}
	return last
}

// foldOverstrikes collapses man-page bold (X\bX), underline (_\bX or X\b_)
// and bullet (+\bo or o\b+) overstrikes into the character they format.
func foldOverstrikes(s string) string {
	if strings.IndexByte(s, '\b') < 0 {
		return s
	}
	runes := []rune(s)
	out := make([]rune, 0, len(runes))
	for i := 0; i < len(runes); i++ {
		r := runes[i]
		if r == '\b' && len(out) > 0 && i+1 < len(runes) {
			prev, next := out[len(out)-1], runes[i+1]
			if prev > 0x20 && prev != 0x7f && next > 0x20 && next != 0x7f {
				switch {
				case prev == next, prev == '_':
					out[len(out)-1] = next
					i++
					continue
				case next == '_':
					out[len(out)-1] = prev
					i++
					continue
				case (prev == '+' && next == 'o') || (prev == 'o' && next == '+'):
					out[len(out)-1] = '\u2022'
					i++
					continue
				}
			}
		}
		out = append(out, r)
	}
	return string(out)
}

// stripSGR removes benign CSI SGR colour/style sequences (\x1b[[0-9;:]*m)
// while keeping all other control sequences intact.
func stripSGR(s string) string {
	if !strings.Contains(s, "\x1b[") {
		return s
	}
	var b strings.Builder
	b.Grow(len(s))
	for i := 0; i < len(s); {
		if i+1 < len(s) && s[i] == '\x1b' && s[i+1] == '[' {
			j := i + 2
			for j < len(s) && ((s[j] >= '0' && s[j] <= '9') || s[j] == ';' || s[j] == ':') {
				j++
			}
			if j < len(s) && s[j] == 'm' {
				i = j + 1
				continue
			}
		}
		b.WriteByte(s[i])
		i++
	}
	return b.String()
}

// clean makes output fit to show: NUL bytes are dropped, bytes that are
// not valid UTF-8 become U+FFFD (one for each run of them), man-page
// backspace overstrikes (X\bX and _\bX) collapse to the character they
// mark, and SGR colour codes (\x1b[...m) are stripped.
func clean(b []byte) string {
	if bytes.IndexByte(b, 0) >= 0 {
		b = bytes.ReplaceAll(b, []byte{0}, nil)
	}
	return stripSGR(foldOverstrikes(strings.ToValidUTF8(string(b), "\uFFFD")))
}

// incompleteSuffix returns how many bytes at the end of b are the start of
// a UTF-8 sequence whose rest is missing: 0 to 3. Bytes that could never
// become a character count as complete, so that they are shown, as U+FFFD,
// rather than waited on.
func incompleteSuffix(b []byte) int {
	for i := 1; i <= 3 && i <= len(b); i++ {
		if p := b[len(b)-i:]; utf8.RuneStart(p[0]) {
			if utf8.FullRune(p) {
				return 0
			}
			return i
		}
	}
	return 0
}

// dropBrokenEnd returns b without a UTF-8 sequence cut short at its end.
func dropBrokenEnd(b []byte) []byte {
	return b[:len(b)-incompleteSuffix(b)]
}

// dropBrokenStart returns b without the tail of a UTF-8 sequence whose
// start is missing at its beginning.
func dropBrokenStart(b []byte) []byte {
	for i := 0; i < 3 && len(b) > 0 && !utf8.RuneStart(b[0]); i++ {
		b = b[1:]
	}
	return b
}

// firstRunes returns the first n characters of s.
func firstRunes(s string, n int) string {
	if len(s) <= n {
		return s
	}
	i := 0
	for ; n > 0 && i < len(s); n-- {
		_, w := utf8.DecodeRuneInString(s[i:])
		i += w
	}
	return s[:i]
}

// lastRunes returns the last n characters of s.
func lastRunes(s string, n int) string {
	if len(s) <= n {
		return s
	}
	i := len(s)
	for ; n > 0 && i > 0; n-- {
		_, w := utf8.DecodeLastRuneInString(s[:i])
		i -= w
	}
	return s[i:]
}
