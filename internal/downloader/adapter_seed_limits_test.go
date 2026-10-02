package downloader

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"sync"
	"testing"

	"github.com/vavallee/bindery/internal/models"
)

func seedIntPtr(v int) *int { return &v }

// TestSendDownload_QbittorrentSeedTimeOnly: a grab whose indexer has only a
// seed time (no ratio) must still reach setShareLimits (#2206). Before, the
// call was gated on the ratio alone, so a seed time would have been dropped.
func TestSendDownload_QbittorrentSeedTimeOnly(t *testing.T) {
	var mu sync.Mutex
	var share url.Values
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v2/auth/login":
			_, _ = w.Write([]byte("Ok."))
		case "/api/v2/torrents/add":
			_, _ = w.Write([]byte("Ok."))
		case "/api/v2/torrents/setShareLimits":
			_ = r.ParseForm()
			mu.Lock()
			share = r.PostForm
			mu.Unlock()
			w.WriteHeader(http.StatusOK)
		}
	}))
	defer srv.Close()
	host, port := serverHostPort(t, srv.URL)
	client := &models.DownloadClient{Type: "qbittorrent", Host: host, Port: port, Username: "u", Password: "p"}
	if _, err := SendDownload(context.Background(), client, "magnet:?xt=urn:btih:ABCDEF123&dn=Book", "", SendOptions{
		MediaType:               models.MediaTypeEbook,
		SeedTimeMinutes:         seedIntPtr(4320),
		InactiveSeedTimeMinutes: seedIntPtr(60),
	}); err != nil {
		t.Fatalf("SendDownload: %v", err)
	}
	mu.Lock()
	defer mu.Unlock()
	if share == nil {
		t.Fatal("setShareLimits was not called for a seed time override")
	}
	if g := share.Get("hashes"); g != "abcdef123" {
		t.Errorf("hashes = %q, want abcdef123", g)
	}
	if g := share.Get("ratioLimit"); g != "-2" {
		t.Errorf("ratioLimit = %q, want -2 (no ratio override)", g)
	}
	if g := share.Get("seedingTimeLimit"); g != "4320" {
		t.Errorf("seedingTimeLimit = %q, want 4320", g)
	}
	if g := share.Get("inactiveSeedingTimeLimit"); g != "60" {
		t.Errorf("inactiveSeedingTimeLimit = %q, want 60", g)
	}
}

// TestSendDownload_QbittorrentNoLimitsNoShareCall: no override at all keeps
// the old behaviour of not touching the torrent's share limits.
func TestSendDownload_QbittorrentNoLimitsNoShareCall(t *testing.T) {
	var called bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v2/auth/login", "/api/v2/torrents/add":
			_, _ = w.Write([]byte("Ok."))
		case "/api/v2/torrents/setShareLimits":
			called = true
		}
	}))
	defer srv.Close()
	host, port := serverHostPort(t, srv.URL)
	client := &models.DownloadClient{Type: "qbittorrent", Host: host, Port: port, Username: "u", Password: "p"}
	if _, err := SendDownload(context.Background(), client, "magnet:?xt=urn:btih:ABCDEF123&dn=Book", "", SendOptions{}); err != nil {
		t.Fatalf("SendDownload: %v", err)
	}
	if called {
		t.Error("setShareLimits must not be called when the indexer has no overrides")
	}
}

// TestSendDownload_TransmissionInactiveSeedTime: the inactive limit reaches
// Transmission as seedIdleLimit on torrent-set; the total seed time has no
// Transmission equivalent and is not sent in any form.
func TestSendDownload_TransmissionInactiveSeedTime(t *testing.T) {
	var mu sync.Mutex
	var setArgs map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Method    string         `json:"method"`
			Arguments map[string]any `json:"arguments"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		switch req.Method {
		case "torrent-add":
			_, _ = w.Write([]byte(`{"result":"success","arguments":{"torrent-added":{"id":4,"name":"B","hashString":"aa11"}}}`))
		case "torrent-set":
			mu.Lock()
			setArgs = req.Arguments
			mu.Unlock()
			_, _ = w.Write([]byte(`{"result":"success","arguments":{}}`))
		}
	}))
	defer srv.Close()
	host, port := serverHostPort(t, srv.URL)
	client := &models.DownloadClient{Type: "transmission", Host: host, Port: port}
	if _, err := SendDownload(context.Background(), client, "magnet:?xt=urn:btih:aa11", "", SendOptions{
		SeedTimeMinutes:         seedIntPtr(4320),
		InactiveSeedTimeMinutes: seedIntPtr(45),
	}); err != nil {
		t.Fatalf("SendDownload: %v", err)
	}
	mu.Lock()
	defer mu.Unlock()
	if setArgs == nil {
		t.Fatal("torrent-set was not called for an inactive seed time")
	}
	if g := setArgs["seedIdleLimit"]; g != float64(45) {
		t.Errorf("seedIdleLimit = %v, want 45", g)
	}
	if g := setArgs["seedIdleMode"]; g != float64(1) {
		t.Errorf("seedIdleMode = %v, want 1", g)
	}
	for k, v := range setArgs {
		if v == float64(4320) {
			t.Errorf("total seed time leaked into torrent-set as %s", k)
		}
	}
}

// TestSeedLimitsFor copies the three overrides off an indexer and is nil safe
// for a grab whose indexer could not be resolved.
func TestSeedLimitsFor(t *testing.T) {
	if got := SeedLimitsFor(nil); got != (SeedLimits{}) {
		t.Errorf("SeedLimitsFor(nil) = %+v, want zero", got)
	}
	ratio := 2.0
	idx := &models.Indexer{SeedRatio: &ratio, SeedTimeMinutes: seedIntPtr(10), InactiveSeedTimeMinutes: seedIntPtr(5)}
	got := SeedLimitsFor(idx)
	if got.Ratio != idx.SeedRatio || got.SeedTimeMinutes != idx.SeedTimeMinutes || got.InactiveSeedTimeMinutes != idx.InactiveSeedTimeMinutes {
		t.Errorf("SeedLimitsFor = %+v, want the indexer's own pointers", got)
	}
	opts := SendOptions{MediaType: models.MediaTypeEbook}.WithSeedLimits(got)
	if opts.SeedRatio != idx.SeedRatio || opts.SeedTimeMinutes != idx.SeedTimeMinutes || opts.InactiveSeedTimeMinutes != idx.InactiveSeedTimeMinutes || opts.MediaType != models.MediaTypeEbook {
		t.Errorf("WithSeedLimits = %+v", opts)
	}
}

// TestUnappliedSeedLimits documents, per client, which overrides the client
// cannot carry. These are the ones the adapter logs at debug instead of
// silently pretending to apply.
func TestUnappliedSeedLimits(t *testing.T) {
	all := SendOptions{
		SeedRatio:               func() *float64 { f := 1.0; return &f }(),
		SeedTimeMinutes:         seedIntPtr(10),
		InactiveSeedTimeMinutes: seedIntPtr(5),
	}
	cases := []struct {
		client string
		opts   SendOptions
		want   []string
	}{
		{"qbittorrent", all, nil},
		{"transmission", all, []string{"seedTimeMinutes"}},
		{"deluge", all, []string{"seedTimeMinutes", "inactiveSeedTimeMinutes"}},
		// rTorrent cannot take the ratio either, but its client already warns
		// about that itself, so only the time limits are reported here.
		{"rtorrent", all, []string{"seedTimeMinutes", "inactiveSeedTimeMinutes"}},
		{"transmission", SendOptions{InactiveSeedTimeMinutes: seedIntPtr(5)}, nil},
		{"deluge", SendOptions{}, nil},
		// Usenet clients have no seeding, so nothing is "unapplied" there.
		{"sabnzbd", all, nil},
		{"nzbget", all, nil},
	}
	for _, tc := range cases {
		got := unappliedSeedLimits(tc.client, tc.opts)
		if !reflect.DeepEqual(got, tc.want) {
			t.Errorf("%s: unappliedSeedLimits = %v, want %v", tc.client, got, tc.want)
		}
	}
}
