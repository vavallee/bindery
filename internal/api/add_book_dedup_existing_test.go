package api

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/vavallee/bindery/internal/db"
	"github.com/vavallee/bindery/internal/metadata"
	"github.com/vavallee/bindery/internal/models"
)

// TestAddBook_DedupedOntoExistingRowAnswersWithThatRow is the #3466 regression.
//
// The author already has "Ready Player One (Movie Tie-In)". Adding the plain
// "Ready Player One" under a different work id dedups onto that row on purpose
// (the qualifier strip in the dedup key, #1612), and the row keeps its own
// foreign id, so the poll for the requested id could never succeed. The request
// then sat out the 15 s poll and answered 404 "try again shortly", which a retry
// reproduced forever. It must answer at once with the existing row instead,
// the same 409 a re-add of a known id gets (#1227).
func TestAddBook_DedupedOntoExistingRowAnswersWithThatRow(t *testing.T) {
	database, err := db.OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	database.SetMaxOpenConns(1)

	ctx := context.Background()
	authorRepo := db.NewAuthorRepo(database)
	bookRepo := db.NewBookRepo(database)
	profileRepo := db.NewMetadataProfileRepo(database)

	author := &models.Author{
		ForeignID: "OL6941868A", Name: "Ernest Cline", SortName: "Cline, Ernest",
		MetadataProvider: "openlibrary", Monitored: true,
	}
	if err := authorRepo.Create(ctx, author); err != nil {
		t.Fatal(err)
	}
	existing := &models.Book{
		ForeignID: "OL19331965W", AuthorID: author.ID, Title: "Ready Player One (Movie Tie-In)",
		SortTitle: "Ready Player One (Movie Tie-In)", Status: models.BookStatusWanted,
		Genres: []string{}, MetadataProvider: "openlibrary", MediaType: models.MediaTypeEbook,
		Monitored: true,
	}
	if err := bookRepo.Create(ctx, existing); err != nil {
		t.Fatal(err)
	}

	requested := &models.Book{
		ForeignID: "OL15936512W", Title: "Ready Player One", SortTitle: "Ready Player One",
		Status: models.BookStatusWanted, Genres: []string{}, MetadataProvider: "openlibrary",
		MediaType: models.MediaTypeEbook,
	}
	stub := &stubMetaProvider{
		name:        "openlibrary",
		getBookByID: map[string]*models.Book{"OL15936512W": requested},
	}
	h := NewAuthorHandler(authorRepo, nil, bookRepo, nil, metadata.NewAggregator(stub), nil, profileRepo, nil)

	body, _ := json.Marshal(map[string]any{
		"foreignBookId":   "OL15936512W",
		"foreignAuthorId": "OL6941868A",
		"authorName":      "Ernest Cline",
	})
	req := httptest.NewRequest(http.MethodPost, "/api/v1/author/book", bytes.NewReader(body))
	rec := httptest.NewRecorder()

	start := time.Now()
	h.AddBook(rec, req)
	if d := time.Since(start); d > 5*time.Second {
		t.Fatalf("request polled for %s instead of answering at once", d)
	}

	if rec.Code != http.StatusConflict {
		t.Fatalf("expected 409, got %d: %s", rec.Code, rec.Body.String())
	}
	var resp struct {
		ExistingBookID int64 `json:"existingBookId"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if resp.ExistingBookID != existing.ID {
		t.Fatalf("existingBookId = %d, want %d: %s", resp.ExistingBookID, existing.ID, rec.Body.String())
	}
	books, err := bookRepo.ListByAuthorIncludingExcluded(ctx, author.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(books) != 1 {
		t.Fatalf("dedup must not create a second row, got %d", len(books))
	}
}

// TestAddBook_DedupedOntoExistingRowReportsFormatWidened is the #3482
// regression. Adding the other format of a book deduped onto an existing row
// widens that row to both, and the 409 must say so with formatWidened. A
// request that changed nothing keeps the plain 409 without the flag.
func TestAddBook_DedupedOntoExistingRowReportsFormatWidened(t *testing.T) {
	cases := []struct {
		name         string
		existingType string
		requestType  string
		wantWidened  bool
		wantType     string
	}{
		{"ebook then audiobook", models.MediaTypeEbook, models.MediaTypeAudiobook, true, models.MediaTypeBoth},
		{"audiobook then ebook", models.MediaTypeAudiobook, models.MediaTypeEbook, true, models.MediaTypeBoth},
		{"same format again", models.MediaTypeEbook, models.MediaTypeEbook, false, models.MediaTypeEbook},
		{"already both", models.MediaTypeBoth, models.MediaTypeAudiobook, false, models.MediaTypeBoth},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			database, err := db.OpenMemory()
			if err != nil {
				t.Fatal(err)
			}
			defer database.Close()
			database.SetMaxOpenConns(1)

			ctx := context.Background()
			authorRepo := db.NewAuthorRepo(database)
			bookRepo := db.NewBookRepo(database)
			profileRepo := db.NewMetadataProfileRepo(database)

			author := &models.Author{
				ForeignID: "OL6941868A", Name: "Ernest Cline", SortName: "Cline, Ernest",
				MetadataProvider: "openlibrary", Monitored: true,
			}
			if err := authorRepo.Create(ctx, author); err != nil {
				t.Fatal(err)
			}
			existing := &models.Book{
				ForeignID: "OL19331965W", AuthorID: author.ID, Title: "Ready Player One (Movie Tie-In)",
				SortTitle: "Ready Player One (Movie Tie-In)", Status: models.BookStatusWanted,
				Genres: []string{}, MetadataProvider: "openlibrary", MediaType: tc.existingType,
				Monitored: true,
			}
			if err := bookRepo.Create(ctx, existing); err != nil {
				t.Fatal(err)
			}
			requested := &models.Book{
				ForeignID: "OL15936512W", Title: "Ready Player One", SortTitle: "Ready Player One",
				Status: models.BookStatusWanted, Genres: []string{}, MetadataProvider: "openlibrary",
				MediaType: tc.requestType,
			}
			stub := &stubMetaProvider{
				name:        "openlibrary",
				getBookByID: map[string]*models.Book{"OL15936512W": requested},
			}
			h := NewAuthorHandler(authorRepo, nil, bookRepo, nil, metadata.NewAggregator(stub), nil, profileRepo, nil)

			body, _ := json.Marshal(map[string]any{
				"foreignBookId":   "OL15936512W",
				"foreignAuthorId": "OL6941868A",
				"authorName":      "Ernest Cline",
				"mediaType":       tc.requestType,
			})
			req := httptest.NewRequest(http.MethodPost, "/api/v1/author/book", bytes.NewReader(body))
			rec := httptest.NewRecorder()
			h.AddBook(rec, req)

			if rec.Code != http.StatusConflict {
				t.Fatalf("expected 409, got %d: %s", rec.Code, rec.Body.String())
			}
			var resp map[string]any
			if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
				t.Fatal(err)
			}
			flag, present := resp["formatWidened"]
			if tc.wantWidened {
				if flag != true {
					t.Errorf("formatWidened = %v, want true: %s", flag, rec.Body.String())
				}
			} else if present {
				t.Errorf("formatWidened must be absent when nothing was widened: %s", rec.Body.String())
			}
			got, err := bookRepo.GetByID(ctx, existing.ID)
			if err != nil || got == nil {
				t.Fatalf("reload: %v", err)
			}
			if got.MediaType != tc.wantType {
				t.Errorf("row media type = %q, want %q", got.MediaType, tc.wantType)
			}
		})
	}
}
