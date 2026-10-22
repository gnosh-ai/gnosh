package message

import (
	"slices"
	"strings"
)

type Attachment struct {
	FilePath string
	FileName string
	MimeType string
	Content  []byte
}

func (a Attachment) IsText() bool     { return strings.HasPrefix(a.MimeType, "text/") }
func (a Attachment) IsImage() bool    { return strings.HasPrefix(a.MimeType, "image/") }
func (a Attachment) IsMarkdown() bool { return a.MimeType == "text/markdown" }

// ContainsTextAttachment returns true if any of the attachments is a text attachment.
func ContainsTextAttachment(attachments []Attachment) bool {
	return slices.ContainsFunc(attachments, func(a Attachment) bool {
		return a.IsText()
	})
}

// PoppedPrompt is a queued prompt taken out of a session's queue to be
// edited: its text and the attachments that came with it, so editing it
// loses nothing it carried.
type PoppedPrompt struct {
	Prompt      string
	Attachments []Attachment
}
