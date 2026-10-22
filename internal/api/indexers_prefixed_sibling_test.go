package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"slices"
	"strconv"
	"testing"

	"github.com/vavallee/bindery/internal/db"
	"github.com/vavallee/bindery/internal/indexer/newznab"
	"github.com/vavallee/bindery/internal/models"
)

// Call path pin for #3382. This test passes without the fix too: it pins the
// input the new rule depends on, and the rule itself is tested in
// internal/indexer. Interactive search hands the relevance filter the author's NON
// excluded titles. The imported, series prefixed sibling is among them, which
// is what lets the filter read its suffix as another book; the excluded
// standalone duplicate is not, and need not be.
func TestSearchBook_CarriesPrefixedSiblingTitle(t *testing.T) {
	database, err := db.OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { database.Close() })

	ctx := context.Background()
	authorRepo := db.NewAuthorRepo(database)
	author := &models.Author{ForeignID: "OL3382A", Name: "R. L. Stine", SortName: "Stine, R. L.", MetadataProvider: "openlibrary", Monitored: true}
	if err := authorRepo.Create(ctx, author); err != nil {
		t.Fatal(err)
	}
	bookRepo := db.NewBookRepo(database)
	wanted := &models.Book{Title: "Rotten School", ForeignID: "OL3382M1", AuthorID: author.ID, MediaType: models.MediaTypeEbook, Monitored: true}
	prefixed := &models.Book{Title: "Rotten School - Punk'd and skunked", ForeignID: "OL3382M2", AuthorID: author.ID, MediaType: models.MediaTypeEbook, Monitored: true}
	duplicate := &models.Book{Title: "Punk'd and Skunked", ForeignID: "OL3382M3", AuthorID: author.ID, MediaType: models.MediaTypeEbook}
	for _, b := range []*models.Book{wanted, prefixed, duplicate} {
		if err := bookRepo.Create(ctx, b); err != nil {
			t.Fatal(err)
		}
	}
	if err := bookRepo.SetExcluded(ctx, duplicate.ID, true); err != nil {
		t.Fatal(err)
	}

	mock := &mockIndexerSearcher{ebookResults: []newznab.SearchResult{{GUID: "r1", Title: "R L Stine - [Rotten School 11] - Punk'd and Skunked (retail) (epub)"}}}
	h := NewIndexerHandler(
		db.NewIndexerRepo(database), bookRepo, authorRepo,
		db.NewMetadataProfileRepo(database), mock,
		db.NewSettingsRepo(database), db.NewBlocklistRepo(database),
	)

	rec := httptest.NewRecorder()
	req := withURLParam(
		httptest.NewRequest(http.MethodGet, "/indexer/book/1/search", nil),
		"id", strconv.FormatInt(wanted.ID, 10),
	)
	h.SearchBook(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	if got := mock.criteria().AuthorTitles; !slices.Equal(got, []string{"Rotten School - Punk'd and skunked"}) {
		t.Errorf("AuthorTitles = %v, want only the non excluded prefixed sibling", got)
	}
}
