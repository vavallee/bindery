package api

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"

	"github.com/vavallee/bindery/internal/auth"
	"github.com/vavallee/bindery/internal/db"
	"github.com/vavallee/bindery/internal/models"
)

// The book page lists the series a book was taken out of, and Unlock all
// fields forgets them; any other edit leaves them alone (#2554).
// exclusionsEnv is a book handler with series wired, one book and the
// database, for tests that inject failures with a temporary trigger.
func exclusionsEnv(t *testing.T) (*BookHandler, *db.SeriesRepo, *models.Book, *sql.DB, context.Context) {
	t.Helper()
	database, err := db.OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { database.Close() })
	ctx := context.Background()
	books, series := db.NewBookRepo(database), db.NewSeriesRepo(database)
	author := &models.Author{ForeignID: "OL1A", Name: "Kari Nordmann", SortName: "Nordmann, Kari", MetadataProvider: "openlibrary"}
	if err := db.NewAuthorRepo(database).Create(ctx, author); err != nil {
		t.Fatal(err)
	}
	h := NewBookHandler(books, nil, db.NewHistoryRepo(database), nil).WithSeries(series)
	book := &models.Book{ForeignID: "B1", AuthorID: author.ID, Title: "Fjellvinden", SortTitle: "fjellvinden",
		Status: "wanted", Genres: []string{}, MetadataProvider: "openlibrary"}
	if err := books.Create(ctx, book); err != nil {
		t.Fatal(err)
	}
	return h, series, book, database, ctx
}

func TestBookSeriesExclusionsEndpointAndUnlockAll(t *testing.T) {
	h, series, book, _, ctx := exclusionsEnv(t)
	s := &models.Series{ForeignID: "s:fjell", Title: "Fjellserien"}
	if err := series.CreateOrGet(ctx, s); err != nil {
		t.Fatal(err)
	}
	if err := series.LinkBook(ctx, s.ID, book.ID, "2", true); err != nil {
		t.Fatal(err)
	}
	if _, err := series.RemoveBookFromSeries(ctx, s.ID, book.ID); err != nil {
		t.Fatal(err)
	}
	id := strconv.FormatInt(book.ID, 10)

	list := func() []db.BookSeriesExclusion {
		t.Helper()
		rec := httptest.NewRecorder()
		h.SeriesExclusions(rec, withURLParam(httptest.NewRequest(http.MethodGet, "/api/v1/book/"+id+"/series-exclusions", nil), "id", id))
		if rec.Code != http.StatusOK {
			t.Fatalf("list: %d %s", rec.Code, rec.Body.String())
		}
		var out []db.BookSeriesExclusion
		if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
			t.Fatal(err)
		}
		return out
	}
	update := func(role, body string) {
		t.Helper()
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPut, "/api/v1/book/"+id, bytes.NewBufferString(body))
		h.Update(rec, withURLParam(req.WithContext(auth.WithUserRole(req.Context(), role)), "id", id))
		if rec.Code != http.StatusOK {
			t.Fatalf("update %s: %d %s", body, rec.Code, rec.Body.String())
		}
	}

	if got := list(); len(got) != 1 || got[0].SeriesID != s.ID || got[0].SeriesTitle != "Fjellserien" || got[0].Position != "2" {
		t.Fatalf("exclusions = %+v, want Fjellserien at 2", got)
	}
	update(auth.RoleAdmin, `{"title":"Fjellvinden (ny)"}`)
	if got := list(); len(got) != 1 {
		t.Fatalf("an ordinary edit cleared the exclusions: %+v", got)
	}
	// Series changes are admin only: another user's Unlock all unlocks the
	// fields and leaves the book's series as they are.
	update(auth.RoleUser, `{"lockedFields":[]}`)
	if got := list(); len(got) != 1 {
		t.Fatalf("a non admin's Unlock all cleared the exclusions: %+v", got)
	}
	update(auth.RoleAdmin, `{"lockedFields":[]}`)
	if got := list(); len(got) != 0 {
		t.Errorf("after Unlock all = %+v, want none", got)
	}
}

func TestBookSeriesExclusionsEdges(t *testing.T) {
	h, _, book, _, _ := exclusionsEnv(t)
	get := func(h *BookHandler, id string) *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		h.SeriesExclusions(rec, withURLParam(httptest.NewRequest(http.MethodGet, "/api/v1/book/"+id+"/series-exclusions", nil), "id", id))
		return rec
	}
	if rec := get(h, "999"); rec.Code != http.StatusNotFound {
		t.Errorf("unknown book: %d, want 404", rec.Code)
	}
	// A handler without series wiring has nothing to report.
	bare := NewBookHandler(h.books, nil, nil, nil)
	if rec := get(bare, strconv.FormatInt(book.ID, 10)); rec.Code != http.StatusOK || rec.Body.String() != "[]\n" {
		t.Errorf("no series repo: %d %q, want an empty list", rec.Code, rec.Body.String())
	}
}

// When forgetting the exclusions fails, Unlock all reports it instead of
// answering as if the book had been handed back to refresh.
func TestUnlockAllReportsAFailedExclusionClear(t *testing.T) {
	h, _, book, database, _ := exclusionsEnv(t)
	if _, err := database.Exec(`CREATE TEMP TRIGGER fail_clear BEFORE DELETE ON book_series_exclusions BEGIN SELECT RAISE(ABORT, 'injected'); END`); err != nil {
		t.Fatal(err)
	}
	if _, err := database.Exec(`INSERT INTO book_series_exclusions (book_id, series_foreign_id) VALUES (?, 's:x')`, book.ID); err != nil {
		t.Fatal(err)
	}
	id := strconv.FormatInt(book.ID, 10)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPut, "/api/v1/book/"+id, bytes.NewBufferString(`{"lockedFields":[]}`))
	h.Update(rec, withURLParam(req.WithContext(auth.WithUserRole(req.Context(), auth.RoleAdmin)), "id", id))
	if rec.Code != http.StatusInternalServerError {
		t.Errorf("status = %d %s, want 500", rec.Code, rec.Body.String())
	}
}

// A removal that fails is a 500, and leaves the book in the series.
func TestRemoveBookReportsAFailedRemoval(t *testing.T) {
	_, series, book, database, ctx := exclusionsEnv(t)
	s := &models.Series{ForeignID: "s:fjell", Title: "Fjellserien"}
	if err := series.CreateOrGet(ctx, s); err != nil {
		t.Fatal(err)
	}
	if err := series.LinkBook(ctx, s.ID, book.ID, "1", true); err != nil {
		t.Fatal(err)
	}
	if _, err := database.Exec(`CREATE TEMP TRIGGER fail_unlink BEFORE DELETE ON series_books BEGIN SELECT RAISE(ABORT, 'injected'); END`); err != nil {
		t.Fatal(err)
	}
	sh := NewSeriesHandler(series, db.NewBookRepo(database), db.NewAuthorRepo(database), nil, nil)
	sid, bid := strconv.FormatInt(s.ID, 10), strconv.FormatInt(book.ID, 10)
	rec := httptest.NewRecorder()
	sh.RemoveBook(rec, withURLParams(httptest.NewRequest(http.MethodDelete, "/api/v1/series/"+sid+"/books/"+bid, nil), map[string]string{"id": sid, "bookId": bid}))
	if rec.Code != http.StatusInternalServerError {
		t.Errorf("status = %d, want 500", rec.Code)
	}
	if ids, _ := series.GetSeriesIDsForBook(ctx, book.ID); len(ids) != 1 {
		t.Errorf("series ids = %v, want the book still in its series", ids)
	}
}
