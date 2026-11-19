package llm

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"iter"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"sync"

	"github.com/gnosh-ai/gnosh/internal/core"
)

// anthropic speaks Anthropic's Messages API.
type anthropic struct {
	*provider
	// listed is what the Models API last said of each model it listed.
	listedMu sync.Mutex
	listed   map[string]listedModel
}

// listedModel is what the Models API says of a model that requests go by.
type listedModel struct {
	// maxOutput is the most it writes in one answer: what a request with
	// no cap of its own asks for at most.
	maxOutput int
	// off is how its thinking is turned off: "" for thinking
	// {type: "disabled"} (or not listed), "between_tools" for a model
	// that takes only that (Claude Sonnet 5.5), and "none" for one whose
	// thinking cannot be turned off (Claude Opus 5.5, the Fable models).
	off string
}

var (
	_ core.Provider     = (*anthropic)(nil)
	_ core.ModelLister  = (*anthropic)(nil)
	_ core.TokenCounter = (*anthropic)(nil)
)

// Stream sends req to the service's messages endpoint and yields the
// answer's events.

// minThinkingBudget is the least thinking budget the Messages API takes.
const minThinkingBudget = 1024

func (a *anthropic) Stream(ctx context.Context, req *core.Request) iter.Seq2[core.Event, error] {
	return a.stream(ctx, a, req)
}

// apiURL is the URL of one of the API's endpoints. The base URL may stop
// before "/v1" or after it.
func (a *anthropic) apiURL(suffix string, query url.Values) string {
	if strings.HasSuffix(a.base.Path, "/v1") {
		return a.endpoint(suffix, query)
	}
	return a.endpoint("/v1"+suffix, query)
}

func (a *anthropic) streamURL() string { return a.apiURL("/messages", nil) }

func (a *anthropic) encode(req *core.Request) ([]byte, error) {
	body, err := a.messagesBody(req, a.promptCache)
	if err != nil {
		return nil, err
	}
	body["stream"] = true

	// The API will not take a request without max_tokens.
	limit := req.MaxTokens
	if limit <= 0 {
		limit = a.defaultMaxTokens
		// A thinking budget has to fit under the cap; a default must
		// not be what makes the request invalid.
		if t := req.Thinking; t.Mode != "off" && t.BudgetTokens >= limit {
			limit += t.BudgetTokens
		}
		// Nor must a default above what the model writes in one answer
		// (an older Claude's 32K or 8K): every request so capped was
		// refused (the model client review of 2026-11-19).
		if most := a.listedModel(req.Model).maxOutput; most > 0 && limit > most {
			limit = most
		}
	}
	body["max_tokens"] = limit
	// A budget has to stand below the cap: a cap set under it takes the
	// budget down, and one with no room for the least budget leaves the
	// thinking to the model.
	if th, ok := body["thinking"].(obj); ok && th["type"] == "enabled" {
		if budget, _ := th["budget_tokens"].(int); budget >= limit {
			if budget = limit - 1; budget >= minThinkingBudget {
				th["budget_tokens"] = budget
			} else {
				body["thinking"] = obj{"type": "adaptive"}
			}
		}
	}

	if req.Temperature != nil {
		body["temperature"] = *req.Temperature
	}
	if req.TopP != nil {
		body["top_p"] = *req.TopP
	}
	if len(req.Stop) > 0 {
		body["stop_sequences"] = req.Stop
	}
	return a.encodeBody(body, req)
}

// messagesBody builds the part of a request body that says what the model
// is to read: the model, the system prompt, the tools, the conversation
// and how to think. It is the same for a message and for a token count.
func (a *anthropic) messagesBody(req *core.Request, cache bool) (obj, error) {
	body := obj{"model": req.Model}

	var system []obj
	for _, part := range req.System {
		if strings.TrimSpace(part) != "" {
			system = append(system, obj{"type": "text", "text": part})
		}
	}
	if len(system) > 0 {
		body["system"] = system
	}

	var tools []obj
	for _, t := range req.Tools {
		schema, err := a.schema(t)
		if err != nil {
			return nil, err
		}
		tool := obj{"name": t.Name, "input_schema": schema}
		if t.Description != "" {
			tool["description"] = t.Description
		}
		tools = append(tools, tool)
	}
	if len(tools) > 0 {
		body["tools"] = tools
	}

	messages := make([]obj, 0, len(req.Messages))
	for _, m := range req.Messages {
		blocks := a.blocks(m.Blocks)
		if len(blocks) == 0 {
			// The API refuses a message with nothing in it, and joins
			// two messages of one role that end up side by side.
			continue
		}
		messages = append(messages, obj{"role": string(m.Role), "content": blocks})
	}
	body["messages"] = messages

	switch t := req.Thinking; {
	case t.Mode == "off":
		// A model that refuses thinking {type: "disabled"} refused every
		// request that asked for no thinking (the model client review of
		// 2026-11-19): it is asked its own way, or for its least effort.
		switch a.listedModel(req.Model).off {
		case "between_tools":
			body["thinking"] = obj{"type": "between_tools"}
		case "none":
			body["output_config"] = obj{"effort": "low"}
		default:
			body["thinking"] = obj{"type": "disabled"}
		}
	case t.BudgetTokens > 0:
		// (The API takes a budget of at least 1,024 tokens.)
		body["thinking"] = obj{"type": "enabled", "budget_tokens": max(t.BudgetTokens, minThinkingBudget)}
	case t.Mode == "on":
		body["thinking"] = obj{"type": "adaptive"}
	}
	if req.Thinking.Effort != "" {
		body["output_config"] = obj{"effort": req.Thinking.Effort}
	}

	if cache {
		a.markCache(system, tools, messages)
	}
	return body, nil
}

// blocks turns a message's blocks into the API's, one for one. What the
// API would refuse is left out: text that is empty or all white space, and
// thinking that carries neither a signature nor an opaque payload, which
// is to say thinking that some other provider's model wrote.
func (a *anthropic) blocks(in []core.Block) []obj {
	out := make([]obj, 0, len(in))
	for _, b := range in {
		switch b.Type {
		case core.BlockText:
			if strings.TrimSpace(b.Text) != "" {
				out = append(out, obj{"type": "text", "text": b.Text})
			}
		case core.BlockThinking:
			if !a.keepThinking {
				continue
			}
			switch {
			case b.Signature != "":
				// Sent back as it came, an empty text included: the
				// signature is what the API reads.
				out = append(out, obj{"type": "thinking", "thinking": b.Text, "signature": b.Signature})
			case b.Opaque != "":
				out = append(out, obj{"type": "redacted_thinking", "data": b.Opaque})
			}
		case core.BlockToolUse:
			out = append(out, obj{"type": "tool_use", "id": b.ID, "name": b.Name, "input": objectJSON(b.Input)})
		case core.BlockToolResult:
			result := obj{"type": "tool_result", "tool_use_id": b.ToolUseID}
			if content := a.blocks(b.Content); len(content) > 0 {
				result["content"] = content
			}
			if b.IsError {
				result["is_error"] = true
			}
			out = append(out, result)
		case core.BlockImage:
			media := b.MediaType
			if media == "" {
				media = http.DetectContentType(b.Data)
			}
			out = append(out, obj{"type": "image", "source": obj{
				"type":       "base64",
				"media_type": media,
				"data":       base64.StdEncoding.EncodeToString(b.Data),
			}})
		}
	}
	return out
}

// maxCacheMarks is how many cache breakpoints the API takes in a request.
const maxCacheMarks = 4

// markCache puts the prompt-cache breakpoints on a request. The API
// caches a prompt as a prefix, in the order tools, system, messages, and
// writes a cache entry only where a block is marked. The marks go:
//
//   - on the last tool, so that the tools are cached whatever follows;
//   - on the last block of the last message, so that each request finds
//     the one before it in the cache and adds its own turn;
//   - on the end of each system part, with what is left of the four the
//     API allows. The parts come ordered from the one that changes least,
//     so when there are more parts than marks left the first ones get
//     them: they are the boundaries most likely to still be good when
//     something after them has changed, and the mark on the last message
//     already covers a request in which nothing has.
func (a *anthropic) markCache(system, tools, messages []obj) {
	mark := obj{"type": "ephemeral"}
	if a.cacheTTL != "" {
		mark["ttl"] = a.cacheTTL
	}
	left := maxCacheMarks
	if len(tools) > 0 {
		tools[len(tools)-1]["cache_control"] = mark
		left--
	}
	if block := lastCacheable(messages); block != nil {
		block["cache_control"] = mark
		left--
	}
	for i := 0; i < len(system) && left > 0; i++ {
		system[i]["cache_control"] = mark
		left--
	}
}

// lastCacheable finds the last block of a conversation that can carry a
// cache mark. Thinking blocks cannot.
func lastCacheable(messages []obj) obj {
	for i := len(messages) - 1; i >= 0; i-- {
		blocks, _ := messages[i]["content"].([]obj)
		for j := len(blocks) - 1; j >= 0; j-- {
			switch blocks[j]["type"] {
			case "thinking", "redacted_thinking":
			default:
				return blocks[j]
			}
		}
	}
	return nil
}

// An event of a Messages stream.
type claudeEvent struct {
	Type    string `json:"type"`
	Index   *num   `json:"index"`
	Message *struct {
		Usage *claudeUsage `json:"usage"`
	} `json:"message"`
	ContentBlock *claudeBlock    `json:"content_block"`
	Delta        *claudeDelta    `json:"delta"`
	Usage        *claudeUsage    `json:"usage"`
	Error        json.RawMessage `json:"error"`
}

type claudeBlock struct {
	Type      string          `json:"type"`
	Text      string          `json:"text"`
	Thinking  string          `json:"thinking"`
	Signature string          `json:"signature"`
	Data      string          `json:"data"`
	ID        string          `json:"id"`
	Name      string          `json:"name"`
	Input     json.RawMessage `json:"input"`
}

type claudeDelta struct {
	Type        string          `json:"type"`
	Text        string          `json:"text"`
	Thinking    string          `json:"thinking"`
	Signature   string          `json:"signature"`
	PartialJSON string          `json:"partial_json"`
	StopReason  json.RawMessage `json:"stop_reason"`
}

type claudeUsage struct {
	InputTokens         *num `json:"input_tokens"`
	OutputTokens        *num `json:"output_tokens"`
	CacheReadTokens     *num `json:"cache_read_input_tokens"`
	CacheCreationTokens *num `json:"cache_creation_input_tokens"`
}

// claudeOpen is a content block that has started and not yet stopped.
type claudeOpen struct {
	kind      string // the block's type
	id        string // a tool_use block's id, as its events carry it
	signature strings.Builder
	input     string // a tool_use block's input, if it came whole with the start
	gotInput  bool   // input arrived in pieces
}

// claudeDecoder reads the stream of one Messages answer.
type claudeDecoder struct {
	p     *provider
	begun func()

	open     map[int]*claudeOpen
	ids      map[string]bool // tool call ids handed out in this answer
	calls    int             // tool calls in the answer
	usage    core.Usage
	stop     string // stop_reason, once the stream gave it
	answered bool   // a content block arrived
	last     []byte // the last event, for error reports
}

func (a *anthropic) newDecoder(begun func()) decoder {
	return &claudeDecoder{p: a.provider, begun: begun, open: make(map[int]*claudeOpen)}
}

func (d *claudeDecoder) stopped() bool { return d.stop != "" }

// leftover has nothing to add: this decoder holds no text back.
func (d *claudeDecoder) leftover(out []core.Event) []core.Event { return out }

func (d *claudeDecoder) event(out []core.Event, sse sseEvent) ([]core.Event, bool, error) {
	data := bytes.TrimSpace(sse.Data)
	var ev claudeEvent
	if len(data) > 0 {
		if data[0] != '{' {
			return out, false, nil // Not an event of this API.
		}
		d.last = append(d.last[:0], data...)
		if err := json.Unmarshal(data, &ev); err != nil {
			return out, false, d.p.badEvent(data, err)
		}
	}
	// The event's name is in its data too; the data's is the one the
	// API documents, and the line's is used when the data has none.
	kind := ev.Type
	if kind == "" {
		kind = sse.Name
	}

	switch kind {
	case "message_start":
		if ev.Message != nil && ev.Message.Usage != nil {
			out = d.takeUsage(out, ev.Message.Usage)
		}

	case "content_block_start":
		if ev.ContentBlock == nil {
			break
		}
		// A block has opened: the model is answering, even if this
		// block's first words are still to come, or never come, as
		// with thinking that the API is asked not to show.
		d.answered = true
		d.begun()
		out = d.startBlock(out, countOf(ev.Index), ev.ContentBlock)

	case "content_block_delta":
		if ev.Delta != nil {
			out = d.blockDelta(out, countOf(ev.Index), ev.Delta)
		}

	case "content_block_stop":
		i := countOf(ev.Index)
		if b := d.open[i]; b != nil {
			out = d.stopBlock(out, b)
			delete(d.open, i)
		}

	case "message_delta":
		if ev.Delta != nil {
			if reason, ok := jsonString(ev.Delta.StopReason); ok && reason != "" {
				d.stop = reason
			}
		}
		if ev.Usage != nil {
			out = d.takeUsage(out, ev.Usage)
		}

	case "message_stop":
		out, err := d.finishUp(out, true)
		return out, true, err

	case "error":
		return out, false, d.p.apiError(0, nil, data)

	default:
		// "ping", and whatever the API adds later: its documentation
		// asks that unknown events be passed over.
	}
	return out, false, nil
}

// takeUsage folds a usage report into the counts so far and hands the
// whole of them on. The API's counts are running totals, and a report
// leaves out what has not changed.
func (d *claudeDecoder) takeUsage(out []core.Event, u *claudeUsage) []core.Event {
	if u.InputTokens != nil {
		d.usage.InputTokens = u.InputTokens.count()
	}
	if u.OutputTokens != nil {
		d.usage.OutputTokens = u.OutputTokens.count()
	}
	if u.CacheReadTokens != nil {
		d.usage.CacheReadTokens = u.CacheReadTokens.count()
	}
	if u.CacheCreationTokens != nil {
		d.usage.CacheWriteTokens = u.CacheCreationTokens.count()
	}
	return append(out, core.Event{Type: core.EventUsage, Usage: d.usage})
}

func (d *claudeDecoder) startBlock(out []core.Event, index int, cb *claudeBlock) []core.Event {
	if old := d.open[index]; old != nil {
		out = d.stopBlock(out, old) // The server never closed the block it is replacing.
	}
	b := &claudeOpen{kind: cb.Type}
	d.open[index] = b
	switch cb.Type {
	case "text":
		if cb.Text != "" {
			out = append(out, core.Event{Type: core.EventTextDelta, Text: cb.Text})
		}
	case "thinking":
		if cb.Thinking != "" {
			out = append(out, core.Event{Type: core.EventThinkingDelta, Text: cb.Thinking})
		}
		b.signature.WriteString(cb.Signature)
	case "redacted_thinking":
		// Reasoning the API will not show: it arrives whole, and goes
		// back whole in the next request.
		if cb.Data != "" {
			out = append(out, core.Event{Type: core.EventThinkingSignature, Opaque: cb.Data})
		}
	case "tool_use":
		if d.ids == nil {
			d.ids = make(map[string]bool)
		}
		b.id = d.p.callID(d.ids, cb.ID)
		d.calls++
		// The input is "{}" here and follows in pieces. A server that
		// sends it whole with the start is taken at its word if no
		// pieces follow.
		if in := bytes.TrimSpace(cb.Input); len(in) > 0 && !bytes.Equal(in, []byte("{}")) && !bytes.Equal(in, []byte("null")) {
			b.input = string(in)
		}
		out = append(out, core.Event{Type: core.EventToolUseStart, ID: b.id, Name: cb.Name})
	default:
		// Blocks of the API's own tools (server_tool_use and their
		// results) and kinds added later have no place in core's
		// vocabulary and are passed over.
	}
	return out
}

func (d *claudeDecoder) blockDelta(out []core.Event, index int, delta *claudeDelta) []core.Event {
	b := d.open[index]
	switch delta.Type {
	case "text_delta":
		if delta.Text != "" {
			out = append(out, core.Event{Type: core.EventTextDelta, Text: delta.Text})
		}
	case "thinking_delta":
		if delta.Thinking != "" {
			out = append(out, core.Event{Type: core.EventThinkingDelta, Text: delta.Thinking})
		}
	case "signature_delta":
		if b != nil {
			b.signature.WriteString(delta.Signature)
		}
	case "input_json_delta":
		// The API's own tools stream their input the same way; only a
		// tool_use block's input is the caller's business.
		if b != nil && b.kind == "tool_use" && delta.PartialJSON != "" {
			b.gotInput = true
			out = append(out, core.Event{Type: core.EventToolUseDelta, ID: b.id, Text: delta.PartialJSON})
		}
	}
	return out
}

// stopBlock closes a content block: a thinking block hands on its
// signature, a tool call is declared complete.
func (d *claudeDecoder) stopBlock(out []core.Event, b *claudeOpen) []core.Event {
	switch b.kind {
	case "thinking":
		if sig := b.signature.String(); sig != "" {
			out = append(out, core.Event{Type: core.EventThinkingSignature, Signature: sig})
		}
	case "tool_use":
		if !b.gotInput && b.input != "" {
			out = append(out, core.Event{Type: core.EventToolUseDelta, ID: b.id, Text: b.input})
		}
		out = append(out, core.Event{Type: core.EventToolUseStop, ID: b.id})
	}
	return out
}

func (d *claudeDecoder) end(out []core.Event) ([]core.Event, error) {
	return d.finishUp(out, false)
}

// finishUp closes the answer. said reports whether the stream ended with
// message_stop; otherwise the connection just closed.
//
// The rule is the chat-completions one. A stream that gave a stop_reason
// is whole with or without message_stop. A stream that gave none is whole
// only if it ended with message_stop and brought some answer; the reason
// is then read off the answer.
func (d *claudeDecoder) finishUp(out []core.Event, said bool) ([]core.Event, error) {
	if d.stop == "" && !(said && d.answered) {
		// A block that was open when the stream was cut is not closed:
		// nobody may take half a tool call's arguments for all of them.
		return out, d.p.endedEarly(d.last)
	}
	// Blocks the server left open are closed in their order.
	indexes := make([]int, 0, len(d.open))
	for i := range d.open {
		indexes = append(indexes, i)
	}
	sort.Ints(indexes)
	for _, i := range indexes {
		out = d.stopBlock(out, d.open[i])
		delete(d.open, i)
	}
	return append(out, core.Event{Type: core.EventStop, StopReason: d.stopReason()}), nil
}

// stopReason turns the API's stop_reason into core's.
func (d *claudeDecoder) stopReason() core.StopReason {
	switch d.stop {
	case "max_tokens", "model_context_window_exceeded":
		// The second is the answer running into the end of the
		// context window: cut short all the same.
		return core.StopMaxTokens
	case "stop_sequence":
		return core.StopSequence
	case "refusal":
		return core.StopRefusal
	}
	// "end_turn", "tool_use", "pause_turn" (a pause in the API's own
	// tools, which core has no word for), no reason, or a new one. As
	// with chat completions, whether tools are to be run is read off the
	// answer: tool calls that are there must be answered.
	if d.calls > 0 {
		return core.StopToolUse
	}
	return core.StopEndTurn
}

// Models lists the models the account can use, with the sizes the API
// reports for them.
func (a *anthropic) Models(ctx context.Context) ([]core.ModelInfo, error) {
	var out []core.ModelInfo
	listed := map[string]listedModel{}
	after := ""
	seen := make(map[string]bool) // the places the list has continued from
	for {
		query := url.Values{"limit": {"1000"}}
		if after != "" {
			query.Set("after_id", after)
		}
		body, err := a.call(ctx, http.MethodGet, a.apiURL("/models", query), nil)
		if err != nil {
			return nil, err
		}
		var page struct {
			Data []struct {
				ID             string        `json:"id"`
				MaxInputTokens *num          `json:"max_input_tokens"`
				MaxTokens      *num          `json:"max_tokens"`
				Capabilities   *capabilities `json:"capabilities"`
			} `json:"data"`
			HasMore bool   `json:"has_more"`
			LastID  string `json:"last_id"`
		}
		if err := json.Unmarshal(body, &page); err != nil {
			e := a.fail(core.ErrBadResponse, "the model list cannot be read: %v", err)
			e.Body = string(clip(body, bodyStart))
			return nil, e
		}
		for _, m := range page.Data {
			out = append(out, core.ModelInfo{
				ID:            m.ID,
				ContextWindow: countOf(m.MaxInputTokens),
				MaxOutput:     countOf(m.MaxTokens),
			})
			listed[m.ID] = listedModel{maxOutput: countOf(m.MaxTokens), off: m.Capabilities.thinkingOff()}
		}
		if !page.HasMore {
			a.listedMu.Lock()
			a.listed = listed
			a.listedMu.Unlock()
			return out, nil
		}
		if page.LastID == "" || seen[page.LastID] || len(page.Data) == 0 {
			// The list says it goes on, and gives no way to go on or
			// one that leads back to where it has been.
			e := a.fail(core.ErrBadResponse, "the model list says there is more and does not say where it continues")
			e.Body = string(clip(body, bodyStart))
			return nil, e
		}
		seen[page.LastID] = true
		after = page.LastID
	}
}

// listedModel is what the Models API said of model when it was last
// listed: nothing when it was not.
func (a *anthropic) listedModel(model string) listedModel {
	a.listedMu.Lock()
	defer a.listedMu.Unlock()
	return a.listed[model]
}

// capabilities is the part of a model's capabilities that requests go by.
type capabilities struct {
	Thinking *struct {
		Types *struct {
			Disabled     *supported `json:"disabled"`
			BetweenTools *supported `json:"between_tools"`
		} `json:"types"`
	} `json:"thinking"`
}

type supported struct {
	Supported *bool `json:"supported"`
}

// thinkingOff says how a model's thinking is turned off (listedModel.off).
// A model that does not say is asked with "disabled", as before.
func (c *capabilities) thinkingOff() string {
	if c == nil || c.Thinking == nil || c.Thinking.Types == nil {
		return ""
	}
	t := c.Thinking.Types
	if t.Disabled == nil || t.Disabled.Supported == nil || *t.Disabled.Supported {
		return ""
	}
	if t.BetweenTools != nil && t.BetweenTools.Supported != nil && *t.BetweenTools.Supported {
		return "between_tools"
	}
	return "none"
}

// countFields are the fields the API's token-counting endpoint takes. It
// refuses a body with any other, so extra fields meant for a message are
// left out of a count.
var countFields = map[string]bool{
	"model": true, "messages": true, "system": true, "tools": true, "tool_choice": true,
	"thinking": true, "output_config": true, "cache_control": true, "speed": true,
}

// CountTokens asks the API how many tokens req's prompt takes, without
// running it.
func (a *anthropic) CountTokens(ctx context.Context, req *core.Request) (int, error) {
	if req == nil {
		return 0, a.fail(core.ErrInvalidRequest, "there is no request to count")
	}
	body, err := a.messagesBody(req, false)
	if err != nil {
		return 0, err
	}
	for _, extra := range []obj{a.extra, req.Extra} {
		known := make(obj)
		for k, v := range extra {
			if countFields[k] {
				known[k] = v
			}
		}
		merge(body, known)
	}
	raw, err := encodeJSON(body)
	if err != nil {
		return 0, a.fail(core.ErrInvalidRequest, "the request cannot be written as JSON: %v", err)
	}
	answer, err := a.call(ctx, http.MethodPost, a.apiURL("/messages/count_tokens", nil), raw)
	if err != nil {
		return 0, err
	}
	var counted struct {
		InputTokens *num `json:"input_tokens"`
	}
	if json.Unmarshal(answer, &counted) != nil || counted.InputTokens == nil {
		e := a.fail(core.ErrBadResponse, "the token count cannot be read")
		e.Body = string(clip(answer, bodyStart))
		return 0, e
	}
	return counted.InputTokens.count(), nil
}
