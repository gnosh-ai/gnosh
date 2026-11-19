package prompt

import (
	"strings"

	"github.com/gnosh-ai/gnosh/internal/core"
)

// The tools whose presence decides a part of the system prompt.
const (
	toolGnovmRun = "gnovm_run"
	toolDeliver  = "Deliver"
)

// MemoryForm chooses between the two texts gnosh has about mem. Which of
// them serves a model better is to be settled by measurement on the
// person's own machine, the same tasks run with each; until it is, the
// lean one is sent.
type MemoryForm int

const (
	// MemoryLean is the text "memory", and the default. It is under 1,500
	// tokens, and leaves to mem's own receipts and refusals, and to the
	// frame line, what they say at the moment it matters.
	MemoryLean MemoryForm = iota
	// MemoryFull is the text "memory-full": the lean text with every item
	// the mem spec lists for the system prompt said in it, about 250
	// tokens more.
	MemoryFull
)

// SystemInput is what the system prompt of a session depends on. Nothing
// in it is the date, the working directory, the model or the session:
// those go with the first prompt (Environment), so that the system prompt
// is the same bytes from one session to the next.
type SystemInput struct {
	// Tools are the tools the session's requests carry. The part about
	// mem is sent when gnovm_run is among them, the part about
	// deliverables when Deliver is, and with no tools at all the model is
	// told that it has none.
	Tools []core.ToolSpec

	// Memory chooses which text about mem is sent when the session has
	// gnovm_run. The zero value is MemoryLean.
	Memory MemoryForm

	// GnoProject says the working directory holds Gno code: a gnomod.toml,
	// a gno.mod, a gnowork.toml or a .gno file. GnoGuides are where the Gno
	// project's interrealm and security guides are on this machine, in
	// that order (gnodocs), for the audit the part about Gno asks for; the
	// part is sent only with both.
	GnoProject bool
	GnoGuides  []string

	// Instructions is the project's and the person's own instruction
	// files, already put together by the package that reads them. It is
	// placed as it is, but for control sequences and gnosh's own tag,
	// which are made harmless: these are files, and a stranger's
	// repository can bring one.
	Instructions string

	// Replace, when not empty, stands in place of every part gnosh writes
	// itself. Instructions and Append are still sent. It is what
	// --system-prompt gives.
	Replace string

	// Append, when not empty, is sent last. It is what
	// --append-system-prompt gives.
	Append string

	// Style is the output style the person chose (Claude Code's
	// outputStyle), or nil for the default.
	Style *Style

	// Language is the language the person wants replies in, as they
	// named it (Claude Code's language), or empty.
	Language string

	// AttributionCommit and AttributionPR are the lines the model ends a
	// commit's message and a pull request's description with (Claude
	// Code's attribution), or empty for none.
	AttributionCommit, AttributionPR string
}

// Style is an output style: instructions on how the model replies, sent
// right after the core. KeepCoding keeps the core's first line, the coding
// agent's role; without it a style of the person's own gives the model its
// role (core-any-role), and the rest of the core, which is how to work in
// gnosh, stays.
type Style struct {
	Name, Description string
	Text              string
	KeepCoding        bool
}

// BuiltinStyles are the output styles gnosh has of its own, Claude Code's
// four besides its default, in the order a list shows them. Each keeps the
// coding instructions.
func (s *Set) BuiltinStyles() []Style {
	return []Style{
		{Name: "Proactive", Description: "starts work at once and makes the routine choices itself", Text: s.fill(textStyleProactive, nil), KeepCoding: true},
		{Name: "Concise", Description: "leads with the result, and leaves out the lead-in, the narration and the recap", Text: s.fill(textStyleConcise, nil), KeepCoding: true},
		{Name: "Explanatory", Description: "adds short Insight blocks on the choices behind the work", Text: s.fill(textStyleExplain, nil), KeepCoding: true},
		{Name: "Learning", Description: "explains its choices and leaves pieces of code for you to write", Text: s.fill(textStyleLearning, nil), KeepCoding: true},
	}
}

// System returns the system prompt as parts, the part that changes least
// first, as core.Request.System wants them. The order never changes; a
// part that does not apply is left out:
//
//  1. the fixed core, with the security rule as its last lines: the same
//     bytes whatever is in SystemInput, but for a Style of the person's
//     own that does not keep the coding instructions, whose role stands in
//     place of the core's first line; then the Style, when there is one;
//     then the language replies are in, when the person set one, and
//     what commits and pull requests are signed with, when a settings
//     file says (with Replace too: they are the person's choices, not
//     gnosh's words);
//  2. the note that there are no tools, when there are none;
//  3. the part about mem, when the session has gnovm_run: the lean text,
//     or with Memory set to MemoryFull the full one;
//  4. the Gno rule, in a project with Gno code;
//  5. the project's and the person's instructions;
//  6. the part about deliverables, when the session has Deliver. It stands
//     after the instructions because only some sessions of a project have
//     the tool: placed here, a session with it and one without share
//     everything before it in a model server's cache;
//  7. Append.
//
// With Replace set, parts 1 (the Style with it), 3, 4 and 6 give way to it.
func (s *Set) System(in SystemInput) []string {
	var parts []string
	add := func(part string) {
		if part = strings.TrimSpace(part); part != "" {
			parts = append(parts, part)
		}
	}
	own := strings.TrimSpace(in.Replace) == ""
	switch {
	case !own:
		add(in.Replace)
	case in.Style != nil && !in.Style.KeepCoding:
		// The coding agent's role gives way to the style's; how to work
		// in gnosh stays.
		core := s.Core()
		_, rest, _ := strings.Cut(core, "\n")
		add(s.fill(textCoreAnyRole, nil) + "\n" + rest)
	default:
		add(s.Core())
	}
	if own && in.Style != nil {
		add(in.Style.Text)
	}
	if lang := oneLine(in.Language, maxName); lang != "" {
		// The person's own choice: it holds under --system-prompt too.
		add(s.fill(textLanguage, values{"language": lang}))
	}
	if commit, pr := lines(Defuse(in.AttributionCommit), maxQuote), lines(Defuse(in.AttributionPR), maxQuote); commit != "" || pr != "" {
		add(s.fill(textAttribution, values{"commit": commit, "pr": pr}))
	}
	if len(in.Tools) == 0 {
		add(s.fill(textNoTools, nil))
	}
	if own && hasTool(in.Tools, toolGnovmRun) {
		if in.Memory == MemoryFull {
			add(s.fill(textMemoryFull, nil))
		} else {
			add(s.fill(textMemory, nil))
		}
	}
	if own && in.GnoProject && len(in.GnoGuides) == 2 {
		add(s.fill(textGno, values{"interrealm": in.GnoGuides[0], "security": in.GnoGuides[1]}))
	}
	add(Untrusted(strings.TrimSpace(in.Instructions)))
	if own && hasTool(in.Tools, toolDeliver) {
		add(s.fill(textDeliver, nil))
	}
	add(in.Append)
	return parts
}

// Core returns the fixed core: the first part of every system prompt gnosh
// writes. It depends on nothing, so it is the same bytes in every session,
// project and machine, and on every day.
func (s *Set) Core() string {
	return s.fill(textCore, nil) + "\n" + s.fill(textSecurity, nil)
}

// hasTool reports whether a tool called name is among tools.
func hasTool(tools []core.ToolSpec, name string) bool {
	for _, t := range tools {
		if t.Name == name {
			return true
		}
	}
	return false
}

// AgentWorkTree is the part of a sub-agent's system prompt, sent last,
// that tells it it works in a git work tree of its own at tree, made from
// the person's checkout at repo, and that its changes stay there.
func (s *Set) AgentWorkTree(tree, repo string) string {
	return s.fill(textAgentWorkTree, values{"tree": tree, "repo": repo})
}
