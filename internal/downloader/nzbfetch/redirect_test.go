package nzbfetch

import (
	"errors"
	"net/http"
	"strings"
	"testing"
)

func hop(t *testing.T, rawURL string) *http.Request {
	t.Helper()
	req, err := http.NewRequest(http.MethodGet, rawURL, nil)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	return req
}

func TestRedirectPolicy_CrossHostKeepsOnlyNeutralHeaders(t *testing.T) {
	first := hop(t, "http://prowlarr:9696/1/download?apikey=K")
	next := hop(t, "https://indexer.example/getnzb/abc")
	next.Header.Set("User-Agent", "Bindery/test")
	next.Header.Set("Accept", "application/x-nzb")
	next.Header.Set("Referer", "http://prowlarr:9696/1/download?apikey=K")
	next.Header.Set("X-Api-Key", "K")

	if err := RedirectPolicy(nil)(next, []*http.Request{first}); err != nil {
		t.Fatalf("policy: %v", err)
	}
	if next.Header.Get("Referer") != "" || next.Header.Get("X-Api-Key") != "" {
		t.Fatalf("cross host hop kept a header it must drop: %v", next.Header)
	}
	if next.Header.Get("User-Agent") != "Bindery/test" || next.Header.Get("Accept") != "application/x-nzb" {
		t.Fatalf("cross host hop dropped a neutral header: %v", next.Header)
	}
}

func TestRedirectPolicy_SameHostKeepsHeadersButNotReferer(t *testing.T) {
	first := hop(t, "http://indexer.example/api?t=get&id=1&apikey=K")
	next := hop(t, "https://indexer.example/getnzb/abc")
	next.Header.Set("Referer", "http://indexer.example/api?t=get&id=1&apikey=K")
	next.Header.Set("X-Custom", "v")

	if err := RedirectPolicy(nil)(next, []*http.Request{first}); err != nil {
		t.Fatalf("policy: %v", err)
	}
	if next.Header.Get("Referer") != "" {
		t.Fatalf("Referer survived: %q", next.Header.Get("Referer"))
	}
	if next.Header.Get("X-Custom") != "v" {
		t.Fatalf("same host hop dropped a header: %v", next.Header)
	}
}

func TestRedirectPolicy_ValidatesEveryHop(t *testing.T) {
	first := hop(t, "http://prowlarr:9696/1/download")
	next := hop(t, "http://169.254.169.254/latest/meta-data")
	sentinel := errors.New("url not allowed: points to link-local address")
	var seen string
	err := RedirectPolicy(func(u string) error { seen = u; return sentinel })(next, []*http.Request{first})
	if !errors.Is(err, sentinel) {
		t.Fatalf("want the validator's error, got %v", err)
	}
	if seen != next.URL.String() {
		t.Fatalf("validator saw %q, want the hop URL", seen)
	}
}

func TestRedirectPolicy_StopsAfterTenHops(t *testing.T) {
	via := make([]*http.Request, maxRedirects)
	for i := range via {
		via[i] = hop(t, "http://indexer.example/r")
	}
	err := RedirectPolicy(nil)(hop(t, "http://indexer.example/r"), via)
	if err == nil || !strings.Contains(err.Error(), "redirects") {
		t.Fatalf("want a too many redirects error, got %v", err)
	}
}
