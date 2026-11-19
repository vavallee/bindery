package api

import (
	"context"
	"database/sql"
	"errors"
	"log/slog"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/vavallee/bindery/internal/auth"
	"github.com/vavallee/bindery/internal/concurrency"
	"github.com/vavallee/bindery/internal/db"
	"github.com/vavallee/bindery/internal/importer"
	"github.com/vavallee/bindery/internal/indexer"
	"github.com/vavallee/bindery/internal/models"
	"github.com/vavallee/bindery/internal/textutil"
)

// catalogueSyncOptions configures one catalogue sync run (fetchAuthorBooks).
// A struct rather than a widening parameter list: the flags interact — see
// authorAcceptsDiscoveredBooks — and every call site should read as a sentence
// about intent, not as four positional booleans.
type catalogueSyncOptions struct {
	// autoSearch queues an indexer search for each newly-created monitored
	// book. Only the add flow sets it; refresh paths never auto-grab.
	autoSearch bool
	// mediaType is applied to created books the provider gave no format for.
	// Empty accepts whatever the provider set.
	mediaType string
	// discovery marks a REFRESH-path sync (single-author Refresh, bulk
	// refresh, refresh-all, relink) as opposed to the initial add/migrate
	// sync. Refresh runs may only create books the library doesn't have when
	// the author's monitoring policy says it wants new work from them
	// (authorAcceptsDiscoveredBooks) — see #1348, #1815.
	discovery bool
	// onlyForeignID restricts the run to a single provider work. Set by
	// AddBook's fallback so "add this one book" can never turn into "import
	// this author's back catalogue" (#1816). Everything else the provider
	// returns is dropped before the create loop sees it.
	//
	// A run with it set is an explicit user pick standing in for the direct
	// insert, so it also skips the author-wide side effects (profile refresh,
	// Calibre re-link, sync summary) and the catalogue heuristics that may veto
	// a work — the strict media-type clamp and the language filter (#1612).
	onlyForeignID string

	// refreshFromProvider marks the manual per author Refresh Metadata action.
	// The author's profile, catalogue and Audible lookups skip the 24 hour
	// metadata cache (metadata.WithCacheBypass) so the user sees what the
	// provider says now, not what it said yesterday (#2601). Bulk refresh,
	// Refresh all, relink and the add flows leave it off: they span many
	// authors, and the cache is what keeps a repeat run from refetching the
	// whole library.
	refreshFromProvider bool

	// enrichProfile asks the author enrichers (#3287) during the profile
	// refresh, whatever the author's enrichment stamp says. Only the two
	// single author actions a user takes set it, adding the author and its
	// Refresh button, so a new author gets its photo and bio straight away.
	// Bulk refresh, Refresh all and scheduled discovery leave it off but
	// still enrich an author that is due, within syncEnrichmentsPerHour
	// (#3398); the imports never ask.
	enrichProfile bool

	// ownerForUnownedAuthor owns the books this run creates when the author
	// has no owner. Add Book's single work fallback sets it to the caller
	// under tenancy, so a book picked under a shared author belongs to the
	// user who picked it, as the direct insert's does. Zero keeps the
	// author's owner, which for a shared author is none.
	ownerForUnownedAuthor int64

	// syncClaimed marks a run its caller already counted in runningSyncs.
	// The manual Refresh claims the author before it answers, so a second
	// click sees the first; fetchAuthorBooks counts every other run itself.
	syncClaimed bool

	// deferCoverEnrichment marks a scheduled discovery run (#2236). The works
	// lookup skips its per work cover enrichment, and the sync enriches only
	// the works the author does not already have. Existing coverless books
	// lose the cover backfill on these runs (#1748) to save the provider
	// calls; a manual refresh still fills them.
	deferCoverEnrichment bool
}

func (h *AuthorHandler) fetchAuthorBooksAsync(author *models.Author, opts catalogueSyncOptions) {
	if author == nil {
		return
	}
	snapshot := *author
	// With a jobs group wired the sync runs on the shutdown-scoped context, so
	// SIGTERM cancels and drains it before the DB closes rather than cutting it
	// off mid-write. Fall back to an untracked goroutine for tests and
	// non-wired callers, the same shape StartScan uses (#1458, #2371).
	if h.jobs != nil {
		if !h.jobs.Go("author-catalogue-sync", func(ctx context.Context) {
			h.fetchAuthorBooks(ctx, &snapshot, opts)
		}) {
			// Go is a documented no-op once the group is shutting down. The
			// only state to roll back is a Refresh's running mark, and the drop
			// is worth a line: the author was created and its catalogue was not.
			if opts.syncClaimed {
				h.runningSyncs.done(snapshot.ID)
			}
			slog.Warn("author catalogue sync not started: server is shutting down",
				"author", snapshot.Name, "foreignId", snapshot.ForeignID)
		}
		return
	}
	go h.fetchAuthorBooks(h.bgCtx(), &snapshot, opts)
}

// refreshAuthorProfile re-fetches the author's profile fields (bio, photo,
// disambiguation, ratings) from the linked metadata provider and persists any
// the provider now supplies. The manual "Refresh Metadata" action previously
// only repopulated the catalogue, leaving the author's own Description and
// ImageURL empty even after the user added them upstream (Discussion #1226).
//
// Best-effort: a missing aggregator, a provider miss, or a save error is logged
// and swallowed — the catalogue sync that follows must still run. Identity
// (ForeignID/Name/MetadataProvider) and user-controlled monitoring fields are
// never touched here; only the read-only profile fields are merged.
func (h *AuthorHandler) refreshAuthorProfile(ctx context.Context, author *models.Author, enrich bool) {
	if h.meta == nil || author == nil {
		return
	}
	// The row as read before the provider call. The write below is guarded
	// on it, so a photo or bio saved while the call is in flight wins.
	snapshot := author.UpdatedAtRaw
	upstream, err := h.meta.GetAuthor(ctx, author.ForeignID)
	if err != nil {
		slog.Warn("author profile refresh: metadata lookup failed",
			"author", author.Name, "foreignId", author.ForeignID, "error", err)
		return
	}
	if upstream == nil {
		return
	}
	// enrich is set by the caller (#3287): always for an add or a Refresh
	// click, whose repeat the enrichers' own cache keeps off the network,
	// and for the other refresh-path syncs once the stamp has aged. A full
	// answer is stamped, so the author is not asked again for a month.
	enrichAnswered := enrich && h.meta.EnrichAuthor(ctx, upstream)
	defer func() {
		if enrichAnswered {
			if err := h.authors.StampEnrichment(ctx, author.ID, time.Now()); err != nil {
				slog.Warn("author profile refresh: could not record enrichment", "author", author.Name, "error", err)
			}
		}
	}()
	now := time.Now().UTC()
	if !mergeAuthorProfileFields(author, upstream) {
		// Nothing new, so skip the whole row write, but the profile was
		// checked: stamp freshness with a single column write so the
		// staggered background refresh does not fetch it again today (#2595).
		// A failure is only logged, the stamp is an optimisation.
		if err := h.authors.StampMetadataRefresh(ctx, author.ID, now); err != nil {
			slog.Warn("author profile refresh: could not record freshness", "author", author.Name, "error", err)
			return
		}
		author.LastMetadataRefreshAt = &now
		return
	}
	author.LastMetadataRefreshAt = &now
	// Dropped rather than retried when the row moved, as the scheduler does
	// (persistRefreshedAuthor): the next Refresh or scheduled pass picks the
	// profile up again.
	written, err := h.authors.UpdateIfUnchanged(ctx, author, snapshot)
	if err != nil || !written {
		if err != nil {
			slog.Warn("author profile refresh: save failed", "author", author.Name, "error", err)
		} else {
			slog.Info("author profile refresh skipped: the author changed while its profile was being fetched",
				"author", author.Name, "authorId", author.ID)
		}
		enrichAnswered = false
		return
	}
	slog.Info("refreshed author profile from metadata provider",
		"author", author.Name, "foreignId", author.ForeignID)
}

// takeSyncEnrichment spends one of the hour's syncEnrichmentsPerHour on a
// many-author sync, reporting false once the hour's budget is gone. A fixed
// window from the first spend: simple, and the staggered refresh picks up
// whatever a full window leaves due.
func (h *AuthorHandler) takeSyncEnrichment(now time.Time) bool {
	h.syncEnrichMu.Lock()
	defer h.syncEnrichMu.Unlock()
	if now.Sub(h.syncEnrichWindow) >= time.Hour {
		h.syncEnrichWindow, h.syncEnrichCount = now, 0
	}
	if h.syncEnrichCount >= syncEnrichmentsPerHour {
		return false
	}
	h.syncEnrichCount++
	return true
}

// mergeAuthorProfileFields copies provider-supplied profile fields onto the
// local author, following the project's established "only overwrite when the
// provider has a non-empty value" merge policy (mirrors
// relinkExistingAuthorToUpstream). It reports whether any field actually
// changed so the caller can skip a redundant DB write. SortName is only filled
// when missing, since the local sort order may have been curated.
func mergeAuthorProfileFields(author, upstream *models.Author) bool {
	changed := models.MergeProfileField(&author.Description, &author.DescriptionSource, textutil.CleanDescription(upstream.Description), upstream.DescriptionSource)
	if models.MergeProfileField(&author.ImageURL, &author.ImageSource, strings.TrimSpace(upstream.ImageURL), upstream.ImageSource) {
		changed = true
	}
	if disambiguation := strings.TrimSpace(upstream.Disambiguation); disambiguation != "" && disambiguation != author.Disambiguation {
		author.Disambiguation = disambiguation
		changed = true
	}
	if upstream.RatingsCount > 0 && upstream.RatingsCount != author.RatingsCount {
		author.RatingsCount = upstream.RatingsCount
		changed = true
	}
	if upstream.AverageRating > 0 && upstream.AverageRating != author.AverageRating {
		author.AverageRating = upstream.AverageRating
		changed = true
	}
	if sn := strings.TrimSpace(upstream.SortName); sn != "" && strings.TrimSpace(author.SortName) == "" {
		author.SortName = sn
		changed = true
	}
	return changed
}

// errAuthorRowChanged reports that a whole-row author write was refused
// because the row changed after the snapshot it was built from was read.
var errAuthorRowChanged = errors.New("author changed since it was read")

// updateAuthorIfUnchanged writes the whole author row only if it still matches
// snapshot, the UpdatedAtRaw read before a provider call. The relinks rebuild
// the row from that snapshot, so an unguarded write would undo a photo, bio or
// setting saved during the call (#3287).
func (h *AuthorHandler) updateAuthorIfUnchanged(ctx context.Context, author *models.Author, snapshot string) error {
	written, err := h.authors.UpdateIfUnchanged(ctx, author, snapshot)
	if err != nil {
		return err
	}
	if !written {
		return errAuthorRowChanged
	}
	return nil
}

// FetchAuthorBooks populates the author's catalogue from the metadata provider.
// mediaType is applied to each newly-created book when the provider didn't
// return one; pass an empty string to accept whatever the provider set.
//
// This is the INITIAL-sync entry point (add flow, migrations): monitoring of
// created books is governed by the author's MonitorMode alone. Refresh paths
// must use RefreshAuthorBooks instead so the author's MonitorNewItems policy
// applies to later-discovered works (issue #1348).
func (h *AuthorHandler) FetchAuthorBooks(author *models.Author, autoSearch bool, mediaType string) {
	h.fetchAuthorBooks(h.bgCtx(), author, catalogueSyncOptions{autoSearch: autoSearch, mediaType: mediaType})
}

// RefreshAuthorBooks is the discovery variant of FetchAuthorBooks used by the
// refresh paths (single-author Refresh, bulk refresh, refresh-all, relink).
// A refresh always updates the metadata of the books the library already has,
// but it only DISCOVERS new works for an author whose monitoring policy asks
// for them (authorAcceptsDiscoveredBooks) — a refresh must never grow the
// library behind the user's back (issues #1348, #1815).
func (h *AuthorHandler) RefreshAuthorBooks(author *models.Author, autoSearch bool, mediaType string) {
	h.fetchAuthorBooks(h.bgCtx(), author, catalogueSyncOptions{autoSearch: autoSearch, mediaType: mediaType, discovery: true})
}

// authorAcceptsDiscoveredBooks reports whether a refresh-path catalogue sync
// may CREATE book rows for works the library doesn't have yet.
//
// Refreshing metadata and importing a back catalogue are two different
// operations that shared one code path until #1815: a user with every
// monitoring switch off clicked "Refresh metadata" to fix missing cover art
// and got the author's entire bibliography inserted as new rows. Monitoring is
// the only intent signal the user has, so it is the one that decides:
//
//   - Unmonitored author — "I'm not tracking new work from them". Refresh
//     updates what's there and adds nothing.
//   - MonitorNewItems "none" — the setting's whole purpose, and what every
//     importer (ABS, Calibre) stamps on the authors it creates, precisely
//     because those catalogues are partial and the first refresh was the
//     classic detonation point (#1348). It now means "don't add them" rather
//     than "add them unmonitored", which is what the users who asked for it
//     were describing.
//
// MonitorMode "none" is deliberately NOT in this list: with new items allowed
// it is the "list the whole catalogue, monitor none of it" setup (#1290's
// Hardcover-list authors are exactly that), and those rows are created
// unmonitored anyway.
func authorAcceptsDiscoveredBooks(author *models.Author) bool {
	if author == nil {
		return false
	}
	if !author.Monitored {
		return false
	}
	return models.NormalizeAuthorMonitorNewItems(author.MonitorNewItems) != models.AuthorMonitorNewItemsNone
}

// authorAwaitsFirstCatalogue reports whether this author is the empty-author
// repair case: no books at all (excluded ones included — the user excluding
// every book is a decision, not an empty catalogue) and no record of a sync
// ever having populated them.
//
// A read error is answered false. The carve-out is the permissive branch, so
// the safe answer when we can't tell is "don't discover": a refresh that adds
// nothing is a support question, a refresh that re-imports 500 books the user
// deleted is the bug this whole change exists to fix.
func (h *AuthorHandler) authorAwaitsFirstCatalogue(ctx context.Context, author *models.Author, bookCount int) bool {
	if bookCount > 0 {
		return false
	}
	populatedAt, err := h.authors.CataloguePopulatedAt(ctx, author.ID)
	if err != nil {
		slog.Warn("could not read author catalogue-populated marker; treating the author as already populated",
			"author", author.Name, "authorId", author.ID, "error", err)
		return false
	}
	return populatedAt == nil
}

// ctx is the background context the sync runs on: the jobs group's
// shutdown-scoped one when the async path launched it, h.bgCtx() otherwise.
//
// The outcome is logged by runCatalogueSync; callers that need it (scheduled
// discovery, #2236) call runCatalogueSync directly.
func (h *AuthorHandler) fetchAuthorBooks(ctx context.Context, author *models.Author, opts catalogueSyncOptions) {
	if _, err := h.runCatalogueSync(ctx, author, opts); err != nil {
		slog.Warn("author catalogue sync stopped", "author", author.Name, "error", err)
	}
}

// refreshTitleMatch re-reads a row the catalogue sync matched by title. The
// title index holds rows as the sync first read them, and the cover, edition
// and hydration calls since then are long enough for a user to save an edit
// that writing the old copy back would undo (#2926). It reports false, and
// the sync leaves the row alone, when the row is gone or now excluded.
func (h *AuthorHandler) refreshTitleMatch(ctx context.Context, existing *models.Book) bool {
	if err := h.books.ReloadHydratedBook(ctx, existing); err != nil {
		if !errors.Is(err, sql.ErrNoRows) {
			slog.Warn("catalogue sync: could not re-read a matched book", "book_id", existing.ID, "error", err)
		}
		return false
	}
	return !existing.Excluded
}

// writeTitleMatch writes a row refreshTitleMatch re-read, guarded on that
// read. On a lost guard nothing is written and existing is reloaded so the
// steps after it see the edit; the next sync makes the change again.
func (h *AuthorHandler) writeTitleMatch(ctx context.Context, existing *models.Book, expectedUpdatedAt string) (bool, error) {
	written, err := h.books.UpdateIfUnchanged(ctx, existing, expectedUpdatedAt)
	if err != nil || written {
		return written, err
	}
	slog.Info("catalogue sync: book changed while it was being updated, leaving it for the next sync",
		"title", existing.Title, "book_id", existing.ID)
	if err := h.books.ReloadHydratedBook(ctx, existing); err != nil {
		slog.Warn("catalogue sync: could not re-read a book after a concurrent edit", "book_id", existing.ID, "error", err)
	}
	return false, nil
}

// refreshProviderFields lets an id-resolved book take the release date and,
// if it has none, the description the current provider record carries (#3105;
// the per-field rules are on Book.AdoptRefreshedProviderFields).
//
// It acts only when the record's own id is the row's id. A row that matched
// through a secondary id or a title may be a different work with the same name
// (two OpenLibrary works can share a title), and adopting that work's date
// would make the row alternate between them on every refresh.
//
// A field with an Audiobookshelf conflict recorded is skipped: the user's
// per-field source choice there is not something a refresh may override. The
// row is re-read and the write guarded on that read, so an edit made in between
// wins and the next sync makes the change again. An excluded or vanished row is
// left alone. A refresh that changes nothing writes nothing.
func (h *AuthorHandler) refreshProviderFields(ctx context.Context, existing, incoming *models.Book) {
	if existing.ForeignID != incoming.ForeignID {
		return
	}
	src := *incoming
	src.UnknownFields = slices.Clone(incoming.UnknownFields)
	for field, conflictName := range map[string]string{
		models.BookFieldReleaseDate: "release_date",
		models.BookFieldDescription: "description",
	} {
		has, err := h.books.HasABSConflictFor(ctx, existing.ID, conflictName)
		if err != nil || has {
			// Unreadable counts as present: skipping is the safe answer.
			src.UnknownFields = append(src.UnknownFields, field)
		}
	}
	probe := *existing
	if len(probe.AdoptRefreshedProviderFields(&src)) == 0 {
		return
	}
	if !h.refreshTitleMatch(ctx, existing) {
		return
	}
	expectedUpdatedAt := existing.UpdatedAtRaw
	if h.afterProviderFieldsRead != nil {
		h.afterProviderFieldsRead()
	}
	changed := existing.AdoptRefreshedProviderFields(&src)
	if len(changed) == 0 {
		return
	}
	if written, err := h.writeTitleMatch(ctx, existing, expectedUpdatedAt); err != nil {
		slog.Warn("catalogue sync: could not update provider fields", "book_id", existing.ID, "error", err)
	} else if written {
		slog.Debug("catalogue sync: updated provider fields", "book_id", existing.ID, "fields", changed)
	}
}

// keepWorkWithForeignID narrows a provider works list to the single work the
// caller asked for. Returns an empty slice when the list doesn't contain it —
// the sync then adds nothing, which is the correct answer for a single-book
// add whose work the author endpoint doesn't list (#1816).
func keepWorkWithForeignID(books []models.Book, foreignID string) []models.Book {
	for _, b := range books {
		if b.ForeignID == foreignID {
			return []models.Book{b}
		}
	}
	return nil
}

// reparentMisattachedBook re-links existing to the author being synced when
// the row is attached to an author that demonstrably shouldn't own it (issue
// #1405). foreign_id is globally UNIQUE, so a work fetched during this
// author's sync can match a row created under a duplicate/stale author record
// (OpenLibrary has duplicate author keys), a calibre shell author, or an
// author that no longer exists — and such a row never shows on this author's
// page. The provider's credited-author list is what separates that from a
// legitimately co-authored work: stealing a co-authored row would make it
// ping-pong between its authors on alternating syncs, so when the current
// owner is credited (or authorship is unknown) the row stays put.
// Returns true when existing.AuthorID was changed; the caller persists it.
func (h *AuthorHandler) reparentMisattachedBook(ctx context.Context, existing *models.Book, author *models.Author, creditedAuthorIDs []string) bool {
	if existing.AuthorID == author.ID {
		return false
	}
	// The row was found by a foreign id lookup that spans every user, so under
	// tenancy it can be another user's book. Moving it would take it out of
	// their author and into a different library. No owner counts as an owner
	// of its own: a shared author's sync must not take a user's book out from
	// under that user's author, and a shared book must not move under one
	// user's private author. Only a move within one owner goes ahead.
	if auth.EnforceTenancy() && existing.OwnerUserID != author.OwnerUserID {
		return false
	}
	owner, err := h.authors.GetByID(ctx, existing.AuthorID)
	if err != nil {
		// Can't verify ownership — leave the row alone rather than moving it
		// on a transient DB error.
		return false
	}
	// Two accounts' copies of one author (#3555) stay after tenancy is turned
	// off. Moving a book between them would park one account's book under the
	// other's copy, the next sync of its own copy would move it back, and
	// deleting the other copy meanwhile would cascade into it. Copies are told
	// by owners that differ while the rows share any provider id, primary or
	// secondary, since the two may know the author under different primary
	// ids. Such a pair never moves, whatever the tenancy setting or the
	// credited list. Rows of different owners that share no id are not
	// copies and keep the rules below.
	if owner != nil && owner.OwnerUserID != author.OwnerUserID && h.sharesProviderID(ctx, owner, author) {
		return false
	}
	switch {
	case owner == nil:
		// Owner row was deleted; the book is orphaned.
	case owner.ForeignID == author.ForeignID:
		// Duplicate author rows sharing one provider identity.
	case len(creditedAuthorIDs) > 0 && !slices.Contains(creditedAuthorIDs, owner.ForeignID):
		// The synced author's catalogue contains this work but the current
		// owner isn't credited on it — a mis-parent, not a co-author.
	default:
		// Owner is credited on the work (co-author) or authorship is unknown
		// (no credited list from this provider) — don't move it.
		return false
	}
	slog.Info("re-linking book to synced author",
		"title", existing.Title, "book_id", existing.ID,
		"from_author_id", existing.AuthorID, "to_author_id", author.ID)
	existing.AuthorID = author.ID
	return true
}

// handleNewWantedBook performs the post-create steps that every newly-created
// wanted book requires regardless of the creation path:
//  1. Link any provider series refs into the series/series_books tables.
//  2. Check whether the user already owns the file via LibraryFinder; if so,
//     record the path and return true so the caller can skip auto-search.
//
// Returns true when an existing file was found (caller must NOT auto-search),
// false otherwise.
func handleNewWantedBook(ctx context.Context, books *db.BookRepo, series *db.SeriesRepo, finder LibraryFinder, book models.Book, authorName string) (fileFound bool) {
	return handleNewWantedBookAmong(ctx, books, series, finder, book, authorName, nil)
}

// handleNewWantedBookAmong is handleNewWantedBook for a caller that already
// holds the author's catalogue, passed as siblings so the rival title check
// (#2941) does not list the author again for every book. nil siblings reads
// them from books.
func handleNewWantedBookAmong(ctx context.Context, books *db.BookRepo, series *db.SeriesRepo, finder LibraryFinder, book models.Book, authorName string, siblings []models.Book) (fileFound bool) {
	// Populate series membership for this book.
	if series != nil {
		for _, ref := range book.SeriesRefs {
			s := &models.Series{ForeignID: ref.ForeignID, Title: ref.Title}
			if err := series.CreateOrGet(ctx, s); err != nil {
				slog.Warn("failed to upsert series", "series", ref.Title, "error", err)
				continue
			}
			if err := series.LinkBook(ctx, s.ID, book.ID, ref.Position, ref.Primary); err != nil {
				slog.Warn("failed to link book to series", "book", book.Title, "series", ref.Title, "error", err)
			}
			// A Hardcover ref already names the series exactly, so record
			// the Hardcover link row too (#2245). Without it the series has
			// the right foreign id and no link, which is the only state the
			// UI and the fill paths read: every series on a freshly added
			// Hardcover author showed "(no Hardcover link)" and Fill did
			// nothing. Best effort, like the two calls above.
			if linked, err := series.EnsureHardcoverLinkFromForeignID(ctx, s.ID, ref.ForeignID, ref.Title); err != nil {
				slog.Warn("failed to record hardcover series link", "series", ref.Title, "error", err)
			} else if linked {
				slog.Debug("linked series to hardcover from provider series ref", "series", ref.Title, "foreignId", ref.ForeignID)
			}
		}
	}

	// Check if the user already owns this book before queuing a download.
	if finder != nil {
		if existingPath := findExistingForNewBook(ctx, books, finder, book, authorName, siblings); existingPath != "" {
			if existingFileOwnedByOtherBook(ctx, books, existingPath, book.ID) {
				slog.Info("library: matching file already belongs to another book, not binding it",
					"title", book.Title, "path", existingPath)
				return false
			}
			slog.Info("library: found existing file, skipping auto-search", "title", book.Title, "path", existingPath)
			if err := books.SetFilePath(ctx, book.ID, existingPath); err != nil {
				slog.Warn("authors: record existing file path", "error", err, "book_id", book.ID)
				// Another book took the file after the check above (#2937):
				// nothing was bound, so let the auto-search run.
				var owned *db.PathOwnedError
				if errors.As(err, &owned) {
					return false
				}
			}
			return true
		}
	}
	return false
}

// rivalAwareFinder is the optional capability of a LibraryFinder that can
// weigh a library file against the author's other catalogue titles before
// offering it for a new book. Implemented by *importer.LibrarySnapshot.
type rivalAwareFinder interface {
	FindExistingAmong(ctx context.Context, title, authorName, mediaType string, rivals []string) string
}

// findExistingForNewBook asks finder for a file the user already has for
// book, with the author's other books as rival titles when the finder can use
// them (#2941). FindExisting alone matches on the wanted title, so the
// shorter "Harry Potter" took an untracked "Harry Potter en het vervloekte
// kind.epub", skipped its search, and left the exact book unbound once the
// file was owned.
//
// siblings is the author's catalogue when the caller already holds it, which
// the author sync does: reading it again per new book costs a full author
// listing each time. nil means read it here; every caller runs after its book
// rows are written, so the listing holds the new book's siblings.
//
// Excluded books are not rivals: a row the user removed from the catalogue
// must not keep a file from the book they kept. Another user's book is left
// out too, so one user's catalogue never steers another's binds. A listing
// error falls back to the plain lookup, which is the old behaviour.
func findExistingForNewBook(ctx context.Context, books *db.BookRepo, finder LibraryFinder, book models.Book, authorName string, siblings []models.Book) string {
	// A Scanner hands out a snapshot; a fresh one costs the same single walk
	// its own FindExisting makes.
	finder = snapshotFinder(finder)
	ra, ok := finder.(rivalAwareFinder)
	if !ok || book.AuthorID == 0 || (siblings == nil && books == nil) {
		return finder.FindExisting(ctx, book.Title, authorName, book.MediaType)
	}
	// Rivals drop files before ranking, so they can turn a near tie into a
	// clear answer as well as remove one: the lookup always needs them.
	if siblings == nil {
		var err error
		siblings, err = books.ListByAuthor(ctx, book.AuthorID)
		if err != nil {
			slog.Warn("library: list author books for rival titles", "error", err, "book_id", book.ID)
			return finder.FindExisting(ctx, book.Title, authorName, book.MediaType)
		}
	}
	rivals := make([]string, 0, len(siblings))
	for i := range siblings {
		s := &siblings[i]
		if s.ID == book.ID || s.Excluded || s.AuthorID != book.AuthorID {
			continue
		}
		if book.OwnerUserID != 0 && s.OwnerUserID != 0 && s.OwnerUserID != book.OwnerUserID {
			continue
		}
		rivals = append(rivals, s.Title)
	}
	return ra.FindExistingAmong(ctx, book.Title, authorName, book.MediaType, rivals)
}

// existingFileOwnedByOtherBook reports whether a file FindExisting offered
// for bookID is already in book_files under another book: the file itself,
// or for an audio track the folder holding it, which is how an imported or
// reconciled audiobook is recorded (#2716).
//
// FindExisting matches on the title alone and knows nothing about what is
// tracked, so without this a new book could take a file another book owns.
// That is how "Defiance of the Fall 17" was bound to volume 1's m4b (#2810).
// The volume veto closes that layout, but not one where volume 1's folder
// and file carry no number at all, which Libation's default naming produces.
// Declining the bind leaves the book wanted and lets auto-search run, which
// is the right outcome for a different book and a visible one for a
// duplicate row. A lookup error keeps the old behaviour and binds. A row left
// by a deleted book does not count: SetFilePath takes it over (#2937).
func existingFileOwnedByOtherBook(ctx context.Context, books *db.BookRepo, path string, bookID int64) bool {
	if books == nil {
		return false
	}
	candidates := []string{path}
	if importer.IsAudioFile(path) {
		candidates = append(candidates, filepath.Dir(path))
	}
	for _, p := range candidates {
		owned, err := books.PathOwnedByLiveOtherBook(ctx, p, bookID)
		if err != nil {
			slog.Warn("library: book_files owner lookup failed", "path", p, "error", err)
			continue
		}
		if owned {
			return true
		}
	}
	return false
}

func runBookSearches(ctx context.Context, searcher BookSearcher, books []models.Book, maxConcurrent int) {
	if searcher == nil || len(books) == 0 {
		return
	}
	// Paced so a large author's auto-search doesn't burst every indexer at
	// once as slots free up (#1515); shares the package pace with the bulk
	// "search all" and series-fill fan-outs.
	concurrency.RunBoundedPaced(ctx, books, maxConcurrent, searchPaceInterval, func(ctx context.Context, book models.Book) {
		searcher.SearchAndGrabBook(indexer.WithSearchOrigin(ctx, indexer.OriginAuthor), book)
	})
}
