package api

import (
	"context"
	"errors"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/vavallee/bindery/internal/bookhydrate"
	"github.com/vavallee/bindery/internal/concurrency"
	"github.com/vavallee/bindery/internal/covers"
	"github.com/vavallee/bindery/internal/db"
	"github.com/vavallee/bindery/internal/jobs"
	"github.com/vavallee/bindery/internal/metadata"
	"github.com/vavallee/bindery/internal/models"
	"github.com/vavallee/bindery/internal/searchruns"
)

var (
	errNoMetadataAggregator = errors.New("metadata aggregator not configured")
	errNoMetadataMatch      = errors.New("no exact-name match in metadata provider")
	// errPrimaryProviderUnavailable is returned instead of a match when the
	// configured primary provider dropped out of the search and the only
	// candidates came from a fallback. Binding one of those writes the wrong
	// provider into the author row permanently (#2271), so these paths keep
	// whatever identity the author already had and let a later attempt, made
	// when the primary is answering, do the linking.
	errPrimaryProviderUnavailable = errors.New("primary metadata provider unavailable, not linking to a fallback provider")
	errAmbiguousMetadataMatch     = errors.New("multiple exact-name matches in metadata provider")
)

const authorAutoSearchConcurrency = 4

// isPartBookTitle reports whether title looks like a box set, omnibus, or
// carton rather than a single book. It is the full two-tier detector: the
// unambiguous keywords plus the lower-confidence ones (a bare trailing
// "omnibus", the slash-separated namings, "Books N-M"), which is what the
// opt-in SkipPartBooks metadata-profile setting has always implied it
// screened out.
//
// The patterns themselves live in internal/metadata (bundle_titles.go), so
// there is one keyword list rather than two: catalogue ingestion prunes the
// unambiguous tier of that same list for every profile (#1780), and this
// call adds the ambiguous tier for profiles that asked for it.
func isPartBookTitle(title string) bool {
	return metadata.IsBundleTitle(title)
}

// syncEnrichmentsPerHour caps how many due authors the many-author syncs
// (Refresh all, bulk refresh, scheduled discovery) enrich in a rolling hour
// between them (#3398). The staggered profile refresh enriches the rest, at
// its own pace, so a large library is never asked about in one burst. A var
// only so tests can lower it.
var syncEnrichmentsPerHour = 100

type AuthorHandler struct {
	// afterProviderFieldsRead is a test seam: it runs between refreshProviderFields
	// re-reading a row and writing it, where a concurrent edit would land (#3105).
	afterProviderFieldsRead func()
	// syncEnrich counts the enrichments those syncs spent in the current
	// hour; see takeSyncEnrichment.
	syncEnrichMu     sync.Mutex
	syncEnrichWindow time.Time
	syncEnrichCount  int

	// coverStore holds photos uploaded through POST /author/{id}/photo,
	// the same store book covers are uploaded to (#3017).
	coverStore                  *covers.Store
	authors                     *db.AuthorRepo
	aliases                     *db.AuthorAliasRepo
	books                       *db.BookRepo
	series                      *db.SeriesRepo
	meta                        *metadata.Aggregator
	settings                    *db.SettingsRepo
	profiles                    *db.MetadataProfileRepo
	searcher                    BookSearcher
	runs                        *searchruns.Store // optional; gives a search on add a pollable run (#3133)
	finder                      LibraryFinder
	editions                    *db.EditionRepo
	roots                       *LibraryRoots // optional: library-root containment for delete
	enhancedHardcoverEnvEnabled bool

	editionFetcher bookhydrate.EditionFetcher

	// lifetimeCtx is the process-lifecycle context, cancelled on server
	// shutdown so the FetchAuthorBooks / orphan-cleanup / SearchOnAdd
	// goroutines do not outlive the process. Falls back to
	// context.Background() when not set; see #846 and recommendations.go.
	lifetimeCtx context.Context

	// jobs tracks the catalogue-sync goroutine so shutdown can drain it before
	// the database closes (#1458, #2371). Optional: without it the sync runs as
	// an untracked goroutine, which is what tests and non-wired callers get.
	jobs *jobs.Group

	// syncSummaries records what each catalogue sync added and dropped so the
	// detail endpoint can report it instead of leaving the drops to a Debug
	// log line nobody reads (#1889).
	syncSummaries authorSyncSummaries

	// runningSyncs counts the catalogue syncs in flight per author, so a
	// manual Refresh can refuse to start a second one and the author page can
	// tell when the one it started has finished (#2601).
	runningSyncs authorSyncsRunning

	// notif publishes bookAnnounced (#2236). Optional; see WithNotifier in
	// author_discovery.go.
	notif eventSender

	// catalogueWrites serialises the write half of catalogue syncs per
	// author, and discovering marks authors a scheduled discovery run holds
	// (#2236). Both live in author_discovery.go.
	catalogueWrites authorCatalogueLocks
	discovering     sync.Map
}

func NewAuthorHandler(authors *db.AuthorRepo, aliases *db.AuthorAliasRepo, books *db.BookRepo, series *db.SeriesRepo, meta *metadata.Aggregator, settings *db.SettingsRepo, profiles *db.MetadataProfileRepo, searcher BookSearcher) *AuthorHandler {
	return &AuthorHandler{authors: authors, aliases: aliases, books: books, series: series, meta: meta, settings: settings, profiles: profiles, searcher: searcher}
}

// WithJobs registers the process-wide background-jobs group so the async
// catalogue sync an author add kicks off is tracked and drained on shutdown
// before the database closes, mirroring Scanner.WithJobs (#1458, #2371).
// Without it the sync is a bare goroutine that a SIGTERM cuts off mid-write,
// leaving the author with a partial catalogue and "database is closed" in the
// log.
func (h *AuthorHandler) WithJobs(g *jobs.Group) *AuthorHandler {
	h.jobs = g
	return h
}

// WithFinder attaches a LibraryFinder to the handler. When set, FetchAuthorBooks
// will check whether each newly-created book already exists on disk before
// queuing an auto-search, preventing re-downloads of books the user owns.
func (h *AuthorHandler) WithFinder(f LibraryFinder) *AuthorHandler {
	h.finder = f
	return h
}

// WithEditionHydration wires edition persistence for supplemental Hardcover
// books created while syncing author catalogues.
func (h *AuthorHandler) WithEditionHydration(editions *db.EditionRepo) *AuthorHandler {
	h.editions = editions
	return h
}

// WithRoots wires the library-root containment checker used by Delete to
// refuse on-disk removal of paths outside the configured library. A nil
// value disables the check.
func (h *AuthorHandler) WithRoots(r *LibraryRoots) *AuthorHandler {
	h.roots = r
	return h
}

// WithEditionFetcher overrides the edition fetcher used by tests.
func (h *AuthorHandler) WithEditionFetcher(fetcher bookhydrate.EditionFetcher) *AuthorHandler {
	h.editionFetcher = fetcher
	return h
}

// WithHardcoverFeatureSettings wires the enhanced Hardcover feature gate used
// when a primary-provider book has only a supplemental Hardcover match.
func (h *AuthorHandler) WithHardcoverFeatureSettings(settings *db.SettingsRepo, envEnabled bool) *AuthorHandler {
	h.settings = settings
	h.enhancedHardcoverEnvEnabled = envEnabled
	return h
}

// WithSearchRuns attaches the store that gives a search on add a run id
// (#3133). Optional.
func (h *AuthorHandler) WithSearchRuns(runs *searchruns.Store) *AuthorHandler {
	h.runs = runs
	return h
}

// WithLifetimeCtx attaches the process-lifecycle context so background work
// started from a request handler (FetchAuthorBooks, AddBook's SearchOnAdd
// goroutine, orphan-cleanup) is cancelled on shutdown rather than running
// against context.Background(). A nil ctx is tolerated and ignored. See #846.
func (h *AuthorHandler) WithLifetimeCtx(ctx context.Context) *AuthorHandler {
	if ctx != nil {
		h.lifetimeCtx = ctx
	}
	return h
}

// bgCtx returns the lifetime context if set, otherwise context.Background().
// Used by every spawn site so tests that construct a handler without
// WithLifetimeCtx keep their previous semantics.
func (h *AuthorHandler) bgCtx() context.Context {
	if h.lifetimeCtx != nil {
		return h.lifetimeCtx
	}
	return context.Background()
}

func (h *AuthorHandler) enhancedHardcoverEnabled(ctx context.Context) bool {
	if h.settings == nil {
		return h.enhancedHardcoverEnvEnabled
	}
	return HardcoverFeatureStateFor(ctx, h.settings, h.enhancedHardcoverEnvEnabled).EnhancedHardcoverAPI
}

// editionTarget names the single Hardcover edition lookup a hydration attempt
// would make. ViaProvider distinguishes the two fetchers the hydrate path picks
// between, because they are separate provider calls even when the id matches.
type editionTarget struct {
	ForeignID   string
	ViaProvider bool
}

// cacheKey is the editionPrefetch map key. The fetcher is part of it: an
// unqualified GetEditions and a Hardcover-scoped GetEditionsFromProvider are
// different calls, so a result from one must not answer for the other.
func (t editionTarget) cacheKey() string {
	if t.ViaProvider {
		return "p:" + t.ForeignID
	}
	return "g:" + t.ForeignID
}

// resolveEditionTarget reports which edition lookup hydrating this book would
// perform, and whether it would hydrate at all.
//
// It is the single source of truth for that decision, shared by the hydrate
// path and by the prefetch that fills its cache, so the two cannot drift into
// disagreeing about which id gets fetched.
func (h *AuthorHandler) resolveEditionTarget(ctx context.Context, book *models.Book, hardcoverForeignID string) (editionTarget, bool) {
	if book == nil || h.editions == nil {
		return editionTarget{}, false
	}
	hardcoverForeignID = strings.TrimSpace(hardcoverForeignID)
	if hardcoverForeignID == "" && !bookhydrate.IsHardcoverBook(book, book.MetadataProvider) {
		hardcoverForeignID = strings.TrimSpace(book.HardcoverForeignID)
	}
	if hardcoverForeignID != "" {
		if !strings.HasPrefix(hardcoverForeignID, "hc:") || !h.enhancedHardcoverEnabled(ctx) {
			return editionTarget{}, false
		}
		return editionTarget{ForeignID: hardcoverForeignID, ViaProvider: true}, true
	}
	// The unqualified path fetches against the book's own id, and
	// bookhydrate drops anything that is not recognisably Hardcover.
	id := strings.TrimSpace(book.ForeignID)
	if id == "" || !bookhydrate.IsHardcoverBook(book, book.MetadataProvider) {
		return editionTarget{}, false
	}
	return editionTarget{ForeignID: id}, true
}

// editionPrefetch holds editions fetched ahead of the sync loop so hydration
// inside the loop does not pay a serial provider round trip per book (#1929).
//
// A miss is not an error: the caller falls back to the live call it would have
// made anyway. That keeps the prefetch a pure optimisation, so if it ever
// predicts the wrong set of books the result is a slower sync rather than a
// wrong one.
type editionPrefetch struct {
	mu sync.Mutex
	by map[string][]models.Edition
}

func newEditionPrefetch() *editionPrefetch {
	return &editionPrefetch{by: make(map[string][]models.Edition)}
}

func (c *editionPrefetch) store(key string, editions []models.Edition) {
	if c == nil {
		return
	}
	c.mu.Lock()
	c.by[key] = editions
	c.mu.Unlock()
}

func (c *editionPrefetch) lookup(key string) ([]models.Edition, bool) {
	if c == nil {
		return nil, false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	editions, ok := c.by[key]
	return editions, ok
}

// wrap returns live with the cache consulted first.
func (c *editionPrefetch) wrap(target editionTarget, live bookhydrate.EditionFetcher) bookhydrate.EditionFetcher {
	if c == nil || live == nil {
		return live
	}
	key := target.cacheKey()
	return func(ctx context.Context, foreignID string) ([]models.Edition, error) {
		// Guard against bookhydrate resolving a different id than the one this
		// cache entry was fetched for; on any mismatch, go live.
		if foreignID == target.ForeignID {
			if editions, ok := c.lookup(key); ok {
				return editions, nil
			}
		}
		return live(ctx, foreignID)
	}
}

// hydrateHardcoverEditions and hydrateMatchedHardcoverEditions fill a book's
// editions from Hardcover. mediaTypePinned forwards the caller's "this format
// was chosen, not guessed" signal so hydration leaves the media type alone
// (#2768). The created-books sync passes false (the format is the provider's or
// default.media_type, the same kind of guess a series fill makes when its
// request names none); the paths that hydrate an existing library row, or a row
// an explicit add named a format for, pass true.
func (h *AuthorHandler) hydrateHardcoverEditions(ctx context.Context, book *models.Book, cache *editionPrefetch, mediaTypePinned bool) {
	h.hydrateHardcoverEditionsFrom(ctx, book, "", cache, mediaTypePinned)
}

func (h *AuthorHandler) hydrateMatchedHardcoverEditions(ctx context.Context, book *models.Book, hardcoverForeignID string, cache *editionPrefetch, mediaTypePinned bool) {
	h.hydrateHardcoverEditionsFrom(ctx, book, hardcoverForeignID, cache, mediaTypePinned)
}

func (h *AuthorHandler) hydrateHardcoverEditionsFrom(ctx context.Context, book *models.Book, hardcoverForeignID string, cache *editionPrefetch, mediaTypePinned bool) {
	target, ok := h.resolveEditionTarget(ctx, book, hardcoverForeignID)
	if !ok {
		return
	}
	fetcher := h.editionFetcher
	if fetcher == nil && h.meta != nil {
		if target.ViaProvider {
			fetcher = func(ctx context.Context, foreignID string) ([]models.Edition, error) {
				return h.meta.GetEditionsFromProvider(ctx, "hardcover", foreignID)
			}
		} else {
			fetcher = h.meta.GetEditions
		}
	}
	providerForeignID := ""
	if target.ViaProvider {
		providerForeignID = target.ForeignID
	}
	bookhydrate.HydrateHardcoverEditions(ctx, bookhydrate.Options{
		Book:              book,
		Provider:          book.MetadataProvider,
		ProviderForeignID: providerForeignID,
		Editions:          h.editions,
		Books:             h.books,
		FetchEditions:     cache.wrap(target, fetcher),
		Enricher:          h.meta,
		MediaTypePinned:   mediaTypePinned,
	})
}

// prefetchEditionBatchSize is how many Hardcover works one prefetch batch names.
// It matches the client's chunk, so a batch is one query.
const prefetchEditionBatchSize = 10

// prefetchHardcoverEditions fills a cache with the edition lookups the sync loop
// is about to make, running them a few at a time instead of one after another.
//
// Before this, edition hydration was the last per-created-book provider call
// left in the loop after #1930 removed the library walk: a 65 work author paid
// 65 sequential Hardcover round trips, each one waiting on the last.
//
// Which books hydrate is decided inside the loop, so this predicts the set from
// the same resolver the loop uses and accepts being wrong: an id fetched here
// that turns out not to be needed costs one call, and one the loop needs that
// is missing here falls through to the live call. Only books the loop would
// actually create are considered, so a refresh that adds nothing still fetches
// nothing.
func (h *AuthorHandler) prefetchHardcoverEditions(ctx context.Context, targets []editionTarget, seeded map[string][]models.Edition) *editionPrefetch {
	cache := newEditionPrefetch()
	for key, editions := range seeded {
		cache.store(key, editions)
	}
	pending := make([]editionTarget, 0, len(targets))
	seen := make(map[string]struct{}, len(targets))
	for _, t := range targets {
		key := t.cacheKey()
		if _, ok := cache.lookup(key); ok {
			continue // already fetched for the MinPages/SkipMissingISBN pass
		}
		if _, dup := seen[key]; dup {
			continue
		}
		seen[key] = struct{}{}
		pending = append(pending, t)
	}
	if len(pending) == 0 || h.meta == nil {
		return cache
	}
	fetchOne := func(ctx context.Context, t editionTarget) {
		var (
			editions []models.Edition
			err      error
		)
		if t.ViaProvider {
			editions, err = h.meta.GetEditionsFromProvider(ctx, "hardcover", t.ForeignID)
		} else {
			editions, err = h.meta.GetEditions(ctx, t.ForeignID)
		}
		if err != nil {
			// Leave it out of the cache. Hydration will retry live and log
			// there, so a transient failure here changes nothing but timing.
			slog.Debug("edition prefetch failed; hydration will fetch it live",
				"foreignId", t.ForeignID, "viaProvider", t.ViaProvider, "error", err)
			return
		}
		cache.store(t.cacheKey(), editions)
	}

	// Targets Hardcover answers go out a chunk per request (#2795). Anything
	// the batch leaves unanswered, and every target another provider owns,
	// takes the per book path above.
	var batchable []editionTarget
	var perBook []editionTarget
	for _, t := range pending {
		if t.ViaProvider || strings.HasPrefix(strings.TrimSpace(t.ForeignID), "hc:") {
			batchable = append(batchable, t)
		} else {
			perBook = append(perBook, t)
		}
	}
	var groups [][]editionTarget
	for start := 0; start < len(batchable); start += prefetchEditionBatchSize {
		groups = append(groups, batchable[start:min(start+prefetchEditionBatchSize, len(batchable))])
	}
	var (
		leftMu     sync.Mutex
		unanswered []editionTarget
	)
	concurrency.RunBounded(ctx, groups, authorAutoSearchConcurrency, func(ctx context.Context, group []editionTarget) {
		ids := make([]string, 0, len(group))
		for _, t := range group {
			ids = append(ids, t.ForeignID)
		}
		answered := h.meta.GetEditionsBatchFromProvider(ctx, "hardcover", ids)
		for _, t := range group {
			if editions, ok := answered[strings.TrimSpace(t.ForeignID)]; ok {
				cache.store(t.cacheKey(), editions)
				continue
			}
			leftMu.Lock()
			unanswered = append(unanswered, t)
			leftMu.Unlock()
		}
	})
	// The per book fetches run a few at a time, as they did before batching.
	perBook = append(perBook, unanswered...)
	concurrency.RunBounded(ctx, perBook, authorAutoSearchConcurrency, fetchOne)
	return cache
}
