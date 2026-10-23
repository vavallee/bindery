package scheduler

import (
	"context"
	"slices"
	"testing"

	"github.com/vavallee/bindery/internal/db"
	"github.com/vavallee/bindery/internal/models"
)

// Auto-grab must carry what the library knows about the wanted book (its
// series position, the author's other titles) into MatchCriteria, or the
// relevance filter's trailing title rules cannot use it (#3268).
func TestSearchAndGrabFormat_CarriesSeriesPositionAndAuthorTitles(t *testing.T) {
	database, err := db.OpenMemory()
	if err != nil {
		t.Fatalf("OpenMemory: %v", err)
	}
	t.Cleanup(func() { database.Close() })

	ctx := context.Background()
	authors := db.NewAuthorRepo(database)
	books := db.NewBookRepo(database)
	seriesRepo := db.NewSeriesRepo(database)

	a := &models.Author{ForeignID: "OL-3268-A", Name: "Leigh Bardugo", SortName: "Bardugo, Leigh", MetadataProvider: "ol", Monitored: true}
	if err := authors.Create(ctx, a); err != nil {
		t.Fatalf("author create: %v", err)
	}
	wanted := models.Book{
		ForeignID: "OL-3268-B1", AuthorID: a.ID, Title: "King of Scars", SortTitle: "King of Scars",
		Status: models.BookStatusWanted, Genres: []string{}, MetadataProvider: "ol",
		Monitored: true, MediaType: models.MediaTypeAudiobook,
	}
	other := models.Book{
		ForeignID: "OL-3268-B2", AuthorID: a.ID, Title: "Rule of Wolves", SortTitle: "Rule of Wolves",
		Status: models.BookStatusWanted, Genres: []string{}, MetadataProvider: "ol",
		Monitored: true, MediaType: models.MediaTypeAudiobook,
	}
	for _, b := range []*models.Book{&wanted, &other} {
		if err := books.Create(ctx, b); err != nil {
			t.Fatalf("book create: %v", err)
		}
	}
	series := &models.Series{ForeignID: "OL-3268-S", Title: "Nikolai Duology"}
	if err := seriesRepo.Create(ctx, series); err != nil {
		t.Fatalf("series create: %v", err)
	}
	if err := seriesRepo.LinkBook(ctx, series.ID, wanted.ID, "1", true); err != nil {
		t.Fatalf("link: %v", err)
	}

	ss := &fixedResultsSearcher{}
	s := &Scheduler{
		searcher:  ss,
		indexers:  db.NewIndexerRepo(database),
		authors:   authors,
		books:     books,
		series:    seriesRepo,
		settings:  db.NewSettingsRepo(database),
		blocklist: db.NewBlocklistRepo(database),
		downloads: db.NewDownloadRepo(database),
		clients:   db.NewDownloadClientRepo(database),
	}

	s.searchAndGrabFormat(ctx, wanted, models.MediaTypeAudiobook, nil)

	if ss.lastCrit.SeriesTitle != "Nikolai Duology" {
		t.Errorf("SeriesTitle = %q, want %q", ss.lastCrit.SeriesTitle, "Nikolai Duology")
	}
	if ss.lastCrit.SeriesPosition != "1" {
		t.Errorf("SeriesPosition = %q, want %q", ss.lastCrit.SeriesPosition, "1")
	}
	if !slices.Equal(ss.lastCrit.AuthorTitles, []string{"Rule of Wolves"}) {
		t.Errorf("AuthorTitles = %v, want only the other book, not the wanted one", ss.lastCrit.AuthorTitles)
	}
}
