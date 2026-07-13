package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/vavallee/bindery/internal/auth"
	"github.com/vavallee/bindery/internal/concurrency"
	"github.com/vavallee/bindery/internal/db"
	"github.com/vavallee/bindery/internal/models"
)

// errBulkNotOwned is the sentinel returned by the per-ID bulk helpers when the
// caller does not own the targeted resource under BINDERY_ENFORCE_TENANCY. It
// is surfaced in the per-ID result map with the same opaque "not found" message
// the single-resource handlers use (404 on mismatch), so a non-owner cannot
// distinguish "exists but yours" from "does not exist" and cannot probe for the
// existence of another user's authors/books (#947). When tenancy is off
// auth.CheckOwnership is a no-op, so this is never returned and behaviour is
// unchanged.
var (
	errBulkAuthorNotOwned = fmt.Errorf("author not found")
	errBulkBookNotOwned   = fmt.Errorf("book not found")
)

// bulkSearchConcurrency caps how many indexer searches a single bulk
// action can fan out at once. Sized to a small fixed number rather than
// the configured indexer count so a 500-book author "search all" can't
// drown every indexer in one click; the scheduled wanted-search loop
// uses the same bound. Tune in code if real-world indexer pools change
// shape — a setting for one number would be more noise than signal.
const bulkSearchConcurrency = 8

// searchPaceInterval is the minimum gap between successive indexer-search
// launches in every user- or catalogue-triggered fan-out in this package
// (bulk "search all", per-author auto-search, series fill). The concurrency
// caps bound parallelism; this bounds rate, so a large author or series can't
// burst a rate-limit-free Prowlarr into dropping requests (#1515). Fixed in
// code, like the concurrency caps — one tunable number is more noise than
// signal. The scheduled wanted loop paces itself with the same value
// (scheduler.searchPaceInterval, kept in sync).
//
// A var, not a const, only so tests that assert concurrency behaviour can set
// it to 0 and observe an unpaced fan-out; production never mutates it.
var searchPaceInterval = 3 * time.Second

// BulkHandler handles multi-ID mutation endpoints for authors, books, and
// wanted books. All three endpoints use a per-ID result map with 200 status
// so callers can distinguish partial failures without a second request.
// Rationale for per-ID rather than all-or-nothing: bulk operations routinely
// include stale IDs (page loaded, item deleted, user re-selects); aborting
// the whole batch on one bad ID is more disruptive than reporting it inline.
type BulkHandler struct {
	authors   *db.AuthorRepo
	books     *db.BookRepo
	series    *db.SeriesRepo
	blocklist *db.BlocklistRepo
	searcher  BookSearcher

	// refreshAuthor repopulates a single author's catalogue from the metadata
	// provider (and resolves the default media type for newly-discovered
	// books). Injected via WithRefreshFunc so the bulk "refresh" action reuses
	// exactly the same fetch the per-author Refresh handler runs — it fetches
	// metadata but never auto-grabs. Nil when not wired (e.g. in tests that
	// don't exercise refresh), in which case "refresh" is rejected.
	refreshAuthor func(author *models.Author)

	// lifetimeCtx is the process-lifecycle context; cancelled on server
	// shutdown so a bulk-action POST that fans out indexer searches does
	// not leak goroutines (or worse, grab a torrent mid-shutdown) past
	// Server.Shutdown. Falls back to context.Background() when not set;
	// see #846 and the mirroring pattern in recommendations.go.
	lifetimeCtx context.Context
}

func NewBulkHandler(authors *db.AuthorRepo, books *db.BookRepo, blocklist *db.BlocklistRepo, searcher BookSearcher) *BulkHandler {
	return &BulkHandler{authors: authors, books: books, blocklist: blocklist, searcher: searcher}
}

// WithSeriesRepo attaches series lookups used by monitor-mode application.
func (h *BulkHandler) WithSeriesRepo(series *db.SeriesRepo) *BulkHandler {
	if series != nil {
		h.series = series
	}
	return h
}

// WithRefreshFunc attaches the per-author catalogue-refresh callback used by
// the bulk "refresh" action. The callback must fetch metadata only (never
// auto-grab) — wire it to AuthorHandler.FetchAuthorBooks(author, false,
// defaultMediaType), matching the single-author Refresh endpoint. A nil fn is
// tolerated and ignored; the "refresh" action then reports an error per ID.
func (h *BulkHandler) WithRefreshFunc(fn func(author *models.Author)) *BulkHandler {
	if fn != nil {
		h.refreshAuthor = fn
	}
	return h
}

// WithLifetimeCtx attaches the process-lifecycle context so the bulk-search
// fan-out goroutine is cancelled on shutdown rather than running against
// context.Background(). A nil ctx is tolerated and ignored (the handler then
// falls back to context.Background() at fan-out time). See #846.
func (h *BulkHandler) WithLifetimeCtx(ctx context.Context) *BulkHandler {
	if ctx != nil {
		h.lifetimeCtx = ctx
	}
	return h
}

// bgCtx returns the lifetime context if set, otherwise context.Background().
// Centralised so every spawn site uses the same fallback rule and tests that
// construct a handler without WithLifetimeCtx behave as they did before.
func (h *BulkHandler) bgCtx() context.Context {
	if h.lifetimeCtx != nil {
		return h.lifetimeCtx
	}
	return context.Background()
}

// bulkItemResult is the per-ID result entry in a bulk response.
type bulkItemResult struct {
	OK    bool   `json:"ok"`
	Error string `json:"error,omitempty"`
}

// bulkResponse is the envelope returned by all three bulk endpoints.
// Keys are stringified IDs; every requested ID has an entry.
type bulkResponse struct {
	Results map[string]bulkItemResult `json:"results"`
}

// AuthorsBulk handles POST /api/v1/author/bulk.
//
// Supported actions: "monitor", "unmonitor", "delete", "search", "refresh", "set_media_type", "set_monitor_mode".
// "search" fires an async indexer search for every wanted book belonging
// to each requested author and always returns ok:true immediately (the search
// outcome is visible in History).
// "refresh" repopulates each author's catalogue from the metadata provider
// (the same fetch the per-author Refresh endpoint runs) — it fetches metadata
// only and never auto-grabs. Like "search" it dispatches under a bounded pool
// after the response and returns ok:true immediately. Used to recover authors
// imported with empty catalogues (e.g. plain-name CSV rows) without clicking
// per-author Refresh one at a time.
// "set_media_type" requires a "mediaType" field ("ebook"|"audiobook"|"both")
// and applies it to every book belonging to each author — the companion
// mass-migration action for the global default.media_type setting.
// "set_monitor_mode" requires a "monitorMode" field ("all"|"future"|"latest"|"none")
// and optionally applies that mode to existing books when
// "applyMonitorModeToExisting" is true. Series mode is intentionally excluded:
// it needs each author's own selected series list.
func (h *BulkHandler) AuthorsBulk(w http.ResponseWriter, r *http.Request) {
	var req struct {
		IDs                        []int64 `json:"ids"`
		Action                     string  `json:"action"`
		MediaType                  string  `json:"mediaType"`
		MonitorMode                string  `json:"monitorMode"`
		MonitorLatestCount         *int    `json:"monitorLatestCount"`
		ApplyMonitorModeToExisting bool    `json:"applyMonitorModeToExisting"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid request body"})
		return
	}
	if len(req.IDs) == 0 {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "ids required"})
		return
	}

	switch req.Action {
	case "monitor", "unmonitor", "delete", "search", "refresh", "set_media_type", "set_monitor_mode":
	default:
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "unknown action: " + req.Action})
		return
	}
	if req.Action == "refresh" && h.refreshAuthor == nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "refresh action not available"})
		return
	}
	if req.Action == "set_media_type" {
		switch req.MediaType {
		case models.MediaTypeEbook, models.MediaTypeAudiobook, models.MediaTypeBoth:
		default:
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "mediaType must be 'ebook', 'audiobook', or 'both'"})
			return
		}
	}
	if req.Action == "set_monitor_mode" {
		req.MonitorMode = strings.TrimSpace(req.MonitorMode)
		switch req.MonitorMode {
		case models.AuthorMonitorModeAll, models.AuthorMonitorModeFuture, models.AuthorMonitorModeLatest, models.AuthorMonitorModeNone:
		case models.AuthorMonitorModeSeries:
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "monitorMode 'series' cannot be set in bulk"})
			return
		default:
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "monitorMode must be one of: all, future, latest, none"})
			return
		}
		if req.MonitorMode == models.AuthorMonitorModeLatest && req.MonitorLatestCount == nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "monitorLatestCount must be a positive integer"})
			return
		}
		if req.MonitorLatestCount != nil && *req.MonitorLatestCount <= 0 {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "monitorLatestCount must be a positive integer"})
			return
		}
	}

	resp := bulkResponse{Results: make(map[string]bulkItemResult, len(req.IDs))}

	// For the "search" action we collect wanted+monitored books across every
	// requested author and dispatch the indexer fan-out under a single bounded
	// pool after the handler returns. This caps a 500-book author (or 50
	// authors with 50 books each) at bulkSearchConcurrency in-flight searches
	// rather than the prior unbounded `go ... per book`.
	var searchTargets []models.Book

	// For the "refresh" action we load each author while the request context
	// is alive, then dispatch the metadata fetch under the same bounded pool
	// as "search" after the handler returns.
	var refreshTargets []*models.Author

	for _, id := range req.IDs {
		key := fmt.Sprintf("%d", id)
		switch req.Action {
		case "monitor":
			if err := h.setAuthorMonitored(r.Context(), id, true); err != nil {
				resp.Results[key] = bulkItemResult{Error: err.Error()}
				continue
			}
		case "unmonitor":
			if err := h.setAuthorMonitored(r.Context(), id, false); err != nil {
				resp.Results[key] = bulkItemResult{Error: err.Error()}
				continue
			}
		case "delete":
			// Ownership-gate the delete: load the author first so we can verify
			// the caller owns it before mutating (the repo deletes purely by id).
			author, err := h.authors.GetByID(r.Context(), id)
			if err != nil {
				resp.Results[key] = bulkItemResult{Error: err.Error()}
				continue
			}
			if author == nil || !auth.CheckOwnership(r.Context(), author.OwnerUserID) {
				resp.Results[key] = bulkItemResult{Error: errBulkAuthorNotOwned.Error()}
				continue
			}
			if err := h.authors.Delete(r.Context(), id); err != nil {
				resp.Results[key] = bulkItemResult{Error: err.Error()}
				continue
			}
		case "search":
			// Verify ownership before fanning out searches for another user's
			// catalogue. Load the author to read OwnerUserID, then collect its
			// wanted books while the request context is still alive.
			author, err := h.authors.GetByID(r.Context(), id)
			if err != nil {
				resp.Results[key] = bulkItemResult{Error: err.Error()}
				continue
			}
			if author == nil || !auth.CheckOwnership(r.Context(), author.OwnerUserID) {
				resp.Results[key] = bulkItemResult{Error: errBulkAuthorNotOwned.Error()}
				continue
			}
			books, err := h.books.ListByAuthor(r.Context(), id)
			if err != nil {
				resp.Results[key] = bulkItemResult{Error: err.Error()}
				continue
			}
			if h.searcher != nil {
				for _, b := range books {
					if b.Status == models.BookStatusWanted && b.Monitored {
						searchTargets = append(searchTargets, b)
					}
				}
			}
		case "refresh":
			// Load the author now (request context alive); collect for the
			// bounded post-response metadata fetch.
			author, err := h.authors.GetByID(r.Context(), id)
			if err != nil {
				resp.Results[key] = bulkItemResult{Error: err.Error()}
				continue
			}
			if author == nil || !auth.CheckOwnership(r.Context(), author.OwnerUserID) {
				resp.Results[key] = bulkItemResult{Error: errBulkAuthorNotOwned.Error()}
				continue
			}
			refreshTargets = append(refreshTargets, author)
		case "set_media_type":
			if err := h.setAuthorBooksMediaType(r.Context(), id, req.MediaType); err != nil {
				resp.Results[key] = bulkItemResult{Error: err.Error()}
				continue
			}
		case "set_monitor_mode":
			if err := h.setAuthorMonitorMode(r.Context(), id, req.MonitorMode, req.MonitorLatestCount, req.ApplyMonitorModeToExisting); err != nil {
				resp.Results[key] = bulkItemResult{Error: err.Error()}
				continue
			}
		}
		resp.Results[key] = bulkItemResult{OK: true}
	}

	if len(searchTargets) > 0 && h.searcher != nil {
		h.fanOutSearches(searchTargets)
	}
	if len(refreshTargets) > 0 && h.refreshAuthor != nil {
		h.fanOutRefreshes(refreshTargets)
	}

	writeJSON(w, http.StatusOK, resp)
}

// fanOutRefreshes dispatches per-author catalogue refreshes under the same
// bounded pool as fanOutSearches so a bulk refresh of many authors can't spawn
// one goroutine per author. Each call fetches metadata only (never auto-grabs)
// via the injected refreshAuthor callback. Runs on the process-lifecycle
// context so the HTTP response isn't blocked on provider round-trips and a
// shutdown cancels in-flight fetches. Fire-and-forget by design.
func (h *BulkHandler) fanOutRefreshes(authors []*models.Author) {
	if h.refreshAuthor == nil || len(authors) == 0 {
		return
	}
	bgCtx := h.bgCtx()
	go concurrency.RunBounded(bgCtx, authors, bulkSearchConcurrency, func(_ context.Context, a *models.Author) {
		h.refreshAuthor(a)
	})
}

// fanOutSearches dispatches per-book indexer searches under a bounded pool
// so a single bulk action can't spawn one goroutine per book. The pool
// runs on the process-lifecycle context (see WithLifetimeCtx, falling back
// to context.Background()), so the HTTP response is not blocked on
// indexer round-trips and a server shutdown cancels in-flight searches
// rather than letting them grab torrents mid-shutdown. The bulk endpoint
// is fire-and-forget for searches by design (results show up in History).
func (h *BulkHandler) fanOutSearches(books []models.Book) {
	if h.searcher == nil || len(books) == 0 {
		return
	}
	bgCtx := h.bgCtx()
	go concurrency.RunBoundedPaced(bgCtx, books, bulkSearchConcurrency, searchPaceInterval, func(ctx context.Context, b models.Book) {
		h.searcher.SearchAndGrabBook(ctx, b)
	})
}

// BooksBulk handles POST /api/v1/book/bulk.
//
// Supported actions: "monitor", "unmonitor", "delete", "search", "set_media_type", "exclude".
// For "set_media_type" the body must also include "mediaType": "ebook"|"audiobook".
// "exclude" sets the book's excluded flag to true so it disappears from author
// lists and is blocked from re-import on the next OL sync — mirrors the
// single-book PUT /book/:id/exclude path but skips the toggle semantics
// (bulk callers always want exclude=true; un-excluding remains a per-book
// affordance).
func (h *BulkHandler) BooksBulk(w http.ResponseWriter, r *http.Request) {
	var req struct {
		IDs       []int64 `json:"ids"`
		Action    string  `json:"action"`
		MediaType string  `json:"mediaType"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid request body"})
		return
	}
	if len(req.IDs) == 0 {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "ids required"})
		return
	}

	switch req.Action {
	case "monitor", "unmonitor", "delete", "search", "set_media_type", "exclude":
	default:
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "unknown action: " + req.Action})
		return
	}
	if req.Action == "set_media_type" && req.MediaType != models.MediaTypeEbook && req.MediaType != models.MediaTypeAudiobook {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "mediaType must be 'ebook' or 'audiobook'"})
		return
	}

	resp := bulkResponse{Results: make(map[string]bulkItemResult, len(req.IDs))}

	var searchTargets []models.Book

	for _, id := range req.IDs {
		key := fmt.Sprintf("%d", id)
		var opErr error
		switch req.Action {
		case "monitor":
			opErr = h.setBookMonitored(r.Context(), id, true)
		case "unmonitor":
			opErr = h.setBookMonitored(r.Context(), id, false)
		case "delete":
			opErr = h.deleteBook(r.Context(), id)
		case "search":
			book, err := h.books.GetByID(r.Context(), id)
			if err != nil || book == nil || !auth.CheckOwnership(r.Context(), book.OwnerUserID) {
				resp.Results[key] = bulkItemResult{Error: errBulkBookNotOwned.Error()}
				continue
			}
			if h.searcher != nil {
				searchTargets = append(searchTargets, *book)
			}
		case "set_media_type":
			opErr = h.setBookMediaType(r.Context(), id, req.MediaType)
		case "exclude":
			opErr = h.setBookExcluded(r.Context(), id, true)
		}
		if opErr != nil {
			resp.Results[key] = bulkItemResult{Error: opErr.Error()}
			continue
		}
		resp.Results[key] = bulkItemResult{OK: true}
	}

	if len(searchTargets) > 0 && h.searcher != nil {
		h.fanOutSearches(searchTargets)
	}

	writeJSON(w, http.StatusOK, resp)
}

// WantedBulk handles POST /api/v1/wanted/bulk.
//
// Supported actions: "search", "unmonitor", "blocklist".
// "blocklist" marks the book as unmonitored with status "skipped" so it
// disappears from the Wanted page and is not auto-searched again.
func (h *BulkHandler) WantedBulk(w http.ResponseWriter, r *http.Request) {
	var req struct {
		IDs    []int64 `json:"ids"`
		Action string  `json:"action"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid request body"})
		return
	}
	if len(req.IDs) == 0 {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "ids required"})
		return
	}

	switch req.Action {
	case "search", "unmonitor", "blocklist":
	default:
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "unknown action: " + req.Action})
		return
	}

	resp := bulkResponse{Results: make(map[string]bulkItemResult, len(req.IDs))}

	var searchTargets []models.Book

	// The search action needs the full book for each id. Fetch them in one
	// query instead of a GetByID per id — GetByID runs the book CTE (which
	// aggregates the whole book_files table twice) for a single row, so a
	// 500-id bulk search was ~500 of those. A nil map (query error) makes every
	// lookup miss, which yields the same not-owned result the per-id path did.
	var booksByID map[int64]*models.Book
	if req.Action == "search" {
		booksByID, _ = h.books.GetByIDs(r.Context(), req.IDs)
	}

	for _, id := range req.IDs {
		key := fmt.Sprintf("%d", id)
		var opErr error
		switch req.Action {
		case "search":
			book := booksByID[id]
			if book == nil || !auth.CheckOwnership(r.Context(), book.OwnerUserID) {
				resp.Results[key] = bulkItemResult{Error: errBulkBookNotOwned.Error()}
				continue
			}
			if h.searcher != nil {
				searchTargets = append(searchTargets, *book)
			}
		case "unmonitor":
			opErr = h.setBookMonitored(r.Context(), id, false)
		case "blocklist":
			opErr = h.skipBook(r.Context(), id)
		}
		if opErr != nil {
			resp.Results[key] = bulkItemResult{Error: opErr.Error()}
			continue
		}
		resp.Results[key] = bulkItemResult{OK: true}
	}

	if len(searchTargets) > 0 && h.searcher != nil {
		h.fanOutSearches(searchTargets)
	}

	writeJSON(w, http.StatusOK, resp)
}

// --- helpers -----------------------------------------------------------------

func (h *BulkHandler) setAuthorMonitored(ctx context.Context, id int64, monitored bool) error {
	author, err := h.authors.GetByID(ctx, id)
	if err != nil {
		return err
	}
	if author == nil || !auth.CheckOwnership(ctx, author.OwnerUserID) {
		return errBulkAuthorNotOwned
	}
	author.Monitored = monitored
	return h.authors.Update(ctx, author)
}

func (h *BulkHandler) setAuthorMonitorMode(ctx context.Context, id int64, mode string, latestCount *int, applyExisting bool) error {
	author, err := h.authors.GetByID(ctx, id)
	if err != nil {
		return err
	}
	if author == nil || !auth.CheckOwnership(ctx, author.OwnerUserID) {
		return errBulkAuthorNotOwned
	}

	author.MonitorMode = mode
	if mode == models.AuthorMonitorModeLatest {
		if latestCount == nil || *latestCount <= 0 {
			return fmt.Errorf("monitorLatestCount must be a positive integer")
		}
		author.MonitorLatestCount = *latestCount
	}
	if err := h.authors.Update(ctx, author); err != nil {
		return err
	}
	if applyExisting {
		if err := applyMonitorModeToExistingBooks(ctx, h.books, h.authors, h.series, author); err != nil {
			return err
		}
	}
	return nil
}

// deleteBook removes a book after a Tier-1 ownership check, mirroring the
// single-book BookHandler.Delete guard. Without this the bulk delete path
// deleted purely by id (BookRepo.Delete is `DELETE FROM books WHERE id=?`),
// letting a non-admin user cascade-delete another tenant's books under
// BINDERY_ENFORCE_TENANCY.
func (h *BulkHandler) deleteBook(ctx context.Context, id int64) error {
	book, err := h.books.GetByID(ctx, id)
	if err != nil {
		return err
	}
	if book == nil || !auth.CheckOwnership(ctx, book.OwnerUserID) {
		return errBulkBookNotOwned
	}
	return h.books.Delete(ctx, id)
}

func (h *BulkHandler) setBookMonitored(ctx context.Context, id int64, monitored bool) error {
	book, err := h.books.GetByID(ctx, id)
	if err != nil {
		return err
	}
	if book == nil || !auth.CheckOwnership(ctx, book.OwnerUserID) {
		return errBulkBookNotOwned
	}
	book.Monitored = monitored
	return h.books.Update(ctx, book)
}

// setBookExcluded flags a book as excluded so it is hidden from author/book
// lists and skipped on subsequent OL refreshes. Unlike the toggle endpoint
// at PUT /book/:id/exclude, this is a one-way set used by bulk callers; the
// repo handles the timestamp update.
func (h *BulkHandler) setBookExcluded(ctx context.Context, id int64, excluded bool) error {
	book, err := h.books.GetByID(ctx, id)
	if err != nil {
		return err
	}
	if book == nil || !auth.CheckOwnership(ctx, book.OwnerUserID) {
		return errBulkBookNotOwned
	}
	return h.books.SetExcluded(ctx, id, excluded)
}

func (h *BulkHandler) setBookMediaType(ctx context.Context, id int64, mediaType string) error {
	book, err := h.books.GetByID(ctx, id)
	if err != nil {
		return err
	}
	if book == nil || !auth.CheckOwnership(ctx, book.OwnerUserID) {
		return errBulkBookNotOwned
	}
	book.MediaType = mediaType
	reevaluateBookStatus(book)
	return h.books.Update(ctx, book)
}

// reevaluateBookStatus recomputes the wanted↔imported boundary after a
// media-type change so the book lands on the right list: switching a book
// from 'ebook' to 'audiobook' when only the ebook is on disk must flip it
// back to 'wanted' so it reappears on the Wanted page (and vice versa when
// a file already satisfies the new type). Mid-pipeline ('downloading',
// 'downloaded') and explicitly-skipped books are left alone — retriggering
// a search on a book the download client is already working would duplicate
// effort, and 'skipped' encodes a user decision we don't want to override.
func reevaluateBookStatus(b *models.Book) {
	switch b.Status {
	case models.BookStatusSkipped, models.BookStatusDownloading, models.BookStatusDownloaded:
		return
	}
	if b.NeedsEbook() || b.NeedsAudiobook() {
		b.Status = models.BookStatusWanted
		return
	}
	if b.EbookFilePath != "" || b.AudiobookFilePath != "" {
		b.Status = models.BookStatusImported
	}
}

// setAuthorBooksMediaType applies the given media type to every book in an
// author's catalogue. Used by the Authors page bulk action so a user
// switching their whole library from ebook to audiobook (or vice versa)
// doesn't have to touch each book individually. The author must exist;
// books already carrying the target value are still rewritten (cheap no-op
// update rather than a dedicated WHERE clause so callers don't have to
// understand SQLite semantics).
func (h *BulkHandler) setAuthorBooksMediaType(ctx context.Context, authorID int64, mediaType string) error {
	author, err := h.authors.GetByID(ctx, authorID)
	if err != nil {
		return err
	}
	if author == nil || !auth.CheckOwnership(ctx, author.OwnerUserID) {
		return errBulkAuthorNotOwned
	}
	books, err := h.books.ListByAuthor(ctx, authorID)
	if err != nil {
		return fmt.Errorf("list books: %w", err)
	}
	for i := range books {
		if books[i].MediaType == mediaType {
			continue
		}
		books[i].MediaType = mediaType
		reevaluateBookStatus(&books[i])
		if err := h.books.Update(ctx, &books[i]); err != nil {
			return fmt.Errorf("update book %d: %w", books[i].ID, err)
		}
	}
	return nil
}

// skipBook marks a wanted book as unmonitored and skipped so it is removed
// from the Wanted list and not auto-searched again. Equivalent to the
// per-book Sonarr "mark as unmonitored" workflow when the user has no
// interest in acquiring the title.
func (h *BulkHandler) skipBook(ctx context.Context, id int64) error {
	book, err := h.books.GetByID(ctx, id)
	if err != nil {
		return err
	}
	if book == nil || !auth.CheckOwnership(ctx, book.OwnerUserID) {
		return errBulkBookNotOwned
	}
	book.Monitored = false
	book.Status = models.BookStatusSkipped
	return h.books.Update(ctx, book)
}
