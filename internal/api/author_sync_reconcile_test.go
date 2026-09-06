package api

import (
	"context"
	"testing"
	"time"

	"github.com/vavallee/bindery/internal/db"
	"github.com/vavallee/bindery/internal/metadata"
	"github.com/vavallee/bindery/internal/models"
)

// Every provider work leaves fetchAuthorBooks through exactly one counted
// path. This test runs the real loop over a catalogue built to trip each of
// them at once and asserts the counts add back up to Total.
//
// It exists because they did not (#2449). Two outcomes had no counter: a work
// that resolved to a book already in the library, which on an established
// author is almost the whole run, and a create whose write failed. With both
// invisible, a real author reported total=106, added=1, skippedLanguage=2, and
// the only honest reading of that was that 103 works had vanished. They had
// not; 102 of them were already on the shelf and the summary had no word for
// it. An arithmetic assertion is what makes that unrepeatable.
func TestAuthorSyncSummaryReconciles(t *testing.T) {
	ctx := context.Background()
	database, err := db.OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { database.Close() })

	authorRepo := db.NewAuthorRepo(database)
	bookRepo := db.NewBookRepo(database)
	profileRepo := db.NewMetadataProfileRepo(database)

	// Every filter armed at once. A profile this strict is unusual, and that is
	// the point: one run has to exercise paths that are normally spread over
	// several installs.
	profile := &models.MetadataProfile{
		Name:                    "Everything on",
		AllowedLanguages:        "eng",
		UnknownLanguageBehavior: models.UnknownLanguageFail,
		SkipPartBooks:           true,
		SkipMissingDate:         true,
		MinPages:                100,
		SkipMissingISBN:         true,
	}
	if err := profileRepo.Create(ctx, profile); err != nil {
		t.Fatal(err)
	}

	author := &models.Author{
		ForeignID: "OL2449A", Name: "Prolix Author", SortName: "Author, Prolix",
		MetadataProvider: "openlibrary", MetadataProfileID: &profile.ID,
	}
	if err := authorRepo.Create(ctx, author); err != nil {
		t.Fatal(err)
	}

	// Two books already in the library, one to be matched by foreign id and one
	// by normalised title under a foreign id the library has never seen. Both
	// are the ordinary case a refresh spends most of its time on.
	held := []*models.Book{
		{AuthorID: author.ID, ForeignID: "OL-held-id", Title: "The Held One", SortTitle: "held one",
			Language: "eng", Status: models.BookStatusWanted, MetadataProvider: "openlibrary", Genres: []string{}},
		{AuthorID: author.ID, ForeignID: "OL-held-title", Title: "The Retitled One", SortTitle: "retitled one",
			Language: "eng", Status: models.BookStatusWanted, MetadataProvider: "openlibrary", Genres: []string{}},
	}
	for _, b := range held {
		if err := bookRepo.Create(ctx, b); err != nil {
			t.Fatal(err)
		}
	}

	released := time.Date(2020, 3, 1, 0, 0, 0, 0, time.UTC)
	pages := func(n int) *int { return &n }
	isbn := func(s string) *string { return &s }
	fat := []models.Edition{{NumPages: pages(400), ISBN13: isbn("9780000000001")}}
	thin := []models.Edition{{NumPages: pages(12), ISBN13: isbn("9780000000002")}}
	noISBN := []models.Edition{{NumPages: pages(400)}}

	work := func(id, title, lang string, date *time.Time, ratings int) models.Book {
		return models.Book{
			ForeignID: id, Title: title, SortTitle: title, Language: lang,
			ReleaseDate: date, RatingsCount: ratings, AverageRating: 4,
			Status: models.BookStatusWanted, MetadataProvider: "openlibrary", Genres: []string{},
		}
	}

	works := []models.Book{
		work("OL-keeper", "A Book Worth Adding", "eng", &released, 500),
		work("OL-lang", "Un Livre", "fre", &released, 500),
		work("OL-junk", "Prolix Author", "eng", &released, 500),
		work("OL-part", "The Saga: Books 1-3", "eng", &released, 500),
		work("OL-nodate", "A Book With No Date", "eng", nil, 500),
		work("OL-thin", "A Very Short Book", "eng", &released, 500),
		work("OL-noisbn", "A Book With No ISBN", "eng", &released, 500),
		// The two already-held works. The second arrives under a foreign id the
		// library has never held, so only the title index can connect it.
		work("OL-held-id", "The Held One", "eng", &released, 500),
		work("OL-reissued", "The Retitled One", "eng", &released, 500),
	}

	provider := &stubMetaProvider{
		works: works,
		editionsByBook: map[string][]models.Edition{
			"OL-keeper":   fat,
			"OL-thin":     thin,
			"OL-noisbn":   noISBN,
			"OL-part":     fat,
			"OL-unpop":    fat,
			"OL-nodate":   fat,
			"OL-lang":     fat,
			"OL-junk":     fat,
			"OL-held-id":  fat,
			"OL-reissued": fat,
		},
	}
	h := NewAuthorHandler(authorRepo, nil, bookRepo, nil, metadata.NewAggregator(provider), nil, profileRepo, nil)
	h.FetchAuthorBooks(author, false, "")

	sync := h.syncSummaries.get(author.ID)
	if sync == nil {
		t.Fatal("no summary recorded for the sync")
	}

	// Report the whole picture on failure. A bare "want 0, got 3" on the
	// reconciliation is the least useful possible version of this message,
	// because the interesting part is which bucket is short.
	report := func() string {
		return "" +
			"\n  total                 " + itoa(sync.Total) +
			"\n  added                 " + itoa(sync.Added) +
			"\n  matched               " + itoa(sync.Matched) +
			"\n  failed                " + itoa(sync.Failed) +
			"\n  skippedLanguage       " + itoa(sync.SkippedLanguage) +
			"\n  skippedJunk           " + itoa(sync.SkippedJunk) +
			"\n  skippedMediaType      " + itoa(sync.SkippedMediaType) +
			"\n  skippedNotAccepted    " + itoa(sync.SkippedNotAccepted) +
			"\n  skippedExcluded       " + itoa(sync.SkippedExcluded) +
			"\n  skippedPartBooks      " + itoa(sync.SkippedPartBooks) +
			"\n  skippedMissingDate    " + itoa(sync.SkippedMissingDate) +
			"\n  skippedMinPages       " + itoa(sync.SkippedMinPages) +
			"\n  skippedMissingIsbn    " + itoa(sync.SkippedMissingISBN) +
			"\n  UNACCOUNTED           " + itoa(sync.Unaccounted())
	}

	for _, tc := range []struct {
		name string
		got  int
		want int
	}{
		{"total", sync.Total, len(works)},
		{"added", sync.Added, 1},
		{"matched", sync.Matched, 2},
		{"failed", sync.Failed, 0},
		{"skippedLanguage", sync.SkippedLanguage, 1},
		{"skippedJunk", sync.SkippedJunk, 1},
		{"skippedPartBooks", sync.SkippedPartBooks, 1},
		{"skippedMissingDate", sync.SkippedMissingDate, 1},
		{"skippedMinPages", sync.SkippedMinPages, 1},
		{"skippedMissingIsbn", sync.SkippedMissingISBN, 1},
	} {
		if tc.got != tc.want {
			t.Errorf("%s = %d, want %d%s", tc.name, tc.got, tc.want, report())
		}
	}

	if sync.Unaccounted() != 0 {
		t.Errorf("%d of %d works left the sync through a path with no counter.%s",
			sync.Unaccounted(), sync.Total, report())
	}
}

// itoa keeps the failure report above free of an fmt import for one call site.
func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}

// The two counters the strict-profile run above cannot reach: a refresh of an
// author who is not taking newly discovered books, and a work whose title
// belongs to a book the user excluded by hand.
//
// SkippedExcluded is the one Skipped* the notice deliberately does not render
// (internal/api/authors.go, the counter declaration). Reconciling Total is a
// separate question from deciding what to put on the page, and this asserts
// the first without disturbing the second.
func TestAuthorSyncSummaryReconcilesOnNonAcceptingRefresh(t *testing.T) {
	ctx := context.Background()
	database, err := db.OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { database.Close() })

	authorRepo := db.NewAuthorRepo(database)
	bookRepo := db.NewBookRepo(database)
	profileRepo := db.NewMetadataProfileRepo(database)

	profile := &models.MetadataProfile{Name: "Permissive", AllowedLanguages: "eng"}
	if err := profileRepo.Create(ctx, profile); err != nil {
		t.Fatal(err)
	}

	// Unmonitored, so authorAcceptsDiscoveredBooks says no. It still needs a
	// book, or authorAwaitsFirstCatalogue treats it as the empty-author repair
	// case and discovers anyway.
	author := &models.Author{
		ForeignID: "OL2449B", Name: "Retired Author", SortName: "Author, Retired",
		MetadataProvider: "openlibrary", MetadataProfileID: &profile.ID, Monitored: false,
	}
	if err := authorRepo.Create(ctx, author); err != nil {
		t.Fatal(err)
	}

	kept := &models.Book{AuthorID: author.ID, ForeignID: "OL-kept", Title: "Still Here", SortTitle: "still here",
		Language: "eng", Status: models.BookStatusWanted, MetadataProvider: "openlibrary", Genres: []string{}}
	if err := bookRepo.Create(ctx, kept); err != nil {
		t.Fatal(err)
	}
	dropped := &models.Book{AuthorID: author.ID, ForeignID: "OL-dropped", Title: "Never Again", SortTitle: "never again",
		Language: "eng", Status: models.BookStatusWanted, MetadataProvider: "openlibrary", Genres: []string{}}
	if err := bookRepo.Create(ctx, dropped); err != nil {
		t.Fatal(err)
	}
	// BookRepo.Create does not write the excluded column; exclusion is its own
	// setter. Creating with Excluded:true silently produces an included book,
	// which is how this test first "passed" the wrong way round.
	if err := bookRepo.SetExcluded(ctx, dropped.ID, true); err != nil {
		t.Fatal(err)
	}

	released := time.Date(2020, 3, 1, 0, 0, 0, 0, time.UTC)
	work := func(id, title string) models.Book {
		return models.Book{
			ForeignID: id, Title: title, SortTitle: title, Language: "eng",
			ReleaseDate: &released, Status: models.BookStatusWanted,
			MetadataProvider: "openlibrary", Genres: []string{},
		}
	}
	works := []models.Book{
		work("OL-kept", "Still Here"),
		// A re-ided provider record for the excluded title. The exclusion is
		// keyed on the title precisely so a new foreign id cannot smuggle it
		// back in (#1815).
		work("OL-dropped-reissue", "Never Again"),
		work("OL-new-one", "A Brand New Book"),
		work("OL-new-two", "Another Brand New Book"),
	}

	h := NewAuthorHandler(authorRepo, nil, bookRepo, nil,
		metadata.NewAggregator(&stubMetaProvider{works: works}), nil, profileRepo, nil)
	h.RefreshAuthorBooks(author, false, "")

	sync := h.syncSummaries.get(author.ID)
	if sync == nil {
		t.Fatal("no summary recorded for the refresh")
	}
	if sync.Total != len(works) {
		t.Errorf("total = %d, want %d", sync.Total, len(works))
	}
	if sync.Added != 0 {
		t.Errorf("added = %d, want 0: an unmonitored author must not grow", sync.Added)
	}
	if sync.Matched != 1 {
		t.Errorf("matched = %d, want 1 (the book already in the library)", sync.Matched)
	}
	if sync.SkippedExcluded != 1 {
		t.Errorf("skippedExcluded = %d, want 1 (the re-ided excluded title)", sync.SkippedExcluded)
	}
	if sync.SkippedNotAccepted != 2 {
		t.Errorf("skippedNotAccepted = %d, want 2", sync.SkippedNotAccepted)
	}
	// The excluded book stays out of the notice's own sum, so the page does not
	// ask the user to explain a decision they already made.
	if sync.SkippedTotal() != 2 {
		t.Errorf("SkippedTotal() = %d, want 2: the excluded title must not be counted as something to explain", sync.SkippedTotal())
	}
	if sync.Unaccounted() != 0 {
		t.Errorf("%d of %d works left the refresh through a path with no counter", sync.Unaccounted(), sync.Total)
	}
}
