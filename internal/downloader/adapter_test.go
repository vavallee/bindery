package downloader

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"testing"
	"time"

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
					"status":     "Downloading",
					"mb":         "10.0",
					"mbleft":     "5.0",
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
	if status.Size != 10*1024*1024 || status.SizeLeft != 5*1024*1024 || status.Status != "Downloading" {
		t.Fatalf("unexpected size/status overlay: %+v", status)
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
						"id":            7,
						"percentDone":   0.42,
						"totalSize":     1000,
						"leftUntilDone": 580,
						"status":        2,
						"eta":           125,
						"rateDownload":  4096,
						"downloadDir":   "/books",
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
	if status.Size != 1000 || status.SizeLeft != 580 || status.Status != "2" {
		t.Fatalf("unexpected size/status: %+v", status)
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
				"hash":        "ABCDEF",
				"progress":    0.9,
				"eta":         300,
				"size":        2000,
				"amount_left": 200,
				"state":       "downloading",
				"dlspeed":     1024,
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
	if status.Size != 2000 || status.SizeLeft != 200 || status.Status != "downloading" || status.Speed == "" {
		t.Fatalf("unexpected size/status/speed: %+v", status)
	}
}

func TestGetLiveStatusesNZBGet(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Method string `json:"method"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		if req.Method != "listgroups" {
			t.Fatalf("expected listgroups, got %q", req.Method)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"result": []map[string]any{{
				"NZBID":           101,
				"Status":          "DOWNLOADING",
				"FileSizeMB":      20.0,
				"RemainingSizeMB": 2.5,
			}},
		})
	}))
	defer srv.Close()

	host, port := serverHostPort(t, srv.URL)
	client := &models.DownloadClient{Type: "nzbget", Host: host, Port: port}

	statusByID, usesTorrentID, err := GetLiveStatuses(context.Background(), client)
	if err != nil {
		t.Fatalf("GetLiveStatuses: %v", err)
	}
	if usesTorrentID {
		t.Fatalf("expected usesTorrentID=false for nzbget")
	}
	status, ok := statusByID["101"]
	if !ok {
		t.Fatalf("expected NZBID 101 status")
	}
	if status.Size != 20*1024*1024 || status.SizeLeft != int64(2.5*1024*1024) || status.Status != "DOWNLOADING" {
		t.Fatalf("unexpected status: %+v", status)
	}
}

func TestGetLiveStatusesDeluge(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/json" {
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
		var req struct {
			Method string `json:"method"`
			ID     int64  `json:"id"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		var result any
		switch req.Method {
		case "auth.login":
			result = true
		case "core.get_torrents_status":
			result = map[string]any{
				"abc123": map[string]any{
					"hash":                  "abc123",
					"progress":              25.0,
					"state":                 "Downloading",
					"eta":                   60,
					"download_payload_rate": 2048,
					"total_size":            4000,
					"total_done":            1000,
				},
			}
		default:
			t.Fatalf("unexpected method: %s", req.Method)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"result": result, "error": nil, "id": req.ID})
	}))
	defer srv.Close()

	host, port := serverHostPort(t, srv.URL)
	client := &models.DownloadClient{Type: "deluge", Host: host, Port: port, Password: "pw"}

	statusByID, usesTorrentID, err := GetLiveStatuses(context.Background(), client)
	if err != nil {
		t.Fatalf("GetLiveStatuses: %v", err)
	}
	if !usesTorrentID {
		t.Fatalf("expected usesTorrentID=true for deluge")
	}
	status, ok := statusByID["abc123"]
	if !ok {
		t.Fatalf("expected abc123 status")
	}
	if status.Size != 4000 || status.SizeLeft != 3000 || status.Status != "Downloading" {
		t.Fatalf("unexpected status: %+v", status)
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

func TestTestClient_ConnectionFailureMatrix(t *testing.T) {
	tests := []struct {
		name       string
		clientType string
	}{
		{name: "SABnzbd", clientType: "sabnzbd"},
		{name: "NZBGet", clientType: "nzbget"},
		{name: "Transmission", clientType: "transmission"},
		{name: "qBittorrent", clientType: "qbittorrent"},
		{name: "Deluge", clientType: "deluge"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
			host, port := serverHostPort(t, srv.URL)
			srv.Close()

			client := &models.DownloadClient{
				Type:     tc.clientType,
				Host:     host,
				Port:     port,
				APIKey:   "testkey",
				Username: "user",
				Password: "pass",
			}
			if err := TestClient(context.Background(), client); err == nil {
				t.Fatal("expected connection failure error, got nil")
			}
		})
	}
}

func TestTestClient_TimeoutMatrix(t *testing.T) {
	tests := []struct {
		name       string
		clientType string
	}{
		{name: "SABnzbd", clientType: "sabnzbd"},
		{name: "NZBGet", clientType: "nzbget"},
		{name: "Transmission", clientType: "transmission"},
		{name: "qBittorrent", clientType: "qbittorrent"},
		{name: "Deluge", clientType: "deluge"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, _ *http.Request) {
				time.Sleep(200 * time.Millisecond)
			}))
			defer srv.Close()

			host, port := serverHostPort(t, srv.URL)
			client := &models.DownloadClient{
				Type:     tc.clientType,
				Host:     host,
				Port:     port,
				APIKey:   "testkey",
				Username: "user",
				Password: "pass",
			}
			ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
			defer cancel()

			err := TestClient(ctx, client)
			if err == nil {
				t.Fatal("expected timeout error, got nil")
			}
			if !errors.Is(err, context.DeadlineExceeded) {
				t.Fatalf("expected context deadline exceeded, got %v", err)
			}
		})
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

// ---------------------------------------------------------------------------
// liveStatusIsError — issue #426
// ---------------------------------------------------------------------------

func TestLiveStatusIsError_TransmissionIntegerCodes(t *testing.T) {
	tests := []struct {
		status string
		want   bool
	}{
		// Transmission error codes
		{"16", true}, // TR_STATUS_CHECK_WAIT (error)
		{"32", true}, // TR_STATUS_ISOLATED_ERROR
		{"0", false}, // stopped but not an error code
		{"3", false}, // seeding
		{"2", false}, // downloading
		// String-based statuses (qBittorrent, Deluge)
		{"error", true},
		{"Error", true},
		{"stalledDL", false},
		{"downloading", false},
		{"failed", true},
		{"Failed", true},
		{"", false},
	}
	for _, tc := range tests {
		ls := LiveStatus{Status: tc.status}
		if got := liveStatusIsError(ls); got != tc.want {
			t.Errorf("liveStatusIsError(%q) = %v, want %v", tc.status, got, tc.want)
		}
	}
}

func TestGetLiveStatusesTransmission_PopulatesStatus(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"arguments": map[string]any{
				"torrents": []map[string]any{
					{
						"id":          10,
						"percentDone": 0.5,
						"status":      16, // Transmission error code
					},
				},
			},
			"result": "success",
		})
	}))
	defer srv.Close()

	host, port := serverHostPort(t, srv.URL)
	client := &models.DownloadClient{Type: "transmission", Host: host, Port: port}
	statusByID, _, err := GetLiveStatuses(context.Background(), client)
	if err != nil {
		t.Fatalf("GetLiveStatuses: %v", err)
	}
	ls, ok := statusByID["10"]
	if !ok {
		t.Fatalf("expected torrent id 10 in status map")
	}
	if ls.Status != "16" {
		t.Errorf("expected Status=%q, got %q", "16", ls.Status)
	}
	if !liveStatusIsError(ls) {
		t.Error("expected liveStatusIsError to return true for Transmission status 16")
	}
}
