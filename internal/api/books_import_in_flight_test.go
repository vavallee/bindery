package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"

	"github.com/vavallee/bindery/internal/auth"
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

// TestBookGet_ImportInFlightIsOwnerScoped: under tenancy the flag follows the
// queue's owner scope, so a user viewing a book nobody owns cannot learn that
// another user has a grab for it in flight.
func TestBookGet_ImportInFlightIsOwnerScoped(t *testing.T) {
	auth.SetEnforceTenancyForTests(t, true)
	database, err := db.OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { database.Close() })
	ctx := context.Background()
	books := db.NewBookRepo(database)
	authors := db.NewAuthorRepo(database)
	downloads := db.NewDownloadRepo(database)
	users := db.NewUserRepo(database)
	grabber, err := users.Create(ctx, "grabber", "h-grabber")
	if err != nil {
		t.Fatal(err)
	}
	other, err := users.Create(ctx, "other", "h-other")
	if err != nil {
		t.Fatal(err)
	}

	author := &models.Author{ForeignID: "OL-2423-S", Name: "Scope Author", SortName: "Author, Scope", MetadataProvider: "openlibrary", Monitored: true}
	if err := authors.Create(ctx, author); err != nil {
		t.Fatal(err)
	}
	book := &models.Book{
		ForeignID: "OL-2423-S1", AuthorID: author.ID, Title: "Shared Book", SortTitle: "shared book",
		Status: models.BookStatusWanted, Monitored: true, MediaType: models.MediaTypeEbook,
		MetadataProvider: "openlibrary", Genres: []string{},
	}
	if err := books.Create(ctx, book); err != nil {
		t.Fatal(err)
	}
	dl := &models.Download{GUID: "guid-2423-s", BookID: &book.ID, Title: "Shared Book", Status: models.StateDownloading, OwnerUserID: grabber.ID}
	if err := downloads.Create(ctx, dl); err != nil {
		t.Fatal(err)
	}
	h := NewBookHandler(books, nil, db.NewHistoryRepo(database), nil).WithDownloads(downloads)

	get := func(userID int64, role string) (int, any) {
		t.Helper()
		id := strconv.FormatInt(book.ID, 10)
		req := withURLParam(httptest.NewRequest(http.MethodGet, "/api/v1/book/"+id, nil), "id", id)
		req = req.WithContext(auth.WithUserRole(auth.WithUserID(req.Context(), userID), role))
		rec := httptest.NewRecorder()
		h.Get(rec, req)
		var body map[string]any
		_ = json.Unmarshal(rec.Body.Bytes(), &body)
		return rec.Code, body["importInFlight"]
	}

	if code, v := get(other.ID, "user"); code != http.StatusOK || v == true {
		t.Errorf("another user: status %d importInFlight %v, want 200 and no flag", code, v)
	}
	if code, v := get(grabber.ID, "user"); code != http.StatusOK || v != true {
		t.Errorf("the grabbing user: status %d importInFlight %v, want 200 and true", code, v)
	}
	if code, v := get(1, "admin"); code != http.StatusOK || v != true {
		t.Errorf("admin: status %d importInFlight %v, want 200 and true", code, v)
	}
}
