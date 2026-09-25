package downloader

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/vavallee/bindery/internal/models"
)

// TestGetStalledTorrents_QBittorrent_StalledDL verifies that qBittorrent torrents
// in the stalledDL state are reported as stalled and that their hashes are
// lower-cased in the returned map.
func TestGetStalledTorrents_QBittorrent_StalledDL(t *testing.T) {
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

	report, err := GetStalledTorrents(context.Background(), client)
	if err != nil {
		t.Fatalf("GetStalledTorrents: %v", err)
	}
	if !report.UsesTorrentID {
		t.Fatal("expected UsesTorrentID=true for qbittorrent")
	}
	if len(report.ClientReported) != 2 {
		t.Fatalf("expected 2 stalled entries, got %d: %v", len(report.ClientReported), report.ClientReported)
	}
	if !report.ClientReported["abcdef"] {
		t.Error("expected 'abcdef' (lower-cased) to be stalled")
	}
	if !report.ClientReported["ffffff"] {
		t.Error("expected case-insensitive match for 'StalledDL'")
	}
	if report.ClientReported["123abc"] {
		t.Error("non-stalled torrent incorrectly flagged")
	}
}

// TestGetStalledTorrents_QBittorrent_EmptyList verifies a well-formed response
// with no torrents returns an empty (non-nil) map.
func TestGetStalledTorrents_QBittorrent_EmptyList(t *testing.T) {
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

	report, err := GetStalledTorrents(context.Background(), client)
	if err != nil {
		t.Fatalf("GetStalledTorrents: %v", err)
	}
	if len(report.ClientReported)+len(report.NoMetadata) != 0 {
		t.Errorf("expected an empty report, got %+v", report)
	}
}

// TestGetStalledTorrents_Transmission_StoppedWithError verifies that Transmission
// torrents in status 0 (stopped) with a non-empty errorString are reported
// as stalled, while other states are not.
//
// Every torrent in the fixture carries a totalSize, which is what a torrent
// whose metadata has arrived looks like. Without it they would all also match
// the no-metadata rule (#2709) and the test would be asserting two things at
// once; the no-metadata rule has its own test below.
func TestGetStalledTorrents_Transmission_StoppedWithError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/transmission/rpc" {
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"arguments": map[string]any{
				"torrents": []map[string]any{
					{"id": 1, "status": 0, "errorString": "tracker error", "totalSize": 4096, "percentDone": 0.5, "metadataPercentComplete": 1},
					{"id": 2, "status": 0, "errorString": "", "totalSize": 4096, "percentDone": 0.5, "metadataPercentComplete": 1},
					{"id": 3, "status": 2, "errorString": "some error", "totalSize": 4096, "percentDone": 0.5, "metadataPercentComplete": 1},
					{"id": 4, "status": 0, "errorString": "   ", "totalSize": 4096, "percentDone": 0.5, "metadataPercentComplete": 1},
				},
			},
			"result": "success",
		})
	}))
	defer srv.Close()

	host, port := serverHostPort(t, srv.URL)
	client := &models.DownloadClient{Type: "transmission", Host: host, Port: port}

	report, err := GetStalledTorrents(context.Background(), client)
	if err != nil {
		t.Fatalf("GetStalledTorrents: %v", err)
	}
	if !report.UsesTorrentID {
		t.Fatal("expected UsesTorrentID=true for transmission")
	}
	if len(report.ClientReported) != 1 {
		t.Fatalf("expected 1 stalled entry, got %d: %v", len(report.ClientReported), report.ClientReported)
	}
	if !report.ClientReported["1"] {
		t.Error("expected transmission id '1' to be stalled")
	}
	if len(report.NoMetadata) != 0 {
		t.Errorf("no torrent here is missing metadata, got %v", report.NoMetadata)
	}
}

// TestGetStalledTorrents_Sabnzbd_NotSupported verifies SABnzbd returns nil map
// with no error — the caller treats this as "nothing stalled".
func TestGetStalledTorrents_Sabnzbd_NotSupported(t *testing.T) {
	client := &models.DownloadClient{Type: "sabnzbd", Host: "localhost", Port: 1, APIKey: "k"}
	report, err := GetStalledTorrents(context.Background(), client)
	if err != nil {
		t.Fatalf("GetStalledTorrents sabnzbd: %v", err)
	}
	if report.UsesTorrentID {
		t.Error("expected UsesTorrentID=false for sabnzbd")
	}
	if len(report.ClientReported) != 0 || len(report.NoMetadata) != 0 {
		t.Errorf("expected an empty report for sabnzbd, got %+v", report)
	}
}

// TestGetStalledTorrents_QBittorrent_ServerError surfaces the transport error.
func TestGetStalledTorrents_QBittorrent_ServerError(t *testing.T) {
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
	if _, err := GetStalledTorrents(context.Background(), client); err == nil {
		t.Fatal("expected error from 500 response, got nil")
	}
}
