package scheduler

import (
	"context"
	"slices"
	"testing"

	"github.com/vavallee/bindery/internal/db"
	"github.com/vavallee/bindery/internal/indexer"
	"github.com/vavallee/bindery/internal/models"
)

// Automatic search must carry the install wide rejected words into
// MatchCriteria (#3416), and an unset setting must carry nothing.
func TestSearchAndGrabFormat_CarriesRejectWords(t *testing.T) {
	database, err := db.OpenMemory()
	if err != nil {
		t.Fatalf("OpenMemory: %v", err)
	}
	t.Cleanup(func() { database.Close() })

	ctx := context.Background()
	authors := db.NewAuthorRepo(database)
	books := db.NewBookRepo(database)
	a := &models.Author{ForeignID: "OL-3416-A", Name: "Frank Herbert", SortName: "Herbert, Frank", MetadataProvider: "ol", Monitored: true}
	if err := authors.Create(ctx, a); err != nil {
		t.Fatalf("author create: %v", err)
	}
	wanted := models.Book{
		ForeignID: "OL-3416-B", AuthorID: a.ID, Title: "Dune", SortTitle: "Dune",
		Status: models.BookStatusWanted, Genres: []string{}, MetadataProvider: "ol",
		Monitored: true, MediaType: models.MediaTypeAudiobook,
	}
	if err := books.Create(ctx, &wanted); err != nil {
		t.Fatalf("book create: %v", err)
	}
	settings := db.NewSettingsRepo(database)

	run := func() indexer.MatchCriteria {
		ss := &fixedResultsSearcher{}
		s := &Scheduler{
			searcher: ss, indexers: db.NewIndexerRepo(database), authors: authors, books: books,
			settings: settings, blocklist: db.NewBlocklistRepo(database),
			downloads: db.NewDownloadRepo(database), clients: db.NewDownloadClientRepo(database),
		}
		s.searchAndGrabFormat(ctx, wanted, models.MediaTypeAudiobook, nil)
		return ss.lastCrit
	}

	if c := run(); len(c.RejectWords) != 0 {
		t.Fatalf("unset setting produced %v", c.RejectWords)
	}
	if err := settings.Set(ctx, indexer.SettingRejectWords, "Graphic Audio, Full Cast"); err != nil {
		t.Fatal(err)
	}
	if c := run(); !slices.Equal(c.RejectWords, []string{"Graphic Audio", "Full Cast"}) {
		t.Fatalf("RejectWords = %v", c.RejectWords)
	}
}
