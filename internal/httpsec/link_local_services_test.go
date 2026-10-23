package httpsec

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

// metadataAddrs is every address the carve-out must keep refusing, in every
// textual form a URL or resolver can hand us.
var metadataAddrs = []string{
	"169.254.169.254",
	"169.254.170.2",
	"fd00:ec2::254",
	"::ffff:169.254.169.254",
	"::ffff:169.254.170.2",
}

func linkLocalOn(t *testing.T)  { t.Setenv(LinkLocalServicesEnvVar, "1") }
func linkLocalOff(t *testing.T) { t.Setenv(LinkLocalServicesEnvVar, "") }

func urlFor(ip string) string {
	if strings.Contains(ip, ":") {
		return "http://[" + ip + "]:9696/api"
	}
	return "http://" + ip + ":9696/api"
}

func TestLinkLocalServices_EnvParsing(t *testing.T) {
	cases := map[string]bool{
		"1": true, "true": true, "TRUE": true, " 1 ": true,
		"": false, "0": false, "false": false, "yes": false, "on": false, "garbage": false,
	}
	for v, want := range cases {
		t.Setenv(LinkLocalServicesEnvVar, v)
		if got := linkLocalServicesAllowed(); got != want {
			t.Errorf("%q: allowed = %v, want %v", v, got, want)
		}
	}
}

func TestLinkLocalServices_OffRefusesEverywhere(t *testing.T) {
	linkLocalOff(t)
	r := fakeResolver{m: map[string][]net.IP{}}
	for _, p := range []Policy{PolicyStrict, PolicyLAN, PolicyLANLoopback, PolicyService} {
		for _, ip := range []string{"169.254.1.2", "fe80::1", "::ffff:169.254.1.2"} {
			if err := validate(urlFor(ip), p, r); err == nil || !strings.Contains(err.Error(), "link-local") {
				t.Errorf("policy %d %s: err = %v, want link-local refusal", p, ip, err)
			}
		}
	}
}

func TestLinkLocalServices_OffHintOnlyForServiceClass(t *testing.T) {
	linkLocalOff(t)
	r := fakeResolver{m: map[string][]net.IP{}}
	err := validate(urlFor("169.254.1.2"), PolicyService, r)
	if err == nil || !strings.Contains(err.Error(), LinkLocalServicesEnvVar) {
		t.Errorf("service class error = %v, want it to name %s", err, LinkLocalServicesEnvVar)
	}
	for _, p := range []Policy{PolicyStrict, PolicyLAN, PolicyLANLoopback} {
		err := validate(urlFor("169.254.1.2"), p, r)
		if err == nil || strings.Contains(err.Error(), LinkLocalServicesEnvVar) {
			t.Errorf("policy %d error = %v, must not advertise the service option", p, err)
		}
	}
	// The hint would be a lie for metadata: setting the variable cannot help.
	err = validate(urlFor("169.254.169.254"), PolicyService, r)
	if err == nil || strings.Contains(err.Error(), LinkLocalServicesEnvVar) {
		t.Errorf("metadata error = %v, must not suggest the option", err)
	}
}

func TestLinkLocalServices_OnAllowsServiceClassOnly(t *testing.T) {
	linkLocalOn(t)
	r := fakeResolver{m: map[string][]net.IP{}}
	for _, ip := range []string{"169.254.1.2", "169.254.0.1", "fe80::1", "::ffff:169.254.1.2"} {
		if err := validate(urlFor(ip), PolicyService, r); err != nil {
			t.Errorf("PolicyService %s with option on: %v", ip, err)
		}
		for _, p := range []Policy{PolicyStrict, PolicyLAN, PolicyLANLoopback} {
			if err := validate(urlFor(ip), p, r); err == nil {
				t.Errorf("policy %d %s must stay refused with the option on", p, ip)
			}
		}
	}
}

func TestLinkLocalServices_MetadataAlwaysRefused(t *testing.T) {
	r := fakeResolver{m: map[string][]net.IP{}}
	for _, env := range []string{"1", ""} {
		t.Setenv(LinkLocalServicesEnvVar, env)
		for _, ip := range metadataAddrs {
			for _, p := range []Policy{PolicyStrict, PolicyLAN, PolicyLANLoopback, PolicyService} {
				if err := validate(urlFor(ip), p, r); err == nil {
					t.Errorf("env=%q policy %d %s: metadata allowed", env, p, ip)
				}
				if err := ValidateIP(mustIP(ip), p); err == nil {
					t.Errorf("env=%q policy %d ValidateIP %s: metadata allowed", env, p, ip)
				}
			}
		}
	}
	// The hostname form is refused before any lookup, whatever the case.
	linkLocalOn(t)
	for _, h := range []string{"metadata.google.internal", "METADATA.Google.Internal"} {
		if err := validate("http://"+h+"/", PolicyService, r); err == nil {
			t.Errorf("%s allowed", h)
		}
	}
}

func TestLinkLocalServices_HostnameResolution(t *testing.T) {
	linkLocalOn(t)
	pasta := []net.IP{mustIP("169.254.1.2")}
	// Case and a trailing dot are spellings of the same name; the fake
	// resolver is exact match so each spelling is registered explicitly.
	good := fakeResolver{m: map[string][]net.IP{
		"prowlarr.example.com":  pasta,
		"PROWLARR.Example.COM":  pasta,
		"prowlarr.example.com.": pasta,
		"v6.example.com":        {mustIP("fe80::1")},
	}}
	for h := range good.m {
		if err := validate("https://"+h+"/1/api", PolicyService, good); err != nil {
			t.Errorf("%s: %v", h, err)
		}
		if err := validate("https://"+h+"/1/api", PolicyStrict, good); err == nil {
			t.Errorf("%s: strict must refuse", h)
		}
	}
	bad := fakeResolver{m: map[string][]net.IP{
		"mixed.example.com":  {mustIP("169.254.1.2"), mustIP("169.254.169.254")},
		"mixed2.example.com": {mustIP("169.254.169.254"), mustIP("169.254.1.2")},
		"ecs.example.com":    {mustIP("169.254.170.2")},
		"mapped.example.com": {mustIP("::ffff:169.254.169.254")},
		"ec2.example.com":    {mustIP("fd00:ec2::254")},
	}}
	for h := range bad.m {
		if err := validate("https://"+h+"/", PolicyService, bad); err == nil {
			t.Errorf("%s resolves to metadata and must be refused", h)
		}
	}
}

// Dial time: the hostname is re-resolved on every connect, and the address the
// kernel is about to dial is what gets checked, not the name.
func TestLinkLocalServices_DialTimeCheck(t *testing.T) {
	linkLocalOn(t)
	dial := func(policy Policy, ips ...string) error {
		stubResolver(t, ips...)
		ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
		defer cancel()
		c, err := NewDialContext(policy)(ctx, "tcp", "rebind.example.com:9696")
		if c != nil {
			_ = c.Close()
		}
		return err
	}
	// Rebinding: validation saw a public address, the dial-time lookup is metadata.
	for _, ip := range metadataAddrs {
		err := dial(PolicyService, ip)
		if err == nil || !strings.Contains(err.Error(), "not allowed") {
			t.Errorf("dial resolving to %s: err = %v, want a policy refusal", ip, err)
		}
	}
	// One good record beside a metadata record still refuses the whole dial.
	if err := dial(PolicyService, "169.254.1.2", "169.254.169.254"); err == nil || !strings.Contains(err.Error(), "not allowed") {
		t.Errorf("mixed answer: err = %v, want a policy refusal", err)
	}
	// Literal metadata IP at dial time.
	if _, err := NewDialContext(PolicyService)(context.Background(), "tcp", "169.254.169.254:80"); err == nil || !strings.Contains(err.Error(), "not allowed") {
		t.Errorf("literal metadata dial: err = %v", err)
	}
	// Link-local is not refused by policy (it may still fail to connect here).
	if err := dial(PolicyService, "169.254.1.2"); err != nil && strings.Contains(err.Error(), "not allowed") {
		t.Errorf("link-local dial refused by policy with the option on: %v", err)
	}
	// Other classes never get it, even with the option on.
	for _, p := range []Policy{PolicyStrict, PolicyLAN, PolicyLANLoopback} {
		if err := dial(p, "169.254.1.2"); err == nil || !strings.Contains(err.Error(), "not allowed") {
			t.Errorf("policy %d dial to link-local: err = %v, want a policy refusal", p, err)
		}
	}
	// Option off: refused at dial too.
	linkLocalOff(t)
	if err := dial(PolicyService, "169.254.1.2"); err == nil || !strings.Contains(err.Error(), "not allowed") {
		t.Errorf("option off dial: err = %v", err)
	}
}

func TestLinkLocalServices_RedirectGuard(t *testing.T) {
	linkLocalOn(t)
	hop := func(policy Policy, target string) error {
		req, _ := http.NewRequest(http.MethodGet, target, nil)
		return RedirectGuard(policy)(req, nil)
	}
	for _, ip := range metadataAddrs {
		if err := hop(PolicyService, urlFor(ip)); err == nil {
			t.Errorf("redirect to %s allowed", ip)
		}
	}
	// The allowance is for the typed base URL only, never for a redirect hop.
	if err := hop(PolicyService, urlFor("169.254.1.2")); err == nil {
		t.Error("redirect to link-local allowed with option on")
	}
	if err := hop(PolicyLANLoopback, urlFor("169.254.1.2")); err == nil {
		t.Error("non-service policy followed a redirect to link-local")
	}
	linkLocalOff(t)
	if err := hop(PolicyService, urlFor("169.254.1.2")); err == nil {
		t.Error("redirect to link-local allowed with option off")
	}
}

// End to end: an allowed LAN service answers with a redirect to metadata and
// the guarded client must not follow it.
func TestLinkLocalServices_ClientRefusesRedirectToMetadata(t *testing.T) {
	linkLocalOn(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "http://169.254.169.254/latest/meta-data/", http.StatusFound)
	}))
	defer srv.Close()
	c := &http.Client{
		Timeout:       2 * time.Second,
		Transport:     DirectGuardedTransport(PolicyService),
		CheckRedirect: RedirectGuard(PolicyService),
	}
	resp, err := c.Get(srv.URL)
	if resp != nil {
		_ = resp.Body.Close()
	}
	if err == nil || !strings.Contains(err.Error(), "redirect blocked") {
		t.Fatalf("err = %v, want the redirect refused at the hop", err)
	}
}

// The outbound proxy treats link-local as local and dials it directly. That
// direct dial still goes through the guard, which is the only check left once
// the proxy is out of the path.
func TestLinkLocalServices_ProxyBypassStillGuarded(t *testing.T) {
	if !isPrivateIP(mustIP("169.254.1.2")) {
		t.Fatal("link-local is expected to count as local for the proxy bypass")
	}
	prev := outboundProxyURL
	outboundProxyURL, _ = url.Parse("http://proxy.lan:3128")
	t.Cleanup(func() { outboundProxyURL = prev })
	linkLocalOn(t)
	dial := guardedDialContext(PolicyService)
	for _, addr := range []string{"169.254.169.254:80", "169.254.170.2:80"} {
		if _, err := dial(context.Background(), "tcp", addr); err == nil || !strings.Contains(err.Error(), "not allowed") {
			t.Errorf("direct dial to %s: err = %v, want a policy refusal", addr, err)
		}
	}
	linkLocalOff(t)
	if _, err := dial(context.Background(), "tcp", "169.254.1.2:80"); err == nil || !strings.Contains(err.Error(), "not allowed") {
		t.Errorf("option off: err = %v, want a policy refusal", err)
	}
}
