package api

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/vavallee/bindery/internal/auth"
	"github.com/vavallee/bindery/internal/db"
	"github.com/vavallee/bindery/internal/indexer"
	"github.com/vavallee/bindery/internal/models"
)

// addBookParams is everything an Add Book needs, independent of HTTP. The
// AddBook handler fills it from the request body; other callers (library
// adoption, request approval) fill it themselves. Ownership is not a field:
// the core reads the acting user from ctx, exactly as the handler always has.
type addBookParams struct {
	ForeignBookID   string
	ForeignAuthorID string
	AuthorName      string
	SearchOnAdd     bool
	// MediaType optionally forces ebook, audiobook or both for the added
	// book (#1397). Empty keeps the provider's media type, falling back to
	// the default.media_type setting.
	MediaType string
	// Monitored overrides the monitored flag step 3 sets on the book. Nil
	// keeps the handler's behaviour, which always marks the book monitored.
	Monitored *bool
	// SkipCatalogueSync suppresses the single work fallback sync that runs
	// when the direct insert produced no row. With it set, the core looks
	// for the row once instead of polling, since nothing it started could
	// still create it. False keeps the handler's behaviour.
	//
	// That fallback is the only sync this core ever starts, so the skip
	// removes exactly one author works call and the row it would create.
	// It skips nothing else: the fallback is a single work run, which never
	// refreshes the author profile (description, image), never relinks a
	// Calibre author and never searches. SearchOnAdd is independent of it
	// and still searches for the added book, so the two may be combined.
	SkipCatalogueSync bool
}

// addBookResult is what an Add Book produced.
type addBookResult struct {
	// Book is the library row the add resolved to, after the monitored and
	// media type update.
	Book *models.Book
	// BookCreated is true when this call inserted the row: the direct insert
	// created it, or the single work fallback sync ran and the row appeared.
	// False when an existing title equivalent row was adopted, or when a
	// concurrent add or sync created it first.
	BookCreated bool
	// Author is the author the book was filed under.
	Author *models.Author
	// AuthorCreated is true when this call inserted the author row.
	AuthorCreated bool
}

// Refusals from addBookCore that carry no data. The AddBook handler maps
// each to the status and body it has always answered with (see
// addBookErrorResponses); other callers match them with errors.Is.
var (
	errAddBookForeignBookIDRequired     = errors.New("add book: foreignBookId required")
	errAddBookInvalidMediaType          = errors.New("add book: invalid media type")
	errAddBookAuthorMetadataUnavailable = errors.New("add book: author metadata unavailable")
	errAddBookAuthorExists              = errors.New("add book: author already exists")
	errAddBookAuthorUnresolved          = errors.New("add book: could not resolve author")
	errAddBookCancelled                 = errors.New("add book: request cancelled")
	errAddBookNotFound                  = errors.New("add book: book not found after author sync")
	errAddBookHeldByAnotherUser         = errors.New("add book: book is held by another user")
)

// addBookErrorResponses holds the status and body for each sentinel above.
var addBookErrorResponses = []struct {
	err    error
	status int
	body   string
}{
	{errAddBookForeignBookIDRequired, http.StatusBadRequest, "foreignBookId required"},
	{errAddBookInvalidMediaType, http.StatusBadRequest, "mediaType must be 'ebook', 'audiobook', or 'both'"},
	{errAddBookAuthorMetadataUnavailable, http.StatusUnprocessableEntity, "Author metadata unavailable for this result. Add the author manually first (Authors → Add Author by name), then try again."},
	{errAddBookAuthorExists, http.StatusConflict, "author already exists"},
	{errAddBookAuthorUnresolved, http.StatusInternalServerError, "could not resolve author"},
	{errAddBookCancelled, http.StatusGatewayTimeout, "request cancelled"},
	{errAddBookNotFound, http.StatusNotFound, "book not found after author sync — try again shortly"},
	{errAddBookHeldByAnotherUser, http.StatusConflict, "book is held by another user"},
}

// bookInLibraryError refuses to re-add a book the user can already see
// (#1227). Book is the existing row, which the handler returns in the 409.
type bookInLibraryError struct {
	Book *models.Book
}

func (e *bookInLibraryError) Error() string { return "add book: book already in library" }

// primaryProviderUnavailableError refuses a fallback provider's record
// because the primary metadata provider did not answer (#2612).
type primaryProviderUnavailableError struct {
	Primary string
}

func (e *primaryProviderUnavailableError) Error() string {
	return primaryProviderUnavailableMessage(e.Primary)
}

// addBookLookupError wraps a failed book metadata lookup while resolving the
// author for a result that carried no author id. The handler answers 502
// with the wrapped error's text.
type addBookLookupError struct {
	Err error
}

func (e *addBookLookupError) Error() string { return e.Err.Error() }
func (e *addBookLookupError) Unwrap() error { return e.Err }

// writeAddBookError answers an addBookCore error with the status and body
// the AddBook handler returned before the core was split out.
func (h *AuthorHandler) writeAddBookError(w http.ResponseWriter, r *http.Request, err error) {
	var inLibrary *bookInLibraryError
	if errors.As(err, &inLibrary) {
		h.writeExistingBookConflict(w, inLibrary.Book)
		return
	}
	var unavailable *primaryProviderUnavailableError
	if errors.As(err, &unavailable) {
		writePrimaryProviderUnavailable(w, unavailable.Primary)
		return
	}
	var lookup *addBookLookupError
	if errors.As(err, &lookup) {
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": lookup.Err.Error()})
		return
	}
	for _, resp := range addBookErrorResponses {
		if errors.Is(err, resp.err) {
			writeJSON(w, resp.status, map[string]string{"error": resp.body})
			return
		}
	}
	writeServerError(w, r, err)
}

// addBookCore adds one book to the library: it resolves or creates the
// author, inserts the requested work, waits for the row, and marks it
// monitored. It is AddBook without the HTTP layer; see addBookParams for
// the inputs and the error types above for its refusals.
func (h *AuthorHandler) addBookCore(ctx context.Context, req addBookParams) (addBookResult, error) {
	if req.ForeignBookID == "" {
		return addBookResult{}, errAddBookForeignBookIDRequired
	}
	switch req.MediaType {
	case "", models.MediaTypeEbook, models.MediaTypeAudiobook, models.MediaTypeBoth:
	default:
		return addBookResult{}, errAddBookInvalidMediaType
	}

	// bookInserted and fallbackSynced record how the row came to exist, for
	// addBookResult.BookCreated.
	bookInserted := false
	fallbackSynced := false

	// bookCreated flips true once the poll loop confirms the requested
	// book is in the DB. The orphan-cleanup defer below reads it on
	// AddBook return — when false (poll timeout, ctx cancel, etc.) the
	// just-created author row is deleted iff it has zero books. Fixes
	// issue #667 bug 3.
	bookCreated := false

	// authorWasJustCreated tracks whether this request inserted the author
	// row (vs. found it already present). Used by both the orphan-cleanup
	// defer and the direct-insert block below — when the author was just
	// created the async catalogue sync may take longer than the 15s poll
	// budget for prolific authors, so we synchronously persist the requested
	// book to guarantee it exists before the cleanup defer runs (#804).
	authorWasJustCreated := false

	// 0. Refuse to re-add a book the user already owns (#1227). Before this
	// check the handler reused the existing row and force-monitored it at
	// step 3, so re-adding an owned book silently flipped it back to wanted.
	// Runs before any author creation or upstream fetch so a conflict has no
	// side effects. Scoped like the library list the user is looking at
	// (ListScopeUserID plus NULL owners): a NULL owned row is in that list,
	// and an admin under tenancy sees every row, so both are conflicts. A
	// non admin's request never sees another user's copy here; the guard
	// after the poll covers that case.
	userID := auth.UserIDFromContext(ctx)
	scopeID := auth.ListScopeUserID(ctx)
	if existing, err := h.books.GetByForeignIDVisibleTo(ctx, req.ForeignBookID, scopeID); err != nil {
		return addBookResult{}, err
	} else if existing != nil {
		return addBookResult{}, &bookInLibraryError{Book: existing}
	}

	if req.ForeignAuthorID == "" {
		resolved, outcome, err := h.resolveAuthorForBook(ctx, req.ForeignBookID)
		if err != nil {
			return addBookResult{}, &addBookLookupError{Err: err}
		}
		// The ISBN walk steps past a primary that timed out, so a fallback's
		// record can win here only because the primary never answered. Adding
		// it makes that provider the author's permanent link, and the same
		// person is duplicated once the primary is back (#2117, #2612). Nothing
		// has been written yet, so refuse and let a retry do the resolving.
		if resolved != nil && !outcome.SafeToBind(resolved.Author.ForeignID) {
			slog.Warn("AddBook: refusing to bind author to a fallback provider",
				"foreignBookId", req.ForeignBookID, "primary", outcome.Primary,
				"failed", outcome.FailureSummary(), "wouldHaveLinked", resolved.Author.ForeignID)
			return addBookResult{}, &primaryProviderUnavailableError{Primary: outcome.Primary}
		}
		if resolved != nil {
			// Rewrite the request so the existing fetch+poll flow targets the
			// canonical provider's IDs. The user sees the canonical record (e.g.
			// the OpenLibrary version) in their library; the original DNB record
			// is dropped because bindery's author/book identity is single-source.
			req.ForeignBookID = resolved.ForeignID
			req.ForeignAuthorID = resolved.Author.ForeignID
			if req.AuthorName == "" {
				req.AuthorName = resolved.Author.Name
			}
			// The canonical id may itself already be in the library even though
			// the id the client sent was not (#1227).
			if existing, err := h.books.GetByForeignIDVisibleTo(ctx, req.ForeignBookID, scopeID); err != nil {
				return addBookResult{}, err
			} else if existing != nil {
				return addBookResult{}, &bookInLibraryError{Book: existing}
			}
		} else if req.AuthorName != "" {
			// ISBN-based resolution failed (e.g. Google Books: author name, no
			// author ID, no ISBN). Resolve the author by NAME — prefer one already
			// in the library so we reuse the user's existing author instead of
			// duplicating it; otherwise adopt OpenLibrary's canonical record. Keep
			// the chosen edition (req.ForeignBookID) — the other providers don't
			// have this book.
			if existing := h.findLibraryAuthorByName(ctx, req.AuthorName); existing != nil {
				req.ForeignAuthorID = existing.ForeignID
			} else if canonical, cErr := h.meta.ResolveCanonicalAuthor(ctx, req.AuthorName); cErr == nil && canonical != nil && outcome.SafeToBind(canonical.ForeignID) {
				// The canonical lookup is OpenLibrary's, which is a fallback
				// when another provider is primary, so the same guard applies.
				req.ForeignAuthorID = canonical.ForeignID
			}
		}
		if req.ForeignAuthorID == "" && outcome.PrimaryFailed {
			// No provider placed the author, but the primary never answered,
			// so "add the author manually" is the wrong advice: the primary
			// may well know this book.
			return addBookResult{}, &primaryProviderUnavailableError{Primary: outcome.Primary}
		}
		if req.ForeignAuthorID == "" {
			return addBookResult{}, errAddBookAuthorMetadataUnavailable
		}
	}

	// 1. Find or create the author (unmonitored if new so we don't auto-want all books).
	author, _ := h.authors.GetByForeignIDForUser(ctx, req.ForeignAuthorID, userID)
	if author == nil {
		author, _ = h.authors.GetByAnyForeignIDForUser(ctx, req.ForeignAuthorID, userID)
	}
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

		// Dedupe path: if a canonical provider (OL / Hardcover / …) is being
		// added for a SortName previously persisted as a synthetic DNB-only
		// row, migrate that row in place rather than creating a duplicate.
		// The synthetic row was created because the DNB record had only an
		// author name (no GND link, no OL coverage). Now that a canonical
		// identity exists, collapse the two onto a single primary key so
		// the user keeps one author with all their books attached.
		if !strings.HasPrefix(fetched.ForeignID, "dnb:") {
			if existing, lookupErr := h.authors.GetByDNBSyntheticName(ctx, fetched.SortName, userID); lookupErr == nil && existing != nil {
				if err := h.authors.UpgradeSyntheticDNB(ctx, existing.ForeignID, fetched); err != nil {
					slog.Debug("AddBook: upgrade synthetic DNB author failed", "from", existing.ForeignID, "to", fetched.ForeignID, "error", err)
				} else {
					// Re-fetch the row by its new canonical ForeignID so subsequent
					// steps see the upgraded record (ID preserved).
					if upgraded, getErr := h.authors.GetByForeignIDForUser(ctx, fetched.ForeignID, userID); getErr == nil && upgraded != nil {
						author = upgraded
					}
				}
			}
		}

		// CreateForUser may collide with a concurrent request inserting the
		// same author; the UNIQUE-constraint branch below recovers by
		// re-fetching the row. authorWasJustCreated stays false on the race
		// path so the orphan-cleanup defer never rolls back somebody else's
		// author row (issue #667).
		if author == nil {
			// Add-book creates the author as a side effect, so it never carries
			// an explicit monitor choice — take the install-wide default (#1666).
			db.ApplyAuthorMonitorDefaults(ctx, h.settings, fetched)
			if err := h.authors.CreateForUser(ctx, fetched, userID); err != nil {
				if !strings.Contains(err.Error(), "UNIQUE constraint failed") && !errors.Is(err, db.ErrAuthorIdentifierConflict) {
					return addBookResult{}, err
				}
				// Race: another request created it between our check and insert.
				author, _ = h.authors.GetByAnyForeignIDForUser(ctx, req.ForeignAuthorID, userID)
				if author == nil {
					return addBookResult{}, errAddBookAuthorExists
				}
			} else {
				author = fetched
				authorWasJustCreated = true
				// No speculative catalogue fetch here (#1816). Adding a book
				// creates its author as a side effect; the user picked ONE
				// title, and pulling that author's whole bibliography in behind
				// it is the "my collection went from 75 books to over 500"
				// report — the thing nobody expects because adding a film to
				// Radarr does not import the director's filmography.
				//
				// Nothing downstream needs it: the direct insert below creates
				// the picked book synchronously, which is what makes the poll
				// succeed and what keeps the orphan-cleanup defer from
				// rolling the author back. The narrow case where that insert
				// cannot produce the row has its own single-work fallback,
				// just past the direct-insert block.
			}
		}
		// Defer the orphan cleanup so cancellation paths inside the poll
		// loop also benefit. Runs only after a CreateForUser this request.
		if authorWasJustCreated {
			defer h.cleanupOrphanIfNoBooks(author, &bookCreated)
		}
	}
	if author == nil {
		return addBookResult{}, errAddBookAuthorUnresolved
	}

	// 1b. Direct insert for the requested book.
	//
	// Originally added (#667) for DNB synthetic IDs, whose async sync returns
	// zero books because DNB's public SRU has no author→works relationship.
	// #804 widened this: for any author the request just created, the async
	// catalogue sync can take longer than the 15 s poll budget (OpenLibrary
	// took >32 s for a 175-work author in the bug report). When the poll
	// times out, the orphan-cleanup defer deletes the author row — and the
	// still-running goroutine then logs a FK-constraint failure for every
	// book it tries to insert against the now-deleted author_id.
	//
	// Synchronously fetching and persisting the single requested record
	// guarantees the poll succeeds on its first iteration AND that the
	// cleanup defer sees a non-empty book list (so it keeps the author).
	// The async sync still runs as a backfill for the rest of the catalogue;
	// any UNIQUE collision against this row is silently tolerated.
	//
	// #1612 made the direct insert unconditional. When the author already
	// EXISTED, AddBook used to skip it and rely entirely on a catalogue sync
	// having created the row — but the sync can deterministically refuse a
	// specific work (e.g. the work-level language sampled from the first few
	// OpenLibrary editions falls outside the profile's allowed set, which is
	// how heavily-translated works ended up permanently un-addable). Every
	// attempt then polled 15 s for a row nothing would ever create and
	// returned 404 "try again shortly" forever. An explicit add of one
	// specific work is the strongest possible user signal and must not be
	// vetoed by catalogue-sync heuristics; those heuristics still govern
	// everything the user did NOT explicitly pick.
	if existing, _ := h.books.GetByForeignID(ctx, req.ForeignBookID); existing == nil {
		primary, err := h.meta.GetBook(ctx, req.ForeignBookID)
		if err != nil {
			slog.Warn("AddBook: direct fetch failed",
				"foreignBookId", req.ForeignBookID, "error", err)
		} else if primary != nil && h.directInsertTitleUsable(primary.Title, author.Name) {
			primary.AuthorID = author.ID
			// Tenancy (#1457): inherit the author's owner. Read off the author
			// row rather than the request, because that row usually pre-exists
			// this request (#1612) — the scoped lookup above is what makes it
			// the correct owner either way.
			primary.OwnerUserID = author.OwnerUserID
			primary.Monitored = author.Monitored
			if primary.Status == "" {
				primary.Status = models.BookStatusWanted
			}
			// An explicit request choice wins over the provider's media type
			// (#1397). Otherwise some providers (notably Google Books) don't
			// set one; fall back to the global default so the row isn't
			// created with an empty format (which would mis-route its
			// indexer search).
			if req.MediaType != "" {
				primary.MediaType = req.MediaType
			} else if primary.MediaType == "" {
				primary.MediaType = h.resolveDefaultMediaType(ctx)
			}
			// Reuse a title-equivalent row under the same author instead of
			// inserting a second one. The catalogue sync runs this same dedup
			// (see the FindByAuthorAndDedupKey switch above), and skipping it
			// here produced real duplicates: a Calibre-imported library holds
			// the work under a `calibre:` foreign id, and OpenLibrary splits
			// some works into separate ebook and audiobook Works that the sync
			// merges into one media_type=both row — in both cases the
			// requested foreign id has no row of its own, which is exactly the
			// state that brings a user here.
			match, ferr := h.books.FindByAuthorAndDedupKey(ctx, author.ID, primary.Title)
			// A subtitle-collapsed dedup key (indexer.CanonicalDedupKey strips a
			// ": subtitle" tail) merges every "Series: Volume" sibling onto one
			// key. Adopting such a match would rebind the requested foreign id
			// onto a *different* volume and — because adopt is a no-op when the
			// row needs no field change — leave the poll below unable to find the
			// requested id, returning 404 forever. When the requested work is a
			// distinct volume of the matched row's series (same series, different
			// sequence), skip the adopt and create a distinct row instead.
			if ferr == nil && match != nil && !h.directInsertSeriesConflict(ctx, match.ID, primary.SeriesRefs) {
				h.adoptDirectInsertMatch(ctx, match, primary, req.ForeignBookID)
			} else if err := h.books.Create(ctx, primary); err != nil {
				if !strings.Contains(err.Error(), "UNIQUE constraint failed") {
					slog.Warn("AddBook: direct insert failed",
						"foreignBookId", req.ForeignBookID, "error", err)
				}
			} else {
				bookInserted = true
				// An explicit request format is a pin; a provider- or
				// default-derived one is not (see addBookParams.MediaType above).
				h.hydrateHardcoverEditions(ctx, primary, nil, req.MediaType != "")
				// Same post-create work every other creation path does
				// (recommendations.go, series.go): check the library for a
				// file we already have and link the book into its series.
				// Without this the row is wanted-but-unchecked, so with
				// searchOnAdd enabled Bindery re-downloads a book already on
				// disk — the regression #940 and migration 026 exist to stop.
				created := *primary
				handleNewWantedBook(ctx, h.books, h.series, h.finder, created, author.Name)
			}
		}
	}

	// 1c. Single-work fallback. The direct insert above covers the request in
	// all but a couple of cases: the provider's book endpoint failed (#1612's
	// OpenLibrary 502) or returned a record the title guard rejected, and the
	// library has no row for the id either way. Ask the author endpoint for
	// this ONE work instead — the same fetch the old speculative catalogue
	// sync ran, restricted to the work the user actually picked, so the poll
	// below can still succeed without the rest of the bibliography riding
	// along (#1816).
	if existing, _ := h.books.GetByForeignID(ctx, req.ForeignBookID); existing == nil && !req.SkipCatalogueSync {
		// mediaType only fills a format the provider left blank, and step 3
		// below applies the request's explicit choice to whatever row the poll
		// finds — so the default is the right value to pass here. It no longer
		// decides whether the work is created at all: a single-work run is
		// exempt from the strict media-type clamp (#1612).
		fallbackSynced = true
		h.fetchAuthorBooksAsync(author, catalogueSyncOptions{
			mediaType:     h.resolveDefaultMediaType(ctx),
			onlyForeignID: req.ForeignBookID,
		})
	}

	// 2. Poll until the book appears (the single-work fallback, if it ran,
	// creates it asynchronously).
	deadline := time.Now().Add(15 * time.Second)
	if req.SkipCatalogueSync {
		deadline = time.Now()
	}
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
			return addBookResult{}, errAddBookCancelled
		case <-time.After(500 * time.Millisecond):
		}
	}

	if book == nil {
		return addBookResult{}, errAddBookNotFound
	}
	// The poll looks the row up globally because books.foreign_id is UNIQUE
	// across users, so under tenancy it can return another user's copy: the
	// conflict gate above never saw it (scoped to this user), the direct
	// insert skipped it, and until #1227 the update below then flipped that
	// user's book to monitored. Refuse instead, with no row in the body since
	// the caller is not allowed to see it. bookCreated stays false so the
	// orphan cleanup defer removes any author row this request created.
	if book.OwnerUserID != 0 && userID != 0 && book.OwnerUserID != userID {
		return addBookResult{}, errAddBookHeldByAnotherUser
	}
	bookCreated = true

	// 3. Mark the book monitored (wanted). An explicit media-type choice is
	// applied here too — the poll may have found a row created by the async
	// catalogue sync (or one already in the library) carrying the default.
	// Re-evaluate status on a change so e.g. adding an already-imported ebook
	// as 'both' flips it back to wanted for the missing format (#1148).
	book.Monitored = true
	if req.Monitored != nil {
		book.Monitored = *req.Monitored
	}
	if req.MediaType != "" && book.MediaType != req.MediaType {
		book.MediaType = req.MediaType
		reevaluateBookStatus(book)
	}
	if err := h.books.Update(ctx, book); err != nil {
		return addBookResult{}, err
	}

	// 3b. Say so when this add went past the strict media-type policy (#1759).
	//
	// The policy is a catalogue-population rule, not a veto on what the user
	// may own: the direct insert above never consults it, and the single-work
	// fallback is exempt by #1612's rule that "an explicit add of one specific
	// work must not be vetoed by catalogue-sync heuristics". Both of those are
	// deliberate, because silently refusing an explicit user action is the
	// worse of the two failures.
	//
	// What was missing is that it happened invisibly, so a user who turned the
	// setting on to stop un-grabbable rows appearing had no way to learn that
	// their own add was the exception. The setting's help text now says the
	// same thing, which is the half most people will actually see.
	h.logStrictMediaTypeBypass(ctx, book)

	// 4. Optionally trigger an indexer search. Use the process-lifecycle
	// context so the search goroutine is cancelled on shutdown rather than
	// running against context.Background(). See #846.
	// OriginAdd, not OriginAuthor (#2742): this is one book the user named and
	// explicitly asked to search on add, so the author monitoring rule must
	// leave it alone. The other OriginAuthor site is the catalogue sync fanning
	// out over works it discovered by itself, which the rule does suppress.
	if req.SearchOnAdd && h.searcher != nil {
		go h.searcher.SearchAndGrabBook(indexer.WithSearchOrigin(h.bgCtx(), indexer.OriginAdd), *book) // #nosec G118 -- intentional: search must outlive the request
	}

	return addBookResult{
		Book:          book,
		BookCreated:   bookInserted || fallbackSynced,
		Author:        author,
		AuthorCreated: authorWasJustCreated,
	}, nil
}
