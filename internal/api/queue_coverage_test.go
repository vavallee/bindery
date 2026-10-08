package api

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/vavallee/bindery/internal/auth"
	"github.com/vavallee/bindery/internal/db"
	"github.com/vavallee/bindery/internal/downloader"
	"github.com/vavallee/bindery/internal/httpsec"
	"github.com/vavallee/bindery/internal/models"
)

// covQIDRequest builds a request carrying a chi {id} param.
func covQIDRequest(method, target, id string, body string) *http.Request {
	var r *http.Request
	if body == "" {
		r = httptest.NewRequest(method, target, nil)
	} else {
		r = httptest.NewRequest(method, target, strings.NewReader(body))
	}
	return withURLParam(r, "id", id)
}

// covQAsUser scopes a request to a non-admin user.
func covQAsUser(r *http.Request, uid int64) *http.Request {
	ctx := auth.WithUserRole(auth.WithUserID(r.Context(), uid), "user")
	return r.WithContext(ctx)
}

func covQErrorBody(t *testing.T, rec *httptest.ResponseRecorder) string {
	t.Helper()
	var m map[string]string
	if err := json.Unmarshal(rec.Body.Bytes(), &m); err != nil {
		t.Fatalf("decode error body %q: %v", rec.Body.String(), err)
	}
	return m["error"]
}

func TestQueueSortArrQueueRecordsByEveryKey(t *testing.T) {
	base := []arrQueueRecord{
		{ID: 2, Title: "beta", Status: "queued", Size: 20, SizeLeft: 5, DownloadClient: "Zed", Protocol: "usenet"},
		{ID: 1, Title: "Alpha", Status: "completed", Size: 30, SizeLeft: 1, DownloadClient: "alpha", Protocol: "torrent"},
		{ID: 3, Title: "gamma", Status: "paused", Size: 10, SizeLeft: 9, DownloadClient: "mid", Protocol: "usenet"},
	}
	ids := func(rs []arrQueueRecord) []int64 {
		out := make([]int64, len(rs))
		for i, r := range rs {
			out[i] = r.ID
		}
		return out
	}
	cases := []struct {
		key, dir string
		want     []int64
	}{
		{"id", "ascending", []int64{1, 2, 3}},
		{"ID", "desc", []int64{3, 2, 1}},
		{"title", "", []int64{1, 2, 3}},
		{"status", "", []int64{1, 3, 2}},
		{"size", "descending", []int64{1, 2, 3}},
		{"sizeleft", "", []int64{1, 2, 3}},
		{"downloadClient", "", []int64{1, 3, 2}},
		{"protocol", "", []int64{1, 2, 3}},
		// An unknown key compares nothing, so the stable sort keeps input order.
		{"bogus", "", []int64{2, 1, 3}},
		// No key leaves the slice untouched.
		{"", "descending", []int64{2, 1, 3}},
	}
	for _, c := range cases {
		rs := append([]arrQueueRecord(nil), base...)
		sortArrQueueRecords(rs, c.key, c.dir)
		got := ids(rs)
		for i := range got {
			if got[i] != c.want[i] {
				t.Errorf("sort %q %q = %v, want %v", c.key, c.dir, got, c.want)
				break
			}
		}
	}
}

func TestQueuePaginateArrQueueRecordsEdges(t *testing.T) {
	recs := []arrQueueRecord{{ID: 1}, {ID: 2}, {ID: 3}}
	if got := paginateArrQueueRecords(recs, 1, 0); len(got) != 3 {
		t.Errorf("pageSize 0 should return everything, got %d", len(got))
	}
	if got := paginateArrQueueRecords(recs, 0, 2); len(got) != 2 || got[0].ID != 1 {
		t.Errorf("page 0 should be treated as page 1, got %+v", got)
	}
	if got := paginateArrQueueRecords(nil, 1, 2); got == nil || len(got) != 0 {
		t.Errorf("empty input should be a non-nil empty page, got %#v", got)
	}
	if got := paginateArrQueueRecords(recs, 2, 2); len(got) != 1 || got[0].ID != 3 {
		t.Errorf("last partial page = %+v, want [3]", got)
	}
	if got := paginateArrQueueRecords(recs, 5, 2); got == nil || len(got) != 0 {
		t.Errorf("page past the end should be an empty page, got %#v", got)
	}
}

func TestQueueSizeLeftFromPercentage(t *testing.T) {
	cases := []struct {
		size   int64
		pct    string
		want   int64
		wantOK bool
	}{
		{0, "50", 0, false},
		{100, "", 0, false},
		{100, " % ", 0, false},
		{100, "abc", 0, false},
		{100, "0", 100, true},
		{100, "-5", 100, true},
		{100, "100%", 0, true},
		{100, "150", 0, true},
		{200, "25%", 150, true},
		{1000, " 33.3 ", 667, true},
	}
	for _, c := range cases {
		got, ok := sizeLeftFromPercentage(c.size, c.pct)
		if got != c.want || ok != c.wantOK {
			t.Errorf("sizeLeftFromPercentage(%d, %q) = (%d, %v), want (%d, %v)", c.size, c.pct, got, ok, c.want, c.wantOK)
		}
	}
}

func TestQueueItemSizeLeftPrefersLiveData(t *testing.T) {
	dl := models.Download{Size: 400, Status: models.StateDownloading}
	cases := []struct {
		name string
		item enrichedQueueItem
		want int64
	}{
		{"live size left", enrichedQueueItem{Download: dl, HasLive: true, Live: downloader.LiveStatus{SizeLeft: 7}}, 7},
		{"live size with nothing left", enrichedQueueItem{Download: dl, HasLive: true, Live: downloader.LiveStatus{Size: 400}}, 0},
		{"live percentage only", enrichedQueueItem{Download: dl, HasLive: true, Live: downloader.LiveStatus{Percentage: "75%"}}, 100},
		{"live with nothing usable falls back to stored size", enrichedQueueItem{Download: dl, HasLive: true}, 400},
		{"no live, downloading", enrichedQueueItem{Download: dl}, 400},
		{"no live, completed", enrichedQueueItem{Download: models.Download{Size: 400, Status: models.StateCompleted}}, 0},
		{"no live, import failed", enrichedQueueItem{Download: models.Download{Size: 400, Status: models.StateImportFailed}}, 0},
	}
	for _, c := range cases {
		if got := queueItemSizeLeft(c.item); got != c.want {
			t.Errorf("%s: sizeLeft = %d, want %d", c.name, got, c.want)
		}
	}
}

// covQClosedQueue returns a queue handler whose database is already closed,
// so every repository call fails.
func covQClosedQueue(t *testing.T) *QueueHandler {
	t.Helper()
	h, database, _, _, _, _ := queueFixture(t)
	if err := database.Close(); err != nil {
		t.Fatal(err)
	}
	return h
}

func TestQueueHandlersRejectBadIDs(t *testing.T) {
	h, _, _, _, _, _ := queueFixture(t)
	for name, fn := range map[string]http.HandlerFunc{
		"RetryImport":   h.RetryImport,
		"RetryDownload": h.RetryDownload,
		"Delete":        h.Delete,
	} {
		rec := httptest.NewRecorder()
		fn(rec, covQIDRequest(http.MethodPost, "/api/v1/queue/abc", "abc", ""))
		if rec.Code != http.StatusBadRequest {
			t.Errorf("%s with a non-numeric id: status %d, want 400", name, rec.Code)
		}
	}
}

func TestQueueHandlersSurfaceRepositoryErrors(t *testing.T) {
	h := covQClosedQueue(t)
	cases := map[string]struct {
		fn   http.HandlerFunc
		req  *http.Request
		want int
	}{
		"List":          {h.List, httptest.NewRequest(http.MethodGet, "/api/v1/queue", nil), http.StatusInternalServerError},
		"ArrList":       {h.ListArrCompatible, httptest.NewRequest(http.MethodGet, "/api/v1/queue/arr", nil), http.StatusInternalServerError},
		"RetryImport":   {h.RetryImport, covQIDRequest(http.MethodPost, "/", "1", ""), http.StatusInternalServerError},
		"RetryDownload": {h.RetryDownload, covQIDRequest(http.MethodPost, "/", "1", ""), http.StatusInternalServerError},
		"Delete":        {h.Delete, covQIDRequest(http.MethodDelete, "/", "1", ""), http.StatusInternalServerError},
		"BulkRetry":     {h.BulkRetry, httptest.NewRequest(http.MethodPost, "/", strings.NewReader(`{"ids":[1]}`)), http.StatusInternalServerError},
		"BulkDelete":    {h.BulkDelete, httptest.NewRequest(http.MethodPost, "/", strings.NewReader(`{"ids":[1]}`)), http.StatusInternalServerError},
	}
	for name, c := range cases {
		rec := httptest.NewRecorder()
		c.fn(rec, c.req)
		if rec.Code != c.want {
			t.Errorf("%s on a closed database: status %d, want %d (%s)", name, rec.Code, c.want, rec.Body.String())
		}
	}
}

func TestQueueBulkEndpointsValidateBody(t *testing.T) {
	h, _, _, _, _, _ := queueFixture(t)
	for _, body := range []string{`not json`, `{"ids":[]}`, `{}`} {
		rec := httptest.NewRecorder()
		h.BulkRetry(rec, httptest.NewRequest(http.MethodPost, "/", strings.NewReader(body)))
		if rec.Code != http.StatusBadRequest {
			t.Errorf("BulkRetry %s: status %d, want 400", body, rec.Code)
		}
		rec = httptest.NewRecorder()
		h.BulkDelete(rec, httptest.NewRequest(http.MethodPost, "/", strings.NewReader(body)))
		if rec.Code != http.StatusBadRequest {
			t.Errorf("BulkDelete %s: status %d, want 400", body, rec.Code)
		}
	}
}

// TestQueueBulkDeleteRefusesDeleteFilesWithoutClient: deleteFiles is passed
// only to the download client, so asking for it while keeping the client out
// of the removal is refused before any row is touched.
func TestQueueBulkDeleteRefusesDeleteFilesWithoutClient(t *testing.T) {
	h, _, downloads, _, _, ctx := queueFixture(t)
	dl := &models.Download{GUID: "covq-bd", Title: "T", Status: models.StateDownloading, Protocol: "usenet"}
	if err := downloads.Create(ctx, dl); err != nil {
		t.Fatal(err)
	}
	body := `{"ids":[` + strconv.FormatInt(dl.ID, 10) + `],"deleteFiles":true,"removeFromClient":false}`
	rec := httptest.NewRecorder()
	h.BulkDelete(rec, httptest.NewRequest(http.MethodPost, "/", strings.NewReader(body)))
	if rec.Code != http.StatusBadRequest || !strings.Contains(covQErrorBody(t, rec), "removeFromClient") {
		t.Fatalf("status %d body %s, want 400 naming removeFromClient", rec.Code, rec.Body.String())
	}
	if got, _ := downloads.GetByID(ctx, dl.ID); got == nil {
		t.Fatal("refused bulk delete still removed the row")
	}
}

// TestQueueBulkDeleteReportsPerItem: an unknown id and another user's row
// both answer the same opaque "download not found", the caller's own row is
// removed, and a removal with removeFromClient=false never needs a client.
func TestQueueBulkDeleteReportsPerItem(t *testing.T) {
	auth.SetEnforceTenancyForTests(t, true)
	h, database, downloads, _, _, ctx := queueFixture(t)
	owner, caller := seedTwoUsers(t, database, ctx)

	mine := &models.Download{GUID: "covq-mine", Title: "Mine", Status: models.StateDownloading, Protocol: "usenet"}
	theirs := &models.Download{GUID: "covq-theirs", Title: "Theirs", Status: models.StateDownloading, Protocol: "usenet"}
	for _, d := range []*models.Download{mine, theirs} {
		if err := downloads.Create(ctx, d); err != nil {
			t.Fatal(err)
		}
	}
	covQSetOwner(t, database, "downloads", mine.ID, caller)
	covQSetOwner(t, database, "downloads", theirs.ID, owner)

	body := `{"ids":[` + strconv.FormatInt(mine.ID, 10) + `,` + strconv.FormatInt(theirs.ID, 10) + `,999999],"removeFromClient":false}`
	rec := httptest.NewRecorder()
	h.BulkDelete(rec, covQAsUser(httptest.NewRequest(http.MethodPost, "/", strings.NewReader(body)), caller))
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
	var resp bulkResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if !resp.Results[strconv.FormatInt(mine.ID, 10)].OK {
		t.Errorf("own row not removed: %+v", resp.Results)
	}
	for _, id := range []string{strconv.FormatInt(theirs.ID, 10), "999999"} {
		if r := resp.Results[id]; r.OK || r.Error != "download not found" {
			t.Errorf("id %s result = %+v, want download not found", id, r)
		}
	}
	if got, _ := downloads.GetByID(ctx, theirs.ID); got == nil {
		t.Error("another user's row was removed")
	}
	if got, _ := downloads.GetByID(ctx, mine.ID); got != nil {
		t.Error("own row still present after bulk delete")
	}
}

func covQSetOwner(t *testing.T, database *sql.DB, table string, id, owner int64) {
	t.Helper()
	if _, err := database.Exec("UPDATE "+table+" SET owner_user_id=? WHERE id=?", owner, id); err != nil {
		t.Fatal(err)
	}
}

// TestQueueRetryDownloadWithoutClientIs400: a failed row whose protocol has no
// enabled client is a configuration problem the user can fix, so it answers
// 400 with the actionable message rather than a 502.
func TestQueueRetryDownloadWithoutClientIs400(t *testing.T) {
	h, _, downloads, _, _, ctx := queueFixture(t)
	dl := &models.Download{GUID: "covq-noclient", Title: "No Client", Status: models.StateFailed, Protocol: "usenet", NZBURL: "http://example.invalid/x.nzb"}
	if err := downloads.Create(ctx, dl); err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	h.RetryDownload(rec, retryDownloadRequest(t, dl.ID))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status %d (%s), want 400", rec.Code, rec.Body.String())
	}
	if msg := covQErrorBody(t, rec); !strings.Contains(msg, "no enabled") {
		t.Fatalf("error %q should say no enabled client is configured", msg)
	}
	got, _ := downloads.GetByID(ctx, dl.ID)
	if got == nil || got.Status != models.StateFailed {
		t.Fatalf("row after a refused retry = %+v, want still failed", got)
	}
}

// TestQueueRetryDownloadClientFailureIs502: when the client rejects the
// release, the retry answers 502 and the row records the client's error.
func TestQueueRetryDownloadClientFailureIs502(t *testing.T) {
	t.Cleanup(httpsec.AllowLoopbackForTests())
	sab := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"status": false, "error": "covq refused"})
	}))
	t.Cleanup(sab.Close)
	indexerSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`<?xml version="1.0" encoding="UTF-8"?><nzb></nzb>`))
	}))
	t.Cleanup(indexerSrv.Close)

	h, _, downloads, clients, _, ctx := queueFixture(t)
	host, port := testServerHostPort(t, sab.URL)
	if err := clients.Create(ctx, &models.DownloadClient{Name: "sab", Type: "sabnzbd", Host: host, Port: port, Enabled: true}); err != nil {
		t.Fatal(err)
	}
	dl := &models.Download{GUID: "covq-502", Title: "Refused", Status: models.StateFailed, Protocol: "usenet", NZBURL: indexerSrv.URL + "/x.nzb"}
	if err := downloads.Create(ctx, dl); err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	h.RetryDownload(rec, retryDownloadRequest(t, dl.ID))
	if rec.Code != http.StatusBadGateway {
		t.Fatalf("status %d (%s), want 502", rec.Code, rec.Body.String())
	}
	got, _ := downloads.GetByID(ctx, dl.ID)
	if got == nil || got.ErrorMessage == "" {
		t.Fatalf("row after a client failure = %+v, want an error message recorded", got)
	}
}

// TestQueueRetryDownloadForeignBookIs404: the caller owns the row but the
// book it names now belongs to another user; the retry must not send for a
// book the caller may not act on, and answers the same 404 as a missing book.
func TestQueueRetryDownloadForeignBookIs404(t *testing.T) {
	auth.SetEnforceTenancyForTests(t, true)
	var adds sabAddRecorder
	h, database, downloads, release := retryFixture(t, &adds)
	ctx := context.Background()
	owner, caller := seedTwoUsers(t, database, ctx)

	authors := db.NewAuthorRepo(database)
	a := &models.Author{ForeignID: "covq-a", Name: "A", SortName: "A"}
	if err := authors.CreateForUser(ctx, a, owner); err != nil {
		t.Fatal(err)
	}
	book := &models.Book{ForeignID: "covq-b", AuthorID: a.ID, Title: "Theirs", SortTitle: "Theirs"}
	if err := db.NewBookRepo(database).Create(ctx, book); err != nil {
		t.Fatal(err)
	}
	covQSetOwner(t, database, "books", book.ID, owner)

	dl := &models.Download{GUID: "covq-foreign-book", Title: "Theirs", Status: models.StateFailed, Protocol: "usenet", NZBURL: release, BookID: &book.ID}
	if err := downloads.Create(ctx, dl); err != nil {
		t.Fatal(err)
	}
	covQSetOwner(t, database, "downloads", dl.ID, caller)

	rec := httptest.NewRecorder()
	h.RetryDownload(rec, covQAsUser(retryDownloadRequest(t, dl.ID), caller))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status %d (%s), want 404", rec.Code, rec.Body.String())
	}
	if adds.calls != 0 {
		t.Fatalf("download client was called %d times for a foreign book", adds.calls)
	}
}

// TestQueueBulkRetryReportsResendFailure: a failed row whose resend cannot
// reach a client gets a per-ID error rather than failing the batch.
func TestQueueBulkRetryReportsResendFailure(t *testing.T) {
	h, _, downloads, _, _, ctx := queueFixture(t)
	dl := &models.Download{GUID: "covq-br", Title: "T", Status: models.StateFailed, Protocol: "usenet", NZBURL: "http://example.invalid/x.nzb"}
	if err := downloads.Create(ctx, dl); err != nil {
		t.Fatal(err)
	}
	rec, parsed := postBulkRetry(t, h, `{"ids":[`+strconv.FormatInt(dl.ID, 10)+`,424242]}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
	got := parsed.Results[strconv.FormatInt(dl.ID, 10)]
	if got.OK || !strings.Contains(got.Error, "no enabled") {
		t.Errorf("failed row result = %+v, want a no-client error", got)
	}
	if missing := parsed.Results["424242"]; missing.OK || missing.Error != "download not found" {
		t.Errorf("unknown id result = %+v", missing)
	}
}

// TestQueueRetryImportAcceptsImportBlocked: RetryImport re-arms an
// importBlocked row as well as importFailed, answering 202.
func TestQueueRetryImportAcceptsImportBlocked(t *testing.T) {
	h, _, downloads, _, _, ctx := queueFixture(t)
	dl := &models.Download{GUID: "covq-blocked", Title: "Blocked", Status: models.StateImportBlocked, Protocol: "usenet"}
	if err := downloads.Create(ctx, dl); err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	h.RetryImport(rec, covQIDRequest(http.MethodPost, "/", strconv.FormatInt(dl.ID, 10), ""))
	if rec.Code != http.StatusAccepted {
		t.Fatalf("status %d (%s), want 202", rec.Code, rec.Body.String())
	}
	got, _ := downloads.GetByID(ctx, dl.ID)
	if got == nil || got.Status == models.StateImportBlocked {
		t.Fatalf("row after retry-import = %+v, want it moved out of importBlocked", got)
	}
}

// TestQueueGrabRecordsFailureHistory: when the client refuses a fresh grab
// the handler answers an error, marks the new row failed and writes a
// downloadFailed history event.
func TestQueueGrabRecordsFailureHistory(t *testing.T) {
	t.Cleanup(httpsec.AllowLoopbackForTests())
	sab := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	t.Cleanup(sab.Close)
	h, database, downloads, clients, _, ctx := queueFixture(t)
	host, port := testServerHostPort(t, sab.URL)
	if err := clients.Create(ctx, &models.DownloadClient{Name: "sab", Type: "sabnzbd", Host: host, Port: port, Enabled: true}); err != nil {
		t.Fatal(err)
	}
	_, err := h.grab(ctx, grabRequest{GUID: "covq-grabfail", Title: "Some.Book.epub", NZBURL: sab.URL + "/x.nzb"})
	if err == nil || !strings.Contains(err.Error(), "failed to send to downloader") {
		t.Fatalf("grab err = %v, want a send failure", err)
	}
	dl, _ := downloads.GetByGUID(ctx, "covq-grabfail")
	if dl == nil || dl.ErrorMessage == "" {
		t.Fatalf("row after failed send = %+v, want an error recorded", dl)
	}
	events, _, err := db.NewHistoryRepo(database).ListPage(ctx, db.HistoryListOpts{EventType: models.HistoryEventDownloadFailed, Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 1 || events[0].SourceTitle != "Some.Book.epub" {
		t.Fatalf("downloadFailed history = %+v, want one event for the release", events)
	}
}

// TestQueueRecordHistoryWithoutRepo: a handler built without a history repo
// skips the write rather than panicking.
func TestQueueRecordHistoryWithoutRepo(t *testing.T) {
	h := newQueueTestHandler(t)
	h.recordHistory(context.Background(), models.HistoryEventGrabbed, "t", nil, map[string]any{"x": time.Now()})
}

// TestQueueRecordHistoryUnmarshalableData: data that cannot be marshalled is
// logged and dropped, not written as a half event.
func TestQueueRecordHistoryUnmarshalableData(t *testing.T) {
	h, database, _, _, _, ctx := queueFixture(t)
	h.recordHistory(ctx, models.HistoryEventGrabbed, "covq-bad", nil, map[string]any{"ch": make(chan int)})
	events, _, err := db.NewHistoryRepo(database).ListPage(ctx, db.HistoryListOpts{Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 0 {
		t.Fatalf("history written for unmarshalable data: %+v", events)
	}
}
