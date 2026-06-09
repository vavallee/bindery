package nzbget

import (
	"net"
	"net/url"
	"strings"
	"testing"
)

// FuzzValidateNZBFetchURL fuzzes the NZB fetch-URL guard that runs on every
// indexer-supplied download URL before Bindery fetches it. A panic here is a
// DoS on the grab path; worse, a private/loopback/non-http(s) target slipping
// through is an SSRF. The default client validator delegates to
// httpsec.ValidateOutboundURL(PolicyLAN), which under PolicyLAN blocks
// loopback, link-local, and cloud-metadata while permitting RFC1918 LAN hosts.
//
// To stay hermetic and bounded (no DNS, no network) we only invoke the real
// validator when the host is empty or an IP literal — hostname resolution is
// out of scope for a fuzz target and would hang. Runs only the seed corpus
// under `go test`; doubles as an OpenSSF Scorecard fuzz target.
func FuzzValidateNZBFetchURL(f *testing.F) {
	seeds := []string{
		"",
		"http://10.0.0.5:8080/get.nzb",      // RFC1918: allowed under LAN
		"http://127.0.0.1/get.nzb",          // loopback: must reject
		"http://[::1]:6789/x",               // loopback v6: must reject
		"http://169.254.169.254/latest",     // cloud metadata: must reject
		"http://[fe80::1]/x",                // link-local: must reject
		"ftp://10.0.0.5/x",                  // non-http(s): must reject
		"file:///etc/passwd",                // non-http(s): must reject
		"http://192.168.1.10:6789/get.nzb",  // RFC1918: allowed
		"https://[2001:db8::1]/x",           // documentation range: allowed
		"http://user:pass@127.0.0.1:99999/", // loopback w/ creds + bad port
		"http://%zz",
		"\x00\x01\x02",
	}
	for _, s := range seeds {
		f.Add(s)
	}

	c := New("nzbget.invalid", 6789, "", "", "", false)

	f.Fuzz(func(t *testing.T, raw string) {
		host := hostnameOf(raw)
		// Skip non-IP hostnames so we never trigger DNS in CI/fuzzing.
		if host != "" && net.ParseIP(host) == nil {
			return
		}

		err := c.validateNZBFetchURL(raw) // must never panic

		// SSRF invariant: a dangerous IP-literal target must be rejected.
		if ip := net.ParseIP(host); ip != nil && dangerousUnderLAN(ip) && err == nil {
			t.Fatalf("validateNZBFetchURL(%q) accepted dangerous target %s", raw, ip)
		}
		// Non-http(s) schemes must always be rejected.
		if sch := schemeOf(raw); sch != "" && sch != "http" && sch != "https" && err == nil {
			t.Fatalf("validateNZBFetchURL(%q) accepted non-http(s) scheme %q", raw, sch)
		}
	})
}

// hostnameOf returns the URL hostname, or "" if raw is unparseable.
func hostnameOf(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return ""
	}
	return u.Hostname()
}

// schemeOf returns the lowercased URL scheme, or "" if raw is unparseable.
func schemeOf(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return ""
	}
	return strings.ToLower(u.Scheme)
}

// dangerousUnderLAN reports whether ip must be rejected under PolicyLAN:
// loopback, link-local, or the AWS/Azure/DO cloud-metadata address. RFC1918 is
// deliberately allowed under LAN policy so it is NOT dangerous here.
func dangerousUnderLAN(ip net.IP) bool {
	if v4 := ip.To4(); v4 != nil {
		ip = v4
	}
	if ip.IsLoopback() || ip.IsLinkLocalUnicast() {
		return true
	}
	if v4 := ip.To4(); v4 != nil {
		return v4[0] == 169 && v4[1] == 254 && v4[2] == 169 && v4[3] == 254
	}
	return false
}
