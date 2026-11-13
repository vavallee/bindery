package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/vavallee/bindery/internal/auth"
	"github.com/vavallee/bindery/internal/models"
)

// These cover #3835: a request waited for approval, and meanwhile someone else
// added the same book. The approver used to get a 409 on approve and nothing
// better to do than decline.

// pendingRequestThenInLibrary files a pending book request, then puts the same
// book in the library as someone else's add would have, in the state given.
func pendingRequestThenInLibrary(t *testing.T, f *requestsFixture, mediaType string, book models.Book) (requestResponse, *models.Book) {
	t.Helper()
	ctx := context.Background()
	created := decodeRequest(t, f.create(t, f.requester, `{"kind":"book","foreignId":"OL27482W","mediaType":"`+mediaType+`"}`))
	author := seedWells(t, f.authors)
	book.ForeignID, book.AuthorID = "OL27482W", author.ID
	book.Title, book.SortTitle = "The War of the Worlds", "war of the worlds"
	// File paths live in book_files and are derived on read, so a seeded path
	// goes in through AddBookFile after the row exists.
	ebookFile, audioFile := book.EbookFilePath, book.AudiobookFilePath
	book.EbookFilePath, book.AudiobookFilePath = "", ""
	if err := f.books.Create(ctx, &book); err != nil {
		t.Fatal(err)
	}
	if ebookFile != "" {
		if err := f.books.AddBookFile(ctx, book.ID, models.MediaTypeEbook, ebookFile); err != nil {
			t.Fatal(err)
		}
	}
	if audioFile != "" {
		if err := f.books.AddBookFile(ctx, book.ID, models.MediaTypeAudiobook, audioFile); err != nil {
			t.Fatal(err)
		}
	}
	stored, err := f.books.GetByForeignID(ctx, "OL27482W")
	if err != nil || stored == nil {
		t.Fatalf("seeded book missing: %v", err)
	}
	return created, stored
}

func countBooks(t *testing.T, f *requestsFixture) int {
	t.Helper()
	var n int
	if err := f.database.QueryRow("SELECT COUNT(*) FROM books").Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func TestRequestsDecline_WorksWhenTheBookIsAlreadyInTheLibrary(t *testing.T) {
	f := newRequestsFixture(t, wellsRequestStub(), nil)
	created, _ := pendingRequestThenInLibrary(t, f, "audiobook", models.Book{MediaType: models.MediaTypeEbook, Monitored: true, Status: models.BookStatusWanted})

	rec := httptest.NewRecorder()
	f.h.Decline(rec, withID(asUser(httptest.NewRequest(http.MethodPost, "/", strings.NewReader(`{"reason":"Someone beat you to it"}`)), f.admin), created.ID))
	if rec.Code != http.StatusOK {
		t.Fatalf("decline: %d %s", rec.Code, rec.Body.String())
	}
	if got := decodeRequest(t, rec); got.Status != "declined" || got.DeclineReason != "Someone beat you to it" {
		t.Fatalf("declined = %+v", got)
	}
}

// The requester asked for the audiobook, the library has the ebook only: the
// request stands, and approving adds the audiobook to the existing book.
func TestRequestsApprove_InLibraryAddsTheMissingFormat(t *testing.T) {
	f := newRequestsFixture(t, wellsRequestStub(), nil)
	created, book := pendingRequestThenInLibrary(t, f, "audiobook", models.Book{
		MediaType: models.MediaTypeEbook, Monitored: true, Status: models.BookStatusImported, EbookFilePath: "/books/war.epub",
	})
	before := countBooks(t, f)

	rec := f.approve(f.admin, created.ID, `{"searchOnAdd":false}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("approve: %d %s", rec.Code, rec.Body.String())
	}
	got := decodeRequest(t, rec)
	if got.Status != "approved" || got.ResultBookID == nil || *got.ResultBookID != book.ID {
		t.Fatalf("approved = %+v, want approved with result book %d", got, book.ID)
	}
	if !strings.Contains(got.Notice, "already in the library") || !strings.Contains(got.Notice, "audiobook") {
		t.Errorf("notice = %q, want it to say the book was in the library and the audiobook was added", got.Notice)
	}
	after, _ := f.books.GetByID(context.Background(), book.ID)
	if after.MediaType != models.MediaTypeBoth || !after.Monitored {
		t.Errorf("book media %q monitored %v, want both and monitored", after.MediaType, after.Monitored)
	}
	// The ebook on disk is untouched, and the missing audiobook makes it wanted.
	if after.EbookFilePath != "/books/war.epub" || after.Status != models.BookStatusWanted {
		t.Errorf("book ebook path %q status %q, want the ebook kept and status wanted", after.EbookFilePath, after.Status)
	}
	if n := countBooks(t, f); n != before {
		t.Errorf("books = %d, want %d: the approval must not add a second row", n, before)
	}
}

// Every requested format is already monitored or owned: approving adds nothing,
// resolves the request and says so.
func TestRequestsApprove_InLibraryResolvesWhenEveryFormatIsCovered(t *testing.T) {
	adder := &fakeAdder{}
	f := newRequestsFixture(t, wellsRequestStub(), adder)
	created, book := pendingRequestThenInLibrary(t, f, "both", models.Book{
		MediaType: models.MediaTypeBoth, Monitored: true, Status: models.BookStatusWanted,
	})

	rec := f.approve(f.admin, created.ID, `{}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("approve: %d %s", rec.Code, rec.Body.String())
	}
	got := decodeRequest(t, rec)
	if got.Status != "approved" || got.ResultBookID == nil || *got.ResultBookID != book.ID {
		t.Fatalf("approved = %+v", got)
	}
	if !strings.Contains(got.Notice, "already in the library") || !strings.Contains(got.Notice, "nothing was added") {
		t.Errorf("notice = %q", got.Notice)
	}
	if adder.calls.Load() != 0 || adder.existingCalls.Load() != 0 {
		t.Errorf("add cores ran (%d, %d times), want none", adder.calls.Load(), adder.existingCalls.Load())
	}
	after, _ := f.books.GetByID(context.Background(), book.ID)
	if after.MediaType != book.MediaType || after.Status != book.Status {
		t.Errorf("book changed from %q/%q to %q/%q", book.MediaType, book.Status, after.MediaType, after.Status)
	}
	// A second approval is still refused: the request was decided.
	if rec := f.approve(f.admin, created.ID, `{}`); rec.Code != http.StatusConflict {
		t.Fatalf("second approve: %d, want 409", rec.Code)
	}
}

// A format owned as a file counts as covered even when the book is not
// monitored, so an owned ebook resolves an ebook request and the fulfilled
// flag follows the book's status.
func TestRequestsApprove_InLibraryOwnedFormatResolvesAsFulfilled(t *testing.T) {
	f := newRequestsFixture(t, wellsRequestStub(), nil)
	created, book := pendingRequestThenInLibrary(t, f, "ebook", models.Book{
		MediaType: models.MediaTypeEbook, Monitored: false, Status: models.BookStatusImported, EbookFilePath: "/books/war.epub",
	})
	rec := f.approve(f.admin, created.ID, `{}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("approve: %d %s", rec.Code, rec.Body.String())
	}
	got := decodeRequest(t, rec)
	if got.Status != "approved" || !got.Fulfilled || !strings.Contains(got.Notice, "nothing was added") {
		t.Fatalf("approved = %+v, want approved, fulfilled, nothing added", got)
	}
	if after, _ := f.books.GetByID(context.Background(), book.ID); after.Monitored {
		t.Error("an owned format must not be switched to monitored")
	}
}

// A book the user skipped has nobody acquiring it, so a request for it is not
// covered: approving makes it wanted again.
func TestRequestsApprove_InLibrarySkippedBookIsWantedAgain(t *testing.T) {
	f := newRequestsFixture(t, wellsRequestStub(), nil)
	created, book := pendingRequestThenInLibrary(t, f, "ebook", models.Book{
		MediaType: models.MediaTypeEbook, Monitored: false, Status: models.BookStatusSkipped,
	})
	rec := f.approve(f.admin, created.ID, `{"searchOnAdd":false}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("approve: %d %s", rec.Code, rec.Body.String())
	}
	after, _ := f.books.GetByID(context.Background(), book.ID)
	if !after.Monitored || after.Status != models.BookStatusWanted {
		t.Fatalf("book monitored %v status %q, want monitored and wanted", after.Monitored, after.Status)
	}
}

// A request that names no format means the install default, the same value
// the add path gives a book with no format.
func TestRequestsApprove_InLibraryAnyFormatMeansTheDefault(t *testing.T) {
	f := newRequestsFixture(t, wellsRequestStub(), nil)
	if err := f.settings.Set(context.Background(), SettingDefaultMediaType, models.MediaTypeAudiobook); err != nil {
		t.Fatal(err)
	}
	created, book := pendingRequestThenInLibrary(t, f, "", models.Book{
		MediaType: models.MediaTypeEbook, Monitored: true, Status: models.BookStatusWanted,
	})
	if rec := f.approve(f.admin, created.ID, `{"searchOnAdd":false}`); rec.Code != http.StatusOK {
		t.Fatalf("approve: %d %s", rec.Code, rec.Body.String())
	}
	if after, _ := f.books.GetByID(context.Background(), book.ID); after.MediaType != models.MediaTypeBoth {
		t.Fatalf("media type = %q, want both (ebook kept, default audiobook added)", after.MediaType)
	}
}

// Under tenancy another account's row is not rewritten by an approval: the
// refusal stays, and the request can still be declined.
func TestRequestsApprove_InLibraryHeldByAnotherAccountStaysRefused(t *testing.T) {
	auth.SetEnforceTenancyForTests(t, true)
	f := newRequestsFixture(t, wellsRequestStub(), nil)
	created, book := pendingRequestThenInLibrary(t, f, "audiobook", models.Book{
		OwnerUserID: f.other.ID, MediaType: models.MediaTypeEbook, Monitored: true, Status: models.BookStatusWanted,
	})
	rec := f.approve(f.admin, created.ID, `{}`)
	if rec.Code != http.StatusConflict || requestErrorBody(rec) != "This book is already in the library." {
		t.Fatalf("approve: %d %q, want the old 409", rec.Code, requestErrorBody(rec))
	}
	if after, _ := f.books.GetByID(context.Background(), book.ID); after.MediaType != models.MediaTypeEbook {
		t.Fatalf("another account's book was changed to %q", after.MediaType)
	}
	if row, _ := f.requests.GetByID(context.Background(), created.ID); row.Status != models.RequestStatusPending {
		t.Fatalf("status = %q, want pending", row.Status)
	}
	rec = httptest.NewRecorder()
	f.h.Decline(rec, withID(asUser(httptest.NewRequest(http.MethodPost, "/", strings.NewReader(`{}`)), f.admin), created.ID))
	if rec.Code != http.StatusOK {
		t.Fatalf("decline: %d", rec.Code)
	}
}

// A requester can neither approve nor decline, and sees only their own
// requests, with none of the approver's hints.
func TestRequestsInLibrary_ApproverGateAndRequesterIsolation(t *testing.T) {
	f := newRequestsFixture(t, wellsRequestStub(), nil)
	created, _ := pendingRequestThenInLibrary(t, f, "ebook", models.Book{
		MediaType: models.MediaTypeEbook, Monitored: true, Status: models.BookStatusWanted,
	})

	// The routes sit behind auth.RequireAdmin (cmd/bindery/request_routes.go).
	r := chi.NewRouter()
	r.With(auth.RequireAdmin).Post("/requests/{id}/approve", f.h.Approve)
	r.With(auth.RequireAdmin).Post("/requests/{id}/decline", f.h.Decline)
	r.With(auth.RequireAdmin).Get("/requests/queue", f.h.Queue)
	for _, route := range []struct{ method, path string }{
		{http.MethodPost, "/requests/" + strconv.FormatInt(created.ID, 10) + "/approve"},
		{http.MethodPost, "/requests/" + strconv.FormatInt(created.ID, 10) + "/decline"},
		{http.MethodGet, "/requests/queue"},
	} {
		rec := httptest.NewRecorder()
		r.ServeHTTP(rec, asUser(httptest.NewRequest(route.method, route.path, strings.NewReader(`{}`)), f.requester))
		if rec.Code != http.StatusForbidden {
			t.Errorf("%s %s as a requester: %d, want 403", route.method, route.path, rec.Code)
		}
	}
	if row, _ := f.requests.GetByID(context.Background(), created.ID); row.Status != models.RequestStatusPending {
		t.Fatalf("a refused caller changed the request to %q", row.Status)
	}

	// Another requester does not see it, and the owner's own list carries no
	// approver fields.
	rec := httptest.NewRecorder()
	f.h.ListMine(rec, asUser(httptest.NewRequest(http.MethodGet, "/", nil), f.other))
	var theirs requestListResponse
	_ = json.Unmarshal(rec.Body.Bytes(), &theirs)
	if len(theirs.Items) != 0 {
		t.Fatalf("another requester sees %d requests, want 0", len(theirs.Items))
	}
	rec = httptest.NewRecorder()
	f.h.ListMine(rec, asUser(httptest.NewRequest(http.MethodGet, "/", nil), f.requester))
	if strings.Contains(rec.Body.String(), "alreadyInLibrary") || strings.Contains(rec.Body.String(), "notice") {
		t.Fatalf("requester's own list carries approver fields: %s", rec.Body.String())
	}
}

// The queue marks a pending book request whose formats the library covers,
// and only that one.
func TestRequestsQueue_FlagsPendingRequestsTheLibraryAlreadyCovers(t *testing.T) {
	f := newRequestsFixture(t, wellsRequestStub(), nil)
	ctx := context.Background()
	// Two requests wait for the same book, for different formats; then the book
	// reaches the library with the ebook monitored.
	covered := decodeRequest(t, f.create(t, f.requester, `{"kind":"book","foreignId":"OL27482W","mediaType":"ebook"}`))
	standing := decodeRequest(t, f.create(t, f.other, `{"kind":"book","foreignId":"OL27482W","mediaType":"audiobook"}`))
	author := seedWells(t, f.authors)
	if err := f.books.Create(ctx, &models.Book{
		ForeignID: "OL27482W", AuthorID: author.ID, Title: "The War of the Worlds", SortTitle: "war",
		MediaType: models.MediaTypeEbook, Monitored: true, Status: models.BookStatusWanted,
	}); err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	f.h.Queue(rec, asUser(httptest.NewRequest(http.MethodGet, "/requests/queue", nil), f.admin))
	var q requestListResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &q); err != nil {
		t.Fatal(err)
	}
	flags := map[int64]bool{}
	for _, it := range q.Items {
		flags[it.ID] = it.AlreadyInLibrary
	}
	if len(flags) != 2 || !flags[covered.ID] || flags[standing.ID] {
		t.Fatalf("flags = %v, want only request %d flagged", flags, covered.ID)
	}
}

func TestMissingRequestFormats(t *testing.T) {
	for _, tc := range []struct {
		name               string
		book               models.Book
		asked              string
		wantEbook, wantAud bool
	}{
		{"ebook monitored, ebook asked", models.Book{MediaType: "ebook", Monitored: true}, "ebook", false, false},
		{"ebook monitored, audiobook asked", models.Book{MediaType: "ebook", Monitored: true}, "audiobook", false, true},
		{"ebook monitored, both asked", models.Book{MediaType: "ebook", Monitored: true}, "both", false, true},
		{"both monitored, both asked", models.Book{MediaType: "both", Monitored: true}, "both", false, false},
		{"unmonitored, nothing owned", models.Book{MediaType: "ebook"}, "ebook", true, false},
		{"unmonitored, ebook owned", models.Book{MediaType: "ebook", EbookFilePath: "/b.epub"}, "ebook", false, false},
		{"audiobook owned but unmonitored, both asked", models.Book{MediaType: "audiobook", AudiobookFilePath: "/a"}, "both", true, false},
		{"legacy file path counts for its format", models.Book{MediaType: "ebook", FilePath: "/b.epub"}, "ebook", false, false},
		{"legacy file path is not the other format", models.Book{MediaType: "ebook", FilePath: "/b.epub"}, "audiobook", false, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e, a := missingRequestFormats(&tc.book, tc.asked)
			if e != tc.wantEbook || a != tc.wantAud {
				t.Fatalf("missing = (ebook %v, audiobook %v), want (%v, %v)", e, a, tc.wantEbook, tc.wantAud)
			}
		})
	}
}
