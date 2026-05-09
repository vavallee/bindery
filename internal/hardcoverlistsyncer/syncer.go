// Package hardcoverlistsyncer syncs Hardcover reading lists into Bindery's
// book catalogue as "wanted" books.
package hardcoverlistsyncer

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"

	"github.com/vavallee/bindery/internal/db"
	"github.com/vavallee/bindery/internal/metadata/hardcover"
	"github.com/vavallee/bindery/internal/models"
)

// ListSyncer syncs enabled Hardcover import lists into Bindery's book catalogue.
type ListSyncer struct {
	importLists *db.ImportListRepo
	authors     *db.AuthorRepo
	books       *db.BookRepo
}

// New creates a new ListSyncer.
func New(importLists *db.ImportListRepo, authors *db.AuthorRepo, books *db.BookRepo) *ListSyncer {
	return &ListSyncer{
		importLists: importLists,
		authors:     authors,
		books:       books,
	}
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
	ErrNotFound  = errors.New("import list not found")
	ErrWrongType = errors.New("import list is not a hardcover list")
)

func (s *ListSyncer) syncList(ctx context.Context, il models.ImportList) error {
	client := hardcover.NewAuthenticated(il.APIKey)

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
		slog.Info("imported book from hardcover list", "title", book.Title, "author_id", authorID)
	}

	return nil
}

// ensureAuthor looks up the author by foreign ID, creating a minimal record if
// missing. Returns the author's database ID.
func (s *ListSyncer) ensureAuthor(ctx context.Context, book *models.Book) (int64, error) {
	if book.Author == nil {
		return 0, fmt.Errorf("book %q has no author metadata", book.Title)
	}

	existing, err := s.authors.GetByForeignID(ctx, book.Author.ForeignID)
	if err != nil {
		return 0, err
	}
	if existing != nil {
		return existing.ID, nil
	}

	// Create a minimal author record
	author := book.Author
	author.Monitored = true
	author.MetadataProvider = "hardcover"
	if author.SortName == "" {
		author.SortName = sortName(author.Name)
	}

	if err := s.authors.Create(ctx, author); err != nil {
		if strings.Contains(err.Error(), "UNIQUE constraint failed") {
			// Race: author created between our check and insert
			existing, _ = s.authors.GetByForeignID(ctx, author.ForeignID)
			if existing != nil {
				return existing.ID, nil
			}
		}
		return 0, fmt.Errorf("create author %q: %w", author.Name, err)
	}
	slog.Info("created author from hardcover list", "name", author.Name, "foreignID", author.ForeignID)
	return author.ID, nil
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
