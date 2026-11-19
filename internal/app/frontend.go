package app

import (
	"context"
	"encoding/json"
	"io"
	"os"
	"time"

	"github.com/gnosh-ai/gnosh/internal/agent"
	"github.com/gnosh-ai/gnosh/internal/hooks"
)

// Frontend is whatever stands between a session and whoever watches it: a
// person at a terminal, or a script reading standard output. The session
// code knows nothing else of either. Every front end gnosh has is one: the
// three writers of a run (text, json, stream-json), the plain interactive
// session that reads lines, and later the full interface, which is
// attached through Options.Interactive without a change to the session
// code.
//
// A front end is told everything that happens (Event), puts questions to
// the person when one arrives as an event, and drives the session through
// the handful of calls of Controls.
type Frontend interface {
	// Run drives the session from its first prompt to its end and returns
	// the program's exit code. It is called once, on the program's main
	// goroutine, when the session is ready. It returns promptly when ctx
	// ends. Stopping what runs, the SessionEnd hooks and closing the
	// session's file are done by the session code after Run returns.
	//
	// The Controls it is handed are always SessionControls too, which
	// have a few calls more.
	Run(ctx context.Context, c Controls) int

	// Event is told everything that happens in the session, in order and
	// one call at a time, from a goroutine of the session's. ev is a
	// pointer to one of the loop's events (*agent.TextEvent,
	// *agent.ToolStartEvent, *agent.ToolEndEvent, *agent.NoticeEvent,
	// *agent.TurnEndEvent and the rest of that package's list) or to one
	// of this package's (*HookEvent, *CapEvent, *TasksEvent,
	// *StatusEvent, *DeliverablesEvent). A front end passes over the kinds
	// it does not know.
	//
	// A question for the person arrives as an *agent.QuestionEvent and is
	// answered with Controls.Answer; an *agent.AnswerEvent says that it is
	// over, also when it was taken back unanswered.
	//
	// Event may take its time: events wait for it in order, and the
	// session never waits for a front end. It must not call
	// Controls.Command or Controls.Wait, which wait for the session.
	Event(ev any)
}

// Asker is a Frontend that can put a question to the person before a
// session exists: whether the folder is trusted, which model to use. A
// front end that is not one is treated as having nobody to ask.
type Asker interface {
	// AskBefore puts the question and returns the answer. An error means
	// the person left instead of answering.
	AskBefore(q agent.Question) (agent.Answer, error)
}

// Suggester is a Frontend that shows the person's likely next prompt
// (SuggestionEvent) in its box. Only for one is the model asked for it
// (promptSuggestionEnabled): a front end with no box to show it in would
// pass over what the request made.
type Suggester interface {
	ShowsSuggestions() bool
}

// FrontendInfo is what the maker of an interactive front end is given
// (Options.Interactive). The maker may decline, by returning nil: there is
// no terminal it could draw on. The session is then given the plain front
// end, which reads lines and works anywhere. (Options.Interactive may be
// nil itself, for the same reason and with the same result.)
type FrontendInfo struct {
	// Stdin, Stdout and Stderr are the process's streams.
	Stdin  io.Reader
	Stdout io.Writer
	Stderr io.Writer
	// In and Out are standard input and standard output as the files they
	// are, when they are files: what an interface that takes the terminal
	// over opens it on. Either is nil when the stream is not a file.
	In, Out *os.File
	// Prompt is the prompt the command line gave (gnosh "fix the test"),
	// or empty. The front end sends it as the session's first prompt.
	Prompt string
	// Getenv looks up an environment variable of the process.
	Getenv func(string) string
}

// Controls is the handful of calls a front end makes on a session. Every
// method may be called from any goroutine, and none waits for a turn
// unless it says so. In an interactive session /clear puts a new session
// behind the same Controls: a front end keeps the one it was given.
type Controls interface {
	// Submit sends a prompt and returns its id at once. A prompt sent
	// while a turn runs is queued. What becomes of it arrives as events: a
	// *agent.TurnEndEvent whose result names the prompt, or a
	// *agent.PromptReturnedEvent when it never went out.
	Submit(p agent.Prompt) (id string, err error)
	// Interrupt stops the turn that is running, all of it and at once. It
	// is what Esc and the first Ctrl+C do.
	Interrupt()
	// Answer gives the person's answer to the question with that id.
	Answer(question string, a agent.Answer) error
	// Queued lists the prompts that wait for their turn, and TakeQueued
	// takes the person's own off the queue for editing, as Up does in an
	// empty input box.
	Queued() []agent.QueuedPrompt
	TakeQueued() []agent.Prompt

	// Mode returns the permission mode in force, and SetMode changes it:
	// default, acceptEdits, plan, dontAsk or bypassPermissions. Skipping
	// permissions can only be switched to in a session that was started
	// with it within reach.
	Mode() string
	SetMode(mode string) error

	// Command runs a line that begins with a slash as a command of
	// gnosh's. Handled is false when the line names no command, and the
	// front end then sends it as a prompt or gives it back. It waits for
	// the command, and so must be called from Run and never from Event.
	Command(ctx context.Context, line string) (CommandResult, error)

	// State says what the session is doing right now and what it is
	// waiting for, and Context how full the model's window is. Neither
	// ever blocks, so a session that seems stuck can always be asked.
	State() agent.State
	Context() agent.ContextStatus
	// Info describes the session: its id, its model, its tools.
	Info() SessionInfo
	// Usage returns what the session has cost so far.
	Usage() agent.Totals

	// Signals delivers the SIGINT, SIGTERM and SIGHUP the process
	// receives. What each means is the front end's to say: a run stops and
	// exits, an interactive session stops the turn at the first Ctrl+C.
	//
	// The channel is the program's, and the program reads it again once
	// Run has returned: from then on the program is shutting down, and the
	// next signal on the channel ends the process at once, without the
	// rest of the shutdown (the second signal of the specs). That is so
	// whether the signal arrived after Run returned or was still waiting
	// on the channel when it did. So a front end takes off the channel
	// every signal it acts on, and returns from Run promptly once it has
	// decided to leave; a signal it leaves there is one that cuts the
	// shutdown short.
	Signals() <-chan os.Signal
	// Notify fires the person's Notification hooks: kind is
	// "permission_prompt" (a question has waited a while), "push_notification"
	// (the model's PushNotification) or "idle_prompt"
	// (a turn finished a while ago and nothing was typed since). It does
	// not wait for them.
	Notify(kind, title, message string)
	// RunEnd is called by the front end of a non-interactive run when its
	// last turn has ended, with the exit code the run would end with, and
	// returns the code it ends with: a feature may raise it. The features
	// are asked once they have been told how that turn ended, and RunEnd
	// returns when the front end has been told everything they said, so
	// that what it prints next comes last. It waits for the front end's
	// Event, and so must be called from Run and never from Event.
	RunEnd(res agent.TurnResult, exit int) int
	// Wait waits until the session has nothing in hand and the front end
	// has been told everything that happened so far, or until ctx ends.
	// It must be called from Run and never from Event.
	Wait(ctx context.Context) error
	// Tasks returns the session's task list as it stands: what a feature
	// last set (a *TasksEvent says when it changes). It is empty when the
	// session has none.
	Tasks() []Task
	// RunPrompt asks the features for the first prompt of a run that was
	// given none. It reports false when none of them has one.
	RunPrompt(ctx context.Context) (agent.Prompt, bool)
	// Quit says why the session is ending, in the SessionEnd hooks' words:
	// "prompt_input_exit" when the person left, "other" otherwise. The
	// front end then returns from Run, and the session is closed.
	Quit(reason string)
}

// SessionControls is all that a session's controls can do: Controls, and
// the calls that were added after front ends were first written against
// it. The Controls this package hands a front end is always one, so a
// front end reaches the rest by asking the value it was given, for all of
// it or for one call:
//
//	if more, ok := c.(app.SessionControls); ok { ... }
//	if sh, ok := c.(interface{ Shell(string) error }); ok { ... }
//
// The two are kept apart so that whatever stands in for a session in a
// front end's own tests remains a Controls, and a front end can say what
// it does without each of these calls.
type SessionControls interface {
	Controls

	// Status returns the named facts the session has for a status line,
	// as they stand (a *StatusEvent says when one changes): "frame", the
	// frame the session is in; "background", how many commands run in the
	// background; "goal"; "session", the session's name. A fact that is
	// not there has no value now.
	Status() map[string]string

	// Shell runs a command line of the person's own: what they typed
	// behind "!". It is theirs, so nothing is asked and no rule is looked
	// at. It returns at once, with an error when the command could not be
	// started (another of the person's commands is still running). The
	// command is shown as a tool call is: a *agent.ToolStartEvent, its
	// output as *agent.ToolProgressEvents, and a *agent.ToolEndEvent,
	// under a call id of its own. When it ends, the command and what it
	// printed are handed to the model, which answers them; one that was
	// stopped with Interrupt is put in front of the model with the next
	// prompt instead.
	Shell(command string) error
	// Background moves the command that the tool call with that id is
	// waiting for to the background, where it keeps running, and reports
	// whether there was such a command. The call returns at once with the
	// command's task id. It works for a call of the model's and for a
	// command started with Shell. tool is the call's tool: a Bash call
	// whose command has not started yet (its permission is checked, its
	// PreToolUse hooks run) goes to the background as it starts.
	Background(callID, tool string) bool
	// History returns the prompts the person sent from this folder, in
	// earlier sessions and in this one, the oldest first: what Up and a
	// search of the history reach. It reads the folder's most recent
	// sessions and may take a moment, so it is asked once.
	History() []string
	// Withdraw takes one prompt of the person's that waits for its turn
	// off the queue, by the id Submit returned, and returns it for
	// editing. It reports false when the prompt no longer waits.
	Withdraw(id string) (agent.Prompt, bool)
	// RewindPoints lists the person's prompts of this conversation, the
	// oldest first: the points Rewind can take it back to. Rewind takes
	// the conversation back to before one of them and returns that
	// prompt's text, for the prompt box. Only the conversation goes back
	// (no file is restored), the session must be idle, and it is opened
	// again from its file, as /clear opens a new one: the front end
	// forgets what it held of the session's state. The command /rewind N
	// does the same and says so in its result (CommandResult.Reset).
	RewindPoints() []RewindPoint
	Rewind(ctx context.Context, point string) (text string, err error)
	// ResumeChoices lists the folder's other sessions, the one written
	// to last first: what the command /resume offers, and opens in place
	// of this session with an id after it (CommandResult.Reset).
	ResumeChoices() []SessionChoice
	// Deliverables returns the session's list of deliverables as it
	// stands: what a feature last set (a *DeliverablesEvent says when it
	// changes). It is empty when the session has none.
	Deliverables() []Deliverable
	// Transcript returns the session's conversation as its file holds it,
	// the oldest entry first, for the transcript view (Ctrl+O): whole,
	// with the work of popped steps and what came before a summary, but
	// not what a rewind cut off.
	Transcript() []TranscriptEntry
	// ModelChoices lists the models /model can switch to, the one in use
	// marked, for the picker that /model with nothing after it opens. It
	// asks the providers' servers which models they have, within ctx;
	// switching is the command /model provider/model [level].
	ModelChoices(ctx context.Context) []ModelChoice
	// ConfigChoices lists gnosh's options with their values in force, for
	// the picker that /config with nothing after it opens; changing one is
	// the command /config key=value.
	ConfigChoices() []ConfigChoice
	// MemoryFiles lists the context files the /memory picker offers to
	// open in the person's editor: those the model is given, then the
	// person's own and the project's GNOSH.md.
	MemoryFiles() []MemoryFile
	// MCPChoices lists the session's MCP servers as they stand now, for
	// the picker that /mcp with nothing after it opens; what is done to
	// one is the command /mcp reconnect, enable or disable and its name.
	MCPChoices() []MCPChoice
	// HookChoices lists the hooks in force, for the read-only browser that
	// /hooks with nothing after it opens, and a note on those configured
	// and not in force.
	HookChoices() ([]HookChoice, string)
	// AgentChoices lists the kinds of sub-agent, for the read-only browser
	// that /agents with nothing after it opens, and whether the model can
	// start them (the Agent tool is on).
	AgentChoices() ([]AgentChoice, bool)
	// PermissionRules returns the permission mode and the rules in force,
	// for the picker that /permissions with nothing after it opens;
	// adding and taking out a rule are the commands /permissions allow,
	// ask, deny and remove.
	PermissionRules() (mode string, rules []PermissionRule)
	// SkillChoices lists the skills, for the picker that /skills with
	// nothing after it opens; how far one is seen is the command /skills
	// <name> on|name-only|user-invocable-only|off.
	SkillChoices() []SkillChoice
	// Effort returns the thinking levels of the model in use and the
	// level it thinks at ("" for its own default), for the picker that
	// /effort with nothing after it opens and for Alt+T; setting one is
	// the command /effort and the level.
	Effort() (levels []string, level string)
	// StopBackgroundAgents stops the session's sub-agents that run in the
	// background (Ctrl+X Ctrl+K), and says how many there were; the
	// background commands go on. It may take a moment.
	StopBackgroundAgents() int
}

// CommandResult is what running a slash command came to.
type CommandResult struct {
	// Handled says the line named a command and it was run.
	Handled bool
	// Text is what the command said, for the person.
	Text string
	// Quit says the command asked for the session to end.
	Quit bool
	// Name is the command that ran, without its slash.
	Name string
	// Reset says the command put another session behind the controls, or
	// this one at another point of its conversation (/clear, /rewind):
	// what the front end holds of the session's state is to be forgotten.
	// Draft is then a prompt to put back into the prompt box, when there
	// is one: the prompt a rewind went back to.
	Reset bool
	Draft string
	// Prompt is the id of a prompt the command sent to the model, when it
	// sent one: a skill is such a command. What becomes of it arrives as
	// events, as for a prompt the front end submits itself.
	Prompt string
	// Copy is text the command asks the front end to put on the person's
	// clipboard (/copy), through the terminal.
	Copy string
}

// MCPServerStatus is one MCP server and how it stands, as stream-json's
// system/init line names them.
type MCPServerStatus struct {
	Name   string `json:"name"`
	Status string `json:"status"`
}

// SessionInfo describes a session to its front end.
type SessionInfo struct {
	// ID is the session's id and Path its file.
	ID   string
	Path string
	// Cwd is the working directory it started in.
	Cwd string
	// Provider and Model name the model main requests go to, Address is
	// where its server lives, and Thinking its thinking level.
	Provider string
	Model    string
	Address  string
	Thinking string
	// ThinkingBudget is the most tokens the model may think before a
	// reply (thinkingBudget, MAX_THINKING_TOKENS; 0 for no cap), and
	// BudgetNote the words the server puts at the end of a thought it cut
	// there, by which the interface knows one was cut.
	ThinkingBudget int
	BudgetNote     string
	// MCPServers are the session's MCP servers and how each stands:
	// "pending", "connected" or "failed".
	MCPServers []MCPServerStatus
	// Tools are the tools the model may call, by name, in the order it is
	// told of them.
	Tools []string
	// Mode is the permission mode in force.
	Mode string
	// Commands are the slash commands, by name, and CommandList says of
	// each what a front end needs to complete it and to show it.
	Commands    []string
	CommandList []CommandInfo
	// Skills are the person's skills that can be run by typing a slash
	// and the name, as they stand now: Args is the hint for what follows
	// the name, and Summary what the skill is for.
	Skills []CommandInfo
	// Version names the build.
	Version string
	// Interactive says a person is there to be asked.
	Interactive bool
	// Verbose says the interface shows the model's reasoning and the
	// calls' whole output as they come (the verbose option).
	Verbose bool
	// ViewMode is the viewMode option, when something set it: "default",
	// "verbose" or "focus" (each run of tool calls folded into one line).
	// It wins over Verbose; "" leaves Verbose to decide.
	ViewMode string
	// HideTurnDuration says a turn that ended as it should goes without
	// the line that says how long it took (the showTurnDuration option
	// off, as the session has it now).
	HideTurnDuration bool
	// ListIgnored says the @ file picker lists what git ignores too (the
	// respectGitignore option off, as the session has it now).
	ListIgnored bool
	// FileSuggestion says a command of the person's offers the paths an @
	// mention completes to (Claude Code's fileSuggestion), which the
	// controls' SuggestFiles runs, in place of the picker's own search.
	FileSuggestion bool
	// SidePanel says the session starts with the side panel on (the
	// sidePanel option).
	SidePanel bool
	// OutputStyle is the name of the output style in force, "default" for
	// none.
	OutputStyle string
	// Color is the colour /color gave the session's prompt mark, one of
	// PromptColors, or "" for the interface's own.
	Color string
	// Keybindings is the person's keybindings.json, where the keys of the
	// interface are bound to other actions (/keybindings).
	Keybindings string
	// ShowPast says the session was opened again, and its conversation is
	// not on the screen: the interface shows where it left off.
	ShowPast bool
	// NotifChannel is how a person who seems to be away is told the
	// session waits (the preferredNotifChannel option): "auto",
	// "terminal_bell", "iterm2", "iterm2_with_bell", "kitty", "ghostty"
	// or "notifications_disabled".
	NotifChannel string
	// Theme is the theme option: "auto", "dark", "light" or "ansi".
	Theme string
	// AskTimeout is how long a question of the model's (AskUserQuestion)
	// may go without a key before the interface closes it by itself, with
	// what was picked by then and the Choice AskedAway (the
	// askUserQuestionTimeout option); zero for never.
	AskTimeout time.Duration
	// Vim says the box takes vim's keys (the editorMode option, "vim"),
	// and VimRemaps are the INSERT-mode pairs that stand for Esc (the
	// vimInsertModeRemaps option).
	Vim       bool
	VimRemaps []string
	// Resume is the command line that opens this session again.
	Resume string
	// NotifyHooks says a Notification hook is configured, so that the
	// timers behind Controls.Notify are worth keeping.
	NotifyHooks bool
}

// HookEvent says that hooks ran at one of the session's moments, and what
// each of them did. Every hook that ran is in it, also the ones that had
// nothing to say, so that no hook is ever invisible.
type HookEvent struct {
	// Event is the moment.
	Event hooks.Event
	// CallID and Tool name the tool call the hooks ran on, for the tool
	// events.
	CallID string
	Tool   string
	// Records has one entry for every hook that ran, in the order they
	// are configured.
	Records []hooks.Record
}

// HookStatusEvent says that hooks with a status message have started at
// one of the session's moments (Status is their messages), or, with an
// empty Status, that they have finished. A front end shows the status in
// place of its working line's own words while they run.
type HookStatusEvent struct {
	Event  hooks.Event
	Status string
}

// CommandInfo describes a slash command to a front end.
type CommandInfo struct {
	// Name is the command's name without its slash, and Aliases its other
	// spellings.
	Name    string
	Aliases []string
	// Args says what may follow the name, as help shows it
	// ("[instructions]"); it is empty for a command that takes nothing.
	Args string
	// Summary says in a line what the command does.
	Summary string
	// Sensitive lists the first words of the command's arguments after
	// which what is typed is a text the person wants kept nowhere: for
	// /mem it holds "redact". A front end shows such a line, and keeps it
	// in its history, only as far as that word. An entry of "" means
	// everything after the command's name.
	Sensitive []string
	// InRun says the command may be the prompt of a non-interactive run.
	InRun bool
}

// RestartEvent says the session closes for gnosh to start again at once
// and go on with it elsewhere: a tool moved it (EnterWorktree,
// ExitWorktree), and its turn has ended. A front end ends Run as it does
// after /restart, with Why, a line for the person, said first; the new
// start says itself where the session is.
type RestartEvent struct {
	Why string
}

// PushEvent asks the front end to tell the person something the model
// thinks worth calling them back for (PushNotification): a desktop note by
// their channel (preferredNotifChannel) and their Notification hooks, of
// the type "push_notification". A front end that sees the person at the
// terminal (a key pressed in the last half minute) sends nothing: what
// the model writes reaches them already. It answers on Reply, once, with
// what it did in a few words ("sent", or why not); a front end that knows
// nothing of it never answers, and the tool says so.
type PushEvent struct {
	Message string
	Reply   chan<- string
}

// StatusEvent says that named facts for a status line have changed: see
// SessionControls.Status for the names. A fact with an empty value is taken off.
type StatusEvent struct {
	// Facts are the facts that changed, by name.
	Facts map[string]string
}

// StatusFacts returns the facts that changed.
func (e *StatusEvent) StatusFacts() map[string]string { return e.Facts }

// QuestionData is what a permission question carries in
// agent.Question.Data, for a front end that draws more than a list of
// choices.
type QuestionData struct {
	// Until is when the question is taken back unanswered, or the zero
	// time when it waits for as long as the person takes. It is set on
	// the question about a removal that is asked even while permissions
	// are skipped.
	Until time.Time
	// Modes names, by the key of a choice, the permission mode that the
	// choice switches the session to: "acceptEdits" for the yes that also
	// accepts edits from then on.
	Modes map[string]string
	// CommentOnYes says that words given with a yes (Answer.Text) are
	// passed on to the model, as words given with a no are.
	CommentOnYes bool
	// Input is the input of the call the question is about, for the
	// channels that relay permission questions (channelrelay.go).
	Input json.RawMessage
	// PersonOnly says the question is for the person alone: a call that
	// throws away what cannot be got back (core.Action.Confirm), or one
	// the engine found could destroy what has to be kept. No channel is
	// sent it.
	PersonOnly bool
	// Stops is what a bare "no" stops, when it is not the turn: "that
	// sub-agent", for a question of a sub-agent in the background.
	Stops string
}

// Deadline is when the question is taken back unanswered, or the zero
// time.
func (d QuestionData) Deadline() time.Time { return d.Until }

// NoStops is what a bare "no" stops, or "" for the turn.
func (d QuestionData) NoStops() string { return d.Stops }

// TasksEvent says the session's task list has changed, and carries all of
// it: the list a front end shows under the prompt, in order. An empty list
// means there is none now. One is sent when a session that already has a
// list is opened, too.
type TasksEvent struct {
	// Tasks is the whole list as it now stands.
	Tasks []Task
}

// CapEvent says a non-interactive run reached the number of replies it was
// given with --max-turns, and its turn is being stopped there.
type CapEvent struct {
	// Turns is the number that was given, and Turn the turn that reached
	// it.
	Turns int
	Turn  string
}
