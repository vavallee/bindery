package deluge_test

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/vavallee/bindery/internal/downloader/deluge"
)

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

// delugeServer is a minimal Deluge Web UI JSON-RPC stub.
type delugeServer struct {
	t        *testing.T
	password string
	torrents map[string]deluge.TorrentStatus // keyed by lower-cased hash

	// optional overrides
	loginErr     bool
	addMagnetErr bool
	removeFail   bool
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

		case "core.add_torrent_magnet":
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
			s.torrents[hash] = deluge.TorrentStatus{Hash: hash, State: "Downloading", Progress: 0}
			write(hash)

		case "web.download_torrent_from_url":
			write("/tmp/bindery_test.torrent")

		case "web.add_torrents":
			const newHash = "deadbeefdeadbeefdeadbeefdeadbeefdeadbeef"
			s.torrents[newHash] = deluge.TorrentStatus{Hash: newHash, State: "Downloading", Progress: 10}
			write([]bool{true})

		case "label.set_torrent":
			write(nil)

		case "core.get_torrents_status":
			write(s.torrents)

		case "core.remove_torrent":
			var hash string
			json.Unmarshal(req.Params[0], &hash)
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
	hash, err := c.AddTorrent(context.Background(), magnet, "books")
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

func TestAddTorrent_URL(t *testing.T) {
	srv, _ := newTestServer(t, "pw")
	c := clientFromServer(srv, "pw")

	hash, err := c.AddTorrent(context.Background(), "http://indexer.example/torrent.torrent", "")
	if err != nil {
		t.Fatalf("AddTorrent URL: %v", err)
	}
	if hash == "" {
		t.Error("expected non-empty hash")
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

// TestAddTorrent_URL_HashLookupTimeout verifies that when the torrent URL path
// never produces a new hash within the deadline, an ERROR is logged with
// before/after hash lists and the appropriate error is returned.
func TestAddTorrent_URL_HashLookupTimeout(t *testing.T) {
	restore := deluge.SetHashPollTimeout(50 * time.Millisecond)
	t.Cleanup(restore)

	catcher := &logCatcher{}
	origLogger := slog.Default()
	slog.SetDefault(slog.New(catcher))
	t.Cleanup(func() { slog.SetDefault(origLogger) })

	// Build a minimal server that accepts the add flow but never exposes a
	// new torrent in core.get_torrents_status, so the poll times out.
	existing := map[string]deluge.TorrentStatus{
		"existinghash": {Hash: "existinghash", State: "Seeding"},
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
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
			_ = json.NewEncoder(w).Encode(map[string]any{"result": result, "error": nil, "id": req.ID})
		}
		switch req.Method {
		case "auth.login":
			write(true)
		case "web.download_torrent_from_url":
			write("/tmp/test.torrent")
		case "web.add_torrents":
			// Accept the add but do NOT insert into existing — torrent never appears.
			write([]bool{true})
		case "core.get_torrents_status":
			write(existing)
		default:
			write(nil)
		}
	}))
	t.Cleanup(srv.Close)

	c := clientFromServer(srv, "pw")
	_, err := c.AddTorrent(context.Background(), "http://example.com/book.torrent", "scifi")
	if err == nil {
		t.Fatal("expected error on timeout, got nil")
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
