package api

import (
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
	books    *db.BookRepo
	meta     *metadata.Aggregator
	settings *db.SettingsRepo
}

func NewAuthorHandler(authors *db.AuthorRepo, books *db.BookRepo, meta *metadata.Aggregator, settings *db.SettingsRepo) *AuthorHandler {
	return &AuthorHandler{authors: authors, books: books, meta: meta, settings: settings}
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

	writeJSON(w, http.StatusOK, author)
}

func (h *AuthorHandler) Create(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ForeignID        string `json:"foreignAuthorId"`
		Name             string `json:"authorName"`
		QualityProfileID *int64 `json:"qualityProfileId"`
		RootFolderID     *int64 `json:"rootFolderId"`
		Monitored        bool   `json:"monitored"`
		SearchOnAdd      bool   `json:"searchOnAdd"`
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

	if err := h.authors.Create(r.Context(), author); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}

	// Fetch and store books for this author
	if req.SearchOnAdd {
		go h.FetchAuthorBooks(author)
	}

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
		Monitored        *bool  `json:"monitored"`
		QualityProfileID *int64 `json:"qualityProfileId"`
		RootFolderID     *int64 `json:"rootFolderId"`
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
	if err := h.authors.Delete(r.Context(), id); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
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

	go h.FetchAuthorBooks(author)
	writeJSON(w, http.StatusAccepted, map[string]string{"message": "refresh started"})
}

func (h *AuthorHandler) FetchAuthorBooks(author *models.Author) {
	ctx := contextBackground()
	slog.Info("fetching books for author", "author", author.Name, "foreignId", author.ForeignID)

	// Use the dedicated author works endpoint for accurate results
	books, err := h.meta.GetAuthorWorks(ctx, author.ForeignID)
	if err != nil {
		slog.Error("failed to fetch books", "author", author.Name, "error", err)
		return
	}

	// Read preferred language setting; default to "eng" (English only)
	preferredLang := "eng"
	if s, _ := h.settings.Get(ctx, "search.preferredLanguage"); s != nil {
		switch s.Value {
		case "any":
			preferredLang = "any"
		case "en":
			preferredLang = "eng"
		default:
			preferredLang = s.Value
		}
	}

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

		// Filter by language: skip books whose language is known and doesn't match.
		// Books with an empty language (data unavailable) are always kept.
		if preferredLang != "any" && b.Language != "" && b.Language != preferredLang {
			skippedLang++
			slog.Debug("skipping non-preferred-language book", "title", b.Title, "language", b.Language, "preferred", preferredLang)
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
	}
	slog.Info("author books synced", "author", author.Name, "added", added, "skipped_language", skippedLang, "skipped_junk", skippedJunk, "total", len(books))
}
