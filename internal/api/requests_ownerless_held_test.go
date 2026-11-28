package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strconv"
	"testing"
	"time"

	"github.com/vavallee/bindery/internal/auth"
	"github.com/vavallee/bindery/internal/models"
	"github.com/vavallee/bindery/internal/notifier"
)

// seedOwnerlessEbook adds the shared (no owner) ebook only book the requests
// below ask the audiobook of.
func seedOwnerlessEbook(t *testing.T, f *requestsFixture) *models.Book {
	t.Helper()
	return seedEbookOwnedBy(t, f, 0)
}

// seedEbookOwnedBy is seedOwnerlessEbook with an owner; 0 means shared.
func seedEbookOwnedBy(t *testing.T, f *requestsFixture, owner int64) *models.Book {
	t.Helper()
	author := seedWells(t, f.authors)
	book := &models.Book{ForeignID: "OL27482W", AuthorID: author.ID, Title: "The War of the Worlds", SortTitle: "war",
		OwnerUserID: owner, Status: models.BookStatusWanted, Monitored: true, MediaType: models.MediaTypeEbook}
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

// #4089: a requester asking for a missing format of a book they can't change,
// a shared one or another account's, gets a request in the admin's queue
// instead of a refusal. It is never auto approved, whatever the account's
// setting, so the book is neither widened nor searched until an admin
// approves, and the admin is notified. The admin's approval then adds the
// format to the book, which keeps its owner.
func TestRequestsCreate_MissingFormatOfBookRequesterCannotChangeWaitsForAdmin(t *testing.T) {
	for _, tc := range []struct {
		name   string
		owner  func(f *requestsFixture) int64
		accept string // the account's auto approve setting
	}{
		{"shared book, auto approve on", func(*requestsFixture) int64 { return 0 }, "on"},
		{"shared book, auto approve from the default", func(*requestsFixture) int64 { return 0 }, "default"},
		{"another account's book, auto approve on", func(f *requestsFixture) int64 { return f.other.ID }, "on"},
		{"another account's book, auto approve off", func(f *requestsFixture) int64 { return f.other.ID }, "off"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			auth.SetEnforceTenancyForTests(t, true)
			ctx := context.Background()
			adder := &fakeAdder{}
			f := newRequestsFixture(t, wellsRequestStub(), adder)
			capture := &capturingNotifier{events: make(chan capturedEvent, 4)}
			f.h.WithNotifier(capture)
			switch tc.accept {
			case "on":
				setAutoApprove(t, f, f.requester.ID, true)
			case "off":
				setAutoApprove(t, f, f.requester.ID, false)
			case "default":
				setAutoApproveDefault(t, f.settings, "true")
			}
			owner := tc.owner(f)
			book := seedEbookOwnedBy(t, f, owner)
			before, _ := f.books.GetByID(ctx, book.ID)

			rec := f.create(t, f.requester, `{"kind":"book","foreignId":"OL27482W","mediaType":"audiobook"}`)
			if rec.Code != http.StatusCreated {
				t.Fatalf("create: %d %s, want 201", rec.Code, rec.Body.String())
			}
			created := decodeRequest(t, rec)
			if created.Status != models.RequestStatusPending {
				t.Fatalf("status %q, want pending", created.Status)
			}
			if n := addCalls(adder); n != 0 {
				t.Fatalf("add core ran %d times before an admin approved", n)
			}
			if after, _ := f.books.GetByID(ctx, book.ID); !reflect.DeepEqual(before, after) {
				t.Fatalf("book changed before an admin approved: %+v -> %+v", before, after)
			}
			select {
			case ev := <-capture.events:
				if ev.event != notifier.EventRequestCreated {
					t.Fatalf("event %q, want %q", ev.event, notifier.EventRequestCreated)
				}
			case <-time.After(5 * time.Second):
				t.Fatal("a request waiting for an admin sent no requestCreated event")
			}

			approved := f.approve(f.admin, created.ID, `{}`)
			if approved.Code != http.StatusOK {
				t.Fatalf("approve: %d %s", approved.Code, approved.Body.String())
			}
			if got := decodeRequest(t, approved); got.Status != models.RequestStatusApproved {
				t.Fatalf("approved status %q", got.Status)
			}
			if adder.existingCalls.Load() != 1 {
				t.Fatalf("monitor core ran %d times on approval, want 1", adder.existingCalls.Load())
			}
			if after, _ := f.books.GetByID(ctx, book.ID); after.OwnerUserID != owner {
				t.Fatalf("owner %d after approval, want %d", after.OwnerUserID, owner)
			}
		})
	}
}

// The account's own book is not held: its auto approval widens it as before.
func TestRequestsCreate_MissingFormatOfOwnBookStillAutoApproves(t *testing.T) {
	auth.SetEnforceTenancyForTests(t, true)
	adder := &fakeAdder{}
	f := newRequestsFixture(t, wellsRequestStub(), adder)
	setAutoApprove(t, f, f.requester.ID, true)
	seedEbookOwnedBy(t, f, f.requester.ID)
	rec := f.create(t, f.requester, `{"kind":"book","foreignId":"OL27482W","mediaType":"audiobook"}`)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create: %d %s", rec.Code, rec.Body.String())
	}
	if got := decodeRequest(t, rec); got.Status != models.RequestStatusApproved {
		t.Fatalf("status %q, want approved", got.Status)
	}
	if adder.existingCalls.Load() != 1 {
		t.Fatalf("monitor core ran %d times, want 1", adder.existingCalls.Load())
	}
}

// An admin files for a requester who auto approves, for a missing format of a
// shared book. The admin may file it, but the approval acts as the requester,
// so the request stays pending and the book is neither monitored nor searched.
func TestRequestsCreate_ForUserAutoApproveOwnerlessBookStaysPending(t *testing.T) {
	auth.SetEnforceTenancyForTests(t, true)
	adder := &fakeAdder{}
	f := newRequestsFixture(t, wellsRequestStub(), adder)
	setAutoApprove(t, f, f.requester.ID, true)
	book := seedOwnerlessEbook(t, f)
	before, _ := f.books.GetByID(context.Background(), book.ID)
	rec := f.create(t, f.admin, `{"kind":"book","foreignId":"OL27482W","mediaType":"audiobook","forUser":`+strconv.FormatInt(f.requester.ID, 10)+`}`)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create for user: %d %s", rec.Code, rec.Body.String())
	}
	if got := decodeRequest(t, rec); got.Status != models.RequestStatusPending {
		t.Fatalf("status %q, want pending", got.Status)
	}
	if n := addCalls(adder); n != 0 {
		t.Fatalf("add core ran %d times for a shared book", n)
	}
	if after, _ := f.books.GetByID(context.Background(), book.ID); !reflect.DeepEqual(before, after) {
		t.Fatalf("shared book changed: %+v -> %+v", before, after)
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
