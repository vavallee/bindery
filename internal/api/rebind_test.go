package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/vavallee/bindery/internal/db"
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

// TestRebind_KeepsAPinnedMediaType is the #2768 regression for the rebind path.
// Rebind preserves media_type as a user-managed field, so hydration must not
// widen an ebook the user chose to "both" just because the new record lists an
// audio edition. Before the fix this handler never forwarded the pin, so the
// row came back "both" carrying the audiobook's ASIN — the exact shape #1732
// and #1802 were filed about.
func TestRebind_KeepsAPinnedMediaType(t *testing.T) {
	h, books, _, _, author, book, ctx := rebindFixture(t)
	book.MediaType = models.MediaTypeEbook
	if err := books.Update(ctx, book); err != nil {
		t.Fatal(err)
	}
	audioASIN := "B2768REBIND"
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
	if updated.MediaType != models.MediaTypeEbook {
		t.Fatalf("MediaType = %q, want ebook (a rebind must not widen the user's format)", updated.MediaType)
	}
	if updated.ASIN != "" {
		t.Fatalf("ASIN = %q, want empty on an ebook-pinned book", updated.ASIN)
	}
	// Hydration still ran, so the media type holding is the pin and not a
	// skipped hydration.
	editions, err := h.editions.ListByBook(ctx, book.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(editions) != 1 || editions[0].ForeignID != "hc:correct-audio" {
		t.Fatalf("expected hydrated edition, got %+v", editions)
	}
}

// editingLookup runs edit while the provider request is in flight, committing
// a change to the book between Rebind's read and its write (#2926).
type editingLookup struct {
	book *models.Book
	edit func()
}

func (s *editingLookup) GetBookFromProvider(_ context.Context, _, _ string) (*models.Book, error) {
	s.edit()
	return s.book, nil
}

// TestRebind_ConflictWhenBookChangesDuringFetch: the full row rebind write
// must not overwrite a change made during the provider call. It reports a
// conflict and leaves the row as the concurrent writer left it.
func TestRebind_ConflictWhenBookChangesDuringFetch(t *testing.T) {
	h, books, _, _, author, book, ctx := rebindFixture(t)
	h.WithMetaLookup(&editingLookup{
		book: &models.Book{
			Title: "Correct Book", SortTitle: "Correct Book",
			Author: &models.Author{ForeignID: author.ForeignID, Name: author.Name},
		},
		edit: func() {
			current, err := books.GetByID(ctx, book.ID)
			if err != nil || current == nil {
				t.Errorf("load book for concurrent edit: %v", err)
				return
			}
			current.Monitored = false
			if err := books.Update(ctx, current); err != nil {
				t.Errorf("concurrent edit: %v", err)
			}
		},
	})

	rec := httptest.NewRecorder()
	h.Rebind(rec, rebindRequest(book.ID, map[string]any{
		"provider": "openlibrary", "foreign_id": "OL2W",
	}))

	if rec.Code != http.StatusConflict {
		t.Fatalf("want 409, got %d: %s", rec.Code, rec.Body.String())
	}
	// The UI tells this apart from the duplicate foreign ID 409 by reason.
	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil || body["reason"] != "changed" {
		t.Fatalf("409 body = %s, want reason \"changed\"", rec.Body.String())
	}
	stored, err := books.GetByID(ctx, book.ID)
	if err != nil || stored == nil {
		t.Fatalf("reload book: %v", err)
	}
	if stored.Monitored || stored.ForeignID != "OL1W" || stored.Title != "Wrong Book" {
		t.Fatalf("concurrent edit overwritten: monitored=%v foreignId=%q title=%q", stored.Monitored, stored.ForeignID, stored.Title)
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

// rebindFromRichRecord gives the fixture book a full set of provider fields,
// so a rebind to a record without them shows what is cleared (#3104).
func rebindFromRichRecord(t *testing.T, h *BookHandler, books *db.BookRepo, book *models.Book, locked ...string) {
	t.Helper()
	d := time.Date(2001, 5, 6, 0, 0, 0, 0, time.UTC)
	book.ReleaseDate = &d
	book.Language = "eng"
	book.Description = "Old description"
	book.Genres = []string{"Old"}
	book.OriginalTitle = "Old original"
	book.AverageRating = 4.5
	book.RatingsCount = 10
	book.ImageURL = "https://example.test/old.jpg"
	book.Narrator = "Kept Narrator"
	book.LockedFields = locked
	if err := books.Update(context.Background(), book); err != nil {
		t.Fatal(err)
	}
}

func TestRebind_ClearsFieldsTheNewRecordLacks(t *testing.T) {
	for _, force := range []bool{false, true} {
		t.Run("force="+strconv.FormatBool(force), func(t *testing.T) {
			h, books, _, _, author, book, ctx := rebindFixture(t)
			rebindFromRichRecord(t, h, books, book)
			h.WithMetaLookup(&stubLookup{book: &models.Book{
				Title: "Correct Book", SortTitle: "Correct Book",
				Author: &models.Author{ForeignID: author.ForeignID, Name: author.Name},
			}})
			rec := httptest.NewRecorder()
			h.Rebind(rec, rebindRequest(book.ID, map[string]any{"provider": "openlibrary", "foreign_id": "OL2W", "force": force}))
			if rec.Code != http.StatusOK {
				t.Fatalf("want 200, got %d: %s", rec.Code, rec.Body.String())
			}
			got, _ := books.GetByID(ctx, book.ID)
			if got.ReleaseDate != nil {
				t.Errorf("release date kept: %v", got.ReleaseDate)
			}
			if got.Language != "" || got.Description != "" || len(got.Genres) != 0 {
				t.Errorf("provider fields kept: %+v", got)
			}
			if got.AverageRating != 4.5 || got.OriginalTitle != "Old original" {
				t.Errorf("rating or original title cleared: %v %q", got.AverageRating, got.OriginalTitle)
			}
			var body struct {
				Cleared []string `json:"cleared"`
			}
			_ = json.Unmarshal(rec.Body.Bytes(), &body)
			if len(body.Cleared) != 4 {
				t.Errorf("response cleared = %v, want the four fields", body.Cleared)
			}
			ev, _ := h.history.ListByBook(ctx, book.ID)
			if len(ev) == 0 || !strings.Contains(ev[0].Data, `"cleared"`) {
				t.Errorf("history event lacks cleared: %+v", ev)
			}
			if got.ImageURL != "https://example.test/old.jpg" || got.Narrator != "Kept Narrator" {
				t.Errorf("a non-listed field was cleared: image=%q narrator=%q", got.ImageURL, got.Narrator)
			}
		})
	}
}

func TestRebind_LockedFieldSurvivesAMissingValue(t *testing.T) {
	h, books, _, _, author, book, ctx := rebindFixture(t)
	rebindFromRichRecord(t, h, books, book, models.BookFieldReleaseDate, models.BookFieldDescription)
	h.WithMetaLookup(&stubLookup{book: &models.Book{
		Title: "Correct Book", SortTitle: "Correct Book",
		Author: &models.Author{ForeignID: author.ForeignID, Name: author.Name},
	}})
	rec := httptest.NewRecorder()
	h.Rebind(rec, rebindRequest(book.ID, map[string]any{"provider": "openlibrary", "foreign_id": "OL2W"}))
	if rec.Code != http.StatusOK {
		t.Fatalf("want 200, got %d: %s", rec.Code, rec.Body.String())
	}
	got, _ := books.GetByID(ctx, book.ID)
	if got.ReleaseDate == nil || got.Description != "Old description" {
		t.Errorf("locked fields were cleared: date=%v desc=%q", got.ReleaseDate, got.Description)
	}
	if got.Language != "" {
		t.Errorf("unlocked language kept: %q", got.Language)
	}
}

func TestRebind_NewRecordValueStillWinsOverALock(t *testing.T) {
	h, books, _, _, author, book, ctx := rebindFixture(t)
	rebindFromRichRecord(t, h, books, book, models.BookFieldDescription)
	h.WithMetaLookup(&stubLookup{book: &models.Book{
		Title: "Correct Book", SortTitle: "Correct Book", Description: "New description",
		Author: &models.Author{ForeignID: author.ForeignID, Name: author.Name},
	}})
	rec := httptest.NewRecorder()
	h.Rebind(rec, rebindRequest(book.ID, map[string]any{"provider": "openlibrary", "foreign_id": "OL2W"}))
	got, _ := books.GetByID(ctx, book.ID)
	if got.Description != "New description" {
		t.Errorf("description = %q, the existing rebind semantics adopt the new value", got.Description)
	}
}

// A record whose secondary lookup failed says so in UnknownFields; the rebind
// must keep the old values rather than read the gap as "the record lacks it"
// (#3104).
func TestRebind_KeepsFieldsTheFetchCouldNotDetermine(t *testing.T) {
	h, books, _, _, author, book, ctx := rebindFixture(t)
	rebindFromRichRecord(t, h, books, book)
	h.WithMetaLookup(&stubLookup{book: &models.Book{
		Title: "Correct Book", SortTitle: "Correct Book",
		UnknownFields: []string{models.BookFieldReleaseDate, models.BookFieldLanguage},
		Author:        &models.Author{ForeignID: author.ForeignID, Name: author.Name},
	}})
	rec := httptest.NewRecorder()
	h.Rebind(rec, rebindRequest(book.ID, map[string]any{"provider": "openlibrary", "foreign_id": "OL2W"}))
	if rec.Code != http.StatusOK {
		t.Fatalf("want 200, got %d: %s", rec.Code, rec.Body.String())
	}
	got, _ := books.GetByID(ctx, book.ID)
	if got.ReleaseDate == nil || got.Language != "eng" {
		t.Errorf("undetermined fields were erased: date=%v language=%q", got.ReleaseDate, got.Language)
	}
	if got.Description != "" {
		t.Errorf("description is determined absent and should clear, got %q", got.Description)
	}
}
