package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
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

// indexerSearcher is the subset of indexer.Searcher used by IndexerHandler.
// It is an interface so tests can inject a mock.
type indexerSearcher interface {
	SearchBookWithDebug(ctx context.Context, indexers []models.Indexer, c indexer.MatchCriteria) ([]newznab.SearchResult, *indexer.SearchDebug)
	SearchQuery(ctx context.Context, indexers []models.Indexer, query string) []newznab.SearchResult
	// Cooldown reports whether the searcher is holding off on idx after a
	// rate limit, and until when.
	Cooldown(idx models.Indexer) (until time.Time, reason string, held bool)
}

type IndexerHandler struct {
	indexers *db.IndexerRepo
	// clients validates a download client pin (#3080) on save. Optional: when
	// nil, any pin is refused rather than stored unchecked.
	clients            *db.DownloadClientRepo
	books              *db.BookRepo
	authors            *db.AuthorRepo
	aliases            *db.AuthorAliasRepo
	libraryContextWarn sync.Once
	series             *db.SeriesRepo // optional; the wanted book's series position for the relevance filter (#3268)
	profiles           *db.MetadataProfileRepo
	searcher           indexerSearcher
	settings           *db.SettingsRepo
	blocklist          *db.BlocklistRepo
	// qualityProfiles is optional; when set, the author's allowed-formats list
	// annotates interactive search results (#1693).
	qualityProfiles *db.QualityProfileRepo
	// editions is optional; when set, the book's edition ISBNs feed the
	// ISBN exact-match bonus in the ranker (#1724).
	editions *db.EditionRepo
	// lastDebug is the log behind GET /search/last-debug. The scheduler
	// writes its automatic searches to the same log (#2154); see
	// WithSearchDebugLog.
	lastDebug *indexer.DebugLog
	// searchResults records what the search endpoints return, so a grab from
	// a non-admin account can be held to it. See SearchResultRegistry.
	searchResults *SearchResultRegistry
}

func NewIndexerHandler(indexers *db.IndexerRepo, books *db.BookRepo, authors *db.AuthorRepo, profiles *db.MetadataProfileRepo, searcher indexerSearcher, settings *db.SettingsRepo, blocklist *db.BlocklistRepo) *IndexerHandler {
	return &IndexerHandler{
		indexers: indexers, books: books, authors: authors, profiles: profiles,
		searcher: searcher, settings: settings, blocklist: blocklist,
		lastDebug: indexer.NewDebugLog(),
	}
}

// WithSearchDebugLog replaces the handler's private debug log with one shared
// with the scheduler, so GET /search/last-debug also answers for scheduled,
// bulk and other automatic searches (#2154).
func (h *IndexerHandler) WithSearchDebugLog(l *indexer.DebugLog) *IndexerHandler {
	if l != nil {
		h.lastDebug = l
	}
	return h
}

// WithDownloadClients attaches the download client repo, which a per-indexer
// download client pin (#3080) is validated against on save.
func (h *IndexerHandler) WithDownloadClients(clients *db.DownloadClientRepo) *IndexerHandler {
	h.clients = clients
	return h
}

// WithSeries attaches the series repo so interactive search can give the
// relevance filter the wanted book's series position (#3268). Optional.
func (h *IndexerHandler) WithSeries(series *db.SeriesRepo) *IndexerHandler {
	h.series = series
	return h
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
	idxs = h.withCooldown(idxs)
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

// withCooldown fills the response-only CooldownUntil and CooldownReason on
// every indexer the searcher is holding off on, so a rate limit is visible on
// the Indexers tab rather than only in a search's details panel. The state
// lives in the searcher's memory, so it is exact and costs no query.
func (h *IndexerHandler) withCooldown(idxs []models.Indexer) []models.Indexer {
	if h.searcher == nil {
		return idxs
	}
	for i := range idxs {
		until, reason, held := h.searcher.Cooldown(idxs[i])
		if !held {
			continue
		}
		idxs[i].CooldownUntil = &until
		idxs[i].CooldownReason = &reason
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

// validateDownloadClientPin checks an indexer's download client pin (#3080) on
// save. The client must exist and serve the indexer's protocol. It must also be
// enabled, but only when the pin is new or changed: disabling a client is
// allowed, and an unrelated edit of an indexer pinned to a disabled client has
// to keep saving. A disabled pinned client fails its grabs instead.
func (h *IndexerHandler) validateDownloadClientPin(ctx context.Context, idx models.Indexer, stored *models.Indexer) error {
	if idx.DownloadClientID == nil {
		return nil
	}
	var c *models.DownloadClient
	if h.clients != nil {
		var err error
		if c, err = h.clients.GetByID(ctx, *idx.DownloadClientID); err != nil {
			return err
		}
	}
	if c == nil {
		return errPinInvalid(fmt.Sprintf("downloadClientId %d: that download client does not exist", *idx.DownloadClientID))
	}
	// A wrong protocol is refused only when this request sets the pin or the
	// indexer type. Otherwise an unrelated update, such as disabling the
	// indexer, would be blocked by a client changed behind the pin's back, and
	// the grab fails loudly at grab time anyway.
	pinChanged := stored == nil || stored.DownloadClientID == nil || *stored.DownloadClientID != *idx.DownloadClientID
	typeChanged := stored == nil || effectiveIndexerType(stored.Type) != effectiveIndexerType(idx.Type)
	if got := db.ClientProtocol(c.Type); got != idx.Protocol() && (pinChanged || typeChanged) {
		return errPinInvalid(fmt.Sprintf("download client %q is a %s client but this indexer is %s, so it cannot be pinned to it; pick a %s client",
			c.Name, got, idx.Protocol(), idx.Protocol()))
	}
	if pinChanged && !c.Enabled {
		return errPinInvalid(fmt.Sprintf("download client %q is disabled; enable it first or pick another", c.Name))
	}
	return nil
}

// effectiveIndexerType reads an empty type as the default, usenet, which is what
// Indexer.Protocol and the searcher already do, so an update body that sends an
// empty type is not a change of type for an indexer that is already usenet.
func effectiveIndexerType(t string) string {
	if t == "" {
		return "newznab"
	}
	return t
}

// pinInvalidError is a 400 about the download client pin, as opposed to a
// storage failure.
type pinInvalidError string

func (e pinInvalidError) Error() string { return string(e) }

func errPinInvalid(msg string) error { return pinInvalidError(msg) }

// writePinError answers a failed validateDownloadClientPin: a 400 for a bad
// pin, a generic 500 for a storage fault.
func writePinError(w http.ResponseWriter, r *http.Request, err error) {
	var bad pinInvalidError
	if errors.As(err, &bad) {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": bad.Error()})
		return
	}
	writeServerError(w, r, err)
}

// maxSeedTimeMinutes bounds the per-indexer seed time limits (#2206) at ten
// years. Nothing real needs more, and it keeps the value well inside the int32
// that qBittorrent parses it into.
const maxSeedTimeMinutes = 10 * 365 * 24 * 60

// validateSeedTimeLimits rejects a seed time limit below one minute or above
// maxSeedTimeMinutes. nil (the key omitted or sent as null) means "no
// override" and is always fine. Zero is rejected rather than read as "unset":
// qBittorrent would take it as "stop seeding the moment the download
// finishes", which on a private tracker is a hit and run, and the form clears
// the field by sending null.
func validateSeedTimeLimits(idx models.Indexer) error {
	check := func(name string, v *int) error {
		if v == nil {
			return nil
		}
		if *v < 1 {
			return fmt.Errorf("%s must be at least 1 minute; leave it empty to use the download client's own rule", name)
		}
		if *v > maxSeedTimeMinutes {
			return fmt.Errorf("%s must be at most %d minutes", name, maxSeedTimeMinutes)
		}
		return nil
	}
	if err := check("seedTimeMinutes", idx.SeedTimeMinutes); err != nil {
		return err
	}
	return check("inactiveSeedTimeMinutes", idx.InactiveSeedTimeMinutes)
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
	if err != nil {
		writeServerError(w, r, err)
		return
	}
	if idx == nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "indexer not found"})
		return
	}
	writeJSON(w, http.StatusOK, h.withQueryUsageOne(r.Context(), indexerResponse(*idx)))
}

func (h *IndexerHandler) Create(w http.ResponseWriter, r *http.Request) {
	var idx models.Indexer
	if !decodeJSON(w, r, &idx) {
		return
	}
	if idx.Name == "" || idx.URL == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "name and url required"})
		return
	}
	if err := httpsec.ValidateOutboundURL(idx.URL, httpsec.PolicyService); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	if err := validateDailyQueryLimit(idx); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	if err := validateSeedTimeLimits(idx); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	if idx.Type == "" {
		idx.Type = "newznab"
	}
	if err := h.validateDownloadClientPin(r.Context(), idx, nil); err != nil {
		writePinError(w, r, err)
		return
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
	// Same rule for the seed time (#2206). Anything a client sent in the
	// source field itself is ignored: provenance is the server's to record.
	if idx.SeedTimeMinutes != nil {
		idx.SeedTimeSource = models.SeedRatioSourceUser
	} else {
		idx.SeedTimeSource = models.SeedRatioSourceUnset
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
	if err != nil {
		writeServerError(w, r, err)
		return
	}
	if existing == nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "indexer not found"})
		return
	}

	// Buffered rather than streamed because the body is read twice: once into
	// the struct and once as a raw key map for the write-only API key below.
	// MaxRequestBody bounds it.
	body, err := io.ReadAll(r.Body)
	if err != nil {
		writeBodyReadError(w, err)
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
	// An explicit empty type means "keep the stored type": saved as empty it
	// would be read as usenet and flip a torznab indexer's protocol.
	if idx.Type == "" {
		idx.Type = existing.Type
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
		if err := httpsec.ValidateOutboundURL(idx.URL, httpsec.PolicyService); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
			return
		}
	}
	if err := validateDailyQueryLimit(idx); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	if err := validateSeedTimeLimits(idx); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	if err := h.validateDownloadClientPin(r.Context(), idx, existing); err != nil {
		writePinError(w, r, err)
		return
	}
	idx.ID = id
	// A user editing the indexer takes ownership of the seed-ratio override, so
	// the Prowlarr syncer (#1065) will not overwrite it on the next sync. This
	// holds even when the user clears the value to null ("no override") or sets
	// the -1 unlimited sentinel — the explicit choice sticks.
	idx.SeedRatioSource = models.SeedRatioSourceUser
	// The seed time (#2206) follows the same rule, so a value the user saw
	// auto-filled from Prowlarr and saved, or cleared, stays as they left it.
	idx.SeedTimeSource = models.SeedRatioSourceUser
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
	if err != nil {
		writeServerError(w, r, err)
		return
	}
	if idx == nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "indexer not found"})
		return
	}
	if idx.URL != "" {
		if err := httpsec.ValidateOutboundURL(idx.URL, httpsec.PolicyService); err != nil {
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
	if !decodeJSON(w, r, &idx) {
		return
	}
	if idx.URL == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "url required"})
		return
	}
	if err := httpsec.ValidateOutboundURL(idx.URL, httpsec.PolicyService); err != nil {
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
	if err != nil {
		writeServerError(w, r, err)
		return
	}
	if book == nil {
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
		Title:            indexer.SearchTitle(*book, allowedLangs),
		Author:           authorName,
		MediaType:        book.MediaType,
		ASIN:             book.ASIN,
		AllowedLanguages: allowedLangs,
		AuthorAliases:    authorAliases,
		// The searcher ranks by the profile's order (#2733); the same profile
		// builds the QualityAllowed annotation below, so the book page's
		// order and its approved flags come from one definition.
		Profile: qualityProfile,
		// The book's stored runtime lets the size term score an audio release
		// by its density instead of its total size; zero keeps the flat bonus
		// (#2740).
		DurationSeconds: book.DurationSeconds,
	}
	if book.ReleaseDate != nil {
		crit.Year = book.ReleaseDate.Year()
	}
	// What the library knows about the wanted book, for the relevance filter
	// (#3268). Two reads per search; the author's titles are scoped to the
	// caller like every other book listing.
	indexer.WarnLibraryContextMissing(&h.libraryContextWarn, "interactive search", h.series == nil, h.books == nil)
	var seriesRead indexer.SeriesReader
	if h.series != nil {
		seriesRead = h.series.GetPrimarySeriesForBook
	}
	var booksRead indexer.AuthorBooksReader
	if h.books != nil {
		uid := auth.UserIDFromContext(r.Context())
		booksRead = func(ctx context.Context, authorID int64) ([]models.Book, error) {
			return h.books.ListByAuthorAndUser(ctx, authorID, uid)
		}
	}
	crit.SetLibraryContext(r.Context(), *book, seriesRead, booksRead)
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
	// Releases the relevance stage dropped, kept whole for the "filtered"
	// section (#2334). Never mixed into results: only this handler reads them.
	var relDropped []indexer.DroppedRelease
	if book.MediaType == models.MediaTypeBoth {
		// Both legs copy crit, so each carries the profile and ranks by its
		// own list: ebooks by the ebook list, audiobooks by the audiobook list.
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
			stampDropped(d, "ebook")
			ebookCh <- searchOut{res, d}
		}()
		go func() {
			res, d := h.searcher.SearchBookWithDebug(ctx, idxs, audioCrit)
			for i := range res {
				res[i].MediaType = "audiobook"
			}
			stampDropped(d, "audiobook")
			audioCh <- searchOut{res, d}
		}()
		ebookOut := <-ebookCh
		audioOut := <-audioCh
		ebookResults, ebookDbg := ebookOut.results, ebookOut.dbg
		audioResults, audioDbg := audioOut.results, audioOut.dbg
		// The ebook block then the audiobook block, as it has always been.
		// What is new is that each block is ranked by its own list, and the
		// two legs' scores are not comparable (each list ranks n down to 1
		// over its own length), so concatenating rather than interleaving by
		// score is now load bearing rather than incidental.
		results = append(ebookResults, audioResults...)
		results = indexer.DedupeResults(results)
		if ebookDbg != nil {
			relDropped = append(relDropped, ebookDbg.Dropped...)
		}
		if audioDbg != nil {
			relDropped = append(relDropped, audioDbg.Dropped...)
		}
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
		if dbg != nil {
			relDropped = dbg.Dropped
		}
	}
	if dbg != nil {
		// The debug log keeps this payload; it must not keep raw URLs.
		dbg.Dropped = nil
	}

	// Build decision specs.
	var specs []decision.Specification

	// Language filter: author profile takes precedence, fall back to global setting.
	beforeLang := len(results)
	filterDesc := ""
	var langDropped []newznab.SearchResult
	if len(allowedLangs) > 0 {
		results, langDropped = indexer.SplitByAllowedLanguages(results, allowedLangs)
		filterDesc = "allowed=" + strings.Join(allowedLangs, ",")
	} else if s, _ := h.settings.Get(r.Context(), "search.preferredLanguage"); s != nil {
		results, langDropped = indexer.SplitByLanguage(results, s.Value)
		filterDesc = "filter=" + s.Value
	}
	langReason := "release name tagged with a language outside the profile (" + filterDesc + ")"
	if dbg != nil && beforeLang != len(results) {
		dbg.Filters = append(dbg.Filters, indexer.FilterDebug{
			Stage:  indexer.StageLanguage,
			Reason: langReason,
			Title:  "(" + strconv.Itoa(beforeLang-len(results)) + " result(s) dropped)",
		})
	}
	// Releases the relevance and language stages removed, offered behind a
	// collapsed section so the user can still grab one deliberately (#2334).
	// Language drops come first: they are the right book in the wrong tag.
	filtered, filteredTotal := collectFiltered(results, langDropped, langReason, relDropped)

	// Blocklist spec.
	if h.blocklist != nil {
		entries, _ := h.blocklist.List(r.Context())
		specs = append(specs, decision.NewBlocklistedSpec(entries))
	}

	// Already-imported spec.
	specs = append(specs, decision.AlreadyImportedSpec{})

	// Allowed-formats spec (#1693). Annotates only — see WithQualityProfiles.
	// The media type decides which of the profile's two lists judges a release
	// (#2733). For a dual-format book this is "both", which the spec ignores in
	// favour of the per-result media type the two legs stamped above.
	if qualityProfile != nil {
		specs = append(specs, decision.QualityAllowed{Profile: qualityProfile, MediaType: book.MediaType})
	}

	dm := decision.New(specs...)
	releases := make([]decision.Release, len(results))
	for i, res := range results {
		releases[i] = decision.ReleaseFromSearchResult(res)
	}

	decisions := dm.Evaluate(releases, *book)

	out := make([]searchDecision, len(decisions))
	for i, d := range decisions {
		res := results[i]
		// Strip every credential (the indexer apikey the search path signs in,
		// a Jackett key, a tracker passkey) from the download URL, GUID and
		// detail link before they reach the client. Interactive search is
		// available to non-admin users; the grab takes the real URL from the
		// search result registry.
		redactSearchResult(&res)
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

	// Filtered releases are recorded first so the main results are the newest
	// entries and the last the registry's cap would evict. They go out
	// labelled not approved with the stage's reason; they are not run through
	// the decision specs, which judge a release that passed the filters.
	filteredOut := make([]searchDecision, len(filtered))
	rawFiltered := make([]newznab.SearchResult, len(filtered))
	for i, f := range filtered {
		rawFiltered[i] = f.Result
		res := f.Result
		redactSearchResult(&res)
		filteredOut[i] = searchDecision{SearchResult: res, Approved: false, Rejection: f.Reason}
	}
	h.searchResults.remember(rawFiltered)

	// Every result is grabbable, approved or not: a rejection only labels it.
	// The registry keeps the raw URL, before the redaction above, so a grab
	// never depends on the URL the client posts back.
	h.searchResults.remember(results)

	// Remember the most recent debug payload so the UI can re-fetch it
	// (e.g. after a page reload) without having to re-run the search.
	if dbg != nil {
		uid := auth.UserIDFromContext(r.Context())
		dbg.Origin = indexer.DebugOriginInteractive
		dbg.BookID = book.ID
		dbg.UserID = uid
		h.lastDebug.RecordInteractive(uid, dbg)
	}

	writeJSON(w, http.StatusOK, map[string]interface{}{
		"results": out,
		// filtered is the releases the relevance and language filters removed,
		// same item shape as results with approved=false and the filter's
		// reason as rejection, capped at maxFilteredReleases. filteredTotal is
		// the count before the cap.
		"filtered":      filteredOut,
		"filteredTotal": filteredTotal,
		"debug":         dbg,
	})
}

// searchDecision is one release in an interactive search response: the search
// result plus the verdict on it.
type searchDecision struct {
	newznab.SearchResult
	Approved  bool   `json:"approved"`
	Rejection string `json:"rejection,omitempty"`
}

// maxFilteredReleases bounds the "filtered" array of one interactive search,
// and with it the registry entries a search adds beyond its main results. A
// generic title can have hundreds of unrelated hits dropped by the relevance
// filter; a collapsed list of that size helps nobody, and remembering all of
// them would let one search push earlier searches out of the registry. The
// relevance drops are ranked best first, so the cap keeps the likeliest ones.
const maxFilteredReleases = 100

// stampDropped sets the media type on a dual-format leg's dropped releases, as
// the leg's main results are stamped.
func stampDropped(d *indexer.SearchDebug, mediaType string) {
	if d == nil {
		return
	}
	for i := range d.Dropped {
		d.Dropped[i].Result.MediaType = mediaType
	}
}

// collectFiltered gathers the language drops and the relevance drops into one
// list: de-duplicated by GUID, never repeating a release that is in the main
// results, GUID-less ones skipped (a grab needs a GUID), capped at
// maxFilteredReleases. It returns the capped list and the count before the cap.
func collectFiltered(kept, langDropped []newznab.SearchResult, langReason string, relDropped []indexer.DroppedRelease) ([]indexer.DroppedRelease, int) {
	// Keyed the way the registry and the UI key a release: the GUID with its
	// credentials stripped, so two releases differing only in credentials are
	// one release here too.
	seen := make(map[string]bool, len(kept))
	for _, r := range kept {
		seen[registryKey(r.GUID)] = true
	}
	var all []indexer.DroppedRelease
	add := func(d indexer.DroppedRelease) {
		if d.Result.GUID == "" || seen[registryKey(d.Result.GUID)] {
			return
		}
		seen[registryKey(d.Result.GUID)] = true
		all = append(all, d)
	}
	for _, r := range langDropped {
		add(indexer.DroppedRelease{Result: r, Stage: indexer.StageLanguage, Reason: langReason})
	}
	for _, d := range relDropped {
		add(d)
	}
	all = interleaveByMediaType(all)
	total := len(all)
	if total > maxFilteredReleases {
		all = all[:maxFilteredReleases]
	}
	return all, total
}

// interleaveByMediaType round robins releases across media types, keeping each
// type's own order, so the cap keeps the best of each format of a dual-format
// book rather than all of one format's. Without a media type (a single-format
// book) it is the identity.
func interleaveByMediaType(in []indexer.DroppedRelease) []indexer.DroppedRelease {
	var order []string
	groups := map[string][]indexer.DroppedRelease{}
	for _, d := range in {
		mt := d.Result.MediaType
		if _, ok := groups[mt]; !ok {
			order = append(order, mt)
		}
		groups[mt] = append(groups[mt], d)
	}
	if len(order) < 2 {
		return in
	}
	out := make([]indexer.DroppedRelease, 0, len(in))
	for i := 0; len(out) < len(in); i++ {
		for _, mt := range order {
			if i < len(groups[mt]) {
				out = append(out, groups[mt][i])
			}
		}
	}
	return out
}

// LastSearchDebug returns the newest search audit trail the caller may see,
// or 404 when there is none.
//
// That is the caller's own latest interactive search or the latest automatic
// search (scheduled, bulk, series fill and so on) of a book the caller may
// see, whichever ran last (#2154). A caller holding the install's API key, or
// one with no user identity at all, also sees every user's interactive
// search: the key is admin equivalent, and a script reading this endpoint
// after a user searched in the browser would otherwise get an older search
// from a different bucket. A signed in user never sees another user's
// interactive search (#1859).
//
// The payload's origin, bookId and outcome fields say which search it is, so
// an older or unrelated search cannot pass for the one just started.
func (h *IndexerHandler) LastSearchDebug(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	uid := auth.UserIDFromContext(ctx)
	seeAll := uid == 0 || auth.AuthedViaAPIKey(ctx)
	dbg := h.lastDebug.Latest(uid, seeAll, func(ownerUserID int64) bool {
		return auth.CheckOwnership(ctx, ownerUserID)
	})
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
	// Recorded raw, before the redaction below: see SearchBook.
	h.searchResults.remember(results)
	// Strip every credential before returning to the client (see SearchBook).
	for i := range results {
		redactSearchResult(&results[i])
	}
	writeJSON(w, http.StatusOK, results)
}
