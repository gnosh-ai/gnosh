package fstools

import (
	"context"
	"fmt"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gnosh-ai/gnosh/internal/core"
	"github.com/gnosh-ai/gnosh/internal/tools/fstools/wild"
)

// grepTool is the Grep tool.
type grepTool struct {
	opts *Options
}

func (t *grepTool) props() []prop {
	return []prop{
		{name: "pattern", kind: "string", desc: "The regular expression to search for, in Go (RE2) syntax."},
		{name: "path", kind: "string", desc: "The file or directory to search. Leave out for the working directory."},
		{name: "glob", kind: "string", desc: "Search only files whose name or path matches this glob, such as *.go, *.{ts,tsx} or src/**/*.ts. A glob without a slash is compared with the file's name; one with a slash, with its path below the search directory. Start it with ! to leave matching files out instead."},
		{name: "type", kind: "string", desc: "Search only files of this kind, such as go, py, js, ts, rust, java, c, cpp, md, json, yaml, sh."},
		{name: "output_mode", kind: "string", enum: []string{"files_with_matches", "content", "count"}, desc: "What to return: files_with_matches (the default) for the paths of files with a match, content for the matching lines, count for the number of matching lines in each file."},
		{name: "-i", kind: "boolean", desc: "Ignore letter case."},
		{name: "-n", kind: "boolean", desc: "Show line numbers in content mode. Default true."},
		{name: "-o", kind: "boolean", desc: "Show only the text that matched, not the whole line. Needs output_mode content; context lines are not shown with it."},
		{name: "-A", kind: "integer", desc: "Lines of context to show after each match, in content mode."},
		{name: "-B", kind: "integer", desc: "Lines of context to show before each match, in content mode."},
		{name: "-C", kind: "integer", desc: "Lines of context to show both before and after each match, in content mode."},
		{name: "context", kind: "integer", desc: "The same as -C."},
		{name: "multiline", kind: "boolean", desc: "Let the pattern match across line breaks. Default false: a match lies within one line."},
		{name: "head_limit", kind: "integer", desc: fmt.Sprintf("Show at most this many lines (content mode) or files (the other modes). Default %d; 0 means no limit.", t.opts.GrepHeadLimit)},
		{name: "offset", kind: "integer", desc: "Skip this many lines or files before showing any. Use it with head_limit to page through a long result."},
	}
}

// Spec tells the model what Grep does.
func (t *grepTool) Spec() core.ToolSpec {
	o := t.opts
	var left []string
	if !o.GrepNoIgnore {
		left = append(left, "files that .gitignore rules leave out")
	}
	if !o.GrepHidden {
		left = append(left, "files and directories whose names start with a dot")
	}
	if !o.GrepBinary {
		left = append(left, "files that look binary")
	}
	left = append(left, "anything in a .git directory", "anything reached through a symbolic link")
	desc := fmt.Sprintf(`Searches the contents of files for a regular expression.

The pattern is a Go regular expression (RE2 syntax). It has the usual character classes, groups, alternatives and repetition, but no look-ahead or look-behind and no backreferences. The characters ( ) [ ] { } . * + ? | ^ $ and \ have meanings in a pattern: put a backslash before one to search for the character itself.

path is a file or a directory; left out, it is the working directory. In a directory, every file below it is searched except: %[1]s. To search one of those, give its path. glob and type narrow the search to some files.

output_mode chooses what comes back:
- files_with_matches (the default): the paths of the files that have a match.
- content: the matching lines, under the path of the file they are in, each as its line number, a colon, and the line. -A, -B and -C add lines around each match; those have a dash where matching lines have the colon, and a line of -- stands between stretches that are not next to each other. -o shows only the text that matched.
- count: for each file, its path, a colon, and how many of its lines match; then the total.

Files with a match come most recently changed first; lines and counts come in the order of their files' paths, so that paging with offset is steady. A match lies within one line unless multiline is true. With multiline the pattern may run over line breaks (write \n for one; . still does not match a line break unless the pattern starts with (?s)), ^ and $ match at the start and end of every line, and count counts matches, not lines.

At most %[2]d lines (content) or files (the other modes) are shown unless head_limit says otherwise, and at most %[3]s of text whatever it says. offset skips that many first. When something is left out, a note at the end says so and gives the offset to go on from.

To find files by name, use %[4]s.`,
		strings.Join(left, "; "), o.GrepHeadLimit, byteSize(int64(o.GrepMaxOutputBytes)), o.GlobName)
	return core.ToolSpec{
		Name:        o.GrepName,
		Description: desc,
		InputSchema: schema([]string{"pattern"}, t.props()...),
	}
}

// grepQuery is a Grep call worked out.
type grepQuery struct {
	scanSpec
	pattern  string
	modeName string
	// numbers says whether content lines carry their line numbers.
	numbers bool
	// limit is the most entries to show; zero means no limit. offset is
	// how many to skip first.
	limit, offset int
	// glob, when not nil, is the file filter; globBase says it is compared
	// with the file's name rather than its path, and globNot that it
	// leaves matching files out.
	glob     *wild.Glob
	globText string
	globBase bool
	globNot  bool
	// typ, when not nil, is the kind of file to search.
	typ *fileType
	// root is the file or directory to search.
	root location
}

// usesLookAround reports whether a pattern uses the syntax that Go's
// regular expressions do not have: look-ahead, look-behind, or a
// backreference to a group by its number.
func usesLookAround(pattern string) bool {
	for _, s := range []string{"(?=", "(?!", "(?<=", "(?<!"} {
		if strings.Contains(pattern, s) {
			return true
		}
	}
	for i := 0; i+1 < len(pattern); i++ {
		if pattern[i] != '\\' {
			continue
		}
		if c := pattern[i+1]; c >= '1' && c <= '9' {
			return true
		}
		// A backslash takes the next character with it, whatever it is.
		i++
	}
	return false
}

func (t *grepTool) plan(call core.ToolCall, env *core.ToolEnv) (*grepQuery, *failure) {
	o := t.opts
	a, f := parseArgs(o.GrepName, call.Input, propNames(t.props())...)
	if f != nil {
		return nil, f
	}
	q := &grepQuery{}
	q.searchBinary = o.GrepBinary
	q.maxLine = o.GrepMaxLineBytes
	q.maxColumns = o.GrepMaxColumns
	q.maxWhole = o.GrepMaxMultilineBytes
	if q.pattern, f = a.required("pattern"); f != nil {
		return nil, f
	}
	if q.pattern == "" {
		return nil, failf("%s: pattern is empty.", o.GrepName)
	}

	q.modeName, _, f = a.text("output_mode")
	if f != nil {
		return nil, f
	}
	switch q.modeName {
	case "", "files_with_matches":
		q.mode, q.modeName = modeFiles, "files_with_matches"
	case "content":
		q.mode = modeContent
	case "count":
		q.mode = modeCount
	default:
		return nil, failf("%s: output_mode must be files_with_matches, content or count, not %q.", o.GrepName, short(q.modeName, 60))
	}

	fold, _, f := a.flag("-i")
	if f != nil {
		return nil, f
	}
	if q.multiline, _, f = a.flag("multiline"); f != nil {
		return nil, f
	}
	if q.only, _, f = a.flag("-o"); f != nil {
		return nil, f
	}
	if q.only && q.mode != modeContent {
		return nil, failf("%s: -o shows the text that matched, so it needs output_mode content; this call asked for %s.", o.GrepName, q.modeName)
	}
	numbers, numbersGiven, f := a.flag("-n")
	if f != nil {
		return nil, f
	}
	q.numbers = numbers || !numbersGiven

	around, aroundGiven, f := a.whole("-C")
	if f != nil {
		return nil, f
	}
	if !aroundGiven {
		if around, _, f = a.whole("context"); f != nil {
			return nil, f
		}
	} else if _, _, f = a.whole("context"); f != nil {
		return nil, f
	}
	q.before, q.after = around, around
	if n, given, f := a.whole("-B"); f != nil {
		return nil, f
	} else if given {
		q.before = n
	}
	if n, given, f := a.whole("-A"); f != nil {
		return nil, f
	} else if given {
		q.after = n
	}

	for _, c := range []struct {
		name string
		n    int
	}{{"-B", q.before}, {"-A", q.after}} {
		if c.n > o.GrepMaxContext {
			return nil, failf("%s: %d lines of context is more than the %d a call may ask for (%s, or -C). To see that much of a file, read it with %s.", o.GrepName, c.n, o.GrepMaxContext, c.name, o.ReadName)
		}
	}

	limit, limitGiven, f := a.whole("head_limit")
	if f != nil {
		return nil, f
	}
	q.limit = limit
	if !limitGiven {
		q.limit = o.GrepHeadLimit
	}
	if q.offset, _, f = a.whole("offset"); f != nil {
		return nil, f
	}

	flags := ""
	if fold {
		flags += "i"
	}
	if q.multiline {
		// So that ^ and $ mean what they mean in a search line by line.
		flags += "m"
	}
	expr := q.pattern
	if flags != "" {
		expr = "(?" + flags + ")" + expr
	}
	re, err := regexp.Compile(expr)
	if err != nil {
		why := strings.TrimPrefix(err.Error(), "error parsing regexp: ")
		hint := ""
		if usesLookAround(q.pattern) {
			hint = " Go's regular expressions have no look-ahead, look-behind or backreferences: write the pattern without them, or search for the plain part and check the rest in the lines that come back."
		}
		return nil, failf("%s: the pattern %q is not a regular expression Go accepts: %s.%s", o.GrepName, short(q.pattern, 200), why, hint)
	}
	q.re = re
	if prefix, _ := re.LiteralPrefix(); prefix != "" {
		q.literal = []byte(prefix)
	} else if fold {
		q.fold = foldLiteral(q.pattern, strings.ReplaceAll(flags, "i", ""))
	}

	if q.globText, _, f = a.text("glob"); f != nil {
		return nil, f
	}
	if q.globText != "" {
		text := q.globText
		if strings.HasPrefix(text, "!") {
			q.globNot = true
			text = text[1:]
		}
		text = strings.TrimPrefix(text, "./")
		if strings.Contains(text, "/") {
			text = strings.TrimLeft(text, "/")
		} else {
			q.globBase = true
		}
		if text == "" {
			return nil, failf("%s: glob %q names no files.", o.GrepName, q.globText)
		}
		g, err := wild.CompileGlob(text)
		if err != nil {
			return nil, failf("%s: %v.", o.GrepName, strings.Replace(patternProblem(err, q.globText), "the pattern", "the glob", 1))
		}
		q.glob = g
	}
	typeName, _, f := a.text("type")
	if f != nil {
		return nil, f
	}
	if typeName != "" {
		typ, ok := lookupFileType(typeName)
		if !ok {
			return nil, failf("%s: %q is not a file type it knows. The types are: %s. For anything else, use glob.", o.GrepName, short(typeName, 60), strings.Join(fileTypeNames(), ", "))
		}
		q.typ = typ
	}

	dir, _, f := a.text("path")
	if f != nil {
		return nil, f
	}
	if dir == "" {
		if env == nil || env.Cwd == "" {
			return nil, failf("%s: there is no path and the session has no working directory. Give the file or directory to search in path.", o.GrepName)
		}
		dir = env.Cwd
	}
	if q.root, f = o.locate(env, o.GrepName, "path", dir); f != nil {
		return nil, f
	}
	return q, nil
}

// wants reports whether the glob and type filters let a file through. rel
// is its path below the search directory, with slashes.
func (q *grepQuery) wants(rel string) bool {
	base := rel[strings.LastIndexByte(rel, '/')+1:]
	if q.typ != nil && !q.typ.matches(base) {
		return false
	}
	if q.glob != nil {
		name := rel
		if q.globBase {
			name = base
		}
		if q.glob.Match(name) == q.globNot {
			return false
		}
	}
	return true
}

// filters says which filters the call set, for messages.
func (q *grepQuery) filters() string {
	var parts []string
	if q.glob != nil {
		parts = append(parts, fmt.Sprintf("glob %q", q.globText))
	}
	if q.typ != nil {
		parts = append(parts, fmt.Sprintf("type %q", q.typ.name))
	}
	return strings.Join(parts, " and ")
}

// Describe says where a Grep call would search. It does not say whether
// the path is there.
func (t *grepTool) Describe(call core.ToolCall, env *core.ToolEnv) (core.Action, error) {
	q, f := t.plan(call, env)
	if f != nil {
		return core.Action{}, f
	}
	summary := fmt.Sprintf("Search for %s in %s", short(q.pattern, 200), q.root.abs)
	if fl := q.filters(); fl != "" {
		summary += " (" + fl + ")"
	}
	return core.Action{Kind: core.ActionRead, Paths: q.root.paths(), Summary: summary}, nil
}

// grepHit is a file with a match.
type grepHit struct {
	// path is the file's path as shown: under the search path as written.
	path string
	// real is the path the file is opened by.
	real    string
	modTime time.Time
	// count is the number of matching lines (or of matches, in a multiline
	// search); entries is the number of lines a content result holds.
	count, entries int
}

// grepTally counts what a search looked at.
type grepTally struct {
	seen     atomic.Int64 // files found, before the glob and type filters
	searched atomic.Int64 // files searched
	binary   atomic.Int64 // files passed over as binary
	tooBig   atomic.Int64 // files too large to search whole

	mu         sync.Mutex
	hits       []grepHit
	unreadable int
	firstBad   []string
}

// search searches one file and files what it finds.
func (q *grepQuery) search(ctx context.Context, tally *grepTally, shown, real string) {
	sc, err := q.scan(ctx, real, nil)
	if err != nil {
		if ctx.Err() != nil {
			return
		}
		tally.mu.Lock()
		tally.unreadable++
		if len(tally.firstBad) < 3 {
			tally.firstBad = append(tally.firstBad, fmt.Sprintf("%s (%v)", shown, strings.TrimSuffix(strings.TrimPrefix(underlying(err).Error(), real+" "), ".")))
		}
		tally.mu.Unlock()
		return
	}
	switch {
	case sc.binary:
		tally.binary.Add(1)
		return
	case sc.tooBig:
		tally.tooBig.Add(1)
		return
	}
	tally.searched.Add(1)
	if sc.matched {
		tally.mu.Lock()
		tally.hits = append(tally.hits, grepHit{path: shown, real: real, modTime: sc.modTime, count: sc.count, entries: sc.entries})
		tally.mu.Unlock()
	}
}

// Run searches.
func (t *grepTool) Run(ctx context.Context, call core.ToolCall, env *core.ToolEnv) (core.ToolResult, error) {
	o := t.opts
	q, f := t.plan(call, env)
	if f != nil {
		return refused(f)
	}
	if q.root.trouble != nil {
		return refused(q.root.trouble)
	}
	if !q.root.exists {
		return refused(failf("%s does not exist, so there is nothing to search. Check path.", q.root.abs))
	}
	sctx, cancel := o.searchContext(ctx)
	defer cancel()

	tally := &grepTally{}
	problems := &walkProblems{}
	single := false
	switch mode := q.root.info.Mode(); {
	case mode.IsDir():
		job := &walkJob{
			root:    q.root.real,
			hidden:  o.GrepHidden,
			rules:   o.ignoreRules(o.GrepNoIgnore, q.root.real),
			workers: o.SearchWorkers,
			spread:  true,
			visit: func(f walkedFile) {
				if f.isLink() {
					return
				}
				shown := filepath.Join(q.root.abs, filepath.FromSlash(f.rel))
				if o.denied(shown, f.path) {
					return
				}
				tally.seen.Add(1)
				if q.wants(f.rel) {
					q.search(sctx, tally, shown, f.path)
				}
			},
		}
		if err := walkTree(sctx, job, problems); err != nil {
			return o.searchStopped(ctx, o.GrepName, q.root.abs, err)
		}
	case mode.IsRegular():
		// A file named outright is searched whatever the ignore rules say
		// and whether or not it is hidden.
		single = true
		tally.seen.Add(1)
		if q.wants(filepath.Base(q.root.abs)) {
			q.search(sctx, tally, q.root.abs, q.root.real)
			if err := sctx.Err(); err != nil {
				return o.searchStopped(ctx, o.GrepName, q.root.abs, context.Cause(sctx))
			}
		}
	default:
		return refused(failf("%s is %s, which cannot be searched.", q.root.abs, kindOf(mode)))
	}

	// Files with a match come newest first, the order Claude Code gives;
	// lines and counts come in the order of their files' paths, which does
	// not change while they are paged through with offset (a file written
	// between two pages would move, and lines be shown twice or never).
	hits := tally.hits
	sort.Slice(hits, func(i, j int) bool {
		if q.mode == modeFiles && !hits[i].modTime.Equal(hits[j].modTime) {
			return hits[i].modTime.After(hits[j].modTime)
		}
		return hits[i].path < hits[j].path
	})

	// What was left out of the search, said at the end of any result.
	var notes []string
	if note := problems.note(); note != "" {
		notes = append(notes, note)
	}
	if tally.unreadable > 0 {
		note := fmt.Sprintf("%s could not be read and %s not searched: %s", countOf(tally.unreadable, "file", "files"),
			wasWere(tally.unreadable), strings.Join(tally.firstBad, ", "))
		if tally.unreadable > len(tally.firstBad) {
			note += fmt.Sprintf(", and %d more", tally.unreadable-len(tally.firstBad))
		}
		notes = append(notes, note+".")
	}
	if n := int(tally.tooBig.Load()); n > 0 {
		notes = append(notes, fmt.Sprintf("%s larger than %s %s not searched: a multiline search has to hold a whole file in memory.",
			countOf(n, "file", "files"), byteSize(o.GrepMaxMultilineBytes), wasWere(n)))
	}

	meta := map[string]any{"mode": q.modeName, "numFiles": 0, "filenames": []string{}}
	if q.limit > 0 {
		meta["appliedLimit"] = q.limit
	}
	if q.offset > 0 {
		meta["appliedOffset"] = q.offset
	}
	finish := func(text string) (core.ToolResult, error) {
		if text != "" && !strings.HasSuffix(text, "\n") {
			text += "\n"
		}
		if len(notes) > 0 {
			if text != "" {
				text += "\n"
			}
			for _, note := range notes {
				text += "[" + note + "]\n"
			}
		}
		res := core.TextResult(text)
		res.Meta = meta
		return res, nil
	}

	if len(hits) == 0 {
		return finish(q.noMatches(tally, single))
	}
	switch q.mode {
	case modeContent:
		return q.contentResult(ctx, sctx, o, hits, meta, &notes, finish)
	case modeCount:
		return finish(q.countResult(o, hits, meta, &notes))
	}
	return finish(q.filesResult(o, hits, meta, &notes))
}

func wasWere(n int) string {
	if n == 1 {
		return "was"
	}
	return "were"
}

// noMatches words the result of a search that found nothing, saying how
// much was searched so that "nothing matched" can be told from "nothing
// was looked at".
func (q *grepQuery) noMatches(tally *grepTally, single bool) string {
	searched := int(tally.searched.Load())
	seen := int(tally.seen.Load())
	binary := int(tally.binary.Load())
	if single {
		switch {
		case searched > 0:
			return fmt.Sprintf("No matches found in %s.%s", q.root.abs, q.lineBreakHint())
		case binary > 0:
			return fmt.Sprintf("%s looks like a binary file and was not searched.", q.root.abs)
		case tally.unreadable > 0 || tally.tooBig.Load() > 0:
			return fmt.Sprintf("%s was not searched.", q.root.abs)
		}
		return fmt.Sprintf("%s was not searched: it does not match %s.", q.root.abs, q.filters())
	}
	if searched > 0 {
		return fmt.Sprintf("No matches found. %s searched under %s.%s", countOf(searched, "file was", "files were"), q.root.abs, q.lineBreakHint())
	}
	text := fmt.Sprintf("No matches found: no files were searched under %s.", q.root.abs)
	switch {
	case seen == 0:
		text += " It holds no files, or only files that are hidden or left out by ignore rules."
	case q.filters() != "" && binary == 0:
		text += fmt.Sprintf(" None of the %s there matches %s.", countOf(seen, "file", "files"), q.filters())
	case binary > 0:
		text += fmt.Sprintf(" %s passed over as binary.", countOf(binary, "file was", "files were"))
	}
	return text
}

// lineBreakHint is added to "no matches" when the pattern asks for a line
// break and the search was line by line, where it can never find one.
func (q *grepQuery) lineBreakHint() string {
	if q.multiline || !(strings.Contains(q.pattern, `\n`) || strings.Contains(q.pattern, "\n")) {
		return ""
	}
	return " The pattern has a line break in it, and a match lies within one line unless multiline is true."
}

// page works out which entries of total a call shows: it returns the
// index of the first and how many, before any cut for size.
func (q *grepQuery) page(total int) (first, count int) {
	first = min(q.offset, total)
	count = total - first
	if q.limit > 0 {
		count = min(count, q.limit)
	}
	return first, count
}

// pageNote says what part of a result was shown, when it was not all of
// it. what names the entries: "files", "result lines".
func (q *grepQuery) pageNote(o *Options, what string, first, shown, total int, cutBySize bool) string {
	switch {
	case total == 0:
		return ""
	case shown == 0:
		return fmt.Sprintf("Nothing to show at offset %d: there are only %d %s. Use a smaller offset.", q.offset, total, what)
	case first+shown < total:
		note := fmt.Sprintf("Showing %s %d-%d of %d.", what, first+1, first+shown, total)
		if cutBySize {
			note += fmt.Sprintf(" This call stopped at the %s one call may return.", byteSize(int64(o.GrepMaxOutputBytes)))
		}
		return note + fmt.Sprintf(" To see more, use offset=%d.", first+shown)
	case first > 0:
		return fmt.Sprintf("Showing %s %d-%d of %d: these are the last.", what, first+1, first+shown, total)
	}
	return ""
}

// filesResult lists the files that have a match.
func (q *grepQuery) filesResult(o *Options, hits []grepHit, meta map[string]any, notes *[]string) string {
	first, count := q.page(len(hits))
	var out strings.Builder
	names := []string{}
	cut := false
	for _, h := range hits[first : first+count] {
		if len(names) > 0 && out.Len()+len(h.path)+1 > o.GrepMaxOutputBytes {
			cut = true
			break
		}
		out.WriteString(h.path)
		out.WriteByte('\n')
		names = append(names, h.path)
	}
	if note := q.pageNote(o, "files", first, len(names), len(hits), cut); note != "" {
		*notes = append([]string{note}, *notes...)
	}
	meta["numFiles"] = len(names)
	meta["filenames"] = names
	meta["totalFiles"] = len(hits)
	return out.String()
}

// countResult lists how many lines match in each file, and the total.
func (q *grepQuery) countResult(o *Options, hits []grepHit, meta map[string]any, notes *[]string) string {
	first, count := q.page(len(hits))
	total := 0
	for _, h := range hits {
		total += h.count
	}
	var out strings.Builder
	shown := 0
	cut := false
	for _, h := range hits[first : first+count] {
		line := fmt.Sprintf("%s:%d\n", h.path, h.count)
		if shown > 0 && out.Len()+len(line) > o.GrepMaxOutputBytes {
			cut = true
			break
		}
		out.WriteString(line)
		shown++
	}
	what := countOf(total, "matching line", "matching lines")
	if q.multiline {
		what = countOf(total, "match", "matches")
	}
	if shown > 0 {
		out.WriteByte('\n')
	}
	fmt.Fprintf(&out, "Total: %s in %s.\n", what, countOf(len(hits), "file", "files"))
	if note := q.pageNote(o, "files", first, shown, len(hits), cut); note != "" {
		*notes = append([]string{note}, *notes...)
	}
	meta["numFiles"] = len(hits)
	meta["numMatches"] = total
	return out.String()
}

// contentResult shows the matching lines. The search has only counted
// them; the files whose lines fall on the page are read once more to
// fetch the lines, so that a search with a great many matches never has
// to hold them all.
func (q *grepQuery) contentResult(parent, ctx context.Context, o *Options, hits []grepHit, meta map[string]any, notes *[]string,
	finish func(string) (core.ToolResult, error)) (core.ToolResult, error) {
	total := 0
	for _, h := range hits {
		total += h.entries
	}
	meta["numLines"] = 0
	meta["totalLines"] = total
	if total == 0 {
		// Only -o can come to this: lines matched, but nothing in them
		// did.
		return finish(fmt.Sprintf("The pattern matched in %s, but only empty text, so there is nothing to show.", countOf(len(hits), "file", "files")))
	}
	first, count := q.page(total)
	var out strings.Builder
	shown := 0
	cut := false
	skip := first
	withBreaks := !q.only && (q.before > 0 || q.after > 0)
	for _, h := range hits {
		if shown >= count || cut {
			break
		}
		if skip >= h.entries {
			skip -= h.entries
			continue
		}
		want := min(h.entries-skip, count-shown)
		var lines []grepLine
		passed := 0
		_, err := q.scan(ctx, h.real, func(l grepLine) bool {
			if passed >= skip {
				lines = append(lines, l)
			}
			passed++
			return len(lines) < want
		})
		if ctx.Err() != nil {
			return o.searchStopped(parent, o.GrepName, q.root.abs, context.Cause(ctx))
		}
		if err != nil && len(lines) == 0 {
			// The file went away or changed between the two passes.
			skip = 0
			continue
		}
		skip = 0
		if len(lines) == 0 {
			continue
		}
		head := h.path + "\n"
		if out.Len() > 0 {
			head = "\n" + head
		}
		if shown > 0 && out.Len()+len(head)+len(lines[0].text)+16 > o.GrepMaxOutputBytes {
			cut = true
			break
		}
		out.WriteString(head)
		last := 0
		for i, l := range lines {
			var line strings.Builder
			if withBreaks && i > 0 && l.num > last+1 {
				line.WriteString("--\n")
			}
			if q.numbers {
				if l.match {
					fmt.Fprintf(&line, "%d:", l.num)
				} else {
					fmt.Fprintf(&line, "%d-", l.num)
				}
			}
			line.WriteString(l.text)
			line.WriteByte('\n')
			if shown > 0 && out.Len()+line.Len() > o.GrepMaxOutputBytes {
				cut = true
				break
			}
			out.WriteString(line.String())
			shown++
			last = l.num
		}
	}
	if note := q.pageNote(o, "result lines", first, shown, total, cut); note != "" {
		*notes = append([]string{note}, *notes...)
	}
	meta["numLines"] = shown
	meta["totalLines"] = total
	return finish(out.String())
}
