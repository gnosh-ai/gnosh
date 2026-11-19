package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/gnosh-ai/gnosh/internal/app/agentdef"
	"github.com/gnosh-ai/gnosh/internal/core"
	"github.com/gnosh-ai/gnosh/internal/transcript"
)

// This file is SendMessage: the model gives a sub-agent it started, and
// that has ended, more to do (spec/cc/subagents.md 9). The sub-agent runs
// again in the background under the same id, with everything it had from
// its runs before (its session file is its history) and the tools of its
// kind, and its report comes as a notice, as a background sub-agent's
// does. Explore and Plan are one-shot, as Claude Code's are; a sub-agent
// that was stopped or ended for good is not taken up again; and one that
// worked in a git work tree of its own is not, since the work tree is not
// opened again. The call is judged as starting a sub-agent of its kind is
// (resumeAsStart in gate.go), and is offered only where Agent is.

// sendMessageName is the tool's name, as Claude Code's.
const sendMessageName = "SendMessage"

// The meta keys of a sub-agent's session file that SendMessage reads.
const (
	metaAgentType = "agentType" // its kind
	metaEnded     = "ended"     // how its last run ended: completed, partial, failed, halted, stopped
	metaWorkTree  = "workTree"  // the git work tree of its own it worked in
	metaModel     = "model"     // the model its Agent call named, when it named one
	metaEffort    = "effort"    // how hard it thought, when its call or its kind said
)

// sendMessageTool is SendMessage.
type sendMessageTool struct{ l *live }

// sendMessageInput is what a SendMessage call is given.
type sendMessageInput struct {
	To      string `json:"to"`
	Message string `json:"message"`
	Summary string `json:"summary"`
}

// resumable reports whether a sub-agent of the kind def can be given more
// to do once it ended.
func resumable(def agentdef.Def) bool {
	return def.Name != "Explore" && def.Name != "Plan" && def.Isolation == ""
}

// takesMore reports whether a sub-agent of the kind def whose last run
// ended as ended (its status) is given more to do: its report then ends
// with its id, and SendMessage takes it up. One that was stopped is not,
// nor one ended for good (halted).
func takesMore(def agentdef.Def, ended string) bool {
	switch ended {
	case "completed", "partial", "failed":
		return resumable(def)
	}
	return false
}

// subagentsDir is the folder of the session's sub-agents' session files.
func (l *live) subagentsDir() string {
	return filepath.Join(l.st.dirs(l.ts.ID()).Session, "subagents")
}

// Spec says what the model is told about the tool.
func (t *sendMessageTool) Spec() core.ToolSpec {
	return core.ToolSpec{
		Name: sendMessageName,
		Description: "Gives a sub-agent you started, which has ended, more to do: it works on your message in the background with everything it knew from before, and its report comes to you as a notice from gnosh. " +
			"to is its id, as its report gave it (agent id: ...). Explore and Plan sub-agents cannot be given more, nor one that was stopped or that worked in a work tree of its own; start a new one with Agent instead.",
		InputSchema: json.RawMessage(`{"type":"object","properties":{"to":{"type":"string","description":"The sub-agent's id."},"message":{"type":"string","description":"What it is to do now, complete in itself as far as what it knew does not cover it."},"summary":{"type":"string","description":"Three to five words that say what it is doing now, for the person watching."}},"required":["to","message"],"additionalProperties":false}`),
	}
}

// Describe says what a call would do; it is never asked about, as
// starting a sub-agent is not.
func (t *sendMessageTool) Describe(call core.ToolCall, env *core.ToolEnv) (core.Action, error) {
	var in sendMessageInput
	if err := json.Unmarshal(call.Input, &in); err != nil {
		return core.Action{Kind: core.ActionOther}, fmt.Errorf("its arguments cannot be read (%v)", err)
	}
	return core.Action{Kind: core.ActionOther, Summary: "Give the sub-agent " + oneLine(in.To, 40) + " more to do: " + oneLine(firstOf(in.Summary, in.Message), 100)}, nil
}

// Run takes the sub-agent up again, in the background.
func (t *sendMessageTool) Run(ctx context.Context, call core.ToolCall, env *core.ToolEnv) (core.ToolResult, error) {
	l := t.l
	var in sendMessageInput
	if err := json.Unmarshal(call.Input, &in); err != nil {
		return core.ErrorResult(fmt.Sprintf("Nothing was sent: the arguments cannot be read (%v).", err)), nil
	}
	to, message := strings.TrimSpace(in.To), strings.TrimSpace(in.Message)
	if to == "" || message == "" {
		return core.ErrorResult("Nothing was sent: give both to, the sub-agent's id, and message, what it is to do."), nil
	}
	refuse := func(format string, args ...any) (core.ToolResult, error) {
		return core.ErrorResult("Nothing was sent: " + strings.TrimRight(fmt.Sprintf(format, args...), ".") + "."), nil
	}
	for _, b := range l.backgroundAgents() {
		if b.id == to {
			return refuse("the sub-agent %s is still running; its report comes as a notice when it ends, and TaskStop stops it", to)
		}
	}
	meta, err := subagentMeta(ctx, l.subagentsDir(), to)
	if err != nil {
		return refuse("%v", err)
	}
	if n, most := l.runningAgents(), l.maxConcurrentAgents(); n >= most {
		return refuse("%d sub-agents run already, which is the most there may be at once", most)
	}
	def, ok := l.agentDefs().Get(meta[metaAgentType])
	switch {
	case !ok:
		return refuse("the kind of sub-agent %s was, %q, is no longer there", to, meta[metaAgentType])
	case meta[metaWorkTree] != "":
		return refuse("the sub-agent %s worked in a git work tree of its own (%s), which is not opened again; start a new one with Agent, isolation worktree, and tell it what the first did", to, meta[metaWorkTree])
	case !resumable(def):
		return refuse("a %s sub-agent does one task and is not given more; start a new one with Agent", def.Name)
	case meta[metaEnded] == "stopped":
		return refuse("the sub-agent %s was stopped, and a stopped sub-agent is not taken up again; start a new one with Agent", to)
	case meta[metaEnded] == "halted":
		return refuse("the sub-agent %s was ended for good (by a permission the person refused, a hook, or a tool that said stop), and is not taken up again", to)
	case !takesMore(def, meta[metaEnded]):
		return refuse("the sub-agent %s never ended a run that could be taken up", to)
	case l.agentKinds != nil && !slices.Contains(l.agentKinds, def.Name):
		// As the Agent tool would not start one of its kind.
		return refuse("this session runs as the agent %s, whose tools let it start only these kinds of sub-agent: %s; the sub-agent %s is a %s", l.agentName(), strings.Join(l.agentKinds, ", "), to, def.Name)
	}
	if why := l.cannotRunAgent(def); why != "" {
		return refuse("%s", why)
	}
	if why := l.backgroundOff(nil); why != "" {
		return refuse("it would run in the background, and %s", why)
	}
	// As its Agent call had it: the model it named, and how hard it
	// thought.
	if e := meta[metaEffort]; e != "" {
		def.Effort = e
	}
	s, err := l.newSub(l.ctx, l.agent, nil, def, call.ID, meta[metaModel], true, to)
	if err != nil {
		return refuse("%v", err)
	}
	out := l.startBackground(s, agentInput{Description: firstOf(strings.TrimSpace(in.Summary), "more to do"), Prompt: message})
	return out, nil
}

// subagentMeta reads the meta entries of the sub-agent id's session file in
// dir, the last value of each key; an error says, for the model, why there
// is none.
func subagentMeta(ctx context.Context, dir, id string) (map[string]string, error) {
	if strings.ContainsAny(id, `/\`) || strings.HasPrefix(id, ".") {
		return nil, fmt.Errorf("%q is no sub-agent's id", id)
	}
	store := &transcript.Store{Dir: dir}
	ts, err := store.OpenReadOnly(ctx, id)
	if errors.Is(err, transcript.ErrNotFound) || errors.Is(err, fs.ErrNotExist) {
		return nil, fmt.Errorf("no sub-agent of this session has the id %q", id)
	}
	if err != nil {
		return nil, fmt.Errorf("the session file of the sub-agent %s could not be read (%v)", id, err)
	}
	defer ts.Close()
	meta := map[string]string{}
	for _, e := range ts.Entries() {
		if e.Type == transcript.TypeMeta {
			meta[e.Key] = e.Value
		}
	}
	if meta[metaAgentType] == "" {
		return nil, fmt.Errorf("no sub-agent of this session has the id %q", id)
	}
	return meta, nil
}

var _ core.Tool = (*sendMessageTool)(nil)

// earlierAgent is a sub-agent of this session that has ended: its id, its
// kind, how its last run ended and what it was given to do, and whether
// SendMessage would give it more (more).
type earlierAgent struct {
	id, kind, ended, task string
	at                    time.Time
	more                  bool
}

// earlierShown is how many ended sub-agents /tasks lists, the newest
// first.
const earlierShown = 10

// earlierAgents are the sub-agents of this session that have ended, as
// Claude Code's /tasks lists them under "Earlier in this session": read
// from their session files, the newest first. The ones still running are
// left out.
func (l *live) earlierAgents(ctx context.Context) []earlierAgent {
	dir := l.subagentsDir()
	running := map[string]bool{}
	for _, b := range l.backgroundAgents() {
		running[b.id] = true
	}
	// (Their files lie where the session store keeps sessions: under
	// projects/, in a folder for the working directory each was made in.)
	files, _ := filepath.Glob(filepath.Join(dir, "projects", "*", "*.jsonl"))
	var out []earlierAgent
	for _, path := range files {
		id := strings.TrimSuffix(filepath.Base(path), ".jsonl")
		if running[id] || transcript.InUse(path) {
			continue
		}
		info, err := os.Stat(path)
		if err != nil {
			continue
		}
		store := &transcript.Store{Dir: dir}
		ts, err := store.OpenReadOnly(ctx, id)
		if err != nil {
			continue
		}
		a := earlierAgent{id: id, at: info.ModTime()}
		worktree := false
		for _, e := range ts.Entries() {
			switch {
			case e.Type == transcript.TypeMeta && e.Key == metaAgentType:
				a.kind = e.Value
			case e.Type == transcript.TypeMeta && e.Key == metaEnded:
				a.ended = e.Value
			case e.Type == transcript.TypeMeta && e.Key == metaWorkTree:
				worktree = e.Value != ""
			case e.Type == transcript.TypeTitle && a.task == "":
				a.task = e.Title
			case e.Type == transcript.TypeUser && a.task == "":
				a.task = e.Message.Text()
			}
		}
		_ = ts.Close()
		if a.kind != "" {
			// As SendMessage judges it: a kind that takes more, from a run
			// that ended so it can, and not in a work tree of its own.
			def, ok := l.agentDefs().Get(a.kind)
			a.more = ok && !worktree && takesMore(def, a.ended)
			out = append(out, a)
		}
	}
	slices.SortFunc(out, func(a, b earlierAgent) int { return b.at.Compare(a.at) })
	if len(out) > earlierShown {
		out = out[:earlierShown]
	}
	return out
}
