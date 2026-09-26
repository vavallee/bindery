package api

import (
	"bytes"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"

	"github.com/vavallee/bindery/internal/models"
)

// A book can legitimately already be 'wanted' and unmonitored — that is the
// shape a Hardcover list book lands in when its list has MonitorNew off. The
// moment it becomes eligible to grab is when the user monitors it, but the
// transition hook on this handler only looked at status, so monitoring an
// already-wanted book fired nothing and it waited for the next wanted sweep
// (#2722).
func TestBookUpdate_SearchWhenAnAlreadyWantedBookIsMonitored(t *testing.T) {
	searcher := newMockBookSearcher()
	h, books, author, ctx := bookFixtureWithSearcher(t, searcher)

	book := &models.Book{
		ForeignID: "B_MON_WANTED", AuthorID: author.ID, Title: "Wanted Unmonitored",
		SortTitle: "wanted unmonitored", Status: models.BookStatusWanted,
		MediaType: models.MediaTypeEbook, Genres: []string{},
		MetadataProvider: "openlibrary", Monitored: false,
	}
	if err := books.Create(ctx, book); err != nil {
		t.Fatal(err)
	}

	body := bytes.NewBufferString(`{"monitored":true}`)
	req := withURLParam(httptest.NewRequest(http.MethodPut, "/api/v1/book/"+strconv.FormatInt(book.ID, 10), body), "id", strconv.FormatInt(book.ID, 10))
	rec := httptest.NewRecorder()
	h.Update(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	if call := searcher.waitForCall(t, time.Second); call.ID != book.ID {
		t.Errorf("searcher called with wrong book id: got %d, want %d", call.ID, book.ID)
	}
}

// The counterpart: editing a book that was already wanted and monitored must
// not start another search. Repeat searches belong to the sweep; this hook is
// for transitions into the state only.
func TestBookUpdate_NoSearchWhenAlreadyWantedAndMonitored(t *testing.T) {
	searcher := newMockBookSearcher()
	h, books, author, ctx := bookFixtureWithSearcher(t, searcher)

	book := &models.Book{
		ForeignID: "B_ALREADY", AuthorID: author.ID, Title: "Already Eligible",
		SortTitle: "already eligible", Status: models.BookStatusWanted,
		MediaType: models.MediaTypeEbook, Genres: []string{},
		MetadataProvider: "openlibrary", Monitored: true,
	}
	if err := books.Create(ctx, book); err != nil {
		t.Fatal(err)
	}

	body := bytes.NewBufferString(`{"title":"Already Eligible, Revised"}`)
	req := withURLParam(httptest.NewRequest(http.MethodPut, "/api/v1/book/"+strconv.FormatInt(book.ID, 10), body), "id", strconv.FormatInt(book.ID, 10))
	rec := httptest.NewRecorder()
	h.Update(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	searcher.assertNoCall(t, 200*time.Millisecond)
}

// The bulk "monitor" action had the same blind spot: setBookMonitored wrote
// the flag and nothing queued a search, so monitoring a page of already-wanted
// books from the Wanted list grabbed none of them (#2722).
func TestBooksBulk_MonitorSearchesAnAlreadyWantedBook(t *testing.T) {
	searcher := newMockBookSearcher()
	h, _, books, author, ctx := bulkFixtureWithSearcher(t, searcher)

	book := mustCreateBook(t, books, ctx, &models.Book{
		ForeignID: "B_BULK_MON_WANTED", AuthorID: author.ID, Title: "Bulk Wanted Unmonitored",
		SortTitle: "bulk wanted unmonitored", Status: models.BookStatusWanted,
		MediaType: models.MediaTypeEbook, Genres: []string{},
		MetadataProvider: "openlibrary", Monitored: false,
	})

	body := fmt.Sprintf(`{"ids":[%d],"action":"monitor"}`, book.ID)
	rec := postBulk(t, h.BooksBulk, body)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	if call := searcher.waitForCall(t, time.Second); call.ID != book.ID {
		t.Errorf("searcher called with wrong book id: got %d, want %d", call.ID, book.ID)
	}
}

// Re-monitoring a book that is already monitored and wanted is a no-op, not a
// reason to search again.
func TestBooksBulk_MonitorSkipsAnAlreadyMonitoredBook(t *testing.T) {
	searcher := newMockBookSearcher()
	h, _, books, author, ctx := bulkFixtureWithSearcher(t, searcher)

	book := mustCreateBook(t, books, ctx, &models.Book{
		ForeignID: "B_BULK_ALREADY", AuthorID: author.ID, Title: "Bulk Already Eligible",
		SortTitle: "bulk already eligible", Status: models.BookStatusWanted,
		MediaType: models.MediaTypeEbook, Genres: []string{},
		MetadataProvider: "openlibrary", Monitored: true,
	})

	body := fmt.Sprintf(`{"ids":[%d],"action":"monitor"}`, book.ID)
	rec := postBulk(t, h.BooksBulk, body)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	searcher.assertNoCall(t, 200*time.Millisecond)
}
