package api

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/vavallee/bindery/internal/db"
	"github.com/vavallee/bindery/internal/metadata"
	"github.com/vavallee/bindery/internal/models"
)

// TestAddBook_DirectInsertCreatesDistinctSeriesVolume is the #1785 add-path
// regression. indexer.CanonicalDedupKey strips a ": subtitle" tail, so
// "A Thousand Li: The Second Step" collapses to the same dedup key as the
// already-imported "A Thousand Li: The First Step". Before the fix, the direct
// insert found that sibling via FindByAuthorAndDedupKey and adoptDirectInsertMatch
// no-op'd (the row is not a calibre stub and needs no field change), so the
// requested foreign id was never bound to any row and the poll 404'd forever
// ("book not found after author sync — try again shortly"). With series
// awareness the requested work is recognised as a distinct volume (same series,
// different sequence) and a new row is created.
func TestAddBook_DirectInsertCreatesDistinctSeriesVolume(t *testing.T) {
	database, err := db.OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	database.SetMaxOpenConns(1)

	ctx := context.Background()
	authorRepo := db.NewAuthorRepo(database)
	bookRepo := db.NewBookRepo(database)
	seriesRepo := db.NewSeriesRepo(database)
	profileRepo := db.NewMetadataProfileRepo(database)

	author := &models.Author{
		ForeignID: "OLTW", Name: "Tao Wong", SortName: "Wong, Tao",
		MetadataProvider: "openlibrary", Monitored: true,
	}
	if err := authorRepo.Create(ctx, author); err != nil {
		t.Fatal(err)
	}

	// Volume 1 is already in the library, filed under its full title and linked
	// to the series at sequence 1.
	vol1 := &models.Book{
		ForeignID: "OLATL1", AuthorID: author.ID, Title: "A Thousand Li: The First Step",
		SortTitle: "A Thousand Li: The First Step", Status: models.BookStatusWanted,
		Genres: []string{}, MetadataProvider: "openlibrary", MediaType: models.MediaTypeEbook,
		Monitored: true,
	}
	if err := bookRepo.Create(ctx, vol1); err != nil {
		t.Fatal(err)
	}
	series := &models.Series{ForeignID: "manual:a-thousand-li", Title: "A Thousand Li"}
	if err := seriesRepo.Create(ctx, series); err != nil {
		t.Fatal(err)
	}
	if err := seriesRepo.LinkBook(ctx, series.ID, vol1.ID, "1", true); err != nil {
		t.Fatal(err)
	}

	// GetBook resolves the requested volume 2 — same series, sequence 2 — with a
	// dedup key equal to volume 1's.
	requested := &models.Book{
		ForeignID: "OLATL2", Title: "A Thousand Li: The Second Step",
		SortTitle: "A Thousand Li: The Second Step", Status: models.BookStatusWanted,
		Genres: []string{}, MetadataProvider: "openlibrary",
		SeriesRefs: []models.SeriesRef{{Title: "A Thousand Li", Position: "2"}},
	}
	stub := &stubMetaProvider{
		name:        "openlibrary",
		works:       nil,
		getBookByID: map[string]*models.Book{"OLATL2": requested},
	}
	agg := metadata.NewAggregator(stub)
	h := NewAuthorHandler(authorRepo, nil, bookRepo, seriesRepo, agg, nil, profileRepo, nil)

	body, _ := json.Marshal(map[string]any{
		"foreignBookId":   "OLATL2",
		"foreignAuthorId": "OLTW",
		"authorName":      "Tao Wong",
	})
	req := httptest.NewRequest(http.MethodPost, "/api/v1/author/book", bytes.NewReader(body))
	rec := httptest.NewRecorder()

	h.AddBook(rec, req)

	// 201 (not 404) proves the requested volume was created as its own row
	// rather than silently deduped onto volume 1.
	if rec.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d: %s", rec.Code, rec.Body.String())
	}
	got, err := bookRepo.GetByForeignID(ctx, "OLATL2")
	if err != nil || got == nil {
		t.Fatalf("requested volume not persisted under its own foreign id: err=%v got=%v", err, got)
	}
	if got.ID == vol1.ID {
		t.Fatal("requested volume was deduped onto volume 1 instead of created distinctly")
	}

	books, err := bookRepo.ListByAuthorIncludingExcluded(ctx, author.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(books) != 2 {
		t.Fatalf("expected 2 distinct volumes for the author, got %d", len(books))
	}
}

// TestAddBook_DirectInsertAdoptsSameSequenceEdition guards against over-splitting
// the add path: when the requested work is the *same* volume as an existing row
// (same series and sequence, subtitle differs), the direct insert must still
// dedup onto it rather than create a second row.
func TestAddBook_DirectInsertAdoptsSameSequenceEdition(t *testing.T) {
	database, err := db.OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	database.SetMaxOpenConns(1)

	ctx := context.Background()
	authorRepo := db.NewAuthorRepo(database)
	bookRepo := db.NewBookRepo(database)
	seriesRepo := db.NewSeriesRepo(database)
	profileRepo := db.NewMetadataProfileRepo(database)

	author := &models.Author{
		ForeignID: "OLTW", Name: "Tao Wong", SortName: "Wong, Tao",
		MetadataProvider: "openlibrary", Monitored: true,
	}
	if err := authorRepo.Create(ctx, author); err != nil {
		t.Fatal(err)
	}
	// Existing calibre stub for volume 1 at sequence 1.
	vol1 := &models.Book{
		ForeignID: "calibre:book:1", AuthorID: author.ID, Title: "A Thousand Li: The First Step",
		SortTitle: "A Thousand Li: The First Step", Status: models.BookStatusImported,
		Genres: []string{}, MetadataProvider: "calibre", MediaType: models.MediaTypeEbook,
		Monitored: true,
	}
	if err := bookRepo.Create(ctx, vol1); err != nil {
		t.Fatal(err)
	}
	series := &models.Series{ForeignID: "manual:a-thousand-li", Title: "A Thousand Li"}
	if err := seriesRepo.Create(ctx, series); err != nil {
		t.Fatal(err)
	}
	if err := seriesRepo.LinkBook(ctx, series.ID, vol1.ID, "1", true); err != nil {
		t.Fatal(err)
	}

	// Requested work is the same volume (sequence 1) under the provider id.
	requested := &models.Book{
		ForeignID: "OLATL1", Title: "A Thousand Li", // subtitle dropped
		SortTitle: "A Thousand Li", Status: models.BookStatusWanted,
		Genres: []string{}, MetadataProvider: "openlibrary",
		SeriesRefs: []models.SeriesRef{{Title: "A Thousand Li", Position: "1"}},
	}
	stub := &stubMetaProvider{
		name:        "openlibrary",
		works:       nil,
		getBookByID: map[string]*models.Book{"OLATL1": requested},
	}
	agg := metadata.NewAggregator(stub)
	h := NewAuthorHandler(authorRepo, nil, bookRepo, seriesRepo, agg, nil, profileRepo, nil)

	body, _ := json.Marshal(map[string]any{
		"foreignBookId":   "OLATL1",
		"foreignAuthorId": "OLTW",
		"authorName":      "Tao Wong",
	})
	req := httptest.NewRequest(http.MethodPost, "/api/v1/author/book", bytes.NewReader(body))
	rec := httptest.NewRecorder()

	h.AddBook(rec, req)

	if rec.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d: %s", rec.Code, rec.Body.String())
	}
	books, err := bookRepo.ListByAuthorIncludingExcluded(ctx, author.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(books) != 1 {
		t.Fatalf("same-sequence edition should adopt the existing row, got %d books", len(books))
	}
}
