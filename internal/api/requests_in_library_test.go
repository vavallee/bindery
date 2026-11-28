package api

import (
	"context"
	"encoding/json"
	"errors"
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

// Under tenancy an admin approving by hand may widen another account's book,
// as the book routes let an admin change it (#4089). The book keeps its owner.
func TestRequestsApprove_InLibraryMissingFormatOnAnotherAccountsBookAdminWidens(t *testing.T) {
	auth.SetEnforceTenancyForTests(t, true)
	f := newRequestsFixture(t, wellsRequestStub(), nil)
	created, book := pendingRequestThenInLibrary(t, f, "audiobook", models.Book{
		OwnerUserID: f.other.ID, MediaType: models.MediaTypeEbook, Monitored: true, Status: models.BookStatusWanted,
	})
	rec := f.approve(f.admin, created.ID, `{"searchOnAdd":false}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("approve: %d %s", rec.Code, rec.Body.String())
	}
	if got := decodeRequest(t, rec); got.Status != models.RequestStatusApproved || got.ResultBookID == nil || *got.ResultBookID != book.ID {
		t.Fatalf("approved = %+v", got)
	}
	after, _ := f.books.GetByID(context.Background(), book.ID)
	if after.MediaType != models.MediaTypeBoth || after.OwnerUserID != f.other.ID {
		t.Fatalf("book after approval: media %q owner %d, want both owned by %d", after.MediaType, after.OwnerUserID, f.other.ID)
	}
}

// The same book stays out of reach of an approval acting as a requester: the
// approval core refuses to widen it and leaves it exactly as it was.
func TestRequestsApprove_InLibraryMissingFormatOnAnotherAccountsBookRefusedForRequester(t *testing.T) {
	auth.SetEnforceTenancyForTests(t, true)
	adder := &fakeAdder{}
	f := newRequestsFixture(t, wellsRequestStub(), adder)
	_, book := pendingRequestThenInLibrary(t, f, "audiobook", models.Book{
		OwnerUserID: f.other.ID, MediaType: models.MediaTypeEbook, Monitored: true, Status: models.BookStatusWanted,
	})
	ctx := auth.WithUserRole(auth.WithUserID(context.Background(), f.requester.ID), auth.RoleRequester)
	if _, _, err := f.h.approveExistingBook(ctx, book, models.MediaTypeAudiobook, true); !errors.Is(err, errBookHeldElsewhere) {
		t.Fatalf("approveExistingBook as the requester: %v, want errBookHeldElsewhere", err)
	}
	after, _ := f.books.GetByID(context.Background(), book.ID)
	if after.MediaType != models.MediaTypeEbook || after.UpdatedAtRaw != book.UpdatedAtRaw {
		t.Fatalf("another account's book was changed: media %q, updated %q -> %q", after.MediaType, book.UpdatedAtRaw, after.UpdatedAtRaw)
	}
	if n := addCalls(adder); n != 0 {
		t.Errorf("the add cores ran %d times, want 0", n)
	}
}

// The issue's own scenario under tenancy: someone else (the admin, say) added
// the book. When it already covers what was asked, approving resolves the
// request without touching their row, exactly as with one account.
func TestRequestsApprove_InLibraryCoveredByAnotherAccountsBookResolves(t *testing.T) {
	auth.SetEnforceTenancyForTests(t, true)
	adder := &fakeAdder{}
	f := newRequestsFixture(t, wellsRequestStub(), adder)
	created, book := pendingRequestThenInLibrary(t, f, "ebook", models.Book{
		OwnerUserID: f.admin.ID, MediaType: models.MediaTypeEbook, Monitored: true, Status: models.BookStatusWanted,
	})
	rec := f.approve(f.admin, created.ID, `{}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("approve: %d %s", rec.Code, rec.Body.String())
	}
	got := decodeRequest(t, rec)
	if got.Status != "approved" || got.ResultBookID == nil || *got.ResultBookID != book.ID || !strings.Contains(got.Notice, "nothing was added") {
		t.Fatalf("approved = %+v", got)
	}
	after, _ := f.books.GetByID(context.Background(), book.ID)
	if after.UpdatedAtRaw != book.UpdatedAtRaw || after.OwnerUserID != f.admin.ID {
		t.Fatalf("the other account's book was written: updated %q -> %q, owner %d", book.UpdatedAtRaw, after.UpdatedAtRaw, after.OwnerUserID)
	}
	if adder.calls.Load() != 0 || adder.existingCalls.Load() != 0 {
		t.Errorf("add cores ran (%d, %d times), want none", adder.calls.Load(), adder.existingCalls.Load())
	}
}

// A book the owner skipped, with an audiobook only request: the skipped
// ebook setting is not a format anyone is acquiring, so the book ends up
// monitored for the audiobook alone, not for both.
func TestRequestsApprove_InLibrarySkippedBookGetsOnlyTheRequestedFormat(t *testing.T) {
	f := newRequestsFixture(t, wellsRequestStub(), nil)
	created, book := pendingRequestThenInLibrary(t, f, "audiobook", models.Book{
		MediaType: models.MediaTypeEbook, Monitored: false, Status: models.BookStatusSkipped,
	})
	if rec := f.approve(f.admin, created.ID, `{"searchOnAdd":false}`); rec.Code != http.StatusOK {
		t.Fatalf("approve: %d %s", rec.Code, rec.Body.String())
	}
	after, _ := f.books.GetByID(context.Background(), book.ID)
	if after.MediaType != models.MediaTypeAudiobook || !after.Monitored || after.Status != models.BookStatusWanted {
		t.Fatalf("book media %q monitored %v status %q, want audiobook, monitored, wanted", after.MediaType, after.Monitored, after.Status)
	}
}

func TestWidenedMediaType(t *testing.T) {
	for _, tc := range []struct {
		name  string
		book  models.Book
		asked string
		want  string
	}{
		{"monitored ebook plus audiobook", models.Book{MediaType: "ebook", Monitored: true, Status: "wanted"}, "audiobook", "both"},
		{"skipped ebook setting is dropped", models.Book{MediaType: "ebook", Status: "skipped"}, "audiobook", "audiobook"},
		{"unmonitored audiobook setting is dropped", models.Book{MediaType: "audiobook"}, "ebook", "ebook"},
		{"owned file is kept even when unmonitored", models.Book{MediaType: "ebook", EbookFilePath: "/b.epub"}, "audiobook", "both"},
		{"monitored but skipped counts as nothing", models.Book{MediaType: "both", Monitored: true, Status: "skipped"}, "ebook", "ebook"},
		{"both asked", models.Book{MediaType: "ebook"}, "both", "both"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := widenedMediaType(&tc.book, tc.asked); got != tc.want {
				t.Fatalf("widenedMediaType = %q, want %q", got, tc.want)
			}
		})
	}
}

// The approval saves only if the book is as it was read. A concurrent write
// between the read and the save (an import recording the audiobook's details,
// here) must survive: the approval reads again and widens the fresh row.
func TestRequestsApprove_InLibraryDoesNotOverwriteAConcurrentWrite(t *testing.T) {
	f := newRequestsFixture(t, wellsRequestStub(), nil)
	ctx := context.Background()
	_, book := pendingRequestThenInLibrary(t, f, "audiobook", models.Book{
		MediaType: models.MediaTypeEbook, Monitored: true, Status: models.BookStatusWanted,
	})
	stale := *book
	concurrent := *book
	concurrent.Narrator, concurrent.DurationSeconds = "Imported Narrator", 3600
	if err := f.books.Update(ctx, &concurrent); err != nil {
		t.Fatal(err)
	}

	runID, notice, err := f.h.approveExistingBook(ctx, &stale, models.MediaTypeAudiobook, false)
	if err != nil {
		t.Fatalf("approveExistingBook with a stale snapshot: %v", err)
	}
	if runID != "" || !strings.Contains(notice, "audiobook") {
		t.Errorf("run %q notice %q", runID, notice)
	}
	after, _ := f.books.GetByID(ctx, book.ID)
	if after.Narrator != "Imported Narrator" || after.DurationSeconds != 3600 {
		t.Fatalf("the concurrent write was overwritten: narrator %q duration %d", after.Narrator, after.DurationSeconds)
	}
	if after.MediaType != models.MediaTypeBoth {
		t.Errorf("media type %q, want both after the retry", after.MediaType)
	}
}

// A write that keeps winning is not retried forever: one retry, then the
// approver is told to try again and the request stays pending.
func TestRequestsApprove_InLibraryGivesUpAfterOneRetry(t *testing.T) {
	adder := &fakeAdder{err: errBookChangedMeanwhile}
	f := newRequestsFixture(t, wellsRequestStub(), adder)
	created, _ := pendingRequestThenInLibrary(t, f, "audiobook", models.Book{
		MediaType: models.MediaTypeEbook, Monitored: true, Status: models.BookStatusWanted,
	})
	rec := f.approve(f.admin, created.ID, `{}`)
	if rec.Code != http.StatusConflict || requestErrorBody(rec) != bookChangedMessage {
		t.Fatalf("approve: %d %q, want 409 with %q", rec.Code, requestErrorBody(rec), bookChangedMessage)
	}
	if n := adder.existingCalls.Load(); n != 2 {
		t.Errorf("the monitor core ran %d times, want 2 (first try and one retry)", n)
	}
	if row, _ := f.requests.GetByID(context.Background(), created.ID); row.Status != models.RequestStatusPending {
		t.Fatalf("status = %q, want pending", row.Status)
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

	// Another requester does not see it.
	rec := httptest.NewRecorder()
	f.h.ListMine(rec, asUser(httptest.NewRequest(http.MethodGet, "/", nil), f.other))
	var theirs requestListResponse
	_ = json.Unmarshal(rec.Body.Bytes(), &theirs)
	if len(theirs.Items) != 0 {
		t.Fatalf("another requester sees %d requests, want 0", len(theirs.Items))
	}
}

// The approver's hints are filled in on the model, so the check that a
// requester never gets them has to set them: a list read straight from the
// database can never carry them and would pass either way.
func TestToRequestResponse_ApproverFieldsStayOffRequesterResponses(t *testing.T) {
	req := models.LibraryRequest{
		ID: 1, Kind: models.RequestKindBook, Status: models.RequestStatusApproved,
		Notice: "This book was already in the library.", AlreadyInLibrary: true, SearchRunID: "run-1",
	}
	asRequester, _ := json.Marshal(toRequestResponse(req, false))
	if strings.Contains(string(asRequester), "notice") || strings.Contains(string(asRequester), "alreadyInLibrary") {
		t.Fatalf("requester response carries approver fields: %s", asRequester)
	}
	asAdmin, _ := json.Marshal(toRequestResponse(req, true))
	if !strings.Contains(string(asAdmin), `"notice"`) || !strings.Contains(string(asAdmin), `"alreadyInLibrary":true`) {
		t.Fatalf("admin response lacks the approver fields: %s", asAdmin)
	}
}

// A skipped book that reaches the library between the create check and the
// answer is not auto approved (#3898, #3954): Skipped is an admin's decision,
// so the request stays pending, the existing row is not touched, and the
// create response the requester gets carries no notice. This test used to pin
// a monitored book here, which the old rule kept pending as any existing book;
// that rule is gone, so it now uses the case that still waits.
func TestRequestsCreate_AutoApproveInLibraryStaysPendingWithNoNotice(t *testing.T) {
	auth.SetEnforceTenancyForTests(t, true)
	adder := &fakeAdder{}
	f := newRequestsFixture(t, wellsRequestStub(), adder)
	setAutoApprove(t, f, f.requester.ID, true)
	ctx := context.Background()
	author := seedWells(t, f.authors)
	if err := f.books.Create(ctx, &models.Book{
		ForeignID: "OL27482W", AuthorID: author.ID, Title: "The War of the Worlds", SortTitle: "war",
		OwnerUserID: f.admin.ID, MediaType: models.MediaTypeEbook, Monitored: false, Status: models.BookStatusSkipped,
	}); err != nil {
		t.Fatal(err)
	}
	req := models.LibraryRequest{
		OwnerUserID: f.requester.ID, Kind: models.RequestKindBook, ForeignID: "OL27482W", MediaType: "ebook",
		Title: "The War of the Worlds", PayloadJSON: `{"kind":"book","foreignId":"OL27482W","mediaType":"ebook"}`,
	}
	if err := f.requests.Create(ctx, &req, 0); err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	f.h.writeCreated(ctx, rec, req, f.requester.ID, "")
	if rec.Code != http.StatusCreated {
		t.Fatalf("create: %d %s", rec.Code, rec.Body.String())
	}
	if got := decodeRequest(t, rec); got.Status != models.RequestStatusPending {
		t.Fatalf("status %q, want pending for an existing book: %s", got.Status, rec.Body.String())
	}
	if n := adder.calls.Load() + adder.existingCalls.Load(); n != 0 {
		t.Fatalf("the add ran %d times for a request that must wait", n)
	}
	if strings.Contains(rec.Body.String(), "notice") {
		t.Fatalf("the requester's create response carries a notice: %s", rec.Body.String())
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

// The badge and approve ask one question. Under tenancy, for a page of
// requests over books owned by various accounts, a row is badged exactly when
// approving it resolves it, and a row with a format missing on another
// account's book is not badged, since approving it adds the format.
func TestRequestsQueue_BadgeAgreesWithApproveUnderTenancy(t *testing.T) {
	auth.SetEnforceTenancyForTests(t, true)
	f := newRequestsFixture(t, wellsRequestStub(), &fakeAdder{})
	ctx := context.Background()
	author := seedWells(t, f.authors)
	seedBook := func(foreignID string, owner int64, book models.Book) {
		book.ForeignID, book.AuthorID, book.OwnerUserID = foreignID, author.ID, owner
		book.Title, book.SortTitle = foreignID, foreignID
		if err := f.books.Create(ctx, &book); err != nil {
			t.Fatal(err)
		}
	}
	file := func(foreignID, mediaType string) int64 {
		payload := `{"kind":"book","foreignId":"` + foreignID + `","mediaType":"` + mediaType + `"}`
		req := models.LibraryRequest{
			OwnerUserID: f.requester.ID, Kind: models.RequestKindBook, ForeignID: foreignID, MediaType: mediaType,
			Title: foreignID, PayloadJSON: payload,
		}
		if err := f.requests.Create(ctx, &req, 0); err != nil {
			t.Fatal(err)
		}
		return req.ID
	}
	ebookWanted := models.Book{MediaType: models.MediaTypeEbook, Monitored: true, Status: models.BookStatusWanted}
	seedBook("OL-OTHER-COVERED", f.other.ID, ebookWanted)
	seedBook("OL-OTHER-MISSING", f.other.ID, ebookWanted)
	seedBook("OL-OWN-MISSING", f.requester.ID, ebookWanted)
	covered := file("OL-OTHER-COVERED", "ebook")
	held := file("OL-OTHER-MISSING", "audiobook")
	widen := file("OL-OWN-MISSING", "audiobook")
	absent := file("OL-NOT-THERE", "ebook")

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
	want := map[int64]bool{covered: true, held: false, widen: false, absent: false}
	for id, w := range want {
		if got, ok := flags[id]; !ok || got != w {
			t.Errorf("request %d badge = %v (present %v), want %v", id, got, ok, w)
		}
	}

	// Approve agrees: the badged row resolves without adding anything, and
	// the unbadged row on another account's book adds the format (#4089).
	approved := f.approve(f.admin, covered, `{}`)
	if approved.Code != http.StatusOK || !strings.Contains(decodeRequest(t, approved).Notice, "nothing was added") {
		t.Fatalf("approve of the badged row: %d %s", approved.Code, approved.Body.String())
	}
	widened := f.approve(f.admin, held, `{}`)
	if widened.Code != http.StatusOK || !strings.Contains(decodeRequest(t, widened).Notice, "now monitored") {
		t.Fatalf("approve of the unbadged row on another account's book: %d %s", widened.Code, widened.Body.String())
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
		{"monitored but skipped covers nothing", models.Book{MediaType: "ebook", Monitored: true, Status: "skipped"}, "ebook", true, false},
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

// #3898: a book the library holds as metadata only (no file, not monitored)
// is not covered, so a requester can ask for it. A book with its file is.
func TestRequestsCreate_BookWithoutFileCanBeRequested(t *testing.T) {
	f := newRequestsFixture(t, wellsRequestStub(), nil)
	author := seedWells(t, f.authors)
	book := &models.Book{ForeignID: "OL27482W", AuthorID: author.ID, Title: "The War of the Worlds", SortTitle: "war of the worlds", Status: models.BookStatusImported, MediaType: models.MediaTypeEbook}
	if err := f.books.Create(context.Background(), book); err != nil {
		t.Fatal(err)
	}
	rec := f.create(t, f.requester, `{"kind":"book","foreignId":"OL27482W"}`)
	if rec.Code != http.StatusCreated {
		t.Fatalf("file-less book request: %d %s", rec.Code, rec.Body.String())
	}
}

func TestRequestsCreate_CoveredBookStillRefused(t *testing.T) {
	f := newRequestsFixture(t, wellsRequestStub(), nil)
	// Monitored for the default format: someone is already getting it.
	author := seedWells(t, f.authors)
	book := &models.Book{ForeignID: "OL27482W", AuthorID: author.ID, Title: "The War of the Worlds", SortTitle: "war of the worlds", Status: models.BookStatusWanted, Monitored: true, MediaType: models.MediaTypeEbook}
	if err := f.books.Create(context.Background(), book); err != nil {
		t.Fatal(err)
	}
	rec := f.create(t, f.requester, `{"kind":"book","foreignId":"OL27482W"}`)
	if rec.Code != http.StatusConflict || requestErrorBody(rec) != "This book is already in the library." {
		t.Fatalf("covered book: %d %q", rec.Code, requestErrorBody(rec))
	}
}

// A requester picks the format, so the in library check is judged against the
// format named, not only the default (#3898).
func TestRequestsCreate_ExplicitMediaTypeAgainstExistingBook(t *testing.T) {
	const covered = "This book is already in the library."
	cases := []struct {
		name      string
		book      models.Book
		ebookFile bool
		mediaType string
		want      int
	}{
		{"ebook on disk, audiobook asked", models.Book{Status: models.BookStatusImported, MediaType: models.MediaTypeEbook}, true, "audiobook", http.StatusCreated},
		{"ebook on disk, both asked", models.Book{Status: models.BookStatusImported, MediaType: models.MediaTypeEbook}, true, "both", http.StatusCreated},
		{"ebook on disk, ebook asked", models.Book{Status: models.BookStatusImported, MediaType: models.MediaTypeEbook}, true, "ebook", http.StatusConflict},
		{"ebook on disk, default asked", models.Book{Status: models.BookStatusImported, MediaType: models.MediaTypeEbook}, true, "", http.StatusConflict},
		{"audiobook monitored, audiobook asked", models.Book{Status: models.BookStatusWanted, Monitored: true, MediaType: models.MediaTypeAudiobook}, false, "audiobook", http.StatusConflict},
		{"audiobook monitored, ebook asked", models.Book{Status: models.BookStatusWanted, Monitored: true, MediaType: models.MediaTypeAudiobook}, false, "ebook", http.StatusCreated},
		{"audiobook monitored, default asked", models.Book{Status: models.BookStatusWanted, Monitored: true, MediaType: models.MediaTypeAudiobook}, false, "", http.StatusCreated},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newRequestsFixture(t, wellsRequestStub(), nil)
			ctx := context.Background()
			author := seedWells(t, f.authors)
			book := tc.book
			book.ForeignID, book.AuthorID, book.Title, book.SortTitle = "OL27482W", author.ID, "The War of the Worlds", "war of the worlds"
			if err := f.books.Create(ctx, &book); err != nil {
				t.Fatal(err)
			}
			if tc.ebookFile {
				if err := f.books.AddBookFile(ctx, book.ID, models.MediaTypeEbook, "/books/wotw.epub"); err != nil {
					t.Fatal(err)
				}
			}
			rec := f.create(t, f.requester, `{"kind":"book","foreignId":"OL27482W","mediaType":"`+tc.mediaType+`"}`)
			if rec.Code != tc.want {
				t.Fatalf("create: %d %s, want %d", rec.Code, rec.Body.String(), tc.want)
			}
			if tc.want == http.StatusConflict && requestErrorBody(rec) != covered {
				t.Fatalf("refusal sentence %q, want %q", requestErrorBody(rec), covered)
			}
		})
	}
}

// #4089: under tenancy a book another account holds without the requested
// format no longer refuses a create. The request is created and waits for an
// admin. Once the book is covered, a create is refused as in the library,
// whoever owns it.
func TestRequestsCreate_HeldElsewhereMissingFormatIsQueued(t *testing.T) {
	auth.SetEnforceTenancyForTests(t, true)
	f := newRequestsFixture(t, wellsRequestStub(), nil)
	ctx := context.Background()
	author := seedWells(t, f.authors)
	held := &models.Book{ForeignID: "OL27482W", AuthorID: author.ID, Title: "The War of the Worlds", SortTitle: "war of the worlds", Status: models.BookStatusImported, MediaType: models.MediaTypeEbook, OwnerUserID: f.other.ID}
	if err := f.books.Create(ctx, held); err != nil {
		t.Fatal(err)
	}
	heldElsewhere := f.create(t, f.requester, `{"kind":"book","foreignId":"OL27482W"}`)
	if heldElsewhere.Code != http.StatusCreated {
		t.Fatalf("held elsewhere: %d %s, want 201", heldElsewhere.Code, heldElsewhere.Body.String())
	}
	if got := decodeRequest(t, heldElsewhere); got.Status != models.RequestStatusPending {
		t.Fatalf("status %q, want pending", got.Status)
	}

	if err := f.books.AddBookFile(ctx, held.ID, models.MediaTypeEbook, "/books/wotw.epub"); err != nil {
		t.Fatal(err)
	}
	covered := f.create(t, f.admin, `{"kind":"book","foreignId":"OL27482W"}`)
	if covered.Code != http.StatusConflict || requestErrorBody(covered) != "This book is already in the library." {
		t.Fatalf("covered: %d %q", covered.Code, requestErrorBody(covered))
	}
}

// #3961: approving with search on add must start a search whenever a
// requested format is monitored on the book but has no file, whether or not
// the author is monitored. A format on disk is never searched again, and a
// format the book does not monitor is added through the monitor core, which
// does its own search.
func TestRequestsApprove_InLibrarySearchOnAddMatrix(t *testing.T) {
	const ebookPath, audioPath = "/library/war.epub", "/library/war.m4b"
	cases := []struct {
		name          string
		requested     string
		book          models.Book
		authorMon     bool
		body          string
		heldElsewhere bool
		wantSearch    int32 // searchExistingBookCore calls
		wantWiden     int32 // monitorExistingBookCore calls
	}{
		{name: "reported case: monitored wanted book, author not monitored, no file", requested: "ebook",
			book: models.Book{MediaType: models.MediaTypeEbook, Monitored: true, Status: models.BookStatusWanted}, wantSearch: 1},
		{name: "monitored wanted book, author monitored, no file", requested: "ebook", authorMon: true,
			book: models.Book{MediaType: models.MediaTypeEbook, Monitored: true, Status: models.BookStatusWanted}, wantSearch: 1},
		{name: "audiobook wanted, no file, author not monitored", requested: "audiobook",
			book: models.Book{MediaType: models.MediaTypeAudiobook, Monitored: true, Status: models.BookStatusWanted}, wantSearch: 1},
		{name: "both asked, ebook on disk, audiobook monitored without a file", requested: "both",
			book: models.Book{MediaType: models.MediaTypeBoth, Monitored: true, Status: models.BookStatusWanted, EbookFilePath: ebookPath}, wantSearch: 1},
		{name: "imported with the requested ebook on disk does not search", requested: "ebook",
			book: models.Book{MediaType: models.MediaTypeEbook, Monitored: true, Status: models.BookStatusImported, EbookFilePath: ebookPath}},
		{name: "unmonitored book holding the file does not search", requested: "ebook",
			book: models.Book{MediaType: models.MediaTypeEbook, Monitored: false, Status: models.BookStatusImported, EbookFilePath: ebookPath}},
		{name: "both asked and both on disk does not search", requested: "both",
			book: models.Book{MediaType: models.MediaTypeBoth, Monitored: true, Status: models.BookStatusImported, EbookFilePath: ebookPath, AudiobookFilePath: audioPath}},
		{name: "search on add off does not search", requested: "ebook", body: `{}`,
			book: models.Book{MediaType: models.MediaTypeEbook, Monitored: true, Status: models.BookStatusWanted}},
		{name: "unmonitored book without a file goes through the monitor core", requested: "ebook",
			book: models.Book{MediaType: models.MediaTypeEbook, Monitored: false, Status: models.BookStatusWanted}, wantWiden: 1},
		{name: "skipped book goes through the monitor core", requested: "ebook",
			book: models.Book{MediaType: models.MediaTypeEbook, Monitored: true, Status: models.BookStatusSkipped}, wantWiden: 1},
		{name: "missing format goes through the monitor core, not a bare search", requested: "audiobook",
			book: models.Book{MediaType: models.MediaTypeEbook, Monitored: true, Status: models.BookStatusImported, EbookFilePath: ebookPath}, wantWiden: 1},
		{name: "an admin approval searches another account's monitored book", requested: "ebook", heldElsewhere: true,
			book: models.Book{MediaType: models.MediaTypeEbook, Monitored: true, Status: models.BookStatusWanted}, wantSearch: 1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if tc.heldElsewhere {
				auth.SetEnforceTenancyForTests(t, true)
			}
			adder := &fakeAdder{}
			f := newRequestsFixture(t, wellsRequestStub(), adder)
			book := tc.book
			if tc.heldElsewhere {
				book.OwnerUserID = f.other.ID
			}
			created, stored := pendingRequestThenInLibrary(t, f, tc.requested, book)
			ctx := context.Background()
			author, err := f.authors.GetByID(ctx, stored.AuthorID)
			if err != nil || author == nil {
				t.Fatalf("author: %v", err)
			}
			author.Monitored = tc.authorMon
			if err := f.authors.Update(ctx, author); err != nil {
				t.Fatal(err)
			}
			body := tc.body
			if body == "" {
				body = `{"searchOnAdd":true}`
			}

			rec := f.approve(f.admin, created.ID, body)
			if rec.Code != http.StatusOK {
				t.Fatalf("approve: %d %s", rec.Code, rec.Body.String())
			}
			got := decodeRequest(t, rec)
			if got.Status != "approved" || got.ResultBookID == nil || *got.ResultBookID != stored.ID {
				t.Fatalf("approved = %+v", got)
			}
			if n := adder.searchCalls.Load(); n != tc.wantSearch {
				t.Errorf("searches = %d, want %d", n, tc.wantSearch)
			}
			if n := adder.existingCalls.Load(); n != tc.wantWiden {
				t.Errorf("monitor core calls = %d, want %d", n, tc.wantWiden)
			}
			if adder.calls.Load() != 0 {
				t.Errorf("addBookCore ran %d times, want 0", adder.calls.Load())
			}
			if tc.wantSearch > 0 && got.SearchRunID != "run-existing" {
				t.Errorf("searchRunId = %q, want the run the search started", got.SearchRunID)
			}
			// A bare search writes nothing to the book or its author.
			after, _ := f.books.GetByID(ctx, stored.ID)
			if tc.wantWiden == 0 && (after.UpdatedAtRaw != stored.UpdatedAtRaw || after.Monitored != stored.Monitored ||
				after.Status != stored.Status || after.MediaType != stored.MediaType) {
				t.Errorf("book changed: %+v -> %+v", stored, after)
			}
			if a2, _ := f.authors.GetByID(ctx, stored.AuthorID); a2.Monitored != tc.authorMon {
				t.Errorf("author monitored = %v, want %v", a2.Monitored, tc.authorMon)
			}
		})
	}
}
