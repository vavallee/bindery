package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"

	"github.com/go-chi/chi/v5"

	"github.com/vavallee/bindery/internal/auth"
	"github.com/vavallee/bindery/internal/bookhydrate"
	"github.com/vavallee/bindery/internal/db"
	"github.com/vavallee/bindery/internal/indexer"
	"github.com/vavallee/bindery/internal/metadata"
	"github.com/vavallee/bindery/internal/models"
	"github.com/vavallee/bindery/internal/textutil"
)

var (
	errNoMetadataAggregator   = errors.New("metadata aggregator not configured")
	errNoMetadataMatch        = errors.New("no exact-name match in metadata provider")
	errAmbiguousMetadataMatch = errors.New("multiple exact-name matches in metadata provider")
)

const authorAutoSearchConcurrency = 4

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
	userID := auth.UserIDFromContext(ctx)
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
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
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
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
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

	// Attach books
	books, err := h.books.ListByAuthor(r.Context(), id)
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
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
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
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
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
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
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
				writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
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
			h.fetchAuthorBooksAsync(canonical, req.SearchOnAdd, mediaType)
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
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
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
	h.fetchAuthorBooksAsync(author, req.SearchOnAdd, mediaType)

	writeJSON(w, http.StatusCreated, author)
}

func cleanAuthorDescription(author *models.Author) {
	if author != nil {
		author.Description = textutil.CleanDescription(author.Description)
	}
}

func (h *AuthorHandler) fetchAuthorBooksAsync(author *models.Author, autoSearch bool, mediaType string) {
	if author == nil {
		return
	}
	snapshot := *author
	go h.FetchAuthorBooks(&snapshot, autoSearch, mediaType)
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
	if h.settings == nil {
		return models.DefaultAuthorMonitorMode
	}
	s, _ := h.settings.Get(ctx, SettingAuthorDefaultMonitorMode)
	if s == nil || s.Value == "" || !models.IsAuthorMonitorModeValid(s.Value) {
		return models.DefaultAuthorMonitorMode
	}
	return s.Value
}

func (h *AuthorHandler) resolveDefaultAuthorMonitorLatestCount(ctx context.Context) int {
	if h.settings == nil {
		return models.DefaultAuthorMonitorLatestCount
	}
	s, _ := h.settings.Get(ctx, SettingAuthorDefaultMonitorLatestCount)
	if s == nil || s.Value == "" {
		return models.DefaultAuthorMonitorLatestCount
	}
	n, err := strconv.Atoi(s.Value)
	if err != nil || n <= 0 {
		return models.DefaultAuthorMonitorLatestCount
	}
	return n
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
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
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
				writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
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
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
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
		if err := h.applyMonitorModeToExistingBooks(r.Context(), author); err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
			return
		}
	}
	writeJSON(w, http.StatusOK, author)
}

func (h *AuthorHandler) applyMonitorModeToExistingBooks(ctx context.Context, author *models.Author) error {
	books, err := h.books.ListByAuthorIncludingExcluded(ctx, author.ID)
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
		ids, err := h.authors.ListMonitoredSeriesIDs(ctx, author.ID)
		if err != nil {
			return fmt.Errorf("list monitored series ids: %w", err)
		}
		monitoredSet = make(map[int64]struct{}, len(ids))
		for _, id := range ids {
			monitoredSet[id] = struct{}{}
		}
		if h.series != nil {
			bookSeries, err = h.series.ListBookSeriesByAuthor(ctx, author.ID)
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
		if err := h.books.Update(ctx, &books[i]); err != nil {
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
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
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
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	} else if ambiguous {
		writeJSON(w, http.StatusConflict, map[string]string{"error": "author name resolves ambiguously — merge manually"})
		return
	} else if canonical != nil && canonical.ID != author.ID {
		h.writeCanonicalAuthorConflict(w, canonical, "author name already resolves to an existing author — confirm merge")
		return
	}
	if existing, err := h.authors.GetByAnyForeignIDForUser(r.Context(), upstream.ForeignID, userID); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
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
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
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
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
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
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
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
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
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
		books, err := h.books.ListByAuthor(r.Context(), id)
		if err != nil {
			slog.Warn("delete author: failed to list books for file cleanup", "author_id", id, "error", err)
		}
		for _, b := range books {
			if b.FilePath != "" {
				pathsToRemove = append(pathsToRemove, b.FilePath)
			}
		}
	}

	if err := h.authors.Delete(r.Context(), id); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}

	for _, p := range pathsToRemove {
		if _, err := safeRemoveBookPath(r.Context(), h.roots, p, "", "author_id", id); err != nil {
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

	// Manual refresh always repopulates the catalogue but never auto-grabs —
	// the user triggered it to refresh metadata, not to queue downloads.
	// Newly-discovered books inherit the global default media type; rows
	// that already exist keep whatever value they were created with.
	h.fetchAuthorBooksAsync(author, false, h.resolveDefaultMediaType(r.Context()))
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
func (h *AuthorHandler) FetchAuthorBooks(author *models.Author, autoSearch bool, mediaType string) {
	ctx := h.bgCtx()
	slog.Info("fetching books for author", "author", author.Name, "foreignId", author.ForeignID)

	// Calibre-imported authors carry a synthetic "calibre:author:N" foreign ID
	// that has no counterpart in OL/Hardcover — they come in with no image,
	// description, or real catalogue. Re-link them to the upstream metadata
	// provider by name so the first Refresh Metadata click pulls real data.
	// If the re-link fails (name not found, network error) we fall through and
	// keep the synthetic ID, matching the prior skip-silently behaviour.
	wasCalibre := strings.HasPrefix(author.ForeignID, "calibre:")
	if wasCalibre {
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
	if !wasCalibre {
		h.refreshAuthorProfile(ctx, author)
	}

	// Use the dedicated author works endpoint for accurate results, with
	// author-scoped supplemental providers when available.
	books, err := h.meta.GetAuthorWorksForAuthor(ctx, *author)
	if err != nil {
		slog.Error("failed to fetch books", "author", author.Name, "error", err)
		return
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
	// network round-trip to api.audible.com.
	if mediaType == models.MediaTypeAudiobook || mediaType == models.MediaTypeBoth {
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

	// Track titles we've already added (case-insensitive) to avoid OL duplicates.
	// The value is a pointer to the existing book so we can enrich calibre-imported
	// stubs with the OL foreign ID and language when they title-match an OL record.
	existingBooks, _ := h.books.ListByAuthor(ctx, author.ID)
	seenTitles := make(map[string]*models.Book)
	for i := range existingBooks {
		seenTitles[indexer.NormalizeTitleForDedup(existingBooks[i].Title)] = &existingBooks[i]
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

	var added, skippedLang, skippedJunk int
	for _, b := range books {
		b.AuthorID = author.ID
		// Apply the caller-provided default media type when the provider
		// didn't set one. Never overwrite an explicit value — the audiobook
		// enrichment flow relies on provider-supplied audiobook rows coming
		// through with MediaType=audiobook already.
		if mediaType != "" && b.MediaType == "" {
			b.MediaType = mediaType
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
		if !models.IsLanguageAllowed(b.Language, allowedLangs, unknownFail) {
			skippedLang++
			slog.Debug("skipping non-allowed-language book", "title", b.Title, "language", b.Language, "allowed", allowedLangs, "edition_sampled", langSampled)
			continue
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

		// Update ratings + genres on existing books, then skip further
		// processing (we don't want to overwrite user state like status).
		existing, _ := h.books.GetByForeignID(ctx, b.ForeignID)
		if existing != nil {
			changed := false
			if b.RatingsCount > 0 && (existing.RatingsCount == 0 || b.RatingsCount > existing.RatingsCount) {
				existing.RatingsCount = b.RatingsCount
				existing.AverageRating = b.AverageRating
				changed = true
			}
			// Backfill Hardcover genres onto rows imported before genre
			// sourcing existed. Gated to Hardcover provenance (a HardcoverForeignID
			// means HC matched this work this fetch) so a refresh while HC is
			// unavailable never downgrades clean genres back to OL subjects.
			// NOTE: once a per-book genre override lands (follow-up), this must
			// be guarded to not clobber a user-edited value.
			if b.HardcoverForeignID != "" && len(b.Genres) > 0 && !slices.Equal(existing.Genres, b.Genres) {
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
		dedupKey := indexer.NormalizeTitleForDedup(b.Title)
		if existing := seenTitles[dedupKey]; existing != nil {
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
		seenTitles[dedupKey] = &b

		if err := h.books.Create(ctx, &b); err != nil {
			// A UNIQUE constraint on foreign_id means the book was already
			// created by a concurrent or earlier sync — treat as a benign skip.
			if strings.Contains(err.Error(), "UNIQUE constraint failed") {
				continue
			}
			slog.Warn("failed to create book", "title", b.Title, "error", err)
			continue
		}
		h.hydrateHardcoverEditions(ctx, &b)
		added++

		if fileFound := handleNewWantedBook(ctx, h.books, h.series, h.finder, b, author.Name); fileFound {
			continue // don't auto-search for a book we already have
		}

		// Auto-search the freshly-added wanted book only when the per-add
		// flag AND the global auto-grab kill-switch both say yes.
		if autoSearchEnabled && b.Monitored {
			searchQueue = append(searchQueue, b)
		}
	}
	runBookSearches(ctx, h.searcher, searchQueue, authorAutoSearchConcurrency)
	slog.Info("author books synced", "author", author.Name, "added", added, "skipped_language", skippedLang, "skipped_junk", skippedJunk, "total", len(books))
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

func runBookSearches(ctx context.Context, searcher BookSearcher, books []models.Book, concurrency int) {
	if searcher == nil || len(books) == 0 {
		return
	}
	if concurrency <= 0 {
		concurrency = 1
	}

	sem := make(chan struct{}, concurrency)
	var wg sync.WaitGroup
	for _, book := range books {
		select {
		case sem <- struct{}{}:
		case <-ctx.Done():
			wg.Wait()
			return
		}
		book := book
		wg.Add(1)
		go func() {
			defer wg.Done()
			defer func() { <-sem }()
			searcher.SearchAndGrabBook(ctx, book)
		}()
	}
	wg.Wait()
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

func authorSearchQueries(name string) []string {
	name = strings.TrimSpace(name)
	if name == "" {
		return nil
	}
	queries := []string{name}
	if compact := compactInitialsAuthorQuery(name); compact != "" {
		queries = append(queries, compact)
	}
	if norm := textutil.NormalizeAuthorName(name); norm != "" {
		queries = append(queries, norm)
		if surname := initialedSurnameFallback(norm); surname != "" {
			queries = append(queries, surname)
		}
	}
	return dedupeAuthorQueries(queries)
}

func compactInitialsAuthorQuery(name string) string {
	fields := strings.Fields(name)
	if len(fields) < 3 {
		return ""
	}
	initials := make([]string, 0, len(fields)-1)
	idx := 0
	for idx < len(fields)-1 {
		initial, ok := authorInitial(fields[idx])
		if !ok {
			break
		}
		initials = append(initials, strings.ToUpper(initial)+".")
		idx++
	}
	if len(initials) < 2 || idx >= len(fields) {
		return ""
	}
	return strings.Join(initials, "") + " " + strings.Join(fields[idx:], " ")
}

func authorInitial(token string) (string, bool) {
	var letters []rune
	for _, r := range token {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			letters = append(letters, unicode.ToLower(r))
		}
	}
	if len(letters) != 1 {
		return "", false
	}
	return string(letters[0]), true
}

func initialedSurnameFallback(normalized string) string {
	fields := strings.Fields(normalized)
	if len(fields) < 2 {
		return ""
	}
	for _, field := range fields[:len(fields)-1] {
		if len([]rune(field)) != 1 {
			return ""
		}
	}
	return fields[len(fields)-1]
}

func dedupeAuthorQueries(values []string) []string {
	out := make([]string, 0, len(values))
	seen := make(map[string]struct{}, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		key := strings.ToLower(value)
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		out = append(out, value)
	}
	return out
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
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid request body"})
		return
	}
	if req.ForeignBookID == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "foreignBookId required"})
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

	// resolvedByName is set when the author was resolved from the result's NAME
	// (not an ISBN/provider ID) — the case for Google Books results, which carry
	// an author name but no author ID and no ISBN editions. It forces a direct
	// insert of the chosen edition (the author's catalogue fetch won't contain a
	// cross-provider book) and suppresses the speculative catalogue fetch.
	resolvedByName := false

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
			resolvedByName = req.ForeignAuthorID != ""
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
	authorMatchedByAlternateID := false
	if author == nil {
		author, _ = h.authors.GetByAnyForeignIDForUser(ctx, req.ForeignAuthorID, userID)
		authorMatchedByAlternateID = author != nil
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
			if err := h.authors.CreateForUser(ctx, fetched, userID); err != nil {
				if !strings.Contains(err.Error(), "UNIQUE constraint failed") && !errors.Is(err, db.ErrAuthorIdentifierConflict) {
					writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
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
				// A name-resolved add (e.g. a Google Books pick) means the user
				// chose ONE book; don't speculatively fetch this newly-adopted
				// author's whole catalogue and flood Wanted. The synchronous
				// direct-insert below still creates the picked book and keeps the
				// author non-orphan.
				if !resolvedByName {
					h.fetchAuthorBooksAsync(author, false, h.resolveDefaultMediaType(ctx))
				}
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
	directInsertNeeded := authorWasJustCreated || authorMatchedByAlternateID || strings.HasPrefix(req.ForeignBookID, "dnb:") || resolvedByName
	if directInsertNeeded {
		if existing, _ := h.books.GetByForeignID(ctx, req.ForeignBookID); existing == nil {
			primary, err := h.meta.GetBook(ctx, req.ForeignBookID)
			if err != nil {
				slog.Warn("AddBook: direct fetch failed",
					"foreignBookId", req.ForeignBookID, "error", err)
			} else if primary != nil {
				primary.AuthorID = author.ID
				primary.Monitored = author.Monitored
				if primary.Status == "" {
					primary.Status = models.BookStatusWanted
				}
				// Some providers (notably Google Books) don't set a media type;
				// fall back to the global default so the row isn't created with an
				// empty format (which would mis-route its indexer search).
				if primary.MediaType == "" {
					primary.MediaType = h.resolveDefaultMediaType(ctx)
				}
				if err := h.books.Create(ctx, primary); err != nil {
					if !strings.Contains(err.Error(), "UNIQUE constraint failed") {
						slog.Warn("AddBook: direct insert failed",
							"foreignBookId", req.ForeignBookID, "error", err)
					}
				} else {
					h.hydrateHardcoverEditions(ctx, primary)
				}
			}
		}
	}

	// 2. Poll until the book appears (FetchAuthorBooks runs asynchronously).
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

	// 3. Mark the book monitored (wanted).
	book.Monitored = true
	if err := h.books.Update(ctx, book); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}

	// 4. Optionally trigger an indexer search. Use the process-lifecycle
	// context so the search goroutine is cancelled on shutdown rather than
	// running against context.Background(). See #846.
	if req.SearchOnAdd && h.searcher != nil {
		go h.searcher.SearchAndGrabBook(h.bgCtx(), *book) // #nosec G118 -- intentional: search must outlive the request
	}

	writeJSON(w, http.StatusCreated, book)
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
