package api

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/vavallee/bindery/internal/db"
	"github.com/vavallee/bindery/internal/metadata"
	"github.com/vavallee/bindery/internal/models"
)

var (
	errNoMetadataAggregator = errors.New("metadata aggregator not configured")
	errNoMetadataMatch      = errors.New("no exact-name match in metadata provider")
)

type AuthorHandler struct {
	authors  *db.AuthorRepo
	aliases  *db.AuthorAliasRepo
	books    *db.BookRepo
	series   *db.SeriesRepo
	meta     *metadata.Aggregator
	settings *db.SettingsRepo
	profiles *db.MetadataProfileRepo
	searcher BookSearcher
	finder   LibraryFinder
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

func (h *AuthorHandler) List(w http.ResponseWriter, r *http.Request) {
	authors, err := h.authors.List(r.Context())
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	if authors == nil {
		authors = []models.Author{}
	}
	for i := range authors {
		proxyAuthorImages(&authors[i])
	}
	writeJSON(w, http.StatusOK, authors)
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

	proxyAuthorImages(author)
	writeJSON(w, http.StatusOK, author)
}

func (h *AuthorHandler) Create(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ForeignID         string `json:"foreignAuthorId"`
		Name              string `json:"authorName"`
		QualityProfileID  *int64 `json:"qualityProfileId"`
		MetadataProfileID *int64 `json:"metadataProfileId"`
		RootFolderID      *int64 `json:"rootFolderId"`
		Monitored         bool   `json:"monitored"`
		SearchOnAdd       bool   `json:"searchOnAdd"`
		MediaType         string `json:"mediaType"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid request body"})
		return
	}
	if req.ForeignID == "" || req.Name == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "foreignAuthorId and authorName required"})
		return
	}

	// Check if already exists
	existing, _ := h.authors.GetByForeignID(r.Context(), req.ForeignID)
	if existing != nil {
		writeJSON(w, http.StatusConflict, map[string]string{"error": "author already exists"})
		return
	}

	// Alias dedupe: if the requested name (or foreign id) already resolves
	// to a canonical author via the alias table, surface the canonical row
	// to the client as a 409 with `canonicalAuthorId` so the UI can prompt
	// for merge instead of creating a duplicate.
	if h.aliases != nil {
		if existingID, _ := h.aliases.LookupByName(r.Context(), req.Name); existingID != nil {
			canonical, _ := h.authors.GetByID(r.Context(), *existingID)
			writeJSON(w, http.StatusConflict, map[string]any{
				"error":             "author name already resolves to an existing author — confirm merge",
				"canonicalAuthorId": *existingID,
				"canonicalAuthor":   canonical,
			})
			return
		}
	}

	// Fetch full author metadata
	author, err := h.meta.GetAuthor(r.Context(), req.ForeignID)
	if err != nil {
		slog.Warn("metadata lookup failed, using provided name", "error", err)
		author = &models.Author{
			ForeignID:        req.ForeignID,
			Name:             req.Name,
			SortName:         sortName(req.Name),
			MetadataProvider: "openlibrary",
		}
	}
	author.Monitored = req.Monitored
	author.QualityProfileID = req.QualityProfileID
	author.RootFolderID = req.RootFolderID
	// Default to the seeded "Standard" profile (id=1) so the language filter
	// has something to consult when the UI didn't send an explicit choice.
	// The client can opt out by sending a profile whose allowed_languages is
	// empty or "any".
	if req.MetadataProfileID != nil {
		author.MetadataProfileID = req.MetadataProfileID
	} else {
		def := models.DefaultMetadataProfileID
		author.MetadataProfileID = &def
	}

	if err := h.authors.Create(r.Context(), author); err != nil {
		slog.Error("create author failed", "foreign_id", req.ForeignID, "error", err)
		if strings.Contains(err.Error(), "UNIQUE constraint failed") {
			writeJSON(w, http.StatusConflict, map[string]string{"error": "author already exists"})
			return
		}
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}

	// Resolve effective media type for books created under this author:
	// explicit request value wins, else the global default.media_type
	// setting, else ebook (backwards compat).
	mediaType := req.MediaType
	if mediaType == "" {
		mediaType = h.resolveDefaultMediaType(r.Context())
	}

	// Fetch and store books for this author. Always populate the catalogue;
	// pass searchOnAdd so FetchAuthorBooks knows whether to also queue grabs.
	go h.FetchAuthorBooks(author, req.SearchOnAdd, mediaType)

	writeJSON(w, http.StatusCreated, author)
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

	var req struct {
		Monitored         *bool  `json:"monitored"`
		QualityProfileID  *int64 `json:"qualityProfileId"`
		MetadataProfileID *int64 `json:"metadataProfileId"`
		RootFolderID      *int64 `json:"rootFolderId"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid request body"})
		return
	}
	if req.Monitored != nil {
		author.Monitored = *req.Monitored
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

	if err := h.authors.Update(r.Context(), author); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, author)
}

func (h *AuthorHandler) Delete(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid id"})
		return
	}

	// Opt-in `?deleteFiles=true` sweeps every book's on-disk path after the
	// DB delete. We must collect the paths *before* deleting the author —
	// the FK cascade removes the book rows along with it, which would leave
	// us nothing to walk. Per-path errors are logged but don't abort the
	// response: the author is already gone, and a partial sweep is better
	// than rolling the whole thing back.
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
		if err := removeBookPath(p); err != nil {
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

	// Manual refresh always repopulates the catalogue but never auto-grabs —
	// the user triggered it to refresh metadata, not to queue downloads.
	// Newly-discovered books inherit the global default media type; rows
	// that already exist keep whatever value they were created with.
	go h.FetchAuthorBooks(author, false, h.resolveDefaultMediaType(r.Context()))
	writeJSON(w, http.StatusAccepted, map[string]string{"message": "refresh started"})
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
	if full.Description != "" {
		author.Description = full.Description
	}
	if full.SortName != "" {
		author.SortName = full.SortName
	}
	if full.Disambiguation != "" {
		author.Disambiguation = full.Disambiguation
	}
	if err := h.authors.Update(ctx, author); err != nil {
		return err
	}
	slog.Info("relinked calibre author to metadata provider",
		"author", author.Name, "newForeignId", author.ForeignID)
	return nil
}

// FetchAuthorBooks populates the author's catalogue from the metadata provider.
// mediaType is applied to each newly-created book when the provider didn't
// return one; pass an empty string to accept whatever the provider set.
func (h *AuthorHandler) FetchAuthorBooks(author *models.Author, autoSearch bool, mediaType string) {
	ctx := contextBackground()
	slog.Info("fetching books for author", "author", author.Name, "foreignId", author.ForeignID)

	// Calibre-imported authors carry a synthetic "calibre:author:N" foreign ID
	// that has no counterpart in OL/Hardcover — they come in with no image,
	// description, or real catalogue. Re-link them to the upstream metadata
	// provider by name so the first Refresh Metadata click pulls real data.
	// If the re-link fails (name not found, network error) we fall through and
	// keep the synthetic ID, matching the prior skip-silently behaviour.
	if strings.HasPrefix(author.ForeignID, "calibre:") {
		if err := h.relinkCalibreAuthor(ctx, author); err != nil {
			slog.Info("calibre author not re-linked to metadata provider", "author", author.Name, "reason", err)
			return
		}
	}

	// Use the dedicated author works endpoint for accurate results
	books, err := h.meta.GetAuthorWorks(ctx, author.ForeignID)
	if err != nil {
		slog.Error("failed to fetch books", "author", author.Name, "error", err)
		return
	}

	// Resolve the author's metadata profile (falling back to the seeded
	// default) and parse its allowed_languages CSV. Nil means "no filter".
	allowedLangs, unknownFail := h.resolveAllowedLanguages(ctx, author)

	// Track titles we've already added (case-insensitive) to avoid OL duplicates
	existingBooks, _ := h.books.ListByAuthor(ctx, author.ID)
	seenTitles := make(map[string]bool)
	for _, eb := range existingBooks {
		seenTitles[strings.ToLower(eb.Title)] = true
	}

	normalizedAuthor := strings.ToLower(strings.TrimSpace(author.Name))

	var added, skippedLang, skippedJunk int
	for _, b := range books {
		b.AuthorID = author.ID
		b.Monitored = author.Monitored
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
			slog.Debug("skipping non-allowed-language book", "title", b.Title, "language", b.Language, "allowed", allowedLangs)
			continue
		}

		// Skip if foreign ID already exists
		existing, _ := h.books.GetByForeignID(ctx, b.ForeignID)
		if existing != nil {
			continue
		}

		// Skip duplicate titles (OpenLibrary often has multiple works for the same book)
		if seenTitles[normalizedTitle] {
			continue
		}
		seenTitles[normalizedTitle] = true

		if err := h.books.Create(ctx, &b); err != nil {
			// A UNIQUE constraint on foreign_id means the book was already
			// created by a concurrent or earlier sync — treat as a benign skip.
			if strings.Contains(err.Error(), "UNIQUE constraint failed") {
				continue
			}
			slog.Warn("failed to create book", "title", b.Title, "error", err)
			continue
		}
		added++

		// Populate series membership for this book.
		for _, ref := range b.SeriesRefs {
			s := &models.Series{ForeignID: ref.ForeignID, Title: ref.Title}
			if err := h.series.CreateOrGet(ctx, s); err != nil {
				slog.Warn("failed to upsert series", "series", ref.Title, "error", err)
				continue
			}
			if err := h.series.LinkBook(ctx, s.ID, b.ID, ref.Position, ref.Primary); err != nil {
				slog.Warn("failed to link book to series", "book", b.Title, "series", ref.Title, "error", err)
			}
		}

		// Check if the user already owns this book before queuing a download.
		if h.finder != nil {
			if existingPath := h.finder.FindExisting(ctx, b.Title, author.Name); existingPath != "" {
				slog.Info("library: found existing file, skipping auto-search", "title", b.Title, "path", existingPath)
				_ = h.books.SetFilePath(ctx, b.ID, existingPath)
				continue // don't auto-search for a book we already have
			}
		}

		// Auto-search the freshly-added wanted book only when the per-add
		// flag AND the global auto-grab kill-switch both say yes.
		if autoSearch && h.searcher != nil && author.Monitored && h.isAutoGrabEnabled(ctx) {
			h.searcher.SearchAndGrabBook(ctx, b)
		}
	}
	slog.Info("author books synced", "author", author.Name, "added", added, "skipped_language", skippedLang, "skipped_junk", skippedJunk, "total", len(books))
}

// AddBook adds a single book to the wanted list by its metadata foreign ID.
// If the author is not yet in Bindery it is added as unmonitored and its
// books are fetched in the background; the endpoint then polls until the
// requested book appears and marks it monitored before responding.
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
	if req.ForeignBookID == "" || req.ForeignAuthorID == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "foreignBookId and foreignAuthorId required"})
		return
	}

	ctx := r.Context()

	// 1. Find or create the author (unmonitored if new so we don't auto-want all books).
	author, _ := h.authors.GetByForeignID(ctx, req.ForeignAuthorID)
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
		if err := h.authors.Create(ctx, fetched); err != nil {
			if !strings.Contains(err.Error(), "UNIQUE constraint failed") {
				writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
				return
			}
			// Race: another request created it between our check and insert.
			author, _ = h.authors.GetByForeignID(ctx, req.ForeignAuthorID)
		} else {
			author = fetched
			go h.FetchAuthorBooks(author, false, h.resolveDefaultMediaType(ctx))
		}
	}
	if author == nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "could not resolve author"})
		return
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

	// 3. Mark the book monitored (wanted).
	book.Monitored = true
	if err := h.books.Update(ctx, book); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}

	// 4. Optionally trigger an indexer search.
	if req.SearchOnAdd && h.searcher != nil {
		go h.searcher.SearchAndGrabBook(contextBackground(), *book)
	}

	writeJSON(w, http.StatusCreated, book)
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
