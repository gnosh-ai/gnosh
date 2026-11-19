package llm

import (
	"bytes"
	"encoding/json"
	"math"
	"net/http"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/gnosh-ai/gnosh/internal/core"
)

// errorRule is one line of the table that sorts a service's failures into
// core's kinds. A rule matches a failure when any one of its three lists
// does: the failure carries one of the codes, its message holds one of the
// phrases, or its status falls in one of the ranges.
type errorRule struct {
	kind core.ErrorKind
	// codes are error types and codes as the APIs spell them, in lower
	// case. They are compared whole.
	codes []string
	// phrases are parts of error messages, in lower case.
	phrases []string
	// inMessage reads the phrases in the error's own message alone, not
	// in the rest of the body, which may quote the request.
	inMessage bool
	// status are ranges of HTTP statuses, both ends included.
	status [][2]int
}

// errorRules is the whole table, for all three wire formats. The first
// rule that matches wins, so the order is the precedence: what a failure
// says about itself (its code, its words) comes before its bare status,
// and "the prompt does not fit" comes before everything, because services
// report it under many statuses and it is the one failure the caller must
// answer by sending less.
//
// To teach the package a new service's way of saying something, add its
// code or phrase to the rule of the right kind.
var errorRules = []errorRule{
	{
		kind: core.ErrContextTooLong,
		codes: []string{
			"context_length_exceeded",       // OpenAI, Groq, Cerebras, OpenRouter
			"exceed_context_size_error",     // llama.cpp
			"request_too_large",             // Anthropic, with HTTP 413
			"payload_too_large",             // OpenRouter, with HTTP 413
			"string_above_max_length",       // OpenAI: one message is too long
			"string_too_long",               // OpenRouter's name for the same
			"context_window_exceeded",       // proxies
			"model_context_window_exceeded", // proxies
		},
		phrases: []string{
			"maximum context length",               // OpenAI, vLLM, Mistral, OpenRouter, Together
			"context length exceeded",              //
			"exceeds the available context size",   // llama.cpp
			"exceed context limit",                 // Anthropic: input length and max_tokens exceed context limit
			"exceeds the context window",           // OpenAI: your input exceeds the context window of this model
			"exceed the context window",            //
			"exceeds the model's context window",   //
			"exceed the model's context window",    // OpenRouter
			"prompt is too long",                   // Anthropic: prompt is too long: N tokens > M maximum
			"request too large",                    // OpenAI and Groq: one request above the tokens-per-minute limit
			"input is too long",                    // Amazon Bedrock
			"exceeds the maximum number of tokens", // Gemini
			"maximum prompt length",                // xAI
			"reduce the length of the messages",    // OpenAI, Groq, Cerebras
		},
		status: [][2]int{{413, 413}},
	},
	{
		kind: core.ErrAuth,
		codes: []string{
			"authentication_error", "authentication", "invalid_api_key", "unauthenticated", "unauthorized",
			"permission_error", "permission_denied",
			// No money is "not allowed to do this" as well: waiting does
			// not help, so these must not be taken for rate limits even
			// though most arrive with HTTP 429.
			"billing_error", "payment_required", "insufficient_quota", "credit_balance_exhausted",
			"billing_not_active", "usage_limit_exceeded", "enforced_spend_limit_reached",
			"organization_spend_limit_exceeded", "project_spend_limit_exceeded",
			"organization_usage_limit_exceeded", "account_deactivated",
		},
	},
	{
		kind:  core.ErrRateLimit,
		codes: []string{"rate_limit_error", "rate_limit_exceeded", "rate_limited", "slow_down", "resource_exhausted", "too_many_requests"},
	},
	{
		kind: core.ErrOverloaded,
		codes: []string{
			"overloaded_error", "overloaded", "server_is_overloaded", "server_overloaded", "engine_overloaded",
			"service_unavailable_error", "service_unavailable", "provider_overloaded", "unavailable_error", "unavailable",
		},
	},
	{
		kind: core.ErrInvalidRequest,
		codes: []string{
			"invalid_request_error", "invalid_request", "invalid_argument", "invalid_prompt", "unprocessable",
			"not_found_error", "not_found", "model_not_found", "content_policy_violation", "refusal",
		},
	},
	{
		// A message that says the service is overloaded. It comes after
		// the codes that say a request is refused, and before a server's
		// own, which an overloaded one may give: a request refused as
		// invalid whose message or body only quoted the word (a C++
		// "overloaded function") was waited on for ever, as a busy service
		// is (the model client review of 2026-11-19).
		kind: core.ErrOverloaded, phrases: []string{"overloaded"}, inMessage: true,
	},
	{
		kind:  core.ErrServer,
		codes: []string{"api_error", "server_error", "internal_server_error", "internal_error", "internal", "timeout_error", "timeout", "provider_unavailable", "server", "unmapped"},
	},
	{kind: core.ErrAuth, status: [][2]int{{401, 403}}},
	{kind: core.ErrRateLimit, status: [][2]int{{429, 429}}},
	{kind: core.ErrOverloaded, status: [][2]int{{503, 503}, {529, 529}}},
	{kind: core.ErrServer, status: [][2]int{{408, 409}, {425, 425}, {500, 599}}},
	{kind: core.ErrInvalidRequest, status: [][2]int{{300, 499}}},
}

// classify runs a failure through errorRules. status is zero when there
// was none; codes, the error's message and text (the message and the
// start of the body) are in lower case.
func classify(status int, codes []string, message, text string) (core.ErrorKind, bool) {
	for _, r := range errorRules {
		for _, c := range r.codes {
			for _, have := range codes {
				if have == c {
					return r.kind, true
				}
			}
		}
		where := text
		if r.inMessage {
			where = message
		}
		for _, ph := range r.phrases {
			if strings.Contains(where, ph) {
				return r.kind, true
			}
		}
		for _, rng := range r.status {
			if status >= rng[0] && status <= rng[1] {
				return r.kind, true
			}
		}
	}
	return "", false
}

// errorInfo is what a service's error body says, whatever shape it came
// in.
type errorInfo struct {
	message string
	// codes are every type, code and status word found, in lower case.
	codes []string
	// status is an HTTP status the body itself names, or zero. Errors in
	// the middle of a stream have no other.
	status int
	// retryAfter is a wait the body asks for, or zero.
	retryAfter time.Duration
}

// readErrorBody reads a service's error out of a response body or out of
// an error event. It knows the shapes the APIs document:
//
//	{"error": {"message", "type", "code", "param"}}                     OpenAI and the servers that copy it
//	{"error": {"code": 400, "message", "type"}}                         llama.cpp
//	{"type": "error", "error": {"type", "message", "details"}}          Anthropic
//	{"error": {"code": 429, "message", "metadata": {"error_type"}}}     OpenRouter
//	{"error": {"code", "message", "status", "details": [...]}}          Google, sometimes inside a list
//	{"error": "words"}, {"message": "words"}, {"detail": "words"}       assorted servers
//
// and takes anything else as plain words.
func readErrorBody(body []byte) errorInfo {
	var info errorInfo
	body = bytes.TrimSpace(body)
	if len(body) == 0 {
		return info
	}
	var top any
	dec := json.NewDecoder(bytes.NewReader(body))
	dec.UseNumber()
	if err := dec.Decode(&top); err != nil {
		info.message = plainWords(body)
		return info
	}
	if list, ok := top.([]any); ok && len(list) > 0 {
		top = list[0]
	}
	m, ok := top.(map[string]any)
	if !ok {
		if s, ok := top.(string); ok {
			info.message = s
		} else {
			info.message = plainWords(body)
		}
		return info
	}
	switch e := m["error"].(type) {
	case string:
		info.message = e
	case map[string]any:
		info.take(e)
	}
	info.take(m)
	if info.retryAfter == 0 {
		info.retryAfter = retryHint(info.message)
	}
	return info
}

// take adds what one JSON object says about an error.
func (info *errorInfo) take(e map[string]any) {
	if info.message == "" {
		for _, k := range []string{"message", "msg", "detail", "error_description"} {
			if s, ok := e[k].(string); ok && s != "" {
				info.message = s
				break
			}
		}
	}
	for _, k := range []string{"type", "code", "status", "error_type"} {
		switch v := e[k].(type) {
		case string:
			info.code(v)
		case json.Number:
			if n, err := strconv.Atoi(v.String()); err == nil && n >= 100 && n <= 599 && info.status == 0 {
				info.status = n
			}
		}
	}
	if meta, ok := e["metadata"].(map[string]any); ok {
		for _, k := range []string{"error_type", "provider_code"} {
			if s, ok := meta[k].(string); ok {
				info.code(s)
			}
		}
	}
	switch d := e["details"].(type) {
	case map[string]any: // Anthropic
		if s, ok := d["error_code"].(string); ok {
			info.code(s)
		}
	case []any: // Google
		for _, item := range d {
			dm, ok := item.(map[string]any)
			if !ok {
				continue
			}
			if s, ok := dm["reason"].(string); ok {
				info.code(s)
			}
			if s, ok := dm["retryDelay"].(string); ok && info.retryAfter == 0 {
				if d, ok := leadingDuration(strings.ToLower(s)); ok {
					info.retryAfter = d
				}
			}
		}
	}
	// A FastAPI server lists what is wrong with a request under "detail".
	if list, ok := e["detail"].([]any); ok && info.message == "" && len(list) > 0 {
		if dm, ok := list[0].(map[string]any); ok {
			if s, ok := dm["msg"].(string); ok {
				info.message = s
			}
		}
	}
}

// code adds one type, code or status word to what is known of the error.
func (info *errorInfo) code(s string) {
	s = strings.ToLower(strings.TrimSpace(s))
	// "error" is the type of Anthropic's envelope, not of what went wrong.
	if s == "" || s == "error" {
		return
	}
	info.codes = append(info.codes, s)
}

// plainWords makes a one-line message out of a body that is not JSON. A
// web page, which is what a proxy in front of a service sends when the
// service is down, is reduced to its title.
func plainWords(body []byte) string {
	s := string(clip(body, 4096))
	if strings.HasPrefix(s, "<") {
		// Only ASCII letters are folded, so that the positions found in
		// the folded text are positions in the text itself.
		low := strings.Map(func(r rune) rune {
			if r >= 'A' && r <= 'Z' {
				return r + ('a' - 'A')
			}
			return r
		}, strings.ToValidUTF8(s, "?"))
		s = strings.ToValidUTF8(s, "?")
		i := strings.Index(low, "<title>")
		j := strings.Index(low, "</title>")
		if i < 0 || j < i+len("<title>") {
			return ""
		}
		s = s[i+len("<title>") : j]
	}
	s = strings.Join(strings.Fields(s), " ")
	return strings.TrimSpace(string(clip([]byte(s), 300)))
}

// clip returns the first max bytes of b, cut at a character boundary.
func clip(b []byte, max int) []byte {
	if len(b) <= max {
		return b
	}
	b = b[:max]
	for i := 0; i < utf8.UTFMax && len(b) > 0; i++ {
		if r, size := utf8.DecodeLastRune(b); r != utf8.RuneError || size != 1 {
			break
		}
		b = b[:len(b)-1]
	}
	return b
}

// bodyStart is how much of a raw response an error keeps for the log.
const bodyStart = 2048

// apiError makes the error for a failure the service reported: a response
// with an error status, or an error in the middle of a stream (status is
// then zero and header nil).
func (p *provider) apiError(status int, header http.Header, body []byte) *core.ProviderError {
	info := readErrorBody(body)
	if status < 300 {
		// The response itself was fine, or there was none: go by the
		// status the error names, as OpenRouter's errors in a stream do.
		status = 0
		if info.status >= 400 {
			status = info.status
		}
	}
	// The telling words are usually in the message, but a service that
	// passes on another's failure may only quote them further down.
	text := strings.ToLower(info.message + "\n" + string(clip(body, 4096)))
	kind, ok := classify(status, info.codes, strings.ToLower(info.message), text)
	if !ok {
		// No status and nothing recognisable: the service broke off an
		// answer without a reason anyone can act on.
		kind = core.ErrServer
	}

	e := &core.ProviderError{
		Provider: p.name,
		Kind:     kind,
		Status:   status,
		Message:  info.message,
		Body:     string(clip(body, bodyStart)),
	}
	if header != nil {
		e.RetryAfter = retryAfterHeader(header, p.now)
	}
	if e.RetryAfter == 0 {
		e.RetryAfter = info.retryAfter
	}
	// A service that takes payment per request and names a wait wants the
	// caller to come back, not to pay (OpenRouter's in-flight budget).
	if status == http.StatusPaymentRequired && e.RetryAfter > 0 && kind == core.ErrAuth {
		e.Kind = core.ErrRateLimit
	}
	if status >= 300 && status < 400 {
		e.Message = "the server answered with a redirect"
		if header != nil && header.Get("Location") != "" {
			e.Message += " to " + header.Get("Location")
		}
		e.Message += "; the base URL has to be the address the service answers at"
	}
	if e.Message == "" {
		switch {
		case status == 529:
			e.Message = "the service is overloaded"
		case http.StatusText(status) != "":
			e.Message = strings.ToLower(http.StatusText(status))
		default:
			e.Message = "the service reported an error without saying what it was"
		}
	}
	return e
}

// retryAfterHeader reads how long a response asks the caller to wait: the
// retry-after-ms header (milliseconds), or Retry-After as seconds or as a
// date. A date is measured against the response's own Date header when it
// has one, so that a wrong clock on this machine does not matter.
func retryAfterHeader(h http.Header, now func() time.Time) time.Duration {
	if d, ok := waitOf(h.Get("Retry-After-Ms"), time.Millisecond); ok {
		return d
	}
	v := strings.TrimSpace(h.Get("Retry-After"))
	if d, ok := waitOf(v, time.Second); ok {
		return d
	}
	if t, err := http.ParseTime(v); err == nil {
		from := now()
		if d, err := http.ParseTime(h.Get("Date")); err == nil {
			from = d
		}
		if wait := t.Sub(from); wait > 0 {
			return wait
		}
	}
	return 0
}

// waitOf reads a number of units as a wait.
func waitOf(s string, unit time.Duration) (time.Duration, bool) {
	f, err := strconv.ParseFloat(strings.TrimSpace(s), 64)
	if err != nil || math.IsNaN(f) || f < 0 {
		return 0, false
	}
	return scale(f, unit), true
}

// scale returns f units as a duration, without running over.
func scale(f float64, unit time.Duration) time.Duration {
	ns := f * float64(unit)
	if ns >= float64(math.MaxInt64) {
		return time.Duration(math.MaxInt64)
	}
	return time.Duration(ns)
}

// retryHint reads a wait out of an error message, for the services that
// only say it in words: "Please try again in 1.5s" (OpenAI, Groq),
// "retry after 26 seconds" (Azure).
func retryHint(msg string) time.Duration {
	// (A hint is a few words near the start; an error a server streams
	// may be megabytes.)
	if len(msg) > maxHintRead {
		msg = msg[:maxHintRead]
	}
	low := strings.ToLower(msg)
	for _, lead := range []string{"try again in ", "retry after ", "retry in "} {
		i := strings.Index(low, lead)
		if i < 0 {
			continue
		}
		if d, ok := leadingDuration(low[i+len(lead):]); ok {
			return d
		}
	}
	return 0
}

// maxHintRead is how much of an error message retryHint reads.
const maxHintRead = 4 << 10

// leadingDuration reads a time span from the start of s, which is in lower
// case: "20ms", "1.5s", "6m0s", "26 seconds". Go writes spans as "6m0s",
// so the parts that follow one another at once are added up, a few of
// them. (It called itself for each part, and a message of a million
// "1s" ran the stack out, which no recover catches: the model client
// review of 2026-11-19.)
func leadingDuration(s string) (time.Duration, bool) {
	var total time.Duration
	read := false
	for range 4 {
		d, rest, ok := spanPart(s)
		if !ok {
			break
		}
		read = true
		if total > math.MaxInt64-d {
			total = math.MaxInt64
		} else {
			total += d
		}
		s = rest
	}
	return total, read
}

// spanPart reads one number and its unit from the start of s, and returns
// what follows them.
func spanPart(s string) (time.Duration, string, bool) {
	n := 0
	for n < len(s) && (s[n] >= '0' && s[n] <= '9' || s[n] == '.') {
		n++
	}
	if n == 0 {
		return 0, s, false
	}
	f, err := strconv.ParseFloat(s[:n], 64)
	if err != nil {
		return 0, s, false
	}
	rest := strings.TrimLeft(s[n:], " ")
	u := 0
	for u < len(rest) && rest[u] >= 'a' && rest[u] <= 'z' {
		u++
	}
	var unit time.Duration
	switch rest[:u] {
	case "ms", "msec", "millisecond", "milliseconds":
		unit = time.Millisecond
	case "s", "sec", "secs", "second", "seconds":
		unit = time.Second
	case "m", "min", "mins", "minute", "minutes":
		unit = time.Minute
	case "h", "hr", "hrs", "hour", "hours":
		unit = time.Hour
	default:
		return 0, s, false
	}
	return scale(f, unit), rest[u:], true
}
