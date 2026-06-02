package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/vavallee/bindery/internal/bookhydrate"
	"github.com/vavallee/bindery/internal/concurrency"
	"github.com/vavallee/bindery/internal/db"
	"github.com/vavallee/bindery/internal/metadata"
	"github.com/vavallee/bindery/internal/models"
	"github.com/vavallee/bindery/internal/seriesmatch"
	"github.com/vavallee/bindery/internal/textutil"
)

// seriesFillSearchConcurrency caps how many indexer searches a single
// Series.Fill action fans out at once. A 30-book series is a normal
// shape, so the prior `go SearchAndGrabBook` per book could burst dozens
// of simultaneous indexer calls. The bound is intentionally tighter than
// the bulk endpoint's because Fill is more often clicked on multiple
// series back-to-back.
const seriesFillSearchConcurrency = 4

type SeriesHandler struct {
	series                      *db.SeriesRepo
	books                       *db.BookRepo
	authors                     *db.AuthorRepo
	meta                        *metadata.Aggregator
	searcher                    BookSearcher
	settings                    *db.SettingsRepo
	finder                      LibraryFinder
	enhancedHardcoverEnvEnabled bool
	editions                    *db.EditionRepo

	editionFetcher bookhydrate.EditionFetcher
}

const (
	autoHardcoverLinkMinConfidence = 0.70
	hardcoverNoEvidenceScoreCap    = autoHardcoverLinkMinConfidence - 0.01
	seriesTitleMaxLength           = 500
)

func NewSeriesHandler(series *db.SeriesRepo, books *db.BookRepo, authors *db.AuthorRepo, meta *metadata.Aggregator, searcher BookSearcher) *SeriesHandler {
	return &SeriesHandler{series: series, books: books, authors: authors, meta: meta, searcher: searcher}
}

func (h *SeriesHandler) WithHardcoverFeatureSettings(settings *db.SettingsRepo, envEnabled bool) *SeriesHandler {
	h.settings = settings
	h.enhancedHardcoverEnvEnabled = envEnabled
	return h
}

// WithFinder attaches a LibraryFinder so that ensureHardcoverCatalogBook can
// check whether a newly-created wanted book already exists on disk.
func (h *SeriesHandler) WithFinder(f LibraryFinder) *SeriesHandler {
	h.finder = f
	return h
}

// WithEditionHydration wires edition persistence for Hardcover catalog books.
func (h *SeriesHandler) WithEditionHydration(editions *db.EditionRepo) *SeriesHandler {
	h.editions = editions
	return h
}

// WithEditionFetcher overrides the edition fetcher used by tests.
func (h *SeriesHandler) WithEditionFetcher(fetcher bookhydrate.EditionFetcher) *SeriesHandler {
	h.editionFetcher = fetcher
	return h
}

func (h *SeriesHandler) hydrateHardcoverEditions(ctx context.Context, book *models.Book) {
	if book == nil || h.editions == nil {
		return
	}
	fetcher := h.editionFetcher
	if fetcher == nil && h.meta != nil {
		fetcher = func(ctx context.Context, foreignID string) ([]models.Edition, error) {
			return h.meta.GetEditionsFromProvider(ctx, "hardcover", foreignID)
		}
	}
	bookhydrate.HydrateHardcoverEditions(ctx, bookhydrate.Options{
		Book:          book,
		Provider:      "hardcover",
		Editions:      h.editions,
		Books:         h.books,
		FetchEditions: fetcher,
		Enricher:      h.meta,
	})
}

func (h *SeriesHandler) hardcoverFeatureState(ctx context.Context) HardcoverFeatureState {
	if h.settings == nil {
		return HardcoverFeatureState{EnhancedHardcoverAPI: true, HardcoverTokenConfigured: true}
	}
	return HardcoverFeatureStateFor(ctx, h.settings, h.enhancedHardcoverEnvEnabled)
}

func (h *SeriesHandler) enhancedHardcoverEnabled(ctx context.Context) bool {
	return h.hardcoverFeatureState(ctx).EnhancedHardcoverAPI
}

func (h *SeriesHandler) requireEnhancedHardcoverAPI(w http.ResponseWriter, r *http.Request) bool {
	state := h.hardcoverFeatureState(r.Context())
	if state.EnhancedHardcoverAPI {
		return true
	}
	writeJSON(w, http.StatusNotFound, map[string]string{
		"error":  "enhanced hardcover api disabled",
		"reason": state.EnhancedHardcoverDisabledReason,
	})
	return false
}

func validateSeriesTitle(title string) (string, string) {
	title = strings.TrimSpace(title)
	if title == "" {
		return "", "title is required"
	}
	if len(title) > seriesTitleMaxLength {
		return "", "title is too long"
	}
	return title, ""
}

func (h *SeriesHandler) List(w http.ResponseWriter, r *http.Request) {
	series, err := h.series.ListWithBooks(r.Context())
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	if series == nil {
		series = []models.Series{}
	}
	writeJSON(w, http.StatusOK, series)
}

func (h *SeriesHandler) Get(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid id"})
		return
	}

	s, err := h.series.GetByID(r.Context(), id)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	if s == nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "series not found"})
		return
	}
	if s.Books == nil {
		s.Books = []models.SeriesBook{}
	}
	writeJSON(w, http.StatusOK, s)
}

func (h *SeriesHandler) Create(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Title string `json:"title"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid request body"})
		return
	}
	title, validationErr := validateSeriesTitle(body.Title)
	if validationErr != "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": validationErr})
		return
	}
	series, err := h.series.CreateManual(r.Context(), title)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusCreated, series)
}

func (h *SeriesHandler) Update(w http.ResponseWriter, r *http.Request) {
	id, ok := parseID(w, r)
	if !ok {
		return
	}
	var body struct {
		Title string `json:"title"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid request body"})
		return
	}
	title, validationErr := validateSeriesTitle(body.Title)
	if validationErr != "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": validationErr})
		return
	}
	existing, err := h.series.GetByID(r.Context(), id)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	if existing == nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "series not found"})
		return
	}
	if err := h.series.UpdateTitle(r.Context(), id, title); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	updated, err := h.series.GetByID(r.Context(), id)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, updated)
}

func (h *SeriesHandler) Delete(w http.ResponseWriter, r *http.Request) {
	id, ok := parseID(w, r)
	if !ok {
		return
	}
	existing, err := h.series.GetByID(r.Context(), id)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	if existing == nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "series not found"})
		return
	}
	if err := h.series.Delete(r.Context(), id); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *SeriesHandler) AddBook(w http.ResponseWriter, r *http.Request) {
	id, ok := parseID(w, r)
	if !ok {
		return
	}
	var body struct {
		BookID           int64  `json:"bookId"`
		PositionInSeries string `json:"positionInSeries"`
		PrimarySeries    *bool  `json:"primarySeries"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid request body"})
		return
	}
	if body.BookID <= 0 {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "bookId is required"})
		return
	}
	series, err := h.series.GetByID(r.Context(), id)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	if series == nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "series not found"})
		return
	}
	book, err := h.books.GetByID(r.Context(), body.BookID)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	if book == nil || book.Excluded {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "book not found"})
		return
	}
	primary := true
	if body.PrimarySeries != nil {
		primary = *body.PrimarySeries
	}
	if err := h.series.UpsertBookLink(r.Context(), id, body.BookID, body.PositionInSeries, primary); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	updated, err := h.series.GetByID(r.Context(), id)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, updated)
}

// Monitor toggles the monitored flag on a series.
func (h *SeriesHandler) Monitor(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid id"})
		return
	}

	var body struct {
		Monitored bool `json:"monitored"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid request body"})
		return
	}

	if err := h.series.SetMonitored(r.Context(), id, body.Monitored); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"monitored": body.Monitored})
}

type seriesFillRequest struct {
	ForeignBookID string `json:"foreignBookId"`
	ProviderID    string `json:"providerId"`
	Position      string `json:"position"`
}

func (r *seriesFillRequest) normalize() {
	r.ForeignBookID = strings.TrimSpace(r.ForeignBookID)
	r.ProviderID = strings.TrimSpace(r.ProviderID)
	r.Position = strings.TrimSpace(r.Position)
}

func (r seriesFillRequest) hasBookSelector() bool {
	return r.ForeignBookID != "" || r.ProviderID != "" || r.Position != ""
}

func (r seriesFillRequest) matchesDiffBook(book seriesHardcoverDiffBook) bool {
	matched := false
	if r.ForeignBookID != "" {
		if r.ForeignBookID != book.ForeignBookID {
			return false
		}
		matched = true
	}
	if r.ProviderID != "" {
		if r.ProviderID != book.ProviderID {
			return false
		}
		matched = true
	}
	if r.Position != "" {
		if !seriesmatch.SamePosition(r.Position, book.Position) {
			return false
		}
		matched = true
	}
	return matched
}

// Fill marks non-imported books in a series as wanted and kicks off searches.
func (h *SeriesHandler) Fill(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid id"})
		return
	}

	var body seriesFillRequest
	if r.Body != nil {
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil && !errors.Is(err, io.EOF) {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid request body"})
			return
		}
	}
	body.normalize()

	if body.hasBookSelector() {
		if !h.requireEnhancedHardcoverAPI(w, r) {
			return
		}
		book, err := h.createMissingHardcoverBook(r.Context(), id, body)
		if err != nil {
			if errors.Is(err, errSeriesNotFound) {
				writeJSON(w, http.StatusNotFound, map[string]string{"error": "series not found"})
				return
			}
			if errors.Is(err, errSeriesCatalogBookNotFound) {
				writeJSON(w, http.StatusNotFound, map[string]string{"error": "hardcover book not found in missing catalog"})
				return
			}
			if errors.Is(err, errSeriesMetadataProvider) {
				writeUpstreamError(w, err)
				return
			}
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
			return
		}

		queued := 0
		if book != nil {
			didQueue, queuedBook, err := h.queueSeriesBook(r.Context(), *book)
			if err != nil {
				writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
				return
			}
			if didQueue {
				queued = 1
				h.fanOutSeriesSearches(r.Context(), []models.Book{queuedBook})
			}
		}
		writeJSON(w, http.StatusOK, map[string]int{"queued": queued})
		return
	}

	if h.enhancedHardcoverEnabled(r.Context()) {
		if err := h.createMissingHardcoverBooks(r.Context(), id); err != nil {
			if !errors.Is(err, errSeriesMetadataProvider) {
				writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
				return
			}
			slog.Warn("series fill: failed to expand Hardcover catalog; continuing with local books", "seriesID", id, "error", err)
		}
	}

	books, err := h.series.ListBooksInSeries(r.Context(), id)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}

	queued := 0
	searchTargets := make([]models.Book, 0, len(books))
	for _, b := range books {
		didQueue, queuedBook, err := h.queueSeriesBook(r.Context(), b)
		if err != nil {
			slog.Warn("series fill: failed to update book", "book", b.Title, "error", err)
			continue
		}
		if didQueue {
			queued++
			searchTargets = append(searchTargets, queuedBook)
		}
	}

	h.fanOutSeriesSearches(r.Context(), searchTargets)

	writeJSON(w, http.StatusOK, map[string]int{"queued": queued})
}

// fanOutSeriesSearches dispatches per-book indexer searches for a Series.Fill
// under a bounded pool so a 30-book series can't burst one goroutine per
// book at the indexers. The pool runs on a detached context derived from
// ctx (via context.WithoutCancel) so the HTTP response is not held while
// the searches run, but credentials and per-user metadata on ctx still
// flow through to the indexer layer.
func (h *SeriesHandler) fanOutSeriesSearches(ctx context.Context, books []models.Book) {
	if h.searcher == nil || len(books) == 0 {
		return
	}
	bgCtx := context.WithoutCancel(ctx)
	go concurrency.RunBounded(bgCtx, books, seriesFillSearchConcurrency, func(ctx context.Context, b models.Book) {
		h.searcher.SearchAndGrabBook(ctx, b)
	})
}

// queueSeriesBook marks a series book wanted+monitored and returns the
// reloaded book (the second return value) so the caller can hand it to a
// bounded indexer fan-out. The boolean reports whether the book was newly
// queued (false for already-satisfied / in-flight books, which are
// skipped without touching the DB and without scheduling a search).
func (h *SeriesHandler) queueSeriesBook(ctx context.Context, b models.Book) (bool, models.Book, error) {
	// Only act on books that are not already satisfied or in flight. Re-queuing
	// a book that is downloading or downloaded would reset it to wanted and fire
	// a second grab for a download already underway.
	switch b.Status {
	case models.BookStatusImported, models.BookStatusDownloading, models.BookStatusDownloaded:
		return false, models.Book{}, nil
	}
	if err := h.books.MarkWantedMonitored(ctx, b.ID); err != nil {
		return false, models.Book{}, err
	}
	if full, err := h.books.GetByID(ctx, b.ID); err != nil {
		slog.Warn("series fill: failed to reload queued book metadata", "bookID", b.ID, "error", err)
	} else if full != nil {
		b = *full
	}
	b.Status = models.BookStatusWanted
	b.Monitored = true
	return true, b, nil
}

type seriesHardcoverSearchResult struct {
	ForeignID    string   `json:"foreignId"`
	ProviderID   string   `json:"providerId"`
	Title        string   `json:"title"`
	AuthorName   string   `json:"authorName"`
	BookCount    int      `json:"bookCount"`
	ReadersCount int      `json:"readersCount"`
	Books        []string `json:"books"`
	Confidence   float64  `json:"confidence,omitempty"`
}

type seriesHardcoverLinkRequest struct {
	ForeignID  string  `json:"foreignId"`
	ProviderID string  `json:"providerId"`
	Title      string  `json:"title"`
	AuthorName string  `json:"authorName"`
	BookCount  int     `json:"bookCount"`
	Confidence float64 `json:"confidence"`
}

type seriesHardcoverAutoResponse struct {
	Linked     bool                          `json:"linked"`
	Link       *models.SeriesHardcoverLink   `json:"link,omitempty"`
	Candidates []seriesHardcoverSearchResult `json:"candidates"`
	Reason     string                        `json:"reason,omitempty"`
}

type seriesHardcoverDiffBook struct {
	ForeignBookID   string     `json:"foreignBookId"`
	ProviderID      string     `json:"providerId"`
	Title           string     `json:"title"`
	Subtitle        string     `json:"subtitle,omitempty"`
	Position        string     `json:"position"`
	ImageURL        string     `json:"imageUrl,omitempty"`
	AuthorName      string     `json:"authorName,omitempty"`
	ReleaseDate     *time.Time `json:"releaseDate,omitempty"`
	UsersCount      int        `json:"usersCount,omitempty"`
	LocalBookID     *int64     `json:"localBookId,omitempty"`
	LocalTitle      string     `json:"localTitle,omitempty"`
	LocalStatus     string     `json:"localStatus,omitempty"`
	MatchConfidence float64    `json:"matchConfidence,omitempty"`
}

type seriesHardcoverDiffResponse struct {
	SeriesID     int64                       `json:"seriesId"`
	Link         *models.SeriesHardcoverLink `json:"link"`
	Present      []seriesHardcoverDiffBook   `json:"present"`
	Missing      []seriesHardcoverDiffBook   `json:"missing"`
	LocalOnly    []seriesHardcoverDiffBook   `json:"localOnly"`
	Uncertain    []seriesHardcoverDiffBook   `json:"uncertain"`
	PresentCount int                         `json:"presentCount"`
	MissingCount int                         `json:"missingCount"`
}

var (
	errSeriesMetadataProvider    = errors.New("series metadata provider")
	errSeriesNotFound            = errors.New("series not found")
	errSeriesCatalogBookNotFound = errors.New("series catalog book not found")
)

func (h *SeriesHandler) SearchHardcover(w http.ResponseWriter, r *http.Request) {
	if !h.requireEnhancedHardcoverAPI(w, r) {
		return
	}
	term := strings.TrimSpace(r.URL.Query().Get("term"))
	if term == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "term parameter required"})
		return
	}
	limit := 10
	if raw := strings.TrimSpace(r.URL.Query().Get("limit")); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil || parsed <= 0 {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid limit"})
			return
		}
		limit = parsed
	}
	if limit > 50 {
		limit = 50
	}

	results, err := h.searchHardcoverSeries(r.Context(), term, limit)
	if err != nil {
		writeUpstreamError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, results)
}

func (h *SeriesHandler) GetHardcoverLink(w http.ResponseWriter, r *http.Request) {
	if !h.requireEnhancedHardcoverAPI(w, r) {
		return
	}
	id, ok := parseID(w, r)
	if !ok {
		return
	}
	link, err := h.series.GetHardcoverLink(r.Context(), id)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	if link == nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "hardcover link not found"})
		return
	}
	writeJSON(w, http.StatusOK, link)
}

func (h *SeriesHandler) AutoLinkHardcover(w http.ResponseWriter, r *http.Request) {
	if !h.requireEnhancedHardcoverAPI(w, r) {
		return
	}
	id, ok := parseID(w, r)
	if !ok {
		return
	}
	series, err := h.series.GetByID(r.Context(), id)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	if series == nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "series not found"})
		return
	}

	results, err := h.searchHardcoverSeries(r.Context(), series.Title, 10)
	if err != nil {
		writeUpstreamError(w, err)
		return
	}
	candidates, err := h.scoreHardcoverCandidates(r.Context(), series, results)
	if err != nil {
		writeUpstreamError(w, err)
		return
	}
	if len(candidates) == 0 {
		writeJSON(w, http.StatusOK, seriesHardcoverAutoResponse{Candidates: []seriesHardcoverSearchResult{}, Reason: "no candidates"})
		return
	}
	top := candidates[0]
	ambiguous := len(candidates) > 1 && top.Confidence-candidates[1].Confidence < 0.05
	if top.Confidence < autoHardcoverLinkMinConfidence || ambiguous {
		reason := "low confidence"
		if ambiguous {
			reason = "ambiguous candidates"
		}
		writeJSON(w, http.StatusOK, seriesHardcoverAutoResponse{Candidates: candidates, Reason: reason})
		return
	}

	link := &models.SeriesHardcoverLink{
		SeriesID:            series.ID,
		HardcoverSeriesID:   top.ForeignID,
		HardcoverProviderID: top.ProviderID,
		HardcoverTitle:      top.Title,
		HardcoverAuthorName: top.AuthorName,
		HardcoverBookCount:  top.BookCount,
		Confidence:          top.Confidence,
		LinkedBy:            "auto",
	}
	if err := h.series.UpsertHardcoverLink(r.Context(), link); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, seriesHardcoverAutoResponse{Linked: true, Link: link, Candidates: candidates})
}

func (h *SeriesHandler) PutHardcoverLink(w http.ResponseWriter, r *http.Request) {
	if !h.requireEnhancedHardcoverAPI(w, r) {
		return
	}
	id, ok := parseID(w, r)
	if !ok {
		return
	}
	if series, err := h.series.GetByID(r.Context(), id); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	} else if series == nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "series not found"})
		return
	}

	var body seriesHardcoverLinkRequest
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid request body"})
		return
	}
	body.ForeignID = strings.TrimSpace(body.ForeignID)
	if body.ForeignID == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "foreignId is required"})
		return
	}

	link, err := h.linkFromRequest(r.Context(), id, body, "manual")
	if err != nil {
		if errors.Is(err, errSeriesMetadataProvider) {
			writeUpstreamError(w, err)
			return
		}
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	if err := h.series.UpsertHardcoverLink(r.Context(), link); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, link)
}

func (h *SeriesHandler) DeleteHardcoverLink(w http.ResponseWriter, r *http.Request) {
	if !h.requireEnhancedHardcoverAPI(w, r) {
		return
	}
	id, ok := parseID(w, r)
	if !ok {
		return
	}
	if err := h.series.DeleteHardcoverLink(r.Context(), id); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"success": true})
}

func (h *SeriesHandler) HardcoverDiff(w http.ResponseWriter, r *http.Request) {
	if !h.requireEnhancedHardcoverAPI(w, r) {
		return
	}
	id, ok := parseID(w, r)
	if !ok {
		return
	}
	series, err := h.series.GetByID(r.Context(), id)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	if series == nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "series not found"})
		return
	}
	link := series.HardcoverLink
	if link == nil {
		link, err = h.series.GetHardcoverLink(r.Context(), id)
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
			return
		}
	}
	if link == nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "hardcover link not found"})
		return
	}
	catalog, err := h.hardcoverCatalog(r.Context(), link.HardcoverSeriesID)
	if err != nil {
		writeUpstreamError(w, err)
		return
	}
	diff := buildHardcoverDiff(series, link, catalog)
	writeJSON(w, http.StatusOK, diff)
}

func (h *SeriesHandler) searchHardcoverSeries(ctx context.Context, term string, limit int) ([]seriesHardcoverSearchResult, error) {
	if h.meta == nil {
		return nil, errors.New("metadata aggregator is not configured")
	}
	results, err := h.meta.SearchSeries(ctx, term, limit)
	if err != nil {
		return nil, err
	}
	out := make([]seriesHardcoverSearchResult, 0, len(results))
	for _, result := range results {
		out = append(out, mapHardcoverSearchResult(result))
	}
	return out, nil
}

func mapHardcoverSearchResult(result metadata.SeriesSearchResult) seriesHardcoverSearchResult {
	books := result.Books
	if books == nil {
		books = []string{}
	}
	return seriesHardcoverSearchResult{
		ForeignID:    result.ForeignID,
		ProviderID:   result.ProviderID,
		Title:        result.Title,
		AuthorName:   result.AuthorName,
		BookCount:    result.BookCount,
		ReadersCount: result.ReadersCount,
		Books:        books,
	}
}

func (h *SeriesHandler) scoreHardcoverCandidates(ctx context.Context, series *models.Series, results []seriesHardcoverSearchResult) ([]seriesHardcoverSearchResult, error) {
	candidates := make([]seriesHardcoverSearchResult, 0, len(results))
	for _, result := range results {
		catalog, err := h.meta.GetSeriesCatalog(ctx, result.ForeignID)
		if err != nil {
			return nil, err
		}
		if catalog != nil {
			if result.Title == "" {
				result.Title = catalog.Title
			}
			if result.AuthorName == "" {
				result.AuthorName = catalog.AuthorName
			}
			if result.BookCount == 0 {
				result.BookCount = catalog.BookCount
			}
		}
		result.Confidence = scoreHardcoverCandidate(series, result, catalog)
		hasEvidence, err := h.hardcoverCandidateHasLocalEvidence(ctx, series, result, catalog)
		if err != nil {
			return nil, err
		}
		if !hasEvidence && result.Confidence >= autoHardcoverLinkMinConfidence {
			result.Confidence = hardcoverNoEvidenceScoreCap
		}
		candidates = append(candidates, result)
	}
	sort.SliceStable(candidates, func(i, j int) bool {
		return candidates[i].Confidence > candidates[j].Confidence
	})
	return candidates, nil
}

func (h *SeriesHandler) hardcoverCandidateHasLocalEvidence(ctx context.Context, series *models.Series, result seriesHardcoverSearchResult, catalog *metadata.SeriesCatalog) (bool, error) {
	if series == nil {
		return false, nil
	}
	if hardcoverCandidateHasBookOverlap(series, result, catalog) {
		return true, nil
	}
	candidateAuthor := result.AuthorName
	if catalog != nil && strings.TrimSpace(catalog.AuthorName) != "" {
		candidateAuthor = catalog.AuthorName
	}
	if strings.TrimSpace(candidateAuthor) == "" {
		return false, nil
	}
	return h.seriesHasAuthorAgreement(ctx, series, candidateAuthor)
}

func hardcoverCandidateHasBookOverlap(series *models.Series, result seriesHardcoverSearchResult, catalog *metadata.SeriesCatalog) bool {
	if series == nil {
		return false
	}
	for _, local := range series.Books {
		if local.Book == nil {
			continue
		}
		if catalog != nil && bestCatalogMatch(local, catalog.Books).score >= 85 {
			return true
		}
		for _, title := range result.Books {
			if seriesmatch.TitleScore(local.Book.Title, title) >= 85 {
				return true
			}
		}
	}
	return false
}

func (h *SeriesHandler) seriesHasAuthorAgreement(ctx context.Context, series *models.Series, candidateAuthor string) (bool, error) {
	if series == nil {
		return false, nil
	}
	authorsByID := map[int64]string{}
	for _, local := range series.Books {
		if local.Book == nil {
			continue
		}
		localAuthor := ""
		if local.Book.Author != nil {
			localAuthor = local.Book.Author.Name
		}
		if strings.TrimSpace(localAuthor) == "" && local.Book.AuthorID > 0 && h.authors != nil {
			if cached, ok := authorsByID[local.Book.AuthorID]; ok {
				localAuthor = cached
			} else {
				author, err := h.authors.GetByID(ctx, local.Book.AuthorID)
				if err != nil {
					return false, err
				}
				if author != nil {
					localAuthor = author.Name
				}
				authorsByID[local.Book.AuthorID] = localAuthor
			}
		}
		match := textutil.MatchAuthorName(localAuthor, candidateAuthor)
		if match.Kind == textutil.AuthorMatchExact || match.Kind == textutil.AuthorMatchFuzzyAuto {
			return true, nil
		}
	}
	return false, nil
}

func scoreHardcoverCandidate(series *models.Series, result seriesHardcoverSearchResult, catalog *metadata.SeriesCatalog) float64 {
	if series == nil {
		return 0
	}
	candidateTitle := result.Title
	if catalog != nil && strings.TrimSpace(catalog.Title) != "" {
		candidateTitle = catalog.Title
	}
	seriesTitleScore := seriesmatch.TitleScore(series.Title, candidateTitle)
	if seriesmatch.NormalizeSeriesName(series.Title) == seriesmatch.NormalizeSeriesName(candidateTitle) {
		seriesTitleScore = 100
	}

	bookTitleScore := 0
	for _, title := range result.Books {
		bookTitleScore = max(bookTitleScore, seriesmatch.TitleScore(series.Title, title))
	}
	if catalog != nil {
		for _, book := range catalog.Books {
			bookTitleScore = max(bookTitleScore, seriesmatch.TitleScore(series.Title, firstNonEmpty(book.Title, book.Book.Title)))
		}
	}

	score := float64(seriesTitleScore)
	if seriesTitleScore < 92 && bookTitleScore >= 92 {
		score = 70
	} else if bookTitleScore > 0 {
		score = 0.75*float64(seriesTitleScore) + 0.25*float64(bookTitleScore)
	}

	if catalog != nil && len(series.Books) > 0 {
		matches := 0
		checked := 0
		for _, local := range series.Books {
			if local.Book == nil {
				continue
			}
			checked++
			if bestCatalogMatch(local, catalog.Books).score >= 85 {
				matches++
			}
		}
		if checked > 0 {
			overlapScore := 65 + 30*(float64(matches)/float64(checked))
			score = maxFloat(score, overlapScore)
		}
	}

	if score > 100 {
		score = 100
	}
	return score / 100
}

func (h *SeriesHandler) linkFromRequest(ctx context.Context, seriesID int64, body seriesHardcoverLinkRequest, linkedBy string) (*models.SeriesHardcoverLink, error) {
	confidence := body.Confidence
	if confidence <= 0 {
		confidence = 1
	}
	link := &models.SeriesHardcoverLink{
		SeriesID:            seriesID,
		HardcoverSeriesID:   body.ForeignID,
		HardcoverProviderID: firstNonEmpty(body.ProviderID, strings.TrimPrefix(body.ForeignID, "hc-series:")),
		HardcoverTitle:      body.Title,
		HardcoverAuthorName: body.AuthorName,
		HardcoverBookCount:  body.BookCount,
		Confidence:          confidence,
		LinkedBy:            linkedBy,
	}
	if h.meta == nil {
		return link, nil
	}
	catalog, err := h.meta.GetSeriesCatalog(ctx, body.ForeignID)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", errSeriesMetadataProvider, err)
	}
	if catalog == nil {
		return link, nil
	}
	link.HardcoverSeriesID = catalog.ForeignID
	link.HardcoverProviderID = catalog.ProviderID
	link.HardcoverTitle = catalog.Title
	link.HardcoverAuthorName = catalog.AuthorName
	link.HardcoverBookCount = catalog.BookCount
	return link, nil
}

func (h *SeriesHandler) hardcoverCatalog(ctx context.Context, foreignID string) (*metadata.SeriesCatalog, error) {
	if h.meta == nil {
		return nil, errors.New("metadata aggregator is not configured")
	}
	catalog, err := h.meta.GetSeriesCatalog(ctx, foreignID)
	if err != nil {
		return nil, err
	}
	if catalog == nil {
		return nil, fmt.Errorf("hardcover series %q not found", foreignID)
	}
	return catalog, nil
}

func buildHardcoverDiff(series *models.Series, link *models.SeriesHardcoverLink, catalog *metadata.SeriesCatalog) seriesHardcoverDiffResponse {
	diff := seriesHardcoverDiffResponse{
		SeriesID:  series.ID,
		Link:      link,
		Present:   []seriesHardcoverDiffBook{},
		Missing:   []seriesHardcoverDiffBook{},
		LocalOnly: []seriesHardcoverDiffBook{},
		Uncertain: []seriesHardcoverDiffBook{},
	}
	matchedCatalog := make(map[int]struct{})
	for _, local := range series.Books {
		if local.Book == nil {
			continue
		}
		match := bestCatalogMatch(local, catalog.Books)
		localItem := localDiffBook(local)
		if match.index < 0 {
			diff.LocalOnly = append(diff.LocalOnly, localItem)
			continue
		}
		item := catalogDiffBook(catalog.Books[match.index], catalog.AuthorName)
		item.LocalBookID = &local.Book.ID
		item.LocalTitle = local.Book.Title
		item.LocalStatus = local.Book.Status
		item.MatchConfidence = float64(match.score) / 100
		if match.score >= 90 || match.foreignID {
			diff.Present = append(diff.Present, item)
			matchedCatalog[match.index] = struct{}{}
		} else if match.score >= 70 {
			diff.Uncertain = append(diff.Uncertain, item)
			matchedCatalog[match.index] = struct{}{}
		} else {
			diff.LocalOnly = append(diff.LocalOnly, localItem)
		}
	}
	for i, book := range catalog.Books {
		if _, ok := matchedCatalog[i]; ok {
			continue
		}
		diff.Missing = append(diff.Missing, catalogDiffBook(book, catalog.AuthorName))
	}
	diff.PresentCount = len(diff.Present)
	diff.MissingCount = len(diff.Missing)
	return diff
}

type catalogMatch struct {
	index     int
	score     int
	foreignID bool
}

func bestCatalogMatch(local models.SeriesBook, books []metadata.SeriesCatalogBook) catalogMatch {
	best := catalogMatch{index: -1}
	if local.Book == nil {
		return best
	}
	for i, candidate := range books {
		foreignMatch := local.Book.ForeignID != "" && (local.Book.ForeignID == candidate.ForeignID || local.Book.ForeignID == candidate.Book.ForeignID)
		score := 0
		if foreignMatch {
			score = 100
		} else {
			score = seriesmatch.TitleScore(local.Book.Title, firstNonEmpty(candidate.Title, candidate.Book.Title))
			if seriesmatch.SamePosition(local.PositionInSeries, candidate.Position) && score >= 70 {
				score = max(score, 90)
			}
		}
		if score > best.score {
			best = catalogMatch{index: i, score: score, foreignID: foreignMatch}
		}
	}
	return best
}

func catalogDiffBook(book metadata.SeriesCatalogBook, fallbackAuthor string) seriesHardcoverDiffBook {
	title := firstNonEmpty(book.Title, book.Book.Title)
	author := fallbackAuthor
	if book.Book.Author != nil && strings.TrimSpace(book.Book.Author.Name) != "" {
		author = book.Book.Author.Name
	}
	return seriesHardcoverDiffBook{
		ForeignBookID: firstNonEmpty(book.ForeignID, book.Book.ForeignID),
		ProviderID:    book.ProviderID,
		Title:         title,
		Subtitle:      book.Subtitle,
		Position:      book.Position,
		ImageURL:      book.Book.ImageURL,
		AuthorName:    author,
		ReleaseDate:   book.Book.ReleaseDate,
		UsersCount:    book.UsersCount,
	}
}

func localDiffBook(local models.SeriesBook) seriesHardcoverDiffBook {
	item := seriesHardcoverDiffBook{Position: local.PositionInSeries}
	if local.Book == nil {
		return item
	}
	item.ForeignBookID = local.Book.ForeignID
	item.Title = local.Book.Title
	item.ImageURL = local.Book.ImageURL
	item.ReleaseDate = local.Book.ReleaseDate
	item.LocalBookID = &local.Book.ID
	item.LocalTitle = local.Book.Title
	item.LocalStatus = local.Book.Status
	return item
}

func (h *SeriesHandler) createMissingHardcoverBooks(ctx context.Context, seriesID int64) error {
	if !h.enhancedHardcoverEnabled(ctx) {
		return nil
	}
	if h.meta == nil || h.authors == nil {
		return nil
	}
	series, err := h.series.GetByID(ctx, seriesID)
	if err != nil {
		return err
	}
	if series == nil || series.HardcoverLink == nil {
		return nil
	}
	catalog, err := h.meta.GetSeriesCatalog(ctx, series.HardcoverLink.HardcoverSeriesID)
	if err != nil {
		return fmt.Errorf("%w: %w", errSeriesMetadataProvider, err)
	}
	if catalog == nil {
		return nil
	}
	diff := buildHardcoverDiff(series, series.HardcoverLink, catalog)
	for _, missing := range diff.Missing {
		catalogBook, ok := findCatalogBook(catalog.Books, missing.ForeignBookID, missing.Position)
		if !ok {
			continue
		}
		if _, err := h.ensureHardcoverCatalogBook(ctx, series, catalog.AuthorName, catalogBook); err != nil {
			return err
		}
	}
	return nil
}

func (h *SeriesHandler) createMissingHardcoverBook(ctx context.Context, seriesID int64, selector seriesFillRequest) (*models.Book, error) {
	if h.meta == nil {
		return nil, errors.New("metadata aggregator is not configured")
	}
	if h.authors == nil {
		return nil, errors.New("author repository is not configured")
	}
	series, err := h.series.GetByID(ctx, seriesID)
	if err != nil {
		return nil, err
	}
	if series == nil {
		return nil, errSeriesNotFound
	}
	if series.HardcoverLink == nil {
		return nil, errSeriesCatalogBookNotFound
	}
	catalog, err := h.meta.GetSeriesCatalog(ctx, series.HardcoverLink.HardcoverSeriesID)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", errSeriesMetadataProvider, err)
	}
	if catalog == nil {
		return nil, errSeriesCatalogBookNotFound
	}
	diff := buildHardcoverDiff(series, series.HardcoverLink, catalog)
	for _, missing := range diff.Missing {
		if !selector.matchesDiffBook(missing) {
			continue
		}
		catalogBook, ok := findCatalogBook(catalog.Books, missing.ForeignBookID, missing.Position)
		if !ok {
			return nil, errSeriesCatalogBookNotFound
		}
		return h.ensureHardcoverCatalogBook(ctx, series, catalog.AuthorName, catalogBook)
	}
	return nil, errSeriesCatalogBookNotFound
}

func findCatalogBook(books []metadata.SeriesCatalogBook, foreignID, position string) (metadata.SeriesCatalogBook, bool) {
	for _, book := range books {
		if foreignID != "" && (foreignID == book.ForeignID || foreignID == book.Book.ForeignID) {
			return book, true
		}
		if seriesmatch.SamePosition(position, book.Position) {
			return book, true
		}
	}
	return metadata.SeriesCatalogBook{}, false
}

func (h *SeriesHandler) ensureHardcoverCatalogBook(ctx context.Context, series *models.Series, fallbackAuthor string, catalogBook metadata.SeriesCatalogBook) (*models.Book, error) {
	if series == nil {
		return nil, errSeriesNotFound
	}
	book := catalogBook.Book
	book.ForeignID = firstNonEmpty(book.ForeignID, catalogBook.ForeignID)
	if book.ForeignID == "" {
		book.ForeignID = "hc:" + catalogBook.ProviderID
	}
	if existing, err := h.books.GetByForeignID(ctx, book.ForeignID); err != nil {
		return nil, err
	} else if existing != nil {
		if existing.Excluded {
			return nil, nil
		}
		_, err := h.series.LinkBookIfMissing(ctx, series.ID, existing.ID, catalogBook.Position, true)
		return existing, err
	}

	author := book.Author
	if author == nil {
		author = &models.Author{
			ForeignID:        hardcoverAuthorFallbackID(fallbackAuthor),
			Name:             fallbackAuthor,
			SortName:         sortName(fallbackAuthor),
			MetadataProvider: "hardcover",
		}
	}
	if strings.TrimSpace(author.Name) == "" {
		author.Name = "Unknown Author"
	}
	if strings.TrimSpace(author.SortName) == "" {
		author.SortName = sortName(author.Name)
	}
	if strings.TrimSpace(author.ForeignID) == "" {
		author.ForeignID = hardcoverAuthorFallbackID(author.Name)
	}
	if strings.TrimSpace(author.MetadataProvider) == "" {
		author.MetadataProvider = "hardcover"
	}
	metadataProfileID := models.DefaultMetadataProfileID
	author.MetadataProfileID = &metadataProfileID

	storedAuthor, skip, err := h.resolveHardcoverCatalogAuthor(ctx, series, author)
	if err != nil {
		return nil, err
	}
	if skip {
		return nil, nil
	}
	if storedAuthor == nil {
		if err := h.authors.Create(ctx, author); err != nil {
			return nil, err
		}
		storedAuthor = author
	}

	existingByTitle, err := h.books.ListByAuthorIncludingExcluded(ctx, storedAuthor.ID)
	if err != nil {
		return nil, err
	}
	blockedByExcludedTitle := false
	for _, existing := range existingByTitle {
		if seriesmatch.TitleScore(existing.Title, firstNonEmpty(book.Title, catalogBook.Title)) >= 92 {
			if existing.Excluded {
				blockedByExcludedTitle = true
				continue
			}
			_, err := h.series.LinkBookIfMissing(ctx, series.ID, existing.ID, catalogBook.Position, true)
			return &existing, err
		}
	}
	if blockedByExcludedTitle {
		return nil, nil
	}

	book.AuthorID = storedAuthor.ID
	book.Author = nil
	book.Title = firstNonEmpty(book.Title, catalogBook.Title)
	book.SortTitle = firstNonEmpty(book.SortTitle, book.Title)
	book.Status = models.BookStatusWanted
	book.Monitored = true
	book.AnyEditionOK = true
	book.MediaType = firstNonEmpty(book.MediaType, models.MediaTypeEbook)
	book.Language = firstNonEmpty(book.Language, "eng")
	book.MetadataProvider = firstNonEmpty(book.MetadataProvider, "hardcover")
	if book.Genres == nil {
		book.Genres = []string{}
	}
	if err := h.books.Create(ctx, &book); err != nil {
		return nil, err
	}
	h.hydrateHardcoverEditions(ctx, &book)
	if _, err := h.series.LinkBookIfMissing(ctx, series.ID, book.ID, catalogBook.Position, true); err != nil {
		return nil, err
	}
	handleNewWantedBook(ctx, h.books, h.series, h.finder, book, storedAuthor.Name)
	return &book, nil
}

func (h *SeriesHandler) resolveHardcoverCatalogAuthor(ctx context.Context, series *models.Series, candidate *models.Author) (*models.Author, bool, error) {
	if candidate == nil || h.authors == nil {
		return nil, false, nil
	}
	if strings.TrimSpace(candidate.ForeignID) != "" {
		author, err := h.authors.GetByForeignID(ctx, candidate.ForeignID)
		if err != nil || author != nil {
			return author, false, err
		}
	}
	if strings.TrimSpace(candidate.Name) == "" {
		return nil, false, nil
	}
	if author, ambiguous, err := h.matchSeriesAuthorByName(ctx, series, candidate.Name); err != nil || ambiguous || author != nil {
		return author, ambiguous, err
	}
	return h.matchGlobalAuthorByName(ctx, candidate.Name)
}

func (h *SeriesHandler) matchSeriesAuthorByName(ctx context.Context, series *models.Series, name string) (*models.Author, bool, error) {
	if series == nil || h.authors == nil {
		return nil, false, nil
	}
	seen := map[int64]struct{}{}
	var match *models.Author
	for _, seriesBook := range series.Books {
		var author *models.Author
		if seriesBook.Book != nil && seriesBook.Book.AuthorID > 0 {
			if _, ok := seen[seriesBook.Book.AuthorID]; ok {
				continue
			}
			seen[seriesBook.Book.AuthorID] = struct{}{}
			var err error
			author, err = h.authors.GetByID(ctx, seriesBook.Book.AuthorID)
			if err != nil {
				return nil, false, err
			}
		} else if seriesBook.Book != nil && seriesBook.Book.Author != nil {
			author = seriesBook.Book.Author
		}
		if author == nil || author.ID == 0 {
			continue
		}
		if !trustedAuthorNameMatch(author, name) {
			continue
		}
		if match != nil && match.ID != author.ID {
			return nil, true, nil
		}
		match = author
	}
	return match, false, nil
}

func (h *SeriesHandler) matchGlobalAuthorByName(ctx context.Context, name string) (*models.Author, bool, error) {
	if h.authors == nil {
		return nil, false, nil
	}
	authors, err := h.authors.List(ctx)
	if err != nil {
		return nil, false, err
	}
	var match *models.Author
	for idx := range authors {
		if !trustedAuthorNameMatch(&authors[idx], name) {
			continue
		}
		if match != nil && match.ID != authors[idx].ID {
			return nil, true, nil
		}
		copy := authors[idx]
		match = &copy
	}
	return match, false, nil
}

func trustedAuthorNameMatch(author *models.Author, name string) bool {
	if author == nil {
		return false
	}
	match := textutil.MatchAuthorName(author.Name, name)
	return match.Kind == textutil.AuthorMatchExact || match.Kind == textutil.AuthorMatchFuzzyAuto
}

func hardcoverAuthorFallbackID(name string) string {
	key := strings.ReplaceAll(seriesmatch.CleanTitle(name), " ", "-")
	if key == "" {
		key = "unknown"
	}
	return "hc-author:" + key
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value != "" {
			return value
		}
	}
	return ""
}

func maxFloat(a, b float64) float64 {
	if a > b {
		return a
	}
	return b
}
