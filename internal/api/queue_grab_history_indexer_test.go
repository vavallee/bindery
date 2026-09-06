package api

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/vavallee/bindery/internal/db"
	"github.com/vavallee/bindery/internal/httpsec"
	"github.com/vavallee/bindery/internal/models"
)

// TestQueueGrab_HistoryRecordsResolvedIndexerID is the regression guard for
// #2368. When the request omits indexerId, signNZBURL's host-match fallback
// (#2053) works out which indexer owns the URL and the download row is stamped
// with it. The grab history event was still reading the request field, so
// history's indexer attribution was null for exactly the callers the fallback
// exists to serve.
func TestQueueGrab_HistoryRecordsResolvedIndexerID(t *testing.T) {
	defer httpsec.AllowLoopbackForTests()()

	indexerSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("apikey") != leakedAPIKey {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		_, _ = w.Write([]byte(`<?xml version="1.0" encoding="UTF-8"?><nzb></nzb>`))
	}))
	defer indexerSrv.Close()

	sab := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"status": true, "nzo_ids": []string{"nzo-1"}})
	}))
	defer sab.Close()

	h, database, downloads, clients, _, ctx := queueFixture(t)
	host, port := testServerHostPort(t, sab.URL)
	if err := clients.Create(ctx, &models.DownloadClient{
		Name: "sab", Type: "sabnzbd", Host: host, Port: port, Enabled: true,
	}); err != nil {
		t.Fatalf("create client: %v", err)
	}

	indexers := db.NewIndexerRepo(database)
	idx := &models.Indexer{
		Name: "prowlarr", Type: "newznab", URL: indexerSrv.URL + "/3/api", APIKey: leakedAPIKey, Enabled: true,
	}
	if err := indexers.Create(ctx, idx); err != nil {
		t.Fatalf("create indexer: %v", err)
	}
	h.WithIndexers(indexers)

	// No indexerId in the body: the shape an API client sends, and the only
	// shape where the fallback has anything to resolve.
	unsigned := indexerSrv.URL + "/3/download?file=Lee+Child&link=abc"
	body := bytes.NewBufferString(`{"guid":"guid-history","title":"One Shot","nzbUrl":"` + unsigned + `"}`)
	rec := httptest.NewRecorder()
	h.Grab(rec, httptest.NewRequest(http.MethodPost, "/api/v1/queue/grab", body))
	if rec.Code != http.StatusAccepted {
		t.Fatalf("expected 202, got %d: %s", rec.Code, rec.Body.String())
	}

	// The download row is the reference: history must agree with it.
	stored, err := downloads.GetByGUID(ctx, "guid-history")
	if err != nil {
		t.Fatalf("GetByGUID: %v", err)
	}
	if stored.IndexerID == nil || *stored.IndexerID != idx.ID {
		t.Fatalf("download row indexer id = %v, want %d — the fallback did not resolve, so this test would prove nothing",
			stored.IndexerID, idx.ID)
	}

	events, err := db.NewHistoryRepo(database).ListByType(ctx, models.HistoryEventGrabbed)
	if err != nil {
		t.Fatalf("ListByType: %v", err)
	}
	if len(events) != 1 {
		t.Fatalf("expected 1 grabbed history event, got %d", len(events))
	}
	var payload struct {
		IndexerID *int64 `json:"indexerId"`
	}
	if err := json.Unmarshal([]byte(events[0].Data), &payload); err != nil {
		t.Fatalf("decode history data %q: %v", events[0].Data, err)
	}
	if payload.IndexerID == nil {
		t.Fatalf("grab history recorded a null indexerId; want %d (the id the download row got)", idx.ID)
	}
	if *payload.IndexerID != idx.ID {
		t.Errorf("grab history indexerId = %d, want %d", *payload.IndexerID, idx.ID)
	}
}
