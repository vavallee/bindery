package slskd

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"
)

const testKey = "k3y-secret-value"

type fake struct {
	app      string
	role     int // status the role probe returns
	options  string
	sawQuery bool
}

func (f *fake) handler() http.Handler {
	mux := http.NewServeMux()
	guard := func(h http.HandlerFunc) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			if r.URL.RawQuery != "" {
				f.sawQuery = true
			}
			if r.Header.Get("X-API-Key") != testKey {
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			h(w, r)
		}
	}
	mux.HandleFunc("/api/v0/application", guard(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(f.app))
	}))
	mux.HandleFunc("/api/v0/options", guard(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(f.options))
	}))
	mux.HandleFunc("/api/v0/searches/", guard(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPut {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		w.WriteHeader(f.role)
	}))
	return mux
}

func newTest(t *testing.T, f *fake, key string) *Client {
	t.Helper()
	srv := httptest.NewServer(f.handler())
	t.Cleanup(srv.Close)
	u, _ := url.Parse(srv.URL)
	port, _ := strconv.Atoi(u.Port())
	return New(u.Hostname(), port, key, "", false)
}

const goodApp = `{"version":{"current":"0.26.0"},"server":{"isConnected":true,"isLoggedIn":true},"vpn":{"forwardedPort":50300}}`

func TestTestSuccessAndHeaderOnly(t *testing.T) {
	f := &fake{app: goodApp}
	c := newTest(t, f, testKey)
	if err := c.Test(context.Background()); err != nil {
		t.Fatal(err)
	}
	if f.sawQuery {
		t.Fatal("API key or anything else was sent in a query string")
	}
	app, err := c.Application(context.Background())
	if err != nil || app.Vpn.ForwardedPort == nil || *app.Vpn.ForwardedPort != 50300 {
		t.Fatalf("app = %+v err = %v", app, err)
	}
}

func TestTestOldVersion(t *testing.T) {
	f := &fake{app: `{"version":{"current":"0.25.1"},"server":{"isLoggedIn":true}}`}
	err := newTest(t, f, testKey).Test(context.Background())
	if err == nil || !strings.Contains(err.Error(), "0.26.0") || !strings.Contains(err.Error(), "too old") {
		t.Fatalf("err = %v", err)
	}
}

func TestTestNotLoggedIn(t *testing.T) {
	f := &fake{app: `{"version":{"current":"0.26.0"},"server":{"isLoggedIn":false}}`}
	err := newTest(t, f, testKey).Test(context.Background())
	if err == nil || !strings.Contains(err.Error(), "not logged in") {
		t.Fatalf("err = %v", err)
	}
}

func TestTestWrongKeyRedacted(t *testing.T) {
	f := &fake{app: goodApp}
	err := newTest(t, f, "wrong-key-value").Test(context.Background())
	if err == nil || !strings.Contains(err.Error(), "401") {
		t.Fatalf("err = %v", err)
	}
	if strings.Contains(err.Error(), "wrong-key-value") {
		t.Fatal("key leaked into error")
	}
}

func TestPingSkipsVersionAndLogin(t *testing.T) {
	f := &fake{app: `{"version":{"current":"0.1.0"},"server":{"isLoggedIn":false}}`}
	if err := newTest(t, f, testKey).Ping(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestOversizedBody(t *testing.T) {
	f := &fake{app: `{"version":{"current":"` + strings.Repeat("a", maxApplicationBytes+10) + `"}}`}
	err := newTest(t, f, testKey).Test(context.Background())
	if err == nil || !strings.Contains(err.Error(), "larger than") {
		t.Fatalf("err = %v", err)
	}
}

func TestNotJSON(t *testing.T) {
	f := &fake{app: `<html>hello</html>`}
	err := newTest(t, f, testKey).Test(context.Background())
	if err == nil || !strings.Contains(err.Error(), "not slskd") {
		t.Fatalf("err = %v", err)
	}
}

func TestTimeout(t *testing.T) {
	block := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-block:
		case <-r.Context().Done():
		}
	}))
	defer srv.Close()
	defer close(block)
	u, _ := url.Parse(srv.URL)
	port, _ := strconv.Atoi(u.Port())
	c := New(u.Hostname(), port, testKey, "", false)
	c.http.Timeout = 100 * time.Millisecond
	if err := c.Test(context.Background()); err == nil {
		t.Fatal("expected timeout")
	}
}

func TestProbeRole(t *testing.T) {
	cases := []struct {
		status int
		want   Role
		isErr  bool
	}{
		{http.StatusNotFound, RoleWrite, false},
		{http.StatusForbidden, RoleReadOnly, false},
		{http.StatusOK, RoleUnknown, true},
		{http.StatusInternalServerError, RoleUnknown, true},
		{http.StatusTooManyRequests, RoleUnknown, true},
	}
	for _, tc := range cases {
		f := &fake{app: goodApp, role: tc.status}
		got, err := newTest(t, f, testKey).ProbeRole(context.Background())
		if got != tc.want || (err != nil) != tc.isErr {
			t.Errorf("status %d: role %v err %v", tc.status, got, err)
		}
	}
}

func TestDownloadsDir(t *testing.T) {
	f := &fake{options: `{"directories":{"downloads":"/app/downloads ","incomplete":"/app/incomplete"}}`}
	dir, err := newTest(t, f, testKey).DownloadsDir(context.Background())
	if err != nil || dir != "/app/downloads" {
		t.Fatalf("dir %q err %v", dir, err)
	}
	f.options = `{}`
	dir, err = newTest(t, f, testKey).DownloadsDir(context.Background())
	if err != nil || dir != "" {
		t.Fatalf("dir %q err %v", dir, err)
	}
}

func TestCheckVersion(t *testing.T) {
	ok := []string{"0.26.0", "0.26.1", "0.27.0", "1.0.0", "v0.26.0", "0.26.0-canary.1", "0.26.0+abc", "0.26.0.0"}
	bad := []string{"0.25.9", "0.9.0", "", "nope", "0.x.1", "1"}
	for _, v := range ok {
		if err := CheckVersion(v); err != nil {
			t.Errorf("%q: %v", v, err)
		}
	}
	for _, v := range bad {
		if err := CheckVersion(v); err == nil {
			t.Errorf("%q accepted", v)
		}
	}
}

func TestURLBase(t *testing.T) {
	c := New("slskd.lan", 5030, "k", "/slskd/", true)
	if c.baseURL != "https://slskd.lan:5030/slskd" {
		t.Fatalf("baseURL = %s", c.baseURL)
	}
}

func TestRandomGUIDShape(t *testing.T) {
	g, err := randomGUID()
	if err != nil || len(g) != 36 || g[14] != '4' {
		t.Fatalf("guid %q err %v", g, err)
	}
}

// TestEndlessBodyIsCutOff serves a body that never ends. Only the read limit
// stops it: without it the client reads until its timeout, so the error would
// be a timeout rather than the size error.
func TestEndlessBodyIsCutOff(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		chunk := []byte(strings.Repeat("a", 64<<10))
		_, _ = w.Write([]byte(`{"version":{"current":"`))
		for r.Context().Err() == nil {
			if _, err := w.Write(chunk); err != nil {
				return
			}
		}
	}))
	defer srv.Close()
	u, _ := url.Parse(srv.URL)
	port, _ := strconv.Atoi(u.Port())
	c := New(u.Hostname(), port, testKey, "", false)
	c.http.Timeout = 3 * time.Second
	_, err := c.Application(context.Background())
	if err == nil || !strings.Contains(err.Error(), "larger than") {
		t.Fatalf("err = %v, want the size error", err)
	}
}
