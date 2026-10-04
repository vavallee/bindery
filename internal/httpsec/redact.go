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
}

// RedactSecrets strips credentials from an arbitrary string: the value of any
// secret query parameter (see secretParamNames) and the token in a known
// webhook path (Discord, Telegram, Slack). It is meant for error strings and
// log lines that may embed an upstream request URL (e.g. a wrapped
// *url.Error), so the secret is replaced with REDACTED before the error is
// logged, stored on a download row, or surfaced to a client.
//
// The output is for people to read, never to fetch: a redacted URL no longer
// authenticates.
func RedactSecrets(s string) string {
	s = secretQueryParamRE.ReplaceAllString(s, "${1}REDACTED")
	for _, re := range secretPathPatterns {
		s = re.ReplaceAllString(s, "${1}REDACTED")
	}
	return s
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
func RedactURLError(err error) error {
	var ue *url.Error
	if errors.As(err, &ue) {
		ue.URL = RedactSecrets(ue.URL)
	}
	return err
}

// MapSecretQueryParams rewrites the secret parameters of a raw (still escaped)
// query string and leaves every other parameter byte for byte as it was, in
// its original order. fn receives the parameter's name as written and its raw
// value, and returns the raw value to put back, or keep=false to drop the
// parameter. Parameters with an empty value are passed through untouched.
//
// It exists for callers that must change a URL's credentials without
// re-encoding the rest of it: url.Values.Encode sorts and re-escapes every
// parameter, which is harmless for display but not for a URL that will be
// fetched again.
func MapSecretQueryParams(rawQuery string, fn func(name, rawValue string) (newRawValue string, keep bool)) string {
	if rawQuery == "" {
		return rawQuery
	}
	parts := strings.Split(rawQuery, "&")
	out := parts[:0]
	for _, part := range parts {
		rawName, rawValue, hasValue := strings.Cut(part, "=")
		name, err := url.QueryUnescape(rawName)
		if err != nil {
			name = rawName
		}
		if !hasValue || rawValue == "" || !IsSecretParam(name) {
			out = append(out, part)
			continue
		}
		v, keep := fn(name, rawValue)
		if !keep {
			continue
		}
		out = append(out, rawName+"="+v)
	}
	return strings.Join(out, "&")
}
