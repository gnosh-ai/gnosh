package chat

import (
	"encoding/json"

	"github.com/charmbracelet/crush/internal/agent/tools"
	"github.com/charmbracelet/crush/internal/message"
	"github.com/charmbracelet/crush/internal/ui/styles"
)

// -----------------------------------------------------------------------------
// x402 Fetch Tool
// -----------------------------------------------------------------------------

// X402FetchToolMessageItem is a message item that represents an x402_fetch
// tool call: a fetch the user may pay for.
type X402FetchToolMessageItem struct {
	*baseToolMessageItem
}

var _ ToolMessageItem = (*X402FetchToolMessageItem)(nil)

// NewX402FetchToolMessageItem creates a new [X402FetchToolMessageItem].
func NewX402FetchToolMessageItem(
	sty *styles.Styles,
	toolCall message.ToolCall,
	result *message.ToolResult,
	canceled bool,
) ToolMessageItem {
	return newBaseToolMessageItem(sty, toolCall, result, &X402FetchToolRenderContext{}, canceled)
}

// X402FetchToolRenderContext renders x402_fetch tool messages: the URL and the
// key that pays, then the receipt and the content, or why nothing was paid.
type X402FetchToolRenderContext struct{}

// RenderTool implements the [ToolRenderer] interface.
func (x *X402FetchToolRenderContext) RenderTool(sty *styles.Styles, width int, opts *ToolRenderOpts) string {
	cappedWidth := cappedMessageWidth(width)
	if opts.IsPending() {
		return pendingTool(sty, "Pay", opts.Anim, opts.Compact)
	}

	var params tools.X402FetchParams
	if err := json.Unmarshal([]byte(opts.ToolCall.Input), &params); err != nil {
		return toolErrorContent(sty, &message.ToolResult{Content: "Invalid parameters"}, cappedWidth)
	}

	toolParams := []string{params.URL, "key", params.Key}
	if params.Master != "" {
		toolParams = append(toolParams, "for", params.Master)
	}

	header := toolHeader(sty, opts.Status, "Pay", cappedWidth, opts, toolParams...)
	if opts.Compact {
		return header
	}

	if earlyState, ok := toolEarlyStateContent(sty, opts, cappedWidth); ok {
		return joinToolParts(header, earlyState)
	}

	if opts.HasEmptyResult() {
		return header
	}

	body := toolOutputCodeContent(sty, getFileExtensionForFormat(params.Format), opts.Result.Content, 0, cappedWidth, opts.ExpandedContent)
	return joinToolParts(header, body)
}
