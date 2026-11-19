package fstools

import (
	"context"
	"crypto/sha256"
	"fmt"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/gnosh-ai/gnosh/internal/core"
)

// editTool is the Edit tool.
type editTool struct {
	opts *Options
	sys  *fileOps
}

func (t *editTool) props() []prop {
	return []prop{
		{name: "file_path", kind: "string", desc: "Path of the file to change. Absolute is best; a relative path is taken from the working directory."},
		{name: "old_string", kind: "string", desc: "The text to replace, exactly as it is in the file, indentation included. Without the line numbers that the read tool shows."},
		{name: "new_string", kind: "string", desc: "The text to put in its place. An empty string deletes old_string."},
		{name: "replace_all", kind: "boolean", desc: "Replace every occurrence of old_string, not just one. Default false."},
	}
}

// Spec tells the model what Edit does.
func (t *editTool) Spec() core.ToolSpec {
	o := t.opts
	desc := fmt.Sprintf(`Changes a file by replacing one exact piece of its text with another.

old_string must be in the file character for character: same indentation, same spaces, same letter case. Nothing is matched loosely and it is not a pattern. Copy it from what %[1]s showed, leaving out the line number and the tab in front of each line.

old_string must pick out one place. If it is found more than once the call is refused: add surrounding lines until it is unique, or set replace_all to change every occurrence.

The lines being changed must have been shown to this session by %[1]s, and the file must not have changed on disk since; otherwise the call is refused and says what to read. The file keeps its line endings (a line break in old_string matches LF and CRLF alike), its byte order mark and its permissions. When old_string is not found, the error says where the closest text is and how it differs.

To make a new file or replace a whole one, use %[2]s.`,
		o.ReadName, o.WriteName)
	return core.ToolSpec{
		Name:        o.EditName,
		Description: desc,
		InputSchema: schema([]string{"file_path", "old_string", "new_string"}, t.props()...),
	}
}

// lfView is a text with every CRLF read as a plain line break, and the way
// back from a place in it to the same place in the text it was made from.
type lfView struct {
	text string
	// cr holds, for every CRLF of the original, the offset in text of the
	// line break that stands for it, in rising order.
	cr []int
}

func newLFView(raw string) lfView {
	if !strings.Contains(raw, "\r\n") {
		return lfView{text: raw}
	}
	var b strings.Builder
	b.Grow(len(raw))
	var cr []int
	for {
		i := strings.Index(raw, "\r\n")
		if i < 0 {
			b.WriteString(raw)
			break
		}
		b.WriteString(raw[:i])
		cr = append(cr, b.Len())
		b.WriteByte('\n')
		raw = raw[i+2:]
	}
	return lfView{text: b.String(), cr: cr}
}

// rawOffset turns an offset in the view's text into the offset of the
// same place in the original. An offset that points at a line break which
// was a CRLF comes out pointing at the carriage return, so that a range
// which starts or ends at a line break takes in, or leaves out, the whole
// CRLF.
func (v lfView) rawOffset(i int) int {
	return i + sort.SearchInts(v.cr, i)
}

// crlfAt reports whether the line holding offset i of raw ends with CRLF.
// A last line with no line break after it goes by the line before it; a
// text of one line counts as LF.
func crlfAt(raw string, i int) bool {
	if j := strings.IndexByte(raw[i:], '\n'); j >= 0 {
		p := i + j
		return p > 0 && raw[p-1] == '\r'
	}
	if j := strings.LastIndexByte(raw[:i], '\n'); j >= 0 {
		return j > 0 && raw[j-1] == '\r'
	}
	return false
}

// findAll returns the offset of every place old is in text, left to
// right, none overlapping another.
func findAll(text, old string) []int {
	var starts []int
	for from := 0; ; {
		i := strings.Index(text[from:], old)
		if i < 0 {
			return starts
		}
		starts = append(starts, from+i)
		from += i + len(old)
	}
}

// substitute puts repl in the place of old at each of the given offsets
// of the view's text and returns the result as bytes of the original:
// every byte outside the replaced stretches is the original's own, and a
// line break in repl is written the way the line it lands in ends. body is
// the text the view was made from; old and repl have plain line breaks.
func substitute(body string, view lfView, starts []int, old, repl string) string {
	var out strings.Builder
	out.Grow(len(body) + len(starts)*(len(repl)-len(old)) + 16)
	var withCRLF string
	hasBreak := strings.Contains(repl, "\n")
	pos := 0
	for _, s := range starts {
		from, to := view.rawOffset(s), view.rawOffset(s+len(old))
		out.WriteString(body[pos:from])
		if hasBreak && crlfAt(body, from) {
			if withCRLF == "" {
				withCRLF = strings.ReplaceAll(repl, "\n", "\r\n")
			}
			out.WriteString(withCRLF)
		} else {
			out.WriteString(repl)
		}
		pos = to
	}
	out.WriteString(body[pos:])
	return out.String()
}

// plan works out what an Edit call would do. Once the path is known it
// returns a change even alongside a failure, so that a caller can say
// which file the call was about.
func (t *editTool) plan(call core.ToolCall, env *core.ToolEnv) (*change, *failure) {
	o := t.opts
	a, f := parseArgs(o.EditName, call.Input, propNames(t.props())...)
	if f != nil {
		return nil, f
	}
	path, f := a.required("file_path")
	if f != nil {
		return nil, f
	}
	old, f := a.required("old_string")
	if f != nil {
		return nil, f
	}
	repl, f := a.required("new_string")
	if f != nil {
		return nil, f
	}
	all, _, f := a.flag("replace_all")
	if f != nil {
		return nil, f
	}
	if old == "" {
		return nil, failf("%s: old_string is empty, so there is nothing to find. To fill an empty file or make a new one, use %s.", o.EditName, o.WriteName)
	}
	if old == repl {
		return nil, failf("%s: old_string and new_string are the same, so there is nothing to change.", o.EditName)
	}
	loc, f := o.locateFile(env, o.EditName, path)
	if f != nil {
		return nil, f
	}
	c := &change{loc: loc, shown: true, replaceAll: all, summary: "Edit " + loc.abs}
	if loc.trouble != nil {
		return c, loc.trouble
	}
	if !loc.exists {
		return c, failf("%s To make a new file, use %s.", missingFile(&loc).msg, o.WriteName)
	}
	if f := o.writable(o.EditName, &loc); f != nil {
		return c, f
	}
	rec, known := sessionOf(env).record(loc.real)
	if !known {
		return c, failf("This session has not read %s. Nothing was changed. Read it with %s first, then edit.", loc.abs, o.ReadName)
	}
	if loc.info.Size() > o.EditMaxBytes {
		return c, failf("%s is %s, more than the %s that %s takes on. Nothing was changed. Use a shell command to change it.", loc.abs, byteSize(loc.info.Size()), byteSize(o.EditMaxBytes), o.EditName)
	}
	content, info, f := readWhole(loc.real, o.EditMaxBytes)
	if f != nil {
		return c, f
	}
	if content == nil {
		return c, failf("%s is %s, more than the %s that %s takes on. Nothing was changed. Use a shell command to change it.", loc.abs, byteSize(info.Size()), byteSize(o.EditMaxBytes), o.EditName)
	}
	c.old = info
	if !rec.unchanged(info, content) {
		return c, staleFailure(o, &loc)
	}
	if looksBinary(content) {
		return c, failf("%s looks like a binary file, and %s only changes text. Nothing was changed.", loc.abs, o.EditName)
	}
	if !utf8.Valid(content) {
		return c, failf("%s is not valid UTF-8: it may be in an older encoding such as Latin-1 or Shift-JIS, or hold a damaged character. %s cannot tell where its characters begin and end, so it changes nothing in such a file. Make the change with a shell command that keeps the file's encoding, or ask whether the file should be converted to UTF-8 first.", loc.abs, o.EditName)
	}
	raw := string(content)
	c.before = raw
	c.rec = rec

	bom, body := "", raw
	if strings.HasPrefix(raw, utf8BOM) {
		bom, body = utf8BOM, raw[len(utf8BOM):]
	}
	view := newLFView(body)
	oldLF := strings.ReplaceAll(old, "\r\n", "\n")
	newLF := strings.ReplaceAll(repl, "\r\n", "\n")

	starts := findAll(view.text, oldLF)
	if len(starts) == 0 {
		return c, missingText(o, &loc, view.text, oldLF, len(view.cr) > 0)
	}

	// The lines each place touches, in the file as it is.
	oldBreaks := strings.Count(oldLF, "\n")
	spanLines := oldBreaks
	if strings.HasSuffix(oldLF, "\n") {
		// A line break at the end of old_string belongs to its last line.
		spanLines--
	}
	delta := strings.Count(newLF, "\n") - oldBreaks
	line, at := 1, 0
	for _, s := range starts {
		line += strings.Count(view.text[at:s], "\n")
		at = s
		c.edits = append(c.edits, lineEdit{first: line, last: line + spanLines, delta: delta})
	}
	if len(starts) > 1 && !all {
		return c, failf("old_string is found %d times in %s (at %s). Nothing was changed. Add more of the surrounding text to old_string so that it picks out one place, or set replace_all to true to change every one.",
			len(starts), loc.abs, editLines(c.edits, false))
	}
	var unseen []lineEdit
	for _, e := range c.edits {
		if !rec.sees(span{e.first, e.last}) {
			unseen = append(unseen, e)
		}
	}
	if len(unseen) > 0 {
		return c, failf("The text to replace is at %s of %s, which this session has not been shown: it has seen %s. Nothing was changed. Read those lines with %s (offset=%d), then edit.",
			editLines(unseen, true), loc.abs, rec.seenText(), o.ReadName, unseen[0].first)
	}

	c.after = bom + substitute(body, view, starts, oldLF, newLF)
	if c.after == raw {
		return c, failf("The edit would leave %s exactly as it is: old_string and new_string differ only in how their lines end, and %s keeps the file's line endings. Nothing was changed.", loc.abs, o.EditName)
	}

	c.count = len(starts)
	shift := 0
	for _, e := range c.edits {
		c.lines = append(c.lines, e.first+shift)
		shift += e.delta
	}
	c.summary = fmt.Sprintf("Edit %s: replace %s, at %s", loc.abs, countOf(c.count, "occurrence", "occurrences"), editLines(c.edits, false))
	return c, nil
}

// editLines lists where replacements are, for messages: "line 12",
// "lines 12, 40 and 97", or with ranges, "lines 12-14".
func editLines(edits []lineEdit, ranges bool) string {
	parts := make([]string, 0, len(edits))
	many := len(edits) > 1
	for i, e := range edits {
		if i == 8 {
			parts = append(parts, fmt.Sprintf("%d more", len(edits)-i))
			break
		}
		if ranges && e.last > e.first {
			parts = append(parts, fmt.Sprintf("%d-%d", e.first, e.last))
			many = true
		} else {
			parts = append(parts, fmt.Sprint(e.first))
		}
	}
	if many {
		return "lines " + joinAnd(parts)
	}
	return "line " + parts[0]
}

// Describe says what an Edit call would do, with the diff it would make.
// When the call would fail, the error comes with an Action that still
// names the paths, so that a permission layer can refuse the path before
// anything about the file is passed on.
func (t *editTool) Describe(call core.ToolCall, env *core.ToolEnv) (core.Action, error) {
	c, f := t.plan(call, env)
	if c == nil {
		return core.Action{Kind: core.ActionEdit}, asError(f)
	}
	if f != nil {
		return core.Action{Kind: core.ActionEdit, Paths: c.loc.paths(), Summary: c.summary}, f
	}
	t.sys.judge(call.ID, c.loc.real)
	return c.action(), nil
}

// Run makes the edit.
func (t *editTool) Run(ctx context.Context, call core.ToolCall, env *core.ToolEnv) (core.ToolResult, error) {
	o := t.opts
	// Find out which file the call is about and wait for the session's
	// other changes to it to finish before looking at it: an edit that ran
	// while this one waited must be seen, not lost.
	where, ok := pathArg(o, o.EditName, call, env)
	if !ok {
		_, f := t.plan(call, env)
		return unusable(o.EditName, f)
	}
	unlock, err := lockFor(ctx, env, o.EditName, &where)
	if err != nil {
		return core.ToolResult{}, err
	}
	defer unlock()
	c, f := t.plan(call, env)
	if f != nil {
		return refused(f)
	}
	if c.loc.real != where.real {
		return refused(failf("%s leads somewhere else than it did a moment ago (%s, not %s). Nothing was changed. Run the call again if that is what you mean.", c.loc.abs, c.loc.real, where.real))
	}
	if was, moved := t.sys.judgedElsewhere(call.ID, c.loc.real); moved {
		return refused(failf("%s leads somewhere else than when the call was looked at (%s, not %s): a link or a folder on the way changed in between. Nothing was changed. Run the call again if that is what you mean.", c.loc.abs, c.loc.real, was))
	}
	if err := ctx.Err(); err != nil {
		return core.ToolResult{}, fmt.Errorf("%s of %s stopped before anything was changed: %w", o.EditName, c.loc.abs, context.Cause(ctx))
	}
	if f := o.beforeChange(env, c); f != nil {
		return refused(f)
	}
	info, inPlace, f := commit(t.sys, c)
	if f != nil {
		return refused(f)
	}

	// The session has seen what it had seen before, moved to where those
	// lines now are, and it has seen the text it put in.
	lines := lineCount(strings.TrimPrefix(c.after, utf8BOM))
	seen := []span(nil)
	if !c.rec.all {
		seen = shiftSpans(c.rec.seen, c.edits)
	}
	sessionOf(env).wrote(c.loc.real, info, sha256.Sum256([]byte(c.after)), c.rec.all, seen, lines)

	var text strings.Builder
	fmt.Fprintf(&text, "Edited %s%s: %s, at %s", c.loc.abs, c.loc.viaLink(), countOf(c.count, "replacement", "replacements"), newLines(c.lines))
	if inPlace {
		text.WriteString(". The file was overwritten where it is, because its directory did not allow replacing it in one step")
	}
	if snippet := t.snippet(c); snippet != "" {
		text.WriteString(". ")
		text.WriteString(snippet)
	} else {
		text.WriteString(".")
	}
	res := core.TextResult(text.String())
	res.Display = c.display()
	res.Meta = map[string]any{
		"filePath":     c.loc.abs,
		"replaceAll":   c.replaceAll,
		"replacements": c.count,
		"lines":        c.lines,
	}
	if c.loc.real != c.loc.abs {
		res.Meta["realPath"] = c.loc.real
	}
	if inPlace {
		res.Meta["inPlace"] = true
	}
	return res, nil
}

// newLines lists the lines, in the file as it now is, where replacements
// start.
func newLines(lines []int) string {
	parts := make([]string, 0, len(lines))
	for i, n := range lines {
		if i == 8 {
			parts = append(parts, fmt.Sprintf("%d more", len(lines)-i))
			break
		}
		parts = append(parts, fmt.Sprint(n))
	}
	if len(parts) == 1 {
		return "line " + parts[0]
	}
	return "lines " + joinAnd(parts)
}

// snippet shows the model the lines around the first replacement as they
// now are, numbered the way Read numbers them, so that it can check the
// edit without reading the file again.
func (t *editTool) snippet(c *change) string {
	ctx := t.opts.EditContextLines
	if ctx < 0 || len(c.edits) == 0 {
		return ""
	}
	e := c.edits[0]
	start := c.lines[0]
	// The lines the new text takes up; none when the edit only removed
	// lines.
	length := max(e.last-e.first+1+e.delta, 0)
	const mostNew = 24
	shownNew := min(length, mostNew)
	first := max(start-ctx, 1)
	want := (start - first) + shownNew
	if shownNew == length {
		want += ctx
	}
	body := strings.TrimPrefix(c.after, utf8BOM)
	lines := linesFrom(body, first, want)
	if len(lines) == 0 {
		return ""
	}
	var out strings.Builder
	last := first + len(lines) - 1
	if first == last {
		fmt.Fprintf(&out, "Line %d is now:\n", first)
	} else {
		fmt.Fprintf(&out, "Lines %d-%d are now:\n", first, last)
	}
	for i, line := range lines {
		kept, dropped := cutChars([]byte(strings.TrimSuffix(line, "\r")), t.opts.ReadMaxLineChars)
		text, _ := showable(kept)
		if dropped > 0 {
			text += cutMark(dropped)
		}
		fmt.Fprintf(&out, "%d\t%s\n", first+i, text)
	}
	if shownNew < length {
		fmt.Fprintf(&out, "[... %d more lines of the new text follow.]\n", length-shownNew)
	}
	switch others := len(c.lines) - 1; {
	case others == 1:
		out.WriteString("[The other replacement is not shown.]\n")
	case others > 1:
		fmt.Fprintf(&out, "[The other %d replacements are not shown.]\n", others)
	}
	return out.String()
}

// linesFrom returns up to count lines of text starting at line first,
// which counts from one. The lines come without their line breaks.
func linesFrom(text string, first, count int) []string {
	for n := 1; n < first; n++ {
		i := strings.IndexByte(text, '\n')
		if i < 0 {
			return nil
		}
		text = text[i+1:]
	}
	var out []string
	for len(out) < count && text != "" {
		i := strings.IndexByte(text, '\n')
		if i < 0 {
			out = append(out, text)
			break
		}
		out = append(out, text[:i])
		text = text[i+1:]
	}
	return out
}
