package api

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/vavallee/bindery/internal/auth"
	"github.com/vavallee/bindery/internal/db"
	"github.com/vavallee/bindery/internal/decision"
	"github.com/vavallee/bindery/internal/httpsec"
	"github.com/vavallee/bindery/internal/indexer"
	"github.com/vavallee/bindery/internal/indexer/newznab"
	"github.com/vavallee/bindery/internal/models"
	"github.com/vavallee/bindery/internal/telemetry"
)

// lastDebugStore holds each caller's most recent SearchBook audit trail. The
// debug panel surfaces "what happened on my last search", not a history.
// Handler calls come from different goroutines, so access is mutex-guarded.
type lastDebugStore struct {
	mu     sync.RWMutex
	byUser map[int64]*indexer.SearchDebug
}

func (s *lastDebugStore) set(userID int64, d *indexer.SearchDebug) {
	s.mu.Lock()
	if s.byUser == nil {
		s.byUser = make(map[int64]*indexer.SearchDebug)
	}
	s.byUser[userID] = d
	s.mu.Unlock()
}

func (s *lastDebugStore) get(userID int64) *indexer.SearchDebug {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.byUser[userID]
}

// indexerSearcher is the subset of indexer.Searcher used by IndexerHandler.
// It is an interface so tests can inject a mock.
type indexerSearcher interface {
	SearchBookWithDebug(ctx context.Context, indexers []models.Indexer, c indexer.MatchCriteria) ([]newznab.SearchResult, *indexer.SearchDebug)
	SearchQuery(ctx context.Context, indexers []models.Indexer, query string) []newznab.SearchResult
}

type IndexerHandler struct {
	indexers  *db.IndexerRepo
	books     *db.BookRepo
	authors   *db.AuthorRepo
	aliases   *db.AuthorAliasRepo
	profiles  *db.MetadataProfileRepo
	searcher  indexerSearcher
	settings  *db.SettingsRepo
	blocklist *db.BlocklistRepo
	// qualityProfiles is optional; when set, the author's allowed-formats list
	// annotates interactive search results (#1693).
	qualityProfiles *db.QualityProfileRepo
	// editions is optional; when set, the book's edition ISBNs feed the
	// ISBN exact-match bonus in the ranker (#1724).
	editions  *db.EditionRepo
	lastDebug *lastDebugStore
}

func NewIndexerHandler(indexers *db.IndexerRepo, books *db.BookRepo, authors *db.AuthorRepo, profiles *db.MetadataProfileRepo, searcher indexerSearcher, settings *db.SettingsRepo, blocklist *db.BlocklistRepo) *IndexerHandler {
	return &IndexerHandler{
		indexers: indexers, books: books, authors: authors, profiles: profiles,
		searcher: searcher, settings: settings, blocklist: blocklist,
		lastDebug: &lastDebugStore{},
	}
}

// WithAliases attaches the author alias repo used to populate AuthorAliases in MatchCriteria.
func (h *IndexerHandler) WithAliases(aliases *db.AuthorAliasRepo) *IndexerHandler {
	h.aliases = aliases
	return h
}

// WithQualityProfiles attaches the quality profile repo so interactive search
// annotates releases the author's profile disallows (#1693).
//
// Unlike the scheduler, this path only LABELS: every result is still returned,
// carrying approved=false and the rejection reason, so the user can see why a
// release is out of policy and still grab it deliberately. Silently hiding it
// would be worse than the bug being fixed. The scheduler enforces the same
// spec for real, because auto-grab has no human in the loop.
func (h *IndexerHandler) WithQualityProfiles(qp *db.QualityProfileRepo) *IndexerHandler {
	h.qualityProfiles = qp
	return h
}

// WithEditions attaches the edition repo so interactive search can populate
// MatchCriteria.ISBN from the book's editions (#1724).
func (h *IndexerHandler) WithEditions(editions *db.EditionRepo) *IndexerHandler {
	h.editions = editions
	return h
}

// indexerResponses shapes a list of indexers for the wire. See indexerResponse.
func indexerResponses(idxs []models.Indexer) []models.Indexer {
	out := make([]models.Indexer, 0, len(idxs))
	for _, idx := range idxs {
		out = append(out, indexerResponse(idx))
	}
	return out
}

// indexerQueryWindow must match indexer.quotaWindow: the number the UI renders
// against the cap has to be summed over the same period the searcher enforces
// it over, or the tab would say 400 of 1000 about an indexer that is refusing
// searches.
const indexerQueryWindow = 24 * time.Hour

// withQueryUsage fills the response-only DailyQueriesUsed on every indexer that
// has a cap set, so the Indexers tab can show how much of it is spent (#2312).
//
// The stored counts lag the live tally by up to one flush interval, so this is a
// display figure and not the value the cap is enforced against. A failed read
// leaves the field nil, which renders as "no usage known" rather than failing
// the whole list request over a decoration.
func (h *IndexerHandler) withQueryUsage(ctx context.Context, idxs []models.Indexer) []models.Indexer {
	capped := false
	for _, idx := range idxs {
		if idx.DailyQueryLimit != nil && *idx.DailyQueryLimit > 0 {
			capped = true
			break
		}
	}
	if !capped {
		return idxs
	}
	usage, err := h.indexers.QueryUsage(ctx, time.Now().Add(-indexerQueryWindow))
	if err != nil {
		slog.Warn("failed to read indexer query usage", "error", err)
		return idxs
	}
	for i := range idxs {
		if idxs[i].DailyQueryLimit == nil || *idxs[i].DailyQueryLimit <= 0 {
			continue
		}
		used := usage[idxs[i].ID]
		idxs[i].DailyQueriesUsed = &used
	}
	return idxs
}

// withQueryUsageOne is withQueryUsage for a single row. Every response that
// carries an indexer goes through one of the two, including Create and Update:
// the web app splices the response straight back into its list state, so an
// indexer showing "950 of 1000, limit reached" would drop to "0 of 1000" the
// moment someone renamed it or flipped its enable toggle, while the searcher
// was still skipping it. That is exactly the looks-idle-but-is-not state the
// banner exists to prevent.
func (h *IndexerHandler) withQueryUsageOne(ctx context.Context, idx models.Indexer) models.Indexer {
	return h.withQueryUsage(ctx, []models.Indexer{idx})[0]
}

// validateDailyQueryLimit rejects a negative cap. Zero is allowed and means the
// same as unset — "no cap" — so a user who types 0 to clear the field gets what
// they expect rather than an indexer that can never be searched.
func validateDailyQueryLimit(idx models.Indexer) error {
	if idx.DailyQueryLimit != nil && *idx.DailyQueryLimit < 0 {
		return errors.New("dailyQueryLimit must be zero or greater")
	}
	return nil
}

// indexerResponse strips the stored API key and reports whether one is set.
// Indexer credentials are write-only over the API: the client needs to know
// that a key exists so it can render "leave blank to keep the existing key",
// but it never needs the value back. Mirrors importListResponse.
func indexerResponse(idx models.Indexer) models.Indexer {
	idx.APIKeyConfigured = idx.APIKey != ""
	idx.APIKey = ""
	return idx
}

// resolveWriteOnlyAPIKey decides what an update should do with a write-only
// API key field, given the raw request body and the stored value.
//
// An absent key and an explicitly sent empty string both mean "keep the stored
// value": the web app spreads the redacted object back into its payload, so a
// blank field must never be read as "wipe the credential". Clearing takes an
// explicit "clearApiKey": true. Mirrors the import-list patch semantics.
func resolveWriteOnlyAPIKey(raw map[string]json.RawMessage, stored string) (string, error) {
	clearKey := false
	if value, ok := raw["clearApiKey"]; ok {
		if err := json.Unmarshal(value, &clearKey); err != nil {
			return "", err
		}
	}
	submitted := ""
	if value, ok := raw["apiKey"]; ok {
		if err := json.Unmarshal(value, &submitted); err != nil {
			return "", err
		}
	}
	if clearKey && submitted != "" {
		return "", errors.New("apiKey and clearApiKey cannot both be set")
	}
	if clearKey {
		return "", nil
	}
	if submitted != "" {
		return submitted, nil
	}
	return stored, nil
}

func (h *IndexerHandler) List(w http.ResponseWriter, r *http.Request) {
	idxs, err := h.indexers.List(r.Context())
	if err != nil {
		writeServerError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, h.withQueryUsage(r.Context(), indexerResponses(idxs)))
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
	writeJSON(w, http.StatusOK, h.withQueryUsageOne(r.Context(), indexerResponse(*idx)))
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
	if err := httpsec.ValidateOutboundURL(idx.URL, httpsec.PolicyLANLoopback); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	if err := validateDailyQueryLimit(idx); err != nil {
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

	// A seed-ratio override supplied on a manually-created indexer is a user
	// choice (#1065). Manual indexers carry no Prowlarr ID so the syncer never
	// touches them, but recording the provenance keeps the field consistent.
	if idx.SeedRatio != nil {
		idx.SeedRatioSource = models.SeedRatioSourceUser
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
		writeServerError(w, r, err)
		return
	}
	telemetry.MarkFirst(r.Context(), h.settings, telemetry.SettingFirstIndexerAt)
	writeJSON(w, http.StatusCreated, h.withQueryUsageOne(r.Context(), indexerResponse(idx)))
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

	// Buffered rather than streamed because the body is read twice: once into
	// the struct and once as a raw key map for the write-only API key below.
	// MaxRequestBody bounds it.
	body, err := io.ReadAll(r.Body)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid request body"})
		return
	}

	// Decode over a copy of the stored row rather than a zero value: JSON
	// decoding only writes the keys the client actually sent, so an omitted
	// field keeps whatever is on disk instead of being reset. Previously any
	// client that did not know about a boolean — freeleechOnly, and now
	// includeParentCategories — silently turned it off on every save.
	// An explicitly sent false still disables it.
	idx := *existing
	if err := json.Unmarshal(body, &idx); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid request body"})
		return
	}
	// The API key is write-only, so it needs the raw body: the struct decode
	// above cannot tell an omitted key from an explicitly blank one, and the
	// web app sends the redacted (blank) value straight back on every save.
	raw := map[string]json.RawMessage{}
	if err := json.Unmarshal(body, &raw); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid request body"})
		return
	}
	apiKey, err := resolveWriteOnlyAPIKey(raw, existing.APIKey)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	idx.APIKey = apiKey
	if idx.URL != "" {
		if err := httpsec.ValidateOutboundURL(idx.URL, httpsec.PolicyLANLoopback); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
			return
		}
	}
	if err := validateDailyQueryLimit(idx); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	idx.ID = id
	// A user editing the indexer takes ownership of the seed-ratio override, so
	// the Prowlarr syncer (#1065) will not overwrite it on the next sync. This
	// holds even when the user clears the value to null ("no override") or sets
	// the -1 unlimited sentinel — the explicit choice sticks.
	idx.SeedRatioSource = models.SeedRatioSourceUser
	if err := h.indexers.Update(r.Context(), &idx); err != nil {
		writeServerError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, h.withQueryUsageOne(r.Context(), indexerResponse(idx)))
}

func (h *IndexerHandler) Delete(w http.ResponseWriter, r *http.Request) {
	id, ok := parseID(w, r)
	if !ok {
		return
	}
	if err := h.indexers.Delete(r.Context(), id); err != nil {
		writeServerError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// IndexerTestResponse summarizes a connectivity and search probe. The
// handler always returns HTTP 200 on a reachable-but-failed probe (e.g. 401
// from the upstream indexer) so the UI can render the specific error inline
// instead of a generic "request failed" toast.
type IndexerTestResponse struct {
	OK            bool   `json:"ok"`
	Status        int    `json:"status"`
	Categories    int    `json:"categories"`
	BookSearch    bool   `json:"bookSearch"`
	LatencyMs     int64  `json:"latencyMs"`
	SearchResults int    `json:"searchResults"`
	SearchError   string `json:"searchError,omitempty"`
	Message       string `json:"message,omitempty"`
	Error         string `json:"error,omitempty"`
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
	if idx.URL != "" {
		if err := httpsec.ValidateOutboundURL(idx.URL, httpsec.PolicyLANLoopback); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
			return
		}
	}

	client := newznab.New(idx.URL, idx.APIKey)
	probe := client.Probe(r.Context())
	resp := IndexerTestResponse{
		Status:        probe.Status,
		Categories:    probe.Categories,
		BookSearch:    probe.BookSearch,
		LatencyMs:     probe.LatencyMs,
		SearchResults: probe.SearchResults,
		SearchError:   probe.SearchError,
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

// TestConfig probes an indexer configuration supplied in the request body
// without persisting it. This backs the inline "Test" button on the Add/Edit
// forms so a user can verify the URL/API key before saving. The response
// shape mirrors Test (test-by-id) so the UI reuses one rendering path. Like
// Test, a reachable-but-failed probe returns HTTP 200 with the error inline.
func (h *IndexerHandler) TestConfig(w http.ResponseWriter, r *http.Request) {
	var idx models.Indexer
	if err := json.NewDecoder(r.Body).Decode(&idx); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid request body"})
		return
	}
	if idx.URL == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "url required"})
		return
	}
	if err := httpsec.ValidateOutboundURL(idx.URL, httpsec.PolicyLANLoopback); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}

	client := newznab.New(idx.URL, idx.APIKey)
	probe := client.Probe(r.Context())
	resp := IndexerTestResponse{
		Status:        probe.Status,
		Categories:    probe.Categories,
		BookSearch:    probe.BookSearch,
		LatencyMs:     probe.LatencyMs,
		SearchResults: probe.SearchResults,
		SearchError:   probe.SearchError,
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
	// Tier-1 cross-user IDOR guard (D1).
	if !auth.CheckOwnership(r.Context(), book.OwnerUserID) {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "book not found"})
		return
	}

	idxs, err := h.indexers.List(r.Context())
	if err != nil {
		writeServerError(w, r, err)
		return
	}

	// Resolve author name, metadata profile and aliases for better search results.
	authorName := ""
	var allowedLangs []string
	var authorAliases []string
	var qualityProfile *models.QualityProfile
	if author, err := h.authors.GetByID(r.Context(), book.AuthorID); err == nil && author != nil {
		authorName = author.Name
		allowedLangs = h.resolveAllowedLanguages(r.Context(), author)
		qualityProfile = db.ResolveAuthorQualityProfile(r.Context(), h.qualityProfiles, author)
		if h.aliases != nil {
			if aliases, err := h.aliases.ListByAuthor(r.Context(), author.ID); err == nil {
				for _, a := range aliases {
					authorAliases = append(authorAliases, a.Name)
				}
			}
		}
	}

	crit := indexer.MatchCriteria{
		Title:            book.Title,
		Author:           authorName,
		MediaType:        book.MediaType,
		ASIN:             book.ASIN,
		AllowedLanguages: allowedLangs,
		AuthorAliases:    authorAliases,
	}
	if book.ReleaseDate != nil {
		crit.Year = book.ReleaseDate.Year()
	}
	if h.editions != nil {
		if eds, err := h.editions.ListByBook(r.Context(), book.ID); err != nil {
			slog.Warn("failed to load editions for search ISBN", "book_id", book.ID, "error", err)
		} else {
			crit.ISBN = indexer.CriteriaISBN(book, eds)
		}
	}

	// For dual-format books (media_type='both'), run one search per format so
	// each uses its own category tree (7xxx for ebooks, 3xxx for audiobooks).
	// A single "both" search falls through to the ebook branch in
	// filterCategoriesForMedia, silently dropping all audiobook results.
	var results []newznab.SearchResult
	var dbg *indexer.SearchDebug
	if book.MediaType == models.MediaTypeBoth {
		ebookCrit := crit
		ebookCrit.MediaType = models.MediaTypeEbook
		audioCrit := crit
		audioCrit.MediaType = models.MediaTypeAudiobook

		type searchOut struct {
			results []newznab.SearchResult
			dbg     *indexer.SearchDebug
		}
		ctx := r.Context()
		ebookCh := make(chan searchOut, 1)
		audioCh := make(chan searchOut, 1)
		go func() {
			res, d := h.searcher.SearchBookWithDebug(ctx, idxs, ebookCrit)
			for i := range res {
				res[i].MediaType = "ebook"
			}
			ebookCh <- searchOut{res, d}
		}()
		go func() {
			res, d := h.searcher.SearchBookWithDebug(ctx, idxs, audioCrit)
			for i := range res {
				res[i].MediaType = "audiobook"
			}
			audioCh <- searchOut{res, d}
		}()
		ebookOut := <-ebookCh
		audioOut := <-audioCh
		ebookResults, ebookDbg := ebookOut.results, ebookOut.dbg
		audioResults, audioDbg := audioOut.results, audioOut.dbg
		results = append(ebookResults, audioResults...)
		results = indexer.DedupeResults(results)
		// Merge debug info from both searches.
		if ebookDbg != nil && audioDbg != nil {
			merged := *ebookDbg
			merged.Indexers = append(merged.Indexers, audioDbg.Indexers...)
			merged.Filters = append(merged.Filters, audioDbg.Filters...)
			merged.Pipeline.RawCount += audioDbg.Pipeline.RawCount
			merged.DurationMs += audioDbg.DurationMs
			dbg = &merged
		} else if audioDbg != nil {
			dbg = audioDbg
		} else {
			dbg = ebookDbg
		}
		// The merged panel describes one search over both category trees, so
		// the Query summary reports the book's media type. Whichever leg the
		// merge happened to start from, its per-format criteria would read as
		// "only ebooks were searched" (#1636) while the indexer rows below it
		// list both the 7xxx and 3xxx queries.
		if dbg != nil {
			dbg.Query.MediaType = book.MediaType
		}
	} else {
		results, dbg = h.searcher.SearchBookWithDebug(r.Context(), idxs, crit)
	}

	// Build decision specs.
	var specs []decision.Specification

	// Language filter: author profile takes precedence, fall back to global setting.
	beforeLang := len(results)
	filterDesc := ""
	if len(allowedLangs) > 0 {
		results = indexer.FilterByAllowedLanguages(results, allowedLangs)
		filterDesc = "allowed=" + strings.Join(allowedLangs, ",")
	} else if s, _ := h.settings.Get(r.Context(), "search.preferredLanguage"); s != nil {
		results = indexer.FilterByLanguage(results, s.Value)
		filterDesc = "filter=" + s.Value
	}
	if dbg != nil && beforeLang != len(results) {
		dbg.Filters = append(dbg.Filters, indexer.FilterDebug{
			Stage:  "language",
			Reason: "release name tagged with a language outside the profile (" + filterDesc + ")",
			Title:  "(" + strconv.Itoa(beforeLang-len(results)) + " result(s) dropped)",
		})
	}

	// Blocklist spec.
	if h.blocklist != nil {
		entries, _ := h.blocklist.List(r.Context())
		specs = append(specs, decision.NewBlocklistedSpec(entries))
	}

	// Already-imported spec.
	specs = append(specs, decision.AlreadyImportedSpec{})

	// Allowed-formats spec (#1693). Annotates only — see WithQualityProfiles.
	if qualityProfile != nil {
		specs = append(specs, decision.QualityAllowed{Profile: qualityProfile})
	}

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
		res := results[i]
		// Strip the indexer apikey the search path signs into the download URL
		// before it reaches the client. Interactive search is available to
		// non-admin users, so returning the signed URL leaks the shared indexer
		// credential; the grab handler re-signs from the indexer id server-side.
		res.NZBURL = newznab.RedactDownloadURL(res.NZBURL)
		out[i] = searchDecision{
			SearchResult: res,
			Approved:     d.Approved,
			Rejection:    d.Rejection,
		}
		if !d.Approved && dbg != nil {
			dbg.Filters = append(dbg.Filters, indexer.FilterDebug{
				Title:       results[i].Title,
				IndexerName: results[i].IndexerName,
				Stage:       "decision",
				Reason:      d.Rejection,
			})
		}
	}

	// Remember the most recent debug payload so the UI can re-fetch it
	// (e.g. after a page reload) without having to re-run the search.
	if dbg != nil {
		h.lastDebug.set(auth.UserIDFromContext(r.Context()), dbg)
	}

	writeJSON(w, http.StatusOK, map[string]interface{}{
		"results": out,
		"debug":   dbg,
	})
}

// LastSearchDebug returns the caller's most recent SearchBook audit trail, or
// 404 if that caller has not run a search yet. User ID 0 retains the shared
// behavior for API-key, disabled-auth, and local-only requests.
func (h *IndexerHandler) LastSearchDebug(w http.ResponseWriter, r *http.Request) {
	dbg := h.lastDebug.get(auth.UserIDFromContext(r.Context()))
	if dbg == nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "no search has run yet"})
		return
	}
	writeJSON(w, http.StatusOK, dbg)
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

// SearchQuery performs a freeform search across all indexers.
func (h *IndexerHandler) SearchQuery(w http.ResponseWriter, r *http.Request) {
	query := r.URL.Query().Get("q")
	if query == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "q parameter required"})
		return
	}

	idxs, err := h.indexers.List(r.Context())
	if err != nil {
		writeServerError(w, r, err)
		return
	}

	results := h.searcher.SearchQuery(r.Context(), idxs, query)
	// Strip the indexer apikey from each download URL before returning to the
	// client; the grab handler re-signs server-side (see SearchBook).
	for i := range results {
		results[i].NZBURL = newznab.RedactDownloadURL(results[i].NZBURL)
	}
	writeJSON(w, http.StatusOK, results)
}
