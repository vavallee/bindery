package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"strings"

	"github.com/vavallee/bindery/internal/db"
	"github.com/vavallee/bindery/internal/metadata"
	"github.com/vavallee/bindery/internal/models"
	"github.com/vavallee/bindery/internal/textutil"
)

func (h *AuthorHandler) AddBook(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ForeignBookID   string `json:"foreignBookId"`
		ForeignAuthorID string `json:"foreignAuthorId"`
		AuthorName      string `json:"authorName"`
		SearchOnAdd     bool   `json:"searchOnAdd"`
		// MediaType optionally forces ebook/audiobook/both for the added book
		// (#1397). Empty keeps the provider's media type, falling back to the
		// default.media_type setting — the pre-selector behaviour.
		MediaType string `json:"mediaType"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid request body"})
		return
	}
	res, err := h.addBookCore(r.Context(), addBookParams{
		ForeignBookID:   req.ForeignBookID,
		ForeignAuthorID: req.ForeignAuthorID,
		AuthorName:      req.AuthorName,
		SearchOnAdd:     req.SearchOnAdd,
		MediaType:       req.MediaType,
	})
	if err != nil {
		h.writeAddBookError(w, r, err)
		return
	}
	writeBook(w, http.StatusCreated, res.Book)
}

// logStrictMediaTypeBypass records an explicit add that the strict media-type
// policy would have excluded from a catalogue sync.
//
// Fires only when the policy is on AND pinned to one format, which is the
// condition under which fetchAuthorBooks narrows a dual-format work or skips a
// single-format one. A default of "both" clamps nothing, so there is nothing to
// bypass and nothing to say.
func (h *AuthorHandler) logStrictMediaTypeBypass(ctx context.Context, book *models.Book) {
	if !h.strictMediaTypeBypassed(ctx, book) {
		return
	}
	slog.Info("added a book the strict media-type default would have excluded from a catalogue sync; an explicit add is not vetoed by it",
		"title", book.Title, "bookMediaType", book.MediaType, "default", h.resolveDefaultMediaType(ctx))
}

// strictMediaTypeBypassed is logStrictMediaTypeBypass's condition, split out so
// the decision can be asserted directly rather than by capturing log output.
func (h *AuthorHandler) strictMediaTypeBypassed(ctx context.Context, book *models.Book) bool {
	if book == nil || !h.resolveDefaultMediaTypeStrict(ctx) {
		return false
	}
	def := h.resolveDefaultMediaType(ctx)
	// Only a single-format default clamps anything, so only that can be
	// bypassed. "both" narrows nothing and skips nothing.
	if def != models.MediaTypeEbook && def != models.MediaTypeAudiobook {
		return false
	}
	return book.MediaType != def
}

// cleanupOrphanIfNoBooks deletes the given author iff (a) the book add
// did not succeed (bookCreated is false) and (b) the author currently has
// zero books in the DB. Called via defer in AddBook so any path that
// returns without a successful book add — poll timeout, ctx cancel, 500
// from book.Update — rolls back the speculative author insert. Uses a
// background context so client-cancellation paths still run the cleanup.
//
// The "zero books" guard is what makes this safe even when the async
// FetchAuthorBooks goroutine has raced ahead and inserted some books for
// this author: in that case the user still gets an author with content,
// so we keep it. For DNB synthetic IDs (issue #667), the async fetch
// returns zero rows deterministically and this cleanup fires.
func (h *AuthorHandler) cleanupOrphanIfNoBooks(author *models.Author, bookCreated *bool) {
	if bookCreated != nil && *bookCreated {
		return
	}
	if author == nil || author.ID == 0 {
		return
	}
	ctx := h.bgCtx()
	books, err := h.books.ListByAuthor(ctx, author.ID)
	if err != nil {
		slog.Warn("AddBook: orphan-cleanup ListByAuthor failed",
			"authorId", author.ID, "error", err)
		return
	}
	if len(books) > 0 {
		return
	}
	if err := h.authors.Delete(ctx, author.ID); err != nil {
		slog.Warn("AddBook: orphan-cleanup Delete failed",
			"authorId", author.ID, "foreignId", author.ForeignID, "error", err)
		return
	}
	slog.Info("AddBook: cleaned up orphan author after failed add",
		"authorId", author.ID, "foreignId", author.ForeignID, "name", author.Name)
}

// resolveAuthorForBook looks up the foreign book by primary metadata
// provider, walks its editions for an ISBN, then asks the aggregator to find
// the same ISBN in any provider that exposes a real author ID. Returns nil
// when no ISBN is found or no provider can place the author. This is the
// fallback path for AddBook when the search result didn't carry a
// foreignAuthorId — currently the case for every DNB result.
//
// The returned outcome pools every ISBN lookup. The editions are alternative
// ways of asking for the same book, so a primary that failed on one of them
// taints the result whichever edition finally matched, as in the Goodreads
// importer. The caller must check SafeToBind on the resolved author before
// rewriting the request to its ids (#2612).
func (h *AuthorHandler) resolveAuthorForBook(ctx context.Context, foreignBookID string) (*models.Book, metadata.SearchOutcome, error) {
	var outcome metadata.SearchOutcome
	primaryBook, err := h.meta.GetBook(ctx, foreignBookID)
	if err != nil {
		return nil, outcome, fmt.Errorf("look up book metadata: %w", err)
	}
	if primaryBook == nil {
		return nil, outcome, nil
	}
	for _, ed := range primaryBook.Editions {
		var isbn string
		switch {
		case ed.ISBN13 != nil && *ed.ISBN13 != "":
			isbn = *ed.ISBN13
		case ed.ISBN10 != nil && *ed.ISBN10 != "":
			isbn = *ed.ISBN10
		}
		if isbn == "" {
			continue
		}
		resolved, isbnOutcome, err := h.meta.ResolveBookByISBNWithOutcome(ctx, isbn)
		if !outcome.PrimaryFailed {
			outcome = isbnOutcome
		}
		if err != nil {
			slog.Debug("resolveAuthorForBook: provider lookup failed", "isbn", isbn, "error", err)
			continue
		}
		if resolved != nil {
			return resolved, outcome, nil
		}
	}
	return nil, outcome, nil
}

// primaryProviderUnavailableMessage is the user facing reason for refusing a
// fallback provider's record because the primary did not answer (#2612).
func primaryProviderUnavailableMessage(primary string) string {
	return fmt.Sprintf("The primary metadata provider (%s) did not answer, so no record from another provider was used. Please try again once it responds.", primary)
}

// writePrimaryProviderUnavailable answers an Add Book or ISBN lookup refused
// because the primary metadata provider did not answer. 503 for the relink
// endpoint's reason: nothing upstream gave a bad answer, and retrying shortly
// is the correct action. The Add Book dialog shows the error text as it
// stands.
func writePrimaryProviderUnavailable(w http.ResponseWriter, primary string) {
	writeJSON(w, http.StatusServiceUnavailable, map[string]string{
		"error": primaryProviderUnavailableMessage(primary),
	})
}

// saveAlternateNames persists any latin-script OL alternate names from
// author.AlternateNames into the author_aliases table. This lets non-latin
// primary names (e.g. "村上春樹") be matched against latin-script release
// names (e.g. "Murakami") during indexer searches.
func (h *AuthorHandler) saveAlternateNames(ctx context.Context, author *models.Author) {
	if h.aliases == nil || len(author.AlternateNames) == 0 {
		return
	}
	// These latin-script aliases exist only so a non-latin primary name can be
	// reached by a latin release name (e.g. "Murakami" -> "村上春樹"). For a
	// latin-script author every alternate name is just another latin name, and
	// minting it as an alias would file unrelated real authors (a pen name, or a
	// co-author credit) under this one. textutil.LatinAliasBinds is the shared
	// rule: the same test the Calibre importer applies when it reads an
	// unattributed alias back, and the search path when it expands one (#2419).
	for _, name := range author.AlternateNames {
		if !textutil.LatinAliasBinds(author.Name, name) {
			continue
		}
		alias := &models.AuthorAlias{AuthorID: author.ID, Name: name}
		if err := h.aliases.Create(ctx, alias); err != nil {
			slog.Debug("saveAlternateNames: could not save alias", "name", name, "authorId", author.ID, "error", err)
		}
	}
}

// canUpgradeToBoth reports whether combining existingMediaType and
// incomingMediaType yields a dual-format upgrade. It returns true exactly when
// one side is "ebook" and the other is "audiobook" — the two formats are
// complementary, so the already-tracked row should become "both" rather than
// a second row being created (issue #442).
// directInsertTitleUsable rejects provider records that would create a
// nameless row. The catalogue sync blocks the same two shapes before insert;
// without the guard an empty title persists and renders destination folders
// like "Jared M. Diamond/Jared M. Diamond ()".
func (h *AuthorHandler) directInsertTitleUsable(title, authorName string) bool {
	title = strings.TrimSpace(title)
	if title == "" {
		return false
	}
	return !strings.EqualFold(title, strings.TrimSpace(authorName))
}

// directInsertSeriesConflict reports whether the dedup-key match found for a
// direct insert is a *different volume of the same series* as the requested
// work: the match's primary series title equals one of the requested work's
// series refs and both sequence numbers are present but unequal. When true the
// caller creates a distinct row rather than adopting the collapsed sibling. A
// blank sequence on either side, or no primary series on the match, is not a
// conflict — there is no positive evidence the two are different volumes, so the
// existing dedup/adopt behavior is preserved.
func (h *AuthorHandler) directInsertSeriesConflict(ctx context.Context, matchBookID int64, requested []models.SeriesRef) bool {
	if h.series == nil || len(requested) == 0 {
		return false
	}
	matchTitle, matchPos, err := h.series.GetPrimarySeriesForBook(ctx, matchBookID)
	if err != nil {
		return false
	}
	matchTitle = strings.TrimSpace(matchTitle)
	matchPos = strings.TrimSpace(matchPos)
	if matchTitle == "" || matchPos == "" {
		return false
	}
	for _, ref := range requested {
		name := strings.TrimSpace(ref.Title)
		pos := strings.TrimSpace(ref.Position)
		if name == "" || pos == "" {
			continue
		}
		if strings.EqualFold(name, matchTitle) && !seriesSequencesEqual(pos, matchPos) {
			return true
		}
	}
	return false
}

// seriesSequencesEqual compares two series sequence strings numerically when
// both parse ("1" == "1.0"), else by trimmed case-insensitive string compare.
func seriesSequencesEqual(a, b string) bool {
	a = strings.TrimSpace(a)
	b = strings.TrimSpace(b)
	if fa, errA := strconv.ParseFloat(a, 64); errA == nil {
		if fb, errB := strconv.ParseFloat(b, 64); errB == nil {
			return fa == fb
		}
	}
	return strings.EqualFold(a, b)
}

// adoptDirectInsertMatch points an existing title-equivalent row at the
// requested foreign id instead of inserting a duplicate, mirroring the
// catalogue sync's dedup arms: a Calibre stub is upgraded to the real
// provider id so the caller's poll finds it, and an ebook/audiobook pair
// collapses into one dual-format row. It reports whether the row's media type
// was widened to both and saved, which the 409 for a deduped add carries so the
// dialog can say so (#3482).
func (h *AuthorHandler) adoptDirectInsertMatch(ctx context.Context, match, primary *models.Book, foreignID string) (widened bool) {
	changed := false
	if strings.HasPrefix(match.ForeignID, "calibre:") {
		match.ForeignID = foreignID
		changed = true
	}
	if canUpgradeToBoth(match.MediaType, primary.MediaType) {
		match.MediaType = models.MediaTypeBoth
		changed = true
		widened = true
	}
	if match.Language == "" && primary.Language != "" {
		match.Language = primary.Language
		changed = true
	}
	if !changed {
		slog.Debug("AddBook: direct insert deduped against existing row",
			"foreignBookId", foreignID, "bookId", match.ID, "title", match.Title)
		return false
	}
	if err := h.books.Update(ctx, match); err != nil {
		slog.Warn("AddBook: failed to adopt existing row during direct insert",
			"foreignBookId", foreignID, "bookId", match.ID, "error", err)
		return false
	}
	// The matched row is already in the library, so its format belongs to the
	// user and hydration must not widen it (#2768).
	h.hydrateHardcoverEditions(ctx, match, nil, true)
	return widened
}

func canUpgradeToBoth(existingMediaType, incomingMediaType string) bool {
	switch {
	case existingMediaType == models.MediaTypeEbook && incomingMediaType == models.MediaTypeAudiobook:
		return true
	case existingMediaType == models.MediaTypeAudiobook && incomingMediaType == models.MediaTypeEbook:
		return true
	default:
		return false
	}
}

// attachProviderIdentifiers populates author.ProviderIdentifiers from the
// author_identifiers table.
//
// The field is transport-only: it exists so the metadata layer can resolve
// "which upstream author is this row actually linked to" without being handed a
// database. A relink moves the primary foreign id to the new provider and keeps
// the previous one as an identifier, so an author can legitimately hold several
// at once (#1734).
func (h *AuthorHandler) attachProviderIdentifiers(ctx context.Context, author *models.Author) {
	if author == nil || h.authors == nil || author.ID == 0 {
		return
	}
	ids, err := h.authors.ListAuthorIdentifiers(ctx, author.ID)
	if err != nil {
		slog.Debug("could not load author identifiers; supplements will match by name",
			"author", author.Name, "authorId", author.ID, "error", err)
		return
	}
	if len(ids) == 0 {
		return
	}
	out := make(map[string]string, len(ids))
	for _, id := range ids {
		if id.Provider == "" || id.ForeignID == "" {
			continue
		}
		// First one wins. Several ids for one provider means the row absorbed
		// more than one upstream identity, which is the merge #1734 describes;
		// picking either is better than the name query that caused it, and the
		// relink-upstream candidates flow is how it gets untangled properly.
		if _, seen := out[id.Provider]; !seen {
			out[id.Provider] = id.ForeignID
		}
	}
	author.ProviderIdentifiers = out
}

// recordBookIdentities attaches the provider ids a fetch knows a book by, so a
// later sync from any of those providers resolves this row instead of creating
// a second one (#1705).
//
// Best effort and deliberately non-destructive. A conflict means a different
// book row already claims that id, which happens when the catalogue holds two
// rows for what upstream considers one work: the pre-existing duplicates this
// change stops being created, plus genuine mistakes. Re-pointing the id would
// move a book's identity out from under whoever is looking at it, so the
// conflict is logged and both rows are left as they are.
func (h *AuthorHandler) recordBookIdentities(ctx context.Context, book *models.Book, ids ...string) {
	if book == nil || book.ID == 0 || h.books == nil {
		return
	}
	for _, id := range ids {
		if strings.TrimSpace(id) == "" {
			continue
		}
		if err := h.books.UpsertBookIdentifier(ctx, book.ID, id); err != nil {
			if errors.Is(err, db.ErrBookIdentifierConflict) {
				slog.Debug("book identifier already belongs to another book",
					"bookId", book.ID, "title", book.Title, "foreignId", id, "error", err)
				continue
			}
			slog.Warn("could not record book identifier",
				"bookId", book.ID, "title", book.Title, "foreignId", id, "error", err)
		}
	}
}

// moveHealedAliases hands created the ids it is known by that from still holds
// as aliases (#3792). from is a row an earlier release bound created's work to
// by title alone; the sync has just judged them different works and created
// created as its own row, and Create could not record ids another row holds.
// Without the move, a lookup by the work's hc: id would keep finding from.
// The row's own foreign id is never touched, and a row with a file is left
// alone entirely.
func (h *AuthorHandler) moveHealedAliases(ctx context.Context, from, created *models.Book) {
	// A row with a file keeps every id it holds. Taking one off it could
	// leave the user's book unresolvable by an id it was imported under, and
	// the new row finds itself by its own foreign id regardless.
	if from.EbookFilePath != "" || from.AudiobookFilePath != "" || from.FilePath != "" {
		return
	}
	for _, id := range []string{created.ForeignID, created.HardcoverForeignID} {
		id = strings.TrimSpace(id)
		if id == "" || id == from.ForeignID {
			continue
		}
		owner, err := h.books.GetBookIdentifier(ctx, id)
		if err != nil || owner == nil || owner.BookID != from.ID {
			continue
		}
		if err := h.books.DeleteBookIdentifier(ctx, from.ID, id); err != nil {
			slog.Warn("could not release a wrongly bound book identifier", "bookId", from.ID, "foreignId", id, "error", err)
			continue
		}
		if err := h.books.UpsertBookIdentifier(ctx, created.ID, id); err != nil {
			slog.Warn("could not record a released book identifier", "bookId", created.ID, "foreignId", id, "error", err)
			continue
		}
		slog.Info("moved a book identifier from a row it was wrongly bound to",
			"foreignId", id, "fromBookId", from.ID, "toBookId", created.ID, "title", created.Title)
	}
}

// resolveExistingBook finds the book row a fetched work already corresponds to,
// trying each id the fetch knows it by in turn (#1705).
//
// Order matters: the work's own foreign id is the identity it would be created
// with, so it is checked first and a hit there needs no further lookups. The
// supplemental provider id is the cross-provider link, and is what finds a row
// created from the other provider entirely.
func (h *AuthorHandler) resolveExistingBook(ctx context.Context, ids ...string) *models.Book {
	if h.books == nil {
		return nil
	}
	for _, id := range ids {
		if strings.TrimSpace(id) == "" {
			continue
		}
		existing, err := h.books.GetByAnyForeignID(ctx, id)
		if err != nil {
			slog.Debug("book identity lookup failed", "foreignId", id, "error", err)
			continue
		}
		if existing != nil {
			return existing
		}
	}
	return nil
}
