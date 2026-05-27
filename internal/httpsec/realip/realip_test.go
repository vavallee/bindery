package realip

import (
	"net"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/vavallee/bindery/internal/auth"
)

// captured records the r.RemoteAddr the wrapped handler observed.
type captured struct {
	remote string
}

func (c *captured) ServeHTTP(_ http.ResponseWriter, r *http.Request) {
	c.remote = r.RemoteAddr
}

func mustCIDRs(t *testing.T, raw string) []*net.IPNet {
	t.Helper()
	return auth.ParseTrustedProxyCIDRs(raw)
}

// runReq drives one request through TrustedRealIP and returns whatever
// RemoteAddr the inner handler saw.
func runReq(t *testing.T, trusted []*net.IPNet, peer string, headers http.Header) string {
	t.Helper()
	sink := &captured{}
	mw := TrustedRealIP(trusted)(sink)
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.RemoteAddr = peer
	if headers != nil {
		req.Header = headers
	}
	mw.ServeHTTP(httptest.NewRecorder(), req)
	return sink.remote
}

func TestTrustedRealIP_NoHeaders_TrustedPeer_Unchanged(t *testing.T) {
	got := runReq(t, mustCIDRs(t, "10.0.0.0/8"), "10.0.0.1:55000", nil)
	if got != "10.0.0.1:55000" {
		t.Fatalf("RemoteAddr = %q; want unchanged 10.0.0.1:55000", got)
	}
}

func TestTrustedRealIP_NoHeaders_UntrustedPeer_Unchanged(t *testing.T) {
	got := runReq(t, mustCIDRs(t, "10.0.0.0/8"), "8.8.8.8:443", nil)
	if got != "8.8.8.8:443" {
		t.Fatalf("RemoteAddr = %q; want unchanged 8.8.8.8:443", got)
	}
}

func TestTrustedRealIP_TrustedChain_PicksRealClient(t *testing.T) {
	// Peer 10.0.0.1 (trusted proxy) presents a chain where 10.0.0.x are all
	// trusted proxies and 1.2.3.4 is the original (untrusted) client.
	// Right-to-left walk should peel 10.0.0.2, then return 1.2.3.4.
	h := http.Header{}
	h.Set("X-Forwarded-For", "1.2.3.4, 10.0.0.2")
	got := runReq(t, mustCIDRs(t, "10.0.0.0/8"), "10.0.0.1:55000", h)
	if got != "1.2.3.4:55000" {
		t.Fatalf("RemoteAddr = %q; want 1.2.3.4:55000", got)
	}
}

func TestTrustedRealIP_UntrustedPeer_SpoofRejected(t *testing.T) {
	// Open-internet peer 1.2.3.4 forges XFF. RemoteAddr must stay 1.2.3.4.
	h := http.Header{}
	h.Set("X-Forwarded-For", "5.6.7.8")
	h.Set("True-Client-IP", "9.9.9.9")
	h.Set("X-Real-IP", "10.10.10.10")
	got := runReq(t, mustCIDRs(t, "10.0.0.0/8"), "1.2.3.4:55000", h)
	if got != "1.2.3.4:55000" {
		t.Fatalf("RemoteAddr = %q; want unchanged 1.2.3.4:55000 (spoof rejected)", got)
	}
}

func TestTrustedRealIP_AllHopsTrusted_LeftmostWins(t *testing.T) {
	// Every XFF hop is itself a trusted proxy. Fall back to leftmost
	// (standard XFF semantics — the outermost is the original client).
	h := http.Header{}
	h.Set("X-Forwarded-For", "10.0.0.5, 10.0.0.4, 10.0.0.3")
	got := runReq(t, mustCIDRs(t, "10.0.0.0/8"), "10.0.0.1:55000", h)
	if got != "10.0.0.5:55000" {
		t.Fatalf("RemoteAddr = %q; want 10.0.0.5:55000", got)
	}
}

func TestTrustedRealIP_TrueClientIP_TrustedPeer(t *testing.T) {
	h := http.Header{}
	h.Set("True-Client-IP", "203.0.113.42")
	got := runReq(t, mustCIDRs(t, "10.0.0.0/8"), "10.0.0.1:55000", h)
	if got != "203.0.113.42:55000" {
		t.Fatalf("RemoteAddr = %q; want 203.0.113.42:55000", got)
	}
}

func TestTrustedRealIP_XRealIP_TrustedPeer(t *testing.T) {
	h := http.Header{}
	h.Set("X-Real-IP", "203.0.113.99")
	got := runReq(t, mustCIDRs(t, "10.0.0.0/8"), "10.0.0.1:55000", h)
	if got != "203.0.113.99:55000" {
		t.Fatalf("RemoteAddr = %q; want 203.0.113.99:55000", got)
	}
}

func TestTrustedRealIP_XFFGarbage_FallsThrough(t *testing.T) {
	// Unparseable XFF entry: the chain of trust is broken at that hop, so
	// clientFromXFF returns the raw token. RemoteAddr is rewritten with it
	// (a downstream IP-parser will then fail closed). We assert that XFF
	// short-circuits and the True-Client-IP / X-Real-IP fallbacks are NOT
	// consulted when XFF was present.
	h := http.Header{}
	h.Set("X-Forwarded-For", "not-an-ip")
	h.Set("True-Client-IP", "203.0.113.7")
	got := runReq(t, mustCIDRs(t, "10.0.0.0/8"), "10.0.0.1:55000", h)
	if got != "not-an-ip:55000" {
		t.Fatalf("RemoteAddr = %q; want not-an-ip:55000 (XFF garbage short-circuits)", got)
	}
}

func TestTrustedRealIP_GarbageTrueClientIP_LeavesAlone(t *testing.T) {
	h := http.Header{}
	h.Set("True-Client-IP", "definitely not an ip")
	got := runReq(t, mustCIDRs(t, "10.0.0.0/8"), "10.0.0.1:55000", h)
	if got != "10.0.0.1:55000" {
		t.Fatalf("RemoteAddr = %q; want unchanged 10.0.0.1:55000", got)
	}
}

func TestTrustedRealIP_EmptyTrustedCIDRs_Noop(t *testing.T) {
	// When trustedCIDRs is empty the middleware MUST be a no-op regardless
	// of headers or peer — even a peer that *would* be trusted in a different
	// config must not have its RemoteAddr rewritten.
	h := http.Header{}
	h.Set("X-Forwarded-For", "5.6.7.8")
	h.Set("True-Client-IP", "9.9.9.9")
	h.Set("X-Real-IP", "10.10.10.10")
	if got := runReq(t, nil, "10.0.0.1:55000", h); got != "10.0.0.1:55000" {
		t.Fatalf("RemoteAddr = %q; want unchanged 10.0.0.1:55000 (no trusted proxies)", got)
	}
	if got := runReq(t, nil, "1.2.3.4:55000", h); got != "1.2.3.4:55000" {
		t.Fatalf("RemoteAddr = %q; want unchanged 1.2.3.4:55000 (no trusted proxies)", got)
	}
}

func TestTrustedRealIP_PreservesIPv6Port(t *testing.T) {
	// IPv6 peer with port. Rewritten RemoteAddr must be valid for
	// net.SplitHostPort, i.e. bracketed.
	h := http.Header{}
	h.Set("X-Forwarded-For", "1.2.3.4")
	trusted := mustCIDRs(t, "fd00::/8")
	got := runReq(t, trusted, "[fd00::1]:55000", h)
	if got != "1.2.3.4:55000" {
		t.Fatalf("RemoteAddr = %q; want 1.2.3.4:55000", got)
	}
	if h2, p2, err := net.SplitHostPort(got); err != nil || h2 != "1.2.3.4" || p2 != "55000" {
		t.Fatalf("rewritten addr not parseable: %v %q %q", err, h2, p2)
	}
}

func TestTrustedRealIP_NoPortInPeer_NoPortInRewrite(t *testing.T) {
	// Unusual: r.RemoteAddr without a port (custom listener). Keep the
	// rewrite portless rather than inventing a synthetic port.
	h := http.Header{}
	h.Set("X-Forwarded-For", "1.2.3.4")
	got := runReq(t, mustCIDRs(t, "10.0.0.0/8"), "10.0.0.1", h)
	if got != "1.2.3.4" {
		t.Fatalf("RemoteAddr = %q; want 1.2.3.4", got)
	}
}
