package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"

	"github.com/vavallee/bindery/internal/db"
	"github.com/vavallee/bindery/internal/models"
)

// TestBookGet_ImportInFlight pins the signal the book page's live refresh arms
// on (#2423). The old poll keyed off book statuses nothing ever wrote, so it
// never ran. A download row for the book that is still on its way into the
// library is what actually exists while an import is in flight.
func TestBookGet_ImportInFlight(t *testing.T) {
	database, err := db.OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { database.Close() })
	ctx := context.Background()
	books := db.NewBookRepo(database)
	authors := db.NewAuthorRepo(database)
	downloads := db.NewDownloadRepo(database)

	author := &models.Author{ForeignID: "OL-2423-A", Name: "Poll Author", SortName: "Author, Poll", MetadataProvider: "openlibrary", Monitored: true}
	if err := authors.Create(ctx, author); err != nil {
		t.Fatal(err)
	}
	book := &models.Book{
		ForeignID: "OL-2423-W", AuthorID: author.ID, Title: "Poll Book", SortTitle: "poll book",
		Status: models.BookStatusWanted, Monitored: true, MediaType: models.MediaTypeEbook,
		MetadataProvider: "openlibrary", Genres: []string{},
	}
	if err := books.Create(ctx, book); err != nil {
		t.Fatal(err)
	}
	h := NewBookHandler(books, nil, db.NewHistoryRepo(database), nil).WithDownloads(downloads)

	get := func() map[string]any {
		t.Helper()
		id := strconv.FormatInt(book.ID, 10)
		rec := httptest.NewRecorder()
		h.Get(rec, withURLParam(httptest.NewRequest(http.MethodGet, "/api/v1/book/"+id, nil), "id", id))
		if rec.Code != http.StatusOK {
			t.Fatalf("GET book: %d %s", rec.Code, rec.Body.String())
		}
		var body map[string]any
		if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
			t.Fatal(err)
		}
		return body
	}

	if v, ok := get()["importInFlight"]; ok && v != false {
		t.Fatalf("importInFlight = %v with no download, want absent or false", v)
	}

	dl := &models.Download{GUID: "guid-2423", BookID: &book.ID, Title: "Poll Book", Status: models.StateDownloading}
	if err := downloads.Create(ctx, dl); err != nil {
		t.Fatal(err)
	}
	if v := get()["importInFlight"]; v != true {
		t.Fatalf("importInFlight = %v while a download for the book is in flight, want true", v)
	}

	// Terminal and parked states do not keep the page polling: an import
	// that finished, failed, or waits on an outside tool for hours.
	for _, st := range []models.DownloadState{models.StateImported, models.StateImportFailed, models.StateImportExternal, models.StateImportHeld} {
		if _, err := database.ExecContext(ctx, `UPDATE downloads SET status=? WHERE id=?`, st, dl.ID); err != nil {
			t.Fatal(err)
		}
		if v, ok := get()["importInFlight"]; ok && v != false {
			t.Errorf("importInFlight = %v with the download %s, want absent or false", v, st)
		}
	}
}
