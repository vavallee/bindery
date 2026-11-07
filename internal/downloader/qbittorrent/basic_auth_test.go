package qbittorrent

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"sync"
	"testing"
)

// proxiedQbit stands in for a seedbox: a reverse proxy demands HTTP basic auth
// on every request before qBittorrent's own login form is reached (#3693).
func proxiedQbit(t *testing.T) (*httptest.Server, func() (ok, rejected int)) {
	t.Helper()
	var mu sync.Mutex
	var okN, rejectedN int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		user, pass, has := r.BasicAuth()
		mu.Lock()
		if !has || user != "proxyuser" || pass != "proxypass" {
			rejectedN++
			mu.Unlock()
			w.Header().Set("WWW-Authenticate", `Basic realm="seedbox"`)
			http.Error(w, "Unauthorized", http.StatusUnauthorized)
			return
		}
		okN++
		mu.Unlock()
		switch r.URL.Path {
		case "/api/v2/auth/login":
			_, _ = w.Write([]byte("Ok."))
		case "/api/v2/app/version":
			_, _ = w.Write([]byte("v4.6.0"))
		case "/api/v2/torrents/info":
			_, _ = w.Write([]byte("[]"))
		case "/api/v2/torrents/delete":
			w.WriteHeader(http.StatusOK)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(srv.Close)
	return srv, func() (int, int) {
		mu.Lock()
		defer mu.Unlock()
		return okN, rejectedN
	}
}

func clientFor(t *testing.T, srv *httptest.Server) *Client {
	t.Helper()
	u, _ := url.Parse(srv.URL)
	port, _ := strconv.Atoi(u.Port())
	return New(u.Hostname(), port, "admin", "secret", "", false)
}

func TestBasicAuth_EveryRequestCarriesTheHeader(t *testing.T) {
	srv, counts := proxiedQbit(t)
	c := clientFor(t, srv)
	c.SetBasicAuth("proxyuser", "proxypass")
	ctx := context.Background()

	if err := c.Test(ctx); err != nil {
		t.Fatalf("Test (login + version): %v", err)
	}
	if _, err := c.GetTorrents(ctx, ""); err != nil {
		t.Fatalf("GetTorrents: %v", err)
	}
	if err := c.DeleteTorrent(ctx, "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", true); err != nil {
		t.Fatalf("DeleteTorrent: %v", err)
	}
	ok, rejected := counts()
	if rejected != 0 {
		t.Errorf("proxy rejected %d request(s) for missing basic auth", rejected)
	}
	if ok < 4 {
		t.Errorf("expected login, version, info and delete to reach qBittorrent, got %d", ok)
	}
}

func TestBasicAuth_AbsentWhenNotConfigured(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if _, _, has := r.BasicAuth(); has {
			t.Errorf("unexpected Authorization header on %s", r.URL.Path)
		}
		switch r.URL.Path {
		case "/api/v2/auth/login":
			_, _ = w.Write([]byte("Ok."))
		default:
			_, _ = w.Write([]byte("v4.6.0"))
		}
	}))
	defer srv.Close()
	if err := clientFor(t, srv).Test(context.Background()); err != nil {
		t.Fatalf("Test: %v", err)
	}
}

func TestBasicAuth_WithoutItTheProxyAnswers401(t *testing.T) {
	srv, _ := proxiedQbit(t)
	if err := clientFor(t, srv).Test(context.Background()); err == nil {
		t.Fatal("expected a 401 from the proxy")
	}
}

// A redirect to another host must not carry the proxy credentials with it.
func TestBasicAuth_NotForwardedToAnotherHost(t *testing.T) {
	other := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if _, _, has := r.BasicAuth(); has {
			t.Errorf("proxy credentials leaked to a different host")
		}
		_, _ = w.Write([]byte("v4.6.0"))
	}))
	defer other.Close()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v2/auth/login" {
			_, _ = w.Write([]byte("Ok."))
			return
		}
		http.Redirect(w, r, other.URL+"/api/v2/app/version", http.StatusFound)
	}))
	defer srv.Close()
	c := clientFor(t, srv)
	c.SetBasicAuth("proxyuser", "proxypass")
	_ = c.Test(context.Background())
}
