package api

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/vavallee/bindery/internal/auth"
	"github.com/vavallee/bindery/internal/db"
	"github.com/vavallee/bindery/internal/models"
	"github.com/vavallee/bindery/internal/textutil"
)

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
	author, ok := h.loadOwnedAuthor(w, r)
	if !ok {
		return
	}
	id := author.ID

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
	// And whether a sync is running now, which the page polls after a manual
	// Refresh so it can show the result instead of the state before it (#2601).
	author.SyncInProgress = h.runningSyncs.running(id)

	cleanAuthorDescription(author)
	writeAuthor(w, http.StatusOK, author)
}

// ListSeries returns the series belonging to the author — i.e. the series
// that any of the author's books are linked to. Backs the monitor-by-series
// picker (#810) so the edit modal can render a checkbox list scoped to this
// author rather than the global /series collection.
func (h *AuthorHandler) ListSeries(w http.ResponseWriter, r *http.Request) {
	// The ownership check has to run before we list series belonging to the
	// author, hence the load here rather than a bare parseID.
	author, ok := h.loadOwnedAuthor(w, r)
	if !ok {
		return
	}
	id := author.ID
	if h.series == nil {
		writeJSON(w, http.StatusOK, []models.Series{})
		return
	}
	series, err := h.series.ListByAuthorAndUser(r.Context(), id, auth.ListScopeUserID(r.Context()))
	if err != nil {
		writeServerError(w, r, err)
		return
	}
	proxySeriesImages(series)
	writeJSON(w, http.StatusOK, series)
}

func (h *AuthorHandler) Create(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ForeignID             string  `json:"foreignAuthorId"`
		Name                  string  `json:"authorName"`
		MetadataProfileID     *int64  `json:"metadataProfileId"`
		RootFolderID          *int64  `json:"rootFolderId"`
		AudiobookRootFolderID *int64  `json:"audiobookRootFolderId"`
		Monitored             bool    `json:"monitored"`
		MonitorMode           *string `json:"monitorMode"`
		MonitorLatestCount    *int    `json:"monitorLatestCount"`
		// Settable at add time as of the Add Author redesign: before this it
		// could only be changed on the author afterwards, so "catalogue this
		// author once and never let a refresh grow it" was not expressible
		// when adding. Same validation as Update.
		MonitorNewItems *string `json:"monitorNewItems"`
		SearchOnAdd     bool    `json:"searchOnAdd"`
		MediaType       string  `json:"mediaType"`
		// Left out means the install wide default quality profile; null
		// means none, which accepts every format (#3831).
		QualityProfileID nullableInt64 `json:"qualityProfileId"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid request body"})
		return
	}
	res, err := h.createAuthorCore(r.Context(), createAuthorParams{
		ForeignID:              req.ForeignID,
		Name:                   req.Name,
		QualityProfileID:       req.QualityProfileID.Value,
		QualityProfileExplicit: req.QualityProfileID.Set,
		MetadataProfileID:      req.MetadataProfileID,
		RootFolderID:           req.RootFolderID,
		AudiobookRootFolderID:  req.AudiobookRootFolderID,
		Monitored:              req.Monitored,
		MonitorMode:            req.MonitorMode,
		MonitorLatestCount:     req.MonitorLatestCount,
		MonitorNewItems:        req.MonitorNewItems,
		SearchOnAdd:            req.SearchOnAdd,
		MediaType:              req.MediaType,
	})
	if err != nil {
		h.writeCreateAuthorError(w, r, err)
		return
	}
	if !res.Created {
		writeAuthor(w, http.StatusOK, res.Author)
		return
	}
	writeAuthor(w, http.StatusCreated, res.Author)
}

func cleanAuthorDescription(author *models.Author) {
	if author != nil {
		author.Description = textutil.CleanDescription(author.Description)
	}
}

// stampProviderMismatch marks an add-flow response when the linked record's
// catalogue provider (derived from the foreign-ID prefix, the same way
// providerForForeignID routes sync fetches) differs from the configured
// primary (#2237). With metadata.primary_provider = hardcover, a Hardcover
// search miss makes the add flow pick a record from another provider and the
// author syncs from that provider forever; making Hardcover's search
// exhaustive is provider behaviour we cannot fix, but the fallback happening
// silently is ours to fix. Call before fetchAuthorBooksAsync: the spawn
// snapshots the author, so mutating it afterwards would race the read.
func (h *AuthorHandler) stampProviderMismatch(author *models.Author) {
	if author == nil || h.meta == nil {
		return
	}
	primary := h.meta.PrimaryProviderName()
	if primary == "" {
		return
	}
	if linked := models.AuthorProviderFromForeignID(author.ForeignID); linked != primary {
		author.ProviderMismatch = &models.AuthorProviderMismatch{PrimaryProvider: primary, LinkedProvider: linked}
	}
}

func (h *AuthorHandler) fetchAuthorForCreate(ctx context.Context, foreignID, fallbackName string) (*models.Author, error) {
	if h.meta == nil {
		return &models.Author{
			ForeignID:        foreignID,
			Name:             fallbackName,
			SortName:         sortName(fallbackName),
			MetadataProvider: models.AuthorProviderFromForeignID(foreignID),
		}, nil
	}
	author, err := h.meta.GetAuthor(ctx, foreignID)
	if err != nil {
		slog.Warn("metadata lookup failed, using provided name", "foreignID", foreignID, "error", err)
		return &models.Author{
			ForeignID:        foreignID,
			Name:             fallbackName,
			SortName:         sortName(fallbackName),
			MetadataProvider: models.AuthorProviderFromForeignID(foreignID),
		}, nil
	}
	if author == nil {
		return &models.Author{
			ForeignID:        foreignID,
			Name:             fallbackName,
			SortName:         sortName(fallbackName),
			MetadataProvider: models.AuthorProviderFromForeignID(foreignID),
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

func (h *AuthorHandler) Update(w http.ResponseWriter, r *http.Request) {
	author, ok := h.loadWritableAuthor(w, r)
	if !ok {
		return
	}

	var req struct {
		Monitored          *bool   `json:"monitored"`
		MonitorMode        *string `json:"monitorMode"`
		MonitorLatestCount *int    `json:"monitorLatestCount"`
		MonitorNewItems    *string `json:"monitorNewItems"`
		// Left out leaves the profile alone; an explicit null clears it, so
		// the Edit Author dialog's "None" can be saved (#3831).
		QualityProfileID      nullableInt64 `json:"qualityProfileId"`
		MetadataProfileID     *int64        `json:"metadataProfileId"`
		RootFolderID          *int64        `json:"rootFolderId"`
		AudiobookRootFolderID *int64        `json:"audiobookRootFolderId"`
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
		// Description sets the author's bio by hand. It is kept as the
		// user's own, so no refresh replaces it; an empty string clears it
		// and lets the provider or an enricher fill it again (#3287).
		Description *string `json:"description"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid request body"})
		return
	}
	if req.Description != nil {
		desc := strings.TrimSpace(*req.Description)
		if len(desc) > maxAuthorDescriptionBytes {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "description is longer than 20000 bytes"})
			return
		}
		if desc != author.Description {
			author.Description = desc
			author.DescriptionSource = nil
			if desc != "" {
				author.DescriptionSource = &models.ProfileSource{Provider: models.ProfileSourceUser}
			}
		}
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
	if req.QualityProfileID.Set {
		author.QualityProfileID = req.QualityProfileID.Value
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

	if !h.saveAuthorIfUnchanged(w, r, author) {
		return
	}

	// Persist the per-author series pin set before applying so the apply pass
	// reads the freshly-written rows. Validate every ID belongs to this
	// author's series — accepting arbitrary series IDs would let one author's
	// monitor selection silently reference an unrelated catalog row.
	if req.MonitoredSeriesIDs != nil {
		if len(*req.MonitoredSeriesIDs) > 0 {
			ownSeries, err := h.series.ListByAuthorAndUser(r.Context(), author.ID, auth.ListScopeUserID(r.Context()))
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
	writeAuthor(w, http.StatusOK, author)
}

func applyMonitorModeToExistingBooks(ctx context.Context, booksRepo *db.BookRepo, authorsRepo *db.AuthorRepo, seriesRepo *db.SeriesRepo, author *models.Author) error {
	books, err := booksRepo.ListByAuthorIncludingExcluded(ctx, author.ID)
	if err != nil {
		return fmt.Errorf("list author books: %w", err)
	}
	// The caller has write access to the author, not to every book under it: a
	// shared author can hold other accounts' books and unowned ones. Rewrite
	// only the books this caller may write; admins and tenancy off keep all.
	books = writableBooks(ctx, books)
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
		// Series mode recomputes next from membership alone, so it has to
		// re-apply the one rule that outranks every mode: an unmonitored
		// author monitors nothing (#2742). shouldMonitorBookForAuthor says so
		// for the other four modes already, and without this a cascade over an
		// unmonitored author in series mode monitored their books back on.
		if author.MonitorMode == models.AuthorMonitorModeSeries {
			next = author.Monitored && bookInMonitoredSeries(books[i].ID, bookSeries, monitoredSet)
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

func (h *AuthorHandler) Delete(w http.ResponseWriter, r *http.Request) {
	// The load has to happen before any destructive work, so the ownership
	// check runs first.
	author, ok := h.loadWritableAuthor(w, r)
	if !ok {
		return
	}
	id := author.ID
	// The FK cascade below takes every book under the author, so a user who
	// is not an admin may only delete an author whose books are all theirs.
	if held, err := authorHoldsOthersBooks(r.Context(), h.authors, id); err != nil {
		writeServerError(w, r, err)
		return
	} else if held {
		writeJSON(w, http.StatusConflict, map[string]string{"error": errAuthorHoldsOthersBooks.Error()})
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
	// The ebook paths the author's own book_files rows name. The cascade drops
	// those rows before the sweep, so the guard's own-ebook exception (#3561)
	// would otherwise find nothing and keep an ebook that sits inside another
	// author's book folder.
	ownEbooks := map[string]struct{}{}
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
					if f.Format == models.MediaTypeEbook {
						ownEbooks[f.Path] = struct{}{}
					}
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
	owner := cascadedEbookOwner{bookFileOwner: h.books, ebooks: ownEbooks}
	for _, p := range pathsToRemove {
		if _, err := safeRemoveBookPath(r.Context(), h.roots, owner, 0, p, "", "author_id", id); err != nil {
			slog.Warn("delete author: failed to remove file", "author_id", id, "path", p, "error", err)
		}
	}

	w.WriteHeader(http.StatusNoContent)
}

func (h *AuthorHandler) Refresh(w http.ResponseWriter, r *http.Request) {
	author, ok := h.loadWritableAuthor(w, r)
	if !ok {
		return
	}

	// Manual refresh re-reads the author's metadata but never auto-grabs — the
	// user triggered it to refresh metadata, not to queue downloads. Whether
	// it may also CREATE rows for works the library doesn't have is the
	// author's monitoring policy's call (see authorAcceptsDiscoveredBooks);
	// books that do get created inherit the global default media type, and
	// rows that already exist keep whatever value they were created with.
	//
	// It is also the one refresh that asks the provider for current data
	// rather than a cached copy up to 24 hours old: the user clicked it because
	// something changed upstream (#2601).
	//
	// One sync per author at a time: five quick clicks used to start five
	// concurrent full syncs, each one past the cache. The page waits on the
	// running sync instead, and a second click gets 409 like the other
	// "already running" endpoints (Refresh all, imports).
	if !h.runningSyncs.tryStart(author.ID) {
		writeJSON(w, http.StatusConflict, map[string]string{"error": h.refreshConflictMessage(author.ID)})
		return
	}
	h.fetchAuthorBooksAsync(author, catalogueSyncOptions{mediaType: h.resolveDefaultMediaType(r.Context()), discovery: true, refreshFromProvider: true, enrichProfile: true, syncClaimed: true})
	writeJSON(w, http.StatusAccepted, map[string]string{"message": "refresh started"})
}
