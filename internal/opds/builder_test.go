package opds

import (
	"context"
	"encoding/xml"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/vavallee/bindery/internal/models"
)

// --- fakes --------------------------------------------------------------------

type fakeBooks struct {
	all []models.Book
}

func (f *fakeBooks) List(_ context.Context) ([]models.Book, error) {
	out := make([]models.Book, len(f.all))
	copy(out, f.all)
	return out, nil
}
func (f *fakeBooks) ListByAuthor(_ context.Context, authorID int64) ([]models.Book, error) {
	var out []models.Book
	for _, b := range f.all {
		if b.AuthorID == authorID {
			out = append(out, b)
		}
	}
	return out, nil
}
func (f *fakeBooks) ListByStatus(_ context.Context, status string) ([]models.Book, error) {
	var out []models.Book
	for _, b := range f.all {
		if b.Status == status {
			out = append(out, b)
		}
	}
	return out, nil
}
func (f *fakeBooks) GetByID(_ context.Context, id int64) (*models.Book, error) {
	for i := range f.all {
		if f.all[i].ID == id {
			b := f.all[i]
			return &b, nil
		}
	}
	return nil, nil
}

type fakeAuthors struct{ all []models.Author }

func (f *fakeAuthors) List(_ context.Context) ([]models.Author, error) {
	out := make([]models.Author, len(f.all))
	copy(out, f.all)
	return out, nil
}
func (f *fakeAuthors) GetByID(_ context.Context, id int64) (*models.Author, error) {
	for i := range f.all {
		if f.all[i].ID == id {
			a := f.all[i]
			return &a, nil
		}
	}
	return nil, nil
}

type fakeSeries struct {
	all []models.Series
}

func (f *fakeSeries) List(_ context.Context) ([]models.Series, error) {
	out := make([]models.Series, len(f.all))
	copy(out, f.all)
	return out, nil
}
func (f *fakeSeries) GetByID(_ context.Context, id int64) (*models.Series, error) {
	for i := range f.all {
		if f.all[i].ID == id {
			s := f.all[i]
			return &s, nil
		}
	}
	return nil, nil
}

// --- fixture -----------------------------------------------------------------

// fixture returns a small library: two authors, three books (two imported),
// one series containing the two imported books.
func fixture() (*fakeBooks, *fakeAuthors, *fakeSeries) {
	tzero := time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC)
	tlater := time.Date(2026, 3, 2, 9, 0, 0, 0, time.UTC)
	releaseTwo := time.Date(2019, 6, 11, 0, 0, 0, 0, time.UTC)

	a1 := models.Author{ID: 1, Name: "Ada Palmer", SortName: "Palmer, Ada", UpdatedAt: tzero}
	a2 := models.Author{ID: 2, Name: "Becky Chambers", SortName: "Chambers, Becky", UpdatedAt: tzero}

	b1 := models.Book{
		ID: 10, AuthorID: 1, Title: "Too Like the Lightning",
		SortTitle: "too like the lightning",
		FilePath:  "/library/Ada Palmer/Too Like the Lightning.epub",
		Status:    models.BookStatusImported, Language: "eng",
		UpdatedAt: tzero,
	}
	b2 := models.Book{
		ID: 11, AuthorID: 2, Title: "Record of a Spaceborn Few",
		SortTitle: "record of a spaceborn few",
		FilePath:  "/library/Becky Chambers/Record of a Spaceborn Few.epub",
		Status:    models.BookStatusImported, Language: "eng",
		ReleaseDate: &releaseTwo,
		UpdatedAt:   tlater,
	}
	b3 := models.Book{
		ID: 12, AuthorID: 2, Title: "Wanted Book",
		Status:    models.BookStatusWanted, // not imported — must not appear
		UpdatedAt: tzero,
	}

	s1 := models.Series{ID: 100, Title: "Wayfarers", CreatedAt: tzero, Books: []models.SeriesBook{
		{SeriesID: 100, BookID: 11, PositionInSeries: "3", PrimarySeries: true},
	}}

	return &fakeBooks{all: []models.Book{b1, b2, b3}}, &fakeAuthors{all: []models.Author{a1, a2}}, &fakeSeries{all: []models.Series{s1}}
}

func newBuilder() *Builder {
	b, a, s := fixture()
	return NewBuilder(Config{Title: "Bindery", PageSize: 50}, b, a, s)
}

// --- tests -------------------------------------------------------------------

func TestBuildRoot(t *testing.T) {
	b := newBuilder()
	f := b.BuildRoot("http://host:8787")

	if f.Title != "Bindery" {
		t.Errorf("title = %q, want Bindery", f.Title)
	}
	if f.ID != "urn:bindery:opds:root" {
		t.Errorf("id = %q", f.ID)
	}
	if len(f.Entries) != 3 {
		t.Fatalf("want 3 root entries, got %d", len(f.Entries))
	}
	mustHaveRel(t, f.Links, RelSelf, "http://host:8787/opds")
	mustHaveRel(t, f.Links, RelStart, "http://host:8787/opds")

	gotTitles := map[string]bool{}
	for _, e := range f.Entries {
		gotTitles[e.Title] = true
	}
	for _, want := range []string{"Authors", "Series", "Recently Added"} {
		if !gotTitles[want] {
			t.Errorf("missing root entry %q", want)
		}
	}
}

func TestBuildAuthors_SkipsEmptyAuthors(t *testing.T) {
	// Add an author with no imported books — should be hidden.
	books, authors, series := fixture()
	authors.all = append(authors.all, models.Author{ID: 99, Name: "Empty Zee", SortName: "Zee, Empty"})
	b := NewBuilder(Config{PageSize: 50}, books, authors, series)

	f, err := b.BuildAuthors(context.Background(), "http://host", 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(f.Entries) != 2 {
		t.Fatalf("expected 2 authors with imported books, got %d", len(f.Entries))
	}
	// SortName lex order: Chambers, Becky < Palmer, Ada
	if f.Entries[0].Title != "Becky Chambers" {
		t.Errorf("first = %q, want Becky Chambers", f.Entries[0].Title)
	}
	if f.TotalResults != 2 || f.ItemsPerPage != 50 || f.StartIndex != 1 {
		t.Errorf("paging: total=%d items=%d start=%d", f.TotalResults, f.ItemsPerPage, f.StartIndex)
	}
}

func TestBuildAuthors_Paging(t *testing.T) {
	// Force multiple pages with a tiny page size.
	books, _, series := fixture()
	var bigAuthors []models.Author
	for i := int64(1); i <= 5; i++ {
		bigAuthors = append(bigAuthors, models.Author{ID: i, Name: "A", SortName: "a"})
	}
	// Give every author one imported book so none are filtered.
	var bigBooks []models.Book
	for i := int64(1); i <= 5; i++ {
		bigBooks = append(bigBooks, models.Book{
			ID: 100 + i, AuthorID: i, Title: "X",
			FilePath: "/x.epub", Status: models.BookStatusImported,
		})
	}
	books.all = bigBooks

	b := NewBuilder(Config{PageSize: 2}, books, &fakeAuthors{all: bigAuthors}, series)

	page1, _ := b.BuildAuthors(context.Background(), "http://h", 1)
	if len(page1.Entries) != 2 {
		t.Errorf("page 1 entries = %d", len(page1.Entries))
	}
	if !hasRel(page1.Links, RelNext) {
		t.Error("page 1 missing rel=next")
	}
	if hasRel(page1.Links, RelPrevious) {
		t.Error("page 1 should not have rel=previous")
	}

	page3, _ := b.BuildAuthors(context.Background(), "http://h", 3)
	if len(page3.Entries) != 1 {
		t.Errorf("page 3 entries = %d, want 1", len(page3.Entries))
	}
	if hasRel(page3.Links, RelNext) {
		t.Error("page 3 should not have rel=next")
	}
	if !hasRel(page3.Links, RelPrevious) {
		t.Error("page 3 missing rel=previous")
	}
}

func TestBuildAuthor_NotFound(t *testing.T) {
	b := newBuilder()
	_, err := b.BuildAuthor(context.Background(), "http://h", 9999)
	if !errors.Is(err, ErrNotFound) {
		t.Errorf("err = %v, want ErrNotFound", err)
	}
}

func TestBuildAuthor_Acquisition(t *testing.T) {
	b := newBuilder()
	f, err := b.BuildAuthor(context.Background(), "http://host:8787", 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(f.Entries) != 1 {
		t.Fatalf("entries = %d, want 1", len(f.Entries))
	}
	entry := f.Entries[0]
	if entry.Title != "Too Like the Lightning" {
		t.Errorf("title = %q", entry.Title)
	}
	acq := findLink(entry.Links, RelAcquisition)
	if acq == nil {
		t.Fatal("acquisition link missing")
	}
	if acq.Href != "http://host:8787/opds/book/10/file" {
		t.Errorf("acq href = %q", acq.Href)
	}
	if acq.Type != "application/epub+zip" {
		t.Errorf("acq type = %q", acq.Type)
	}
	if len(entry.Authors) != 1 || entry.Authors[0].Name != "Ada Palmer" {
		t.Errorf("entry authors = %v", entry.Authors)
	}
}

func TestBuildSeriesList_SortedByTitle(t *testing.T) {
	b := newBuilder()
	f, err := b.BuildSeriesList(context.Background(), "http://h", 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(f.Entries) != 1 || f.Entries[0].Title != "Wayfarers" {
		t.Errorf("entries = %+v", f.Entries)
	}
}

func TestBuildSeries_PrefixesPosition(t *testing.T) {
	b := newBuilder()
	f, err := b.BuildSeries(context.Background(), "http://h", 100)
	if err != nil {
		t.Fatal(err)
	}
	if len(f.Entries) != 1 {
		t.Fatalf("entries = %d", len(f.Entries))
	}
	if !strings.HasPrefix(f.Entries[0].Title, "3. ") {
		t.Errorf("title = %q, want to start with '3. '", f.Entries[0].Title)
	}
}

func TestBuildRecent_OrdersByUpdatedDesc(t *testing.T) {
	b := newBuilder()
	f, err := b.BuildRecent(context.Background(), "http://h")
	if err != nil {
		t.Fatal(err)
	}
	if len(f.Entries) != 2 {
		t.Fatalf("entries = %d", len(f.Entries))
	}
	// b2 updated 2026-03-02, b1 updated 2026-03-01 — b2 must come first.
	if f.Entries[0].Title != "Record of a Spaceborn Few" {
		t.Errorf("first = %q, want Record of a Spaceborn Few", f.Entries[0].Title)
	}
}

func TestBuildBook_NotFound(t *testing.T) {
	b := newBuilder()
	_, err := b.BuildBook(context.Background(), "http://h", 9999)
	if !errors.Is(err, ErrNotFound) {
		t.Errorf("err = %v", err)
	}
}

func TestBuildBook_OneEntry(t *testing.T) {
	b := newBuilder()
	f, err := b.BuildBook(context.Background(), "http://h", 11)
	if err != nil {
		t.Fatal(err)
	}
	if len(f.Entries) != 1 {
		t.Fatalf("entries = %d", len(f.Entries))
	}
	if f.Entries[0].Issued != "2019-06-11" {
		t.Errorf("issued = %q", f.Entries[0].Issued)
	}
	if f.Entries[0].Language != "eng" {
		t.Errorf("language = %q", f.Entries[0].Language)
	}
}

// TestXMLMarshal validates that the feed round-trips through encoding/xml
// with the namespace prefixes and OPDS element names that strict clients
// (KOReader, Moon+ Reader) require.
func TestXMLMarshal(t *testing.T) {
	b := newBuilder()
	f, err := b.BuildAuthor(context.Background(), "http://host:8787", 1)
	if err != nil {
		t.Fatal(err)
	}
	buf, err := xml.MarshalIndent(f, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	out := string(buf)

	required := []string{
		`xmlns="http://www.w3.org/2005/Atom"`,
		`xmlns:dc="http://purl.org/dc/terms/"`,
		`xmlns:opds="http://opds-spec.org/2010/catalog"`,
		`<dc:language>eng</dc:language>`,
		`rel="http://opds-spec.org/acquisition"`,
		`type="application/epub+zip"`,
		`<title>Too Like the Lightning</title>`,
		`urn:bindery:book:10`,
	}
	for _, s := range required {
		if !strings.Contains(out, s) {
			t.Errorf("xml missing %q\n---\n%s", s, out)
		}
	}
	// Self-sanity: parse it back.
	var parsed Feed
	if err := xml.Unmarshal(buf, &parsed); err != nil {
		t.Fatalf("re-parse: %v", err)
	}
}

// TestPageBounds covers the edge cases of the pagination helper directly.
func TestPageBounds(t *testing.T) {
	cases := []struct {
		page, size, total  int
		wantStart, wantEnd int
	}{
		{1, 10, 0, 0, 0},
		{1, 10, 5, 0, 5},
		{1, 10, 25, 0, 10},
		{3, 10, 25, 20, 25},
		{4, 10, 25, 25, 25}, // overrun
	}
	for _, c := range cases {
		s, e := pageBounds(c.page, c.size, c.total)
		if s != c.wantStart || e != c.wantEnd {
			t.Errorf("pageBounds(%d,%d,%d) = (%d,%d), want (%d,%d)",
				c.page, c.size, c.total, s, e, c.wantStart, c.wantEnd)
		}
	}
}

func TestGuessFileType(t *testing.T) {
	cases := map[string]string{
		"/a/b.epub":                       "application/epub+zip",
		"/a/b.EPUB":                       "application/epub+zip",
		"/a/b.pdf":                        "application/pdf",
		"/a/b.mobi":                       "application/x-mobipocket-ebook",
		"/a/b.unknown":                    "application/octet-stream",
		"/library/author/title/directory": "application/octet-stream",
	}
	for path, want := range cases {
		got := guessFileType(path, models.MediaTypeEbook)
		if got != want {
			t.Errorf("guessFileType(%q) = %q, want %q", path, got, want)
		}
	}
	if guessFileType("/a/b.m4b", models.MediaTypeAudiobook) != "application/zip" {
		t.Error("audiobook must serialize as zip")
	}
}

// --- helpers -----------------------------------------------------------------

func findLink(links []Link, rel string) *Link {
	for i := range links {
		if links[i].Rel == rel {
			return &links[i]
		}
	}
	return nil
}

func hasRel(links []Link, rel string) bool { return findLink(links, rel) != nil }

func mustHaveRel(t *testing.T, links []Link, rel, href string) {
	t.Helper()
	l := findLink(links, rel)
	if l == nil {
		t.Fatalf("missing rel=%s", rel)
	}
	if l.Href != href {
		t.Errorf("rel=%s href = %q, want %q", rel, l.Href, href)
	}
}
