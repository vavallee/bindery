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
	// IPTorrents download links: ?u=<uid>;tp=<passkey>.
	"tp",
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
	s = redactMagnetURLsInText(s)
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
// that is about to leave the server: a download link or GUID in a search,
// queue or pending response. Every other parameter stays byte for byte as it
// was, in its original order, and a URL with nothing to remove is returned
// unchanged, so stripping twice is the same as stripping once. Parameters are
// split on ';' as well as '&' (IPTorrents writes ?u=1;tp=<passkey>).
//
// A path segment shaped like a passkey, RSS key or download token is replaced
// with a placeholder that differs per secret (see redactURLPath), so two
// GUIDs that differ only there still differ once stripped. Credentials in
// the authority (user:pass@host) are replaced the same way.
//
// A magnet loses its tr= announce URLs and its xs= and as= source URLs, which
// can carry private tracker passkeys. Inside a newznab getnzb link "r" is the
// API key too, including the getnzb/<guid>.nzb&i=<uid>&r=<apikey> shape where
// the parameters follow an & in the path. A string that does not parse as a
// URL is passed through RedactSecrets instead.
//
// The result does not authenticate. The grab handler takes the real URL from
// its own record of what the search returned; see api.SearchResultRegistry.
func StripURLSecrets(raw string) string {
	return stripURLSecrets(raw, true)
}

// StripDetailURLSecrets is StripURLSecrets for a detail page link, which
// people click: secret parameters and user:pass@ credentials go, but the path
// is kept as is. A detail page's path is the indexer's id for the release and
// is often hex (an MD5 on Anna's Archive, an info hash on bt4g), which the
// path rule would replace and so break the link. A detail link that is really
// the download link (a torznab item with no enclosure) belongs to
// StripURLSecrets instead.
func StripDetailURLSecrets(raw string) string {
	return stripURLSecrets(raw, false)
}

func stripURLSecrets(raw string, paths bool) string {
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
			// A magnet's announce and source URLs carry a private tracker's
			// passkey, in the query or the path, so a displayed magnet
			// drops them.
			(magnet && isMagnetURLParam(name))
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
	// Basic credentials in the authority (user:pass@host).
	if redactUserinfo(u) {
		changed = true
	}
	// A passkey or download token in the path (see redactURLPath). Only an
	// absolute URL has a path to look at: a bare newznab GUID is hex too, and
	// is an id, not a credential.
	if paths && u.Host != "" && u.Opaque == "" {
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

// magnetInTextRE finds the query of each magnet link in free text.
var magnetInTextRE = regexp.MustCompile(`(?i)(magnet:\?)([^\s"'<>]+)`)

// redactMagnetURLsInText runs the tracker and source URLs inside each magnet
// (tr=, xs=, as=) through RedactSecrets. They are percent encoded
// (tr=https%3A%2F%2F...%3Fpasskey%3D...), so the patterns that find a secret
// in a plain URL never see one; each value is decoded, redacted, and written
// back encoded when it held a secret. Everything else is left as written.
func redactMagnetURLsInText(s string) string {
	if !strings.Contains(strings.ToLower(s), "magnet:?") {
		return s
	}
	return magnetInTextRE.ReplaceAllStringFunc(s, func(m string) string {
		sub := magnetInTextRE.FindStringSubmatch(m)
		params := strings.Split(sub[2], "&")
		changed := false
		for i, p := range params {
			name, value, ok := strings.Cut(p, "=")
			if !ok || !isMagnetURLParam(name) {
				continue
			}
			dec, err := url.QueryUnescape(value)
			if err != nil || dec == value {
				continue
			}
			if red := RedactSecrets(dec); red != dec {
				params[i] = name + "=" + url.QueryEscape(red)
				changed = true
			}
		}
		if !changed {
			return m
		}
		return sub[1] + strings.Join(params, "&")
	})
}

// isMagnetURLParam reports whether a magnet parameter holds a URL that can
// carry a tracker passkey: tr (announce), xs (exact source), as (acceptable
// source).
func isMagnetURLParam(name string) bool {
	return strings.EqualFold(name, "tr") || strings.EqualFold(name, "xs") || strings.EqualFold(name, "as")
}

// stripParams drops every name=value pair, separated by '&' or ';', whose
// (unescaped) name isSecret reports, leaving the rest and the separators
// between them exactly as written.
func stripParams(params string, isSecret func(string) bool) string {
	if params == "" {
		return params
	}
	var b strings.Builder
	dropped := false
	sep, rest := "", params
	for {
		part, next := rest, ""
		i := strings.IndexAny(rest, "&;")
		if i >= 0 {
			part, next = rest[:i], rest[i:]
		}
		rawName, _, _ := strings.Cut(part, "=")
		name, err := url.QueryUnescape(rawName)
		if err != nil {
			name = rawName
		}
		if isSecret(name) {
			dropped = true
		} else {
			if b.Len() > 0 {
				b.WriteString(sep)
			}
			b.WriteString(part)
		}
		if i < 0 {
			break
		}
		sep, rest = next[:1], next[1:]
	}
	if !dropped {
		return params
	}
	return b.String()
}
