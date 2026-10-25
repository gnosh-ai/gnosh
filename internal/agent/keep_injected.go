package agent

import "charm.land/fantasy"

// injectedMessages are messages a step of a turn put in after its input,
// after the first at messages of it.
type injectedMessages struct {
	at       int
	messages []fantasy.Message
}

// keepInjected is a step's input with what the turn's earlier steps put
// in after theirs put back where it was. fantasy builds each step from
// the turn's messages and the steps' responses alone, so a prompt folded
// into the turn, a background job's notice or a todo reminder was in one
// step and gone from the next, though it is kept in the conversation: the
// model lost a prompt typed while it worked, and each later step read the
// turn again from where it had been.
func keepInjected(input []fantasy.Message, injected []injectedMessages) []fantasy.Message {
	if len(injected) == 0 {
		return input
	}
	kept := make([]fantasy.Message, 0, len(input)+len(injected))
	next := 0
	for i := 0; i <= len(input); i++ {
		for ; next < len(injected) && injected[next].at == i; next++ {
			kept = append(kept, injected[next].messages...)
		}
		if i < len(input) {
			kept = append(kept, input[i])
		}
	}
	return kept
}
