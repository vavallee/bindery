package api

import (
	"context"
	"encoding/json"
	"net/http"

	"github.com/vavallee/bindery/internal/db"
	"github.com/vavallee/bindery/internal/decision"
	"github.com/vavallee/bindery/internal/httpsec"
	"github.com/vavallee/bindery/internal/indexer"
	"github.com/vavallee/bindery/internal/indexer/newznab"
	"github.com/vavallee/bindery/internal/models"
)

type IndexerHandler struct {
	indexers  *db.IndexerRepo
	books     *db.BookRepo
	authors   *db.AuthorRepo
	profiles  *db.MetadataProfileRepo
	searcher  *indexer.Searcher
	settings  *db.SettingsRepo
	blocklist *db.BlocklistRepo
}

func NewIndexerHandler(indexers *db.IndexerRepo, books *db.BookRepo, authors *db.AuthorRepo, profiles *db.MetadataProfileRepo, searcher *indexer.Searcher, settings *db.SettingsRepo, blocklist *db.BlocklistRepo) *IndexerHandler {
	return &IndexerHandler{indexers: indexers, books: books, authors: authors, profiles: profiles, searcher: searcher, settings: settings, blocklist: blocklist}
}

func (h *IndexerHandler) List(w http.ResponseWriter, r *http.Request) {
	idxs, err := h.indexers.List(r.Context())
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	if idxs == nil {
		idxs = []models.Indexer{}
	}
	writeJSON(w, http.StatusOK, idxs)
}

func (h *IndexerHandler) Get(w http.ResponseWriter, r *http.Request) {
	id, ok := parseID(w, r)
	if !ok {
		return
	}
	idx, err := h.indexers.GetByID(r.Context(), id)
	if err != nil || idx == nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "indexer not found"})
		return
	}
	writeJSON(w, http.StatusOK, idx)
}

func (h *IndexerHandler) Create(w http.ResponseWriter, r *http.Request) {
	var idx models.Indexer
	if err := json.NewDecoder(r.Body).Decode(&idx); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid request body"})
		return
	}
	if idx.Name == "" || idx.URL == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "name and url required"})
		return
	}
	if err := httpsec.ValidateOutboundURL(idx.URL, httpsec.PolicyLAN); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	if idx.Type == "" {
		idx.Type = "newznab"
	}
	if len(idx.Categories) == 0 {
		// Books (7000 parent, 7020 ebook) + Audio/Audiobook (3030).
		// The searcher filters per-media-type at query time.
		idx.Categories = []int{7000, 7020, 3030}
	}

	// Check for duplicate URL
	existing, _ := h.indexers.List(r.Context())
	for _, e := range existing {
		if e.URL == idx.URL {
			writeJSON(w, http.StatusConflict, map[string]string{"error": "indexer with this URL already exists"})
			return
		}
	}

	if err := h.indexers.Create(r.Context(), &idx); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusCreated, idx)
}

func (h *IndexerHandler) Update(w http.ResponseWriter, r *http.Request) {
	id, ok := parseID(w, r)
	if !ok {
		return
	}
	existing, err := h.indexers.GetByID(r.Context(), id)
	if err != nil || existing == nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "indexer not found"})
		return
	}

	var idx models.Indexer
	if err := json.NewDecoder(r.Body).Decode(&idx); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid request body"})
		return
	}
	if idx.URL != "" {
		if err := httpsec.ValidateOutboundURL(idx.URL, httpsec.PolicyLAN); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
			return
		}
	}
	idx.ID = id
	if err := h.indexers.Update(r.Context(), &idx); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, idx)
}

func (h *IndexerHandler) Delete(w http.ResponseWriter, r *http.Request) {
	id, ok := parseID(w, r)
	if !ok {
		return
	}
	if err := h.indexers.Delete(r.Context(), id); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// IndexerTestResponse summarizes a lightweight connectivity probe. The
// handler always returns HTTP 200 on a reachable-but-failed probe (e.g. 401
// from the upstream indexer) so the UI can render the specific error inline
// instead of a generic "request failed" toast.
type IndexerTestResponse struct {
	OK         bool   `json:"ok"`
	Status     int    `json:"status"`
	Categories int    `json:"categories"`
	BookSearch bool   `json:"bookSearch"`
	LatencyMs  int64  `json:"latencyMs"`
	Message    string `json:"message,omitempty"`
	Error      string `json:"error,omitempty"`
}

func (h *IndexerHandler) Test(w http.ResponseWriter, r *http.Request) {
	id, ok := parseID(w, r)
	if !ok {
		return
	}
	idx, err := h.indexers.GetByID(r.Context(), id)
	if err != nil || idx == nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "indexer not found"})
		return
	}

	client := newznab.New(idx.URL, idx.APIKey)
	probe := client.Probe(r.Context())
	resp := IndexerTestResponse{
		Status:     probe.Status,
		Categories: probe.Categories,
		BookSearch: probe.BookSearch,
		LatencyMs:  probe.LatencyMs,
	}
	if probe.Error != "" {
		resp.Error = probe.Error
		writeJSON(w, http.StatusOK, resp)
		return
	}
	resp.OK = true
	resp.Message = "ok"
	writeJSON(w, http.StatusOK, resp)
}

// SearchBook searches all enabled indexers for a specific book.
func (h *IndexerHandler) SearchBook(w http.ResponseWriter, r *http.Request) {
	bookID, ok := parseID(w, r)
	if !ok {
		return
	}
	book, err := h.books.GetByID(r.Context(), bookID)
	if err != nil || book == nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "book not found"})
		return
	}

	idxs, err := h.indexers.List(r.Context())
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}

	// Resolve author name and metadata profile for better search results.
	authorName := ""
	var allowedLangs []string
	if author, err := h.authors.GetByID(r.Context(), book.AuthorID); err == nil && author != nil {
		authorName = author.Name
		allowedLangs = h.resolveAllowedLanguages(r.Context(), author)
	}

	crit := indexer.MatchCriteria{
		Title:            book.Title,
		Author:           authorName,
		MediaType:        book.MediaType,
		ASIN:             book.ASIN,
		AllowedLanguages: allowedLangs,
	}
	if book.ReleaseDate != nil {
		crit.Year = book.ReleaseDate.Year()
	}

	// For dual-format books (media_type='both'), run one search per format so
	// each uses its own category tree (7xxx for ebooks, 3xxx for audiobooks).
	// A single "both" search falls through to the ebook branch in
	// filterCategoriesForMedia, silently dropping all audiobook results.
	var results []newznab.SearchResult
	if book.MediaType == models.MediaTypeBoth {
		ebookCrit := crit
		ebookCrit.MediaType = models.MediaTypeEbook
		audioCrit := crit
		audioCrit.MediaType = models.MediaTypeAudiobook
		results = h.searcher.SearchBook(r.Context(), idxs, ebookCrit)
		results = append(results, h.searcher.SearchBook(r.Context(), idxs, audioCrit)...)
		results = indexer.DedupeResults(results)
	} else {
		results = h.searcher.SearchBook(r.Context(), idxs, crit)
	}

	// Build decision specs.
	var specs []decision.Specification

	// Language filter: author profile takes precedence, fall back to global setting.
	lang := langFilterFromAllowed(allowedLangs)
	if lang == "" {
		if s, _ := h.settings.Get(r.Context(), "search.preferredLanguage"); s != nil {
			lang = s.Value
		}
	}
	results = indexer.FilterByLanguage(results, lang)

	// Blocklist spec.
	if h.blocklist != nil {
		entries, _ := h.blocklist.List(r.Context())
		specs = append(specs, decision.NewBlocklistedSpec(entries))
	}

	// Already-imported spec.
	specs = append(specs, decision.AlreadyImportedSpec{})

	dm := decision.New(specs...)
	releases := make([]decision.Release, len(results))
	for i, res := range results {
		releases[i] = decision.ReleaseFromSearchResult(res)
	}

	decisions := dm.Evaluate(releases, *book)

	type searchDecision struct {
		newznab.SearchResult
		Approved  bool   `json:"approved"`
		Rejection string `json:"rejection,omitempty"`
	}
	out := make([]searchDecision, len(decisions))
	for i, d := range decisions {
		out[i] = searchDecision{
			SearchResult: results[i],
			Approved:     d.Approved,
			Rejection:    d.Rejection,
		}
	}

	writeJSON(w, http.StatusOK, out)
}

// resolveAllowedLanguages returns the parsed allowed-language list for an
// author's metadata profile. Returns empty (no filter) when the profile
// cannot be loaded — imposing English-only as a fallback silently breaks
// users whose indexers return language-tagged releases.
func (h *IndexerHandler) resolveAllowedLanguages(ctx context.Context, author *models.Author) []string {
	if h.profiles == nil {
		return []string{}
	}
	id := models.DefaultMetadataProfileID
	if author.MetadataProfileID != nil {
		id = *author.MetadataProfileID
	}
	p, err := h.profiles.GetByID(ctx, id)
	if err != nil || p == nil {
		return []string{}
	}
	return models.ParseAllowedLanguages(p.AllowedLanguages)
}

// langFilterFromAllowed converts an AllowedLanguages slice to the single-lang
// token expected by indexer.FilterByLanguage. Returns "en" only when the
// profile is English-exclusive (so the foreign-tag filter is active).
// Returns "" for multi-language or empty profiles (filter is skipped).
func langFilterFromAllowed(langs []string) string {
	if len(langs) == 1 && (langs[0] == "en" || langs[0] == "eng") {
		return "en"
	}
	return ""
}

// SearchQuery performs a freeform search across all indexers.
func (h *IndexerHandler) SearchQuery(w http.ResponseWriter, r *http.Request) {
	query := r.URL.Query().Get("q")
	if query == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "q parameter required"})
		return
	}

	idxs, err := h.indexers.List(r.Context())
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}

	results := h.searcher.SearchQuery(r.Context(), idxs, query)
	writeJSON(w, http.StatusOK, results)
}
