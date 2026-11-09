package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/vavallee/bindery/internal/auth"
	"github.com/vavallee/bindery/internal/models"
	"github.com/vavallee/bindery/internal/searchruns"
)

// runSpy is a BookSearcher that finishes the run it was handed the way the
// scheduler does, so a test can tell which run id reached the search.
type runSpy struct {
	store *searchruns.Store
	mu    sync.Mutex
	seen  map[int64]string // book id -> run id the search was given
	done  chan struct{}
	want  int
}

func newRunSpy(store *searchruns.Store, want int) *runSpy {
	return &runSpy{store: store, seen: map[int64]string{}, done: make(chan struct{}), want: want}
}

func (s *runSpy) SearchAndGrabBook(ctx context.Context, book models.Book) {
	id := searchruns.RunIDFrom(ctx)
	s.store.Running(id)
	s.store.Record(id, searchruns.Result{Outcome: "no results"})
	s.store.Finish(id)
	s.mu.Lock()
	s.seen[book.ID] = id
	if len(s.seen) == s.want {
		close(s.done)
	}
	s.mu.Unlock()
}

func (s *runSpy) wait(t *testing.T) {
	t.Helper()
	select {
	case <-s.done:
	case <-time.After(2 * time.Second):
		t.Fatal("search was not dispatched")
	}
}

func getRun(t *testing.T, h *SearchRunHandler, id string, uid int64, role string) *httptest.ResponseRecorder {
	t.Helper()
	r := chi.NewRouter()
	r.Get("/search/runs/{id}", h.Get)
	req := httptest.NewRequest(http.MethodGet, "/search/runs/"+id, nil)
	req = req.WithContext(withAuthCtx(req.Context(), uid, role))
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	return rec
}

func TestSearchRunEndpoint_ReturnsAFinishedRunToItsOwner(t *testing.T) {
	auth.SetEnforceTenancyForTests(t, true)
	store := searchruns.NewStore(10, time.Hour)
	h := NewSearchRunHandler(store)
	id := store.Start(7, 42, "bulk")
	store.Running(id)
	store.Record(id, searchruns.Result{Outcome: "grabbed", Indexers: 2, RawResults: 9, AfterFilters: 5, Approved: 2, ElapsedMs: 31,
		Grabbed: &searchruns.Release{Title: "Dune epub", Indexer: "nzb", MediaType: "ebook"}})
	store.Finish(id)

	rec := getRun(t, h, id, 7, auth.RoleUser)
	if rec.Code != http.StatusOK {
		t.Fatalf("owner: %d %s", rec.Code, rec.Body.String())
	}
	var got map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	for k, want := range map[string]any{
		"id": id, "state": "finished", "outcome": "grabbed", "bookId": 42.0, "origin": "bulk",
		"indexers": 2.0, "rawResults": 9.0, "afterFilters": 5.0, "approved": 2.0, "elapsedMs": 31.0,
	} {
		if got[k] != want {
			t.Errorf("%s = %v, want %v", k, got[k], want)
		}
	}
	grabbed, _ := got["grabbed"].(map[string]any)
	if grabbed["title"] != "Dune epub" {
		t.Errorf("grabbed = %v", got["grabbed"])
	}
	if _, leaked := got["ownerUserId"]; leaked {
		t.Error("the owner id must not be in the response")
	}
}

func TestSearchRunEndpoint_UnknownAndForeignRunsAreBothNotFound(t *testing.T) {
	auth.SetEnforceTenancyForTests(t, true)
	store := searchruns.NewStore(10, time.Hour)
	h := NewSearchRunHandler(store)
	theirs := store.Start(2, 1, "book")
	unowned := store.Start(0, 1, "scheduled")

	unknown := getRun(t, h, "0123456789abcdef0123456789abcdef", 1, auth.RoleUser)
	foreign := getRun(t, h, theirs, 1, auth.RoleUser)
	noOwner := getRun(t, h, unowned, 1, auth.RoleUser)
	for name, rec := range map[string]*httptest.ResponseRecorder{"unknown": unknown, "foreign": foreign, "unowned": noOwner} {
		if rec.Code != http.StatusNotFound {
			t.Errorf("%s: %d, want 404", name, rec.Code)
		}
	}
	// No existence leak: the three answers are byte for byte the same.
	if foreign.Body.String() != unknown.Body.String() || noOwner.Body.String() != unknown.Body.String() {
		t.Errorf("404 bodies differ: %q %q %q", unknown.Body, foreign.Body, noOwner.Body)
	}
	for _, id := range []string{theirs, unowned} {
		if rec := getRun(t, h, id, 99, auth.RoleAdmin); rec.Code != http.StatusOK {
			t.Errorf("admin must read %s, got %d", id, rec.Code)
		}
	}
}

func TestSearchRunEndpoint_TenancyOffReadsEveryRun(t *testing.T) {
	auth.SetEnforceTenancyForTests(t, false)
	store := searchruns.NewStore(10, time.Hour)
	h := NewSearchRunHandler(store)
	id := store.Start(2, 1, "book")
	if rec := getRun(t, h, id, 1, auth.RoleUser); rec.Code != http.StatusOK {
		t.Fatalf("gate off should not scope, got %d", rec.Code)
	}
}

func TestSearchRunEndpoint_NilStoreIsNotFound(t *testing.T) {
	h := NewSearchRunHandler(nil)
	if rec := getRun(t, h, "abc", 1, auth.RoleAdmin); rec.Code != http.StatusNotFound {
		t.Fatalf("got %d", rec.Code)
	}
}

// POST /book/bulk search answers with one run id per book that will search,
// and the search is handed that same id (#3133).
func TestBulk_BookSearch_ReturnsARunIDPerBook(t *testing.T) {
	auth.SetEnforceTenancyForTests(t, true)
	f := seedTwoUserBulk(t)
	store := searchruns.NewStore(10, time.Hour)
	spy := newRunSpy(store, 1)
	f.h.searcher = spy
	f.h.WithSearchRuns(store)

	rec := postBulkAs(t, f.h.BooksBulk, fmt.Sprintf(`{"ids":[%d],"action":"search"}`, f.b1.ID), f.u1, auth.RoleUser)
	resp := decodeBulk(t, rec)
	item := resp.Results[strconv.FormatInt(f.b1.ID, 10)]
	if !item.OK || !item.Queued || item.RunID == "" {
		t.Fatalf("want ok, queued and a runId, got %+v", item)
	}
	spy.wait(t)
	if spy.seen[f.b1.ID] != item.RunID {
		t.Fatalf("search was given run %q, response said %q", spy.seen[f.b1.ID], item.RunID)
	}
	run, ok := store.Get(item.RunID, f.u1)
	if !ok || run.BookID != f.b1.ID || run.Origin != "bulk" || run.State != searchruns.StateFinished {
		t.Fatalf("owner must read the run: %+v ok=%v", run, ok)
	}
	if _, ok := store.Get(item.RunID, f.u2); ok {
		t.Fatal("another user must not read the run")
	}
}

// A book that will not search gets no run id: nothing was registered.
func TestBulk_BookSearch_NoRunIDWhenNothingIsQueued(t *testing.T) {
	auth.SetEnforceTenancyForTests(t, false)
	f := seedTwoUserBulk(t)
	store := searchruns.NewStore(10, time.Hour)
	f.h.searcher = newRunSpy(store, 99)
	f.h.WithSearchRuns(store)
	book := *f.b1
	book.EbookFilePath = "/lib/x.epub"
	if err := f.books.Update(context.Background(), &book); err != nil {
		t.Fatal(err)
	}

	rec := postBulkAs(t, f.h.BooksBulk, fmt.Sprintf(`{"ids":[%d],"action":"search"}`, f.b1.ID), f.u1, auth.RoleUser)
	item := decodeBulk(t, rec).Results[strconv.FormatInt(f.b1.ID, 10)]
	if item.Queued || item.RunID != "" || item.SearchSkipped != searchSkipNoFormatNeeded {
		t.Fatalf("want skipped without a run id, got %+v", item)
	}
	if store.Len() != 0 {
		t.Fatalf("no run should be registered, have %d", store.Len())
	}
}

func TestBulk_WantedSearch_ReturnsARunID(t *testing.T) {
	auth.SetEnforceTenancyForTests(t, false)
	f := seedTwoUserBulk(t)
	store := searchruns.NewStore(10, time.Hour)
	spy := newRunSpy(store, 1)
	f.h.searcher = spy
	f.h.WithSearchRuns(store)

	rec := postBulkAs(t, f.h.WantedBulk, fmt.Sprintf(`{"ids":[%d],"action":"search"}`, f.b1.ID), f.u1, auth.RoleUser)
	item := decodeBulk(t, rec).Results[strconv.FormatInt(f.b1.ID, 10)]
	spy.wait(t)
	if item.RunID == "" || spy.seen[f.b1.ID] != item.RunID {
		t.Fatalf("want the run id the search got, response %+v, search %q", item, spy.seen[f.b1.ID])
	}
}

// With no store wired the fields are simply absent and searching still works.
func TestBulk_BookSearch_WithoutAStoreHasNoRunID(t *testing.T) {
	auth.SetEnforceTenancyForTests(t, false)
	f := seedTwoUserBulk(t)
	spy := newMockBookSearcher()
	f.h.searcher = spy
	rec := postBulkAs(t, f.h.BooksBulk, fmt.Sprintf(`{"ids":[%d],"action":"search"}`, f.b1.ID), f.u1, auth.RoleUser)
	if strings.Contains(rec.Body.String(), "runId") {
		t.Fatalf("no runId expected: %s", rec.Body.String())
	}
	spy.waitForCall(t, 2*time.Second)
}

// Monitoring a wanted book through PUT /book/{id} starts a search; the book in
// the response carries its run id (#3133).
func TestBookUpdate_MonitoringReturnsTheSearchRunID(t *testing.T) {
	store := searchruns.NewStore(10, time.Hour)
	spy := newRunSpy(store, 1)
	h, books, author, ctx := bookFixtureWithSearcher(t, spy)
	h.WithSearchRuns(store)
	book := &models.Book{
		ForeignID: "B-RUN", AuthorID: author.ID, Title: "Run Book", SortTitle: "run book",
		Status: models.BookStatusWanted, MediaType: models.MediaTypeEbook,
		Genres: []string{}, MetadataProvider: "openlibrary", Monitored: false,
	}
	if err := books.Create(ctx, book); err != nil {
		t.Fatal(err)
	}

	req := withURLParam(httptest.NewRequest(http.MethodPut, "/api/v1/book/x", strings.NewReader(`{"monitored":true}`)), "id", strconv.FormatInt(book.ID, 10))
	rec := httptest.NewRecorder()
	h.Update(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("update: %d %s", rec.Code, rec.Body.String())
	}
	var got models.Book
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.SearchRunID == "" {
		t.Fatalf("response must carry searchRunId: %s", rec.Body.String())
	}
	spy.wait(t)
	if spy.seen[book.ID] != got.SearchRunID {
		t.Fatalf("search got run %q, response said %q", spy.seen[book.ID], got.SearchRunID)
	}
}

// A write that starts no search carries no run id.
func TestBookUpdate_NoSearchRunIDWhenNoSearchStarts(t *testing.T) {
	store := searchruns.NewStore(10, time.Hour)
	h, books, author, ctx := bookFixtureWithSearcher(t, newRunSpy(store, 99))
	h.WithSearchRuns(store)
	book := &models.Book{
		ForeignID: "B-RUN2", AuthorID: author.ID, Title: "Quiet Book", SortTitle: "quiet book",
		Status: models.BookStatusWanted, MediaType: models.MediaTypeEbook,
		Genres: []string{}, MetadataProvider: "openlibrary", Monitored: true,
	}
	if err := books.Create(ctx, book); err != nil {
		t.Fatal(err)
	}
	req := withURLParam(httptest.NewRequest(http.MethodPut, "/api/v1/book/x", strings.NewReader(`{"title":"Quiet Book 2"}`)), "id", strconv.FormatInt(book.ID, 10))
	rec := httptest.NewRecorder()
	h.Update(rec, req)
	if rec.Code != http.StatusOK || strings.Contains(rec.Body.String(), "searchRunId") || store.Len() != 0 {
		t.Fatalf("want a plain update, got %d %s (runs=%d)", rec.Code, rec.Body.String(), store.Len())
	}
}

// Search on add registers a run, owned by the book's owner, and the add result
// and the search both carry its id.
func TestAddBookCore_SearchOnAddRegistersARun(t *testing.T) {
	f := newCoreFixture(t, addBookBackCatalogueStub(false))
	store := searchruns.NewStore(10, time.Hour)
	spyRef := newRunSpy(store, 1)
	f.h.searcher = spyRef
	f.h.WithSearchRuns(store)
	p := wellsAddBookParams
	p.SearchOnAdd = true

	res, err := f.h.addBookCore(context.Background(), p)
	if err != nil {
		t.Fatal(err)
	}
	if res.SearchRunID == "" || res.Book.SearchRunID != res.SearchRunID {
		t.Fatalf("result must carry the run id on itself and on the book: %q / %q", res.SearchRunID, res.Book.SearchRunID)
	}
	spyRef.wait(t)
	if spyRef.seen[res.Book.ID] != res.SearchRunID {
		t.Fatalf("search got run %q, result said %q", spyRef.seen[res.Book.ID], res.SearchRunID)
	}
	run, ok := store.Get(res.SearchRunID, 0)
	if !ok || run.BookID != res.Book.ID || run.Origin != "add" {
		t.Fatalf("run = %+v ok=%v, want the added book and origin add", run, ok)
	}
}

func TestAddBookCore_NoSearchOnAddNoRun(t *testing.T) {
	f := newCoreFixture(t, addBookBackCatalogueStub(false))
	store := searchruns.NewStore(10, time.Hour)
	f.h.searcher = newRunSpy(store, 99)
	f.h.WithSearchRuns(store)
	res, err := f.h.addBookCore(context.Background(), wellsAddBookParams)
	if err != nil {
		t.Fatal(err)
	}
	if res.SearchRunID != "" || res.Book.SearchRunID != "" || store.Len() != 0 {
		t.Fatalf("no search, no run: %+v", res)
	}
}

// An approval that searches on add answers with the run id the add made, so a
// companion can poll how that search ended (#3133).
func TestRequestsApprove_ReturnsTheSearchRunID(t *testing.T) {
	adder := &fakeAdder{runID: "feedfacefeedfacefeedfacefeedface"}
	f := newRequestsFixture(t, wellsRequestStub(), adder)
	created := decodeRequest(t, f.create(t, f.requester, `{"kind":"book","foreignId":"OL27482W"}`))

	rec := f.approve(f.admin, created.ID, `{"searchOnAdd":true}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("approve: %d %s", rec.Code, rec.Body.String())
	}
	if got := decodeRequest(t, rec).SearchRunID; got != adder.runID {
		t.Fatalf("searchRunId = %q, want %q (%s)", got, adder.runID, rec.Body.String())
	}
}

func TestRequestsApprove_NoRunIDWhenNothingSearched(t *testing.T) {
	f := newRequestsFixture(t, wellsRequestStub(), &fakeAdder{})
	created := decodeRequest(t, f.create(t, f.requester, `{"kind":"book","foreignId":"OL27482W"}`))
	rec := f.approve(f.admin, created.ID, `{}`)
	if rec.Code != http.StatusOK || strings.Contains(rec.Body.String(), "searchRunId") {
		t.Fatalf("want no searchRunId: %d %s", rec.Code, rec.Body.String())
	}
}
