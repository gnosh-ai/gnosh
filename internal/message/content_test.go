package message

import (
	"encoding/base64"
	"fmt"
	"strings"
	"testing"

	"charm.land/fantasy"
	"github.com/stretchr/testify/require"
)

func makeTestAttachments(n int, contentSize int) []Attachment {
	attachments := make([]Attachment, n)
	content := []byte(strings.Repeat("x", contentSize))
	for i := range n {
		attachments[i] = Attachment{
			FilePath: fmt.Sprintf("/path/to/file%d.txt", i),
			MimeType: "text/plain",
			Content:  content,
		}
	}
	return attachments
}

func TestToAIMessage_CorruptedMediaData(t *testing.T) {
	t.Parallel()

	msg := &Message{
		Role: Tool,
		Parts: []ContentPart{
			ToolResult{
				ToolCallID: "call_123",
				Name:       "screenshot",
				Content:    "Loaded image/png content",
				Data:       "abc\x80def",
				MIMEType:   "image/png",
			},
		},
	}

	messages := msg.ToAIMessage()
	require.Len(t, messages, 1)
	require.Len(t, messages[0].Content, 1)

	part, ok := messages[0].Content[0].(fantasy.ToolResultPart)
	require.True(t, ok)

	require.Equal(t, "call_123", part.ToolCallID)

	textContent, ok := part.Output.(fantasy.ToolResultOutputContentText)
	require.True(t, ok, "corrupted media should be downgraded to text")
	require.Equal(t, mediaLoadFailedPlaceholder, textContent.Text)
}

func TestToAIMessage_ValidMediaData(t *testing.T) {
	t.Parallel()

	validBase64 := base64.StdEncoding.EncodeToString([]byte{0x89, 0x50, 0x4E, 0x47})

	msg := &Message{
		Role: Tool,
		Parts: []ContentPart{
			ToolResult{
				ToolCallID: "call_456",
				Name:       "screenshot",
				Content:    "Loaded image/png content",
				Data:       validBase64,
				MIMEType:   "image/png",
			},
		},
	}

	messages := msg.ToAIMessage()
	require.Len(t, messages, 1)
	require.Len(t, messages[0].Content, 1)

	part, ok := messages[0].Content[0].(fantasy.ToolResultPart)
	require.True(t, ok)

	require.Equal(t, "call_456", part.ToolCallID)

	mediaContent, ok := part.Output.(fantasy.ToolResultOutputContentMedia)
	require.True(t, ok, "valid media should remain as media")
	require.Equal(t, validBase64, mediaContent.Data)
	require.Equal(t, "image/png", mediaContent.MediaType)
}

func TestToAIMessage_ASCIIButInvalidBase64(t *testing.T) {
	t.Parallel()

	msg := &Message{
		Role: Tool,
		Parts: []ContentPart{
			ToolResult{
				ToolCallID: "call_789",
				Name:       "screenshot",
				Content:    "Loaded image/png content",
				Data:       "not-valid-base64!!!",
				MIMEType:   "image/png",
			},
		},
	}

	messages := msg.ToAIMessage()
	require.Len(t, messages, 1)
	require.Len(t, messages[0].Content, 1)

	part, ok := messages[0].Content[0].(fantasy.ToolResultPart)
	require.True(t, ok)

	require.Equal(t, "call_789", part.ToolCallID)

	textContent, ok := part.Output.(fantasy.ToolResultOutputContentText)
	require.True(t, ok, "ASCII but invalid base64 should be downgraded to text")
	require.Equal(t, mediaLoadFailedPlaceholder, textContent.Text)
}

func BenchmarkPromptWithTextAttachments(b *testing.B) {
	cases := []struct {
		name        string
		numFiles    int
		contentSize int
	}{
		{"1file_100bytes", 1, 100},
		{"5files_1KB", 5, 1024},
		{"10files_10KB", 10, 10 * 1024},
		{"20files_50KB", 20, 50 * 1024},
	}

	for _, tc := range cases {
		attachments := makeTestAttachments(tc.numFiles, tc.contentSize)
		prompt := "Process these files"

		b.Run(tc.name, func(b *testing.B) {
			b.ReportAllocs()
			for range b.N {
				_ = PromptWithTextAttachments(prompt, attachments)
			}
		})
	}
}

func TestResetStreamedContent(t *testing.T) {
	t.Parallel()

	msg := &Message{}
	msg.AddImageURL("https://example.com/img.png", "high")
	msg.AppendContent("partial answer")
	msg.AppendReasoningContent("thinking...")
	msg.AddToolCall(ToolCall{ID: "1", Name: "bash"})
	msg.AddToolResult(ToolResult{ToolCallID: "1", Content: "output"})
	msg.AddFinish(FinishReasonError, "boom", "stream died")

	msg.ResetStreamedContent()

	// Streamed parts are gone.
	require.Empty(t, msg.Content().Text, "text should be cleared")
	require.Empty(t, msg.ReasoningContent().Thinking, "reasoning should be cleared")
	require.Empty(t, msg.ToolCalls(), "tool calls should be cleared")
	require.Nil(t, msg.FinishPart(), "finish should be cleared")

	// Non-streamed parts survive.
	require.Len(t, msg.ImageURLContent(), 1, "image should survive")
	require.Len(t, msg.ToolResults(), 1, "tool results should survive")
}

func TestResetStreamedContentEmpty(t *testing.T) {
	t.Parallel()

	// Reset on an empty message is a no-op and must not panic.
	msg := &Message{}
	msg.ResetStreamedContent()
	require.Empty(t, msg.Parts)
}

func TestToolCallSummary(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		call  ToolCall
		want  string
		check func(t *testing.T, got string)
	}{
		{
			name: "bash command",
			call: ToolCall{ID: "c1", Name: "bash", Input: `{"command":"go test ./...","description":"run tests"}`},
			want: "go test ./...",
		},
		{
			name: "file path",
			call: ToolCall{ID: "c2", Name: "view", Input: `{"file_path":"/tmp/a/b.go","offset":10}`},
			want: "/tmp/a/b.go",
		},
		{
			name: "path",
			call: ToolCall{ID: "c3", Name: "ls", Input: `{"path":"/src"}`},
			want: "/src",
		},
		{
			name: "grep pattern",
			call: ToolCall{ID: "c4", Name: "grep", Input: `{"pattern":"foo"}`},
			want: "foo",
		},
		{
			name: "grep pattern over the directory it searched",
			call: ToolCall{ID: "c4b", Name: "grep", Input: `{"pattern":"runStream","path":"/src/internal/cmd"}`},
			want: "runStream",
		},
		{
			name: "sub-agent prompt, not its raw input",
			call: ToolCall{ID: "c4c", Name: "agent", Input: `{"prompt":"Find where tool results are stored"}`},
			want: "Find where tool results are stored",
		},
		{
			name: "url",
			call: ToolCall{ID: "c5", Name: "fetch", Input: `{"url":"https://example.com/a?b=c"}`},
			want: "https://example.com/a?b=c",
		},
		{
			name: "symbol",
			call: ToolCall{ID: "c6", Name: "lsp_definition", Input: `{"symbol":"RunNonInteractive"}`},
			want: "RunNonInteractive",
		},
		{
			name: "empty input",
			call: ToolCall{ID: "c7", Name: "gnosh_info"},
			want: "",
		},
		{
			name: "empty object",
			call: ToolCall{ID: "c8", Name: "ls", Input: `{}`},
			want: "",
		},
		{
			name: "no known field falls back to flattened input",
			call: ToolCall{ID: "c10", Name: "mcp_tool", Input: `{"a":1,\n"b":"x"}`},
			want: `{"a":1, "b":"x"}`,
		},
		{
			name: "a multi-line command on one line",
			call: ToolCall{ID: "c12", Name: "bash", Input: `{"command":"cat > f <<'EOF'\nhello\n\tworld\nEOF"}`},
			want: "cat > f <<'EOF' hello world EOF",
		},
		{
			name: "laid-out raw input on one line",
			call: ToolCall{ID: "c13", Name: "mcp_tool", Input: "{\n  \"a\": 1\n}"},
			want: `{ "a": 1 }`,
		},
		{
			name: "long input truncated",
			call: ToolCall{ID: "c11", Name: "bash", Input: `{"command":"` + strings.Repeat("x", 200) + `"}`},
			check: func(t *testing.T, got string) {
				require.True(t, len([]rune(got)) == 120, "expected 120 runes, got %d", len([]rune(got)))
				require.True(t, strings.HasSuffix(got, "..."), "expected ellipsis suffix, got %q", got)
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := tt.call.Summary()
			if tt.check != nil {
				tt.check(t, got)
				return
			}
			require.Equal(t, tt.want, got)
		})
	}
}

// Streaming text into a part keeps what the part is: hidden, a notice.
func TestAppendContentKeepsHiddenAndNotice(t *testing.T) {
	t.Parallel()

	msg := Message{Parts: []ContentPart{TextContent{Text: "Background", Hidden: true, Notice: true}}}
	msg.AppendContent(" command")
	require.Equal(t, TextContent{Text: "Background command", Hidden: true, Notice: true}, msg.Content())
}
