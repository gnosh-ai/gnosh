package app

import (
	"fmt"
	"slices"
	"strings"

	"github.com/gnosh-ai/gnosh/internal/agent"
	"github.com/gnosh-ai/gnosh/internal/app/agentdef"
	"github.com/gnosh-ai/gnosh/internal/app/skill"
	"github.com/gnosh-ai/gnosh/internal/hooks"
	"github.com/gnosh-ai/gnosh/internal/prompt"
)

// This file is what a kind of agent brings beside its prompt and its tools
// (spec/cc/subagents.md 2): the skills its definition preloads (skills),
// whose whole text it starts with, the thinking level it asks for
// (effort), and hooks of its own (hooks). All three hold for a sub-agent
// of the kind and for a session that runs as it (--agent). A preloaded skill is its text and nothing else:
// the permission rules, hooks and thinking level it carries are put in
// force when it is run with the Skill tool, not when it is preloaded.

// maxPreloaded is the most skills a definition may preload.
const maxPreloaded = 32

// skillsToPreload finds the skills a definition preloads: each must be one
// this session has, one the model may load (its file does not say
// disable-model-invocation), and one this gnosh can run. It says why not,
// or "".
func (l *live) skillsToPreload(def agentdef.Def) ([]skill.Skill, string) {
	if len(def.Skills) == 0 {
		return nil, ""
	}
	where := firstOf(def.Source, "gnosh's own definition")
	switch {
	case len(def.Skills) > maxPreloaded:
		return nil, fmt.Sprintf("the kind %q preloads %d skills (%s), and the most a kind may preload is %d", def.Name, len(def.Skills), where, maxPreloaded)
	case l.skillsOff():
		return nil, fmt.Sprintf("the kind %q preloads skills (%s), and skills are off in this session", def.Name, where)
	}
	var out []skill.Skill
	for _, name := range def.Skills {
		sk, ok := l.skills().Get(name)
		switch {
		case !ok:
			return nil, fmt.Sprintf("the kind %q preloads the skill %q (%s), which this session does not have. It is not run without it", def.Name, name, where)
		case sk.NoModel:
			return nil, fmt.Sprintf("the kind %q preloads the skill %s (%s), whose file keeps it from the model (disable-model-invocation), so it cannot be preloaded", def.Name, sk.Name, where)
		case len(sk.Unbuilt) > 0:
			return nil, unbuiltSkill(sk)
		}
		out = append(out, sk)
	}
	return out, ""
}

// preloadParts are the parts a session of the kind starts with, one for
// each skill it preloads: the skill's text with no arguments and its
// !`command` lines run, under a line of gnosh's that says whose it is.
func (l *live) preloadParts(def agentdef.Def, sessionID string) ([]agent.Part, error) {
	skills, why := l.skillsToPreload(def)
	if why != "" {
		return nil, fmt.Errorf("%s", why)
	}
	var parts []agent.Part
	for _, sk := range skills {
		vars := skill.Vars{SessionID: sessionID, SkillDir: sk.Dir, ProjectDir: l.cfg.Paths.ProjectRoot, Effort: def.Effort}
		text, err := l.injectCommands(sk, sk.Render("", vars))
		if err != nil {
			return nil, fmt.Errorf("the skill %s, which the kind %q preloads, could not be made ready: %w", sk.Name, def.Name, err)
		}
		lead := fmt.Sprintf("Your definition (the kind %s) preloads the skill %s: the user's own instructions for one kind of task, which follow. Where your task is of that kind, carry them out.", def.Name, sk.Name)
		parts = append(parts, agent.Part{Source: "skill:" + sk.Name, Text: prompt.Wrap(lead) + "\n\n" + strings.TrimSpace(text)})
	}
	return parts, nil
}

// agentPreloaded is the parts of the skills that the kind the session runs
// as preloads (--agent), made the first time they are wanted: for the
// first request, or after a summary in a session that was resumed.
func (l *live) agentPreloaded() ([]agent.Part, error) {
	if l.asAgent == nil || len(l.asAgent.Skills) == 0 {
		return nil, nil
	}
	l.skillMu.Lock()
	pre := l.preloaded
	l.skillMu.Unlock()
	if pre != nil {
		return pre, nil
	}
	pre, err := l.preloadParts(*l.asAgent, l.ts.ID())
	if err != nil {
		return nil, err
	}
	l.skillMu.Lock()
	l.preloaded = pre
	l.skillMu.Unlock()
	return pre, nil
}

// agentHooks puts the hooks of a kind's definition (hooks) in force: for
// the events of one sub-agent of the kind (id), with a Stop hook as its
// SubagentStop, until the function it returns is called as it ends; or,
// for the session that runs as the kind (id ""), for the whole session.
// They are checked as the settings' hooks are, and a kind whose hooks
// would not run is not run either. With every hook switched off
// (disableAllHooks, safe mode) there are none to put in force.
func (l *live) agentHooks(def agentdef.Def, id string) (func(), error) {
	none := func() {}
	if len(def.Hooks) == 0 || l.hooks == nil || l.hooks.r == nil || l.cfg.HooksOff != "" {
		return none, nil
	}
	where := firstOf(def.Source, "gnosh's own definition")
	cfg, err := hooks.ParseConfig(def.Hooks, where+" (hooks)")
	if err == nil {
		// "once" is a skill's: in an agent's definition it is not read,
		// as in Claude Code, so that the agent's guard is not gone after
		// its first run.
		for i := range cfg.Hooks {
			cfg.Hooks[i].Once = false
		}
		if id == "" {
			return none, l.hookErr(def, l.hooks.r.Add(cfg))
		}
		var remove func()
		if remove, err = l.hooks.r.AddFor(cfg, id); err == nil {
			return remove, nil
		}
	}
	return nil, l.hookErr(def, err)
}

// hookErr words why the hooks of a kind cannot be put in force, or is nil.
// The error names the file the hooks are in.
func (l *live) hookErr(def agentdef.Def, err error) error {
	if err == nil {
		return nil
	}
	return fmt.Errorf("the hooks the kind %q carries cannot be put in force: %s. It is not run without them", def.Name, strings.TrimRight(err.Error(), "."))
}

// thinkingFor is the level of m an effort word stands for: one of its
// levels, max for its most, or auto for its own default (""). ok is false
// for a word that is none of these.
func thinkingFor(m agent.Model, word string) (level string, ok bool) {
	switch {
	case word == "auto":
		return "", true
	case word == "max" && len(m.Levels) > 0:
		return m.Levels[len(m.Levels)-1], true
	case slices.Contains(m.Levels, word):
		return word, true
	}
	return "", false
}

// agentEffort puts the thinking level a definition asks for (effort) on
// the agent that runs as it. A level its model does not have leaves
// thinking as it is, and the person is told.
func (l *live) agentEffort(a *agent.Agent, def agentdef.Def) {
	if def.Effort == "" || a == nil {
		return
	}
	m := a.Model()
	level, ok := thinkingFor(m, def.Effort)
	if !ok {
		l.notice(agent.LevelInfo, "agent", fmt.Sprintf("The kind %s asks for the effort %s, which is not a thinking level of %s; thinking stays as it is.", def.Name, def.Effort, firstOf(m.Info.ID, "the model")))
		return
	}
	_, _ = a.SetThinking(level)
}
