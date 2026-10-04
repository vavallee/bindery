package httpsec

import (
	"errors"
	"net/url"
	"regexp"
	"strings"
)

// secretParamNames is the one list of query-parameter names whose value is a
// credential. Every redaction helper (RedactSecrets, RedactURLError,
// newznab.RedactDownloadURL and the indexer query log) works from it, so a
// name added here is covered everywhere at once.
//
// Names are matched exactly and case insensitively: "key" redacts ?key= and
// ?KEY= but leaves ?keyword= and ?monkey= alone.
var secretParamNames = []string{
	// Newznab, Prowlarr, Jackett, SABnzbd and most *arr style APIs.
	"apikey", "api_key", "jackett_apikey",
	// Google Books (?key=) and generic bearer or OAuth style tokens.
	"key", "token", "access_token", "auth",
	// Private tracker credentials embedded in .torrent and RSS links.
	"authkey", "passkey", "torrent_pass", "rsskey",
	// Basic credentials some feeds and webhooks accept in the query.
	"pass", "password", "secret",
	// Signed download links (the signature is the credential until it expires).
	"sig", "signature",
}

// secretParamSet is secretParamNames keyed by lower case name.
var secretParamSet = func() map[string]struct{} {
	m := make(map[string]struct{}, len(secretParamNames))
	for _, n := range secretParamNames {
		m[strings.ToLower(n)] = struct{}{}
	}
	return m
}()

// IsSecretParam reports whether a query parameter with this name carries a
// credential. The comparison is exact and case insensitive.
func IsSecretParam(name string) bool {
	_, ok := secretParamSet[strings.ToLower(name)]
	return ok
}

// secretQueryParamRE matches a secret parameter and its value inside free
// text, typically a URL embedded in an error string such as a *url.Error's
// `Get "https://host/x?apikey=...": dial tcp ...`. The name must follow ?, &
// or ; so only whole parameter names match, and the value stops at the next
// parameter, whitespace, quote, fragment or angle bracket so the rest of the
// message survives.
var secretQueryParamRE = func() *regexp.Regexp {
	quoted := make([]string, len(secretParamNames))
	for i, n := range secretParamNames {
		quoted[i] = regexp.QuoteMeta(n)
	}
	return regexp.MustCompile(`(?i)([?&;](?:` + strings.Join(quoted, "|") + `)=)[^&;\s"'#<>]*`)
}()

// secretPathPatterns match webhook URL shapes that carry the credential in the
// path instead of the query string. Each keeps the identifying prefix (so a
// log still says which service failed) and replaces only the token.
var secretPathPatterns = []*regexp.Regexp{
	// Discord: /api/webhooks/<id>/<token>, optionally versioned /api/v10/.
	regexp.MustCompile(`(?i)(/api(?:/v\d+)?/webhooks/\d+/)[^/?#\s"'<>]+`),
	// Telegram Bot API: /bot<id>:<token>/method.
	regexp.MustCompile(`(?i)(/bot)\d+:[A-Za-z0-9_-]+`),
	// Slack incoming webhooks and workflow triggers.
	regexp.MustCompile(`(?i)(hooks\.slack\.com/(?:services|workflows|triggers)/)[^?#\s"'<>]+`),
	// Home Assistant: /api/webhook/<webhook_id> (singular, unlike Discord).
	regexp.MustCompile(`(?i)(/api/webhook/)[^/?#\s"'<>]+`),
	// Microsoft Teams connectors: <tenant>.webhook.office.com/webhookb2/...
	// and the older outlook.office.com/webhook/...; the whole path is the key.
	regexp.MustCompile(`(?i)((?:webhook\.office\.com/webhookb2|outlook\.office\.com/webhook)/)[^?#\s"'<>]+`),
	// Apprise API stateful notify: /notify/<config key>.
	regexp.MustCompile(`(?i)(/notify/)[^/?#\s"'<>]+`),
	// ntfy.sh: the topic name is the only thing guarding a public topic.
	regexp.MustCompile(`(?i)(ntfy\.sh/)[^/?#\s"'<>]+`),
	// newznab-tmux download links: getnzb/<guid>.nzb&i=<uid>&r=<apikey> or
	// getnzb?id=<guid>&r=<apikey>. "r" is too short a name to treat as secret
	// everywhere, so it is only redacted inside a getnzb link.
	regexp.MustCompile(`(?i)(/getnzb[^#\s"'<>]*?[?&;]r=)[^&;#\s"'<>]+`),
}

// RedactSecrets strips credentials from an arbitrary string: the value of any
// secret query parameter (see secretParamNames), the token in a known
// webhook or indexer URL shape (Discord, Telegram, Slack, Home Assistant,
// Teams, Apprise, ntfy.sh, newznab getnzb links), and any URL path segment
// shaped like a passkey or download token (see redactURLPath), and the
// user:pass@ credentials in a URL's authority. It is meant
// for error strings and
// log lines that may embed an upstream request URL (e.g. a wrapped
// *url.Error), so the secret is replaced with REDACTED before the error is
// logged, stored on a download row, or surfaced to a client.
//
// The output is for people to read, never to fetch: a redacted URL no longer
// authenticates.
func RedactSecrets(s string) string {
	s = redactUserinfoInText(s)
	s = secretQueryParamRE.ReplaceAllString(s, "${1}REDACTED")
	for _, re := range secretPathPatterns {
		s = re.ReplaceAllString(s, "${1}REDACTED")
	}
	return redactURLPathsInText(s)
}

// RedactURLError scrubs credentials from the URL embedded in a *url.Error, in
// place, and returns the same error. Transport failures (timeout, DNS, TLS, an
// SSRF-guard dial rejection, a refused redirect) surface as a *url.Error whose
// Error() prints the full request URL, and download-fetch URLs carry the
// indexer apikey (see newznab.signDownloadURL) or a tracker passkey, so a raw
// %w-wrap leaks it into the download row, history, and webhook payloads.
//
// The error chain is left intact so errors.As/Is still reach the underlying net
// error (nethint's timeout/DNS classification depends on it). Non-*url.Error
// values pass through unchanged.
//
// The wrapped error's message is scrubbed too: net/http reports a Location
// header it cannot parse as `failed to parse Location header "<url>"` inside
// the *url.Error. That inner error is replaced only when its text actually
// held a secret, by a wrapper that still unwraps to it.
func RedactURLError(err error) error {
	var ue *url.Error
	if errors.As(err, &ue) {
		ue.URL = RedactSecrets(ue.URL)
		if ue.Err != nil {
			msg := ue.Err.Error()
			if redacted := RedactSecrets(msg); redacted != msg {
				ue.Err = &redactedError{msg: redacted, err: ue.Err}
			}
		}
	}
	return err
}

// redactedError carries a scrubbed message for err while keeping err
// reachable through errors.Is/As and its timeout classification.
type redactedError struct {
	msg string
	err error
}

func (e *redactedError) Error() string { return e.msg }
func (e *redactedError) Unwrap() error { return e.err }

// Timeout forwards to the wrapped error, since url.Error.Timeout type-asserts
// its direct Err rather than unwrapping.
func (e *redactedError) Timeout() bool {
	var t interface{ Timeout() bool }
	return errors.As(e.err, &t) && t.Timeout()
}

// StripURLSecrets removes every secret parameter, name and value, from a URL
// that is about to leave the server: a download link, GUID or detail link in a
// search, queue or pending response. Every other parameter stays byte for byte
// as it was, in its original order, and a URL with nothing to remove is
// returned unchanged, so stripping twice is the same as stripping once.
//
// A path segment shaped like a passkey, RSS key or download token is replaced
// with a placeholder that differs per secret (see redactURLPath), so two
// GUIDs that differ only there still differ once stripped. Credentials in
// the authority (user:pass@host) are replaced the same way.
//
// A magnet loses its tr= announce URLs, which carry private tracker passkeys.
// Inside a newznab getnzb link "r" is the API key too, including the
// getnzb/<guid>.nzb&i=<uid>&r=<apikey> shape where the parameters follow an &
// in the path. A string that does not parse as a URL is passed through
// RedactSecrets instead.
//
// The result does not authenticate. The grab handler takes the real URL from
// its own record of what the search returned; see api.SearchResultRegistry.
func StripURLSecrets(raw string) string {
	if raw == "" {
		return raw
	}
	u, err := url.Parse(raw)
	if err != nil {
		return RedactSecrets(raw)
	}
	getnzb := strings.Contains(strings.ToLower(u.Path), "/getnzb")
	magnet := strings.EqualFold(u.Scheme, "magnet")
	isSecret := func(name string) bool {
		return IsSecretParam(name) ||
			(getnzb && strings.EqualFold(name, "r")) ||
			// A magnet's announce URLs carry a private tracker's passkey, in
			// the query or the path, so a displayed magnet drops them.
			(magnet && strings.EqualFold(name, "tr"))
	}
	changed := false
	if getnzb {
		if base, params, ok := strings.Cut(u.Path, "&"); ok {
			if kept := stripParams(params, isSecret); kept != params {
				changed = true
				u.Path, u.RawPath = base, ""
				if kept != "" {
					u.Path += "&" + kept
				}
			}
		}
	}
	if kept := stripParams(u.RawQuery, isSecret); kept != u.RawQuery {
		changed = true
		u.RawQuery = kept
	}
	// A passkey or download token in the path (see redactURLPath). Only an
	// absolute URL has a path to look at: a bare newznab GUID is hex too, and
	// is an id, not a credential.
	// Basic credentials in the authority (user:pass@host).
	if redactUserinfo(u) {
		changed = true
	}
	if u.Host != "" && u.Opaque == "" {
		if p, ok := redactURLPath(u.EscapedPath()); ok {
			if dec, err := url.PathUnescape(p); err == nil {
				changed = true
				u.Path, u.RawPath = dec, p
			}
		}
	}
	if !changed {
		return raw
	}
	return u.String()
}

// stripParams drops every &-separated name=value pair whose (unescaped) name
// isSecret reports, leaving the rest exactly as written.
func stripParams(params string, isSecret func(string) bool) string {
	if params == "" {
		return params
	}
	parts := strings.Split(params, "&")
	kept := make([]string, 0, len(parts))
	for _, part := range parts {
		rawName, _, _ := strings.Cut(part, "=")
		name, err := url.QueryUnescape(rawName)
		if err != nil {
			name = rawName
		}
		if isSecret(name) {
			continue
		}
		kept = append(kept, part)
	}
	if len(kept) == len(parts) {
		return params
	}
	return strings.Join(kept, "&")
}
