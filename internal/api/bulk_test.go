package api

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/vavallee/bindery/internal/auth"
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
	series := db.NewSeriesRepo(database)
	blocklist := db.NewBlocklistRepo(database)

	ctx := context.Background()
	author := &models.Author{
		ForeignID: "OL_BULK_A", Name: "Bulk Author", SortName: "Author, Bulk",
		MetadataProvider: "openlibrary", Monitored: true,
	}
	if err := authors.Create(ctx, author); err != nil {
		t.Fatal(err)
	}

	h := NewBulkHandler(authors, books, blocklist, nil).WithSeriesRepo(series)
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

func TestAuthorsBulk_SetMonitorMode(t *testing.T) {
	h, authors, _, author, ctx := bulkFixture(t)

	second := &models.Author{
		ForeignID:        "OL_BULK_MODE_2",
		Name:             "Second Author",
		SortName:         "Author, Second",
		MetadataProvider: "openlibrary",
		Monitored:        true,
	}
	if err := authors.Create(ctx, second); err != nil {
		t.Fatal(err)
	}

	body := fmt.Sprintf(`{"ids":[%d,%d],"action":"set_monitor_mode","monitorMode":"none"}`, author.ID, second.ID)
	rec := postBulk(t, h.AuthorsBulk, body)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	var resp bulkResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	for _, id := range []int64{author.ID, second.ID} {
		key := fmt.Sprintf("%d", id)
		if r := resp.Results[key]; !r.OK {
			t.Errorf("author %d: expected ok, got %+v", id, r)
		}
		got, _ := authors.GetByID(ctx, id)
		if got.MonitorMode != models.AuthorMonitorModeNone {
			t.Errorf("author %d: monitor mode = %q, want none", id, got.MonitorMode)
		}
	}
}

func TestAuthorsBulk_SetMonitorModeLatest_AppliesToExistingBooks(t *testing.T) {
	h, authors, books, author, ctx := bulkFixture(t)

	oldDate := time.Date(2020, time.January, 1, 0, 0, 0, 0, time.UTC)
	midDate := time.Date(2022, time.January, 1, 0, 0, 0, 0, time.UTC)
	newDate := time.Date(2024, time.January, 1, 0, 0, 0, 0, time.UTC)
	excludedDate := time.Date(2025, time.January, 1, 0, 0, 0, 0, time.UTC)

	old := mustCreateBook(t, books, ctx, &models.Book{
		ForeignID: "OL_MODE_OLD", AuthorID: author.ID, Title: "Old Book",
		SortTitle: "old book", ReleaseDate: &oldDate, Status: models.BookStatusWanted,
		Genres: []string{}, MetadataProvider: "openlibrary", Monitored: true,
	})
	mid := mustCreateBook(t, books, ctx, &models.Book{
		ForeignID: "OL_MODE_MID", AuthorID: author.ID, Title: "Middle Book",
		SortTitle: "middle book", ReleaseDate: &midDate, Status: models.BookStatusWanted,
		Genres: []string{}, MetadataProvider: "openlibrary", Monitored: false,
	})
	newest := mustCreateBook(t, books, ctx, &models.Book{
		ForeignID: "OL_MODE_NEW", AuthorID: author.ID, Title: "Newest Book",
		SortTitle: "newest book", ReleaseDate: &newDate, Status: models.BookStatusWanted,
		Genres: []string{}, MetadataProvider: "openlibrary", Monitored: false,
	})
	excluded := mustCreateBook(t, books, ctx, &models.Book{
		ForeignID: "OL_MODE_EXCLUDED", AuthorID: author.ID, Title: "Excluded Future Book",
		SortTitle: "excluded future book", ReleaseDate: &excludedDate, Status: models.BookStatusWanted,
		Genres: []string{}, MetadataProvider: "openlibrary", Monitored: true,
	})
	if err := books.SetExcluded(ctx, excluded.ID, true); err != nil {
		t.Fatal(err)
	}

	body := fmt.Sprintf(`{"ids":[%d],"action":"set_monitor_mode","monitorMode":"latest","monitorLatestCount":2,"applyMonitorModeToExisting":true}`, author.ID)
	rec := postBulk(t, h.AuthorsBulk, body)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	gotAuthor, _ := authors.GetByID(ctx, author.ID)
	if gotAuthor.MonitorMode != models.AuthorMonitorModeLatest || gotAuthor.MonitorLatestCount != 2 {
		t.Fatalf("author monitor options = %q/%d, want latest/2", gotAuthor.MonitorMode, gotAuthor.MonitorLatestCount)
	}
	cases := []struct {
		book *models.Book
		want bool
	}{
		{old, false},
		{mid, true},
		{newest, true},
		{excluded, false},
	}
	for _, tc := range cases {
		got, _ := books.GetByID(ctx, tc.book.ID)
		if got.Monitored != tc.want {
			t.Errorf("%s monitored = %v, want %v", got.Title, got.Monitored, tc.want)
		}
	}
}

func TestAuthorsBulk_SetMonitorMode_DoesNotApplyExistingWhenDisabled(t *testing.T) {
	h, authors, books, author, ctx := bulkFixture(t)

	book := mustCreateBook(t, books, ctx, &models.Book{
		ForeignID: "OL_MODE_KEEP", AuthorID: author.ID, Title: "Keep Monitored",
		SortTitle: "keep monitored", Status: models.BookStatusWanted,
		Genres: []string{}, MetadataProvider: "openlibrary", Monitored: true,
	})

	body := fmt.Sprintf(`{"ids":[%d],"action":"set_monitor_mode","monitorMode":"none","applyMonitorModeToExisting":false}`, author.ID)
	rec := postBulk(t, h.AuthorsBulk, body)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	gotAuthor, _ := authors.GetByID(ctx, author.ID)
	if gotAuthor.MonitorMode != models.AuthorMonitorModeNone {
		t.Fatalf("author monitor mode = %q, want none", gotAuthor.MonitorMode)
	}
	gotBook, _ := books.GetByID(ctx, book.ID)
	if !gotBook.Monitored {
		t.Error("book should stay monitored when applyMonitorModeToExisting is false")
	}
}

func TestAuthorsBulk_SetMonitorMode_Invalid(t *testing.T) {
	h, _, _, author, _ := bulkFixture(t)
	cases := []struct {
		name string
		body string
	}{
		{
			name: "unknown mode",
			body: fmt.Sprintf(`{"ids":[%d],"action":"set_monitor_mode","monitorMode":"yesterday"}`, author.ID),
		},
		{
			name: "series mode",
			body: fmt.Sprintf(`{"ids":[%d],"action":"set_monitor_mode","monitorMode":"series"}`, author.ID),
		},
		{
			name: "non-positive latest count",
			body: fmt.Sprintf(`{"ids":[%d],"action":"set_monitor_mode","monitorMode":"latest","monitorLatestCount":0}`, author.ID),
		},
		{
			name: "missing latest count",
			body: fmt.Sprintf(`{"ids":[%d],"action":"set_monitor_mode","monitorMode":"latest"}`, author.ID),
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec := postBulk(t, h.AuthorsBulk, tc.body)
			if rec.Code != http.StatusBadRequest {
				t.Fatalf("expected 400, got %d: %s", rec.Code, rec.Body.String())
			}
		})
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

// TestAuthorsBulk_Refresh_FiresFetchPerAuthor covers the bulk "refresh" action:
// for each selected author id it must invoke the injected catalogue-fetch
// callback exactly once (the same metadata-only fetch the per-author Refresh
// endpoint runs — never an auto-grab). This is the mass-recovery path for
// authors imported with empty catalogues.
func TestAuthorsBulk_Refresh_FiresFetchPerAuthor(t *testing.T) {
	h, authors, _, author, ctx := bulkFixture(t)

	// Two more authors so we exercise the per-id fan-out.
	var ids []int64
	ids = append(ids, author.ID)
	for i := 0; i < 2; i++ {
		a := &models.Author{
			ForeignID:        fmt.Sprintf("OL_REFRESH_%d", i),
			Name:             fmt.Sprintf("Refresh Author %d", i),
			SortName:         fmt.Sprintf("Author, Refresh %d", i),
			MetadataProvider: "openlibrary",
			Monitored:        true,
		}
		if err := authors.Create(ctx, a); err != nil {
			t.Fatal(err)
		}
		ids = append(ids, a.ID)
	}

	var wg sync.WaitGroup
	wg.Add(len(ids))
	var mu sync.Mutex
	seen := map[int64]int{}
	h.WithRefreshFunc(func(a *models.Author) {
		mu.Lock()
		seen[a.ID]++
		mu.Unlock()
		wg.Done()
	})

	idsJSON, _ := json.Marshal(ids)
	body := fmt.Sprintf(`{"ids":%s,"action":"refresh"}`, idsJSON)
	rec := postBulk(t, h.AuthorsBulk, body)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	var resp bulkResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	for _, id := range ids {
		key := fmt.Sprintf("%d", id)
		if r := resp.Results[key]; !r.OK {
			t.Errorf("expected ok for author %d, got %+v", id, r)
		}
	}

	// Fan-out runs in a goroutine; wait for all callbacks.
	done := make(chan struct{})
	go func() { wg.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("refresh callback never fired for all authors")
	}

	mu.Lock()
	defer mu.Unlock()
	if len(seen) != len(ids) {
		t.Fatalf("refresh fired for %d distinct authors, want %d", len(seen), len(ids))
	}
	for _, id := range ids {
		if seen[id] != 1 {
			t.Errorf("author %d refreshed %d times, want 1", id, seen[id])
		}
	}
}

// When no refresh callback is wired the action must be rejected rather than
// silently succeeding (so the UI surfaces a real error instead of a no-op).
func TestAuthorsBulk_Refresh_RejectedWhenUnwired(t *testing.T) {
	h, _, _, author, _ := bulkFixture(t)
	body := fmt.Sprintf(`{"ids":[%d],"action":"refresh"}`, author.ID)
	rec := postBulk(t, h.AuthorsBulk, body)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("expected 400 when refresh func unwired, got %d", rec.Code)
	}
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

// ctxCapturingSearcher records the per-call context the bulk fan-out passes
// in so #846 regression tests can assert the lifetime ctx is propagated.
// The first call publishes its ctx and signals seen; later calls overwrite
// the field but never re-close the channel (close-once via sync.Once).
type ctxCapturingSearcher struct {
	mu      sync.Mutex
	gotCtx  context.Context
	seen    chan struct{}
	seenOne sync.Once
}

func newCtxCapturingSearcher() *ctxCapturingSearcher {
	return &ctxCapturingSearcher{seen: make(chan struct{})}
}

func (s *ctxCapturingSearcher) SearchAndGrabBook(ctx context.Context, _ models.Book) {
	s.mu.Lock()
	s.gotCtx = ctx
	s.mu.Unlock()
	s.seenOne.Do(func() { close(s.seen) })
	// Block until the test cancels the lifetime ctx so the goroutine is
	// observably alive when the assertion runs. A bare receive on ctx.Done
	// is the production contract we are guarding: the searcher must observe
	// cancellation rather than running on context.Background().
	<-ctx.Done()
}

// TestBulkHandler_GoroutineCancelsOnLifetimeCtxCancel is the #846 regression
// guard for BulkHandler. The bulk-action search fan-out must derive from the
// process-lifecycle ctx passed via WithLifetimeCtx; cancelling that ctx must
// be observed inside the spawned goroutine. Without the fix the goroutine
// would run on context.Background() and never see Done.
func TestBulkHandler_GoroutineCancelsOnLifetimeCtxCancel(t *testing.T) {
	searcher := newCtxCapturingSearcher()
	h, _, books, author, ctx := bulkFixtureWithSearcher(t, searcher)

	lifetimeCtx, cancel := context.WithCancel(context.Background())
	defer cancel()
	h.WithLifetimeCtx(lifetimeCtx)

	book := mustCreateBook(t, books, ctx, &models.Book{
		ForeignID: "BLK_LIFE", AuthorID: author.ID, Title: "Lifetime Ctx",
		SortTitle: "lifetime ctx", Status: models.BookStatusWanted,
		Genres: []string{}, MetadataProvider: "openlibrary", Monitored: true,
	})

	body := fmt.Sprintf(`{"ids":[%d],"action":"search"}`, book.ID)
	rec := postBulk(t, h.BooksBulk, body)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}

	// Wait until the searcher actually starts (so we know fan-out spawned).
	select {
	case <-searcher.seen:
	case <-time.After(2 * time.Second):
		t.Fatal("searcher was not invoked within 2s")
	}

	// Cancel the lifetime ctx; the goroutine, parked on ctx.Done, must
	// unblock. If the bulk handler had used context.Background() the
	// receive below would never complete.
	cancel()

	searcher.mu.Lock()
	gotCtx := searcher.gotCtx
	searcher.mu.Unlock()
	select {
	case <-gotCtx.Done():
		// good
	case <-time.After(2 * time.Second):
		t.Fatal("spawned goroutine did not observe lifetime ctx cancellation")
	}
}

// ---------------------------------------------------------------------------
// Per-user ownership / IDOR regression suite (#947)
//
// Bulk endpoints are registered at the authenticated user level (not admin),
// so a non-admin can legitimately bulk-act on their OWN resources. The bug:
// the handlers mutated purely by id, so under BINDERY_ENFORCE_TENANCY a
// non-admin could pass another user's ids and delete/unmonitor/exclude/skip
// them. These tests assert the durable invariant (the victim row is untouched)
// rather than just the per-id error string. Mirrors the gate/admin/gate-off
// matrix in authorization_test.go.
// ---------------------------------------------------------------------------

type bulkAuthzFixture struct {
	database *sql.DB
	h        *BulkHandler
	authors  *db.AuthorRepo
	books    *db.BookRepo
	u1, u2   int64
	// Alice's (u1) resources.
	a1 *models.Author
	b1 *models.Book
	// Bob's (u2) resources.
	a2 *models.Author
	b2 *models.Book
}

// seedTwoUserBulk builds two users, each owning one author and one book. Book
// ownership is stamped by hand because BookRepo.Create does not yet write
// owner_user_id (same approach authorization_test.go's setOwner uses).
func seedTwoUserBulk(t *testing.T) bulkAuthzFixture {
	t.Helper()
	database, err := db.OpenMemory()
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { _ = database.Close() })

	users := db.NewUserRepo(database)
	authors := db.NewAuthorRepo(database)
	books := db.NewBookRepo(database)
	series := db.NewSeriesRepo(database)
	blocklist := db.NewBlocklistRepo(database)

	ctx := context.Background()
	u1, err := users.Create(ctx, "alice", "hash1")
	if err != nil {
		t.Fatalf("create alice: %v", err)
	}
	u2, err := users.Create(ctx, "bob", "hash2")
	if err != nil {
		t.Fatalf("create bob: %v", err)
	}

	a1 := &models.Author{ForeignID: "OL-BA1", Name: "Alice Author", SortName: "Author, Alice", MetadataProvider: "openlibrary", Monitored: true}
	if err := authors.CreateForUser(ctx, a1, u1.ID); err != nil {
		t.Fatalf("create alice author: %v", err)
	}
	a2 := &models.Author{ForeignID: "OL-BA2", Name: "Bob Author", SortName: "Author, Bob", MetadataProvider: "openlibrary", Monitored: true}
	if err := authors.CreateForUser(ctx, a2, u2.ID); err != nil {
		t.Fatalf("create bob author: %v", err)
	}

	b1 := mustCreateBook(t, books, ctx, &models.Book{
		ForeignID: "OL-BB1", AuthorID: a1.ID, Title: "Alice Book", SortTitle: "alice book",
		Status: models.BookStatusWanted, MediaType: models.MediaTypeEbook,
		Genres: []string{}, MetadataProvider: "openlibrary", Monitored: true,
	})
	b2 := mustCreateBook(t, books, ctx, &models.Book{
		ForeignID: "OL-BB2", AuthorID: a2.ID, Title: "Bob Book", SortTitle: "bob book",
		Status: models.BookStatusWanted, MediaType: models.MediaTypeEbook,
		Genres: []string{}, MetadataProvider: "openlibrary", Monitored: true,
	})
	setOwner(t, database, "books", b1.ID, u1.ID)
	setOwner(t, database, "books", b2.ID, u2.ID)

	// Re-read so the owner column lands on the structs the handler reads.
	a1, _ = authors.GetByID(ctx, a1.ID)
	a2, _ = authors.GetByID(ctx, a2.ID)
	b1, _ = books.GetByID(ctx, b1.ID)
	b2, _ = books.GetByID(ctx, b2.ID)

	h := NewBulkHandler(authors, books, blocklist, nil).WithSeriesRepo(series).WithRefreshFunc(func(*models.Author) {})

	return bulkAuthzFixture{
		database: database, h: h, authors: authors, books: books,
		u1: u1.ID, u2: u2.ID, a1: a1, b1: b1, a2: a2, b2: b2,
	}
}

// postBulkAs posts a bulk request whose context carries the given identity.
func postBulkAs(t *testing.T, handler http.HandlerFunc, body string, userID int64, role string) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/", bytes.NewBufferString(body))
	req = req.WithContext(withAuthCtx(req.Context(), userID, role))
	handler(rec, req)
	return rec
}

// resultOK reports the ok flag for a single id in a bulk response.
func resultOK(t *testing.T, rec *httptest.ResponseRecorder, id int64) bool {
	t.Helper()
	var resp bulkResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode bulk response: %v (body=%s)", err, rec.Body.String())
	}
	return resp.Results[fmt.Sprintf("%d", id)].OK
}

// TestBulk_Author_OwnershipMatrix exercises AuthorsBulk delete across the
// gate-on/non-owner, gate-on/owner, gate-on/admin, and gate-off axes. The
// durable invariant is whether the targeted author still exists afterwards.
func TestBulk_Author_OwnershipMatrix(t *testing.T) {
	cases := []struct {
		name        string
		gateOn      bool
		callerIsBob bool // false => Alice (the owner)
		admin       bool
		wantDeleted bool // did the victim author get deleted?
		wantOK      bool // per-id ok flag
	}{
		{"gate on, cross-user blocked", true, true, false, false, false},
		{"gate on, owner allowed", true, false, false, true, true},
		{"gate on, admin allowed", true, true, true, true, true},
		{"gate off, cross-user allowed", false, true, false, true, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			auth.SetEnforceTenancyForTests(t, tc.gateOn)
			f := seedTwoUserBulk(t)

			caller := f.u1
			role := "user"
			if tc.callerIsBob {
				caller = f.u2
			}
			if tc.admin {
				caller = 99
				role = "admin"
			}

			// Always target Alice's author (a1).
			body := fmt.Sprintf(`{"ids":[%d],"action":"delete"}`, f.a1.ID)
			rec := postBulkAs(t, f.h.AuthorsBulk, body, caller, role)
			if rec.Code != http.StatusOK {
				t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
			}
			if got := resultOK(t, rec, f.a1.ID); got != tc.wantOK {
				t.Errorf("per-id ok=%v, want %v", got, tc.wantOK)
			}
			got, _ := f.authors.GetByID(context.Background(), f.a1.ID)
			deleted := got == nil
			if deleted != tc.wantDeleted {
				t.Errorf("author deleted=%v, want %v (durable invariant)", deleted, tc.wantDeleted)
			}
		})
	}
}

func TestBulk_AuthorSetMonitorMode_RejectsCrossUser(t *testing.T) {
	auth.SetEnforceTenancyForTests(t, true)
	f := seedTwoUserBulk(t)

	body := fmt.Sprintf(`{"ids":[%d],"action":"set_monitor_mode","monitorMode":"none","applyMonitorModeToExisting":true}`, f.a1.ID)
	rec := postBulkAs(t, f.h.AuthorsBulk, body, f.u2, "user")
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	if ok := resultOK(t, rec, f.a1.ID); ok {
		t.Error("cross-user monitor-mode update must be rejected per item")
	}

	ctx := context.Background()
	gotAuthor, _ := f.authors.GetByID(ctx, f.a1.ID)
	if gotAuthor.MonitorMode == models.AuthorMonitorModeNone {
		t.Error("victim author monitor mode must be unchanged")
	}
	gotBook, _ := f.books.GetByID(ctx, f.b1.ID)
	if !gotBook.Monitored {
		t.Error("victim author books must be unchanged")
	}
}

// TestBulk_Book_OwnershipMatrix exercises BooksBulk unmonitor across the same
// axes. The durable invariant is whether Alice's book stays monitored.
func TestBulk_Book_OwnershipMatrix(t *testing.T) {
	cases := []struct {
		name          string
		gateOn        bool
		callerIsBob   bool
		admin         bool
		wantUnmonitor bool
		wantOK        bool
	}{
		{"gate on, cross-user blocked", true, true, false, false, false},
		{"gate on, owner allowed", true, false, false, true, true},
		{"gate on, admin allowed", true, true, true, true, true},
		{"gate off, cross-user allowed", false, true, false, true, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			auth.SetEnforceTenancyForTests(t, tc.gateOn)
			f := seedTwoUserBulk(t)

			caller := f.u1
			role := "user"
			if tc.callerIsBob {
				caller = f.u2
			}
			if tc.admin {
				caller = 99
				role = "admin"
			}

			body := fmt.Sprintf(`{"ids":[%d],"action":"unmonitor"}`, f.b1.ID)
			rec := postBulkAs(t, f.h.BooksBulk, body, caller, role)
			if rec.Code != http.StatusOK {
				t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
			}
			if got := resultOK(t, rec, f.b1.ID); got != tc.wantOK {
				t.Errorf("per-id ok=%v, want %v", got, tc.wantOK)
			}
			got, _ := f.books.GetByID(context.Background(), f.b1.ID)
			unmonitored := !got.Monitored
			if unmonitored != tc.wantUnmonitor {
				t.Errorf("book unmonitored=%v, want %v (durable invariant)", unmonitored, tc.wantUnmonitor)
			}
		})
	}
}

// TestBulk_Wanted_OwnershipMatrix exercises WantedBulk blocklist across the
// same axes. The durable invariant is whether Alice's book got skipped.
func TestBulk_Wanted_OwnershipMatrix(t *testing.T) {
	cases := []struct {
		name        string
		gateOn      bool
		callerIsBob bool
		admin       bool
		wantSkipped bool
		wantOK      bool
	}{
		{"gate on, cross-user blocked", true, true, false, false, false},
		{"gate on, owner allowed", true, false, false, true, true},
		{"gate on, admin allowed", true, true, true, true, true},
		{"gate off, cross-user allowed", false, true, false, true, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			auth.SetEnforceTenancyForTests(t, tc.gateOn)
			f := seedTwoUserBulk(t)

			caller := f.u1
			role := "user"
			if tc.callerIsBob {
				caller = f.u2
			}
			if tc.admin {
				caller = 99
				role = "admin"
			}

			body := fmt.Sprintf(`{"ids":[%d],"action":"blocklist"}`, f.b1.ID)
			rec := postBulkAs(t, f.h.WantedBulk, body, caller, role)
			if rec.Code != http.StatusOK {
				t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
			}
			if got := resultOK(t, rec, f.b1.ID); got != tc.wantOK {
				t.Errorf("per-id ok=%v, want %v", got, tc.wantOK)
			}
			got, _ := f.books.GetByID(context.Background(), f.b1.ID)
			skipped := got.Status == models.BookStatusSkipped
			if skipped != tc.wantSkipped {
				t.Errorf("book skipped=%v, want %v (durable invariant)", skipped, tc.wantSkipped)
			}
		})
	}
}

// TestBulk_MixedOwnership_PartialEnforcement proves a single bulk call mixing
// the caller's own id with a victim's id mutates only the owned row: Alice
// bulk-deletes [a1 (hers), a2 (Bob's)] under the gate; a1 must be deleted, a2
// must survive, and a2's per-id result must be an error.
func TestBulk_MixedOwnership_PartialEnforcement(t *testing.T) {
	auth.SetEnforceTenancyForTests(t, true)
	f := seedTwoUserBulk(t)

	body := fmt.Sprintf(`{"ids":[%d,%d],"action":"delete"}`, f.a1.ID, f.a2.ID)
	rec := postBulkAs(t, f.h.AuthorsBulk, body, f.u1, "user")
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	if ok := resultOK(t, rec, f.a1.ID); !ok {
		t.Error("Alice's own author should be deleted (ok:true)")
	}
	if ok := resultOK(t, rec, f.a2.ID); ok {
		t.Error("Bob's author must be rejected (ok:false) for Alice")
	}

	ctx := context.Background()
	if got, _ := f.authors.GetByID(ctx, f.a1.ID); got != nil {
		t.Error("Alice's author should be gone")
	}
	if got, _ := f.authors.GetByID(ctx, f.a2.ID); got == nil {
		t.Error("Bob's author must be untouched (durable invariant)")
	}
}
