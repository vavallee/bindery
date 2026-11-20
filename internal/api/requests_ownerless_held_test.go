package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"

	"github.com/vavallee/bindery/internal/auth"
	"github.com/vavallee/bindery/internal/models"
)

// seedOwnerlessEbook adds the shared (no owner) ebook only book the requests
// below ask the audiobook of.
func seedOwnerlessEbook(t *testing.T, f *requestsFixture) *models.Book {
	t.Helper()
	author := seedWells(t, f.authors)
	book := &models.Book{ForeignID: "OL27482W", AuthorID: author.ID, Title: "The War of the Worlds", SortTitle: "war",
		OwnerUserID: 0, Status: models.BookStatusWanted, Monitored: true, MediaType: models.MediaTypeEbook}
	if err := f.books.Create(context.Background(), book); err != nil {
		t.Fatal(err)
	}
	return book
}

func autoApproveCreated(t *testing.T, f *requestsFixture, mediaType string) requestResponse {
	t.Helper()
	ctx := context.Background()
	req := models.LibraryRequest{
		OwnerUserID: f.requester.ID, Kind: models.RequestKindBook, ForeignID: "OL27482W", MediaType: mediaType,
		Title: "The War of the Worlds", PayloadJSON: `{"kind":"book","foreignId":"OL27482W","mediaType":"` + mediaType + `"}`,
	}
	if err := f.requests.Create(ctx, &req, 0); err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	f.h.writeCreated(ctx, rec, req, f.requester.ID, "")
	return decodeRequest(t, rec)
}

func addCalls(a *fakeAdder) int32 {
	return a.existingCalls.Load() + a.searchCalls.Load() + a.calls.Load()
}

// With tenancy on, a requester's auto approval acts like the book routes: it
// may not widen a shared book. The request stays pending.
func TestRequestsCreate_AutoApproveOwnerlessBookStaysPending(t *testing.T) {
	auth.SetEnforceTenancyForTests(t, true)
	adder := &fakeAdder{}
	f := newRequestsFixture(t, wellsRequestStub(), adder)
	setAutoApprove(t, f, f.requester.ID, true)
	book := seedOwnerlessEbook(t, f)
	before, _ := f.books.GetByID(context.Background(), book.ID)
	if got := autoApproveCreated(t, f, "audiobook"); got.Status != models.RequestStatusPending {
		t.Fatalf("status %q, want pending", got.Status)
	}
	if n := addCalls(adder); n != 0 {
		t.Fatalf("add core ran %d times for a shared book", n)
	}
	if after, _ := f.books.GetByID(context.Background(), book.ID); !reflect.DeepEqual(before, after) {
		t.Fatalf("shared book changed: %+v -> %+v", before, after)
	}
}

// Covered by monitoring but not on disk: no search starts either.
func TestRequestsCreate_AutoApproveOwnerlessCoveredBookIsNotSearched(t *testing.T) {
	auth.SetEnforceTenancyForTests(t, true)
	adder := &fakeAdder{}
	f := newRequestsFixture(t, wellsRequestStub(), adder)
	setAutoApprove(t, f, f.requester.ID, true)
	seedOwnerlessEbook(t, f)
	autoApproveCreated(t, f, "ebook")
	if n := addCalls(adder); n != 0 {
		t.Fatalf("add core ran %d times for a shared book", n)
	}
}

// A requester asking for a missing format of a shared book is answered as for
// another account's book: already in the library.
func TestRequestsCreate_OwnerlessMissingFormatAnswersLikeHeldElsewhere(t *testing.T) {
	auth.SetEnforceTenancyForTests(t, true)
	f := newRequestsFixture(t, wellsRequestStub(), &fakeAdder{})
	seedOwnerlessEbook(t, f)
	rec := f.create(t, f.requester, `{"kind":"book","foreignId":"OL27482W","mediaType":"audiobook"}`)
	if rec.Code != http.StatusConflict {
		t.Fatalf("create: %d %s, want 409", rec.Code, rec.Body.String())
	}
}

// An admin approving by hand may still widen a shared book.
func TestRequestsApprove_OwnerlessBookAdminWidens(t *testing.T) {
	auth.SetEnforceTenancyForTests(t, true)
	adder := &fakeAdder{}
	f := newRequestsFixture(t, wellsRequestStub(), adder)
	created, _ := pendingRequestThenInLibrary(t, f, "audiobook", models.Book{
		MediaType: models.MediaTypeEbook, Monitored: true, Status: models.BookStatusWanted,
	})
	rec := f.approve(f.admin, created.ID, `{}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("approve: %d %s", rec.Code, rec.Body.String())
	}
	if adder.existingCalls.Load() != 1 {
		t.Fatalf("monitor core ran %d times, want 1", adder.existingCalls.Load())
	}
}

// With tenancy off nothing is held: a requester's auto approval widens a book
// with no owner as before.
func TestRequestsCreate_AutoApproveOwnerlessBookTenancyOffUnchanged(t *testing.T) {
	auth.SetEnforceTenancyForTests(t, false)
	adder := &fakeAdder{}
	f := newRequestsFixture(t, wellsRequestStub(), adder)
	setAutoApprove(t, f, f.requester.ID, true)
	seedOwnerlessEbook(t, f)
	if got := autoApproveCreated(t, f, "audiobook"); got.Status != models.RequestStatusApproved {
		t.Fatalf("status %q, want approved", got.Status)
	}
	if adder.existingCalls.Load() != 1 {
		t.Fatalf("monitor core ran %d times, want 1", adder.existingCalls.Load())
	}
}
