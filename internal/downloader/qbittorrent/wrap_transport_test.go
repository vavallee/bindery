package qbittorrent

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"testing"

	"github.com/vavallee/bindery/internal/httpsec"
)

type recordingTransport struct {
	inner http.RoundTripper
	hosts []string
}

func (r *recordingTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	r.hosts = append(r.hosts, req.URL.Host)
	return r.inner.RoundTrip(req)
}

// TestWrapTransport_SeesAPIRequests checks the wrapper WrapTransport installs
// is the one the client's API calls go through, and that it is handed the
// existing transport to delegate to (#3851).
func TestWrapTransport_SeesAPIRequests(t *testing.T) {
	defer httpsec.AllowLoopbackForTests()()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "down", http.StatusInternalServerError)
	}))
	defer srv.Close()
	u, err := url.Parse(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	host := u.Hostname()
	port, err := strconv.Atoi(u.Port())
	if err != nil {
		t.Fatal(err)
	}

	c := New(host, port, "u", "p", "", false)
	rec := &recordingTransport{}
	c.WrapTransport(func(inner http.RoundTripper) http.RoundTripper {
		if inner == nil {
			t.Fatal("WrapTransport passed a nil inner transport")
		}
		rec.inner = inner
		return rec
	})
	_ = c.Test(context.Background())

	if len(rec.hosts) == 0 {
		t.Fatal("no API request went through the wrapped transport")
	}
	for _, h := range rec.hosts {
		if h != u.Host {
			t.Errorf("wrapped transport saw a request to %q, want %q", h, u.Host)
		}
	}
}
