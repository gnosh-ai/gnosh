package chat

import (
	"encoding/json"
	"strconv"
	"strings"

	"github.com/charmbracelet/crush/internal/gnovm"
	gnovmtools "github.com/charmbracelet/crush/internal/gnovm/tools"
	"github.com/charmbracelet/crush/internal/message"
	"github.com/charmbracelet/crush/internal/ui/common"
	"github.com/charmbracelet/crush/internal/ui/styles"
)

// -----------------------------------------------------------------------------
// GnoVM Run Tool
// -----------------------------------------------------------------------------

// NewGnoVMRunToolMessageItem creates the item for a gnovm_run call. It is
// headed by the Gno the call runs, highlighted as Go in muted colours, not
// by the call's JSON: Gnovm Run mem.Show().
func NewGnoVMRunToolMessageItem(
	sty *styles.Styles,
	toolCall message.ToolCall,
	result *message.ToolResult,
	canceled bool,
) ToolMessageItem {
	return newBaseToolMessageItem(sty, toolCall, result, &GnoVMRunToolRenderContext{}, canceled)
}

// GnoVMRunToolRenderContext renders gnovm_run calls.
type GnoVMRunToolRenderContext struct{}

// RenderTool implements the [ToolRenderer] interface.
func (g *GnoVMRunToolRenderContext) RenderTool(sty *styles.Styles, width int, opts *ToolRenderOpts) string {
	cappedWidth := cappedMessageWidth(width)
	name := humanizedToolName(opts.ToolCall.Name)
	if opts.IsPending() {
		// While the call streams, the code so far heads it, as it is
		// written, with the spinner after it; until the code begins, the
		// spinner alone.
		fields := partialJSONFields(opts.ToolCall.Input)
		code, _ := fields["code"].(string)
		if strings.TrimSpace(code) == "" {
			return pendingTool(sty, name, opts.Anim, opts.Compact)
		}
		vm, _ := fields["name"].(string)
		header := gnoVMRunHeader(sty, cappedWidth, opts, name, gnovmtools.RunParams{Code: code, Name: vm})
		if opts.Anim != nil {
			header += " " + opts.Anim.Render()
		}
		return header
	}

	var params gnovmtools.RunParams
	if err := json.Unmarshal([]byte(opts.ToolCall.Input), &params); err != nil || strings.TrimSpace(params.Code) == "" {
		return (&GenericToolRenderContext{}).RenderTool(sty, width, opts)
	}
	header := gnoVMRunHeader(sty, cappedWidth, opts, name, params)
	return toolWithResult(sty, cappedWidth, opts, header)
}

// gnoVMRunHeader is the header of a gnovm_run call that runs params: the
// code, highlighted, after the tool's name.
func gnoVMRunHeader(sty *styles.Styles, cappedWidth int, opts *ToolRenderOpts, name string, params gnovmtools.RunParams) string {
	code := strings.ReplaceAll(strings.TrimSpace(params.Code), "\t", "    ")
	if highlighted, err := common.SyntaxHighlightMuted(sty, code, "go", sty.Tool.ParamMain.GetForeground()); err == nil {
		code = highlighted
	}
	// The VM leads the line, unless it is mem, which its calls name, or the
	// session's scratch VM, which no name means.
	if params.Name != "" && params.Name != gnovm.MemVM {
		code = sty.Tool.JobToolName.Render(params.Name) + " " + code
	}
	toolParams := []string{code}
	if params.Gas > 0 {
		gas := strconv.FormatFloat(params.Gas, 'g', -1, 64)
		toolParams = append(toolParams, "gas", strings.NewReplacer("e+0", "e", "e+", "e").Replace(gas))
	}
	if params.Reset {
		toolParams = append(toolParams, "reset", "true")
	}

	// The code is always shown whole, as a bash command is; expansion only
	// controls the output under it.
	headerOpts := *opts
	headerOpts.ExpandedContent = true
	return toolHeader(sty, opts.Status, name, cappedWidth, &headerOpts, toolParams...)
}
