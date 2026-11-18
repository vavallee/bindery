package db

import (
	"context"
	"database/sql"
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/vavallee/bindery/internal/models"
)

// The book SELECT used to read the first file of each format from two CTEs
// built over the whole book_files table. It now uses correlated subqueries.
// These constants are the previous text, kept as an oracle: the test below
// runs every BookRepo read through both and requires identical results.
const (
	oldBookCTE = `WITH first_ebook AS (
	SELECT book_id, path FROM book_files
	WHERE format = 'ebook'
	GROUP BY book_id HAVING id = MIN(id)
),
first_audiobook AS (
	SELECT book_id, path FROM book_files
	WHERE format = 'audiobook'
	GROUP BY book_id HAVING id = MIN(id)
)`
	oldBookJoins = `LEFT JOIN first_ebook    fe ON fe.book_id = books.id
LEFT JOIN first_audiobook fa ON fa.book_id = books.id
LEFT JOIN authors         au ON au.id = books.author_id`
)

const oldBookColumns = `books.id, books.foreign_id, books.author_id, books.title, books.sort_title,
	books.original_title, books.description, books.image_url, books.release_date,
	books.genres, books.average_rating, books.ratings_count, books.monitored, books.status,
	books.any_edition_ok, books.selected_edition_id, books.file_path, books.language,
	books.media_type, books.narrator, books.duration_seconds, books.asin,
	books.calibre_id, books.metadata_provider, books.last_metadata_refresh_at,
	books.created_at, books.updated_at, CAST(books.updated_at AS TEXT),
	COALESCE(NULLIF(books.ebook_file_path, ''), fe.path, ''),
	COALESCE(NULLIF(books.audiobook_file_path, ''), fa.path, ''),
	books.excluded, COALESCE(books.dedup_key, ''),
	au.id, au.foreign_id, au.name, au.sort_name,
	COALESCE(books.owner_user_id, 0),
	COALESCE(books.locked_fields, '[]')`

// oldShapeExec rewrites the current book SELECT text back to the previous CTE
// shape, so the real repo methods run unchanged against it.
type oldShapeExec struct {
	db       *sql.DB
	rewrites int
}

func (o *oldShapeExec) rewrite(q string) string {
	if !strings.Contains(q, bookJoins) {
		return q
	}
	o.rewrites++
	if !strings.Contains(q, bookColumns) && !strings.Contains(q, "SELECT COUNT(*)") {
		panic("book query without bookColumns cannot be mapped to the old shape: " + q)
	}
	q = strings.Replace(q, bookColumns, oldBookColumns, 1)
	q = strings.Replace(q, bookJoins, oldBookJoins, 1)
	return oldBookCTE + q
}
func (o *oldShapeExec) ExecContext(ctx context.Context, q string, a ...any) (sql.Result, error) {
	return o.db.ExecContext(ctx, q, a...)
}
func (o *oldShapeExec) QueryContext(ctx context.Context, q string, a ...any) (*sql.Rows, error) {
	return o.db.QueryContext(ctx, o.rewrite(q), a...)
}
func (o *oldShapeExec) QueryRowContext(ctx context.Context, q string, a ...any) *sql.Row {
	return o.db.QueryRowContext(ctx, o.rewrite(q), a...)
}

func seedEquivalenceLibrary(t *testing.T, database *sql.DB) {
	t.Helper()
	mustExec := func(q string, args ...any) {
		t.Helper()
		if _, err := database.Exec(q, args...); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	mustExec(`INSERT INTO users (id, username, password_hash) VALUES (1, 'u1', 'x'), (2, 'u2', 'x')`)
	for i := 1; i <= 3; i++ {
		mustExec(`INSERT INTO authors (id, foreign_id, name, sort_name, monitored) VALUES (?, ?, ?, ?, 1)`,
			i, fmt.Sprintf("OL%dA", i), fmt.Sprintf("Author %d Tolkien", i), fmt.Sprintf("Author%d", i))
	}
	statuses := []string{"wanted", "imported", "skipped"}
	media := []string{"ebook", "audiobook", "both", ""}
	storedE := []any{"", "", "/stored/e/%d.epub"}
	storedA := []any{"", "/stored/a/%d"}
	owners := []any{nil, 1, 2}
	n := 0
	addFile := func(bookID int64, format, path string) {
		mustExec(`INSERT INTO book_files (book_id, format, path) VALUES (?, ?, ?)`, bookID, format, path)
	}
	// Files for a book are inserted in descending path order and interleaved
	// with filler rows of other books, so MIN(id) is neither the smallest path
	// nor a contiguous id.
	for rep := 0; rep < 2; rep++ {
		for _, ne := range []int{0, 1, 3} {
			for _, na := range []int{0, 1, 2} {
				for si := range storedE {
					for sj := range storedA {
						n++
						title := "The Hobbit " + fmt.Sprint(n)
						if n%7 == 0 {
							title = "Müller story " + fmt.Sprint(n)
						}
						se, sa := storedE[si], storedA[sj]
						if s, ok := se.(string); ok && strings.Contains(s, "%d") {
							se = fmt.Sprintf(s, n)
						}
						if s, ok := sa.(string); ok && strings.Contains(s, "%d") {
							sa = fmt.Sprintf(s, n)
						}
						res, err := database.Exec(`INSERT INTO books (foreign_id, author_id, title, sort_title, status, monitored,
						media_type, excluded, owner_user_id, ebook_file_path, audiobook_file_path, release_date, average_rating, ratings_count, dedup_key)
						VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
							fmt.Sprintf("FB%d", n), 1+n%3, title, title, statuses[n%3], n%4 != 0, media[n%4], n%11 == 0, owners[n%3],
							se, sa, nilIf(n%5 != 0, fmt.Sprintf("20%02d-01-01", n%30)), float64(n%6), n%9, fmt.Sprintf("k%d", n))
						if err != nil {
							t.Fatal(err)
						}
						id, _ := res.LastInsertId()
						for k := ne; k >= 1; k-- {
							p := fmt.Sprintf("/lib/%d/book-%d.ext", n, k)
							if n%13 == 0 && k == ne {
								// the first row inserted has an empty path (the column is unique,
								// so only one book can) or a path that sorts last
								p = fmt.Sprintf("/lib/%d/zzz", n)
								if n == 13 {
									p = ""
								}
							}
							addFile(id, "ebook", p)
							if k > 1 {
								addFile(1, "ebook", fmt.Sprintf("/filler/%d/%d", n, k))
							}
						}
						for k := na; k >= 1; k-- {
							addFile(id, "audiobook", fmt.Sprintf("/lib/%d/audio-%d", n, k))
							addFile(2, "audiobook", fmt.Sprintf("/filler/a/%d/%d", n, k))
						}
					}
				}
			}
		}
	}
	// Deleted files leave id gaps: the next row becomes the first.
	mustExec(`DELETE FROM book_files WHERE path LIKE '/filler/%' AND id % 3 = 0`)
	mustExec(`DELETE FROM book_files WHERE id = (SELECT MIN(id) FROM book_files WHERE book_id = 20 AND format = 'ebook')`)
	// An empty path that is FIRST for its book, with a real path after it.
	res, err := database.Exec(`INSERT INTO books (foreign_id, author_id, title, sort_title, status, monitored, media_type) VALUES ('EMPTYFIRST', 1, 'Empty first', 'Empty first', 'wanted', 1, 'both')`)
	if err != nil {
		t.Fatal(err)
	}
	id, _ := res.LastInsertId()
	// path is UNIQUE and book 13 already holds the one empty path, so free it first.
	mustExec(`UPDATE book_files SET path = '/was-empty' WHERE path = ''`)
	addFile(id, "ebook", "")
	addFile(id, "ebook", "/real/after-empty")
	addFile(id, "audiobook", "/real/audio-only")
}

func nilIf(cond bool, v any) any {
	if cond {
		return v
	}
	return nil
}

func TestBookQueries_EquivalentToCTEShape(t *testing.T) {
	database, err := OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	seedEquivalenceLibrary(t, database)
	ctx := context.Background()

	newRepo := NewBookRepo(database)
	oldExec := &oldShapeExec{db: database}
	oldRepo := NewBookRepo(database)
	oldRepo.exec = oldExec

	var total, withFiles, storedEmptyWithFile int
	_ = database.QueryRow(`SELECT COUNT(*) FROM books`).Scan(&total)
	_ = database.QueryRow(`SELECT COUNT(*) FROM books WHERE id IN (SELECT book_id FROM book_files)`).Scan(&withFiles)
	_ = database.QueryRow(`SELECT COUNT(*) FROM books WHERE COALESCE(ebook_file_path,'') = '' AND id IN (SELECT book_id FROM book_files WHERE format='ebook')`).Scan(&storedEmptyWithFile)
	if total < 100 || withFiles < 75 || storedEmptyWithFile < 30 {
		t.Fatalf("fixture too thin: books=%d withFiles=%d storedEmptyWithFile=%d", total, withFiles, storedEmptyWithFile)
	}

	rowsCompared := 0
	check := func(name string, run func(r *BookRepo) (any, error)) {
		t.Helper()
		want, werr := run(oldRepo)
		got, gerr := run(newRepo)
		if (werr == nil) != (gerr == nil) {
			t.Errorf("%s: error mismatch old=%v new=%v", name, werr, gerr)
			return
		}
		if !reflect.DeepEqual(want, got) {
			t.Errorf("%s: results differ\nold: %+v\nnew: %+v", name, want, got)
			return
		}
		if s := reflect.ValueOf(want); s.Kind() == reflect.Slice {
			rowsCompared += s.Len()
		} else {
			rowsCompared++
		}
	}
	type pageOut struct {
		Books []models.Book
		Total int
	}

	// Every column of every row, straight from the SELECT text.
	rawDump := func(cols, joins, prefix string) [][]any {
		rows, err := database.Query(prefix + " SELECT " + cols + " FROM books " + joins + " ORDER BY books.id")
		if err != nil {
			t.Fatal(err)
		}
		defer rows.Close()
		ct, _ := rows.ColumnTypes()
		var out [][]any
		for rows.Next() {
			vals := make([]any, len(ct))
			ptrs := make([]any, len(ct))
			for i := range vals {
				ptrs[i] = &vals[i]
			}
			if err := rows.Scan(ptrs...); err != nil {
				t.Fatal(err)
			}
			for i, v := range vals {
				if b, ok := v.([]byte); ok {
					vals[i] = string(b)
				}
			}
			out = append(out, vals)
		}
		if err := rows.Err(); err != nil {
			t.Fatal(err)
		}
		return out
	}
	oldRaw := rawDump(oldBookColumns, oldBookJoins, oldBookCTE)
	newRaw := rawDump(bookColumns, bookJoins, bookCTE)
	if len(oldRaw) != total || len(newRaw) != total {
		t.Fatalf("raw rows old=%d new=%d want %d", len(oldRaw), len(newRaw), total)
	}
	cells := 0
	for i := range oldRaw {
		if len(oldRaw[i]) != len(newRaw[i]) {
			t.Fatalf("column count differs: %d vs %d", len(oldRaw[i]), len(newRaw[i]))
		}
		for c := range oldRaw[i] {
			cells++
			if !reflect.DeepEqual(oldRaw[i][c], newRaw[i][c]) {
				t.Errorf("row %d col %d: old %#v new %#v", i, c, oldRaw[i][c], newRaw[i][c])
			}
		}
	}
	t.Logf("raw: %d rows x %d columns = %d cells identical", len(oldRaw), len(oldRaw[0]), cells)

	check("List", func(r *BookRepo) (any, error) { return r.List(ctx) })
	check("ListIncludingExcluded", func(r *BookRepo) (any, error) { return r.ListIncludingExcluded(ctx) })
	for _, u := range []int64{0, 1, 2} {
		check(fmt.Sprintf("ListByUser(%d)", u), func(r *BookRepo) (any, error) { return r.ListByUser(ctx, u) })
		for _, st := range []string{"wanted", "imported", "skipped"} {
			check(fmt.Sprintf("ListByStatusAndUser(%s,%d)", st, u), func(r *BookRepo) (any, error) { return r.ListByStatusAndUser(ctx, st, u) })
			check(fmt.Sprintf("ListByStatusIncludingExcludedAndUser(%s,%d)", st, u), func(r *BookRepo) (any, error) {
				return r.ListByStatusIncludingExcludedAndUser(ctx, st, u)
			})
		}
		for a := int64(1); a <= 3; a++ {
			check(fmt.Sprintf("ListByAuthorAndUser(%d,%d)", a, u), func(r *BookRepo) (any, error) { return r.ListByAuthorAndUser(ctx, a, u) })
		}
		for _, off := range []int{0, 7, 150, 100000} {
			check(fmt.Sprintf("ListPage(%d,%d)", u, off), func(r *BookRepo) (any, error) {
				b, n, err := r.ListPage(ctx, u, 25, off)
				return pageOut{b, n}, err
			})
		}
	}
	for a := int64(1); a <= 4; a++ {
		check(fmt.Sprintf("ListByAuthor(%d)", a), func(r *BookRepo) (any, error) { return r.ListByAuthor(ctx, a) })
		check(fmt.Sprintf("ListByAuthorIncludingExcluded(%d)", a), func(r *BookRepo) (any, error) { return r.ListByAuthorIncludingExcluded(ctx, a) })
	}
	for _, st := range []string{"wanted", "imported", "skipped"} {
		check("ListByStatus "+st, func(r *BookRepo) (any, error) { return r.ListByStatus(ctx, st) })
	}
	check("ListWithFilesWithoutCover", func(r *BookRepo) (any, error) { return r.ListWithFilesWithoutCover(ctx) })
	check("ListWithLocalImagePath", func(r *BookRepo) (any, error) { return r.ListWithLocalImagePath(ctx) })

	var ids []int64
	for id := int64(1); id <= int64(total)+2; id++ {
		ids = append(ids, id)
		check(fmt.Sprintf("GetByID(%d)", id), func(r *BookRepo) (any, error) { return r.GetByID(ctx, id) })
	}
	check("GetByIDs", func(r *BookRepo) (any, error) { return r.GetByIDs(ctx, ids) })
	for n := 1; n <= 30; n++ {
		fid := fmt.Sprintf("FB%d", n)
		check("GetByForeignID "+fid, func(r *BookRepo) (any, error) { return r.GetByForeignID(ctx, fid) })
		check("GetByAnyForeignID "+fid, func(r *BookRepo) (any, error) { return r.GetByAnyForeignID(ctx, fid) })
		for _, u := range []int64{0, 1, 2} {
			check(fmt.Sprintf("GetByForeignIDForUser %s %d", fid, u), func(r *BookRepo) (any, error) { return r.GetByForeignIDForUser(ctx, fid, u) })
			check(fmt.Sprintf("GetByForeignIDVisibleTo %s %d", fid, u), func(r *BookRepo) (any, error) { return r.GetByForeignIDVisibleTo(ctx, fid, u) })
		}
	}
	check("GetByForeignID missing", func(r *BookRepo) (any, error) { return r.GetByForeignID(ctx, "nope") })
	check("GetByAnyForeignID missing", func(r *BookRepo) (any, error) { return r.GetByAnyForeignID(ctx, "nope") })
	check("GetByForeignID EMPTYFIRST", func(r *BookRepo) (any, error) { return r.GetByForeignID(ctx, "EMPTYFIRST") })

	// The filtered pager: every sort x status x media type x search x monitored x user.
	sorts := []string{"", "title-az", "title-za", "date-new", "date-old", "author-az", "author-za", "type-az", "type-za",
		"status-az", "status-za", "rating-asc", "rating-desc"}
	no := false
	pages, combo := 0, 0
	for _, so := range sorts {
		for _, st := range []string{"", "imported", "wanted", "skipped"} {
			for _, mt := range []string{"", "ebook", "audiobook", "both"} {
				for _, se := range []string{"", "tolkien hobbit", "muller"} {
					for _, mo := range []*bool{nil, &no} {
						for _, u := range []int64{0, 1} {
							f := BookListFilter{UserID: u, Search: se, Status: st, MediaType: mt, Monitored: mo, Sort: so}
							for _, pg := range [][2]int{{20, 40}} {
								// The full grid is 2,500 pages: too slow under -race, so
								// walk every 17th combination, which spreads
								// the sample across the sorts, statuses and media types.
								if combo++; combo%17 != 0 {
									continue
								}
								pages++
								check(fmt.Sprintf("ListPageFiltered %+v %v", f, pg), func(r *BookRepo) (any, error) {
									b, n, err := r.ListPageFiltered(ctx, f, pg[0], pg[1])
									return pageOut{b, n}, err
								})
							}
						}
					}
				}
			}
		}
	}
	f := BookListFilter{ReleaseFrom: "2005-01-01", ReleaseBefore: "2015-01-01", Sort: "date-new"}
	check("ListPageFiltered release", func(r *BookRepo) (any, error) {
		b, n, err := r.ListPageFiltered(ctx, f, 500, 0)
		return pageOut{b, n}, err
	})
	if oldExec.rewrites == 0 {
		t.Fatal("old-shape executor rewrote nothing; oracle is not exercising the old SQL")
	}
	t.Logf("compared %d result rows across %d filtered pages and all other shapes; %d statements ran in the old CTE shape", rowsCompared, pages, oldExec.rewrites)
}
