package config

import (
	"net/url"
	"strings"
)

// secrets remembers every secret value a load came across, so that none of
// them can leave in a message or a dump. Values are kept out of messages by
// how the messages are written; this is the second line of defence.
type secrets struct {
	vals []string
}

// leastSecret is the shortest value scrub looks for. A shorter one would
// match by accident all over ordinary text.
const leastSecret = 6

// add remembers a secret value.
func (s *secrets) add(v string) {
	if s == nil || len(v) < leastSecret {
		return
	}
	for _, have := range s.vals {
		if have == v {
			return
		}
	}
	s.vals = append(s.vals, v)
}

// addURL remembers the parts of an address that can hold a secret: the
// password, and the query string.
func (s *secrets) addURL(raw string) {
	u, err := url.Parse(raw)
	if err != nil {
		return
	}
	if u.User != nil {
		if pw, ok := u.User.Password(); ok {
			s.add(pw)
		}
	}
	for _, vals := range u.Query() {
		for _, v := range vals {
			s.add(v)
		}
	}
}

// scrub replaces every remembered secret in text.
func (s *secrets) scrub(text string) string {
	if s == nil {
		return text
	}
	for _, v := range s.vals {
		if strings.Contains(text, v) {
			text = strings.ReplaceAll(text, v, hidden)
		}
	}
	return text
}

// DisplayURL is an address without the parts that can hold a secret: no
// user name or password, and no query string.
func DisplayURL(raw string) string {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return "(an address that cannot be read)"
	}
	out := u.Scheme + "://" + u.Host + u.EscapedPath()
	if u.RawQuery != "" {
		out += "?…"
	}
	return out
}

// ShownHookURL is a hook's address as it is shown and logged: no user name
// or password, no query string, and each long piece of the path cut to its
// start, since a webhook's path is often its secret (Slack's
// /services/T…/B…/<token>, Discord's /api/webhooks/<id>/<token>). The
// address went to /status, gnosh doctor, /hooks and the hook records whole
// (the config review of 2026-11-19).
func ShownHookURL(raw string) string {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return "(an address that cannot be read)"
	}
	parts := strings.Split(u.EscapedPath(), "/")
	for i, p := range parts {
		if len(p) > maxShownPathPiece {
			parts[i] = p[:4] + "…"
		}
	}
	out := u.Scheme + "://" + u.Host + strings.Join(parts, "/")
	if u.RawQuery != "" {
		out += "?…"
	}
	return out
}

// maxShownPathPiece is the longest piece of a hook's path shown whole.
const maxShownPathPiece = 16
