package qbittorrent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"
)

// fakeTorrentContent is minimal bencoded content used across tests.
const fakeTorrentContent = "d8:announce27:http://tracker.example.com/ae"

// newFakeIndexer returns a test server that serves fakeTorrentContent at /torrent.
func newFakeIndexer(t *testing.T) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/torrent" {
			_, _ = w.Write([]byte(fakeTorrentContent))
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
}

// TestAddTorrent_ConcurrentUniqueHashes is a regression test for Bug 2.
// When AddTorrent is called concurrently (e.g. during a bulk grab), each call
// must return its own unique torrent hash. Without serialisation, both
// goroutines snapshot beforeSet while it is empty, both torrents are submitted,
// and both goroutines resolve to the same "newest" torrent (highest AddedOn) —
// leaving one download record permanently mapped to the wrong hash.
func TestAddTorrent_ConcurrentUniqueHashes(t *testing.T) {
	const (
		hashA = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
		hashB = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	)

	var mu sync.Mutex
	addedCount := 0

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v2/auth/login":
			_, _ = w.Write([]byte("Ok."))
		case "/api/v2/torrents/info":
			mu.Lock()
			count := addedCount
			mu.Unlock()

			var torrents []Torrent
			if count >= 1 {
				torrents = append(torrents, Torrent{Hash: hashA, Name: "Book A", AddedOn: 1000})
			}
			if count >= 2 {
				torrents = append(torrents, Torrent{Hash: hashB, Name: "Book B", AddedOn: 2000})
			}
			body, _ := json.Marshal(torrents)
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write(body)
		case "/api/v2/torrents/add":
			// Sleep long enough to guarantee both goroutines complete their
			// initial "before" GetTorrents snapshots before any add is
			// acknowledged, reliably opening the race window.
			time.Sleep(20 * time.Millisecond)
			mu.Lock()
			addedCount++
			mu.Unlock()
			_, _ = w.Write([]byte("Ok."))
		}
	}))
	defer srv.Close()

	indexer := newFakeIndexer(t)
	defer indexer.Close()

	c := newTestClient(srv.URL, "admin", "pass")
	allowTorrentFetch(c)
	c.loggedIn = true

	var wg sync.WaitGroup
	results := make([]string, 2)
	errs := make([]error, 2)

	for i := range results {
		i := i
		wg.Add(1)
		go func() {
			defer wg.Done()
			results[i], errs[i] = c.AddTorrent(
				context.Background(),
				indexer.URL+"/torrent",
				"", "",
			)
		}()
	}
	wg.Wait()

	for i, err := range errs {
		if err != nil {
			t.Fatalf("goroutine %d: %v", i, err)
		}
	}

	if results[0] == results[1] {
		t.Errorf("bug 2 race: both concurrent AddTorrent calls returned %q; each must return a unique hash", results[0])
	}
	got := map[string]bool{results[0]: true, results[1]: true}
	if !got[hashA] || !got[hashB] {
		t.Errorf("want one goroutine to get %q and the other %q, got %q and %q", hashA, hashB, results[0], results[1])
	}
}

// TestAddTorrent_V5JSONResponse is a regression test for #690.
// qBittorrent 5.x returns a JSON body from POST /api/v2/torrents/add instead
// of the plaintext "Ok." that v4 returned. The legacy check rejected the JSON
// even though the add succeeded, causing every grab to surface as failed.
// The fix accepts either shape: plaintext "Ok." (v4) or JSON with success_count
// >= 1 or non-empty added_torrent_ids (v5). When v5 returns the hash inline,
// we use it directly and skip the hash-poll.
func TestAddTorrent_V5JSONResponse(t *testing.T) {
	const v5Hash = "cccccccccccccccccccccccccccccccccccccccc"

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v2/auth/login":
			_, _ = w.Write([]byte("Ok."))
		case "/api/v2/torrents/add":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"added_torrent_ids":["` + v5Hash + `"],"failure_count":0,"pending_count":0,"success_count":1}`))
		}
	}))
	defer srv.Close()

	indexer := newFakeIndexer(t)
	defer indexer.Close()

	c := newTestClient(srv.URL, "admin", "pass")
	allowTorrentFetch(c)
	c.loggedIn = true

	got, err := c.AddTorrent(context.Background(), indexer.URL+"/torrent", "", "")
	if err != nil {
		t.Fatalf("unexpected error on v5 JSON response: %v", err)
	}
	if got != v5Hash {
		t.Errorf("hash from added_torrent_ids: want %q, got %q", v5Hash, got)
	}
}

// TestAddTorrent_V5JSONResponse_FailureCount verifies that a v5 JSON body with
// success_count=0 and an empty added_torrent_ids is correctly treated as a
// failure, not accepted on the basis of being JSON.
func TestAddTorrent_V5JSONResponse_FailureCount(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v2/auth/login":
			_, _ = w.Write([]byte("Ok."))
		case "/api/v2/torrents/add":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"added_torrent_ids":[],"failure_count":1,"pending_count":0,"success_count":0}`))
		}
	}))
	defer srv.Close()

	indexer := newFakeIndexer(t)
	defer indexer.Close()

	c := newTestClient(srv.URL, "admin", "pass")
	allowTorrentFetch(c)
	c.loggedIn = true

	_, err := c.AddTorrent(context.Background(), indexer.URL+"/torrent", "", "")
	if err == nil {
		t.Fatal("expected failure for success_count=0 / empty added_torrent_ids, got nil")
	}
}

// logCatcher captures slog records for test assertions.
type logCatcher struct {
	mu      sync.Mutex
	records []slog.Record
}

func (lc *logCatcher) Enabled(_ context.Context, _ slog.Level) bool { return true }
func (lc *logCatcher) Handle(_ context.Context, r slog.Record) error {
	lc.mu.Lock()
	lc.records = append(lc.records, r.Clone())
	lc.mu.Unlock()
	return nil
}
func (lc *logCatcher) WithAttrs(_ []slog.Attr) slog.Handler { return lc }
func (lc *logCatcher) WithGroup(_ string) slog.Handler      { return lc }

func (lc *logCatcher) Records() []slog.Record {
	lc.mu.Lock()
	defer lc.mu.Unlock()
	return lc.records
}

// newTestClient creates a Client pointing at the given test server URL.
func newTestClient(serverURL, username, password string) *Client {
	c := New("localhost", 8080, username, password, "", false)
	c.baseURL = serverURL
	return c
}

func allowTorrentFetch(c *Client) {
	c.validateTorrentURL = func(string) error { return nil }
}

func TestNew(t *testing.T) {
	c := New("myhost", 8080, "admin", "secret", "", false)
	if c.baseURL != "http://myhost:8080" {
		t.Errorf("baseURL: want %q, got %q", "http://myhost:8080", c.baseURL)
	}
	if c.username != "admin" || c.password != "secret" {
		t.Error("credentials not stored correctly")
	}
	if c.loggedIn {
		t.Error("should not be logged in on construction")
	}

	cs := New("securehost", 443, "u", "p", "", true)
	if cs.baseURL != "https://securehost:443" {
		t.Errorf("SSL baseURL: got %q", cs.baseURL)
	}
}

func TestLogin_Success(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v2/auth/login" {
			t.Errorf("unexpected path: %s", r.URL.Path)
		}
		if err := r.ParseForm(); err != nil {
			t.Fatal(err)
		}
		if r.FormValue("username") != "admin" || r.FormValue("password") != "pass" {
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte("Fails."))
			return
		}
		http.SetCookie(w, &http.Cookie{Name: "SID", Value: "test-sid"})
		_, _ = w.Write([]byte("Ok."))
	}))
	defer srv.Close()

	c := newTestClient(srv.URL, "admin", "pass")
	if err := c.Login(context.Background()); err != nil {
		t.Fatalf("Login: %v", err)
	}
	if !c.loggedIn {
		t.Error("loggedIn should be true after successful login")
	}
}

func TestLogin_Fails(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("Fails."))
	}))
	defer srv.Close()

	c := newTestClient(srv.URL, "bad", "creds")
	if err := c.Login(context.Background()); err == nil {
		t.Fatal("expected Login to return error on 'Fails.' body")
	}
	if c.loggedIn {
		t.Error("loggedIn should remain false after failed login")
	}
}

func TestLogin_BadStatus(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte("internal error"))
	}))
	defer srv.Close()

	c := newTestClient(srv.URL, "admin", "pass")
	if err := c.Login(context.Background()); err == nil {
		t.Fatal("expected error on 500 response")
	}
}

// TestLogin_V5_NoContent verifies that qBittorrent v5.x's `204 No Content`
// login response is treated as a success (v4.x returned `200 OK` + "Ok.").
func TestLogin_V5_NoContent(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v2/auth/login" {
			t.Errorf("unexpected path: %s", r.URL.Path)
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()

	c := newTestClient(srv.URL, "admin", "pass")
	if err := c.Login(context.Background()); err != nil {
		t.Fatalf("Login: %v", err)
	}
	if !c.loggedIn {
		t.Error("loggedIn should be true after 204 response")
	}
}

// TestLogin_SendsCSRFHeaders verifies that Origin and Referer headers are
// sent on every login request, as required by qBittorrent v5.x CSRF checks.
// v4.x ignores these headers, so setting them is safe across versions.
func TestLogin_SendsCSRFHeaders(t *testing.T) {
	var gotOrigin, gotReferer string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v2/auth/login" {
			gotOrigin = r.Header.Get("Origin")
			gotReferer = r.Header.Get("Referer")
			_, _ = w.Write([]byte("Ok."))
		}
	}))
	defer srv.Close()

	c := newTestClient(srv.URL, "admin", "pass")
	if err := c.Login(context.Background()); err != nil {
		t.Fatalf("Login: %v", err)
	}
	if gotOrigin != srv.URL {
		t.Errorf("Origin: want %q, got %q", srv.URL, gotOrigin)
	}
	if gotReferer != srv.URL {
		t.Errorf("Referer: want %q, got %q", srv.URL, gotReferer)
	}
}

// TestLogin_AuthError_BadCreds covers the "200 + Fails." path. Bindery
// should return an *AuthError that surfaces the credentials hint.
func TestLogin_AuthError_BadCreds(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("Fails."))
	}))
	defer srv.Close()

	c := newTestClient(srv.URL, "bad", "creds")
	err := c.Login(context.Background())
	if err == nil {
		t.Fatal("expected error")
		return
	}
	var authErr *AuthError
	if !errors.As(err, &authErr) {
		t.Fatalf("expected *AuthError, got %T: %v", err, err)
	}
	if authErr.Status != http.StatusOK || authErr.Body != "Fails." {
		t.Errorf("AuthError fields: got status=%d body=%q", authErr.Status, authErr.Body)
	}
	if !strings.Contains(err.Error(), "credentials rejected") {
		t.Errorf("expected credentials hint, got %q", err.Error())
	}
}

// TestLogin_AuthError_BanEmpty403 covers the qBit IP-ban shape: HTTP 403
// with an empty body. Pre-fix this surfaced as "qBittorrent login failed: "
// (nothing useful). Now should explain IP-ban + how to clear it.
func TestLogin_AuthError_BanEmpty403(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
	}))
	defer srv.Close()

	c := newTestClient(srv.URL, "admin", "pass")
	err := c.Login(context.Background())
	if err == nil {
		t.Fatal("expected error")
		return
	}
	var authErr *AuthError
	if !errors.As(err, &authErr) {
		t.Fatalf("expected *AuthError, got %T: %v", err, err)
	}
	if authErr.Status != http.StatusForbidden || authErr.Body != "" {
		t.Errorf("AuthError fields: got status=%d body=%q", authErr.Status, authErr.Body)
	}
	if !strings.Contains(err.Error(), "IP is most likely banned") {
		t.Errorf("expected IP-ban hint, got %q", err.Error())
	}
}

// TestTest_AuthErrorDoesNotMisdirect proves Test() no longer wraps auth
// failures with the "could not reach + use container name" hint that only
// makes sense for actual transport failures.
func TestTest_AuthErrorDoesNotMisdirect(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v2/auth/login" {
			w.WriteHeader(http.StatusForbidden) // simulate IP ban
			return
		}
		t.Errorf("unexpected path: %s", r.URL.Path)
	}))
	defer srv.Close()

	c := newTestClient(srv.URL, "admin", "pass")
	err := c.Test(context.Background())
	if err == nil {
		t.Fatal("expected error")
		return
	}
	msg := err.Error()
	if strings.Contains(msg, "could not reach") {
		t.Errorf("auth failure must not be reported as 'could not reach': %q", msg)
	}
	if !strings.Contains(msg, "connected to qBittorrent") {
		t.Errorf("expected 'connected to qBittorrent at ... but ...' wording: %q", msg)
	}
	if !strings.Contains(msg, "IP is most likely banned") {
		t.Errorf("expected the underlying AuthError hint to propagate: %q", msg)
	}
}

func TestTest_Success(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v2/auth/login":
			_, _ = w.Write([]byte("Ok."))
		case "/api/v2/app/version":
			_, _ = w.Write([]byte("5.0.0"))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()

	c := newTestClient(srv.URL, "admin", "pass")
	if err := c.Test(context.Background()); err != nil {
		t.Fatalf("Test: %v", err)
	}
}

func TestTest_HTTPError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v2/auth/login":
			_, _ = w.Write([]byte("Ok."))
		default:
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = w.Write([]byte("server error"))
		}
	}))
	defer srv.Close()

	c := newTestClient(srv.URL, "admin", "pass")
	if err := c.Test(context.Background()); err == nil {
		t.Fatal("expected Test to fail on 500")
	}
}

func TestAddTorrent_Success(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v2/auth/login":
			_, _ = w.Write([]byte("Ok."))
		case "/api/v2/torrents/add":
			if err := r.ParseForm(); err != nil {
				t.Fatal(err)
			}
			if r.FormValue("urls") == "" {
				t.Error("expected urls in form body")
			}
			_, _ = w.Write([]byte("Ok."))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()

	c := newTestClient(srv.URL, "admin", "pass")
	if _, err := c.AddTorrent(context.Background(), "magnet:?xt=urn:btih:abc123", "", ""); err != nil {
		t.Fatalf("AddTorrent: %v", err)
	}
}

func TestAddTorrent_WithCategoryAndSavePath(t *testing.T) {
	var gotCategory, gotSavePath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v2/auth/login":
			_, _ = w.Write([]byte("Ok."))
		case "/api/v2/torrents/add":
			_ = r.ParseForm()
			gotCategory = r.FormValue("category")
			gotSavePath = r.FormValue("savepath")
			_, _ = w.Write([]byte("Ok."))
		}
	}))
	defer srv.Close()

	c := newTestClient(srv.URL, "admin", "pass")
	if _, err := c.AddTorrent(context.Background(), "magnet:?xt=urn:btih:abc", "books", "/downloads"); err != nil {
		t.Fatalf("AddTorrent: %v", err)
	}
	if gotCategory != "books" {
		t.Errorf("category: want 'books', got %q", gotCategory)
	}
	if gotSavePath != "/downloads" {
		t.Errorf("savepath: want '/downloads', got %q", gotSavePath)
	}
}

func TestGetCategories_NormalizesSavePathKeys(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v2/auth/login":
			_, _ = w.Write([]byte("Ok."))
		case "/api/v2/torrents/categories":
			_, _ = w.Write([]byte(`{
				"books":{"name":"books","savePath":"/media/books"},
				"audiobooks":{"name":"audiobooks","save_path":"/media/audio"}
			}`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()

	c := newTestClient(srv.URL, "admin", "pass")
	cats, err := c.GetCategories(context.Background())
	if err != nil {
		t.Fatalf("GetCategories: %v", err)
	}
	if cats["books"].SavePath != "/media/books" {
		t.Errorf("books save path = %q", cats["books"].SavePath)
	}
	if cats["audiobooks"].SavePath != "/media/audio" {
		t.Errorf("audiobooks save path = %q", cats["audiobooks"].SavePath)
	}
}

func TestAddTorrent_FailsBody(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v2/auth/login":
			_, _ = w.Write([]byte("Ok."))
		case "/api/v2/torrents/add":
			_, _ = w.Write([]byte("Fails."))
		}
	}))
	defer srv.Close()

	c := newTestClient(srv.URL, "admin", "pass")
	if _, err := c.AddTorrent(context.Background(), "magnet:?xt=urn:btih:abc", "", ""); err == nil {
		t.Fatal("expected error on 'Fails.' body")
	}
}

func TestAddTorrent_HTTPError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v2/auth/login":
			_, _ = w.Write([]byte("Ok."))
		case "/api/v2/torrents/add":
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte("bad request"))
		}
	}))
	defer srv.Close()

	c := newTestClient(srv.URL, "admin", "pass")
	if _, err := c.AddTorrent(context.Background(), "magnet:?xt=urn:btih:abc", "", ""); err == nil {
		t.Fatal("expected error on 400")
	}
}

// TestAddTorrent_TorrentURL_SubmitsMultipart verifies that an http URL causes
// Bindery to fetch the torrent content and submit it via multipart rather than
// passing the URL to qBittorrent (which may not be able to reach the indexer).
func TestAddTorrent_TorrentURL_SubmitsMultipart(t *testing.T) {
	var gotMultipart bool
	var gotTorrentContent []byte
	var mu sync.Mutex
	added := false

	indexer := newFakeIndexer(t)
	defer indexer.Close()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v2/auth/login":
			_, _ = w.Write([]byte("Ok."))
		case "/api/v2/torrents/add":
			ct := r.Header.Get("Content-Type")
			if strings.HasPrefix(ct, "multipart/form-data") {
				gotMultipart = true
				mr, err := r.MultipartReader()
				if err != nil {
					t.Errorf("parse multipart: %v", err)
					break
				}
				for {
					part, err := mr.NextPart()
					if err == io.EOF {
						break
					}
					if err != nil {
						t.Errorf("next part: %v", err)
						break
					}
					if part.FormName() == "torrents" {
						gotTorrentContent, _ = io.ReadAll(part)
					}
				}
			}
			mu.Lock()
			added = true
			mu.Unlock()
			_, _ = w.Write([]byte("Ok."))
		case "/api/v2/torrents/info":
			mu.Lock()
			isAdded := added
			mu.Unlock()
			if isAdded {
				_, _ = w.Write([]byte(`[{"hash":"aabbccdd","name":"test","added_on":1000}]`))
			} else {
				_, _ = w.Write([]byte("[]"))
			}
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()

	c := newTestClient(srv.URL, "admin", "pass")
	allowTorrentFetch(c)
	if _, err := c.AddTorrent(context.Background(), indexer.URL+"/torrent", "", ""); err != nil {
		t.Fatalf("AddTorrent: %v", err)
	}
	if !gotMultipart {
		t.Error("expected multipart/form-data submission for http URL, got url-encoded form")
	}
	if string(gotTorrentContent) != fakeTorrentContent {
		t.Errorf("torrent content: want %q, got %q", fakeTorrentContent, string(gotTorrentContent))
	}
}

// TestAddTorrent_TorrentURL_WithCategoryAndSavePath verifies that category and
// savepath are included in the multipart body when submitting a torrent URL.
func TestAddTorrent_TorrentURL_WithCategoryAndSavePath(t *testing.T) {
	var gotCategory, gotSavePath string
	var mu sync.Mutex
	added := false

	indexer := newFakeIndexer(t)
	defer indexer.Close()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v2/auth/login":
			_, _ = w.Write([]byte("Ok."))
		case "/api/v2/torrents/add":
			if err := r.ParseMultipartForm(1 << 20); err == nil {
				gotCategory = r.FormValue("category")
				gotSavePath = r.FormValue("savepath")
			}
			mu.Lock()
			added = true
			mu.Unlock()
			_, _ = w.Write([]byte("Ok."))
		case "/api/v2/torrents/info":
			mu.Lock()
			isAdded := added
			mu.Unlock()
			if isAdded {
				_, _ = w.Write([]byte(`[{"hash":"aabbccdd","name":"test","added_on":1000}]`))
			} else {
				_, _ = w.Write([]byte("[]"))
			}
		}
	}))
	defer srv.Close()

	c := newTestClient(srv.URL, "admin", "pass")
	allowTorrentFetch(c)
	if _, err := c.AddTorrent(context.Background(), indexer.URL+"/torrent", "books", "/downloads"); err != nil {
		t.Fatalf("AddTorrent: %v", err)
	}
	if gotCategory != "books" {
		t.Errorf("category: want %q, got %q", "books", gotCategory)
	}
	if gotSavePath != "/downloads" {
		t.Errorf("savepath: want %q, got %q", "/downloads", gotSavePath)
	}
}

// TestAddTorrent_TorrentURL_FetchFailure verifies that a non-200 from the
// indexer is returned as an error and qBittorrent is never contacted.
func TestAddTorrent_TorrentURL_FetchFailure(t *testing.T) {
	qbitCalled := false

	indexer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer indexer.Close()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v2/auth/login":
			_, _ = w.Write([]byte("Ok."))
		case "/api/v2/torrents/add":
			qbitCalled = true
			_, _ = w.Write([]byte("Ok."))
		}
	}))
	defer srv.Close()

	c := newTestClient(srv.URL, "admin", "pass")
	allowTorrentFetch(c)
	_, err := c.AddTorrent(context.Background(), indexer.URL+"/torrent", "", "")
	if err == nil {
		t.Fatal("expected error when indexer returns 401")
		return
	}
	if !strings.Contains(err.Error(), "401") {
		t.Errorf("error should mention 401, got: %v", err)
	}
	if qbitCalled {
		t.Error("qBittorrent should not be contacted when torrent fetch fails")
	}
}

func TestAddTorrent_TorrentURL_RedirectToMagnetUsesURLForm(t *testing.T) {
	const magnet = "magnet:?xt=urn:btih:ABCDEF123&dn=Book"
	indexer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, magnet, http.StatusFound)
	}))
	defer indexer.Close()

	var gotURL string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v2/auth/login":
			_, _ = w.Write([]byte("Ok."))
		case "/api/v2/torrents/add":
			if err := r.ParseForm(); err != nil {
				t.Fatal(err)
			}
			gotURL = r.FormValue("urls")
			_, _ = w.Write([]byte("Ok."))
		case "/api/v2/torrents/info":
			_, _ = w.Write([]byte("[]"))
		}
	}))
	defer srv.Close()

	c := newTestClient(srv.URL, "admin", "pass")
	allowTorrentFetch(c)
	hash, err := c.AddTorrent(context.Background(), indexer.URL+"/redirect", "books", "")
	if err != nil {
		t.Fatalf("AddTorrent: %v", err)
	}
	if hash != "abcdef123" {
		t.Errorf("hash: want abcdef123, got %q", hash)
	}
	if gotURL != magnet {
		t.Errorf("urls field: want %q, got %q", magnet, gotURL)
	}
}

func TestAddTorrent_TorrentURL_OversizedResponseDoesNotCallQbitAdd(t *testing.T) {
	orig := maxTorrentFileBytes
	maxTorrentFileBytes = 8
	t.Cleanup(func() { maxTorrentFileBytes = orig })

	indexer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("123456789"))
	}))
	defer indexer.Close()

	qbitCalled := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v2/auth/login":
			_, _ = w.Write([]byte("Ok."))
		case "/api/v2/torrents/add":
			qbitCalled = true
			_, _ = w.Write([]byte("Ok."))
		case "/api/v2/torrents/info":
			_, _ = w.Write([]byte("[]"))
		}
	}))
	defer srv.Close()

	c := newTestClient(srv.URL, "admin", "pass")
	allowTorrentFetch(c)
	_, err := c.AddTorrent(context.Background(), indexer.URL+"/too-large.torrent", "", "")
	if err == nil {
		t.Fatal("expected oversized response error")
		return
	}
	if !strings.Contains(err.Error(), "exceeds") {
		t.Errorf("expected oversized error, got: %v", err)
	}
	if qbitCalled {
		t.Error("qBittorrent should not be contacted when torrent fetch is oversized")
	}
}

func TestAddTorrent_TorrentURL_DefaultValidationRejectsLoopbackBeforeFetch(t *testing.T) {
	indexerCalled := false
	indexer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		indexerCalled = true
		_, _ = w.Write([]byte(fakeTorrentContent))
	}))
	defer indexer.Close()

	qbitAddCalled := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v2/auth/login":
			_, _ = w.Write([]byte("Ok."))
		case "/api/v2/torrents/add":
			qbitAddCalled = true
			_, _ = w.Write([]byte("Ok."))
		case "/api/v2/torrents/info":
			_, _ = w.Write([]byte("[]"))
		}
	}))
	defer srv.Close()

	c := newTestClient(srv.URL, "admin", "pass")
	_, err := c.AddTorrent(context.Background(), indexer.URL+"/blocked.torrent", "", "")
	if err == nil {
		t.Fatal("expected loopback URL validation failure")
		return
	}
	if !strings.Contains(err.Error(), "url not allowed") {
		t.Errorf("expected URL validation error, got: %v", err)
	}
	if indexerCalled {
		t.Error("indexer URL must not be fetched after validation failure")
	}
	if qbitAddCalled {
		t.Error("qBittorrent add endpoint should not be contacted after validation failure")
	}
}

func TestGetTorrents_Success(t *testing.T) {
	want := []Torrent{
		{Hash: "abc123", Name: "My Book", Size: 1024, Progress: 0.5, State: "downloading", Category: "books"},
		{Hash: "def456", Name: "Another Book", Size: 2048, Progress: 1.0, State: "seeding"},
	}
	body, _ := json.Marshal(want)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v2/auth/login":
			_, _ = w.Write([]byte("Ok."))
		case "/api/v2/torrents/info":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write(body)
		}
	}))
	defer srv.Close()

	c := newTestClient(srv.URL, "admin", "pass")
	torrents, err := c.GetTorrents(context.Background(), "")
	if err != nil {
		t.Fatalf("GetTorrents: %v", err)
	}
	if len(torrents) != 2 {
		t.Fatalf("expected 2 torrents, got %d", len(torrents))
	}
	if torrents[0].Hash != "abc123" {
		t.Errorf("first hash: want 'abc123', got %q", torrents[0].Hash)
	}
	if torrents[1].Name != "Another Book" {
		t.Errorf("second name: want 'Another Book', got %q", torrents[1].Name)
	}
}

func TestGetTorrents_WithCategory(t *testing.T) {
	var gotRawQuery string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v2/auth/login":
			_, _ = w.Write([]byte("Ok."))
		case "/api/v2/torrents/info":
			gotRawQuery = r.URL.RawQuery
			_, _ = w.Write([]byte("[]"))
		}
	}))
	defer srv.Close()

	c := newTestClient(srv.URL, "admin", "pass")
	_, err := c.GetTorrents(context.Background(), "audiobooks")
	if err != nil {
		t.Fatalf("GetTorrents: %v", err)
	}
	if !strings.Contains(gotRawQuery, "category=audiobooks") {
		t.Errorf("expected category in query string, got: %q", gotRawQuery)
	}
}

func TestGetTorrents_InvalidJSON(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v2/auth/login":
			_, _ = w.Write([]byte("Ok."))
		case "/api/v2/torrents/info":
			_, _ = w.Write([]byte("not valid json"))
		}
	}))
	defer srv.Close()

	c := newTestClient(srv.URL, "admin", "pass")
	if _, err := c.GetTorrents(context.Background(), ""); err == nil {
		t.Fatal("expected error on invalid JSON")
	}
}

// TestGetTorrents_NormalizesWindowsPaths is the PixieApples follow-up to
// #800: a qBittorrent instance running on Windows reports SavePath /
// ContentPath / Name with backslash separators, which downstream Linux
// path code in Bindery (filepath.Walk, PathRemap.Apply, pathIsAtOrUnder)
// cannot process. The normalization must happen at the API boundary so
// every consumer sees forward-slash paths regardless of qBit's host OS.
func TestGetTorrents_NormalizesWindowsPaths(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v2/auth/login":
			_, _ = w.Write([]byte("Ok."))
		case "/api/v2/torrents/info":
			// Backslashes in JSON strings are escaped as \\; the resulting
			// Go string contains single backslashes.
			_, _ = w.Write([]byte(`[{
				"hash":"abc",
				"name":"Book Title",
				"state":"pausedUP",
				"progress":1.0,
				"save_path":"N:\\Torrents\\complete",
				"content_path":"N:\\Torrents\\complete\\library\\book"
			}]`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()

	c := newTestClient(srv.URL, "admin", "pass")
	torrents, err := c.GetTorrents(context.Background(), "")
	if err != nil {
		t.Fatalf("GetTorrents: %v", err)
	}
	if len(torrents) != 1 {
		t.Fatalf("expected 1 torrent, got %d", len(torrents))
	}
	if got, want := torrents[0].SavePath, "N:/Torrents/complete"; got != want {
		t.Errorf("SavePath = %q, want %q (backslashes should be normalized)", got, want)
	}
	if got, want := torrents[0].ContentPath, "N:/Torrents/complete/library/book"; got != want {
		t.Errorf("ContentPath = %q, want %q (backslashes should be normalized)", got, want)
	}
}

// TestGetCategories_NormalizesWindowsSavePath asserts the same normalization
// applies to Category.SavePath returned by GET /torrents/categories, so the
// qBit health-check (which uses category save paths) doesn't fail on a
// Windows-qBit deployment.
func TestGetCategories_NormalizesWindowsSavePath(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v2/auth/login":
			_, _ = w.Write([]byte("Ok."))
		case "/api/v2/torrents/categories":
			_, _ = w.Write([]byte(`{"library":{"name":"library","savePath":"N:\\Torrents\\complete\\library"}}`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()

	c := newTestClient(srv.URL, "admin", "pass")
	cats, err := c.GetCategories(context.Background())
	if err != nil {
		t.Fatalf("GetCategories: %v", err)
	}
	if got, want := cats["library"].SavePath, "N:/Torrents/complete/library"; got != want {
		t.Errorf("SavePath = %q, want %q (backslashes should be normalized)", got, want)
	}
}

func TestDeleteTorrent_Success(t *testing.T) {
	var gotHash, gotDeleteFiles string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v2/auth/login":
			_, _ = w.Write([]byte("Ok."))
		case "/api/v2/torrents/delete":
			_ = r.ParseForm()
			gotHash = r.FormValue("hashes")
			gotDeleteFiles = r.FormValue("deleteFiles")
			w.WriteHeader(http.StatusOK)
		}
	}))
	defer srv.Close()

	c := newTestClient(srv.URL, "admin", "pass")
	if err := c.DeleteTorrent(context.Background(), "abc123", true); err != nil {
		t.Fatalf("DeleteTorrent: %v", err)
	}
	if gotHash != "abc123" {
		t.Errorf("hashes: want 'abc123', got %q", gotHash)
	}
	if gotDeleteFiles != "true" {
		t.Errorf("deleteFiles: want 'true', got %q", gotDeleteFiles)
	}
}

func TestDeleteTorrent_KeepFiles(t *testing.T) {
	var gotDeleteFiles string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v2/auth/login":
			_, _ = w.Write([]byte("Ok."))
		case "/api/v2/torrents/delete":
			_ = r.ParseForm()
			gotDeleteFiles = r.FormValue("deleteFiles")
			w.WriteHeader(http.StatusOK)
		}
	}))
	defer srv.Close()

	c := newTestClient(srv.URL, "admin", "pass")
	_ = c.DeleteTorrent(context.Background(), "abc", false)
	if gotDeleteFiles != "false" {
		t.Errorf("deleteFiles: want 'false', got %q", gotDeleteFiles)
	}
}

func TestDeleteTorrent_HTTPError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v2/auth/login":
			_, _ = w.Write([]byte("Ok."))
		case "/api/v2/torrents/delete":
			w.WriteHeader(http.StatusInternalServerError)
		}
	}))
	defer srv.Close()

	c := newTestClient(srv.URL, "admin", "pass")
	if err := c.DeleteTorrent(context.Background(), "abc", false); err == nil {
		t.Fatal("expected error on 500")
	}
}

// TestGet_403Retry verifies that a 403 triggers re-login and a single retry.
func TestGet_403Retry(t *testing.T) {
	loginCount := 0
	versionHits := 0

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v2/auth/login":
			loginCount++
			_, _ = w.Write([]byte("Ok."))
		case "/api/v2/app/version":
			versionHits++
			if versionHits == 1 {
				// Simulate expired session
				w.WriteHeader(http.StatusForbidden)
				return
			}
			_, _ = w.Write([]byte("5.0.0"))
		}
	}))
	defer srv.Close()

	c := newTestClient(srv.URL, "admin", "pass")
	// Mark as already logged in so the first call skips the initial login.
	c.loggedIn = true

	if err := c.Test(context.Background()); err != nil {
		t.Fatalf("expected retry to succeed: %v", err)
	}
	if loginCount != 1 {
		t.Errorf("expected 1 re-login on 403, got %d", loginCount)
	}
	if versionHits != 2 {
		t.Errorf("expected 2 version requests (403 + retry), got %d", versionHits)
	}
}

// TestEnsureLoggedIn_AlreadyLoggedIn verifies that Login is not called again.
func TestEnsureLoggedIn_AlreadyLoggedIn(t *testing.T) {
	loginCount := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v2/auth/login" {
			loginCount++
		}
		_, _ = w.Write([]byte("Ok."))
	}))
	defer srv.Close()

	c := newTestClient(srv.URL, "admin", "pass")
	c.loggedIn = true
	if err := c.ensureLoggedIn(context.Background()); err != nil {
		t.Fatalf("ensureLoggedIn: %v", err)
	}
	if loginCount != 0 {
		t.Errorf("Login should not be called when already logged in; called %d times", loginCount)
	}
}

// TestEnsureLoggedIn_NotLoggedIn verifies that Login is called when loggedIn=false.
func TestEnsureLoggedIn_NotLoggedIn(t *testing.T) {
	loginCount := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v2/auth/login" {
			loginCount++
			_, _ = w.Write([]byte("Ok."))
		}
	}))
	defer srv.Close()

	c := newTestClient(srv.URL, "admin", "pass")
	if err := c.ensureLoggedIn(context.Background()); err != nil {
		t.Fatalf("ensureLoggedIn: %v", err)
	}
	if loginCount != 1 {
		t.Errorf("Login should be called once when not logged in; called %d times", loginCount)
	}
}

// TestAddTorrent_HashFoundUnderDifferentCategory verifies that the unfiltered
// poll finds a torrent even when qBittorrent has initially placed it under a
// different category than the one requested.
func TestAddTorrent_HashFoundUnderDifferentCategory(t *testing.T) {
	const wantHash = "deadbeefdeadbeefdeadbeefdeadbeefdeadbeef"
	var setCategoryHash, setCategoryValue string
	var mu sync.Mutex
	added := false // becomes true after /torrents/add is called
	infoQueries := []string{}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v2/auth/login":
			_, _ = w.Write([]byte("Ok."))
		case "/api/v2/torrents/add":
			mu.Lock()
			added = true
			mu.Unlock()
			_, _ = w.Write([]byte("Ok."))
		case "/api/v2/torrents/info":
			mu.Lock()
			infoQueries = append(infoQueries, r.URL.RawQuery)
			mu.Unlock()
			if r.URL.Query().Get("category") != "" {
				// A category-filtered lookup would reproduce the race from #418:
				// qBittorrent can expose the hash before category metadata lands.
				_, _ = w.Write([]byte("[]"))
				return
			}
			mu.Lock()
			isAdded := added
			mu.Unlock()
			if !isAdded {
				// Before add: no torrents yet.
				_, _ = w.Write([]byte("[]"))
				return
			}
			// After add: torrent appears under "uncategorized" (different from requested "books").
			torrents := []Torrent{{
				Hash:     wantHash,
				Name:     "Test Book",
				Category: "uncategorized",
				AddedOn:  1000,
			}}
			body, _ := json.Marshal(torrents)
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write(body)
		case "/api/v2/torrents/setCategory":
			_ = r.ParseForm()
			mu.Lock()
			setCategoryHash = r.FormValue("hashes")
			setCategoryValue = r.FormValue("category")
			mu.Unlock()
			w.WriteHeader(http.StatusOK)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()

	indexer := newFakeIndexer(t)
	defer indexer.Close()

	c := newTestClient(srv.URL, "admin", "pass")
	allowTorrentFetch(c)
	hash, err := c.AddTorrent(context.Background(), indexer.URL+"/torrent", "books", "")
	if err != nil {
		t.Fatalf("AddTorrent: %v", err)
	}
	if hash != wantHash {
		t.Errorf("hash: want %q, got %q", wantHash, hash)
	}
	mu.Lock()
	gotSetCatHash := setCategoryHash
	gotSetCatVal := setCategoryValue
	mu.Unlock()
	if gotSetCatHash != wantHash {
		t.Errorf("setCategory hashes: want %q, got %q", wantHash, gotSetCatHash)
	}
	if gotSetCatVal != "books" {
		t.Errorf("setCategory category: want %q, got %q", "books", gotSetCatVal)
	}
	mu.Lock()
	gotInfoQueries := append([]string(nil), infoQueries...)
	mu.Unlock()
	if len(gotInfoQueries) == 0 {
		t.Fatal("expected torrent info to be polled")
	}
	for _, rawQuery := range gotInfoQueries {
		if rawQuery != "" {
			t.Errorf("torrent info poll should be unfiltered, got query %q", rawQuery)
		}
	}
}

// TestAddTorrent_HashLookupTimeout verifies that when the torrent never appears
// within the deadline, an ERROR is logged with before/after hash lists and the
// appropriate error is returned.
func TestAddTorrent_HashLookupTimeout(t *testing.T) {
	orig := hashPollTimeout
	hashPollTimeout = 50 * time.Millisecond
	t.Cleanup(func() { hashPollTimeout = orig })

	catcher := &logCatcher{}
	origLogger := slog.Default()
	slog.SetDefault(slog.New(catcher))
	t.Cleanup(func() { slog.SetDefault(origLogger) })

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v2/auth/login":
			_, _ = w.Write([]byte("Ok."))
		case "/api/v2/torrents/add":
			_, _ = w.Write([]byte("Ok."))
		case "/api/v2/torrents/info":
			// Never return the new torrent — list stays empty.
			_, _ = w.Write([]byte("[]"))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()

	indexer := newFakeIndexer(t)
	defer indexer.Close()

	c := newTestClient(srv.URL, "admin", "pass")
	allowTorrentFetch(c)
	_, err := c.AddTorrent(context.Background(), indexer.URL+"/torrent", "books", "")
	if err == nil {
		t.Fatal("expected error on timeout, got nil")
		return
	}
	if !strings.Contains(err.Error(), "hash could not be determined") {
		t.Errorf("unexpected error: %v", err)
	}

	records := catcher.Records()
	if len(records) == 0 {
		t.Fatal("expected slog.Error to be called on timeout")
	}
	found := false
	for _, r := range records {
		if r.Level == slog.LevelError && strings.Contains(r.Message, "timed out") {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("expected ERROR log with 'timed out' message, got %d records", len(records))
	}
}

// roundTripFunc is a test helper that implements http.RoundTripper via a function.
type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

// qbNetTimeoutErr is a minimal net.Error that signals a timeout.
type qbNetTimeoutErr struct{}

func (e *qbNetTimeoutErr) Error() string   { return "i/o timeout" }
func (e *qbNetTimeoutErr) Timeout() bool   { return true }
func (e *qbNetTimeoutErr) Temporary() bool { return true }

// newTransportClient creates a qBittorrent Client with a custom HTTP transport.
func newTransportClient(transport http.RoundTripper) *Client {
	c := New("fake-host", 8080, "admin", "pass", "", false)
	c.http = &http.Client{Transport: transport, Jar: c.http.Jar}
	return c
}

// TestTest_DNSNotFound verifies that a DNS lookup failure appends the Docker
// network hint and does NOT misclassify it as an auth error.
func TestTest_DNSNotFound(t *testing.T) {
	dnsErr := &net.DNSError{Name: "qbittorrent-container", IsNotFound: true}
	c := newTransportClient(roundTripFunc(func(*http.Request) (*http.Response, error) {
		return nil, fmt.Errorf("dial: %w", dnsErr)
	}))

	err := c.Test(context.Background())
	if err == nil {
		t.Fatal("expected error")
		return
	}
	msg := err.Error()
	if strings.Contains(msg, "connected to qBittorrent") {
		t.Errorf("DNS failure must not be reported as auth error: %q", msg)
	}
	if !strings.Contains(msg, "same Docker network") {
		t.Errorf("expected Docker network hint, got: %q", msg)
	}
}

// TestTest_ConnectionRefused verifies that ECONNREFUSED appends the port hint.
func TestTest_ConnectionRefused(t *testing.T) {
	c := newTransportClient(roundTripFunc(func(*http.Request) (*http.Response, error) {
		return nil, fmt.Errorf("dial tcp: %w", syscall.ECONNREFUSED)
	}))

	err := c.Test(context.Background())
	if err == nil {
		t.Fatal("expected error")
		return
	}
	if !strings.Contains(err.Error(), "host firewall is rejecting") {
		t.Errorf("expected port hint, got: %q", err.Error())
	}
}

// TestTest_Timeout_QBit verifies that a timeout error appends the firewall hint.
func TestTest_Timeout_QBit(t *testing.T) {
	c := newTransportClient(roundTripFunc(func(*http.Request) (*http.Response, error) {
		return nil, &qbNetTimeoutErr{}
	}))

	err := c.Test(context.Background())
	if err == nil {
		t.Fatal("expected error")
		return
	}
	if !strings.Contains(err.Error(), "firewall or proxy") {
		t.Errorf("expected firewall hint, got: %q", err.Error())
	}
}

// TestTest_ServerError_NoHint_QBit verifies that an HTTP 500 from the server
// does NOT produce a network hint (qBittorrent responded, transport worked).
func TestTest_ServerError_NoHint_QBit(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v2/auth/login":
			_, _ = w.Write([]byte("Ok."))
		default:
			http.Error(w, "server error", http.StatusInternalServerError)
		}
	}))
	defer srv.Close()

	c := newTestClient(srv.URL, "admin", "pass")
	err := c.Test(context.Background())
	if err == nil {
		t.Fatal("expected error")
		return
	}
	msg := err.Error()
	for _, hint := range []string{"Docker network", "host firewall is rejecting", "firewall or proxy"} {
		if strings.Contains(msg, hint) {
			t.Errorf("server error must not produce hint %q; got: %q", hint, msg)
		}
	}
}

// --------------------------------------------------------------------------
// qBittorrent v4 / v5 response-shape regression fixtures (#692).
//
// The qBittorrent v5.x release reworked several WebUI API response shapes that
// v4.x clients had relied on. Three were already patched after users hit them
// (#623 auth, #641 categories/savepath, #690 add-torrent). The holistic audit
// in #692 confirmed no further functional shape mismatches remain, but both v4
// and v5 deployments are still in the wild — these fixtures lock in that every
// endpoint Bindery touches parses BOTH shapes identically, so a future change
// can't silently regress one of them.
//
// The official v5.0 wiki still documents the v4 shapes for /auth/login and
// /torrents/add; the v5 shapes below are the ones observed in the field and
// confirmed by the #623 / #690 fixes.
// --------------------------------------------------------------------------

// loginFixture is one observed /auth/login response shape.
type loginFixture struct {
	name   string
	status int
	body   string
}

// TestLogin_V4AndV5_ResponseShapes verifies every successful-login response
// shape across qBittorrent versions is accepted:
//   - v4.x: 200 OK + plaintext "Ok."
//   - v5.x: 204 No Content + empty body
//   - v5.x (some builds): 200 OK + empty body
func TestLogin_V4AndV5_ResponseShapes(t *testing.T) {
	fixtures := []loginFixture{
		{name: "v4_200_Ok", status: http.StatusOK, body: "Ok."},
		{name: "v5_204_empty", status: http.StatusNoContent, body: ""},
		{name: "v5_200_empty", status: http.StatusOK, body: ""},
	}
	for _, f := range fixtures {
		t.Run(f.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/api/v2/auth/login" {
					t.Errorf("unexpected path: %s", r.URL.Path)
				}
				if f.status != http.StatusOK {
					w.WriteHeader(f.status)
				}
				if f.body != "" {
					_, _ = w.Write([]byte(f.body))
				}
			}))
			defer srv.Close()

			c := newTestClient(srv.URL, "admin", "pass")
			if err := c.Login(context.Background()); err != nil {
				t.Fatalf("Login should accept %s shape: %v", f.name, err)
			}
			if !c.loggedIn {
				t.Errorf("loggedIn should be true after %s response", f.name)
			}
		})
	}
}

// TestAddTorrent_V4AndV5_ResponseShapes verifies POST /torrents/add accepts
// both the v4 plaintext body and the v5 JSON body on a successful add.
//   - v4.x: plaintext "Ok." (hash resolved via the before/after poll)
//   - v5.x: JSON {"added_torrent_ids":[...],"success_count":N} (hash inline)
func TestAddTorrent_V4AndV5_ResponseShapes(t *testing.T) {
	const v5Hash = "dddddddddddddddddddddddddddddddddddddddd"
	const v4Hash = "eeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee"

	fixtures := []struct {
		name     string
		body     string
		wantHash string // exact hash expected back, empty = don't assert
	}{
		{name: "v4_plaintext_Ok", body: "Ok.", wantHash: v4Hash},
		{
			name:     "v5_json",
			body:     `{"added_torrent_ids":["` + v5Hash + `"],"failure_count":0,"pending_count":0,"success_count":1}`,
			wantHash: v5Hash,
		},
	}
	for _, f := range fixtures {
		t.Run(f.name, func(t *testing.T) {
			var mu sync.Mutex
			added := false
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/api/v2/auth/login":
					_, _ = w.Write([]byte("Ok."))
				case "/api/v2/torrents/add":
					mu.Lock()
					added = true
					mu.Unlock()
					_, _ = w.Write([]byte(f.body))
				case "/api/v2/torrents/info":
					mu.Lock()
					isAdded := added
					mu.Unlock()
					if isAdded {
						_, _ = w.Write([]byte(`[{"hash":"` + v4Hash + `","name":"Book","added_on":1000}]`))
					} else {
						_, _ = w.Write([]byte("[]"))
					}
				default:
					w.WriteHeader(http.StatusNotFound)
				}
			}))
			defer srv.Close()

			indexer := newFakeIndexer(t)
			defer indexer.Close()

			c := newTestClient(srv.URL, "admin", "pass")
			allowTorrentFetch(c)
			c.loggedIn = true

			got, err := c.AddTorrent(context.Background(), indexer.URL+"/torrent", "", "")
			if err != nil {
				t.Fatalf("AddTorrent should accept %s shape: %v", f.name, err)
			}
			if f.wantHash != "" && got != f.wantHash {
				t.Errorf("%s: hash want %q, got %q", f.name, f.wantHash, got)
			}
		})
	}
}

// TestGetTorrents_V4AndV5_StateValues verifies GET /torrents/info parses the
// torrent-list JSON identically across versions. qBittorrent 5.0 renamed the
// user-paused states from v4's `pausedDL`/`pausedUP` to `stoppedDL`/`stoppedUP`
// (the WebUI "Pause" button became "Stop"). Bindery passes the state string
// through verbatim and only special-cases `error`/`missingfiles`/`stalleddl`,
// so the rename is non-breaking — this fixture locks that in: both vocabularies
// decode into the same struct shape with the state preserved as-is.
func TestGetTorrents_V4AndV5_StateValues(t *testing.T) {
	fixtures := []struct {
		name      string
		body      string
		wantState string
	}{
		{
			name:      "v4_pausedUP",
			body:      `[{"hash":"abc","name":"Book","state":"pausedUP","progress":1.0,"amount_left":0,"eta":8640000,"save_path":"/dl"}]`,
			wantState: "pausedUP",
		},
		{
			name:      "v5_stoppedUP",
			body:      `[{"hash":"abc","name":"Book","state":"stoppedUP","progress":1.0,"amount_left":0,"eta":8640000,"save_path":"/dl"}]`,
			wantState: "stoppedUP",
		},
		{
			name:      "v4_pausedDL",
			body:      `[{"hash":"abc","name":"Book","state":"pausedDL","progress":0.4,"amount_left":600,"eta":8640000,"save_path":"/dl"}]`,
			wantState: "pausedDL",
		},
		{
			name:      "v5_stoppedDL",
			body:      `[{"hash":"abc","name":"Book","state":"stoppedDL","progress":0.4,"amount_left":600,"eta":8640000,"save_path":"/dl"}]`,
			wantState: "stoppedDL",
		},
	}
	for _, f := range fixtures {
		t.Run(f.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/api/v2/auth/login":
					_, _ = w.Write([]byte("Ok."))
				case "/api/v2/torrents/info":
					w.Header().Set("Content-Type", "application/json")
					_, _ = w.Write([]byte(f.body))
				default:
					w.WriteHeader(http.StatusNotFound)
				}
			}))
			defer srv.Close()

			c := newTestClient(srv.URL, "admin", "pass")
			torrents, err := c.GetTorrents(context.Background(), "")
			if err != nil {
				t.Fatalf("GetTorrents (%s): %v", f.name, err)
			}
			if len(torrents) != 1 {
				t.Fatalf("%s: want 1 torrent, got %d", f.name, len(torrents))
			}
			if torrents[0].State != f.wantState {
				t.Errorf("%s: state want %q, got %q", f.name, f.wantState, torrents[0].State)
			}
			if torrents[0].Hash != "abc" {
				t.Errorf("%s: hash want %q, got %q", f.name, "abc", torrents[0].Hash)
			}
		})
	}
}

// TestGetCategories_V4AndV5_ResponseShapes verifies GET /torrents/categories
// parses both the v4 and v5 JSON object shapes. The endpoint has always
// returned a name->object map; the variation across versions is the per-entry
// save-path key (`savePath`, `save_path`, `download_path`), which
// Category.UnmarshalJSON normalizes. qBittorrent v5.1.4 can also include a
// boolean `download_path` flag alongside `savePath`; that alias must be ignored
// instead of failing the whole categories response. This fixture exercises every
// observed key and confirms a missing-name entry is backfilled from its map key.
func TestGetCategories_V4AndV5_ResponseShapes(t *testing.T) {
	fixtures := []struct {
		name string
		body string
	}{
		{
			name: "v5_savePath_camel",
			body: `{"Video":{"name":"Video","savePath":"/dl/video"},"eBooks":{"name":"eBooks","savePath":"/dl/ebooks"}}`,
		},
		{
			name: "v4_save_path_snake",
			body: `{"Video":{"name":"Video","save_path":"/dl/video"},"eBooks":{"name":"eBooks","save_path":"/dl/ebooks"}}`,
		},
		{
			name: "mixed_with_download_path_and_missing_name",
			body: `{"Video":{"savePath":"/dl/video"},"eBooks":{"name":"eBooks","download_path":"/dl/ebooks"}}`,
		},
		{
			name: "v5_1_4_boolean_download_path_flag",
			body: `{"Video":{"name":"Video","download_path":false,"savePath":"/dl/video"},"eBooks":{"name":"eBooks","download_path":false,"savePath":"/dl/ebooks"}}`,
		},
	}
	for _, f := range fixtures {
		t.Run(f.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/api/v2/auth/login":
					_, _ = w.Write([]byte("Ok."))
				case "/api/v2/torrents/categories":
					_, _ = w.Write([]byte(f.body))
				default:
					w.WriteHeader(http.StatusNotFound)
				}
			}))
			defer srv.Close()

			c := newTestClient(srv.URL, "admin", "pass")
			cats, err := c.GetCategories(context.Background())
			if err != nil {
				t.Fatalf("GetCategories (%s): %v", f.name, err)
			}
			if cats["Video"].SavePath != "/dl/video" {
				t.Errorf("%s: Video save path = %q, want /dl/video", f.name, cats["Video"].SavePath)
			}
			if cats["eBooks"].SavePath != "/dl/ebooks" {
				t.Errorf("%s: eBooks save path = %q, want /dl/ebooks", f.name, cats["eBooks"].SavePath)
			}
			// Name is backfilled from the map key when the entry omits it.
			if cats["Video"].Name != "Video" {
				t.Errorf("%s: Video name = %q, want Video (backfilled from key)", f.name, cats["Video"].Name)
			}
		})
	}
}

// TestGetCategories_EmptyObject verifies an installation with no categories
// (qBittorrent returns `{}` on both v4 and v5) decodes to an empty, non-nil-
// usable map rather than an error.
func TestGetCategories_EmptyObject(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v2/auth/login":
			_, _ = w.Write([]byte("Ok."))
		case "/api/v2/torrents/categories":
			_, _ = w.Write([]byte(`{}`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()

	c := newTestClient(srv.URL, "admin", "pass")
	cats, err := c.GetCategories(context.Background())
	if err != nil {
		t.Fatalf("GetCategories on empty object: %v", err)
	}
	if len(cats) != 0 {
		t.Errorf("want 0 categories, got %d", len(cats))
	}
}

// TestGetDefaultSavePath_V4AndV5 verifies GET /app/defaultSavePath is parsed
// as a trimmed plaintext string. The endpoint returns plaintext on both v4 and
// v5; this fixture covers a Unix path, a Windows path, and a trailing-newline
// body (qBittorrent sometimes appends one).
func TestGetDefaultSavePath_V4AndV5(t *testing.T) {
	fixtures := []struct {
		name string
		body string
		want string
	}{
		{name: "unix_path", body: "/downloads", want: "/downloads"},
		{name: "windows_path", body: `C:/Users/Dayman/Downloads`, want: `C:/Users/Dayman/Downloads`},
		{name: "trailing_newline", body: "/downloads\n", want: "/downloads"},
	}
	for _, f := range fixtures {
		t.Run(f.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/api/v2/auth/login":
					_, _ = w.Write([]byte("Ok."))
				case "/api/v2/app/defaultSavePath":
					_, _ = w.Write([]byte(f.body))
				default:
					w.WriteHeader(http.StatusNotFound)
				}
			}))
			defer srv.Close()

			c := newTestClient(srv.URL, "admin", "pass")
			got, err := c.GetDefaultSavePath(context.Background())
			if err != nil {
				t.Fatalf("GetDefaultSavePath (%s): %v", f.name, err)
			}
			if got != f.want {
				t.Errorf("%s: want %q, got %q", f.name, f.want, got)
			}
		})
	}
}

// TestDeleteTorrent_V4AndV5 verifies POST /torrents/delete succeeds on the
// 200-with-empty-body response both versions return. v5.0 documents
// "200 All scenarios" with no body; v4 behaved the same. The client discards
// the body and keys only off the status code — this fixture locks that in.
func TestDeleteTorrent_V4AndV5(t *testing.T) {
	fixtures := []struct {
		name string
		body string
	}{
		{name: "v5_empty_body", body: ""},
		{name: "v4_plaintext_body", body: "Ok."},
	}
	for _, f := range fixtures {
		t.Run(f.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/api/v2/auth/login":
					_, _ = w.Write([]byte("Ok."))
				case "/api/v2/torrents/delete":
					w.WriteHeader(http.StatusOK)
					if f.body != "" {
						_, _ = w.Write([]byte(f.body))
					}
				default:
					w.WriteHeader(http.StatusNotFound)
				}
			}))
			defer srv.Close()

			c := newTestClient(srv.URL, "admin", "pass")
			if err := c.DeleteTorrent(context.Background(), "abc123", true); err != nil {
				t.Fatalf("DeleteTorrent should accept %s: %v", f.name, err)
			}
		})
	}
}

// TestTest_V4AndV5_VersionString verifies Test() (via GET /app/version)
// accepts both the v4 and v5 plaintext version-string bodies.
func TestTest_V4AndV5_VersionString(t *testing.T) {
	fixtures := []struct {
		name string
		body string
	}{
		{name: "v4_version", body: "v4.6.7"},
		{name: "v5_version", body: "v5.0.4"},
	}
	for _, f := range fixtures {
		t.Run(f.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/api/v2/auth/login":
					_, _ = w.Write([]byte("Ok."))
				case "/api/v2/app/version":
					_, _ = w.Write([]byte(f.body))
				default:
					w.WriteHeader(http.StatusNotFound)
				}
			}))
			defer srv.Close()

			c := newTestClient(srv.URL, "admin", "pass")
			if err := c.Test(context.Background()); err != nil {
				t.Fatalf("Test should accept %s version body: %v", f.name, err)
			}
		})
	}
}

// TestClient_Files_ReturnsRelativeNames verifies that Files() decodes the
// /api/v2/torrents/files response into the importer's []File shape with
// names left relative to the torrent's save path. Issue #903 regression
// guard: the importer joins these names with save_path to identify the
// exact files of THIS torrent, avoiding a walk of the shared download
// root.
func TestClient_Files_ReturnsRelativeNames(t *testing.T) {
	const hash = "deadbeef1234567890deadbeef1234567890dead"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v2/auth/login":
			_, _ = w.Write([]byte("Ok."))
		case "/api/v2/torrents/files":
			if got := r.URL.Query().Get("hash"); got != hash {
				t.Errorf("Files: missing or wrong hash query, got %q", got)
			}
			_, _ = w.Write([]byte(`[
				{"name":"MyBook/disc01.m4b","size":1024,"progress":1,"priority":1},
				{"name":"MyBook/disc02.m4b","size":2048,"progress":1,"priority":1},
				{"name":"MyBook/cover.jpg","size":128,"progress":1,"priority":1}
			]`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()

	c := newTestClient(srv.URL, "admin", "pass")
	files, err := c.Files(context.Background(), hash)
	if err != nil {
		t.Fatalf("Files: %v", err)
	}
	if len(files) != 3 {
		t.Fatalf("want 3 files, got %d", len(files))
	}
	if files[0].Name != "MyBook/disc01.m4b" || files[0].Size != 1024 {
		t.Errorf("file 0 mismatch: %+v", files[0])
	}
	if files[2].Name != "MyBook/cover.jpg" {
		t.Errorf("file 2 mismatch: %+v", files[2])
	}
}

// TestClient_Files_SingleFileTorrent — the issue #903 shape: a torrent
// containing a single loose file at the save root. Name is just the
// basename so the importer can resolve it directly via SavePath join.
func TestClient_Files_SingleFileTorrent(t *testing.T) {
	const hash = "1111111111111111111111111111111111111111"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v2/auth/login":
			_, _ = w.Write([]byte("Ok."))
		case "/api/v2/torrents/files":
			_, _ = w.Write([]byte(`[{"name":"standalone-book.m4b","size":50000,"progress":1}]`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()

	c := newTestClient(srv.URL, "admin", "pass")
	files, err := c.Files(context.Background(), hash)
	if err != nil {
		t.Fatalf("Files: %v", err)
	}
	if len(files) != 1 || files[0].Name != "standalone-book.m4b" {
		t.Errorf("expected single basename-only file, got %+v", files)
	}
}

// TestClient_Files_TorrentMissing — qBittorrent returns 404 for an unknown
// hash. Files() surfaces this as an error so the importer can decide to
// fall back to the directory walk (with a WARN) rather than swallowing the
// signal.
func TestClient_Files_TorrentMissing(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v2/auth/login":
			_, _ = w.Write([]byte("Ok."))
		case "/api/v2/torrents/files":
			w.WriteHeader(http.StatusNotFound)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()

	c := newTestClient(srv.URL, "admin", "pass")
	if _, err := c.Files(context.Background(), "nonexistent-hash"); err == nil {
		t.Fatal("expected error for unknown torrent hash")
	}
}

// TestClient_Files_WindowsBackslashesNormalized — a Windows qBittorrent
// instance reports nested file names with backslashes. The importer
// downstream uses forward-slash path operations, so Files() must normalize
// at the API boundary.
func TestClient_Files_WindowsBackslashesNormalized(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v2/auth/login":
			_, _ = w.Write([]byte("Ok."))
		case "/api/v2/torrents/files":
			_, _ = w.Write([]byte(`[{"name":"MyBook\\disc01.m4b","size":1024}]`))
		}
	}))
	defer srv.Close()

	c := newTestClient(srv.URL, "admin", "pass")
	files, err := c.Files(context.Background(), "h")
	if err != nil {
		t.Fatalf("Files: %v", err)
	}
	if len(files) != 1 || files[0].Name != "MyBook/disc01.m4b" {
		t.Errorf("backslash not normalized: %+v", files)
	}
}
