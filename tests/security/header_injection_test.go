package security_test

import (
	"testing"

	"github.com/vavallee/bindery/internal/httpsec"
)

// TestHeaderInjection_CRLFInURL asserts that URL inputs carrying raw CRLF
// sequences are rejected before reaching the HTTP client. Without this,
// an attacker who controls a webhook URL could inject an arbitrary second
// header or a CRLF-terminated fake response.
//
// Real Go's net/http library rejects these at request-construction time,
// but belt-and-suspenders: we want the SSRF validator to fail first so
// we get a 400 with a clean error instead of a stacktrace.
func TestHeaderInjection_CRLFInURL(t *testing.T) {
	t.Parallel()
	// Percent-encoded CRLF (%0d%0a) is intentionally NOT in this list:
	// Go's net/http preserves the encoding on the wire, so the request-line
	// path is still a single line. Only raw CRLF bytes in the input string
	// are dangerous.
	cases := []string{
		"http://example.com/\r\nX-Injected: yes",
		"http://example.com/\nX-Injected: yes",
		"http://example.com/\r\n\r\nHTTP/1.1 200 OK",
	}
	for _, u := range cases {
		if err := httpsec.ValidateOutboundURL(u, httpsec.PolicyStrict); err == nil {
			t.Errorf("CRLF-laced URL %q should be rejected", u)
		}
	}
}
