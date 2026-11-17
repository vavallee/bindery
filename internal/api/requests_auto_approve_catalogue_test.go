package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"
	"time"

	"github.com/vavallee/bindery/internal/auth"
	"github.com/vavallee/bindery/internal/models"
	"github.com/vavallee/bindery/internal/notifier"
)

// Tests for #3954: an auto approving account's request for a book the library
// already has a row for. Only a book an admin excluded or skipped waits; everything else
// goes through the approval an admin would run.

func TestRequestsCreate_AutoApproveCatalogueBookTable(t *testing.T) {
	const ebookPath = "/library/war.epub"
	type want struct {
		approved bool
		widen    int32 // monitorExistingBookCore calls
		search   int32 // searchExistingBookCore calls
	}
	cases := []struct {
		name      string
		excluded  bool
		book      models.Book
		requested string
		account   *bool // the account's own setting, nil follows the default
		sysOn     bool  // requests.auto_approve_default
		want      want
	}{
		{name: "reporter: unmonitored catalogue book, no file, account on", requested: "ebook",
			book: models.Book{MediaType: models.MediaTypeEbook, Status: models.BookStatusWanted}, account: bptr(true),
			want: want{approved: true, widen: 1}},
		{name: "unmonitored imported-status row with no file, account on", requested: "ebook",
			book: models.Book{MediaType: models.MediaTypeEbook, Status: models.BookStatusImported}, account: bptr(true),
			want: want{approved: true, widen: 1}},
		{name: "unmonitored book, account follows the default which is on", requested: "ebook",
			book: models.Book{MediaType: models.MediaTypeEbook, Status: models.BookStatusWanted}, sysOn: true,
			want: want{approved: true, widen: 1}},
		{name: "unmonitored book, account follows the default which is off", requested: "ebook",
			book: models.Book{MediaType: models.MediaTypeEbook, Status: models.BookStatusWanted}},
		{name: "unmonitored book, account Never overrides the default on", requested: "ebook",
			book: models.Book{MediaType: models.MediaTypeEbook, Status: models.BookStatusWanted}, account: bptr(false), sysOn: true},
		{name: "monitored, requested format missing on disk: search only", requested: "ebook",
			book: models.Book{MediaType: models.MediaTypeEbook, Monitored: true, Status: models.BookStatusWanted}, account: bptr(true),
			want: want{approved: true, search: 1}},
		{name: "monitored, account off", requested: "ebook",
			book: models.Book{MediaType: models.MediaTypeEbook, Monitored: true, Status: models.BookStatusWanted}, account: bptr(false)},
		{name: "ebook only in the catalogue, audiobook asked", requested: "audiobook",
			book: models.Book{MediaType: models.MediaTypeEbook, Monitored: true, Status: models.BookStatusImported, EbookFilePath: ebookPath}, account: bptr(true),
			want: want{approved: true, widen: 1}},
		{name: "ebook only in the catalogue, audiobook asked, account off", requested: "audiobook",
			book: models.Book{MediaType: models.MediaTypeEbook, Monitored: true, Status: models.BookStatusImported, EbookFilePath: ebookPath}, account: bptr(false)},
		{name: "skipped book, account on", requested: "ebook",
			book: models.Book{MediaType: models.MediaTypeEbook, Status: models.BookStatusSkipped}, account: bptr(true)},
		{name: "skipped book, default on", requested: "ebook",
			book: models.Book{MediaType: models.MediaTypeEbook, Status: models.BookStatusSkipped}, sysOn: true},
		{name: "skipped but still flagged monitored, account on", requested: "ebook",
			book: models.Book{MediaType: models.MediaTypeEbook, Monitored: true, Status: models.BookStatusSkipped}, account: bptr(true)},
		{name: "excluded book, account on", requested: "ebook", excluded: true,
			book: models.Book{MediaType: models.MediaTypeEbook, Status: models.BookStatusWanted}, account: bptr(true)},
		{name: "excluded book, default on", requested: "ebook", excluded: true,
			book: models.Book{MediaType: models.MediaTypeEbook, Status: models.BookStatusWanted}, sysOn: true},
		{name: "excluded but monitored, account on", requested: "ebook", excluded: true,
			book: models.Book{MediaType: models.MediaTypeEbook, Monitored: true, Status: models.BookStatusWanted}, account: bptr(true)},
		{name: "excluded and skipped, account on", requested: "ebook", excluded: true,
			book: models.Book{MediaType: models.MediaTypeEbook, Status: models.BookStatusSkipped}, account: bptr(true)},
		{name: "requested format on disk resolves without a write or search", requested: "ebook",
			book: models.Book{MediaType: models.MediaTypeEbook, Monitored: true, Status: models.BookStatusImported, EbookFilePath: ebookPath}, account: bptr(true),
			want: want{approved: true}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			adder := &fakeAdder{}
			f := newRequestsFixture(t, wellsRequestStub(), adder)
			capture := &capturingNotifier{events: make(chan capturedEvent, 4)}
			f.h.WithNotifier(capture)
			if tc.sysOn {
				setAutoApproveDefault(t, f.settings, "true")
			}
			if tc.account != nil {
				setAutoApprove(t, f, f.requester.ID, *tc.account)
			}
			ctx := context.Background()
			author := seedWells(t, f.authors)
			book := tc.book
			book.ForeignID, book.AuthorID = "OL27482W", author.ID
			book.Title, book.SortTitle = "The War of the Worlds", "war"
			file := book.EbookFilePath
			book.EbookFilePath = ""
			if err := f.books.Create(ctx, &book); err != nil {
				t.Fatal(err)
			}
			if file != "" {
				if err := f.books.AddBookFile(ctx, book.ID, models.MediaTypeEbook, file); err != nil {
					t.Fatal(err)
				}
			}
			if tc.excluded {
				if err := f.books.SetExcluded(ctx, book.ID, true); err != nil {
					t.Fatal(err)
				}
			}
			before, _ := f.books.GetByID(ctx, book.ID)

			req := models.LibraryRequest{
				OwnerUserID: f.requester.ID, Kind: models.RequestKindBook, ForeignID: "OL27482W", MediaType: tc.requested,
				Title: "The War of the Worlds", PayloadJSON: `{"kind":"book","foreignId":"OL27482W","mediaType":"` + tc.requested + `"}`,
			}
			if err := f.requests.Create(ctx, &req, 0); err != nil {
				t.Fatal(err)
			}
			rec := httptest.NewRecorder()
			f.h.writeCreated(ctx, rec, req, f.requester.ID, "")
			if rec.Code != http.StatusCreated {
				t.Fatalf("create: %d %s", rec.Code, rec.Body.String())
			}
			got := decodeRequest(t, rec)
			wantStatus := models.RequestStatusPending
			if tc.want.approved {
				wantStatus = models.RequestStatusApproved
			}
			if got.Status != wantStatus {
				t.Fatalf("status %q, want %q: %s", got.Status, wantStatus, rec.Body.String())
			}
			if n := adder.existingCalls.Load(); n != tc.want.widen {
				t.Errorf("monitor core calls = %d, want %d", n, tc.want.widen)
			}
			if n := adder.searchCalls.Load(); n != tc.want.search {
				t.Errorf("searches = %d, want %d", n, tc.want.search)
			}
			if n := adder.calls.Load(); n != 0 {
				t.Errorf("addBookCore ran %d times for a book already in the library", n)
			}
			after, _ := f.books.GetByID(ctx, book.ID)
			if before.Status != after.Status || before.Monitored != after.Monitored || before.Excluded != after.Excluded || before.UpdatedAtRaw != after.UpdatedAtRaw {
				t.Errorf("book row changed outside the monitor core: %+v -> %+v", before, after)
			}
			// requestCreated goes out only when the request stays pending.
			select {
			case ev := <-capture.events:
				if tc.want.approved {
					t.Errorf("approved request sent %s", ev.event)
				} else if ev.event != notifier.EventRequestCreated {
					t.Errorf("event %q, want %q", ev.event, notifier.EventRequestCreated)
				}
			case <-time.After(300 * time.Millisecond):
				if !tc.want.approved {
					t.Error("a pending request sent no requestCreated event")
				}
			}
		})
	}
}

// The reporter's case end to end through the create endpoint: the book is in
// the catalogue, nobody monitors it, there is no file, and the account auto
// approves. It was left for an admin before #3954.
func TestRequestsCreate_AutoApproveUnmonitoredCatalogueBookEndToEnd(t *testing.T) {
	adder := &fakeAdder{}
	f := newRequestsFixture(t, wellsRequestStub(), adder)
	setAutoApprove(t, f, f.requester.ID, true)
	ctx := context.Background()
	author := seedWells(t, f.authors)
	author.Monitored = false
	if err := f.authors.Update(ctx, author); err != nil {
		t.Fatal(err)
	}
	book := &models.Book{ForeignID: "OL27482W", AuthorID: author.ID, Title: "The War of the Worlds", SortTitle: "war",
		Status: models.BookStatusWanted, MediaType: models.MediaTypeEbook}
	if err := f.books.Create(ctx, book); err != nil {
		t.Fatal(err)
	}
	rec := f.create(t, f.requester, `{"kind":"book","foreignId":"OL27482W"}`)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create: %d %s", rec.Code, rec.Body.String())
	}
	got := decodeRequest(t, rec)
	if got.Status != models.RequestStatusApproved {
		t.Fatalf("got %+v, want approved", got)
	}
	if n := adder.existingCalls.Load(); n != 1 {
		t.Fatalf("monitor core calls = %d, want 1", n)
	}
	if a, _ := f.authors.GetByID(ctx, author.ID); a.Monitored {
		t.Fatal("the author was monitored by a book request")
	}
}

// seedCatalogueBooks puts the requested book and an unrelated sibling by the
// same author in the library, and files an auto approving requester's request
// for the first. The handler is built with the real add core (no fake), so a
// write to the wrong row would show.
func seedCatalogueBooks(t *testing.T, f *requestsFixture, target models.Book) (*models.Book, *models.Book, *models.Author, models.LibraryRequest) {
	t.Helper()
	ctx := context.Background()
	author := seedWells(t, f.authors)
	author.Monitored = false
	if err := f.authors.Update(ctx, author); err != nil {
		t.Fatal(err)
	}
	target.ForeignID, target.AuthorID, target.Title, target.SortTitle = "OL27482W", author.ID, "The War of the Worlds", "war"
	sibling := &models.Book{ForeignID: "OL9999W", AuthorID: author.ID, Title: "The Time Machine", SortTitle: "time",
		Status: models.BookStatusWanted, MediaType: models.MediaTypeEbook}
	for _, b := range []*models.Book{&target, sibling} {
		if err := f.books.Create(ctx, b); err != nil {
			t.Fatal(err)
		}
	}
	req := models.LibraryRequest{
		OwnerUserID: f.requester.ID, Kind: models.RequestKindBook, ForeignID: "OL27482W", MediaType: "ebook",
		Title: "The War of the Worlds", PayloadJSON: `{"kind":"book","foreignId":"OL27482W","mediaType":"ebook"}`,
	}
	if err := f.requests.Create(ctx, &req, 0); err != nil {
		t.Fatal(err)
	}
	return &target, sibling, author, req
}

// Against the real book repo and add core: a Skipped book's row is exactly as
// it was and the request waits.
func TestRequestsCreate_AutoApproveSkippedRowUntouchedRealRepo(t *testing.T) {
	f := newRequestsFixture(t, wellsRequestStub(), nil)
	setAutoApprove(t, f, f.requester.ID, true)
	ctx := context.Background()
	target, sibling, author, req := seedCatalogueBooks(t, f, models.Book{Status: models.BookStatusSkipped, MediaType: models.MediaTypeEbook})
	beforeT, _ := f.books.GetByID(ctx, target.ID)
	beforeS, _ := f.books.GetByID(ctx, sibling.ID)
	beforeA, _ := f.authors.GetByID(ctx, author.ID)

	rec := httptest.NewRecorder()
	f.h.writeCreated(ctx, rec, req, f.requester.ID, "")
	if got := decodeRequest(t, rec); got.Status != models.RequestStatusPending {
		t.Fatalf("status %q, want pending", got.Status)
	}
	afterT, _ := f.books.GetByID(ctx, target.ID)
	afterS, _ := f.books.GetByID(ctx, sibling.ID)
	afterA, _ := f.authors.GetByID(ctx, author.ID)
	if !reflect.DeepEqual(beforeT, afterT) || !reflect.DeepEqual(beforeS, afterS) || !reflect.DeepEqual(beforeA, afterA) {
		t.Fatalf("rows changed: book %+v -> %+v", beforeT, afterT)
	}
}

// Against the real book repo and add core: an Excluded book's row is exactly as
// it was (still excluded, still unmonitored) and the request waits. Excluded is
// what the web UI's Exclude action sets; it has no Skip action.
func TestRequestsCreate_AutoApproveExcludedRowUntouchedRealRepo(t *testing.T) {
	f := newRequestsFixture(t, wellsRequestStub(), nil)
	setAutoApprove(t, f, f.requester.ID, true)
	ctx := context.Background()
	target, sibling, author, req := seedCatalogueBooks(t, f, models.Book{Status: models.BookStatusWanted, MediaType: models.MediaTypeEbook})
	if err := f.books.SetExcluded(ctx, target.ID, true); err != nil {
		t.Fatal(err)
	}
	beforeT, _ := f.books.GetByID(ctx, target.ID)
	beforeS, _ := f.books.GetByID(ctx, sibling.ID)
	beforeA, _ := f.authors.GetByID(ctx, author.ID)

	rec := httptest.NewRecorder()
	f.h.writeCreated(ctx, rec, req, f.requester.ID, "")
	if got := decodeRequest(t, rec); got.Status != models.RequestStatusPending {
		t.Fatalf("status %q, want pending: %s", got.Status, rec.Body.String())
	}
	afterT, _ := f.books.GetByID(ctx, target.ID)
	afterS, _ := f.books.GetByID(ctx, sibling.ID)
	afterA, _ := f.authors.GetByID(ctx, author.ID)
	if !afterT.Excluded {
		t.Fatal("the book is no longer excluded")
	}
	if !reflect.DeepEqual(beforeT, afterT) || !reflect.DeepEqual(beforeS, afterS) || !reflect.DeepEqual(beforeA, afterA) {
		t.Fatalf("rows changed: book %+v -> %+v", beforeT, afterT)
	}
}

// A widen writes the requested book and nothing else: the author and a sibling
// book keep their rows exactly.
func TestRequestsCreate_AutoApproveWidenTouchesOnlyThatBook(t *testing.T) {
	f := newRequestsFixture(t, wellsRequestStub(), nil)
	setAutoApprove(t, f, f.requester.ID, true)
	ctx := context.Background()
	target, sibling, author, req := seedCatalogueBooks(t, f, models.Book{Status: models.BookStatusWanted, MediaType: models.MediaTypeEbook})
	beforeT, _ := f.books.GetByID(ctx, target.ID)
	beforeS, _ := f.books.GetByID(ctx, sibling.ID)
	beforeA, _ := f.authors.GetByID(ctx, author.ID)

	rec := httptest.NewRecorder()
	f.h.writeCreated(ctx, rec, req, f.requester.ID, "")
	if got := decodeRequest(t, rec); got.Status != models.RequestStatusApproved {
		t.Fatalf("status %q, want approved: %s", got.Status, rec.Body.String())
	}
	afterT, _ := f.books.GetByID(ctx, target.ID)
	afterS, _ := f.books.GetByID(ctx, sibling.ID)
	afterA, _ := f.authors.GetByID(ctx, author.ID)
	if !afterT.Monitored || afterT.Status != models.BookStatusWanted {
		t.Fatalf("requested book not monitored and wanted: %+v", afterT)
	}
	if beforeT.Monitored {
		t.Fatal("test setup: the book was already monitored")
	}
	if !reflect.DeepEqual(beforeS, afterS) || !reflect.DeepEqual(beforeA, afterA) {
		t.Fatalf("sibling or author changed: %+v -> %+v, author %+v -> %+v", beforeS, afterS, beforeA, afterA)
	}
}

// Under tenancy a book held by another account is never widened by an auto
// approval: the request stays pending, nothing is written, and an admin is told.
func TestRequestsCreate_AutoApproveBookHeldByAnotherAccountStaysPending(t *testing.T) {
	auth.SetEnforceTenancyForTests(t, true)
	adder := &fakeAdder{}
	f := newRequestsFixture(t, wellsRequestStub(), adder)
	capture := &capturingNotifier{events: make(chan capturedEvent, 4)}
	f.h.WithNotifier(capture)
	setAutoApprove(t, f, f.requester.ID, true)
	ctx := context.Background()
	author := seedWells(t, f.authors)
	book := &models.Book{ForeignID: "OL27482W", AuthorID: author.ID, Title: "The War of the Worlds", SortTitle: "war",
		OwnerUserID: f.other.ID, Status: models.BookStatusWanted, MediaType: models.MediaTypeEbook}
	if err := f.books.Create(ctx, book); err != nil {
		t.Fatal(err)
	}
	before, _ := f.books.GetByID(ctx, book.ID)
	req := models.LibraryRequest{
		OwnerUserID: f.requester.ID, Kind: models.RequestKindBook, ForeignID: "OL27482W", MediaType: "ebook",
		Title: "The War of the Worlds", PayloadJSON: `{"kind":"book","foreignId":"OL27482W","mediaType":"ebook"}`,
	}
	if err := f.requests.Create(ctx, &req, 0); err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	f.h.writeCreated(ctx, rec, req, f.requester.ID, "")
	if got := decodeRequest(t, rec); got.Status != models.RequestStatusPending {
		t.Fatalf("status %q, want pending", got.Status)
	}
	if n := adder.existingCalls.Load() + adder.searchCalls.Load() + adder.calls.Load(); n != 0 {
		t.Fatalf("add core ran %d times for another account's book", n)
	}
	if after, _ := f.books.GetByID(ctx, book.ID); !reflect.DeepEqual(before, after) {
		t.Fatalf("another account's book changed: %+v -> %+v", before, after)
	}
	select {
	case ev := <-capture.events:
		if ev.event != notifier.EventRequestCreated {
			t.Fatalf("event %q, want %q", ev.event, notifier.EventRequestCreated)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("a pending request sent no requestCreated event")
	}
}
