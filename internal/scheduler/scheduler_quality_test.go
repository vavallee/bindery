package scheduler

import (
	"context"
	"testing"

	"github.com/vavallee/bindery/internal/db"
	"github.com/vavallee/bindery/internal/indexer/newznab"
	"github.com/vavallee/bindery/internal/models"
)

// qualityFixture builds a DB-backed scheduler whose author is bound to a
// quality profile, plus one enabled usenet client so an approved release
// proceeds all the way to a download record. Mirrors languageFixture: the
// client points at 127.0.0.1:1 so the send fails fast, but the download row is
// created first and is what these tests assert on.
//
// A nil items slice means "author has a profile row but no format list"; pass
// attachProfile=false to leave the author with no profile at all.
func qualityFixture(t *testing.T, attachProfile bool, items []models.QualityItem, titles ...string) (*Scheduler, *db.DownloadRepo, models.Book) {
	t.Helper()
	database, err := db.OpenMemory()
	if err != nil {
		t.Fatalf("OpenMemory: %v", err)
	}
	t.Cleanup(func() { database.Close() })

	ctx := context.Background()
	authors := db.NewAuthorRepo(database)
	books := db.NewBookRepo(database)
	clients := db.NewDownloadClientRepo(database)
	downloads := db.NewDownloadRepo(database)
	qualityProfiles := db.NewQualityProfileRepo(database)

	a := &models.Author{
		ForeignID: "OL-QP-A", Name: "Quality Author", SortName: "Author, Quality",
		MetadataProvider: "ol", Monitored: true,
	}
	if attachProfile {
		p := &models.QualityProfile{Name: "EPUB only", Cutoff: "epub", Items: items}
		if err := qualityProfiles.Create(ctx, p); err != nil {
			t.Fatalf("quality profile create: %v", err)
		}
		a.QualityProfileID = &p.ID
	}
	if err := authors.Create(ctx, a); err != nil {
		t.Fatalf("author create: %v", err)
	}
	book := models.Book{
		ForeignID: "OL-QP-B", AuthorID: a.ID, Title: "Quality Book",
		SortTitle: "Quality Book", Status: models.BookStatusWanted,
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
	results := make([]newznab.SearchResult, 0, len(titles))
	for _, title := range titles {
		results = append(results, newznab.SearchResult{
			GUID: "guid-" + title, Title: title, IndexerID: idx.ID,
			NZBURL: "http://127.0.0.1:1/nzb", Protocol: "usenet",
		})
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
	}
	// Attach through the same setter main.go uses, so the wiring is exercised
	// rather than bypassed by poking the field.
	s.WithQualityProfiles(qualityProfiles)
	return s, downloads, book
}

// epubOnly is the profile shape the UI produces when a user ticks EPUB and
// unticks the rest: every format is listed, only some are allowed.
func epubOnly() []models.QualityItem {
	return []models.QualityItem{
		{Quality: "pdf", Allowed: false},
		{Quality: "mobi", Allowed: false},
		{Quality: "epub", Allowed: true},
		{Quality: "azw3", Allowed: false},
	}
}

// TestSearchAndGrabFormat_RejectsDisallowedFormat is the #1693 regression test.
//
// decision.QualityAllowed existed but was never constructed anywhere in
// production code, so a quality profile's "Allowed formats" checkboxes did
// nothing at grab time: models.QualityRank scoring is a ranking PREFERENCE, and
// a disallowed format was grabbed happily whenever it was the only or
// best-scoring candidate. The UI presents those checkboxes as a hard
// allow-list, so users believed they were protected when they were not.
func TestSearchAndGrabFormat_RejectsDisallowedFormat(t *testing.T) {
	ctx := context.Background()
	s, downloads, book := qualityFixture(t, true, epubOnly(), "Quality.Book.2024.pdf")

	s.searchAndGrabFormat(ctx, book, models.MediaTypeEbook, nil)

	rows, err := downloads.List(ctx)
	if err != nil {
		t.Fatalf("downloads list: %v", err)
	}
	if len(rows) != 0 {
		t.Errorf("pdf release must not be grabbed under an EPUB-only profile, got %d download(s)", len(rows))
	}
}

// TestSearchAndGrabFormat_GrabsAllowedFormat is the control: the filter must
// not become a blanket block.
func TestSearchAndGrabFormat_GrabsAllowedFormat(t *testing.T) {
	ctx := context.Background()
	s, downloads, book := qualityFixture(t, true, epubOnly(), "Quality.Book.2024.epub")

	s.searchAndGrabFormat(ctx, book, models.MediaTypeEbook, nil)

	rows, err := downloads.List(ctx)
	if err != nil {
		t.Fatalf("downloads list: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("epub release should be grabbed under an EPUB-only profile, got %d download(s)", len(rows))
	}
}

// TestSearchAndGrabFormat_PicksAllowedOverBetterRankedDisallowed pins the
// distinction the bug was really about: azw3 OUTRANKS epub in
// models.QualityRank, so scoring alone would pick it. The allow-list has to
// override the ranking preference, not merely tie-break it.
func TestSearchAndGrabFormat_PicksAllowedOverBetterRankedDisallowed(t *testing.T) {
	ctx := context.Background()
	s, downloads, book := qualityFixture(t, true, epubOnly(),
		"Quality.Book.2024.azw3", "Quality.Book.2024.epub")

	s.searchAndGrabFormat(ctx, book, models.MediaTypeEbook, nil)

	rows, err := downloads.List(ctx)
	if err != nil {
		t.Fatalf("downloads list: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("expected exactly one grab, got %d", len(rows))
	}
	if rows[0].GUID != "guid-Quality.Book.2024.epub" {
		t.Errorf("grabbed %q, want the epub — azw3 outranks epub but is disallowed", rows[0].GUID)
	}
}

// TestSearchAndGrabFormat_UntaggedReleasePasses pins the deliberate fail-open
// case. ParseRelease only sets Format when the title contains a known token,
// and many legitimate Usenet titles carry none. Rejecting those would turn any
// ticked checkbox into a near-total grab blackout — the filter can only speak
// to formats it can actually see.
func TestSearchAndGrabFormat_UntaggedReleasePasses(t *testing.T) {
	ctx := context.Background()
	s, downloads, book := qualityFixture(t, true, epubOnly(), "Quality Author - Quality Book (2024)")

	s.searchAndGrabFormat(ctx, book, models.MediaTypeEbook, nil)

	rows, err := downloads.List(ctx)
	if err != nil {
		t.Fatalf("downloads list: %v", err)
	}
	if len(rows) != 1 {
		t.Errorf("a release with no parseable format must not be blocked, got %d download(s)", len(rows))
	}
}

// TestSearchAndGrabFormat_NoProfileIsUnfiltered pins that authors without a
// quality profile keep the pre-#1693 behaviour: no format filtering at all.
func TestSearchAndGrabFormat_NoProfileIsUnfiltered(t *testing.T) {
	ctx := context.Background()
	s, downloads, book := qualityFixture(t, false, nil, "Quality.Book.2024.pdf")

	s.searchAndGrabFormat(ctx, book, models.MediaTypeEbook, nil)

	rows, err := downloads.List(ctx)
	if err != nil {
		t.Fatalf("downloads list: %v", err)
	}
	if len(rows) != 1 {
		t.Errorf("an author with no quality profile must not be format-filtered, got %d download(s)", len(rows))
	}
}

// TestSearchAndGrabFormat_EmptyItemsIsUnfiltered covers the seeded profiles,
// which ship with items='[]'. An empty list means "allow all", so wiring the
// spec in must be a no-op for a default install.
func TestSearchAndGrabFormat_EmptyItemsIsUnfiltered(t *testing.T) {
	ctx := context.Background()
	s, downloads, book := qualityFixture(t, true, nil, "Quality.Book.2024.pdf")

	s.searchAndGrabFormat(ctx, book, models.MediaTypeEbook, nil)

	rows, err := downloads.List(ctx)
	if err != nil {
		t.Fatalf("downloads list: %v", err)
	}
	if len(rows) != 1 {
		t.Errorf("a profile with no format list means allow-all, got %d download(s)", len(rows))
	}
}

// audiobookOnly is the profile shape a user gets when they build a profile for
// an author they collect on audio: only audiobook containers listed, nothing
// said about ebooks either way.
func audiobookOnly() []models.QualityItem {
	return []models.QualityItem{
		{Quality: "mp3", Allowed: true},
		{Quality: "m4a", Allowed: false},
		{Quality: "m4b", Allowed: true},
		{Quality: "flac", Allowed: false},
	}
}

// TestSearchAndGrabFormat_AudiobookProfileDoesNotBlockEbookGrabs is the half of
// #2307 with real consequences.
//
// quality_profile_id lives on authors, so an author tracked in both formats has
// exactly one profile. With an audiobook profile attached, QualityAllowed
// rejected every ebook release as "not in quality profile", and because the
// scheduler uses the spec as a hard filter the book could never be auto-grabbed
// at all. Interactive search only annotates, so that half was merely
// misleading; this half was silent.
func TestSearchAndGrabFormat_AudiobookProfileDoesNotBlockEbookGrabs(t *testing.T) {
	ctx := context.Background()
	s, downloads, book := qualityFixture(t, true, audiobookOnly(), "Quality.Book.2024.epub")

	s.searchAndGrabFormat(ctx, book, models.MediaTypeEbook, nil)

	rows, err := downloads.List(ctx)
	if err != nil {
		t.Fatalf("downloads list: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("an audiobook-only profile must not block an ebook grab, got %d download(s)", len(rows))
	}
}

// TestSearchAndGrabFormat_AudiobookProfileStillFiltersAudiobooks is the control
// for the test above: within the media type the profile does list, the
// allow-list is still authoritative. m4a is listed and unticked, so the grab
// must not happen.
func TestSearchAndGrabFormat_AudiobookProfileStillFiltersAudiobooks(t *testing.T) {
	ctx := context.Background()
	s, downloads, book := qualityFixture(t, true, audiobookOnly(), "Quality.Book.2024.m4a")

	s.searchAndGrabFormat(ctx, book, models.MediaTypeAudiobook, nil)

	rows, err := downloads.List(ctx)
	if err != nil {
		t.Fatalf("downloads list: %v", err)
	}
	if len(rows) != 0 {
		t.Errorf("m4a is unticked in the profile and must stay rejected, got %d download(s)", len(rows))
	}
}
