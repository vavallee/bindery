package scheduler

import (
	"context"
	"testing"

	"github.com/vavallee/bindery/internal/db"
	"github.com/vavallee/bindery/internal/indexer"
	"github.com/vavallee/bindery/internal/indexer/newznab"
	"github.com/vavallee/bindery/internal/models"
)

// fixedResultsSearcher returns a canned result set and records the criteria
// it was called with, so tests can assert what searchAndGrabFormat resolved.
type fixedResultsSearcher struct {
	results  []newznab.SearchResult
	lastCrit indexer.MatchCriteria
}

func (s *fixedResultsSearcher) SearchBook(_ context.Context, _ []models.Indexer, c indexer.MatchCriteria) []newznab.SearchResult {
	s.lastCrit = c
	return s.results
}

// languageFixture builds a DB-backed scheduler whose author is bound to a
// metadata profile restricted to allowedLanguages, plus one enabled usenet
// client so an approved release proceeds all the way to a download record.
// The client points at 127.0.0.1:1, so the send itself fails fast — the
// download row is still created first, which is the observable we assert on.
func languageFixture(t *testing.T, allowedLanguages string, results []newznab.SearchResult) (*Scheduler, *fixedResultsSearcher, *db.DownloadRepo, models.Book) {
	t.Helper()
	database, err := db.OpenMemory()
	if err != nil {
		t.Fatalf("OpenMemory: %v", err)
	}
	t.Cleanup(func() { database.Close() })

	ctx := context.Background()
	profiles := db.NewMetadataProfileRepo(database)
	authors := db.NewAuthorRepo(database)
	books := db.NewBookRepo(database)
	clients := db.NewDownloadClientRepo(database)
	downloads := db.NewDownloadRepo(database)

	p := &models.MetadataProfile{Name: "Restricted", AllowedLanguages: allowedLanguages}
	if err := profiles.Create(ctx, p); err != nil {
		t.Fatalf("profile create: %v", err)
	}
	a := &models.Author{
		ForeignID: "OL-LANG-A", Name: "Autore Prova", SortName: "Prova, Autore",
		MetadataProvider: "ol", Monitored: true, MetadataProfileID: &p.ID,
	}
	if err := authors.Create(ctx, a); err != nil {
		t.Fatalf("author create: %v", err)
	}
	book := models.Book{
		ForeignID: "OL-LANG-B", AuthorID: a.ID, Title: "Il Libro",
		SortTitle: "Il Libro", Status: models.BookStatusWanted,
		Genres: []string{}, MetadataProvider: "ol", Monitored: true,
		MediaType: models.MediaTypeEbook,
	}
	if err := books.Create(ctx, &book); err != nil {
		t.Fatalf("book create: %v", err)
	}
	if err := clients.Create(ctx, &models.DownloadClient{
		Name: "sab", Type: "sabnzbd", Host: "127.0.0.1", Port: 1, Enabled: true,
	}); err != nil {
		t.Fatalf("client create: %v", err)
	}

	// downloads.indexer_id carries an FK, so results need a real indexer row.
	indexers := db.NewIndexerRepo(database)
	idx := &models.Indexer{Name: "stub", Type: "newznab", URL: "http://127.0.0.1:1", Enabled: true}
	if err := indexers.Create(ctx, idx); err != nil {
		t.Fatalf("indexer create: %v", err)
	}
	for i := range results {
		results[i].IndexerID = idx.ID
	}

	ss := &fixedResultsSearcher{results: results}
	s := &Scheduler{
		searcher:  ss,
		indexers:  indexers,
		authors:   authors,
		settings:  db.NewSettingsRepo(database),
		blocklist: db.NewBlocklistRepo(database),
		downloads: downloads,
		clients:   clients,
		profiles:  profiles,
	}
	return s, ss, downloads, book
}

// TestSearchAndGrabFormat_DropsDisallowedLanguage is the regression test for
// Discussion #1572: auto-grab used to ignore the author's metadata-profile
// languages entirely, so an ITA-only profile happily grabbed German releases.
func TestSearchAndGrabFormat_DropsDisallowedLanguage(t *testing.T) {
	ctx := context.Background()
	s, ss, downloads, book := languageFixture(t, "ita", []newznab.SearchResult{
		{GUID: "g-ger", Title: "Das.Buch.GERMAN.epub", NZBURL: "http://127.0.0.1:1/1", Protocol: "usenet"},
	})

	s.searchAndGrabFormat(ctx, book, models.MediaTypeEbook)

	if got := ss.lastCrit.AllowedLanguages; len(got) != 1 || got[0] != "ita" {
		t.Errorf("expected profile languages [ita] on the search criteria, got %v", got)
	}
	rows, err := downloads.List(ctx)
	if err != nil {
		t.Fatalf("downloads list: %v", err)
	}
	if len(rows) != 0 {
		t.Errorf("GERMAN-tagged release must not be grabbed under an ITA-only profile, got %d download(s)", len(rows))
	}
}

// TestSearchAndGrabFormat_GrabsAllowedLanguage is the control: an allowed
// (or untagged) release still proceeds to a download record.
func TestSearchAndGrabFormat_GrabsAllowedLanguage(t *testing.T) {
	ctx := context.Background()
	s, _, downloads, book := languageFixture(t, "ita,eng", []newznab.SearchResult{
		{GUID: "g-ita", Title: "Il.Libro.ITALIANO.epub", NZBURL: "http://127.0.0.1:1/2", Protocol: "usenet"},
	})

	s.searchAndGrabFormat(ctx, book, models.MediaTypeEbook)

	rows, err := downloads.List(ctx)
	if err != nil {
		t.Fatalf("downloads list: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("ITALIANO-tagged release should be grabbed under an ita,eng profile, got %d download(s)", len(rows))
	}
	if rows[0].GUID != "g-ita" {
		t.Errorf("grabbed GUID = %q, want g-ita", rows[0].GUID)
	}
}
