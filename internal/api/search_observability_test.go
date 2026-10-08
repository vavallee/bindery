package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"

	"github.com/vavallee/bindery/internal/auth"
	"github.com/vavallee/bindery/internal/db"
	"github.com/vavallee/bindery/internal/indexer"
	"github.com/vavallee/bindery/internal/models"
)

// bulkResultFields decodes a bulk response loosely, so a field the response
// does not carry reads as absent rather than failing to compile.
func bulkResultFields(t *testing.T, rec *httptest.ResponseRecorder) map[string]map[string]any {
	t.Helper()
	var resp struct {
		Results map[string]map[string]any `json:"results"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode bulk response: %v: %s", err, rec.Body.String())
	}
	return resp.Results
}

// #2154 point 2: a bulk search answers ok:true before any indexer has been
// asked, because the searches run on a pool after the response is written.
// The response now says the book was queued, so ok:true can no longer be read
// as "searched and done".
func TestBulkSearch_SaysQueuedNotFinished(t *testing.T) {
	searcher := newMockBookSearcher()
	h, _, books, author, ctx := bulkFixtureWithSearcher(t, searcher)
	book := mustCreateBook(t, books, ctx, &models.Book{
		ForeignID: "Q_SRCH", AuthorID: author.ID, Title: "Queued Book",
		SortTitle: "queued book", Status: models.BookStatusWanted,
		Genres: []string{}, MetadataProvider: "openlibrary", Monitored: true,
	})
	key := strconv.FormatInt(book.ID, 10)

	cases := []struct {
		name    string
		handler http.HandlerFunc
		id      int64
	}{
		{"book bulk", h.BooksBulk, book.ID},
		{"wanted bulk", h.WantedBulk, book.ID},
		{"author bulk", h.AuthorsBulk, author.ID},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec := postBulk(t, tc.handler, fmt.Sprintf(`{"ids":[%d],"action":"search"}`, tc.id))
			if rec.Code != http.StatusOK {
				t.Fatalf("status = %d: %s", rec.Code, rec.Body.String())
			}
			item := bulkResultFields(t, rec)[strconv.FormatInt(tc.id, 10)]
			if item["ok"] != true || item["queued"] != true {
				t.Fatalf("result = %v, want ok:true and queued:true", item)
			}
			if got := searcher.waitForCall(t, time.Second); got.ID != book.ID {
				t.Fatalf("searched book %d, want %d", got.ID, book.ID)
			}
		})
	}

	// A non search action is done by the time the response is written, so it
	// must not claim to be queued.
	rec := postBulk(t, h.BooksBulk, fmt.Sprintf(`{"ids":[%d],"action":"unmonitor"}`, book.ID))
	if item := bulkResultFields(t, rec)[key]; item["ok"] != true || item["queued"] != nil {
		t.Fatalf("unmonitor result = %v, want ok:true without queued", item)
	}
}

// An author with nothing wanted queues nothing, and says so by leaving queued
// off rather than claiming a search that never runs.
func TestBulkSearch_AuthorWithNothingWantedIsNotQueued(t *testing.T) {
	searcher := newMockBookSearcher()
	h, _, books, author, ctx := bulkFixtureWithSearcher(t, searcher)
	mustCreateBook(t, books, ctx, &models.Book{
		ForeignID: "Q_IMP", AuthorID: author.ID, Title: "Already Here",
		SortTitle: "already here", Status: models.BookStatusImported,
		Genres: []string{}, MetadataProvider: "openlibrary", Monitored: true,
	})
	rec := postBulk(t, h.AuthorsBulk, fmt.Sprintf(`{"ids":[%d],"action":"search"}`, author.ID))
	item := bulkResultFields(t, rec)[strconv.FormatInt(author.ID, 10)]
	if item["ok"] != true || item["queued"] != nil {
		t.Fatalf("result = %v, want ok:true without queued", item)
	}
	searcher.assertNoCall(t, 50*time.Millisecond)
}

// #2154 point 3: GET /search/last-debug was partitioned by user id, so a
// script holding the API key read its own bucket and never saw the search a
// signed in user had just run in the browser; it got an older, unrelated
// search or a 404 with nothing to say which. The API key is admin equivalent
// and now reads the newest interactive search, which names its origin and
// book.
func TestLastSearchDebug_APIKeyReadsTheBrowserSearch(t *testing.T) {
	database, err := db.OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { database.Close() })
	ctx := context.Background()

	authorRepo := db.NewAuthorRepo(database)
	author := &models.Author{
		ForeignID: "OL9A", Name: "Frank Herbert", SortName: "Herbert, Frank",
		MetadataProvider: "openlibrary", Monitored: true,
	}
	if err := authorRepo.Create(ctx, author); err != nil {
		t.Fatal(err)
	}
	bookRepo := db.NewBookRepo(database)
	book := &models.Book{
		Title: "Dune", ForeignID: "OL9M",
		AuthorID: author.ID, MediaType: models.MediaTypeEbook, Monitored: true,
	}
	if err := bookRepo.Create(ctx, book); err != nil {
		t.Fatal(err)
	}
	h := NewIndexerHandler(
		db.NewIndexerRepo(database), bookRepo, authorRepo,
		db.NewMetadataProfileRepo(database), debugSearcher{},
		db.NewSettingsRepo(database), db.NewBlocklistRepo(database),
	)

	const (
		operatorID int64 = 1 // the identity the middleware stamps on API key requests
		browserID  int64 = 2 // a signed in user searching from the book page
	)

	searchReq := withURLParam(
		httptest.NewRequest(http.MethodPost, "/book/1/search", nil),
		"id", strconv.FormatInt(book.ID, 10),
	)
	searchReq = searchReq.WithContext(withAuthCtx(searchReq.Context(), browserID, "user"))
	searchRec := httptest.NewRecorder()
	h.SearchBook(searchRec, searchReq)
	if searchRec.Code != http.StatusOK {
		t.Fatalf("search status = %d: %s", searchRec.Code, searchRec.Body.String())
	}

	readReq := httptest.NewRequest(http.MethodGet, "/search/last-debug", nil)
	readReq = readReq.WithContext(auth.WithAPIKeyAuth(withAuthCtx(readReq.Context(), operatorID, "admin")))
	readRec := httptest.NewRecorder()
	h.LastSearchDebug(readRec, readReq)
	if readRec.Code != http.StatusOK {
		t.Fatalf("API key read status = %d, want 200 with the browser search: %s", readRec.Code, readRec.Body.String())
	}
	var got struct {
		Origin string `json:"origin"`
		BookID int64  `json:"bookId"`
		UserID int64  `json:"userId"`
		Query  struct {
			Title string `json:"title"`
		} `json:"query"`
	}
	if err := json.Unmarshal(readRec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got.Query.Title != "Dune" || got.BookID != book.ID || got.Origin != indexer.DebugOriginInteractive || got.UserID != browserID {
		t.Fatalf("API key read %+v, want the browser's interactive search of book %d by user %d", got, book.ID, browserID)
	}

	// A different signed in user still cannot read it (#1859).
	otherReq := httptest.NewRequest(http.MethodGet, "/search/last-debug", nil)
	otherReq = otherReq.WithContext(withAuthCtx(otherReq.Context(), 3, "admin"))
	otherRec := httptest.NewRecorder()
	h.LastSearchDebug(otherRec, otherReq)
	if otherRec.Code != http.StatusNotFound {
		t.Fatalf("another user's read = %d %s, want 404", otherRec.Code, otherRec.Body.String())
	}
}

// Automatic searches are recorded by the scheduler into the same log. They
// are shown to whoever may see the book, newest search first, and hidden from
// a user who may not see the book once tenancy is on.
func TestLastSearchDebug_AutomaticSearchFollowsBookVisibility(t *testing.T) {
	auth.SetEnforceTenancyForTests(t, true)
	h := &IndexerHandler{lastDebug: indexer.NewDebugLog()}
	const (
		ownerID int64 = 5
		otherID int64 = 7
	)
	h.lastDebug.RecordInteractive(ownerID, &indexer.SearchDebug{Origin: indexer.DebugOriginInteractive, BookID: 1})
	h.lastDebug.RecordInteractive(otherID, &indexer.SearchDebug{Origin: indexer.DebugOriginInteractive, BookID: 2})
	h.lastDebug.RecordBackground(ownerID, &indexer.SearchDebug{Origin: string(indexer.OriginScheduled), BookID: 3, Outcome: "no results"})

	read := func(uid int64) (int, indexer.SearchDebug) {
		req := httptest.NewRequest(http.MethodGet, "/search/last-debug", nil)
		req = req.WithContext(withAuthCtx(req.Context(), uid, "user"))
		rec := httptest.NewRecorder()
		h.LastSearchDebug(rec, req)
		var d indexer.SearchDebug
		_ = json.Unmarshal(rec.Body.Bytes(), &d)
		return rec.Code, d
	}

	if code, d := read(ownerID); code != http.StatusOK || d.BookID != 3 || d.Origin != "scheduled" || d.Outcome != "no results" {
		t.Fatalf("owner read %d %+v, want the newer scheduled search of book 3", code, d)
	}
	if code, d := read(otherID); code != http.StatusOK || d.BookID != 2 {
		t.Fatalf("other user read %d %+v, want their own search of book 2, not the owner's scheduled one", code, d)
	}
}
