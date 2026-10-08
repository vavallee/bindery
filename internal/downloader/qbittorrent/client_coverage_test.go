package qbittorrent

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

func TestAuthError_Messages(t *testing.T) {
	cases := []struct {
		err  AuthError
		want string
	}{
		{AuthError{Status: http.StatusForbidden, Body: "Unauthorized"}, "host-header validation"},
		{AuthError{Status: http.StatusInternalServerError, Body: "oops"}, "HTTP 500): oops"},
		{AuthError{Status: http.StatusOK, Body: "weird"}, "qBittorrent auth failed: weird"},
	}
	for _, tc := range cases {
		if got := tc.err.Error(); !strings.Contains(got, tc.want) {
			t.Errorf("AuthError%+v = %q, want it to contain %q", tc.err, got, tc.want)
		}
	}
}

// rejectingQbit answers every login with qBittorrent's credential rejection
// ("Fails.") and records whether any other endpoint was reached.
func rejectingQbit(t *testing.T) (*Client, *int) {
	t.Helper()
	other := 0
	var mu sync.Mutex
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v2/auth/login" {
			_, _ = io.WriteString(w, "Fails.")
			return
		}
		mu.Lock()
		other++
		mu.Unlock()
		_, _ = io.WriteString(w, "[]")
	}))
	t.Cleanup(srv.Close)
	return newTestClient(srv.URL, "admin", "wrong"), &other
}

// Every mutating call logs in first; a rejected login must stop the call
// before it reaches the endpoint and surface as an *AuthError.
func TestLoginFailureStopsEveryCall(t *testing.T) {
	ctx := context.Background()
	calls := map[string]func(*Client) error{
		"SetShareLimits":    func(c *Client) error { return c.SetShareLimits(ctx, "h", 1.5) },
		"AddTorrent":        func(c *Client) error { _, err := c.AddTorrent(ctx, "magnet:?xt=urn:btih:abc", "", ""); return err },
		"DeleteTorrent":     func(c *Client) error { return c.DeleteTorrent(ctx, "h", false) },
		"setCategory":       func(c *Client) error { return c.setCategory(ctx, "h", "books") },
		"setAutoManagement": func(c *Client) error { return c.setAutoManagement(ctx, "h", true) },
		"GetTorrents":       func(c *Client) error { _, err := c.GetTorrents(ctx, ""); return err },
	}
	for name, call := range calls {
		t.Run(name, func(t *testing.T) {
			c, other := rejectingQbit(t)
			err := call(c)
			var ae *AuthError
			if !errors.As(err, &ae) {
				t.Fatalf("err = %v, want *AuthError", err)
			}
			if *other != 0 {
				t.Errorf("%d request(s) reached the API after a rejected login", *other)
			}
		})
	}
}

// failingAfterLogin logs in successfully (v5 204) and then fails every other
// request at the transport, standing in for qBittorrent dying mid-session.
func failingAfterLogin() *Client {
	return newTransportClient(roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if r.URL.Path == "/api/v2/auth/login" {
			return &http.Response{StatusCode: http.StatusNoContent, Body: http.NoBody, Request: r}, nil
		}
		return nil, errors.New("connection reset by peer")
	}))
}

func TestTransportErrorsAreWrapped(t *testing.T) {
	ctx := context.Background()
	cases := []struct {
		name string
		call func(*Client) error
		want string
	}{
		{"setShareLimits", func(c *Client) error { return c.SetShareLimits(ctx, "h", 2) }, "setShareLimits: "},
		{"add", func(c *Client) error { _, err := c.AddTorrent(ctx, "magnet:?xt=urn:btih:abc", "", ""); return err }, "add torrent: "},
		{"delete", func(c *Client) error { return c.DeleteTorrent(ctx, "h", true) }, "delete torrent: "},
		{"setCategory", func(c *Client) error { return c.setCategory(ctx, "h", "b") }, "setCategory: "},
		{"setAutoManagement", func(c *Client) error { return c.setAutoManagement(ctx, "h", true) }, "setAutoManagement: "},
		{"get", func(c *Client) error { _, err := c.GetDefaultSavePath(ctx); return err }, "GET /api/v2/app/defaultSavePath: "},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.call(failingAfterLogin())
			if err == nil || !strings.Contains(err.Error(), tc.want) || !strings.Contains(err.Error(), "connection reset") {
				t.Fatalf("err = %v, want prefix %q and the cause", err, tc.want)
			}
		})
	}
}

func TestBuildRequestErrors(t *testing.T) {
	ctx := context.Background()
	c := New("h", 1, "u", "p", "", false)
	c.baseURL = "http://bad host"

	if err := c.Login(ctx); err == nil || !strings.Contains(err.Error(), "build login request") {
		t.Errorf("Login: %v", err)
	}

	c.loggedIn = true
	cases := map[string]func() error{
		"build setShareLimits request":    func() error { return c.SetShareLimits(ctx, "h", 1) },
		"build add request":               func() error { _, err := c.AddTorrent(ctx, "magnet:?xt=urn:btih:abc", "", ""); return err },
		"build delete request":            func() error { return c.DeleteTorrent(ctx, "h", false) },
		"build setCategory request":       func() error { return c.setCategory(ctx, "h", "b") },
		"build setAutoManagement request": func() error { return c.setAutoManagement(ctx, "h", true) },
		"build request":                   func() error { _, err := c.GetCategories(ctx); return err },
		"build torrent fetch request":     func() error { _, err := c.fetchTorrentContent(ctx, "http://bad host/x"); return err },
	}
	c.validateTorrentURL = func(string) error { return nil }
	for want, call := range cases {
		if err := call(); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("err = %v, want %q", err, want)
		}
	}
}

// qbitScript is a configurable fake qBittorrent: each endpoint answers from a
// handler, with sensible defaults (login ok, empty torrent list).
type qbitScript struct {
	*httptest.Server
	mu       sync.Mutex
	handlers map[string]http.HandlerFunc
	hits     map[string]int
}

func newQbitScript(t *testing.T, handlers map[string]http.HandlerFunc) *qbitScript {
	t.Helper()
	q := &qbitScript{handlers: handlers, hits: map[string]int{}}
	q.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		q.mu.Lock()
		q.hits[r.URL.Path]++
		h, ok := q.handlers[r.URL.Path]
		q.mu.Unlock()
		switch {
		case ok:
			h(w, r)
		case r.URL.Path == "/api/v2/auth/login":
			_, _ = io.WriteString(w, "Ok.")
		case r.URL.Path == "/api/v2/torrents/info":
			_, _ = io.WriteString(w, "[]")
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(q.Close)
	return q
}

func (q *qbitScript) count(path string) int {
	q.mu.Lock()
	defer q.mu.Unlock()
	return q.hits[path]
}

func status(code int) http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(code) }
}

func TestAddTorrent_DuplicateWithoutDeterminableHash(t *testing.T) {
	q := newQbitScript(t, map[string]http.HandlerFunc{
		"/api/v2/torrents/add": func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusConflict)
			_, _ = io.WriteString(w, "Torrent already exists")
		},
	})
	c := newTestClient(q.URL, "u", "p")
	_, err := c.AddTorrent(context.Background(), "magnet:?dn=no-hash-here", "books", "")
	if err == nil || !strings.Contains(err.Error(), "hash could not be determined") {
		t.Fatalf("err = %v", err)
	}
}

// On a duplicate add with a category, re-categorising is best-effort: both
// follow-up calls failing must still return the existing hash.
func TestAddTorrent_DuplicateRecategoriseFailuresAreBestEffort(t *testing.T) {
	q := newQbitScript(t, map[string]http.HandlerFunc{
		"/api/v2/torrents/add":               status(http.StatusConflict),
		"/api/v2/torrents/setCategory":       status(http.StatusConflict),
		"/api/v2/torrents/setAutoManagement": status(http.StatusInternalServerError),
	})
	c := newTestClient(q.URL, "u", "p")
	hash, err := c.AddTorrent(context.Background(), "magnet:?xt=urn:btih:ABCDEF0123456789ABCDEF0123456789ABCDEF01", "books", "")
	if err != nil || hash != "abcdef0123456789abcdef0123456789abcdef01" {
		t.Fatalf("AddTorrent = %q, %v", hash, err)
	}
	if q.count("/api/v2/torrents/setCategory") != 1 || q.count("/api/v2/torrents/setAutoManagement") != 1 {
		t.Errorf("follow-ups: setCategory=%d setAutoManagement=%d, want 1 each",
			q.count("/api/v2/torrents/setCategory"), q.count("/api/v2/torrents/setAutoManagement"))
	}
}

func TestAddTorrent_DuplicateWithoutCategorySkipsRecategorise(t *testing.T) {
	q := newQbitScript(t, map[string]http.HandlerFunc{"/api/v2/torrents/add": status(http.StatusConflict)})
	c := newTestClient(q.URL, "u", "p")
	hash, err := c.AddTorrent(context.Background(), "magnet:?xt=urn:btih:abcdef0123456789abcdef0123456789abcdef01", "", "")
	if err != nil || hash != "abcdef0123456789abcdef0123456789abcdef01" {
		t.Fatalf("AddTorrent = %q, %v", hash, err)
	}
	if q.count("/api/v2/torrents/setCategory") != 0 {
		t.Error("no category was asked for, so none may be set")
	}
}

// After an accepted .torrent upload with no hash in the reply, a failing
// torrent list must fail the add rather than report a hashless success.
func TestAddTorrent_HashLookupListFails(t *testing.T) {
	infoHits := 0
	q := newQbitScript(t, map[string]http.HandlerFunc{
		"/api/v2/torrents/add": func(w http.ResponseWriter, _ *http.Request) { _, _ = io.WriteString(w, "Ok.") },
		"/api/v2/torrents/info": func(w http.ResponseWriter, _ *http.Request) {
			infoHits++
			if infoHits == 1 {
				_, _ = io.WriteString(w, "[]") // before-snapshot
				return
			}
			http.Error(w, "boom", http.StatusInternalServerError)
		},
	})
	indexer := newFakeIndexer(t)
	defer indexer.Close()
	c := newTestClient(q.URL, "u", "p")
	allowTorrentFetch(c)
	_, err := c.AddTorrent(context.Background(), indexer.URL+"/torrent", "", "")
	if err == nil || !strings.Contains(err.Error(), "hash lookup failed") {
		t.Fatalf("err = %v", err)
	}
}

func TestAddTorrent_RecoveredTorrentRecategoriseIsBestEffort(t *testing.T) {
	infoHits := 0
	q := newQbitScript(t, map[string]http.HandlerFunc{
		"/api/v2/torrents/add": func(w http.ResponseWriter, _ *http.Request) { _, _ = io.WriteString(w, "Ok.") },
		"/api/v2/torrents/info": func(w http.ResponseWriter, _ *http.Request) {
			infoHits++
			if infoHits == 1 {
				_, _ = io.WriteString(w, `[{"hash":"OLD","added_on":1}]`)
				return
			}
			_, _ = io.WriteString(w, `[{"hash":"OLD","added_on":1},{"hash":"NEW1","added_on":5},{"hash":"NEW2","added_on":9}]`)
		},
		"/api/v2/torrents/setCategory":       status(http.StatusConflict),
		"/api/v2/torrents/setAutoManagement": status(http.StatusInternalServerError),
	})
	indexer := newFakeIndexer(t)
	defer indexer.Close()
	c := newTestClient(q.URL, "u", "p")
	allowTorrentFetch(c)
	hash, err := c.AddTorrent(context.Background(), indexer.URL+"/torrent", "books", "")
	if err != nil || hash != "new2" {
		t.Fatalf("AddTorrent = %q, %v; want the newest unseen torrent, lower-cased", hash, err)
	}
	if q.count("/api/v2/torrents/setAutoManagement") != 1 {
		t.Error("auto-management must still be attempted after a failed setCategory")
	}
}

func TestAddTorrent_HashLookupTimeoutListsHashes(t *testing.T) {
	orig := hashPollTimeout
	hashPollTimeout = 0 // one poll, then give up: no sleeping
	t.Cleanup(func() { hashPollTimeout = orig })
	q := newQbitScript(t, map[string]http.HandlerFunc{
		"/api/v2/torrents/add":  func(w http.ResponseWriter, _ *http.Request) { _, _ = io.WriteString(w, "Ok.") },
		"/api/v2/torrents/info": func(w http.ResponseWriter, _ *http.Request) { _, _ = io.WriteString(w, `[{"hash":"SAME"}]`) },
	})
	indexer := newFakeIndexer(t)
	defer indexer.Close()
	c := newTestClient(q.URL, "u", "p")
	allowTorrentFetch(c)
	_, err := c.AddTorrent(context.Background(), indexer.URL+"/torrent", "", "")
	if err == nil || !strings.Contains(err.Error(), "hash could not be determined") {
		t.Fatalf("err = %v", err)
	}
	if n := q.count("/api/v2/torrents/info"); n != 2 {
		t.Errorf("torrents/info hit %d times, want before-snapshot plus one poll", n)
	}
}

func TestAddTorrent_CancelledWhilePolling(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	infoHits := 0
	q := newQbitScript(t, map[string]http.HandlerFunc{
		"/api/v2/torrents/add": func(w http.ResponseWriter, _ *http.Request) { _, _ = io.WriteString(w, "Ok.") },
		"/api/v2/torrents/info": func(w http.ResponseWriter, _ *http.Request) {
			infoHits++
			if infoHits == 2 {
				cancel() // the first poll found nothing; the wait must end on ctx
			}
			_, _ = io.WriteString(w, "[]")
		},
	})
	indexer := newFakeIndexer(t)
	defer indexer.Close()
	c := newTestClient(q.URL, "u", "p")
	allowTorrentFetch(c)
	_, err := c.AddTorrent(ctx, indexer.URL+"/torrent", "", "")
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
}

func TestFetchTorrentContent_Paths(t *testing.T) {
	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/relative":
			w.Header().Set("Location", "/final")
			w.WriteHeader(http.StatusMovedPermanently)
		case "/final":
			_, _ = io.WriteString(w, fakeTorrentContent)
		case "/nolocation":
			w.WriteHeader(http.StatusFound)
		case "/ftp":
			w.Header().Set("Location", "ftp://files.example/x.torrent")
			w.WriteHeader(http.StatusFound)
		case "/loop":
			http.Redirect(w, r, srv.URL+"/loop", http.StatusFound)
		case "/empty":
			w.WriteHeader(http.StatusOK)
		case "/forbidden":
			w.WriteHeader(http.StatusForbidden)
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	c := newTestClient(srv.URL, "", "")
	allowTorrentFetch(c)
	ctx := context.Background()

	got, err := c.fetchTorrentContent(ctx, srv.URL+"/relative")
	if err != nil || string(got.data) != fakeTorrentContent {
		t.Fatalf("relative redirect: %v, %+v", err, got)
	}
	for path, want := range map[string]string{
		"/nolocation": "redirect without location",
		"/ftp":        `unsupported redirect scheme "ftp"`,
		"/loop":       "too many redirects",
		"/empty":      "empty torrent response",
		"/forbidden":  "indexer returned HTTP 403",
	} {
		if _, err := c.fetchTorrentContent(ctx, srv.URL+path); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%s: err = %v, want %q", path, err, want)
		}
	}

	dead := httptest.NewServer(http.NotFoundHandler())
	deadURL := dead.URL
	dead.Close()
	_, err = c.fetchTorrentContent(ctx, deadURL+"/x?passkey=SECRET")
	if err == nil || strings.Contains(err.Error(), "SECRET") {
		t.Errorf("transport error = %v, want an error without the passkey", err)
	}

	c.validateTorrentURL = nil
	if err := c.validateTorrentFetchURL("gopher://x"); err == nil {
		t.Error("default policy must reject a non-HTTP scheme")
	}
}

func TestGet_RetryPaths(t *testing.T) {
	t.Run("re-login rejected", func(t *testing.T) {
		q := newQbitScript(t, map[string]http.HandlerFunc{
			"/api/v2/auth/login":  func(w http.ResponseWriter, _ *http.Request) { _, _ = io.WriteString(w, "Fails.") },
			"/api/v2/app/version": status(http.StatusForbidden),
		})
		c := newTestClient(q.URL, "u", "p")
		c.loggedIn = true
		err := c.Test(context.Background())
		var ae *AuthError
		if !errors.As(err, &ae) || !strings.Contains(err.Error(), "connected to qBittorrent") {
			t.Fatalf("err = %v, want the auth failure without a reachability hint", err)
		}
	})
	t.Run("retry still non-200", func(t *testing.T) {
		q := newQbitScript(t, map[string]http.HandlerFunc{
			"/api/v2/app/defaultSavePath": func(w http.ResponseWriter, _ *http.Request) {
				http.Error(w, "Forbidden", http.StatusForbidden)
			},
		})
		c := newTestClient(q.URL, "u", "p")
		c.loggedIn = true
		_, err := c.GetDefaultSavePath(context.Background())
		if err == nil || !strings.Contains(err.Error(), "HTTP 403") {
			t.Fatalf("err = %v", err)
		}
		if n := q.count("/api/v2/auth/login"); n != 1 {
			t.Errorf("re-login attempts = %d, want exactly one", n)
		}
	})
	t.Run("retry transport error", func(t *testing.T) {
		versionHits := 0
		c := newTransportClient(roundTripFunc(func(r *http.Request) (*http.Response, error) {
			switch r.URL.Path {
			case "/api/v2/auth/login":
				return &http.Response{StatusCode: http.StatusNoContent, Body: http.NoBody, Request: r}, nil
			default:
				versionHits++
				if versionHits == 1 {
					return &http.Response{StatusCode: http.StatusForbidden, Body: http.NoBody, Request: r}, nil
				}
				return nil, errors.New("connection reset")
			}
		}))
		c.loggedIn = true
		_, err := c.GetDefaultSavePath(context.Background())
		if err == nil || !strings.Contains(err.Error(), "(retry)") {
			t.Fatalf("err = %v", err)
		}
	})
}

func TestFiles_SkipsUnnamedEntries(t *testing.T) {
	q := newQbitScript(t, map[string]http.HandlerFunc{
		"/api/v2/torrents/files": func(w http.ResponseWriter, _ *http.Request) {
			_, _ = io.WriteString(w, `[{"name":"","size":1},{"name":"a\\b.epub","size":2}]`)
		},
	})
	files, err := newTestClient(q.URL, "u", "p").Files(context.Background(), "h")
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != 1 || files[0].Name != "a/b.epub" || files[0].Size != 2 {
		t.Fatalf("files = %+v", files)
	}
}

func TestFilesAndCategories_DecodeErrors(t *testing.T) {
	q := newQbitScript(t, map[string]http.HandlerFunc{
		"/api/v2/torrents/files":      func(w http.ResponseWriter, _ *http.Request) { _, _ = io.WriteString(w, `{"not":"a list"}`) },
		"/api/v2/torrents/categories": func(w http.ResponseWriter, _ *http.Request) { _, _ = io.WriteString(w, `{"books":"oops"}`) },
		"/api/v2/torrents/info":       func(w http.ResponseWriter, _ *http.Request) { http.Error(w, "nope", http.StatusBadGateway) },
	})
	c := newTestClient(q.URL, "u", "p")
	ctx := context.Background()
	if _, err := c.Files(ctx, "h"); err == nil || !strings.Contains(err.Error(), "decode torrent files") {
		t.Errorf("Files: %v", err)
	}
	if _, err := c.GetCategories(ctx); err == nil || !strings.Contains(err.Error(), "decode categories") {
		t.Errorf("GetCategories: %v", err)
	}
	if _, err := c.GetTorrents(ctx, ""); err == nil || !strings.Contains(err.Error(), "HTTP 502") {
		t.Errorf("GetTorrents: %v", err)
	}
}

func TestCategory_NonStringSavePathIgnored(t *testing.T) {
	var cat Category
	if err := json.Unmarshal([]byte(`{"name":"b","savePath":42,"save_path":"D:\\Books"}`), &cat); err != nil {
		t.Fatal(err)
	}
	if cat.SavePath != "D:/Books" {
		t.Errorf("SavePath = %q, want the snake_case fallback, slash-normalised", cat.SavePath)
	}
}

func TestDecodeJSON_LongNonJSONBodyIsClipped(t *testing.T) {
	body := []byte("Z" + strings.Repeat("abc ", 100))
	var out []Torrent
	err := decodeJSON("torrents", body, &out)
	if err == nil {
		t.Fatal("want an error")
	}
	if len(err.Error()) > 200 {
		t.Errorf("error quotes too much of the body (%d bytes): %v", len(err.Error()), err)
	}
}
