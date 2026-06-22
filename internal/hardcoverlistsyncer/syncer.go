// Package hardcoverlistsyncer syncs Hardcover reading lists into Bindery's
// book catalogue as "wanted" books.
package hardcoverlistsyncer

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"

	"github.com/vavallee/bindery/internal/bookhydrate"
	"github.com/vavallee/bindery/internal/db"
	"github.com/vavallee/bindery/internal/metadata/hardcover"
	"github.com/vavallee/bindery/internal/models"
)

// ListSyncer syncs enabled Hardcover import lists into Bindery's book catalogue.
type ListSyncer struct {
	importLists *db.ImportListRepo
	authors     *db.AuthorRepo
	books       *db.BookRepo
	series      seriesLinker
	editions    *db.EditionRepo

	tokenSource   func(context.Context) string
	clientFactory hardcoverClientFactory
	enricher      bookhydrate.AudiobookEnricher
}

type hardcoverClient interface {
	GetUserLists(context.Context) ([]hardcover.HCList, error)
	GetListBooks(context.Context, int) ([]models.Book, error)
	GetEditions(context.Context, string) ([]models.Edition, error)
}

// seriesLinker is the slice of *db.SeriesRepo the syncer needs to persist
// the primary-series association attached to imported books. Declared as an
// interface so tests can stub it without standing up the full SQL schema.
type seriesLinker interface {
	CreateOrGet(ctx context.Context, s *models.Series) error
	LinkBook(ctx context.Context, seriesID, bookID int64, position string, primary bool) error
}

type hardcoverClientFactory func(string) hardcoverClient

// New creates a new ListSyncer.
func New(importLists *db.ImportListRepo, authors *db.AuthorRepo, books *db.BookRepo) *ListSyncer {
	return &ListSyncer{
		importLists:   importLists,
		authors:       authors,
		books:         books,
		clientFactory: func(apiKey string) hardcoverClient { return hardcover.NewAuthenticated(apiKey) },
	}
}

// WithSeriesRepo wires the series persistence layer so that books imported
// from Hardcover lists carry forward their primary-series association.
// Without it, SeriesRefs on imported books are silently dropped.
func (s *ListSyncer) WithSeriesRepo(repo *db.SeriesRepo) *ListSyncer {
	if repo == nil {
		s.series = nil
		return s
	}
	s.series = repo
	return s
}

// WithEditionHydration wires edition persistence for Hardcover list imports.
func (s *ListSyncer) WithEditionHydration(editions *db.EditionRepo, enricher bookhydrate.AudiobookEnricher) *ListSyncer {
	s.editions = editions
	s.enricher = enricher
	return s
}

// WithClientFactory overrides the Hardcover client factory used by tests.
func (s *ListSyncer) WithClientFactory(factory hardcoverClientFactory) *ListSyncer {
	if factory != nil {
		s.clientFactory = factory
	}
	return s
}

// WithTokenSource configures the fallback Hardcover API token used when an
// import list has no per-list override token.
func (s *ListSyncer) WithTokenSource(source func(context.Context) string) *ListSyncer {
	s.tokenSource = source
	return s
}

// Sync processes all enabled import lists of type "hardcover".
func (s *ListSyncer) Sync(ctx context.Context) error {
	lists, err := s.importLists.ListByType(ctx, "hardcover")
	if err != nil {
		return fmt.Errorf("list hardcover import lists: %w", err)
	}
	if len(lists) == 0 {
		slog.Debug("no enabled hardcover import lists")
		return nil
	}

	var firstErr error
	for _, il := range lists {
		if err := s.syncList(ctx, il); err != nil {
			slog.Error("hardcover list sync failed", "list", il.Name, "error", err)
			if firstErr == nil {
				firstErr = err
			}
			continue
		}
		if err := s.importLists.UpdateLastSyncAt(ctx, il.ID); err != nil {
			slog.Error("failed to update last_sync_at", "list", il.Name, "error", err)
		}
	}
	return firstErr
}

// SyncOne syncs a single hardcover import list by ID. Used by the manual
// "Sync now" UI affordance. Returns ErrNotFound if the list doesn't exist,
// ErrWrongType if it's not a hardcover list, or the underlying sync error.
func (s *ListSyncer) SyncOne(ctx context.Context, id int64) error {
	il, err := s.importLists.GetByID(ctx, id)
	if err != nil {
		return fmt.Errorf("load import list %d: %w", id, err)
	}
	if il == nil {
		return ErrNotFound
	}
	if il.Type != "hardcover" {
		return ErrWrongType
	}
	if !il.Enabled {
		return ErrDisabled
	}
	if err := s.syncList(ctx, *il); err != nil {
		return err
	}
	if err := s.importLists.UpdateLastSyncAt(ctx, il.ID); err != nil {
		slog.Error("failed to update last_sync_at", "list", il.Name, "error", err)
	}
	return nil
}

// Sentinel errors for SyncOne so the API handler can map them to HTTP status
// codes without string-matching.
var (
	ErrNotFound     = errors.New("import list not found")
	ErrWrongType    = errors.New("import list is not a hardcover list")
	ErrDisabled     = errors.New("import list is disabled")
	ErrMissingToken = errors.New("hardcover API token is not configured")
)

func (s *ListSyncer) syncList(ctx context.Context, il models.ImportList) error {
	token := s.tokenForList(ctx, il)
	if token == "" {
		return ErrMissingToken
	}
	client := s.clientFactory(token)

	// Resolve the list by slug
	userLists, err := client.GetUserLists(ctx)
	if err != nil {
		return fmt.Errorf("get user lists: %w", err)
	}

	var listID int
	for _, ul := range userLists {
		if ul.Slug == il.URL {
			listID = ul.ID
			break
		}
	}
	if listID == 0 {
		return fmt.Errorf("list with slug %q not found in user's Hardcover lists", il.URL)
	}

	books, err := client.GetListBooks(ctx, listID)
	if err != nil {
		return fmt.Errorf("get list books: %w", err)
	}

	slog.Info("syncing hardcover list", "list", il.Name, "slug", il.URL, "books", len(books))

	for _, book := range books {
		if book.ForeignID == "" {
			continue
		}

		// Skip if already tracked
		existing, _ := s.books.GetByForeignID(ctx, book.ForeignID)
		if existing != nil {
			slog.Debug("book already tracked, skipping", "title", book.Title, "foreignID", book.ForeignID)
			continue
		}

		// Look up or create the author
		authorID, err := s.ensureAuthor(ctx, &book)
		if err != nil {
			slog.Warn("failed to ensure author for book", "title", book.Title, "error", err)
			continue
		}

		book.AuthorID = authorID
		book.Monitored = true
		book.Status = models.BookStatusWanted
		if book.Genres == nil {
			book.Genres = []string{}
		}

		if err := s.books.Create(ctx, &book); err != nil {
			if strings.Contains(err.Error(), "UNIQUE constraint failed") {
				slog.Debug("book already exists (race)", "title", book.Title)
				continue
			}
			slog.Warn("failed to create book", "title", book.Title, "error", err)
			continue
		}
		s.hydrateHardcoverEditions(ctx, &book, client)
		slog.Info("imported book from hardcover list", "title", book.Title, "author_id", authorID)

		s.linkSeriesRefs(ctx, &book)
	}

	return nil
}

// linkSeriesRefs persists each Hardcover SeriesRef into the series table and
// links it to the freshly imported book. Best-effort: a failed link must not
// roll back or block the book import — log and move on.
func (s *ListSyncer) linkSeriesRefs(ctx context.Context, book *models.Book) {
	if s.series == nil || len(book.SeriesRefs) == 0 || book.ID == 0 {
		return
	}
	for _, ref := range book.SeriesRefs {
		ser := &models.Series{ForeignID: ref.ForeignID, Title: ref.Title}
		if err := s.series.CreateOrGet(ctx, ser); err != nil {
			slog.Warn("hardcover list sync: upsert series failed", "series", ref.Title, "book", book.Title, "error", err)
			continue
		}
		if err := s.series.LinkBook(ctx, ser.ID, book.ID, ref.Position, ref.Primary); err != nil {
			slog.Warn("hardcover list sync: link book to series failed", "series", ref.Title, "book", book.Title, "error", err)
		}
	}
}

func (s *ListSyncer) tokenForList(ctx context.Context, il models.ImportList) string {
	if token := hardcover.NormalizeAPIToken(il.APIKey); token != "" {
		return token
	}
	if s.tokenSource == nil {
		return ""
	}
	return hardcover.NormalizeAPIToken(s.tokenSource(ctx))
}

func (s *ListSyncer) hydrateHardcoverEditions(ctx context.Context, book *models.Book, client hardcoverClient) {
	if book == nil || client == nil || s.editions == nil {
		return
	}
	bookhydrate.HydrateHardcoverEditions(ctx, bookhydrate.Options{
		Book:          book,
		Provider:      "hardcover",
		Editions:      s.editions,
		Books:         s.books,
		FetchEditions: client.GetEditions,
		Enricher:      s.enricher,
	})
}

// ensureAuthor looks up the author by foreign ID, creating a minimal record if
// missing. Returns the author's database ID.
//
// Lookup order:
//  1. Primary foreign_id or alternate identifier match via GetByAnyForeignID.
//  2. Normalized-name match against existing authors. When a confident hit is
//     found, the Hardcover foreign_id is attached as an alias via
//     UpsertAuthorIdentifier so future list syncs hit step 1 directly.
//     This is the path that prevents the duplicate-author row described in
//     issue #1223: a library populated by ABS/OpenLibrary has no Hardcover
//     foreign_id, so step 1 misses even though the author already exists.
//
// Returns the author's database ID.
func (s *ListSyncer) ensureAuthor(ctx context.Context, book *models.Book) (int64, error) {
	if book.Author == nil {
		return 0, fmt.Errorf("book %q has no author metadata", book.Title)
	}

	existing, err := s.authors.GetByAnyForeignID(ctx, book.Author.ForeignID)
	if err != nil {
		return 0, err
	}
	if existing != nil {
		return existing.ID, nil
	}

	// Fallback: normalize the candidate name and look for an existing author
	// whose stored name (or sort_name) normalizes to the same value. This
	// matches "George R. R. Martin" against "George R.R. Martin", and
	// "Stephen  King" against "Stephen King". On a hit we attach the
	// Hardcover foreign_id as an alias so the next list sync hits step 1.
	if matched, matchErr := s.findExistingAuthorByName(ctx, book.Author); matchErr != nil {
		slog.Warn("hardcover list sync: normalized author lookup failed; creating new author",
			"name", book.Author.Name, "error", matchErr)
	} else if matched != nil {
		if err := s.authors.UpsertAuthorIdentifier(ctx, matched.ID, book.Author.ForeignID); err != nil {
			if errors.Is(err, db.ErrAuthorIdentifierConflict) {
				// Another author already owns this Hardcover foreign_id —
				// almost certainly via a concurrent sync. Re-fetch the true
				// owner via the alias table and reuse that author instead of
				// the one we matched by name, which may be a homonym
				// (issue #1224 review).
				owner, ownerErr := s.authors.GetByAnyForeignID(ctx, book.Author.ForeignID)
				if ownerErr != nil {
					return 0, fmt.Errorf("resolve author-identifier conflict for %q: %w", book.Author.ForeignID, ownerErr)
				}
				if owner == nil {
					return 0, fmt.Errorf("author identifier conflict for %q but no current owner found", book.Author.ForeignID)
				}
				if owner.ID != matched.ID {
					slog.Info("hardcover list sync: name match lost identifier race; reusing identifier owner",
						"matchedAuthorID", matched.ID, "matchedName", matched.Name,
						"ownerAuthorID", owner.ID, "ownerName", owner.Name,
						"hcForeignID", book.Author.ForeignID)
				}
				return owner.ID, nil
			}
			// Any other alias-attach failure is best-effort — log and keep
			// reusing the name-matched author for this sync. Next sync will
			// retry.
			slog.Warn("hardcover list sync: failed to attach Hardcover alias to existing author",
				"existingAuthorID", matched.ID, "name", matched.Name, "hcForeignID", book.Author.ForeignID, "error", err)
		} else {
			slog.Info("reused existing author for hardcover list import",
				"existingAuthorID", matched.ID, "name", matched.Name, "hcForeignID", book.Author.ForeignID)
		}
		return matched.ID, nil
	}

	// Create a minimal author record
	author := book.Author
	author.Monitored = true
	author.MetadataProvider = "hardcover"
	if author.SortName == "" {
		author.SortName = sortName(author.Name)
	}

	if err := s.authors.Create(ctx, author); err != nil {
		if strings.Contains(err.Error(), "UNIQUE constraint failed") || errors.Is(err, db.ErrAuthorIdentifierConflict) {
			// Race: author created between our check and insert
			existing, _ = s.authors.GetByAnyForeignID(ctx, author.ForeignID)
			if existing != nil {
				return existing.ID, nil
			}
		}
		return 0, fmt.Errorf("create author %q: %w", author.Name, err)
	}
	slog.Info("created author from hardcover list", "name", author.Name, "foreignID", author.ForeignID)
	return author.ID, nil
}

// findExistingAuthorByName scans the existing authors and returns the
// unique author whose name or sort_name normalizes to the same value as the
// candidate's Name. Returns nil with no error when no match is found OR
// when the match is ambiguous (two or more distinct authors normalize to
// the same key) — merging homonyms silently is the failure mode flagged in
// issue #1224's review, so an ambiguous match falls through to create-new.
//
// The scan is linear over List(); acceptable here because list sync is a
// scheduled batch job (not request-path) and the author table is bounded
// by a single user's library.
func (s *ListSyncer) findExistingAuthorByName(ctx context.Context, candidate *models.Author) (*models.Author, error) {
	if candidate == nil || strings.TrimSpace(candidate.Name) == "" {
		return nil, nil
	}
	target := normalizeAuthorName(candidate.Name)
	if target == "" {
		return nil, nil
	}

	all, err := s.authors.List(ctx)
	if err != nil {
		return nil, fmt.Errorf("list authors for name match: %w", err)
	}
	var matches []*models.Author
	for i := range all {
		a := &all[i]
		if normalizeAuthorName(a.Name) == target || normalizeAuthorName(a.SortName) == target {
			matches = append(matches, a)
		}
	}
	switch len(matches) {
	case 0:
		return nil, nil
	case 1:
		return matches[0], nil
	default:
		// Ambiguous: multiple distinct authors collapse to the same
		// normalized key. Refuse to merge them — a real "Stephen King"
		// and a real "Stephen Hawking" must not be collapsed just because
		// some future bug drops the surname. Log the candidates and fall
		// through to create-new.
		names := make([]string, 0, len(matches))
		for _, m := range matches {
			names = append(names, m.Name)
		}
		slog.Warn("hardcover list sync: ambiguous normalized-name match; creating new author instead of merging",
			"candidateName", candidate.Name,
			"candidateForeignID", candidate.ForeignID,
			"candidateCount", len(matches),
			"candidateNames", names)
		return nil, nil
	}
}

// normalizeAuthorName produces a canonical form suitable for author-identity
// matching: lowercased, with periods and commas stripped, and runs of
// whitespace condensed to a single space. "George R. R. Martin",
// "George R.R. Martin", and "george r r martin" all collapse to the same
// key. Empty and whitespace-only inputs return "" so callers can short-
// circuit without a separate check.
func normalizeAuthorName(name string) string {
	name = strings.TrimSpace(name)
	if name == "" {
		return ""
	}
	var b strings.Builder
	b.Grow(len(name))
	prevSpace := false
	for _, r := range name {
		switch r {
		case '.', ',', ';', ':', '\t', '\n', '\r':
			// Skip — treat as separators.
			if !prevSpace && b.Len() > 0 {
				b.WriteByte(' ')
				prevSpace = true
			}
		case ' ':
			if !prevSpace && b.Len() > 0 {
				b.WriteByte(' ')
				prevSpace = true
			}
		default:
			b.WriteRune(r)
			prevSpace = false
		}
	}
	out := strings.ToLower(strings.TrimSpace(b.String()))
	return out
}

func sortName(name string) string {
	parts := strings.Fields(name)
	if len(parts) < 2 {
		return name
	}
	last := parts[len(parts)-1]
	rest := strings.Join(parts[:len(parts)-1], " ")
	return last + ", " + rest
}

// HCListSyncer is satisfied by *ListSyncer — the scheduler uses this
// interface to avoid a direct import of this package.
type HCListSyncer interface {
	Sync(ctx context.Context) error
}

// Ensure ListSyncer implements HCListSyncer at compile time.
var _ HCListSyncer = (*ListSyncer)(nil)

// RunSync implements the scheduler.CalibreSyncer-style signature so the
// scheduler can call a single method with no return value, ignoring errors
// (they are already logged inside Sync).
func (s *ListSyncer) RunSync(ctx context.Context) {
	if err := s.Sync(ctx); err != nil {
		slog.Error("hardcover list sync error (top-level)", "error", err)
	}
}

// Ensure *ListSyncer satisfies the narrow RunSync shape used by the scheduler.
var _ interface{ RunSync(context.Context) } = (*ListSyncer)(nil)
