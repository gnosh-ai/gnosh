package fstools

import (
	"runtime"
	"time"

	"github.com/gnosh-ai/gnosh/internal/core"
)

// Options holds every name and every limit of the file tools, so that each
// can be changed in one place. A field left at its zero value takes the
// default written beside it; Defaults returns them all filled in. A limit
// set below one takes its default too, except where a field says what a
// negative value means.
type Options struct {
	// ReadName, WriteName, EditName, GlobName and GrepName are the names
	// the model calls the tools by, which are also the names permission
	// rules and hooks use. Defaults: "Read", "Write", "Edit", "Glob",
	// "Grep".
	ReadName  string
	WriteName string
	EditName  string
	GlobName  string
	GrepName  string

	// HomeDir is what a leading "~/" in a path argument stands for. When
	// it is empty, which is the default, a path that starts with "~" is
	// refused with a message saying so. The tools never read the
	// environment to find a home directory themselves.
	HomeDir string

	// ReadDefaultLines is how many lines Read shows when the call gives no
	// limit. Default 2000.
	ReadDefaultLines int

	// ReadMaxBytes is the most file text, in bytes, one Read call shows,
	// whatever limit the call asks for. Default 100000, which is about
	// 25,000 tokens.
	ReadMaxBytes int

	// ReadMaxLineChars is the longest line, in characters, Read shows
	// whole. A longer line is cut there and marked. Default 2000.
	ReadMaxLineChars int

	// ReadCountBytes is how far past the end of the page it shows Read
	// keeps reading in order to count the file's lines. A file that goes
	// on beyond that is reported without a line count. Default 64 MiB; a
	// negative value means never read past the page.
	ReadCountBytes int64

	// ReadMaxImageBytes is the largest image file Read returns as a
	// picture. Default 5 MiB. Read does not shrink images.
	ReadMaxImageBytes int64

	// EditMaxBytes is the largest file Edit changes. Edit holds the whole
	// file in memory, twice. Default 32 MiB.
	EditMaxBytes int64

	// DiffMaxBytes is the largest existing file whose old content Write
	// loads to show as the "before" side of its diff. Over it, Write shows
	// a line of text in place of a diff. Default 10 MiB.
	DiffMaxBytes int64

	// EditContextLines is how many lines around a change Edit shows the
	// model in its result. Default 3; a negative value shows none.
	EditContextLines int

	// BeforeChange, when set, is called by Write and Edit just before they
	// change a file, with the call's environment and the file's real path:
	// the file is then as it was, and locked against the session's other
	// changes to it. A harness that keeps what files held, to put them back
	// later, reads the file there. An error stops the change: nothing is
	// written, and the model reads why.
	BeforeChange func(env *core.ToolEnv, path string) error

	// RefuseLinkWrites makes Write and Edit refuse a path that is itself a
	// symbolic link and name the link's target instead, as Claude Code
	// does. By default they write the file the link leads to and leave the
	// link alone.
	RefuseLinkWrites bool

	// ReadDenied, when set, reports whether a Read deny rule covers a file,
	// given its absolute path as written or as reached. Glob and Grep leave
	// out what it covers in a folder they walk: its name is not listed and
	// its content is not searched. (The folder itself is judged by the gate,
	// before the walk.)
	ReadDenied func(path string) bool

	// GlobMaxResults is the most files Glob lists. Default 100.
	GlobMaxResults int

	// GlobNoIgnore makes Glob list files that ignore rules leave out. By
	// default Glob honours the rules.
	GlobNoIgnore bool

	// GlobSkipHidden makes Glob pass over files and directories whose
	// names start with a dot. By default Glob lists them.
	GlobSkipHidden bool

	// GrepNoIgnore makes Grep search files that ignore rules leave out.
	GrepNoIgnore bool

	// GrepHidden makes Grep search files and directories whose names start
	// with a dot. By default it passes over them.
	GrepHidden bool

	// GrepBinary makes Grep search files that look binary. By default it
	// passes over them.
	GrepBinary bool

	// GrepHeadLimit is how many lines or entries Grep shows when the call
	// gives no head_limit. Default 250; a negative value means no limit
	// unless the call sets one.
	GrepHeadLimit int

	// GrepMaxContext is the most lines of context (-A, -B, -C) a Grep call
	// may ask for around each match. More is refused, with a pointer to
	// Read. Default 1000.
	GrepMaxContext int

	// GrepMaxOutputBytes is the most text one Grep call returns, whatever
	// head_limit says. Default 40 KiB.
	GrepMaxOutputBytes int

	// GrepMaxColumns is the most bytes of one line Grep shows. A longer
	// line is cut around its first match. Default 500.
	GrepMaxColumns int

	// GrepMaxLineBytes is the longest line Grep matches in one piece. A
	// longer line is matched in pieces of this size, so a match that lies
	// across two pieces is missed. Default 1 MiB.
	GrepMaxLineBytes int

	// GrepMaxMultilineBytes is the largest file Grep searches with
	// multiline set, when it has to hold the whole file in memory. Larger
	// files are passed over and the result says how many. Default 32 MiB.
	GrepMaxMultilineBytes int64

	// SearchTimeout is how long one Glob or Grep call may take. A call
	// that runs out of time fails with a message that says so; it never
	// returns part of an answer. Default 20 seconds; a negative value
	// means no limit beyond the caller's context.
	SearchTimeout time.Duration

	// SearchWorkers is how many goroutines Glob and Grep search with.
	// Default: the number of CPUs Go may use, but no more than 8.
	SearchWorkers int

	// IgnoreFiles are the names of the ignore files Glob and Grep read in
	// every directory, in rising order of weight. Default: .gitignore
	// alone. An empty, non-nil list reads none.
	IgnoreFiles []string

	// NoGitExclude stops Glob and Grep reading a repository's
	// .git/info/exclude.
	NoGitExclude bool
}

// Defaults returns the Options with every default filled in.
func Defaults() Options {
	return Options{}.withDefaults()
}

func (o Options) withDefaults() Options {
	text := func(v *string, d string) {
		if *v == "" {
			*v = d
		}
	}
	// A limit below one would mean "show nothing", which is never what a
	// caller wants; it takes the default like a zero does. The few fields
	// where a negative value has a meaning of its own say so above and
	// are set apart below.
	num := func(v *int, d int) {
		if *v <= 0 {
			*v = d
		}
	}
	big := func(v *int64, d int64) {
		if *v <= 0 {
			*v = d
		}
	}
	text(&o.ReadName, "Read")
	text(&o.WriteName, "Write")
	text(&o.EditName, "Edit")
	text(&o.GlobName, "Glob")
	text(&o.GrepName, "Grep")
	num(&o.ReadDefaultLines, 2000)
	num(&o.ReadMaxBytes, 100_000)
	num(&o.ReadMaxLineChars, 2000)
	if o.ReadCountBytes == 0 {
		o.ReadCountBytes = 64 << 20
	}
	big(&o.ReadMaxImageBytes, 5<<20)
	big(&o.EditMaxBytes, 32<<20)
	big(&o.DiffMaxBytes, 10<<20)
	if o.EditContextLines == 0 {
		o.EditContextLines = 3
	}
	num(&o.GlobMaxResults, 100)
	switch {
	case o.GrepHeadLimit == 0:
		o.GrepHeadLimit = 250
	case o.GrepHeadLimit < 0:
		// No limit unless a call sets one.
		o.GrepHeadLimit = 0
	}
	num(&o.GrepMaxContext, 1000)
	num(&o.GrepMaxOutputBytes, 40<<10)
	num(&o.GrepMaxColumns, 500)
	num(&o.GrepMaxLineBytes, 1<<20)
	big(&o.GrepMaxMultilineBytes, 32<<20)
	if o.SearchTimeout == 0 {
		o.SearchTimeout = 20 * time.Second
	}
	if o.SearchWorkers <= 0 {
		o.SearchWorkers = min(runtime.GOMAXPROCS(0), 8)
	}
	if o.IgnoreFiles == nil {
		o.IgnoreFiles = []string{".gitignore"}
	}
	return o
}

// New returns the five file tools, in the order Read, Write, Edit, Glob,
// Grep, set up with opts. Each of them is a core.Tool and a
// core.Describer. The tools keep nothing between calls themselves: what a
// session has read and written lives in the core.ToolEnv.Store they are
// handed, so one set of tools serves any number of sessions.
func New(opts Options) []core.Tool {
	o := opts.withDefaults()
	sys := newFileOps()
	return []core.Tool{
		&readTool{opts: &o},
		&writeTool{opts: &o, sys: sys},
		&editTool{opts: &o, sys: sys},
		&globTool{opts: &o},
		&grepTool{opts: &o},
	}
}

// denied reports whether ReadDenied covers one of the paths of a file.
func (o *Options) denied(paths ...string) bool {
	if o.ReadDenied == nil {
		return false
	}
	for _, p := range paths {
		if p != "" && o.ReadDenied(p) {
			return true
		}
	}
	return false
}
