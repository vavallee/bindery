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

// Interactive search must carry what the library knows about the wanted book
// (its series position, the author's other titles) into MatchCriteria, or the
// relevance filter's trailing title rules cannot use it (#3268).
func TestSearchBook_CarriesSeriesPositionAndAuthorTitles(t *testing.T) {
	database, err := db.OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { database.Close() })

	ctx := context.Background()
	authorRepo := db.NewAuthorRepo(database)
	author := &models.Author{ForeignID: "OL3268A", Name: "Leigh Bardugo", SortName: "Bardugo, Leigh", MetadataProvider: "openlibrary", Monitored: true}
	if err := authorRepo.Create(ctx, author); err != nil {
		t.Fatal(err)
	}
	bookRepo := db.NewBookRepo(database)
	wanted := &models.Book{Title: "King of Scars", ForeignID: "OL3268M1", AuthorID: author.ID, MediaType: models.MediaTypeAudiobook, Monitored: true}
	other := &models.Book{Title: "Rule of Wolves", ForeignID: "OL3268M2", AuthorID: author.ID, MediaType: models.MediaTypeAudiobook, Monitored: true}
	for _, b := range []*models.Book{wanted, other} {
		if err := bookRepo.Create(ctx, b); err != nil {
			t.Fatal(err)
		}
	}
	seriesRepo := db.NewSeriesRepo(database)
	series := &models.Series{ForeignID: "OL3268S", Title: "Nikolai Duology"}
	if err := seriesRepo.Create(ctx, series); err != nil {
		t.Fatal(err)
	}
	if err := seriesRepo.LinkBook(ctx, series.ID, wanted.ID, "1", true); err != nil {
		t.Fatal(err)
	}

	mock := &mockIndexerSearcher{audioResults: []newznab.SearchResult{{GUID: "r1", Title: "Leigh Bardugo - King of Scars MP3"}}}
	h := NewIndexerHandler(
		db.NewIndexerRepo(database), bookRepo, authorRepo,
		db.NewMetadataProfileRepo(database), mock,
		db.NewSettingsRepo(database), db.NewBlocklistRepo(database),
	).WithSeries(seriesRepo)

	rec := httptest.NewRecorder()
	req := withURLParam(
		httptest.NewRequest(http.MethodGet, "/indexer/book/1/search", nil),
		"id", strconv.FormatInt(wanted.ID, 10),
	)
	h.SearchBook(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	crit := mock.criteria()
	if crit.SeriesTitle != "Nikolai Duology" {
		t.Errorf("SeriesTitle = %q, want %q", crit.SeriesTitle, "Nikolai Duology")
	}
	if crit.SeriesPosition != "1" {
		t.Errorf("SeriesPosition = %q, want %q", crit.SeriesPosition, "1")
	}
	if !slices.Equal(crit.AuthorTitles, []string{"Rule of Wolves"}) {
		t.Errorf("AuthorTitles = %v, want only the other book, not the wanted one", crit.AuthorTitles)
	}
}
