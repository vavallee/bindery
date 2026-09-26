package api

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/vavallee/bindery/internal/auth"
	"github.com/vavallee/bindery/internal/db"
	"github.com/vavallee/bindery/internal/httpsec"
	"github.com/vavallee/bindery/internal/models"
)

// sabAddRecorder stands in for SABnzbd: it accepts an addfile and records the
// release URL it was handed, so a retry can be shown to re-send the SAME
// release rather than run a fresh search (#2295).
type sabAddRecorder struct {
	calls int
	names []string
}

func newSabRecorder(t *testing.T, rec *sabAddRecorder) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("mode") != "addfile" {
			t.Errorf("expected mode=addfile, got %q", r.URL.Query().Get("mode"))
		}
		rec.calls++
		rec.names = append(rec.names, r.URL.Query().Get("nzbname"))
		_ = json.NewEncoder(w).Encode(map[string]any{
			"status":  true,
			"nzo_ids": []string{"nzo-" + strconv.Itoa(rec.calls)},
		})
	}))
	t.Cleanup(srv.Close)
	return srv
}

// retryFixture wires a queue handler with one enabled usenet client and an
// indexer server that serves the stored release, and returns the release URL.
func retryFixture(t *testing.T, adds *sabAddRecorder) (*QueueHandler, *sql.DB, *db.DownloadRepo, string) {
	t.Helper()
	t.Cleanup(httpsec.AllowLoopbackForTests())

	indexerSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`<?xml version="1.0" encoding="UTF-8"?><nzb></nzb>`))
	}))
	t.Cleanup(indexerSrv.Close)

	sab := newSabRecorder(t, adds)
	h, database, downloads, clients, _, ctx := queueFixture(t)
	host, port := testServerHostPort(t, sab.URL)
	if err := clients.Create(ctx, &models.DownloadClient{
		Name:    "sab",
		Type:    "sabnzbd",
		Host:    host,
		Port:    port,
		Enabled: true,
	}); err != nil {
		t.Fatalf("create client: %v", err)
	}
	return h, database, downloads, indexerSrv.URL + "/stored.nzb"
}

// seedTwoUsers returns (owner, caller) ids: downloads.owner_user_id is a real
// FK, so a tenancy case needs rows in users rather than made-up ids.
func seedTwoUsers(t *testing.T, database *sql.DB, ctx context.Context) (int64, int64) {
	t.Helper()
	users := db.NewUserRepo(database)
	owner, err := users.Create(ctx, "owner", "x")
	if err != nil {
		t.Fatalf("create owner: %v", err)
	}
	caller, err := users.Create(ctx, "caller", "x")
	if err != nil {
		t.Fatalf("create caller: %v", err)
	}
	return owner.ID, caller.ID
}

func retryDownloadRequest(t *testing.T, id int64) *http.Request {
	t.Helper()
	idStr := strconv.FormatInt(id, 10)
	return withURLParam(httptest.NewRequest(http.MethodPost, "/api/v1/queue/"+idStr+"/retry", nil), "id", idStr)
}

// TestQueueRetryDownload_ResendsTheStoredRelease pins the whole point of the
// failed-stage retry: the row already holds the release, so retrying it sends
// that release again. It does NOT re-search — re-searching is what the book
// page's Search button does, and conflating the two makes a fifty-row bulk
// retry unpredictable (#2295).
func TestQueueRetryDownload_ResendsTheStoredRelease(t *testing.T) {
	var adds sabAddRecorder
	h, _, downloads, releaseURL := retryFixture(t, &adds)
	ctx := t.Context()

	dl := &models.Download{
		GUID:         "failed-guid",
		Title:        "Dune EPUB",
		NZBURL:       releaseURL,
		Size:         4242,
		Status:       models.StateFailed,
		Protocol:     "usenet",
		Quality:      "epub",
		ErrorMessage: "connection refused",
	}
	if err := downloads.Create(ctx, dl); err != nil {
		t.Fatalf("create failed download: %v", err)
	}

	rec := httptest.NewRecorder()
	h.RetryDownload(rec, retryDownloadRequest(t, dl.ID))
	if rec.Code != http.StatusAccepted {
		t.Fatalf("expected 202, got %d: %s", rec.Code, rec.Body.String())
	}
	if adds.calls != 1 {
		t.Fatalf("expected exactly one send to the download client, got %d", adds.calls)
	}

	got, err := downloads.GetByID(ctx, dl.ID)
	if err != nil || got == nil {
		t.Fatalf("reload download: %v", err)
	}
	if got.Status != models.StateDownloading {
		t.Fatalf("expected the row back in downloading, got %q", got.Status)
	}
	if got.GUID != "failed-guid" || got.NZBURL != releaseURL || got.Title != "Dune EPUB" || got.Size != 4242 {
		t.Fatalf("retry must re-send the stored release unchanged, got %+v", got)
	}
	if got.ErrorMessage != "" {
		t.Fatalf("expected the previous failure cleared, got %q", got.ErrorMessage)
	}
	if got.SABnzbdNzoID == nil || *got.SABnzbdNzoID != "nzo-1" {
		t.Fatalf("expected the new client job id recorded, got %v", got.SABnzbdNzoID)
	}
}

// TestQueueRetryDownload_RefusesLiveRow: a row that is still live work must not
// be re-sent, or the retry duplicates the download. Same gate the search page's
// Grab applies (regrabbable).
func TestQueueRetryDownload_RefusesLiveRow(t *testing.T) {
	var adds sabAddRecorder
	h, _, downloads, releaseURL := retryFixture(t, &adds)
	ctx := t.Context()

	dl := &models.Download{
		GUID:     "live-guid",
		Title:    "Live Release",
		NZBURL:   releaseURL,
		Status:   models.StateDownloading,
		Protocol: "usenet",
	}
	if err := downloads.Create(ctx, dl); err != nil {
		t.Fatalf("create download: %v", err)
	}

	rec := httptest.NewRecorder()
	h.RetryDownload(rec, retryDownloadRequest(t, dl.ID))
	if rec.Code != http.StatusConflict {
		t.Fatalf("expected 409, got %d: %s", rec.Code, rec.Body.String())
	}
	if adds.calls != 0 {
		t.Fatalf("expected no send to the download client, got %d", adds.calls)
	}
}

// TestResendRelease_GateRefusesBeforeTouchingTheClient pins the gate itself,
// not just the status code the handler ends up with: resendRelease refuses a
// live row with errRetryNotResendable and a message naming the state, so the
// refusal is the retry's own and does not depend on grab's "already grabbed"
// path catching it a layer later.
func TestResendRelease_GateRefusesBeforeTouchingTheClient(t *testing.T) {
	var adds sabAddRecorder
	h, _, downloads, releaseURL := retryFixture(t, &adds)
	ctx := t.Context()

	for _, status := range []models.DownloadState{
		models.StateGrabbed,
		models.StateDownloading,
		models.StateCompleted,
		models.StateImportPending,
		models.StateImporting,
		models.StateImportFailed,
		models.StateImportExternal,
		models.StateImportHeld,
	} {
		dl := &models.Download{GUID: "gate-" + string(status), Title: "Gate " + string(status), NZBURL: releaseURL, Status: status, Protocol: "usenet"}
		if err := downloads.Create(ctx, dl); err != nil {
			t.Fatalf("create %s: %v", status, err)
		}
		_, err := h.resendRelease(ctx, *dl)
		if !errors.Is(err, errRetryNotResendable) {
			t.Errorf("%s: expected errRetryNotResendable, got %v", status, err)
		}
		if err != nil && !strings.Contains(err.Error(), string(status)) {
			t.Errorf("%s: refusal should name the state, got %q", status, err)
		}
	}
	if adds.calls != 0 {
		t.Fatalf("a refused retry must not reach the download client, got %d sends", adds.calls)
	}
}

func TestQueueRetryDownload_NotFound(t *testing.T) {
	var adds sabAddRecorder
	h, _, _, _ := retryFixture(t, &adds)
	rec := httptest.NewRecorder()
	h.RetryDownload(rec, retryDownloadRequest(t, 4242))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d: %s", rec.Code, rec.Body.String())
	}
}

// TestQueueRetryDownload_AnotherUsersRowIs404 is the tenancy guard: 404 rather
// than 403, so the id space stays unprobeable (D3).
func TestQueueRetryDownload_AnotherUsersRowIs404(t *testing.T) {
	auth.SetEnforceTenancyForTests(t, true)
	var adds sabAddRecorder
	h, database, downloads, releaseURL := retryFixture(t, &adds)
	ctx := t.Context()
	owner, caller := seedTwoUsers(t, database, ctx)

	dl := &models.Download{
		GUID:        "other-users-guid",
		Title:       "Not Yours",
		NZBURL:      releaseURL,
		Status:      models.StateFailed,
		Protocol:    "usenet",
		OwnerUserID: owner,
	}
	if err := downloads.Create(ctx, dl); err != nil {
		t.Fatalf("create download: %v", err)
	}

	req := retryDownloadRequest(t, dl.ID)
	req = req.WithContext(auth.WithUserID(req.Context(), caller))
	rec := httptest.NewRecorder()
	h.RetryDownload(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("expected 404 for another user's row, got %d: %s", rec.Code, rec.Body.String())
	}
	if adds.calls != 0 {
		t.Fatalf("expected no send to the download client, got %d", adds.calls)
	}
}

type bulkRetryBody struct {
	Results map[string]struct {
		OK     bool   `json:"ok"`
		Error  string `json:"error"`
		Action string `json:"action"`
	} `json:"results"`
}

func postBulkRetry(t *testing.T, h *QueueHandler, body string) (*httptest.ResponseRecorder, bulkRetryBody) {
	t.Helper()
	rec := httptest.NewRecorder()
	h.BulkRetry(rec, httptest.NewRequest(http.MethodPost, "/api/v1/queue/bulk-retry", bytes.NewBufferString(body)))
	var parsed bulkRetryBody
	if rec.Code == http.StatusOK {
		if err := json.Unmarshal(rec.Body.Bytes(), &parsed); err != nil {
			t.Fatalf("decode bulk-retry response: %v (%s)", err, rec.Body.String())
		}
	}
	return rec, parsed
}

// TestQueueBulkRetry_OneCallPerSelection covers the fan-out this endpoint
// exists to remove: the page used to fire one POST per selected row from the
// browser. One request now retries the whole selection, and each row gets the
// retry its own state has: an import-stage row re-arms its import, a failed
// row has its release re-sent.
func TestQueueBulkRetry_OneCallPerSelection(t *testing.T) {
	var adds sabAddRecorder
	h, _, downloads, releaseURL := retryFixture(t, &adds)
	ctx := t.Context()

	failed := &models.Download{GUID: "bulk-failed", Title: "Failed Row", NZBURL: releaseURL, Status: models.StateFailed, Protocol: "usenet", Quality: "epub", ErrorMessage: "refused"}
	importFailed := &models.Download{GUID: "bulk-import-failed", Title: "Import Failed Row", NZBURL: releaseURL, Status: models.StateImportFailed, Protocol: "usenet", ErrorMessage: "no match"}
	blocked := &models.Download{GUID: "bulk-import-blocked", Title: "Blocked Row", NZBURL: releaseURL, Status: models.StateImportBlocked, Protocol: "usenet", ErrorMessage: "permission denied"}
	live := &models.Download{GUID: "bulk-live", Title: "Live Row", NZBURL: releaseURL, Status: models.StateDownloading, Protocol: "usenet"}
	for _, dl := range []*models.Download{failed, importFailed, blocked, live} {
		if err := downloads.Create(ctx, dl); err != nil {
			t.Fatalf("create %s: %v", dl.GUID, err)
		}
	}

	ids := []int64{failed.ID, importFailed.ID, blocked.ID, live.ID}
	payload, _ := json.Marshal(map[string]any{"ids": ids})
	rec, body := postBulkRetry(t, h, string(payload))
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	if len(body.Results) != len(ids) {
		t.Fatalf("expected one result per id, got %d: %v", len(body.Results), body.Results)
	}

	key := func(id int64) string { return strconv.FormatInt(id, 10) }
	if r := body.Results[key(failed.ID)]; !r.OK || r.Action != "resend" {
		t.Errorf("failed row: expected ok with action=resend, got %+v", r)
	}
	if r := body.Results[key(importFailed.ID)]; !r.OK || r.Action != "import" {
		t.Errorf("importFailed row: expected ok with action=import, got %+v", r)
	}
	if r := body.Results[key(blocked.ID)]; !r.OK || r.Action != "import" {
		t.Errorf("importBlocked row: expected ok with action=import, got %+v", r)
	}
	if r := body.Results[key(live.ID)]; r.OK || r.Error == "" {
		t.Errorf("downloading row: expected a per-id refusal, got %+v", r)
	}

	// Exactly one release re-sent: the failed row. An import retry must never
	// touch the download client.
	if adds.calls != 1 {
		t.Fatalf("expected one send to the download client, got %d", adds.calls)
	}

	gotFailed, _ := downloads.GetByID(ctx, failed.ID)
	if gotFailed == nil || gotFailed.Status != models.StateDownloading {
		t.Errorf("failed row should be downloading again, got %+v", gotFailed)
	}
	gotBlocked, _ := downloads.GetByID(ctx, blocked.ID)
	if gotBlocked == nil || gotBlocked.Status != models.StateImportFailed || gotBlocked.ErrorMessage != "" {
		t.Errorf("blocked row should be re-armed for import, got %+v", gotBlocked)
	}
	gotLive, _ := downloads.GetByID(ctx, live.ID)
	if gotLive == nil || gotLive.Status != models.StateDownloading {
		t.Errorf("live row must be untouched, got %+v", gotLive)
	}
}

func TestQueueBulkRetry_RequiresIDs(t *testing.T) {
	var adds sabAddRecorder
	h, _, _, _ := retryFixture(t, &adds)
	for _, body := range []string{`{}`, `{"ids":[]}`} {
		rec, _ := postBulkRetry(t, h, body)
		if rec.Code != http.StatusBadRequest {
			t.Errorf("body %q: expected 400, got %d", body, rec.Code)
		}
	}
	rec, _ := postBulkRetry(t, h, "not-json")
	if rec.Code != http.StatusBadRequest {
		t.Errorf("expected 400 for a bad body, got %d", rec.Code)
	}
}

// TestQueueBulkRetry_SkipsAnotherUsersRow: a bulk retry must not become a way
// to restart someone else's download, and must report it with the same opaque
// message the single path uses.
func TestQueueBulkRetry_SkipsAnotherUsersRow(t *testing.T) {
	auth.SetEnforceTenancyForTests(t, true)
	var adds sabAddRecorder
	h, database, downloads, releaseURL := retryFixture(t, &adds)
	ctx := t.Context()
	owner, caller := seedTwoUsers(t, database, ctx)

	theirs := &models.Download{GUID: "theirs", Title: "Theirs", NZBURL: releaseURL, Status: models.StateFailed, Protocol: "usenet", OwnerUserID: owner}
	mine := &models.Download{GUID: "mine", Title: "Mine", NZBURL: releaseURL, Status: models.StateFailed, Protocol: "usenet", OwnerUserID: caller}
	for _, dl := range []*models.Download{theirs, mine} {
		if err := downloads.Create(ctx, dl); err != nil {
			t.Fatalf("create %s: %v", dl.GUID, err)
		}
	}

	payload, _ := json.Marshal(map[string]any{"ids": []int64{theirs.ID, mine.ID}})
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/queue/bulk-retry", bytes.NewReader(payload))
	req = req.WithContext(auth.WithUserID(req.Context(), caller))
	h.BulkRetry(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	var body bulkRetryBody
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if r := body.Results[strconv.FormatInt(theirs.ID, 10)]; r.OK || r.Error != "download not found" {
		t.Errorf("another user's row: expected \"download not found\", got %+v", r)
	}
	if r := body.Results[strconv.FormatInt(mine.ID, 10)]; !r.OK {
		t.Errorf("own row: expected ok, got %+v", r)
	}
	if adds.calls != 1 {
		t.Fatalf("expected exactly one send (the caller's own row), got %d", adds.calls)
	}
}

// TestQueueRetryImport_ConflictNamesEveryAcceptedState: the 409 said "download
// is not in importFailed state", which describes neither what was refused nor
// what is accepted — ResetImportRetry takes importFailed AND importBlocked.
func TestQueueRetryImport_ConflictNamesEveryAcceptedState(t *testing.T) {
	h, _, downloads, _, _, ctx := queueFixture(t)
	dl := &models.Download{
		GUID:     "conflict-guid",
		Title:    "Still Downloading",
		NZBURL:   "http://example/x.nzb",
		Status:   models.StateDownloading,
		Protocol: "usenet",
	}
	if err := downloads.Create(ctx, dl); err != nil {
		t.Fatalf("create download: %v", err)
	}

	idStr := strconv.FormatInt(dl.ID, 10)
	req := withURLParam(httptest.NewRequest(http.MethodPost, "/api/v1/queue/"+idStr+"/retry-import", nil), "id", idStr)
	rec := httptest.NewRecorder()
	h.RetryImport(rec, req)
	if rec.Code != http.StatusConflict {
		t.Fatalf("expected 409, got %d", rec.Code)
	}
	var body map[string]string
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	msg := body["error"]
	for _, want := range []string{"importFailed", "importBlocked", "downloading"} {
		if !bytes.Contains([]byte(msg), []byte(want)) {
			t.Errorf("409 message %q should name %q", msg, want)
		}
	}
}
