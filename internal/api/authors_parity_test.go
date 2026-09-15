package api

import (
	"context"
	"testing"
	"time"

	"github.com/vavallee/bindery/internal/db"
	"github.com/vavallee/bindery/internal/metadata"
	"github.com/vavallee/bindery/internal/models"
)

// TestAuthorSyncParity_DefaultVetoWeightsMatchExpectedCounts is a small,
// hardcoded-expectation fixture (3 works, one each tripping language and
// junk-title) at the exact migration-086 shipped default (keep_threshold =
// exclude_threshold = 0). It is NOT the golden comparison against the
// pre-#2235 boolean chain — that claim belongs to
// TestAuthorSyncSummaryReconciles (author_sync_reconcile_test.go), which
// exercises every ported signal at once against a catalogue built to trip
// each one and passed UNMODIFIED once filterengine replaced the inline
// boolean checks; that's the actual evidence the wiring didn't shift
// behavior. This test only pins that a small, easy-to-read fixture produces
// the counts you'd expect at the shipped default, as a second, independent
// data point — not a re-derivation of parity from first principles.
//
// This test does NOT sweep other equal threshold pairs — see
// TestAuthorSyncParity_NonZeroEqualThresholdsDiverge below for why "keep ==
// exclude" alone is not the actual parity invariant, and
// validateScoreThresholds (internal/api/metadata_profiles.go) enforces the
// narrower one this test relies on.
func TestAuthorSyncParity_DefaultVetoWeightsMatchExpectedCounts(t *testing.T) {
	released := time.Date(2020, 3, 1, 0, 0, 0, 0, time.UTC)
	baseWorks := []models.Book{
		{ForeignID: "OL-keep", Title: "A Real Book", SortTitle: "A Real Book", Language: "eng",
			ReleaseDate: &released, Status: models.BookStatusWanted, MetadataProvider: "openlibrary", Genres: []string{}},
		{ForeignID: "OL-lang", Title: "Un Livre", SortTitle: "Un Livre", Language: "fre",
			ReleaseDate: &released, Status: models.BookStatusWanted, MetadataProvider: "openlibrary", Genres: []string{}},
		{ForeignID: "OL-junk", Title: "Prolix Author", SortTitle: "Prolix Author", Language: "eng",
			ReleaseDate: &released, Status: models.BookStatusWanted, MetadataProvider: "openlibrary", Genres: []string{}},
	}

	runAt := func(t *testing.T, keep, exclude float64) (added, skippedLang, skippedJunk int) {
		t.Helper()
		ctx := context.Background()
		database, err := db.OpenMemory()
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { database.Close() })

		authorRepo := db.NewAuthorRepo(database)
		bookRepo := db.NewBookRepo(database)
		profileRepo := db.NewMetadataProfileRepo(database)

		profile := &models.MetadataProfile{
			Name: "Parity", AllowedLanguages: "eng",
			KeepThreshold: keep, ExcludeThreshold: exclude,
		}
		if err := profileRepo.Create(ctx, profile); err != nil {
			t.Fatal(err)
		}
		author := &models.Author{
			ForeignID: "OL-parity", Name: "Prolix Author", SortName: "Author, Prolix",
			MetadataProvider: "openlibrary", MetadataProfileID: &profile.ID,
		}
		if err := authorRepo.Create(ctx, author); err != nil {
			t.Fatal(err)
		}

		provider := &stubMetaProvider{works: baseWorks}
		h := NewAuthorHandler(authorRepo, nil, bookRepo, nil, metadata.NewAggregator(provider), nil, profileRepo, nil)
		h.FetchAuthorBooks(author, false, "")

		sync := h.syncSummaries.get(author.ID)
		if sync == nil {
			t.Fatal("no summary recorded for the sync")
		}
		return sync.Added, sync.SkippedLanguage, sync.SkippedJunk
	}

	added, lang, junk := runAt(t, 0, 0)
	if added != 1 || lang != 1 || junk != 1 {
		t.Errorf("at the shipped 0/0 default: added=%d skippedLanguage=%d skippedJunk=%d, want 1/1/1 "+
			"(1 real book kept, the French book language-excluded, the author-name-titled work junk-excluded)",
			added, lang, junk)
	}
}

// TestAuthorSyncParity_NonZeroEqualThresholdsDiverge documents a real
// correctness finding from building this test suite, not a hypothetical:
// keep_threshold == exclude_threshold alone does NOT reproduce the pre-#2235
// boolean chain. It only does at exactly 0, because every v1 signal is a
// veto with Context.Prior hardcoded to 0 — an unfiltered candidate always
// scores exactly 0, so the shared threshold has to sit exactly there for a
// clean candidate to land KEEP. A nonzero equal pair (this test uses 50/50)
// excludes every candidate, filtered or not, because 0 < 50.
//
// This is exactly why validateScoreThresholds rejects any nonzero value,
// not merely an unequal pair — an earlier version of that function only
// checked ExcludeThreshold != KeepThreshold, which this exact scenario
// caught as broken. This test constructs the profile through the repo
// directly (bypassing the API's validation) specifically to demonstrate
// what that validation exists to prevent, matching the failure mode it
// guards against rather than asserting the guard itself (that's
// TestMetaProfileCreate_RejectsNonZeroEqualThresholds in
// metadata_profiles_test.go).
func TestAuthorSyncParity_NonZeroEqualThresholdsDiverge(t *testing.T) {
	ctx := context.Background()
	database, err := db.OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { database.Close() })

	authorRepo := db.NewAuthorRepo(database)
	bookRepo := db.NewBookRepo(database)
	profileRepo := db.NewMetadataProfileRepo(database)

	// Bypasses the API's validateScoreThresholds on purpose (see doc above).
	profile := &models.MetadataProfile{
		Name: "Broken", AllowedLanguages: "eng",
		KeepThreshold: 50, ExcludeThreshold: 50,
	}
	if err := profileRepo.Create(ctx, profile); err != nil {
		t.Fatal(err)
	}
	author := &models.Author{
		ForeignID: "OL-broken", Name: "Real Author", SortName: "Author, Real",
		MetadataProvider: "openlibrary", MetadataProfileID: &profile.ID,
	}
	if err := authorRepo.Create(ctx, author); err != nil {
		t.Fatal(err)
	}

	released := time.Date(2020, 3, 1, 0, 0, 0, 0, time.UTC)
	provider := &stubMetaProvider{works: []models.Book{
		{ForeignID: "OL-clean", Title: "A Perfectly Ordinary Book", SortTitle: "A Perfectly Ordinary Book",
			Language: "eng", ReleaseDate: &released, Status: models.BookStatusWanted,
			MetadataProvider: "openlibrary", Genres: []string{}},
	}}
	h := NewAuthorHandler(authorRepo, nil, bookRepo, nil, metadata.NewAggregator(provider), nil, profileRepo, nil)
	h.FetchAuthorBooks(author, false, "")

	sync := h.syncSummaries.get(author.ID)
	if sync == nil {
		t.Fatal("no summary recorded for the sync")
	}
	// This IS the divergence: a book with nothing wrong with it — no filter
	// would have touched it under the pre-#2235 boolean chain — is excluded
	// solely because the profile's thresholds are nonzero. added should be 1
	// under correct (0/0) behavior; it is 0 here, which is the point.
	if sync.Added != 0 {
		t.Fatalf("expected the nonzero-threshold bug to reproduce (added=0), got added=%d — "+
			"either the bug this test documents was fixed at the engine level (update this test and its doc) "+
			"or something else changed", sync.Added)
	}
}

// TestAuthorSyncParity_ProviderNoiseDoesNotStealMonitorLatestSlot is the
// regression test for a real parity break found reviewing #2235, NOT a
// hypothetical: the branch's headline claim is that the shipped 0/0 default
// changes no observable behavior except AuthorSyncSummary.Total, but moving
// OpenLibrary's companion-material check from "drop in the provider client"
// to "flag and let the engine decide" also, silently, put those works into
// every collective-inference stage that runs over the RAW provider slice
// before the create loop.
//
// latestBookMonitorKeys is the damaging one. It awards the author's
// MonitorLatestCount slots from that raw slice, by release date, before any
// filtering. Companion material is typically published long after the work it
// accompanies, so a flagged study guide wins the auction, is then excluded by
// ProviderNoiseSignal in the loop, and the real book it displaced is created
// UNMONITORED — no auto-search, no grab, and no counter anywhere saying why.
//
// The fix is in isAuthorWorkMonitorCandidate: a work the create loop will
// refuse to create must not be able to take a slot first.
func TestAuthorSyncParity_ProviderNoiseDoesNotStealMonitorLatestSlot(t *testing.T) {
	ctx := context.Background()
	database, err := db.OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { database.Close() })

	authorRepo := db.NewAuthorRepo(database)
	bookRepo := db.NewBookRepo(database)
	profileRepo := db.NewMetadataProfileRepo(database)

	author := &models.Author{
		ForeignID: "OL-NOISE-AUTHOR", Name: "Noisy Author", SortName: "Author, Noisy",
		MetadataProvider:   "openlibrary",
		Monitored:          true,
		MonitorMode:        models.AuthorMonitorModeLatest,
		MonitorLatestCount: 1,
		MonitorNewItems:    models.AuthorMonitorNewItemsAll,
	}
	if err := authorRepo.Create(ctx, author); err != nil {
		t.Fatal(err)
	}

	older := time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC)
	newer := time.Date(2023, 1, 1, 0, 0, 0, 0, time.UTC)
	stub := &stubMetaProvider{works: []models.Book{
		{ForeignID: "OL-REAL", Title: "A Real Novel", SortTitle: "A Real Novel", Language: "eng",
			ReleaseDate: &older, Status: models.BookStatusWanted, MetadataProvider: "openlibrary", Genres: []string{}},
		// Flagged companion material, newer than the real work. Pre-#2235 the
		// OpenLibrary client dropped this before fetchAuthorBooks saw it.
		{ForeignID: "OL-NOISE", Title: "Cliffsnotes on A Real Novel", SortTitle: "Cliffsnotes on A Real Novel",
			Language: "eng", ReleaseDate: &newer, Status: models.BookStatusWanted,
			MetadataProvider: "openlibrary", Genres: []string{},
			Observations: []models.FilterObservation{
				{Signal: models.SignalProviderOpenLibraryNoise, Reason: `title contains the companion-material phrase "cliffsnotes"`},
			}},
	}}
	h := NewAuthorHandler(authorRepo, nil, bookRepo, nil, metadata.NewAggregator(stub), nil, profileRepo, nil)
	h.FetchAuthorBooks(author, false, "")

	books, err := bookRepo.ListByAuthor(ctx, author.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(books) != 1 || books[0].Title != "A Real Novel" {
		t.Fatalf("books = %+v, want exactly the one real novel (the flagged work must not be created)", books)
	}
	if !books[0].Monitored {
		t.Error("the real novel was created unmonitored: a provider-flagged companion-material work " +
			"took the author's only monitor-latest slot, which it could not do before #2235")
	}
}

// TestAuthorSyncParity_MultiSignalCandidateCountsOnceAttributedToStrongest is
// the golden proof the #2235 accumulation rework exists for: a candidate
// that trips more than one signal must still be counted exactly once (never
// double-counted across two Skipped* buckets, which would break
// AccountedFor/Unaccounted reconciliation), attributed to the strongest
// observation in its ledger — which at v1's equal veto magnitude means the
// one earliest in registry order (registry_default.go: media type, junk
// title, provider noise, language, part book, missing date, missing ISBN,
// min pages).
//
// Before this rework, each of fetchAuthorBooks's Decide calls banded and
// `continue`d independently, so a candidate tripping two filters was
// attributed to whichever call site the loop happened to reach first — which
// for these fixtures is the SAME registry-order-earliest signal, meaning a
// black-box counter check alone cannot distinguish the old (first-checked-
// wins) implementation from the new (accumulate-then-attribute-strongest)
// one. That distinction is proven white-box, directly on the accumulated
// Result, by TestEvaluateCandidate_AccumulatesEveryFiringSignal
// (authors_filterengine_test.go). This test's job is different: proving the
// full HTTP-level sync loop still reconciles correctly with two independent
// multi-trip fixtures in play, one in each evaluation pass (free signals;
// structural signals).
func TestAuthorSyncParity_MultiSignalCandidateCountsOnceAttributedToStrongest(t *testing.T) {
	ctx := context.Background()
	database, err := db.OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { database.Close() })

	authorRepo := db.NewAuthorRepo(database)
	bookRepo := db.NewBookRepo(database)
	profileRepo := db.NewMetadataProfileRepo(database)

	profile := &models.MetadataProfile{
		Name: "Multi-trip", AllowedLanguages: "eng",
		SkipPartBooks: true, SkipMissingDate: true,
	}
	if err := profileRepo.Create(ctx, profile); err != nil {
		t.Fatal(err)
	}
	author := &models.Author{
		ForeignID: "OL-multitrip", Name: "Prolix Author", SortName: "Author, Prolix",
		MetadataProvider: "openlibrary", MetadataProfileID: &profile.ID,
	}
	if err := authorRepo.Create(ctx, author); err != nil {
		t.Fatal(err)
	}

	released := time.Date(2020, 3, 1, 0, 0, 0, 0, time.UTC)
	works := []models.Book{
		// Trips junk.titleEmptyOrAuthorName AND language.notAllowed (free
		// pass, evaluated together in one Decide call): title equals the
		// author's own name, language is French. Registry order puts junk
		// before language, so this must land in SkippedJunk, not
		// SkippedLanguage.
		{ForeignID: "OL-junk-and-lang", Title: "Prolix Author", SortTitle: "Prolix Author", Language: "fre",
			ReleaseDate: &released, Status: models.BookStatusWanted, MetadataProvider: "openlibrary", Genres: []string{}},
		// Trips structure.partBookTitle AND catalog.missingReleaseDate
		// (structural pass, evaluated together in the same Decide call):
		// box-set title with no release date. Registry order puts part-book
		// before missing-date, so this must land in SkippedPartBooks, not
		// SkippedMissingDate.
		{ForeignID: "OL-part-and-nodate", Title: "The Saga: Books 1-3", SortTitle: "The Saga: Books 1-3", Language: "eng",
			ReleaseDate: nil, Status: models.BookStatusWanted, MetadataProvider: "openlibrary", Genres: []string{}},
		// Clean control: trips nothing, must be added.
		{ForeignID: "OL-clean", Title: "A Perfectly Ordinary Book", SortTitle: "A Perfectly Ordinary Book", Language: "eng",
			ReleaseDate: &released, Status: models.BookStatusWanted, MetadataProvider: "openlibrary", Genres: []string{}},
	}

	provider := &stubMetaProvider{works: works}
	h := NewAuthorHandler(authorRepo, nil, bookRepo, nil, metadata.NewAggregator(provider), nil, profileRepo, nil)
	h.FetchAuthorBooks(author, false, "")

	sync := h.syncSummaries.get(author.ID)
	if sync == nil {
		t.Fatal("no summary recorded for the sync")
	}

	if sync.Added != 1 {
		t.Errorf("Added = %d, want 1 (only the clean control)", sync.Added)
	}
	if sync.SkippedJunk != 1 {
		t.Errorf("SkippedJunk = %d, want 1 (the junk+language candidate, attributed to the registry-earlier signal)", sync.SkippedJunk)
	}
	if sync.SkippedLanguage != 0 {
		t.Errorf("SkippedLanguage = %d, want 0: the junk+language candidate must be counted once, under junk, not twice", sync.SkippedLanguage)
	}
	if sync.SkippedPartBooks != 1 {
		t.Errorf("SkippedPartBooks = %d, want 1 (the part-book+missing-date candidate, attributed to the registry-earlier signal)", sync.SkippedPartBooks)
	}
	if sync.SkippedMissingDate != 0 {
		t.Errorf("SkippedMissingDate = %d, want 0: the part-book+missing-date candidate must be counted once, under part-books, not twice", sync.SkippedMissingDate)
	}
	if sync.Total != len(works) {
		t.Errorf("Total = %d, want %d", sync.Total, len(works))
	}
	if sync.Unaccounted() != 0 {
		t.Errorf("Unaccounted() = %d, want 0 — every work must leave the sync through exactly one counted path even when it trips multiple signals",
			sync.Unaccounted())
	}
}

// TestApplyAuthorMajorityLanguageFallback_IgnoresProviderNoise pins the
// second collective-inference stage the same #2235 change leaked into. The
// majority-language vote also runs over the raw provider slice, so a run of
// English-language companion material could newly carry an otherwise
// non-dominant language over the dominance threshold — changing which real
// works get a language assigned, and therefore which ones the language filter
// then drops.
func TestApplyAuthorMajorityLanguageFallback_IgnoresProviderNoise(t *testing.T) {
	noise := []models.FilterObservation{{Signal: models.SignalProviderOpenLibraryNoise, Reason: "companion material"}}
	books := []models.Book{
		{ForeignID: "1", Language: "fre"},
		{ForeignID: "2", Language: "fre"},
		{ForeignID: "3", Language: "eng", Observations: noise},
		{ForeignID: "4", Language: "eng", Observations: noise},
		{ForeignID: "5", Language: "eng", Observations: noise},
		{ForeignID: "6", Language: "eng", Observations: noise},
		{ForeignID: "7", Language: ""},
	}
	applyAuthorMajorityLanguageFallback(books)
	if books[6].Language != "" {
		t.Errorf("unresolved work was assigned %q: only 2 real works are resolved, which is below the "+
			"minimum sample — the four flagged companion-material works must not vote", books[6].Language)
	}
}
