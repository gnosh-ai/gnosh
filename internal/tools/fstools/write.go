package fstools

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"path/filepath"
	"strings"
	"syscall"
	"unicode/utf8"

	"github.com/gnosh-ai/gnosh/internal/core"
)

// change is a change to one file, worked out in full and not yet made.
// Write and Edit both come down to one: Describe shows it, Run makes it.
type change struct {
	loc location
	// old is the file as it is now, or nil when there is none.
	old fs.FileInfo
	// before is the file's present content. shown says whether it is: the
	// content of a file too large to hold is left out, and shown is false.
	before string
	shown  bool
	// after is the content the file is to have.
	after string
	// same says that the file already holds exactly after.
	same bool
	// summary is one line saying what the change is.
	summary string

	// The rest is Edit's: how many replacements, whether the call asked
	// for every occurrence, the line of the new file each replacement
	// starts at, what they did to the old file's lines, and what the
	// session had seen of the file.
	count      int
	replaceAll bool
	lines      []int
	edits      []lineEdit
	rec        fileRecord
}

// action is the change as a permission check sees it.
func (c *change) action() core.Action {
	return core.Action{Kind: core.ActionEdit, Paths: c.loc.paths(), Summary: c.summary, Preview: c.display()}
}

// display is the change as a person sees it: both sides of a diff, or a
// line of text when the old side is too large to show.
func (c *change) display() *core.Display {
	title := filepath.Base(c.loc.abs)
	if !c.shown {
		return &core.Display{
			Kind:  "text",
			Title: title,
			Path:  c.loc.abs,
			Text:  fmt.Sprintf("%s: its %s are replaced by %s. The old content is too large to show as a diff.", c.loc.abs, byteSize(c.old.Size()), byteSize(int64(len(c.after)))),
		}
	}
	return &core.Display{Kind: "diff", Title: title, Path: c.loc.abs, Before: c.before, After: c.after}
}

// readWhole reads a regular file of at most max bytes. For a larger file
// it returns nil content and no failure. The info is that of the file that
// was opened.
func readWhole(path string, max int64) ([]byte, fs.FileInfo, *failure) {
	f, info, fail := openRegular(path)
	if fail != nil {
		return nil, nil, fail
	}
	defer f.Close()
	if info.Size() > max {
		return nil, info, nil
	}
	data, err := io.ReadAll(io.LimitReader(f, max+1))
	if err != nil {
		return nil, nil, failf("%s could not be read: %v.", path, underlying(err))
	}
	if int64(len(data)) > max {
		return nil, info, nil
	}
	if data == nil {
		data = []byte{}
	}
	return data, info, nil
}

// writable checks that the thing at a location may be replaced by a file:
// it is a regular file, and not reached through a link when links are
// refused.
func (o *Options) writable(tool string, loc *location) *failure {
	if loc.info.IsDir() {
		return failf("%s is a directory. %s writes files; give the path of a file inside it.", loc.abs, tool)
	}
	if !loc.info.Mode().IsRegular() {
		return failf("%s is %s. %s only writes regular files.", loc.abs, kindOf(loc.info.Mode()), tool)
	}
	if f := o.linkRefusal(tool, loc); f != nil {
		return f
	}
	return readOnly(loc)
}

// readOnly reports why the existing file at a location may not be
// written by this process, or nil when it may. Replacing a file by rename
// needs only the directory's leave, so without this check a file marked
// read-only would be changed all the same.
func readOnly(loc *location) *failure {
	err := mayWrite(loc.real)
	switch {
	case err == nil:
		return nil
	case errors.Is(err, fs.ErrPermission):
		return failf("%s may not be written: permission denied (its mode is %s). Nothing was changed.", loc.abs, loc.info.Mode().Perm())
	case errors.Is(err, syscall.EROFS):
		return failf("%s may not be written: it is on a read-only file system. Nothing was changed.", loc.abs)
	}
	// Anything else is for the write itself to run into, or not.
	return nil
}

// linkRefusal is the refusal to write through a symbolic link, when the
// options ask for one.
func (o *Options) linkRefusal(tool string, loc *location) *failure {
	if o.RefuseLinkWrites && isLink(loc.abs) {
		return failf("%s is a symbolic link, and %s does not write through one. Its target is %s: use that path.", loc.abs, tool, loc.real)
	}
	return nil
}

// staleFailure is the refusal to change a file that is not as the session
// last saw it.
func staleFailure(o *Options, loc *location) *failure {
	return failf("%s has changed on disk since this session last read it, so what you know of it is out of date. Nothing was written. Read it again with %s, then make the change.", loc.abs, o.ReadName)
}

// beforeChange tells the harness, when it asked, that the file of a change
// is about to change (Options.BeforeChange).
func (o *Options) beforeChange(env *core.ToolEnv, c *change) *failure {
	if o.BeforeChange == nil {
		return nil
	}
	if err := o.BeforeChange(env, c.loc.real); err != nil {
		return failf("%s was not changed: %v.", c.loc.abs, err)
	}
	return nil
}

// commit makes a change: it writes the file and takes back whatever it
// had made if the write fails. It returns the info of the written file and
// whether it had to be written in place.
func commit(sys *fileOps, c *change) (fs.FileInfo, bool, *failure) {
	var made []string
	if c.old == nil {
		var err error
		if made, err = makeParents(filepath.Dir(c.loc.real)); err != nil {
			return nil, false, failf("%s could not be written: %v. Nothing was written.", c.loc.abs, err)
		}
	}
	info, inPlace, err := sys.replaceFile(c.loc.real, []byte(c.after), c.old)
	if err != nil {
		removeDirs(made)
		return nil, false, failf("%s could not be written: %v.", c.loc.abs, err)
	}
	return info, inPlace, nil
}

// pathArg reads just the file_path of a Write or Edit call and finds
// where it leads. ok is false when the argument or the path cannot be
// used; the tool's plan then says why, in full.
func pathArg(o *Options, tool string, call core.ToolCall, env *core.ToolEnv) (loc location, ok bool) {
	var fields struct {
		Path *string `json:"file_path"`
	}
	if len(bytes.TrimSpace(call.Input)) == 0 || json.Unmarshal(call.Input, &fields) != nil || fields.Path == nil {
		return location{}, false
	}
	loc, f := o.locateFile(env, tool, *fields.Path)
	if f != nil || loc.trouble != nil {
		return location{}, false
	}
	return loc, true
}

// unusable is what Run returns for a call whose path pathArg could not
// use: the failure that plan gives for it.
func unusable(tool string, f *failure) (core.ToolResult, error) {
	if f == nil {
		f = failf("%s: the arguments could not be used.", tool)
	}
	return refused(f)
}

// lockFor waits for the call's turn at a file. It returns a nil unlock and
// an error when the wait was cancelled.
func lockFor(ctx context.Context, env *core.ToolEnv, tool string, loc *location) (func(), error) {
	unlock, err := sessionOf(env).lock(ctx, loc.real)
	if err != nil {
		return nil, fmt.Errorf("%s of %s stopped while waiting for another change to the file: %w", tool, loc.abs, err)
	}
	return unlock, nil
}

// writeTool is the Write tool.
type writeTool struct {
	opts *Options
	sys  *fileOps
}

func (t *writeTool) props() []prop {
	return []prop{
		{name: "file_path", kind: "string", desc: "Path of the file to write. Absolute is best; a relative path is taken from the working directory."},
		{name: "content", kind: "string", desc: "Everything the file is to hold. It replaces whatever the file held before."},
	}
}

// Spec tells the model what Write does.
func (t *writeTool) Spec() core.ToolSpec {
	o := t.opts
	desc := fmt.Sprintf(`Writes a whole file: makes a new one, or replaces everything in one that exists.

The content you give becomes the file, byte for byte; nothing of the old content is kept. Missing directories on the way are made. A replaced file keeps its permissions.

A file that already exists and is not empty must have been shown in full by %[1]s in this session, and must not have changed on disk since, or the call is refused and nothing is written. To change part of a file use %[2]s: it sends less and cannot drop lines by accident.`,
		o.ReadName, o.EditName)
	return core.ToolSpec{
		Name:        o.WriteName,
		Description: desc,
		InputSchema: schema([]string{"file_path", "content"}, t.props()...),
	}
}

// plan works out what a Write call would do. Once the path is known it
// returns a change even alongside a failure, so that a caller can say
// which file the call was about.
func (t *writeTool) plan(call core.ToolCall, env *core.ToolEnv) (*change, *failure) {
	o := t.opts
	a, f := parseArgs(o.WriteName, call.Input, propNames(t.props())...)
	if f != nil {
		return nil, f
	}
	path, f := a.required("file_path")
	if f != nil {
		return nil, f
	}
	content, f := a.required("content")
	if f != nil {
		return nil, f
	}
	loc, f := o.locateFile(env, o.WriteName, path)
	if f != nil {
		return nil, f
	}
	c := &change{loc: loc, after: content, shown: true}
	c.summary = "Write " + loc.abs
	if loc.trouble != nil {
		return c, loc.trouble
	}
	lines := countOf(lineCount(content), "line", "lines")
	if !loc.exists {
		if f := o.linkRefusal(o.WriteName, &loc); f != nil {
			return c, f
		}
		c.summary = fmt.Sprintf("Create %s (%s)", loc.abs, lines)
		return c, nil
	}
	if f := o.writable(o.WriteName, &loc); f != nil {
		return c, f
	}
	c.old = loc.info
	c.summary = fmt.Sprintf("Overwrite %s (%s)", loc.abs, lines)
	if loc.info.Size() == 0 {
		// An empty file has nothing to lose, so it need not have been read.
		c.same = content == ""
		return c, nil
	}
	rec, known := sessionOf(env).record(loc.real)
	if !known {
		return c, failf("%s already exists and this session has not read it. Nothing was written. Read it with %s first; to change only part of it, use %s after that.", loc.abs, o.ReadName, o.EditName)
	}
	before, info, f := readWhole(loc.real, o.DiffMaxBytes)
	if f != nil {
		return c, f
	}
	c.old = info
	if !rec.unchanged(info, before) {
		return c, staleFailure(o, &loc)
	}
	if !rec.all {
		return c, failf("%s would be replaced whole, but this session has only been shown %s. Nothing was written. Read the rest with %s (offset and limit), or change the part you have seen with %s.", loc.abs, rec.seenText(), o.ReadName, o.EditName)
	}
	if before == nil {
		c.shown = false
		return c, nil
	}
	c.before = string(before)
	if !utf8.Valid(before) && strings.Contains(content, replacement) {
		return c, failf("%s is not valid UTF-8, and the new content holds U+FFFD, the mark %s shows in place of bytes it cannot decode. Writing it would make that damage permanent. Nothing was written. Leave those characters out, or change the file with a shell command that keeps its encoding.", loc.abs, o.ReadName)
	}
	c.same = c.before == content
	c.summary = fmt.Sprintf("Overwrite %s (%s, was %s)", loc.abs, lines, countOf(lineCount(c.before), "line", "lines"))
	return c, nil
}

// Describe says what a Write call would do, with the diff it would make.
// When the call would fail, the error comes with an Action that still
// names the paths, so that a permission layer can refuse the path before
// anything about the file is passed on.
func (t *writeTool) Describe(call core.ToolCall, env *core.ToolEnv) (core.Action, error) {
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

// Run writes the file.
func (t *writeTool) Run(ctx context.Context, call core.ToolCall, env *core.ToolEnv) (core.ToolResult, error) {
	o := t.opts
	// Find out which file the call is about, wait for the session's other
	// changes to that file to finish, and only then look at the file: a
	// look taken while another call was halfway through its change would
	// be a look at something that is about to be untrue.
	where, ok := pathArg(o, o.WriteName, call, env)
	if !ok {
		_, f := t.plan(call, env)
		return unusable(o.WriteName, f)
	}
	unlock, err := lockFor(ctx, env, o.WriteName, &where)
	if err != nil {
		return core.ToolResult{}, err
	}
	defer unlock()
	c, f := t.plan(call, env)
	if f != nil {
		return refused(f)
	}
	if c.loc.real != where.real {
		return refused(failf("%s leads somewhere else than it did a moment ago (%s, not %s). Nothing was written. Run the call again if that is what you mean.", c.loc.abs, c.loc.real, where.real))
	}
	if was, moved := t.sys.judgedElsewhere(call.ID, c.loc.real); moved {
		return refused(failf("%s leads somewhere else than when the call was looked at (%s, not %s): a link or a folder on the way changed in between. Nothing was written. Run the call again if that is what you mean.", c.loc.abs, c.loc.real, was))
	}
	if err := ctx.Err(); err != nil {
		return core.ToolResult{}, fmt.Errorf("%s of %s stopped before anything was written: %w", o.WriteName, c.loc.abs, context.Cause(ctx))
	}

	sess := sessionOf(env)
	sum := sha256.Sum256([]byte(c.after))
	lines := lineCount(c.after)
	meta := map[string]any{"type": "update", "filePath": c.loc.abs, "bytes": len(c.after), "lines": lines}
	if c.old == nil {
		meta["type"] = "create"
	}
	if c.loc.real != c.loc.abs {
		meta["realPath"] = c.loc.real
	}
	if c.same {
		// Writing the same bytes again would only stir up whatever watches
		// the file.
		sess.saw(c.loc.real, sighting{info: c.old, sum: &sum, lines: lines, whole: true})
		meta["unchanged"] = true
		res := core.TextResult(fmt.Sprintf("%s already holds exactly this content%s. Nothing was written.", c.loc.abs, c.loc.viaLink()))
		res.Display = c.display()
		res.Meta = meta
		return res, nil
	}
	if f := o.beforeChange(env, c); f != nil {
		return refused(f)
	}
	info, inPlace, f := commit(t.sys, c)
	if f != nil {
		return refused(f)
	}
	sess.wrote(c.loc.real, info, sum, true, nil, lines)

	var text string
	size := fmt.Sprintf("%s, %s", countOf(lines, "line", "lines"), byteSize(int64(len(c.after))))
	switch {
	case c.old == nil:
		text = fmt.Sprintf("Created %s%s (%s).", c.loc.abs, c.loc.viaLink(), size)
	case c.shown:
		text = fmt.Sprintf("Wrote %s%s (%s), replacing its %s.", c.loc.abs, c.loc.viaLink(), size, countOf(lineCount(c.before), "line", "lines"))
	default:
		text = fmt.Sprintf("Wrote %s%s (%s), replacing its %s.", c.loc.abs, c.loc.viaLink(), size, byteSize(c.old.Size()))
	}
	if inPlace {
		text += " The file was overwritten where it is, because its directory did not allow replacing it in one step."
		meta["inPlace"] = true
	}
	res := core.TextResult(text)
	res.Display = c.display()
	res.Meta = meta
	return res, nil
}
