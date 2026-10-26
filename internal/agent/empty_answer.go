package agent

import (
	"strings"

	"github.com/charmbracelet/crush/internal/message"
)

// emptyAnswerMaxTries is how many times in a row a turn is continued
// because the model answered with nothing. Each try is one more request,
// so this is not a budget to spend freely; it is there because a model
// that has returned nothing three times running is wedged rather than
// unlucky, and spinning on it would burn a local box all night. The
// attempts are logged, and the last one says what to change.
const emptyAnswerMaxTries = 3

// answeredNothing reports whether the assistant message carries nothing
// anyone can use: no tool call, and no text beyond whitespace.
//
// A model that streams a reasoning channel and then stops without a final
// one ends its turn exactly this way. On the AI box's abliterated
// gpt-oss-120b that is about one response in seven, and it used to end
// the turn in silence: the user typed "continue", the model thought, and
// nothing came back, which reads as the agent having hung. Only the
// model's own silence counts here — a turn the user cancelled, one that
// hit an error, and one a content filter stopped are all genuinely over,
// and asking again would be wrong.
func answeredNothing(msg *message.Message) bool {
	if msg == nil || len(msg.ToolCalls()) > 0 {
		return false
	}
	switch msg.FinishReason() {
	case message.FinishReasonEndTurn, message.FinishReasonUnknown:
	default:
		return false
	}
	return strings.TrimSpace(msg.Content().String()) == ""
}

// emptyAnswerContinuation is the turn to run when the model answered with
// nothing, and whether there is one: this same call with its prompt
// replaced, as a Stop hook's continuation is, so the turn keeps its RunID
// and is not over. It reports false once the model has been asked
// emptyAnswerMaxTries times without answering.
func emptyAnswerContinuation(call SessionAgentCall, msg *message.Message) (SessionAgentCall, bool) {
	if !answeredNothing(msg) || call.emptyAnswerDepth >= emptyAnswerMaxTries {
		return call, false
	}
	next := call
	next.emptyAnswerDepth = call.emptyAnswerDepth + 1
	next.Prompt = emptyAnswerPrompt
	// Generated, not typed: keep it out of the transcript the user reads.
	next.HiddenUserMessage = true
	// Attachments belong to the user's own prompt and went out with it.
	next.Attachments = nil
	// A pending Stop hook checkpoint question is no longer the latest
	// prompt, so a stop marker in the answer to this one must not count.
	next.stopHookReflecting = false
	return next, true
}

// emptyAnswerPrompt asks for the answer the model kept to itself. It says
// what happened rather than repeating the task: the task is still right
// there in the conversation, and restating it invites the model to start
// over instead of finishing.
const emptyAnswerPrompt = "Your last reply was empty: it reached me with no text and no tool call. " +
	"Whatever you worked out, I never saw it. Send it now, in the reply itself: " +
	"either the answer, or the next tool call. Do not put it in your reasoning alone."
