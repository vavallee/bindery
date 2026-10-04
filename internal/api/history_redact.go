package api

import (
	"encoding/json"
	"strings"

	"github.com/vavallee/bindery/internal/httpsec"
	"github.com/vavallee/bindery/internal/indexer/newznab"
)

// redactEventData strips credentials from a history event's data blob before
// it goes into a response. Grab, import and failure events store the release
// GUID raw (and failure messages can quote a download URL), and a torznab GUID
// is often the download URL with a Jackett key and passkey in it. GET /history
// is open to every user, so nothing in the blob may carry one.
//
// URL-shaped strings, at any depth, lose every secret parameter
// (newznab.RedactDownloadURL); other strings have embedded URLs redacted
// (httpsec.RedactSecrets). The stored row is untouched, so blocklisting from
// history still keys on the raw GUID. A blob that does not decode is redacted
// as text.
func redactEventData(raw string) string {
	if raw == "" {
		return raw
	}
	var data any
	if err := json.Unmarshal([]byte(raw), &data); err != nil {
		return httpsec.RedactSecrets(raw)
	}
	redacted, changed := redactJSONValue(data)
	if !changed {
		return raw
	}
	out, err := json.Marshal(redacted)
	if err != nil {
		return httpsec.RedactSecrets(raw)
	}
	return string(out)
}

func redactJSONValue(v any) (any, bool) {
	switch t := v.(type) {
	case string:
		r := redactDataString(t)
		return r, r != t
	case map[string]any:
		changed := false
		for k, inner := range t {
			if r, c := redactJSONValue(inner); c {
				t[k] = r
				changed = true
			}
		}
		return t, changed
	case []any:
		changed := false
		for i, inner := range t {
			if r, c := redactJSONValue(inner); c {
				t[i] = r
				changed = true
			}
		}
		return t, changed
	default:
		return v, false
	}
}

// redactDataString strips a URL-shaped string with the response redaction
// and runs free text through the log redactor.
func redactDataString(s string) string {
	if !strings.ContainsAny(s, " \t\n") && (strings.Contains(s, "://") || strings.HasPrefix(strings.ToLower(s), "magnet:")) {
		return newznab.RedactDownloadURL(s)
	}
	return httpsec.RedactSecrets(s)
}
