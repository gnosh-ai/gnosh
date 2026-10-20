package agent

import (
	"strings"

	"charm.land/fantasy"
)

// zeroWidth is a zero-width space (U+200B). Placed inside a model control
// sequence it stops the server's tokenizer from recognizing the sequence as a
// special token, while staying invisible to a reader and transparent to the
// model, which reads straight through it.
const zeroWidth = "​"

// neutralizeControlTokens defuses model/runtime control sequences that may
// appear verbatim in untrusted text (web pages a tool fetched, command output,
// server responses such as /props, pasted content). Two shapes matter:
//
//   - ChatML / Qwen special tokens, all of the form <|...|> (<|im_start|>,
//     <|im_end|>, <|vision_start|>, <|image_pad|>, <|vision_end|>,
//     <|endoftext|>, ...).
//   - llama.cpp's media marker. Current builds randomize it as
//     <__media_<random>__> (the /props endpoint returns the exact value), so a
//     tool result that echoes it makes a vision-enabled server count an image
//     with no bitmap and reject the whole turn ("Failed to tokenize prompt" /
//     "number of media markers in text (1) exceeds number of bitmaps (0)").
//     Older builds use the fixed <__media__>. Breaking the "<__media" prefix
//     covers both, whatever the random suffix.
//
// Inserting a zero-width space right after the opening '<' breaks the literal
// match without changing how the text reads. The transform is idempotent: once
// a space sits after '<', the "<|" and "<__media" substrings are gone, so a
// second pass does nothing.
func neutralizeControlTokens(s string) string {
	if !strings.Contains(s, "<|") && !strings.Contains(s, "<__media") {
		return s
	}
	s = strings.ReplaceAll(s, "<|", "<"+zeroWidth+"|")
	s = strings.ReplaceAll(s, "<__media", "<"+zeroWidth+"__media")
	return s
}

// sanitizeControlTokens runs neutralizeControlTokens over every text-bearing
// part of every non-system message: user and assistant text, model reasoning,
// and tool results (both plain text and the text that rides alongside media).
// System messages are Crush's own prompt and are left untouched. Slices are
// copied before mutation so stored messages are never changed in place.
func sanitizeControlTokens(messages []fantasy.Message) []fantasy.Message {
	for i, msg := range messages {
		if msg.Role == fantasy.MessageRoleSystem {
			continue
		}
		var content []fantasy.MessagePart
		touch := func() {
			if content == nil {
				content = append([]fantasy.MessagePart(nil), msg.Content...)
			}
		}
		for j, part := range msg.Content {
			switch {
			case isTextPart(part):
				tp, _ := fantasy.AsMessagePart[fantasy.TextPart](part)
				clean := neutralizeControlTokens(tp.Text)
				if clean != tp.Text {
					touch()
					tp.Text = clean
					content[j] = tp
				}
			case isReasoningPart(part):
				rp, _ := fantasy.AsMessagePart[fantasy.ReasoningPart](part)
				clean := neutralizeControlTokens(rp.Text)
				if clean != rp.Text {
					touch()
					rp.Text = clean
					content[j] = rp
				}
			case isToolResultPart(part):
				rp, _ := fantasy.AsMessagePart[fantasy.ToolResultPart](part)
				if out, ok := fantasy.AsToolResultOutputType[fantasy.ToolResultOutputContentText](rp.Output); ok {
					clean := neutralizeControlTokens(out.Text)
					if clean != out.Text {
						touch()
						rp.Output = fantasy.ToolResultOutputContentText{Text: clean}
						content[j] = rp
					}
				} else if media, ok := fantasy.AsToolResultOutputType[fantasy.ToolResultOutputContentMedia](rp.Output); ok {
					clean := neutralizeControlTokens(media.Text)
					if clean != media.Text {
						touch()
						media.Text = clean
						rp.Output = media
						content[j] = rp
					}
				}
			}
		}
		if content != nil {
			messages[i].Content = content
		}
	}
	return messages
}

func isTextPart(p fantasy.MessagePart) bool {
	_, ok := fantasy.AsMessagePart[fantasy.TextPart](p)
	return ok
}

func isReasoningPart(p fantasy.MessagePart) bool {
	_, ok := fantasy.AsMessagePart[fantasy.ReasoningPart](p)
	return ok
}

func isToolResultPart(p fantasy.MessagePart) bool {
	_, ok := fantasy.AsMessagePart[fantasy.ToolResultPart](p)
	return ok
}
