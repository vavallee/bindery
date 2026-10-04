package auth

import (
	"bytes"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestHostAllowedForModeGrant(t *testing.T) {
	t.Setenv(AllowedHostsEnv, "books.example.com, *.home.example.net, https://reader.example.io:8443/path")
	t.Setenv(oidcRedirectBaseEnv, "https://bindery.example.org/")
	cases := []struct {
		host string
		want bool
	}{
		{"", true},
		{"192.168.1.10", true},
		{"192.168.1.10:8787", true},
		{"[fd00::1]:8787", true},
		{"[fe80::1%25eth0]:8787", true},
		{"::1", true},
		{"localhost", true},
		{"LOCALHOST:8787", true},
		{"app.localhost", true},
		{"bindery", true},
		{"nas:8787", true},
		{"nas.", true},
		{"nas.lan", true},
		{"bindery.local", true},
		{"bindery.home.arpa", true},
		{"bindery.internal", true},
		{"nas.localdomain", true},
		{"books.example.com", true},
		{"BOOKS.example.com.:443", true},
		{"a.home.example.net", true},
		{"reader.example.io", true},
		{"bindery.example.org", true},

		{"attacker.example", false},
		{"attacker.example.", false},
		{"home.example.net", false},
		{"evilhome.example.net", false},
		{"books.example.com.attacker.example", false},
		{"lan.attacker.example", false},
		{"attacker.example.com", false},
		{"0x7f.1", false},
	}
	for _, c := range cases {
		r := httptest.NewRequest(http.MethodGet, "/", nil)
		r.Host = c.host
		if got := HostAllowedForModeGrant(r); got != c.want {
			t.Errorf("Host %q: allowed=%v, want %v", c.host, got, c.want)
		}
	}
}

func TestHostAllowedForModeGrant_NoAllowlistRejectsFQDN(t *testing.T) {
	t.Setenv(AllowedHostsEnv, "")
	t.Setenv(oidcRedirectBaseEnv, "")
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	r.Host = "bindery.example.com"
	if HostAllowedForModeGrant(r) {
		t.Fatal("a dotted public name must not pass without being listed")
	}
}

func TestHostAllowedForModeGrant_ForwardedHostEveryEntry(t *testing.T) {
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	r.Host = "bindery:8787"
	r.Header.Set("X-Forwarded-Host", "nas.lan, attacker.example")
	if host, ok := rejectedModeGrantHost(r); ok || host != "attacker.example" {
		t.Fatalf("got (%q, %v), want the foreign forwarded name refused", host, ok)
	}
	r.Header.Set("X-Forwarded-Host", "nas.lan")
	if !HostAllowedForModeGrant(r) {
		t.Fatal("a local forwarded name should pass")
	}
}

func TestRefusedModeGrantHost(t *testing.T) {
	t.Setenv(AllowedHostsEnv, "")
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	r.RemoteAddr = "192.168.1.5:1234"
	r.Host = "attacker.example"
	if h, ok := RefusedModeGrantHost(ModeLocalOnly, r, nil); !ok || h != "attacker.example" {
		t.Errorf("local-only LAN peer: got (%q, %v)", h, ok)
	}
	if _, ok := RefusedModeGrantHost(ModeEnabled, r, nil); ok {
		t.Error("enabled mode never grants, so nothing is refused")
	}
	r.RemoteAddr = "203.0.113.9:1234"
	if _, ok := RefusedModeGrantHost(ModeLocalOnly, r, nil); ok {
		t.Error("a public peer is refused by the network rule, not the host rule")
	}
	if ModeGrantsAdmin(ModeDisabled, r, nil) {
		t.Error("disabled mode must not grant under a foreign Host")
	}
}

func TestRejectLoggerDeduplicatesAndCaps(t *testing.T) {
	var buf bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, nil)))
	t.Cleanup(func() { slog.SetDefault(prev) })

	l := &rejectLogger{}
	for i := 0; i < 5; i++ {
		l.note("attacker.example")
		l.note("ATTACKER.example:8080")
	}
	if n := strings.Count(buf.String(), "refused the login free grant"); n != 1 {
		t.Fatalf("one host logged %d times, want once:\n%s", n, buf.String())
	}
	buf.Reset()
	for i := 0; i < 200; i++ {
		l.note("h" + strings.Repeat("x", i%50) + ".example" + string(rune('a'+i%26)))
	}
	lines := strings.Count(buf.String(), "\n")
	if lines > hostRejectLogMax+1 {
		t.Fatalf("%d log lines for 200 hosts, want at most %d", lines, hostRejectLogMax+1)
	}
	if !strings.Contains(buf.String(), "not logging further ones") {
		t.Error("expected one suppression notice once the cap is reached")
	}
}

func TestWriteHostRejectedNamesEnvVar(t *testing.T) {
	rec := httptest.NewRecorder()
	writeHostRejected(rec, `evil"<host>`)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("code %d", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); ct != "application/json" {
		t.Errorf("content type %q", ct)
	}
	body := rec.Body.String()
	if !strings.Contains(body, AllowedHostsEnv) {
		t.Errorf("body should name %s: %s", AllowedHostsEnv, body)
	}
	if strings.Contains(body, "<host>") {
		t.Errorf("host must be JSON escaped in the body: %s", body)
	}
}
