package httpsec

import (
	"net"
	"testing"
)

// fuzzResolver is a hermetic stub so the fuzzer never touches DNS. It returns a
// mix of address families per call so the IP-range checks (loopback, RFC1918,
// link-local, cloud-metadata, ULA) are all exercised regardless of the hostname.
type fuzzResolver struct{}

func (fuzzResolver) LookupIP(host string) ([]net.IP, error) {
	return []net.IP{
		net.ParseIP("127.0.0.1"),
		net.ParseIP("10.0.0.1"),
		net.ParseIP("169.254.169.254"),
		net.ParseIP("::1"),
		net.ParseIP("8.8.8.8"),
	}, nil
}

// FuzzValidateOutboundURL exercises the SSRF guard with arbitrary URL strings
// across every policy. ValidateOutboundURL runs on operator- and (for some
// callers) attacker-influenced URLs, so a panic in it is a denial-of-service on
// the request path. It must reject or accept, never crash. A hermetic resolver
// keeps the fuzzer off the network. Doubles as an OpenSSF Scorecard fuzz target.
func FuzzValidateOutboundURL(f *testing.F) {
	seeds := []string{
		"",
		"http://127.0.0.1:50155",
		"https://example.com/path?q=1",
		"http://[::1]:8080",
		"http://169.254.169.254/latest/meta-data/",
		"ftp://example.com",
		"http://10.0.0.1",
		"http://user:pass@host:99999",
		"http://%zz",
		"http://example.com:not-a-port",
		"https://[fe80::1]/x",
		"http://localhost",
		"\x00\x01\x02",
		"http://" + string(rune(0)) + ".com",
	}
	for _, s := range seeds {
		f.Add(s)
	}

	policies := []Policy{PolicyStrict, PolicyLAN, PolicyLANLoopback}
	r := fuzzResolver{}
	f.Fuzz(func(t *testing.T, raw string) {
		for _, p := range policies {
			// Must never panic. The boolean result is irrelevant to the
			// invariant — we only assert it returns.
			_ = validateWithResolver(raw, p, r)
		}
	})
}
