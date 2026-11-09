package qbittorrent

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

// addRecorder is a fake qBittorrent that records what reaches the add endpoint.
type addRecorder struct {
	srv  *httptest.Server
	mu   sync.Mutex
	urls []string
	adds int
}

func newAddRecorder(t *testing.T) *addRecorder {
	t.Helper()
	r := &addRecorder{}
	r.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		switch req.URL.Path {
		case "/api/v2/auth/login":
			_, _ = w.Write([]byte("Ok."))
		case "/api/v2/torrents/info":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`[]`))
		case "/api/v2/torrents/add":
			if strings.HasPrefix(req.Header.Get("Content-Type"), "multipart/") {
				_ = req.ParseMultipartForm(1 << 20)
			} else {
				_ = req.ParseForm()
			}
			r.mu.Lock()
			r.adds++
			r.urls = append(r.urls, req.FormValue("urls"))
			r.mu.Unlock()
			_, _ = w.Write([]byte("Ok."))
		default:
			_, _ = w.Write([]byte("Ok."))
		}
	}))
	t.Cleanup(r.srv.Close)
	oldFetch := torrentFetchTimeout
	torrentFetchTimeout = 500 * time.Millisecond
	t.Cleanup(func() { torrentFetchTimeout = oldFetch })
	old := hashPollTimeout
	hashPollTimeout = 50 * time.Millisecond
	t.Cleanup(func() { hashPollTimeout = old })
	return r
}

func (r *addRecorder) addCount() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.adds
}

func TestAddTorrent_LinkSchemes_OnlyHTTPHTTPSAndMagnetAreHandedOver(t *testing.T) {
	refused := []string{
		"http://10.0.0.5/latest/meta-data/",
		"HTTP://10.0.0.5/x",
		"Http://127.0.0.1:8080/x",
		"HTTPS://10.0.0.5/x",
		"ftp://10.0.0.5/x",
		"file:///config/x",
		"/config/x.torrent",
		"",
		"  ",
		"gopher://10.0.0.5/x",
		"://broken",
	}
	for _, raw := range refused {
		t.Run("refuse/"+raw, func(t *testing.T) {
			r := newAddRecorder(t)
			c := newTestClient(r.srv.URL, "u", "p")
			c.loggedIn = true
			if _, err := c.AddTorrent(context.Background(), raw, "", ""); err == nil {
				t.Fatalf("%q: expected an error", raw)
			}
			if n := r.addCount(); n != 0 {
				t.Fatalf("%q reached the client add endpoint %d time(s)", raw, n)
			}
		})
	}
}

func TestAddTorrent_LinkSchemes_MagnetAccepted(t *testing.T) {
	const hash = "0123456789abcdef0123456789abcdef01234567"
	for _, raw := range []string{
		"magnet:?xt=urn:btih:" + hash,
		"MAGNET:?xt=urn:btih:" + hash,
		"Magnet:?xt=urn:btih:" + hash + "&dn=a%20b",
		"magnet:?xt=urn:btih:" + hash + "&dn=a b",
		"magnet:?xt=urn:btih:" + hash + "&dn=a\u00a0b",
	} {
		t.Run(raw, func(t *testing.T) {
			r := newAddRecorder(t)
			c := newTestClient(r.srv.URL, "u", "p")
			c.loggedIn = true
			if _, err := c.AddTorrent(context.Background(), raw, "", ""); err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if r.addCount() != 1 || r.urls[0] != raw {
				t.Fatalf("magnet not passed through unchanged: adds=%d urls=%q", r.addCount(), r.urls)
			}
		})
	}
}

func TestAddTorrent_LinkSchemes_UppercaseSchemeUsesGuardedFetch(t *testing.T) {
	// Production validator: loopback is refused before anything is sent on.
	r := newAddRecorder(t)
	c := newTestClient(r.srv.URL, "u", "p")
	c.loggedIn = true
	_, err := c.AddTorrent(context.Background(), "HTTPS://127.0.0.1:1/x.torrent", "", "")
	if err == nil || !strings.Contains(err.Error(), "fetch torrent content") {
		t.Fatalf("want guarded fetch error, got %v", err)
	}
	if r.addCount() != 0 {
		t.Fatal("refused link reached the add endpoint")
	}

	// Relaxed validator: the fetch happens in Bindery and the bytes are uploaded.
	var hits int
	var mu sync.Mutex
	indexer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		mu.Lock()
		hits++
		mu.Unlock()
		_, _ = w.Write([]byte("d3:foo3:bare"))
	}))
	defer indexer.Close()
	c2 := newTestClient(r.srv.URL, "u", "p")
	c2.loggedIn = true
	allowTorrentFetch(c2)
	upper := "HTTP" + strings.TrimPrefix(indexer.URL, "http") + "/t"
	// The stub body is not a real torrent, so the hash lookup may report that
	// it could not be determined. What matters is who fetched and what was sent.
	_, err = c2.AddTorrent(context.Background(), upper, "", "")
	if err != nil && !strings.Contains(err.Error(), "hash could not be determined") {
		t.Fatalf("unexpected error: %v", err)
	}
	mu.Lock()
	defer mu.Unlock()
	if hits != 1 {
		t.Fatalf("indexer fetched %d times, want 1", hits)
	}
	if r.addCount() != 1 || r.urls[0] != "" {
		t.Fatalf("expected a file upload, got urls=%q", r.urls)
	}
}

func TestAddTorrent_LinkSchemes_MagnetWithControlCharactersRefused(t *testing.T) {
	const m = "magnet:?xt=urn:btih:0123456789abcdef0123456789abcdef01234567"
	for name, raw := range map[string]string{
		"newline http": m + "\nhttp://10.0.0.5/x",
		"crlf file":    m + "\r\nfile:///tmp/x",
		"short ftp":    "magnet:x\nftp://10.0.0.5/x",
		"cr":           m + "\rhttp://10.0.0.5/x",
		"nul":          m + "\x00http://10.0.0.5/x",
	} {
		t.Run(name, func(t *testing.T) {
			r := newAddRecorder(t)
			c := newTestClient(r.srv.URL, "u", "p")
			c.loggedIn = true
			if _, err := c.AddTorrent(context.Background(), raw, "", ""); err == nil {
				t.Fatal("expected an error")
			}
			if n := r.addCount(); n != 0 {
				t.Fatalf("reached the client add endpoint %d time(s)", n)
			}
		})
	}
}

func TestAddTorrent_LinkSchemes_PercentEncodedMagnetAccepted(t *testing.T) {
	raw := "magnet:?xt=urn:btih:0123456789abcdef0123456789abcdef01234567&dn=My%20Book%0A%E2%80%93%20Vol%201&tr=udp%3A%2F%2Ftracker.example%3A80%2Fannounce"
	r := newAddRecorder(t)
	c := newTestClient(r.srv.URL, "u", "p")
	c.loggedIn = true
	if _, err := c.AddTorrent(context.Background(), raw, "", ""); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if r.addCount() != 1 || r.urls[0] != raw {
		t.Fatalf("magnet not passed through unchanged: %q", r.urls)
	}
}

// A redirect Location cannot carry CR, LF or NUL through Go's HTTP client, so
// the redirect magnet path is covered by the shared check directly.
func TestValidMagnet_RefusesLineBreaksAndNULOnly(t *testing.T) {
	for raw, want := range map[string]bool{
		"magnet:?xt=urn:btih:0123":             true,
		"magnet:?xt=urn:btih:0123&dn=a b":      true,
		"magnet:?xt=urn:btih:0123&dn=a\u00a0b": true,
		"magnet:?xt=urn:btih:0123\nx":          false,
		"magnet:?xt=urn:btih:0123\rx":          false,
		"magnet:?xt=urn:btih:0123\x00x":        false,
	} {
		if got := validMagnet(raw); got != want {
			t.Errorf("validMagnet(%q) = %v; want %v", raw, got, want)
		}
	}
}
