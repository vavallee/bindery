package transmission

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"syscall"
	"testing"
)

// newJSONDecoder is a wrapper so tests don't import encoding/json
// alongside the helpers; keeps the helper section narrow.
func newJSONDecoder(r io.Reader) *json.Decoder { return json.NewDecoder(r) }

// decodeBase64 is a wrapper around base64.StdEncoding.DecodeString.
func decodeBase64(s string) ([]byte, error) { return base64.StdEncoding.DecodeString(s) }

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

// TestClient_Files_ReturnsRelativeNames verifies that Files() decodes the
// torrent-get RPC "files" entries into the importer's []File shape with
// names left in the form Transmission reported them (relative to the
// torrent's downloadDir). Issue #903 regression guard: the importer relies
// on these names being the authoritative file list of the torrent so it
// can avoid walking the shared download root.
func TestClient_Files_ReturnsRelativeNames(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{
			"result":"success",
			"arguments":{
				"torrents":[{
					"id":42,
					"files":[
						{"name":"MyBook/disc01.m4b","length":1024,"bytesCompleted":1024},
						{"name":"MyBook/disc02.m4b","length":2048,"bytesCompleted":2048},
						{"name":"MyBook/cover.jpg","length":128,"bytesCompleted":128}
					]
				}]
			}
		}`))
	}))
	defer srv.Close()

	c := newTestClient(srv.URL, "user", "pass")
	files, err := c.Files(context.Background(), 42)
	if err != nil {
		t.Fatalf("Files: %v", err)
	}
	if len(files) != 3 {
		t.Fatalf("want 3 files, got %d", len(files))
	}
	if files[0].Name != "MyBook/disc01.m4b" || files[0].Size != 1024 {
		t.Errorf("file 0 mismatch: %+v", files[0])
	}
	if files[2].Name != "MyBook/cover.jpg" || files[2].Size != 128 {
		t.Errorf("file 2 mismatch: %+v", files[2])
	}
}

// TestClient_Files_SingleFileTorrent — issue #903 specific shape: a torrent
// containing a single loose file with no subfolder. The Name is just the
// basename; joining it with downloadDir yields the file's on-disk path.
func TestClient_Files_SingleFileTorrent(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{
			"result":"success",
			"arguments":{
				"torrents":[{
					"id":7,
					"files":[
						{"name":"standalone-book.m4b","length":50000,"bytesCompleted":50000}
					]
				}]
			}
		}`))
	}))
	defer srv.Close()

	c := newTestClient(srv.URL, "user", "pass")
	files, err := c.Files(context.Background(), 7)
	if err != nil {
		t.Fatalf("Files: %v", err)
	}
	if len(files) != 1 {
		t.Fatalf("want 1 file, got %d", len(files))
	}
	if files[0].Name != "standalone-book.m4b" {
		t.Errorf("expected basename-only Name, got %q", files[0].Name)
	}
}

// TestClient_Files_TorrentMissing — when torrent-get returns an empty torrents
// array Transmission has no record of the supplied id (already removed, or
// the supplied id was wrong). The caller needs a distinct signal so the
// importer can decide whether to retry the next cycle or terminally fail.
func TestClient_Files_TorrentMissing(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"result":"success","arguments":{"torrents":[]}}`))
	}))
	defer srv.Close()

	c := newTestClient(srv.URL, "user", "pass")
	_, err := c.Files(context.Background(), 999)
	if err == nil {
		t.Fatal("expected error for unknown torrent id")
	}
	if !strings.Contains(err.Error(), "not found") {
		t.Errorf("expected 'not found' in error, got: %v", err)
	}
}

// TestClient_Files_NoFilesYet — a torrent that is still resolving metadata
// (e.g. magnet at 0%) appears in torrent-get but with an empty files array.
// Files() returns an empty slice with no error so the importer can retry
// next cycle once the metadata flushes.
func TestClient_Files_NoFilesYet(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{
			"result":"success",
			"arguments":{
				"torrents":[{"id":1,"files":[]}]
			}
		}`))
	}))
	defer srv.Close()

	c := newTestClient(srv.URL, "user", "pass")
	files, err := c.Files(context.Background(), 1)
	if err != nil {
		t.Fatalf("Files: %v", err)
	}
	if len(files) != 0 {
		t.Errorf("want empty file list, got %d entries", len(files))
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

// roundTripFunc is a test helper that implements http.RoundTripper via a function.
type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

// netTimeoutErr is a minimal net.Error that signals a timeout.
type netTimeoutErr struct{}

func (e *netTimeoutErr) Error() string   { return "i/o timeout" }
func (e *netTimeoutErr) Timeout() bool   { return true }
func (e *netTimeoutErr) Temporary() bool { return true }

// newTransportClient creates a test Client with a custom HTTP transport.
// Unlike newTestClient, this does not short-circuit via a real server URL —
// the transport is responsible for all responses.
func newTransportClient(transport http.RoundTripper) *Client {
	c := New("fake-host", 9091, "", "", "", false)
	parsed, _ := url.Parse("http://fake-host:9091/transmission/rpc")
	c.rpcURL = parsed
	c.baseURL = parsed.String()
	c.http = &http.Client{Transport: transport}
	// CheckRedirect was set in New(); replace http.Client entirely so we keep
	// the CheckRedirect behaviour by setting it again.
	c.http.CheckRedirect = c.checkRedirect
	return c
}

// TestTest_DNSNotFound verifies that a DNS lookup failure appends the Docker
// network hint.
func TestTest_DNSNotFound(t *testing.T) {
	dnsErr := &net.DNSError{Name: "transmission-container", IsNotFound: true}
	c := newTransportClient(roundTripFunc(func(*http.Request) (*http.Response, error) {
		return nil, fmt.Errorf("dial: %w", dnsErr)
	}))

	err := c.Test(context.Background())
	if err == nil {
		t.Fatal("expected error")
		return
	}
	if !strings.Contains(err.Error(), "same Docker network") {
		t.Errorf("expected Docker network hint, got: %q", err.Error())
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

// TestTest_Timeout verifies that a timeout error appends the firewall hint.
func TestTest_Timeout(t *testing.T) {
	c := newTransportClient(roundTripFunc(func(*http.Request) (*http.Response, error) {
		return nil, &netTimeoutErr{}
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

// TestTest_ServerError_NoHint verifies that a clean HTTP 500 does NOT append
// any network hint.
func TestTest_ServerError_NoHint(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte("internal error"))
	}))
	defer srv.Close()

	c := newTestClient(srv.URL, "user", "pass")
	err := c.Test(context.Background())
	if err == nil {
		t.Fatal("expected error")
		return
	}
	msg := err.Error()
	for _, hint := range []string{"Docker network", "host firewall is rejecting", "firewall or proxy"} {
		if strings.Contains(msg, hint) {
			t.Errorf("clean server error must not produce hint %q; got: %q", hint, msg)
		}
	}
}

const testTorrentContent = "d8:announce32:http://tracker.example.com/announce4:infod6:lengthi32e4:name8:test.txt12:piece lengthi32e6:pieces20:" +
	"01234567890123456789ee"

// allowTorrentFetch bypasses the SSRF guard so a test can point at a
// loopback httptest server. Mirrors allowNZBFetch in the sabnzbd tests.
func allowTorrentFetch(c *Client) {
	c.validateTorrentURL = func(string) error { return nil }
}

// readRPCArgs decodes the JSON-RPC request body and returns the arguments
// map. Used to assert against the metainfo/filename keys Transmission sees.
func readRPCArgs(t *testing.T, r *http.Request) map[string]any {
	t.Helper()
	body := struct {
		Method    string         `json:"method"`
		Arguments map[string]any `json:"arguments"`
	}{}
	if err := decodeJSONBody(r, &body); err != nil {
		t.Fatalf("decode rpc body: %v", err)
	}
	return body.Arguments
}

func decodeJSONBody(r *http.Request, dst any) error {
	defer r.Body.Close()
	dec := newJSONDecoder(r.Body)
	return dec.Decode(dst)
}

// TestAddTorrent_HTTPURLFetchesContent verifies that an http(s) URL is
// fetched by Bindery (not Transmission) and submitted as metainfo. This
// is the fix path for the VPN-isolated-transmission case in #873.
func TestAddTorrent_HTTPURLFetchesContent(t *testing.T) {
	indexerSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/x-bittorrent")
		_, _ = w.Write([]byte(testTorrentContent))
	}))
	defer indexerSrv.Close()

	var gotArgs map[string]any
	transSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotArgs = readRPCArgs(t, r)
		_, _ = w.Write([]byte(`{"result":"success","arguments":{"torrent-added":{"id":42,"name":"x"}}}`))
	}))
	defer transSrv.Close()

	c := newTestClient(transSrv.URL, "", "")
	allowTorrentFetch(c)

	id, err := c.AddTorrent(context.Background(), indexerSrv.URL+"/file.torrent", "")
	if err != nil {
		t.Fatalf("AddTorrent: %v", err)
	}
	if id != 42 {
		t.Errorf("id = %d, want 42", id)
	}
	if gotArgs["filename"] != nil {
		t.Errorf("filename arg must not be set when content is sent via metainfo; got: %v", gotArgs["filename"])
	}
	meta, ok := gotArgs["metainfo"].(string)
	if !ok || meta == "" {
		t.Fatalf("metainfo arg missing or non-string; got: %v", gotArgs["metainfo"])
	}
	decoded, err := decodeBase64(meta)
	if err != nil {
		t.Fatalf("metainfo not base64: %v", err)
	}
	if string(decoded) != testTorrentContent {
		t.Errorf("metainfo payload mismatch:\n want %q\n got  %q", testTorrentContent, string(decoded))
	}
}

// TestAddTorrent_MagnetLinkSkipsFetch verifies the magnet-link
// pass-through preserves the old behaviour: no HTTP fetch, magnet URL
// goes directly into Transmission's filename arg.
func TestAddTorrent_MagnetLinkSkipsFetch(t *testing.T) {
	fetchCalled := false
	indexerSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fetchCalled = true
	}))
	defer indexerSrv.Close()

	var gotArgs map[string]any
	transSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotArgs = readRPCArgs(t, r)
		_, _ = w.Write([]byte(`{"result":"success","arguments":{"torrent-added":{"id":7,"name":"m"}}}`))
	}))
	defer transSrv.Close()

	c := newTestClient(transSrv.URL, "", "")
	allowTorrentFetch(c)

	magnet := "magnet:?xt=urn:btih:abc123&tr=" + url.QueryEscape(indexerSrv.URL)
	id, err := c.AddTorrent(context.Background(), magnet, "")
	if err != nil {
		t.Fatalf("AddTorrent: %v", err)
	}
	if id != 7 {
		t.Errorf("id = %d, want 7", id)
	}
	if fetchCalled {
		t.Error("magnet link path must not fetch the URL through Bindery")
	}
	if gotArgs["metainfo"] != nil {
		t.Errorf("magnet path must not set metainfo; got: %v", gotArgs["metainfo"])
	}
	if gotArgs["filename"] != magnet {
		t.Errorf("magnet path filename mismatch; got: %v", gotArgs["filename"])
	}
}

// TestAddTorrent_HTTPURLFetchFailure verifies that when the indexer
// returns an error, AddTorrent fails at the fetch step and never reaches
// Transmission. This is the diagnostic path: a clear "indexer returned
// HTTP X" beats Transmission timing out 15 seconds later.
func TestAddTorrent_HTTPURLFetchFailure(t *testing.T) {
	indexerSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
	}))
	defer indexerSrv.Close()

	transSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("Transmission must not be called when torrent fetch fails")
	}))
	defer transSrv.Close()

	c := newTestClient(transSrv.URL, "", "")
	allowTorrentFetch(c)

	_, err := c.AddTorrent(context.Background(), indexerSrv.URL+"/file.torrent", "")
	if err == nil {
		t.Fatal("expected error on indexer 401")
		return
	}
	if !strings.Contains(err.Error(), "401") {
		t.Errorf("expected HTTP 401 in error; got: %v", err)
	}
}

// TestAddTorrent_HTTPURLSSRFBlocked verifies the validateTorrentURL guard
// rejects loopback URLs in production (when the test bypass is not
// applied). Symmetric with the SAB and NZBGet SSRF tests.
func TestAddTorrent_HTTPURLSSRFBlocked(t *testing.T) {
	transSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("Transmission must not be called when SSRF guard blocks the indexer URL")
	}))
	defer transSrv.Close()

	c := newTestClient(transSrv.URL, "", "")
	// Do NOT call allowTorrentFetch; the guard must be active.

	_, err := c.AddTorrent(context.Background(), "http://127.0.0.1:9999/file.torrent", "")
	if err == nil {
		t.Fatal("expected SSRF guard to block loopback URL")
		return
	}
	if !strings.Contains(err.Error(), "url not allowed") {
		t.Errorf("expected 'url not allowed' in error; got: %v", err)
	}
}

// TestIsMagnetLink verifies the scheme detection that switches between
// pass-through and fetch-then-submit. Tested independently so a future
// refactor that breaks the detection (e.g. case-sensitivity) shows up
// here rather than as a Transmission-side failure.
func TestIsMagnetLink(t *testing.T) {
	cases := map[string]bool{
		"magnet:?xt=urn:btih:abc":  true,
		"MAGNET:?xt=urn:btih:abc":  true,
		"  magnet:?xt=urn:btih:a ": true,
		"http://x/file.torrent":    false,
		"https://x/file.torrent":   false,
		"":                         false,
		"magneturl":                false,
	}
	for in, want := range cases {
		if got := isMagnetLink(in); got != want {
			t.Errorf("isMagnetLink(%q) = %v, want %v", in, got, want)
		}
	}
}
