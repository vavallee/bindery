package api

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"

	"github.com/vavallee/bindery/internal/db"
	"github.com/vavallee/bindery/internal/models"
)

// seedLockTestBook creates one plain book under the fixture author.
func seedLockTestBook(t *testing.T, books *db.BookRepo, authorID int64, foreignID, title string) *models.Book {
	t.Helper()
	b := &models.Book{
		ForeignID: foreignID, AuthorID: authorID, Title: title, SortTitle: title,
		Status: models.BookStatusWanted, Genres: []string{"Old Genre"},
		Language: "en", MetadataProvider: "openlibrary", Monitored: true,
	}
	if err := books.Create(context.Background(), b); err != nil {
		t.Fatal(err)
	}
	return b
}

func putBook(t *testing.T, h *BookHandler, id string, body map[string]any) *httptest.ResponseRecorder {
	t.Helper()
	raw, _ := json.Marshal(body)
	req := withURLParam(httptest.NewRequest(http.MethodPut, "/api/v1/book/"+id, bytes.NewReader(raw)), "id", id)
	rec := httptest.NewRecorder()
	h.Update(rec, req)
	return rec
}

// TestBookUpdate_ManualEditLocksFields pins the #1237/#1446 contract: a
// manual metadata edit through PUT /book/{id} both applies the value and
// locks the field, and an explicit lockedFields array replaces the lock set
// (the unlock path).
func TestBookUpdate_ManualEditLocksFields(t *testing.T) {
	h, books, _, author, ctx := bookFixture(t)
	b := seedLockTestBook(t, books, author.ID, "LOCK1", "Original Title")
	id := strconv.FormatInt(b.ID, 10)

	rec := putBook(t, h, id, map[string]any{
		"title":       "Edited Title",
		"description": "Edited description",
		"genres":      []string{"Fantasy", " ", "Epic"},
		"language":    "it",
		"releaseDate": "2020-05-04",
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	got, err := books.GetByID(ctx, b.ID)
	if err != nil || got == nil {
		t.Fatalf("reload book: %v", err)
	}
	if got.Title != "Edited Title" || got.Description != "Edited description" || got.Language != "it" {
		t.Fatalf("edited values not persisted: %+v", got)
	}
	if len(got.Genres) != 2 || got.Genres[0] != "Fantasy" || got.Genres[1] != "Epic" {
		t.Fatalf("genres = %v, want cleaned [Fantasy Epic]", got.Genres)
	}
	if got.ReleaseDate == nil || got.ReleaseDate.Format("2006-01-02") != "2020-05-04" {
		t.Fatalf("releaseDate = %v, want 2020-05-04", got.ReleaseDate)
	}
	for _, f := range models.LockableBookFields {
		if !got.IsFieldLocked(f) {
			t.Errorf("field %q should be locked after manual edit; locked=%v", f, got.LockedFields)
		}
	}

	// Unlock path: an explicit empty lockedFields clears the set without
	// touching values.
	rec = putBook(t, h, id, map[string]any{"lockedFields": []string{}})
	if rec.Code != http.StatusOK {
		t.Fatalf("unlock: expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	got, _ = books.GetByID(ctx, b.ID)
	if len(got.LockedFields) != 0 {
		t.Fatalf("lockedFields = %v, want empty after explicit unlock", got.LockedFields)
	}
	if got.Title != "Edited Title" {
		t.Fatalf("unlock must not change values; title = %q", got.Title)
	}
}

// TestBookUpdate_LockValidation pins the 400 paths: unknown lock names, junk
// release dates, and empty titles are rejected without side effects.
func TestBookUpdate_LockValidation(t *testing.T) {
	h, books, _, author, ctx := bookFixture(t)
	b := seedLockTestBook(t, books, author.ID, "LOCK2", "Keep Title")
	id := strconv.FormatInt(b.ID, 10)

	cases := []map[string]any{
		{"lockedFields": []string{"bogus"}},
		{"releaseDate": "05/04/2020"},
		{"title": "   "},
	}
	for _, body := range cases {
		if rec := putBook(t, h, id, body); rec.Code != http.StatusBadRequest {
			t.Errorf("body %v: expected 400, got %d: %s", body, rec.Code, rec.Body.String())
		}
	}
	got, _ := books.GetByID(ctx, b.ID)
	if got.Title != "Keep Title" || len(got.LockedFields) != 0 {
		t.Fatalf("rejected requests must not mutate: %+v", got)
	}
}

// TestAuthorApplyGenres pins the author-level genre override (#1446): every
// book under the author gets the genre list and a genres lock.
func TestAuthorApplyGenres(t *testing.T) {
	database, err := db.OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { database.Close() })
	books := db.NewBookRepo(database)
	authors := db.NewAuthorRepo(database)
	ctx := context.Background()

	author := &models.Author{ForeignID: "OL1A", Name: "A", SortName: "A", MetadataProvider: "openlibrary"}
	if err := authors.Create(ctx, author); err != nil {
		t.Fatal(err)
	}
	b1 := seedLockTestBook(t, books, author.ID, "G1", "Book One")
	b2 := seedLockTestBook(t, books, author.ID, "G2", "Book Two")

	h := NewAuthorHandler(authors, nil, books, nil, nil, nil, nil, nil)
	raw, _ := json.Marshal(map[string]any{"genres": []string{" Nonfiction ", ""}})
	req := withURLParam(httptest.NewRequest(http.MethodPut, "/api/v1/author/"+strconv.FormatInt(author.ID, 10)+"/genres", bytes.NewReader(raw)), "id", strconv.FormatInt(author.ID, 10))
	rec := httptest.NewRecorder()
	h.ApplyGenres(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	var resp map[string]int
	_ = json.Unmarshal(rec.Body.Bytes(), &resp)
	if resp["updated"] != 2 {
		t.Fatalf("updated = %d, want 2", resp["updated"])
	}
	for _, id := range []int64{b1.ID, b2.ID} {
		got, _ := books.GetByID(ctx, id)
		if len(got.Genres) != 1 || got.Genres[0] != "Nonfiction" {
			t.Errorf("book %d genres = %v, want [Nonfiction]", id, got.Genres)
		}
		if !got.IsFieldLocked(models.BookFieldGenres) {
			t.Errorf("book %d genres should be locked", id)
		}
	}
}

// TestSeriesApplyGenres pins the series-level genre override (#1446).
func TestSeriesApplyGenres(t *testing.T) {
	database, err := db.OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { database.Close() })
	books := db.NewBookRepo(database)
	authors := db.NewAuthorRepo(database)
	seriesRepo := db.NewSeriesRepo(database)
	ctx := context.Background()

	author := &models.Author{ForeignID: "OL1A", Name: "A", SortName: "A", MetadataProvider: "openlibrary"}
	if err := authors.Create(ctx, author); err != nil {
		t.Fatal(err)
	}
	inSeries := seedLockTestBook(t, books, author.ID, "S1", "Series Book")
	outside := seedLockTestBook(t, books, author.ID, "S2", "Standalone")

	series, err := seriesRepo.CreateManual(ctx, "Demo Series")
	if err != nil {
		t.Fatal(err)
	}
	if err := seriesRepo.LinkBook(ctx, series.ID, inSeries.ID, "1", true); err != nil {
		t.Fatal(err)
	}

	h := NewSeriesHandler(seriesRepo, books, authors, nil, nil)
	raw, _ := json.Marshal(map[string]any{"genres": []string{"Fantasy"}})
	req := withURLParam(httptest.NewRequest(http.MethodPut, "/api/v1/series/"+strconv.FormatInt(series.ID, 10)+"/genres", bytes.NewReader(raw)), "id", strconv.FormatInt(series.ID, 10))
	rec := httptest.NewRecorder()
	h.ApplyGenres(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	got, _ := books.GetByID(ctx, inSeries.ID)
	if len(got.Genres) != 1 || got.Genres[0] != "Fantasy" || !got.IsFieldLocked(models.BookFieldGenres) {
		t.Fatalf("series book not updated+locked: %+v %v", got.Genres, got.LockedFields)
	}
	other, _ := books.GetByID(ctx, outside.ID)
	if other.IsFieldLocked(models.BookFieldGenres) {
		t.Fatalf("standalone book must be untouched; locked=%v", other.LockedFields)
	}
}
