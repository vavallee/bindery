package httpsec

import "regexp"

// secretQueryParamRE matches sensitive query-string parameters whose values
// must never reach a client-facing error. Google Books puts the API key in the
// query string as ?key=... / &key=..., which a *url.Error.Error() embeds
// verbatim; OpenLibrary and others may pass tokens/api_key similarly. The match
// is case-insensitive and stops at the next & or whitespace so only the value
// is redacted.
var secretQueryParamRE = regexp.MustCompile(`(?i)([?&](?:key|api_key|apikey|token|access_token|auth)=)[^&\s]*`)

// RedactSecrets strips sensitive query-parameter values (API keys, tokens) from
// an arbitrary string. It is meant for error strings that may embed an upstream
// request URL (e.g. a wrapped *url.Error), so the key/token is replaced with
// REDACTED before the error is logged or, worst case, surfaced to a client.
//
// This is defense-in-depth: client responses should already use a generic
// message, but redacting at the error-construction site keeps secrets out of
// any path that stringifies the error.
func RedactSecrets(s string) string {
	return secretQueryParamRE.ReplaceAllString(s, "${1}REDACTED")
}
