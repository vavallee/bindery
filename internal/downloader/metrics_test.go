package downloader

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"slices"
	"testing"

	"github.com/vavallee/bindery/internal/httpsec"
	"github.com/vavallee/bindery/internal/metrics/metricstest"
	"github.com/vavallee/bindery/internal/models"
)

func grabs(t *testing.T, mediaType, result string) float64 {
	return metricstest.Value(t, "bindery_grabs_total", map[string]string{"media_type": mediaType, "result": result})
}

func clientRequests(t *testing.T, clientType, result string) float64 {
	return metricstest.Value(t, "bindery_download_client_requests_total", map[string]string{"client_type": clientType, "result": result})
}

func clientDurationCount(t *testing.T, clientType string) float64 {
	return metricstest.Value(t, "bindery_download_client_request_duration_seconds", map[string]string{"client_type": clientType})
}

// TestSendDownload_RecordsGrabAndClientRequest drives a real grab through
// SendDownload to a fake SABnzbd and checks both the grab counter and the
// per request download client counter and histogram moved.
func TestSendDownload_RecordsGrabAndClientRequest(t *testing.T) {
	defer httpsec.AllowLoopbackForTests()()
	indexerSrv := fakeNZBIndexer(t)
	defer indexerSrv.Close()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"status": true, "nzo_ids": []string{"nzo1"}})
	}))
	defer srv.Close()
	host, port := serverHostPort(t, srv.URL)
	client := &models.DownloadClient{ID: 913851, Type: "sabnzbd", Host: host, Port: port, APIKey: "k"}

	grabsBefore := grabs(t, "audiobook", "ok")
	reqBefore := clientRequests(t, "sabnzbd", "ok")
	durBefore := clientDurationCount(t, "sabnzbd")

	if _, err := SendDownload(context.Background(), client, indexerSrv.URL+"/file.nzb", "A Title",
		SendOptions{MediaType: models.MediaTypeAudiobook}); err != nil {
		t.Fatalf("SendDownload: %v", err)
	}

	if got := grabs(t, "audiobook", "ok") - grabsBefore; got != 1 {
		t.Errorf("bindery_grabs_total{audiobook,ok} moved by %v, want 1", got)
	}
	if got := clientRequests(t, "sabnzbd", "ok") - reqBefore; got < 1 {
		t.Errorf("bindery_download_client_requests_total{sabnzbd,ok} moved by %v, want at least 1", got)
	}
	if got := clientDurationCount(t, "sabnzbd") - durBefore; got < 1 {
		t.Errorf("download client duration histogram moved by %v, want at least 1", got)
	}
}

// TestSendDownload_FailureRecordsErrorResults covers a client that answers
// 500: the grab is result error and so is the client request.
func TestSendDownload_FailureRecordsErrorResults(t *testing.T) {
	defer httpsec.AllowLoopbackForTests()()
	indexerSrv := fakeNZBIndexer(t)
	defer indexerSrv.Close()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "boom", http.StatusInternalServerError)
	}))
	defer srv.Close()
	host, port := serverHostPort(t, srv.URL)
	client := &models.DownloadClient{ID: 913852, Type: "sabnzbd", Host: host, Port: port, APIKey: "k"}

	grabsBefore := grabs(t, "ebook", "error")
	reqBefore := clientRequests(t, "sabnzbd", "error")

	if _, err := SendDownload(context.Background(), client, indexerSrv.URL+"/file.nzb", "A Title",
		SendOptions{MediaType: models.MediaTypeEbook}); err == nil {
		t.Fatal("expected an error from a client answering 500")
	}

	if got := grabs(t, "ebook", "error") - grabsBefore; got != 1 {
		t.Errorf("bindery_grabs_total{ebook,error} moved by %v, want 1", got)
	}
	if got := clientRequests(t, "sabnzbd", "error") - reqBefore; got < 1 {
		t.Errorf("bindery_download_client_requests_total{sabnzbd,error} moved by %v, want at least 1", got)
	}
}

// TestClientPoll_RecordsClientRequest checks the scanner's poll path, which
// calls the cached client directly rather than through SendDownload, is
// counted too: the wrapper sits on every client the cache builds.
func TestClientPoll_RecordsClientRequest(t *testing.T) {
	defer httpsec.AllowLoopbackForTests()()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("mode") == "history" {
			_ = json.NewEncoder(w).Encode(map[string]any{"history": map[string]any{"slots": []any{}}})
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"status": true})
	}))
	defer srv.Close()
	host, port := serverHostPort(t, srv.URL)
	client := &models.DownloadClient{ID: 913853, Type: "sabnzbd", Host: host, Port: port, APIKey: "k"}

	before := clientRequests(t, "sabnzbd", "ok")
	if _, err := SabnzbdFor(client).GetHistory(context.Background(), "", 10); err != nil {
		t.Fatalf("GetHistory: %v", err)
	}
	if got := clientRequests(t, "sabnzbd", "ok") - before; got != 1 {
		t.Errorf("poll request moved the counter by %v, want 1", got)
	}
}

// TestDownloadClientMetrics_LabelsStayBounded sends with a client type and a
// media type Bindery does not know. Both must collapse to other, and no label
// value outside the fixed sets may appear.
func TestDownloadClientMetrics_LabelsStayBounded(t *testing.T) {
	defer httpsec.AllowLoopbackForTests()()
	indexerSrv := fakeNZBIndexer(t)
	defer indexerSrv.Close()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"status": true, "nzo_ids": []string{"nzo2"}})
	}))
	defer srv.Close()
	host, port := serverHostPort(t, srv.URL)
	// An unknown type falls through to the SABnzbd branch of SendDownload.
	client := &models.DownloadClient{ID: 913854, Type: "My Client http://x", Host: host, Port: port, APIKey: "k"}

	if _, err := SendDownload(context.Background(), client, indexerSrv.URL+"/file.nzb", "A Title",
		SendOptions{MediaType: "Some Title by Someone"}); err != nil {
		t.Fatalf("SendDownload: %v", err)
	}
	if clientRequests(t, "other", "ok") < 1 {
		t.Error("unknown client type was not recorded as other")
	}
	if grabs(t, "other", "ok") < 1 {
		t.Error("unknown media type was not recorded as other")
	}

	clientTypes := []string{"sabnzbd", "nzbget", "transmission", "qbittorrent", "deluge", "rtorrent", "other"}
	for _, v := range metricstest.LabelValues(t, "bindery_download_client_requests_total", "client_type") {
		if !slices.Contains(clientTypes, v) {
			t.Errorf("client_type label %q is outside the bounded set", v)
		}
	}
	for _, v := range metricstest.LabelValues(t, "bindery_download_client_requests_total", "result") {
		if !slices.Contains([]string{"ok", "rejected", "error"}, v) {
			t.Errorf("result label %q is outside the bounded set", v)
		}
	}
	for _, v := range metricstest.LabelValues(t, "bindery_grabs_total", "media_type") {
		if !slices.Contains([]string{"ebook", "audiobook", "other"}, v) {
			t.Errorf("media_type label %q is outside the bounded set", v)
		}
	}
}
