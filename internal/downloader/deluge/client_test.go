package deluge_test

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"syscall"
	"testing"

	"github.com/vavallee/bindery/internal/downloader/deluge"
	"github.com/vavallee/bindery/internal/downloader/infohash"
)

// delugeServer is a minimal Deluge Web UI JSON-RPC stub.
type delugeServer struct {
	t        *testing.T
	password string
	torrents map[string]deluge.TorrentStatus // keyed by lower-cased hash
	// files maps torrent hash to the per-file list returned by
	// core.get_torrent_status with keys=["files"]. Tests that don't care
	// about Files() can leave this nil.
	files map[string][]map[string]any

	// optional overrides
	loginErr     bool
	addMagnetErr bool
	removeFail   bool
	// removeRefused makes core.remove_torrent answer a plain `false` with no
	// RPC error object: Deluge's "I declined, but nothing went wrong on the
	// wire" reply (#2192).
	removeRefused bool
	// nullResult makes core.remove_torrent answer `null` instead of a boolean,
	// which is not a refusal and must not be reported as one.
	nullResult bool
	// statusErr forces core.get_torrent_status to return an RPC error,
	// exercising the Files() error path.
	statusErr bool

	// daemonConnected is what web.connected reports: whether the Web UI
	// session is attached to a deluged daemon (#2204). Defaults to true so the
	// stub behaves like the common auto-connected deployment; web.connect
	// flips it.
	daemonConnected bool
	// hosts is what web.get_hosts returns — Deluge 2.x rows of
	// [id, host, port, username].
	hosts []any
	// connectErr makes web.connect fail, standing in for a daemon that refuses
	// the Web UI's connection.
	connectErr bool
	// connectedCalls counts web.connected; connectCalls records the host ids
	// passed to web.connect, in order.
	connectedCalls int
	connectCalls   []string

	// stopRatio records the ratio passed to core.set_torrent_stop_ratio per
	// hash; stopAtRatio records whether core.set_torrent_stop_at_ratio(true)
	// fired. Both stay zero/false when AddTorrent skips the seed-ratio step.
	stopRatio    map[string]float64
	stopAtRatio  map[string]bool
	stopRatioErr bool

	// addTorrentFileFilename and addTorrentFilePayload record the arguments
	// passed to the most recent core.add_torrent_file call.
	addTorrentFileFilename string
	addTorrentFilePayload  string

	// rejectDuplicates makes both add methods answer an add of a torrent the
	// session already holds the way Deluge 2.x does: an AddTorrentError RPC
	// error instead of the hash.
	rejectDuplicates bool
	// alwaysDuplicate answers every add with that error, even for a torrent
	// the session does not hold.
	alwaysDuplicate bool
	// nullOnDuplicate answers an add of a known torrent with null, which is
	// what Deluge 1.3 returns when libtorrent refuses the duplicate.
	nullOnDuplicate bool
	// realFileHash makes core.add_torrent_file report the payload's real v1
	// infohash instead of the fixed placeholder.
	realFileHash bool
}

// duplicate answers an add of hash the way the configured Deluge does when
// the session already holds it, and reports whether it wrote a reply.
func (s *delugeServer) duplicate(hash string, write func(any), writeErr func(string)) bool {
	_, known := s.torrents[hash]
	switch {
	case s.alwaysDuplicate || (s.rejectDuplicates && known):
		writeErr("AddTorrentError: Torrent already in session (" + hash + ").")
		return true
	case s.nullOnDuplicate && known:
		write(nil)
		return true
	}
	return false
}

func (s *delugeServer) handler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/json" {
			http.Error(w, "not found", http.StatusNotFound)
			return
		}
		var req struct {
			Method string            `json:"method"`
			Params []json.RawMessage `json:"params"`
			ID     int64             `json:"id"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}

		write := func(result any) {
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(map[string]any{
				"result": result,
				"error":  nil,
				"id":     req.ID,
			})
		}
		writeErr := func(msg string) {
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(map[string]any{
				"result": nil,
				"error":  map[string]any{"code": -1, "message": msg},
				"id":     req.ID,
			})
		}

		switch req.Method {
		case "auth.login":
			if s.loginErr {
				writeErr("auth error")
				return
			}
			var pw string
			json.Unmarshal(req.Params[0], &pw)
			write(pw == s.password)

		case "web.connected":
			s.connectedCalls++
			write(s.daemonConnected)

		case "web.get_hosts":
			write(s.hosts)

		case "web.connect":
			if s.connectErr {
				writeErr("Failed to connect to daemon")
				return
			}
			var id string
			json.Unmarshal(req.Params[0], &id)
			s.connectCalls = append(s.connectCalls, id)
			s.daemonConnected = true
			// The real method answers with the daemon's method list.
			write([]string{"core.add_torrent_magnet", "core.get_torrents_status"})

		case "core.add_torrent_magnet":
			if !s.daemonConnected {
				// What deluge-web actually does with an unattached session:
				// there is no daemon to proxy the call to.
				writeErr("Not connected to a daemon")
				return
			}
			if s.addMagnetErr {
				writeErr("add error")
				return
			}
			var magnet string
			json.Unmarshal(req.Params[0], &magnet)
			// Extract hash from urn:btih:HASH
			hash := "aabbccddeeff00112233445566778899aabbccdd"
			if idx := strings.Index(strings.ToLower(magnet), "urn:btih:"); idx >= 0 {
				hash = strings.ToLower(magnet[idx+9:])
				if i := strings.Index(hash, "&"); i >= 0 {
					hash = hash[:i]
				}
			}
			// Deluge stores and reports the hex hash whichever spelling the
			// magnet used.
			if n := infohash.Normalize(hash); n != "" {
				hash = n
			}
			if s.duplicate(hash, write, writeErr) {
				return
			}
			s.torrents[hash] = deluge.TorrentStatus{Hash: hash, State: "Downloading", Progress: 0}
			write(hash)

		case "core.add_torrent_file":
			const newHash = "deadbeefdeadbeefdeadbeefdeadbeefdeadbeef"
			if len(req.Params) >= 2 {
				json.Unmarshal(req.Params[0], &s.addTorrentFileFilename)
				json.Unmarshal(req.Params[1], &s.addTorrentFilePayload)
			}
			hash := newHash
			if s.realFileHash {
				if raw, err := base64.StdEncoding.DecodeString(s.addTorrentFilePayload); err == nil {
					if h := infohash.FromTorrentFile(raw); h != "" {
						hash = h
					}
				}
			}
			if s.duplicate(hash, write, writeErr) {
				return
			}
			s.torrents[hash] = deluge.TorrentStatus{Hash: hash, State: "Downloading", Progress: 10}
			write(hash)

		case "label.set_torrent":
			write(nil)

		case "core.set_torrent_stop_ratio":
			if s.stopRatioErr {
				writeErr("set stop ratio error")
				return
			}
			var hash string
			var ratio float64
			json.Unmarshal(req.Params[0], &hash)
			json.Unmarshal(req.Params[1], &ratio)
			if s.stopRatio == nil {
				s.stopRatio = make(map[string]float64)
			}
			s.stopRatio[strings.ToLower(hash)] = ratio
			write(nil)

		case "core.set_torrent_stop_at_ratio":
			var hash string
			var enabled bool
			json.Unmarshal(req.Params[0], &hash)
			json.Unmarshal(req.Params[1], &enabled)
			if s.stopAtRatio == nil {
				s.stopAtRatio = make(map[string]bool)
			}
			s.stopAtRatio[strings.ToLower(hash)] = enabled
			write(nil)

		case "core.get_torrents_status":
			// Honour an {"id": [...]} filter the way Deluge does: ids the
			// session does not hold are dropped, not reported as an error.
			var filter map[string]any
			if len(req.Params) > 0 {
				json.Unmarshal(req.Params[0], &filter)
			}
			ids, ok := filter["id"].([]any)
			if !ok {
				write(s.torrents)
				return
			}
			out := map[string]deluge.TorrentStatus{}
			for _, id := range ids {
				if h, ok := id.(string); ok {
					if st, ok := s.torrents[h]; ok {
						out[h] = st
					}
				}
			}
			write(out)

		case "core.get_torrent_status":
			if s.statusErr {
				writeErr("status error")
				return
			}
			var hash string
			json.Unmarshal(req.Params[0], &hash)
			hash = strings.ToLower(hash)
			files, ok := s.files[hash]
			if !ok {
				// Unknown hash: Deluge surfaces this as a KeyError RPC.
				writeErr("torrent not found")
				return
			}
			write(map[string]any{"files": files})

		case "core.remove_torrent":
			var hash string
			json.Unmarshal(req.Params[0], &hash)
			if s.removeRefused {
				write(false)
				return
			}
			if s.nullResult {
				write(nil)
				return
			}
			delete(s.torrents, hash)
			if s.removeFail {
				writeErr("remove failed")
				return
			}
			write(true)

		default:
			writeErr("unknown method: " + req.Method)
		}
	}
}

func newTestServer(t *testing.T, password string) (*httptest.Server, *delugeServer) {
	t.Helper()
	ds := &delugeServer{
		t:        t,
		password: password,
		torrents: make(map[string]deluge.TorrentStatus),
		files:    make(map[string][]map[string]any),
		// Default to the deployment shape most people have: deluge-web already
		// auto-connected to its single local daemon.
		daemonConnected: true,
		hosts:           []any{[]any{"hostid-local", "127.0.0.1", 58846, "localclient"}},
	}
	srv := httptest.NewServer(ds.handler())
	t.Cleanup(srv.Close)
	return srv, ds
}

func clientFromServer(srv *httptest.Server, password string) *deluge.Client {
	// Parse host/port from the test server URL.
	addr := strings.TrimPrefix(srv.URL, "http://")
	parts := strings.SplitN(addr, ":", 2)
	port := 80
	if len(parts) == 2 {
		_, _ = parts[1], &port
		p := 0
		for _, c := range parts[1] {
			p = p*10 + int(c-'0')
		}
		port = p
	}
	return deluge.New(parts[0], port, password, "", false)
}

func TestLogin_Success(t *testing.T) {
	srv, _ := newTestServer(t, "secret")
	c := clientFromServer(srv, "secret")
	if err := c.Login(context.Background()); err != nil {
		t.Fatalf("expected login to succeed: %v", err)
	}
}

func TestLogin_WrongPassword(t *testing.T) {
	srv, _ := newTestServer(t, "secret")
	c := clientFromServer(srv, "wrong")
	if err := c.Login(context.Background()); err == nil {
		t.Fatal("expected login to fail with wrong password")
	}
}

func TestTest_Success(t *testing.T) {
	srv, _ := newTestServer(t, "pw")
	c := clientFromServer(srv, "pw")
	if err := c.Test(context.Background()); err != nil {
		t.Fatalf("Test() failed: %v", err)
	}
}

func TestAddTorrent_Magnet(t *testing.T) {
	srv, ds := newTestServer(t, "pw")
	c := clientFromServer(srv, "pw")

	magnet := "magnet:?xt=urn:btih:aabbccddeeff00112233445566778899aabbccdd&dn=Test+Book"
	hash, err := c.AddTorrent(context.Background(), magnet, "books", nil)
	if err != nil {
		t.Fatalf("AddTorrent magnet: %v", err)
	}
	if hash != "aabbccddeeff00112233445566778899aabbccdd" {
		t.Errorf("unexpected hash %q", hash)
	}
	if _, ok := ds.torrents[hash]; !ok {
		t.Error("torrent not found in server after add")
	}
}

func ratioPtr(f float64) *float64 { return &f }

// TestAddTorrent_SeedRatio_Positive: a non-negative override is applied via
// core.set_torrent_stop_ratio and ratio-stopping is enabled.
func TestAddTorrent_SeedRatio_Positive(t *testing.T) {
	srv, ds := newTestServer(t, "pw")
	c := clientFromServer(srv, "pw")

	magnet := "magnet:?xt=urn:btih:aabbccddeeff00112233445566778899aabbccdd"
	hash, err := c.AddTorrent(context.Background(), magnet, "", ratioPtr(2))
	if err != nil {
		t.Fatalf("AddTorrent: %v", err)
	}
	if got, ok := ds.stopRatio[hash]; !ok || got != 2 {
		t.Errorf("stopRatio[%s] = %v (set=%v), want 2", hash, got, ok)
	}
	if !ds.stopAtRatio[hash] {
		t.Error("set_torrent_stop_at_ratio(true) should have fired for a positive override")
	}
}

// TestAddTorrent_SeedRatio_Unlimited: the -1 sentinel must NOT call
// set_torrent_stop_ratio (Deluge rejects negatives) — the global default stays.
func TestAddTorrent_SeedRatio_Unlimited(t *testing.T) {
	srv, ds := newTestServer(t, "pw")
	c := clientFromServer(srv, "pw")

	magnet := "magnet:?xt=urn:btih:aabbccddeeff00112233445566778899aabbccdd"
	hash, err := c.AddTorrent(context.Background(), magnet, "", ratioPtr(-1))
	if err != nil {
		t.Fatalf("AddTorrent: %v", err)
	}
	if _, ok := ds.stopRatio[hash]; ok {
		t.Error("stopRatio must not be set for the unlimited sentinel")
	}
}

// TestAddTorrent_SeedRatio_Unset: a nil override skips the ratio call entirely.
func TestAddTorrent_SeedRatio_Unset(t *testing.T) {
	srv, ds := newTestServer(t, "pw")
	c := clientFromServer(srv, "pw")

	magnet := "magnet:?xt=urn:btih:aabbccddeeff00112233445566778899aabbccdd"
	hash, err := c.AddTorrent(context.Background(), magnet, "", nil)
	if err != nil {
		t.Fatalf("AddTorrent: %v", err)
	}
	if _, ok := ds.stopRatio[hash]; ok {
		t.Error("stopRatio must not be set when no override is provided")
	}
}

// TestAddTorrent_SeedRatio_ErrorNonFatal: a failure setting the ratio must not
// fail the grab — the torrent is already added.
func TestAddTorrent_SeedRatio_ErrorNonFatal(t *testing.T) {
	srv, ds := newTestServer(t, "pw")
	ds.stopRatioErr = true
	c := clientFromServer(srv, "pw")

	magnet := "magnet:?xt=urn:btih:aabbccddeeff00112233445566778899aabbccdd"
	if _, err := c.AddTorrent(context.Background(), magnet, "", ratioPtr(2)); err != nil {
		t.Fatalf("AddTorrent must succeed despite a ratio-set failure: %v", err)
	}
}

// TestAddTorrent_URL verifies that AddTorrent with a .torrent URL causes
// Bindery to fetch the bytes and submit them via core.add_torrent_file.
// The returned hash must match what the RPC stub returns, and the payload
// must be a non-empty base64 string.
func TestAddTorrent_URL(t *testing.T) {
	// Minimal valid bencoded .torrent bytes (just enough to be non-empty).
	torrentBytes := []byte("d4:infod4:name9:test.m4be4:lengthi0eee")

	// Serve the .torrent file over HTTP.
	indexer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/x-bittorrent")
		w.WriteHeader(http.StatusOK)
		w.Write(torrentBytes)
	}))
	t.Cleanup(indexer.Close)

	srv, ds := newTestServer(t, "pw")
	c := clientFromServer(srv, "pw")
	// Allow loopback so the test server URL passes the SSRF check.
	c.SetValidateTorrentURL(func(string) error { return nil })

	hash, err := c.AddTorrent(context.Background(), indexer.URL+"/test.torrent", "books", nil)
	if err != nil {
		t.Fatalf("AddTorrent URL: %v", err)
	}
	const wantHash = "deadbeefdeadbeefdeadbeefdeadbeefdeadbeef"
	if hash != wantHash {
		t.Errorf("hash = %q, want %q", hash, wantHash)
	}
	if ds.addTorrentFilePayload == "" {
		t.Error("core.add_torrent_file was not called")
	}
	if ds.addTorrentFileFilename != "test.torrent" {
		t.Errorf("filename = %q, want %q", ds.addTorrentFileFilename, "test.torrent")
	}
}

// TestAddTorrent_URL_MagnetRedirect verifies that when the indexer responds
// with a 302 redirect to a magnet: URI, AddTorrent transparently hands off
// to the magnet path and returns the hash extracted from the magnet link.
func TestAddTorrent_URL_MagnetRedirect(t *testing.T) {
	const magnet = "magnet:?xt=urn:btih:aabbccddeeff00112233445566778899aabbccdd&dn=Test+Book"

	indexer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, magnet, http.StatusFound)
	}))
	t.Cleanup(indexer.Close)

	srv, _ := newTestServer(t, "pw")
	c := clientFromServer(srv, "pw")
	c.SetValidateTorrentURL(func(string) error { return nil })

	hash, err := c.AddTorrent(context.Background(), indexer.URL+"/redir.torrent", "", nil)
	if err != nil {
		t.Fatalf("AddTorrent magnet-redirect: %v", err)
	}
	const wantHash = "aabbccddeeff00112233445566778899aabbccdd"
	if hash != wantHash {
		t.Errorf("hash = %q, want %q", hash, wantHash)
	}
}

func TestGetTorrents(t *testing.T) {
	srv, ds := newTestServer(t, "pw")
	c := clientFromServer(srv, "pw")

	ds.torrents["abc123"] = deluge.TorrentStatus{Hash: "abc123", State: "Downloading", Progress: 50}

	torrents, err := c.GetTorrents(context.Background())
	if err != nil {
		t.Fatalf("GetTorrents: %v", err)
	}
	if _, ok := torrents["abc123"]; !ok {
		t.Error("expected abc123 in torrents map")
	}
}

func TestRemoveTorrent(t *testing.T) {
	srv, ds := newTestServer(t, "pw")
	c := clientFromServer(srv, "pw")

	ds.torrents["deadbeef"] = deluge.TorrentStatus{Hash: "deadbeef", State: "Seeding"}

	if err := c.RemoveTorrent(context.Background(), "deadbeef", false); err != nil {
		t.Fatalf("RemoveTorrent: %v", err)
	}
	if _, ok := ds.torrents["deadbeef"]; ok {
		t.Error("torrent should have been removed")
	}
}

func TestGetTorrents_StalledState(t *testing.T) {
	srv, ds := newTestServer(t, "pw")
	c := clientFromServer(srv, "pw")

	ds.torrents["errhash"] = deluge.TorrentStatus{Hash: "errhash", State: "Error"}
	ds.torrents["goodhash"] = deluge.TorrentStatus{Hash: "goodhash", State: "Downloading"}

	torrents, err := c.GetTorrents(context.Background())
	if err != nil {
		t.Fatalf("GetTorrents: %v", err)
	}
	if strings.ToLower(torrents["errhash"].State) != "error" {
		t.Error("expected Error state")
	}
}

// roundTripFunc is a test helper that implements http.RoundTripper via a function.
type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

// delugeNetTimeoutErr is a minimal net.Error that signals a timeout.
type delugeNetTimeoutErr struct{}

func (e *delugeNetTimeoutErr) Error() string   { return "i/o timeout" }
func (e *delugeNetTimeoutErr) Timeout() bool   { return true }
func (e *delugeNetTimeoutErr) Temporary() bool { return true }

// TestTest_DNSNotFound verifies that a DNS lookup failure on Deluge appends the
// Docker network hint.
func TestTest_DNSNotFound(t *testing.T) {
	dnsErr := &net.DNSError{Name: "deluge-container", IsNotFound: true}
	c := deluge.New("deluge-container", 8112, "pw", "", false)
	c.SetHTTPTransport(roundTripFunc(func(*http.Request) (*http.Response, error) {
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
	c := deluge.New("127.0.0.1", 8112, "pw", "", false)
	c.SetHTTPTransport(roundTripFunc(func(*http.Request) (*http.Response, error) {
		return nil, fmt.Errorf("dial tcp: %w", syscall.ECONNREFUSED)
	}))

	err := c.Test(context.Background())
	if err == nil {
		t.Fatal("expected error")
		return
	}
	if !strings.Contains(err.Error(), "check the port") {
		t.Errorf("expected port hint, got: %q", err.Error())
	}
}

// TestTest_Timeout verifies that a timeout error appends the firewall hint.
func TestTest_Timeout(t *testing.T) {
	c := deluge.New("10.0.0.1", 8112, "pw", "", false)
	c.SetHTTPTransport(roundTripFunc(func(*http.Request) (*http.Response, error) {
		return nil, &delugeNetTimeoutErr{}
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

// TestTest_ServerError_NoHint verifies that a server-side error (bad password,
// HTTP 500, etc.) does NOT append any network hint.
func TestTest_ServerError_NoHint(t *testing.T) {
	srv, _ := newTestServer(t, "correct")
	// Use the wrong password so the server rejects login (application error, not
	// a transport failure).
	c := clientFromServer(srv, "wrong")

	err := c.Test(context.Background())
	if err == nil {
		t.Fatal("expected error")
		return
	}
	msg := err.Error()
	for _, hint := range []string{"Docker network", "check the port", "firewall or proxy"} {
		if strings.Contains(msg, hint) {
			t.Errorf("server-side error must not produce hint %q; got: %q", hint, msg)
		}
	}
}

// TestClient_Files_ReturnsRelativeNames verifies that Files() decodes the
// core.get_torrent_status "files" entries into the importer's []File shape
// with paths left relative to the torrent's save_path. Issue #903 regression
// guard.
func TestClient_Files_ReturnsRelativeNames(t *testing.T) {
	srv, ds := newTestServer(t, "pw")
	c := clientFromServer(srv, "pw")

	const hash = "deadbeefdeadbeefdeadbeefdeadbeefdeadbeef"
	ds.files[hash] = []map[string]any{
		{"index": 0, "offset": 0, "path": "MyBook/disc01.m4b", "size": 1024},
		{"index": 1, "offset": 1024, "path": "MyBook/disc02.m4b", "size": 2048},
		{"index": 2, "offset": 3072, "path": "MyBook/cover.jpg", "size": 128},
	}

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

// TestClient_Files_SingleFileTorrent — the issue #903 shape: a single
// loose file at the save root. The path entry is just the basename.
func TestClient_Files_SingleFileTorrent(t *testing.T) {
	srv, ds := newTestServer(t, "pw")
	c := clientFromServer(srv, "pw")

	const hash = "11111111111111111111111111111111aaaabbbb"
	ds.files[hash] = []map[string]any{
		{"index": 0, "offset": 0, "path": "standalone-book.m4b", "size": 50000},
	}

	files, err := c.Files(context.Background(), hash)
	if err != nil {
		t.Fatalf("Files: %v", err)
	}
	if len(files) != 1 || files[0].Name != "standalone-book.m4b" {
		t.Errorf("expected single basename-only file, got %+v", files)
	}
}

// TestClient_Files_TorrentMissing — Deluge surfaces an unknown hash as a
// KeyError RPC fault. Files() returns an error so the importer can fall
// back to the directory walk with a WARN log.
func TestClient_Files_TorrentMissing(t *testing.T) {
	srv, _ := newTestServer(t, "pw")
	c := clientFromServer(srv, "pw")

	if _, err := c.Files(context.Background(), "nonexistent-hash"); err == nil {
		t.Fatal("expected error for unknown torrent hash")
	}
}

// TestClient_Files_StatusErrorReturnsError — when core.get_torrent_status
// itself errors (e.g. transient daemon issue) Files() must surface the
// error rather than silently returning empty.
func TestClient_Files_StatusErrorReturnsError(t *testing.T) {
	srv, ds := newTestServer(t, "pw")
	ds.statusErr = true
	c := clientFromServer(srv, "pw")

	if _, err := c.Files(context.Background(), "any-hash"); err == nil {
		t.Fatal("expected error when status RPC fails")
	}
}
