package transmission

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
)

// newTestClient creates a Client pointing at the given test server URL.
func newTestClient(serverURL, username, password string) *Client {
	c := New("localhost", 9091, username, password, "", false)
	c.baseURL = serverURL
	parsed, err := url.Parse(serverURL)
	if err != nil {
		panic(err)
	}
	c.rpcURL = parsed
	return c
}

func TestNew(t *testing.T) {
	c := New("myhost", 9091, "admin", "secret", "", false)
	if c.baseURL != "http://myhost:9091/transmission/rpc" {
		t.Errorf("baseURL: want %q, got %q", "http://myhost:9091/transmission/rpc", c.baseURL)
	}
	if c.initErr != nil {
		t.Fatalf("unexpected initErr: %v", c.initErr)
	}
	if c.username != "admin" || c.password != "secret" {
		t.Error("credentials not stored correctly")
	}

	cs := New("securehost", 443, "u", "p", "", true)
	if cs.baseURL != "https://securehost:443/transmission/rpc" {
		t.Errorf("SSL baseURL: got %q", cs.baseURL)
	}
}

func TestNew_InvalidHost(t *testing.T) {
	c := New("http://bad-host", 9091, "admin", "secret", "", false)
	if c.initErr == nil {
		t.Fatal("expected invalid host to be rejected")
	}
	if err := c.Test(context.Background()); err == nil {
		t.Fatal("expected client operations to fail when initialized with invalid host")
	}
}

func TestTest_Success(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"result":"success","arguments":{}}`))
	}))
	defer srv.Close()

	c := newTestClient(srv.URL, "user", "pass")
	if err := c.Test(context.Background()); err != nil {
		t.Fatalf("Test: %v", err)
	}
}

func TestTest_HTTPError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte("internal error"))
	}))
	defer srv.Close()

	c := newTestClient(srv.URL, "user", "pass")
	if err := c.Test(context.Background()); err == nil {
		t.Fatal("expected error on 500")
	}
}

func TestAddTorrent_NewTorrent(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"result":"success","arguments":{"torrent-added":{"id":42,"name":"My Book"}}}`))
	}))
	defer srv.Close()

	c := newTestClient(srv.URL, "user", "pass")
	id, err := c.AddTorrent(context.Background(), "magnet:?xt=urn:btih:abc123", "")
	if err != nil {
		t.Fatalf("AddTorrent: %v", err)
	}
	if id != 42 {
		t.Errorf("expected id=42, got %d", id)
	}
}

func TestAddTorrent_DuplicateTorrent(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"result":"success","arguments":{"torrent-duplicate":{"id":7,"name":"Existing Book"}}}`))
	}))
	defer srv.Close()

	c := newTestClient(srv.URL, "user", "pass")
	id, err := c.AddTorrent(context.Background(), "magnet:?xt=urn:btih:abc", "")
	if err != nil {
		t.Fatalf("AddTorrent: %v", err)
	}
	if id != 7 {
		t.Errorf("expected duplicate id=7, got %d", id)
	}
}

func TestAddTorrent_NoID(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"result":"success","arguments":{}}`))
	}))
	defer srv.Close()

	c := newTestClient(srv.URL, "user", "pass")
	if _, err := c.AddTorrent(context.Background(), "magnet:?xt=urn:btih:abc", ""); err == nil {
		t.Fatal("expected error when no torrent ID returned")
	}
}

func TestAddTorrent_FailResult(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"result":"duplicate torrent","arguments":{}}`))
	}))
	defer srv.Close()

	c := newTestClient(srv.URL, "user", "pass")
	if _, err := c.AddTorrent(context.Background(), "magnet:?xt=urn:btih:abc", ""); err == nil {
		t.Fatal("expected error on non-success result")
	}
}

func TestAddTorrent_HTTPError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte("forbidden"))
	}))
	defer srv.Close()

	c := newTestClient(srv.URL, "user", "pass")
	if _, err := c.AddTorrent(context.Background(), "magnet:?xt=urn:btih:abc", ""); err == nil {
		t.Fatal("expected error on 403")
	}
}

func TestAddTorrent_WithDownloadDir(t *testing.T) {
	var gotBody []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotBody, _ = func() ([]byte, error) {
			buf := make([]byte, r.ContentLength)
			_, _ = r.Body.Read(buf)
			return buf, nil
		}()
		_, _ = w.Write([]byte(`{"result":"success","arguments":{"torrent-added":{"id":9,"name":"Dir Book"}}}`))
	}))
	defer srv.Close()

	c := newTestClient(srv.URL, "user", "pass")
	id, err := c.AddTorrent(context.Background(), "magnet:?xt=urn:btih:abc", "/custom/dir")
	if err != nil {
		t.Fatalf("AddTorrent: %v", err)
	}
	if id != 9 {
		t.Errorf("expected id=9, got %d", id)
	}
	_ = gotBody // body inspection is optional in this style
}

func TestGetTorrents_Success(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{
			"result":"success",
			"arguments":{
				"torrents":[
					{"id":1,"name":"Book A","percentDone":0.5,"status":2,"downloadDir":"/downloads"},
					{"id":2,"name":"Book B","percentDone":1.0,"status":3,"downloadDir":"/downloads"}
				]
			}
		}`))
	}))
	defer srv.Close()

	c := newTestClient(srv.URL, "user", "pass")
	torrents, err := c.GetTorrents(context.Background(), "")
	if err != nil {
		t.Fatalf("GetTorrents: %v", err)
	}
	if len(torrents) != 2 {
		t.Fatalf("expected 2 torrents, got %d", len(torrents))
	}
	if torrents[0].Name != "Book A" {
		t.Errorf("first name: want 'Book A', got %q", torrents[0].Name)
	}
	if torrents[1].PercentDone != 1.0 {
		t.Errorf("second percentDone: want 1.0, got %f", torrents[1].PercentDone)
	}
}

func TestGetTorrents_WithDir(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{
			"result":"success",
			"arguments":{
				"torrents":[
					{"id":1,"name":"Book A","downloadDir":"/books"},
					{"id":2,"name":"Music","downloadDir":"/music"}
				]
			}
		}`))
	}))
	defer srv.Close()

	c := newTestClient(srv.URL, "user", "pass")
	torrents, err := c.GetTorrents(context.Background(), "/books")
	if err != nil {
		t.Fatalf("GetTorrents: %v", err)
	}
	if len(torrents) != 1 {
		t.Fatalf("expected 1 filtered torrent, got %d", len(torrents))
	}
	if torrents[0].DownloadDir != "/books" {
		t.Errorf("unexpected downloadDir: %q", torrents[0].DownloadDir)
	}
}

func TestGetTorrents_FailResult(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"result":"no session","arguments":{}}`))
	}))
	defer srv.Close()

	c := newTestClient(srv.URL, "user", "pass")
	if _, err := c.GetTorrents(context.Background(), ""); err == nil {
		t.Fatal("expected error on non-success result")
	}
}

func TestGetTorrents_InvalidJSON(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("not valid json"))
	}))
	defer srv.Close()

	c := newTestClient(srv.URL, "user", "pass")
	if _, err := c.GetTorrents(context.Background(), ""); err == nil {
		t.Fatal("expected error on invalid JSON")
	}
}

func TestRemoveTorrent_Success(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"result":"success","arguments":{}}`))
	}))
	defer srv.Close()

	c := newTestClient(srv.URL, "user", "pass")
	if err := c.RemoveTorrent(context.Background(), 42, false); err != nil {
		t.Fatalf("RemoveTorrent: %v", err)
	}
}

func TestRemoveTorrent_WithDeleteFiles(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"result":"success","arguments":{}}`))
	}))
	defer srv.Close()

	c := newTestClient(srv.URL, "user", "pass")
	if err := c.RemoveTorrent(context.Background(), 42, true); err != nil {
		t.Fatalf("RemoveTorrent with deleteFiles: %v", err)
	}
}

func TestRemoveTorrent_HTTPError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	c := newTestClient(srv.URL, "user", "pass")
	if err := c.RemoveTorrent(context.Background(), 42, false); err == nil {
		t.Fatal("expected error on 500")
	}
}

// TestSession_409Retry verifies that a 409 Conflict triggers a session-ID
// update and a single transparent retry, matching Transmission's CSRF
// protection protocol.
func TestSession_409Retry(t *testing.T) {
	attempts := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempts++
		if attempts == 1 {
			w.Header().Set("X-Transmission-Session-Id", "new-session-token")
			w.WriteHeader(http.StatusConflict)
			return
		}
		if r.Header.Get("X-Transmission-Session-Id") != "new-session-token" {
			t.Errorf("expected session ID on retry, got %q", r.Header.Get("X-Transmission-Session-Id"))
		}
		_, _ = w.Write([]byte(`{"result":"success","arguments":{}}`))
	}))
	defer srv.Close()

	c := newTestClient(srv.URL, "user", "pass")
	if err := c.Test(context.Background()); err != nil {
		t.Fatalf("expected retry to succeed: %v", err)
	}
	if attempts != 2 {
		t.Errorf("expected 2 attempts (initial + retry), got %d", attempts)
	}
}

// TestSession_409NoHeader verifies that a 409 without a session header fails
// rather than retrying infinitely.
func TestSession_409NoHeader(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusConflict)
	}))
	defer srv.Close()

	c := newTestClient(srv.URL, "user", "pass")
	if err := c.Test(context.Background()); err == nil {
		t.Fatal("expected error when 409 has no session header")
	}
}

func TestDoRequest_RejectsUnexpectedTarget(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"result":"success","arguments":{}}`))
	}))
	defer srv.Close()

	c := newTestClient(srv.URL, "user", "pass")
	req, err := http.NewRequestWithContext(context.Background(), http.MethodPost, srv.URL+"/other", nil)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}

	if _, err := c.doRequest(req); err == nil {
		t.Fatal("expected unexpected target to be rejected")
	}
}

func TestDoRequest_RejectsRedirectToDifferentTarget(t *testing.T) {
	redirected := false
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		redirected = true
		_, _ = w.Write([]byte(`{"result":"success","arguments":{}}`))
	}))
	defer target.Close()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL, http.StatusFound)
	}))
	defer srv.Close()

	c := newTestClient(srv.URL, "user", "pass")
	if err := c.Test(context.Background()); err == nil {
		t.Fatal("expected redirect to different target to be rejected")
	}
	if redirected {
		t.Fatal("unexpectedly followed redirect to different target")
	}
}
