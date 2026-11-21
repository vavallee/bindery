package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/vavallee/bindery/internal/db"
	"github.com/vavallee/bindery/internal/indexer"
	"github.com/vavallee/bindery/internal/indexer/newznab"
	"github.com/vavallee/bindery/internal/models"
)

// slskdSearchServer is a slskd that answers every search at once with one
// ebook folder, counting what reaches it.
func slskdSearchServer(t *testing.T, requests *atomic.Int32) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v0/searches", func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		var req struct {
			ID string `json:"id"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		_, _ = w.Write([]byte(`{"id":"` + req.ID + `","isComplete":false}`))
	})
	mux.HandleFunc("/api/v0/searches/", func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		switch {
		case r.Method == http.MethodDelete:
			w.WriteHeader(http.StatusNoContent)
		case r.URL.Query().Get("includeResponses") == "true":
			_, _ = w.Write([]byte(`{"isComplete":true,"responses":[{"username":"peer1","hasFreeUploadSlot":true,"queueLength":0,"uploadSpeed":2000000,
				"files":[{"filename":"Books\\Frank Herbert - Dune.epub","size":2097152,"extension":"epub"}]}]}`))
		default:
			_, _ = w.Write([]byte(`{"isComplete":true}`))
		}
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

func soulseekSearchHandler(t *testing.T, enabled bool, requests *atomic.Int32) *IndexerHandler {
	t.Helper()
	database, err := db.OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { database.Close() })
	srv := slskdSearchServer(t, requests)
	host, port := testServerHostPort(t, srv.URL)
	clients := db.NewDownloadClientRepo(database)
	if err := clients.Create(t.Context(), &models.DownloadClient{
		Name: "my slskd", Type: "slskd", Host: host, Port: port, APIKey: "test-key",
		Enabled: enabled, EnabledForBooks: true, EnabledForAudiobooks: true,
	}); err != nil {
		t.Fatal(err)
	}
	return NewIndexerHandler(db.NewIndexerRepo(database), db.NewBookRepo(database), db.NewAuthorRepo(database),
		db.NewMetadataProfileRepo(database), indexer.NewSearcher(), db.NewSettingsRepo(database), db.NewBlocklistRepo(database)).
		WithDownloadClients(clients).WithSearchResults(NewSearchResultRegistry())
}

func TestSearchQuery_ShowsSoulseekResultWithNullIndexerID(t *testing.T) {
	var requests atomic.Int32
	h := soulseekSearchHandler(t, true, &requests)
	rec := httptest.NewRecorder()
	h.SearchQuery(rec, httptest.NewRequest(http.MethodGet, "/indexer/search?q=dune+herbert", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
	var raw []map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &raw); err != nil {
		t.Fatal(err)
	}
	if len(raw) != 1 {
		t.Fatalf("got %d results, want the one Soulseek result: %s", len(raw), rec.Body.String())
	}
	r := raw[0]
	if v, present := r["indexerId"]; !present || v != nil {
		t.Errorf("indexerId = %v (present %v), want JSON null", v, present)
	}
	if r["protocol"] != "soulseek" || r["indexerName"] != "my slskd" {
		t.Errorf("result = %v", r)
	}
	sl, _ := r["soulseek"].(map[string]any)
	if sl["username"] != "peer1" || sl["freeSlot"] != true || sl["format"] != "epub" {
		t.Errorf("soulseek info = %v", sl)
	}
	if !strings.HasPrefix(r["guid"].(string), "slskd:") {
		t.Errorf("guid = %v", r["guid"])
	}
	if strings.Contains(rec.Body.String(), "test-key") {
		t.Error("the slskd API key reached the search response")
	}
}

// Opt in: a disabled slskd client means nothing is searched on Soulseek.
func TestSearchQuery_DisabledSlskdClientSendsNothing(t *testing.T) {
	var requests atomic.Int32
	h := soulseekSearchHandler(t, false, &requests)
	rec := httptest.NewRecorder()
	h.SearchQuery(rec, httptest.NewRequest(http.MethodGet, "/indexer/search?q=dune+herbert", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d", rec.Code)
	}
	if strings.TrimSpace(rec.Body.String()) != "[]" && strings.TrimSpace(rec.Body.String()) != "null" {
		t.Errorf("body = %s", rec.Body.String())
	}
	if n := requests.Load(); n != 0 {
		t.Errorf("a disabled slskd client received %d requests", n)
	}
}

// A result with no indexer must round trip the search result registry as "no
// indexer", not as id 0.
func TestSearchResultRegistry_NilIndexerIDStaysNil(t *testing.T) {
	r := NewSearchResultRegistry()
	r.remember([]newznab.SearchResult{{GUID: "slskd:1", Title: "T", Protocol: "soulseek"}})
	e, ok := r.lookup("slskd:1")
	if !ok {
		t.Fatal("not recorded")
	}
	var req grabRequest
	e.apply(&req)
	if req.IndexerID != nil {
		t.Errorf("IndexerID = %d, want nil", *req.IndexerID)
	}
}
