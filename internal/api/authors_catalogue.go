package api

import (
	"context"
	"log/slog"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/vavallee/bindery/internal/concurrency"
	"github.com/vavallee/bindery/internal/indexer"
	"github.com/vavallee/bindery/internal/metadata"
	"github.com/vavallee/bindery/internal/models"
)

// runCatalogueSync is the catalogue sync behind fetchAuthorBooks. It returns
// how many books the run created and, when the provider could not list the
// author's works, that error, so scheduled discovery can tell a rate limit
// from an ordinary run (#2236). Every other early exit returns a nil error:
// those are decisions, not failures.
func (h *AuthorHandler) runCatalogueSync(ctx context.Context, author *models.Author, opts catalogueSyncOptions) (int, error) {
	autoSearch, mediaType, discovery := opts.autoSearch, opts.mediaType, opts.discovery
	// singleWork: the caller picked one specific book and the direct insert
	// couldn't produce it. This run exists only to create that one row, so it
	// touches nothing about the author beyond it — no profile rewrite, no
	// Calibre re-link, no sync summary — and it is exempt from the
	// catalogue-sync heuristics that may veto a work (#1612).
	singleWork := opts.onlyForeignID != ""
	// Count this run in runningSyncs for as long as it lasts (#2601). The
	// manual Refresh claimed its run before answering; every other caller is
	// counted here. The ID is copied because the sync can rewrite author.
	if syncID := author.ID; syncID != 0 {
		if !opts.syncClaimed {
			h.runningSyncs.start(syncID)
		}
		defer h.runningSyncs.done(syncID)
	}
	if err := h.meta.CheckQuota(ctx); err != nil {
		return 0, err
	}
	// A manual Refresh Metadata reads the author's profile, catalogue and
	// Audible catalogue past the metadata cache (#2601). Only those lookups
	// get metaCtx: the Calibre re-link below resolves an identity the cache
	// has not seen, and the aggregator stops the bypass at the author lookups,
	// so editions, covers and ISBN matches keep their cache.
	metaCtx := ctx
	if opts.refreshFromProvider {
		metaCtx = metadata.WithCacheBypass(ctx)
	}
	if opts.deferCoverEnrichment {
		metaCtx = metadata.WithDeferredCoverEnrichment(metaCtx)
	}
	slog.Info("fetching books for author", "author", author.Name, "foreignId", author.ForeignID)

	// Calibre-imported authors carry a synthetic "calibre:author:N" foreign ID
	// that has no counterpart in OL/Hardcover — they come in with no image,
	// description, or real catalogue. Re-link them to the upstream metadata
	// provider by name so the first Refresh Metadata click pulls real profile
	// data and can update the books the import produced. It does not pull the
	// rest of the catalogue: importers stamp MonitorNewItems=none precisely so
	// it doesn't (#1815), and those authors have books, so the never-populated
	// carve-out doesn't apply either. Turning the setting back on for an author
	// is what asks for their full bibliography.
	//
	// If the re-link fails (name not found, network error) we fall through and
	// keep the synthetic ID, matching the prior skip-silently behaviour.
	//
	// Not on a single-work run. Rewriting a Calibre author's identity is a
	// consequential, author-wide change, and it has no business happening as a
	// side effect of one Add Book whose provider call happened to 502. The
	// author's synthetic id also means the works endpoint below has nothing to
	// answer with, so there is nothing to fall through to.
	wasCalibre := strings.HasPrefix(author.ForeignID, "calibre:")
	if wasCalibre {
		if singleWork {
			slog.Info("single-work fallback skipped: author is not linked to a metadata provider",
				"author", author.Name, "foreignId", author.ForeignID)
			return 0, nil
		}
		if err := h.relinkCalibreAuthor(ctx, author); err != nil {
			slog.Info("calibre author not re-linked to metadata provider", "author", author.Name, "reason", err)
			return 0, nil
		}
	}

	// Refresh the author's OWN profile (bio, photo, disambiguation, ratings)
	// from the metadata provider. Everything below only repopulates the
	// author's BOOKS; without this step an already-linked author's Description
	// and ImageURL stay stale on a manual "Refresh Metadata" even after they
	// appear upstream (Discussion #1226). Calibre authors are skipped here
	// because relinkCalibreAuthor already pulled and persisted their profile
	// just above, so re-fetching would only spend a redundant round-trip.
	//
	// Single-work runs are skipped for the same reason as the re-link: the
	// author usually already existed before this request, and an Add Book is
	// not a request to rewrite their profile.
	//
	// An add and the Refresh button always enrich. The other refresh-path
	// syncs (Refresh all, bulk refresh, scheduled discovery) enrich when it
	// is due: they stamp the profile fresh, which takes the author out of the
	// staggered refresh that would otherwise have enriched it, so leaving it
	// to that refresh postponed enrichment by a day each time they ran. They
	// share a budget of syncEnrichmentsPerHour, so Refresh all on a large
	// library enriches the first due authors and leaves the rest due for the
	// staggered refresh. An import still never asks.
	if !wasCalibre && !singleWork {
		enrich := opts.enrichProfile || (opts.discovery && h.authors.EnrichmentDue(metaCtx, author.ID) && h.takeSyncEnrichment(time.Now()))
		h.refreshAuthorProfile(metaCtx, author, enrich)
	}

	// Load the author's secondary provider identities so supplemental
	// providers can query by identity rather than by name. Without this a
	// Hardcover supplement selects works by name, which cannot separate two
	// real people publishing under the same one and merges their catalogues
	// (#1734). Best effort: a failure here costs precision, not the sync.
	h.attachProviderIdentifiers(ctx, author)

	// Use the dedicated author works endpoint for accurate results, with
	// author-scoped supplemental providers when available.
	books, err := h.meta.GetAuthorWorksForAuthor(metaCtx, *author)
	if err != nil {
		slog.Error("failed to fetch books", "author", author.Name, "error", err)
		return 0, err
	}

	// Single-work run (#1816): the caller asked for one specific book, so drop
	// everything else the provider returned right here — ahead of the Audible
	// supplement, the language sampling and the create loop, none of which can
	// tell us anything about a work we already have the foreign ID for. Every
	// filter and dedup rule below still applies to the one work that survives.
	if opts.onlyForeignID != "" {
		books = keepWorkWithForeignID(books, opts.onlyForeignID)
	}

	// Supplement with Audible-direct author lookup for audiobook-favoring
	// flows. OpenLibrary and Hardcover both miss a large share of
	// audiobook ASINs for prolific authors, so Audible's own catalogue
	// fills the gap (#302). Audible books carry MediaType=audiobook with
	// an ASIN preset; they feed through the same dedup + language filter
	// as the OL results below, so foreign-language ASINs do NOT slip past
	// the active metadata profile's allowed_languages set.
	//
	// Gated on the effective media type — ebook-only setups would just
	// see audiobook rows they never asked for, and each call spends a
	// network round-trip to api.audible.com. A single-work run skips it: the
	// requested work is identified by foreign ID, which Audible's catalogue
	// cannot supply.
	if opts.onlyForeignID == "" && (mediaType == models.MediaTypeAudiobook || mediaType == models.MediaTypeBoth) {
		if audibleBooks, err := h.meta.GetAuthorAudiobooks(metaCtx, author.Name); err != nil {
			slog.Warn("audible author lookup failed", "author", author.Name, "error", err)
		} else if len(audibleBooks) > 0 {
			slog.Debug("audible author lookup supplemented catalogue", "author", author.Name, "count", len(audibleBooks))
			books = append(books, audibleBooks...)
		}
	}

	// Resolve the author's metadata profile (falling back to the seeded
	// default) and parse its allowed_languages CSV. Nil means "no filter".
	allowedLangs, unknownFail := h.resolveAllowedLanguages(ctx, author)
	skipPartBooks := h.resolveSkipPartBooks(ctx, author)
	skipMissingDate := h.resolveSkipMissingDate(ctx, author)
	minPages, skipMissingISBN := h.resolveEditionFilters(ctx, author)
	minEditionCount := h.resolveMinEditionCount(ctx, author)
	var languageEvidence map[string]metadata.AuthorWorkLanguageEvidence
	if !singleWork {
		var languageEvidenceErr error
		languageEvidence, languageEvidenceErr = h.meta.GetAuthorWorkLanguageEvidence(ctx, books, allowedLangs)
		if languageEvidenceErr != nil {
			languageEvidence = nil
			slog.Warn("author work language evidence lookup failed; using existing language fallbacks",
				"author", author.Name, "error", languageEvidenceErr)
		}
	}
	// Both minPages>0 and skipMissingISBN require a real edition lookup per
	// candidate work (page count and ISBN live on Edition, not Book, and
	// aren't populated until an edition fetch runs). Gate the fetch on
	// whether either setting is actually on, so an author whose profile
	// touches neither pays no extra provider round-trips. FillMissingAuthorWorkLanguages
	// below follows the same gating principle (skip the round-trip when the
	// profile doesn't need it) but is a single batched call, not a
	// per-candidate one — see the prefetch below for the batching equivalent
	// for editions (#1929 tracks per-book serial provider calls as a known
	// author-sync perf problem; this avoids adding another one).
	needsEditionPreview := minPages > 0 || skipMissingISBN

	// OpenLibrary works carry no work-level language; the search enricher only
	// backfills it for indexed works, so a tail of works (often translations)
	// reach the filter below with Language="" and slip through the unknown
	// fallback. When the profile actually restricts language, edition-sample
	// those works so the OL tail is caught (#891). Gated on allowedLangs being
	// set so we don't spend the round-trips when the filter is "any".
	adoptMergedHardcoverLanguages(books, languageEvidence, allowedLangs)
	var langSampled int
	if len(allowedLangs) > 0 {
		langSampled = h.meta.FillMissingAuthorWorkLanguages(ctx, books)
	}

	// Author-batch majority-language fallback: FillMissingAuthorWorkLanguages
	// above only resolves a language when at least one sampled edition
	// carries OpenLibrary's `languages` field, which is a common data gap,
	// not a rare one — a work can be genuinely English and still have zero
	// sampled editions reporting a language. When that happens, most authors
	// write predominantly (or exclusively) in one language, so assign the
	// language already resolved for most of this author's OTHER works in the
	// same batch rather than leaving it blank. This is pure in-memory
	// bookkeeping over books already fetched — no new provider round-trips —
	// gated the same as the edition-sample backfill above so an "any
	// language" profile never pays for it. A strict-language profile is then
	// judging a real, resolved language for these works instead of an
	// OpenLibrary metadata gap it has no way to tell apart from an actual
	// foreign-language book.
	if len(allowedLangs) > 0 {
		applyAuthorMajorityLanguageFallback(books, languageEvidence)
	}

	// Everything above can take minutes for a prolific author (works fetch,
	// Audible supplement, language sampling), and this goroutine holds only a
	// snapshot of the author row. By now the row may be gone: AddBook's
	// orphan cleanup rolls back a speculative author insert when its poll
	// times out and the direct insert didn't land (#804, #1559), and a user
	// can delete the author mid-refresh. Bail out instead of running an
	// insert loop where every row fails the author_id FK constraint.
	if current, err := h.authors.GetByID(ctx, author.ID); err == nil {
		if current == nil {
			slog.Info("author deleted while catalogue fetch was running; aborting sync",
				"author", author.Name, "authorId", author.ID)
			return 0, nil
		}
		// This re-read is also the last chance to correct a stale owner before
		// the insert loop stamps it onto every new book. `author` is a
		// caller-supplied snapshot whose OwnerUserID may never have matched the
		// row, and the row itself can be re-owned between the snapshot and here.
		// The persisted value is the only one that satisfies the books.owner_user_id
		// foreign key and the only one per-user scoping will agree with (#1872).
		if author.OwnerUserID != current.OwnerUserID {
			slog.Debug("catalogue sync adopting the author row's persisted owner",
				"author", author.Name, "authorId", author.ID,
				"snapshotOwner", author.OwnerUserID, "persistedOwner", current.OwnerUserID)
			author.OwnerUserID = current.OwnerUserID
		}
	}

	// Track titles we've already added (case-insensitive) to avoid OL duplicates.
	// The value is a pointer to the existing book so we can enrich calibre-imported
	// stubs with the OL foreign ID and language when they title-match an OL record.
	// IncludingExcluded, deliberately. An excluded book is one the user told
	// Bindery to never bring back, so it has to count both ways below: as
	// evidence the author is not an empty one waiting to be populated, and as a
	// title that must not be re-created under a different foreign id. Reading
	// only the non-excluded rows made "Exclude them all" look identical to
	// "this author has no catalogue yet", so the documented cleanup disarmed
	// the very guard it was recommended for (#1815).
	//
	// From here until the created books are hydrated, one sync per author at
	// a time: two overlapping runs (a scheduled discovery and a manual or
	// bulk refresh) would both read the catalogue before either wrote, and
	// each announce the books it won (#2236). The later run waits, then reads
	// what the earlier one created. See lockCatalogueWrites for what is left
	// outside the lock.
	release, err := h.lockCatalogueWrites(ctx, author, opts)
	if err != nil {
		slog.Info("catalogue sync stopped while waiting for another sync of the same author",
			"author", author.Name, "authorId", author.ID, "error", err)
		return 0, err
	}
	defer release()
	allBooks, err := h.books.ListByAuthorIncludingExcluded(ctx, author.ID)
	if err != nil {
		// Reading nothing would look like an empty author: the run would
		// recreate the whole catalogue as new books (#2236).
		slog.Error("catalogue sync aborted: could not read the author's books", "author", author.Name, "authorId", author.ID, "error", err)
		return 0, err
	}
	existingBooks := make([]models.Book, 0, len(allBooks))
	// Excluded titles, keyed the same way as seenTitles below. Kept separate
	// from it rather than merged in: the seenTitles branches UPDATE the row
	// they match (dual-format upgrades, calibre-stub adoption, ratings), and an
	// excluded row is one the user wants left alone, not quietly upgraded.
	excludedKeys := indexer.NewTitleIndex[struct{}]()
	for i := range allBooks {
		if allBooks[i].Excluded {
			excludedKeys.Add(allBooks[i].Title, struct{}{})
			continue
		}
		existingBooks = append(existingBooks, allBooks[i])
	}
	// CanonicalDedupKey, not NormalizeTitleForDedup: this map decides whether a
	// provider work becomes a NEW book row, which is exactly the #940 invariant
	// that CanonicalDedupKey exists to be the single authority for. The two
	// differ by StripBracketSuffixes, so an ABS-sourced row stored as
	// "X [Unabridged]" did not match OpenLibrary's "X" here — a second row was
	// created, and both then carried the same books.dedup_key, violating the
	// invariant from the inside (#1648).
	//
	// indexer.TitleIndex, not a plain map: since #2042 the canonical key no
	// longer truncates at ": ", so a stored "Mistborn: The Final Empire" and a
	// provider "Mistborn" hash differently. The index probes the main-title
	// bucket too and adjudicates with indexer.CompareTitles, so subtitle-only
	// divergence still matches while "Star Wars: A New Hope" no longer matches
	// "Star Wars: The Empire Strikes Back".
	seenTitles := indexer.NewTitleIndex[*models.Book]()
	// The author credits this fetch carries for each work, keyed by work id,
	// so a stored row can be judged on the credits of its own work when that
	// work is in the same catalogue answer. Credits are transport only and
	// never stored, so this is the only place a stored row's come from
	// (metadata.CreditsShowAnotherAuthorsWork, #3792).
	creditsByWork := make(map[string][]string, len(books))
	for _, w := range books {
		if id := strings.TrimSpace(w.ForeignID); id != "" && len(w.CreditedAuthorForeignIDs) > 0 {
			creditsByWork[id] = w.CreditedAuthorForeignIDs
		}
	}
	anotherAuthorsWork := func(stored *models.Book, incoming models.Book) bool {
		return metadata.CreditsShowAnotherAuthorsWork(
			creditsByWork[strings.TrimSpace(stored.ForeignID)], incoming.CreditedAuthorForeignIDs, author.ForeignID)
	}
	for i := range existingBooks {
		seenTitles.Add(existingBooks[i].Title, &existingBooks[i])
	}

	// Discovery policy (#1815). A refresh may always UPDATE the books the
	// library already has — covers, ratings, genres, series links, the reason
	// people click "Refresh metadata" — but it may only CREATE rows for works
	// the user doesn't have when the author's monitoring says they want them.
	//
	// The one exception is an author who has never been populated. That is the
	// repair the bulk "refresh" action and the "Refresh all authors" job exist
	// for (an import that resolved the author but no catalogue, an add whose
	// initial sync failed), and it cannot be the "my library went from 75 books
	// to 500" surprise: there is nothing there to be surprised about yet.
	//
	// "Has no books" alone is not that test. Deleting every book under an
	// unmonitored author leaves exactly the same zero-book row, so the next
	// bulk refresh re-imported the whole bibliography — and bulk-deleting the
	// clutter is precisely the cleanup this feature's own documentation
	// recommends. authors.catalogue_populated_at (migration 075) is stamped on
	// the author's first book — by BookRepo.Create, so an import or a list sync
	// counts too — and never cleared, so a library the user emptied stays empty
	// while a never-populated author is still repaired.
	allowNewBooks := true
	if discovery && !authorAcceptsDiscoveredBooks(author) && !h.authorAwaitsFirstCatalogue(ctx, author, len(allBooks)) {
		allowNewBooks = false
		slog.Info("refresh will update existing books only; author is not accepting newly-discovered books",
			"author", author.Name, "authorId", author.ID,
			"monitored", author.Monitored, "monitorNewItems", models.NormalizeAuthorMonitorNewItems(author.MonitorNewItems))
	}

	// Whether the author had a catalogue before this run, for the bookAnnounced
	// rule (#2236). Read before anything is created.
	populatedBefore := catalogueWasPopulated(opts, len(allBooks))

	normalizedAuthor := strings.ToLower(strings.TrimSpace(author.Name))
	latestKeys := latestBookMonitorKeys(books, author.MonitorLatestCount, func(book models.Book) bool {
		return isAuthorWorkMonitorCandidate(book, normalizedAuthor, allowedLangs, unknownFail)
	})
	today := dateOnly(time.Now().UTC())

	// Build a foreign-id index of the author's monitored series so that, in
	// series mode, freshly-discovered books can be flipped to monitored at
	// creation time if their provider-supplied SeriesRefs already match one
	// of the pinned series. Without this lookup the user would have to wait
	// for a subsequent apply pass to flip them on.
	monitoredSeriesForeignIDs := map[string]struct{}{}
	if author.MonitorMode == models.AuthorMonitorModeSeries && h.series != nil {
		pinIDs, err := h.authors.ListMonitoredSeriesIDs(ctx, author.ID)
		if err != nil {
			slog.Warn("failed to load monitored series ids for author works fetch", "author", author.Name, "error", err)
		} else if len(pinIDs) > 0 {
			ownSeries, err := h.series.ListByAuthor(ctx, author.ID)
			if err != nil {
				slog.Warn("failed to load author series for series-mode fetch", "author", author.Name, "error", err)
			} else {
				pinSet := make(map[int64]struct{}, len(pinIDs))
				for _, id := range pinIDs {
					pinSet[id] = struct{}{}
				}
				var pinned []int64
				for _, s := range ownSeries {
					if _, ok := pinSet[s.ID]; ok {
						pinned = append(pinned, s.ID)
						if s.ForeignID != "" {
							monitoredSeriesForeignIDs[s.ForeignID] = struct{}{}
						}
					}
				}
				// A provider can still report a pinned series under an id
				// merged into it (#2554); that book belongs to the pinned
				// series too, so it is monitored at creation as well.
				aliases, err := h.series.AliasForeignIDs(ctx, pinned)
				if err != nil {
					slog.Warn("failed to load merged series ids for series-mode fetch", "author", author.Name, "error", err)
				}
				for _, id := range aliases {
					monitoredSeriesForeignIDs[id] = struct{}{}
				}
			}
		}
	}

	// Series links for the books the library already has (#2328). Lazy: it
	// reads nothing until the first existing book turns up carrying a series
	// ref, so a sync that creates everything, or a provider with no series
	// data, costs nothing.
	//
	// It is handed the ids of the author's books as they are right now,
	// before this run creates or re-parents anything. That set is what makes
	// its one author-scoped membership snapshot trustworthy: for a book
	// outside it, "no row in the snapshot" means "the snapshot cannot see
	// this book", not "this book is in no series", and it reads that book on
	// its own instead.
	preexistingBookIDs := make(map[int64]struct{}, len(allBooks))
	for i := range allBooks {
		preexistingBookIDs[allBooks[i].ID] = struct{}{}
	}
	seriesLinker := newExistingBookSeriesLinker(h.series, author.ID, preexistingBookIDs)

	searchQueue := make([]models.Book, 0)
	// createdBooks collects the books this sync creates so their edition
	// hydration and on-disk check run as one batched pass afterwards rather
	// than a serial provider round trip per book (#1929).
	createdBooks := make([]models.Book, 0)
	autoSearchEnabled := autoSearch && h.searcher != nil && author.Monitored && h.isAutoGrabEnabled(ctx)

	// One library snapshot for the whole create loop (#1888, #1929). The loop
	// below calls handleNewWantedBook once per new book, and its FindExisting
	// used to re-walk every library root per call — a 65-book sync did 65 full
	// walks of the library, which on network storage is minutes to an hour of
	// pure stat traffic. The snapshot walks each root once, on first use.
	//
	// The staleness this buys is deliberate and near-vacuous here: a file that
	// can match a book this loop is creating was on disk before the sync began,
	// because auto-search for these books only runs after the loop. See the
	// LibrarySnapshot contract for the full argument.
	finder := snapshotFinder(h.finder)

	// Strict media-type policy (#1575). When on and the default is a single
	// format, catalogue population is narrowed to that format so an ebook-only
	// (or audiobook-only) user never accumulates rows they can't grab.
	strictMediaType := h.resolveDefaultMediaTypeStrict(ctx)

	// skippedExcluded is carried in AuthorSyncSummary but not rendered by the
	// notice: the notice explains works the user did NOT expect to lose, and a
	// book they excluded by hand is not one of them. It is still reported so
	// Total reconciles (#2449).
	//
	// matched and failed are the two outcomes that had no counter at all until
	// #2449. matched is the ordinary "this work is already a book" path and on
	// an established author it is most of the run; failed is a create that lost
	// the write. Both were invisible, which made Total minus everything else
	// look like a hole.
	var added, matched, failed, skippedLang, skippedJunk, skippedMediaType, skippedNotAccepted, skippedExcluded int
	var skippedPartBooks, skippedMissingDate, skippedMinPages, skippedMissingISBN, skippedThinCluster int
	// Names of the first few language-rejected works, reported to the user
	// alongside the count (#1889): "65 books skipped" is alarming, but it is
	// the titles and their language codes that tell them whether the profile
	// is set the way they meant.
	var skippedLangSample []models.AuthorSyncSkippedBook
	// The first few page-count- and ISBN-rejected works, same reasoning and
	// cap as skippedLangSample (#1889 established the pattern; requested
	// again for these filters specifically in PR review, vavallee).
	var skippedPartBooksSample, skippedMissingDateSample []models.AuthorSyncSkippedBook
	var skippedMinPagesSample, skippedMissingISBNSample []models.AuthorSyncSkippedBook
	var skippedThinClusterSample []models.AuthorSyncSkippedBook
	// clusterEditions maps a normalised title to the highest known edition
	// count among the works carrying that title, built as the candidates
	// accumulate below. It backs the MinEditionCount filter in the loop that
	// follows (#2235): a title whose works all report fewer editions than the
	// profile's floor is the noise shape that filter drops. It is only
	// populated when the filter is actually on, so a profile without it pays
	// nothing. A title absent from the map, or mapped to 0, means "no work in
	// the cluster reported an edition count" — unknown, not zero — and the
	// filter lets it through, the same unknown-passes semantics MinPages uses.
	var clusterEditions map[string]int
	if minEditionCount > 0 {
		clusterEditions = make(map[string]int, len(books))
	}
	// candidates accumulates every work that survives the free (in-memory)
	// filters below and would otherwise reach the MinPages/SkipMissingISBN
	// check. Splitting the loop here lets the edition lookups those two
	// filters need be batched with bounded concurrency instead of firing one
	// serial provider round-trip per candidate inside the loop below (#1929
	// tracks per-book serial provider calls during author sync as a known
	// perf problem; this avoids adding another instance of it).
	candidates := make([]models.Book, 0, len(books))
	for _, b := range books {
		b.AuthorID = author.ID
		// Apply the caller-provided default media type when the provider
		// didn't set one. Never overwrite an explicit value — the audiobook
		// enrichment flow relies on provider-supplied audiobook rows coming
		// through with MediaType=audiobook already.
		if mediaType != "" && b.MediaType == "" {
			b.MediaType = mediaType
		}

		// Strict media-type policy (#1575): when enabled and the default is a
		// single format, keep the catalogue to that format. A "both" work is
		// narrowed to the default — its wanted format is still available. A
		// work that is ONLY the other format has nothing the user asked for,
		// so it is skipped rather than created as an un-grabbable row. A "both"
		// default disables the clamp (the user wants everything). Existing rows
		// are untouched; the Books bulk-edit action migrates those.
		//
		// A single-work run is exempt. #1612: "an explicit add of one specific
		// work must not be vetoed by catalogue-sync heuristics" — the direct
		// insert this run stands in for applies neither clamp nor language
		// filter, and applying them here would turn an explicitly-picked
		// audiobook into a 15s poll, a 404 "try again shortly", and an orphan
		// cleanup that deletes the author the request just created.
		if !singleWork && strictMediaType && (mediaType == models.MediaTypeEbook || mediaType == models.MediaTypeAudiobook) {
			switch b.MediaType {
			case models.MediaTypeBoth:
				b.MediaType = mediaType
			case mediaType:
				// already the wanted single format
			default:
				skippedMediaType++
				slog.Debug("skipping media-type-mismatched book under strict default",
					"title", b.Title, "bookMediaType", b.MediaType, "default", mediaType)
				continue
			}
		}

		// Filter out OpenLibrary "works" whose title is empty or is just the
		// author name — a recurring OL data-quality problem where the Work
		// record was never titled and falls back to the author's name.
		// Letting these through pollutes the Wanted page and produces
		// nonsense destination folders like "Jared M. Diamond/Jared M. Diamond ()".
		normalizedTitle := strings.ToLower(strings.TrimSpace(b.Title))
		if normalizedTitle == "" || normalizedTitle == normalizedAuthor {
			skippedJunk++
			slog.Debug("skipping junk-title OL work", "title", b.Title, "foreignId", b.ForeignID)
			continue
		}

		// Filter by the author's metadata-profile allowed_languages.
		// Books whose language is unknown honor the profile's
		// unknown_language_behavior (pass by default; see #232).
		//
		// Exempt on a single-work run, and this is the exact case #1612 was
		// filed for: a heavily-translated work whose edition-sampled language
		// falls outside the profile became permanently un-addable, because the
		// only path that could create it kept refusing to.
		languageAllowed, _ := authorWorkPassesLanguageFilter(&b, allowedLangs, unknownFail, languageEvidence)
		if !singleWork && !languageAllowed {
			skippedLang++
			if len(skippedLangSample) < authorSyncSkippedSampleLimit {
				skippedLangSample = append(skippedLangSample, models.AuthorSyncSkippedBook{Title: b.Title, Language: b.Language})
			}
			slog.Debug("skipping non-allowed-language book", "title", b.Title, "language", b.Language, "allowed", allowedLangs, "edition_sampled", langSampled)
			continue
		}

		if clusterEditions != nil && b.EditionCount > clusterEditions[normalizedTitle] {
			clusterEditions[normalizedTitle] = b.EditionCount
		}
		candidates = append(candidates, b)
	}

	// Prefetch editions for every surviving candidate with bounded
	// concurrency (same cap as the auto-search fan-out below) rather than
	// fetching one at a time inside the loop that follows. Only runs when a
	// profile setting actually needs edition data, so an author whose
	// profile touches neither MinPages nor SkipMissingISBN triggers no
	// extra provider round-trips at all. A candidate missing from the map
	// means its lookup failed (network error, provider outage) — the loop
	// below treats that the same as "not enforcing for this work" a live
	// per-item call would have, so a transient failure never drops a book.
	var editionsByForeignID map[string][]models.Edition
	// Works the author already has are exempt from both filters below, so
	// their editions are not fetched (P1, #2236), and a discovery run
	// enriches covers only for the rest.
	var newWorks []int
	if (needsEditionPreview || opts.deferCoverEnrichment) && len(candidates) > 0 {
		newWorks = h.newWorkIndexes(ctx, author.ID, allBooks, candidates)
	}
	if opts.deferCoverEnrichment {
		h.enrichNewWorkCovers(ctx, candidates, newWorks)
	}
	if needsEditionPreview && len(candidates) > 0 {
		prefetch := booksAt(candidates, newWorks)
		editionsByForeignID = make(map[string][]models.Edition, len(prefetch))
		var mu sync.Mutex
		concurrency.RunBounded(ctx, prefetch, authorAutoSearchConcurrency, func(ctx context.Context, b models.Book) {
			// BnF works come from the author catalogue with every edition
			// already attached, and BnF's per work editions lookup is a full
			// GetBook: up to four requests at one a second, so a large
			// author cost minutes. The attached editions are the same
			// records, so they are the evidence (as in catalogue
			// reconciliation). A work with none attached still goes to the
			// lookup.
			if bookProvider(&b) == "bnf" && len(b.Editions) > 0 {
				mu.Lock()
				editionsByForeignID[b.ForeignID] = b.Editions
				mu.Unlock()
				return
			}
			editions, err := h.meta.GetEditions(ctx, b.ForeignID)
			if err != nil {
				slog.Debug("edition lookup failed while checking MinPages/SkipMissingISBN; not enforcing for this work",
					"title", b.Title, "foreignId", b.ForeignID, "error", err)
				return
			}
			mu.Lock()
			editionsByForeignID[b.ForeignID] = editions
			mu.Unlock()
		})
	}

	// A daily hold stops creating books, but the books this run already created
	// still get hydration, file matching, search and the summary below: the
	// next sync treats them as existing, so skipping that work would strand them.
	var quotaErr error
	for _, b := range candidates {
		if quotaErr = h.meta.CheckQuota(ctx); quotaErr != nil {
			break
		}
		// A cancelled or timed out run stops creating books rather than
		// logging one failed insert per remaining work.
		if ctx.Err() != nil {
			break
		}
		// Hoisted here, before the edition-gated filters below, so a filter
		// that fires after this point can exempt a book the user already
		// owns. Without this, a filtered-but-owned book never reaches the
		// update branch: it keeps its files, but silently stops getting
		// rating, genre and cover updates, and is reported as "skipped" on
		// every subsequent sync even though the user is looking at it in
		// their library (vavallee, PR review). MinPages/SkipMissingISBN
		// screen works out of discovery — they must not also stop
		// maintaining ones already accepted.
		// Resolve by every id this fetch knows the work by, not just the
		// primary column (#1705).
		//
		// The reported case: books created from Hardcover, then the author is
		// relinked to OpenLibrary and refreshed. The incoming work now carries
		// an OpenLibrary id that no existing row has ever held, so an exact
		// lookup on it misses and a second row is minted for a volume already
		// in the library.
		//
		// What connects them is HardcoverForeignID. mergeAuthorWorks joins the
		// two providers' catalogues in memory before any of this, so the
		// OpenLibrary work arrives already carrying the Hardcover id of the
		// same book, and that id is exactly what the existing row was created
		// with. Probing it turns a cross-provider duplicate into a match.
		existing := h.resolveExistingBook(ctx, b.ForeignID, b.HardcoverForeignID)
		// A row found through an alias rather than its own id may be one an
		// earlier release bound to the wrong work by title: before #3792
		// King's 2026 novel landed on the 2011 anthology of the same name, and
		// the anthology row kept the novel's OpenLibrary and hc: ids as
		// aliases. Every refresh then resolved the novel to it here. When the
		// credits say the row's own work is another author's
		// (metadata.CreditsShowAnotherAuthorsWork), the alias is not trusted
		// and the work is judged on its own; if it is created, the aliases
		// move to its row (moveHealedAliases below).
		var healedFrom *models.Book
		if existing != nil && existing.ForeignID != b.ForeignID && anotherAuthorsWork(existing, b) {
			slog.Info("not resolving a work through an id another work's row holds",
				"title", b.Title, "foreignId", b.ForeignID, "hardcoverId", b.HardcoverForeignID,
				"bookId", existing.ID, "bookForeignId", existing.ForeignID)
			healedFrom = existing
			existing = nil
		}

		// Filter box-set/omnibus/carton "works" when the author's metadata
		// profile has SkipPartBooks enabled. These are real OL records for a
		// bundle of other books, not a book of their own, and previously
		// passed every filter above unchanged (see partBookTitleRe).
		if existing == nil && skipPartBooks && isPartBookTitle(b.Title) {
			skippedPartBooks++
			if len(skippedPartBooksSample) < authorSyncSkippedSampleLimit {
				skippedPartBooksSample = append(skippedPartBooksSample, models.AuthorSyncSkippedBook{Title: b.Title})
			}
			slog.Debug("skipping part-book/box-set title", "title", b.Title, "foreignId", b.ForeignID)
			continue
		}

		// Filter works with no release date when the author's metadata profile
		// has SkipMissingDate enabled. ReleaseDate is already merged in from
		// the provider's work data by this point (aggregator_author_works.go),
		// so this is a straight presence check, not a fetch.
		if existing == nil && skipMissingDate && b.ReleaseDate == nil {
			skippedMissingDate++
			if len(skippedMissingDateSample) < authorSyncSkippedSampleLimit {
				skippedMissingDateSample = append(skippedMissingDateSample, models.AuthorSyncSkippedBook{Title: b.Title})
			}
			slog.Debug("skipping work with no release date", "title", b.Title, "foreignId", b.ForeignID)
			continue
		}

		// Filter works whose title cluster is thinner than the profile's
		// MinEditionCount floor (#2235). Edition count only separates real
		// works from one-off noise once records are grouped by title, so the
		// judgement is made on the cluster's best-known count, not each work's
		// own: a title with at least one well-editioned work is a real book
		// even if this particular record is thin. The cluster map was built in
		// the loop above from works that already survived the junk and
		// language filters.
		//
		// The exemptions mirror the other discovery filters: a book the user
		// already owns (existing != nil) is maintained, never dropped, and a
		// single-work run is exempt per #1612 — an explicit add of one
		// specific work must not be vetoed by catalogue-sync heuristics.
		// A cluster with no known edition count (map miss, or 0) is unknown,
		// not zero, and passes: only OpenLibrary search results populate
		// EditionCount, so a Hardcover-primary author's catalogue — where
		// every work reports none — must survive a profile that turned this
		// filter on.
		if existing == nil && !singleWork && minEditionCount > 0 {
			clusterKey := strings.ToLower(strings.TrimSpace(b.Title))
			if count, ok := clusterEditions[clusterKey]; ok && count > 0 && count < minEditionCount {
				skippedThinCluster++
				if len(skippedThinClusterSample) < authorSyncSkippedSampleLimit {
					skippedThinClusterSample = append(skippedThinClusterSample, models.AuthorSyncSkippedBook{Title: b.Title})
				}
				slog.Debug("skipping work from a title cluster below the edition-count floor",
					"title", b.Title, "foreignId", b.ForeignID, "clusterEditions", count, "minEditionCount", minEditionCount)
				continue
			}
		}

		// MinPages / SkipMissingISBN both need edition data (page count and
		// ISBN live on Edition, not Book) fetched in the prefetch pass above.
		// A book with no entry in editionsByForeignID is either not gated by
		// either setting (needsEditionPreview was false) or its lookup
		// failed — both cases fall through unfiltered.
		//
		// The two filters read a missing/empty edition list differently, and
		// deliberately so, not by oversight:
		//   - MinPages treats "no edition reports a page count" as unknown,
		//     not zero, and lets the work through (see passesMinPagesFilter).
		//     A size floor shouldn't penalize a work the provider simply
		//     hasn't indexed a page count for yet.
		//   - SkipMissingISBN treats "zero editions returned" as missing,
		//     and drops the work. Unlike a page count, there is nothing
		//     partial about "we found no editions to check" for an
		//     identifier-presence gate — it's the strongest available
		//     signal that no ISBN can be confirmed. This matches Chaptarr's
		//     own behavior for the same case: its identifier filter runs
		//     per-edition, and a book with zero editions fails the same
		//     `Editions.Any()` check a book whose editions were all
		//     filtered out for lacking an identifier would.
		if existing == nil {
			if editions, ok := editionsByForeignID[b.ForeignID]; ok {
				if skipMissingISBN && !anyEditionHasISBN(editions) {
					skippedMissingISBN++
					if len(skippedMissingISBNSample) < authorSyncSkippedSampleLimit {
						skippedMissingISBNSample = append(skippedMissingISBNSample, models.AuthorSyncSkippedBook{Title: b.Title})
					}
					slog.Debug("skipping work with no ISBN on any edition", "title", b.Title, "foreignId", b.ForeignID)
					continue
				}
				if minPages > 0 && !passesMinPagesFilter(editions, minPages) {
					skippedMinPages++
					if len(skippedMinPagesSample) < authorSyncSkippedSampleLimit {
						skippedMinPagesSample = append(skippedMinPagesSample, models.AuthorSyncSkippedBook{Title: b.Title})
					}
					slog.Debug("skipping work below the minimum page count", "title", b.Title, "foreignId", b.ForeignID, "minPages", minPages)
					continue
				}
			}
		}

		b.Monitored = shouldMonitorBookForAuthor(author, b, latestKeys, today)
		// Series mode short-circuit: if the upstream provider already says
		// this book is in one of the user's pinned series, monitor it on
		// first discovery instead of waiting for the apply pass.
		if author.MonitorMode == models.AuthorMonitorModeSeries && author.Monitored && len(monitoredSeriesForeignIDs) > 0 {
			for _, ref := range b.SeriesRefs {
				if _, ok := monitoredSeriesForeignIDs[ref.ForeignID]; ok {
					b.Monitored = true
					break
				}
			}
		}
		// MonitorNewItems=none overrides every mode on refresh discovery
		// (issue #1348): works found after the initial sync are created
		// unmonitored, so refreshing metadata can never mass-monitor a
		// back-catalogue. Initial sync (add/migrate) is not affected.
		if discovery && b.Monitored && author.MonitorNewItems == models.AuthorMonitorNewItemsNone {
			b.Monitored = false
		}

		// Update ratings + genres on existing books, then skip further
		// processing (we don't want to overwrite user state like status).
		// existing was resolved above, before the profile filters, so an
		// owned book that trips one of them still reaches this branch.
		if existing != nil {
			changed := false
			// GetByForeignID matches globally (foreign_id is UNIQUE across all
			// authors), so the row may sit under a different author — created
			// under a duplicate/stale author record, a calibre shell author,
			// or one that no longer exists. Such a row is invisible on this
			// author's page (ListByAuthor filters by author_id) and, without
			// this, the sync would refresh its ratings forever while the book
			// never appears (issue #1405). Re-link it — unless the current
			// owner is genuinely credited on the work (co-authored books must
			// not ping-pong between their authors on alternating syncs).
			if h.reparentMisattachedBook(ctx, existing, author, b.CreditedAuthorForeignIDs) {
				changed = true
			}
			if b.RatingsCount > 0 && (existing.RatingsCount == 0 || b.RatingsCount > existing.RatingsCount) {
				existing.RatingsCount = b.RatingsCount
				existing.AverageRating = b.AverageRating
				changed = true
			}
			// Backfill a missing cover. Before #1748 this branch updated only
			// ratings and genres, never image_url, so a row created with a blank
			// cover stayed blank on every Refresh Metadata even once the cover
			// became available upstream (or once edition-cover sampling could
			// find one). Fill-empty, never clobber: a cover the user set or an
			// earlier fetch already resolved is left untouched.
			if existing.ImageURL == "" && b.ImageURL != "" {
				existing.ImageURL = b.ImageURL
				changed = true
			}
			// Backfill Hardcover genres onto rows imported before genre
			// sourcing existed. Gated to Hardcover provenance (a HardcoverForeignID
			// means HC matched this work this fetch) so a refresh while HC is
			// unavailable never downgrades clean genres back to OL subjects.
			// A user-edited genre set (#1446) is locked and never clobbered.
			if b.HardcoverForeignID != "" && len(b.Genres) > 0 && !slices.Equal(existing.Genres, b.Genres) &&
				!existing.IsFieldLocked(models.BookFieldGenres) {
				existing.Genres = b.Genres
				changed = true
			}
			if changed {
				if err := h.books.Update(ctx, existing); err != nil {
					slog.Warn("authors: update during dedup", "error", err, "book_id", existing.ID)
				}
			}
			// Corrections made upstream to the release date, and a description
			// the book lacks (#3105). Never clears.
			h.refreshProviderFields(ctx, existing, &b)
			// Attach whatever ids this fetch knows the book by, so the next
			// sync from either provider resolves it exactly rather than
			// relying on a title comparison (#1705).
			h.recordBookIdentities(ctx, existing, b.ForeignID, b.HardcoverForeignID)
			// The other half of "a refresh may always UPDATE the books the
			// library already has". This branch is the id-resolved match: the
			// work carries an id some local row already holds, which on an
			// imported library is every work. Before #2328 series membership
			// was only ever written for books the sync CREATED, so the one
			// repair nobody could perform was the series one.
			seriesLinker.link(ctx, existing, b.SeriesRefs)
			matched++
			continue
		}

		// Deduplicate by normalized title: OpenLibrary (and Audible enrichment)
		// sometimes surfaces multiple Work records for the same title — most
		// commonly one ebook Work and one audiobook Work.  Rather than silently
		// dropping the duplicate, we upgrade the already-tracked row to
		// media_type="both" so the user gets dual-format support without a
		// second book entry (issue #442).
		//
		// Special cases:
		//   • Calibre-stub rows are upgraded to the real OL foreign_id (existing
		//     behaviour — preserves the pre-#442 upgrade path).
		//   • A duplicate that carries the same media_type as the existing row is
		//     truly redundant and is silently skipped (no format gain).
		//
		// A title is not proof on its own, though. Stephen King's
		// OpenLibrary list holds both his own record for the 2026 novel
		// "Other Worlds Than These" and the 2011 anthology of the same name,
		// and binding the novel here recorded its OpenLibrary and Hardcover
		// ids on the anthology row and never created it (#3792). When the
		// credits say the row's work is another author's and the incoming one
		// is this author's (metadata.CreditsShowAnotherAuthorsWork), the work
		// goes on to the create path as the separate book it is.
		titled, seen := seenTitles.Lookup(b.Title)
		if seen && titled != nil && anotherAuthorsWork(titled, b) {
			slog.Debug("not binding a work to a same-titled row credited to another author",
				"title", b.Title, "foreignId", b.ForeignID, "bookId", titled.ID, "bookForeignId", titled.ForeignID)
			seen = false
		}
		if seen && titled != nil {
			existing := titled
			// existing is the row as this sync first saw it, and the
			// provider calls since then leave plenty of time for an edit.
			// Work from a fresh read and guard each write on it (#2926).
			if !h.refreshTitleMatch(ctx, existing) {
				continue
			}
			expectedUpdatedAt := existing.UpdatedAtRaw
			hydrateExistingFromMatchedHardcover := false
			// lostGuard: an edit landed between the re-read and the write,
			// so this work's change was not made.
			lostGuard := false
			switch {
			case strings.HasPrefix(existing.ForeignID, "calibre:"):
				// Upgrade calibre stub to real OL foreign_id.
				existing.ForeignID = b.ForeignID
				if existing.Language == "" && b.Language != "" {
					existing.Language = b.Language
				}
				if b.RatingsCount > 0 && (existing.RatingsCount == 0 || b.RatingsCount > existing.RatingsCount) {
					existing.RatingsCount = b.RatingsCount
					existing.AverageRating = b.AverageRating
				}
				if written, err := h.writeTitleMatch(ctx, existing, expectedUpdatedAt); err != nil {
					slog.Warn("authors: update during dedup", "error", err, "book_id", existing.ID)
				} else if !written {
					lostGuard = true
				} else if existing.WantsAudiobook() {
					hydrateExistingFromMatchedHardcover = true
				}
			case canUpgradeToBoth(existing.MediaType, b.MediaType) && existing.HasFileForCurrentFormat():
				// The merge below exists to avoid a second row for one Work, which
				// is a real problem for a book nobody has yet. It is the wrong
				// answer for a book the user already owns: widening it invents a
				// want for a format they never asked for, and because status is
				// derived from the formats still missing, the book drops out of
				// Imported and back onto the Wanted list with its file untouched
				// on disk. One reporter's routine refresh flipped 29 owned books
				// in a single run, some of them added two months earlier (#2096).
				//
				// Deliberately not gated on a locked field: mediaType is not in
				// LockableBookFields, and a gate the user has to arm in advance
				// does nothing for a library this has already rewritten.
				slog.Info("keeping the format of a book already on disk; the catalogue lists the other format but nobody asked for it",
					"title", existing.Title, "bookId", existing.ID, "mediaType", existing.MediaType, "foreignId", b.ForeignID)
			case canUpgradeToBoth(existing.MediaType, b.MediaType):
				// One Work is ebook, the other is audiobook — merge into a single
				// dual-format row instead of creating a second book entry.
				existing.MediaType = models.MediaTypeBoth
				if b.RatingsCount > 0 && (existing.RatingsCount == 0 || b.RatingsCount > existing.RatingsCount) {
					existing.RatingsCount = b.RatingsCount
					existing.AverageRating = b.AverageRating
				}
				if written, err := h.writeTitleMatch(ctx, existing, expectedUpdatedAt); err != nil {
					slog.Warn("failed to upgrade book to dual-format", "title", existing.Title, "error", err)
				} else if !written {
					lostGuard = true
				} else {
					slog.Debug("upgraded book to dual-format", "title", existing.Title, "foreignId", b.ForeignID)
					hydrateExistingFromMatchedHardcover = true
				}
			default:
				// Same media type duplicate — just refresh ratings if we have better data.
				if b.RatingsCount > 0 && (existing.RatingsCount == 0 || b.RatingsCount > existing.RatingsCount) {
					existing.RatingsCount = b.RatingsCount
					existing.AverageRating = b.AverageRating
					if written, err := h.writeTitleMatch(ctx, existing, expectedUpdatedAt); err != nil {
						slog.Warn("authors: update during dedup", "error", err, "book_id", existing.ID)
					} else if !written {
						lostGuard = true
					}
				}
				hydrateExistingFromMatchedHardcover = existing.WantsAudiobook()
			}
			if lostGuard {
				// Leave the work unrecorded too. Its ids would make the next
				// sync resolve it through the id branch above, which never
				// relinks a calibre stub or widens the format, so the change
				// would be lost for good instead of retried by title.
				matched++
				continue
			}
			// A title match is a guess that just paid off. Recording the
			// incoming ids turns it into an exact match next time, which
			// matters most for the case that produced this branch: the same
			// volume seen through two providers whose titles agree today and
			// may not after either side edits one (#1705).
			h.recordBookIdentities(ctx, existing, b.ForeignID, b.HardcoverForeignID)
			if hydrateExistingFromMatchedHardcover {
				// The row is already in the library, so its format belongs to
				// the user and hydration must not widen it (#2768).
				h.hydrateMatchedHardcoverEditions(ctx, existing, b.HardcoverForeignID, nil, true)
			}
			// Same treatment as the id-resolved branch, for the row this run
			// recognised by title instead: a calibre stub just upgraded to a
			// real provider id, a dual-format merge, or a same-format
			// duplicate. All three end with one local row standing for this
			// work, and the provider has just told us which series it is in
			// (#2328). The title match is the branch an ABS or calibre import
			// lands in most often, and those rows are precisely the ones that
			// arrived with no series at all.
			seriesLinker.link(ctx, existing, b.SeriesRefs)
			// Same bucket as the id-resolved branch above. From the user's side
			// there is no difference worth a separate number: the work is in
			// their library, Bindery found it, and it did not need creating.
			// Which of the two lookups got there is an implementation detail.
			matched++
			continue
		}
		// Everything from here on CREATES a book the library does not have.
		// On a refresh of an author who isn't taking new work, that is the
		// #1815/#1816 back-catalogue import — count it and move on. Note the
		// placement: every update branch above (ratings, covers, genres,
		// re-parenting, dual-format upgrades) has already run for the books
		// the user does have, which is the half of "refresh" they asked for.
		// An excluded row under this author already says "never bring this
		// back". The GetByForeignID branch above catches the exact id, but a
		// work whose local row carries a different one — a calibre: stub, a
		// re-ided provider work — would otherwise be created afresh as a
		// title the exclusion was meant to cover (#1815).
		if _, excluded := excludedKeys.Lookup(b.Title); excluded {
			skippedExcluded++
			slog.Debug("skipping newly-discovered work: an excluded book under this author has the same title",
				"title", b.Title, "foreignId", b.ForeignID, "author", author.Name)
			continue
		}
		if !allowNewBooks {
			skippedNotAccepted++
			slog.Debug("skipping newly-discovered work: author is not accepting new books",
				"title", b.Title, "foreignId", b.ForeignID, "author", author.Name)
			continue
		}
		seenTitles.Add(b.Title, &b)

		// Tenancy (#1457): a new book inherits its author's owner so per-user
		// scoping sees the sync's output. NULL-owned authors stay NULL-owned,
		// except for Add Book's fallback (see ownerForUnownedAuthor).
		b.OwnerUserID = author.OwnerUserID
		if b.OwnerUserID == 0 {
			b.OwnerUserID = opts.ownerForUnownedAuthor
		}
		if err := h.books.Create(ctx, &b); err != nil {
			// A UNIQUE constraint on foreign_id means the book was already
			// created by a concurrent or earlier sync — treat as a benign
			// skip, but give the mis-attached case (#1405) the same re-link
			// chance the existing-row branch above gets.
			if strings.Contains(err.Error(), "UNIQUE constraint failed") {
				if raced, _ := h.books.GetByForeignID(ctx, b.ForeignID); raced != nil &&
					h.reparentMisattachedBook(ctx, raced, author, b.CreditedAuthorForeignIDs) {
					if uerr := h.books.Update(ctx, raced); uerr != nil {
						slog.Warn("authors: re-link after unique conflict", "error", uerr, "book_id", raced.ID)
					}
				}
				// A losing race still ends with the book present, so this is a
				// match and not a failure.
				//
				// No series link here, deliberately (#2328): the sync that won
				// the race is about to run its own create path over this row,
				// including handleNewWantedBook, and linking it from here
				// would put two writers on one book's primary series. The row
				// gets its links from that sync, or from the next refresh.
				matched++
				continue
			}
			// A FOREIGN KEY failure here almost always means the author row
			// was deleted after the pre-loop existence check (orphan cleanup
			// or a user delete racing this goroutine). Confirm and abort the
			// whole sync — every remaining insert would fail the same way,
			// emitting one WARN per work (#1559 saw 180 in a burst).
			if strings.Contains(err.Error(), "FOREIGN KEY constraint failed") {
				if current, gerr := h.authors.GetByID(ctx, author.ID); gerr == nil && current == nil {
					slog.Info("author deleted mid-sync; aborting catalogue sync",
						"author", author.Name, "authorId", author.ID, "added", added)
					return added, nil
				}
			}
			slog.Warn("failed to create book", "title", b.Title, "error", err)
			failed++
			continue
		}
		if healedFrom != nil {
			h.moveHealedAliases(ctx, healedFrom, &b)
		}
		// Claim this row for the create path (#2328). seenTitles above already
		// holds it, so a later work with the same normalised title reaches the
		// title branch with a book this run made; handleNewWantedBook links
		// its series a few lines below, from the same refs, and two writers
		// racing over which of them is the primary series is exactly the
		// #2525 shape.
		seriesLinker.markCreated(b.ID)
		// Hydration and the on-disk check happen in the pass below, once the
		// whole created set is known, so their provider calls can be made a
		// few at a time instead of one per book in sequence (#1929).
		createdBooks = append(createdBooks, b)
		added++
	}

	// Fetch the editions the pass below is about to hydrate with. This is the
	// last per-created-book provider round trip left in the sync after #1930
	// removed the library walk: on a 65 work author it was 65 calls, each
	// waiting on the one before it. A refresh that creates nothing queues
	// nothing and so still fetches nothing.
	createdTargets := make([]editionTarget, 0, len(createdBooks))
	for i := range createdBooks {
		if target, ok := h.resolveEditionTarget(ctx, &createdBooks[i], ""); ok {
			createdTargets = append(createdTargets, target)
		}
	}
	seededEditions := make(map[string][]models.Edition, len(editionsByForeignID))
	for foreignID, editions := range editionsByForeignID {
		// The MinPages/SkipMissingISBN pass fetched these unqualified, which
		// is the same call the unqualified hydrate path makes.
		seededEditions[editionTarget{ForeignID: foreignID}.cacheKey()] = editions
	}
	editionCache := h.prefetchHardcoverEditions(ctx, createdTargets, seededEditions)
	if quotaErr == nil {
		// Held calls below fail fast, so record the hold and finish local work.
		quotaErr = h.meta.CheckQuota(ctx)
	}

	// The author's catalogue as this sync leaves it, for the rival title
	// check (#2941): read once here instead of once per bound book.
	authorCatalogue := make([]models.Book, 0, len(allBooks)+len(createdBooks))
	authorCatalogue = append(authorCatalogue, allBooks...)
	authorCatalogue = append(authorCatalogue, createdBooks...)
	for i := range createdBooks {
		b := createdBooks[i]
		// Order within a book is unchanged: hydration can widen MediaType and
		// promote an ASIN, and the on-disk lookup below matches on media type,
		// so it has to see the hydrated value. The format here is the
		// provider's or default.media_type, not a caller's choice, so the
		// widening stays available (#2768).
		h.hydrateHardcoverEditions(ctx, &b, editionCache, false)

		if fileFound := handleNewWantedBookAmong(ctx, h.books, h.series, finder, b, author.Name, authorCatalogue); fileFound {
			continue // don't auto-search for a book we already have
		}

		// Auto-search the freshly-added wanted book only when the per-add
		// flag AND the global auto-grab kill-switch both say yes.
		if autoSearchEnabled && b.Monitored {
			searchQueue = append(searchQueue, b)
		}
	}
	// Now that every created book has its own series written, release the refs
	// held back from them (#2328). Two provider works sharing a normalised
	// title means the second one's series never reached the create path at
	// all, because that pass iterates the created rows and not the works.
	seriesLinker.linkDeferred(ctx)
	seriesLinker.logSummary(author.Name)
	// Every write is done and the announcement list is final, so the next
	// sync of this author may start. Indexer searches and webhook delivery
	// can take minutes and need no lock.
	release()
	runBookSearches(ctx, h.searcher, searchQueue, authorAutoSearchConcurrency)

	// A single-work run records nothing (#1816). It fetched the author's works
	// only to pick out the one book the user explicitly asked for, so its
	// totals — 1 work, 1 added, everything else zero — are not an account of
	// this author's catalogue, and writing them would overwrite a real
	// catalogue sync's numbers on the author page and hide its skip notice.
	if singleWork {
		slog.Info("single-work catalogue fallback complete",
			"author", author.Name, "foreignId", opts.onlyForeignID, "added", added)
		return added, quotaErr
	}

	h.announceDiscoveredBooks(context.WithoutCancel(ctx), author, opts, populatedBefore, createdBooks)

	// Publish the run's accounting so the author page can say what happened to
	// the works that never became books (#1889). Recorded for every sync, not
	// just ones that dropped something: "nothing was filtered out" is the
	// answer to "where are my books?" as often as a count is.
	summary := models.AuthorSyncSummary{
		CompletedAt:              time.Now().UTC(),
		Total:                    len(books),
		Added:                    added,
		Matched:                  matched,
		Failed:                   failed,
		SkippedExcluded:          skippedExcluded,
		SkippedLanguage:          skippedLang,
		SkippedJunk:              skippedJunk,
		SkippedMediaType:         skippedMediaType,
		SkippedNotAccepted:       skippedNotAccepted,
		SkippedPartBooks:         skippedPartBooks,
		SkippedPartBooksSample:   skippedPartBooksSample,
		SkippedMissingDate:       skippedMissingDate,
		SkippedMissingDateSample: skippedMissingDateSample,
		SkippedMinPages:          skippedMinPages,
		SkippedMinPagesSample:    skippedMinPagesSample,
		SkippedMissingISBN:       skippedMissingISBN,
		SkippedMissingISBNSample: skippedMissingISBNSample,
		SkippedThinCluster:       skippedThinCluster,
		SkippedThinClusterSample: skippedThinClusterSample,
		AllowedLanguages:         allowedLangs,
		UnknownLanguageFail:      unknownFail,
		SkippedLanguageSample:    skippedLangSample,
	}
	h.syncSummaries.record(author.ID, summary)

	// Same line, louder when the run dropped something. A refresh that quietly
	// discards most of an author's catalogue is not routine Info-level news,
	// and Info is the default level the in-app log view captures — a reporter
	// running rootless couldn't reach the Debug per-book lines at all.
	logArgs := []any{
		"author", author.Name, "added", added, "matched", matched, "failed", failed,
		"skipped_language", skippedLang, "skipped_junk", skippedJunk, "skipped_media_type", skippedMediaType,
		"skipped_not_accepted", skippedNotAccepted, "skipped_excluded", skippedExcluded,
		"skipped_part_books", skippedPartBooks,
		"skipped_missing_date", skippedMissingDate,
		"skipped_min_pages", skippedMinPages, "skipped_missing_isbn", skippedMissingISBN,
		"skipped_thin_cluster", skippedThinCluster,
		"total", len(books),
	}
	// The metadata filters dropping works is the surprising case and stays at
	// Warn. The discovery-policy skip is not: the user configured it, the run
	// already said so once at Info above, and a "Refresh all authors" pass
	// over an ABS-imported library would otherwise emit one Warn per author.
	//
	// failed joins the Warn side, and it is the strongest member of the set: a
	// filter dropping a work is a setting doing its job, while a create that
	// lost its write is the run failing at the thing it exists to do.
	if failed+skippedLang+skippedJunk+skippedMediaType+skippedPartBooks+skippedMissingDate+
		skippedMinPages+skippedMissingISBN+skippedThinCluster > 0 {
		slog.Warn("author books synced", logArgs...)
		return added, quotaErr
	}
	slog.Info("author books synced", logArgs...)
	return added, quotaErr
}
