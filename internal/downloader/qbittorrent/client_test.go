package qbittorrent

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// newTestClient creates a Client pointing at the given test server URL.
func newTestClient(serverURL, username, password string) *Client {
	c := New("localhost", 8080, username, password, false)
	c.baseURL = serverURL
	return c
}

func TestNew(t *testing.T) {
	c := New("myhost", 8080, "admin", "secret", false)
	if c.baseURL != "http://myhost:8080" {
		t.Errorf("baseURL: want %q, got %q", "http://myhost:8080", c.baseURL)
	}
	if c.username != "admin" || c.password != "secret" {
		t.Error("credentials not stored correctly")
	}
	if c.loggedIn {
		t.Error("should not be logged in on construction")
	}

	cs := New("securehost", 443, "u", "p", true)
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

// TestAddTorrent_DifferentCategory verifies that AddTorrent can resolve the hash
// even when qBittorrent assigns the torrent a different category than requested.
// This exercises the fix for #363: the poll loop must use the unfiltered torrent
// list, not the category-filtered one.
func TestAddTorrent_DifferentCategory(t *testing.T) {
	const requestedCategory = "books"
	const actualCategory = "default" // qBittorrent assigns a different category

	newTorrent := Torrent{
		Hash:     "deadbeef1234",
		Name:     "Some Book",
		Category: actualCategory, // different from what Bindery requested
		AddedOn:  1000,
	}
	afterBody, _ := json.Marshal([]Torrent{newTorrent})

	// infoCallCount tracks how many times /torrents/info is called.
	// The first call is the "before" snapshot (returns empty); subsequent calls
	// are poll iterations (return the new torrent under a different category).
	infoCallCount := 0

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v2/auth/login":
			_, _ = w.Write([]byte("Ok."))
		case "/api/v2/torrents/add":
			_, _ = w.Write([]byte("Ok."))
		case "/api/v2/torrents/info":
			// No category filter expected — poll must use unfiltered list.
			if r.URL.Query().Get("category") != "" {
				t.Errorf("poll should not filter by category, got query: %s", r.URL.RawQuery)
			}
			w.Header().Set("Content-Type", "application/json")
			infoCallCount++
			if infoCallCount == 1 {
				// First call: before-snapshot — return empty list.
				_, _ = w.Write([]byte("[]"))
			} else {
				// Subsequent calls: poll — return the new torrent.
				_, _ = w.Write(afterBody)
			}
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()

	c := newTestClient(srv.URL, "admin", "pass")
	// Use a non-magnet URL so the code falls through to the poll loop.
	hash, err := c.AddTorrent(context.Background(), "http://example.com/file.torrent", requestedCategory, "")
	if err != nil {
		t.Fatalf("AddTorrent: %v", err)
	}
	if !strings.EqualFold(hash, newTorrent.Hash) {
		t.Errorf("hash: want %q, got %q", strings.ToLower(newTorrent.Hash), hash)
	}
}

// TestAddTorrent_Timeout verifies that AddTorrent returns an error when the
// poll loop exhausts its deadline without finding a new torrent.
// The timeout is forced by using a context that is already cancelled.
func TestAddTorrent_Timeout(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v2/auth/login":
			_, _ = w.Write([]byte("Ok."))
		case "/api/v2/torrents/add":
			_, _ = w.Write([]byte("Ok."))
		case "/api/v2/torrents/info":
			// Never return the new torrent — always empty list.
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte("[]"))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()

	// Override the package-level timeout to something very short so the test
	// doesn't take 30 seconds.
	orig := addTorrentTimeout
	// addTorrentTimeout is a const so we use a cancelled context instead to
	// force an immediate context-cancellation error path, which is equivalent
	// for our purposes: we verify AddTorrent returns an error when it can't
	// resolve the hash.
	_ = orig

	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()

	c := newTestClient(srv.URL, "admin", "pass")
	_, err := c.AddTorrent(ctx, "http://example.com/file.torrent", "books", "")
	if err == nil {
		t.Fatal("expected error when torrent hash cannot be resolved before timeout")
	}
}
