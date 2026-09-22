package scheduler

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/vavallee/bindery/internal/db"
	"github.com/vavallee/bindery/internal/indexer"
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

// rankedQualityFixture is qualityFixture with a real indexer.Searcher in front
// of an httptest Newznab server, so the results pass through rankResults the
// way a live sweep's do. The stub searcher above returns its slice as given
// and cannot show which release the ranking put first.
func rankedQualityFixture(t *testing.T, items []models.QualityItem, mediaType string, titles ...string) (*Scheduler, *db.DownloadRepo, models.Book) {
	t.Helper()
	s, downloads, book := qualityFixture(t, true, items, titles...)

	var b strings.Builder
	b.WriteString(`<?xml version="1.0" encoding="UTF-8"?>
<rss version="2.0" xmlns:newznab="http://www.newznab.com/DTD/2010/feeds/attributes/">
  <channel>
    <newznab:response offset="0" total="` + strconv.Itoa(len(titles)) + `"/>`)
	for _, title := range titles {
		b.WriteString(`
    <item>
      <title>` + title + `</title>
      <guid isPermaLink="false">guid-` + title + `</guid>
      <enclosure url="http://127.0.0.1:1/nzb" length="1000" type="application/x-nzb"/>
    </item>`)
	}
	b.WriteString(`
  </channel>
</rss>`)
	rss := b.String()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/xml")
		_, _ = w.Write([]byte(rss))
	}))
	t.Cleanup(srv.Close)

	ctx := context.Background()
	idxs, err := s.indexers.List(ctx)
	if err != nil {
		t.Fatalf("list indexers: %v", err)
	}
	for i := range idxs {
		idxs[i].URL = srv.URL
		if mediaType == models.MediaTypeAudiobook {
			idxs[i].Categories = []int{3030}
		} else {
			idxs[i].Categories = []int{7020}
		}
		if err := s.indexers.Update(ctx, &idxs[i]); err != nil {
			t.Fatalf("point indexer at the stub server: %v", err)
		}
	}
	if mediaType == models.MediaTypeAudiobook {
		book.MediaType = models.MediaTypeAudiobook
	}
	s.searcher = indexer.NewSearcher()
	return s, downloads, book
}

// TestSearchAndGrabFormat_ProfileOrderPicksTopTicked is the headline #2733
// case: the profile lists pdf above epub, both ticked, so the sweep grabs the
// pdf. QualityRank alone would grab the epub.
func TestSearchAndGrabFormat_ProfileOrderPicksTopTicked(t *testing.T) {
	ctx := context.Background()
	s, downloads, book := rankedQualityFixture(t, []models.QualityItem{
		{Quality: "pdf", Allowed: true},
		{Quality: "epub", Allowed: true},
	}, models.MediaTypeEbook, "Quality.Book.2024.epub", "Quality.Book.2024.pdf")

	s.searchAndGrabFormat(ctx, book, models.MediaTypeEbook, nil)

	rows, err := downloads.List(ctx)
	if err != nil {
		t.Fatalf("downloads list: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("expected exactly one grab, got %d", len(rows))
	}
	if rows[0].GUID != "guid-Quality.Book.2024.pdf" {
		t.Errorf("grabbed %q, want the pdf: the profile ranks it above epub", rows[0].GUID)
	}
}

// TestSearchAndGrabFormat_ProfileOrderPicksTopTickedAudiobook is the audio
// twin: mp3 above m4b in the profile grabs the mp3, although QualityRank puts
// m4b above mp3.
func TestSearchAndGrabFormat_ProfileOrderPicksTopTickedAudiobook(t *testing.T) {
	ctx := context.Background()
	s, downloads, book := rankedQualityFixture(t, []models.QualityItem{
		{Quality: "mp3", Allowed: true},
		{Quality: "m4b", Allowed: true},
	}, models.MediaTypeAudiobook, "Quality.Book.2024.m4b", "Quality.Book.2024.mp3")

	s.searchAndGrabFormat(ctx, book, models.MediaTypeAudiobook, nil)

	rows, err := downloads.List(ctx)
	if err != nil {
		t.Fatalf("downloads list: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("expected exactly one grab, got %d", len(rows))
	}
	if rows[0].GUID != "guid-Quality.Book.2024.mp3" {
		t.Errorf("grabbed %q, want the mp3: the profile ranks it above m4b", rows[0].GUID)
	}
}
