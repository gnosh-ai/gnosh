package agent

import (
	"testing"

	"charm.land/fantasy"
	"github.com/stretchr/testify/require"
)

func TestNeutralizeControlTokens(t *testing.T) {
	t.Parallel()
	z := "​"
	cases := []struct{ in, want string }{
		{"plain text", "plain text"},
		{"the token is <|image_pad|> here", "the token is <" + z + "|image_pad|> here"},
		{"<|vision_start|><|image_pad|><|vision_end|>", "<" + z + "|vision_start|><" + z + "|image_pad|><" + z + "|vision_end|>"},
		{"<|im_start|>system<|im_end|>", "<" + z + "|im_start|>system<" + z + "|im_end|>"},
		{"fixed marker <__media__> x", "fixed marker <" + z + "__media__> x"},
		{"random marker <__media_4KQ0bnaSxbqiLUlxIDD0rcAfBecKwdGJ__> x", "random marker <" + z + "__media_4KQ0bnaSxbqiLUlxIDD0rcAfBecKwdGJ__> x"},
		{"code a < b | c", "code a < b | c"}, // no "<|" bigram, untouched
	}
	for _, c := range cases {
		require.Equal(t, c.want, neutralizeControlTokens(c.in), "input %q", c.in)
	}
}

func TestNeutralizeControlTokensIdempotent(t *testing.T) {
	t.Parallel()
	for _, in := range []string{"x <|image_pad|> y", "m <__media_ABC123__> n"} {
		once := neutralizeControlTokens(in)
		require.Equal(t, once, neutralizeControlTokens(once), "second pass changed %q", in)
	}
}

func TestSanitizeControlTokensToolResultText(t *testing.T) {
	t.Parallel()
	z := "​"
	tool := fantasy.Message{Role: fantasy.MessageRoleTool, Content: []fantasy.MessagePart{
		fantasy.ToolResultPart{ToolCallID: "c1", Output: fantasy.ToolResultOutputContentText{
			Text: `{"media_marker":"<__media_4KQ0bnaSxbqiLUlxIDD0rcAfBecKwdGJ__>"}`,
		}},
	}}
	user := fantasy.NewUserMessage("hi <|im_start|> there")
	got := sanitizeControlTokens([]fantasy.Message{user, tool})

	rp, ok := fantasy.AsMessagePart[fantasy.ToolResultPart](got[1].Content[0])
	require.True(t, ok)
	out, ok := fantasy.AsToolResultOutputType[fantasy.ToolResultOutputContentText](rp.Output)
	require.True(t, ok)
	require.Equal(t, `{"media_marker":"<`+z+`__media_4KQ0bnaSxbqiLUlxIDD0rcAfBecKwdGJ__>"}`, out.Text)

	tp, ok := fantasy.AsMessagePart[fantasy.TextPart](got[0].Content[0])
	require.True(t, ok)
	require.Equal(t, "hi <"+z+"|im_start|> there", tp.Text)
}

func TestSanitizeControlTokensLeavesCleanMessagesAlone(t *testing.T) {
	t.Parallel()
	msgs := []fantasy.Message{
		fantasy.NewSystemMessage("You are Crush. Do not touch <|this|>."),
		fantasy.NewUserMessage("just a normal question"),
	}
	got := sanitizeControlTokens(msgs)
	sp, ok := fantasy.AsMessagePart[fantasy.TextPart](got[0].Content[0])
	require.True(t, ok)
	require.Contains(t, sp.Text, "<|this|>", "system message must not be sanitized")
}
