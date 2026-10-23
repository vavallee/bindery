package scheduler

import (
	"context"
	"slices"
	"testing"

	"github.com/vavallee/bindery/internal/db"
	"github.com/vavallee/bindery/internal/models"
)

// The author's other titles come from the wanted book's owner, not from every
// user who tracks that author (#3268). An unowned book keeps the old unscoped
// listing.
func TestSearchAndGrabFormat_AuthorTitlesAreScopedToTheBooksOwner(t *testing.T) {
	database, err := db.OpenMemory()
	if err != nil {
		t.Fatalf("OpenMemory: %v", err)
	}
	t.Cleanup(func() { database.Close() })
	ctx := context.Background()

	users := db.NewUserRepo(database)
	u1, err := users.Create(ctx, "alice", "x")
	if err != nil {
		t.Fatalf("user1: %v", err)
	}
	u2, err := users.Create(ctx, "bob", "x")
	if err != nil {
		t.Fatalf("user2: %v", err)
	}

	authors := db.NewAuthorRepo(database)
	books := db.NewBookRepo(database)
	a := &models.Author{ForeignID: "OL-3268-OWN-A", Name: "Leigh Bardugo", SortName: "Bardugo, Leigh", MetadataProvider: "ol", Monitored: true}
	if err := authors.Create(ctx, a); err != nil {
		t.Fatalf("author create: %v", err)
	}
	mk := func(fid, title string, owner int64) models.Book {
		b := models.Book{
			ForeignID: fid, AuthorID: a.ID, Title: title, SortTitle: title, Status: models.BookStatusWanted,
			Genres: []string{}, MetadataProvider: "ol", Monitored: true, MediaType: models.MediaTypeAudiobook, OwnerUserID: owner,
		}
		if err := books.Create(ctx, &b); err != nil {
			t.Fatalf("book create %s: %v", title, err)
		}
		return b
	}
	wanted := mk("OL-3268-OWN-1", "King of Scars", u1.ID)
	mk("OL-3268-OWN-2", "Rule of Wolves", u1.ID)
	mk("OL-3268-OWN-3", "Bobs Private Title", u2.ID)

	ss := &fixedResultsSearcher{}
	s := &Scheduler{
		searcher: ss, indexers: db.NewIndexerRepo(database), authors: authors, books: books,
		settings: db.NewSettingsRepo(database), blocklist: db.NewBlocklistRepo(database),
		downloads: db.NewDownloadRepo(database), clients: db.NewDownloadClientRepo(database),
	}
	s.searchAndGrabFormat(ctx, wanted, models.MediaTypeAudiobook, nil)
	if !slices.Equal(ss.lastCrit.AuthorTitles, []string{"Rule of Wolves"}) {
		t.Errorf("AuthorTitles = %v, want only the owner's other title", ss.lastCrit.AuthorTitles)
	}

	// A book with no owner is not scoped.
	wanted.OwnerUserID = 0
	s.searchAndGrabFormat(ctx, wanted, models.MediaTypeAudiobook, nil)
	if len(ss.lastCrit.AuthorTitles) != 2 {
		t.Errorf("unowned book: AuthorTitles = %v, want both other titles", ss.lastCrit.AuthorTitles)
	}
}
