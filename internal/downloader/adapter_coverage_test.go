package downloader

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/vavallee/bindery/internal/httpsec"
	"github.com/vavallee/bindery/internal/models"
)

// jsonRPCRecorder is a JSON-RPC endpoint (Deluge Web UI or NZBGet shape) that
// answers each method from a table and records the methods and params in
// order. A method missing from the table answers an RPC error.
type jsonRPCRecorder struct {
	mu      sync.Mutex
	answers map[string]any
	methods []string
	params  map[string][]any
}

func newJSONRPCRecorder(t *testing.T, answers map[string]any) (*httptest.Server, *jsonRPCRecorder) {
	t.Helper()
	rec := &jsonRPCRecorder{answers: answers, params: map[string][]any{}}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Method string `json:"method"`
			Params []any  `json:"params"`
			ID     int64  `json:"id"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		rec.mu.Lock()
		rec.methods = append(rec.methods, req.Method)
		rec.params[req.Method] = req.Params
		answer, ok := rec.answers[req.Method]
		rec.mu.Unlock()
		if !ok {
			_ = json.NewEncoder(w).Encode(map[string]any{"result": nil, "error": map[string]any{"code": -1, "message": "no such method " + req.Method}, "id": req.ID})
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"version": "1.1", "result": answer, "error": nil, "id": req.ID})
	}))
	t.Cleanup(srv.Close)
	return srv, rec
}

func (r *jsonRPCRecorder) saw(method string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, m := range r.methods {
		if m == method {
			return true
		}
	}
	return false
}

func (r *jsonRPCRecorder) paramsOf(method string) []any {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.params[method]
}

func TestSendDownload_Deluge(t *testing.T) {
	const hash = "aabbccddeeff00112233445566778899aabbccdd"
	srv, rec := newJSONRPCRecorder(t, map[string]any{
		"auth.login": true, "web.connected": true,
		"core.add_torrent_magnet": hash,
		"label.set_torrent":       nil,
	})
	client := fakeClient(t, srv, "deluge")
	client.Category, client.CategoryAudiobook = "books", "Audio"
	minutes := 30
	res, err := SendDownload(context.Background(), client, "magnet:?xt=urn:btih:"+hash, "", SendOptions{
		MediaType:       models.MediaTypeAudiobook,
		SeedTimeMinutes: &minutes, // Deluge cannot carry it; logged, not sent
	})
	if err != nil {
		t.Fatalf("SendDownload: %v", err)
	}
	if res.RemoteID != hash || res.Protocol != "torrent" || !res.UsesTorrentID {
		t.Errorf("result = %+v", res)
	}
	p := rec.paramsOf("label.set_torrent")
	if len(p) != 2 || p[1] != "audio" {
		t.Errorf("label.set_torrent params = %v, want the audiobook label lowercased", p)
	}
}

func TestSendDownload_DelugeError(t *testing.T) {
	srv, _ := newJSONRPCRecorder(t, map[string]any{"auth.login": false})
	client := fakeClient(t, srv, "deluge")
	if _, err := SendDownload(context.Background(), client, "magnet:?xt=urn:btih:aa", ""); err == nil || !strings.Contains(err.Error(), "wrong password") {
		t.Fatalf("err = %v", err)
	}
}

func TestSendDownload_NZBGet(t *testing.T) {
	defer httpsec.AllowLoopbackForTests()()
	indexer := fakeNZBIndexer(t)
	defer indexer.Close()
	srv, rec := newJSONRPCRecorder(t, map[string]any{
		"config": []map[string]string{{"Name": "Category1.Name", "Value": "books"}},
		"append": 314,
	})
	client := fakeClient(t, srv, "nzbget")
	client.Category = "books"
	res, err := SendDownload(context.Background(), client, indexer.URL+"/get.nzb", "The Book")
	if err != nil {
		t.Fatalf("SendDownload: %v", err)
	}
	if res.RemoteID != "314" || res.Protocol != "usenet" || res.UsesTorrentID {
		t.Errorf("result = %+v", res)
	}
	if !rec.saw("append") {
		t.Error("append was never called")
	}
}

func TestSendDownload_NZBGetUnknownCategory(t *testing.T) {
	defer httpsec.AllowLoopbackForTests()()
	indexer := fakeNZBIndexer(t)
	defer indexer.Close()
	srv, rec := newJSONRPCRecorder(t, map[string]any{
		"config": []map[string]string{{"Name": "Category1.Name", "Value": "tv"}},
		"append": 1,
	})
	client := fakeClient(t, srv, "nzbget")
	client.Category = "books"
	if _, err := SendDownload(context.Background(), client, indexer.URL+"/get.nzb", "The Book"); err == nil {
		t.Fatal("expected a category mismatch error")
	}
	if rec.saw("append") {
		t.Error("append ran although the category does not exist")
	}
}

func TestSendDownload_RtorrentError(t *testing.T) {
	client := fakeClient(t, rtorrentDirFake(t, "", true), "rtorrent")
	if _, err := SendDownload(context.Background(), client, "magnet:?xt=urn:btih:aabbccddeeff00112233445566778899aabbccdd", ""); err == nil {
		t.Fatal("expected the rTorrent failure to fail the grab")
	}
}

// TestSendDownload_QbittorrentShareLimitFailureIsNotFatal: the torrent is
// already added when setShareLimits runs, so its failure is only logged.
func TestSendDownload_QbittorrentShareLimitFailureIsNotFatal(t *testing.T) {
	var shareCalls int
	var mu sync.Mutex
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v2/auth/login":
			_, _ = w.Write([]byte("Ok."))
		case "/api/v2/torrents/add":
			_, _ = w.Write([]byte("Ok."))
		case "/api/v2/torrents/info":
			_ = json.NewEncoder(w).Encode([]map[string]any{{"hash": "ABCDEF123", "progress": 0.0}})
		case "/api/v2/torrents/setShareLimits":
			mu.Lock()
			shareCalls++
			mu.Unlock()
			http.Error(w, "nope", http.StatusInternalServerError)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()
	client := fakeClient(t, srv, "qbittorrent")
	ratio := 2.0
	res, err := SendDownload(context.Background(), client, "magnet:?xt=urn:btih:ABCDEF123&dn=Book", "", SendOptions{SeedRatio: &ratio})
	if err != nil {
		t.Fatalf("a share-limit failure failed the grab: %v", err)
	}
	if res.RemoteID != "abcdef123" {
		t.Errorf("RemoteID = %q, want the lowercased hash", res.RemoteID)
	}
	mu.Lock()
	defer mu.Unlock()
	if shareCalls == 0 {
		t.Error("setShareLimits was never attempted")
	}
}

func TestLogValue(t *testing.T) {
	if got := logValue[int](nil); got != nil {
		t.Errorf("logValue(nil) = %v, want nil", got)
	}
	v := 2.5
	if got := logValue(&v); got != 2.5 {
		t.Errorf("logValue(&2.5) = %v, want the value, not the pointer", got)
	}
}

func TestRemoveDownload_Deluge(t *testing.T) {
	srv, rec := newJSONRPCRecorder(t, map[string]any{
		"auth.login": true, "web.connected": true, "core.remove_torrent": true,
	})
	client := fakeClient(t, srv, "deluge")
	hash := "abc"
	if err := RemoveDownload(context.Background(), client, &models.Download{TorrentID: &hash}, true, ""); err != nil {
		t.Fatalf("RemoveDownload: %v", err)
	}
	p := rec.paramsOf("core.remove_torrent")
	if len(p) != 2 || p[0] != "abc" || p[1] != true {
		t.Errorf("core.remove_torrent params = %v, want [abc true]", p)
	}
}

func TestRemoveDownload_NZBGet(t *testing.T) {
	srv, rec := newJSONRPCRecorder(t, map[string]any{"editqueue": true})
	client := fakeClient(t, srv, "nzbget")
	id := "42"
	if err := RemoveDownload(context.Background(), client, &models.Download{SABnzbdNzoID: &id}, false, ""); err != nil {
		t.Fatalf("RemoveDownload: %v", err)
	}
	p := rec.paramsOf("editqueue")
	if len(p) != 3 || p[0] != "GroupParkDelete" {
		t.Errorf("editqueue params = %v, want GroupParkDelete when keeping files", p)
	}

	bad := "nzo_abc"
	if err := RemoveDownload(context.Background(), client, &models.Download{SABnzbdNzoID: &bad}, false, ""); err == nil || !strings.Contains(err.Error(), "invalid nzbget id") {
		t.Fatalf("non-numeric id: err = %v", err)
	}
}

// TestRemoveDownload_MissingIDsAreNoOps: a row with no remote id has nothing
// to remove, and no client is contacted (the client below points nowhere).
func TestRemoveDownload_MissingIDsAreNoOps(t *testing.T) {
	empty := ""
	for _, typ := range []string{"transmission", "qbittorrent", "deluge", "rtorrent", "nzbget", "sabnzbd"} {
		for _, dl := range []*models.Download{{}, {TorrentID: &empty, SABnzbdNzoID: &empty}} {
			client := &models.DownloadClient{ID: 1_000_000 + coverageClientID.Add(1), Type: typ, Host: "127.0.0.1", Port: 1}
			if err := RemoveDownload(context.Background(), client, dl, true, ""); err != nil {
				t.Errorf("%s: RemoveDownload with no id = %v, want nil", typ, err)
			}
		}
	}
}

// TestRemoveDownload_RtorrentBasePathFailure: when the data path cannot be
// resolved the files are left alone, and the torrent is still erased.
func TestRemoveDownload_RtorrentBasePathFailure(t *testing.T) {
	var mu sync.Mutex
	var methods []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "text/xml")
		mu.Lock()
		defer mu.Unlock()
		switch {
		case strings.Contains(string(body), "<methodName>d.base_path</methodName>"):
			methods = append(methods, "d.base_path")
			_, _ = io.WriteString(w, `<?xml version="1.0"?><methodResponse><fault><value><struct>
<member><name>faultCode</name><value><int>-501</int></value></member>
<member><name>faultString</name><value><string>Could not find info-hash.</string></value></member>
</struct></value></fault></methodResponse>`)
		case strings.Contains(string(body), "<methodName>d.erase</methodName>"):
			methods = append(methods, "d.erase")
			_, _ = io.WriteString(w, `<?xml version="1.0"?><methodResponse><params><param><value><i8>0</i8></value></param></params></methodResponse>`)
		default:
			_, _ = io.WriteString(w, `<?xml version="1.0"?><methodResponse><params><param><value><i8>0</i8></value></param></params></methodResponse>`)
		}
	}))
	defer srv.Close()
	client := fakeClient(t, srv, "rtorrent")
	hash := rtorrentTestHash
	if err := RemoveDownload(context.Background(), client, &models.Download{TorrentID: &hash}, true, ""); err != nil {
		t.Fatalf("RemoveDownload: %v", err)
	}
	mu.Lock()
	defer mu.Unlock()
	if strings.Join(methods, ",") != "d.base_path,d.erase" {
		t.Errorf("methods = %v, want the path lookup then the erase", methods)
	}
}

func TestByteHelpers(t *testing.T) {
	if got := megabytesToBytes(0); got != 0 {
		t.Errorf("megabytesToBytes(0) = %d", got)
	}
	if got := megabytesToBytes(-3); got != 0 {
		t.Errorf("megabytesToBytes(-3) = %d", got)
	}
	if got := megabytesToBytes(1.5); got != 1572864 {
		t.Errorf("megabytesToBytes(1.5) = %d", got)
	}
	if maxInt64(5, 3) != 5 || maxInt64(3, 5) != 5 || maxInt64(-1, -1) != -1 {
		t.Error("maxInt64 picked the wrong value")
	}
}

func TestDefaultCacheAndEvict(t *testing.T) {
	cache := withIsolatedCache(t)
	if DefaultCache() != cache {
		t.Fatal("DefaultCache did not return the installed cache")
	}
	client := &models.DownloadClient{ID: 77, Type: "qbittorrent", Host: "127.0.0.1", Port: 1}
	first := QbittorrentFor(client)
	if cache.Len() != 1 {
		t.Fatalf("Len = %d after one lookup", cache.Len())
	}
	Evict(77)
	if cache.Len() != 0 {
		t.Fatalf("Len = %d after Evict", cache.Len())
	}
	if QbittorrentFor(client) == first {
		t.Error("an evicted client was handed back instead of a fresh one")
	}
	if cache.ConstructorCount() != 2 {
		t.Errorf("ConstructorCount = %d, want 2", cache.ConstructorCount())
	}
}

func TestHealthStoreAttach(t *testing.T) {
	store := NewHealthStore()
	store.Set(5, models.DownloadClientHealth{Status: HealthOK, Message: "fine"})

	client := &models.DownloadClient{ID: 5}
	store.Attach(client)
	if client.Health == nil || client.Health.Status != HealthOK || client.Health.Message != "fine" {
		t.Fatalf("Health = %+v", client.Health)
	}

	other := &models.DownloadClient{ID: 6, Health: &models.DownloadClientHealth{Status: HealthError}}
	store.Attach(other)
	if other.Health != nil {
		t.Errorf("a client with no stored health kept a stale value: %+v", other.Health)
	}

	// Nil receivers and nil clients are tolerated.
	var nilStore *HealthStore
	nilStore.Attach(client)
	store.Attach(nil)
}

func TestStallKindDescriptions(t *testing.T) {
	tests := []struct {
		kind              StallKind
		label             string
		reasonHas         string
		blocklists, wipes bool
	}{
		{StallNone, "none", "BUG", false, false},
		{StallClientReported, "client_reported", "no peers", true, true},
		{StallNoMetadata, "no_metadata", "never resolved", false, false},
	}
	for _, tt := range tests {
		if got := tt.kind.String(); got != tt.label {
			t.Errorf("%d.String() = %q, want %q", tt.kind, got, tt.label)
		}
		if got := tt.kind.Reason(); !strings.Contains(got, tt.reasonHas) {
			t.Errorf("%s.Reason() = %q, want it to mention %q", tt.label, got, tt.reasonHas)
		}
		if got := tt.kind.Blocklists(); got != tt.blocklists {
			t.Errorf("%s.Blocklists() = %v", tt.label, got)
		}
		if got := tt.kind.DeletesData(); got != tt.wipes {
			t.Errorf("%s.DeletesData() = %v", tt.label, got)
		}
	}
}
