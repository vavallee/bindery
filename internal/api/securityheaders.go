package api

import (
	"fmt"
	"net/http"
	"strings"
)

// cspTemplate is the Content-Security-Policy with a single %s placeholder for
// the frame-ancestors source list, which is the only part configurable at
// runtime (issue #1367).
const cspTemplate = "default-src 'self'; img-src 'self' data: https:; style-src 'self' 'unsafe-inline'; script-src 'self'; connect-src 'self'; frame-ancestors %s; base-uri 'self'; form-action 'self'"

// SecurityHeaders returns middleware that sets defensive HTTP response headers
// on every response. It is mounted outside the auth middleware so 401 responses
// are also protected.
//
// frameAncestors controls whether the UI may be embedded in an <iframe>
// (issue #1367 — dashboards such as Organizr). Empty (the default) locks
// embedding down entirely: CSP `frame-ancestors 'none'` plus a belt-and-braces
// `X-Frame-Options: DENY`. A non-empty value is used verbatim as the CSP
// `frame-ancestors` source list — e.g. "'self'" for same-origin framing, or
// "https://organizr.example.com" for a specific dashboard. X-Frame-Options is
// then omitted, because it cannot express an origin allowlist and its
// DENY/SAMEORIGIN values would override the more expressive CSP directive.
func SecurityHeaders(frameAncestors string) func(http.Handler) http.Handler {
	frameAncestors = strings.TrimSpace(frameAncestors)
	locked := frameAncestors == ""
	src := frameAncestors
	if locked {
		src = "'none'"
	}
	csp := fmt.Sprintf(cspTemplate, src)

	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			h := w.Header()
			h.Set("X-Content-Type-Options", "nosniff")
			if locked {
				h.Set("X-Frame-Options", "DENY")
			}
			h.Set("Referrer-Policy", "strict-origin-when-cross-origin")
			h.Set("Content-Security-Policy", csp)

			// HSTS is meaningful only over a TLS connection. Setting it over plain
			// HTTP would cause browsers to break future HTTP access with no benefit.
			if r.TLS != nil || r.Header.Get("X-Forwarded-Proto") == "https" {
				h.Set("Strict-Transport-Security", "max-age=63072000; includeSubDomains")
			}

			next.ServeHTTP(w, r)
		})
	}
}
