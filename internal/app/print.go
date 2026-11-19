package app

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/gnosh-ai/gnosh/internal/text"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/gnosh-ai/gnosh/internal/agent"
	"github.com/gnosh-ai/gnosh/internal/cli"
	"github.com/gnosh-ai/gnosh/internal/core"
	"github.com/gnosh-ai/gnosh/internal/hooks"
	"github.com/gnosh-ai/gnosh/internal/prompt"
)

// printer is the front end of a run that nobody attends (`gnosh -p`): it
// sends the one prompt, writes standard output in the format that was
// asked for and nothing else, puts everything a person would be shown as a
// warning on standard error, and says with the exit code how the run
// ended.
//
// The three formats:
//
//	text         the model's words. Each reply's text is written when the
//	             reply is complete (as it arrives, when standard output
//	             is a terminal), replies set apart by a blank line.
//	json         one object at the end: the result.
//	stream-json  one JSON object a line while the run happens: a line
//	             that says what the run uses, a line for each block of
//	             each reply, a line for each tool result, and the result
//	             as the last line.
//
// The shapes are Claude Code's (spec/cc/output-formats.md), so that
// scripts and jq filters written for it keep working; the first line says
// gnosh_version, since gnosh is not Claude Code.
type printer struct {
	p       *program
	inv     cli.Invocation
	format  string
	verbose bool
	// live says standard output is a terminal, where a person watches the
	// words arrive. safe is what of the reply may be written there: its
	// styles, and no other escape sequence (text.SafeStream).
	live bool
	safe text.SafeStream

	mu sync.Mutex
	c  Controls

	// asking are the permission questions put to the agent view (a run in
	// the background, runasks.go), each with what ends its wait.
	asking map[string]context.CancelFunc

	// The state of text output.
	buf       strings.Builder // the text of the reply being written
	inMessage bool            // something of the reply has been written out
	wroteAny  bool            // something has been written to stdout at all
	endsLine  bool            // what was written last ended a line

	// The state of stream-json output.
	started bool // the first line has been written
	msgSeq  int
	// sm is the reply being streamed as stream events
	// (--include-partial-messages).
	sm streamed
	// turnStart, when set, is when the prompt of the turn being reported
	// went out (--input-format stream-json): a result's duration is the
	// turn's own.
	turnStart time.Time
	// starts, in a run of many, hands on the prompts each turn starts
	// with, so that the next message waits for its turn to begin.
	starts chan []string
	// structured is the run's result as JSON that conforms to
	// --json-schema, or structuredErr why there is none.
	structured    json.RawMessage
	structuredErr string

	capped bool
	// cappedTurn is the turn that reached the cap.
	cappedTurn string
	// resultsOut says each turn's result line was written as its turn
	// ended (stream-json output with background work, printwait.go): the
	// last is not written again.
	resultsOut bool
	// rounds counts the run's own replies as they are reported, for a
	// result that has to be written before the turn has said how it
	// ended.
	rounds int
	// signal is the signal that stopped the run, and inTools says a tool
	// call was running when it came.
	signal  os.Signal
	inTools bool
	// finished says the run's result has been written: nothing follows it.
	finished bool
	// toldUnattended says the person has been told why calls are refused.
	toldUnattended bool
	// noArgs are the tools whose arguments and results --verbose does not
	// show (ToolName.NoLogArgs).
	noArgs  map[string]bool
	results chan agent.TurnResult
	// inboxed counts the prompts the run's inbox handed it (runasks.go).
	inboxed atomic.Int64
}

// newPrinter makes the front end of a run.
func newPrinter(p *program, inv cli.Invocation) *printer {
	return &printer{
		p: p, inv: inv,
		format:  inv.OutputFormat,
		verbose: inv.Verbose,
		live:    p.o.StdoutIsTerminal,
		results: make(chan agent.TurnResult, 64),
		noArgs:  p.noLogTools(),
	}
}

// maxPiped is the most text a run reads from standard input.
const maxPiped = 10 << 20

// Run sends the run's prompt, waits for its turn to end, prints the
// result and returns the exit code.
func (f *printer) Run(ctx context.Context, c Controls) int {
	f.mu.Lock()
	f.c = c
	f.mu.Unlock()
	// A run with a time budget is told to wrap up once most of it is used
	// (printwait.go).
	defer f.watchBudget(c, time.Now())()
	if f.inv.InputFormat == cli.FormatStreamJSON {
		return f.runStream(ctx, c)
	}

	var text string
	var err error
	if sig := f.guarded(ctx, c, func(ctx context.Context) { text, err = f.readPrompt(ctx) }); sig != nil {
		return f.stoppedBySignal(c, "", sig, c.State())
	}
	first := agent.Prompt{Text: text}
	if errors.Is(err, errNoPrompt) {
		// Nothing was typed and nothing was piped in. A feature may still
		// have something for the run to do: a flag it claims.
		if p, ok := c.RunPrompt(ctx); ok {
			first, text, err = p, "", nil
		}
	}
	if err != nil {
		f.p.errf("%v", err)
		return cli.ExitFailure
	}
	id := ""
	if strings.HasPrefix(strings.TrimSpace(text), "/") {
		var res CommandResult
		var err error
		if sig := f.guarded(ctx, c, func(ctx context.Context) { res, err = c.Command(ctx, strings.TrimSpace(text)) }); sig != nil {
			return f.stoppedBySignal(c, "", sig, c.State())
		}
		if (res.Handled && res.Prompt == "") || err != nil {
			return f.reportCommand(c, res, err)
		}
		// A skill: the command sent it to the model, and the run is that
		// prompt's turn.
		id = res.Prompt
	}
	if id == "" {
		if id, err = c.Submit(first); err != nil {
			f.p.errf("the prompt could not be sent: %v", err)
			return cli.ExitFailure
		}
	}
	stopInbox := func() {}
	if f.p.runAsks != "" {
		// A run in the background reads the words sent to it from the
		// agent view as it works (runasks.go), once its prompt is in, and
		// until it has nothing left to do: what comes after stays in its
		// inbox, for the view to send it back to work on.
		inbox, stop := context.WithCancel(ctx)
		defer stop()
		done, _ := readInbox(inbox, f.p.runAsks, func(text string) error {
			_, err := c.Submit(agent.Prompt{Text: text})
			if err == nil {
				f.inboxed.Add(1)
			}
			return err
		})
		stopInbox = func() { stop(); <-done }
	}
	res, sig, st := f.await(ctx, c, id)
	if sig != nil {
		return f.stoppedBySignal(c, id, sig, st)
	}
	if ctx.Err() != nil {
		return cli.ExitFailure
	}
	// The background work the turn started is waited for, and the turns
	// its ends start are the run's too (printwait.go).
	for {
		res, sig, st = f.awaitBackground(ctx, c, res, false)
		// The inbox is read no more; a prompt it handed over as it stopped
		// is still waited for.
		took := f.inboxed.Load()
		stopInbox()
		if sig != nil || ctx.Err() != nil || f.inboxed.Load() == took {
			break
		}
	}
	if sig != nil {
		return f.stoppedBySignal(c, "", sig, st)
	}
	if ctx.Err() != nil {
		return cli.ExitFailure
	}
	if f.inv.JSONSchema != "" && (res.Reason == agent.EndCompleted || res.Reason == agent.EndModelStopped) {
		if sig := f.guarded(ctx, c, func(ctx context.Context) { f.structured, f.structuredErr = f.structuredOutput(ctx, c) }); sig != nil {
			return f.stoppedBySignal(c, "", sig, c.State())
		}
	}
	// The features are asked before anything is printed: what they have
	// to say comes first, and they may raise the exit code.
	code := c.RunEnd(res, f.exitCode(c, res))
	f.report(c, res)
	return code
}

// guarded runs a part of a run that does not read the signals itself
// (reading what is piped in, a slash command given as the prompt, the
// structured output): the first SIGINT, SIGTERM or SIGHUP cancels it and
// is returned, so that the run ends as one a signal stopped does. Without
// it a signal waited unread through the part (ten minutes and more), and
// "producer | timeout 60 gnosh -p" could not be stopped short of SIGKILL.
func (f *printer) guarded(ctx context.Context, c Controls, part func(ctx context.Context)) os.Signal {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	signals := c.Signals()
	got := make(chan os.Signal, 1)
	done := make(chan struct{})
	var watching sync.WaitGroup
	watching.Add(1)
	go func() {
		defer watching.Done()
		select {
		case s := <-signals:
			got <- s
			cancel()
		case <-done:
		}
	}()
	part(ctx)
	close(done)
	watching.Wait()
	select {
	case s := <-got:
		return s
	default:
		return nil
	}
}

// exitCode is the code a run ends with for how its last turn ended: 0
// when the turn finished, 1 when it failed, was ended, never started, or
// ran out of turns.
func (f *printer) exitCode(c Controls, res agent.TurnResult) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.result(c, res).IsError || res.Reason == agent.EndRefused {
		return cli.ExitFailure
	}
	return cli.ExitOK
}

// readPrompt puts the run's prompt together: what was piped in, then a
// blank line, then the prompt of the command line. A pipe that stays
// silent is not waited on for ever when the command line gave a prompt.
func (f *printer) readPrompt(ctx context.Context) (string, error) {
	arg := f.inv.Prompt
	piped := ""
	if f.inv.ReadStdin {
		var err error
		if piped, err = f.p.readPiped(ctx, arg != ""); err != nil {
			return "", err
		}
	}
	text := joinPrompt(piped, arg)
	if strings.TrimSpace(text) == "" {
		return "", errNoPrompt
	}
	return text, nil
}

// errNoPrompt means a run was given no prompt.
var errNoPrompt = errors.New("the prompt is empty, so nothing was sent. Put the prompt on the command line, or pipe it in")

// joinPrompt puts what was piped in before the prompt of the command line,
// with a blank line between the two.
func joinPrompt(piped, arg string) string {
	switch {
	case strings.TrimSpace(piped) == "":
		return arg
	case arg == "":
		return piped
	}
	return strings.TrimRight(piped, "\n") + "\n\n" + arg
}

// readPiped reads standard input to its end. With a prompt already given,
// it waits only a moment for the first byte: a run started with an open
// standard input that never says anything goes on without it.
func (p *program) readPiped(ctx context.Context, havePrompt bool) (string, error) {
	type chunk struct {
		data []byte
		err  error
	}
	chunks := make(chan chunk, 16)
	quit := make(chan struct{})
	defer close(quit)
	go func() {
		for {
			buf := make([]byte, 64<<10)
			n, err := p.o.Stdin.Read(buf)
			select {
			case chunks <- chunk{buf[:n], err}:
			case <-quit:
				return
			}
			if err != nil {
				return
			}
		}
	}()
	wait := p.o.StdinWait
	if wait <= 0 {
		wait = 2 * time.Second
	}
	var silence <-chan time.Time
	if havePrompt {
		timer := time.NewTimer(wait)
		defer timer.Stop()
		silence = timer.C
	}
	var all []byte
	for {
		select {
		case c := <-chunks:
			if len(c.data) > 0 {
				silence = nil // it speaks: it is read to its end
				all = append(all, c.data...)
				if len(all) > maxPiped {
					return "", fmt.Errorf("more than %d MB was piped in, and gnosh reads at most that much from standard input, so nothing was sent. Put the material in a file and name the file in the prompt", maxPiped>>20)
				}
			}
			switch {
			case c.err == io.EOF:
				return string(all), nil
			case c.err != nil:
				p.warnf("standard input could not be read (%v); gnosh goes on with the prompt from the command line.", c.err)
				return string(all), nil
			}
		case <-silence:
			p.warnf("nothing arrived on standard input within %s, so gnosh goes on with the prompt from the command line.", wait)
			return "", nil
		case <-ctx.Done():
			return "", ctx.Err()
		}
	}
}

// await waits for the turn that answers the prompt to end, or for the
// prompt to come back unsent. A signal stops the turn and is returned,
// with what the session was doing when it came.
func (f *printer) await(ctx context.Context, c Controls, id string) (agent.TurnResult, os.Signal, agent.State) {
	signals := c.Signals()
	for {
		select {
		case res := <-f.results:
			if slices.Contains(res.Prompts, id) {
				return res, nil, agent.State{}
			}
		case s := <-signals:
			// The run is over. The turn is stopped, and the rest (its
			// commands, the SessionEnd hooks, the session's file) is the
			// shutdown's, which a second signal cuts short.
			st := c.State()
			c.Interrupt()
			return agent.TurnResult{Reason: agent.EndStopped}, s, st
		case <-ctx.Done():
			return agent.TurnResult{Reason: agent.EndStopped}, nil, agent.State{}
		}
	}
}

// signalWait is how long a run that a signal stopped gives its turn to say
// how far it had come, before the result is written without that. Stopping
// a request or a command takes a moment; whoever sent the signal may
// follow it with a kill, so the result is not held back for long.
const signalWait = 2 * time.Second

// stoppedBySignal ends a run that a signal stopped, and returns its exit
// code: 130 after SIGINT, 143 after SIGTERM. The text format prints
// nothing more. The two JSON formats print the result object, so that a
// script that stops a run still learns what the run was: how many replies
// the model had written, what it had cost, and that it was stopped and by
// what.
//
// From the moment the signal is acted on, the program is shutting down: a
// second signal ends the process at once, whether or not the result has
// been written.
func (f *printer) stoppedBySignal(c Controls, id string, sig os.Signal, st agent.State) int {
	code := signalExit(sig)
	if f.format == cli.FormatText {
		return code
	}
	f.p.beginClosing(code)
	res := agent.TurnResult{Reason: agent.EndStopped}
	wait := time.NewTimer(signalWait)
	defer wait.Stop()
	// (With no turn of the run's own to wait for, no result can come.)
	for waiting := id != ""; waiting; {
		select {
		case r := <-f.results:
			if slices.Contains(r.Prompts, id) {
				res, waiting = r, false
			}
		case <-wait.C:
			waiting = false
		}
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.signal, f.inTools = sig, len(st.Running) > 0
	obj := f.result(c, res)
	if f.format == cli.FormatStreamJSON {
		f.init()
	}
	f.encode(obj)
	f.finished = true
	// A question still put to the agent view goes with the run.
	for id, cancel := range f.asking {
		cancel()
		delete(f.asking, id)
	}
	if f.p.runAsks != "" {
		paths, _ := filepath.Glob(f.p.runAsks + ".ask-*.json")
		for _, p := range paths {
			_ = os.Remove(p)
		}
	}
	return code
}

// signalWord names a signal the way a result's message writes it.
func signalWord(sig os.Signal) string {
	switch sig {
	case os.Interrupt:
		return "an interrupt (SIGINT)"
	case syscall.SIGTERM:
		return "SIGTERM"
	}
	return "a signal (" + sig.String() + ")"
}

// Event takes in what happens in the session.
func (f *printer) Event(ev any) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.finished {
		// The result is the last thing a run prints. A notice of its
		// closing (a hook's, the memory's) still goes to standard error.
		if ev, ok := ev.(*agent.NoticeEvent); ok {
			f.stderr("%s", ev.Text)
		}
		return
	}
	switch ev := ev.(type) {
	case *agent.QuestionEvent:
		// Only a run whose questions the agent view answers is put any
		// (agent.Options.Relay): it writes the question down and waits.
		if f.p.runAsks == "" || f.c == nil {
			return
		}
		if f.asking == nil {
			f.asking = map[string]context.CancelFunc{}
		}
		ctx, cancel := context.WithCancel(context.Background())
		f.asking[ev.Question.ID] = cancel
		q, c := ev.Question, f.c
		f.stderr("waiting for an answer in gnosh agents: %s", oneLine(firstOf(q.Title, q.Kind), 200))
		go relayAsk(ctx, f.p.runAsks, q, f.p.now, func(a agent.Answer) error { return c.Answer(q.ID, a) })
	case *agent.AnswerEvent:
		if cancel := f.asking[ev.ID]; cancel != nil {
			cancel()
			delete(f.asking, ev.ID)
		}
	case *agent.TurnStartEvent:
		f.init()
		if f.starts != nil && ev.Agent == "" {
			select {
			case f.starts <- ev.Prompts:
			default:
			}
		}
	case *agent.RequestEvent:
		if ev.Purpose == "main" && ev.Agent == "" {
			f.sm.model, f.sm.sent = ev.Model, ev.Time
		}
	case *agent.TextEvent:
		if ev.Agent == "" {
			f.text(ev.Text)
			f.partialPiece(ev)
		}
	case *agent.ThinkingEvent:
		if ev.Agent == "" {
			f.partialPiece(ev)
		}
	case *agent.ToolCallEvent:
		if ev.Agent == "" {
			f.partialPiece(ev)
		}
	case *agent.ToolInputEvent:
		if ev.Agent == "" {
			f.partialPiece(ev)
		}
	case *agent.ReplyEvent:
		if ev.Purpose != "main" {
			return
		}
		if ev.Agent == "" {
			f.rounds++
			f.endReply()
			f.partialWhole(ev.Message)
		}
		f.replyLines(ev)
		if ev.Agent == "" {
			f.partialEnd(ev.StopReason, ev.Usage)
		}
	case *agent.RetryEvent:
		if ev.Discarded && ev.Agent == "" && ev.Purpose == "main" {
			// What had arrived of the reply is thrown away; the next try
			// writes it afresh. (A sub-agent's reply is not the one held
			// here: its retry threw away the session's own reply, and its
			// stream-json message, half written.)
			f.buf.Reset()
			f.safe.Reset()
			f.inMessage = false
			f.partialEnd(core.StopCancelled, core.Usage{})
		}
		f.stderr("%s", ev.Message)
		f.line(retryLine{Type: "system", Subtype: "api_retry", UUID: newUUID(), SessionID: f.sessionID(),
			Attempt: ev.Attempt, RetryDelayMS: ev.Wait.Milliseconds(), ErrorStatus: statusOrNil(ev.Status), Error: string(ev.Kind)})
	case *agent.ToolEndEvent:
		f.toolLine(ev)
		if f.verbose {
			mark := ""
			if ev.Result.IsError || ev.Denied {
				mark = " [failed]"
			}
			indent := ""
			if ev.Agent != "" {
				indent = "  "
			}
			if f.noArgs[ev.Name] {
				// Neither its arguments nor its answer are shown here.
				f.stderr("%s%s%s", indent, ev.Name, mark)
			} else {
				f.stderr("%s%s (%s)%s", indent, toolLine(ev.Name, ev.Input), resultLine(ev.Result, ev.Denied, ev.Interrupted), mark)
			}
		}
		if ev.Denied && ev.Unattended && !f.toldUnattended {
			// Said once: the person who started the run is the one who
			// can let it act, and the model cannot tell them how.
			f.toldUnattended = true
			if f.c != nil && f.c.Mode() == cli.ModeBypassPermissions {
				// Permissions are skipped already: what still asks is what
				// no flag waves through, and "give it -y" would be advice
				// the person has taken.
				f.stderr("%s was not run: it is one of the calls gnosh puts to a person even with permissions skipped (a removal that cannot be undone, or what an ask rule or a hook wants asked), and a run has nobody to ask. No flag lets a run approve such a call: run it yourself if it is wanted. (This is said once; other calls may be refused the same way, and the result lists them.)", ev.Name)
			} else {
				f.stderr("%s was not run: it needs a person's approval, and a run has nobody to ask. To let a run act, give it -y, a --permission-mode, or an --allowedTools rule for what it may do. (This is said once; other calls may be refused the same way, and the result lists them.)", ev.Name)
			}
		}
		if ev.Denied {
			f.line(deniedLine{Type: "system", Subtype: "permission_denied", UUID: newUUID(), SessionID: f.sessionID(),
				ToolName: ev.Name, ToolUseID: ev.CallID, Message: core.TextOf(ev.Result.Content)})
		}
	case *agent.NoticeEvent:
		f.stderr("%s", ev.Text)
		level := "info"
		if ev.Level != agent.LevelInfo {
			level = "warning"
		}
		if f.started {
			f.line(infoLine{Type: "system", Subtype: "informational", UUID: newUUID(), SessionID: f.sessionID(), Content: ev.Text, Level: level})
		}
	case *agent.DriverEvent:
		if ev.Action == agent.Continue {
			what := "holds the turn open"
			if ev.Checkpoint {
				what += ", and the model is asked whether there is a real reason to stop"
			}
			f.stderr("the check %s %s: %s", ev.Name, what, oneLine(ev.Objection, 300))
		}
	case *agent.SummaryEvent:
		switch {
		case ev.Phase == "start":
			f.stderr("the conversation is being summarized to make room (%s).", ev.Trigger)
			f.line(statusLine{Type: "system", Subtype: "status", UUID: newUUID(), SessionID: f.sessionID(), Status: ptr("compacting")})
		case ev.Err == nil:
			f.line(compactLine{Type: "system", Subtype: "compact_boundary", UUID: newUUID(), SessionID: f.sessionID(),
				Metadata: compactMeta{Trigger: ev.Trigger, PreTokens: ev.PreTokens}})
			f.line(statusLine{Type: "system", Subtype: "status", UUID: newUUID(), SessionID: f.sessionID()})
		default:
			f.line(statusLine{Type: "system", Subtype: "status", UUID: newUUID(), SessionID: f.sessionID()})
		}
	case *HookEvent:
		for _, r := range ev.Records {
			switch r.Result {
			case hooks.ResultBlocked, hooks.ResultCouldNotEvaluate, hooks.ResultEndedTurn, hooks.ResultWarning, hooks.ResultFeedback, hooks.ResultAsked:
				f.stderr("%s", r.String())
			default:
				if f.verbose {
					f.stderr("%s", r.String())
				}
			}
		}
	case *CapEvent:
		f.capped, f.cappedTurn = true, ev.Turn
		f.stderr("the run has had its %d turns of the model (--max-turns) and stops here.", ev.Turns)
	case *agent.TurnEndEvent:
		f.offer(ev.Result)
	case *agent.PromptReturnedEvent:
		reason := agent.EndWithdrawn
		if ev.Why == agent.ReturnRefused {
			reason = agent.EndRefused
		}
		f.offer(agent.TurnResult{Prompts: []string{ev.ID}, Reason: reason, Detail: ev.Reason})
	}
}

// offer hands a turn's end to Run.
func (f *printer) offer(res agent.TurnResult) {
	select {
	case f.results <- res:
	default:
	}
}

// stderr prints one line on standard error, where a person reads it: what
// a model wrote may be in it (a tool's input), so it may only colour
// itself (text.Safe).
func (f *printer) stderr(format string, args ...any) {
	fmt.Fprintln(f.p.stderr, "gnosh: "+text.Safe(strings.TrimRight(fmt.Sprintf(format, args...), "\n")))
}

// sessionID is the session's id.
func (f *printer) sessionID() string {
	if f.c == nil {
		return ""
	}
	return f.c.Info().ID
}

// text takes the next piece of the answer.
func (f *printer) text(s string) {
	if f.format != cli.FormatText || f.inv.JSONSchema != "" {
		// (With --json-schema the text format prints the JSON alone.)
		return
	}
	if !f.live {
		f.buf.WriteString(s)
		return
	}
	if !f.inMessage {
		// The blanks a model begins a reply with are not the answer.
		if s = strings.TrimLeft(s, " \t\r\n"); s == "" {
			return
		}
		f.separate()
		f.inMessage = true
	}
	// On a terminal, what the model writes may only colour itself: an
	// escape sequence in it could otherwise move the cursor, set the
	// title or write the clipboard. (A pipe or a file gets it as it is.)
	if s = f.safe.Write(s); s == "" {
		return
	}
	fmt.Fprint(f.p.stdout, s)
	f.wroteAny, f.endsLine = true, strings.HasSuffix(s, "\n")
}

// since is when the turn being reported began: its prompt's, in a run of
// many, and the run's own start otherwise.
func (f *printer) since() time.Time {
	if !f.turnStart.IsZero() {
		return f.turnStart
	}
	return f.p.start
}

// dropEchoedLine takes off the end of a reply what a model repeated of
// gnosh's own lines after its answer: a last line, or a last block that
// closes, in gnosh's marker ("ready", and then the mem line), or the
// marker's closing tag stuck to the answer ("ready</gnosh>"). A run's
// result is read by scripts. A reply that is nothing but such text is left
// as it is, and so is a marked block that does not run to the end.
func dropEchoedLine(text string) string {
	t := strings.TrimRight(text, " \t\r\n")
	if strings.HasSuffix(t, prompt.Close) && !strings.Contains(t, prompt.Open) {
		if head := strings.TrimRight(strings.TrimSuffix(t, prompt.Close), " \t\r\n"); head != "" {
			return head
		}
		return text
	}
	i := strings.LastIndex(t, "\n"+prompt.Open)
	if i < 0 {
		return text
	}
	if tail := t[i+1:]; strings.Contains(tail, "\n") && !strings.HasSuffix(tail, prompt.Close) {
		return text
	}
	head := strings.TrimRight(t[:i], " \t\r\n")
	if head == "" {
		return text
	}
	return head
}

// separate sets a new message apart from the one written before it.
func (f *printer) separate() {
	if !f.wroteAny {
		return
	}
	if !f.endsLine {
		fmt.Fprintln(f.p.stdout)
	}
	fmt.Fprintln(f.p.stdout)
	f.endsLine = true
}

// endReply is called when a reply is complete: its text is written out.
func (f *printer) endReply() {
	if f.format != cli.FormatText {
		return
	}
	if !f.live {
		if msg := strings.TrimSpace(dropEchoedLine(f.buf.String())); msg != "" {
			f.separate()
			fmt.Fprintln(f.p.stdout, msg)
			f.wroteAny, f.endsLine = true, true
		}
		f.buf.Reset()
	}
	f.safe.Reset()
	f.inMessage = false
}

// The lines of stream-json output, each with its fields in the order
// Claude Code documents them.

type initLine struct {
	Type           string            `json:"type"`
	Subtype        string            `json:"subtype"`
	UUID           string            `json:"uuid"`
	SessionID      string            `json:"session_id"`
	Cwd            string            `json:"cwd"`
	Model          string            `json:"model"`
	Tools          []string          `json:"tools"`
	MCPServers     []MCPServerStatus `json:"mcp_servers"`
	PermissionMode string            `json:"permissionMode"`
	SlashCommands  []string          `json:"slash_commands"`
	OutputStyle    string            `json:"output_style"`
	Skills         []string          `json:"skills"`
	Plugins        []string          `json:"plugins"`
	APIKeySource   string            `json:"apiKeySource"`
	GnoshVersion   string            `json:"gnosh_version"`
}

type apiUsage struct {
	InputTokens              int `json:"input_tokens"`
	OutputTokens             int `json:"output_tokens"`
	CacheCreationInputTokens int `json:"cache_creation_input_tokens"`
	CacheReadInputTokens     int `json:"cache_read_input_tokens"`
}

func usageOf(u core.Usage) apiUsage {
	return apiUsage{InputTokens: u.InputTokens, OutputTokens: u.OutputTokens, CacheCreationInputTokens: u.CacheWriteTokens, CacheReadInputTokens: u.CacheReadTokens}
}

type apiMessage struct {
	ID           string   `json:"id"`
	Type         string   `json:"type"`
	Role         string   `json:"role"`
	Model        string   `json:"model"`
	Content      []any    `json:"content"`
	StopReason   *string  `json:"stop_reason"`
	StopSequence *string  `json:"stop_sequence"`
	Usage        apiUsage `json:"usage"`
}

type assistantLine struct {
	Type            string     `json:"type"`
	UUID            string     `json:"uuid"`
	SessionID       string     `json:"session_id"`
	ParentToolUseID *string    `json:"parent_tool_use_id"`
	Message         apiMessage `json:"message"`
	Aborted         bool       `json:"aborted,omitempty"`
	AgentID         string     `json:"agent_id,omitempty"`
}

type textBlock struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

type thinkingBlock struct {
	Type      string `json:"type"`
	Thinking  string `json:"thinking"`
	Signature string `json:"signature,omitempty"`
}

type toolUseBlock struct {
	Type  string          `json:"type"`
	ID    string          `json:"id"`
	Name  string          `json:"name"`
	Input json.RawMessage `json:"input"`
}

type imageBlock struct {
	Type   string      `json:"type"`
	Source imageSource `json:"source"`
}

type imageSource struct {
	Type      string `json:"type"`
	MediaType string `json:"media_type"`
	Data      string `json:"data"`
}

type toolResultBlock struct {
	Type      string `json:"type"`
	ToolUseID string `json:"tool_use_id"`
	Content   any    `json:"content"`
	IsError   bool   `json:"is_error,omitempty"`
}

type userMessage struct {
	Role    string `json:"role"`
	Content []any  `json:"content"`
}

type userLine struct {
	Type            string         `json:"type"`
	UUID            string         `json:"uuid"`
	SessionID       string         `json:"session_id"`
	ParentToolUseID *string        `json:"parent_tool_use_id"`
	Message         userMessage    `json:"message"`
	ToolUseResult   map[string]any `json:"tool_use_result,omitempty"`
	AgentID         string         `json:"agent_id,omitempty"`
}

type infoLine struct {
	Type      string `json:"type"`
	Subtype   string `json:"subtype"`
	UUID      string `json:"uuid"`
	SessionID string `json:"session_id"`
	Content   string `json:"content"`
	Level     string `json:"level"`
}

type retryLine struct {
	Type         string `json:"type"`
	Subtype      string `json:"subtype"`
	UUID         string `json:"uuid"`
	SessionID    string `json:"session_id"`
	Attempt      int    `json:"attempt"`
	MaxRetries   int    `json:"max_retries"`
	RetryDelayMS int64  `json:"retry_delay_ms"`
	ErrorStatus  *int   `json:"error_status"`
	Error        string `json:"error"`
}

type deniedLine struct {
	Type      string `json:"type"`
	Subtype   string `json:"subtype"`
	UUID      string `json:"uuid"`
	SessionID string `json:"session_id"`
	ToolName  string `json:"tool_name"`
	ToolUseID string `json:"tool_use_id"`
	Message   string `json:"message"`
}

type statusLine struct {
	Type      string  `json:"type"`
	Subtype   string  `json:"subtype"`
	UUID      string  `json:"uuid"`
	SessionID string  `json:"session_id"`
	Status    *string `json:"status"`
}

type compactMeta struct {
	Trigger   string `json:"trigger"`
	PreTokens int    `json:"pre_tokens"`
}

type compactLine struct {
	Type      string      `json:"type"`
	Subtype   string      `json:"subtype"`
	UUID      string      `json:"uuid"`
	SessionID string      `json:"session_id"`
	Metadata  compactMeta `json:"compact_metadata"`
}

type modelUsage struct {
	InputTokens              int     `json:"inputTokens"`
	OutputTokens             int     `json:"outputTokens"`
	CacheReadInputTokens     int     `json:"cacheReadInputTokens"`
	CacheCreationInputTokens int     `json:"cacheCreationInputTokens"`
	WebSearchRequests        int     `json:"webSearchRequests"`
	CostUSD                  float64 `json:"costUSD"`
	ContextWindow            int     `json:"contextWindow"`
	MaxOutputTokens          int     `json:"maxOutputTokens"`
}

type denial struct {
	ToolName  string          `json:"tool_name"`
	ToolUseID string          `json:"tool_use_id"`
	ToolInput json.RawMessage `json:"tool_input"`
}

// resultObject is the result of a run: the one object json prints, and the
// last line of stream-json. A run that succeeded carries result; one that
// did not carries errors instead.
type resultObject struct {
	Type              string                `json:"type"`
	Subtype           string                `json:"subtype"`
	UUID              string                `json:"uuid"`
	SessionID         string                `json:"session_id"`
	IsError           bool                  `json:"is_error"`
	Result            *string               `json:"result,omitempty"`
	Errors            []string              `json:"errors,omitempty"`
	StopReason        *string               `json:"stop_reason"`
	NumTurns          int                   `json:"num_turns"`
	DurationMS        int64                 `json:"duration_ms"`
	DurationAPIMS     int64                 `json:"duration_api_ms"`
	TotalCostUSD      float64               `json:"total_cost_usd"`
	Usage             apiUsage              `json:"usage"`
	ModelUsage        map[string]modelUsage `json:"modelUsage"`
	PermissionDenials []denial              `json:"permission_denials"`
	TerminalReason    string                `json:"terminal_reason,omitempty"`
	APIErrorStatus    *int                  `json:"api_error_status,omitempty"`
	LocalCommand      string                `json:"local_command,omitempty"`
	UserMessageUUID   string                `json:"user_message_uuid,omitempty"`
	StructuredOutput  json.RawMessage       `json:"structured_output,omitempty"`
}

func ptr[T any](v T) *T { return &v }

func statusOrNil(status int) *int {
	if status == 0 {
		return nil
	}
	return &status
}

// newUUID makes the id of one line of output.
func newUUID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "00000000-0000-4000-8000-000000000000"
	}
	b[6] = b[6]&0x0f | 0x40
	b[8] = b[8]&0x3f | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}

// line writes one line of stream-json output. It does nothing in the
// other formats.
func (f *printer) line(v any) {
	if f.format != cli.FormatStreamJSON {
		return
	}
	f.encode(v)
}

// encode writes v as one line of JSON, with the characters of code left
// as they are.
func (f *printer) encode(v any) {
	var b strings.Builder
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		f.stderr("a line of output could not be written as JSON: %v", err)
		return
	}
	fmt.Fprint(f.p.stdout, b.String())
}

// init writes the first line of a stream: what the run uses. It is
// written when the run's first turn starts, not before the session is
// loaded.
func (f *printer) init() {
	if f.started || f.format != cli.FormatStreamJSON || f.c == nil {
		return
	}
	f.started = true
	info := f.c.Info()
	skills := []string{}
	for _, sk := range info.Skills {
		skills = append(skills, sk.Name)
	}
	f.encode(initLine{
		Type: "system", Subtype: "init", UUID: newUUID(), SessionID: info.ID,
		Cwd: info.Cwd, Model: info.Model,
		Tools:          append([]string{}, info.Tools...),
		MCPServers:     append([]MCPServerStatus{}, info.MCPServers...),
		PermissionMode: info.Mode,
		SlashCommands:  append([]string{}, info.Commands...),
		OutputStyle:    firstOf(info.OutputStyle, defaultStyle),
		Skills:         skills,
		Plugins:        []string{},
		APIKeySource:   "none",
		GnoshVersion:   info.Version,
	})
}

// parent is the id of the tool call that started the sub-agent an event
// belongs to, or nil for the main conversation.
func parent(m *agent.Meta) *string {
	if m.Agent == "" {
		return nil
	}
	return &m.Parent
}

// stopWord is a stop reason in the words scripts know.
func stopWord(r core.StopReason) *string {
	switch r {
	case core.StopEndTurn, core.StopToolUse, core.StopMaxTokens, core.StopSequence, core.StopRefusal:
		return ptr(string(r))
	}
	return nil
}

// replyLines writes one line for each block of a reply. The lines of one
// reply share its message id.
func (f *printer) replyLines(ev *agent.ReplyEvent) {
	if f.format != cli.FormatStreamJSON {
		return
	}
	f.init()
	var id string
	if f.partialOn() && f.sm.open && ev.Agent == "" {
		id = f.sm.id // the message the stream events told
	} else {
		f.msgSeq++
		id = f.messageID(f.msgSeq)
	}
	aborted := ev.StopReason == core.StopCancelled || ev.StopReason == core.StopError
	for _, b := range ev.Message.Blocks {
		var block any
		switch b.Type {
		case core.BlockText:
			if ev.Agent != "" {
				continue // a sub-agent's words are not part of the stream
			}
			text := b.Text
			if ev.Marker != "" {
				text = strings.TrimSpace(strings.Replace(text, ev.Marker, "", 1))
			}
			block = textBlock{Type: "text", Text: text}
		case core.BlockThinking:
			if ev.Agent != "" || b.Text == "" {
				continue
			}
			block = thinkingBlock{Type: "thinking", Thinking: b.Text, Signature: b.Signature}
		case core.BlockToolUse:
			input := b.Input
			if len(input) == 0 {
				input = json.RawMessage("{}")
			}
			block = toolUseBlock{Type: "tool_use", ID: b.ID, Name: b.Name, Input: input}
		default:
			continue
		}
		f.encode(assistantLine{
			Type: "assistant", UUID: newUUID(), SessionID: f.sessionID(), ParentToolUseID: parent(&ev.Meta),
			Message: apiMessage{
				ID: id, Type: "message", Role: "assistant", Model: ev.Model,
				Content: []any{block}, StopReason: stopWord(ev.StopReason), Usage: usageOf(ev.Usage),
			},
			Aborted: aborted, AgentID: ev.Agent,
		})
	}
}

// toolLine writes the line of a tool result: what the model reads, and
// beside it the facts of the call.
func (f *printer) toolLine(ev *agent.ToolEndEvent) {
	if f.format != cli.FormatStreamJSON {
		return
	}
	f.init()
	var content any
	plain := true
	for _, b := range ev.Result.Content {
		if b.Type != core.BlockText {
			plain = false
		}
	}
	if plain {
		content = core.TextOf(ev.Result.Content)
	} else {
		var blocks []any
		for _, b := range ev.Result.Content {
			switch b.Type {
			case core.BlockText:
				blocks = append(blocks, textBlock{Type: "text", Text: b.Text})
			case core.BlockImage:
				blocks = append(blocks, imageBlock{Type: "image", Source: imageSource{Type: "base64", MediaType: b.MediaType, Data: base64.StdEncoding.EncodeToString(b.Data)}})
			}
		}
		content = blocks
	}
	f.encode(userLine{
		Type: "user", UUID: newUUID(), SessionID: f.sessionID(), ParentToolUseID: parent(&ev.Meta),
		Message: userMessage{Role: "user", Content: []any{
			toolResultBlock{Type: "tool_result", ToolUseID: ev.CallID, Content: content, IsError: ev.Result.IsError},
		}},
		ToolUseResult: ev.Result.Meta,
		AgentID:       ev.Agent,
	})
}

// result builds the result object of a run from how its last turn ended.
func (f *printer) result(c Controls, res agent.TurnResult) resultObject {
	info := c.Info()
	obj := resultObject{
		Type: "result", UUID: newUUID(), SessionID: info.ID,
		StopReason:        stopWord(res.StopReason),
		NumTurns:          res.Rounds,
		DurationMS:        f.p.now().Sub(f.since()).Milliseconds(),
		DurationAPIMS:     res.APIDuration.Milliseconds(),
		Usage:             usageOf(res.Usage),
		ModelUsage:        map[string]modelUsage{},
		PermissionDenials: []denial{},
	}
	for id, mt := range c.Usage().ByModel {
		obj.ModelUsage[id] = modelUsage{
			InputTokens: mt.Usage.InputTokens, OutputTokens: mt.Usage.OutputTokens,
			CacheReadInputTokens: mt.Usage.CacheReadTokens, CacheCreationInputTokens: mt.Usage.CacheWriteTokens,
			ContextWindow: mt.ContextWindow, MaxOutputTokens: mt.MaxOutput,
		}
	}
	for _, d := range res.Denials {
		input := d.Input
		if len(input) == 0 {
			input = json.RawMessage("{}")
		}
		obj.PermissionDenials = append(obj.PermissionDenials, denial{ToolName: d.ToolName, ToolUseID: d.ToolUseID, ToolInput: input})
	}
	fail := func(subtype, reason string, errs ...string) {
		obj.Subtype, obj.IsError, obj.TerminalReason, obj.Errors = subtype, true, reason, errs
	}
	switch {
	case f.signal != nil:
		if res.Turn == "" {
			// The turn had not said how it ended when this was written:
			// the counts are the ones seen so far.
			obj.NumTurns = f.rounds
			totals := c.Usage()
			obj.Usage, obj.DurationAPIMS = usageOf(totals.Main), totals.APITime.Milliseconds()
		}
		reason := "aborted_streaming"
		if f.inTools {
			reason = "aborted_tools"
		}
		fail("error_during_execution", reason, fmt.Sprintf("the run was stopped by %s after %d turns of the model", signalWord(f.signal), obj.NumTurns))
	case f.capped && (f.inv.InputFormat != cli.FormatStreamJSON || res.Turn == f.cappedTurn):
		// (With stream-json input the cap is a turn's own: a later
		// message's turn has a fresh count.)
		fail("error_max_turns", "max_turns", fmt.Sprintf("the run stopped after its %d turns of the model (--max-turns)", f.inv.MaxTurns))
	case (res.Reason == agent.EndCompleted || res.Reason == agent.EndModelStopped) && f.structuredErr != "":
		fail("error_max_structured_output_retries", "structured_output_retry_exhausted", f.structuredErr)
	case res.Reason == agent.EndCompleted, res.Reason == agent.EndModelStopped:
		obj.Subtype, obj.Result, obj.TerminalReason = "success", ptr(dropEchoedLine(res.Text)), "completed"
		obj.StructuredOutput = f.structured
	case res.Reason == agent.EndRefused:
		// A prompt a hook blocked is a result all the same: the hook's
		// words, and no turn.
		obj.Subtype, obj.Result = "success", ptr(res.Detail)
	case res.Reason == agent.EndFailed:
		reason := "api_error"
		switch res.ErrorKind {
		case "context_too_long":
			reason = "prompt_too_long"
		case "internal":
			reason = "model_error"
		}
		fail("error_during_execution", reason, firstOf(res.Detail, "the turn failed"))
		obj.APIErrorStatus = statusOrNil(res.Status)
	case res.Reason == agent.EndHalted:
		fail("error_during_execution", "hook_stopped", "the turn was ended: "+firstOf(res.Detail, "no reason was given"))
	default:
		fail("error_during_execution", "aborted_tools", firstOf(res.Detail, "the turn did not finish"))
	}
	return obj
}

// report prints the result of the run in the format that was asked for,
// and returns the exit code: 0 when the turn finished, 1 when it failed,
// was ended, never started, or ran out of turns.
func (f *printer) report(c Controls, res agent.TurnResult) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	obj := f.result(c, res)
	code := cli.ExitOK
	if obj.IsError || res.Reason == agent.EndRefused {
		code = cli.ExitFailure
	}
	if res.Reason == agent.EndRefused {
		f.stderr("the prompt was not sent: %s", firstOf(res.Detail, "a hook refused it"))
	}
	switch {
	case f.format == cli.FormatJSON:
		f.encode(obj)
	case f.format == cli.FormatStreamJSON:
		f.init()
		if !f.resultsOut {
			f.encode(obj)
		}
	case f.inv.JSONSchema != "":
		// The text format prints the structured output alone.
		if f.structured != nil {
			fmt.Fprintln(f.p.stdout, string(f.structured))
		} else if f.structuredErr != "" {
			f.stderr("%s", f.structuredErr)
		}
	default:
		f.endReply()
		if f.wroteAny && !f.endsLine {
			fmt.Fprintln(f.p.stdout)
		}
	}
	// What the session's closing says after this goes to standard error
	// alone: a stream-json line after the result was taken for part of a
	// next one.
	f.finished = true
	return code
}

// reportCommand prints what a slash command given as the run's prompt
// said: it is the run's result.
func (f *printer) reportCommand(c Controls, res CommandResult, err error) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err != nil {
		f.stderr("%v", err)
	}
	said := strings.TrimRight(res.Text, "\n")
	if f.format == cli.FormatText {
		if f.live {
			// What a command says may be a model's words (/btw, /recap)
			// or notes a model wrote (/mem): on a terminal they may only
			// colour themselves.
			said = strings.TrimRight(text.Safe(said), "\n")
		}
		if said != "" {
			fmt.Fprintln(f.p.stdout, said)
		}
	} else {
		info := c.Info()
		obj := resultObject{
			Type: "result", Subtype: "success", UUID: newUUID(), SessionID: info.ID,
			Result:     ptr(said),
			DurationMS: f.p.now().Sub(f.p.start).Milliseconds(),
			ModelUsage: map[string]modelUsage{}, PermissionDenials: []denial{},
			LocalCommand: res.Name,
		}
		if err != nil {
			obj.Subtype, obj.IsError, obj.Result, obj.Errors = "error_during_execution", true, nil, []string{err.Error()}
		}
		if f.format == cli.FormatStreamJSON {
			f.init()
		}
		f.encode(obj)
	}
	f.finished = true
	if err != nil {
		return cli.ExitFailure
	}
	return cli.ExitOK
}
