package downloader

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/vavallee/bindery/internal/models"
)

// TestGetStalledIDs_QBittorrent_StalledDL verifies that qBittorrent torrents
// in the stalledDL state are reported as stalled and that their hashes are
// lower-cased in the returned map.
func TestGetStalledIDs_QBittorrent_StalledDL(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v2/auth/login":
			_, _ = w.Write([]byte("Ok."))
		case "/api/v2/torrents/info":
			_ = json.NewEncoder(w).Encode([]map[string]any{
				{"hash": "ABCDEF", "state": "stalledDL"},
				{"hash": "123ABC", "state": "downloading"},
				{"hash": "FFFFFF", "state": "StalledDL"},
			})
		default:
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
	}))
	defer srv.Close()

	host, port := serverHostPort(t, srv.URL)
	client := &models.DownloadClient{
		Type: "qbittorrent", Host: host, Port: port,
		Username: "u", Password: "p",
	}

	stalled, usesTorrentID, err := GetStalledIDs(context.Background(), client)
	if err != nil {
		t.Fatalf("GetStalledIDs: %v", err)
	}
	if !usesTorrentID {
		t.Fatal("expected usesTorrentID=true for qbittorrent")
	}
	if len(stalled) != 2 {
		t.Fatalf("expected 2 stalled entries, got %d: %v", len(stalled), stalled)
	}
	if !stalled["abcdef"] {
		t.Error("expected 'abcdef' (lower-cased) to be stalled")
	}
	if !stalled["ffffff"] {
		t.Error("expected case-insensitive match for 'StalledDL'")
	}
	if stalled["123abc"] {
		t.Error("non-stalled torrent incorrectly flagged")
	}
}

// TestGetStalledIDs_QBittorrent_EmptyList verifies a well-formed response
// with no torrents returns an empty (non-nil) map.
func TestGetStalledIDs_QBittorrent_EmptyList(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v2/auth/login":
			_, _ = w.Write([]byte("Ok."))
		case "/api/v2/torrents/info":
			_ = json.NewEncoder(w).Encode([]map[string]any{})
		}
	}))
	defer srv.Close()

	host, port := serverHostPort(t, srv.URL)
	client := &models.DownloadClient{
		Type: "qbittorrent", Host: host, Port: port,
		Username: "u", Password: "p",
	}

	stalled, _, err := GetStalledIDs(context.Background(), client)
	if err != nil {
		t.Fatalf("GetStalledIDs: %v", err)
	}
	if len(stalled) != 0 {
		t.Errorf("expected empty map, got %v", stalled)
	}
}

// TestGetStalledIDs_Transmission_StoppedWithError verifies that Transmission
// torrents in status 0 (stopped) with a non-empty errorString are reported
// as stalled, while other states are not.
func TestGetStalledIDs_Transmission_StoppedWithError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/transmission/rpc" {
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"arguments": map[string]any{
				"torrents": []map[string]any{
					{"id": 1, "status": 0, "errorString": "tracker error"},
					{"id": 2, "status": 0, "errorString": ""},
					{"id": 3, "status": 2, "errorString": "some error"},
					{"id": 4, "status": 0, "errorString": "   "},
				},
			},
			"result": "success",
		})
	}))
	defer srv.Close()

	host, port := serverHostPort(t, srv.URL)
	client := &models.DownloadClient{Type: "transmission", Host: host, Port: port}

	stalled, usesTorrentID, err := GetStalledIDs(context.Background(), client)
	if err != nil {
		t.Fatalf("GetStalledIDs: %v", err)
	}
	if !usesTorrentID {
		t.Fatal("expected usesTorrentID=true for transmission")
	}
	if len(stalled) != 1 {
		t.Fatalf("expected 1 stalled entry, got %d: %v", len(stalled), stalled)
	}
	if !stalled["1"] {
		t.Error("expected transmission id '1' to be stalled")
	}
}

// TestGetStalledIDs_Sabnzbd_NotSupported verifies SABnzbd returns nil map
// with no error — the caller treats this as "nothing stalled".
func TestGetStalledIDs_Sabnzbd_NotSupported(t *testing.T) {
	client := &models.DownloadClient{Type: "sabnzbd", Host: "localhost", Port: 1, APIKey: "k"}
	stalled, usesTorrentID, err := GetStalledIDs(context.Background(), client)
	if err != nil {
		t.Fatalf("GetStalledIDs sabnzbd: %v", err)
	}
	if usesTorrentID {
		t.Error("expected usesTorrentID=false for sabnzbd")
	}
	if stalled != nil {
		t.Errorf("expected nil map for sabnzbd, got %v", stalled)
	}
}

// TestGetStalledIDs_QBittorrent_ServerError surfaces the transport error.
func TestGetStalledIDs_QBittorrent_ServerError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v2/auth/login":
			_, _ = w.Write([]byte("Ok."))
		case "/api/v2/torrents/info":
			http.Error(w, "boom", http.StatusInternalServerError)
		}
	}))
	defer srv.Close()

	host, port := serverHostPort(t, srv.URL)
	client := &models.DownloadClient{
		Type: "qbittorrent", Host: host, Port: port,
		Username: "u", Password: "p",
	}
	if _, _, err := GetStalledIDs(context.Background(), client); err == nil {
		t.Fatal("expected error from 500 response, got nil")
	}
}
