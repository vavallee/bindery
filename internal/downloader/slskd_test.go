package downloader

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/vavallee/bindery/internal/httpsec"
	"github.com/vavallee/bindery/internal/models"
)

// slskdRecorder is a fake host that answers like slskd on /api/v0 and records
// every request, so a test can prove nothing reached SABnzbd shaped endpoints
// (the single /api path with mode and apikey parameters).
type slskdRecorder struct {
	mu   sync.Mutex
	reqs []string
	keys []string
}

func (r *slskdRecorder) ServeHTTP(w http.ResponseWriter, req *http.Request) {
	r.mu.Lock()
	r.reqs = append(r.reqs, req.Method+" "+req.URL.RequestURI())
	r.keys = append(r.keys, req.Header.Get("X-API-Key"))
	r.mu.Unlock()
	switch req.URL.Path {
	case "/api/v0/application":
		_, _ = w.Write([]byte(`{"version":{"current":"0.26.0"},"server":{"isConnected":true,"isLoggedIn":true}}`))
	case "/api/v0/options":
		_, _ = w.Write([]byte(`{"directories":{"downloads":"/app/downloads"}}`))
	default:
		http.NotFound(w, req)
	}
}

func (r *slskdRecorder) assertSlskdOnly(t *testing.T) {
	t.Helper()
	r.mu.Lock()
	defer r.mu.Unlock()
	for i, q := range r.reqs {
		path := strings.SplitN(strings.SplitN(q, " ", 2)[1], "?", 2)
		if !strings.HasPrefix(path[0], "/api/v0/") {
			t.Errorf("request %q is not an slskd endpoint, so slskd reached another client's code", q)
		}
		if len(path) > 1 {
			t.Errorf("request %q carries a query string; the key belongs in X-API-Key only", q)
		}
		if r.keys[i] != "slskd-key" {
			t.Errorf("request %q sent X-API-Key %q", q, r.keys[i])
		}
	}
}

func slskdTestClient(t *testing.T, id int64) (*models.DownloadClient, *slskdRecorder) {
	t.Helper()
	t.Cleanup(httpsec.AllowLoopbackForTests())
	rec := &slskdRecorder{}
	srv := httptest.NewServer(rec)
	t.Cleanup(srv.Close)
	host, port := serverHostPort(t, srv.URL)
	return &models.DownloadClient{ID: id, Type: "slskd", Host: host, Port: port, APIKey: "slskd-key"}, rec
}

func TestSlskdProtocolAndNames(t *testing.T) {
	if got := ProtocolForClient("slskd"); got != "soulseek" {
		t.Errorf("ProtocolForClient(slskd) = %q", got)
	}
	if IsTorrentClient("slskd") || IsNZBGetClient("slskd") {
		t.Error("slskd must not be classed as a torrent or NZBGet client")
	}
	if got := ClientTypeName("slskd"); got != "slskd" {
		t.Errorf("ClientTypeName(slskd) = %q, want slskd and not the SABnzbd fallback", got)
	}
	// The other protocols are unchanged.
	if ProtocolForClient("qbittorrent") != "torrent" || ProtocolForClient("sabnzbd") != "usenet" || ProtocolForClient("nzbget") != "usenet" {
		t.Error("existing protocols changed")
	}
}

// TestSlskdNeverReachesSABnzbd drives every dispatch site that defaults an
// unknown type to SABnzbd and checks slskd is answered by its own code.
func TestSlskdNeverReachesSABnzbd(t *testing.T) {
	client, rec := slskdTestClient(t, 913901)
	ctx := context.Background()

	if err := TestClient(ctx, client); err != nil {
		t.Fatalf("TestClient: %v", err)
	}
	if err := TestConnection(ctx, client); err != nil {
		t.Fatalf("TestConnection: %v", err)
	}

	if rep, err := GetStalledTorrents(ctx, client); err != nil || rep.UsesTorrentID || len(rep.ClientReported) != 0 {
		t.Errorf("GetStalledTorrents = %+v, %v; want an empty report", rep, err)
	}
	rep, err := CheckCategories(ctx, client)
	if err != nil || rep.Checked {
		t.Errorf("CheckCategories = %+v, %v; slskd has no categories", rep, err)
	}
	info, err := GrabSavePath(ctx, client, models.MediaTypeEbook, "/books", "", "")
	if err != nil || info.Path != "/app/downloads" {
		t.Errorf("GrabSavePath = %+v, %v", info, err)
	}
	info, err = CompletedPath(ctx, client)
	if err != nil || info.Path != "/app/downloads" {
		t.Errorf("CompletedPath = %+v, %v", info, err)
	}
	if SupportsRemapAssistant("slskd") {
		t.Error("remap assistant must not be offered for slskd")
	}
	if vis := CheckCompletedPathVisibility(ctx, client, "/books", "", ""); vis.Status != PathUnknown {
		t.Errorf("CheckCompletedPathVisibility status = %v, want unknown", vis.Status)
	}

	rec.assertSlskdOnly(t)
	if len(rec.reqs) == 0 {
		t.Fatal("no request reached the fake slskd")
	}
}

func TestSlskdTestClientErrors(t *testing.T) {
	t.Cleanup(httpsec.AllowLoopbackForTests())
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"version":{"current":"0.25.1"},"server":{"isLoggedIn":true}}`))
	}))
	defer srv.Close()
	host, port := serverHostPort(t, srv.URL)
	client := &models.DownloadClient{ID: 913902, Type: "slskd", Host: host, Port: port, APIKey: "slskd-key"}
	err := TestClient(context.Background(), client)
	if err == nil || !strings.Contains(err.Error(), "0.26.0") {
		t.Fatalf("err = %v, want the minimum version named", err)
	}
	// The connection check alone does not judge the version; Diagnose reports
	// that as its own row.
	if err := TestConnection(context.Background(), client); err != nil {
		t.Fatalf("TestConnection: %v", err)
	}
}
