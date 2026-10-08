package opds

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/vavallee/bindery/internal/models"
)

var errCovStore = errors.New("store unavailable")

// covErrBooks wraps fakeBooks and fails the calls named in failing, or
// GetByID for the ids in failIDs.
type covErrBooks struct {
	*fakeBooks
	failing map[string]bool
	failIDs map[int64]bool
}

func (f *covErrBooks) ListByAuthor(ctx context.Context, authorID int64) ([]models.Book, error) {
	if f.failing["ListByAuthor"] {
		return nil, errCovStore
	}
	return f.fakeBooks.ListByAuthor(ctx, authorID)
}

func (f *covErrBooks) ListByAuthorAndUser(ctx context.Context, authorID, userID int64) ([]models.Book, error) {
	if f.failing["ListByAuthorAndUser"] {
		return nil, errCovStore
	}
	return f.fakeBooks.ListByAuthorAndUser(ctx, authorID, userID)
}

func (f *covErrBooks) ListByStatus(ctx context.Context, status string) ([]models.Book, error) {
	if f.failing["ListByStatus"] {
		return nil, errCovStore
	}
	return f.fakeBooks.ListByStatus(ctx, status)
}

func (f *covErrBooks) ListByStatusAndUser(ctx context.Context, status string, userID int64) ([]models.Book, error) {
	if f.failing["ListByStatusAndUser"] {
		return nil, errCovStore
	}
	return f.fakeBooks.ListByStatusAndUser(ctx, status, userID)
}

func (f *covErrBooks) GetByID(ctx context.Context, id int64) (*models.Book, error) {
	if f.failing["GetByID"] || f.failIDs[id] {
		return nil, errCovStore
	}
	return f.fakeBooks.GetByID(ctx, id)
}

type covErrAuthors struct {
	*fakeAuthors
	failing map[string]bool
}

func (f *covErrAuthors) List(ctx context.Context) ([]models.Author, error) {
	if f.failing["List"] {
		return nil, errCovStore
	}
	return f.fakeAuthors.List(ctx)
}

func (f *covErrAuthors) ListByUser(ctx context.Context, userID int64) ([]models.Author, error) {
	if f.failing["ListByUser"] {
		return nil, errCovStore
	}
	return f.fakeAuthors.ListByUser(ctx, userID)
}

func (f *covErrAuthors) GetByID(ctx context.Context, id int64) (*models.Author, error) {
	if f.failing["GetByID"] {
		return nil, errCovStore
	}
	return f.fakeAuthors.GetByID(ctx, id)
}

type covErrSeries struct {
	*fakeSeries
	failing map[string]bool
	failIDs map[int64]bool
}

func (f *covErrSeries) List(ctx context.Context) ([]models.Series, error) {
	if f.failing["List"] {
		return nil, errCovStore
	}
	return f.fakeSeries.List(ctx)
}

func (f *covErrSeries) GetByID(ctx context.Context, id int64) (*models.Series, error) {
	if f.failing["GetByID"] || f.failIDs[id] {
		return nil, errCovStore
	}
	return f.fakeSeries.GetByID(ctx, id)
}

// covMultiUser is a library shared by two users plus one legacy (owner 0)
// book:
//
//	author 3 "Alice Author": book 20 (user 1), book 22 (legacy, owner 0)
//	author 4 "Bob Author":   book 21 (user 2)
//	author 5 "Carol Author": book 23 (user 2, wanted, never in a feed)
//	series 200 "Alpha":  book 20 (user 1)
//	series 201 "Beta":   book 21 (user 2)
//	series 202 "Gamma":  book 22 (legacy)
//	series 203 "Delta":  book 999 (missing row)
func covMultiUser() (*fakeBooks, *fakeAuthors, *fakeSeries) {
	t0 := time.Date(2026, 4, 1, 0, 0, 0, 0, time.UTC)
	imported := func(id, author, owner int64, title string, at time.Time) models.Book {
		return models.Book{
			ID: id, AuthorID: author, OwnerUserID: owner, Title: title,
			FilePath: "/library/" + title + ".epub", Status: models.BookStatusImported,
			UpdatedAt: at,
		}
	}
	books := &fakeBooks{all: []models.Book{
		imported(20, 3, 1, "Alice One", t0),
		imported(21, 4, 2, "Bob One", t0.Add(time.Hour)),
		imported(22, 3, 0, "Alice Legacy", t0.Add(2*time.Hour)),
		{ID: 23, AuthorID: 5, OwnerUserID: 2, Title: "Carol Wanted", Status: models.BookStatusWanted},
	}}
	authors := &fakeAuthors{all: []models.Author{
		{ID: 3, Name: "Alice Author", SortName: "Author, Alice", UpdatedAt: t0},
		{ID: 4, Name: "Bob Author", SortName: "Author, Bob", UpdatedAt: t0},
		{ID: 5, Name: "Carol Author", SortName: "Author, Carol", UpdatedAt: t0},
	}}
	series := &fakeSeries{all: []models.Series{
		{ID: 200, Title: "Alpha", Books: []models.SeriesBook{{SeriesID: 200, BookID: 20, PositionInSeries: "1"}}},
		{ID: 201, Title: "Beta", Books: []models.SeriesBook{{SeriesID: 201, BookID: 21}}},
		{ID: 202, Title: "Gamma", Books: []models.SeriesBook{{SeriesID: 202, BookID: 22}}},
		{ID: 203, Title: "Delta", Books: []models.SeriesBook{{SeriesID: 203, BookID: 999}}},
	}}
	return books, authors, series
}

func covEntryIDs(f Feed) []string {
	out := make([]string, 0, len(f.Entries))
	for _, e := range f.Entries {
		out = append(out, e.ID)
	}
	return out
}

func covSameIDs(t *testing.T, what string, got []string, want ...string) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("%s ids = %v, want %v", what, got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("%s ids = %v, want %v", what, got, want)
		}
	}
}

func TestBuildAuthors_ScopedToUser(t *testing.T) {
	books, authors, series := covMultiUser()
	b := NewBuilder(Config{}, books, authors, series)
	ctx := context.Background()

	// User 1 sees Alice (own book plus legacy) but not Bob (user 2's only book)
	// and not Carol (no imported books at all).
	f, err := b.BuildAuthors(ctx, "http://h/", 1, 1)
	if err != nil {
		t.Fatal(err)
	}
	covSameIDs(t, "user 1 authors", covEntryIDs(f), "urn:bindery:author:3")
	if f.TotalResults != 1 {
		t.Errorf("TotalResults = %d, want 1", f.TotalResults)
	}

	// User 2 sees Alice through the legacy owner-0 book, and Bob.
	f, err = b.BuildAuthors(ctx, "http://h", 0, 2)
	if err != nil {
		t.Fatal(err)
	}
	covSameIDs(t, "user 2 authors", covEntryIDs(f), "urn:bindery:author:3", "urn:bindery:author:4")

	// Unscoped (admin) sees both too, never Carol.
	f, err = b.BuildAuthors(ctx, "http://h", 1, 0)
	if err != nil {
		t.Fatal(err)
	}
	covSameIDs(t, "admin authors", covEntryIDs(f), "urn:bindery:author:3", "urn:bindery:author:4")
	if f.Title != "Bindery — Authors" {
		t.Errorf("default title not applied: %q", f.Title)
	}
	if f.ItemsPerPage != 50 {
		t.Errorf("default page size not applied: %d", f.ItemsPerPage)
	}
}

func TestBuildAuthor_ScopedToUser(t *testing.T) {
	books, authors, series := covMultiUser()
	b := NewBuilder(Config{}, books, authors, series)
	ctx := context.Background()

	// Bob's only book belongs to user 2: user 1 must get ErrNotFound, not an
	// empty feed that confirms the author exists.
	if _, err := b.BuildAuthor(ctx, "http://h", 4, 1); !errors.Is(err, ErrNotFound) {
		t.Fatalf("user 1 BuildAuthor(bob) err = %v, want ErrNotFound", err)
	}
	f, err := b.BuildAuthor(ctx, "http://h", 4, 2)
	if err != nil {
		t.Fatal(err)
	}
	covSameIDs(t, "user 2 bob", covEntryIDs(f), "urn:bindery:book:21")

	// User 2 sees only the legacy book of Alice, not user 1's book.
	f, err = b.BuildAuthor(ctx, "http://h", 3, 2)
	if err != nil {
		t.Fatal(err)
	}
	covSameIDs(t, "user 2 alice", covEntryIDs(f), "urn:bindery:book:22")
	if len(f.Entries[0].Authors) != 1 || f.Entries[0].Authors[0].Name != "Alice Author" {
		t.Errorf("entry authors = %+v", f.Entries[0].Authors)
	}

	// Admin sees both of Alice's books.
	f, err = b.BuildAuthor(ctx, "http://h", 3, 0)
	if err != nil {
		t.Fatal(err)
	}
	covSameIDs(t, "admin alice", covEntryIDs(f), "urn:bindery:book:20", "urn:bindery:book:22")
}

func TestBuildSeriesList_ScopedToUser(t *testing.T) {
	books, authors, series := covMultiUser()
	b := NewBuilder(Config{}, books, authors, series)
	ctx := context.Background()

	f, err := b.BuildSeriesList(ctx, "http://h", 1, 1)
	if err != nil {
		t.Fatal(err)
	}
	// Alpha (own) and Gamma (legacy); Beta is user 2's and Delta points at a
	// book row that no longer exists.
	covSameIDs(t, "user 1 series", covEntryIDs(f), "urn:bindery:series:200", "urn:bindery:series:202")

	f, err = b.BuildSeriesList(ctx, "http://h", 1, 2)
	if err != nil {
		t.Fatal(err)
	}
	covSameIDs(t, "user 2 series", covEntryIDs(f), "urn:bindery:series:201", "urn:bindery:series:202")

	// Unscoped passes the list through untouched.
	f, err = b.BuildSeriesList(ctx, "http://h", 1, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(f.Entries) != 4 {
		t.Errorf("admin series = %v, want all 4", covEntryIDs(f))
	}
}

func TestBuildSeriesList_SkipsSeriesAndBooksThatFailToLoad(t *testing.T) {
	books, authors, series := covMultiUser()
	eb := &covErrBooks{fakeBooks: books, failIDs: map[int64]bool{22: true}}
	es := &covErrSeries{fakeSeries: series, failIDs: map[int64]bool{200: true}}
	b := NewBuilder(Config{}, eb, authors, es)

	f, err := b.BuildSeriesList(context.Background(), "http://h", 1, 1)
	if err != nil {
		t.Fatal(err)
	}
	// Alpha's GetByID fails and Gamma's only book fails to load: nothing is
	// left that user 1 can be shown to own.
	if len(f.Entries) != 0 {
		t.Errorf("entries = %v, want none", covEntryIDs(f))
	}
}

func TestBuildSeries_ScopedToUser(t *testing.T) {
	books, authors, series := covMultiUser()
	b := NewBuilder(Config{}, books, authors, series)
	ctx := context.Background()

	if _, err := b.BuildSeries(ctx, "http://h", 201, 1); !errors.Is(err, ErrNotFound) {
		t.Fatalf("user 1 BuildSeries(beta) err = %v, want ErrNotFound", err)
	}
	f, err := b.BuildSeries(ctx, "http://h", 200, 1)
	if err != nil {
		t.Fatal(err)
	}
	covSameIDs(t, "alpha", covEntryIDs(f), "urn:bindery:book:20")
	if f.Entries[0].Title != "1. Alice One" {
		t.Errorf("title = %q, want position prefix", f.Entries[0].Title)
	}
	// Admin view of a series whose only book row is missing is an empty feed,
	// not an error.
	f, err = b.BuildSeries(ctx, "http://h", 203, 0)
	if err != nil || len(f.Entries) != 0 {
		t.Errorf("admin delta = %v, %v; want empty feed", covEntryIDs(f), err)
	}
	if _, err := b.BuildSeries(ctx, "http://h", 999, 0); !errors.Is(err, ErrNotFound) {
		t.Errorf("missing series err = %v, want ErrNotFound", err)
	}
}

func TestBuildRecent_ScopedToUser(t *testing.T) {
	books, authors, series := covMultiUser()
	b := NewBuilder(Config{}, books, authors, series)
	ctx := context.Background()

	f, err := b.BuildRecent(ctx, "http://h", 1)
	if err != nil {
		t.Fatal(err)
	}
	// Newest first: the legacy book is newer than user 1's own.
	covSameIDs(t, "user 1 recent", covEntryIDs(f), "urn:bindery:book:22", "urn:bindery:book:20")

	f, err = b.BuildRecent(ctx, "http://h", 2)
	if err != nil {
		t.Fatal(err)
	}
	covSameIDs(t, "user 2 recent", covEntryIDs(f), "urn:bindery:book:22", "urn:bindery:book:21")
}

func TestBuildRecent_CapsAtFifty(t *testing.T) {
	t0 := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	books := &fakeBooks{}
	for i := int64(1); i <= 60; i++ {
		books.all = append(books.all, models.Book{
			ID: i, Title: "B", FilePath: "/x.epub", Status: models.BookStatusImported,
			UpdatedAt: t0.Add(time.Duration(i) * time.Minute),
		})
	}
	b := NewBuilder(Config{}, books, &fakeAuthors{}, &fakeSeries{})
	f, err := b.BuildRecent(context.Background(), "http://h", 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(f.Entries) != 50 {
		t.Fatalf("entries = %d, want 50", len(f.Entries))
	}
	if f.Entries[0].ID != "urn:bindery:book:60" || f.Entries[49].ID != "urn:bindery:book:11" {
		t.Errorf("window = %s .. %s, want 60 .. 11", f.Entries[0].ID, f.Entries[49].ID)
	}
}

func TestBuildBook_ScopedToUser(t *testing.T) {
	books, authors, series := covMultiUser()
	b := NewBuilder(Config{}, books, authors, series)
	ctx := context.Background()

	if _, err := b.BuildBook(ctx, "http://h", 21, 1); !errors.Is(err, ErrNotFound) {
		t.Fatalf("user 1 BuildBook(21) err = %v, want ErrNotFound", err)
	}
	f, err := b.BuildBook(ctx, "http://h", 21, 2)
	if err != nil {
		t.Fatal(err)
	}
	covSameIDs(t, "own book", covEntryIDs(f), "urn:bindery:book:21")
	// Legacy books are visible to every user.
	if _, err := b.BuildBook(ctx, "http://h", 22, 1); err != nil {
		t.Errorf("legacy book for user 1: %v", err)
	}
	// A wanted book has no acquisition link.
	f, err = b.BuildBook(ctx, "http://h", 23, 0)
	if err != nil {
		t.Fatal(err)
	}
	if hasRel(f.Entries[0].Links, RelAcquisition) {
		t.Error("wanted book advertises a download link")
	}
}

func TestBuilders_PropagateStoreErrors(t *testing.T) {
	ctx := context.Background()
	newB := func(bf, af, sf map[string]bool) *Builder {
		books, authors, series := covMultiUser()
		return NewBuilder(Config{},
			&covErrBooks{fakeBooks: books, failing: bf},
			&covErrAuthors{fakeAuthors: authors, failing: af},
			&covErrSeries{fakeSeries: series, failing: sf})
	}
	cases := []struct {
		name string
		run  func() error
	}{
		{"authors list", func() error {
			_, err := newB(nil, map[string]bool{"List": true}, nil).BuildAuthors(ctx, "", 1, 0)
			return err
		}},
		{"authors list by user", func() error {
			_, err := newB(nil, map[string]bool{"ListByUser": true}, nil).BuildAuthors(ctx, "", 1, 7)
			return err
		}},
		{"author get", func() error {
			_, err := newB(nil, map[string]bool{"GetByID": true}, nil).BuildAuthor(ctx, "", 3, 0)
			return err
		}},
		{"author books", func() error {
			_, err := newB(map[string]bool{"ListByAuthor": true}, nil, nil).BuildAuthor(ctx, "", 3, 0)
			return err
		}},
		{"author books by user", func() error {
			_, err := newB(map[string]bool{"ListByAuthorAndUser": true}, nil, nil).BuildAuthor(ctx, "", 3, 1)
			return err
		}},
		{"series list", func() error {
			_, err := newB(nil, nil, map[string]bool{"List": true}).BuildSeriesList(ctx, "", 1, 0)
			return err
		}},
		{"series get", func() error {
			_, err := newB(nil, nil, map[string]bool{"GetByID": true}).BuildSeries(ctx, "", 200, 0)
			return err
		}},
		{"recent", func() error {
			_, err := newB(map[string]bool{"ListByStatus": true}, nil, nil).BuildRecent(ctx, "", 0)
			return err
		}},
		{"recent by user", func() error {
			_, err := newB(map[string]bool{"ListByStatusAndUser": true}, nil, nil).BuildRecent(ctx, "", 1)
			return err
		}},
		{"book get", func() error {
			_, err := newB(map[string]bool{"GetByID": true}, nil, nil).BuildBook(ctx, "", 20, 0)
			return err
		}},
	}
	for _, tc := range cases {
		if err := tc.run(); !errors.Is(err, errCovStore) {
			t.Errorf("%s: err = %v, want wrapped store error", tc.name, err)
		}
	}
}

func TestBuildAuthors_DropsAuthorsWhoseBooksFailToLoad(t *testing.T) {
	books, authors, series := covMultiUser()
	b := NewBuilder(Config{}, &covErrBooks{fakeBooks: books, failing: map[string]bool{"ListByAuthor": true}}, authors, series)
	f, err := b.BuildAuthors(context.Background(), "http://h", 1, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(f.Entries) != 0 {
		t.Errorf("entries = %v, want none when book lookups fail", covEntryIDs(f))
	}
}

func TestBuildSeries_SkipsBooksThatFailToLoad(t *testing.T) {
	books, authors, series := covMultiUser()
	b := NewBuilder(Config{}, &covErrBooks{fakeBooks: books, failIDs: map[int64]bool{20: true}}, authors, series)
	// User 1's only book in Alpha cannot be read, so the series reads as
	// not found for them rather than an empty feed.
	if _, err := b.BuildSeries(context.Background(), "http://h", 200, 1); !errors.Is(err, ErrNotFound) {
		t.Errorf("err = %v, want ErrNotFound", err)
	}
}

func TestHelpers_EdgeCases(t *testing.T) {
	if got := nonEmpty(); got != "" {
		t.Errorf("nonEmpty() = %q", got)
	}
	if got := nonEmpty(" ", ""); got != "" {
		t.Errorf("nonEmpty(blank) = %q, want last value", got)
	}
	if got := nonEmpty("", "  ", "fallback"); got != "fallback" {
		t.Errorf("nonEmpty fallback = %q", got)
	}
	if got := nonEmpty("", "Name", "x"); got != "Name" {
		t.Errorf("nonEmpty first = %q", got)
	}
	for in, want := range map[int]int{-3: 1, 0: 1, 1: 1, 7: 7} {
		if got := normalizePage(in); got != want {
			t.Errorf("normalizePage(%d) = %d, want %d", in, got, want)
		}
	}
	if descriptionContent("  \n ") != nil {
		t.Error("blank description should yield nil content")
	}
	if c := descriptionContent(" hi "); c == nil || c.Body != "hi" || c.Type != "text" {
		t.Errorf("descriptionContent = %+v", c)
	}
	if got := rfc3339(time.Time{}); got != "1970-01-01T00:00:00Z" {
		t.Errorf("rfc3339(zero) = %q", got)
	}
	imgs := map[string]string{
		"https://x/a.PNG":           "image/png",
		"https://x/a.gif?v=1":       "image/gif",
		"https://x/a.webp":          "image/webp",
		"https://x/a.svg":           "image/svg+xml",
		"https://x/a.jpg":           "image/jpeg",
		"https://x/cover":           "image/jpeg",
		"bindery-cover:abc.png?x=y": "image/png",
	}
	for in, want := range imgs {
		if got := guessImageType(in); got != want {
			t.Errorf("guessImageType(%q) = %q, want %q", in, got, want)
		}
	}
	files := map[string]string{
		"/a/b.mobi": "application/x-mobipocket-ebook",
		"/a/b.AZW3": "application/vnd.amazon.ebook",
		"/a/b.azw":  "application/vnd.amazon.ebook",
		"/a/b.cbz":  "application/vnd.comicbook+zip",
		"/a/b.cbr":  "application/vnd.comicbook-rar",
		"/a/b.txt":  "text/plain",
		"/a/b.fb2":  "application/x-fictionbook+xml",
		"/a/b.pdf":  "application/pdf",
		"/a/b.djvu": "application/octet-stream",
	}
	for in, want := range files {
		if got := guessFileType(in, models.MediaTypeEbook); got != want {
			t.Errorf("guessFileType(%q) = %q, want %q", in, got, want)
		}
	}
	if got := guessFileType("/a/folder", models.MediaTypeAudiobook); got != "application/zip" {
		t.Errorf("audiobook type = %q, want application/zip", got)
	}
}
