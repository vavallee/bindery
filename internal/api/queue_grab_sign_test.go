package api

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
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

// leakedAPIKey is deliberately distinctive: when one of the assertions below
// trips, the failure message points at the indexer credential itself rather
// than at an anonymous "unexpected substring".
const leakedAPIKey = "SECRET-APIKEY-MUST-NOT-LEAK"

// assertNoIndexerAPIKey fails when the credential appears anywhere in a
// response body. It checks the raw bytes rather than a decoded nzbUrl field on
// purpose: a handler that grows a second field carrying the same URL, or embeds
// the download record inside a new envelope, is caught by this just the same.
func assertNoIndexerAPIKey(t *testing.T, surface string, body []byte) {
	t.Helper()
	if strings.Contains(string(body), leakedAPIKey) {
		t.Errorf("%s leaked the indexer apikey to the caller: %s", surface, body)
	}
}

// TestQueueGrab_ResponseAndListNeverCarryIndexerAPIKey pins the security
// boundary the signing fallback sits on. signNZBURL puts the shared indexer
// apikey back onto the download URL so the download client can fetch the NZB,
// and the URL is persisted in that form — but the indexer apikey is an
// admin-only setting, while the queue is readable by the non-admin user who
// owns the download. So nothing that travels back out to an HTTP caller may
// carry it.
//
// The grab here goes through the new fallback (no indexerId in the request
// body), so it asserts the signing happened — the indexer only answers 200 to
// an authenticated fetch — and then that neither the grab response nor the
// queue listing echoes the key back. The stored row is checked too, to show the
// redaction is a response-shaping step and has not quietly undone the fix.
func TestQueueGrab_ResponseAndListNeverCarryIndexerAPIKey(t *testing.T) {
	defer httpsec.AllowLoopbackForTests()()

	var signedFetch atomic.Bool
	indexerSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("apikey") != leakedAPIKey {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		signedFetch.Store(true)
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
	if err := indexers.Create(ctx, &models.Indexer{
		Name: "prowlarr", Type: "newznab", URL: indexerSrv.URL + "/3/api", APIKey: leakedAPIKey, Enabled: true,
	}); err != nil {
		t.Fatalf("create indexer: %v", err)
	}
	h.WithIndexers(indexers)

	// No indexerId: the request an API client (script, curl) actually sends, so
	// the apikey can only come from signNZBURL's host-match fallback.
	unsigned := indexerSrv.URL + "/3/download?file=Lee+Child&link=abc"
	body := bytes.NewBufferString(`{"guid":"guid-redact","title":"One Shot","nzbUrl":"` + unsigned + `"}`)
	rec := httptest.NewRecorder()
	h.Grab(rec, httptest.NewRequest(http.MethodPost, "/api/v1/queue/grab", body))
	if rec.Code != http.StatusAccepted {
		t.Fatalf("expected 202, got %d: %s", rec.Code, rec.Body.String())
	}
	if !signedFetch.Load() {
		t.Fatal("indexer never saw an authenticated fetch: the URL was not signed, so this test would pass vacuously")
	}

	// 1. The grab response itself — the surface this PR newly puts a signed URL
	//    behind, and the one the review asked about.
	assertNoIndexerAPIKey(t, "grab response", rec.Body.Bytes())
	var grabbed models.Download
	if err := json.Unmarshal(rec.Body.Bytes(), &grabbed); err != nil {
		t.Fatalf("decode grab response: %v (body=%s)", err, rec.Body.String())
	}
	if grabbed.NZBURL != unsigned {
		t.Errorf("grab response nzbUrl = %q, want the redacted form %q", grabbed.NZBURL, unsigned)
	}

	// 2. The queue listing, which the owning non-admin user polls every few
	//    seconds.
	listRec := httptest.NewRecorder()
	h.List(listRec, httptest.NewRequest(http.MethodGet, "/api/v1/queue", nil))
	if listRec.Code != http.StatusOK {
		t.Fatalf("expected 200 from list, got %d: %s", listRec.Code, listRec.Body.String())
	}
	assertNoIndexerAPIKey(t, "queue listing", listRec.Body.Bytes())
	var listed queueListResponse
	if err := json.Unmarshal(listRec.Body.Bytes(), &listed); err != nil {
		t.Fatalf("decode queue list: %v (body=%s)", err, listRec.Body.String())
	}
	found := false
	for _, item := range listed.Items {
		if item.GUID == "guid-redact" {
			found = true
			if item.NZBURL != unsigned {
				t.Errorf("queue listing nzbUrl = %q, want the redacted form %q", item.NZBURL, unsigned)
			}
		}
	}
	if !found {
		t.Fatalf("grabbed download missing from the queue listing, so the redaction assertion proved nothing: %s", listRec.Body.String())
	}

	// 3. The stored row must still hold the signed URL — retries and the
	//    importer re-send it, and redacting it at rest would trade this leak for
	//    the 401 the PR set out to fix.
	stored, err := downloads.GetByGUID(ctx, "guid-redact")
	if err != nil || stored == nil {
		t.Fatalf("read back stored download: %v", err)
	}
	if !strings.Contains(stored.NZBURL, leakedAPIKey) {
		t.Errorf("stored nzbUrl = %q, want it to keep the apikey for retries", stored.NZBURL)
	}
}

// TestPendingList_RedactsIndexerAPIKey covers the sibling surface the audit for
// the review turned up: the scheduler stores a delay-rejected release as the
// raw indexer SearchResult, whose nzbUrl the newznab client had already signed,
// and the pending list handed that blob back to the caller verbatim.
func TestPendingList_RedactsIndexerAPIKey(t *testing.T) {
	h, database, _, _, _, ctx := queueFixture(t)
	books := db.NewBookRepo(database)
	authors := db.NewAuthorRepo(database)
	a := &models.Author{ForeignID: "OL-A", Name: "Lee Child", SortName: "child lee", MetadataProvider: "openlibrary"}
	if err := authors.Create(ctx, a); err != nil {
		t.Fatal(err)
	}
	b := &models.Book{
		ForeignID: "OL-B", AuthorID: a.ID, Title: "One Shot", SortTitle: "one shot",
		Status: models.BookStatusWanted, Genres: []string{}, MetadataProvider: "openlibrary", Monitored: true,
	}
	if err := books.Create(ctx, b); err != nil {
		t.Fatal(err)
	}

	signed := "http://prowlarr:9696/3/download?apikey=" + leakedAPIKey + "&link=abc"
	pending := db.NewPendingReleaseRepo(database)
	if err := pending.Upsert(ctx, &models.PendingRelease{
		BookID: b.ID, MediaType: models.MediaTypeEbook, Title: "One Shot", GUID: "guid-pending",
		Protocol: "usenet", Reason: "delay",
		ReleaseJSON: `{"guid":"guid-pending","title":"One Shot","nzbUrl":"` + signed + `","size":123}`,
	}); err != nil {
		t.Fatal(err)
	}

	ph := NewPendingHandler(pending, h, db.NewDownloadRepo(database), books)
	rec := httptest.NewRecorder()
	ph.List(rec, httptest.NewRequest(http.MethodGet, "/api/v1/pending", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	assertNoIndexerAPIKey(t, "pending listing", rec.Body.Bytes())

	// The entry must survive redaction intact — dropping releaseJson would also
	// pass the assertion above while breaking the UI.
	var got []pendingItem
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode pending list: %v (body=%s)", err, rec.Body.String())
	}
	if len(got) != 1 {
		t.Fatalf("expected 1 pending release, got %d", len(got))
	}
	var release map[string]any
	if err := json.Unmarshal([]byte(got[0].ReleaseJSON), &release); err != nil {
		t.Fatalf("decode releaseJson: %v", err)
	}
	if release["nzbUrl"] != "http://prowlarr:9696/3/download?link=abc" {
		t.Errorf("releaseJson nzbUrl = %v, want the redacted form", release["nzbUrl"])
	}
	if release["guid"] != "guid-pending" || release["title"] != "One Shot" {
		t.Errorf("redaction dropped fields from the release blob: %v", release)
	}
	// The stored blob keeps the key: force-grab re-sends this URL.
	stored, err := pending.List(ctx)
	if err != nil || len(stored) != 1 {
		t.Fatalf("read back pending: %v", err)
	}
	if !strings.Contains(stored[0].ReleaseJSON, leakedAPIKey) {
		t.Errorf("stored releaseJson lost the apikey, breaking force-grab: %s", stored[0].ReleaseJSON)
	}
}
