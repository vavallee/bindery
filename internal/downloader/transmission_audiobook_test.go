package downloader

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/vavallee/bindery/internal/models"
)

// sentTransmissionDir grabs one release of mediaType through a fake
// Transmission and returns the download-dir the torrent-add carried, and
// whether the key was sent at all.
func sentTransmissionDir(t *testing.T, client *models.DownloadClient, mediaType string) (string, bool) {
	t.Helper()
	var mu sync.Mutex
	var addArgs map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Method    string         `json:"method"`
			Arguments map[string]any `json:"arguments"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		if req.Method == "torrent-add" {
			mu.Lock()
			addArgs = req.Arguments
			mu.Unlock()
		}
		_, _ = w.Write([]byte(`{"result":"success","arguments":{"torrent-added":{"id":4,"name":"B","hashString":"aa11"}}}`))
	}))
	defer srv.Close()
	host, port := serverHostPort(t, srv.URL)
	client.Type, client.Host, client.Port = "transmission", host, port
	if _, err := SendDownload(context.Background(), client, "magnet:?xt=urn:btih:aa11", "", SendOptions{MediaType: mediaType}); err != nil {
		t.Fatalf("SendDownload: %v", err)
	}
	mu.Lock()
	defer mu.Unlock()
	got, ok := addArgs["download-dir"].(string)
	return got, ok
}

// An audiobook grab honours the audiobook category as Transmission's
// download-dir, the same way an ebook grab honours the category (#2499).
func TestSendDownload_TransmissionAudiobookDestination(t *testing.T) {
	mk := func() *models.DownloadClient {
		return &models.DownloadClient{Category: "/tr/books", CategoryAudiobook: "/tr/audio"}
	}
	if got, _ := sentTransmissionDir(t, mk(), models.MediaTypeAudiobook); got != "/tr/audio" {
		t.Errorf("audiobook download-dir = %q, want /tr/audio", got)
	}
	if got, _ := sentTransmissionDir(t, mk(), models.MediaTypeEbook); got != "/tr/books" {
		t.Errorf("ebook download-dir = %q, want /tr/books", got)
	}
	// With no audiobook category set the audiobook falls back to Category.
	c := &models.DownloadClient{Category: "/tr/books"}
	if got, _ := sentTransmissionDir(t, c, models.MediaTypeAudiobook); got != "/tr/books" {
		t.Errorf("fallback audiobook download-dir = %q, want /tr/books", got)
	}
	// A bare label is not a directory, so the leading slash rule still sends none.
	c = &models.DownloadClient{Category: "/tr/books", CategoryAudiobook: "audio"}
	if got, ok := sentTransmissionDir(t, c, models.MediaTypeAudiobook); ok {
		t.Errorf("label category sent download-dir %q, want none", got)
	}
}

// The queue progress overlay sees torrents under both the ebook and the
// audiobook destination, not only the ebook one.
func TestGetLiveStatuses_TransmissionAudiobookDestination(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"result": "success",
			"arguments": map[string]any{"torrents": []map[string]any{
				{"id": 1, "hashString": "AAAA", "downloadDir": "/tr/books", "percentDone": 0.5, "totalSize": 10},
				{"id": 2, "hashString": "BBBB", "downloadDir": "/tr/audio", "percentDone": 0.25, "totalSize": 10},
				{"id": 3, "hashString": "CCCC", "downloadDir": "/tr/other", "percentDone": 0.75, "totalSize": 10},
			}},
		})
	}))
	defer srv.Close()
	host, port := serverHostPort(t, srv.URL)
	client := &models.DownloadClient{Type: "transmission", Host: host, Port: port, Category: "/tr/books", CategoryAudiobook: "/tr/audio"}
	got, _, err := GetLiveStatuses(context.Background(), client)
	if err != nil {
		t.Fatalf("GetLiveStatuses: %v", err)
	}
	if _, ok := got["aaaa"]; !ok {
		t.Errorf("ebook torrent missing: %v", got)
	}
	if _, ok := got["bbbb"]; !ok {
		t.Errorf("audiobook torrent missing: %v", got)
	}
	if _, ok := got["cccc"]; ok {
		t.Errorf("torrent outside both destinations was included: %v", got)
	}
}
