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
// download-dir (#2499). The audiobook absolute path wins, otherwise the ebook
// absolute path, otherwise none: an audiobook must never land in a folder
// Bindery does not poll.
func TestSendDownload_TransmissionAudiobookDestination(t *testing.T) {
	cases := []struct {
		name       string
		cat, audio string
		mediaType  string
		want       string
		wantSent   bool
	}{
		{"abs/abs audiobook", "/tr/books", "/tr/audio", models.MediaTypeAudiobook, "/tr/audio", true},
		{"abs/abs ebook", "/tr/books", "/tr/audio", models.MediaTypeEbook, "/tr/books", true},
		{"label/abs audiobook", "books", "/tr/audio", models.MediaTypeAudiobook, "/tr/audio", true},
		{"label/abs ebook", "books", "/tr/audio", models.MediaTypeEbook, "", false},
		{"abs/label audiobook falls back to ebook", "/tr/books", "audio", models.MediaTypeAudiobook, "/tr/books", true},
		{"label/label audiobook", "books", "audio", models.MediaTypeAudiobook, "", false},
		{"abs/empty audiobook", "/tr/books", "", models.MediaTypeAudiobook, "/tr/books", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := &models.DownloadClient{Category: tc.cat, CategoryAudiobook: tc.audio}
			got, ok := sentTransmissionDir(t, c, tc.mediaType)
			if got != tc.want || ok != tc.wantSent {
				t.Errorf("download-dir = %q (sent %v), want %q (sent %v)", got, ok, tc.want, tc.wantSent)
			}
		})
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
