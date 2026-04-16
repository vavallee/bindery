package downloader

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"testing"

	"github.com/vavallee/bindery/internal/models"
)

func TestProtocolForClient(t *testing.T) {
	if got := ProtocolForClient("sabnzbd"); got != "usenet" {
		t.Fatalf("expected usenet, got %q", got)
	}
	if got := ProtocolForClient("transmission"); got != "torrent" {
		t.Fatalf("expected torrent, got %q", got)
	}
	if got := ProtocolForClient("qbittorrent"); got != "torrent" {
		t.Fatalf("expected torrent, got %q", got)
	}
}

func TestGetLiveStatusesSABnzbd(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api" {
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
		if r.URL.Query().Get("mode") != "queue" {
			t.Fatalf("expected mode=queue, got %s", r.URL.Query().Get("mode"))
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"queue": map[string]any{
				"speed": "2.0 MB/s",
				"slots": []map[string]any{{
					"nzo_id":     "nzo123",
					"percentage": "55",
					"timeleft":   "0:10:00",
				}},
			},
		})
	}))
	defer srv.Close()

	host, port := serverHostPort(t, srv.URL)
	client := &models.DownloadClient{Type: "sabnzbd", Host: host, Port: port, APIKey: "k"}

	statusByID, usesTorrentID, err := GetLiveStatuses(context.Background(), client)
	if err != nil {
		t.Fatalf("GetLiveStatuses: %v", err)
	}
	if usesTorrentID {
		t.Fatalf("expected usesTorrentID=false for sabnzbd")
	}
	status, ok := statusByID["nzo123"]
	if !ok {
		t.Fatalf("expected nzo123 status")
	}
	if status.Percentage != "55" || status.TimeLeft != "0:10:00" || status.Speed != "2.0 MB/s" {
		t.Fatalf("unexpected status: %+v", status)
	}
}

func TestGetLiveStatusesTransmission(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/transmission/rpc" {
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"arguments": map[string]any{
				"torrents": []map[string]any{
					{
						"id":           7,
						"percentDone":  0.42,
						"eta":          125,
						"rateDownload": 4096,
						"downloadDir":  "/books",
					},
					{
						"id":          99,
						"percentDone": 1.0,
						"downloadDir": "/other",
					},
				},
			},
			"result": "success",
		})
	}))
	defer srv.Close()

	host, port := serverHostPort(t, srv.URL)

	// No category set — all torrents are returned.
	client := &models.DownloadClient{Type: "transmission", Host: host, Port: port}
	statusByID, usesTorrentID, err := GetLiveStatuses(context.Background(), client)
	if err != nil {
		t.Fatalf("GetLiveStatuses: %v", err)
	}
	if !usesTorrentID {
		t.Fatalf("expected usesTorrentID=true for transmission")
	}
	if len(statusByID) != 2 {
		t.Fatalf("expected 2 statuses with no category filter, got %d", len(statusByID))
	}
	status, ok := statusByID["7"]
	if !ok {
		t.Fatalf("expected torrent id 7 status")
	}
	if status.Percentage != "42.0" {
		t.Fatalf("unexpected percentage: %s", status.Percentage)
	}
	if status.TimeLeft == "" || status.Speed == "" {
		t.Fatalf("expected non-empty timeLeft/speed, got %+v", status)
	}

	// Category acts as a download-directory filter on shared Transmission instances.
	clientFiltered := &models.DownloadClient{Type: "transmission", Host: host, Port: port, Category: "/books"}
	filteredByID, _, err := GetLiveStatuses(context.Background(), clientFiltered)
	if err != nil {
		t.Fatalf("GetLiveStatuses with category: %v", err)
	}
	if len(filteredByID) != 1 {
		t.Fatalf("expected 1 status when category=/books, got %d", len(filteredByID))
	}
	if _, ok := filteredByID["7"]; !ok {
		t.Fatalf("expected torrent id 7 in filtered result")
	}
	if _, ok := filteredByID["99"]; ok {
		t.Fatalf("torrent 99 (/other) should have been filtered out")
	}
}

func TestGetLiveStatusesQbittorrent(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v2/auth/login":
			_, _ = w.Write([]byte("Ok."))
		case "/api/v2/torrents/info":
			_ = json.NewEncoder(w).Encode([]map[string]any{{
				"hash":     "ABCDEF",
				"progress": 0.9,
				"eta":      300,
			}})
		default:
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
	}))
	defer srv.Close()

	host, port := serverHostPort(t, srv.URL)
	client := &models.DownloadClient{Type: "qbittorrent", Host: host, Port: port, Username: "u", Password: "p"}

	statusByID, usesTorrentID, err := GetLiveStatuses(context.Background(), client)
	if err != nil {
		t.Fatalf("GetLiveStatuses: %v", err)
	}
	if !usesTorrentID {
		t.Fatalf("expected usesTorrentID=true for qbittorrent")
	}
	status, ok := statusByID["abcdef"]
	if !ok {
		t.Fatalf("expected normalized hash key")
	}
	if status.Percentage != "90.0" {
		t.Fatalf("unexpected percentage: %s", status.Percentage)
	}
	if status.TimeLeft == "" {
		t.Fatalf("expected non-empty timeLeft")
	}
}

func TestFormattingHelpers(t *testing.T) {
	if got := etaToTimeLeft(0); got != "" {
		t.Fatalf("expected empty eta, got %q", got)
	}
	if got := etaToTimeLeft(3661); got != "1h 01m" {
		t.Fatalf("unexpected eta format: %q", got)
	}
	if got := bytesPerSecondToString(0); got != "" {
		t.Fatalf("expected empty speed, got %q", got)
	}
	if got := bytesPerSecondToString(1024); got != "1.0 KB/s" {
		t.Fatalf("unexpected speed format: %q", got)
	}
}

func serverHostPort(t *testing.T, raw string) (string, int) {
	t.Helper()
	u, err := url.Parse(raw)
	if err != nil {
		t.Fatalf("parse server url: %v", err)
	}
	host := u.Hostname()
	port, err := strconv.Atoi(u.Port())
	if err != nil {
		t.Fatalf("parse server port: %v", err)
	}
	return host, port
}

// ---------------------------------------------------------------------------
// TestClient
// ---------------------------------------------------------------------------

func TestTestClient_SABnzbd(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"categories": []string{"books"}})
	}))
	defer srv.Close()
	host, port := serverHostPort(t, srv.URL)
	client := &models.DownloadClient{Type: "sabnzbd", Host: host, Port: port, APIKey: "k"}
	if err := TestClient(context.Background(), client); err != nil {
		t.Fatalf("TestClient: %v", err)
	}
}

func TestTestClient_Transmission(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"result": "success", "arguments": map[string]any{}})
	}))
	defer srv.Close()
	host, port := serverHostPort(t, srv.URL)
	client := &models.DownloadClient{Type: "transmission", Host: host, Port: port}
	if err := TestClient(context.Background(), client); err != nil {
		t.Fatalf("TestClient: %v", err)
	}
}

func TestTestClient_Qbittorrent(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v2/auth/login":
			_, _ = w.Write([]byte("Ok."))
		default:
			_, _ = w.Write([]byte("1.2.3"))
		}
	}))
	defer srv.Close()
	host, port := serverHostPort(t, srv.URL)
	client := &models.DownloadClient{Type: "qbittorrent", Host: host, Port: port, Username: "u", Password: "p"}
	if err := TestClient(context.Background(), client); err != nil {
		t.Fatalf("TestClient: %v", err)
	}
}

// ---------------------------------------------------------------------------
// SendDownload
// ---------------------------------------------------------------------------

func TestSendDownload_SABnzbd(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"status": true, "nzo_ids": []string{"nzo42"}})
	}))
	defer srv.Close()
	host, port := serverHostPort(t, srv.URL)
	client := &models.DownloadClient{Type: "sabnzbd", Host: host, Port: port, APIKey: "k"}
	result, err := SendDownload(context.Background(), client, "https://example.com/file.nzb", "My Book")
	if err != nil {
		t.Fatalf("SendDownload: %v", err)
	}
	if result.RemoteID != "nzo42" {
		t.Errorf("expected RemoteID=nzo42, got %q", result.RemoteID)
	}
	if result.Protocol != "usenet" {
		t.Errorf("expected Protocol=usenet, got %q", result.Protocol)
	}
	if result.UsesTorrentID {
		t.Error("expected UsesTorrentID=false for sabnzbd")
	}
}

func TestSendDownload_SABnzbd_NoNzoID(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"status": true, "nzo_ids": []string{}})
	}))
	defer srv.Close()
	host, port := serverHostPort(t, srv.URL)
	client := &models.DownloadClient{Type: "sabnzbd", Host: host, Port: port, APIKey: "k"}
	result, err := SendDownload(context.Background(), client, "https://example.com/file.nzb", "My Book")
	if err != nil {
		t.Fatalf("SendDownload: %v", err)
	}
	if result.RemoteID != "" {
		t.Errorf("expected empty RemoteID, got %q", result.RemoteID)
	}
}

func TestSendDownload_Transmission(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"result": "success",
			"arguments": map[string]any{
				"torrent-added": map[string]any{"id": 5, "name": "Book"},
			},
		})
	}))
	defer srv.Close()
	host, port := serverHostPort(t, srv.URL)
	client := &models.DownloadClient{Type: "transmission", Host: host, Port: port}
	result, err := SendDownload(context.Background(), client, "magnet:?xt=urn:btih:abc", "")
	if err != nil {
		t.Fatalf("SendDownload: %v", err)
	}
	if result.RemoteID != "5" {
		t.Errorf("expected RemoteID=5, got %q", result.RemoteID)
	}
	if result.Protocol != "torrent" {
		t.Errorf("expected Protocol=torrent, got %q", result.Protocol)
	}
	if !result.UsesTorrentID {
		t.Error("expected UsesTorrentID=true for transmission")
	}
}

func TestSendDownload_Transmission_ZeroID(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"result": "success", "arguments": map[string]any{}})
	}))
	defer srv.Close()
	host, port := serverHostPort(t, srv.URL)
	client := &models.DownloadClient{Type: "transmission", Host: host, Port: port}
	if _, err := SendDownload(context.Background(), client, "magnet:?xt=urn:btih:abc", ""); err == nil {
		t.Fatal("expected error when torrent ID is 0")
	}
}

// TestSendDownload_Transmission_RelativeCategoryNotSentAsDownloadDir verifies
// that a non-absolute Category value (the common default "books") is not
// forwarded to Transmission as download-dir, which would cause
// "download directory path is not absolute".
func TestSendDownload_Transmission_RelativeCategoryNotSentAsDownloadDir(t *testing.T) {
	var gotBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&gotBody)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"result": "success",
			"arguments": map[string]any{
				"torrent-added": map[string]any{"id": 3, "name": "Book"},
			},
		})
	}))
	defer srv.Close()
	host, port := serverHostPort(t, srv.URL)
	// "books" is a relative label — must NOT be sent as download-dir
	client := &models.DownloadClient{Type: "transmission", Host: host, Port: port, Category: "books"}
	if _, err := SendDownload(context.Background(), client, "magnet:?xt=urn:btih:abc", ""); err != nil {
		t.Fatalf("SendDownload: %v", err)
	}
	args, _ := gotBody["arguments"].(map[string]any)
	if _, hasDir := args["download-dir"]; hasDir {
		t.Errorf("download-dir should not be set for relative category %q, got args: %v", "books", args)
	}
}

// TestSendDownload_Transmission_AbsoluteCategorySentAsDownloadDir verifies
// that when Category is an absolute path it IS forwarded as download-dir.
func TestSendDownload_Transmission_AbsoluteCategorySentAsDownloadDir(t *testing.T) {
	var gotBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&gotBody)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"result": "success",
			"arguments": map[string]any{
				"torrent-added": map[string]any{"id": 4, "name": "Book"},
			},
		})
	}))
	defer srv.Close()
	host, port := serverHostPort(t, srv.URL)
	client := &models.DownloadClient{Type: "transmission", Host: host, Port: port, Category: "/custom/books"}
	if _, err := SendDownload(context.Background(), client, "magnet:?xt=urn:btih:abc", ""); err != nil {
		t.Fatalf("SendDownload: %v", err)
	}
	args, _ := gotBody["arguments"].(map[string]any)
	if dir, _ := args["download-dir"].(string); dir != "/custom/books" {
		t.Errorf("expected download-dir=/custom/books, got %q", dir)
	}
}

func TestSendDownload_Qbittorrent(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v2/auth/login":
			_, _ = w.Write([]byte("Ok."))
		case "/api/v2/torrents/add":
			_, _ = w.Write([]byte("Ok."))
		case "/api/v2/torrents/info":
			_ = json.NewEncoder(w).Encode([]map[string]any{
				{"hash": "ABCDEF123", "progress": 0.0},
			})
		}
	}))
	defer srv.Close()
	host, port := serverHostPort(t, srv.URL)
	client := &models.DownloadClient{Type: "qbittorrent", Host: host, Port: port, Username: "u", Password: "p"}
	result, err := SendDownload(context.Background(), client, "magnet:?xt=urn:btih:ABCDEF123&dn=Book", "")
	if err != nil {
		t.Fatalf("SendDownload: %v", err)
	}
	if result.Protocol != "torrent" {
		t.Errorf("expected Protocol=torrent, got %q", result.Protocol)
	}
	if !result.UsesTorrentID {
		t.Error("expected UsesTorrentID=true for qbittorrent")
	}
}

// ---------------------------------------------------------------------------
// RemoveDownload
// ---------------------------------------------------------------------------

func TestRemoveDownload_SABnzbd(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"status": true})
	}))
	defer srv.Close()
	host, port := serverHostPort(t, srv.URL)
	client := &models.DownloadClient{Type: "sabnzbd", Host: host, Port: port, APIKey: "k"}
	nzoID := "nzo99"
	dl := &models.Download{SABnzbdNzoID: &nzoID}
	if err := RemoveDownload(context.Background(), client, dl, false); err != nil {
		t.Fatalf("RemoveDownload: %v", err)
	}
}

func TestRemoveDownload_SABnzbd_NilNzoID(t *testing.T) {
	client := &models.DownloadClient{Type: "sabnzbd"}
	dl := &models.Download{SABnzbdNzoID: nil}
	if err := RemoveDownload(context.Background(), client, dl, false); err != nil {
		t.Fatalf("expected nil error for empty NzoID: %v", err)
	}
}

func TestRemoveDownload_Transmission(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"result": "success", "arguments": map[string]any{}})
	}))
	defer srv.Close()
	host, port := serverHostPort(t, srv.URL)
	client := &models.DownloadClient{Type: "transmission", Host: host, Port: port}
	torrentID := "7"
	dl := &models.Download{TorrentID: &torrentID}
	if err := RemoveDownload(context.Background(), client, dl, false); err != nil {
		t.Fatalf("RemoveDownload: %v", err)
	}
}

func TestRemoveDownload_Transmission_NilID(t *testing.T) {
	client := &models.DownloadClient{Type: "transmission"}
	dl := &models.Download{TorrentID: nil}
	if err := RemoveDownload(context.Background(), client, dl, false); err != nil {
		t.Fatalf("expected nil error for nil TorrentID: %v", err)
	}
}

func TestRemoveDownload_Transmission_InvalidID(t *testing.T) {
	client := &models.DownloadClient{Type: "transmission"}
	bad := "not-a-number"
	dl := &models.Download{TorrentID: &bad}
	if err := RemoveDownload(context.Background(), client, dl, false); err == nil {
		t.Fatal("expected error for non-numeric torrent ID")
	}
}

func TestRemoveDownload_Qbittorrent(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v2/auth/login":
			_, _ = w.Write([]byte("Ok."))
		default:
			_, _ = w.Write([]byte(""))
		}
	}))
	defer srv.Close()
	host, port := serverHostPort(t, srv.URL)
	client := &models.DownloadClient{Type: "qbittorrent", Host: host, Port: port, Username: "u", Password: "p"}
	hash := "abc123"
	dl := &models.Download{TorrentID: &hash}
	if err := RemoveDownload(context.Background(), client, dl, true); err != nil {
		t.Fatalf("RemoveDownload: %v", err)
	}
}

func TestRemoveDownload_Qbittorrent_NilID(t *testing.T) {
	client := &models.DownloadClient{Type: "qbittorrent"}
	dl := &models.Download{TorrentID: nil}
	if err := RemoveDownload(context.Background(), client, dl, false); err != nil {
		t.Fatalf("expected nil error for nil TorrentID: %v", err)
	}
}

// ---------------------------------------------------------------------------
// Formatter helpers — missing branches
// ---------------------------------------------------------------------------

func TestEtaToTimeLeft_SecondsOnly(t *testing.T) {
	if got := etaToTimeLeft(45); got != "45s" {
		t.Errorf("expected \"45s\", got %q", got)
	}
}

func TestEtaToTimeLeft_MinutesAndSeconds(t *testing.T) {
	if got := etaToTimeLeft(90); got != "1m 30s" {
		t.Errorf("expected \"1m 30s\", got %q", got)
	}
}

func TestBytesPerSecondToString_MB(t *testing.T) {
	if got := bytesPerSecondToString(2 * 1024 * 1024); got != "2.0 MB/s" {
		t.Errorf("expected \"2.0 MB/s\", got %q", got)
	}
}

func TestBytesPerSecondToString_Bytes(t *testing.T) {
	if got := bytesPerSecondToString(512); got != "512 B/s" {
		t.Errorf("expected \"512 B/s\", got %q", got)
	}
}
