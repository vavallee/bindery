package opds

import (
	"context"
	"database/sql"
	"fmt"
	"reflect"
	"sort"
	"testing"
	"time"

	"github.com/vavallee/bindery/internal/db"
	"github.com/vavallee/bindery/internal/models"
)

// The three stores below answer the list questions the way the builder did
// before it asked the database: one query per author, the whole imported
// table sorted in Go, one series and its books at a time. They are the
// reference the SQL has to match, entry for entry.

type legacyAuthors struct {
	*db.AuthorRepo
	books *db.BookRepo
}

func (s legacyAuthors) listBooks(ctx context.Context, authorID, userID int64) ([]models.Book, error) {
	if userID != 0 {
		return s.books.ListByAuthorAndUser(ctx, authorID, userID)
	}
	return s.books.ListByAuthor(ctx, authorID)
}

func (s legacyAuthors) ListWithDownloadableBooks(ctx context.Context, userID int64) ([]models.Author, error) {
	var (
		all []models.Author
		err error
	)
	if userID != 0 {
		all, err = s.ListByUser(ctx, userID)
	} else {
		all, err = s.List(ctx)
	}
	if err != nil {
		return nil, err
	}
	kept := make([]models.Author, 0, len(all))
	for _, a := range all {
		books, err := s.listBooks(ctx, a.ID, userID)
		if err != nil {
			continue
		}
		if len(filterImported(books)) == 0 {
			continue
		}
		kept = append(kept, a)
	}
	return kept, nil
}

type legacyBooks struct{ *db.BookRepo }

func (s legacyBooks) ListRecentDownloadable(ctx context.Context, userID int64, limit int) ([]models.Book, error) {
	var (
		books []models.Book
		err   error
	)
	if userID != 0 {
		books, err = s.ListByStatusAndUser(ctx, models.BookStatusImported, userID)
	} else {
		books, err = s.ListByStatus(ctx, models.BookStatusImported)
	}
	if err != nil {
		return nil, err
	}
	books = filterImported(books)
	sort.SliceStable(books, func(i, j int) bool {
		if books[i].UpdatedAt.Equal(books[j].UpdatedAt) {
			return books[i].ID > books[j].ID
		}
		return books[i].UpdatedAt.After(books[j].UpdatedAt)
	})
	if len(books) > limit {
		books = books[:limit]
	}
	return books, nil
}

type legacySeries struct {
	*db.SeriesRepo
	books *db.BookRepo
}

func (s legacySeries) IDsWithBooksVisibleTo(ctx context.Context, userID int64) (map[int64]struct{}, error) {
	list, err := s.List(ctx)
	if err != nil {
		return nil, err
	}
	ids := map[int64]struct{}{}
	for _, one := range list {
		full, err := s.GetByID(ctx, one.ID)
		if err != nil || full == nil {
			continue
		}
		for _, sb := range full.Books {
			bk, err := s.books.GetByID(ctx, sb.BookID)
			if err != nil || bk == nil {
				continue
			}
			if bk.OwnerUserID == 0 || bk.OwnerUserID == userID {
				ids[one.ID] = struct{}{}
				break
			}
		}
	}
	return ids, nil
}

// equivalenceLibrary is the seeded library both builders read.
type equivalenceLibrary struct {
	database *sql.DB
	books    *db.BookRepo
	authors  *db.AuthorRepo
	series   *db.SeriesRepo
	users    [2]int64
}

func (l equivalenceLibrary) exec(t *testing.T, q string, args ...any) {
	t.Helper()
	if _, err := l.database.Exec(q, args...); err != nil {
		t.Fatalf("%s: %v", q, err)
	}
}

// seedEquivalenceLibrary builds 140 authors (three pages of 50) owned by
// nobody, user 1 or user 2, with every book shape the feeds have to tell
// apart: wanted, imported without a file, excluded, unmonitored, owned by the
// other user. Sort names collide in case, carry accents and are sometimes
// empty, so the in-Go ordering is exercised. updated_at packs far more than 50
// downloadable books into single seconds, written with the fraction lengths
// RFC3339Nano produces, which is where text order and time order part.
func seedEquivalenceLibrary(t *testing.T) equivalenceLibrary {
	t.Helper()
	database, err := db.OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { database.Close() })
	lib := equivalenceLibrary{
		database: database,
		books:    db.NewBookRepo(database),
		authors:  db.NewAuthorRepo(database),
		series:   db.NewSeriesRepo(database),
	}
	ctx := context.Background()
	users := db.NewUserRepo(database)
	for i, name := range []string{"alice", "bob"} {
		u, err := users.Create(ctx, name, "hash")
		if err != nil {
			t.Fatal(err)
		}
		lib.users[i] = u.ID
	}
	// owner maps 0, 1, 2 to nobody, alice, bob.
	owner := func(n int) any {
		if n%3 == 0 {
			return nil
		}
		return lib.users[n%3-1]
	}

	sortNames := []string{"smith, a", "Smith, A", "SMITH, A", "Élan, Zoé", "élan, zoé", "", "Åberg, K", "aberg, k"}
	fractions := []string{"", ".5", ".55", ".123456789", ".1234567", ".05", ".9", ".000001"}
	base := time.Date(2026, 11, 20, 10, 0, 0, 0, time.UTC)

	bookN := 0
	var seriesIDs []int64
	for a := 0; a < 140; a++ {
		sortName := fmt.Sprintf("Author %03d", (a*37)%140)
		if a%6 == 0 {
			sortName = sortNames[(a/6)%len(sortNames)]
		}
		author := &models.Author{
			ForeignID: fmt.Sprintf("eq:author:%d", a),
			Name:      fmt.Sprintf("Name %03d", a),
			SortName:  sortName,
		}
		if err := lib.authors.Create(ctx, author); err != nil {
			t.Fatal(err)
		}
		lib.exec(t, "UPDATE authors SET owner_user_id = ? WHERE id = ?", owner(a), author.ID)

		if a%10 == 0 {
			seriesIDs = append(seriesIDs, 0)
			s := &models.Series{ForeignID: fmt.Sprintf("eq:series:%d", a), Title: fmt.Sprintf("Series %02d", (a*7)%29)}
			if err := lib.series.Create(ctx, s); err != nil {
				t.Fatal(err)
			}
			seriesIDs[len(seriesIDs)-1] = s.ID
		}

		// a%7 == 3 leaves the author with no books at all.
		for k := 0; k < (a%7+4)%7; k++ {
			bookN++
			book := &models.Book{
				ForeignID: fmt.Sprintf("eq:book:%d", bookN),
				AuthorID:  author.ID,
				Title:     fmt.Sprintf("Book %04d", bookN),
				Status:    models.BookStatusWanted,
			}
			if err := lib.books.Create(ctx, book); err != nil {
				t.Fatal(err)
			}
			status, filePath, excluded, monitored := models.BookStatusImported, fmt.Sprintf("/library/%d.epub", bookN), 0, 1
			switch bookN % 11 {
			case 0:
				status, filePath = models.BookStatusWanted, ""
			case 1:
				filePath = "" // imported, but nothing to download
			case 2:
				excluded = 1
			case 3:
				monitored = 0
			case 4:
				status = models.BookStatusWanted // wanted, yet a file is recorded
			}
			// Sixty books to a second, so the 50th newest always sits inside one.
			second := base.Add(time.Duration(bookN/60) * time.Second)
			updatedAt := second.Format("2006-01-02T15:04:05") + fractions[bookN%len(fractions)] + "Z"
			// The author's owner and the book's owner move independently, so
			// some authors a user can see hold only the other user's books.
			lib.exec(t, `UPDATE books SET status = ?, file_path = ?, excluded = ?, monitored = ?,
				owner_user_id = ?, updated_at = ? WHERE id = ?`,
				status, filePath, excluded, monitored, owner(bookN/2), updatedAt, book.ID)

			if bookN%4 == 0 {
				sid := seriesIDs[bookN%len(seriesIDs)]
				if err := lib.series.LinkBook(ctx, sid, book.ID, fmt.Sprint(bookN%9), true); err != nil {
					t.Fatal(err)
				}
			}
		}
	}
	// One series with no books, and one whose only book belongs to bob.
	if err := lib.series.Create(ctx, &models.Series{ForeignID: "eq:series:empty", Title: "Empty"}); err != nil {
		t.Fatal(err)
	}
	bobOnly := &models.Series{ForeignID: "eq:series:bob", Title: "Bob only"}
	if err := lib.series.Create(ctx, bobOnly); err != nil {
		t.Fatal(err)
	}
	var bobBook int64
	if err := database.QueryRow(`SELECT id FROM books WHERE owner_user_id = ?
		AND id NOT IN (SELECT book_id FROM series_books) LIMIT 1`, lib.users[1]).Scan(&bobBook); err != nil {
		t.Fatal(err)
	}
	if err := lib.series.LinkBook(ctx, bobOnly.ID, bobBook, "1", true); err != nil {
		t.Fatal(err)
	}
	return lib
}

// TestListFeeds_MatchPerItemBuilder pins that the feeds built from one query
// are the feeds the per-item builder produced: same entries, same order, same
// paging links and counts, for the unscoped caller and for each user.
func TestListFeeds_MatchPerItemBuilder(t *testing.T) {
	prev := nowRFC3339
	nowRFC3339 = func() string { return "2026-11-27T00:00:00Z" }
	t.Cleanup(func() { nowRFC3339 = prev })

	lib := seedEquivalenceLibrary(t)
	ctx := context.Background()
	const base = "http://host"

	current := NewBuilder(Config{PageSize: 50}, lib.books, lib.authors, lib.series)
	reference := NewBuilder(Config{PageSize: 50},
		legacyBooks{lib.books},
		legacyAuthors{lib.authors, lib.books},
		legacySeries{lib.series, lib.books})

	type build func(b *Builder) (Feed, error)
	check := func(name string, minEntries int, fn build) Feed {
		t.Helper()
		want, err := fn(reference)
		if err != nil {
			t.Fatalf("%s: reference: %v", name, err)
		}
		got, err := fn(current)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if len(want.Entries) < minEntries {
			t.Fatalf("%s: the reference feed has %d entries, the seed should give at least %d", name, len(want.Entries), minEntries)
		}
		if !reflect.DeepEqual(got, want) {
			t.Errorf("%s differs from the per-item builder\n got: %v\nwant: %v", name, covEntryIDs(got), covEntryIDs(want))
		}
		return got
	}

	seen := map[string]map[int64]bool{}
	for _, uid := range []int64{0, lib.users[0], lib.users[1]} {
		authorsSeen := 0
		for page := 0; page <= 4; page++ {
			minEntries := 0
			if page <= 1 {
				minEntries = 1
			}
			f := check(fmt.Sprintf("authors user=%d page=%d", uid, page), minEntries, func(b *Builder) (Feed, error) {
				return b.BuildAuthors(ctx, base, page, uid)
			})
			if page >= 1 {
				authorsSeen += len(f.Entries)
			}
			if page == 1 && f.TotalResults <= 50 {
				t.Errorf("authors user=%d: %d authors, the seed should fill more than one page", uid, f.TotalResults)
			}
			if page == 4 && authorsSeen != f.TotalResults {
				t.Errorf("authors user=%d: pages hold %d entries, total says %d", uid, authorsSeen, f.TotalResults)
			}
		}
		for page := 1; page <= 2; page++ {
			check(fmt.Sprintf("series user=%d page=%d", uid, page), 2-page, func(b *Builder) (Feed, error) {
				return b.BuildSeriesList(ctx, base, page, uid)
			})
		}
		recent := check(fmt.Sprintf("recent user=%d", uid), 50, func(b *Builder) (Feed, error) {
			return b.BuildRecent(ctx, base, uid)
		})
		seen[fmt.Sprint(uid)] = map[int64]bool{}
		for _, e := range recent.Entries {
			var id int64
			if _, err := fmt.Sscanf(e.ID, "urn:bindery:book:%d", &id); err != nil {
				t.Fatal(err)
			}
			seen[fmt.Sprint(uid)][id] = true
		}
	}

	// The scoped feeds are not just equal to the reference, they are scoped:
	// neither user's recent feed carries a book the other owns.
	for i, uid := range lib.users {
		other := lib.users[1-i]
		for id := range seen[fmt.Sprint(uid)] {
			var ownerID sql.NullInt64
			if err := lib.database.QueryRow("SELECT owner_user_id FROM books WHERE id = ?", id).Scan(&ownerID); err != nil {
				t.Fatal(err)
			}
			if ownerID.Valid && ownerID.Int64 == other {
				t.Errorf("user %d's recent feed lists book %d, which user %d owns", uid, id, other)
			}
		}
	}
	if reflect.DeepEqual(seen[fmt.Sprint(lib.users[0])], seen[fmt.Sprint(lib.users[1])]) {
		t.Error("both users got the same recent feed, the seed should separate them")
	}
}

// TestRecentFeed_MatchesPerItemBuilderOnASmallLibrary covers the library with
// fewer downloadable books than the feed holds, where there is no 50th row to
// bound the query by.
func TestRecentFeed_MatchesPerItemBuilderOnASmallLibrary(t *testing.T) {
	prev := nowRFC3339
	nowRFC3339 = func() string { return "2026-11-27T00:00:00Z" }
	t.Cleanup(func() { nowRFC3339 = prev })

	lib := seedEquivalenceLibrary(t)
	// Keep 21 downloadable books.
	lib.exec(t, `UPDATE books SET status = 'wanted' WHERE id NOT IN (
		SELECT id FROM books WHERE status = 'imported' AND file_path != '' AND excluded = 0 AND monitored = 1
		ORDER BY id LIMIT 21)`)
	ctx := context.Background()
	current := NewBuilder(Config{}, lib.books, lib.authors, lib.series)
	reference := NewBuilder(Config{}, legacyBooks{lib.books}, lib.authors, lib.series)
	for _, uid := range []int64{0, lib.users[0], lib.users[1]} {
		want, err := reference.BuildRecent(ctx, "http://host", uid)
		if err != nil {
			t.Fatal(err)
		}
		got, err := current.BuildRecent(ctx, "http://host", uid)
		if err != nil {
			t.Fatal(err)
		}
		if len(want.Entries) == 0 || len(want.Entries) > 21 {
			t.Fatalf("user %d: reference has %d entries, want 1 to 21", uid, len(want.Entries))
		}
		if !reflect.DeepEqual(got, want) {
			t.Errorf("user %d: recent differs\n got: %v\nwant: %v", uid, covEntryIDs(got), covEntryIDs(want))
		}
	}
}

// countingAuthors and countingBooks count the store calls a feed makes.
type countingAuthors struct {
	AuthorStore
	calls int
}

func (c *countingAuthors) ListWithDownloadableBooks(ctx context.Context, userID int64) ([]models.Author, error) {
	c.calls++
	return c.AuthorStore.ListWithDownloadableBooks(ctx, userID)
}

func (c *countingAuthors) GetByID(ctx context.Context, id int64) (*models.Author, error) {
	c.calls++
	return c.AuthorStore.GetByID(ctx, id)
}

type countingBooks struct {
	BookStore
	calls int
}

func (c *countingBooks) ListByAuthor(ctx context.Context, authorID int64) ([]models.Book, error) {
	c.calls++
	return c.BookStore.ListByAuthor(ctx, authorID)
}

func (c *countingBooks) ListByAuthorAndUser(ctx context.Context, authorID, userID int64) ([]models.Book, error) {
	c.calls++
	return c.BookStore.ListByAuthorAndUser(ctx, authorID, userID)
}

func (c *countingBooks) GetByID(ctx context.Context, id int64) (*models.Book, error) {
	c.calls++
	return c.BookStore.GetByID(ctx, id)
}

func (c *countingBooks) ListRecentDownloadable(ctx context.Context, userID int64, limit int) ([]models.Book, error) {
	c.calls++
	return c.BookStore.ListRecentDownloadable(ctx, userID, limit)
}

// TestListFeeds_StoreCallsDoNotGrowWithTheLibrary is the guard against the
// query per author coming back: the authors feed is one store call however
// many authors there are, and the recent feed is one call plus one per
// distinct author on the page.
func TestListFeeds_StoreCallsDoNotGrowWithTheLibrary(t *testing.T) {
	ctx := context.Background()
	for _, n := range []int64{10, 400} {
		books := &fakeBooks{}
		authors := &fakeAuthors{books: books}
		t0 := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
		for i := int64(1); i <= n; i++ {
			authors.all = append(authors.all, models.Author{ID: i, Name: fmt.Sprint("A", i), SortName: fmt.Sprint("a", i)})
			// Two books each, so the recent page holds 25 distinct authors.
			for k := int64(0); k < 2; k++ {
				books.all = append(books.all, models.Book{
					ID: i*2 + k, AuthorID: i, Title: "T", FilePath: "/x.epub",
					Status: models.BookStatusImported, UpdatedAt: t0.Add(time.Duration(i) * time.Minute),
				})
			}
		}
		cb, ca := &countingBooks{BookStore: books}, &countingAuthors{AuthorStore: authors}
		b := NewBuilder(Config{PageSize: 50}, cb, ca, &fakeSeries{})

		f, err := b.BuildAuthors(ctx, "http://h", 1, 0)
		if err != nil {
			t.Fatal(err)
		}
		if f.TotalResults != int(n) {
			t.Fatalf("%d authors: total = %d", n, f.TotalResults)
		}
		if got := cb.calls + ca.calls; got != 1 {
			t.Errorf("%d authors: the authors feed made %d store calls, want 1", n, got)
		}

		cb.calls, ca.calls = 0, 0
		f, err = b.BuildRecent(ctx, "http://h", 0)
		if err != nil {
			t.Fatal(err)
		}
		wantCalls := 1 + min(int(n), recentLimit/2)
		if got := cb.calls + ca.calls; got != wantCalls || len(f.Entries) != min(int(n)*2, recentLimit) {
			t.Errorf("%d authors: the recent feed made %d store calls for %d entries, want %d calls", n, got, len(f.Entries), wantCalls)
		}
	}
}
