package api

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/vavallee/bindery/internal/db"
	"github.com/vavallee/bindery/internal/models"
)

// bulkFixture spins up in-memory storage with one author and a configurable
// set of books, returns a wired BulkHandler plus the underlying repos.
func bulkFixture(t *testing.T) (*BulkHandler, *db.AuthorRepo, *db.BookRepo, *models.Author, context.Context) {
	t.Helper()
	database, err := db.OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { database.Close() })

	authors := db.NewAuthorRepo(database)
	books := db.NewBookRepo(database)
	blocklist := db.NewBlocklistRepo(database)

	ctx := context.Background()
	author := &models.Author{
		ForeignID: "OL_BULK_A", Name: "Bulk Author", SortName: "Author, Bulk",
		MetadataProvider: "openlibrary", Monitored: true,
	}
	if err := authors.Create(ctx, author); err != nil {
		t.Fatal(err)
	}

	h := NewBulkHandler(authors, books, blocklist, nil)
	return h, authors, books, author, ctx
}

// bulkFixtureWithSearcher is like bulkFixture but wires a mock searcher.
func bulkFixtureWithSearcher(t *testing.T, searcher BookSearcher) (*BulkHandler, *db.AuthorRepo, *db.BookRepo, *models.Author, context.Context) {
	t.Helper()
	h, authors, books, author, ctx := bulkFixture(t)
	// Replace nil searcher with the provided one.
	h.searcher = searcher
	return h, authors, books, author, ctx
}

func mustCreateBook(t *testing.T, books *db.BookRepo, ctx context.Context, b *models.Book) *models.Book {
	t.Helper()
	if err := books.Create(ctx, b); err != nil {
		t.Fatalf("create book %q: %v", b.Title, err)
	}
	return b
}

func postBulk(t *testing.T, h http.HandlerFunc, body string) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/", bytes.NewBufferString(body))
	h(rec, req)
	return rec
}

// ---------------------------------------------------------------------------
// AuthorsBulk
// ---------------------------------------------------------------------------

func TestAuthorsBulk_Monitor(t *testing.T) {
	h, authors, _, author, ctx := bulkFixture(t)

	// Start unmonitored.
	author.Monitored = false
	if err := authors.Update(ctx, author); err != nil {
		t.Fatal(err)
	}

	body := fmt.Sprintf(`{"ids":[%d],"action":"monitor"}`, author.ID)
	rec := postBulk(t, h.AuthorsBulk, body)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	var resp bulkResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	key := fmt.Sprintf("%d", author.ID)
	if r := resp.Results[key]; !r.OK {
		t.Errorf("expected ok for author %d, got %+v", author.ID, r)
	}

	got, _ := authors.GetByID(ctx, author.ID)
	if !got.Monitored {
		t.Error("author should be monitored after bulk monitor")
	}
}

func TestAuthorsBulk_Unmonitor(t *testing.T) {
	h, authors, _, author, ctx := bulkFixture(t)

	body := fmt.Sprintf(`{"ids":[%d],"action":"unmonitor"}`, author.ID)
	rec := postBulk(t, h.AuthorsBulk, body)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	got, _ := authors.GetByID(ctx, author.ID)
	if got.Monitored {
		t.Error("author should be unmonitored after bulk unmonitor")
	}
}

func TestAuthorsBulk_Delete(t *testing.T) {
	h, authors, _, author, ctx := bulkFixture(t)

	body := fmt.Sprintf(`{"ids":[%d],"action":"delete"}`, author.ID)
	rec := postBulk(t, h.AuthorsBulk, body)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	got, _ := authors.GetByID(ctx, author.ID)
	if got != nil {
		t.Error("author should be deleted")
	}
}

func TestAuthorsBulk_SetMediaType(t *testing.T) {
	h, _, books, author, ctx := bulkFixture(t)

	// Two books under this author, both currently ebook.
	mustCreateBook(t, books, ctx, &models.Book{
		ForeignID: "OL_A", AuthorID: author.ID, Title: "A",
		SortTitle: "a", MediaType: models.MediaTypeEbook,
		Genres: []string{}, MetadataProvider: "openlibrary",
	})
	mustCreateBook(t, books, ctx, &models.Book{
		ForeignID: "OL_B", AuthorID: author.ID, Title: "B",
		SortTitle: "b", MediaType: models.MediaTypeEbook,
		Genres: []string{}, MetadataProvider: "openlibrary",
	})

	body := fmt.Sprintf(`{"ids":[%d],"action":"set_media_type","mediaType":"audiobook"}`, author.ID)
	rec := postBulk(t, h.AuthorsBulk, body)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	got, _ := books.ListByAuthor(ctx, author.ID)
	if len(got) != 2 {
		t.Fatalf("expected 2 books, got %d", len(got))
	}
	for _, b := range got {
		if b.MediaType != models.MediaTypeAudiobook {
			t.Errorf("book %q: expected audiobook, got %q", b.Title, b.MediaType)
		}
	}
}

// Switching an author's imported ebook catalogue to audiobook must re-flag
// the books as wanted so they reappear on the Wanted page — otherwise the
// user sees "imported" rows with no audiobook on disk. Mirrors the
// DeleteFile handler's "back to wanted if any wanted format is missing".
func TestAuthorsBulk_SetMediaType_ReevaluatesToWanted(t *testing.T) {
	h, _, books, author, ctx := bulkFixture(t)

	// Imported ebook: has an ebook on disk, no audiobook.
	imported := mustCreateBook(t, books, ctx, &models.Book{
		ForeignID: "OL_IMP", AuthorID: author.ID, Title: "On Disk",
		SortTitle: "on disk", Status: models.BookStatusImported,
		MediaType: models.MediaTypeEbook,
		Genres:    []string{}, MetadataProvider: "openlibrary", Monitored: true,
	})
	// Per-format paths are set via Update, not Create.
	imported.EbookFilePath = "/library/on-disk.epub"
	imported.FilePath = "/library/on-disk.epub"
	if err := books.Update(ctx, imported); err != nil {
		t.Fatal(err)
	}

	body := fmt.Sprintf(`{"ids":[%d],"action":"set_media_type","mediaType":"audiobook"}`, author.ID)
	rec := postBulk(t, h.AuthorsBulk, body)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	got, _ := books.GetByID(ctx, imported.ID)
	if got.MediaType != models.MediaTypeAudiobook {
		t.Errorf("mediaType: want audiobook, got %q", got.MediaType)
	}
	if got.Status != models.BookStatusWanted {
		t.Errorf("status: want wanted after ebook→audiobook flip (no audiobook on disk), got %q", got.Status)
	}
}

// Opposite direction: a wanted book whose existing on-disk file satisfies
// the new media type should flip back to imported.
func TestAuthorsBulk_SetMediaType_ReevaluatesToImported(t *testing.T) {
	h, _, books, author, ctx := bulkFixture(t)

	wanted := mustCreateBook(t, books, ctx, &models.Book{
		ForeignID: "OL_WANT", AuthorID: author.ID, Title: "Have Audio",
		SortTitle: "have audio", Status: models.BookStatusWanted,
		MediaType: models.MediaTypeEbook,
		Genres:    []string{}, MetadataProvider: "openlibrary", Monitored: true,
	})
	wanted.AudiobookFilePath = "/library/have-audio.m4b"
	if err := books.Update(ctx, wanted); err != nil {
		t.Fatal(err)
	}

	body := fmt.Sprintf(`{"ids":[%d],"action":"set_media_type","mediaType":"audiobook"}`, author.ID)
	rec := postBulk(t, h.AuthorsBulk, body)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	got, _ := books.GetByID(ctx, wanted.ID)
	if got.Status != models.BookStatusImported {
		t.Errorf("status: want imported after ebook→audiobook flip (audiobook already on disk), got %q", got.Status)
	}
}

// Skipped and mid-pipeline books must survive a media-type flip unchanged —
// skipped encodes an explicit user decision, and disturbing a downloading
// book would duplicate work the download client is already doing.
func TestAuthorsBulk_SetMediaType_PreservesSkippedAndInFlight(t *testing.T) {
	h, _, books, author, ctx := bulkFixture(t)

	skipped := mustCreateBook(t, books, ctx, &models.Book{
		ForeignID: "OL_SKIP", AuthorID: author.ID, Title: "Skipped",
		SortTitle: "skipped", Status: models.BookStatusSkipped,
		MediaType: models.MediaTypeEbook,
		Genres:    []string{}, MetadataProvider: "openlibrary",
	})
	downloading := mustCreateBook(t, books, ctx, &models.Book{
		ForeignID: "OL_DL", AuthorID: author.ID, Title: "Downloading",
		SortTitle: "downloading", Status: models.BookStatusDownloading,
		MediaType: models.MediaTypeEbook,
		Genres:    []string{}, MetadataProvider: "openlibrary", Monitored: true,
	})

	body := fmt.Sprintf(`{"ids":[%d],"action":"set_media_type","mediaType":"audiobook"}`, author.ID)
	rec := postBulk(t, h.AuthorsBulk, body)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	gotSkipped, _ := books.GetByID(ctx, skipped.ID)
	if gotSkipped.Status != models.BookStatusSkipped {
		t.Errorf("skipped: want status preserved, got %q", gotSkipped.Status)
	}
	gotDL, _ := books.GetByID(ctx, downloading.ID)
	if gotDL.Status != models.BookStatusDownloading {
		t.Errorf("downloading: want status preserved, got %q", gotDL.Status)
	}
}

func TestAuthorsBulk_SetMediaType_Invalid(t *testing.T) {
	h, _, _, author, _ := bulkFixture(t)
	body := fmt.Sprintf(`{"ids":[%d],"action":"set_media_type","mediaType":"videogame"}`, author.ID)
	rec := postBulk(t, h.AuthorsBulk, body)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", rec.Code)
	}
}

func TestAuthorsBulk_Search_FiresSearcherForWantedBooks(t *testing.T) {
	searcher := newMockBookSearcher()
	h, _, books, author, ctx := bulkFixtureWithSearcher(t, searcher)

	// One monitored wanted book, one unmonitored wanted book, and one imported
	// book; only the monitored wanted book should be searched.
	mustCreateBook(t, books, ctx, &models.Book{
		ForeignID: "OL_BK1", AuthorID: author.ID, Title: "Wanted Book",
		SortTitle: "wanted book", Status: models.BookStatusWanted,
		Genres: []string{}, MetadataProvider: "openlibrary", Monitored: true,
	})
	mustCreateBook(t, books, ctx, &models.Book{
		ForeignID: "OL_BK2", AuthorID: author.ID, Title: "Unmonitored Wanted Book",
		SortTitle: "unmonitored wanted book", Status: models.BookStatusWanted,
		Genres: []string{}, MetadataProvider: "openlibrary", Monitored: false,
	})
	mustCreateBook(t, books, ctx, &models.Book{
		ForeignID: "OL_BK3", AuthorID: author.ID, Title: "Imported Book",
		SortTitle: "imported book", Status: models.BookStatusImported,
		Genres: []string{}, MetadataProvider: "openlibrary", Monitored: true,
	})

	body := fmt.Sprintf(`{"ids":[%d],"action":"search"}`, author.ID)
	rec := postBulk(t, h.AuthorsBulk, body)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	// Search runs in a goroutine; wait for the single expected call.
	got := searcher.waitForCall(t, time.Second)
	if got.Title != "Wanted Book" {
		t.Errorf("expected search for 'Wanted Book', got %q", got.Title)
	}
	// Imported and unmonitored wanted books must not trigger a search.
	searcher.assertNoCall(t, 50*time.Millisecond)
}

func TestAuthorsBulk_UnknownAction(t *testing.T) {
	h, _, _, author, _ := bulkFixture(t)
	body := fmt.Sprintf(`{"ids":[%d],"action":"nuke"}`, author.ID)
	rec := postBulk(t, h.AuthorsBulk, body)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("expected 400 for unknown action, got %d", rec.Code)
	}
}

func TestAuthorsBulk_EmptyIDs(t *testing.T) {
	h, _, _, _, _ := bulkFixture(t)
	rec := postBulk(t, h.AuthorsBulk, `{"ids":[],"action":"monitor"}`)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("expected 400 for empty ids, got %d", rec.Code)
	}
}

// TestAuthorsBulk_PartialFailure is the 5-IDs-1-bad scenario called out in the
// issue. Four valid authors + one non-existent ID: we expect 200 with four
// ok:true entries and one ok:false entry, not a hard 4xx abort.
func TestAuthorsBulk_PartialFailure(t *testing.T) {
	h, authors, _, _, ctx := bulkFixture(t)

	// Create four more authors.
	var ids []int64
	for i := 0; i < 4; i++ {
		a := &models.Author{
			ForeignID:        fmt.Sprintf("OL_PF%d", i),
			Name:             fmt.Sprintf("Author %d", i),
			SortName:         fmt.Sprintf("Author, %d", i),
			MetadataProvider: "openlibrary",
			Monitored:        true,
		}
		if err := authors.Create(ctx, a); err != nil {
			t.Fatal(err)
		}
		ids = append(ids, a.ID)
	}
	// Fifth ID is intentionally bogus.
	const badID = int64(999999)
	ids = append(ids, badID)

	idsJSON, _ := json.Marshal(ids)
	body := fmt.Sprintf(`{"ids":%s,"action":"unmonitor"}`, idsJSON)
	rec := postBulk(t, h.AuthorsBulk, body)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 even with partial failure, got %d: %s", rec.Code, rec.Body.String())
	}
	var resp bulkResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if len(resp.Results) != 5 {
		t.Fatalf("expected 5 result entries, got %d", len(resp.Results))
	}

	okCount, errCount := 0, 0
	for _, r := range resp.Results {
		if r.OK {
			okCount++
		} else {
			errCount++
		}
	}
	if okCount != 4 {
		t.Errorf("expected 4 ok results, got %d", okCount)
	}
	if errCount != 1 {
		t.Errorf("expected 1 error result, got %d", errCount)
	}

	badKey := fmt.Sprintf("%d", badID)
	if r := resp.Results[badKey]; r.OK || r.Error == "" {
		t.Errorf("expected error entry for bad id %d, got %+v", badID, r)
	}
}

// ---------------------------------------------------------------------------
// BooksBulk
// ---------------------------------------------------------------------------

func TestBooksBulk_Monitor(t *testing.T) {
	h, _, books, author, ctx := bulkFixture(t)

	book := mustCreateBook(t, books, ctx, &models.Book{
		ForeignID: "B_MON", AuthorID: author.ID, Title: "Monitor Me",
		SortTitle: "monitor me", Status: models.BookStatusWanted,
		Genres: []string{}, MetadataProvider: "openlibrary", Monitored: false,
	})

	body := fmt.Sprintf(`{"ids":[%d],"action":"monitor"}`, book.ID)
	rec := postBulk(t, h.BooksBulk, body)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	got, _ := books.GetByID(ctx, book.ID)
	if !got.Monitored {
		t.Error("book should be monitored")
	}
}

func TestBooksBulk_Unmonitor(t *testing.T) {
	h, _, books, author, ctx := bulkFixture(t)

	book := mustCreateBook(t, books, ctx, &models.Book{
		ForeignID: "B_UNM", AuthorID: author.ID, Title: "Unmonitor Me",
		SortTitle: "unmonitor me", Status: models.BookStatusWanted,
		Genres: []string{}, MetadataProvider: "openlibrary", Monitored: true,
	})

	body := fmt.Sprintf(`{"ids":[%d],"action":"unmonitor"}`, book.ID)
	rec := postBulk(t, h.BooksBulk, body)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	got, _ := books.GetByID(ctx, book.ID)
	if got.Monitored {
		t.Error("book should be unmonitored")
	}
}

func TestBooksBulk_Delete(t *testing.T) {
	h, _, books, author, ctx := bulkFixture(t)

	book := mustCreateBook(t, books, ctx, &models.Book{
		ForeignID: "B_DEL", AuthorID: author.ID, Title: "Delete Me",
		SortTitle: "delete me", Status: models.BookStatusWanted,
		Genres: []string{}, MetadataProvider: "openlibrary", Monitored: true,
	})

	body := fmt.Sprintf(`{"ids":[%d],"action":"delete"}`, book.ID)
	rec := postBulk(t, h.BooksBulk, body)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	got, _ := books.GetByID(ctx, book.ID)
	if got != nil {
		t.Error("book should be deleted")
	}
}

// TestBooksBulk_Exclude covers the bulk "exclude" action added for #791:
// the Author detail page lets users mass-exclude unwanted OL imports without
// clicking through each book's exclude toggle.
func TestBooksBulk_Exclude(t *testing.T) {
	h, _, books, author, ctx := bulkFixture(t)

	book := mustCreateBook(t, books, ctx, &models.Book{
		ForeignID: "B_EXC", AuthorID: author.ID, Title: "Exclude Me",
		SortTitle: "exclude me", Status: models.BookStatusWanted,
		Genres: []string{}, MetadataProvider: "openlibrary", Monitored: true,
	})

	body := fmt.Sprintf(`{"ids":[%d],"action":"exclude"}`, book.ID)
	rec := postBulk(t, h.BooksBulk, body)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	var resp bulkResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	key := fmt.Sprintf("%d", book.ID)
	if r := resp.Results[key]; !r.OK {
		t.Errorf("expected ok for book %d, got %+v", book.ID, r)
	}

	// SetExcluded hides the row from default List queries; use the
	// IncludingExcluded variant to confirm the flag round-trips.
	all, err := books.ListByAuthorIncludingExcluded(ctx, author.ID)
	if err != nil {
		t.Fatalf("ListByAuthorIncludingExcluded: %v", err)
	}
	var found *models.Book
	for i := range all {
		if all[i].ID == book.ID {
			found = &all[i]
			break
		}
	}
	if found == nil {
		t.Fatalf("book %d missing from ListByAuthorIncludingExcluded", book.ID)
		return
	}
	if !found.Excluded {
		t.Errorf("book.Excluded: want true after bulk exclude, got false")
	}
}

func TestBooksBulk_SetMediaType_Ebook(t *testing.T) {
	h, _, books, author, ctx := bulkFixture(t)

	book := mustCreateBook(t, books, ctx, &models.Book{
		ForeignID: "B_MT", AuthorID: author.ID, Title: "Media Type Book",
		SortTitle: "media type book", Status: models.BookStatusWanted,
		MediaType: models.MediaTypeAudiobook,
		Genres:    []string{}, MetadataProvider: "openlibrary", Monitored: true,
	})

	body := fmt.Sprintf(`{"ids":[%d],"action":"set_media_type","mediaType":"ebook"}`, book.ID)
	rec := postBulk(t, h.BooksBulk, body)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	got, _ := books.GetByID(ctx, book.ID)
	if got.MediaType != models.MediaTypeEbook {
		t.Errorf("expected ebook, got %q", got.MediaType)
	}
}

func TestBooksBulk_SetMediaType_Invalid(t *testing.T) {
	h, _, books, author, ctx := bulkFixture(t)

	book := mustCreateBook(t, books, ctx, &models.Book{
		ForeignID: "B_MT2", AuthorID: author.ID, Title: "Bad Type",
		SortTitle: "bad type", Status: models.BookStatusWanted,
		Genres: []string{}, MetadataProvider: "openlibrary", Monitored: true,
	})

	body := fmt.Sprintf(`{"ids":[%d],"action":"set_media_type","mediaType":"videogame"}`, book.ID)
	rec := postBulk(t, h.BooksBulk, body)

	if rec.Code != http.StatusBadRequest {
		t.Errorf("expected 400 for invalid media type, got %d", rec.Code)
	}
}

func TestBooksBulk_Search_FiresSearcher(t *testing.T) {
	searcher := newMockBookSearcher()
	h, _, books, author, ctx := bulkFixtureWithSearcher(t, searcher)

	book := mustCreateBook(t, books, ctx, &models.Book{
		ForeignID: "B_SRCH", AuthorID: author.ID, Title: "Search Me",
		SortTitle: "search me", Status: models.BookStatusWanted,
		Genres: []string{}, MetadataProvider: "openlibrary", Monitored: true,
	})

	body := fmt.Sprintf(`{"ids":[%d],"action":"search"}`, book.ID)
	rec := postBulk(t, h.BooksBulk, body)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	got := searcher.waitForCall(t, time.Second)
	if got.ID != book.ID {
		t.Errorf("searcher called with wrong book id: got %d, want %d", got.ID, book.ID)
	}
}

func TestBooksBulk_UnknownAction(t *testing.T) {
	h, _, books, author, ctx := bulkFixture(t)
	book := mustCreateBook(t, books, ctx, &models.Book{
		ForeignID: "B_UNK", AuthorID: author.ID, Title: "Unknown Action",
		SortTitle: "unknown action", Status: models.BookStatusWanted,
		Genres: []string{}, MetadataProvider: "openlibrary", Monitored: true,
	})
	body := fmt.Sprintf(`{"ids":[%d],"action":"explode"}`, book.ID)
	rec := postBulk(t, h.BooksBulk, body)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("expected 400, got %d", rec.Code)
	}
}

func TestBooksBulk_PartialFailure(t *testing.T) {
	h, _, books, author, ctx := bulkFixture(t)

	// Four valid books.
	var ids []int64
	for i := 0; i < 4; i++ {
		b := mustCreateBook(t, books, ctx, &models.Book{
			ForeignID:        fmt.Sprintf("B_PF%d", i),
			AuthorID:         author.ID,
			Title:            fmt.Sprintf("Book %d", i),
			SortTitle:        fmt.Sprintf("book %d", i),
			Status:           models.BookStatusWanted,
			Genres:           []string{},
			MetadataProvider: "openlibrary",
			Monitored:        true,
		})
		ids = append(ids, b.ID)
	}
	// Fifth ID does not exist.
	const badID = int64(888888)
	ids = append(ids, badID)

	idsJSON, _ := json.Marshal(ids)
	body := fmt.Sprintf(`{"ids":%s,"action":"unmonitor"}`, idsJSON)
	rec := postBulk(t, h.BooksBulk, body)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 for partial failure, got %d: %s", rec.Code, rec.Body.String())
	}
	var resp bulkResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if len(resp.Results) != 5 {
		t.Fatalf("expected 5 result entries, got %d", len(resp.Results))
	}
	okCount, errCount := 0, 0
	for _, r := range resp.Results {
		if r.OK {
			okCount++
		} else {
			errCount++
		}
	}
	if okCount != 4 || errCount != 1 {
		t.Errorf("expected 4 ok + 1 error, got %d ok + %d error", okCount, errCount)
	}
}

// ---------------------------------------------------------------------------
// WantedBulk
// ---------------------------------------------------------------------------

func TestWantedBulk_Unmonitor(t *testing.T) {
	h, _, books, author, ctx := bulkFixture(t)

	book := mustCreateBook(t, books, ctx, &models.Book{
		ForeignID: "W_UNM", AuthorID: author.ID, Title: "Unmonitor Wanted",
		SortTitle: "unmonitor wanted", Status: models.BookStatusWanted,
		Genres: []string{}, MetadataProvider: "openlibrary", Monitored: true,
	})

	body := fmt.Sprintf(`{"ids":[%d],"action":"unmonitor"}`, book.ID)
	rec := postBulk(t, h.WantedBulk, body)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	got, _ := books.GetByID(ctx, book.ID)
	if got.Monitored {
		t.Error("book should be unmonitored")
	}
}

func TestWantedBulk_Blocklist(t *testing.T) {
	h, _, books, author, ctx := bulkFixture(t)

	book := mustCreateBook(t, books, ctx, &models.Book{
		ForeignID: "W_BL", AuthorID: author.ID, Title: "Blocklist Wanted",
		SortTitle: "blocklist wanted", Status: models.BookStatusWanted,
		Genres: []string{}, MetadataProvider: "openlibrary", Monitored: true,
	})

	body := fmt.Sprintf(`{"ids":[%d],"action":"blocklist"}`, book.ID)
	rec := postBulk(t, h.WantedBulk, body)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	got, _ := books.GetByID(ctx, book.ID)
	if got.Monitored {
		t.Error("blocklisted book should be unmonitored")
	}
	if got.Status != models.BookStatusSkipped {
		t.Errorf("blocklisted book should have status skipped, got %q", got.Status)
	}
}

func TestWantedBulk_Search_FiresSearcher(t *testing.T) {
	searcher := newMockBookSearcher()
	h, _, books, author, ctx := bulkFixtureWithSearcher(t, searcher)

	book := mustCreateBook(t, books, ctx, &models.Book{
		ForeignID: "W_SRCH", AuthorID: author.ID, Title: "Search Wanted",
		SortTitle: "search wanted", Status: models.BookStatusWanted,
		Genres: []string{}, MetadataProvider: "openlibrary", Monitored: true,
	})

	body := fmt.Sprintf(`{"ids":[%d],"action":"search"}`, book.ID)
	rec := postBulk(t, h.WantedBulk, body)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	got := searcher.waitForCall(t, time.Second)
	if got.ID != book.ID {
		t.Errorf("searcher called with wrong book id: got %d, want %d", got.ID, book.ID)
	}
}

func TestWantedBulk_UnknownAction(t *testing.T) {
	h, _, books, author, ctx := bulkFixture(t)
	book := mustCreateBook(t, books, ctx, &models.Book{
		ForeignID: "W_UNK", AuthorID: author.ID, Title: "Unknown",
		SortTitle: "unknown", Status: models.BookStatusWanted,
		Genres: []string{}, MetadataProvider: "openlibrary", Monitored: true,
	})
	body := fmt.Sprintf(`{"ids":[%d],"action":"orbit"}`, book.ID)
	rec := postBulk(t, h.WantedBulk, body)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("expected 400, got %d", rec.Code)
	}
}

func TestWantedBulk_PartialFailure(t *testing.T) {
	h, _, books, author, ctx := bulkFixture(t)

	var ids []int64
	for i := 0; i < 4; i++ {
		b := mustCreateBook(t, books, ctx, &models.Book{
			ForeignID:        fmt.Sprintf("W_PF%d", i),
			AuthorID:         author.ID,
			Title:            fmt.Sprintf("Wanted %d", i),
			SortTitle:        fmt.Sprintf("wanted %d", i),
			Status:           models.BookStatusWanted,
			Genres:           []string{},
			MetadataProvider: "openlibrary",
			Monitored:        true,
		})
		ids = append(ids, b.ID)
	}
	const badID = int64(777777)
	ids = append(ids, badID)

	idsJSON, _ := json.Marshal(ids)
	body := fmt.Sprintf(`{"ids":%s,"action":"blocklist"}`, idsJSON)
	rec := postBulk(t, h.WantedBulk, body)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	var resp bulkResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if len(resp.Results) != 5 {
		t.Fatalf("expected 5 results, got %d", len(resp.Results))
	}
	okCount, errCount := 0, 0
	for _, r := range resp.Results {
		if r.OK {
			okCount++
		} else {
			errCount++
		}
	}
	if okCount != 4 || errCount != 1 {
		t.Errorf("expected 4 ok + 1 error, got %d ok + %d error", okCount, errCount)
	}
}

// boundedMockSearcher tracks the maximum number of concurrent
// SearchAndGrabBook calls so bulk-fan-out tests can assert the bound is
// enforced. Each call holds the slot for releaseAfter so the pool fills
// before any work finishes; without that, fast workers could complete
// before the next batch is launched and the observed concurrency would
// always look low regardless of the cap.
type boundedMockSearcher struct {
	active       int32
	maxActive    int32
	callCount    int32
	releaseAfter time.Duration
	done         chan struct{}
	target       int32
	once         sync.Once
}

func newBoundedMockSearcher(expected int) *boundedMockSearcher {
	return &boundedMockSearcher{
		releaseAfter: 30 * time.Millisecond,
		done:         make(chan struct{}),
		target:       int32(expected),
	}
}

func (m *boundedMockSearcher) SearchAndGrabBook(_ context.Context, _ models.Book) {
	now := atomic.AddInt32(&m.active, 1)
	for {
		prev := atomic.LoadInt32(&m.maxActive)
		if now <= prev || atomic.CompareAndSwapInt32(&m.maxActive, prev, now) {
			break
		}
	}
	time.Sleep(m.releaseAfter)
	atomic.AddInt32(&m.active, -1)
	if atomic.AddInt32(&m.callCount, 1) >= m.target {
		m.once.Do(func() { close(m.done) })
	}
}

// TestAuthorsBulk_Search_BoundsConcurrency is the Wave 3 / I regression
// guard: a single bulk "search" action over many wanted books must not
// fan out one indexer goroutine per book. The cap is bulkSearchConcurrency
// (8); we use 32 books and assert the observed in-flight count never
// exceeds it.
func TestAuthorsBulk_Search_BoundsConcurrency(t *testing.T) {
	const total = 32
	searcher := newBoundedMockSearcher(total)
	h, _, books, author, ctx := bulkFixtureWithSearcher(t, searcher)

	for i := 0; i < total; i++ {
		mustCreateBook(t, books, ctx, &models.Book{
			ForeignID:        fmt.Sprintf("OL_BULKCONC_%d", i),
			AuthorID:         author.ID,
			Title:            fmt.Sprintf("Wanted %d", i),
			SortTitle:        fmt.Sprintf("wanted %d", i),
			Status:           models.BookStatusWanted,
			Monitored:        true,
			MetadataProvider: "openlibrary",
			Genres:           []string{},
		})
	}

	body := fmt.Sprintf(`{"ids":[%d],"action":"search"}`, author.ID)
	rec := postBulk(t, h.AuthorsBulk, body)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}

	select {
	case <-searcher.done:
	case <-time.After(5 * time.Second):
		t.Fatalf("timeout waiting for bounded fan-out; calls=%d max=%d",
			atomic.LoadInt32(&searcher.callCount), atomic.LoadInt32(&searcher.maxActive))
	}

	if got := atomic.LoadInt32(&searcher.maxActive); got > bulkSearchConcurrency {
		t.Fatalf("max concurrent searches = %d, want <= %d", got, bulkSearchConcurrency)
	}
	if got := atomic.LoadInt32(&searcher.callCount); got != total {
		t.Fatalf("call count = %d, want %d", got, total)
	}
}
