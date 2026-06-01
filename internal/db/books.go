package db

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/vavallee/bindery/internal/models"
)

type BookRepo struct {
	db    *sql.DB
	exec  dbExecutor
	files *BookFileRepo
}

func NewBookRepo(db *sql.DB) *BookRepo {
	r := &BookRepo{db: db, files: NewBookFileRepo(db)}
	r.exec = db
	return r
}

// WithTx returns a clone of this repo whose tx-aware methods (the ones
// rolled into calibre.Rollback's single transaction) route through tx
// instead of the bare *sql.DB. Methods outside that set keep using *sql.DB
// — those repos are not currently exercised inside multi-repo transactions.
func (r *BookRepo) WithTx(tx *sql.Tx) *BookRepo {
	clone := *r
	clone.exec = tx
	return &clone
}

// bookCTE is a WITH clause that materialises the first book_files row per
// format for every book. Using CTEs + LEFT JOINs avoids the correlated
// subqueries that previously fired 2 N extra SQLite queries per book list.
const bookCTE = `WITH first_ebook AS (
	SELECT book_id, path FROM book_files
	WHERE format = 'ebook'
	GROUP BY book_id HAVING id = MIN(id)
),
first_audiobook AS (
	SELECT book_id, path FROM book_files
	WHERE format = 'audiobook'
	GROUP BY book_id HAVING id = MIN(id)
)`

// bookColumns is the canonical column list for book SELECT queries.
// ebook_file_path and audiobook_file_path are derived from book_files first
// (so multi-file books see the first registered path), with the legacy column
// as a fallback for rows that pre-date the migration and have not yet been
// re-imported via AddBookFile.
//
// The trailing author columns hydrate book.Author for List/Get responses
// (#882): the frontend's Books page and Book detail page read
// book.author.authorName and were rendering empty for every row when the
// join was missing. LEFT JOIN so an orphan author_id (author row deleted
// but book still references it) returns NULL columns rather than dropping
// the book from the result; the scan loop handles the NULLs.
const bookColumns = `books.id, books.foreign_id, books.author_id, books.title, books.sort_title,
	books.original_title, books.description, books.image_url, books.release_date,
	books.genres, books.average_rating, books.ratings_count, books.monitored, books.status,
	books.any_edition_ok, books.selected_edition_id, books.file_path, books.language,
	books.media_type, books.narrator, books.duration_seconds, books.asin,
	books.calibre_id, books.metadata_provider, books.last_metadata_refresh_at,
	books.created_at, books.updated_at,
	COALESCE(fe.path, COALESCE(books.ebook_file_path, '')),
	COALESCE(fa.path, COALESCE(books.audiobook_file_path, '')),
	books.excluded,
	au.id, au.foreign_id, au.name, au.sort_name,
	COALESCE(books.owner_user_id, 0)`

// bookJoins are the LEFT JOINs that attach the first_ebook and first_audiobook
// CTE results plus the author row to the books table. Must follow the FROM
// books clause.
const bookJoins = `LEFT JOIN first_ebook    fe ON fe.book_id = books.id
LEFT JOIN first_audiobook fa ON fa.book_id = books.id
LEFT JOIN authors         au ON au.id = books.author_id`

func (r *BookRepo) List(ctx context.Context) ([]models.Book, error) {
	return r.query(ctx, bookCTE+" SELECT "+bookColumns+" FROM books "+bookJoins+" WHERE excluded = 0 ORDER BY sort_title", nil)
}

func (r *BookRepo) ListByUser(ctx context.Context, userID int64) ([]models.Book, error) {
	where, args := QueryScopeFor("books.owner_user_id", "WHERE excluded = 0", userID)
	return r.query(ctx, bookCTE+" SELECT "+bookColumns+" FROM books "+bookJoins+" "+where+" ORDER BY sort_title", args)
}

// ListIncludingExcluded returns all books regardless of their excluded flag.
func (r *BookRepo) ListIncludingExcluded(ctx context.Context) ([]models.Book, error) {
	return r.query(ctx, bookCTE+" SELECT "+bookColumns+" FROM books "+bookJoins+" ORDER BY sort_title", nil)
}

func (r *BookRepo) ListByAuthor(ctx context.Context, authorID int64) ([]models.Book, error) {
	return r.query(ctx, bookCTE+" SELECT "+bookColumns+" FROM books "+bookJoins+" WHERE author_id = ? AND excluded = 0 ORDER BY release_date", []any{authorID})
}

func (r *BookRepo) ListByAuthorAndUser(ctx context.Context, authorID, userID int64) ([]models.Book, error) {
	where, args := QueryScopeFor("books.owner_user_id", "WHERE author_id = ? AND excluded = 0", userID, authorID)
	return r.query(ctx, bookCTE+" SELECT "+bookColumns+" FROM books "+bookJoins+" "+where+" ORDER BY release_date", args)
}

// ListByAuthorIncludingExcluded returns all books for an author regardless of excluded flag.
func (r *BookRepo) ListByAuthorIncludingExcluded(ctx context.Context, authorID int64) ([]models.Book, error) {
	return r.query(ctx, bookCTE+" SELECT "+bookColumns+" FROM books "+bookJoins+" WHERE author_id = ? ORDER BY release_date", []any{authorID})
}

func (r *BookRepo) ListByStatus(ctx context.Context, status string) ([]models.Book, error) {
	return r.query(ctx, bookCTE+" SELECT "+bookColumns+" FROM books "+bookJoins+" WHERE status = ? AND books.monitored = 1 AND excluded = 0 ORDER BY sort_title", []any{status})
}

func (r *BookRepo) ListByStatusAndUser(ctx context.Context, status string, userID int64) ([]models.Book, error) {
	where, args := QueryScopeFor("books.owner_user_id", "WHERE status = ? AND books.monitored = 1 AND excluded = 0", userID, status)
	return r.query(ctx, bookCTE+" SELECT "+bookColumns+" FROM books "+bookJoins+" "+where+" ORDER BY sort_title", args)
}

// ListByStatusIncludingExcluded returns books with the given status regardless of excluded flag.
func (r *BookRepo) ListByStatusIncludingExcluded(ctx context.Context, status string) ([]models.Book, error) {
	return r.query(ctx, bookCTE+" SELECT "+bookColumns+" FROM books "+bookJoins+" WHERE status = ? AND books.monitored = 1 ORDER BY sort_title", []any{status})
}

func (r *BookRepo) GetByID(ctx context.Context, id int64) (*models.Book, error) {
	books, err := r.query(ctx, bookCTE+" SELECT "+bookColumns+" FROM books "+bookJoins+" WHERE books.id = ?", []any{id})
	if err != nil {
		return nil, err
	}
	if len(books) == 0 {
		return nil, nil
	}
	return &books[0], nil
}

func (r *BookRepo) GetByForeignID(ctx context.Context, foreignID string) (*models.Book, error) {
	books, err := r.query(ctx, bookCTE+" SELECT "+bookColumns+" FROM books "+bookJoins+" WHERE books.foreign_id = ?", []any{foreignID})
	if err != nil {
		return nil, err
	}
	if len(books) == 0 {
		return nil, nil
	}
	return &books[0], nil
}

func (r *BookRepo) Create(ctx context.Context, b *models.Book) error {
	now := time.Now().UTC()
	genresJSON, err := json.Marshal(b.Genres)
	if err != nil {
		return fmt.Errorf("marshal book genres: %w", err)
	}

	mediaType := b.MediaType
	if mediaType == "" {
		mediaType = models.MediaTypeEbook
	}

	result, err := r.db.ExecContext(ctx, `
		INSERT INTO books (foreign_id, author_id, title, sort_title, original_title, description,
		                   image_url, release_date, genres, average_rating, ratings_count,
		                   monitored, status, any_edition_ok, selected_edition_id,
		                   language, media_type, narrator, duration_seconds, asin,
		                   metadata_provider, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		b.ForeignID, b.AuthorID, b.Title, b.SortTitle, b.OriginalTitle, b.Description,
		b.ImageURL, b.ReleaseDate, string(genresJSON), b.AverageRating, b.RatingsCount,
		b.Monitored, b.Status, b.AnyEditionOK, b.SelectedEditionID,
		b.Language, mediaType, b.Narrator, b.DurationSeconds, b.ASIN,
		b.MetadataProvider, now, now)
	if err != nil {
		return fmt.Errorf("create book: %w", err)
	}

	id, err := result.LastInsertId()
	if err != nil {
		return fmt.Errorf("get book id: %w", err)
	}
	b.ID = id
	b.CreatedAt = now
	b.UpdatedAt = now
	return nil
}

func (r *BookRepo) Update(ctx context.Context, b *models.Book) error {
	now := time.Now().UTC()
	genresJSON, err := json.Marshal(b.Genres)
	if err != nil {
		return fmt.Errorf("marshal book genres: %w", err)
	}

	mediaType := b.MediaType
	if mediaType == "" {
		mediaType = models.MediaTypeEbook
	}

	_, err = r.exec.ExecContext(ctx, `
		UPDATE books SET foreign_id=?, author_id=?, title=?, sort_title=?, original_title=?, description=?, image_url=?,
		                 release_date=?, genres=?, average_rating=?, ratings_count=?,
		                 monitored=?, status=?, any_edition_ok=?, selected_edition_id=?,
		                 file_path=?, language=?, media_type=?, narrator=?, duration_seconds=?, asin=?,
		                 metadata_provider=?, last_metadata_refresh_at=?, updated_at=?,
		                 ebook_file_path=?, audiobook_file_path=?
		WHERE id=?`,
		b.ForeignID, b.AuthorID, b.Title, b.SortTitle, b.OriginalTitle, b.Description, b.ImageURL,
		b.ReleaseDate, string(genresJSON), b.AverageRating, b.RatingsCount,
		b.Monitored, b.Status, b.AnyEditionOK, b.SelectedEditionID,
		b.FilePath, b.Language, mediaType, b.Narrator, b.DurationSeconds, b.ASIN,
		b.MetadataProvider, b.LastMetadataRefreshAt, now,
		b.EbookFilePath, b.AudiobookFilePath, b.ID)
	if err != nil {
		return fmt.Errorf("update book %d: %w", b.ID, err)
	}
	b.UpdatedAt = now
	return nil
}

// MarkWantedMonitored updates only the fields needed to queue a book for
// searching, preserving metadata that may not be present on sparse callers.
func (r *BookRepo) MarkWantedMonitored(ctx context.Context, id int64) error {
	now := time.Now().UTC()
	_, err := r.db.ExecContext(ctx,
		`UPDATE books SET status = ?, monitored = 1, updated_at = ? WHERE id = ?`,
		models.BookStatusWanted, now, id)
	if err != nil {
		return fmt.Errorf("mark book %d wanted: %w", id, err)
	}
	return nil
}

// AddBookFile records a new on-disk file in book_files and refreshes the
// book's aggregate status. Multiple files for the same format are all tracked
// (e.g. epub + mobi + pdf from a multi-file download).
func (r *BookRepo) AddBookFile(ctx context.Context, bookID int64, format, path string) error {
	if err := r.files.Add(ctx, bookID, format, path); err != nil {
		return err
	}
	return r.refreshBookStatus(ctx, bookID)
}

// ListFiles returns all book_files rows for the given book.
func (r *BookRepo) ListFiles(ctx context.Context, bookID int64) ([]models.BookFile, error) {
	return r.files.ListByBook(ctx, bookID)
}

// RemoveBookFile deletes the book_files row for the given on-disk path and
// refreshes the book's aggregate status. Returns the updated book, or nil if
// the path was not in book_files.
func (r *BookRepo) RemoveBookFile(ctx context.Context, path string) (*models.Book, error) {
	bookID, err := r.files.DeleteByPath(ctx, path)
	if err != nil {
		return nil, err
	}
	if bookID == 0 {
		return nil, nil // not tracked
	}
	b, err := r.GetByID(ctx, bookID)
	if err != nil || b == nil {
		return nil, err
	}
	if err := r.refreshBookStatus(ctx, bookID); err != nil {
		return nil, err
	}
	return r.GetByID(ctx, bookID)
}

// ListAllBookFilePaths returns every path in book_files.
// Used by ScanLibrary to build the set of already-tracked files efficiently.
func (r *BookRepo) ListAllBookFilePaths(ctx context.Context) ([]string, error) {
	return r.files.ListAllPaths(ctx)
}

// refreshBookStatus recomputes the aggregate status for a book from its
// current book_files rows and updates both the status and legacy columns.
// It queries book_files directly so the result is always authoritative,
// bypassing the legacy-column fallback in bookColumns.
func (r *BookRepo) refreshBookStatus(ctx context.Context, bookID int64) error {
	b, err := r.GetByID(ctx, bookID)
	if err != nil {
		return fmt.Errorf("refreshBookStatus: load book: %w", err)
	}
	if b == nil {
		return nil
	}

	// Query book_files directly to get the true first path per format.
	// This bypasses the COALESCE legacy-column fallback in bookColumns so
	// removing the last book_files entry correctly clears the field.
	var ebookPath, audiobookPath string
	if err := r.db.QueryRowContext(ctx,
		`SELECT COALESCE(path,'') FROM book_files WHERE book_id=? AND format='ebook' ORDER BY id LIMIT 1`,
		bookID).Scan(&ebookPath); err != nil && !errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("refreshBookStatus: read ebook path: %w", err)
	}
	if err := r.db.QueryRowContext(ctx,
		`SELECT COALESCE(path,'') FROM book_files WHERE book_id=? AND format='audiobook' ORDER BY id LIMIT 1`,
		bookID).Scan(&audiobookPath); err != nil && !errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("refreshBookStatus: read audiobook path: %w", err)
	}

	b.EbookFilePath = ebookPath
	b.AudiobookFilePath = audiobookPath

	// Derive status from which formats still need files.
	if !b.NeedsEbook() && !b.NeedsAudiobook() {
		b.Status = models.BookStatusImported
	} else if b.Status == models.BookStatusImported {
		b.Status = models.BookStatusWanted
	}

	// Keep legacy file_path column in sync with the first available file path.
	if ebookPath != "" {
		b.FilePath = ebookPath
	} else if audiobookPath != "" {
		b.FilePath = audiobookPath
	} else {
		b.FilePath = ""
	}

	return r.Update(ctx, b)
}

// SetFormatFilePath records the on-disk path for a specific format and
// recomputes the book's aggregate status. It now writes to book_files
// (allowing multiple files per format) rather than overwriting a single column.
// Callers processing multiple files should call AddBookFile for each file.
// SetFormatFilePath is retained for single-canonical-path callers (e.g.
// audiobook folder imports, rescan).
func (r *BookRepo) SetFormatFilePath(ctx context.Context, id int64, mediaType, filePath string) error {
	return r.AddBookFile(ctx, id, mediaType, filePath)
}

// SetFilePath is a backward-compatible wrapper around SetFormatFilePath that
// infers the format from the book's current media_type. Callers that know the
// explicit format should use SetFormatFilePath directly.
func (r *BookRepo) SetFilePath(ctx context.Context, id int64, filePath string) error {
	b, err := r.GetByID(ctx, id)
	if err != nil || b == nil {
		// Fall back to the legacy single-column update so existing code paths
		// never break even if the book can't be loaded.
		_, err2 := r.db.ExecContext(ctx, "UPDATE books SET file_path=?, status=? WHERE id=?",
			filePath, models.BookStatusImported, id)
		return err2
	}
	mediaType := b.MediaType
	if mediaType == models.MediaTypeBoth {
		mediaType = models.MediaTypeEbook // shouldn't happen; default to ebook
	}
	return r.SetFormatFilePath(ctx, id, mediaType, filePath)
}

// SetCalibreID stores the Calibre-assigned book id for the given Bindery
// book row. Called from the importer after a successful `calibredb add`.
func (r *BookRepo) SetCalibreID(ctx context.Context, id, calibreID int64) error {
	_, err := r.db.ExecContext(ctx, "UPDATE books SET calibre_id=? WHERE id=?", calibreID, id)
	return err
}

// GetByCalibreID returns the Bindery book row that currently points at the
// given Calibre book id, or nil if none. The library import flow uses this
// as its primary idempotency key — a second import pass sees the existing
// row and updates in place instead of duplicating.
func (r *BookRepo) GetByCalibreID(ctx context.Context, calibreID int64) (*models.Book, error) {
	books, err := r.query(ctx, bookCTE+" SELECT "+bookColumns+" FROM books "+bookJoins+" WHERE calibre_id = ?", []any{calibreID})
	if err != nil {
		return nil, err
	}
	if len(books) == 0 {
		return nil, nil
	}
	return &books[0], nil
}

// FindByAuthorAndTitle locates a book under authorID whose title matches
// `title` case-insensitively. Used by the Calibre importer as a secondary
// dedupe path when the existing row has no calibre_id yet but the user
// (or a previous Bindery ingest) has already filed a book with the same
// title — re-matching by title links the two rows instead of creating a
// duplicate.
func (r *BookRepo) FindByAuthorAndTitle(ctx context.Context, authorID int64, title string) (*models.Book, error) {
	books, err := r.query(ctx,
		bookCTE+" SELECT "+bookColumns+" FROM books "+bookJoins+" WHERE author_id = ? AND LOWER(title) = LOWER(?)",
		[]any{authorID, title})
	if err != nil {
		return nil, err
	}
	if len(books) == 0 {
		return nil, nil
	}
	return &books[0], nil
}

// SetExcluded toggles the excluded flag on a book.
func (r *BookRepo) SetExcluded(ctx context.Context, id int64, excluded bool) error {
	v := 0
	if excluded {
		v = 1
	}
	_, err := r.db.ExecContext(ctx, "UPDATE books SET excluded=?, updated_at=? WHERE id=?", v, time.Now().UTC(), id)
	return err
}

func (r *BookRepo) Delete(ctx context.Context, id int64) error {
	// book_files rows are removed via ON DELETE CASCADE on the FK.
	_, err := r.exec.ExecContext(ctx, "DELETE FROM books WHERE id=?", id)
	return err
}

func (r *BookRepo) query(ctx context.Context, q string, args []any) ([]models.Book, error) {
	var rows *sql.Rows
	var err error
	if args != nil {
		rows, err = r.exec.QueryContext(ctx, q, args...)
	} else {
		rows, err = r.exec.QueryContext(ctx, q)
	}
	if err != nil {
		return nil, fmt.Errorf("query books: %w", err)
	}
	defer rows.Close()

	var books []models.Book
	for rows.Next() {
		var b models.Book
		var monitored, anyEditionOK, excluded int
		var genresStr string
		// Author columns from the LEFT JOIN. Nullable since the join may not
		// match when author_id points at a deleted author row.
		var authorID sql.NullInt64
		var authorForeignID, authorName, authorSortName sql.NullString
		err := rows.Scan(
			&b.ID, &b.ForeignID, &b.AuthorID, &b.Title, &b.SortTitle,
			&b.OriginalTitle, &b.Description, &b.ImageURL, &b.ReleaseDate,
			&genresStr, &b.AverageRating, &b.RatingsCount,
			&monitored, &b.Status, &anyEditionOK, &b.SelectedEditionID,
			&b.FilePath, &b.Language, &b.MediaType,
			&b.Narrator, &b.DurationSeconds, &b.ASIN,
			&b.CalibreID, &b.MetadataProvider, &b.LastMetadataRefreshAt,
			&b.CreatedAt, &b.UpdatedAt,
			&b.EbookFilePath, &b.AudiobookFilePath,
			&excluded,
			&authorID, &authorForeignID, &authorName, &authorSortName,
			&b.OwnerUserID,
		)
		if err != nil {
			return nil, fmt.Errorf("scan book: %w", err)
		}
		b.Monitored = monitored == 1
		b.AnyEditionOK = anyEditionOK == 1
		b.Excluded = excluded == 1
		_ = json.Unmarshal([]byte(genresStr), &b.Genres)
		if b.Genres == nil {
			b.Genres = []string{}
		}
		// Populate the joined-author projection (ID + foreign ID + name +
		// sort name) when the LEFT JOIN found a row. Other Author fields
		// stay zero; the frontend reads authorName and the URL builder
		// uses book.authorId, so a minimal projection is enough. Callers
		// that need the full Author (description, image, ratings, etc.)
		// should still go through AuthorRepo.GetByID.
		if authorID.Valid {
			b.Author = &models.Author{
				ID:        authorID.Int64,
				ForeignID: authorForeignID.String,
				Name:      authorName.String,
				SortName:  authorSortName.String,
			}
		}
		books = append(books, b)
	}
	return books, rows.Err()
}
