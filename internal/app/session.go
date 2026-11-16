package app

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime/debug"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gnosh-ai/gnosh/internal/agent"
	"github.com/gnosh-ai/gnosh/internal/app/agentdef"
	"github.com/gnosh-ai/gnosh/internal/app/skill"
	"github.com/gnosh-ai/gnosh/internal/cli"
	"github.com/gnosh-ai/gnosh/internal/config"
	"github.com/gnosh-ai/gnosh/internal/core"
	"github.com/gnosh-ai/gnosh/internal/hooks"
	"github.com/gnosh-ai/gnosh/internal/instructions"
	"github.com/gnosh-ai/gnosh/internal/jsonschema"
	"github.com/gnosh-ai/gnosh/internal/permission"
	"github.com/gnosh-ai/gnosh/internal/prompt"
	"github.com/gnosh-ai/gnosh/internal/tools/shelltool"
	"github.com/gnosh-ai/gnosh/internal/transcript"
	"github.com/gnosh-ai/gnosh/internal/trust"
)

// live is one session as the wiring holds it: the session's file, the
// rules it runs under, its tools, its model and its agent, joined
// together. Nothing in it is global: a second session of the same process
// (after /clear) is a second live.
type live struct {
	p   *program
	st  *startup
	inv cli.Invocation
	cfg *config.Config

	// interactive says a person is there to be asked. source is how the
	// session came to be, in the SessionStart hook's words.
	interactive bool
	source      string

	store     *transcript.Store
	ts        *transcript.Session
	log       *sessionLog
	engine    *permission.Engine
	hooks     *hookRunner
	shell     *shelltool.Manager
	texts     *prompt.Set
	instr     *instructions.Set
	tools     []core.Tool
	own       []core.Tool // gnosh's own tools that are offered, without the Agent tool
	toolStore *sync.Map
	roots     []string
	agent     *agent.Agent
	facts     *Session
	contribs  []joined
	slash     *slashSet
	front     Frontend
	flog      *fileLog
	crash     *crashFile
	debugPage *debugPage

	// ctx ends when the session is closed. What runs beside the session
	// and is not waited for (a hook nobody reads the answer of) runs
	// under it.
	ctx    context.Context
	cancel context.CancelFunc
	bg     sync.WaitGroup

	// The session's models, and the client of every provider it has used.
	large *modelUse
	// asAgent is the kind of agent the session runs as (runas.go), or nil.
	asAgent *agentdef.Def
	// agentKinds are the only kinds of sub-agent the session may start,
	// when the kind it runs as lists Agent(kind, ...) among its tools; nil
	// for any.
	agentKinds []string
	// preloaded are the skills the kind it runs as preloads, as parts,
	// once its first request has gone out (agentskills.go).
	preloaded []agent.Part
	small     *modelUse
	provMu    sync.Mutex // guards providers
	providers map[string]*providerUse
	// load watches the model server while the session waits on it
	// (serverload.go).
	load   loadWatch
	typeMu sync.Mutex
	types  map[string]string // a model's id -> its provider's type
	// modelMu guards large once the session is open, and switchMu makes
	// /model one switch at a time.
	modelMu  sync.Mutex
	switchMu sync.Mutex

	// lastSummary is how the last summary ended.
	lastSummary atomic.Pointer[agent.SummaryEvent]

	// tasks is the session's task list, as a feature last set it.
	tasksMu sync.Mutex
	tasks   []Task

	// deliv is the session's list of deliverables, as a feature last set
	// it.
	delivMu sync.Mutex
	deliv   []Deliverable

	// featureSources are the sources of the notices the features raised.
	featureSources sync.Map

	// status holds the named facts for a front end's status line.
	statusMu sync.Mutex
	status   map[string]string

	// shellStop stops the command of the person's own that is running
	// (SessionControls.Shell), or is nil when none is.
	shellMu   sync.Mutex
	shellStop context.CancelFunc

	// subs are the sub-agents that are running, and agentCalls counts the
	// sub-agents the session has started.
	subMu      sync.Mutex
	subs       map[*subSession]struct{}
	agentCalls int

	// evMu makes the events a front end is given arrive one at a time.
	evMu sync.Mutex

	// The count behind --max-turns.
	maxTurns     int
	capMu        sync.Mutex
	roundsBefore int
	capped       bool

	// reopen is the id of the session this one opens again, after a
	// rewind; rewindTo is the point /rewind asked to go back to, which
	// the controls then do.
	reopen   string
	rewindTo string
	// rewindAll says /rewind asked for the files to be put back with the
	// conversation.
	rewindAll bool
	// checks keeps what the files the model changes held, for a rewind,
	// and checkWarn says once that one could not be kept.
	checks    *checkpoints
	checkWarn sync.Once
	// mcp is the session's MCP servers.
	mcp *mcpSet
	// statusLine runs the configured status line command, or is nil.
	statusLine *statusLiner
	// bgAgents are the sub-agents running in the background, and bgWG
	// counts their goroutines.
	bgMu     sync.Mutex
	bgAgents []*bgAgent
	bgWG     sync.WaitGroup
	// resumeTo is the session /resume asked to open in place of this one.
	resumeTo string
	// branchTo is set by /branch: the name of the copy it asked for, which
	// may be empty.
	branchTo *string
	// system is what the model is told as its instructions, and named
	// says the session has a name or was given one after its first
	// prompt.
	system []string
	named  atomic.Bool
	// lastCwd is the directory the shell was last seen in, for
	// CwdChanged; empty means the one the session started in.
	cwdMu   sync.Mutex
	lastCwd string

	// skillRuns are the skills that went into the conversation, the most
	// recent last, each with its text as it went in.
	// vimMode is the mode of the box's vim keys, as the front end last
	// said it (controls.SetVimMode).
	vimMu   sync.Mutex
	vimMode string
	// styles are the output styles there are, and styleName the one in
	// force (outputstyle.go).
	styleMu   sync.Mutex
	styles    []outputStyle
	styleName string
	skillMu   sync.Mutex
	skillRuns []skillRun
	// skillsWoken are the skills for some files (paths) the model has been
	// told of, the session having worked on one of their files, and
	// skillHooksIn those whose own hooks are in force.
	skillsWoken  map[string]bool
	skillHooksIn map[string]bool
	// effortHeld says a skill's effort is in force until the person's
	// next prompt, and effortBefore is the level to go back to then.
	effortHeld   bool
	effortBefore string
	// skillSet is the skills as they were last read, and skillRead when.
	// skillOver is how far each skill is seen (skillOverrides), as the
	// settings had it when the session began and /skills changed it since;
	// nil until first wanted.
	skillSet  *skill.Set
	skillRead time.Time
	skillOver map[string]string

	// gnoGuidePaths are where the Gno project's guides were written for
	// this session (gnoGuides), once.
	gnoGuidesOnce sync.Once
	gnoGuidePaths []string

	// settings is how the settings files stood when last looked at, for
	// taking in a change while the session runs (settingswatch.go).
	settings settingsWatch

	// agentReady is the agent, once the session made it, for code that
	// runs beside the session's opening (hooks in the background) and so
	// may not read l.agent while it is written.
	agentReady atomic.Pointer[agent.Agent]

	// hookChanged holds the ids of the calls of the question tool whose
	// arguments a PreToolUse hook wrote: only such a call may carry its
	// own answers.
	hookChanged sync.Map

	// toldMu guards told, the last few turns whose end the features have
	// been told of (TurnEnded), and toldCh, which is closed and made anew
	// each time one is added.
	toldMu sync.Mutex
	told   []string
	toldCh chan struct{}

	closeOnce sync.Once
	closing   atomic.Bool
	killed    atomic.Bool
	// configNow is what /config put in force in this session, by option.
	configMu  sync.Mutex
	configNow map[string]json.RawMessage
	// newBuildSaid says the person was told of a newer gnosh installed
	// (restart.go).
	newBuildSaid atomic.Bool
	// notif is the notification channel /config set while the session
	// runs; "" is the configuration's.
	notifMu sync.Mutex
	notif   string
	// showPast says the session was opened again long enough after it
	// was last written to that the interface shows where it left off.
	showPast bool
	// promptID is the id of the last prompt the person sent, for the
	// status line.
	promptMu sync.Mutex
	promptID string
	// color is the colour of the session's prompt mark (colorcmd.go),
	// read from its file once (colorRead).
	colorMu   sync.Mutex
	color     string
	colorRead bool
	// bgEver says the session has had work in the background, and
	// bgUntold that the ends of that work are no longer told to the model
	// (the run has stopped waiting for it).
	bgEver, bgUntold atomic.Bool
}

// joined is a feature with what it added to this session.
type joined struct {
	f Feature
	c *Contribution
}

// openSession builds a session: its file, its rules, its tools, its model
// and its agent, in that order. source is "clear" for the session /clear
// starts, "rewind" for a session that is opened again from its file after
// a rewind (reopen is then its id), and empty for the one the command
// line asks for.
func (p *program) openSession(ctx context.Context, st *startup, front Frontend, source string, reopen ...string) (l *live, err error) {
	l = &live{
		reopen: strings.Join(reopen, ""),
		p:      p, st: st, inv: st.inv, cfg: st.cfg,
		interactive: st.attended,
		source:      source,
		front:       front,
		store:       st.sessions(),
		toolStore:   new(sync.Map),
		providers:   map[string]*providerUse{},
		types:       map[string]string{},
		maxTurns:    st.inv.MaxTurns,
	}
	l.ctx, l.cancel = context.WithCancel(context.Background())
	defer func() {
		if err != nil {
			l.close("other")
			l = nil
		}
	}()

	if err := l.openTranscript(ctx); err != nil {
		return l, err
	}
	l.flog = openFileLog(p, l)
	if l.flog != nil {
		l.flog.fromFeature = func(source string) bool {
			_, ok := l.featureSources.Load(source)
			return ok
		}
	}
	l.crash = openCrashFile(p, l)
	l.flog.logf("startup", "gnosh %s, session %s (%s), in %s", p.versionLine(), l.ts.ID(), l.source, l.cfg.Paths.Cwd)
	for _, pr := range st.problems {
		l.flog.logf("config", "%s: %s", pr.Severity, pr.String())
	}
	for _, w := range l.ts.Warnings() {
		l.notice(agent.LevelWarning, "session", "The session's file needed mending when it was opened: "+w.String())
	}

	// The texts gnosh says to a model: its own, with the person's
	// replacements. Safe mode leaves the replacements out.
	l.texts = prompt.Builtin()
	if !l.cfg.Switches.SafeMode.V {
		dir := filepath.Join(l.cfg.Paths.ConfigDir, "prompts")
		set, replaced, err := prompt.Load(dir)
		if err != nil {
			return l, fmt.Errorf("your own prompt texts in %s cannot be used, so nothing was started:\n%w", dir, err)
		}
		l.texts = set
		var mine, stale []string
		for _, r := range replaced {
			if r.InForce {
				mine = append(mine, r.Name)
				if r.Stale {
					stale = append(stale, r.Name)
				}
			}
		}
		if len(mine) > 0 {
			text := fmt.Sprintf("%d of gnosh's prompt texts are replaced by your own files in %s: %s.", len(mine), dir, strings.Join(mine, ", "))
			if len(stale) > 0 {
				text += fmt.Sprintf(" gnosh's own text has changed since these were written: %s.", strings.Join(stale, ", "))
			}
			l.notice(agent.LevelWarning, "prompt", text)
		}
	} else if !l.inv.Bare {
		// (Minimal mode is chosen for what it leaves out, and says
		// nothing about it.)
		l.notice(agent.LevelWarning, "config", fmt.Sprintf(
			"Safe mode is on (%s): hooks, instruction files, MCP servers from files and your own prompt texts are off. Start without it to turn them back on.",
			l.cfg.Switches.SafeMode.Origin))
	}
	if l.cfg.HooksOff != "" && !l.cfg.Switches.SafeMode.V {
		l.notice(agent.LevelWarning, "hooks", fmt.Sprintf("%d hooks are configured and none runs: %s.", len(l.cfg.AllHooks), l.cfg.HooksOff))
	}

	// The rules. The engine is made once to say which tools are offered,
	// which the features may ask, and again once they have said which of
	// their own files are never changed without a question.
	if l.engine, err = permission.New(st.permissionOptions(l.cfg, nil)); err != nil {
		return l, fmt.Errorf("the permission rules cannot be applied:\n%w", err)
	}
	l.facts = l.sessionFacts()
	var protected []string
	for _, f := range p.o.Features {
		if f.Session == nil || l.inv.Bare {
			// (Minimal mode has none of the features: no mem.)
			continue
		}
		c, err := f.Session(ctx, l.facts)
		if err != nil {
			return l, fmt.Errorf("%s could not start for this session: %w", firstOf(f.Name, "a feature"), err)
		}
		if c == nil {
			c = &Contribution{}
		}
		l.contribs = append(l.contribs, joined{f: f, c: c})
		protected = append(protected, c.Protected...)
	}
	if len(protected) > 0 {
		if l.engine, err = permission.New(st.permissionOptions(l.cfg, protected)); err != nil {
			return l, fmt.Errorf("the permission rules cannot be applied:\n%w", err)
		}
	}
	for _, note := range l.engine.Explain().Notes {
		l.notice(agent.LevelWarning, "permission", note)
	}
	l.checkHookFilters()
	l.roots = []string{l.cfg.Paths.ProjectRoot}
	for _, d := range l.engine.Dirs() {
		if !slices.Contains(l.roots, d) {
			l.roots = append(l.roots, d)
		}
	}

	// The agent the session runs as, if any: its prompt and its tools.
	if err := l.runAsAgent(); err != nil {
		return l, err
	}

	// The hooks.
	hc, _ := hooksConfig(l.cfg)
	ho := st.hookOptions(l.cfg)
	ho.Background = l.backgroundHook
	ho.MCP = l.mcpHookCall
	for _, c := range l.contribs {
		if c.c.Hooks != nil {
			fires := ho.Fires
			c.c.Hooks(&ho)
			ho.Fires = fires
		}
	}
	runner, err := hooks.NewRunner(hc, ho)
	if err != nil {
		return l, fmt.Errorf("the hooks cannot be run as they are configured:\n%w", err)
	}
	l.hooks = &hookRunner{l: l, r: runner}
	if l.asAgent != nil {
		// The hooks of the kind the session runs as hold for all of it.
		if _, err := l.agentHooks(*l.asAgent, ""); err != nil {
			return l, fmt.Errorf("%v. Nothing was started", err)
		}
	}

	// The tools, the instruction files and the system prompt. What the
	// files the tools change held is kept beside the session's file.
	l.checks = openCheckpoints(filepath.Join(l.st.dirs(l.ts.ID()).Session, "checkpoints"), l.p.now)
	if err := l.buildTools(); err != nil {
		return l, err
	}
	l.allowSpool()
	if err := l.loadInstructions(ctx); err != nil {
		return l, err
	}
	l.agentNotices()
	l.loadStyles()
	system, err := l.systemPrompt()
	l.system = system
	if err != nil {
		return l, err
	}

	// The models.
	var pick func([]string) (string, bool)
	if asker, ok := front.(Asker); ok && l.interactive {
		pick = func(models []string) (string, bool) {
			q := agent.Question{Kind: "model", Title: "Which model?",
				Text: "Several models are available and none is chosen. gnosh does not guess: naming the wrong one makes a model server unload the one in use."}
			for _, m := range models {
				q.Choices = append(q.Choices, agent.Choice{Key: m, Label: m})
			}
			ans, err := asker.AskBefore(q)
			return ans.Choice, err == nil && slices.Contains(models, ans.Choice)
		}
	}
	if err := l.setupModels(ctx, pick); err != nil {
		return l, err
	}
	if err := l.agentModel(ctx); err != nil {
		return l, err
	}

	// The log, and the agent.
	l.log = newSessionLog(l.ts, l.texts)
	l.log.startedWith = "gnosh " + p.o.BuildID
	if name := l.agentName(); name != "" && name != l.storedAgent() {
		l.log.runAs = name
	}
	for _, c := range l.contribs {
		if c.c.Stored != nil {
			l.log.stored = append(l.log.stored, c.c.Stored)
		}
		if c.c.Slid != nil {
			l.log.slid = append(l.log.slid, c.c.Slid)
		}
		if c.c.AfterStored != nil {
			l.log.afterStored = append(l.log.afterStored, c.c.AfterStored)
		}
	}
	l.log.onCut = func() {
		if l.instr != nil {
			l.instr.Forget()
		}
	}
	texts, err := loopTexts(l.texts, slices.Contains(l.toolNames(), gnoRunTool))
	if err != nil {
		return l, err
	}
	opts := agent.Options{
		SessionID:     l.ts.ID(),
		Cwd:           l.cfg.Paths.Cwd,
		Roots:         l.roots,
		Tree:          l.st.treeEnv(),
		Interactive:   l.interactive,
		StartSource:   l.source,
		Model:         l.large.model,
		System:        system,
		Tools:         l.tools,
		Log:           l.log,
		Gate:          &gate{l: l},
		Moments:       l.moments(),
		Drivers:       l.drivers(),
		OnEvent:       func(ev agent.Event) { l.deliver(ev) },
		Retry:         p.o.Retry,
		Repeat:        l.repeatPolicy(),
		CompactAt:     l.compactAt(),
		NoAutoCompact: !l.cfg.Options.AutoCompactEnabled.V,
		StopGrace:     l.stopGrace(),
		LoopMax:       max(l.cfg.Options.LoopMaxTurns.V, 0),
		Texts:         texts,
		Mark:          prompt.Wrap,
		CleanInput:    cleanInput,
		CleanResult:   cleanResult,
		Outgoing:      l.outgoing,
		OnPanic:       l.onPanic,
		Store:         l.toolStore,
		CallID:        newCallID,
	}
	if l.small != nil {
		small := l.small.model
		opts.Small = &small
	}
	a, err := agent.New(opts)
	l.agent = a
	// Published for the code that runs beside the session's opening (a
	// hook's, in the background): l.agent is written here, as they may
	// read it (race detector, 16 November).
	l.agentReady.Store(a)
	if err != nil {
		return l, fmt.Errorf("the session could not be put together: %w", err)
	}
	if l.asAgent != nil && l.inv.Effort == "" {
		l.agentEffort(l.agent, *l.asAgent)
	}
	l.slash = newSlashSet(l)
	for _, c := range l.contribs {
		for _, cmd := range c.c.Slash {
			if err := l.slash.add(cmd); err != nil {
				return l, fmt.Errorf("the feature %q: %w", c.f.Name, err)
			}
		}
	}
	if l.cfg.Switches.DebugPage.V {
		l.debugPage = startDebugPage(l)
	}
	if name := strings.TrimSpace(l.inv.Name); name != "" && l.source != "clear" {
		l.setStatus("session", name)
	}
	for _, c := range l.contribs {
		if c.c.Ready != nil {
			c.c.Ready(l.agent)
		}
	}
	// The MCP servers: their tools are added as each connects.
	l.mcp = l.loadMCP()
	l.startMCP()
	l.startStatusLine()
	l.startInitialPrompt()
	return l, nil
}

// openTranscript opens the session's file: a new one, or the one the
// command line names, or a copy of it.
func (l *live) openTranscript(ctx context.Context) error {
	inv, cwd := l.inv, l.cfg.Paths.Cwd
	if l.reopen != "" {
		// The session that was just closed, again (a rewind), another
		// session of the folder (/resume), or the copy /branch made.
		ts, err := l.store.Open(ctx, l.reopen)
		if err != nil {
			return fmt.Errorf("the session %s could not be opened again: %w", l.reopen, err)
		}
		l.ts = ts
		// (/resume opens a session whose conversation is not on the
		// screen; a rewind's and a branch's is.)
		l.showPast = l.source == "resume"
		if l.source != "fork" {
			l.source = "resume"
		}
		return nil
	}
	if l.source == "clear" || inv.Conversation == cli.ConversationNew || inv.Conversation == "" {
		var err error
		if inv.SessionID != "" && l.source != "clear" {
			l.ts, err = l.store.CreateWithID(cwd, inv.SessionID)
		} else {
			l.ts, err = l.store.Create(cwd)
		}
		if l.source == "" {
			l.source = "startup"
		}
		if err != nil {
			return fmt.Errorf("a new session could not be started: %w", err)
		}
		return l.nameSession()
	}
	if inv.ForkSession && inv.SessionID != "" {
		return errors.New("--session-id cannot name the copy --fork-session makes yet: the copy gets an id of its own. Leave --session-id out, and nothing was started")
	}

	path, id := "", ""
	switch inv.Conversation {
	case cli.ConversationContinue:
		info, ok, err := l.store.Latest(ctx, cwd)
		if err != nil {
			return fmt.Errorf("the sessions of this folder could not be listed: %w", err)
		}
		if !ok {
			return fmt.Errorf("there is no session to continue: none was started in %s (sessions are kept under %s). Start one without -c", cwd, l.store.Dir)
		}
		id = info.ID
	case cli.ConversationResume:
		switch {
		case inv.ResumeIsFile:
			path = inv.Resume
		case inv.Resume == "":
			picked, err := l.pickSession(ctx)
			if err != nil {
				return err
			}
			id = picked
		default:
			found, err := l.findSession(ctx, inv.Resume)
			if err != nil {
				return err
			}
			id = found
		}
	}

	open := func(write bool) (*transcript.Session, error) {
		switch {
		case path != "":
			return l.store.OpenPath(ctx, path, !write)
		case write:
			return l.store.Open(ctx, id)
		}
		return l.store.OpenReadOnly(ctx, id)
	}
	ts, err := open(!inv.ForkSession)
	if err != nil {
		var locked *transcript.LockedError
		switch {
		case errors.As(err, &locked):
			why := fmt.Sprintf("the session %s is open in another gnosh, and one session has one writer. Close that one, or add --fork-session to go on in a copy", locked.ID)
			if log := strings.TrimSuffix(locked.Path, ".jsonl") + ".background.log"; fileExists(log) {
				// (A run /background or /fork started, most likely.)
				why += fmt.Sprintf(". If it is a run in the background, it lets go of the session when it is done; what it prints is in %s", log)
			}
			return errors.New(why)
		case errors.Is(err, transcript.ErrNotFound):
			return fmt.Errorf("no session matches %q (sessions are kept under %s), so nothing was started", firstOf(id, path), l.store.Dir)
		}
		return fmt.Errorf("the session could not be opened: %w", err)
	}
	l.source = "resume"
	if inv.ForkSession {
		copied, err := l.store.Branch(ts, "")
		ts.Close()
		if err != nil {
			return fmt.Errorf("the session could not be copied: %w", err)
		}
		ts, l.source = copied, "fork"
	}
	l.ts = ts
	// (Not for a gnosh /restart started: the session it opens is on the
	// screen already.)
	l.showPast = l.source == "resume" && !l.p.restarted
	return l.nameSession()
}

// nameSession gives the session the name -n asks for. A name the person
// gave is never replaced by a generated one.
func (l *live) nameSession() error {
	if l.inv.Name == "" || l.source == "clear" {
		return nil
	}
	if _, err := l.ts.Append(transcript.TitleEntry(l.inv.Name, transcript.SourceUser)); err != nil {
		return fmt.Errorf("the session could not be named: %w", err)
	}
	return nil
}

// findSession finds the session -r names: by its id or the start of it,
// or by its name, ignoring letter case and the blanks around it.
func (l *live) findSession(ctx context.Context, word string) (string, error) {
	if _, err := l.store.Path(word); err == nil {
		return word, nil
	} else {
		var ambiguous *transcript.AmbiguousError
		if errors.As(err, &ambiguous) {
			return "", fmt.Errorf("%q is the start of more than one session's id (%s). Give more of it", word, strings.Join(ambiguous.IDs, ", "))
		}
	}
	list, err := l.store.List(ctx, l.cfg.Paths.Cwd)
	if err != nil {
		return "", fmt.Errorf("the sessions of this folder could not be listed: %w", err)
	}
	var matches []transcript.Info
	for _, info := range list {
		if info.Title != "" && strings.EqualFold(strings.TrimSpace(info.Title), strings.TrimSpace(word)) {
			matches = append(matches, info)
		}
	}
	switch len(matches) {
	case 0:
		return "", fmt.Errorf("no session has the id or the name %q (sessions are kept under %s), so nothing was started", word, l.store.Dir)
	case 1:
		return matches[0].ID, nil
	}
	var lines []string
	for _, m := range matches {
		lines = append(lines, fmt.Sprintf("  %s  (last written %s)", m.ID, m.Modified.Local().Format("2006-01-02 15:04")))
	}
	return "", fmt.Errorf("%d sessions are named %q. Resume one by its id:\n%s", len(matches), word, strings.Join(lines, "\n"))
}

// pickSession shows the folder's sessions to pick from, for -r with
// nothing after it.
func (l *live) pickSession(ctx context.Context) (string, error) {
	asker, ok := l.front.(Asker)
	if !ok {
		return "", errors.New("-r needs a session's id or name after it here, where nobody can pick from a list")
	}
	list, err := l.store.List(ctx, l.cfg.Paths.Cwd)
	if err != nil {
		return "", fmt.Errorf("the sessions of this folder could not be listed: %w", err)
	}
	if len(list) == 0 {
		return "", fmt.Errorf("there is no session to resume: none was started in %s", l.cfg.Paths.Cwd)
	}
	q := agent.Question{Kind: "session", Title: "Which session?", Text: "The sessions of this folder, the one written to last first."}
	for i, info := range list {
		if i == 20 {
			break
		}
		what := firstOf(info.Title, info.FirstPrompt, "(no prompt yet)")
		q.Choices = append(q.Choices, agent.Choice{Key: info.ID, Label: fmt.Sprintf("%s  %s  %s", info.ID[:8], info.Modified.Local().Format("2006-01-02 15:04"), oneLine(what, 80))})
	}
	ans, err := asker.AskBefore(q)
	if err != nil || ans.Choice == "" {
		return "", errors.New("no session was picked, so nothing was started")
	}
	return ans.Choice, nil
}

// sessionFacts is the session as a feature sees it.
func (l *live) sessionFacts() *Session {
	return &Session{
		ID:          l.ts.ID(),
		Interactive: l.interactive,
		Source:      l.source,
		Trusted:     l.st.trusted,
		Config:      l.cfg,
		Invocation:  l.inv,
		Dirs:        l.st.dirs(l.ts.ID()),
		Build:       l.p.o.BuildID,
		Sessions:    l.store,
		Transcript:  l.ts,
		Texts:       l.texts,
		Env:         l.env,
		Offered:     func(tool string) bool { return l.engine.ToolEnabled(tool) },
		Agent:       func() *agent.Agent { return l.agent },
		Notice:      l.featureNotice,
		Tasks:       l.setTasks,
		Status:      l.setStatus,
		Mode:        func() string { return string(l.engine.Mode()) },
		Getenv:      l.p.getenv,
		Now:         l.p.now,

		Deliverables: l.setDeliverables,
		Shell:        func() *shelltool.Manager { return l.shell },
	}
}

// setStatus is Session.Status: a named fact for a front end's status line
// is set, or with an empty value taken off, and the front end is told when
// it changed.
func (l *live) setStatus(name, value string) {
	if name == "" {
		return
	}
	l.statusMu.Lock()
	if l.status == nil {
		l.status = map[string]string{}
	}
	same := l.status[name] == value
	if value == "" {
		delete(l.status, name)
	} else {
		l.status[name] = value
	}
	l.statusMu.Unlock()
	if !same {
		l.deliver(&StatusEvent{Facts: map[string]string{name: value}})
	}
}

// statusFacts returns the facts for a status line as they stand.
func (l *live) statusFacts() map[string]string {
	l.statusMu.Lock()
	defer l.statusMu.Unlock()
	out := make(map[string]string, len(l.status))
	for k, v := range l.status {
		out[k] = v
	}
	return out
}

// noteBackground keeps the count of commands that run in the background
// among the facts for a status line.
func (l *live) noteBackground() {
	l.bgEver.Store(true)
	// The commands and the sub-agents that run in the background.
	running := len(l.backgroundAgents())
	if l.shell != nil {
		for _, t := range l.shell.Tasks() {
			if t.Running {
				running++
			}
		}
	}
	value := ""
	if running > 0 {
		value = strconv.Itoa(running)
	}
	l.setStatus("background", value)
}

// markTold records that the features have been told how a turn ended.
func (l *live) markTold(turn string) {
	l.toldMu.Lock()
	defer l.toldMu.Unlock()
	if turn != "" {
		l.told = append(l.told, turn)
		if len(l.told) > 8 {
			l.told = l.told[len(l.told)-8:]
		}
	}
	if l.toldCh != nil {
		close(l.toldCh)
	}
	l.toldCh = make(chan struct{})
}

// awaitTold waits until the features have been told how a turn ended, or
// until ctx ends. The loop tells the front end of a turn's end first
// (TurnEndEvent) and the features a moment after (TurnEnded), so a front
// end that acts on the event the moment it has it could be ahead of them.
func (l *live) awaitTold(ctx context.Context, turn string) {
	if turn == "" {
		// No turn ran: there was nothing to tell.
		return
	}
	tick := time.NewTicker(20 * time.Millisecond)
	defer tick.Stop()
	past := false
	for {
		l.toldMu.Lock()
		told := slices.Contains(l.told, turn)
		if l.toldCh == nil {
			l.toldCh = make(chan struct{})
		}
		ch := l.toldCh
		l.toldMu.Unlock()
		if told || past {
			return
		}
		select {
		case <-ch:
		case <-ctx.Done():
			return
		case <-tick.C:
			// A session that has gone idle, or on to another turn, is
			// past this turn's end. The features are told before either,
			// so if they have not been by now they will not be: the
			// writing of the turn's end crashed.
			st := l.agent.State()
			past = !st.Busy || (st.Turn != "" && st.Turn != turn)
		}
	}
}

// featureNotice is Session.Notice: a line a feature has for the person.
// It is shown and kept in the session's file like any notice; the log is
// told only that there was one.
func (l *live) featureNotice(level agent.Level, source, text string) {
	l.featureSources.Store(source, true)
	l.notice(level, source, text)
}

// setTasks is Session.Tasks: the session's task list is replaced, and the
// front end is told when it changed.
func (l *live) setTasks(tasks []Task) {
	tasks = slices.Clone(tasks)
	l.tasksMu.Lock()
	same := slices.Equal(l.tasks, tasks)
	l.tasks = tasks
	l.tasksMu.Unlock()
	if !same {
		l.deliver(&TasksEvent{Tasks: slices.Clone(tasks)})
	}
}

// taskList returns the session's task list as it stands.
func (l *live) taskList() []Task {
	l.tasksMu.Lock()
	defer l.tasksMu.Unlock()
	return slices.Clone(l.tasks)
}

// setMode changes the permission mode and tells the features.
func (l *live) setMode(m permission.Mode) error {
	before := l.engine.Mode()
	if err := l.engine.SetMode(m); err != nil {
		return err
	}
	if now := l.engine.Mode(); now != before {
		l.flog.logf("permission", "the permission mode is now %s (it was %s)", now, before)
		l.statusLine.poke()
		told := slices.Clone(l.contribs)
		l.subMu.Lock()
		for s := range l.subs {
			told = append(told, s.contribs...)
		}
		l.subMu.Unlock()
		for _, c := range told {
			if c.c.ModeChanged != nil {
				func() {
					defer func() { _ = recover() }()
					c.c.ModeChanged(string(now))
				}()
			}
		}
	}
	return nil
}

// cwd is the session's working directory as it stands.
func (l *live) cwd() string {
	if l.agent != nil {
		return l.agent.Cwd()
	}
	return l.cfg.Paths.Cwd
}

// env is what a tool call of the session is given to work with, as it
// stands now.
func (l *live) env() *core.ToolEnv {
	return &core.ToolEnv{Cwd: l.cwd(), Roots: slices.Clone(l.roots), SessionID: l.ts.ID(), Store: l.toolStore}
}

// envFile is the session's environment file: SessionStart hooks write
// export lines into it (as CLAUDE_ENV_FILE and GNOSH_ENV_FILE), and every
// later command on this machine runs it first. It is in the session's own
// folder, and is there only once a hook has written it.
func (l *live) envFile() string {
	dir := l.st.dirs(l.ts.ID()).Session
	// The folder is made here: a hook may write the file before anything
	// else of the session's is kept there.
	_ = os.MkdirAll(dir, 0o700)
	return filepath.Join(dir, "env.sh")
}

// spoolDir is where the session's commands keep their output: a folder of
// the session's own, beside its file.
func (l *live) spoolDir() string {
	return filepath.Join(l.st.dirs(l.ts.ID()).Session, "tool-results")
}

// allowSpool lets the model read the output files of its own commands
// without a question: a command that moved to the background, or printed
// more than the model is shown, leaves its output in such a file, which
// lies outside the project.
func (l *live) allowSpool() {
	dir := l.spoolDir()
	rule := "Read(/" + filepath.ToSlash(dir) + "/**)"
	err := l.engine.AddLayer(permission.Layer{
		Scope: permission.ScopeSession,
		Name:  "the output files of the session's own commands",
		Allow: []string{rule},
	})
	if err != nil {
		l.flog.logf("permission", "the rule %s could not be added (%v); reading a command's output file will ask", rule, err)
	}
}

// newCallID makes the id a tool call goes by: "call_" and 24 hex digits
// drawn at random. No two calls ever share one, in this session, in
// another session of the project, or on another machine, which is what
// lets what is kept beside a project (its mem, its frames) name a call by
// its id alone. Every provider takes such an id back.
func newCallID() string {
	var b [12]byte
	if _, err := rand.Read(b[:]); err != nil {
		// The loop then keeps the provider's own id, made unique within
		// the session.
		return ""
	}
	return "call_" + hex.EncodeToString(b[:])
}

// stopGrace is how long a stopped turn waits for a tool call that has not
// come back: what the program was started with, or the longest time a
// feature asks for its own tools, whichever is longer. Zero leaves it to
// the loop's own ten seconds.
func (l *live) stopGrace() time.Duration {
	grace := l.p.o.StopGrace
	for _, c := range l.contribs {
		if c.c.StopGrace > 10*time.Second && c.c.StopGrace > grace {
			grace = c.c.StopGrace
		}
	}
	return grace
}

// compactAt is where room is made in the model's window when the person
// asked for it earlier than gnosh's own point: the auto-compact window,
// and a percentage of it.
func (l *live) compactAt() int {
	at := 0
	if w := l.cfg.Options.AutoCompactWindow; w.Set {
		at = w.V
	}
	return l.compactAtWindow(at)
}

// compactAtWindow is compactAt for an auto-compact window of at tokens
// (zero for none).
func (l *live) compactAtWindow(at int) int {
	if pct := l.cfg.Limits.AutoCompactPct; pct.Set {
		base := at
		if base == 0 {
			base = l.large.model.Info.ContextWindow
		}
		if base > 0 {
			at = base * pct.V / 100
		}
	}
	return at
}

// repeatPolicy lets the features say how their tools' calls are compared
// when the loop looks for a model repeating itself, and what the model is
// told then.
func (l *live) repeatPolicy() agent.RepeatPolicy {
	var keys []func(core.ToolCall, core.ToolResult) (string, bool)
	var nudges []func(agent.Repetition) string
	for _, c := range l.contribs {
		if c.c.RepeatKey != nil {
			keys = append(keys, c.c.RepeatKey)
		}
		if c.c.RepeatNudge != nil {
			nudges = append(nudges, c.c.RepeatNudge)
		}
	}
	var pol agent.RepeatPolicy
	if len(keys) > 0 {
		pol.Key = func(call core.ToolCall, res core.ToolResult) string {
			for _, key := range keys {
				if k, ok := key(call, res); ok {
					return k
				}
			}
			failed := "ok"
			if res.IsError {
				failed = "error"
			}
			return call.Name + "\x00" + string(call.Input) + "\x00" + failed + "\x00" + core.TextOf(res.Content)
		}
	}
	if len(nudges) > 0 {
		pol.Nudge = func(rep agent.Repetition) string {
			for _, nudge := range nudges {
				if text := nudge(rep); text != "" {
					return text
				}
			}
			return ""
		}
	}
	return pol
}

// deliver hands an event to the front end, one at a time, and keeps a line
// of it in the log.
func (l *live) deliver(ev any) {
	l.evMu.Lock()
	l.summaryEnd(ev)
	l.flog.event(ev)
	if l.front != nil {
		l.front.Event(ev)
	}
	l.evMu.Unlock()
	l.loadMoment(ev)
	l.statusPoke(ev)
	// A command may have moved to the background, or one there may have
	// been stopped.
	if te, ok := ev.(*agent.ToolEndEvent); ok && (te.Name == shelltool.BashName || te.Name == shelltool.StopName) {
		l.noteBackground()
	}
}

// notice puts a line in front of the person. Once the agent exists the
// line is kept in the session's file too.
func (l *live) notice(level agent.Level, source, text string) {
	if text == "" {
		return
	}
	if a := l.agent; a != nil && !l.closing.Load() {
		a.Notice(level, source, text)
		return
	}
	l.deliver(&agent.NoticeEvent{Meta: agent.Meta{Time: l.p.now(), Session: l.sessionID()}, Level: level, Source: source, Text: text})
}

// sessionID is the session's id, or "" before its file is open.
func (l *live) sessionID() string {
	if l.ts == nil {
		return ""
	}
	return l.ts.ID()
}

// background runs f beside the session, under a context that ends when the
// session is closed. It is for what nobody waits for.
func (l *live) background(f func(ctx context.Context)) {
	if l.closing.Load() {
		return
	}
	l.bg.Add(1)
	go func() {
		defer l.bg.Done()
		defer func() { _ = recover() }()
		f(l.ctx)
	}()
}

// onPanic is told when a tool, a port or the loop itself panicked. The
// loop carries on; what happened is kept in the crash file.
func (l *live) onPanic(what string, v any, stack []byte) {
	l.flog.logf("crash", "%s panicked: %v", what, v)
	if path := l.crash.record(what, v, stack); path != "" {
		l.notice(agent.LevelError, "crash", fmt.Sprintf("%s crashed (%v). gnosh goes on; the details are in %s.", what, v, path))
	}
}

// info describes the session to its front end.
func (l *live) info() SessionInfo {
	m := l.agent.Model()
	info := SessionInfo{
		ID:           l.ts.ID(),
		Path:         l.ts.Path(),
		Cwd:          l.cfg.Paths.Cwd,
		Model:        m.Info.ID,
		Address:      m.Address,
		Thinking:     m.Level,
		Tools:        l.toolNames(),
		MCPServers:   l.mcpStatus(),
		Mode:         string(l.engine.Mode()),
		Commands:     l.slash.names(),
		CommandList:  l.slash.list(),
		Skills:       append(l.typedSkills(), l.mcpPromptInfos()...),
		Version:      l.p.o.BuildID,
		Interactive:  l.interactive,
		SidePanel:    l.cfg.Options.SidePanel.V,
		Verbose:      l.cfg.Options.Verbose.V,
		Vim:          l.cfg.Options.EditorMode.V == "vim",
		OutputStyle:  l.styleNow(),
		Color:        l.sessionColor(),
		Keybindings:  l.keybindingsPath(),
		ShowPast:     l.showPast,
		NotifChannel: l.notifChannel(),
		Resume:       "gnosh -r " + l.ts.ID(),
	}
	for _, it := range l.cfg.Options.VimInsertModeRemaps {
		info.VimRemaps = append(info.VimRemaps, it.V)
	}
	if m.Provider != nil {
		info.Provider = m.Provider.Name()
	}
	info.NotifyHooks = l.hooks.has(hooks.Notification)
	return info
}

// kill leaves the session's file exactly as a killed process would: it is
// closed at once, so that nothing more is written, whatever was under
// way.
func (l *live) kill() {
	l.killed.Store(true)
	if l.ts != nil {
		_ = l.ts.Close()
	}
}

// close ends the session, in the order the specs give: the turn is
// stopped; every command the session started is stopped, with what those
// commands started; the SessionEnd hooks run; the features are told, in
// reverse order; and the session's file is flushed to disk and its lock
// let go. It is safe to call more than once.
func (l *live) close(reason string) {
	l.closeOnce.Do(func() {
		l.closing.Store(true)
		if l.agent != nil {
			// End stops the turn and then tells the SessionEnd moment,
			// which closes the shell and runs the hooks.
			_ = l.agent.End(reason)
		}
		// The sub-agents in the background stop first: they use the shell.
		l.stopBackgroundAgents(15 * time.Second)
		l.closeShell()
		if l.hooks != nil && l.hooks.r != nil {
			// The hooks still running in the background are stopped.
			l.hooks.r.Close(10 * time.Second)
		}
		l.statusLine.stop()
		l.cancel()
		waitGroup(&l.bg, 15*time.Second)
		l.closeMCP()
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		for _, c := range slices.Backward(l.contribs) {
			if c.c.Close == nil {
				continue
			}
			func() {
				defer func() { _ = recover() }()
				c.c.Close(ctx)
			}()
		}
		cancel()
		if l.debugPage != nil {
			l.debugPage.close()
		}
		if l.ts != nil {
			_ = l.ts.Sync()
			_ = l.ts.Close()
		}
		l.flog.logf("shutdown", "session %s closed (%s)", l.sessionID(), reason)
		l.crash.close()
		l.flog.close()
	})
}

// waitGroup waits for wg, but not for ever.
func waitGroup(wg *sync.WaitGroup, limit time.Duration) {
	done := make(chan struct{})
	go func() {
		wg.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(limit):
	}
}

// controls is a session as its front end drives it. In an interactive
// session /clear puts a new session behind the same controls.
var _ SessionControls = (*controls)(nil)

type controls struct {
	p     *program
	st    *startup
	front Frontend

	mu     sync.Mutex
	l      *live
	reason string
}

func (c *controls) cur() *live {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.l
}

// Submit sends a prompt.
func (c *controls) Submit(p agent.Prompt) (string, error) {
	l := c.cur()
	if !p.Hidden {
		// The rules a skill brought with it hold until the person's next
		// prompt, which this is.
		l.dropSkillGrants()
		l.nameAfter(p.Text)
	}
	return l.agent.Submit(p)
}

// Interrupt stops the turn that is running, and the command of the
// person's own that is running, if one is.
func (c *controls) Interrupt() {
	l := c.cur()
	l.shellMu.Lock()
	stop := l.shellStop
	l.shellMu.Unlock()
	if stop != nil {
		stop()
	}
	l.agent.Interrupt()
}

// Answer gives the person's answer to a question.
func (c *controls) Answer(question string, a agent.Answer) error {
	return c.cur().agent.Answer(question, a)
}

// Queued lists the prompts that wait for their turn.
func (c *controls) Queued() []agent.QueuedPrompt { return c.cur().agent.Queued() }

// TakeQueued takes the person's own prompts off the queue.
func (c *controls) TakeQueued() []agent.Prompt { return c.cur().agent.TakeQueued() }

// Mode returns the permission mode in force.
func (c *controls) Mode() string { return string(c.cur().engine.Mode()) }

// SetMode changes the permission mode.
func (c *controls) SetMode(mode string) error {
	l := c.cur()
	m, fell, err := permission.ParseMode(mode)
	switch {
	case err != nil:
		return err
	case fell:
		return errors.New("gnosh has no auto mode: it needs a second model to review each action")
	case m == permission.ModeBypass && !l.cfg.Permissions.BypassAvailable:
		return errors.New("skipping permissions can only be switched to in a session that was started with it (-y) or with it within reach (--allow-dangerously-skip-permissions)")
	}
	return l.setMode(m)
}

// Command runs a line that begins with a slash as a command.
func (c *controls) Command(ctx context.Context, line string) (CommandResult, error) {
	l := c.cur()
	if l.inv.DisableSlashCommands {
		return CommandResult{}, nil
	}
	res, err := l.slash.run(ctx, line)
	if err == nil && res.Handled && res.Name == "clear" {
		if cerr := c.clear(ctx); cerr != nil {
			res.Quit = true
			return res, cerr
		}
		res.Reset = true
		res.Text = "A new session is open: " + c.cur().ts.ID() + ". The one before is kept; " + l.info().Resume + " opens it again."
		if name, byPerson := l.title(); byPerson && name != "" {
			res.Text = "A new session is open: " + c.cur().ts.ID() + ". The one before is kept as " + strconv.Quote(name) + "; gnosh -r " + strconv.Quote(name) + " opens it again."
		}
	}
	if point := l.rewindTo; err == nil && res.Handled && res.Name == "rewind" && point != "" {
		all := l.rewindAll
		l.rewindTo, l.rewindAll = "", false
		files := ""
		if all {
			// The files first, while the session that changed them is the
			// one behind the controls.
			done, failed := l.checks.restore(l.rewindFiles(point))
			l.flog.logf("turn", "the files were put back as they were before the prompt in entry %s: %d put back, %d not", point, len(done), len(failed))
			files = restoreSaid(done, failed)
		}
		text, rerr := c.Rewind(ctx, point)
		if rerr != nil {
			if files != "" {
				return res, fmt.Errorf("%s But the conversation could not be taken back: %w", files, rerr)
			}
			return res, rerr
		}
		res.Reset, res.Draft = true, text
		res.Text = "The conversation is back to before this prompt of yours, which you can send again or change:\n" + text
		if files != "" {
			res.Text += "\n" + files
		} else {
			res.Text += "\nThe files stay as they are now. " + rewindNote
		}
	}
	if id := l.resumeTo; err == nil && res.Handled && res.Name == "resume" && id != "" {
		l.resumeTo = ""
		said, rerr := c.resume(ctx, id)
		if rerr != nil {
			return res, rerr
		}
		res.Reset, res.Text = true, said
	}
	if name := l.branchTo; err == nil && res.Handled && res.Name == "branch" && name != nil {
		l.branchTo = nil
		said, berr := c.branch(ctx, *name)
		if berr != nil {
			return res, berr
		}
		res.Reset, res.Text = true, said
	}
	return res, err
}

// SetVimMode tells the session the mode of the box's vim keys ("NORMAL",
// "INSERT", "VISUAL", "VISUAL LINE", or "" when they are off), for the
// status line command, which is told it as vim.mode and runs again when
// it changes, as Claude Code's does.
func (c *controls) SetVimMode(mode string) { c.cur().setVimMode(mode) }

// clear ends the session with the reason "clear" and opens a new one
// behind the same controls.
func (c *controls) clear(ctx context.Context) error {
	old := c.cur()
	old.close("clear")
	next, err := c.p.openSession(ctx, c.st, c.front, "clear")
	if err != nil {
		return fmt.Errorf("the session was closed, and a new one could not be opened: %w", err)
	}
	c.mu.Lock()
	c.l = next
	c.mu.Unlock()
	return nil
}

// State says what the session is doing right now.
func (c *controls) State() agent.State { return c.cur().agent.State() }

// Context says how full the model's window is.
func (c *controls) Context() agent.ContextStatus { return c.cur().agent.Context() }

// Info describes the session.
func (c *controls) Info() SessionInfo { return c.cur().info() }

// Usage returns what the session has cost so far.
func (c *controls) Usage() agent.Totals { return c.cur().agent.Usage() }

// Signals delivers the signals the process receives.
func (c *controls) Signals() <-chan os.Signal { return c.p.o.Signals }

// Notify fires the person's Notification hooks, and does not wait for
// them.
func (c *controls) Notify(kind, title, message string) {
	l := c.cur()
	if !l.hooks.has(hooks.Notification) || (kind != "permission_prompt" && kind != "idle_prompt") {
		return
	}
	in := hooks.Input{Event: hooks.Notification, NotificationType: kind, Title: title, Message: message}
	l.background(func(ctx context.Context) { _, _ = l.hooks.run(ctx, in) })
}

// RunEnd asks the features whether a run's exit code should be raised. It
// asks once they have been told how the run's last turn ended, and it
// returns once the front end has been told everything they had to say:
// what the front end prints next comes after it.
func (c *controls) RunEnd(res agent.TurnResult, exit int) int {
	l := c.cur()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	l.awaitTold(ctx, res.Turn)
	for _, j := range l.contribs {
		if j.c.RunEnd == nil {
			continue
		}
		if code := j.c.RunEnd(ctx, RunEnd{Result: res, Exit: exit}); code > exit {
			exit = code
		}
	}
	_ = l.agent.Drain(ctx)
	return exit
}

// Wait waits until the session has nothing in hand and the front end has
// been told everything that happened so far.
func (c *controls) Wait(ctx context.Context) error {
	l := c.cur()
	if err := l.agent.Wait(ctx); err != nil {
		return err
	}
	return l.agent.Drain(ctx)
}

// Tasks returns the session's task list.
func (c *controls) Tasks() []Task { return c.cur().taskList() }

// Status returns the facts for a status line.
func (c *controls) Status() map[string]string { return c.cur().statusFacts() }

// Deliverables returns the session's list of deliverables.
func (c *controls) Deliverables() []Deliverable { return c.cur().deliverables() }

// BackgroundWork lists what still runs in the background of the session,
// each as its id and what it is for: its background commands and its
// sub-agents in the background. ever reports whether the session ever
// had any.
func (c *controls) BackgroundWork() (running []string, ever bool) {
	l := c.cur()
	running = l.runningWork()
	return running, l.shell != nil && len(l.shell.Tasks()) > 0 || len(running) > 0 || l.bgEver.Load()
}

// CheckBackground hands the model a checkpoint in a run that has waited
// long for background work: a note that wakes it, says what still runs
// and for how long the run has waited, and asks whether to stop that work
// or to wait on.
func (c *controls) CheckBackground(waited time.Duration, work []string) {
	l := c.cur()
	if l.agent == nil || l.closing.Load() {
		return
	}
	_ = l.agent.Tell(agent.Note{
		Text:   l.texts.PrintWait(roundTime(waited), strings.Join(work, ", ")),
		Source: "print-wait",
		Notice: "The run has waited " + roundTime(waited) + " for background work; the model is asked whether to go on waiting.",
		Wake:   true,
	})
}

// StopBackground stops everything that runs in the background of the
// session: its background commands and its sub-agents.
func (c *controls) StopBackground() {
	l := c.cur()
	l.bgUntold.Store(true)
	if l.shell != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		for _, t := range l.shell.Tasks() {
			if t.Running {
				_, _ = l.shell.Stop(ctx, t.ID)
			}
		}
		cancel()
	}
	l.stopBackgroundAgents(10 * time.Second)
}

// StopBackgroundAgents stops the session's sub-agents that run in the
// background (the interface's Ctrl+X Ctrl+K, as Claude Code's), and says
// how many there were. Background commands go on.
func (c *controls) StopBackgroundAgents() int {
	l := c.cur()
	n := len(l.backgroundAgents())
	if n > 0 {
		l.stopBackgroundAgents(10 * time.Second)
	}
	return n
}

// Background moves the command a tool call waits for to the background.
func (c *controls) Background(callID string) bool {
	l := c.cur()
	return l.shell != nil && callID != "" && l.shell.Background(callID)
}

// Withdraw takes one of the person's queued prompts off the queue.
func (c *controls) Withdraw(id string) (agent.Prompt, bool) { return c.cur().agent.Withdraw(id) }

// History returns the prompts the person sent from this folder.
func (c *controls) History() []string { return c.cur().promptHistory() }

// Shell runs a command line of the person's own.
func (c *controls) Shell(command string) error { return c.cur().shellLine(command) }

// RunPrompt asks the features for the first prompt of a run that was given
// none.
func (c *controls) RunPrompt(ctx context.Context) (agent.Prompt, bool) {
	for _, j := range c.cur().contribs {
		if j.c.RunPrompt == nil {
			continue
		}
		if p, ok := j.c.RunPrompt(ctx); ok {
			return p, true
		}
	}
	return agent.Prompt{}, false
}

// Quit says why the session ends. The front end then returns from Run.
func (c *controls) Quit(reason string) {
	c.mu.Lock()
	c.reason = reason
	c.mu.Unlock()
}

// close ends the session behind the controls.
func (c *controls) close() {
	c.mu.Lock()
	l, reason := c.l, c.reason
	c.mu.Unlock()
	l.close(reason)
}

// sessionCommand is `gnosh`, `gnosh "prompt"` and `gnosh -p`: it settles
// the folder and its config, opens the session, hands it to its front end,
// and closes it.
func (p *program) sessionCommand(ctx context.Context, inv cli.Invocation) (code int) {
	if inv.PrintImplied && p.env["GNOSH"] == "1" {
		// A command a gnosh session runs (its shells set GNOSH=1) that
		// names gnosh without -p is a model that took gnosh for a program
		// of some other kind (gnosh gnovm): a run of its own here would
		// answer its words, saying no to every question, beside the
		// session that started it and on the same model.
		fmt.Fprintln(p.stderr, `gnosh: in a command a gnosh session runs, gnosh answers a prompt only with -p (gnosh -p "..."), so nothing ran. gnosh help lists its commands.`)
		return 1
	}
	interactive := inv.Command == cli.CmdInteractive
	var front Frontend
	switch {
	case !interactive:
		front = newPrinter(p, inv)
	case p.o.Interactive != nil:
		info := FrontendInfo{Stdin: p.o.Stdin, Stdout: p.stdout, Stderr: p.stderr, Prompt: inv.Prompt, Getenv: p.getenv}
		info.In, _ = p.o.Stdin.(*os.File)
		info.Out, _ = p.o.Stdout.(*os.File)
		front = p.o.Interactive(info)
	}
	if front == nil {
		// No other interface was attached, or the one that was declined:
		// it has no terminal it could draw on.
		front = newPlain(p, inv)
	}

	// A front end that cannot be typed into is treated as having nobody
	// to ask: the session then runs as one nobody attends.
	if pl, ok := front.(*plain); ok && !pl.canAsk() {
		interactive = false
	}
	var ask []func(trust.Question) (yes, quit bool)
	if asker, ok := front.(Asker); ok && interactive {
		ask = append(ask, func(q trust.Question) (bool, bool) {
			ans, err := asker.AskBefore(agent.Question{
				Kind:  "trust",
				Title: "Trust this folder?",
				Text:  q.String(),
				Choices: []agent.Choice{
					{Key: "no", Label: "No: start with my own settings only"},
					{Key: "yes", Label: "Yes: use this folder's settings"},
				},
			})
			return ans.Choice == "yes", err != nil
		})
	}
	st, code := p.prepare(ctx, inv, interactive, ask...)
	if st == nil {
		return code
	}
	// --worktree's work tree goes, or is kept, once the session has ended.
	defer st.leaveTree()
	st.attended = interactive
	if st.printProblems() {
		return cli.ExitFailure
	}
	if st.decision.Notice != "" {
		fmt.Fprintln(p.stderr, st.decision.Notice)
	}
	if inv.JSONSchema != "" {
		if _, err := jsonschema.Compile(inv.JSONSchema); err != nil {
			p.errf("--json-schema is not a valid JSON Schema: %v, so nothing was started.", strings.TrimRight(err.Error(), "."))
			return cli.ExitFailure
		}
	}
	if inv.Agents != "" {
		defs, err := p.cliAgents(inv.Agents)
		if err != nil {
			p.errf("--agents: %v, so nothing was started.", strings.TrimRight(err.Error(), "."))
			return cli.ExitFailure
		}
		st.agents = defs
	}
	if inv.NoSessionPersistence {
		// Nothing of the run is kept: its session lives in a folder of
		// its own, which goes once the session is closed.
		dir, err := os.MkdirTemp(p.getenv("TMPDIR"), "gnosh-run-")
		if err != nil {
			p.errf("a folder for the run's session could not be made, so nothing was started: %v", err)
			return cli.ExitFailure
		}
		st.runState = dir
		defer os.RemoveAll(dir)
	}

	l, err := p.openSession(ctx, st, front, "")
	if err != nil {
		p.errf("%v", err)
		return cli.ExitFailure
	}
	c := &controls{p: p, st: st, front: front, l: l, reason: "other"}
	p.setLastStop(func() { c.cur().closeShell() })
	defer func() {
		if v := recover(); v != nil {
			code = p.crashed(c.cur().crashDir(), v, debug.Stack())
			c.cur().kill()
		}
		p.beginClosing(code)
		c.close()
	}()

	done := make(chan int, 1)
	go func() {
		defer func() {
			if v := recover(); v != nil {
				done <- p.crashed(c.cur().crashDir(), v, debug.Stack())
			}
		}()
		done <- front.Run(ctx, c)
	}()
	select {
	case code = <-done:
	case <-ctx.Done():
		// The process is being torn down: the session's file is left as a
		// kill would leave it, and then everything is stopped.
		c.cur().kill()
		select {
		case code = <-done:
		case <-time.After(30 * time.Second):
		}
		if code == cli.ExitOK {
			code = cli.ExitFailure
		}
	}
	return code
}

// notifChannel is the notification channel in force: what /config set
// while the session runs, else the configuration's.
func (l *live) notifChannel() string {
	l.notifMu.Lock()
	defer l.notifMu.Unlock()
	if l.notif != "" {
		return l.notif
	}
	if l.cfg == nil {
		return "auto"
	}
	return firstOf(l.cfg.Options.PreferredNotifChannel.V, "auto")
}
