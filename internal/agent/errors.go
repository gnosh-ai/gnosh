package agent

import "errors"

var (
	ErrRequestCancelled = errors.New("request canceled by user")
	ErrSessionBusy      = errors.New("session is currently processing another request")
	ErrEmptyPrompt      = errors.New("prompt is empty")
	ErrSessionMissing   = errors.New("session id is missing")
	// ErrPromptBlocked is what Run returns, with the hook's reason after
	// it, for a prompt a UserPromptSubmit hook blocked.
	ErrPromptBlocked = errors.New("prompt blocked by a UserPromptSubmit hook")
)
