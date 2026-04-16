package api

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"strconv"
	"strings"

	"github.com/go-chi/chi/v5"

	"github.com/vavallee/bindery/internal/db"
	"github.com/vavallee/bindery/internal/metadata"
	"github.com/vavallee/bindery/internal/models"
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

	// Fetch and store books for this author. Always populate the catalogue;
	// pass searchOnAdd so FetchAuthorBooks knows whether to also queue grabs.
	go h.FetchAuthorBooks(author, req.SearchOnAdd)

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
	go h.FetchAuthorBooks(author, false)
	writeJSON(w, http.StatusAccepted, map[string]string{"message": "refresh started"})
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

func (h *AuthorHandler) FetchAuthorBooks(author *models.Author, autoSearch bool) {
	ctx := contextBackground()
	slog.Info("fetching books for author", "author", author.Name, "foreignId", author.ForeignID)

	// Calibre-imported authors carry a synthetic "calibre:author:N" foreign ID
	// that has no counterpart in OL/Hardcover. Attempting to fetch works for
	// them always produces a 404; skip silently.
	if strings.HasPrefix(author.ForeignID, "calibre:") {
		slog.Debug("skipping metadata fetch for calibre-native author", "author", author.Name, "foreignId", author.ForeignID)
		return
	}

	// Use the dedicated author works endpoint for accurate results
	books, err := h.meta.GetAuthorWorks(ctx, author.ForeignID)
	if err != nil {
		slog.Error("failed to fetch books", "author", author.Name, "error", err)
		return
	}

	// Resolve the author's metadata profile (falling back to the seeded
	// default) and parse its allowed_languages CSV. Nil means "no filter".
	allowedLangs := h.resolveAllowedLanguages(ctx, author)

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
		// Books with an empty language (data unavailable) are always kept so
		// an unclassified release doesn't get dropped by accident.
		if !models.IsLanguageAllowed(b.Language, allowedLangs) {
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

// resolveAllowedLanguages returns the parsed allowed-language set for an
// author's metadata profile. Authors without an explicit profile use the
// seeded "Standard" profile (id=1). If neither can be loaded we fall back to
// English-only so existing behaviour is preserved; returning nil here would
// silently disable the filter, which is the opposite of what users with a
// default install expect.
func (h *AuthorHandler) resolveAllowedLanguages(ctx context.Context, author *models.Author) []string {
	id := models.DefaultMetadataProfileID
	if author.MetadataProfileID != nil {
		id = *author.MetadataProfileID
	}
	p, err := h.profiles.GetByID(ctx, id)
	if err != nil || p == nil {
		return []string{"eng"}
	}
	return models.ParseAllowedLanguages(p.AllowedLanguages)
}
