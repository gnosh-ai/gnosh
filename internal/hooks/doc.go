// Package hooks runs the hooks a person configures to watch and steer the
// agent.
//
// A hook is a command of the user's that gnosh runs at a named moment:
// before a tool call, when a prompt is sent, when a turn is about to end.
// The command reads a description of the moment as JSON on its standard
// input and answers with its exit status and, if it wants finer control,
// with a JSON object on its standard output. Its answer can stop what was
// about to happen, approve it, put it to the user, change a tool's input,
// or add text for the model.
//
// Hooks are used as guards: "the tests must pass before you stop", "never
// force-push". So one rule stands above the others in this package: a
// guard that did not run has not passed. A hook that is written wrongly in
// the settings stops gnosh at startup, and a hook that could not check at
// run time blocks exactly as a hook that said no.
//
// The interface is Claude Code's (event names, the settings shape, the
// JSON a hook reads and writes, the exit codes), so that a hooks block and
// a hook script written for Claude Code work here. Where gnosh differs, it
// is stricter, and the differences are listed at the end.
//
// The package prints nothing and reads nothing from the process around it:
// the shell, the environment, the clock and the directories are passed in
// (Options), and so are the session's facts (Input).
//
// # Using it
//
//	cfg, err := hooks.ParseConfig(block, "~/.config/gnosh/gnosh.json") // Claude Code's "hooks" block
//	h, err := hooks.NewHook(hooks.Spec{Event: "Stop", Command: "./tests.sh"}) // a hook read by someone else
//	cfg.Add(h)
//	all := hooks.Merge(userConfig.AsGlobal(), projectConfig) // weakest layer first
//
//	opts := hooks.Options{Fires: firedByThisBuild, ProjectDir: root}
//	for _, p := range hooks.Check(all, opts) { ... } // every problem at once
//	r, err := hooks.NewRunner(all, opts)           // refuses what Check refuses
//
//	out, err := r.Run(ctx, hooks.Input{Event: hooks.PreToolUse, SessionID: id, Cwd: cwd,
//		ToolName: "Bash", ToolInput: raw, ToolUseID: callID})
//	switch {
//	case err != nil:        // the turn was cancelled; nothing ran to the end
//	case out.EndTurn:       // a hook ended the turn: show out.StopReason
//	case out.Blocked:       // the call does not run: the model reads out.Reason
//	case out.PermissionDecision == hooks.Ask: // put it to the user, with out.Reason
//	case out.PermissionDecision == hooks.Allow: // no question needed
//	}
//	// out.UpdatedInput, out.AdditionalContext, out.SystemMessages, out.Warnings, out.Records
//
// # The events
//
// What a block does, what the matcher is tested against, and how long a
// hook gets by default:
//
//	PreToolUse          the call does not run                    tool name        600 s
//	UserPromptSubmit    the prompt is not sent                   (no matcher)      30 s
//	Stop                the turn does not end                    (no matcher)     600 s
//	SubagentStop        the sub-agent does not finish            (no matcher)     600 s
//	PostToolUse         the reason is feedback for the model     tool name        600 s
//	PostToolUseFailure  the reason is feedback for the model     tool name        600 s
//	PermissionRequest   (answers the question: allow or deny)    tool name        600 s
//	SessionStart        one warning; cannot block                source           600 s
//	SubagentStart       one warning; cannot block                sub-agent type   600 s
//	PreCompact          one warning; cannot block                trigger          600 s
//	PostCompact         one warning; cannot block                trigger          600 s
//	SessionEnd          nothing; an objection is a warning       reason           1.5 s
//	StopFailure         nothing; what it prints is not read      error kind       600 s
//	InstructionsLoaded  nothing; what it prints is not read      load reason      600 s
//	Notification        nothing; what it prints is not read      notification     600 s
//	PreModelSwitch      the model is not switched                model switched to 30 s
//	PostModelSwitch     one warning; cannot block                model switched to 30 s
//	ConfigChange        the new settings are not taken in        source           600 s
//	PostToolBatch       the model is not asked again             (no matcher)     600 s
//	FileChanged         one warning; cannot block                file's name      600 s
//
// A hook's own "timeout" replaces the default; on SessionEnd it counts up
// to 60 seconds. A hook on an event that takes no matcher must not have
// one.
//
// # Matchers
//
// Empty, or "*": everything. Only letters, digits, "_", "-", spaces, ","
// and "|": exact names separated by "|" or ",", so "Bash" does not fit
// "BashOutput". Anything else: a regular expression (Go's, so without
// look-around or backreferences) that may match anywhere in the name, as
// in "mcp__github__.*".
//
// A tool also answers to its older names (Task for Agent, KillShell for
// TaskStop). And a tool Claude Code has nothing like is guarded by the
// hooks written for the Claude Code tool that does the same job
// (Options.Guards): such a hook can block the call, put it to the user,
// add text or change the input, but its approval does not count, because
// what it checked was another tool's input. A name added this way can
// only make gnosh stricter.
//
// # How a hook runs
//
// All the hooks that fit one occurrence of an event start at the same
// time; two with the same command run once. Each is a shell command
// (Options.Shell -c), or in Claude Code's exec form (Hook.Args) a program
// found on the hook's PATH run with its arguments as they are, in the
// session's working directory, in a session and process group of its own
// with no terminal. All are waited for, but those that run in the
// background (Hook.Async, Hook.AsyncRewake: see background.go), which
// decide nothing. A hook
// that passes its timeout, or is still running when the turn is cancelled,
// is stopped with its whole process group: SIGTERM, and SIGKILL after
// Options.KillGrace.
//
// A hook may leave a program running behind it, provided that program
// does not hold the hook's output open: until the output is let go the
// hook may not have said everything, and after Options.PipeWait it counts
// as one that could not evaluate. What a hook prints is read up to
// Options.MaxOutput and no further, without holding the hook up.
//
// On stdin a hook reads one JSON object with Claude Code's field names:
// hook_event_name, session_id, transcript_path, cwd, permission_mode (on
// the tool events, UserPromptSubmit, Stop and SubagentStop), agent_id and
// agent_type (inside a sub-agent), and the event's own fields, which the
// Input type lists.
//
// In its environment a hook finds GNOSH=1, AGENT=gnosh, AI_AGENT=gnosh,
// GNOSH_HOOK_EVENT_NAME, GNOSH_SESSION_ID, GNOSH_CWD, GNOSH_PROJECT_DIR and
// CLAUDE_PROJECT_DIR (the same directory under both names), and where
// they apply GNOSH_TOOL_NAME, GNOSH_TOOL_USE_ID, GNOSH_TOOL_INPUT_COMMAND,
// GNOSH_TOOL_INPUT_FILE_PATH, GNOSH_STOP_HOOK_ACTIVE (1 or 0),
// GNOSH_AGENT_ID, GNOSH_AGENT_TYPE, GNOSH_SESSION_START_SOURCE,
// GNOSH_SESSION_END_REASON, GNOSH_PRE_COMPACT_TRIGGER,
// GNOSH_POST_COMPACT_TRIGGER, GNOSH_STOP_FAILURE_ERROR,
// GNOSH_INSTRUCTIONS_LOAD_REASON, GNOSH_NOTIFICATION_TYPE, GNOSH_ENV_FILE
// and CLAUDE_ENV_FILE (SessionStart only) and GNOSH_MEM_STATUS. A variable
// that does not apply to the event is unset, even if gnosh's own
// environment had it, and so is CLAUDECODE: gnosh does not pass itself off
// as another program.
//
// A variable that repeats a field of the JSON object is named for it, for
// a script that would rather not read JSON: the field as Claude Code
// documents it, in upper case behind GNOSH_ (hook_event_name,
// tool_input.command). Where the field belongs to one event and its name
// alone would not say which (source, reason, trigger, error, load_reason),
// the event's name, or the start of it, stands in front
// (GNOSH_SESSION_START_SOURCE, GNOSH_INSTRUCTIONS_LOAD_REASON).
// GNOSH_PROJECT_DIR and GNOSH_ENV_FILE stand beside Claude Code's own two
// variables, and GNOSH_MEM_STATUS is gnosh's alone.
//
// # What a hook's answer means
//
// The exit status:
//
//	0                    It ran. What it printed is read (below).
//	2                    Block, with what it wrote to stderr as the reason.
//	3                    It could not evaluate. (gnosh's own; 3 is "unknown"
//	                     where 0 is "OK" and 2 is "critical".)
//	126, 127, above 128  It could not start, a command in it was not found,
//	                     or one was killed. As 3.
//	any other            With text on stderr: it ran into trouble and did not
//	                     check. As 3, with the text as the reason.
//	                     Silent: a test that came out false. No opinion.
//
// What it printed counts as a JSON answer when it starts with "{" and
// ends with "}". Otherwise it is plain text: on UserPromptSubmit and
// SessionStart, from a hook that exited 0, text for the model; on
// PreCompact, extra instructions for the summary; elsewhere nothing. One
// kind of plain text is not let pass: a JSON answer with other lines
// around it, such as a shell profile's greeting. To drop the answer with
// the greeting would be to drop a "deny", so it is an answer that could
// not be read.
//
// The JSON answer is Claude Code's:
//
//	"continue": false, "stopReason"   end the turn, whatever other hooks said
//	"systemMessage"                   a warning to show the user; blocks nothing
//	"decision": "block", "reason"     block (on PreToolUse: deny)
//	"decision": "approve"             on PreToolUse: allow (the older form)
//	"hookSpecificOutput":
//	  "permissionDecision"            PreToolUse: "allow", "deny" or "ask";
//	  "permissionDecisionReason"      it wins over a top-level "decision"
//	  "updatedInput"                  PreToolUse: the whole new tool input
//	  "additionalContext"             text for the model: a string or a list
//	  "decision": {"behavior": ...}   PermissionRequest: "allow", or "deny"
//	                                  with "message" and "interrupt"
//
// The answer is read strictly. A decision word that is not exactly one of
// those ("Deny", "reject", "defer"), a field of the wrong type, a changed
// input that is not an object, a field gnosh does not know, and a request
// gnosh does not carry out (initialUserMessage) each
// make it an answer that could not be read, and the message names the
// field. Requests that are harmless to leave undone (terminalSequence,
// sessionTitle, watchPaths, reloadSkills) are noted in the hook's Record.
// A PermissionRequest hook that allows a call with a changed input or
// changed permissions has not answered: the question is shown for the call
// as it stands.
//
// A non-zero exit can only make an answer stricter. Whatever the status,
// a refusal in the JSON holds, even beside a field that could not be read.
// An approval counts only from a hook that exited 0, whose whole answer
// was understood, and that was written for the tool it approved.
//
// A hook's reason, its added text and its warning are each passed on at
// no more than Options.MaxText characters: longer text keeps its start
// and its end around a line that says how much was left out.
//
// # Could not evaluate: gnosh fails closed
//
// A hook could not evaluate when it exited 3, 126, 127 or above 128; when
// it exited with any other status but 0 and 2 and wrote to stderr; when it
// could not be started; when it was killed by a signal; when it passed
// its timeout; when it printed more than Options.MaxOutput; when it exited
// and left a process holding its output open; or when its answer could
// not be read. What happens then:
//
//	PreToolUse           The call is blocked. The model reads that the hook
//	                     could not check, the hook's own words, and that a
//	                     check that could not run does not pass.
//	PostToolUse,         The same sentence goes to the model as feedback.
//	PostToolUseFailure
//	UserPromptSubmit     The prompt is blocked, with the reason.
//	Stop, SubagentStop   The turn is held open, as by a block. (The loop's
//	                     reflection checkpoint is the way out.)
//	PermissionRequest    No answer: the question goes to the user, with a
//	                     warning saying why the hook did not answer.
//	SessionStart,        Nothing is blocked. One warning; the session, the
//	SubagentStart,       sub-agent or the summary goes ahead without the
//	PreCompact,          hook.
//	PostCompact
//	SessionEnd,          Nothing is shown. The hook's Record says that it
//	StopFailure,         could not evaluate.
//	InstructionsLoaded,
//	Notification
//
// In every case Outcome.CouldNotEvaluate is set and the hook's Record has
// the result "could_not_evaluate", so that no interface can show it as a
// check that passed. A turn the user cancelled is not a hook's failure:
// Run returns the context's error and reports no hook as failed.
//
// # Several hooks on one event
//
// Their answers are combined in the order the hooks are configured, never
// in the order they finished. A block wins over an ask, an ask over an
// approval, an approval over no opinion. A hook that ends the turn ends
// it whatever the others said. Reasons, added text and warnings are joined
// in configured order.
//
// Two hooks that both change a tool's input must agree. Each saw the call
// as the model wrote it, so two different rewrites cannot both be
// applied, and the call is blocked. For the same reason an approval is
// for the input its hook saw or wrote: when another hook changes the
// input, that approval does not count and the call goes to the normal
// permission check.
//
// # Configuration, and what stops startup
//
// ParseConfig reads Claude Code's "hooks" block; NewHook checks a hook
// written as a settings line; Merge puts layers together, weakest first.
// Hooks never override each other, except that a hook with a name
// replaces the earlier hook of the same event and name.
//
// Check returns every problem at once, and NewRunner refuses a
// configuration that has one: an event name that is not exactly one of
// gnosh's events (a misspelling, another letter case, or a Claude Code event
// gnosh does not have); an event this build does not fire
// (Options.Fires); a hook type gnosh does not have (it has "command",
// "http", "mcp_tool", "prompt" and "agent"); an "if" that is not a string;
// "args" that are not a list of strings; "onFailure" other than
// "continue"; any field gnosh does not know; a name written twice in one JSON object; a matcher
// on an event that takes none, or one that cannot be read; a missing
// command; a timeout that is not above zero; and, in the user's own
// configuration, a command that starts with a relative path.
//
// # Where gnosh differs from Claude Code
//
//   - A hook that could not evaluate blocks. In Claude Code it passes.
//   - A non-zero exit other than 2 blocks when the hook wrote to stderr.
//     In Claude Code it never blocks.
//   - An unknown event, or an entry gnosh cannot run as written, stops
//     startup. Claude Code skips it with a warning, or runs it another way
//     (a matcher on an event that takes none).
//   - An "if" filter takes a tool call whenever a deny rule of the same
//     text would refuse the call or could apply to it (the agent loop's
//     Input.IfMatch): it is as strong as a deny rule, and a hook that
//     guards is never skipped for a call the rule could be about.
//   - Stop hooks are not capped, and exit 2 with "No such file or
//     directory" on stderr still blocks. (The agent loop asks the model to
//     reflect at every tenth block in a row.)
//   - PreCompact cannot stop a summary, and SubagentStop takes no matcher.
//   - "${CLAUDE_PROJECT_DIR}" in a shell command is filled in by the
//     shell from the environment, not pasted into the command's text. (In
//     the exec form, where no shell reads it, gnosh puts it in, as Claude
//     Code does.)
//   - A hook's answer is read strictly, as above, and text that is too
//     long is cut rather than saved to a file.
//   - "onFailure" may only say "continue", which changes nothing.
//
// # What the agent loop does around this package
//
// It fires the events at their moments and applies each Outcome; it
// counts Stop blocks and sends the reflection checkpoint; it stores added
// text with the prompt it belongs to; it does not wait for
// InstructionsLoaded and Notification hooks; and it shows every Record.
package hooks
