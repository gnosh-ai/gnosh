package llm

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"maps"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gnosh-ai/gnosh/internal/core"
)

// The wire formats a provider can speak, as Config.Type names them.
const (
	// TypeOpenAI is the chat-completions format that OpenAI, OpenRouter,
	// Groq, vLLM, Ollama's /v1 and many others speak.
	TypeOpenAI = "openai"
	// TypeLlamaCpp is the chat-completions format with what llama.cpp's
	// server adds to it.
	TypeLlamaCpp = "llamacpp"
	// TypeAnthropic is Anthropic's Messages API.
	TypeAnthropic = "anthropic"
)

// DefaultMaxTokens is the max_tokens an Anthropic request carries when
// neither the Request nor the Config sets one. The Messages API will not
// take a request without the field. Every model Anthropic serves today
// accepts this value, and the public guidance is to give a streamed answer
// about this much room.
const DefaultMaxTokens = 64000

// Config says how to reach one model service and how to talk to it.
type Config struct {
	// Name is the name the provider is configured under. Provider.Name
	// returns it and every error carries it. Empty means Type.
	Name string

	// Type is the wire format: TypeOpenAI, TypeLlamaCpp or TypeAnthropic.
	Type string

	// BaseURL is where the service lives, such as http://127.0.0.1:8080/v1
	// or https://api.anthropic.com. It is required: nothing is sent
	// anywhere the configuration did not name. A chat-completions URL ends
	// where "/chat/completions" is to be added, which for most services is
	// after "/v1"; a llamacpp URL may leave the "/v1" out, and so may an
	// openai URL that is nothing but a host. An Anthropic URL may stop
	// before or after "/v1". A query string is kept on every request.
	BaseURL string

	// APIKey is the key, already resolved. It may be empty for a local
	// server, and then no key header is sent.
	APIKey string

	// Headers are extra request headers. They win over the ones the
	// provider sets, and an empty value removes a header.
	Headers map[string]string

	// Timeouts are the time limits of every request.
	Timeouts Timeouts

	// Extra is merged into the JSON body of every request. Objects are
	// merged key by key, anything else replaces what the provider would
	// write, and a nil value removes a field. A Request's own Extra is
	// merged after this one, so it wins.
	Extra map[string]any

	// HTTPClient, when not nil, sends the requests instead of the
	// provider's own client. The provider's own client speaks HTTP/1.1,
	// opens one connection per request and closes it when the request is
	// over, takes its proxy from the environment, and does not follow
	// redirects. A client given here keeps its own habits.
	HTTPClient *http.Client

	// Trace, when not nil, is told what goes over the wire: each request
	// body, each response's status and headers, and each raw event of a
	// stream. It is for finding out what a misbehaving server said. It is
	// called on the goroutine that reads the stream and must not block.
	// Request headers are never passed to it, and URLs are passed without
	// their query or password, so it sees no key.
	Trace func(TraceRecord)

	// HistoryThinking says what happens to the thinking blocks of earlier
	// assistant messages when a conversation is sent back.
	//
	//	""      the type's default
	//	"drop"  none are sent: the default for openai and llamacpp, whose
	//	        models read their own old reasoning badly and whose chat
	//	        templates mostly strip it anyway
	//	"keep"  openai and llamacpp send each message's thinking back as
	//	        reasoning_content; anthropic sends back every thinking
	//	        block that carries a signature or an opaque payload, which
	//	        is its default
	//
	// Anthropic thinking blocks without a signature came from another
	// provider and are never sent to it.
	HistoryThinking string

	// ThinkTags says whether <think>…</think> written into the answer's
	// text is split off as thinking: "" for the type's default (on for
	// llamacpp, off for openai), "on" or "off". Anthropic ignores it.
	ThinkTags string

	// PromptCache says whether an Anthropic request marks prompt-cache
	// breakpoints: "" or "on" (the default), or "off". Other types ignore
	// it.
	PromptCache string

	// CacheTTL is how long Anthropic keeps a cache entry: "" or "5m" for
	// the API's five minutes, or "1h".
	CacheTTL string

	// MaxTokensField names the chat-completions field that carries
	// Request.MaxTokens: "max_tokens" or "max_completion_tokens". Empty
	// means the type's default: max_completion_tokens for openai, which
	// is the documented field and the only one its reasoning models take,
	// and max_tokens for llamacpp.
	MaxTokensField string

	// DefaultMaxTokens is the max_tokens of an Anthropic request whose
	// Request.MaxTokens is zero. Zero means DefaultMaxTokens, the
	// constant. Other types send no cap when the Request has none.
	DefaultMaxTokens int

	// NewID, when not nil, makes the id of a tool call whose server sent
	// none. The default is "call_" and 24 random hex digits.
	NewID func() string

	// Now, when not nil, replaces time.Now. The clock is read only to
	// turn a Retry-After date into a wait when the response carries no
	// Date header of its own.
	Now func() time.Time
}

// Timeouts are the time limits of one request. Zero means no limit. Each
// one that runs out ends the request with a *core.ProviderError whose Kind
// is core.ErrTimeout and whose Timeout is the limit's name.
type Timeouts struct {
	// Connect ("connect") is how long it may take to have a connection to
	// the server: name lookup, dialling, the proxy and TLS. It ends when
	// the connection is ready, not when the response headers arrive,
	// because many servers hold their headers back until the first token
	// (see the package documentation). With a Config.HTTPClient whose
	// transport does not report connections, it ends at the headers.
	Connect time.Duration

	// FirstToken ("first_token") is how long the server may take, from
	// the moment the request is sent, to send the first event that
	// carries some of the answer. A local model can spend minutes reading
	// a long prompt before that.
	FirstToken time.Duration

	// Idle ("idle") is the longest the server may stay silent once the
	// answer has begun. Time the consumer of the stream spends on an
	// event is not counted.
	Idle time.Duration

	// Total ("total") is how long the whole request may take.
	Total time.Duration
}

// New makes the provider cfg describes. The provider is safe for use by
// several goroutines at once. An openai provider is also a
// core.ModelLister; llamacpp and anthropic providers are also a
// core.ModelLister and a core.TokenCounter.
func New(cfg Config) (core.Provider, error) {
	p, err := newProvider(cfg)
	if err != nil {
		return nil, err
	}
	switch p.kind {
	case TypeLlamaCpp:
		return &llamaCpp{openAI{p}}, nil
	case TypeAnthropic:
		return &anthropic{provider: p}, nil
	default:
		return &openAI{p}, nil
	}
}

// provider is what the three wire formats share: where the service is, how
// requests are sent to it, and the options that shape them.
type provider struct {
	name    string
	kind    string
	base    *url.URL
	key     string
	headers map[string]string
	extra   obj
	limits  Timeouts
	client  *http.Client
	traceFn func(TraceRecord)
	newID   func() string
	now     func() time.Time

	keepThinking     bool   // send thinking blocks of earlier turns back
	splitThinkTags   bool   // split <think>…</think> out of the text
	promptCache      bool   // mark Anthropic cache breakpoints
	cacheTTL         string // "" or "1h"
	maxTokensField   string
	defaultMaxTokens int

	// noImages holds the models (by the name a request gives) whose server
	// refused a request's images: from then on each image is sent to
	// them as a line saying it was left out (images.go). A server that
	// serves several models may have the eyes for one and not another.
	noImages sync.Map
	// taken holds the images (imageKey) of requests the server answered,
	// and unreadable those it could not read or would not take for their
	// size, each with the line it is sent as from then on (images.go).
	taken, unreadable sync.Map
	// fewImages holds the models whose server refused a request for
	// holding many images: from then on only the newest are sent whole
	// (images.go).
	fewImages sync.Map
}

// newProvider checks a Config and makes the provider it describes, with
// every option settled to its value for the type.
func newProvider(cfg Config) (*provider, error) {
	name := cfg.Name
	if name == "" {
		name = cfg.Type
	}
	bad := func(format string, args ...any) error {
		return fmt.Errorf("llm: provider %q: %s", name, fmt.Sprintf(format, args...))
	}

	switch cfg.Type {
	case TypeOpenAI, TypeLlamaCpp, TypeAnthropic:
	case "":
		return nil, bad("no type given; it must be %q, %q or %q", TypeOpenAI, TypeLlamaCpp, TypeAnthropic)
	default:
		return nil, bad("unknown type %q; it must be %q, %q or %q", cfg.Type, TypeOpenAI, TypeLlamaCpp, TypeAnthropic)
	}

	if strings.TrimSpace(cfg.BaseURL) == "" {
		return nil, bad("no base URL given")
	}
	base, err := url.Parse(strings.TrimSpace(cfg.BaseURL))
	if err != nil {
		return nil, bad("the base URL cannot be read: %v", err)
	}
	if base.Scheme != "http" && base.Scheme != "https" {
		return nil, bad("the base URL must start with http:// or https://")
	}
	if base.Host == "" {
		return nil, bad("the base URL names no host")
	}
	base.Path = strings.TrimRight(base.Path, "/")
	base.RawPath = ""
	base.Fragment = ""
	// A chat-completions address is wanted with its "/v1", and is taken
	// without it where that cannot be meant any other way: llama.cpp's
	// server has its API under /v1 wherever it is mounted, and no service
	// has it at the root of a host.
	switch {
	case cfg.Type == TypeLlamaCpp && !strings.HasSuffix(base.Path, "/v1"):
		base.Path += "/v1"
	case cfg.Type == TypeOpenAI && base.Path == "":
		base.Path = "/v1"
	}

	for _, d := range []struct {
		name string
		d    time.Duration
	}{
		{"connect", cfg.Timeouts.Connect},
		{"first-token", cfg.Timeouts.FirstToken},
		{"idle", cfg.Timeouts.Idle},
		{"total", cfg.Timeouts.Total},
	} {
		if d.d < 0 {
			return nil, bad("the %s time limit is negative", d.name)
		}
	}

	p := &provider{
		name:             name,
		kind:             cfg.Type,
		base:             base,
		key:              cfg.APIKey,
		headers:          maps.Clone(cfg.Headers),
		extra:            cloneObj(cfg.Extra),
		limits:           cfg.Timeouts,
		client:           cfg.HTTPClient,
		traceFn:          cfg.Trace,
		newID:            cfg.NewID,
		now:              cfg.Now,
		defaultMaxTokens: cfg.DefaultMaxTokens,
	}
	if p.client == nil {
		p.client = newHTTPClient()
	}
	if p.newID == nil {
		p.newID = randomID
	}
	if p.now == nil {
		p.now = time.Now
	}
	if p.defaultMaxTokens < 0 {
		return nil, bad("the default max tokens is negative")
	}
	if p.defaultMaxTokens == 0 {
		p.defaultMaxTokens = DefaultMaxTokens
	}

	switch cfg.HistoryThinking {
	case "":
		p.keepThinking = p.kind == TypeAnthropic
	case "keep":
		p.keepThinking = true
	case "drop":
		p.keepThinking = false
	default:
		return nil, bad("history thinking is %q; it must be \"keep\", \"drop\" or empty", cfg.HistoryThinking)
	}

	if p.splitThinkTags, err = onOff(cfg.ThinkTags, p.kind == TypeLlamaCpp); err != nil {
		return nil, bad("think tags is %s", err)
	}
	if p.kind == TypeAnthropic {
		p.splitThinkTags = false
	}
	if p.promptCache, err = onOff(cfg.PromptCache, true); err != nil {
		return nil, bad("prompt cache is %s", err)
	}

	switch cfg.CacheTTL {
	case "", "5m":
	case "1h":
		p.cacheTTL = "1h"
	default:
		return nil, bad("the cache TTL is %q; it must be \"5m\", \"1h\" or empty", cfg.CacheTTL)
	}

	switch cfg.MaxTokensField {
	case "":
		p.maxTokensField = "max_completion_tokens"
		if p.kind == TypeLlamaCpp {
			p.maxTokensField = "max_tokens"
		}
	case "max_tokens", "max_completion_tokens":
		p.maxTokensField = cfg.MaxTokensField
	default:
		return nil, bad("the max tokens field is %q; it must be \"max_tokens\", \"max_completion_tokens\" or empty", cfg.MaxTokensField)
	}
	return p, nil
}

// onOff reads an option that is "on", "off" or left to its default.
func onOff(s string, def bool) (bool, error) {
	switch s {
	case "":
		return def, nil
	case "on":
		return true, nil
	case "off":
		return false, nil
	}
	return false, fmt.Errorf("%q; it must be \"on\", \"off\" or empty", s)
}

// Name is the name the provider was configured under.
func (p *provider) Name() string { return p.name }

// endpoint is the URL of one of the service's endpoints: the base URL with
// suffix added to its path and query added to its query.
func (p *provider) endpoint(suffix string, query url.Values) string {
	return joinURL(p.base, p.base.Path+suffix, query)
}

// rootEndpoint is like endpoint for an endpoint that lives beside the
// base URL's "/v1" rather than under it, as llama.cpp's own endpoints do.
func (p *provider) rootEndpoint(suffix string, query url.Values) string {
	return joinURL(p.base, strings.TrimSuffix(p.base.Path, "/v1")+suffix, query)
}

// joinURL returns base with its path replaced and query added to its own.
func joinURL(base *url.URL, path string, query url.Values) string {
	u := *base
	u.Path = path
	if len(query) > 0 {
		q := u.Query()
		for k, vs := range query {
			q[k] = vs
		}
		u.RawQuery = q.Encode()
	}
	return u.String()
}

// fail makes the error for something that went wrong on this side of the
// wire or that the service did without saying why.
func (p *provider) fail(kind core.ErrorKind, format string, args ...any) *core.ProviderError {
	return &core.ProviderError{Provider: p.name, Kind: kind, Message: fmt.Sprintf(format, args...)}
}

// tracing reports whether there is a trace hook to tell things to. The
// records are not free to make (a request body is copied into one), so
// callers ask first.
func (p *provider) tracing() bool { return p.traceFn != nil }

// callID returns the id a tool call's events carry: the id the server gave
// it, or a made-up one if the server gave none or the answer already has
// a call by that id. used holds the ids of the answer so far.
func (p *provider) callID(used map[string]bool, wireID string) string {
	id := wireID
	for n := 0; id == "" || used[id]; n++ {
		id = p.newID()
		if n > 0 {
			// A Config.NewID that repeats itself must not hang a stream.
			id += "_" + strconv.Itoa(n)
		}
	}
	used[id] = true
	return id
}

// randomID makes up the id of a tool call whose server sent none. The id
// has to be one of a kind in the whole conversation, not only in one
// answer, because a tool result points back at it.
func randomID() string {
	var b [12]byte
	rand.Read(b[:]) // crypto/rand.Read never fails: it stops the program if the system has no randomness.
	return "call_" + hex.EncodeToString(b[:])
}
