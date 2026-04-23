package urlbase

import "strings"

// Normalize returns a URL-base prefix guaranteed to be either empty or
// a single-leading-slash, no-trailing-slash string that can be appended
// to a "scheme://host:port" base URL.
//
// The normalization handles the forms operators commonly type into the
// DownloadClient form — "qbit", "/qbit", "/qbit/", "https://.../qbit"
// (from which only the path is retained) — and collapses empty and
// whitespace-only values to the empty string, preserving the pre-#369
// behaviour when url_base is unset.
func Normalize(raw string) string {
	s := strings.TrimSpace(raw)
	if s == "" {
		return ""
	}
	// Drop an accidental scheme://host prefix: some users paste the full
	// reverse-proxy URL and expect only the path segment to survive.
	if i := strings.Index(s, "://"); i >= 0 {
		rest := s[i+3:]
		if j := strings.Index(rest, "/"); j >= 0 {
			s = rest[j:]
		} else {
			s = ""
		}
	}
	s = strings.TrimRight(s, "/")
	if s == "" {
		return ""
	}
	if !strings.HasPrefix(s, "/") {
		s = "/" + s
	}
	return s
}
