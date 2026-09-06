package scheduler

import (
	"context"
	"testing"

	"github.com/vavallee/bindery/internal/db"
	"github.com/vavallee/bindery/internal/indexer"
	"github.com/vavallee/bindery/internal/models"
)

// Auto-grab must carry the book's ISBN into MatchCriteria, or the ISBN
// exact-match bonus in the ranker can never fire (#1724). The edition here
// records only an isbn_10, the form a release name never carries, so the
// criteria has to come out converted to ISBN-13.
func TestSearchAndGrabFormat_PopulatesISBNFromEdition(t *testing.T) {
	// A release name the parser accepts, carrying the ISBN-13 of the same
	// edition the book stores as an ISBN-10.
	const releaseTitle = "Dune.Frank.Herbert.9780441172719.epub"

	database, err := db.OpenMemory()
	if err != nil {
		t.Fatalf("OpenMemory: %v", err)
	}
	t.Cleanup(func() { database.Close() })

	ctx := context.Background()
	authors := db.NewAuthorRepo(database)
	books := db.NewBookRepo(database)
	editions := db.NewEditionRepo(database)

	a := &models.Author{ForeignID: "OL-ISBN-A", Name: "Frank Herbert", SortName: "Herbert, Frank", MetadataProvider: "ol", Monitored: true}
	if err := authors.Create(ctx, a); err != nil {
		t.Fatalf("author create: %v", err)
	}
	book := models.Book{
		ForeignID: "OL-ISBN-B", AuthorID: a.ID, Title: "Dune", SortTitle: "Dune",
		Status: models.BookStatusWanted, Genres: []string{}, MetadataProvider: "ol",
		Monitored: true, MediaType: models.MediaTypeEbook,
	}
	if err := books.Create(ctx, &book); err != nil {
		t.Fatalf("book create: %v", err)
	}
	isbn10 := "0-441-17271-7"
	if err := editions.Upsert(ctx, &models.Edition{
		ForeignID: "OL-ISBN-E", BookID: book.ID, Title: "Dune", ISBN10: &isbn10,
	}); err != nil {
		t.Fatalf("edition upsert: %v", err)
	}

	ss := &fixedResultsSearcher{}
	s := &Scheduler{
		searcher:  ss,
		indexers:  db.NewIndexerRepo(database),
		authors:   authors,
		settings:  db.NewSettingsRepo(database),
		blocklist: db.NewBlocklistRepo(database),
		downloads: db.NewDownloadRepo(database),
		clients:   db.NewDownloadClientRepo(database),
		editions:  editions,
	}

	s.searchAndGrabFormat(ctx, book, models.MediaTypeEbook, nil)

	// The criteria the scheduler built must be the same string the release
	// parser pulls out of a matching release name — that equality is the
	// whole point of the bonus, and is what was missing.
	want := indexer.ParseRelease(releaseTitle).ISBN
	if want == "" {
		t.Fatal("test setup: release title must parse to a non-empty ISBN")
	}
	if ss.lastCrit.ISBN != want {
		t.Errorf("search criteria ISBN = %q, want %q (the ISBN parsed from a matching release)", ss.lastCrit.ISBN, want)
	}
}
