package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"

	"github.com/vavallee/bindery/internal/db"
	"github.com/vavallee/bindery/internal/metadata"
	"github.com/vavallee/bindery/internal/models"
)

// stubLookup is a BookMetaLookup whose behaviour is controlled per-test.
type stubLookup struct {
	book *models.Book
	err  error
}

func (s *stubLookup) GetBookFromProvider(_ context.Context, _, _ string) (*models.Book, error) {
	return s.book, s.err
}

// rebindFixture wires up a BookHandler with in-memory repos, one author, and
// one wanted book, then returns them for use in Rebind tests.
func rebindFixture(t *testing.T) (*BookHandler, *db.BookRepo, *db.AuthorRepo, *db.SeriesRepo, *models.Author, *models.Book, context.Context) {
	t.Helper()
	database, err := db.OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { database.Close() })

	ctx := context.Background()
	books := db.NewBookRepo(database)
	authors := db.NewAuthorRepo(database)
	series := db.NewSeriesRepo(database)
	history := db.NewHistoryRepo(database)
	editions := db.NewEditionRepo(database)

	author := &models.Author{
		ForeignID: "OL1A", Name: "Test Author", SortName: "Author, Test",
		MetadataProvider: "openlibrary", Monitored: true,
	}
	if err := authors.Create(ctx, author); err != nil {
		t.Fatal(err)
	}

	book := &models.Book{
		ForeignID: "OL1W", AuthorID: author.ID, Title: "Wrong Book",
		SortTitle: "Wrong Book", Status: models.BookStatusWanted,
		MetadataProvider: "openlibrary", Monitored: true, Genres: []string{},
	}
	if err := books.Create(ctx, book); err != nil {
		t.Fatal(err)
	}

	h := NewBookHandler(books, nil, history, nil).WithAuthors(authors).WithSeries(series).WithEditionHydration(editions)
	return h, books, authors, series, author, book, ctx
}

func rebindRequest(bookID int64, body any) *http.Request {
	b, _ := json.Marshal(body)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/book/"+strconv.FormatInt(bookID, 10)+"/rebind", bytes.NewReader(b))
	return withURLParam(req, "id", strconv.FormatInt(bookID, 10))
}

func TestRebind_HappyPath(t *testing.T) {
	h, books, _, _, author, book, ctx := rebindFixture(t)
	h.WithMetaLookup(&stubLookup{book: &models.Book{
		Title: "Correct Book", SortTitle: "Correct Book",
		Description: "The real one.", Genres: []string{"Fantasy"},
		Author: &models.Author{ForeignID: author.ForeignID, Name: author.Name},
	}})

	rec := httptest.NewRecorder()
	h.Rebind(rec, rebindRequest(book.ID, map[string]any{
		"provider": "openlibrary", "foreign_id": "OL2W",
	}))

	if rec.Code != http.StatusOK {
		t.Fatalf("want 200, got %d: %s", rec.Code, rec.Body.String())
	}
	updated, _ := books.GetByID(ctx, book.ID)
	if updated.ForeignID != "OL2W" {
		t.Errorf("foreign_id not updated: got %q", updated.ForeignID)
	}
	if updated.Title != "Correct Book" {
		t.Errorf("title not updated: got %q", updated.Title)
	}
}

func TestRebind_HydratesHardcoverEditions(t *testing.T) {
	h, books, _, _, author, book, ctx := rebindFixture(t)
	book.MediaType = models.MediaTypeAudiobook
	if err := books.Update(ctx, book); err != nil {
		t.Fatal(err)
	}
	audioASIN := "B123456789"
	h.WithMetaLookup(&stubLookup{book: &models.Book{
		Title:  "Correct Book",
		Author: &models.Author{ForeignID: author.ForeignID, Name: author.Name},
	}})
	h.WithEditionFetcher(func(context.Context, string) ([]models.Edition, error) {
		return []models.Edition{{
			ForeignID: "hc:correct-audio",
			Title:     "Correct Book",
			ASIN:      &audioASIN,
			Format:    "Audiobook",
			Monitored: true,
		}}, nil
	})

	rec := httptest.NewRecorder()
	h.Rebind(rec, rebindRequest(book.ID, map[string]any{
		"provider": "hardcover", "foreign_id": "123",
	}))
	if rec.Code != http.StatusOK {
		t.Fatalf("want 200, got %d: %s", rec.Code, rec.Body.String())
	}
	updated, err := books.GetByID(ctx, book.ID)
	if err != nil {
		t.Fatal(err)
	}
	if updated.ASIN != audioASIN {
		t.Fatalf("ASIN = %q, want %q", updated.ASIN, audioASIN)
	}
	editions, err := h.editions.ListByBook(ctx, book.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(editions) != 1 || editions[0].ForeignID != "hc:correct-audio" {
		t.Fatalf("expected hydrated edition, got %+v", editions)
	}
}

func TestMapMetadata_HydratesHardcoverEditions(t *testing.T) {
	h, books, _, _, author, book, ctx := rebindFixture(t)
	book.MediaType = models.MediaTypeAudiobook
	if err := books.Update(ctx, book); err != nil {
		t.Fatal(err)
	}
	audioASIN := "B987654321"
	provider := &stubMetaProvider{
		name: "hardcover",
		getBookByID: map[string]*models.Book{
			"hc:mapped": {
				ForeignID:        "hc:mapped",
				Title:            "Mapped Book",
				SortTitle:        "Mapped Book",
				MetadataProvider: "hardcover",
				Author:           &models.Author{ForeignID: author.ForeignID, Name: author.Name},
			},
		},
		editionsByBook: map[string][]models.Edition{
			"hc:mapped": {{
				ForeignID: "hc:mapped-audio",
				Title:     "Mapped Book",
				ASIN:      &audioASIN,
				Format:    "Audiobook",
				Monitored: true,
			}},
		},
	}
	h.meta = metadata.NewAggregator(provider).WithAudnexClient(nil)

	body := bytes.NewBufferString(`{"foreignBookId":"hc:mapped"}`)
	rec := httptest.NewRecorder()
	h.MapMetadata(rec, withURLParam(httptest.NewRequest(http.MethodPost, "/api/v1/book/1/map-metadata", body), "id", strconv.FormatInt(book.ID, 10)))
	if rec.Code != http.StatusOK {
		t.Fatalf("want 200, got %d: %s", rec.Code, rec.Body.String())
	}
	updated, err := books.GetByID(ctx, book.ID)
	if err != nil {
		t.Fatal(err)
	}
	if updated.ASIN != audioASIN {
		t.Fatalf("ASIN = %q, want %q", updated.ASIN, audioASIN)
	}
	editions, err := h.editions.ListByBook(ctx, book.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(editions) != 1 || editions[0].ForeignID != "hc:mapped-audio" {
		t.Fatalf("expected hydrated edition, got %+v", editions)
	}
}

func TestRebind_BookNotFound(t *testing.T) {
	h, _, _, _, _, _, _ := rebindFixture(t)
	h.WithMetaLookup(&stubLookup{book: &models.Book{Title: "X"}})

	rec := httptest.NewRecorder()
	h.Rebind(rec, rebindRequest(99999, map[string]any{"provider": "openlibrary", "foreign_id": "OL2W"}))
	if rec.Code != http.StatusNotFound {
		t.Errorf("want 404, got %d", rec.Code)
	}
}

func TestRebind_InvalidProvider(t *testing.T) {
	h, _, _, _, _, book, _ := rebindFixture(t)
	h.WithMetaLookup(&stubLookup{book: &models.Book{Title: "X"}})

	rec := httptest.NewRecorder()
	h.Rebind(rec, rebindRequest(book.ID, map[string]any{"provider": "goodreads", "foreign_id": "123"}))
	if rec.Code != http.StatusBadRequest {
		t.Errorf("want 400, got %d", rec.Code)
	}
}

func TestRebind_MissingForeignID(t *testing.T) {
	h, _, _, _, _, book, _ := rebindFixture(t)
	h.WithMetaLookup(&stubLookup{book: &models.Book{Title: "X"}})

	rec := httptest.NewRecorder()
	h.Rebind(rec, rebindRequest(book.ID, map[string]any{"provider": "openlibrary", "foreign_id": "   "}))
	if rec.Code != http.StatusBadRequest {
		t.Errorf("want 400, got %d", rec.Code)
	}
}

func TestRebind_NilAggregator_Returns424(t *testing.T) {
	h, _, _, _, _, book, _ := rebindFixture(t)
	// h.lookup is nil — no WithMetaLookup call

	rec := httptest.NewRecorder()
	h.Rebind(rec, rebindRequest(book.ID, map[string]any{"provider": "openlibrary", "foreign_id": "OL2W"}))
	if rec.Code != http.StatusFailedDependency {
		t.Errorf("want 424, got %d", rec.Code)
	}
}

func TestRebind_ProviderError_Returns502(t *testing.T) {
	h, _, _, _, _, book, _ := rebindFixture(t)
	h.WithMetaLookup(&stubLookup{err: errors.New("upstream timeout")})

	rec := httptest.NewRecorder()
	h.Rebind(rec, rebindRequest(book.ID, map[string]any{"provider": "openlibrary", "foreign_id": "OL2W"}))
	if rec.Code != http.StatusBadGateway {
		t.Errorf("want 502, got %d", rec.Code)
	}
}

func TestRebind_AuthorMismatch_Rejected(t *testing.T) {
	h, _, _, _, _, book, _ := rebindFixture(t)
	h.WithMetaLookup(&stubLookup{book: &models.Book{
		Title:  "Different Author's Book",
		Author: &models.Author{ForeignID: "OL_OTHER_A", Name: "Someone Else"},
	}})

	rec := httptest.NewRecorder()
	h.Rebind(rec, rebindRequest(book.ID, map[string]any{
		"provider": "openlibrary", "foreign_id": "OL3W", "force": false,
	}))
	if rec.Code != http.StatusConflict {
		t.Fatalf("want 409 on author mismatch, got %d: %s", rec.Code, rec.Body.String())
	}
	var body map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &body)
	if body["force_required"] != true {
		t.Errorf("expected force_required=true in body: %v", body)
	}
}

func TestRebind_AuthorMismatch_ForceOverrides(t *testing.T) {
	h, _, _, _, _, book, _ := rebindFixture(t)
	h.WithMetaLookup(&stubLookup{book: &models.Book{
		Title:  "Different Author's Book",
		Author: &models.Author{ForeignID: "OL_OTHER_A", Name: "Someone Else"},
	}})

	rec := httptest.NewRecorder()
	h.Rebind(rec, rebindRequest(book.ID, map[string]any{
		"provider": "openlibrary", "foreign_id": "OL3W", "force": true,
	}))
	if rec.Code != http.StatusOK {
		t.Errorf("want 200 with force=true, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestRebind_SeriesRelinked(t *testing.T) {
	h, _, _, series, author, book, ctx := rebindFixture(t)

	// Seed an existing series link that should be cleared on rebind.
	oldSeries := &models.Series{ForeignID: "SER_OLD", Title: "Old Series"}
	if err := series.CreateOrGet(ctx, oldSeries); err != nil {
		t.Fatal(err)
	}
	if err := series.LinkBook(ctx, oldSeries.ID, book.ID, "1", true); err != nil {
		t.Fatal(err)
	}

	h.WithMetaLookup(&stubLookup{book: &models.Book{
		Title:  "New Title",
		Author: &models.Author{ForeignID: author.ForeignID},
		SeriesRefs: []models.SeriesRef{
			{ForeignID: "SER_NEW", Title: "New Series", Position: "2", Primary: true},
		},
	}})

	rec := httptest.NewRecorder()
	h.Rebind(rec, rebindRequest(book.ID, map[string]any{"provider": "openlibrary", "foreign_id": "OL4W"}))
	if rec.Code != http.StatusOK {
		t.Fatalf("want 200, got %d: %s", rec.Code, rec.Body.String())
	}

	oldIDs, _ := series.GetSeriesIDsForBook(ctx, book.ID)
	for _, id := range oldIDs {
		if id == oldSeries.ID {
			t.Errorf("old series link %d should have been removed", oldSeries.ID)
		}
	}

	title, pos, err := series.GetPrimarySeriesForBook(ctx, book.ID)
	if err != nil || title != "New Series" || pos != "2" {
		t.Errorf("new series not linked correctly: title=%q pos=%q err=%v", title, pos, err)
	}
}

func TestRebind_HistoryEventWritten(t *testing.T) {
	database, err := db.OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { database.Close() })
	ctx := context.Background()

	books := db.NewBookRepo(database)
	authors := db.NewAuthorRepo(database)
	series := db.NewSeriesRepo(database)
	history := db.NewHistoryRepo(database)

	author := &models.Author{
		ForeignID: "OL1A", Name: "Author", SortName: "Author",
		MetadataProvider: "openlibrary", Monitored: true,
	}
	if err := authors.Create(ctx, author); err != nil {
		t.Fatal(err)
	}
	book := &models.Book{
		ForeignID: "OL1W", AuthorID: author.ID, Title: "Old Title",
		SortTitle: "Old Title", Status: models.BookStatusWanted,
		MetadataProvider: "openlibrary", Monitored: true, Genres: []string{},
	}
	if err := books.Create(ctx, book); err != nil {
		t.Fatal(err)
	}

	h := NewBookHandler(books, nil, history, nil).WithAuthors(authors).WithSeries(series)
	h.WithMetaLookup(&stubLookup{book: &models.Book{
		Title:  "New Title",
		Author: &models.Author{ForeignID: author.ForeignID},
	}})

	rec := httptest.NewRecorder()
	h.Rebind(rec, rebindRequest(book.ID, map[string]any{"provider": "openlibrary", "foreign_id": "OL5W"}))
	if rec.Code != http.StatusOK {
		t.Fatalf("want 200, got %d: %s", rec.Code, rec.Body.String())
	}

	events, err := history.ListByBook(ctx, book.ID)
	if err != nil {
		t.Fatal(err)
	}
	var found bool
	for _, e := range events {
		if e.EventType == models.HistoryEventBookRebound {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("expected a %q history event, got %v", models.HistoryEventBookRebound, events)
	}
}
