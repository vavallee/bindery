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

// TestQueueGrab_SignsDownloadURLWithoutIndexerID replays the Prowlarr grab
// failure: the search response strips the apikey from nzbUrl, so an API client
// that posts only {guid, nzbUrl} — no indexerId — hands back a URL the indexer
// answers with 401, and the grab dies as
// "failed to send to downloader: fetch nzb: indexer returned HTTP 401".
// The apikey must be restored from the configured indexer whose host matches
// the download URL.
func TestQueueGrab_SignsDownloadURLWithoutIndexerID(t *testing.T) {
	defer httpsec.AllowLoopbackForTests()()

	indexerSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("apikey") != "SECRET" {
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

	h, database, _, clients, _, ctx := queueFixture(t)
	host, port := testServerHostPort(t, sab.URL)
	if err := clients.Create(ctx, &models.DownloadClient{
		Name: "sab", Type: "sabnzbd", Host: host, Port: port, Enabled: true,
	}); err != nil {
		t.Fatalf("create client: %v", err)
	}

	indexers := db.NewIndexerRepo(database)
	if err := indexers.Create(ctx, &models.Indexer{
		Name: "prowlarr", Type: "newznab", URL: indexerSrv.URL + "/3/api", APIKey: "SECRET", Enabled: true,
	}); err != nil {
		t.Fatalf("create indexer: %v", err)
	}
	h.WithIndexers(indexers)

	body := bytes.NewBufferString(`{"guid":"guid-1","title":"One Shot","nzbUrl":"` +
		indexerSrv.URL + `/3/download?file=Lee+Child&link=abc"}`)
	rec := httptest.NewRecorder()
	h.Grab(rec, httptest.NewRequest(http.MethodPost, "/api/v1/queue/grab", body))
	if rec.Code != http.StatusAccepted {
		t.Fatalf("expected 202, got %d: %s", rec.Code, rec.Body.String())
	}
}

// TestSignNZBURL covers the paths around that restore: the indexer id names the
// credential when the client sends one, the host match stands in when it does
// not, and neither may hand the key to a host that is not the indexer's.
func TestSignNZBURL(t *testing.T) {
	h, database, _, _, _, ctx := queueFixture(t)
	indexers := db.NewIndexerRepo(database)
	h.WithIndexers(indexers)

	first := &models.Indexer{Name: "prowlarr-1", Type: "newznab", URL: "http://prowlarr:9696/3/api", APIKey: "SECRET", Enabled: true}
	if err := indexers.Create(ctx, first); err != nil {
		t.Fatalf("create indexer: %v", err)
	}

	const dl = "http://prowlarr:9696/3/download?file=Lee+Child&link=abc"
	const signed = "http://prowlarr:9696/3/download?apikey=SECRET&file=Lee+Child&link=abc"

	if got := h.signNZBURL(ctx, dl, &first.ID); got != signed {
		t.Errorf("with indexer id: got %q, want %q", got, signed)
	}
	if got := h.signNZBURL(ctx, dl, nil); got != signed {
		t.Errorf("without indexer id: got %q, want %q", got, signed)
	}
	// A stale id must not lose the key either: the host match still resolves it.
	stale := int64(9999)
	if got := h.signNZBURL(ctx, dl, &stale); got != signed {
		t.Errorf("stale indexer id: got %q, want %q", got, signed)
	}
	// Already signed (scheduler / retry paths) and direct-from-uploader links on
	// a foreign host are both returned untouched.
	if got := h.signNZBURL(ctx, signed, nil); got != signed {
		t.Errorf("already signed: got %q, want it unchanged", got)
	}
	const foreign = "http://uploader.example.com/dl?id=abc"
	if got := h.signNZBURL(ctx, foreign, nil); got != foreign {
		t.Errorf("foreign host: got %q, want it unchanged", got)
	}

	// A second indexer on the same host with the same key stays unambiguous.
	if err := indexers.Create(ctx, &models.Indexer{
		Name: "prowlarr-2", Type: "newznab", URL: "http://prowlarr:9696/4/api", APIKey: "SECRET", Enabled: true,
	}); err != nil {
		t.Fatalf("create indexer: %v", err)
	}
	if got := h.signNZBURL(ctx, dl, nil); got != signed {
		t.Errorf("two indexers, one key: got %q, want %q", got, signed)
	}

	// A third with a different key makes the host ambiguous — guessing would
	// only trade one 401 for another, so the URL is left as it came in.
	if err := indexers.Create(ctx, &models.Indexer{
		Name: "prowlarr-3", Type: "newznab", URL: "http://prowlarr:9696/5/api", APIKey: "OTHER", Enabled: true,
	}); err != nil {
		t.Fatalf("create indexer: %v", err)
	}
	if got := h.signNZBURL(ctx, dl, nil); got != dl {
		t.Errorf("ambiguous host: got %q, want it unchanged", got)
	}
	// The explicit indexer id still resolves it.
	if got := h.signNZBURL(ctx, dl, &first.ID); got != signed {
		t.Errorf("ambiguous host with indexer id: got %q, want %q", got, signed)
	}
}
