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

	"github.com/vavallee/bindery/internal/auth"
	"github.com/vavallee/bindery/internal/db"
	"github.com/vavallee/bindery/internal/models"
)

// covQPendingGrabFixture wires a PendingHandler over a queue with a working
// SABnzbd stand in, plus one book and one pending release for it whose stored
// blob is a real grab request.
type covQPendingGrab struct {
	h         *PendingHandler
	database  *sql.DB
	pending   *db.PendingReleaseRepo
	downloads *db.DownloadRepo
	book      *models.Book
	prID      int64
	adds      *sabAddRecorder
}

func covQNewPendingGrab(t *testing.T, releaseMedia string) *covQPendingGrab {
	t.Helper()
	adds := &sabAddRecorder{}
	queue, database, downloads, release := retryFixture(t, adds)
	ctx := context.Background()
	books := db.NewBookRepo(database)
	authors := db.NewAuthorRepo(database)
	a := &models.Author{ForeignID: "covq-pa", Name: "Pending Author", SortName: "Author, Pending"}
	if err := authors.Create(ctx, a); err != nil {
		t.Fatal(err)
	}
	b := &models.Book{ForeignID: "covq-pb", AuthorID: a.ID, Title: "Pending Book", SortTitle: "pending book", MediaType: models.MediaTypeAudiobook}
	if err := books.Create(ctx, b); err != nil {
		t.Fatal(err)
	}
	blob, _ := json.Marshal(grabRequest{GUID: "covq-pending-guid", Title: "Pending.Book.2020", NZBURL: release, Protocol: "usenet", Size: 1234})
	pending := db.NewPendingReleaseRepo(database)
	pr := &models.PendingRelease{BookID: b.ID, MediaType: releaseMedia, Title: "Pending.Book.2020", GUID: "covq-pending-guid", Protocol: "usenet", Reason: "delay", ReleaseJSON: string(blob)}
	if err := pending.Upsert(ctx, pr); err != nil {
		t.Fatal(err)
	}
	all, err := pending.List(ctx)
	if err != nil || len(all) != 1 {
		t.Fatalf("pending list = %+v err %v", all, err)
	}
	return &covQPendingGrab{
		h:         NewPendingHandler(pending, queue, downloads, books),
		database:  database,
		pending:   pending,
		downloads: downloads,
		book:      b,
		prID:      all[0].ID,
		adds:      adds,
	}
}

func (f *covQPendingGrab) grab(t *testing.T, r *http.Request) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	f.h.Grab(rec, r)
	return rec
}

func covQPendingReq(id int64) *http.Request {
	s := strconv.FormatInt(id, 10)
	return covQIDRequest(http.MethodPost, "/api/v1/pending/"+s+"/grab", s, "")
}

// TestPendingGrabSendsStoredReleaseAndClearsRow: a force grab sends the
// stored release to the client under the pending row's own format, answers
// 201 with the download, and removes the pending row.
func TestPendingGrabSendsStoredReleaseAndClearsRow(t *testing.T) {
	f := covQNewPendingGrab(t, models.MediaTypeEbook)
	rec := f.grab(t, covQPendingReq(f.prID))
	if rec.Code != http.StatusCreated {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
	var dl models.Download
	if err := json.Unmarshal(rec.Body.Bytes(), &dl); err != nil {
		t.Fatal(err)
	}
	if dl.BookID == nil || *dl.BookID != f.book.ID || dl.Status != models.StateDownloading {
		t.Fatalf("download = %+v, want bound to book %d and downloading", dl, f.book.ID)
	}
	if f.adds.calls != 1 {
		t.Fatalf("client add calls = %d, want 1", f.adds.calls)
	}
	if left, _ := f.pending.List(context.Background()); len(left) != 0 {
		t.Fatalf("pending rows after grab = %+v, want none", left)
	}
}

// TestPendingGrabFallsBackToBookMediaType: an older pending row with no
// usable format of its own takes the book's.
func TestPendingGrabFallsBackToBookMediaType(t *testing.T) {
	f := covQNewPendingGrab(t, models.MediaTypeEbook)
	if _, err := f.database.Exec("UPDATE pending_releases SET media_type = 'both' WHERE id = ?", f.prID); err != nil {
		t.Fatal(err)
	}
	rec := f.grab(t, covQPendingReq(f.prID))
	if rec.Code != http.StatusCreated {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
	if f.adds.calls != 1 {
		t.Fatalf("client add calls = %d, want 1", f.adds.calls)
	}
}

// TestPendingGrabAlreadyGrabbedIs409: a release whose GUID is already live in
// the queue is refused with 409 and the pending row is kept.
func TestPendingGrabAlreadyGrabbedIs409(t *testing.T) {
	f := covQNewPendingGrab(t, models.MediaTypeEbook)
	if err := f.downloads.Create(context.Background(), &models.Download{GUID: "covq-pending-guid", Title: "live", Status: models.StateDownloading, Protocol: "usenet"}); err != nil {
		t.Fatal(err)
	}
	rec := f.grab(t, covQPendingReq(f.prID))
	if rec.Code != http.StatusConflict {
		t.Fatalf("status %d: %s, want 409", rec.Code, rec.Body.String())
	}
	if f.adds.calls != 0 {
		t.Fatalf("client called %d times for an already grabbed release", f.adds.calls)
	}
	if left, _ := f.pending.List(context.Background()); len(left) != 1 {
		t.Fatalf("pending row was removed after a refused grab: %+v", left)
	}
}

func TestPendingGrabRejectsCorruptBlobAndMissingQueue(t *testing.T) {
	f := covQNewPendingGrab(t, models.MediaTypeEbook)

	// Missing queue handler: 503, nothing sent.
	noQueue := NewPendingHandler(f.pending, nil, f.downloads, nil)
	rec := httptest.NewRecorder()
	noQueue.Grab(rec, covQPendingReq(f.prID))
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("nil queue: status %d, want 503", rec.Code)
	}

	// Corrupt stored blob: 500.
	if _, err := f.database.Exec("UPDATE pending_releases SET release_json = '{not json' WHERE id = ?", f.prID); err != nil {
		t.Fatal(err)
	}
	rec = f.grab(t, covQPendingReq(f.prID))
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("corrupt blob: status %d, want 500", rec.Code)
	}
	if f.adds.calls != 0 {
		t.Fatalf("client called %d times", f.adds.calls)
	}
}

func TestPendingGrabNotFoundAndBadID(t *testing.T) {
	f := covQNewPendingGrab(t, models.MediaTypeEbook)
	rec := f.grab(t, covQPendingReq(987654))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("unknown id: status %d, want 404", rec.Code)
	}
	rec = f.grab(t, covQIDRequest(http.MethodPost, "/", "x", ""))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("bad id: status %d, want 400", rec.Code)
	}
}

// TestPendingGrabCrossUserBlockedWhenGateOn: with tenancy on, bob cannot
// force grab alice's pending release; the answer is the same 404 as a
// missing row and nothing reaches the queue.
func TestPendingGrabCrossUserBlockedWhenGateOn(t *testing.T) {
	auth.SetEnforceTenancyForTests(t, true)
	h, database, _, uBob, idA, _ := seedTwoUserPending(t)
	rec := httptest.NewRecorder()
	h.Grab(rec, covQAsUser(covQPendingReq(idA), uBob))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status %d, want 404", rec.Code)
	}
	var n int
	if err := database.QueryRow("SELECT COUNT(*) FROM downloads").Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Fatalf("%d downloads created by a refused cross user grab", n)
	}
}

func TestPendingDeleteBadIDNotFoundAndDBError(t *testing.T) {
	h, database, _, _, _, _ := seedTwoUserPending(t)
	rec := httptest.NewRecorder()
	h.Delete(rec, covQIDRequest(http.MethodDelete, "/", "nope", ""))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("bad id: %d", rec.Code)
	}
	rec = httptest.NewRecorder()
	h.Delete(rec, covQIDRequest(http.MethodDelete, "/", "999999", ""))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("unknown id: %d", rec.Code)
	}
	if err := database.Close(); err != nil {
		t.Fatal(err)
	}
	rec = httptest.NewRecorder()
	h.Delete(rec, covQIDRequest(http.MethodDelete, "/", "1", ""))
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("closed db delete: %d, want 500", rec.Code)
	}
	rec = httptest.NewRecorder()
	h.List(rec, httptest.NewRequest(http.MethodGet, "/", nil))
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("closed db list: %d, want 500", rec.Code)
	}
	rec = httptest.NewRecorder()
	h.Grab(rec, covQPendingReq(1))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("closed db grab: %d, want 404", rec.Code)
	}
}

// TestPendingListRedactsStoredCredentials: the stored blob keeps the
// indexer apikey for the force grab, but the list never shows it.
func TestPendingListRedactsStoredCredentials(t *testing.T) {
	h, database, _, _, idA, _ := seedTwoUserPending(t)
	blob := `{"nzbUrl":"http://indexer.example/api?t=get&id=7&apikey=COVQSECRET","guid":"http://indexer.example/details/7","infoUrl":"http://indexer.example/details/7","extra":5}`
	if _, err := database.Exec("UPDATE pending_releases SET release_json = ? WHERE id = ?", blob, idA); err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	h.List(rec, httptest.NewRequest(http.MethodGet, "/", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d", rec.Code)
	}
	if strings.Contains(rec.Body.String(), "COVQSECRET") {
		t.Fatalf("pending list leaked the indexer apikey: %s", rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), `\"extra\":5`) {
		t.Fatalf("unknown blob fields should survive redaction: %s", rec.Body.String())
	}
}

func TestPendingRedactReleaseJSONLeavesUnparseableAndCleanBlobs(t *testing.T) {
	for _, raw := range []string{
		"",
		"{not json",
		`{"nzbUrl":5,"guid":"plain-guid"}`,
		`{"title":"no urls here"}`,
	} {
		if got := redactReleaseJSON(raw); got != raw {
			t.Errorf("redactReleaseJSON(%q) = %q, want it unchanged", raw, got)
		}
	}
}
