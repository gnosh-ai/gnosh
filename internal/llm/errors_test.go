package llm

import (
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/gnosh-ai/gnosh/internal/core"
)

func testProvider(t *testing.T, typ string) *provider {
	t.Helper()
	p, err := newProvider(Config{Name: "test", Type: typ, BaseURL: "http://203.0.113.7/v1"})
	if err != nil {
		t.Fatal(err)
	}
	return p
}

// TestErrorShapes runs the error bodies the APIs document, and the ones
// their neighbours send, through the one table that sorts them.
func TestErrorShapes(t *testing.T) {
	tests := []struct {
		name    string
		status  int
		header  map[string]string
		body    string
		kind    core.ErrorKind
		message string // what the message must contain
		retry   time.Duration
	}{
		// OpenAI: {"error": {"message", "type", "param", "code"}}
		{"openai bad key", 401, nil,
			`{"error":{"message":"Incorrect API key provided: sk-abc.","type":"invalid_request_error","param":null,"code":"invalid_api_key"}}`,
			core.ErrAuth, "Incorrect API key", 0},
		{"openai country", 403, nil,
			`{"error":{"message":"Country, region, or territory not supported","type":"request_forbidden","param":null,"code":"unsupported_country_region_territory"}}`,
			core.ErrAuth, "not supported", 0},
		{"openai rate limit, seconds", 429, map[string]string{"Retry-After": "7"},
			`{"error":{"message":"Rate limit reached for requests","type":"requests","param":null,"code":"rate_limit_exceeded"}}`,
			core.ErrRateLimit, "Rate limit reached", 7 * time.Second},
		{"openai rate limit, milliseconds win", 429, map[string]string{"Retry-After": "7", "Retry-After-Ms": "1500"},
			`{"error":{"message":"Rate limit reached","type":"tokens","code":"rate_limit_exceeded"}}`,
			core.ErrRateLimit, "", 1500 * time.Millisecond},
		{"openai rate limit, wait only in the words", 429, nil,
			`{"error":{"message":"Rate limit reached for gpt-4o in organization org-x on tokens per min (TPM): Limit 30000, Used 29000, Requested 2000. Please try again in 1.2s. Visit the site.","type":"tokens","code":"rate_limit_exceeded"}}`,
			core.ErrRateLimit, "", 1200 * time.Millisecond},
		{"openai slow down", 429, nil,
			`{"error":{"message":"Slow down","type":"rate_limit_error","code":"slow_down"}}`,
			core.ErrRateLimit, "Slow down", 0},
		{"openai out of quota is not a rate limit", 429, nil,
			`{"error":{"message":"You exceeded your current quota, please check your plan and billing details.","type":"insufficient_quota","param":null,"code":"insufficient_quota"}}`,
			core.ErrAuth, "quota", 0},
		{"openai out of credit", 429, nil,
			`{"error":{"message":"Your organization has no prepaid credits remaining.","type":"insufficient_quota","code":"credit_balance_exhausted"}}`,
			core.ErrAuth, "credits", 0},
		{"openai context too long", 400, nil,
			`{"error":{"message":"This model's maximum context length is 128000 tokens. However, your messages resulted in 130532 tokens. Please reduce the length of the messages.","type":"invalid_request_error","param":"messages","code":"context_length_exceeded"}}`,
			core.ErrContextTooLong, "maximum context length", 0},
		{"openai context too long, newer wording, no code", 400, nil,
			`{"error":{"message":"Your input exceeds the context window of this model. Please adjust your input and try again.","type":"invalid_request_error","param":"input","code":null}}`,
			core.ErrContextTooLong, "context window", 0},
		{"openai one request above the per-minute limit", 429, nil,
			`{"error":{"message":"Request too large for gpt-4o in organization org-x on tokens per min (TPM): Limit 30000, Requested 45000. The input or output tokens must be reduced in order to run successfully.","type":"tokens","param":null,"code":"rate_limit_exceeded"}}`,
			core.ErrContextTooLong, "Request too large", 0},
		{"openai bad request", 400, nil,
			`{"error":{"message":"Invalid value for 'reasoning_effort'.","type":"invalid_request_error","param":"reasoning_effort","code":"invalid_value"}}`,
			core.ErrInvalidRequest, "reasoning_effort", 0},
		{"openai no such model", 404, nil,
			`{"error":{"message":"The model 'gpt-9' does not exist","type":"invalid_request_error","param":null,"code":"model_not_found"}}`,
			core.ErrInvalidRequest, "does not exist", 0},
		{"openai server error", 500, nil,
			`{"error":{"message":"The server had an error while processing your request.","type":"server_error","param":null,"code":null}}`,
			core.ErrServer, "server had an error", 0},
		{"openai overloaded", 503, map[string]string{"Retry-After": "3"},
			`{"error":{"message":"The requested model is temporarily overloaded.","type":"service_unavailable_error","code":"server_is_overloaded"}}`,
			core.ErrOverloaded, "overloaded", 3 * time.Second},

		// Anthropic: {"type": "error", "error": {"type", "message"}, "request_id"}
		{"anthropic bad request", 400, nil,
			`{"type":"error","error":{"type":"invalid_request_error","message":"messages: roles must alternate between \"user\" and \"assistant\""},"request_id":"req_011"}`,
			core.ErrInvalidRequest, "roles must alternate", 0},
		{"anthropic prompt too long", 400, nil,
			`{"type":"error","error":{"type":"invalid_request_error","message":"prompt is too long: 213742 tokens > 200000 maximum"},"request_id":"req_011"}`,
			core.ErrContextTooLong, "prompt is too long", 0},
		{"anthropic input and max_tokens too long", 400, nil,
			"{\"type\":\"error\",\"error\":{\"type\":\"invalid_request_error\",\"message\":\"input length and `max_tokens` exceed context limit: 190000 + 20000 > 200000, decrease input length or `max_tokens` and try again\"}}",
			core.ErrContextTooLong, "exceed context limit", 0},
		{"anthropic bad key", 401, nil,
			`{"type":"error","error":{"type":"authentication_error","message":"invalid x-api-key"},"request_id":"req_011"}`,
			core.ErrAuth, "invalid x-api-key", 0},
		{"anthropic billing", 402, nil,
			`{"type":"error","error":{"type":"billing_error","message":"Your credit balance is too low."}}`,
			core.ErrAuth, "credit balance", 0},
		{"anthropic permission", 403, nil,
			`{"type":"error","error":{"type":"permission_error","message":"Your API key does not have permission to use the specified resource."}}`,
			core.ErrAuth, "permission", 0},
		{"anthropic not found", 404, nil,
			`{"type":"error","error":{"type":"not_found_error","message":"model: claude-nope"}}`,
			core.ErrInvalidRequest, "claude-nope", 0},
		{"anthropic request too large", 413, nil,
			`{"type":"error","error":{"type":"request_too_large","message":"Request exceeds the maximum allowed number of bytes."}}`,
			core.ErrContextTooLong, "maximum allowed number of bytes", 0},
		{"anthropic rate limit", 429, map[string]string{"Retry-After": "42"},
			`{"type":"error","error":{"type":"rate_limit_error","message":"This request would exceed your organization's rate limit of 400,000 input tokens per minute."}}`,
			core.ErrRateLimit, "rate limit", 42 * time.Second},
		{"anthropic long-context rate limit is still a rate limit", 429, map[string]string{"Retry-After": "5"},
			`{"type":"error","error":{"type":"rate_limit_error","message":"This request would exceed the rate limit for your organization of 1,000,000 long context input tokens per minute."}}`,
			core.ErrRateLimit, "long context", 5 * time.Second},
		{"anthropic spend cap is not a rate limit", 429, nil,
			`{"type":"error","error":{"type":"rate_limit_error","message":"You have reached your API usage limits. You will regain access on 2026-09-01 at 00:00 UTC.","details":{"error_code":"enforced_spend_limit_reached"}},"request_id":"req_018"}`,
			core.ErrAuth, "usage limits", 0},
		{"anthropic api error", 500, nil,
			`{"type":"error","error":{"type":"api_error","message":"Internal server error"}}`,
			core.ErrServer, "Internal server error", 0},
		{"anthropic timeout", 504, nil,
			`{"type":"error","error":{"type":"timeout_error","message":"The request timed out."}}`,
			core.ErrServer, "timed out", 0},
		{"anthropic overloaded", 529, nil,
			`{"type":"error","error":{"type":"overloaded_error","message":"Overloaded"}}`,
			core.ErrOverloaded, "Overloaded", 0},
		{"anthropic overloaded with no body", 529, nil, ``, core.ErrOverloaded, "overloaded", 0},
		{"anthropic thinking block from an edited conversation", 400, nil,
			"{\"type\":\"error\",\"error\":{\"type\":\"invalid_request_error\",\"message\":\"messages.5.content.0: Invalid `signature` in `thinking` block. The block is bound to a different conversation.\"}}",
			core.ErrInvalidRequest, "bound to a different conversation", 0},

		// llama.cpp: {"error": {"code": 400, "message", "type"}}
		{"llama.cpp bad key", 401, nil,
			`{"error":{"code":401,"message":"Invalid API Key","type":"authentication_error"}}`,
			core.ErrAuth, "Invalid API Key", 0},
		{"llama.cpp context too long", 400, nil,
			`{"error":{"code":400,"message":"the request exceeds the available context size, try increasing it","type":"exceed_context_size_error","n_prompt_tokens":9000,"n_ctx":8192}}`,
			core.ErrContextTooLong, "exceeds the available context size", 0},
		{"llama.cpp context too long, by the words alone", 500, nil,
			`{"error":{"code":500,"message":"the request exceeds the available context size. try increasing the context size or enable context shift","type":"server_error"}}`,
			core.ErrContextTooLong, "context size", 0},
		{"llama.cpp loading", 503, nil,
			`{"error":{"code":503,"message":"Loading model","type":"unavailable_error"}}`,
			core.ErrOverloaded, "Loading model", 0},
		{"llama.cpp not found", 404, nil,
			`{"error":{"code":404,"message":"File Not Found","type":"not_found_error"}}`,
			core.ErrInvalidRequest, "File Not Found", 0},
		{"llama.cpp server error", 500, nil,
			`{"error":{"code":500,"message":"Failed to parse tool call arguments as JSON","type":"server_error"}}`,
			core.ErrServer, "Failed to parse", 0},

		// OpenRouter: {"error": {"code": 402, "message", "metadata": {"error_type"}}}
		{"openrouter out of credits", 402, nil,
			`{"error":{"code":402,"message":"Insufficient credits","metadata":{"error_type":"payment_required"}}}`,
			core.ErrAuth, "Insufficient credits", 0},
		{"openrouter asks to come back", 402, map[string]string{"Retry-After": "12"},
			`{"error":{"code":402,"message":"In-flight budget exceeded","metadata":{"limit_source":"openrouter_in_flight_budget"}}}`,
			core.ErrRateLimit, "budget", 12 * time.Second},
		{"openrouter provider rate limit", 429, map[string]string{"Retry-After": "60"},
			`{"error":{"code":429,"message":"Rate limit exceeded","metadata":{"error_type":"rate_limit_exceeded","provider_code":"rate_limited"}}}`,
			core.ErrRateLimit, "Rate limit exceeded", 60 * time.Second},
		{"openrouter context, by its error type", 400, nil,
			`{"error":{"code":400,"message":"Provider returned error","metadata":{"error_type":"context_length_exceeded"}}}`,
			core.ErrContextTooLong, "Provider returned error", 0},
		{"openrouter context, quoted from the provider", 400, nil,
			`{"error":{"code":400,"message":"Provider returned error","metadata":{"raw":"This endpoint's maximum context length is 8192 tokens. However, you requested about 9000 tokens."}}}`,
			core.ErrContextTooLong, "Provider returned error", 0},
		{"openrouter provider down", 502, nil,
			`{"error":{"code":502,"message":"Provider disconnected unexpectedly","metadata":{"error_type":"provider_unavailable"}}}`,
			core.ErrServer, "disconnected", 0},
		{"openrouter no provider", 503, nil,
			`{"error":{"code":503,"message":"No available model provider","metadata":{"error_type":"provider_overloaded"}}}`,
			core.ErrOverloaded, "No available", 0},
		{"openrouter moderation", 403, nil,
			`{"error":{"code":403,"message":"Input flagged","metadata":{"error_type":"content_policy_violation"}}}`,
			core.ErrInvalidRequest, "flagged", 0},

		// Others.
		{"groq request too large", 413, nil,
			`{"error":{"message":"Request too large for model x in organization y on tokens per minute (TPM): Limit 6000, Requested 9000, please reduce your message size and try again.","type":"tokens","code":"rate_limit_exceeded"}}`,
			core.ErrContextTooLong, "Request too large", 0},
		{"google quota with a wait in the body", 429, nil,
			`[{"error":{"code":429,"message":"Resource has been exhausted (e.g. check quota).","status":"RESOURCE_EXHAUSTED","details":[{"@type":"type.googleapis.com/google.rpc.RetryInfo","retryDelay":"17s"}]}}]`,
			core.ErrRateLimit, "exhausted", 17 * time.Second},
		{"azure wait in the words", 429, nil,
			`{"error":{"code":"429","message":"Requests have exceeded the call rate limit. Please retry after 26 seconds."}}`,
			core.ErrRateLimit, "call rate limit", 26 * time.Second},
		{"ollama: the error is a string", 404, nil, `{"error":"model 'nope' not found"}`, core.ErrInvalidRequest, "model 'nope' not found", 0},
		{"vllm: no error wrapper", 400, nil,
			`{"object":"error","message":"This model's maximum context length is 4096 tokens. However, you requested 5000 tokens (4000 in the messages, 1000 in the completion).","type":"BadRequestError","param":null,"code":400}`,
			core.ErrContextTooLong, "maximum context length", 0},
		{"fastapi: detail is a string", 422, nil, `{"detail":"Unprocessable"}`, core.ErrInvalidRequest, "Unprocessable", 0},
		{"fastapi: detail is a list", 422, nil, `{"detail":[{"loc":["body","model"],"msg":"field required","type":"value_error.missing"}]}`, core.ErrInvalidRequest, "field required", 0},
		{"a proxy's web page", 502, nil, "<html>\n<head><title>502 Bad Gateway</title></head>\n<body><center><h1>502 Bad Gateway</h1></center></body></html>", core.ErrServer, "502 Bad Gateway", 0},
		{"a web page without a title", 500, nil, "<html><body>oops</body></html>", core.ErrServer, "internal server error", 0},
		{"plain words", 500, nil, "upstream connect error or disconnect/reset before headers", core.ErrServer, "upstream connect error", 0},
		{"a bare overloaded message", 500, nil, `{"error":{"message":"The engine is currently overloaded, please try again later"}}`, core.ErrOverloaded, "overloaded", 0},
		{"a refused request that quotes the word", 400, nil, `{"type":"error","error":{"type":"invalid_request_error","message":"messages.3: tool 'resolve overloaded function' is not a valid name"}}`, core.ErrInvalidRequest, "overloaded function", 0},
		{"a proxy's echo of the request", 502, nil, `{"error":{"message":"upstream failed"},"request":{"prompt":"why is my overloaded operator ambiguous?"}}`, core.ErrServer, "upstream failed", 0},
		{"no body at all", 500, nil, ``, core.ErrServer, "internal server error", 0},
		{"too many requests, no body", 429, map[string]string{"Retry-After": "2.5"}, ``, core.ErrRateLimit, "too many requests", 2500 * time.Millisecond},
		{"body too large for a proxy", 413, nil, "<html><head><title>413 Request Entity Too Large</title></head></html>", core.ErrContextTooLong, "413", 0},
		{"request timeout", 408, nil, ``, core.ErrServer, "request timeout", 0},
		{"conflict", 409, nil, ``, core.ErrServer, "conflict", 0},
		{"teapot", 418, nil, ``, core.ErrInvalidRequest, "teapot", 0},
		{"redirect", 307, map[string]string{"Location": "https://example.com/v1/chat/completions"}, ``, core.ErrInvalidRequest, "redirect to https://example.com/v1/chat/completions", 0},
		{"an unheard-of status", 599, nil, ``, core.ErrServer, "without saying", 0},
	}
	p := testProvider(t, TypeOpenAI)
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := http.Header{}
			for k, v := range tt.header {
				h.Set(k, v)
			}
			e := p.apiError(tt.status, h, []byte(tt.body))
			if e.Kind != tt.kind {
				t.Errorf("kind = %s, want %s", e.Kind, tt.kind)
			}
			if e.Status != tt.status {
				t.Errorf("status = %d, want %d", e.Status, tt.status)
			}
			if !strings.Contains(e.Message, tt.message) {
				t.Errorf("message = %q, want it to hold %q", e.Message, tt.message)
			}
			if e.RetryAfter != tt.retry {
				t.Errorf("retry after = %s, want %s", e.RetryAfter, tt.retry)
			}
			if e.Body != tt.body {
				t.Errorf("body = %q, want the raw body", e.Body)
			}
			if e.Provider != "test" {
				t.Errorf("provider = %q", e.Provider)
			}
		})
	}
}

// TestErrorsInAStream covers errors that arrive after the response
// started well: there is no status but the one the error may name.
func TestErrorsInAStream(t *testing.T) {
	tests := []struct {
		name   string
		body   string
		kind   core.ErrorKind
		status int
	}{
		{"anthropic overloaded", `{"type":"error","error":{"type":"overloaded_error","message":"Overloaded"}}`, core.ErrOverloaded, 0},
		{"anthropic api error", `{"type":"error","error":{"type":"api_error","message":"Internal server error"}}`, core.ErrServer, 0},
		{"anthropic rate limit", `{"type":"error","error":{"type":"rate_limit_error","message":"slow"}}`, core.ErrRateLimit, 0},
		{"openrouter names a status", `{"id":"c","error":{"code":502,"message":"Provider disconnected unexpectedly"},"choices":[{"index":0,"delta":{"content":""},"finish_reason":"error"}]}`, core.ErrServer, 502},
		{"openrouter rate limit", `{"error":{"code":429,"message":"Rate limit exceeded","metadata":{"error_type":"rate_limit_exceeded"}}}`, core.ErrRateLimit, 429},
		{"llama.cpp server error", `{"error":{"code":500,"message":"slot failed","type":"server_error"}}`, core.ErrServer, 500},
		{"llama.cpp context", `{"error":{"code":400,"message":"the request exceeds the available context size, try increasing it","type":"exceed_context_size_error"}}`, core.ErrContextTooLong, 400},
		{"nothing to go by", `{"error":{"message":"something broke"}}`, core.ErrServer, 0},
		{"not even JSON", `kaboom`, core.ErrServer, 0},
	}
	p := testProvider(t, TypeOpenAI)
	for _, tt := range tests {
		e := p.apiError(0, nil, []byte(tt.body))
		if e.Kind != tt.kind || e.Status != tt.status {
			t.Errorf("%s: kind %s status %d, want %s %d", tt.name, e.Kind, e.Status, tt.kind, tt.status)
		}
		if e.Message == "" {
			t.Errorf("%s: no message", tt.name)
		}
	}
}

func TestRetryAfterHeader(t *testing.T) {
	now := func() time.Time { return time.Date(2026, 11, 13, 12, 0, 0, 0, time.UTC) }
	tests := []struct {
		name   string
		header map[string]string
		want   time.Duration
	}{
		{"none", nil, 0},
		{"seconds", map[string]string{"Retry-After": "30"}, 30 * time.Second},
		{"a fraction of a second", map[string]string{"Retry-After": "0.25"}, 250 * time.Millisecond},
		{"spaces", map[string]string{"Retry-After": " 3 "}, 3 * time.Second},
		{"milliseconds", map[string]string{"Retry-After-Ms": "250"}, 250 * time.Millisecond},
		{"milliseconds before seconds", map[string]string{"Retry-After-Ms": "250", "Retry-After": "9"}, 250 * time.Millisecond},
		{"bad milliseconds fall back to seconds", map[string]string{"Retry-After-Ms": "soon", "Retry-After": "9"}, 9 * time.Second},
		{"a date, against this machine's clock", map[string]string{"Retry-After": "Fri, 13 Nov 2026 12:00:20 GMT"}, 20 * time.Second},
		{"a date, against the response's own", map[string]string{"Retry-After": "Fri, 13 Nov 2026 12:00:20 GMT", "Date": "Fri, 13 Nov 2026 12:00:15 GMT"}, 5 * time.Second},
		{"a date in the past", map[string]string{"Retry-After": "Fri, 13 Nov 2026 11:00:00 GMT"}, 0},
		{"negative", map[string]string{"Retry-After": "-5"}, 0},
		{"words", map[string]string{"Retry-After": "later"}, 0},
		{"not a number", map[string]string{"Retry-After": "NaN"}, 0},
		{"zero", map[string]string{"Retry-After": "0"}, 0},
		{"absurdly long", map[string]string{"Retry-After": "1e30"}, time.Duration(1<<63 - 1)},
		{"infinite", map[string]string{"Retry-After": "Inf"}, time.Duration(1<<63 - 1)},
	}
	for _, tt := range tests {
		h := http.Header{}
		for k, v := range tt.header {
			h.Set(k, v)
		}
		if got := retryAfterHeader(h, now); got != tt.want {
			t.Errorf("%s: got %s, want %s", tt.name, got, tt.want)
		}
	}
}

func TestRetryHint(t *testing.T) {
	tests := []struct {
		msg  string
		want time.Duration
	}{
		{"Please try again in 20s.", 20 * time.Second},
		{"Please try again in 1.5s. Visit https://example.com", 1500 * time.Millisecond},
		{"Please try again in 6ms.", 6 * time.Millisecond},
		{"Please try again in 6m0s.", 6 * time.Minute},
		{"Please try again in 1h2m3s", time.Hour + 2*time.Minute + 3*time.Second},
		{"Try Again In 2 Minutes", 2 * time.Minute},
		{"Please retry after 26 seconds.", 26 * time.Second},
		{"retry in 1 second", time.Second},
		{"try again in a while", 0},
		{"try again in 5", 0},
		{"try again in 5 parsecs", 0},
		{"no hint here", 0},
		{"", 0},
		{"try again in 99999999999999999999h", time.Duration(1<<63 - 1)},
		{"try again in " + strings.Repeat("1s", 1<<22), 4 * time.Second},
	}
	for _, tt := range tests {
		if got := retryHint(tt.msg); got != tt.want {
			t.Errorf("%q: got %s, want %s", tt.msg, got, tt.want)
		}
	}
}

func TestPlainWords(t *testing.T) {
	tests := []struct{ in, want string }{
		{"", ""},
		{"  some   words\n here ", "some words here"},
		{"<html><head><TITLE>502 Bad\n Gateway</TITLE></head></html>", "502 Bad Gateway"},
		{"<html><body>no title</body></html>", ""},
		{"<title>never closed", ""},
		{"</title><title>", ""},
		// Letters whose lower case is longer than they are must not
		// throw the search for the title off.
		{"<İİİİİİİİ><title>x</title>", "x"},
		{"<\xff\xfe><title>y</title>", "y"},
		{strings.Repeat("word ", 200), strings.TrimSpace(strings.Repeat("word ", 60))},
	}
	for _, tt := range tests {
		if got := plainWords([]byte(tt.in)); got != tt.want {
			t.Errorf("plainWords(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

func TestClip(t *testing.T) {
	if got := string(clip([]byte("hello"), 10)); got != "hello" {
		t.Errorf("got %q", got)
	}
	if got := string(clip([]byte("hello"), 3)); got != "hel" {
		t.Errorf("got %q", got)
	}
	// A cut in the middle of a character backs up to its start.
	if got := string(clip([]byte("aé世"), 2)); got != "a" {
		t.Errorf("got %q", got)
	}
	if got := string(clip([]byte("a世界"), 6)); got != "a世" {
		t.Errorf("got %q", got)
	}
}

func FuzzReadErrorBody(f *testing.F) {
	for _, seed := range []string{
		``, `{}`, `[]`, `[{}]`, `"words"`, `12`, `null`,
		`{"error":{"message":"m","type":"t","code":429,"metadata":{"error_type":"x"},"details":[{"retryDelay":"3s","reason":"R"}]}}`,
		`{"type":"error","error":{"type":"overloaded_error","message":"Overloaded","details":{"error_code":"c"}}}`,
		`{"error":"just words"}`, `{"detail":[{"msg":"x"}]}`, `<html><title>t</title></html>`, `<title>`,
		`{"error":{"message":"Please try again in 1h2m3s"}}`,
	} {
		f.Add([]byte(seed), 500)
	}
	p, err := newProvider(Config{Name: "test", Type: TypeOpenAI, BaseURL: "http://203.0.113.7/v1"})
	if err != nil {
		f.Fatal(err)
	}
	f.Fuzz(func(t *testing.T, body []byte, status int) {
		e := p.apiError(status%700, http.Header{}, body)
		if e.Kind == "" || e.Message == "" {
			t.Fatalf("kind %q, message %q for status %d body %q", e.Kind, e.Message, status, body)
		}
		if e.RetryAfter < 0 {
			t.Fatalf("negative wait %s for body %q", e.RetryAfter, body)
		}
		if len(e.Body) > bodyStart {
			t.Fatalf("the error keeps %d bytes of the body", len(e.Body))
		}
	})
}
