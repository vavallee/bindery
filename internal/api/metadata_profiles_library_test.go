package api

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"

	"github.com/vavallee/bindery/internal/db"
	"github.com/vavallee/bindery/internal/models"
)

// TestMetadataProfileFilteredBooks is the #2208 retroactive test. Every
// profile filter is gated on the book being new, so turning SkipPartBooks or
// SkipMissingDate on never touched the box sets and undated works already on
// Wanted. The preview lists exactly those, and the action unmonitors them
// without deleting anything or touching an imported book, a book whose author
// uses another profile, or a book the filters let through.
func TestMetadataProfileFilteredBooks(t *testing.T) {
	database, err := db.OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { database.Close() })
	ctx := context.Background()
	profiles := db.NewMetadataProfileRepo(database)
	authors := db.NewAuthorRepo(database)
	books := db.NewBookRepo(database)
	h := NewMetadataProfileHandler(profiles).WithLibrary(books)

	p, err := profiles.GetByID(ctx, models.DefaultMetadataProfileID)
	if err != nil || p == nil {
		t.Fatalf("default profile: %v %v", p, err)
	}
	p.SkipPartBooks = true
	p.SkipMissingDate = true
	if err := profiles.Update(ctx, p); err != nil {
		t.Fatal(err)
	}
	other := &models.MetadataProfile{Name: "Anything", AllowedLanguages: "any"}
	if err := profiles.Create(ctx, other); err != nil {
		t.Fatal(err)
	}

	newAuthor := func(name string, profileID *int64) *models.Author {
		t.Helper()
		a := &models.Author{ForeignID: "ol:" + name, Name: name, SortName: name, MetadataProvider: "openlibrary", MetadataProfileID: profileID}
		if err := authors.Create(ctx, a); err != nil {
			t.Fatal(err)
		}
		return a
	}
	corey := newAuthor("James S. A. Corey", nil)
	otherAuthor := newAuthor("Other Author", &other.ID)
	released := time.Date(2011, 6, 15, 0, 0, 0, 0, time.UTC)
	newBook := func(a *models.Author, title, status string, monitored, dated bool) *models.Book {
		t.Helper()
		b := &models.Book{
			ForeignID: "ol:" + title, AuthorID: a.ID, Title: title, SortTitle: title,
			Status: status, Monitored: monitored, MediaType: models.MediaTypeEbook,
			Genres: []string{}, MetadataProvider: "openlibrary",
		}
		if dated {
			b.ReleaseDate = &released
		}
		if err := books.Create(ctx, b); err != nil {
			t.Fatal(err)
		}
		return b
	}
	boxSet := newBook(corey, "The Expanse Books 1-3", models.BookStatusWanted, true, true)
	undated := newBook(corey, "Caliban's War", models.BookStatusWanted, true, false)
	kept := newBook(corey, "Leviathan Wakes", models.BookStatusWanted, true, true)
	ownedBoxSet := newBook(corey, "The Expanse Books 4-6", models.BookStatusImported, true, true)
	otherBoxSet := newBook(otherAuthor, "Other Books 1-3", models.BookStatusWanted, true, true)

	id := strconv.FormatInt(models.DefaultMetadataProfileID, 10)
	rec := httptest.NewRecorder()
	h.FilteredBooks(rec, withURLParam(httptest.NewRequest(http.MethodGet, "/api/v1/metadataprofile/1/filtered-books", nil), "id", id))
	if rec.Code != http.StatusOK {
		t.Fatalf("preview: expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	var preview struct {
		Books []profileFilteredBook `json:"books"`
		Total int                   `json:"total"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&preview); err != nil {
		t.Fatal(err)
	}
	reasons := map[int64]string{}
	for _, b := range preview.Books {
		reasons[b.BookID] = b.Reason
	}
	if preview.Total != 2 || reasons[boxSet.ID] != profileFilterPartBook || reasons[undated.ID] != profileFilterMissingDate {
		t.Fatalf("preview = %+v, want the box set (partBook) and the undated work (missingDate) only", preview)
	}

	// A subset: only the box set.
	rec = httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/metadataprofile/1/filtered-books/unmonitor",
		bytes.NewBufferString(`{"bookIds":[`+strconv.FormatInt(boxSet.ID, 10)+`,`+strconv.FormatInt(kept.ID, 10)+`]}`))
	h.UnmonitorFilteredBooks(rec, withURLParam(req, "id", id))
	if rec.Code != http.StatusOK {
		t.Fatalf("unmonitor: expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	var res map[string]int
	if err := json.NewDecoder(rec.Body).Decode(&res); err != nil {
		t.Fatal(err)
	}
	if res["unmonitored"] != 1 {
		t.Errorf("unmonitored = %d, want 1: a book the filters keep must not be unmonitored because its id was sent", res["unmonitored"])
	}

	// Everything left in the preview.
	rec = httptest.NewRecorder()
	h.UnmonitorFilteredBooks(rec, withURLParam(httptest.NewRequest(http.MethodPost, "/api/v1/metadataprofile/1/filtered-books/unmonitor", nil), "id", id))
	if rec.Code != http.StatusOK {
		t.Fatalf("unmonitor all: expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	want := map[int64]bool{boxSet.ID: false, undated.ID: false, kept.ID: true, ownedBoxSet.ID: true, otherBoxSet.ID: true}
	for bookID, monitored := range want {
		got, err := books.GetByID(ctx, bookID)
		if err != nil {
			t.Fatal(err)
		}
		if got == nil {
			t.Fatalf("book %d was deleted", bookID)
		}
		if got.Monitored != monitored {
			t.Errorf("%q monitored = %v, want %v", got.Title, got.Monitored, monitored)
		}
	}

	rec = httptest.NewRecorder()
	h.FilteredBooks(rec, withURLParam(httptest.NewRequest(http.MethodGet, "/api/v1/metadataprofile/99/filtered-books", nil), "id", "99"))
	if rec.Code != http.StatusNotFound {
		t.Errorf("unknown profile: status = %d, want 404", rec.Code)
	}
}
