package torrentfetch_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/vavallee/bindery/internal/downloader/torrentfetch"
)

func allowAll(string) error { return nil }

func TestFetch_ReturnsBodyAndSendsHeaders(t *testing.T) {
	var accept, ua string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		accept, ua = r.Header.Get("Accept"), r.Header.Get("User-Agent")
		_, _ = w.Write([]byte("d4:infod4:name1:xee"))
	}))
	defer srv.Close()

	got, err := torrentfetch.Fetch(context.Background(), srv.URL, torrentfetch.Options{Validate: allowAll})
	if err != nil {
		t.Fatal(err)
	}
	if string(got.Data) != "d4:infod4:name1:xee" || got.MagnetURL != "" {
		t.Fatalf("got %+v", got)
	}
	if accept != "application/x-bittorrent" {
		t.Errorf("Accept = %q", accept)
	}
	if ua == "" || strings.HasPrefix(ua, "Go-http-client") {
		t.Errorf("User-Agent = %q, want the project agent", ua)
	}
}

func TestFetch_SizeCap(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("0123456789"))
	}))
	defer srv.Close()

	if _, err := torrentfetch.Fetch(context.Background(), srv.URL, torrentfetch.Options{Validate: allowAll, MaxBytes: 10}); err != nil {
		t.Fatalf("a body exactly at the cap must pass: %v", err)
	}
	_, err := torrentfetch.Fetch(context.Background(), srv.URL, torrentfetch.Options{Validate: allowAll, MaxBytes: 9})
	if err == nil || !strings.Contains(err.Error(), "response exceeds 9 bytes") {
		t.Fatalf("err = %v, want a size cap error", err)
	}
}

func TestFetch_EmptyBodyIsAnError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {}))
	defer srv.Close()
	_, err := torrentfetch.Fetch(context.Background(), srv.URL, torrentfetch.Options{Validate: allowAll})
	if err == nil || !strings.Contains(err.Error(), "empty torrent response") {
		t.Fatalf("err = %v", err)
	}
}

func TestFetch_HTTPErrorCarriesStatusAndBodySnippet(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte(" rate limited "))
	}))
	defer srv.Close()
	_, err := torrentfetch.Fetch(context.Background(), srv.URL, torrentfetch.Options{Validate: allowAll})
	if err == nil || !strings.Contains(err.Error(), "indexer returned HTTP 403: rate limited") {
		t.Fatalf("err = %v", err)
	}
}

func TestFetch_RedirectHandling(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/relative":
			http.Redirect(w, r, "/final", http.StatusFound)
		case "/final":
			_, _ = w.Write([]byte("payload"))
		case "/magnet":
			w.Header().Set("Location", "magnet:?xt=urn:btih:abc")
			w.WriteHeader(http.StatusFound)
		case "/magnet-space":
			w.Header().Set("Location", " MAGNET:?xt=urn:btih:abc")
			w.WriteHeader(http.StatusFound)
		case "/nolocation":
			w.WriteHeader(http.StatusFound)
		case "/ftp":
			w.Header().Set("Location", "ftp://example.invalid/x.torrent")
			w.WriteHeader(http.StatusFound)
		case "/loop":
			http.Redirect(w, r, r.URL.Path, http.StatusFound)
		}
	}))
	defer srv.Close()
	opt := torrentfetch.Options{Validate: allowAll}
	ctx := context.Background()

	got, err := torrentfetch.Fetch(ctx, srv.URL+"/relative", opt)
	if err != nil || string(got.Data) != "payload" {
		t.Fatalf("relative redirect: %v, %+v", err, got)
	}
	got, err = torrentfetch.Fetch(ctx, srv.URL+"/magnet", opt)
	if err != nil || got.MagnetURL != "magnet:?xt=urn:btih:abc" || got.Data != nil {
		t.Fatalf("magnet redirect: %v, %+v", err, got)
	}
	if got, err = torrentfetch.Fetch(ctx, srv.URL+"/magnet-space", opt); err != nil || got.MagnetURL == "" {
		t.Fatalf("padded magnet redirect: %v, %+v", err, got)
	}
	for path, want := range map[string]string{
		"/nolocation": "redirect without location",
		"/ftp":        `unsupported redirect scheme "ftp"`,
		"/loop":       "too many redirects",
	} {
		if _, err := torrentfetch.Fetch(ctx, srv.URL+path, opt); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%s: err = %v, want %q", path, err, want)
		}
	}
}

// Every hop is validated, not only the first URL, and a refused hop is never
// requested.
func TestFetch_ValidatesEveryHop(t *testing.T) {
	var hit atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/blocked" {
			hit.Add(1)
			return
		}
		http.Redirect(w, r, "/blocked", http.StatusFound)
	}))
	defer srv.Close()

	validate := func(u string) error {
		if strings.HasSuffix(u, "/blocked") {
			return errBlocked
		}
		return nil
	}
	_, err := torrentfetch.Fetch(context.Background(), srv.URL+"/start", torrentfetch.Options{Validate: validate})
	if err == nil || !strings.Contains(err.Error(), "blocked by test policy") {
		t.Fatalf("err = %v", err)
	}
	if hit.Load() != 0 {
		t.Fatal("the refused hop was requested")
	}
}

type blockedErr struct{}

func (blockedErr) Error() string { return "blocked by test policy" }

var errBlocked = blockedErr{}

func TestFetch_NilValidateRejectsNonHTTPScheme(t *testing.T) {
	_, err := torrentfetch.Fetch(context.Background(), "gopher://x/y", torrentfetch.Options{})
	if err == nil {
		t.Fatal("the default policy must reject a non http scheme")
	}
}

func TestFetch_Timeout(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-r.Context().Done():
		case <-time.After(5 * time.Second):
		}
	}))
	defer srv.Close()
	start := time.Now()
	_, err := torrentfetch.Fetch(context.Background(), srv.URL, torrentfetch.Options{Validate: allowAll, Timeout: 50 * time.Millisecond})
	if err == nil {
		t.Fatal("expected a timeout")
	}
	if time.Since(start) > 3*time.Second {
		t.Fatalf("took %s, the timeout did not apply", time.Since(start))
	}
}

func TestTimeoutIsOneMinute(t *testing.T) {
	if torrentfetch.Timeout != 60*time.Second {
		t.Fatalf("Timeout = %s", torrentfetch.Timeout)
	}
}

func TestFetch_RedactsSecretsInTransportErrors(t *testing.T) {
	dead := httptest.NewServer(http.NotFoundHandler())
	url := dead.URL
	dead.Close()
	_, err := torrentfetch.Fetch(context.Background(), url+"/x?passkey=SECRETVALUE", torrentfetch.Options{Validate: allowAll})
	if err == nil || strings.Contains(err.Error(), "SECRETVALUE") {
		t.Fatalf("err = %v, want an error without the passkey", err)
	}
}
