package migrate

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/vavallee/bindery/internal/db"
	"github.com/vavallee/bindery/internal/models"
)

// goodreadsResolvePacing is the minimum gap between provider lookups during a
// Goodreads import. A typical export is hundreds of rows and every row makes
// at least one OpenLibrary call (ISBN lookup, then a title+author search on a
// miss); OpenLibrary throttles aggressive callers. ~400ms ≈ 150 rows/minute
// keeps Bindery a polite client without making a 300-book import feel stalled.
const goodreadsResolvePacing = 400 * time.Millisecond

// GoodreadsOutcome classifies what happened to one row during resolution.
type GoodreadsOutcome string

const (
	// outcomeResolved: a book was matched and is ready to import.
	outcomeResolved GoodreadsOutcome = "resolved"
	// outcomeSkippedShelf: the row's Exclusive Shelf was excluded by the filter.
	outcomeSkippedShelf GoodreadsOutcome = "skipped_shelf"
	// outcomeSkippedExisting: a book with this foreign ID is already tracked.
	outcomeSkippedExisting GoodreadsOutcome = "skipped_existing"
	// outcomeUnresolved: no provider could match the row by ISBN or title+author.
	outcomeUnresolved GoodreadsOutcome = "unresolved"
)

// GoodreadsResolvedRow is the per-row result of the dry-run resolution pass.
// On a successful resolve, Book holds the metadata-provider book (not yet
// persisted); on a failure, Reason explains why.
type GoodreadsResolvedRow struct {
	Row     GoodreadsRow     `json:"row"`
	Outcome GoodreadsOutcome `json:"outcome"`
	Reason  string           `json:"reason,omitempty"`
	// MatchedBy records which path produced the match: "isbn13", "isbn10",
	// or "title+author". Empty for non-resolved rows.
	MatchedBy string `json:"matchedBy,omitempty"`

	// book is the resolved metadata-provider record. Not serialized — it is
	// held server-side between the preview and commit steps. Title/Author are
	// echoed back to the UI via the embedded Row.
	book *models.Book `json:"-"`
}

// GoodreadsPreview is the dry-run result returned before the user commits.
type GoodreadsPreview struct {
	// Token identifies this preview so a follow-up commit reuses the already
	// resolved books instead of re-hitting every metadata provider.
	Token string `json:"token"`

	TotalRows       int `json:"totalRows"`       // data rows in the uploaded CSV
	Resolved        int `json:"resolved"`        // rows ready to be added
	SkippedShelf    int `json:"skippedShelf"`    // filtered out by the shelf filter
	SkippedExisting int `json:"skippedExisting"` // already tracked in Bindery
	Unresolved      int `json:"unresolved"`      // no metadata match

	// ShelfFilter echoes the shelves that were imported, for the UI summary.
	ShelfFilter []string `json:"shelfFilter"`

	// Rows is the full per-row breakdown, ordered by the CSV row number.
	Rows []GoodreadsResolvedRow `json:"rows"`
}

// GoodreadsCommitResult is returned after the resolved books are persisted.
type GoodreadsCommitResult struct {
	Added   int `json:"added"`
	Skipped int `json:"skipped"` // resolved rows that turned out to already exist
	Failed  int `json:"failed"`  // resolved rows that errored on insert

	// Failures lists rows that could not be persisted, "title" → reason.
	Failures map[string]string `json:"failures,omitempty"`
}

// GoodreadsImportOptions controls a Goodreads import run.
type GoodreadsImportOptions struct {
	// Shelves is the set of Exclusive Shelf values to import. Empty means
	// the default: to-read only.
	Shelves []string
}

// shelfSet returns the effective shelf filter as a lookup set. An empty
// option defaults to {to-read}.
func (o GoodreadsImportOptions) shelfSet() map[string]bool {
	set := map[string]bool{}
	for _, s := range o.Shelves {
		s = normalizeGoodreadsShelf(s)
		if s != "" {
			set[s] = true
		}
	}
	if len(set) == 0 {
		set[GoodreadsShelfToRead] = true
	}
	return set
}

// shelfList returns the effective shelf filter as a sorted-ish slice for the
// preview summary.
func (o GoodreadsImportOptions) shelfList() []string {
	set := o.shelfSet()
	out := make([]string, 0, len(set))
	for _, s := range []string{GoodreadsShelfToRead, GoodreadsShelfCurrentlyReading, GoodreadsShelfRead} {
		if set[s] {
			out = append(out, s)
		}
	}
	return out
}

// goodreadsResolver is the metadata capability the importer needs. The
// concrete *metadata.Aggregator satisfies it; tests use a fake.
type goodreadsResolver interface {
	ResolveBookByISBN(ctx context.Context, isbn string) (*models.Book, error)
	SearchBooks(ctx context.Context, query string) ([]models.Book, error)
}

// ResolveGoodreadsRows runs the dry-run resolution pass: each in-scope row is
// matched against the metadata providers (ISBN-13 → ISBN-10 → title+author)
// and classified. No data is written. The returned GoodreadsResolvedRow
// values carry the resolved (un-persisted) books so CommitGoodreadsImport can
// reuse them without a second round of provider calls.
//
// pacing is the minimum gap between provider lookups; pass 0 in tests to run
// without delay. Rows skipped by the shelf filter or already-tracked rows
// cost no provider call.
func ResolveGoodreadsRows(
	ctx context.Context,
	rows []GoodreadsRow,
	opts GoodreadsImportOptions,
	resolver goodreadsResolver,
	books *db.BookRepo,
	pacing time.Duration,
) []GoodreadsResolvedRow {
	shelves := opts.shelfSet()
	out := make([]GoodreadsResolvedRow, 0, len(rows))

	var ticker *time.Ticker
	if pacing > 0 {
		ticker = time.NewTicker(pacing)
		defer ticker.Stop()
	}
	pace := func() {
		if ticker == nil {
			return
		}
		select {
		case <-ticker.C:
		case <-ctx.Done():
		}
	}

	for _, row := range rows {
		resolved := GoodreadsResolvedRow{Row: row}

		// Shelf filter — cheap, no provider call.
		shelf := row.ExclusiveShelf
		if shelf == "" {
			shelf = GoodreadsShelfToRead // exports occasionally omit it
		}
		if !shelves[shelf] {
			resolved.Outcome = outcomeSkippedShelf
			resolved.Reason = fmt.Sprintf("shelf %q not selected", shelf)
			out = append(out, resolved)
			continue
		}

		if err := ctx.Err(); err != nil {
			resolved.Outcome = outcomeUnresolved
			resolved.Reason = "import cancelled"
			out = append(out, resolved)
			continue
		}

		pace()
		book, matchedBy := resolveGoodreadsRow(ctx, row, resolver)
		if book == nil {
			resolved.Outcome = outcomeUnresolved
			resolved.Reason = goodreadsUnresolvedReason(row)
			out = append(out, resolved)
			continue
		}

		// Already tracked? GetByForeignID is the same dedupe the Hardcover
		// list syncer uses.
		if book.ForeignID != "" && books != nil {
			if existing, _ := books.GetByForeignID(ctx, book.ForeignID); existing != nil {
				resolved.Outcome = outcomeSkippedExisting
				resolved.Reason = "already in your library"
				resolved.MatchedBy = matchedBy
				out = append(out, resolved)
				continue
			}
		}

		resolved.Outcome = outcomeResolved
		resolved.MatchedBy = matchedBy
		resolved.book = book
		out = append(out, resolved)
	}
	return out
}

// resolveGoodreadsRow attempts ISBN-13, then ISBN-10, then a title+author
// search. Returns the matched book and the path that produced it, or
// (nil, "") on a complete miss.
func resolveGoodreadsRow(ctx context.Context, row GoodreadsRow, resolver goodreadsResolver) (*models.Book, string) {
	if isbn := strings.TrimSpace(row.ISBN13); isbn != "" {
		if book, err := resolver.ResolveBookByISBN(ctx, isbn); err != nil {
			slog.Debug("goodreads import: isbn13 lookup failed", "isbn", isbn, "error", err)
		} else if book != nil {
			return book, "isbn13"
		}
	}
	if isbn := strings.TrimSpace(row.ISBN); isbn != "" {
		if book, err := resolver.ResolveBookByISBN(ctx, isbn); err != nil {
			slog.Debug("goodreads import: isbn10 lookup failed", "isbn", isbn, "error", err)
		} else if book != nil {
			return book, "isbn10"
		}
	}
	// Title+author fallback — the path that carries most ISBN-sparse exports.
	if book := resolveGoodreadsByTitleAuthor(ctx, row, resolver); book != nil {
		return book, "title+author"
	}
	return nil, ""
}

// resolveGoodreadsByTitleAuthor searches the primary provider by "title author"
// and picks the first result whose author carries a usable foreign ID (so the
// author can be canonicalised the same way manual add-book does). A result
// with no author identity is unusable for import and is skipped.
func resolveGoodreadsByTitleAuthor(ctx context.Context, row GoodreadsRow, resolver goodreadsResolver) *models.Book {
	title := strings.TrimSpace(row.Title)
	if title == "" {
		return nil
	}
	query := title
	if author := strings.TrimSpace(row.Author); author != "" {
		query = title + " " + author
	}
	results, err := resolver.SearchBooks(ctx, query)
	if err != nil {
		slog.Debug("goodreads import: title+author search failed", "query", query, "error", err)
		return nil
	}
	for i := range results {
		book := results[i]
		if book.Author == nil || strings.TrimSpace(book.Author.ForeignID) == "" {
			continue
		}
		return &book
	}
	return nil
}

// goodreadsUnresolvedReason produces a human-readable failure reason for a row
// that no provider could match — shown in the preview and the failed-rows CSV.
func goodreadsUnresolvedReason(row GoodreadsRow) string {
	if strings.TrimSpace(row.ISBN13) == "" && strings.TrimSpace(row.ISBN) == "" {
		return "no metadata match (row has no ISBN; title+author search found nothing)"
	}
	return "no metadata match for ISBN or title+author"
}

// CommitGoodreadsImport persists every resolved row as a monitored, wanted
// book. Authors are looked up by foreign ID and a minimal record is created
// when missing — the same canonicalisation-by-OL-id path the Hardcover list
// syncer uses, so a Goodreads author and a manually-added author of the same
// person collapse onto one row. Books are never auto-grabbed here; they land
// as Wanted and the normal search loop picks them up.
func CommitGoodreadsImport(
	ctx context.Context,
	resolvedRows []GoodreadsResolvedRow,
	authors *db.AuthorRepo,
	books *db.BookRepo,
) GoodreadsCommitResult {
	result := GoodreadsCommitResult{Failures: map[string]string{}}

	for _, rr := range resolvedRows {
		if rr.Outcome != outcomeResolved || rr.book == nil {
			continue
		}
		book := rr.book

		// Re-check existence at commit time — the preview may be minutes old.
		if book.ForeignID != "" {
			if existing, _ := books.GetByForeignID(ctx, book.ForeignID); existing != nil {
				result.Skipped++
				continue
			}
		}

		authorID, err := ensureGoodreadsAuthor(ctx, authors, book)
		if err != nil {
			result.Failed++
			result.Failures[book.Title] = "author: " + err.Error()
			continue
		}

		book.AuthorID = authorID
		book.Monitored = true
		book.Status = models.BookStatusWanted
		if book.Genres == nil {
			book.Genres = []string{}
		}

		if err := books.Create(ctx, book); err != nil {
			if strings.Contains(err.Error(), "UNIQUE constraint failed") {
				// Race with another importer / manual add — treat as skip.
				result.Skipped++
				continue
			}
			result.Failed++
			result.Failures[book.Title] = err.Error()
			continue
		}
		result.Added++
		slog.Info("goodreads import: book added", "title", book.Title, "matchedBy", rr.MatchedBy)
	}
	return result
}

// ensureGoodreadsAuthor resolves the book's author to a Bindery author ID,
// creating a minimal record (canonicalised by OpenLibrary foreign ID) when
// the author is not yet known. Returns the author's database ID.
func ensureGoodreadsAuthor(ctx context.Context, authors *db.AuthorRepo, book *models.Book) (int64, error) {
	if book.Author == nil || strings.TrimSpace(book.Author.ForeignID) == "" {
		return 0, fmt.Errorf("book %q has no resolvable author", book.Title)
	}
	existing, err := authors.GetByAnyForeignID(ctx, book.Author.ForeignID)
	if err != nil {
		return 0, err
	}
	if existing != nil {
		return existing.ID, nil
	}

	author := book.Author
	author.Monitored = true
	if strings.TrimSpace(author.MetadataProvider) == "" {
		author.MetadataProvider = "openlibrary"
	}
	if strings.TrimSpace(author.SortName) == "" {
		author.SortName = goodreadsSortName(author.Name)
	}
	if err := authors.Create(ctx, author); err != nil {
		if isAuthorCreateConflict(err) {
			existing, _ = authors.GetByAnyForeignID(ctx, author.ForeignID)
			if existing != nil {
				return existing.ID, nil
			}
		}
		return 0, fmt.Errorf("create author %q: %w", author.Name, err)
	}
	return author.ID, nil
}

// goodreadsSortName derives a "Last, First" sort name from a display name.
// A single-word name (mononym or organisation) is returned unchanged.
func goodreadsSortName(name string) string {
	parts := strings.Fields(name)
	if len(parts) < 2 {
		return name
	}
	last := parts[len(parts)-1]
	rest := strings.Join(parts[:len(parts)-1], " ")
	return last + ", " + rest
}

// summarisePreview fills the counts on a GoodreadsPreview from its rows.
func summarisePreview(p *GoodreadsPreview) {
	for _, rr := range p.Rows {
		switch rr.Outcome {
		case outcomeResolved:
			p.Resolved++
		case outcomeSkippedShelf:
			p.SkippedShelf++
		case outcomeSkippedExisting:
			p.SkippedExisting++
		case outcomeUnresolved:
			p.Unresolved++
		}
	}
}
