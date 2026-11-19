package app

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/gnosh-ai/gnosh/internal/agent"
	"github.com/gnosh-ai/gnosh/internal/clipboard"
	"github.com/gnosh-ai/gnosh/internal/core"
	"github.com/gnosh-ai/gnosh/internal/permission"
	"github.com/gnosh-ai/gnosh/internal/prompt"
	"github.com/gnosh-ai/gnosh/internal/tools/shelltool"
	"github.com/gnosh-ai/gnosh/internal/transcript"
)

// This file holds the slash commands that follow Claude Code's own of the
// same names (spec/cc/slash-commands-and-skills.md): /plan, /add-dir,
// /tasks, /cost, /agents, /memory, /init, /export and /diff.

// slashPlan is /plan [task]: plan mode, and the task, if one is given, as
// the prompt that starts it.
func (l *live) slashPlan(ctx context.Context, c *SlashContext) error {
	if err := l.setMode(permission.ModePlan); err != nil {
		return fmt.Errorf("plan mode could not be switched on: %w", err)
	}
	task := strings.TrimSpace(c.Args)
	if task == "" {
		c.Say("Plan mode is on: the model reads and plans, and changes nothing until you switch out of it (Shift+Tab, or /permissions).")
		return nil
	}
	c.Say("Plan mode is on, and the model plans the task.")
	return c.Send(agent.Prompt{Text: task})
}

// slashAddDir is /add-dir <directory>: one more working directory for the
// rest of the session, as --add-dir is for the whole of one. Its files are
// read and changed as the project's are, under the same mode and rules.
// With nothing after it, it lists the working directories.
func (l *live) slashAddDir(ctx context.Context, c *SlashContext) error {
	arg := strings.TrimSpace(c.Args)
	if arg == "" {
		c.Say("The working directories of this session:")
		for i, d := range l.roots {
			label := "added"
			if i == 0 {
				label = "the project"
			}
			c.Say("  %s (%s)", d, label)
		}
		c.Say("/add-dir <directory> adds one for the rest of the session.")
		return nil
	}
	dir := arg
	switch {
	case dir == "~":
		dir = l.cfg.Paths.Home
	case strings.HasPrefix(dir, "~/"):
		dir = filepath.Join(l.cfg.Paths.Home, dir[2:])
	case !filepath.IsAbs(dir):
		dir = filepath.Join(l.cwd(), dir)
	}
	dir = filepath.Clean(dir)
	info, err := os.Stat(dir)
	switch {
	case errors.Is(err, os.ErrNotExist):
		return fmt.Errorf("%s does not exist; /add-dir takes a directory that is there", dir)
	case err != nil:
		return fmt.Errorf("%s cannot be looked at: %v", dir, err)
	case !info.IsDir():
		return fmt.Errorf("%s is a file; /add-dir takes a directory", dir)
	}
	for _, d := range l.roots {
		if d == dir {
			c.Say("%s is a working directory of this session already.", dir)
			return nil
		}
	}
	if err := l.engine.AddDir(dir); err != nil {
		return err
	}
	l.roots = append(slices.Clone(l.roots), dir)
	l.addedDirs = append(l.addedDirs, dir)
	l.agent.SetRoots(l.roots)
	l.flog.logf("permission", "a working directory was added for the rest of the session: %s", dir)
	// The model is told with the next request, so that it knows it may work
	// there.
	_ = l.agent.Tell(agent.Note{Source: "add-dir",
		Text: prompt.Wrap(fmt.Sprintf("The user added %s as a working directory for the rest of this session: its files may be read and changed as the project's are, under the same permission mode and rules.", dir))})
	c.Say("%s is a working directory for the rest of this session: its files are read and changed as the project's are, under the same mode and rules. --add-dir adds one for a whole session.", dir)
	if note := untrustedRootNote(dir); note != "" && l.st.trusted && !l.rootTrusted(dir) {
		c.Say("%s", note)
	}
	l.directoryAdded(ctx, dir)
	return nil
}

// endedWords says how a sub-agent's last run ended, for /tasks.
var endedWords = map[string]string{
	"completed": "done", "partial": "ended part way", "failed": "failed", "halted": "ended for good", "stopped": "stopped",
}

// slashTasks is /tasks (also /bashes): the commands that run, or ran, in
// the background in this session, and "/tasks stop <id>" to stop one.
func (l *live) slashTasks(ctx context.Context, c *SlashContext) error {
	agents := l.backgroundAgents()
	args := strings.Fields(c.Args)
	switch {
	case len(args) == 2 && args[0] == "stop":
		if b := l.findBackgroundAgent(args[1]); b != nil {
			l.stopBackgroundAgent(b)
			c.Say("The background sub-agent %s is stopped: %s", b.id, oneLine(b.desc, 100))
			return nil
		}
		if l.shell != nil {
			if t, err := l.shell.Stop(ctx, args[1]); err == nil {
				c.Say("%s is stopped: %s", t.ID, oneLine(t.Command, 100))
				return nil
			}
		}
		// A session in the background, this folder's or another's, as
		// gnosh stop stops one. The wait for it to end goes on beside the
		// session.
		if dir := l.crashDir(); dir != "" {
			id, log, err := findRun([]string{dir}, args[1])
			if err != nil && strings.HasPrefix(err.Error(), "no session ") {
				id, log, err = findRun(l.store.ProjectDirs(), args[1])
			}
			if err != nil && !strings.HasPrefix(err.Error(), "no session ") {
				return err
			}
			if err == nil {
				proc, said, err := askStop(id, log)
				if err != nil {
					return err
				}
				if proc == nil {
					c.Say("%s", strings.ToUpper(said[:1])+said[1:])
					return nil
				}
				c.Say("The session %s was asked to stop; it ends what it was doing first (up to %s).", RunShort(id), roundTime(stopWait))
				l.background(func(ctx context.Context) {
					if said, err := awaitStop(ctx, l.p.now, id, log, proc); err == nil {
						l.notice(agent.LevelInfo, "tasks", strings.ToUpper(said[:1])+said[1:])
					}
				})
				return nil
			}
		}
		return fmt.Errorf("%s is not one of this session's background tasks, or a session in the background; /tasks lists them, and gnosh agents those of every folder", oneLine(args[1], 40))
	case len(args) > 0:
		return errors.New("/tasks takes nothing after it, or stop and the id of a background task")
	}
	for _, b := range agents {
		c.Say("%s  sub-agent (%s), running for %s: %s", b.id, b.kind, roundTime(l.p.now().Sub(b.started)), oneLine(b.desc, 100))
		if b.output != "" {
			c.Say("     its session file: %s", b.output)
		}
	}
	// The sub-agents of this session that have ended, as Claude Code's
	// "Earlier in this session": the model can give one more to do.
	if earlier := l.earlierAgents(ctx); len(earlier) > 0 {
		c.Say("Earlier in this session:")
		var more []string
		for _, a := range earlier {
			c.Say("%s  sub-agent (%s), %s: %s", a.id, a.kind, firstOf(endedWords[a.ended], "ended"), oneLine(firstOf(a.task, "(no task in its file)"), 100))
			if a.more {
				more = append(more, a.id)
			}
		}
		if len(more) > 0 {
			c.Say("     The model can give %s more to do (SendMessage to its id): ask it to.", strings.Join(more, ", "))
		}
	}
	// The runs /background and /fork started from this folder, from any
	// session: they outlive the session that started them.
	for _, r := range l.backgroundRuns() {
		state := runState(r) + ", started " + roundTime(l.p.now().Sub(r.started)) + " ago"
		if r.running {
			state = "running for " + roundTime(l.p.now().Sub(r.started))
		}
		c.Say("%s  a session in the background, %s: %s", RunShort(r.session), state, oneLine(firstOf(r.name, r.prompt, "(its prompt is not in its log)"), 100))
		if r.running {
			c.Say("     what it prints: %s; /tasks stop %s stops it; gnosh -r %s opens it once it is done", l.tilde(r.log), RunShort(r.session), r.session)
		} else {
			if e := runEnding(r); e != "" {
				c.Say("     %s", e)
			}
			if r.last != "" {
				c.Say("     it ended with: %s", oneLine(r.last, 120))
			}
			c.Say("     gnosh -r %s opens it; all it printed is in %s", r.session, l.tilde(r.log))
		}
	}
	if l.shell == nil {
		if len(agents) == 0 {
			c.Say("This session runs no commands, so none runs in the background.")
		} else {
			c.Say("/tasks stop <id> stops one.")
		}
		return nil
	}
	tasks := l.shell.Tasks()
	if len(tasks) == 0 {
		if len(agents) == 0 {
			c.Say("No command has run in the background in this session. Ctrl+B moves one that is running into the background.")
		} else {
			c.Say("/tasks stop <id> stops one.")
		}
		return nil
	}
	running := 0
	for _, t := range tasks {
		state := "running for " + roundTime(t.Duration)
		if t.Running {
			running++
		} else {
			state = fmt.Sprintf("ended after %s, exit %d", roundTime(t.Duration), t.ExitCode)
			if t.Signal != "" {
				state = fmt.Sprintf("killed by %s after %s", t.Signal, roundTime(t.Duration))
			}
		}
		if t.Monitor {
			state = "a Monitor's, " + state
		}
		c.Say("%s  %s: %s", t.ID, state, oneLine(t.Command, 100))
		c.Say("     its output: %s (%s)", t.OutputFile, byteCount(t.OutputBytes))
	}
	if running > 0 || len(agents) > 0 {
		c.Say("/tasks stop <id> stops one.")
	}
	return nil
}

// slashCost is /cost (also /usage and /stats): what the session has used.
// There is no price list, so it is counted in requests, tokens and time.
func (l *live) slashCost(ctx context.Context, c *SlashContext) error {
	u := c.Agent.Usage()
	if u.Requests == 0 && len(u.ByModel) == 0 {
		c.Say("Nothing has been asked of a model in this session yet.")
		return nil
	}
	m := u.Main
	c.Say("This session: %s to its model, %s of waiting on it.", requests(u.Requests), roundTime(u.APITime))
	c.Say("  read     %s tokens (%s fresh, %s from the cache)", tokenCount(m.PromptTokens()), tokenCount(m.InputTokens+m.CacheWriteTokens), tokenCount(m.CacheReadTokens))
	c.Say("  written  %s tokens", tokenCount(m.OutputTokens))
	if line := cacheLine(u); line != "" {
		c.Say("  cache    %s", line)
	}
	if len(u.ByModel) > 0 {
		c.Say("Every request made for it, by model (summaries, side jobs and sub-agents included):")
		ids := make([]string, 0, len(u.ByModel))
		for id := range u.ByModel {
			ids = append(ids, id)
		}
		sort.Strings(ids)
		for _, id := range ids {
			mt := u.ByModel[id]
			c.Say("  %s: %s, %s tokens read, %s written, %s", id, requests(mt.Requests), tokenCount(mt.Usage.PromptTokens()), tokenCount(mt.Usage.OutputTokens), roundTime(mt.APITime))
		}
	}
	if u.Side.Requests > 0 {
		c.Say("Side jobs (titles, checks, summaries of tool output): %s, %s.", requests(u.Side.Requests), roundTime(u.Side.Time))
	}
	if cs := c.Agent.Context(); cs.Window > 0 {
		c.Say("The context now holds about %s of %s tokens (%d%%).", tokenCount(cs.Used), tokenCount(cs.Window), cs.Used*100/cs.Window)
	}
	return nil
}

// cacheLine says how the model server's prompt cache served the main
// requests (Claude Code's "Prompt cache (main)" line of /usage), or ""
// when the server never said: the share of what was read that came from
// it, the requests that read again what the request before had sent the
// same way, and those that began anew.
func cacheLine(u agent.Totals) string {
	c := u.Cache
	all := u.Main.PromptTokens()
	if !c.Observed || all == 0 {
		return ""
	}
	line := fmt.Sprintf("%d%% of what was read came from the model server's cache", u.Main.CacheReadTokens*100/all)
	if c.Misses > 0 {
		line += fmt.Sprintf("; %s read again about %s tokens it could have kept from the request before, the last at %s (another request took the cache's place, the server started again, or the model's template wrote earlier turns anew)",
			requests(c.Misses), tokenCount(c.MissTokens), c.LastMiss.Local().Format("15:04"))
	}
	if c.Rebuilds > 0 {
		line += fmt.Sprintf("; %s began anew after the conversation's start changed (a summary, a slide, mem putting a frame away, a rewind)", requests(c.Rebuilds))
	}
	return line
}

// requests counts requests in words: "1 request", "3 requests".
func requests(n int) string {
	if n == 1 {
		return "1 request"
	}
	return fmt.Sprintf("%d requests", n)
}

// roundTime writes a span of time to the second, or to the tenth of one
// when it is shorter than a second.
func roundTime(d time.Duration) string {
	if d < time.Second {
		return d.Round(100 * time.Millisecond).String()
	}
	return d.Round(time.Second).String()
}

// slashAgents is /agents: the kinds of sub-agent the Agent tool can start,
// and where each is defined. New kinds are files in an agents folder; the
// model can write one when asked.
func (l *live) slashAgents(ctx context.Context, c *SlashContext) error {
	defs := l.agentDefs().All()
	if len(defs) == 0 {
		c.Say("There are no kinds of sub-agent.")
		return nil
	}
	if !slices.Contains(l.toolNames(), agentToolName) {
		c.Say("The Agent tool is switched off in this session, so none of these is started:")
	} else {
		c.Say("The kinds of sub-agent the model can start:")
	}
	for _, d := range defs {
		where := d.Scope
		if d.Source != "" {
			where = d.Source
		}
		about := oneLine(d.Description, 100)
		if about == "" {
			about = "(no description)"
		}
		c.Say("  %s (%s): %s", d.Name, where, about)
	}
	c.Say("A new kind is a Markdown file in %s, or in .gnosh/agents/ in a trusted project; ask the model to write one.", filepath.Join(l.cfg.Paths.ConfigDir, "agents"))
	return nil
}

// slashMemory is /memory: the context files the model is given at the start
// of each session, and where a new one goes. What the model keeps for
// itself is mem, which /mem shows.
func (l *live) slashMemory(ctx context.Context, c *SlashContext) error {
	var files []string
	if l.instr != nil {
		for _, f := range l.instr.Files() {
			line := fmt.Sprintf("  %s (%s, %s)", f.Path, strings.ToLower(string(f.Scope)), byteCount(f.Size))
			if f.Late {
				line += ", read when a file under it was touched"
			}
			files = append(files, line)
		}
	}
	if len(files) == 0 {
		c.Say("No context file is given to the model in this session.")
	} else {
		c.Say("The context files the model is given:")
		for _, f := range files {
			c.Say("%s", f)
		}
	}
	if len(l.cfg.Context.Global) > 0 {
		c.Say("Yours, for every project: %s", l.cfg.Context.Global[0].V)
	}
	c.Say("The project's, shared with whoever works on it: %s (/init has the model write it)", filepath.Join(l.cfg.Paths.ProjectRoot, "GNOSH.md"))
	c.Say("They are read as a session starts, and a change shows in the next one. What the model keeps for itself between sessions is mem: /mem shows it.")
	return nil
}

// byteCount writes a size in bytes for a person.
func byteCount(n int64) string {
	switch {
	case n < 1024:
		return fmt.Sprintf("%d bytes", n)
	case n < 1024*1024:
		return fmt.Sprintf("%.1f KB", float64(n)/1024)
	}
	return fmt.Sprintf("%.1f MB", float64(n)/(1024*1024))
}

// slashInit is /init: the model looks through the project and writes the
// context file every later session is given, or improves the one there is.
func (l *live) slashInit(ctx context.Context, c *SlashContext) error {
	root := l.cfg.Paths.ProjectRoot
	target := filepath.Join(root, "GNOSH.md")
	var have []string
	for _, name := range []string{"GNOSH.md", "AGENTS.md", "CLAUDE.md"} {
		if info, err := os.Stat(filepath.Join(root, name)); err == nil && info.Mode().IsRegular() {
			have = append(have, name)
		}
	}
	task := fmt.Sprintf(initTask, target)
	switch {
	case slices.Contains(have, "GNOSH.md"):
		task += "\n\nGNOSH.md is there already: read it first, keep what is still true and useful in it, correct what is not, and add what is missing. Do not start it over."
	case len(have) > 0:
		task += fmt.Sprintf("\n\nThe project has %s already, which gnosh reads as well: read it first, and write GNOSH.md only with what it lacks, so that the two do not say the same things twice.", strings.Join(have, " and "))
	}
	c.Say("The model looks through the project and writes %s.", target)
	return c.Send(agent.Prompt{Hidden: true, Source: "init", Text: prompt.Wrap(task),
		Notice: "/init: the model is writing the project's GNOSH.md."})
}

// slashCopy is /copy [N]: the model's last reply, or the Nth from the end,
// goes to the person's clipboard, through the terminal (OSC 52).
func (l *live) slashCopy(ctx context.Context, c *SlashContext) error {
	n := 1
	if a := strings.TrimSpace(c.Args); a != "" {
		v, err := strconv.Atoi(a)
		if err != nil || v < 1 {
			return errors.New("/copy takes nothing, or a number: 1 is the model's last reply, 2 the one before it")
		}
		n = v
	}
	var replies []string
	for _, e := range transcript.Conversation(l.ts.Entries()) {
		if e.Type == transcript.TypeAssistant {
			if text := strings.TrimSpace(e.Message.Text()); text != "" {
				replies = append(replies, text)
			}
		}
	}
	switch {
	case len(replies) == 0:
		return errors.New("the model has not replied in this session yet, so there is nothing to copy")
	case n > len(replies):
		return fmt.Errorf("the model has replied %s in this session, so there is no reply %d from the end", countWords(len(replies), "time", "times"), n)
	}
	text := replies[len(replies)-n]
	which := "The model's last reply"
	if n > 1 {
		which = fmt.Sprintf("The model's reply %d from the end", n)
	}
	l.toClipboard(ctx, c, which, text)
	return nil
}

// toClipboard puts text on the person's clipboard: with this machine's
// own tool where the person's terminal is on this machine, and otherwise
// through the terminal (OSC 52); and says which, of what (which).
func (l *live) toClipboard(ctx context.Context, c *SlashContext, which, text string) {
	size := countWords(utf8.RuneCountInString(text), "character", "characters")
	failed := ""
	if tool := clipboard.Tool(runtime.GOOS, l.p.getenv, l.p.lookPath); tool != nil {
		err := clipboard.Copy(ctx, tool, l.p.o.Environ, text)
		if err == nil {
			c.Say("%s (%s) is on the clipboard.", which, size)
			return
		}
		failed = fmt.Sprintf(" (%s failed: %v)", filepath.Base(tool[0]), err)
	}
	c.Copy(text)
	c.Say("%s (%s) is on its way to the clipboard through the terminal%s: one that does not take OSC 52 (Apple's Terminal, or tmux without set-clipboard) copies nothing.", which, size, failed)
}

// exportClipboard is what /export takes to put the conversation on the
// clipboard instead of in a file: the interface's /export picker runs it.
const exportClipboard = "--clipboard"

// slashExport is /export [file | --clipboard]: the conversation as plain
// text, in a file, or on the clipboard. A relative name is the working
// directory's, a name with no extension gets .txt, and with no name it is
// <date>-<session's name>.txt there. A file that is there already is
// never written over.
func (l *live) slashExport(ctx context.Context, c *SlashContext) error {
	name := strings.TrimSpace(c.Args)
	if name == exportClipboard {
		// (What the interface's picker runs for "the clipboard".)
		l.toClipboard(ctx, c, "The conversation", exportText(l.ts.Entries()))
		return nil
	}
	if name == "" {
		title, _ := l.title()
		name = l.p.now().Format("2006-01-02") + "-" + fileSlug(firstOf(title, "session")) + ".txt"
	}
	switch {
	case strings.HasPrefix(name, "~/"):
		name = filepath.Join(l.cfg.Paths.Home, name[2:])
	case !filepath.IsAbs(name):
		name = filepath.Join(l.cwd(), name)
	}
	if filepath.Ext(name) == "" {
		name += ".txt"
	}
	text := exportText(l.ts.Entries())
	f, err := os.OpenFile(name, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	switch {
	case errors.Is(err, os.ErrExist):
		return fmt.Errorf("%s is there already, and /export writes over nothing; give it another name", name)
	case err != nil:
		return fmt.Errorf("%s cannot be written: %v", name, err)
	}
	_, werr := f.WriteString(text)
	if cerr := f.Close(); werr == nil {
		werr = cerr
	}
	if werr != nil {
		return fmt.Errorf("%s could not be written whole: %v", name, werr)
	}
	c.Say("The conversation is in %s (%d lines).", name, strings.Count(text, "\n"))
	return nil
}

// fileSlug makes a session's name into a piece of a file name: lower case
// letters and digits, with a dash between words, at most 48 characters.
func fileSlug(s string) string {
	var b strings.Builder
	dash := false
	for _, r := range strings.ToLower(s) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			if dash && b.Len() > 0 {
				b.WriteByte('-')
			}
			b.WriteRune(r)
			dash = false
		default:
			dash = true
		}
		if b.Len() >= 48 {
			break
		}
	}
	return firstOf(strings.TrimRight(b.String(), "-"), "session")
}

// exportMaxOutput is how many lines of one tool call's output /export
// keeps; what is cut is counted.
const exportMaxOutput = 40

// exportText writes the conversation as plain text for a person: each of
// their prompts after "> ", the model's answers as it wrote them, each tool
// call as one line with its output indented under it, and a line where the
// conversation was summarized for the model. Reasoning, and what gnosh
// added for the model alone, are left out, and no line is empty but the
// ones inside an answer.
func exportText(entries []transcript.Entry) string {
	var b strings.Builder
	put := func(prefix, more, text string) {
		lines := strings.Split(strings.TrimRight(text, "\n"), "\n")
		for i, line := range lines {
			lead := more
			if i == 0 {
				lead = prefix
			}
			b.WriteString(strings.TrimRight(lead+line, " \t"))
			b.WriteByte('\n')
		}
	}
	for _, e := range transcript.Conversation(entries) {
		switch e.Type {
		case transcript.TypeUser:
			switch e.Source {
			case transcript.SourcePrompt:
				if text := strings.TrimSpace(e.Message.Text()); text != "" {
					put("> ", "  ", text)
				}
			case transcript.SourceToolResults:
				for _, blk := range e.Message.Blocks {
					if blk.Type != core.BlockToolResult {
						continue
					}
					var out strings.Builder
					for _, part := range blk.Content {
						if part.Type == core.BlockText {
							out.WriteString(part.Text)
						}
					}
					_, rest := shelltool.SplitStamp(out.String())
					rest = strings.TrimRight(rest, "\n")
					if strings.TrimSpace(rest) == "" {
						continue
					}
					lines := strings.Split(rest, "\n")
					if len(lines) > exportMaxOutput {
						lines = append(lines[:exportMaxOutput], fmt.Sprintf("(%d more lines)", len(lines)-exportMaxOutput))
					}
					put("    ", "    ", strings.Join(lines, "\n"))
				}
			}
		case transcript.TypeAssistant:
			for _, blk := range e.Message.Blocks {
				switch blk.Type {
				case core.BlockText:
					if text := strings.TrimSpace(blk.Text); text != "" {
						put("", "", text)
					}
				case core.BlockToolUse:
					put("* ", "  ", toolLine(blk.Name, blk.Input))
				}
			}
		case transcript.TypeSummary:
			put("", "", "(The conversation up to here was summarized for the model.)")
		}
	}
	return b.String()
}

// diffMaxLines is how many lines of the diff itself /diff shows; the rest
// is counted, and git diff shows it.
const diffMaxLines = 400

// emptyTree is git's object id of a tree with nothing in it: what a
// repository with no commit yet is compared with.
const emptyTree = "4b825dc642cb6eb9a060e54bf8d69288fbee4904"

// slashDiff is /diff: what has changed in the working tree since the last
// commit, the model's edits among it: a line a file, the files git does not
// track yet, and then the changes themselves.
//
// It runs git, which gnosh otherwise never does itself, and only in a
// folder the person has trusted: a repository's own settings can name
// programs git runs (a diff driver, a text converter, a file-system
// monitor). Those three are switched off all the same; a filter a trusted
// repository names still runs, as it would for the person's own git diff.
func (l *live) slashDiff(ctx context.Context, c *SlashContext) error {
	root := l.cfg.Paths.ProjectRoot
	if !l.st.trusted {
		return errors.New("/diff runs git, and this folder is not trusted: a repository's own settings can name programs git runs. gnosh trust trusts it")
	}
	git := func(args ...string) (string, error) {
		base := []string{"--no-pager", "-c", "core.fsmonitor=false", "-c", "diff.external=", "-c", "core.quotePath=false", "-C", root}
		cmd := exec.CommandContext(ctx, "git", append(base, args...)...)
		cmd.Env = append(os.Environ(), "GIT_EXTERNAL_DIFF=", "GIT_PAGER=cat", "PAGER=cat")
		out, err := cmd.Output()
		return string(out), err
	}
	if _, err := exec.LookPath("git"); err != nil {
		return errors.New("/diff needs git, and there is none on this machine's PATH")
	}
	if _, err := git("rev-parse", "--is-inside-work-tree"); err != nil {
		c.Say("%s is not in a git repository, so there is no last commit to compare with.", root)
		return nil
	}
	against := "HEAD"
	if _, err := git("rev-parse", "--verify", "--quiet", "HEAD"); err != nil {
		against = emptyTree
	}
	diffArgs := []string{"diff", "--no-ext-diff", "--no-textconv", "--no-color"}
	stat, err := git(append(diffArgs, "--stat", against)...)
	if err != nil {
		return fmt.Errorf("git diff did not work: %v", err)
	}
	untracked, _ := git("ls-files", "--others", "--exclude-standard")
	stat, untracked = strings.TrimRight(stat, "\n"), strings.TrimRight(untracked, "\n")
	if stat == "" && untracked == "" {
		c.Say("Nothing has changed since the last commit.")
		return nil
	}
	if stat != "" {
		c.Say("Changed since the last commit:")
		c.Say("%s", stat)
	}
	if untracked != "" {
		files := strings.Split(untracked, "\n")
		shown := files
		if len(shown) > 20 {
			shown = shown[:20]
		}
		c.Say("New files git does not track yet (%d): %s", len(files), strings.Join(shown, ", "))
		if len(files) > len(shown) {
			c.Say("  and %d more; git status lists them all.", len(files)-len(shown))
		}
	}
	if stat == "" {
		return nil
	}
	full, err := git(append(diffArgs, against)...)
	if err != nil {
		return fmt.Errorf("git diff did not work: %v", err)
	}
	lines := strings.Split(strings.TrimRight(full, "\n"), "\n")
	cut := 0
	if len(lines) > diffMaxLines {
		cut = len(lines) - diffMaxLines
		lines = lines[:diffMaxLines]
	}
	c.Say("%s", strings.Join(lines, "\n"))
	if cut > 0 {
		c.Say("(%d more lines of the diff; git diff shows them all.)", cut)
	}
	return nil
}

// initTask is what /init asks of the model. %s is the file it writes.
const initTask = `The user typed /init. Write %s: the file every later session in this project is given at its start, so that an agent that knows nothing of the project can work in it at once.

Look through the project first: its README, its build files and manifests, its test setup, its folder layout, and a few files that show how the code is written. Then write down what an agent needs and could not guess:
- how to build, test and lint, with the exact commands, and how to run one test alone;
- how the code is laid out, and where the main pieces are;
- the project's own conventions, where they differ from the language's usual ones;
- anything that is easy to get wrong: generated files, slow or flaky tests, steps that must come in an order.

Keep it short: plain sentences and lists, the facts of this project and nothing that is true of every project. Do not invent commands or files: write down only what you found. When it is written, say in a few lines what is in it.`
