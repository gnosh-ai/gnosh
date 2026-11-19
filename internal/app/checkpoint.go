package app

import (
	"bufio"
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gnosh-ai/gnosh/internal/agent"
	"github.com/gnosh-ai/gnosh/internal/core"
	"github.com/gnosh-ai/gnosh/internal/transcript"
)

// This file keeps what the files the model changes held, so that a rewind
// can put them back (spec/cc/sessions.md, section 5).
//
// Before Write or Edit first changes a file after one of the session's
// prompts, the file's content then is copied into the session's own folder,
// under that prompt. A rewind to before a prompt puts back every file that
// was changed from that prompt on, as it was just before the first of those
// changes: a file that was not there is taken away again. What shell
// commands change is not kept, as in Claude Code: only the file tools say
// what they are about to change. What a file holds when it is put back or
// taken away, if anything changed it since, is kept beside the copies
// first, so that a rewind never loses work (DECISIONS 143).
//
// The copies are written beside the session's file and read again when the
// session is resumed, so a rewind after a resume can put files back too. A
// session branched from another (/branch, /fork, --fork-session) finds the
// copies kept under the prompts it took over in the folder of the session
// they came from.

// checkpointMax is the largest file whose content is kept. A larger one is
// noted, and a rewind says it cannot put it back.
const checkpointMax = 32 << 20

// checkpointPrompts is how many of the most recent prompts' copies a
// session keeps, as Claude Code keeps those of its 100 most recent
// checkpoints. The copies of older prompts are let go, and a rewind of the
// files to before one of those says it cannot be done.
const checkpointPrompts = 100

// fileCheckpoint is one file as it was before its first change after one
// prompt.
type fileCheckpoint struct {
	// Prompt is the session-file entry of the prompt the change came
	// after, and Path the file's real path.
	Prompt string `json:"prompt"`
	Path   string `json:"path"`
	// Existed says there was a file; Blob is the name of the copy of its
	// content, Mode its permissions and Size its size. Large says it was
	// too large to keep, and Failed that it could not be kept.
	Existed bool        `json:"existed"`
	Blob    string      `json:"blob,omitempty"`
	Mode    fs.FileMode `json:"mode,omitempty"`
	Size    int64       `json:"size,omitempty"`
	Large   bool        `json:"large,omitempty"`
	Failed  bool        `json:"failed,omitempty"`
	Time    time.Time   `json:"time"`
	// dir is the folder that holds the copy: this session's, or that of
	// the session a branch took the prompt over from.
	dir string
}

// checkpoints is the session's store of them.
type checkpoints struct {
	mu   sync.Mutex
	dir  string
	now  func() time.Time
	list []fileCheckpoint
	// kept says which file was kept after which prompt already.
	kept map[string]bool
	// taken are the copies of the sessions this one was branched from,
	// by their folder, read when first needed.
	taken map[string][]fileCheckpoint
	// pruned are the prompts whose copies were let go, here and in the
	// sessions this one was branched from.
	pruned map[string]bool
}

// openCheckpoints opens the store in dir, reading what an earlier run of
// the session kept there. A store that cannot be read is an empty one.
func openCheckpoints(dir string, now func() time.Time) *checkpoints {
	c := &checkpoints{dir: dir, now: now, kept: map[string]bool{}, taken: map[string][]fileCheckpoint{}, pruned: map[string]bool{}}
	c.list = readCheckpoints(dir)
	for _, cp := range c.list {
		c.kept[cp.Prompt+"\x00"+cp.Path] = true
	}
	readPruned(dir, c.pruned)
	return c
}

// readPruned adds the prompts whose copies were let go in dir to set.
func readPruned(dir string, set map[string]bool) {
	data, err := os.ReadFile(filepath.Join(dir, "pruned"))
	if err != nil {
		return
	}
	for _, line := range strings.Fields(string(data)) {
		set[line] = true
	}
}

// readCheckpoints reads the index of the copies kept in dir.
func readCheckpoints(dir string) []fileCheckpoint {
	f, err := os.Open(filepath.Join(dir, "index.jsonl"))
	if err != nil {
		return nil
	}
	defer f.Close()
	var out []fileCheckpoint
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 64<<10), 1<<20)
	for sc.Scan() {
		var cp fileCheckpoint
		if json.Unmarshal(sc.Bytes(), &cp) != nil || cp.Prompt == "" || !filepath.IsAbs(cp.Path) ||
			cp.Blob != "" && (cp.Blob != filepath.Base(cp.Blob) || cp.Blob == "." || cp.Blob == "..") {
			// (A copy is a file of the folder, kept for a file named in
			// full: an entry that says otherwise was not written here.)
			continue
		}
		cp.dir = dir
		out = append(out, cp)
	}
	return out
}

// keep copies the file at path as it is now, under the prompt, unless it
// was kept under that prompt already. A copy that cannot be made is noted
// all the same, as a file that cannot be put back: a later change of the
// file must not be taken for what it held before the first.
func (c *checkpoints) keep(prompt, path string) error {
	if prompt == "" {
		return nil
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	key := prompt + "\x00" + path
	if c.kept[key] {
		return nil
	}
	cp := fileCheckpoint{Prompt: prompt, Path: path, Time: c.now(), dir: c.dir}
	info, err := os.Lstat(path)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		err = nil
	case err != nil:
	case !info.Mode().IsRegular():
		// Write and Edit change regular files only.
		return nil
	case info.Size() > checkpointMax:
		cp.Existed, cp.Large, cp.Size = true, true, info.Size()
	default:
		cp.Existed, cp.Mode = true, info.Mode().Perm()
		var data []byte
		if data, err = os.ReadFile(path); err == nil {
			cp.Size = int64(len(data))
			cp.Blob, err = c.write("", filepath.Base(path), data)
		}
	}
	if err != nil {
		cp.Failed, cp.Blob = true, ""
	}
	fresh := !slices.ContainsFunc(c.list, func(o fileCheckpoint) bool { return o.Prompt == prompt })
	c.list = append(c.list, cp)
	c.kept[key] = true
	if ierr := c.index(cp); err == nil {
		err = ierr
	}
	if fresh {
		if perr := c.prune(); err == nil {
			err = perr
		}
	}
	return err
}

// prune lets go of the copies of all but the checkpointPrompts most recent
// prompts: their files are removed, the index is written again without
// them, and the prompts are noted in the file "pruned". The caller holds
// the lock.
func (c *checkpoints) prune() error {
	var order []string
	seen := map[string]bool{}
	for _, cp := range c.list {
		if !seen[cp.Prompt] {
			seen[cp.Prompt] = true
			order = append(order, cp.Prompt)
		}
	}
	if len(order) <= checkpointPrompts {
		return nil
	}
	drop := map[string]bool{}
	for _, p := range order[:len(order)-checkpointPrompts] {
		drop[p] = true
	}
	var keep []fileCheckpoint
	var lines []byte
	for _, cp := range c.list {
		if !drop[cp.Prompt] {
			keep = append(keep, cp)
			line, err := json.Marshal(cp)
			if err != nil {
				return err
			}
			lines = append(append(lines, line...), '\n')
		}
	}
	// The prompts are noted first: a rewind must never take what is left
	// for all there was.
	var names []byte
	for p := range drop {
		names = append(append(names, p...), '\n')
	}
	f, err := os.OpenFile(filepath.Join(c.dir, "pruned"), os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0o600)
	if err != nil {
		return err
	}
	_, werr := f.Write(names)
	if cerr := f.Close(); werr == nil {
		werr = cerr
	}
	if werr != nil {
		return werr
	}
	for p := range drop {
		c.pruned[p] = true
	}
	if err := putBack(filepath.Join(c.dir, "index.jsonl"), lines, 0o600); err != nil {
		return err
	}
	for _, cp := range c.list {
		if drop[cp.Prompt] && cp.Blob != "" {
			_ = os.Remove(filepath.Join(c.dir, cp.Blob))
		}
	}
	c.list = keep
	return nil
}

// gone reports whether the copies of one of the prompts were let go.
func (c *checkpoints) gone(prompts []string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return slices.ContainsFunc(prompts, func(p string) bool { return c.pruned[p] })
}

// write puts data into a new file of the store, in its folder sub, and
// returns the file's name there. Each name is new: a name made from a count
// could be given twice when the index lost a line.
func (c *checkpoints) write(sub, base string, data []byte) (string, error) {
	dir := filepath.Join(c.dir, sub)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	var r [4]byte
	_, _ = rand.Read(r[:])
	name := strconv.FormatInt(c.now().UnixNano(), 36) + "-" + hex.EncodeToString(r[:])
	if sub != "" {
		name += "-" + base
	}
	f, err := os.OpenFile(filepath.Join(dir, name), os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return "", err
	}
	_, werr := f.Write(data)
	if cerr := f.Close(); werr == nil {
		werr = cerr
	}
	if werr != nil {
		_ = os.Remove(filepath.Join(dir, name))
		return "", werr
	}
	return name, nil
}

// index adds cp to the store's index file.
func (c *checkpoints) index(cp fileCheckpoint) error {
	line, err := json.Marshal(cp)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(c.dir, 0o700); err != nil {
		return err
	}
	f, err := os.OpenFile(filepath.Join(c.dir, "index.jsonl"), os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0o600)
	if err != nil {
		return err
	}
	_, werr := f.Write(append(line, '\n'))
	if cerr := f.Close(); werr == nil {
		werr = cerr
	}
	return werr
}

// inherit makes the copies kept in another session's folder part of what
// this store finds: those of a session this one was branched from.
func (c *checkpoints) inherit(dir string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if dir == c.dir {
		return
	}
	if _, ok := c.taken[dir]; !ok {
		c.taken[dir] = readCheckpoints(dir)
		readPruned(dir, c.pruned)
	}
}

// since returns, for every file changed after one of the prompts, what it
// held before the first of those changes: the oldest copy taken under one
// of them. The files come in the order of their paths.
func (c *checkpoints) since(prompts map[string]bool) []fileCheckpoint {
	c.mu.Lock()
	all := slices.Clone(c.list)
	for _, cps := range c.taken {
		all = append(all, cps...)
	}
	c.mu.Unlock()
	sort.SliceStable(all, func(i, j int) bool { return all[i].Time.Before(all[j].Time) })
	first := map[string]fileCheckpoint{}
	for _, cp := range all {
		if !prompts[cp.Prompt] {
			continue
		}
		if _, ok := first[cp.Path]; !ok {
			first[cp.Path] = cp
		}
	}
	out := make([]fileCheckpoint, 0, len(first))
	for _, cp := range first {
		out = append(out, cp)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Path < out[j].Path })
	return out
}

// restore puts each file back as its copy has it: the content it had, or
// no file where there was none. What a file holds now, when that is not
// what is put back, is kept first, in the store's folder "replaced", and
// what was put back says where. done lists what was put back, and failed
// what could not be, each with why; show writes a path for the person.
func (c *checkpoints) restore(cps []fileCheckpoint, show func(string) string) (done, failed []string) {
	if show == nil {
		show = func(p string) string { return p }
	}
	for _, cp := range cps {
		path := show(cp.Path)
		if cp.Failed {
			failed = append(failed, path+": what it held before could not be kept, so it cannot be put back")
			continue
		}
		if cp.Large {
			failed = append(failed, fmt.Sprintf("%s: it was too large to keep (%s)", path, byteCount(cp.Size)))
			continue
		}
		var want []byte
		if cp.Existed {
			var err error
			if want, err = os.ReadFile(filepath.Join(cp.dir, cp.Blob)); err != nil {
				failed = append(failed, fmt.Sprintf("%s: its copy cannot be read (%v)", path, err))
				continue
			}
		}
		info, err := os.Lstat(cp.Path)
		there := err == nil
		switch {
		case err != nil && !errors.Is(err, fs.ErrNotExist):
			failed = append(failed, fmt.Sprintf("%s: %v", path, err))
			continue
		case there && !info.Mode().IsRegular():
			failed = append(failed, path+": something other than a file is there now, and is left alone")
			continue
		case there && hardLinked(cp.Path):
			// Put back by a new file in its place, it would no longer be
			// the file its other names (as pnpm makes them) stand for.
			failed = append(failed, path+": it is hard-linked under another name, which putting it back would cut it off from, so it is left as it is")
			continue
		}
		saved := ""
		if there {
			now, err := os.ReadFile(cp.Path)
			if err != nil {
				failed = append(failed, fmt.Sprintf("%s: %v", path, err))
				continue
			}
			if !cp.Existed || !bytes.Equal(now, want) {
				name, err := c.write("replaced", filepath.Base(cp.Path), now)
				if err != nil {
					failed = append(failed, fmt.Sprintf("%s: what it holds now could not be kept first (%v), so it is left as it is", path, err))
					continue
				}
				saved = show(filepath.Join(c.dir, "replaced", name))
			}
		}
		kept := ""
		if saved != "" {
			kept = "; what it held is kept at " + saved
		}
		switch {
		case !cp.Existed && !there:
			done = append(done, path+" (was not there, and is not)")
		case !cp.Existed:
			if err := os.Remove(cp.Path); err != nil {
				failed = append(failed, fmt.Sprintf("%s: %v", path, err))
			} else {
				done = append(done, path+" (taken away: it was not there"+kept+")")
			}
		case saved == "" && there:
			done = append(done, path+" (it held that already)")
		default:
			if err := putBack(cp.Path, want, cp.Mode); err != nil {
				failed = append(failed, fmt.Sprintf("%s: %v", path, err))
			} else if kept != "" {
				done = append(done, path+" ("+strings.TrimPrefix(kept, "; ")+")")
			} else {
				done = append(done, path)
			}
		}
	}
	return done, failed
}

// putBack writes data as the file at path, whole or not at all: into a
// new file beside it, which then takes its place.
func putBack(path string, data []byte, mode fs.FileMode) error {
	if mode == 0 {
		mode = 0o644
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".gnosh-")
	if err != nil {
		return err
	}
	tmp := f.Name()
	_, werr := f.Write(data)
	if werr == nil {
		werr = f.Chmod(mode)
	}
	if cerr := f.Close(); werr == nil {
		werr = cerr
	}
	if werr == nil {
		werr = os.Rename(tmp, path)
	}
	if werr != nil {
		_ = os.Remove(tmp)
	}
	return werr
}

// keepFile is the file tools' BeforeChange: a file is about to change,
// after the newest prompt. A sub-agent's changes are not kept, as in
// Claude Code: they are its own, in its own work tree when it has one, and
// a rewind of the session's conversation does not take them back. A copy
// that cannot be made does not stop the change; the person is told once
// that files may not all be put back.
func (l *live) keepFile(env *core.ToolEnv, path string) error {
	if l.checks == nil || l.log == nil || env != nil && env.ParentSessionID != "" || !l.cfg.Options.FileCheckpointingEnabled.V {
		return nil
	}
	if err := l.checks.keep(l.log.lastPrompt(), path); err != nil {
		l.flog.logf("tool", "what %s held could not be kept for a rewind: %v", path, err)
		l.checkWarn.Do(func() {
			l.notice(agent.LevelWarning, "rewind", fmt.Sprintf("What %s held could not be kept (%v), so a rewind cannot put it back, nor perhaps other files.", l.tilde(path), err))
		})
	}
	return nil
}

// promptsInOrder lists the session-file entries of the person's prompts in
// the order of the file, those a rewind took out of the conversation
// among them: their changes were made to the files all the same. It also
// makes the store find the copies of the sessions this one was branched
// from, whose prompts it took over.
func (l *live) promptsInOrder() []string {
	var out []string
	own := l.ts.ID()
	dir := filepath.Dir(l.ts.Path())
	entries := l.ts.Entries()
	for i := range entries {
		e := &entries[i]
		for _, ae := range decode(e) {
			if ae.Kind != agent.EntryPrompt {
				continue
			}
			out = append(out, e.UUID)
			if l.checks != nil && transcript.ValidID(e.SessionID) && e.SessionID != own {
				l.checks.inherit(filepath.Join(dir, e.SessionID, "checkpoints"))
			}
			break
		}
	}
	return out
}

// filesSince returns what a rewind to before the prompt in the session's
// file entry point would put back, given the prompts in the file's order:
// every file changed from that prompt on. gone says the copies of one of
// those prompts were let go, so the files cannot be put back as they were
// then, and nothing is offered.
func (l *live) filesSince(order []string, point string) (cps []fileCheckpoint, gone bool) {
	if l.checks == nil {
		return nil, false
	}
	at := slices.Index(order, point)
	if at < 0 {
		return nil, false
	}
	if l.checks.gone(order[at:]) {
		return nil, true
	}
	prompts := map[string]bool{}
	for _, p := range order[at:] {
		prompts[p] = true
	}
	return l.checks.since(prompts), false
}

// rewindFiles returns what a rewind to before the prompt in the session's
// file entry point would put back, and an error that says why none can be.
func (l *live) rewindFiles(point string) ([]fileCheckpoint, error) {
	cps, gone := l.filesSince(l.promptsInOrder(), point)
	if gone {
		return nil, errFilesGone
	}
	if !l.cfg.Options.FileCheckpointingEnabled.V && len(cps) == 0 {
		return nil, errNoCheckpoints
	}
	return cps, nil
}

// errFilesGone and errNoCheckpoints say why a rewind cannot put files back.
var (
	errFilesGone     = fmt.Errorf("the copies of what the files held before that prompt were let go: gnosh keeps those of your %d most recent prompts", checkpointPrompts)
	errNoCheckpoints = errors.New("what Write and Edit change is not kept here (fileCheckpointingEnabled is false), so no file can be put back")
)

// showPath writes a path for the person: relative to the working
// directory when it lies under it, else with ~ for the home directory.
func (l *live) showPath(p string) string {
	if l.log != nil {
		if s := l.log.shownFile(p); !filepath.IsAbs(s) {
			return s
		}
	}
	return l.tilde(p)
}

// filePaths lists the paths of checkpoints.
func filePaths(cps []fileCheckpoint) []string {
	out := make([]string, len(cps))
	for i, cp := range cps {
		out[i] = cp.Path
	}
	return out
}

// restoreSaid is what a person is told of files put back.
func restoreSaid(done, failed []string) string {
	var b strings.Builder
	switch len(done) {
	case 0:
	case 1:
		b.WriteString("Put back: " + done[0] + ".")
	default:
		b.WriteString(fmt.Sprintf("Put back %d files: %s.", len(done), strings.Join(done, ", ")))
	}
	if len(failed) > 0 {
		if b.Len() > 0 {
			b.WriteString("\n")
		}
		b.WriteString("Not put back: " + strings.Join(failed, "; ") + ".")
	}
	if b.Len() == 0 {
		return "No file was changed by Write or Edit since then, so there was nothing to put back."
	}
	return b.String()
}
