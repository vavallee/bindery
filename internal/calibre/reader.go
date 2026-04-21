package calibre

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	_ "modernc.org/sqlite"
)

// metadataDB is the filename Calibre uses for its library database. The
// library root directory always contains one at this exact name.
const metadataDB = "metadata.db"

// ErrMissingMetadataDB is returned when the library root does not contain a
// metadata.db. Callers surface this as a user-visible error rather than a
// 500 — it usually means the user pointed library_path at the wrong folder.
var ErrMissingMetadataDB = errors.New("calibre metadata.db not found in library_path")

// CalibreBook is the importer-facing view of one Calibre book row, joined
// with its authors, series, identifiers and formats. Field names match
// Bindery conventions (not Calibre's column names) so the importer can pass
// them through without a second translation.
//
// This is intentionally a flat snapshot: Calibre stores everything in one
// database so a single pass can build the full shape, and the importer
// treats each CalibreBook as an atomic unit when upserting.
type CalibreBook struct {
	CalibreID   int64
	Title       string
	SortTitle   string
	PublishDate *time.Time
	ISBN        string
	Language    string // ISO 639-2 code from Calibre's languages table; empty if unset
	Authors     []CalibreAuthor
	Series      *CalibreSeries
	Formats     []CalibreFormat
	CoverPath   string // absolute path to cover.jpg if present; empty if not
	LibraryPath string // absolute path to this book's folder inside the library
}

// CalibreAuthor captures a single authors row. Calibre books can have N
// authors; the importer picks the first as the canonical Bindery author
// and records the rest as aliases.
type CalibreAuthor struct {
	CalibreID int64
	Name      string
	Sort      string
}

// CalibreSeries mirrors Calibre's (name, series_index) tuple.
type CalibreSeries struct {
	Name     string
	Position float64
}

// CalibreFormat is one on-disk file for a Calibre book. Calibre lets a
// single book row carry multiple formats (epub + mobi + pdf); each becomes
// a separate Bindery edition.
type CalibreFormat struct {
	Format       string // uppercase file extension: EPUB, MOBI, PDF, ...
	FileName     string // Calibre's on-disk filename (no extension)
	AbsolutePath string // resolved against library root + book path
	SizeBytes    int64
}

// Reader opens a Calibre library's metadata.db read-only and returns
// populated CalibreBook records. It never mutates the Calibre database —
// we explicitly use `mode=ro&immutable=1` so a concurrent `calibredb`
// invocation from the same Bindery instance cannot deadlock us.
type Reader struct {
	libraryPath string
	db          *sql.DB
}

// OpenReader locates metadata.db inside libraryPath and opens it read-only.
// If metadata.db is absent, returns ErrMissingMetadataDB so the API layer
// can map it to a 400. Callers MUST Close the returned Reader.
func OpenReader(libraryPath string) (*Reader, error) {
	if libraryPath == "" {
		return nil, errors.New("calibre library_path is empty")
	}
	abs, err := filepath.Abs(libraryPath)
	if err != nil {
		return nil, fmt.Errorf("resolve library_path: %w", err)
	}
	dbPath := filepath.Join(abs, metadataDB)
	if _, err := os.Stat(dbPath); err != nil {
		if os.IsNotExist(err) {
			return nil, fmt.Errorf("%w: %s", ErrMissingMetadataDB, dbPath)
		}
		return nil, fmt.Errorf("stat %s: %w", dbPath, err)
	}
	// immutable=1 tells SQLite the file will not change under us, letting
	// it skip locking and rollback journal checks — safe here because we
	// only read, and Calibre's WAL is only active while its own GUI runs.
	conn, err := sql.Open("sqlite", "file:"+dbPath+"?mode=ro&immutable=1")
	if err != nil {
		return nil, fmt.Errorf("open %s: %w", dbPath, err)
	}
	return &Reader{libraryPath: abs, db: conn}, nil
}

// Close releases the SQLite handle. Safe to call on a nil receiver so the
// defer-close pattern works even when OpenReader failed.
func (r *Reader) Close() error {
	if r == nil || r.db == nil {
		return nil
	}
	return r.db.Close()
}

// LibraryPath returns the absolute path the reader was opened against.
// Used by the importer to resolve relative format paths.
func (r *Reader) LibraryPath() string { return r.libraryPath }

// Count returns the total number of books the library contains. Used by
// the importer to seed its progress total before the streaming read.
func (r *Reader) Count(ctx context.Context) (int, error) {
	var n int
	if err := r.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM books`).Scan(&n); err != nil {
		return 0, fmt.Errorf("count books: %w", err)
	}
	return n, nil
}

// Books streams every book row from the library, one at a time, into fn.
// Returning an error from fn aborts the walk and surfaces that error to
// the caller.
//
// Implementation note: we materialise the headline books list first (all
// columns from the books + series join) and close the rows cursor before
// issuing per-book follow-up queries for authors, formats, and ISBNs.
// Calibre ships one SQLite file shared across the app, so nested queries
// on a single connection deadlock — buffering the book list keeps the
// reader compatible with constrained connection pools.
func (r *Reader) Books(ctx context.Context, fn func(CalibreBook) error) error {
	headers, err := r.listBookHeaders(ctx)
	if err != nil {
		return err
	}

	for _, cb := range headers {
		authors, err := r.loadAuthors(ctx, cb.CalibreID)
		if err != nil {
			return err
		}
		cb.Authors = authors

		cb.Formats, err = r.loadFormats(ctx, cb.CalibreID, cb.LibraryPath)
		if err != nil {
			return err
		}

		cb.ISBN, err = r.loadISBN(ctx, cb.CalibreID)
		if err != nil {
			return err
		}

		cb.Language, err = r.loadLanguage(ctx, cb.CalibreID)
		if err != nil {
			return err
		}

		if cover := filepath.Join(cb.LibraryPath, "cover.jpg"); fileExists(cover) {
			cb.CoverPath = cover
		}

		if err := fn(cb); err != nil {
			return err
		}
	}
	return nil
}

// listBookHeaders reads the books table + series join into a slice so the
// outer iterator never holds rows open while child queries fire.
func (r *Reader) listBookHeaders(ctx context.Context) ([]CalibreBook, error) {
	rows, err := r.db.QueryContext(ctx, `
		SELECT b.id, b.title, b.sort, b.pubdate, b.path, b.series_index,
		       COALESCE(s.name, '')
		FROM books b
		LEFT JOIN books_series_link bsl ON bsl.book = b.id
		LEFT JOIN series s               ON s.id   = bsl.series
		ORDER BY b.id`)
	if err != nil {
		return nil, fmt.Errorf("query books: %w", err)
	}
	defer rows.Close()

	var out []CalibreBook
	for rows.Next() {
		var (
			cb          CalibreBook
			pubdate     sql.NullString
			relPath     string
			seriesIndex sql.NullFloat64
			seriesName  string
		)
		if err := rows.Scan(&cb.CalibreID, &cb.Title, &cb.SortTitle, &pubdate,
			&relPath, &seriesIndex, &seriesName); err != nil {
			return nil, fmt.Errorf("scan book: %w", err)
		}
		cb.PublishDate = parseCalibreDate(pubdate.String)
		cb.LibraryPath = filepath.Join(r.libraryPath, relPath)
		if seriesName != "" {
			cb.Series = &CalibreSeries{
				Name:     seriesName,
				Position: seriesIndex.Float64,
			}
		}
		out = append(out, cb)
	}
	return out, rows.Err()
}

func (r *Reader) loadAuthors(ctx context.Context, bookID int64) ([]CalibreAuthor, error) {
	rows, err := r.db.QueryContext(ctx, `
		SELECT a.id, a.name, COALESCE(a.sort, '')
		FROM authors a
		JOIN books_authors_link bal ON bal.author = a.id
		WHERE bal.book = ?
		ORDER BY bal.id`, bookID)
	if err != nil {
		return nil, fmt.Errorf("load authors for book %d: %w", bookID, err)
	}
	defer rows.Close()

	var out []CalibreAuthor
	for rows.Next() {
		var a CalibreAuthor
		if err := rows.Scan(&a.CalibreID, &a.Name, &a.Sort); err != nil {
			return nil, fmt.Errorf("scan author: %w", err)
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

func (r *Reader) loadFormats(ctx context.Context, bookID int64, bookPath string) ([]CalibreFormat, error) {
	rows, err := r.db.QueryContext(ctx, `
		SELECT format, name, uncompressed_size
		FROM data WHERE book = ?
		ORDER BY id`, bookID)
	if err != nil {
		return nil, fmt.Errorf("load formats for book %d: %w", bookID, err)
	}
	defer rows.Close()

	var out []CalibreFormat
	for rows.Next() {
		var f CalibreFormat
		if err := rows.Scan(&f.Format, &f.FileName, &f.SizeBytes); err != nil {
			return nil, fmt.Errorf("scan format: %w", err)
		}
		f.Format = strings.ToUpper(strings.TrimSpace(f.Format))
		// Calibre stores the format file as `<name>.<format>` inside the
		// per-book directory. Build the absolute path so callers don't have
		// to replicate the convention.
		if f.Format != "" && f.FileName != "" && bookPath != "" {
			f.AbsolutePath = filepath.Join(bookPath, f.FileName+"."+strings.ToLower(f.Format))
		}
		out = append(out, f)
	}
	return out, rows.Err()
}

// loadISBN returns the first ISBN identifier (type='isbn') for the book, or
// empty string if none exists. Calibre occasionally stores two ISBNs on one
// book (one per format); we keep the first since Bindery's editions model
// already captures the variance.
func (r *Reader) loadISBN(ctx context.Context, bookID int64) (string, error) {
	var isbn string
	row := r.db.QueryRowContext(ctx,
		`SELECT val FROM identifiers WHERE book = ? AND type = 'isbn' ORDER BY id LIMIT 1`, bookID)
	err := row.Scan(&isbn)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("load isbn for book %d: %w", bookID, err)
	}
	return isbn, nil
}

// loadLanguage returns the primary ISO 639-2 language code for the book from
// Calibre's languages table, or empty string if none is set. Calibre stores
// codes as three-letter ISO 639-2 strings (e.g. "eng", "deu", "fra") which
// are the same format Bindery uses, so no translation is needed.
func (r *Reader) loadLanguage(ctx context.Context, bookID int64) (string, error) {
	var lang string
	row := r.db.QueryRowContext(ctx, `
		SELECT l.lang_code
		FROM books_languages_link bll
		JOIN languages l ON l.id = bll.lang_code
		WHERE bll.book = ?
		ORDER BY bll.item_order
		LIMIT 1`, bookID)
	err := row.Scan(&lang)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	if err != nil {
		// Older Calibre libraries (pre-0.7.x) don't have the languages table.
		if strings.Contains(err.Error(), "no such table") {
			return "", nil
		}
		return "", fmt.Errorf("load language for book %d: %w", bookID, err)
	}
	return lang, nil
}

// parseCalibreDate tolerates the three pubdate formats Calibre has shipped:
// RFC3339, RFC3339 with a +00:00 offset (pre-2020), and an all-day date
// without a time component. Returns nil for the special sentinel 0101-01-01
// Calibre uses to mean "no pubdate".
func parseCalibreDate(s string) *time.Time {
	s = strings.TrimSpace(s)
	if s == "" || strings.HasPrefix(s, "0101-01-01") {
		return nil
	}
	layouts := []string{
		time.RFC3339Nano,
		time.RFC3339,
		"2006-01-02 15:04:05.000000-07:00",
		"2006-01-02 15:04:05-07:00",
		"2006-01-02 15:04:05",
		"2006-01-02",
	}
	for _, layout := range layouts {
		if t, err := time.Parse(layout, s); err == nil {
			t = t.UTC()
			return &t
		}
	}
	return nil
}

func fileExists(p string) bool {
	info, err := os.Stat(p)
	return err == nil && !info.IsDir()
}
