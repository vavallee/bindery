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
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/vavallee/bindery/internal/auth"
	"github.com/vavallee/bindery/internal/db"
	"github.com/vavallee/bindery/internal/metadata"
	"github.com/vavallee/bindery/internal/models"
	"github.com/vavallee/bindery/internal/textutil"
)

func canRelinkAuthorToUpstream(author *models.Author) bool {
	return models.CanReplaceAuthorIdentity(author)
}

func isAuthorIdentityConflict(err error) bool {
	return err != nil && (strings.Contains(err.Error(), "UNIQUE constraint failed") || errors.Is(err, db.ErrAuthorIdentifierConflict))
}

func (h *AuthorHandler) relinkExistingAuthorToUpstream(ctx context.Context, author, upstream *models.Author, requestedName string, monitored bool, monitorMode string, monitorLatestCount int, monitorNewItems string, qualityProfileID, metadataProfileID, rootFolderID, audiobookRootFolderID *int64) error {
	if author == nil || upstream == nil {
		return errors.New("author relink requires local and upstream authors")
	}
	snapshot := author.UpdatedAtRaw
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
	// The credit travels with an enriched photo or bio (#3287).
	models.MergeProfileField(&author.Description, &author.DescriptionSource, textutil.CleanDescription(upstream.Description), upstream.DescriptionSource)
	models.MergeProfileField(&author.ImageURL, &author.ImageSource, strings.TrimSpace(upstream.ImageURL), upstream.ImageSource)
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
		author.MetadataProvider = models.AuthorProviderFromForeignID(author.ForeignID)
	}
	applyAuthorCreateOptions(author, monitored, monitorMode, monitorLatestCount, qualityProfileID, metadataProfileID, rootFolderID, audiobookRootFolderID)
	author.MonitorNewItems = monitorNewItems
	if oldForeignID != "" {
		if err := h.authors.UpsertAuthorIdentifier(ctx, author.ID, oldForeignID); err != nil {
			return err
		}
	}
	now := time.Now().UTC()
	author.LastMetadataRefreshAt = &now
	if err := h.updateAuthorIfUnchanged(ctx, author, snapshot); err != nil {
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
		"canonicalAuthor":   proxiedAuthor(canonical),
	})
}

// writeExistingBookConflict is the AddBook counterpart of
// writeCanonicalAuthorConflict (#1227): 409 with the library row so the client
// can offer to open it instead of showing a generic failure.
//
// formatWidened is added to the body only when this very request widened the
// existing row to both formats (#3482), so the client can say the book is now
// tracked as both instead of only that it was already there.
func (h *AuthorHandler) writeExistingBookConflict(w http.ResponseWriter, existing *models.Book, formatWidened bool) {
	body := map[string]any{
		"error":          "book already in your library; change its format or monitoring from the book page",
		"existingBookId": existing.ID,
		"existingBook":   proxiedBook(existing),
	}
	if formatWidened {
		body["formatWidened"] = true
	}
	writeJSON(w, http.StatusConflict, body)
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
	authors, err := h.authors.ListByUser(ctx, auth.ListScopeUserID(ctx))
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
		// The alias table is not owner scoped, but an alias belongs to its
		// author, and so to that author's owner. Under tenancy another user's
		// author must not match: the caller would get that row back in a
		// conflict, or relink it in place.
		if author != nil && author.ID != excludeID && auth.CheckOwnership(ctx, author.OwnerUserID) {
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
		if author != nil && author.ID != excludeID && auth.CheckOwnership(ctx, author.OwnerUserID) {
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
	// Scope the lookup with ListScopeUserID (not UserIDFromContext), matching
	// Get and Delete: unscoped unless tenancy enforcement is on and the caller
	// is a non-admin. Passing the raw context user id here 404ed relinks for
	// any caller whose id differed from the author's owner even with
	// BINDERY_ENFORCE_TENANCY off, while every sibling endpoint let the same
	// caller read and delete that author (#2243). CheckOwnership below stays
	// the per-item authorisation gate, so the enforcement-on posture is
	// unchanged.
	author, err := h.authors.GetByIDForUser(r.Context(), id, auth.ListScopeUserID(r.Context()))
	if err != nil {
		writeServerError(w, r, err)
		return
	}
	if author == nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "author not found"})
		return
	}
	// Tier-1 cross-user IDOR guard (D1).
	if !auth.CheckWriteOwnership(r.Context(), author.OwnerUserID) {
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
	case errors.Is(err, errPrimaryProviderUnavailable):
		// 503 rather than the default 502: nothing upstream returned a bad
		// answer. The primary provider did not answer at all and we declined
		// to write a link to a fallback that would have stuck (#2271).
		// Retrying later is the correct action, which is what 503 says.
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{
			"error": "the primary metadata provider did not answer, so no link was written; try again shortly",
		})
		return
	default:
		writeUpstreamError(w, err)
		return
	}

	if upstream == nil || strings.TrimSpace(upstream.ForeignID) == "" {
		writeJSON(w, http.StatusConflict, map[string]string{"error": "no confident upstream author match found"})
		return
	}
	if canonical, ambiguous, err := h.findCanonicalAuthorMatchExcluding(r.Context(), author.ID, author.Name, upstream.Name); err != nil {
		writeServerError(w, r, err)
		return
	} else if ambiguous {
		writeJSON(w, http.StatusConflict, map[string]string{"error": "author name resolves ambiguously — merge manually"})
		return
	} else if canonical != nil && canonical.ID != author.ID {
		h.writeCanonicalAuthorConflict(w, canonical, "author name already resolves to an existing author — confirm merge")
		return
	}
	if existing, err := h.authors.GetByAnyForeignIDForUser(r.Context(), upstream.ForeignID, userID); err != nil {
		writeServerError(w, r, err)
		return
	} else if existing != nil && existing.ID != author.ID {
		h.writeCanonicalAuthorConflict(w, existing, "upstream author already exists locally")
		return
	}

	if err := h.relinkExistingAuthorToUpstream(r.Context(), author, upstream, author.Name, author.Monitored, author.MonitorMode, author.MonitorLatestCount, models.NormalizeAuthorMonitorNewItems(author.MonitorNewItems), author.QualityProfileID, author.MetadataProfileID, author.RootFolderID, author.AudiobookRootFolderID); err != nil {
		if isAuthorIdentityConflict(err) {
			writeJSON(w, http.StatusConflict, map[string]string{"error": "upstream author already exists locally"})
			return
		}
		if errors.Is(err, errAuthorRowChanged) {
			writeJSON(w, http.StatusConflict, map[string]string{"error": errAuthorChanged})
			return
		}
		writeServerError(w, r, err)
		return
	}

	cleanAuthorDescription(author)
	writeAuthor(w, http.StatusOK, author)
}

func (h *AuthorHandler) RelinkCandidates(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid id"})
		return
	}
	// Scoped like RelinkUpstream above: ListScopeUserID so the lookup honours
	// tenancy enforcement being off, with CheckOwnership as the per-item
	// authorisation gate when it is on (#2243).
	author, err := h.authors.GetByIDForUser(r.Context(), id, auth.ListScopeUserID(r.Context()))
	if err != nil {
		writeServerError(w, r, err)
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
		writeUpstreamError(w, err)
		return
	}
	if candidates == nil {
		candidates = []models.Author{}
	}
	identifiers, err := h.authors.ListAuthorIdentifiers(r.Context(), author.ID)
	if err != nil {
		writeServerError(w, r, err)
		return
	}
	// buildRelinkCandidates drops the current link and flags the author's
	// former ones instead of hiding them (#2688).
	writeJSON(w, http.StatusOK, buildRelinkCandidates(candidates, author.ForeignID, identifiers))
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
	snapshot := author.UpdatedAtRaw
	results, outcome, err := h.meta.SearchAuthorsWithOutcome(ctx, author.Name)
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
	if !outcome.SafeToBind(match.ForeignID) {
		slog.Warn("calibre relink skipped: primary metadata provider unavailable",
			"author", author.Name, "primary", outcome.Primary,
			"failed", outcome.FailureSummary(), "wouldHaveLinked", match.ForeignID)
		return errPrimaryProviderUnavailable
	}

	full, err := h.meta.GetAuthor(ctx, match.ForeignID)
	if err != nil {
		return err
	}
	if full == nil {
		return errNoMetadataMatch
	}

	author.ForeignID = full.ForeignID
	// The provider the new id belongs to, read the way catalogue fetches
	// route it. A blanket "openlibrary" mislabelled every dnb: and hc: link,
	// including the primary's own match on a Hardcover or DNB primary (#2332).
	author.MetadataProvider = models.AuthorProviderFromForeignID(full.ForeignID)
	models.MergeProfileField(&author.ImageURL, &author.ImageSource, strings.TrimSpace(full.ImageURL), full.ImageSource)
	models.MergeProfileField(&author.Description, &author.DescriptionSource, textutil.CleanDescription(full.Description), full.DescriptionSource)
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
	if err := h.updateAuthorIfUnchanged(ctx, author, snapshot); err != nil {
		return err
	}
	slog.Info("relinked calibre author to metadata provider",
		"author", author.Name, "newForeignId", author.ForeignID)
	return nil
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
	var outcome metadata.SearchOutcome
	for _, query := range queries {
		results, queryOutcome, err := h.meta.SearchAuthorsWithOutcome(ctx, query)
		if err != nil {
			return nil, err
		}
		// One tainted query taints the lookup: the queries are alternative
		// spellings of the same name, so a record the primary would have
		// returned for a later query is just as missing from the picture.
		if queryOutcome.PrimaryFailed {
			outcome = queryOutcome
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
	if !outcome.SafeToBind(match.ForeignID) {
		slog.Warn("author relink skipped: primary metadata provider unavailable",
			"author", name, "primary", outcome.Primary,
			"failed", outcome.FailureSummary(), "wouldHaveLinked", match.ForeignID)
		return nil, errPrimaryProviderUnavailable
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

// authorSearchQueries delegates to textutil, which owns this expansion. It and
// the three helpers it used were byte-identical to the copies in internal/abs
// (#1648).
func authorSearchQueries(name string) []string {
	return textutil.AuthorSearchQueries(name)
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
	return h.findLibraryAuthorByNameForUser(ctx, name, auth.ListScopeUserID(ctx))
}

// findLibraryAuthorByNameForUser is findLibraryAuthorByName over the authors
// userID can see (0 is every author), for a caller that narrows the scope.
func (h *AuthorHandler) findLibraryAuthorByNameForUser(ctx context.Context, name string, userID int64) *models.Author {
	want := metadata.CanonicalAuthorKey(name)
	if want == "" {
		return nil
	}
	authors, err := h.authors.ListByUser(ctx, userID)
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
