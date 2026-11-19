package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/gnosh-ai/gnosh/internal/agent"
	"github.com/gnosh-ai/gnosh/internal/cli"
	"github.com/gnosh-ai/gnosh/internal/config"
	"github.com/gnosh-ai/gnosh/internal/core"
	"github.com/gnosh-ai/gnosh/internal/prompt"
	"github.com/gnosh-ai/gnosh/internal/transcript"
)

// This file holds the smaller commands of a session (/context, /rename,
// /resume), a session's name, and the subcommands that look at what
// gnosh keeps without starting a model (dirs, logs, models, session).

// thousands writes a count of tokens the way the status line does.
func thousands(n int) string {
	if n < 1000 {
		return strconv.Itoa(n)
	}
	return strconv.FormatFloat(float64(n)/1000, 'f', 1, 64) + "K"
}

// slashContext is /context: what fills the model's window, by kind. The
// sizes are estimates at four bytes to a token, beside the loop's own
// count of the whole.
func (l *live) slashContext(ctx context.Context, c *SlashContext) error {
	cs := l.agent.Context()
	est := prompt.EstimateTokens
	system := 0
	for _, part := range l.system {
		system += est(part)
	}
	tools := 0
	var toolRows []string
	for _, t := range l.toolSpecs() {
		n := est(t.Name) + est(t.Description) + est(string(t.InputSchema))
		tools += n
		toolRows = append(toolRows, fmt.Sprintf("      %-18s %s", t.Name, thousands(n)))
	}
	files := est(l.startInstructions())
	defs := est(l.startAgentTypes()) + est(l.skillsLine())
	// The loop counts with each request; before the first there is no
	// count, and the window is only what the configuration says, if it
	// says.
	switch configured := l.agent.Model().Info.ContextWindow; {
	case cs.Window > 0:
		window := thousands(cs.Window)
		if !cs.Known {
			window += " (assumed: the server did not say)"
		}
		c.Say("The model's window is %s tokens; room is made when the conversation reaches %s.", window, thousands(cs.Point))
		c.Say("In use at the last count: about %s (%d%%), with about %s to go before room is made.", thousands(cs.Used), cs.Used*100/cs.Window, thousands(cs.Room()))
	case configured > 0:
		c.Say("The model's window is %s tokens, as configured. Nothing has been sent to it yet; the first request counts what is in it.", thousands(configured))
	default:
		c.Say("Nothing has been sent to the model yet: the first request asks its server how large its window is, and counts what is in it.")
	}
	c.Say("  gnosh's instructions to the model         %s", thousands(system))
	c.Say("  what the model is told of its %d tools     %s", len(toolRows), thousands(tools))
	rest := cs.Used - system - tools
	if rest < 0 {
		rest = 0
	}
	c.Say("  the conversation                          %s", thousands(rest))
	c.Say("    of it, given with the first prompt: your and the project's instruction files %s, the sub-agent kinds and skills %s", thousands(files), thousands(defs))
	if cs.AutoOff {
		c.Say("Automatic summaries are off for this session: one was made and did not help.")
	}
	if strings.TrimSpace(c.Args) != "all" {
		c.Say("/context all lists each tool and each instruction file.")
		return nil
	}
	c.Say("The tools:")
	for _, row := range toolRows {
		c.Say("%s", row)
	}
	if l.instr != nil {
		c.Say("The instruction files in effect from the start:")
		n := 0
		for _, f := range l.instr.Files() {
			c.Say("      %s  %s", f.Path, thousands(est(f.Text)))
			n++
		}
		if n == 0 {
			c.Say("      none")
		}
	}
	return nil
}

// cleanTitle makes a session's name of what was typed: one line, of a
// length a list can show.
func cleanTitle(s string) string {
	return oneLine(strings.Join(strings.Fields(s), " "), 120)
}

// title is the session's name: the last one the person gave, or else the
// last one gnosh made, or empty.
func (l *live) title() (name string, byPerson bool) {
	for _, e := range l.ts.Entries() {
		if e.Type != transcript.TypeTitle || e.Title == "" {
			continue
		}
		switch {
		case e.Source == transcript.SourceUser:
			name, byPerson = e.Title, true
		case !byPerson:
			name = e.Title
		}
	}
	return name, byPerson
}

// nameAfter names a session that has no name after its first prompt, at
// once and without asking a model (LOOP-9). A name the person gave is
// never replaced, and a sub-agent's session is never named.
func (l *live) nameAfter(text string) {
	if l.named.Swap(true) {
		return
	}
	if name, _ := l.title(); name != "" {
		return
	}
	first, _, _ := strings.Cut(strings.TrimSpace(text), "\n")
	name := cleanTitle(first)
	if name == "" {
		l.named.Store(false)
		return
	}
	if _, err := l.ts.Append(transcript.TitleEntry(name, transcript.SourceAuto)); err != nil {
		l.flog.logf("config", "the session could not be named after its first prompt: %v", err)
		return
	}
	l.setStatus("session", name)
}

// slashRename is /rename: it names the session, and shows the name it
// has.
func (l *live) slashRename(ctx context.Context, c *SlashContext) error {
	name := cleanTitle(c.Args)
	if name == "" {
		switch have, byPerson := l.title(); {
		case have == "":
			c.Say("This session has no name yet. /rename and a name gives it one; gnosh -r and that name opens it again.")
		case byPerson:
			c.Say("This session is named %q. /rename and another name changes it.", have)
		default:
			c.Say("This session is named %q, after its first prompt. /rename and a name gives it one of yours. (gnosh does not ask a model for names.)", have)
		}
		return nil
	}
	if _, err := l.ts.Append(transcript.TitleEntry(name, transcript.SourceUser)); err != nil {
		return fmt.Errorf("the name could not be written into the session's file: %w", err)
	}
	l.named.Store(true)
	l.setStatus("session", name)
	c.Say("The session is named %q. gnosh -r %q opens it again.", name, name)
	return nil
}

// slashClear is /clear [name]. The controls open the new session
// (controls.Command); a name given labels the one left behind, as Claude
// Code's /clear does, for /resume and gnosh -r.
func (l *live) slashClear(ctx context.Context, c *SlashContext) error {
	name := cleanTitle(c.Args)
	if name == "" {
		return nil
	}
	if _, err := l.ts.Append(transcript.TitleEntry(name, transcript.SourceUser)); err != nil {
		return fmt.Errorf("nothing was cleared: the name could not be written into the session's file: %w", err)
	}
	l.named.Store(true)
	return nil
}

// SessionChoice is a session of the folder that can be opened in place of
// the one that is open.
type SessionChoice struct {
	// ID is the session's id, and Title what it is called: its name, or
	// the start of its first prompt.
	ID    string
	Title string
	// Modified is when it was last written to, and Prompts how many
	// prompts the person sent in it.
	Modified time.Time
	Prompts  int
}

// resumeChoices lists the folder's other sessions, the one written to
// last first.
func (l *live) resumeChoices() []SessionChoice {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	list, err := l.store.List(ctx, l.cfg.Paths.Cwd)
	if err != nil {
		return nil
	}
	var out []SessionChoice
	for _, info := range list {
		if info.ID == l.ts.ID() || len(out) == 30 {
			continue
		}
		title := firstOf(info.Title, info.FirstPrompt, "(no prompt yet)")
		if transcript.InUse(info.Path) {
			// (Another gnosh holds it, which opening it would be refused.)
			title += " (open now)"
		}
		out = append(out, SessionChoice{ID: info.ID, Title: title, Modified: info.Modified, Prompts: info.Prompts})
	}
	return out
}

// ResumeChoices lists the folder's other sessions, the one written to
// last first: what /resume offers.
func (c *controls) ResumeChoices() []SessionChoice { return c.cur().resumeChoices() }

// slashResume is /resume: with nothing after it, the folder's other
// sessions; with a number from that list, an id or a name, that session
// is opened in place of this one, which the session's controls then do.
func (l *live) slashResume(ctx context.Context, c *SlashContext) error {
	choices := l.resumeChoices()
	word := strings.TrimSpace(c.Args)
	if word == "" {
		if len(choices) == 0 {
			c.Say("There is no other session in this folder to open.")
			return nil
		}
		c.Say("The other sessions of this folder, the one written to last first:")
		l.resumeShown = l.resumeShown[:0]
		for i, ch := range choices {
			c.Say("  %d. %s  %s  %s", i+1, ch.ID[:min(8, len(ch.ID))], ch.Modified.Local().Format("2006-01-02 15:04"), oneLine(ch.Title, 80))
			l.resumeShown = append(l.resumeShown, ch.ID)
		}
		c.Say("/resume N opens one in place of this session, which is kept. An id or a name works too, and so do words from a session's name or first prompt, which finds it among all of them.")
		return nil
	}
	if l.busy(ctx) {
		return errors.New("a turn is running. Stop it first, then open another session")
	}
	id := ""
	// A number is the place in the list as it was shown, or, with none
	// shown, as it stands.
	shown := l.resumeShown
	if len(shown) == 0 {
		for _, ch := range choices {
			shown = append(shown, ch.ID)
		}
	}
	if n, err := strconv.Atoi(word); err == nil && n >= 1 && n <= len(shown) {
		id = shown[n-1]
	} else {
		found, err := l.findSession(ctx, word)
		if err != nil {
			return err
		}
		id = found
	}
	if id == l.ts.ID() {
		return errors.New("that is the session that is open")
	}
	l.resumeTo = id
	return nil
}

// settleWait is how long a command that needs an idle session waits for
// one that is busy to go idle before it refuses.
const settleWait = 3 * time.Second

// busy reports whether a turn is still running, after waiting up to
// settleWait for the session to go idle. The screen shows a turn done when
// it ends, but the moments after it (the turn-ended hooks, the idle
// moment) keep the session busy a little longer, and a command typed at
// once must not be refused for that.
func (l *live) busy(ctx context.Context) bool {
	if !l.agent.State().Busy {
		return false
	}
	wctx, cancel := context.WithTimeout(ctx, settleWait)
	defer cancel()
	return l.agent.Wait(wctx) != nil
}

// slashBranch is /branch: the conversation so far is copied into a new
// session, which the session's controls then open in place of this one.
// The original is kept as it is, and can be resumed.
func (l *live) slashBranch(ctx context.Context, c *SlashContext) error {
	if l.busy(ctx) {
		return errors.New("a turn is running. Stop it first, then branch")
	}
	if !slices.ContainsFunc(l.ts.Entries(), func(e transcript.Entry) bool { return e.Type == transcript.TypeUser }) {
		return errors.New("there is nothing to copy yet: the conversation has not begun")
	}
	name := cleanTitle(c.Args)
	l.branchTo = &name
	return nil
}

// branch copies the conversation into a new session, names the copy, and
// opens it behind the same controls. The original is kept.
func (c *controls) branch(ctx context.Context, name string) (string, error) {
	old := c.cur()
	copied, err := old.store.Branch(old.ts, "")
	if err != nil {
		return "", fmt.Errorf("the conversation could not be copied, so this session stays open: %w", err)
	}
	source := transcript.SourceUser
	if name == "" {
		// The copy would carry the original's name: it is told apart.
		if was, byPerson := old.title(); was != "" {
			name = was + " (branch)"
			if !byPerson {
				source = transcript.SourceAuto
			}
		}
	}
	if name != "" {
		_, err = copied.Append(transcript.TitleEntry(name, source))
	}
	id := copied.ID()
	if cerr := copied.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return "", fmt.Errorf("the copy %s could not be named, so this session stays open: %w", id, err)
	}
	old.close("resume")
	next, err := c.p.openSession(ctx, c.st, c.front, "fork", id)
	if err != nil {
		return "", fmt.Errorf("the session was closed, and its copy %s could not be opened: %w. gnosh -r %s opens the original again", id, err, old.ts.ID())
	}
	c.mu.Lock()
	c.l = next
	c.mu.Unlock()
	said := "You are in a copy of the conversation, session " + id
	if name != "" {
		said += ": " + name
	}
	return said + ". The original is kept as it was; /resume " + old.ts.ID() + " opens it again.", nil
}

// resume opens another session of the folder behind the same controls,
// and keeps the one that was open.
func (c *controls) resume(ctx context.Context, id string) (string, error) {
	old := c.cur()
	// The other session is looked at first: when it cannot be opened,
	// this one stays.
	probe, err := old.store.OpenReadOnly(ctx, id)
	if err != nil {
		return "", fmt.Errorf("the session %s cannot be opened (%v), so this one stays open", id, err)
	}
	_ = probe.Close()
	if probe.ID() != old.ts.ID() && transcript.InUse(probe.Path()) {
		// Another gnosh writes it (a /fork or /background run, most
		// likely), and one session has one writer: this one stays.
		why := fmt.Sprintf("the session %s is open in another gnosh, and one session has one writer, so this one stays open", probe.ID())
		if log := strings.TrimSuffix(probe.Path(), ".jsonl") + ".background.log"; fileExists(log) {
			why += fmt.Sprintf(". If it is a run in the background, it lets go of the session when it is done: /tasks stop %s stops it", RunShort(probe.ID()))
		}
		return "", errors.New(why)
	}
	old.close("resume")
	next, err := c.p.openSession(ctx, c.st, c.front, "resume", id)
	if err != nil {
		return "", fmt.Errorf("the session was closed, and %s could not be opened: %w", id, err)
	}
	c.mu.Lock()
	c.l = next
	c.mu.Unlock()
	name, _ := next.title()
	said := "Session " + id + " is open"
	if name != "" {
		said += ": " + name
	}
	return said + ". The one before is kept; gnosh -r " + old.ts.ID() + " opens it again.", nil
}

// dirsCommand is `gnosh dirs`: the folders in use.
func (p *program) dirsCommand(ctx context.Context, inv cli.Invocation) int {
	st, code := p.prepared(ctx, inv)
	if st == nil {
		return code
	}
	d := st.dirs("")
	settings := st.cfg.Paths.UserSettings
	if !slices.ContainsFunc(st.cfg.Files, func(f config.File) bool { return f.Path == settings }) {
		settings = "(not there yet) " + settings
	}
	rows := [][2]string{
		{"config", d.Config}, {"your settings", settings},
		{"state", d.State}, {"cache", d.Cache},
		{"working folder", d.Cwd}, {"project root", d.ProjectRoot}, {"project data", d.Project},
		{"sessions", st.sessions().ProjectDir(d.Cwd)}, {"log", filepath.Join(st.sessions().ProjectDir(d.Cwd), "gnosh.log")},
		{"your sub-agent kinds", strings.Join(userDefDirs(st.cfg, "agents"), ", ")},
		{"your skills", strings.Join(userDefDirs(st.cfg, "skills"), ", ")},
	}
	for _, r := range rows {
		fmt.Fprintf(p.stdout, "%-22s %s\n", r[0], r[1])
	}
	return cli.ExitOK
}

// logsCommand is `gnosh logs`: the end of the project's log. With
// --follow it keeps printing what is added until it is stopped.
func (p *program) logsCommand(ctx context.Context, inv cli.Invocation) int {
	st, code := p.prepared(ctx, inv)
	if st == nil {
		return code
	}
	dir := st.sessions().ProjectDir(st.cfg.Paths.Cwd)
	path := filepath.Join(dir, "gnosh.log")
	// With a session named, a run's log in place of the project's: what
	// it printed, followed until it lets go of its session.
	// alive says whether what is followed goes on: the project's log
	// always, a run's while the run works.
	alive := func() bool { return true }
	if len(inv.Args) > 0 {
		id, log, err := findRun(st.sessions().ProjectDirs(), inv.Args[0])
		if err != nil {
			p.errf("%v.", err)
			return cli.ExitFailure
		}
		file := runSession(id, log)
		path, alive = log, func() bool { return runAlive(id, log, file) }
	}
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			p.errf("this project has no log yet: nothing has been written to %s.", path)
			return cli.ExitFailure
		}
		p.errf("the log %s could not be read: %v", path, err)
		return cli.ExitFailure
	}
	tail := inv.Tail
	if tail <= 0 {
		tail = 1000
	}
	lines := strings.Split(strings.TrimRight(string(data), "\n"), "\n")
	if len(lines) > tail {
		lines = lines[len(lines)-tail:]
	}
	for _, line := range lines {
		fmt.Fprintln(p.stdout, line)
	}
	if !inv.Follow {
		return cli.ExitOK
	}
	at := int64(len(data))
	tick := time.NewTicker(250 * time.Millisecond)
	defer tick.Stop()
	for {
		// (A run's log is followed while the run works, and read once
		// more after it ends.)
		done := !alive()
		select {
		case <-ctx.Done():
			return cli.ExitOK
		case sig := <-p.o.Signals:
			return signalExit(sig)
		case <-tick.C:
		}
		f, err := os.Open(path)
		if err != nil {
			if done {
				return cli.ExitOK
			}
			continue
		}
		if info, err := f.Stat(); err == nil && info.Size() < at {
			at = 0 // the log was started afresh
		}
		buf := make([]byte, 64<<10)
		for {
			n, err := f.ReadAt(buf, at)
			if n > 0 {
				p.stdout.Write(buf[:n])
				at += int64(n)
			}
			if err != nil {
				break
			}
		}
		f.Close()
		if done {
			return cli.ExitOK
		}
	}
}

// modelsCommand is `gnosh models`: what each provider's server offers. It
// sends one request for the list to each address and never names a model,
// so nothing is loaded.
func (p *program) modelsCommand(ctx context.Context, inv cli.Invocation) int {
	st, code := p.prepared(ctx, inv)
	if st == nil {
		return code
	}
	cfg := st.cfg
	if len(cfg.Providers) == 0 {
		fmt.Fprintln(p.stdout, "No provider is configured. gnosh doctor says how to add one.")
		return cli.ExitFailure
	}
	inUse := ""
	if cfg.Large.Model.Set {
		inUse = cfg.Large.Model.V.Provider + "/" + cfg.Large.Model.V.ID
	}
	reached := false
	for _, pc := range cfg.Providers {
		for _, e := range pc.Endpoints {
			names, err := listModelNames(ctx, pc, e)
			if err != nil {
				fmt.Fprintf(p.stdout, "%s at %s: its list of models could not be read (%v)\n", pc.Name, e.URL, err)
				continue
			}
			reached = true
			fmt.Fprintf(p.stdout, "%s at %s:\n", pc.Name, e.URL)
			for _, name := range names {
				mark := ""
				if pc.Name+"/"+name == inUse {
					mark = "  (the model sessions use)"
				}
				fmt.Fprintf(p.stdout, "  %s/%s%s\n", pc.Name, name, mark)
			}
			if len(names) == 0 {
				fmt.Fprintln(p.stdout, "  (the server lists none)")
			}
		}
	}
	if !reached {
		return cli.ExitFailure
	}
	return cli.ExitOK
}

// sessionCommandFor is `gnosh session list`, `show`, `last` and `rename`.
func (p *program) sessionsCommand(ctx context.Context, inv cli.Invocation) int {
	st, code := p.prepared(ctx, inv)
	if st == nil {
		return code
	}
	store, cwd := st.sessions(), st.cfg.Paths.Cwd
	list, err := store.List(ctx, cwd)
	if err != nil {
		p.errf("the sessions of this folder could not be listed: %v", err)
		return cli.ExitFailure
	}
	find := func(word string) (string, bool) {
		if _, err := store.Path(word); err == nil {
			return word, true
		}
		var hits []string
		for _, info := range list {
			if strings.HasPrefix(info.ID, word) || strings.EqualFold(strings.TrimSpace(info.Title), strings.TrimSpace(word)) {
				hits = append(hits, info.ID)
			}
		}
		switch len(hits) {
		case 1:
			return hits[0], true
		case 0:
			p.errf("no session of this folder matches %q. gnosh session list shows them.", word)
		default:
			p.errf("%q matches %d sessions of this folder. Give more of the id.", word, len(hits))
		}
		return "", false
	}
	switch inv.Command {
	case cli.CmdSessionList:
		if inv.JSON {
			type row struct {
				ID          string    `json:"id"`
				Title       string    `json:"title,omitempty"`
				FirstPrompt string    `json:"firstPrompt,omitempty"`
				Created     time.Time `json:"created"`
				Modified    time.Time `json:"modified"`
				Prompts     int       `json:"prompts"`
				Path        string    `json:"path"`
				Open        bool      `json:"open,omitempty"`
			}
			rows := []row{}
			for _, info := range list {
				rows = append(rows, row{info.ID, info.Title, info.FirstPrompt, info.Created, info.Modified, info.Prompts, info.Path, transcript.InUse(info.Path)})
			}
			enc := json.NewEncoder(p.stdout)
			enc.SetIndent("", "  ")
			_ = enc.Encode(rows)
			return cli.ExitOK
		}
		if len(list) == 0 {
			fmt.Fprintf(p.stdout, "No session has been started in %s.\n", cwd)
			return cli.ExitOK
		}
		for _, info := range list {
			open := ""
			if transcript.InUse(info.Path) {
				// (A session, or a run /background or /fork started.)
				open = "  (open now)"
			}
			fmt.Fprintf(p.stdout, "%s  %s  %3d prompt(s)  %s%s\n", info.ID, info.Modified.Local().Format("2006-01-02 15:04"), info.Prompts, oneLine(firstOf(info.Title, info.FirstPrompt, "(no prompt yet)"), 80), open)
		}
		return cli.ExitOK
	case cli.CmdSessionRename:
		if len(inv.Args) < 2 {
			p.errf("gnosh session rename takes an id and the new title.")
			return cli.ExitFailure
		}
		id, ok := find(inv.Args[0])
		if !ok {
			return cli.ExitFailure
		}
		name := cleanTitle(strings.Join(inv.Args[1:], " "))
		ts, err := store.Open(ctx, id)
		if err != nil {
			var locked *transcript.LockedError
			if errors.As(err, &locked) {
				p.errf("the session %s is open in another gnosh, and one session has one writer. Name it there with /rename.", id)
			} else {
				p.errf("the session %s could not be opened: %v", id, err)
			}
			return cli.ExitFailure
		}
		id = ts.ID()
		_, err = ts.Append(transcript.TitleEntry(name, transcript.SourceUser))
		_ = ts.Close()
		if err != nil {
			p.errf("the name could not be written into the session's file: %v", err)
			return cli.ExitFailure
		}
		fmt.Fprintf(p.stdout, "The session %s is named %q.\n", id, name)
		return cli.ExitOK
	}
	// show and last print one session.
	id := ""
	if inv.Command == cli.CmdSessionLast {
		if len(list) == 0 {
			p.errf("no session has been started in %s.", cwd)
			return cli.ExitFailure
		}
		id = list[0].ID
	} else {
		if len(inv.Args) < 1 {
			p.errf("gnosh session show takes a session's id.")
			return cli.ExitFailure
		}
		var ok bool
		if id, ok = find(inv.Args[0]); !ok {
			return cli.ExitFailure
		}
	}
	ts, err := store.OpenReadOnly(ctx, id)
	if err != nil {
		p.errf("the session %s could not be opened: %v", id, err)
		return cli.ExitFailure
	}
	defer ts.Close()
	entries := transcript.Conversation(ts.Entries())
	if inv.JSON {
		enc := json.NewEncoder(p.stdout)
		for _, e := range entries {
			_ = enc.Encode(e)
		}
		return cli.ExitOK
	}
	fmt.Fprintf(p.stdout, "session %s, started in %s\n", ts.ID(), ts.StartCwd())
	for i := range entries {
		for _, e := range decode(&entries[i]) {
			printEntry(p, e)
		}
	}
	return cli.ExitOK
}

// printEntry writes one entry of a session as a person reads it: what was
// typed, what the model said and called, and the start of each result.
// What gnosh said to the model for itself is left out.
func printEntry(p *program, e agent.Entry) {
	stamp := e.Time.Local().Format("15:04")
	switch e.Kind {
	case agent.EntryPrompt:
		if !e.Hidden {
			fmt.Fprintf(p.stdout, "\n[%s] > %s\n", stamp, visible(strings.TrimSpace(core.TextOf(e.Blocks))))
		}
	case agent.EntryAssistant:
		if text := strings.TrimSpace(core.TextOf(e.Message.Blocks)); text != "" {
			fmt.Fprintf(p.stdout, "\n%s\n", visible(text))
		}
		for _, u := range e.Message.ToolUses() {
			fmt.Fprintf(p.stdout, "* %s: %s\n", u.Name, visible(oneLine(string(u.Input), 200)))
		}
	case agent.EntryResult:
		fmt.Fprintf(p.stdout, "  = %s\n", visible(resultLine(e.Result, e.Denied, e.Interrupted)))
	case agent.EntrySummary:
		fmt.Fprintf(p.stdout, "\n[%s] (the conversation up to here was replaced by a summary)\n", stamp)
	case agent.EntryTurn:
		if e.Reason != agent.EndCompleted {
			fmt.Fprintf(p.stdout, "  -- the turn ended: %s\n", e.Reason)
		}
	}
}
