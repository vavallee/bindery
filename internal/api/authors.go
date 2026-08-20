package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/vavallee/bindery/internal/auth"
	"github.com/vavallee/bindery/internal/bookhydrate"
	"github.com/vavallee/bindery/internal/concurrency"
	"github.com/vavallee/bindery/internal/db"
	"github.com/vavallee/bindery/internal/indexer"
	"github.com/vavallee/bindery/internal/metadata"
	"github.com/vavallee/bindery/internal/models"
	"github.com/vavallee/bindery/internal/telemetry"
	"github.com/vavallee/bindery/internal/textutil"
)

var (
	errNoMetadataAggregator   = errors.New("metadata aggregator not configured")
	errNoMetadataMatch        = errors.New("no exact-name match in metadata provider")
	errAmbiguousMetadataMatch = errors.New("multiple exact-name matches in metadata provider")
)

const authorAutoSearchConcurrency = 4

// partBookTitleRe matches OpenLibrary work titles that describe a box set,
// omnibus, signed-copy carton, or slash-separated multi-title anthology
// rather than a single book. These "works" are real OL records, so they pass
// every other filter (title is non-empty, language is set, media type
// matches) and land in the wanted list indistinguishable from a real book,
// which is what the (until now unused) SkipPartBooks profile setting always
// implied it screened out.
//
// Ported from the interim external workaround (denoise_author.py) once it
// was confirmed empirically, over several authors, that these patterns catch
// the box-set noise without false-positiving on legitimate titles. Kept as a
// title-text heuristic rather than an OL work-type field because OpenLibrary
// does not reliably distinguish "part"/omnibus works from regular ones in its
// API response.
// omnibus is deliberately NOT in this alternation — see hasNonLeadingOmnibus,
// which applies it with an additional leading-word exclusion the rest of
// these keyword checks don't need.
//
// The two slash branches require actual whitespace around every "/" ("\s+"
// either side, not "\s*"). Real anthology naming is always spaced ("Title A
// / Title B"); an unspaced slash is far more often a title using "/" as its
// own punctuation (a two-character-choice title like "He/She/It", or
// "Rock/Paper/Scissors") than a bundle. Confirmed against real
// maintainer-reported false positives (vavallee, PR #1968 review).
var partBookTitleRe = regexp.MustCompile(`(?i:` +
	`\bbox\s*set\b` +
	`|\bboxed\s*set\b` +
	`|\(\s*boxed\s*\)` + // parenthesized bare "(Boxed)" — real OL titles drop "set" entirely, e.g. "4 Vol. (Boxed)".
	// Deliberately NOT a bare \bboxed\b: that would also match "boxed" used
	// as an ordinary word in a real title, which the parenthesized form
	// this was ported from never does.
	`|\bcollection\s*set\b` +
	`|\bcollection\s+of\s+\d` +
	`|\bcarton\s+of\s+\d+\s+signed\s+cop` +
	`|\bbooks?\s+\d+\s*-\s*\d+\b` + // "Books 1-3". Known low-risk residual: could also
	// match a real single volume a publisher numbered like "Book 1-2" — not observed,
	// and the setting is opt-in, but noted per review rather than left a surprise.
	`|\b\d+\s*(?:books?|vol(?:ume)?s?)\s+set\b` + // "3 Books Set", "5 Volumes Set"
	`)` +
	`|(?:[^/]+\s+/\s+){2,}[^/]+` + // "Title A / Title B / Title C" multi-title anthology naming
	`|\([^()]+\s+/\s+[^()]+\)\s*$`) // "Prefix (Title A / Title B)" — 2-title anthology naming inside parens

// bracketContentRe extracts the content of each bracketed span in a title,
// for hasJoinedBundleBracket.
var bracketContentRe = regexp.MustCompile(`\[([^\]]*)\]`)

// hasJoinedBundleBracket reports whether title has a bracketed annotation
// naming a bundle, e.g. "The Hobbit & The Lord of the Rings [collection/set]".
// Requires the bracket content to contain BOTH a "/" AND one of
// collection/set/boxed — not just any one of those words alone, which a
// real single book's bracketed edition/provenance note could plausibly also
// contain for an unrelated reason (e.g. "[Author's Personal Collection]",
// "[Set in Wartime London]"). The joined-pair shape ("X/Y") is what was
// actually observed on real OpenLibrary bundle records; a lone keyword
// wasn't.
func hasJoinedBundleBracket(title string) bool {
	for _, m := range bracketContentRe.FindAllStringSubmatch(title, -1) {
		content := strings.ToLower(m[1])
		if !strings.Contains(content, "/") {
			continue
		}
		if strings.Contains(content, "collection") || strings.Contains(content, "set") || strings.Contains(content, "boxed") {
			return true
		}
	}
	return false
}

// omnibusWordRe and leadingArticleForOmnibusCheckRe back hasNonLeadingOmnibus.
var omnibusWordRe = regexp.MustCompile(`(?i)\bomnibus\b`)
var leadingArticleForOmnibusCheckRe = regexp.MustCompile(`(?i)^(?:the|an?)\s+`)

// hasNonLeadingOmnibus reports whether "omnibus" appears in title as a
// description of a bundle rather than as the title's own subject. Real
// compilation titles put "omnibus" after the name of what's bundled ("The
// Dune Omnibus", "The Silmarillion Omnibus"); a single real book can also use
// "Omnibus" as its own proper-noun title ("The Omnibus of Crime", a genuine
// Dorothy Sayers volume) or as a publisher's brand name leading the title
// ("Omnibus Press Presents..."). Both shapes put "omnibus" at (or immediately
// after only a leading article before) the very start of the title, which
// this excludes. Confirmed against real maintainer-reported false positives
// (vavallee, PR #1968 review).
//
// Known residual gap, documented rather than chased further: this assumes
// real non-bundle usage always leads and real bundle-descriptor usage always
// trails. Two real books break that — "The New Turing Omnibus" and "Thrown
// under the omnibus" both use "omnibus" metaphorically/idiomatically in a
// trailing position, so they're still wrongly caught. The real distinguishing
// signal (does this book actually bundle other separately-catalogued works)
// isn't recoverable from title text alone for a bare keyword the way it is
// for the slash-joined case (pruneAuthorWorkRedundantTitles can check named
// segments against known titles). Hardcover's IsCompilation classification
// gets both right when configured; left for the maintainer to weigh in on.
func hasNonLeadingOmnibus(title string) bool {
	if !omnibusWordRe.MatchString(title) {
		return false
	}
	stripped := leadingArticleForOmnibusCheckRe.ReplaceAllString(strings.TrimSpace(title), "")
	return !strings.HasPrefix(strings.ToLower(stripped), "omnibus")
}

// isPartBookTitle reports whether title looks like a box set, omnibus, or
// carton rather than a single book. See partBookTitleRe and
// hasNonLeadingOmnibus.
//
// Known residual false positive, not fixed here: a genuine single-volume
// edition bundling several distinct classic works under one title, spaced
// and 3+ segments, e.g. the Penguin Nietzsche edition "The Anti-Christ / Ecce
// Homo / Twilight of the Idols" — indistinguishable by title text alone from
// a real anthology bundle, and the maintainer's own suggested fix (spaces +
// 3+ segments) does not clear it either, since it already satisfies both.
func isPartBookTitle(title string) bool {
	return partBookTitleRe.MatchString(title) || hasNonLeadingOmnibus(title) || hasJoinedBundleBracket(title)
}

type AuthorHandler struct {
	authors                     *db.AuthorRepo
	aliases                     *db.AuthorAliasRepo
	books                       *db.BookRepo
	series                      *db.SeriesRepo
	meta                        *metadata.Aggregator
	settings                    *db.SettingsRepo
	profiles                    *db.MetadataProfileRepo
	searcher                    BookSearcher
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

	// syncSummaries records what each catalogue sync added and dropped so the
	// detail endpoint can report it instead of leaving the drops to a Debug
	// log line nobody reads (#1889).
	syncSummaries authorSyncSummaries
}

func NewAuthorHandler(authors *db.AuthorRepo, aliases *db.AuthorAliasRepo, books *db.BookRepo, series *db.SeriesRepo, meta *metadata.Aggregator, settings *db.SettingsRepo, profiles *db.MetadataProfileRepo, searcher BookSearcher) *AuthorHandler {
	return &AuthorHandler{authors: authors, aliases: aliases, books: books, series: series, meta: meta, settings: settings, profiles: profiles, searcher: searcher}
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

func (h *AuthorHandler) hydrateHardcoverEditions(ctx context.Context, book *models.Book) {
	h.hydrateHardcoverEditionsFrom(ctx, book, "")
}

func (h *AuthorHandler) hydrateMatchedHardcoverEditions(ctx context.Context, book *models.Book, hardcoverForeignID string) {
	h.hydrateHardcoverEditionsFrom(ctx, book, hardcoverForeignID)
}

func (h *AuthorHandler) hydrateHardcoverEditionsFrom(ctx context.Context, book *models.Book, hardcoverForeignID string) {
	if book == nil || h.editions == nil {
		return
	}
	hardcoverForeignID = strings.TrimSpace(hardcoverForeignID)
	if hardcoverForeignID == "" && !bookhydrate.IsHardcoverBook(book, book.MetadataProvider) {
		hardcoverForeignID = strings.TrimSpace(book.HardcoverForeignID)
	}
	if hardcoverForeignID != "" {
		if !strings.HasPrefix(hardcoverForeignID, "hc:") || !h.enhancedHardcoverEnabled(ctx) {
			return
		}
	}
	fetcher := h.editionFetcher
	if fetcher == nil && h.meta != nil {
		if hardcoverForeignID != "" {
			fetcher = func(ctx context.Context, foreignID string) ([]models.Edition, error) {
				return h.meta.GetEditionsFromProvider(ctx, "hardcover", foreignID)
			}
		} else {
			fetcher = h.meta.GetEditions
		}
	}
	bookhydrate.HydrateHardcoverEditions(ctx, bookhydrate.Options{
		Book:              book,
		Provider:          book.MetadataProvider,
		ProviderForeignID: hardcoverForeignID,
		Editions:          h.editions,
		Books:             h.books,
		FetchEditions:     fetcher,
		Enricher:          h.meta,
	})
}

// authorListResponse is the paginated wrapper returned by List. Replaces the
// pre-Wave-2 bare `[]models.Author` shape; clients must unwrap `items` to
// reach the rows. See the Wave 2 / E PR for the breaking-change disclosure.
type authorListResponse struct {
	Items  []models.Author `json:"items"`
	Total  int             `json:"total"`
	Limit  int             `json:"limit"`
	Offset int             `json:"offset"`
}

const (
	authorListDefaultLimit = 100
	authorListMaxLimit     = 500
)

func (h *AuthorHandler) List(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	// Scope the browse list with ListScopeUserID (not UserIDFromContext): admins
	// and API-key/no-tenancy callers get 0 = unscoped so they see the shared
	// library, matching CheckOwnership's per-item bypass; non-admins stay scoped
	// to their own + unowned rows. Fixes admins not seeing another admin's
	// authors in the list even though they can open them by ID.
	userID := auth.ListScopeUserID(ctx)
	limit, offset := parseLimitOffset(r, authorListDefaultLimit, authorListMaxLimit)
	filter := db.AuthorListFilter{
		UserID: userID,
		Search: strings.TrimSpace(r.URL.Query().Get("search")),
		Sort:   r.URL.Query().Get("sort"),
	}
	switch r.URL.Query().Get("monitored") {
	case "true":
		v := true
		filter.Monitored = &v
	case "false":
		v := false
		filter.Monitored = &v
	}
	authors, total, err := h.authors.ListPageFiltered(ctx, filter, limit, offset)
	if err != nil {
		writeServerError(w, r, err)
		return
	}
	if authors == nil {
		authors = []models.Author{}
	}
	for i := range authors {
		cleanAuthorDescription(&authors[i])
		proxyAuthorImages(&authors[i])
	}
	writeJSON(w, http.StatusOK, authorListResponse{
		Items:  authors,
		Total:  total,
		Limit:  limit,
		Offset: offset,
	})
}

func (h *AuthorHandler) Get(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid id"})
		return
	}

	author, err := h.authors.GetByID(r.Context(), id)
	if err != nil {
		writeServerError(w, r, err)
		return
	}
	if author == nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "author not found"})
		return
	}
	// Tier-1 cross-user IDOR guard (D1). Return 404 (not 403) on mismatch so
	// non-owners cannot probe for the existence of another user's authors.
	if !auth.CheckOwnership(r.Context(), author.OwnerUserID) {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "author not found"})
		return
	}

	// Attach books, owner-scoped (#1416): the embedded list must apply the
	// same tenancy predicate as GET /book?authorId=, or a co-author book
	// owned by another user appears here but not there.
	books, err := h.books.ListByAuthorAndUser(r.Context(), id, auth.ListScopeUserID(r.Context()))
	if err == nil {
		author.Books = books
	}

	// Attach aliases so the detail page can show alternate names without a
	// second round-trip.
	if h.aliases != nil {
		if aliases, err := h.aliases.ListByAuthor(r.Context(), id); err == nil {
			author.Aliases = aliases
		}
	}

	// Attach the per-author monitored-series pin set when applicable. The
	// field is omitempty so non-series modes don't bloat the payload, but the
	// edit modal still needs the existing selection to preselect chips.
	if author.MonitorMode == models.AuthorMonitorModeSeries {
		if ids, err := h.authors.ListMonitoredSeriesIDs(r.Context(), id); err == nil {
			author.MonitoredSeriesIDs = ids
		}
	}

	// Attach the last catalogue sync's outcome (#1889). No extra scoping is
	// needed: this hangs off the author the ownership guard above already
	// cleared, so a non-owner gets the 404 and never reaches the counts.
	author.LastSync = h.syncSummaries.get(id)

	proxyAuthorImages(author)
	cleanAuthorDescription(author)
	writeJSON(w, http.StatusOK, author)
}

// ListSeries returns the series belonging to the author — i.e. the series
// that any of the author's books are linked to. Backs the monitor-by-series
// picker (#810) so the edit modal can render a checkbox list scoped to this
// author rather than the global /series collection.
func (h *AuthorHandler) ListSeries(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid id"})
		return
	}
	// Tier-1 cross-user IDOR guard (D1). Look the author up so the ownership
	// check runs before we list series belonging to it.
	author, err := h.authors.GetByID(r.Context(), id)
	if err != nil {
		writeServerError(w, r, err)
		return
	}
	if author == nil || !auth.CheckOwnership(r.Context(), author.OwnerUserID) {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "author not found"})
		return
	}
	if h.series == nil {
		writeJSON(w, http.StatusOK, []models.Series{})
		return
	}
	series, err := h.series.ListByAuthor(r.Context(), id)
	if err != nil {
		writeServerError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, series)
}

func (h *AuthorHandler) Create(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ForeignID             string  `json:"foreignAuthorId"`
		Name                  string  `json:"authorName"`
		QualityProfileID      *int64  `json:"qualityProfileId"`
		MetadataProfileID     *int64  `json:"metadataProfileId"`
		RootFolderID          *int64  `json:"rootFolderId"`
		AudiobookRootFolderID *int64  `json:"audiobookRootFolderId"`
		Monitored             bool    `json:"monitored"`
		MonitorMode           *string `json:"monitorMode"`
		MonitorLatestCount    *int    `json:"monitorLatestCount"`
		SearchOnAdd           bool    `json:"searchOnAdd"`
		MediaType             string  `json:"mediaType"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid request body"})
		return
	}
	if req.ForeignID == "" || req.Name == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "foreignAuthorId and authorName required"})
		return
	}
	monitorMode, monitorLatestCount, err := h.resolveCreateMonitorOptions(r.Context(), req.MonitorMode, req.MonitorLatestCount)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}

	// Check if already exists — use user-scoped lookup so this agrees with the
	// author list, which filters by owner_user_id. A global GetByForeignID
	// would block re-creation of authors orphaned under a different user ID.
	userID := auth.UserIDFromContext(r.Context())
	existing, _ := h.authors.GetByAnyForeignIDForUser(r.Context(), req.ForeignID, userID)
	if existing != nil {
		h.writeCanonicalAuthorConflict(w, existing, "author already exists")
		return
	}

	author, err := h.fetchAuthorForCreate(r.Context(), req.ForeignID, req.Name)
	if err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": err.Error()})
		return
	}
	if author.ForeignID != "" {
		if existing, _ := h.authors.GetByAnyForeignIDForUser(r.Context(), author.ForeignID, userID); existing != nil {
			h.writeCanonicalAuthorConflict(w, existing, "author already exists")
			return
		}
	}
	if canonical, ambiguous, err := h.findCanonicalAuthorMatch(r.Context(), req.Name, author.Name); err != nil {
		writeServerError(w, r, err)
		return
	} else if ambiguous {
		writeJSON(w, http.StatusConflict, map[string]string{"error": "author name resolves ambiguously — merge manually"})
		return
	} else if canonical != nil {
		if canRelinkAuthorToUpstream(canonical) {
			if err := h.relinkExistingAuthorToUpstream(r.Context(), canonical, author, req.Name, req.Monitored, monitorMode, monitorLatestCount, req.QualityProfileID, req.MetadataProfileID, req.RootFolderID, req.AudiobookRootFolderID); err != nil {
				if isAuthorIdentityConflict(err) {
					writeJSON(w, http.StatusConflict, map[string]string{"error": "upstream author already exists locally"})
					return
				}
				writeServerError(w, r, err)
				return
			}
			mediaType := req.MediaType
			if mediaType == "" {
				mediaType = h.resolveDefaultMediaType(r.Context())
			}
			// Finish mutating `canonical` (description clean-up) BEFORE spawning
			// the async catalogue+profile refresh. fetchAuthorBooksAsync snapshots
			// the author at spawn time and the goroutine now reads/writes profile
			// fields (Description, ImageURL, ...); cleaning afterwards would race
			// the snapshot read against this write (see fetchAuthorBooksAsync).
			cleanAuthorDescription(canonical)
			h.fetchAuthorBooksAsync(canonical, catalogueSyncOptions{autoSearch: req.SearchOnAdd, mediaType: mediaType})
			writeJSON(w, http.StatusOK, canonical)
			return
		}
		h.writeCanonicalAuthorConflict(w, canonical, "author name already resolves to an existing author — confirm merge")
		return
	}
	applyAuthorCreateOptions(author, req.Monitored, monitorMode, monitorLatestCount, req.QualityProfileID, req.MetadataProfileID, req.RootFolderID, req.AudiobookRootFolderID)

	if err := h.authors.CreateForUser(r.Context(), author, auth.UserIDFromContext(r.Context())); err != nil {
		slog.Error("create author failed", "foreign_id", req.ForeignID, "error", err)
		if strings.Contains(err.Error(), "UNIQUE constraint failed") || errors.Is(err, db.ErrAuthorIdentifierConflict) {
			if existing, _ := h.authors.GetByAnyForeignIDForUser(r.Context(), req.ForeignID, userID); existing != nil {
				h.writeCanonicalAuthorConflict(w, existing, "author already exists")
				return
			}
			if existing, _ := h.authors.GetByAnyForeignIDForUser(r.Context(), author.ForeignID, userID); existing != nil {
				h.writeCanonicalAuthorConflict(w, existing, "author already exists")
				return
			}
			writeJSON(w, http.StatusConflict, map[string]string{"error": "author already exists"})
			return
		}
		writeServerError(w, r, err)
		return
	}
	h.recordAuthorCreateAlias(r.Context(), author, req.Name)

	// Persist any OL alternate names as alias rows so non-latin primary names
	// (e.g. "村上春樹") get their latin-script alternates ("Haruki Murakami")
	// indexed for release-name matching.
	h.saveAlternateNames(r.Context(), author)

	// Resolve effective media type for books created under this author:
	// explicit request value wins, else the global default.media_type
	// setting, else ebook (backwards compat).
	mediaType := req.MediaType
	if mediaType == "" {
		mediaType = h.resolveDefaultMediaType(r.Context())
	}

	// Clean the description BEFORE spawning the async refresh: the goroutine
	// snapshots `author` and now reads/writes its profile fields (Description,
	// ImageURL, ...). Cleaning after the spawn would race the snapshot read
	// against this write (see fetchAuthorBooksAsync).
	cleanAuthorDescription(author)

	// Fetch and store books for this author. Always populate the catalogue;
	// pass searchOnAdd so FetchAuthorBooks knows whether to also queue grabs.
	h.fetchAuthorBooksAsync(author, catalogueSyncOptions{autoSearch: req.SearchOnAdd, mediaType: mediaType})

	telemetry.MarkFirst(r.Context(), h.settings, telemetry.SettingFirstAuthorAt)
	writeJSON(w, http.StatusCreated, author)
}

func cleanAuthorDescription(author *models.Author) {
	if author != nil {
		author.Description = textutil.CleanDescription(author.Description)
	}
}

// catalogueSyncOptions configures one catalogue sync run (fetchAuthorBooks).
// A struct rather than a widening parameter list: the flags interact — see
// authorAcceptsDiscoveredBooks — and every call site should read as a sentence
// about intent, not as four positional booleans.
type catalogueSyncOptions struct {
	// autoSearch queues an indexer search for each newly-created monitored
	// book. Only the add flow sets it; refresh paths never auto-grab.
	autoSearch bool
	// mediaType is applied to created books the provider gave no format for.
	// Empty accepts whatever the provider set.
	mediaType string
	// discovery marks a REFRESH-path sync (single-author Refresh, bulk
	// refresh, refresh-all, relink) as opposed to the initial add/migrate
	// sync. Refresh runs may only create books the library doesn't have when
	// the author's monitoring policy says it wants new work from them
	// (authorAcceptsDiscoveredBooks) — see #1348, #1815.
	discovery bool
	// onlyForeignID restricts the run to a single provider work. Set by
	// AddBook's fallback so "add this one book" can never turn into "import
	// this author's back catalogue" (#1816). Everything else the provider
	// returns is dropped before the create loop sees it.
	//
	// A run with it set is an explicit user pick standing in for the direct
	// insert, so it also skips the author-wide side effects (profile refresh,
	// Calibre re-link, sync summary) and the catalogue heuristics that may veto
	// a work — the strict media-type clamp and the language filter (#1612).
	onlyForeignID string
}

func (h *AuthorHandler) fetchAuthorBooksAsync(author *models.Author, opts catalogueSyncOptions) {
	if author == nil {
		return
	}
	snapshot := *author
	go h.fetchAuthorBooks(&snapshot, opts)
}

func (h *AuthorHandler) fetchAuthorForCreate(ctx context.Context, foreignID, fallbackName string) (*models.Author, error) {
	if h.meta == nil {
		return &models.Author{
			ForeignID:        foreignID,
			Name:             fallbackName,
			SortName:         sortName(fallbackName),
			MetadataProvider: "openlibrary",
		}, nil
	}
	author, err := h.meta.GetAuthor(ctx, foreignID)
	if err != nil {
		slog.Warn("metadata lookup failed, using provided name", "foreignID", foreignID, "error", err)
		return &models.Author{
			ForeignID:        foreignID,
			Name:             fallbackName,
			SortName:         sortName(fallbackName),
			MetadataProvider: "openlibrary",
		}, nil
	}
	if author == nil {
		return &models.Author{
			ForeignID:        foreignID,
			Name:             fallbackName,
			SortName:         sortName(fallbackName),
			MetadataProvider: "openlibrary",
		}, nil
	}
	if strings.TrimSpace(author.Name) == "" {
		author.Name = fallbackName
	}
	if strings.TrimSpace(author.SortName) == "" {
		author.SortName = sortName(author.Name)
	}
	author.Description = textutil.CleanDescription(author.Description)
	return author, nil
}

func applyAuthorCreateOptions(author *models.Author, monitored bool, monitorMode string, monitorLatestCount int, qualityProfileID, metadataProfileID, rootFolderID, audiobookRootFolderID *int64) {
	author.Monitored = monitored
	author.MonitorMode = monitorMode
	author.MonitorLatestCount = monitorLatestCount
	author.QualityProfileID = qualityProfileID
	author.RootFolderID = rootFolderID
	author.AudiobookRootFolderID = audiobookRootFolderID
	// Default to the seeded "Standard" profile (id=1) so the language filter
	// has something to consult when the UI didn't send an explicit choice.
	// The client can opt out by sending a profile whose allowed_languages is
	// empty or "any".
	if metadataProfileID != nil {
		author.MetadataProfileID = metadataProfileID
	} else {
		def := models.DefaultMetadataProfileID
		author.MetadataProfileID = &def
	}
}

func (h *AuthorHandler) resolveCreateMonitorOptions(ctx context.Context, requestedMode *string, requestedLatestCount *int) (string, int, error) {
	mode := h.resolveDefaultAuthorMonitorMode(ctx)
	if requestedMode != nil {
		mode = strings.TrimSpace(*requestedMode)
	}
	if !models.IsAuthorMonitorModeValid(mode) {
		return "", 0, fmt.Errorf("monitorMode must be one of: all, future, latest, none")
	}

	latestCount := h.resolveDefaultAuthorMonitorLatestCount(ctx)
	if requestedLatestCount != nil {
		latestCount = *requestedLatestCount
	}
	if latestCount <= 0 {
		return "", 0, fmt.Errorf("monitorLatestCount must be a positive integer")
	}
	return mode, latestCount, nil
}

func (h *AuthorHandler) resolveDefaultAuthorMonitorMode(ctx context.Context) string {
	mode, _ := db.ResolveAuthorMonitorDefaults(ctx, h.settings)
	return mode
}

func (h *AuthorHandler) resolveDefaultAuthorMonitorLatestCount(ctx context.Context) int {
	_, latestCount := db.ResolveAuthorMonitorDefaults(ctx, h.settings)
	return latestCount
}

func canRelinkAuthorToUpstream(author *models.Author) bool {
	return models.CanReplaceAuthorIdentity(author)
}

func isAuthorIdentityConflict(err error) bool {
	return err != nil && (strings.Contains(err.Error(), "UNIQUE constraint failed") || errors.Is(err, db.ErrAuthorIdentifierConflict))
}

func (h *AuthorHandler) relinkExistingAuthorToUpstream(ctx context.Context, author, upstream *models.Author, requestedName string, monitored bool, monitorMode string, monitorLatestCount int, qualityProfileID, metadataProfileID, rootFolderID, audiobookRootFolderID *int64) error {
	if author == nil || upstream == nil {
		return errors.New("author relink requires local and upstream authors")
	}
	oldName := author.Name
	oldForeignID := strings.TrimSpace(author.ForeignID)
	if foreignID := strings.TrimSpace(upstream.ForeignID); foreignID != "" {
		author.ForeignID = foreignID
	}
	if name := strings.TrimSpace(upstream.Name); name != "" {
		author.Name = name
	}
	if upstreamSortName := strings.TrimSpace(upstream.SortName); upstreamSortName != "" {
		author.SortName = upstreamSortName
	} else if strings.TrimSpace(author.SortName) == "" {
		author.SortName = sortName(author.Name)
	}
	if desc := textutil.CleanDescription(upstream.Description); desc != "" {
		author.Description = desc
	}
	if imageURL := strings.TrimSpace(upstream.ImageURL); imageURL != "" {
		author.ImageURL = imageURL
	}
	if disambiguation := strings.TrimSpace(upstream.Disambiguation); disambiguation != "" {
		author.Disambiguation = disambiguation
	}
	if upstream.RatingsCount > 0 {
		author.RatingsCount = upstream.RatingsCount
	}
	if upstream.AverageRating > 0 {
		author.AverageRating = upstream.AverageRating
	}
	if provider := strings.TrimSpace(upstream.MetadataProvider); provider != "" {
		author.MetadataProvider = provider
	} else {
		author.MetadataProvider = "openlibrary"
	}
	applyAuthorCreateOptions(author, monitored, monitorMode, monitorLatestCount, qualityProfileID, metadataProfileID, rootFolderID, audiobookRootFolderID)
	if oldForeignID != "" {
		if err := h.authors.UpsertAuthorIdentifier(ctx, author.ID, oldForeignID); err != nil {
			return err
		}
	}
	now := time.Now().UTC()
	author.LastMetadataRefreshAt = &now
	if err := h.authors.Update(ctx, author); err != nil {
		return err
	}
	aliasSource := oldForeignID
	if aliasSource == "" {
		aliasSource = strings.TrimSpace(author.ForeignID)
	}
	h.recordAuthorRelinkAlias(ctx, author, oldName, aliasSource)
	h.recordAuthorCreateAlias(ctx, author, requestedName)
	slog.Info("relinked existing author to upstream metadata", "author", author.Name, "foreignId", author.ForeignID, "previousName", oldName)
	return nil
}

func (h *AuthorHandler) writeCanonicalAuthorConflict(w http.ResponseWriter, canonical *models.Author, message string) {
	if canonical == nil {
		writeJSON(w, http.StatusConflict, map[string]string{"error": message})
		return
	}
	writeJSON(w, http.StatusConflict, map[string]any{
		"error":             message,
		"canonicalAuthorId": canonical.ID,
		"canonicalAuthor":   canonical,
	})
}

func (h *AuthorHandler) findCanonicalAuthorMatch(ctx context.Context, names ...string) (*models.Author, bool, error) {
	return h.findCanonicalAuthorMatchExcluding(ctx, 0, names...)
}

func (h *AuthorHandler) findCanonicalAuthorMatchExcluding(ctx context.Context, excludeID int64, names ...string) (*models.Author, bool, error) {
	var resolved *models.Author
	for _, name := range names {
		match, ambiguous, err := h.findAuthorByNameOrAliasExcluding(ctx, excludeID, name)
		if err != nil {
			return nil, false, err
		}
		if ambiguous {
			return nil, true, nil
		}
		if match == nil {
			continue
		}
		if resolved != nil && resolved.ID != match.ID {
			return nil, true, nil
		}
		resolved = match
	}
	return resolved, false, nil
}

func (h *AuthorHandler) findAuthorByNameOrAliasExcluding(ctx context.Context, excludeID int64, name string) (*models.Author, bool, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return nil, false, nil
	}
	authors, err := h.authors.ListByUser(ctx, auth.UserIDFromContext(ctx))
	if err != nil {
		return nil, false, err
	}
	aliases := []models.AuthorAlias{}
	if h.aliases != nil {
		aliases, err = h.aliases.List(ctx)
		if err != nil {
			return nil, false, err
		}
	}

	exact := make(map[int64]*models.Author)
	needle := strings.ToLower(name)
	for idx := range authors {
		if authors[idx].ID == excludeID {
			continue
		}
		if strings.ToLower(strings.TrimSpace(authors[idx].Name)) != needle {
			continue
		}
		copy := authors[idx]
		exact[copy.ID] = &copy
	}
	for _, alias := range aliases {
		if strings.ToLower(strings.TrimSpace(alias.Name)) != needle {
			continue
		}
		author, err := h.authors.GetByID(ctx, alias.AuthorID)
		if err != nil {
			return nil, false, err
		}
		if author != nil && author.ID != excludeID {
			exact[author.ID] = author
		}
	}
	if len(exact) == 1 {
		for _, author := range exact {
			return author, false, nil
		}
	}
	if len(exact) > 1 {
		return nil, true, nil
	}

	normNeedle := textutil.NormalizeAuthorName(name)
	if normNeedle == "" {
		return nil, false, nil
	}
	normalized := make(map[int64]*models.Author)
	for idx := range authors {
		if authors[idx].ID == excludeID {
			continue
		}
		if textutil.NormalizeAuthorName(authors[idx].Name) != normNeedle {
			continue
		}
		copy := authors[idx]
		normalized[copy.ID] = &copy
	}
	for _, alias := range aliases {
		if textutil.NormalizeAuthorName(alias.Name) != normNeedle {
			continue
		}
		author, err := h.authors.GetByID(ctx, alias.AuthorID)
		if err != nil {
			return nil, false, err
		}
		if author != nil && author.ID != excludeID {
			normalized[author.ID] = author
		}
	}
	if len(normalized) == 1 {
		for _, author := range normalized {
			return author, false, nil
		}
	}
	if len(normalized) > 1 {
		return nil, true, nil
	}
	return nil, false, nil
}

func (h *AuthorHandler) recordAuthorCreateAlias(ctx context.Context, author *models.Author, variant string) {
	if author == nil || h.aliases == nil {
		return
	}
	variant = strings.TrimSpace(variant)
	if variant == "" || strings.EqualFold(strings.TrimSpace(author.Name), variant) {
		return
	}
	if textutil.NormalizeAuthorName(author.Name) != textutil.NormalizeAuthorName(variant) {
		return
	}
	if err := h.aliases.Create(ctx, &models.AuthorAlias{AuthorID: author.ID, Name: variant, SourceOLID: author.ForeignID}); err != nil {
		slog.Debug("author create alias skipped", "author", author.Name, "variant", variant, "error", err)
	}
}

func (h *AuthorHandler) recordAuthorRelinkAlias(ctx context.Context, author *models.Author, variant, sourceForeignID string) {
	if author == nil || h.aliases == nil {
		return
	}
	variant = strings.TrimSpace(variant)
	if variant == "" || strings.EqualFold(strings.TrimSpace(author.Name), variant) {
		return
	}
	sourceForeignID = strings.TrimSpace(sourceForeignID)
	if err := h.aliases.Create(ctx, &models.AuthorAlias{AuthorID: author.ID, Name: variant, SourceOLID: sourceForeignID}); err != nil {
		slog.Debug("author relink alias skipped", "author", author.Name, "variant", variant, "sourceForeignID", sourceForeignID, "error", err)
	}
}

func (h *AuthorHandler) Update(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid id"})
		return
	}

	author, err := h.authors.GetByID(r.Context(), id)
	if err != nil || author == nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "author not found"})
		return
	}
	// Tier-1 cross-user IDOR guard (D1).
	if !auth.CheckOwnership(r.Context(), author.OwnerUserID) {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "author not found"})
		return
	}

	var req struct {
		Monitored             *bool   `json:"monitored"`
		MonitorMode           *string `json:"monitorMode"`
		MonitorLatestCount    *int    `json:"monitorLatestCount"`
		MonitorNewItems       *string `json:"monitorNewItems"`
		QualityProfileID      *int64  `json:"qualityProfileId"`
		MetadataProfileID     *int64  `json:"metadataProfileId"`
		RootFolderID          *int64  `json:"rootFolderId"`
		AudiobookRootFolderID *int64  `json:"audiobookRootFolderId"`
		// ClearAudiobookRootFolder lets the client explicitly reset the
		// per-author audiobook root folder to "use the global dir". A nil
		// AudiobookRootFolderID alone is ambiguous (omitted vs. cleared), so
		// the UI sends this flag when the user picks the default option.
		ClearAudiobookRootFolder   bool `json:"clearAudiobookRootFolder"`
		ApplyMonitorModeToExisting bool `json:"applyMonitorModeToExisting"`
		// MonitoredSeriesIDs is the per-author series pin set (#810). Only
		// meaningful when MonitorMode == "series". A nil pointer means "do
		// not touch the existing selection" — an explicit empty array clears
		// it. Validated against the author's own series before persistence.
		MonitoredSeriesIDs *[]int64 `json:"monitoredSeriesIds"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid request body"})
		return
	}
	if req.Monitored != nil {
		author.Monitored = *req.Monitored
	}
	if req.MonitorMode != nil {
		mode := strings.TrimSpace(*req.MonitorMode)
		if !models.IsAuthorMonitorModeValid(mode) {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "monitorMode must be one of: all, future, latest, none, series"})
			return
		}
		author.MonitorMode = mode
	}
	if req.MonitorNewItems != nil {
		v := strings.TrimSpace(*req.MonitorNewItems)
		if !models.IsAuthorMonitorNewItemsValid(v) {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "monitorNewItems must be one of: all, none"})
			return
		}
		author.MonitorNewItems = v
	}
	if req.MonitorLatestCount != nil {
		if *req.MonitorLatestCount <= 0 {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "monitorLatestCount must be a positive integer"})
			return
		}
		author.MonitorLatestCount = *req.MonitorLatestCount
	}
	if req.QualityProfileID != nil {
		author.QualityProfileID = req.QualityProfileID
	}
	if req.MetadataProfileID != nil {
		author.MetadataProfileID = req.MetadataProfileID
	}
	if req.RootFolderID != nil {
		author.RootFolderID = req.RootFolderID
	}
	if req.AudiobookRootFolderID != nil {
		author.AudiobookRootFolderID = req.AudiobookRootFolderID
	} else if req.ClearAudiobookRootFolder {
		author.AudiobookRootFolderID = nil
	}

	if err := h.authors.Update(r.Context(), author); err != nil {
		writeServerError(w, r, err)
		return
	}

	// Persist the per-author series pin set before applying so the apply pass
	// reads the freshly-written rows. Validate every ID belongs to this
	// author's series — accepting arbitrary series IDs would let one author's
	// monitor selection silently reference an unrelated catalog row.
	if req.MonitoredSeriesIDs != nil {
		if len(*req.MonitoredSeriesIDs) > 0 {
			ownSeries, err := h.series.ListByAuthor(r.Context(), author.ID)
			if err != nil {
				writeServerError(w, r, err)
				return
			}
			owned := make(map[int64]struct{}, len(ownSeries))
			for _, s := range ownSeries {
				owned[s.ID] = struct{}{}
			}
			for _, id := range *req.MonitoredSeriesIDs {
				if _, ok := owned[id]; !ok {
					writeJSON(w, http.StatusBadRequest, map[string]string{"error": fmt.Sprintf("series %d does not belong to author %d", id, author.ID)})
					return
				}
			}
		}
		if err := h.authors.SetMonitoredSeriesIDs(r.Context(), author.ID, *req.MonitoredSeriesIDs); err != nil {
			writeServerError(w, r, err)
			return
		}
		author.MonitoredSeriesIDs = append([]int64(nil), (*req.MonitoredSeriesIDs)...)
	} else if author.MonitorMode == models.AuthorMonitorModeSeries {
		// Mode unchanged or set without overriding the pin set: surface the
		// current selection so the client can render chips without a refetch.
		if ids, err := h.authors.ListMonitoredSeriesIDs(r.Context(), author.ID); err == nil {
			author.MonitoredSeriesIDs = ids
		}
	}

	if req.ApplyMonitorModeToExisting {
		if err := applyMonitorModeToExistingBooks(r.Context(), h.books, h.authors, h.series, author); err != nil {
			writeServerError(w, r, err)
			return
		}
	}
	writeJSON(w, http.StatusOK, author)
}

func applyMonitorModeToExistingBooks(ctx context.Context, booksRepo *db.BookRepo, authorsRepo *db.AuthorRepo, seriesRepo *db.SeriesRepo, author *models.Author) error {
	books, err := booksRepo.ListByAuthorIncludingExcluded(ctx, author.ID)
	if err != nil {
		return fmt.Errorf("list author books: %w", err)
	}
	latestKeys := latestBookMonitorKeys(books, author.MonitorLatestCount, func(b models.Book) bool {
		return !b.Excluded
	})

	// For series mode the decision needs book→series membership. Bulk-load
	// once to avoid an N+1 over GetSeriesIDsForBook in the loop below.
	var (
		monitoredSet map[int64]struct{}
		bookSeries   map[int64][]int64
	)
	if author.MonitorMode == models.AuthorMonitorModeSeries {
		ids, err := authorsRepo.ListMonitoredSeriesIDs(ctx, author.ID)
		if err != nil {
			return fmt.Errorf("list monitored series ids: %w", err)
		}
		monitoredSet = make(map[int64]struct{}, len(ids))
		for _, id := range ids {
			monitoredSet[id] = struct{}{}
		}
		if seriesRepo != nil {
			bookSeries, err = seriesRepo.ListBookSeriesByAuthor(ctx, author.ID)
			if err != nil {
				return fmt.Errorf("list book→series for author: %w", err)
			}
		}
	}

	today := dateOnly(time.Now().UTC())
	for i := range books {
		next := shouldMonitorBookForAuthor(author, books[i], latestKeys, today)
		if author.MonitorMode == models.AuthorMonitorModeSeries {
			next = bookInMonitoredSeries(books[i].ID, bookSeries, monitoredSet)
		}
		// Excluded wins over every mode — a user-excluded book must never
		// flip back to monitored regardless of series membership.
		if books[i].Excluded {
			next = false
		}
		if books[i].Monitored == next {
			continue
		}
		books[i].Monitored = next
		if err := booksRepo.Update(ctx, &books[i]); err != nil {
			return fmt.Errorf("update book %d monitor state: %w", books[i].ID, err)
		}
	}
	return nil
}

// bookInMonitoredSeries reports whether the book belongs to at least one
// series in the author's monitored set. An empty monitored set means "monitor
// nothing" — which is the right default when the user picks series mode
// without selecting any series yet.
func bookInMonitoredSeries(bookID int64, bookSeries map[int64][]int64, monitoredSet map[int64]struct{}) bool {
	if len(monitoredSet) == 0 {
		return false
	}
	for _, sid := range bookSeries[bookID] {
		if _, ok := monitoredSet[sid]; ok {
			return true
		}
	}
	return false
}

func (h *AuthorHandler) RelinkUpstream(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid id"})
		return
	}
	var req struct {
		ForeignID string `json:"foreignAuthorId"`
		Name      string `json:"authorName"`
	}
	if r.Body != nil {
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil && !errors.Is(err, io.EOF) {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid request body"})
			return
		}
	}
	req.ForeignID = strings.TrimSpace(req.ForeignID)
	req.Name = strings.TrimSpace(req.Name)

	userID := auth.UserIDFromContext(r.Context())
	author, err := h.authors.GetByIDForUser(r.Context(), id, userID)
	if err != nil {
		writeServerError(w, r, err)
		return
	}
	if author == nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "author not found"})
		return
	}
	// Tier-1 cross-user IDOR guard (D1).
	if !auth.CheckOwnership(r.Context(), author.OwnerUserID) {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "author not found"})
		return
	}
	if req.ForeignID == "" && !canRelinkAuthorToUpstream(author) {
		writeJSON(w, http.StatusConflict, map[string]string{"error": "author is already linked to upstream metadata"})
		return
	}

	upstream, err := h.resolveRelinkUpstreamAuthor(r.Context(), author.Name, req.ForeignID, req.Name)
	switch {
	case err == nil:
	case errors.Is(err, errNoMetadataAggregator):
		writeJSON(w, http.StatusFailedDependency, map[string]string{"error": err.Error()})
		return
	case errors.Is(err, errNoMetadataMatch):
		writeJSON(w, http.StatusConflict, map[string]string{"error": "no confident upstream author match found"})
		return
	case errors.Is(err, errAmbiguousMetadataMatch):
		writeJSON(w, http.StatusConflict, map[string]string{"error": "author name resolves ambiguously in upstream metadata"})
		return
	default:
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": err.Error()})
		return
	}

	if upstream == nil || strings.TrimSpace(upstream.ForeignID) == "" {
		writeJSON(w, http.StatusConflict, map[string]string{"error": "no confident upstream author match found"})
		return
	}
	if canonical, ambiguous, err := h.findCanonicalAuthorMatchExcluding(r.Context(), author.ID, author.Name, upstream.Name); err != nil {
		writeServerError(w, r, err)
		return
	} else if ambiguous {
		writeJSON(w, http.StatusConflict, map[string]string{"error": "author name resolves ambiguously — merge manually"})
		return
	} else if canonical != nil && canonical.ID != author.ID {
		h.writeCanonicalAuthorConflict(w, canonical, "author name already resolves to an existing author — confirm merge")
		return
	}
	if existing, err := h.authors.GetByAnyForeignIDForUser(r.Context(), upstream.ForeignID, userID); err != nil {
		writeServerError(w, r, err)
		return
	} else if existing != nil && existing.ID != author.ID {
		h.writeCanonicalAuthorConflict(w, existing, "upstream author already exists locally")
		return
	}

	if err := h.relinkExistingAuthorToUpstream(r.Context(), author, upstream, author.Name, author.Monitored, author.MonitorMode, author.MonitorLatestCount, author.QualityProfileID, author.MetadataProfileID, author.RootFolderID, author.AudiobookRootFolderID); err != nil {
		if isAuthorIdentityConflict(err) {
			writeJSON(w, http.StatusConflict, map[string]string{"error": "upstream author already exists locally"})
			return
		}
		writeServerError(w, r, err)
		return
	}

	proxyAuthorImages(author)
	cleanAuthorDescription(author)
	writeJSON(w, http.StatusOK, author)
}

func (h *AuthorHandler) RelinkCandidates(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid id"})
		return
	}
	author, err := h.authors.GetByIDForUser(r.Context(), id, auth.UserIDFromContext(r.Context()))
	if err != nil {
		writeServerError(w, r, err)
		return
	}
	if author == nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "author not found"})
		return
	}
	if h.meta == nil {
		writeJSON(w, http.StatusFailedDependency, map[string]string{"error": errNoMetadataAggregator.Error()})
		return
	}
	term := strings.TrimSpace(r.URL.Query().Get("term"))
	if term == "" {
		term = author.Name
	}
	candidates, err := h.meta.SearchAuthorCandidates(r.Context(), term)
	if err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": err.Error()})
		return
	}
	if candidates == nil {
		candidates = []models.Author{}
	}
	attachedIDs := map[string]struct{}{}
	if foreignID := strings.TrimSpace(author.ForeignID); foreignID != "" {
		attachedIDs[strings.ToLower(foreignID)] = struct{}{}
	}
	identifiers, err := h.authors.ListAuthorIdentifiers(r.Context(), author.ID)
	if err != nil {
		writeServerError(w, r, err)
		return
	}
	for _, identifier := range identifiers {
		if foreignID := strings.TrimSpace(identifier.ForeignID); foreignID != "" {
			attachedIDs[strings.ToLower(foreignID)] = struct{}{}
		}
	}
	filtered := candidates[:0]
	for i := range candidates {
		foreignID := strings.TrimSpace(candidates[i].ForeignID)
		if foreignID != "" {
			if _, ok := attachedIDs[strings.ToLower(foreignID)]; ok {
				continue
			}
		}
		proxyAuthorImages(&candidates[i])
		cleanAuthorDescription(&candidates[i])
		filtered = append(filtered, candidates[i])
	}
	writeJSON(w, http.StatusOK, filtered)
}

func (h *AuthorHandler) Delete(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid id"})
		return
	}

	// Tier-1 cross-user IDOR guard (D1). Look the author up so the ownership
	// check can run before any destructive work; return 404 on mismatch or
	// missing row so non-owners cannot probe for existence by status code.
	author, err := h.authors.GetByID(r.Context(), id)
	if err != nil {
		writeServerError(w, r, err)
		return
	}
	if author == nil || !auth.CheckOwnership(r.Context(), author.OwnerUserID) {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "author not found"})
		return
	}

	// Opt-in `?deleteFiles=true` sweeps every book's on-disk path after the
	// DB delete. We must collect the paths *before* deleting the author —
	// the FK cascade removes the book rows along with it, which would leave
	// us nothing to walk. Per-path errors are logged but don't abort the
	// response: the author is already gone, and a partial sweep is better
	// than rolling the whole thing back.
	//
	// Each path is run through the library-root containment check (Wave 1 /
	// Bundle B): if a `file_path` is outside any configured library root —
	// whether through a tampered import, a buggy migration, or a hostile
	// metadata payload — the on-disk delete is skipped with a WARN log
	// rather than walking outside the library.
	var pathsToRemove []string
	if r.URL.Query().Get("deleteFiles") == "true" {
		// Include excluded books: the FK cascade below deletes them too, so
		// their files must be swept as well or they are orphaned on disk.
		books, err := h.books.ListByAuthorIncludingExcluded(r.Context(), id)
		if err != nil {
			slog.Warn("delete author: failed to list books for file cleanup", "author_id", id, "error", err)
		}
		for _, b := range books {
			// Mirror the single-book delete (books.go Delete): prefer the
			// book_files rows so every tracked ebook/audiobook file — including
			// multi-format books and audiobook folders — is collected. Only fall
			// back to the per-format / legacy columns for books that predate the
			// book_files table (no rows). Collecting b.FilePath alone, as this
			// did before, silently orphaned every file tracked in book_files.
			files, _ := h.books.ListFiles(r.Context(), b.ID)
			if len(files) > 0 {
				for _, f := range files {
					pathsToRemove = append(pathsToRemove, f.Path)
				}
				continue
			}
			for _, p := range []string{b.EbookFilePath, b.AudiobookFilePath, b.FilePath} {
				if p != "" {
					pathsToRemove = append(pathsToRemove, p)
				}
			}
		}
	}

	if err := h.authors.Delete(r.Context(), id); err != nil {
		writeServerError(w, r, err)
		return
	}
	// Drop the sync accounting with the row (#1889) — author ids are reused by
	// SQLite's rowid allocation, and a re-added author must not inherit the
	// deleted one's skip counts.
	h.syncSummaries.forget(id)

	// The author's books (and their book_files rows) were cascade-deleted above,
	// so excludeBookID=0 makes the ownership guard skip any path still tracked by
	// a surviving book of another author — deleting an author must not delete a
	// file some other book still owns (#1368).
	for _, p := range pathsToRemove {
		if _, err := safeRemoveBookPath(r.Context(), h.roots, h.books, 0, p, "", "author_id", id); err != nil {
			slog.Warn("delete author: failed to remove file", "author_id", id, "path", p, "error", err)
		}
	}

	w.WriteHeader(http.StatusNoContent)
}

func (h *AuthorHandler) Refresh(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid id"})
		return
	}

	author, err := h.authors.GetByID(r.Context(), id)
	if err != nil || author == nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "author not found"})
		return
	}
	// Tier-1 cross-user IDOR guard (D1).
	if !auth.CheckOwnership(r.Context(), author.OwnerUserID) {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "author not found"})
		return
	}

	// Manual refresh re-reads the author's metadata but never auto-grabs — the
	// user triggered it to refresh metadata, not to queue downloads. Whether
	// it may also CREATE rows for works the library doesn't have is the
	// author's monitoring policy's call (see authorAcceptsDiscoveredBooks);
	// books that do get created inherit the global default media type, and
	// rows that already exist keep whatever value they were created with.
	h.fetchAuthorBooksAsync(author, catalogueSyncOptions{mediaType: h.resolveDefaultMediaType(r.Context()), discovery: true})
	writeJSON(w, http.StatusAccepted, map[string]string{"message": "refresh started"})
}

// ResolveDefaultMediaType is the exported wrapper around resolveDefaultMediaType
// so out-of-package wiring (e.g. the bulk "refresh" callback in main.go) can
// resolve the same default media type the Refresh endpoint uses.
func (h *AuthorHandler) ResolveDefaultMediaType(ctx context.Context) string {
	return h.resolveDefaultMediaType(ctx)
}

// resolveDefaultMediaType reads the global default.media_type setting and
// falls back to ebook when unset so fresh installs keep the historical
// behaviour. An invalid stored value — should never happen because writes
// are validated — also falls back to ebook.
func (h *AuthorHandler) resolveDefaultMediaType(ctx context.Context) string {
	if h.settings == nil {
		return models.MediaTypeEbook
	}
	s, _ := h.settings.Get(ctx, SettingDefaultMediaType)
	if s == nil || s.Value == "" {
		return models.MediaTypeEbook
	}
	switch s.Value {
	case models.MediaTypeEbook, models.MediaTypeAudiobook, models.MediaTypeBoth:
		return s.Value
	default:
		return models.MediaTypeEbook
	}
}

// resolveDefaultMediaTypeStrict reports whether the strict media-type policy
// is enabled (#1575). When on, catalogue books whose media type falls entirely
// outside default.media_type are skipped at add/refresh time instead of being
// created as un-grabbable rows, and "both" works are narrowed to the default.
// Defaults to off so existing installs keep their historical mixed catalogue;
// a "both" default is unaffected because there is nothing to narrow to.
func (h *AuthorHandler) resolveDefaultMediaTypeStrict(ctx context.Context) bool {
	if h.settings == nil {
		return false
	}
	s, _ := h.settings.Get(ctx, SettingDefaultMediaTypeStrict)
	if s == nil {
		return false
	}
	return s.Value == "true"
}

// isAutoGrabEnabled reads the autoGrab.enabled setting. Defaults to true when
// the key is absent so existing installs keep working without any migration.
func (h *AuthorHandler) isAutoGrabEnabled(ctx context.Context) bool {
	if h.settings == nil {
		return true
	}
	s, _ := h.settings.Get(ctx, "autoGrab.enabled")
	if s == nil {
		return true
	}
	return s.Value != "false"
}

// relinkCalibreAuthor looks up a calibre-imported author by name in the
// configured metadata provider and, on the first match, rewrites the row's
// foreign_id, metadata_provider, image, description, and sort_name in place
// so subsequent catalogue fetches work against a real provider ID.
//
// The match is deliberately conservative: we accept the first search result
// only when its name normalises identically (case- and whitespace-insensitive)
// to the Calibre-supplied name. Anything fuzzier risks mis-linking — users
// can still rename the author manually if they want a different provider row.
//
// A nil return means the author row was updated. Any error means "keep the
// synthetic ID and skip the refresh" — this is a best-effort operation, not a
// hard dependency of the import flow.
func (h *AuthorHandler) relinkCalibreAuthor(ctx context.Context, author *models.Author) error {
	if h.meta == nil {
		return errNoMetadataAggregator
	}
	results, err := h.meta.SearchAuthors(ctx, author.Name)
	if err != nil {
		return err
	}
	normWant := strings.ToLower(strings.TrimSpace(author.Name))
	var match *models.Author
	for i := range results {
		if strings.ToLower(strings.TrimSpace(results[i].Name)) == normWant {
			match = &results[i]
			break
		}
	}
	if match == nil {
		return errNoMetadataMatch
	}

	full, err := h.meta.GetAuthor(ctx, match.ForeignID)
	if err != nil {
		return err
	}
	if full == nil {
		return errNoMetadataMatch
	}

	author.ForeignID = full.ForeignID
	author.MetadataProvider = "openlibrary"
	if full.ImageURL != "" {
		author.ImageURL = full.ImageURL
	}
	if desc := textutil.CleanDescription(full.Description); desc != "" {
		author.Description = desc
	}
	if full.SortName != "" {
		author.SortName = full.SortName
	}
	if full.Disambiguation != "" {
		author.Disambiguation = full.Disambiguation
	}
	if full.RatingsCount > 0 {
		author.RatingsCount = full.RatingsCount
	}
	if full.AverageRating > 0 {
		author.AverageRating = full.AverageRating
	}
	if err := h.authors.Update(ctx, author); err != nil {
		return err
	}
	slog.Info("relinked calibre author to metadata provider",
		"author", author.Name, "newForeignId", author.ForeignID)
	return nil
}

// refreshAuthorProfile re-fetches the author's profile fields (bio, photo,
// disambiguation, ratings) from the linked metadata provider and persists any
// the provider now supplies. The manual "Refresh Metadata" action previously
// only repopulated the catalogue, leaving the author's own Description and
// ImageURL empty even after the user added them upstream (Discussion #1226).
//
// Best-effort: a missing aggregator, a provider miss, or a save error is logged
// and swallowed — the catalogue sync that follows must still run. Identity
// (ForeignID/Name/MetadataProvider) and user-controlled monitoring fields are
// never touched here; only the read-only profile fields are merged.
func (h *AuthorHandler) refreshAuthorProfile(ctx context.Context, author *models.Author) {
	if h.meta == nil || author == nil {
		return
	}
	upstream, err := h.meta.GetAuthor(ctx, author.ForeignID)
	if err != nil {
		slog.Warn("author profile refresh: metadata lookup failed",
			"author", author.Name, "foreignId", author.ForeignID, "error", err)
		return
	}
	if upstream == nil {
		return
	}
	if !mergeAuthorProfileFields(author, upstream) {
		return // provider returned nothing new — skip a no-op write
	}
	now := time.Now().UTC()
	author.LastMetadataRefreshAt = &now
	if err := h.authors.Update(ctx, author); err != nil {
		slog.Warn("author profile refresh: save failed", "author", author.Name, "error", err)
		return
	}
	slog.Info("refreshed author profile from metadata provider",
		"author", author.Name, "foreignId", author.ForeignID)
}

// mergeAuthorProfileFields copies provider-supplied profile fields onto the
// local author, following the project's established "only overwrite when the
// provider has a non-empty value" merge policy (mirrors
// relinkExistingAuthorToUpstream). It reports whether any field actually
// changed so the caller can skip a redundant DB write. SortName is only filled
// when missing, since the local sort order may have been curated.
func mergeAuthorProfileFields(author, upstream *models.Author) bool {
	changed := false
	if desc := textutil.CleanDescription(upstream.Description); desc != "" && desc != author.Description {
		author.Description = desc
		changed = true
	}
	if imageURL := strings.TrimSpace(upstream.ImageURL); imageURL != "" && imageURL != author.ImageURL {
		author.ImageURL = imageURL
		changed = true
	}
	if disambiguation := strings.TrimSpace(upstream.Disambiguation); disambiguation != "" && disambiguation != author.Disambiguation {
		author.Disambiguation = disambiguation
		changed = true
	}
	if upstream.RatingsCount > 0 && upstream.RatingsCount != author.RatingsCount {
		author.RatingsCount = upstream.RatingsCount
		changed = true
	}
	if upstream.AverageRating > 0 && upstream.AverageRating != author.AverageRating {
		author.AverageRating = upstream.AverageRating
		changed = true
	}
	if sn := strings.TrimSpace(upstream.SortName); sn != "" && strings.TrimSpace(author.SortName) == "" {
		author.SortName = sn
		changed = true
	}
	return changed
}

// FetchAuthorBooks populates the author's catalogue from the metadata provider.
// mediaType is applied to each newly-created book when the provider didn't
// return one; pass an empty string to accept whatever the provider set.
//
// This is the INITIAL-sync entry point (add flow, migrations): monitoring of
// created books is governed by the author's MonitorMode alone. Refresh paths
// must use RefreshAuthorBooks instead so the author's MonitorNewItems policy
// applies to later-discovered works (issue #1348).
func (h *AuthorHandler) FetchAuthorBooks(author *models.Author, autoSearch bool, mediaType string) {
	h.fetchAuthorBooks(author, catalogueSyncOptions{autoSearch: autoSearch, mediaType: mediaType})
}

// RefreshAuthorBooks is the discovery variant of FetchAuthorBooks used by the
// refresh paths (single-author Refresh, bulk refresh, refresh-all, relink).
// A refresh always updates the metadata of the books the library already has,
// but it only DISCOVERS new works for an author whose monitoring policy asks
// for them (authorAcceptsDiscoveredBooks) — a refresh must never grow the
// library behind the user's back (issues #1348, #1815).
func (h *AuthorHandler) RefreshAuthorBooks(author *models.Author, autoSearch bool, mediaType string) {
	h.fetchAuthorBooks(author, catalogueSyncOptions{autoSearch: autoSearch, mediaType: mediaType, discovery: true})
}

// authorAcceptsDiscoveredBooks reports whether a refresh-path catalogue sync
// may CREATE book rows for works the library doesn't have yet.
//
// Refreshing metadata and importing a back catalogue are two different
// operations that shared one code path until #1815: a user with every
// monitoring switch off clicked "Refresh metadata" to fix missing cover art
// and got the author's entire bibliography inserted as new rows. Monitoring is
// the only intent signal the user has, so it is the one that decides:
//
//   - Unmonitored author — "I'm not tracking new work from them". Refresh
//     updates what's there and adds nothing.
//   - MonitorNewItems "none" — the setting's whole purpose, and what every
//     importer (ABS, Calibre) stamps on the authors it creates, precisely
//     because those catalogues are partial and the first refresh was the
//     classic detonation point (#1348). It now means "don't add them" rather
//     than "add them unmonitored", which is what the users who asked for it
//     were describing.
//
// MonitorMode "none" is deliberately NOT in this list: with new items allowed
// it is the "list the whole catalogue, monitor none of it" setup (#1290's
// Hardcover-list authors are exactly that), and those rows are created
// unmonitored anyway.
func authorAcceptsDiscoveredBooks(author *models.Author) bool {
	if author == nil {
		return false
	}
	if !author.Monitored {
		return false
	}
	return models.NormalizeAuthorMonitorNewItems(author.MonitorNewItems) != models.AuthorMonitorNewItemsNone
}

// authorAwaitsFirstCatalogue reports whether this author is the empty-author
// repair case: no books at all (excluded ones included — the user excluding
// every book is a decision, not an empty catalogue) and no record of a sync
// ever having populated them.
//
// A read error is answered false. The carve-out is the permissive branch, so
// the safe answer when we can't tell is "don't discover": a refresh that adds
// nothing is a support question, a refresh that re-imports 500 books the user
// deleted is the bug this whole change exists to fix.
func (h *AuthorHandler) authorAwaitsFirstCatalogue(ctx context.Context, author *models.Author, bookCount int) bool {
	if bookCount > 0 {
		return false
	}
	populatedAt, err := h.authors.CataloguePopulatedAt(ctx, author.ID)
	if err != nil {
		slog.Warn("could not read author catalogue-populated marker; treating the author as already populated",
			"author", author.Name, "authorId", author.ID, "error", err)
		return false
	}
	return populatedAt == nil
}

func (h *AuthorHandler) fetchAuthorBooks(author *models.Author, opts catalogueSyncOptions) {
	autoSearch, mediaType, discovery := opts.autoSearch, opts.mediaType, opts.discovery
	// singleWork: the caller picked one specific book and the direct insert
	// couldn't produce it. This run exists only to create that one row, so it
	// touches nothing about the author beyond it — no profile rewrite, no
	// Calibre re-link, no sync summary — and it is exempt from the
	// catalogue-sync heuristics that may veto a work (#1612).
	singleWork := opts.onlyForeignID != ""
	ctx := h.bgCtx()
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
			return
		}
		if err := h.relinkCalibreAuthor(ctx, author); err != nil {
			slog.Info("calibre author not re-linked to metadata provider", "author", author.Name, "reason", err)
			return
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
	if !wasCalibre && !singleWork {
		h.refreshAuthorProfile(ctx, author)
	}

	// Use the dedicated author works endpoint for accurate results, with
	// author-scoped supplemental providers when available.
	books, err := h.meta.GetAuthorWorksForAuthor(ctx, *author)
	if err != nil {
		slog.Error("failed to fetch books", "author", author.Name, "error", err)
		return
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
		if audibleBooks, err := h.meta.GetAuthorAudiobooks(ctx, author.Name); err != nil {
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
	minPopularity := h.resolveMinPopularity(ctx, author)
	minPages, skipMissingISBN := h.resolveEditionFilters(ctx, author)
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
		applyAuthorMajorityLanguageFallback(books)
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
			return
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
	allBooks, _ := h.books.ListByAuthorIncludingExcluded(ctx, author.ID)
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
				for _, s := range ownSeries {
					if _, ok := pinSet[s.ID]; ok && s.ForeignID != "" {
						monitoredSeriesForeignIDs[s.ForeignID] = struct{}{}
					}
				}
			}
		}
	}

	searchQueue := make([]models.Book, 0)
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

	// skippedExcluded is logged but deliberately kept out of AuthorSyncSummary:
	// the author page's notice explains works the user did NOT expect to lose,
	// and a book they excluded by hand is not one of them.
	var added, skippedLang, skippedJunk, skippedMediaType, skippedNotAccepted, skippedExcluded int
	var skippedPartBooks, skippedMissingDate, skippedMinPopularity, skippedMinPages, skippedMissingISBN int
	// Names of the first few language-rejected works, reported to the user
	// alongside the count (#1889): "65 books skipped" is alarming, but it is
	// the titles and their language codes that tell them whether the profile
	// is set the way they meant.
	var skippedLangSample []models.AuthorSyncSkippedBook
	// The first few page-count- and ISBN-rejected works, same reasoning and
	// cap as skippedLangSample (#1889 established the pattern; requested
	// again for these filters specifically in PR review, vavallee).
	var skippedPartBooksSample, skippedMissingDateSample []models.AuthorSyncSkippedBook
	var skippedMinPopularitySample []models.AuthorSyncSkippedBook
	var skippedMinPagesSample, skippedMissingISBNSample []models.AuthorSyncSkippedBook
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
		if !singleWork && !models.IsLanguageAllowed(b.Language, allowedLangs, unknownFail) {
			skippedLang++
			if len(skippedLangSample) < authorSyncSkippedSampleLimit {
				skippedLangSample = append(skippedLangSample, models.AuthorSyncSkippedBook{Title: b.Title, Language: b.Language})
			}
			slog.Debug("skipping non-allowed-language book", "title", b.Title, "language", b.Language, "allowed", allowedLangs, "edition_sampled", langSampled)
			continue
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
	if needsEditionPreview && len(candidates) > 0 {
		editionsByForeignID = make(map[string][]models.Edition, len(candidates))
		var mu sync.Mutex
		concurrency.RunBounded(ctx, candidates, authorAutoSearchConcurrency, func(ctx context.Context, b models.Book) {
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

	for _, b := range candidates {
		// Hoisted here, before the edition-gated filters below, so a filter
		// that fires after this point can exempt a book the user already
		// owns. Without this, a filtered-but-owned book never reaches the
		// update branch: it keeps its files, but silently stops getting
		// rating, genre and cover updates, and is reported as "skipped" on
		// every subsequent sync even though the user is looking at it in
		// their library (vavallee, PR review). MinPages/SkipMissingISBN
		// screen works out of discovery — they must not also stop
		// maintaining ones already accepted.
		existing, _ := h.books.GetByForeignID(ctx, b.ForeignID)

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

		// Filter works whose RatingsCount falls below the metadata profile's
		// MinPopularity floor. A work that hasn't released yet is exempt — it
		// can't have accumulated ratings, so judging it by a rating count
		// would only ever penalize forthcoming books, never the intended
		// "low-interest backlist noise" target.
		//
		// hasRatingSignal distinguishes "confirmed unpopular" from "unknown":
		// RatingsCount is not something OpenLibrary reliably supplies (it
		// arrives via the Hardcover supplement in mergeAuthorWorks), so on an
		// install with no Hardcover token — or for any work Hardcover
		// doesn't know — RatingsCount is 0 because nobody told us, not
		// because the work has zero ratings. AverageRating==0 alongside it
		// is indistinguishable from missing data, so a work with no rating
		// signal at all is treated as unknown and passes, mirroring how
		// MinPages treats a work with no page data as unknown rather than
		// zero (vavallee, PR review).
		hasRatingSignal := b.RatingsCount > 0 || b.AverageRating > 0
		if existing == nil && minPopularity > 0 && hasRatingSignal && b.RatingsCount < minPopularity &&
			(b.ReleaseDate == nil || !b.ReleaseDate.After(time.Now())) {
			skippedMinPopularity++
			if len(skippedMinPopularitySample) < authorSyncSkippedSampleLimit {
				skippedMinPopularitySample = append(skippedMinPopularitySample, models.AuthorSyncSkippedBook{Title: b.Title})
			}
			slog.Debug("skipping below-popularity-floor work", "title", b.Title, "ratingsCount", b.RatingsCount, "minPopularity", minPopularity)
			continue
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
		if existing, seen := seenTitles.Lookup(b.Title); seen && existing != nil {
			hydrateExistingFromMatchedHardcover := false
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
				if err := h.books.Update(ctx, existing); err != nil {
					slog.Warn("authors: update during dedup", "error", err, "book_id", existing.ID)
				} else if existing.WantsAudiobook() {
					hydrateExistingFromMatchedHardcover = true
				}
			case canUpgradeToBoth(existing.MediaType, b.MediaType):
				// One Work is ebook, the other is audiobook — merge into a single
				// dual-format row instead of creating a second book entry.
				existing.MediaType = models.MediaTypeBoth
				if b.RatingsCount > 0 && (existing.RatingsCount == 0 || b.RatingsCount > existing.RatingsCount) {
					existing.RatingsCount = b.RatingsCount
					existing.AverageRating = b.AverageRating
				}
				if err := h.books.Update(ctx, existing); err != nil {
					slog.Warn("failed to upgrade book to dual-format", "title", existing.Title, "error", err)
				} else {
					slog.Debug("upgraded book to dual-format", "title", existing.Title, "foreignId", b.ForeignID)
					hydrateExistingFromMatchedHardcover = true
				}
			default:
				// Same media type duplicate — just refresh ratings if we have better data.
				if b.RatingsCount > 0 && (existing.RatingsCount == 0 || b.RatingsCount > existing.RatingsCount) {
					existing.RatingsCount = b.RatingsCount
					existing.AverageRating = b.AverageRating
					if err := h.books.Update(ctx, existing); err != nil {
						slog.Warn("authors: update during dedup", "error", err, "book_id", existing.ID)
					}
				}
				hydrateExistingFromMatchedHardcover = existing.WantsAudiobook()
			}
			if hydrateExistingFromMatchedHardcover {
				h.hydrateMatchedHardcoverEditions(ctx, existing, b.HardcoverForeignID)
			}
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
		// scoping sees the sync's output. NULL-owned authors stay NULL-owned.
		b.OwnerUserID = author.OwnerUserID
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
					return
				}
			}
			slog.Warn("failed to create book", "title", b.Title, "error", err)
			continue
		}
		h.hydrateHardcoverEditions(ctx, &b)
		added++

		if fileFound := handleNewWantedBook(ctx, h.books, h.series, finder, b, author.Name); fileFound {
			continue // don't auto-search for a book we already have
		}

		// Auto-search the freshly-added wanted book only when the per-add
		// flag AND the global auto-grab kill-switch both say yes.
		if autoSearchEnabled && b.Monitored {
			searchQueue = append(searchQueue, b)
		}
	}
	runBookSearches(ctx, h.searcher, searchQueue, authorAutoSearchConcurrency)

	// A single-work run records nothing (#1816). It fetched the author's works
	// only to pick out the one book the user explicitly asked for, so its
	// totals — 1 work, 1 added, everything else zero — are not an account of
	// this author's catalogue, and writing them would overwrite a real
	// catalogue sync's numbers on the author page and hide its skip notice.
	if singleWork {
		slog.Info("single-work catalogue fallback complete",
			"author", author.Name, "foreignId", opts.onlyForeignID, "added", added)
		return
	}

	// Publish the run's accounting so the author page can say what happened to
	// the works that never became books (#1889). Recorded for every sync, not
	// just ones that dropped something: "nothing was filtered out" is the
	// answer to "where are my books?" as often as a count is.
	summary := models.AuthorSyncSummary{
		CompletedAt:                time.Now().UTC(),
		Total:                      len(books),
		Added:                      added,
		SkippedLanguage:            skippedLang,
		SkippedJunk:                skippedJunk,
		SkippedMediaType:           skippedMediaType,
		SkippedNotAccepted:         skippedNotAccepted,
		SkippedPartBooks:           skippedPartBooks,
		SkippedPartBooksSample:     skippedPartBooksSample,
		SkippedMissingDate:         skippedMissingDate,
		SkippedMissingDateSample:   skippedMissingDateSample,
		SkippedMinPopularity:       skippedMinPopularity,
		SkippedMinPopularitySample: skippedMinPopularitySample,
		SkippedMinPages:            skippedMinPages,
		SkippedMinPagesSample:      skippedMinPagesSample,
		SkippedMissingISBN:         skippedMissingISBN,
		SkippedMissingISBNSample:   skippedMissingISBNSample,
		AllowedLanguages:           allowedLangs,
		UnknownLanguageFail:        unknownFail,
		SkippedLanguageSample:      skippedLangSample,
	}
	h.syncSummaries.record(author.ID, summary)

	// Same line, louder when the run dropped something. A refresh that quietly
	// discards most of an author's catalogue is not routine Info-level news,
	// and Info is the default level the in-app log view captures — a reporter
	// running rootless couldn't reach the Debug per-book lines at all.
	logArgs := []any{
		"author", author.Name, "added", added,
		"skipped_language", skippedLang, "skipped_junk", skippedJunk, "skipped_media_type", skippedMediaType,
		"skipped_not_accepted", skippedNotAccepted, "skipped_excluded", skippedExcluded,
		"skipped_part_books", skippedPartBooks,
		"skipped_missing_date", skippedMissingDate, "skipped_min_popularity", skippedMinPopularity,
		"skipped_min_pages", skippedMinPages, "skipped_missing_isbn", skippedMissingISBN,
		"total", len(books),
	}
	// The metadata filters dropping works is the surprising case and stays at
	// Warn. The discovery-policy skip is not: the user configured it, the run
	// already said so once at Info above, and a "Refresh all authors" pass
	// over an ABS-imported library would otherwise emit one Warn per author.
	if skippedLang+skippedJunk+skippedMediaType+skippedPartBooks+skippedMissingDate+
		skippedMinPopularity+skippedMinPages+skippedMissingISBN > 0 {
		slog.Warn("author books synced", logArgs...)
		return
	}
	slog.Info("author books synced", logArgs...)
}

// keepWorkWithForeignID narrows a provider works list to the single work the
// caller asked for. Returns an empty slice when the list doesn't contain it —
// the sync then adds nothing, which is the correct answer for a single-book
// add whose work the author endpoint doesn't list (#1816).
func keepWorkWithForeignID(books []models.Book, foreignID string) []models.Book {
	for _, b := range books {
		if b.ForeignID == foreignID {
			return []models.Book{b}
		}
	}
	return nil
}

// reparentMisattachedBook re-links existing to the author being synced when
// the row is attached to an author that demonstrably shouldn't own it (issue
// #1405). foreign_id is globally UNIQUE, so a work fetched during this
// author's sync can match a row created under a duplicate/stale author record
// (OpenLibrary has duplicate author keys), a calibre shell author, or an
// author that no longer exists — and such a row never shows on this author's
// page. The provider's credited-author list is what separates that from a
// legitimately co-authored work: stealing a co-authored row would make it
// ping-pong between its authors on alternating syncs, so when the current
// owner is credited (or authorship is unknown) the row stays put.
// Returns true when existing.AuthorID was changed; the caller persists it.
func (h *AuthorHandler) reparentMisattachedBook(ctx context.Context, existing *models.Book, author *models.Author, creditedAuthorIDs []string) bool {
	if existing.AuthorID == author.ID {
		return false
	}
	owner, err := h.authors.GetByID(ctx, existing.AuthorID)
	if err != nil {
		// Can't verify ownership — leave the row alone rather than moving it
		// on a transient DB error.
		return false
	}
	switch {
	case owner == nil:
		// Owner row was deleted; the book is orphaned.
	case owner.ForeignID == author.ForeignID:
		// Duplicate author rows sharing one provider identity.
	case len(creditedAuthorIDs) > 0 && !slices.Contains(creditedAuthorIDs, owner.ForeignID):
		// The synced author's catalogue contains this work but the current
		// owner isn't credited on it — a mis-parent, not a co-author.
	default:
		// Owner is credited on the work (co-author) or authorship is unknown
		// (no credited list from this provider) — don't move it.
		return false
	}
	slog.Info("re-linking book to synced author",
		"title", existing.Title, "book_id", existing.ID,
		"from_author_id", existing.AuthorID, "to_author_id", author.ID)
	existing.AuthorID = author.ID
	return true
}

// handleNewWantedBook performs the post-create steps that every newly-created
// wanted book requires regardless of the creation path:
//  1. Link any provider series refs into the series/series_books tables.
//  2. Check whether the user already owns the file via LibraryFinder; if so,
//     record the path and return true so the caller can skip auto-search.
//
// Returns true when an existing file was found (caller must NOT auto-search),
// false otherwise.
func handleNewWantedBook(ctx context.Context, books *db.BookRepo, series *db.SeriesRepo, finder LibraryFinder, book models.Book, authorName string) (fileFound bool) {
	// Populate series membership for this book.
	if series != nil {
		for _, ref := range book.SeriesRefs {
			s := &models.Series{ForeignID: ref.ForeignID, Title: ref.Title}
			if err := series.CreateOrGet(ctx, s); err != nil {
				slog.Warn("failed to upsert series", "series", ref.Title, "error", err)
				continue
			}
			if err := series.LinkBook(ctx, s.ID, book.ID, ref.Position, ref.Primary); err != nil {
				slog.Warn("failed to link book to series", "book", book.Title, "series", ref.Title, "error", err)
			}
		}
	}

	// Check if the user already owns this book before queuing a download.
	if finder != nil {
		if existingPath := finder.FindExisting(ctx, book.Title, authorName, book.MediaType); existingPath != "" {
			slog.Info("library: found existing file, skipping auto-search", "title", book.Title, "path", existingPath)
			if err := books.SetFilePath(ctx, book.ID, existingPath); err != nil {
				slog.Warn("authors: record existing file path", "error", err, "book_id", book.ID)
			}
			return true
		}
	}
	return false
}

func runBookSearches(ctx context.Context, searcher BookSearcher, books []models.Book, maxConcurrent int) {
	if searcher == nil || len(books) == 0 {
		return
	}
	// Paced so a large author's auto-search doesn't burst every indexer at
	// once as slots free up (#1515); shares the package pace with the bulk
	// "search all" and series-fill fan-outs.
	concurrency.RunBoundedPaced(ctx, books, maxConcurrent, searchPaceInterval, func(ctx context.Context, book models.Book) {
		searcher.SearchAndGrabBook(ctx, book)
	})
}

func shouldMonitorBookForAuthor(author *models.Author, book models.Book, latestKeys map[string]struct{}, today time.Time) bool {
	if author == nil || !author.Monitored {
		return false
	}
	switch author.MonitorMode {
	case models.AuthorMonitorModeFuture:
		if book.ReleaseDate == nil {
			return false
		}
		return dateOnly(book.ReleaseDate.UTC()).After(today)
	case models.AuthorMonitorModeLatest:
		_, ok := latestKeys[indexer.NormalizeTitleForDedup(book.Title)]
		return ok
	case models.AuthorMonitorModeNone:
		return false
	case models.AuthorMonitorModeSeries:
		// Newly-discovered books default to unmonitored under series mode:
		// the series-membership join is established by the series sync
		// later, so we don't yet know which series this book belongs to.
		// A subsequent ApplyMonitorModeToExisting (manual or scheduled
		// refresh) flips the flag on once the join row exists.
		return false
	case models.AuthorMonitorModeAll, "":
		return true
	default:
		return true
	}
}

type latestMonitorCandidate struct {
	key         string
	title       string
	releaseDate time.Time
}

func latestBookMonitorKeys(books []models.Book, count int, include func(models.Book) bool) map[string]struct{} {
	if count <= 0 {
		count = models.DefaultAuthorMonitorLatestCount
	}
	byKey := make(map[string]latestMonitorCandidate)
	for _, book := range books {
		if book.ReleaseDate == nil {
			continue
		}
		if include != nil && !include(book) {
			continue
		}
		key := indexer.NormalizeTitleForDedup(book.Title)
		if key == "" {
			continue
		}
		candidate := latestMonitorCandidate{
			key:         key,
			title:       book.Title,
			releaseDate: dateOnly(book.ReleaseDate.UTC()),
		}
		if existing, ok := byKey[key]; ok && !candidate.releaseDate.After(existing.releaseDate) {
			continue
		}
		byKey[key] = candidate
	}
	candidates := make([]latestMonitorCandidate, 0, len(byKey))
	for _, candidate := range byKey {
		candidates = append(candidates, candidate)
	}
	sort.Slice(candidates, func(i, j int) bool {
		if !candidates[i].releaseDate.Equal(candidates[j].releaseDate) {
			return candidates[i].releaseDate.After(candidates[j].releaseDate)
		}
		return strings.ToLower(candidates[i].title) < strings.ToLower(candidates[j].title)
	})
	if len(candidates) > count {
		candidates = candidates[:count]
	}
	keys := make(map[string]struct{}, len(candidates))
	for _, candidate := range candidates {
		keys[candidate.key] = struct{}{}
	}
	return keys
}

func isAuthorWorkMonitorCandidate(book models.Book, normalizedAuthor string, allowedLangs []string, unknownFail bool) bool {
	normalizedTitle := strings.ToLower(strings.TrimSpace(book.Title))
	if normalizedTitle == "" || normalizedTitle == normalizedAuthor {
		return false
	}
	return models.IsLanguageAllowed(book.Language, allowedLangs, unknownFail)
}

func dateOnly(t time.Time) time.Time {
	y, m, d := t.UTC().Date()
	return time.Date(y, m, d, 0, 0, 0, 0, time.UTC)
}

func (h *AuthorHandler) resolveRelinkUpstreamAuthor(ctx context.Context, name, foreignID, candidateName string) (*models.Author, error) {
	if h.meta == nil {
		return nil, errNoMetadataAggregator
	}
	foreignID = strings.TrimSpace(foreignID)
	if foreignID == "" {
		return h.lookupUpstreamAuthorByName(ctx, name)
	}
	upstream, err := h.meta.GetAuthor(ctx, foreignID)
	if err == nil && upstream != nil {
		return upstream, nil
	}
	candidate, fallbackErr := h.lookupRelinkCandidateByForeignID(ctx, foreignID, candidateName)
	if fallbackErr == nil {
		return candidate, nil
	}
	if strings.TrimSpace(candidateName) != "" {
		return nil, fallbackErr
	}
	if err != nil {
		return nil, err
	}
	return nil, errNoMetadataMatch
}

func (h *AuthorHandler) lookupRelinkCandidateByForeignID(ctx context.Context, foreignID, candidateName string) (*models.Author, error) {
	foreignID = strings.TrimSpace(foreignID)
	candidateName = strings.TrimSpace(candidateName)
	if foreignID == "" || candidateName == "" {
		return nil, errNoMetadataMatch
	}
	results, err := h.meta.SearchAuthorCandidates(ctx, candidateName)
	if err != nil {
		return nil, err
	}
	for idx := range results {
		if strings.TrimSpace(results[idx].ForeignID) != foreignID {
			continue
		}
		copy := results[idx]
		return &copy, nil
	}
	return nil, errNoMetadataMatch
}

func (h *AuthorHandler) lookupUpstreamAuthorByName(ctx context.Context, name string) (*models.Author, error) {
	if h.meta == nil {
		return nil, errNoMetadataAggregator
	}
	name = strings.TrimSpace(name)
	if name == "" {
		return nil, errNoMetadataMatch
	}
	want := textutil.NormalizeAuthorName(name)
	if want == "" {
		return nil, errNoMetadataMatch
	}

	queries := authorSearchQueries(name)
	var match *models.Author
	matchedQuery := ""
	for _, query := range queries {
		results, err := h.meta.SearchAuthors(ctx, query)
		if err != nil {
			return nil, err
		}
		for idx := range results {
			if textutil.NormalizeAuthorName(results[idx].Name) != want {
				continue
			}
			if match != nil {
				slog.Info("author relink match ambiguous", "author", name, "query", query)
				return nil, errAmbiguousMetadataMatch
			}
			copy := results[idx]
			match = &copy
		}
		if match != nil {
			matchedQuery = query
			break
		}
	}
	if match == nil {
		slog.Debug("author relink match not found", "author", name, "queries", queries)
		return nil, errNoMetadataMatch
	}

	full, err := h.meta.GetAuthor(ctx, match.ForeignID)
	if err != nil {
		return nil, err
	}
	if full == nil {
		return nil, errNoMetadataMatch
	}
	slog.Info("author relink candidate matched", "author", name, "query", matchedQuery, "foreignId", match.ForeignID)
	return full, nil
}

// authorSearchQueries delegates to textutil, which owns this expansion. It and
// the three helpers it used were byte-identical to the copies in internal/abs
// (#1648).
func authorSearchQueries(name string) []string {
	return textutil.AuthorSearchQueries(name)
}

// AddBook adds a single book to the wanted list by its metadata foreign ID.
// If the author is not yet in Bindery it is added as unmonitored and its
// books are fetched in the background; the endpoint then polls until the
// requested book appears and marks it monitored before responding.
//
// foreignAuthorId is optional. When omitted (typical for DNB search results,
// which don't expose author IDs), the handler resolves the author by looking
// up the book's ISBN against every metadata provider and picking the first
// hit that carries a real author ID — almost always OpenLibrary. When that
// resolution succeeds, both foreignBookId and foreignAuthorId are rewritten
// to the resolved provider's IDs so the rest of the existing flow works as
// before. When it fails, the request is rejected with a friendly hint about
// adding the author manually first.
// findLibraryAuthorByName returns an author already in the user's library whose
// name canonically matches the given name (treating "Last, First" == "First
// Last"), or nil. Lets a name-only search result (e.g. Google Books, which has
// no author ID) attach to the user's existing author instead of duplicating it.
// Identity uses the same key as ResolveCanonicalAuthor so the two agree.
func (h *AuthorHandler) findLibraryAuthorByName(ctx context.Context, name string) *models.Author {
	want := metadata.CanonicalAuthorKey(name)
	if want == "" {
		return nil
	}
	authors, err := h.authors.ListByUser(ctx, auth.UserIDFromContext(ctx))
	if err != nil {
		return nil
	}
	for i := range authors {
		if metadata.CanonicalAuthorKey(authors[i].Name) == want {
			return &authors[i]
		}
	}
	return nil
}

func (h *AuthorHandler) AddBook(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ForeignBookID   string `json:"foreignBookId"`
		ForeignAuthorID string `json:"foreignAuthorId"`
		AuthorName      string `json:"authorName"`
		SearchOnAdd     bool   `json:"searchOnAdd"`
		// MediaType optionally forces ebook/audiobook/both for the added book
		// (#1397). Empty keeps the provider's media type, falling back to the
		// default.media_type setting — the pre-selector behaviour.
		MediaType string `json:"mediaType"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid request body"})
		return
	}
	if req.ForeignBookID == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "foreignBookId required"})
		return
	}
	switch req.MediaType {
	case "", models.MediaTypeEbook, models.MediaTypeAudiobook, models.MediaTypeBoth:
	default:
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "mediaType must be 'ebook', 'audiobook', or 'both'"})
		return
	}

	ctx := r.Context()

	// bookCreated flips true once the poll loop confirms the requested
	// book is in the DB. The orphan-cleanup defer below reads it on
	// AddBook return — when false (poll timeout, ctx cancel, etc.) the
	// just-created author row is deleted iff it has zero books. Fixes
	// issue #667 bug 3.
	bookCreated := false

	// authorWasJustCreated tracks whether this request inserted the author
	// row (vs. found it already present). Used by both the orphan-cleanup
	// defer and the direct-insert block below — when the author was just
	// created the async catalogue sync may take longer than the 15s poll
	// budget for prolific authors, so we synchronously persist the requested
	// book to guarantee it exists before the cleanup defer runs (#804).
	authorWasJustCreated := false

	if req.ForeignAuthorID == "" {
		resolved, err := h.resolveAuthorForBook(ctx, req.ForeignBookID)
		if err != nil {
			writeJSON(w, http.StatusBadGateway, map[string]string{"error": err.Error()})
			return
		}
		if resolved != nil {
			// Rewrite the request so the existing fetch+poll flow targets the
			// canonical provider's IDs. The user sees the canonical record (e.g.
			// the OpenLibrary version) in their library; the original DNB record
			// is dropped because bindery's author/book identity is single-source.
			req.ForeignBookID = resolved.ForeignID
			req.ForeignAuthorID = resolved.Author.ForeignID
			if req.AuthorName == "" {
				req.AuthorName = resolved.Author.Name
			}
		} else if req.AuthorName != "" {
			// ISBN-based resolution failed (e.g. Google Books: author name, no
			// author ID, no ISBN). Resolve the author by NAME — prefer one already
			// in the library so we reuse the user's existing author instead of
			// duplicating it; otherwise adopt OpenLibrary's canonical record. Keep
			// the chosen edition (req.ForeignBookID) — the other providers don't
			// have this book.
			if existing := h.findLibraryAuthorByName(ctx, req.AuthorName); existing != nil {
				req.ForeignAuthorID = existing.ForeignID
			} else if canonical, cErr := h.meta.ResolveCanonicalAuthor(ctx, req.AuthorName); cErr == nil && canonical != nil {
				req.ForeignAuthorID = canonical.ForeignID
			}
		}
		if req.ForeignAuthorID == "" {
			writeJSON(w, http.StatusUnprocessableEntity, map[string]string{
				"error": "Author metadata unavailable for this result. Add the author manually first (Authors → Add Author by name), then try again.",
			})
			return
		}
	}

	// 1. Find or create the author (unmonitored if new so we don't auto-want all books).
	userID := auth.UserIDFromContext(ctx)
	author, _ := h.authors.GetByForeignIDForUser(ctx, req.ForeignAuthorID, userID)
	if author == nil {
		author, _ = h.authors.GetByAnyForeignIDForUser(ctx, req.ForeignAuthorID, userID)
	}
	if author == nil {
		name := req.AuthorName
		if name == "" {
			name = req.ForeignAuthorID
		}
		fetched, err := h.meta.GetAuthor(ctx, req.ForeignAuthorID)
		if err != nil || fetched == nil {
			fetched = &models.Author{
				ForeignID:        req.ForeignAuthorID,
				Name:             name,
				SortName:         sortName(name),
				MetadataProvider: "openlibrary",
			}
		}
		fetched.Monitored = false
		def := models.DefaultMetadataProfileID
		fetched.MetadataProfileID = &def

		// Dedupe path: if a canonical provider (OL / Hardcover / …) is being
		// added for a SortName previously persisted as a synthetic DNB-only
		// row, migrate that row in place rather than creating a duplicate.
		// The synthetic row was created because the DNB record had only an
		// author name (no GND link, no OL coverage). Now that a canonical
		// identity exists, collapse the two onto a single primary key so
		// the user keeps one author with all their books attached.
		if !strings.HasPrefix(fetched.ForeignID, "dnb:") {
			if existing, lookupErr := h.authors.GetByDNBSyntheticName(ctx, fetched.SortName, userID); lookupErr == nil && existing != nil {
				if err := h.authors.UpgradeSyntheticDNB(ctx, existing.ForeignID, fetched); err != nil {
					slog.Debug("AddBook: upgrade synthetic DNB author failed", "from", existing.ForeignID, "to", fetched.ForeignID, "error", err)
				} else {
					// Re-fetch the row by its new canonical ForeignID so subsequent
					// steps see the upgraded record (ID preserved).
					if upgraded, getErr := h.authors.GetByForeignIDForUser(ctx, fetched.ForeignID, userID); getErr == nil && upgraded != nil {
						author = upgraded
					}
				}
			}
		}

		// CreateForUser may collide with a concurrent request inserting the
		// same author; the UNIQUE-constraint branch below recovers by
		// re-fetching the row. authorWasJustCreated stays false on the race
		// path so the orphan-cleanup defer never rolls back somebody else's
		// author row (issue #667).
		if author == nil {
			// Add-book creates the author as a side effect, so it never carries
			// an explicit monitor choice — take the install-wide default (#1666).
			db.ApplyAuthorMonitorDefaults(ctx, h.settings, fetched)
			if err := h.authors.CreateForUser(ctx, fetched, userID); err != nil {
				if !strings.Contains(err.Error(), "UNIQUE constraint failed") && !errors.Is(err, db.ErrAuthorIdentifierConflict) {
					writeServerError(w, r, err)
					return
				}
				// Race: another request created it between our check and insert.
				author, _ = h.authors.GetByAnyForeignIDForUser(ctx, req.ForeignAuthorID, userID)
				if author == nil {
					writeJSON(w, http.StatusConflict, map[string]string{"error": "author already exists"})
					return
				}
			} else {
				author = fetched
				authorWasJustCreated = true
				// No speculative catalogue fetch here (#1816). Adding a book
				// creates its author as a side effect; the user picked ONE
				// title, and pulling that author's whole bibliography in behind
				// it is the "my collection went from 75 books to over 500"
				// report — the thing nobody expects because adding a film to
				// Radarr does not import the director's filmography.
				//
				// Nothing downstream needs it: the direct insert below creates
				// the picked book synchronously, which is what makes the poll
				// succeed and what keeps the orphan-cleanup defer from
				// rolling the author back. The narrow case where that insert
				// cannot produce the row has its own single-work fallback,
				// just past the direct-insert block.
			}
		}
		// Defer the orphan cleanup so cancellation paths inside the poll
		// loop also benefit. Runs only after a CreateForUser this request.
		if authorWasJustCreated {
			defer h.cleanupOrphanIfNoBooks(author, &bookCreated)
		}
	}
	if author == nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "could not resolve author"})
		return
	}

	// 1b. Direct insert for the requested book.
	//
	// Originally added (#667) for DNB synthetic IDs, whose async sync returns
	// zero books because DNB's public SRU has no author→works relationship.
	// #804 widened this: for any author the request just created, the async
	// catalogue sync can take longer than the 15 s poll budget (OpenLibrary
	// took >32 s for a 175-work author in the bug report). When the poll
	// times out, the orphan-cleanup defer deletes the author row — and the
	// still-running goroutine then logs a FK-constraint failure for every
	// book it tries to insert against the now-deleted author_id.
	//
	// Synchronously fetching and persisting the single requested record
	// guarantees the poll succeeds on its first iteration AND that the
	// cleanup defer sees a non-empty book list (so it keeps the author).
	// The async sync still runs as a backfill for the rest of the catalogue;
	// any UNIQUE collision against this row is silently tolerated.
	//
	// #1612 made the direct insert unconditional. When the author already
	// EXISTED, AddBook used to skip it and rely entirely on a catalogue sync
	// having created the row — but the sync can deterministically refuse a
	// specific work (e.g. the work-level language sampled from the first few
	// OpenLibrary editions falls outside the profile's allowed set, which is
	// how heavily-translated works ended up permanently un-addable). Every
	// attempt then polled 15 s for a row nothing would ever create and
	// returned 404 "try again shortly" forever. An explicit add of one
	// specific work is the strongest possible user signal and must not be
	// vetoed by catalogue-sync heuristics; those heuristics still govern
	// everything the user did NOT explicitly pick.
	if existing, _ := h.books.GetByForeignID(ctx, req.ForeignBookID); existing == nil {
		primary, err := h.meta.GetBook(ctx, req.ForeignBookID)
		if err != nil {
			slog.Warn("AddBook: direct fetch failed",
				"foreignBookId", req.ForeignBookID, "error", err)
		} else if primary != nil && h.directInsertTitleUsable(primary.Title, author.Name) {
			primary.AuthorID = author.ID
			// Tenancy (#1457): inherit the author's owner. Read off the author
			// row rather than the request, because that row usually pre-exists
			// this request (#1612) — the scoped lookup above is what makes it
			// the correct owner either way.
			primary.OwnerUserID = author.OwnerUserID
			primary.Monitored = author.Monitored
			if primary.Status == "" {
				primary.Status = models.BookStatusWanted
			}
			// An explicit request choice wins over the provider's media type
			// (#1397). Otherwise some providers (notably Google Books) don't
			// set one; fall back to the global default so the row isn't
			// created with an empty format (which would mis-route its
			// indexer search).
			if req.MediaType != "" {
				primary.MediaType = req.MediaType
			} else if primary.MediaType == "" {
				primary.MediaType = h.resolveDefaultMediaType(ctx)
			}
			// Reuse a title-equivalent row under the same author instead of
			// inserting a second one. The catalogue sync runs this same dedup
			// (see the FindByAuthorAndDedupKey switch above), and skipping it
			// here produced real duplicates: a Calibre-imported library holds
			// the work under a `calibre:` foreign id, and OpenLibrary splits
			// some works into separate ebook and audiobook Works that the sync
			// merges into one media_type=both row — in both cases the
			// requested foreign id has no row of its own, which is exactly the
			// state that brings a user here.
			match, ferr := h.books.FindByAuthorAndDedupKey(ctx, author.ID, primary.Title)
			// A subtitle-collapsed dedup key (indexer.CanonicalDedupKey strips a
			// ": subtitle" tail) merges every "Series: Volume" sibling onto one
			// key. Adopting such a match would rebind the requested foreign id
			// onto a *different* volume and — because adopt is a no-op when the
			// row needs no field change — leave the poll below unable to find the
			// requested id, returning 404 forever. When the requested work is a
			// distinct volume of the matched row's series (same series, different
			// sequence), skip the adopt and create a distinct row instead.
			if ferr == nil && match != nil && !h.directInsertSeriesConflict(ctx, match.ID, primary.SeriesRefs) {
				h.adoptDirectInsertMatch(ctx, match, primary, req.ForeignBookID)
			} else if err := h.books.Create(ctx, primary); err != nil {
				if !strings.Contains(err.Error(), "UNIQUE constraint failed") {
					slog.Warn("AddBook: direct insert failed",
						"foreignBookId", req.ForeignBookID, "error", err)
				}
			} else {
				h.hydrateHardcoverEditions(ctx, primary)
				// Same post-create work every other creation path does
				// (recommendations.go, series.go): check the library for a
				// file we already have and link the book into its series.
				// Without this the row is wanted-but-unchecked, so with
				// searchOnAdd enabled Bindery re-downloads a book already on
				// disk — the regression #940 and migration 026 exist to stop.
				created := *primary
				handleNewWantedBook(ctx, h.books, h.series, h.finder, created, author.Name)
			}
		}
	}

	// 1c. Single-work fallback. The direct insert above covers the request in
	// all but a couple of cases: the provider's book endpoint failed (#1612's
	// OpenLibrary 502) or returned a record the title guard rejected, and the
	// library has no row for the id either way. Ask the author endpoint for
	// this ONE work instead — the same fetch the old speculative catalogue
	// sync ran, restricted to the work the user actually picked, so the poll
	// below can still succeed without the rest of the bibliography riding
	// along (#1816).
	if existing, _ := h.books.GetByForeignID(ctx, req.ForeignBookID); existing == nil {
		// mediaType only fills a format the provider left blank, and step 3
		// below applies the request's explicit choice to whatever row the poll
		// finds — so the default is the right value to pass here. It no longer
		// decides whether the work is created at all: a single-work run is
		// exempt from the strict media-type clamp (#1612).
		h.fetchAuthorBooksAsync(author, catalogueSyncOptions{
			mediaType:     h.resolveDefaultMediaType(ctx),
			onlyForeignID: req.ForeignBookID,
		})
	}

	// 2. Poll until the book appears (the single-work fallback, if it ran,
	// creates it asynchronously).
	deadline := time.Now().Add(15 * time.Second)
	var book *models.Book
	for {
		b, _ := h.books.GetByForeignID(ctx, req.ForeignBookID)
		if b != nil {
			book = b
			break
		}
		if time.Now().After(deadline) {
			break
		}
		select {
		case <-ctx.Done():
			writeJSON(w, http.StatusGatewayTimeout, map[string]string{"error": "request cancelled"})
			return
		case <-time.After(500 * time.Millisecond):
		}
	}

	if book == nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "book not found after author sync — try again shortly"})
		return
	}
	bookCreated = true

	// 3. Mark the book monitored (wanted). An explicit media-type choice is
	// applied here too — the poll may have found a row created by the async
	// catalogue sync (or one already in the library) carrying the default.
	// Re-evaluate status on a change so e.g. adding an already-imported ebook
	// as 'both' flips it back to wanted for the missing format (#1148).
	book.Monitored = true
	if req.MediaType != "" && book.MediaType != req.MediaType {
		book.MediaType = req.MediaType
		reevaluateBookStatus(book)
	}
	if err := h.books.Update(ctx, book); err != nil {
		writeServerError(w, r, err)
		return
	}

	// 3b. Say so when this add went past the strict media-type policy (#1759).
	//
	// The policy is a catalogue-population rule, not a veto on what the user
	// may own: the direct insert above never consults it, and the single-work
	// fallback is exempt by #1612's rule that "an explicit add of one specific
	// work must not be vetoed by catalogue-sync heuristics". Both of those are
	// deliberate, because silently refusing an explicit user action is the
	// worse of the two failures.
	//
	// What was missing is that it happened invisibly, so a user who turned the
	// setting on to stop un-grabbable rows appearing had no way to learn that
	// their own add was the exception. The setting's help text now says the
	// same thing, which is the half most people will actually see.
	h.logStrictMediaTypeBypass(ctx, book)

	// 4. Optionally trigger an indexer search. Use the process-lifecycle
	// context so the search goroutine is cancelled on shutdown rather than
	// running against context.Background(). See #846.
	if req.SearchOnAdd && h.searcher != nil {
		go h.searcher.SearchAndGrabBook(h.bgCtx(), *book) // #nosec G118 -- intentional: search must outlive the request
	}

	writeJSON(w, http.StatusCreated, book)
}

// logStrictMediaTypeBypass records an explicit add that the strict media-type
// policy would have excluded from a catalogue sync.
//
// Fires only when the policy is on AND pinned to one format, which is the
// condition under which fetchAuthorBooks narrows a dual-format work or skips a
// single-format one. A default of "both" clamps nothing, so there is nothing to
// bypass and nothing to say.
func (h *AuthorHandler) logStrictMediaTypeBypass(ctx context.Context, book *models.Book) {
	if !h.strictMediaTypeBypassed(ctx, book) {
		return
	}
	slog.Info("added a book the strict media-type default would have excluded from a catalogue sync; an explicit add is not vetoed by it",
		"title", book.Title, "bookMediaType", book.MediaType, "default", h.resolveDefaultMediaType(ctx))
}

// strictMediaTypeBypassed is logStrictMediaTypeBypass's condition, split out so
// the decision can be asserted directly rather than by capturing log output.
func (h *AuthorHandler) strictMediaTypeBypassed(ctx context.Context, book *models.Book) bool {
	if book == nil || !h.resolveDefaultMediaTypeStrict(ctx) {
		return false
	}
	def := h.resolveDefaultMediaType(ctx)
	// Only a single-format default clamps anything, so only that can be
	// bypassed. "both" narrows nothing and skips nothing.
	if def != models.MediaTypeEbook && def != models.MediaTypeAudiobook {
		return false
	}
	return book.MediaType != def
}

// cleanupOrphanIfNoBooks deletes the given author iff (a) the book add
// did not succeed (bookCreated is false) and (b) the author currently has
// zero books in the DB. Called via defer in AddBook so any path that
// returns without a successful book add — poll timeout, ctx cancel, 500
// from book.Update — rolls back the speculative author insert. Uses a
// background context so client-cancellation paths still run the cleanup.
//
// The "zero books" guard is what makes this safe even when the async
// FetchAuthorBooks goroutine has raced ahead and inserted some books for
// this author: in that case the user still gets an author with content,
// so we keep it. For DNB synthetic IDs (issue #667), the async fetch
// returns zero rows deterministically and this cleanup fires.
func (h *AuthorHandler) cleanupOrphanIfNoBooks(author *models.Author, bookCreated *bool) {
	if bookCreated != nil && *bookCreated {
		return
	}
	if author == nil || author.ID == 0 {
		return
	}
	ctx := h.bgCtx()
	books, err := h.books.ListByAuthor(ctx, author.ID)
	if err != nil {
		slog.Warn("AddBook: orphan-cleanup ListByAuthor failed",
			"authorId", author.ID, "error", err)
		return
	}
	if len(books) > 0 {
		return
	}
	if err := h.authors.Delete(ctx, author.ID); err != nil {
		slog.Warn("AddBook: orphan-cleanup Delete failed",
			"authorId", author.ID, "foreignId", author.ForeignID, "error", err)
		return
	}
	slog.Info("AddBook: cleaned up orphan author after failed add",
		"authorId", author.ID, "foreignId", author.ForeignID, "name", author.Name)
}

// resolveAuthorForBook looks up the foreign book by primary metadata
// provider, walks its editions for an ISBN, then asks the aggregator to find
// the same ISBN in any provider that exposes a real author ID. Returns nil
// when no ISBN is found or no provider can place the author. This is the
// fallback path for AddBook when the search result didn't carry a
// foreignAuthorId — currently the case for every DNB result.
func (h *AuthorHandler) resolveAuthorForBook(ctx context.Context, foreignBookID string) (*models.Book, error) {
	primaryBook, err := h.meta.GetBook(ctx, foreignBookID)
	if err != nil {
		return nil, fmt.Errorf("look up book metadata: %w", err)
	}
	if primaryBook == nil {
		return nil, nil
	}
	for _, ed := range primaryBook.Editions {
		var isbn string
		switch {
		case ed.ISBN13 != nil && *ed.ISBN13 != "":
			isbn = *ed.ISBN13
		case ed.ISBN10 != nil && *ed.ISBN10 != "":
			isbn = *ed.ISBN10
		}
		if isbn == "" {
			continue
		}
		resolved, err := h.meta.ResolveBookByISBN(ctx, isbn)
		if err != nil {
			slog.Debug("resolveAuthorForBook: provider lookup failed", "isbn", isbn, "error", err)
			continue
		}
		if resolved != nil {
			return resolved, nil
		}
	}
	return nil, nil
}

// saveAlternateNames persists any latin-script OL alternate names from
// author.AlternateNames into the author_aliases table. This lets non-latin
// primary names (e.g. "村上春樹") be matched against latin-script release
// names (e.g. "Murakami") during indexer searches.
func (h *AuthorHandler) saveAlternateNames(ctx context.Context, author *models.Author) {
	if h.aliases == nil || len(author.AlternateNames) == 0 {
		return
	}
	for _, name := range author.AlternateNames {
		if !isAllASCII(name) {
			continue
		}
		alias := &models.AuthorAlias{AuthorID: author.ID, Name: name}
		if err := h.aliases.Create(ctx, alias); err != nil {
			slog.Debug("saveAlternateNames: could not save alias", "name", name, "authorId", author.ID, "error", err)
		}
	}
}

// isAllASCII returns true when every byte of s is a 7-bit ASCII character.
func isAllASCII(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] > 127 {
			return false
		}
	}
	return true
}

// canUpgradeToBoth reports whether combining existingMediaType and
// incomingMediaType yields a dual-format upgrade. It returns true exactly when
// one side is "ebook" and the other is "audiobook" — the two formats are
// complementary, so the already-tracked row should become "both" rather than
// a second row being created (issue #442).
// directInsertTitleUsable rejects provider records that would create a
// nameless row. The catalogue sync blocks the same two shapes before insert;
// without the guard an empty title persists and renders destination folders
// like "Jared M. Diamond/Jared M. Diamond ()".
func (h *AuthorHandler) directInsertTitleUsable(title, authorName string) bool {
	title = strings.TrimSpace(title)
	if title == "" {
		return false
	}
	return !strings.EqualFold(title, strings.TrimSpace(authorName))
}

// directInsertSeriesConflict reports whether the dedup-key match found for a
// direct insert is a *different volume of the same series* as the requested
// work: the match's primary series title equals one of the requested work's
// series refs and both sequence numbers are present but unequal. When true the
// caller creates a distinct row rather than adopting the collapsed sibling. A
// blank sequence on either side, or no primary series on the match, is not a
// conflict — there is no positive evidence the two are different volumes, so the
// existing dedup/adopt behavior is preserved.
func (h *AuthorHandler) directInsertSeriesConflict(ctx context.Context, matchBookID int64, requested []models.SeriesRef) bool {
	if h.series == nil || len(requested) == 0 {
		return false
	}
	matchTitle, matchPos, err := h.series.GetPrimarySeriesForBook(ctx, matchBookID)
	if err != nil {
		return false
	}
	matchTitle = strings.TrimSpace(matchTitle)
	matchPos = strings.TrimSpace(matchPos)
	if matchTitle == "" || matchPos == "" {
		return false
	}
	for _, ref := range requested {
		name := strings.TrimSpace(ref.Title)
		pos := strings.TrimSpace(ref.Position)
		if name == "" || pos == "" {
			continue
		}
		if strings.EqualFold(name, matchTitle) && !seriesSequencesEqual(pos, matchPos) {
			return true
		}
	}
	return false
}

// seriesSequencesEqual compares two series sequence strings numerically when
// both parse ("1" == "1.0"), else by trimmed case-insensitive string compare.
func seriesSequencesEqual(a, b string) bool {
	a = strings.TrimSpace(a)
	b = strings.TrimSpace(b)
	if fa, errA := strconv.ParseFloat(a, 64); errA == nil {
		if fb, errB := strconv.ParseFloat(b, 64); errB == nil {
			return fa == fb
		}
	}
	return strings.EqualFold(a, b)
}

// adoptDirectInsertMatch points an existing title-equivalent row at the
// requested foreign id instead of inserting a duplicate, mirroring the
// catalogue sync's dedup arms: a Calibre stub is upgraded to the real
// provider id so the caller's poll finds it, and an ebook/audiobook pair
// collapses into one dual-format row.
func (h *AuthorHandler) adoptDirectInsertMatch(ctx context.Context, match, primary *models.Book, foreignID string) {
	changed := false
	if strings.HasPrefix(match.ForeignID, "calibre:") {
		match.ForeignID = foreignID
		changed = true
	}
	if canUpgradeToBoth(match.MediaType, primary.MediaType) {
		match.MediaType = models.MediaTypeBoth
		changed = true
	}
	if match.Language == "" && primary.Language != "" {
		match.Language = primary.Language
		changed = true
	}
	if !changed {
		slog.Debug("AddBook: direct insert deduped against existing row",
			"foreignBookId", foreignID, "bookId", match.ID, "title", match.Title)
		return
	}
	if err := h.books.Update(ctx, match); err != nil {
		slog.Warn("AddBook: failed to adopt existing row during direct insert",
			"foreignBookId", foreignID, "bookId", match.ID, "error", err)
		return
	}
	h.hydrateHardcoverEditions(ctx, match)
}

func canUpgradeToBoth(existingMediaType, incomingMediaType string) bool {
	switch {
	case existingMediaType == models.MediaTypeEbook && incomingMediaType == models.MediaTypeAudiobook:
		return true
	case existingMediaType == models.MediaTypeAudiobook && incomingMediaType == models.MediaTypeEbook:
		return true
	default:
		return false
	}
}

func (h *AuthorHandler) resolveAllowedLanguages(ctx context.Context, author *models.Author) ([]string, bool) {
	id := models.DefaultMetadataProfileID
	if author.MetadataProfileID != nil {
		id = *author.MetadataProfileID
	}
	p, err := h.profiles.GetByID(ctx, id)
	if err != nil || p == nil {
		return []string{"eng"}, false
	}
	return models.ParseAllowedLanguages(p.AllowedLanguages), p.UnknownLanguageBehavior == models.UnknownLanguageFail
}

// authorMajorityLanguageMinSample is the minimum number of already-resolved
// works required before applying the majority fallback at all — one or two
// data points aren't enough to confidently label the rest of a batch, no
// matter how uniform they happen to be.
const authorMajorityLanguageMinSample = 3

// authorMajorityLanguageThreshold is how dominant a single language must be
// among a batch's resolved works before it's applied to unresolved ones.
// High enough that a genuinely mixed-language author (translations,
// multiple original languages) isn't force-tagged into one language.
const authorMajorityLanguageThreshold = 0.9

// applyAuthorMajorityLanguageFallback assigns each still-blank-Language book
// in books the majority language already resolved among the rest of the
// batch, when that majority clears authorMajorityLanguageThreshold over at
// least authorMajorityLanguageMinSample resolved works. Mutates books in
// place. A no-op when too few works have a resolved language yet, or when
// no single language dominates strongly enough to trust.
func applyAuthorMajorityLanguageFallback(books []models.Book) {
	counts := make(map[string]int, 4)
	resolved := 0
	for _, b := range books {
		if b.Language == "" {
			continue
		}
		counts[b.Language]++
		resolved++
	}
	if resolved < authorMajorityLanguageMinSample {
		return
	}
	var majorityLang string
	var majorityCount int
	for lang, count := range counts {
		if count > majorityCount {
			majorityLang, majorityCount = lang, count
		}
	}
	if float64(majorityCount)/float64(resolved) < authorMajorityLanguageThreshold {
		return
	}
	for i := range books {
		if books[i].Language == "" {
			books[i].Language = majorityLang
		}
	}
}

// resolveSkipMissingDate returns the author's effective metadata profile's
// SkipMissingDate setting. Defaults to false (matches the setting's prior
// no-op behavior) on any lookup failure, so an unresolvable profile never
// turns into unexpected catalogue loss.
func (h *AuthorHandler) resolveSkipMissingDate(ctx context.Context, author *models.Author) bool {
	id := models.DefaultMetadataProfileID
	if author.MetadataProfileID != nil {
		id = *author.MetadataProfileID
	}
	p, err := h.profiles.GetByID(ctx, id)
	if err != nil || p == nil {
		return false
	}
	return p.SkipMissingDate
}

// resolveMinPopularity returns the author's effective metadata profile's
// MinPopularity floor. Zero (the field's default) means "no filter" —
// matches the profile settings UI, which renders 0 as "none". Defaults to
// zero on any lookup failure, so an unresolvable profile never causes
// unexpected catalogue loss.
func (h *AuthorHandler) resolveMinPopularity(ctx context.Context, author *models.Author) int {
	id := models.DefaultMetadataProfileID
	if author.MetadataProfileID != nil {
		id = *author.MetadataProfileID
	}
	p, err := h.profiles.GetByID(ctx, id)
	if err != nil || p == nil {
		return 0
	}
	return p.MinPopularity
}

// resolveEditionFilters returns the author's effective metadata profile's
// MinPages floor and SkipMissingISBN setting. Combined into one lookup
// because both gate the same per-candidate edition prefetch in
// fetchAuthorBooks. Defaults to "no filter" (0, false) on any lookup
// failure, so an unresolvable profile never causes unexpected catalogue
// loss.
func (h *AuthorHandler) resolveEditionFilters(ctx context.Context, author *models.Author) (minPages int, skipMissingISBN bool) {
	id := models.DefaultMetadataProfileID
	if author.MetadataProfileID != nil {
		id = *author.MetadataProfileID
	}
	p, err := h.profiles.GetByID(ctx, id)
	if err != nil || p == nil {
		return 0, false
	}
	return p.MinPages, p.SkipMissingISBN
}

// anyEditionHasISBN reports whether any edition carries an ISBN-13 or
// ISBN-10. Returns false for a nil or empty slice — a work with no editions
// to check has no ISBN to confirm.
func anyEditionHasISBN(editions []models.Edition) bool {
	for _, e := range editions {
		if e.ISBN13 != nil && strings.TrimSpace(*e.ISBN13) != "" {
			return true
		}
		if e.ISBN10 != nil && strings.TrimSpace(*e.ISBN10) != "" {
			return true
		}
	}
	return false
}

// passesMinPagesFilter reports whether a work satisfies the profile's
// MinPages floor. Mirrors Chaptarr's semantics: an edition meeting the
// floor passes the work, but if no edition reports a page count at all,
// that's treated as unknown rather than zero and passes through
// unfiltered — a work with no page data is not the same as a work with no
// pages. (Named to read as "did this pass the filter", not "does the work
// have enough pages" — the unknown-data case makes those two questions
// have different answers.)
func passesMinPagesFilter(editions []models.Edition, minPages int) bool {
	anyReported := false
	for _, e := range editions {
		if e.NumPages == nil || *e.NumPages <= 0 {
			continue
		}
		anyReported = true
		if *e.NumPages >= minPages {
			return true
		}
	}
	return !anyReported
}

// resolveSkipPartBooks returns the author's effective metadata profile's
// SkipPartBooks setting. Defaults to false (matches the setting's prior
// no-op behavior) on any lookup failure, so an unresolvable profile never
// turns into unexpected catalogue loss.
func (h *AuthorHandler) resolveSkipPartBooks(ctx context.Context, author *models.Author) bool {
	id := models.DefaultMetadataProfileID
	if author.MetadataProfileID != nil {
		id = *author.MetadataProfileID
	}
	p, err := h.profiles.GetByID(ctx, id)
	if err != nil || p == nil {
		return false
	}
	return p.SkipPartBooks
}
