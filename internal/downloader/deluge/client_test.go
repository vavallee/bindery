package deluge_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/vavallee/bindery/internal/downloader/deluge"
)

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
	return deluge.New(parts[0], port, password, false)
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
