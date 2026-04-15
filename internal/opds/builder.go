package opds

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/vavallee/bindery/internal/models"
)

// BookStore is the subset of the book repository the builder needs. Kept
// narrow so the test suite can substitute an in-memory fake without pulling
// in SQLite.
type BookStore interface {
	List(ctx context.Context) ([]models.Book, error)
	ListByAuthor(ctx context.Context, authorID int64) ([]models.Book, error)
	ListByStatus(ctx context.Context, status string) ([]models.Book, error)
	GetByID(ctx context.Context, id int64) (*models.Book, error)
}

// AuthorStore is the subset of the author repository the builder needs.
type AuthorStore interface {
	List(ctx context.Context) ([]models.Author, error)
	GetByID(ctx context.Context, id int64) (*models.Author, error)
}

// SeriesStore is the subset of the series repository the builder needs.
type SeriesStore interface {
	List(ctx context.Context) ([]models.Series, error)
	GetByID(ctx context.Context, id int64) (*models.Series, error)
}

// Config controls catalogue pagination and the catalogue title displayed in
// OPDS clients.
type Config struct {
	// Title shown as the root <feed>/<title>.
	Title string
	// PageSize is the number of entries per page on paginated feeds
	// (authors list, series list). Defaults to 50 when ≤ 0.
	PageSize int
}

// Builder assembles OPDS feeds from the three repositories. It has no
// knowledge of HTTP — handlers call one of the Build* methods and serialise
// the returned Feed via encoding/xml.
type Builder struct {
	cfg     Config
	books   BookStore
	authors AuthorStore
	series  SeriesStore
}

// NewBuilder constructs a Builder. A zero-value Config is valid; defaults
// are applied on read.
func NewBuilder(cfg Config, books BookStore, authors AuthorStore, series SeriesStore) *Builder {
	if cfg.PageSize <= 0 {
		cfg.PageSize = 50
	}
	if cfg.Title == "" {
		cfg.Title = "Bindery"
	}
	return &Builder{cfg: cfg, books: books, authors: authors, series: series}
}

// BuildRoot returns the navigation feed at `/opds` — three entries for
// Authors, Series, and Recent.
func (b *Builder) BuildRoot(base string) Feed {
	base = strings.TrimRight(base, "/")
	now := nowRFC3339()
	f := navFeed(b.cfg.Title, "urn:bindery:opds:root", now)
	f.Links = []Link{
		{Rel: RelSelf, Href: base + "/opds", Type: TypeNavigation},
		{Rel: RelStart, Href: base + "/opds", Type: TypeNavigation},
	}
	f.Entries = []Entry{
		{
			ID:      "urn:bindery:opds:authors",
			Title:   "Authors",
			Updated: now,
			Content: &Content{Type: "text", Body: "Browse by author"},
			Links: []Link{{
				Rel: RelSubsection, Type: TypeNavigation,
				Href: base + "/opds/authors", Title: "Authors",
			}},
		},
		{
			ID:      "urn:bindery:opds:series",
			Title:   "Series",
			Updated: now,
			Content: &Content{Type: "text", Body: "Browse by series"},
			Links: []Link{{
				Rel: RelSubsection, Type: TypeNavigation,
				Href: base + "/opds/series", Title: "Series",
			}},
		},
		{
			ID:      "urn:bindery:opds:recent",
			Title:   "Recently Added",
			Updated: now,
			Content: &Content{Type: "text", Body: "Last 50 imported books"},
			Links: []Link{{
				Rel: RelSortNew, Type: TypeAcquisition,
				Href: base + "/opds/recent", Title: "Recently Added",
			}},
		},
	}
	return f
}

// BuildAuthors returns a paginated navigation feed listing every author with
// at least one imported book. page is 1-based.
func (b *Builder) BuildAuthors(ctx context.Context, base string, page int) (Feed, error) {
	base = strings.TrimRight(base, "/")
	authors, err := b.authors.List(ctx)
	if err != nil {
		return Feed{}, fmt.Errorf("list authors: %w", err)
	}
	// Only surface authors with at least one imported book — an empty
	// library is pointless for KOReader to browse into.
	authors = b.filterAuthorsWithImportedBooks(ctx, authors)
	sort.Slice(authors, func(i, j int) bool {
		return strings.ToLower(authors[i].SortName) < strings.ToLower(authors[j].SortName)
	})

	total := len(authors)
	page = normalizePage(page)
	start, end := pageBounds(page, b.cfg.PageSize, total)

	now := nowRFC3339()
	f := navFeed(b.cfg.Title+" — Authors", "urn:bindery:opds:authors", now)
	f.Links = append(f.Links,
		Link{Rel: RelSelf, Href: pagedURL(base+"/opds/authors", page), Type: TypeNavigation},
		Link{Rel: RelStart, Href: base + "/opds", Type: TypeNavigation},
		Link{Rel: RelUp, Href: base + "/opds", Type: TypeNavigation},
	)
	addPagingLinks(&f, base+"/opds/authors", page, b.cfg.PageSize, total)
	f.TotalResults = total
	f.ItemsPerPage = b.cfg.PageSize
	f.StartIndex = start + 1

	for i := start; i < end; i++ {
		a := authors[i]
		f.Entries = append(f.Entries, Entry{
			ID:      fmt.Sprintf("urn:bindery:author:%d", a.ID),
			Title:   nonEmpty(a.Name, a.SortName, "Unknown author"),
			Updated: rfc3339(a.UpdatedAt),
			Content: descriptionContent(a.Description),
			Links: []Link{{
				Rel: RelSubsection, Type: TypeAcquisition,
				Href:  fmt.Sprintf("%s/opds/authors/%d", base, a.ID),
				Title: nonEmpty(a.Name, "Books"),
			}},
		})
	}
	return f, nil
}

// BuildAuthor returns an acquisition feed of every book by the author that
// has an imported file on disk.
func (b *Builder) BuildAuthor(ctx context.Context, base string, authorID int64) (Feed, error) {
	base = strings.TrimRight(base, "/")
	a, err := b.authors.GetByID(ctx, authorID)
	if err != nil {
		return Feed{}, fmt.Errorf("get author: %w", err)
	}
	if a == nil {
		return Feed{}, ErrNotFound
	}
	books, err := b.books.ListByAuthor(ctx, authorID)
	if err != nil {
		return Feed{}, fmt.Errorf("list books: %w", err)
	}
	books = filterImported(books)

	now := nowRFC3339()
	f := acquisitionFeed(a.Name, fmt.Sprintf("urn:bindery:author:%d", a.ID), now)
	f.Links = append(f.Links,
		Link{Rel: RelSelf, Href: fmt.Sprintf("%s/opds/authors/%d", base, a.ID), Type: TypeAcquisition},
		Link{Rel: RelStart, Href: base + "/opds", Type: TypeNavigation},
		Link{Rel: RelUp, Href: base + "/opds/authors", Type: TypeNavigation},
	)
	for _, bk := range books {
		f.Entries = append(f.Entries, b.bookEntry(base, bk, a))
	}
	return f, nil
}

// BuildSeriesList returns a paginated navigation feed of every series that
// has at least one imported book.
func (b *Builder) BuildSeriesList(ctx context.Context, base string, page int) (Feed, error) {
	base = strings.TrimRight(base, "/")
	ser, err := b.series.List(ctx)
	if err != nil {
		return Feed{}, fmt.Errorf("list series: %w", err)
	}
	sort.Slice(ser, func(i, j int) bool {
		return strings.ToLower(ser[i].Title) < strings.ToLower(ser[j].Title)
	})

	total := len(ser)
	page = normalizePage(page)
	start, end := pageBounds(page, b.cfg.PageSize, total)

	now := nowRFC3339()
	f := navFeed(b.cfg.Title+" — Series", "urn:bindery:opds:series", now)
	f.Links = append(f.Links,
		Link{Rel: RelSelf, Href: pagedURL(base+"/opds/series", page), Type: TypeNavigation},
		Link{Rel: RelStart, Href: base + "/opds", Type: TypeNavigation},
		Link{Rel: RelUp, Href: base + "/opds", Type: TypeNavigation},
	)
	addPagingLinks(&f, base+"/opds/series", page, b.cfg.PageSize, total)
	f.TotalResults = total
	f.ItemsPerPage = b.cfg.PageSize
	f.StartIndex = start + 1

	for i := start; i < end; i++ {
		s := ser[i]
		f.Entries = append(f.Entries, Entry{
			ID:      fmt.Sprintf("urn:bindery:series:%d", s.ID),
			Title:   nonEmpty(s.Title, "Untitled series"),
			Updated: rfc3339(s.CreatedAt),
			Content: descriptionContent(s.Description),
			Links: []Link{{
				Rel: RelSubsection, Type: TypeAcquisition,
				Href:  fmt.Sprintf("%s/opds/series/%d", base, s.ID),
				Title: s.Title,
			}},
		})
	}
	return f, nil
}

// BuildSeries returns an acquisition feed of every book in the series that
// has an imported file on disk, ordered by the stored position-in-series.
func (b *Builder) BuildSeries(ctx context.Context, base string, seriesID int64) (Feed, error) {
	base = strings.TrimRight(base, "/")
	s, err := b.series.GetByID(ctx, seriesID)
	if err != nil {
		return Feed{}, fmt.Errorf("get series: %w", err)
	}
	if s == nil {
		return Feed{}, ErrNotFound
	}

	now := nowRFC3339()
	f := acquisitionFeed(s.Title, fmt.Sprintf("urn:bindery:series:%d", s.ID), now)
	f.Links = append(f.Links,
		Link{Rel: RelSelf, Href: fmt.Sprintf("%s/opds/series/%d", base, s.ID), Type: TypeAcquisition},
		Link{Rel: RelStart, Href: base + "/opds", Type: TypeNavigation},
		Link{Rel: RelUp, Href: base + "/opds/series", Type: TypeNavigation},
	)

	// SeriesRepo.GetByID only pulls a thin projection of each book (no
	// file_path, no updated_at, no language). Re-read the full row from
	// the books repo so we can emit a proper acquisition link.
	for _, sb := range s.Books {
		bk, err := b.books.GetByID(ctx, sb.BookID)
		if err != nil || bk == nil || bk.Status != models.BookStatusImported || bk.FilePath == "" {
			continue
		}
		var author *models.Author
		if bk.AuthorID > 0 {
			author, _ = b.authors.GetByID(ctx, bk.AuthorID)
		}
		entry := b.bookEntry(base, *bk, author)
		if sb.PositionInSeries != "" {
			entry.Title = fmt.Sprintf("%s. %s", sb.PositionInSeries, entry.Title)
		}
		f.Entries = append(f.Entries, entry)
	}
	return f, nil
}

// BuildRecent returns an acquisition feed of the 50 most recently updated
// imported books.
func (b *Builder) BuildRecent(ctx context.Context, base string) (Feed, error) {
	base = strings.TrimRight(base, "/")
	books, err := b.books.ListByStatus(ctx, models.BookStatusImported)
	if err != nil {
		return Feed{}, fmt.Errorf("list imported: %w", err)
	}
	books = filterImported(books)
	sort.SliceStable(books, func(i, j int) bool {
		// Tie-break by ID so the sort is deterministic even when many
		// books share an updated_at (common for bulk imports).
		if books[i].UpdatedAt.Equal(books[j].UpdatedAt) {
			return books[i].ID > books[j].ID
		}
		return books[i].UpdatedAt.After(books[j].UpdatedAt)
	})
	if len(books) > 50 {
		books = books[:50]
	}

	now := nowRFC3339()
	f := acquisitionFeed(b.cfg.Title+" — Recently Added", "urn:bindery:opds:recent", now)
	f.Links = append(f.Links,
		Link{Rel: RelSelf, Href: base + "/opds/recent", Type: TypeAcquisition},
		Link{Rel: RelStart, Href: base + "/opds", Type: TypeNavigation},
		Link{Rel: RelUp, Href: base + "/opds", Type: TypeNavigation},
	)
	for _, bk := range books {
		var author *models.Author
		if bk.AuthorID > 0 {
			author, _ = b.authors.GetByID(ctx, bk.AuthorID)
		}
		f.Entries = append(f.Entries, b.bookEntry(base, bk, author))
	}
	return f, nil
}

// BuildBook returns a single-entry acquisition feed for the given book —
// some OPDS clients (Moon+ Reader) expect a feed rather than a bare entry
// when drilling into a publication detail link.
func (b *Builder) BuildBook(ctx context.Context, base string, bookID int64) (Feed, error) {
	base = strings.TrimRight(base, "/")
	bk, err := b.books.GetByID(ctx, bookID)
	if err != nil {
		return Feed{}, fmt.Errorf("get book: %w", err)
	}
	if bk == nil {
		return Feed{}, ErrNotFound
	}
	var author *models.Author
	if bk.AuthorID > 0 {
		author, _ = b.authors.GetByID(ctx, bk.AuthorID)
	}

	now := nowRFC3339()
	f := acquisitionFeed(bk.Title, fmt.Sprintf("urn:bindery:book:%d", bk.ID), now)
	f.Links = append(f.Links,
		Link{Rel: RelSelf, Href: fmt.Sprintf("%s/opds/book/%d", base, bk.ID), Type: TypeAcquisition},
		Link{Rel: RelStart, Href: base + "/opds", Type: TypeNavigation},
	)
	f.Entries = []Entry{b.bookEntry(base, *bk, author)}
	return f, nil
}

// bookEntry produces one <entry> for an imported book. When the book has no
// file on disk the acquisition link is omitted so clients don't offer a
// download that would 404.
func (b *Builder) bookEntry(base string, bk models.Book, author *models.Author) Entry {
	e := Entry{
		ID:       fmt.Sprintf("urn:bindery:book:%d", bk.ID),
		Title:    nonEmpty(bk.Title, "Untitled"),
		Updated:  rfc3339(bk.UpdatedAt),
		Content:  descriptionContent(bk.Description),
		Language: bk.Language,
	}
	if bk.ReleaseDate != nil && !bk.ReleaseDate.IsZero() {
		e.Issued = bk.ReleaseDate.Format("2006-01-02")
	}
	if author != nil {
		e.Authors = []Person{{
			Name: nonEmpty(author.Name, author.SortName, "Unknown"),
		}}
	}
	if bk.ImageURL != "" {
		e.Links = append(e.Links,
			Link{Rel: RelImage, Href: bk.ImageURL, Type: guessImageType(bk.ImageURL)},
			Link{Rel: RelThumbnail, Href: bk.ImageURL, Type: guessImageType(bk.ImageURL)},
		)
	}
	if bk.FilePath != "" && bk.Status == models.BookStatusImported {
		e.Links = append(e.Links, Link{
			Rel:   RelAcquisition,
			Href:  fmt.Sprintf("%s/opds/book/%d/file", base, bk.ID),
			Type:  guessFileType(bk.FilePath, bk.MediaType),
			Title: "Download",
		})
	}
	return e
}

// filterAuthorsWithImportedBooks drops authors whose library is empty.
// A single extra query per author is fine at the author-count scales
// Bindery users run at (hundreds, not tens of thousands).
func (b *Builder) filterAuthorsWithImportedBooks(ctx context.Context, authors []models.Author) []models.Author {
	kept := make([]models.Author, 0, len(authors))
	for _, a := range authors {
		books, err := b.books.ListByAuthor(ctx, a.ID)
		if err != nil {
			continue
		}
		if len(filterImported(books)) == 0 {
			continue
		}
		kept = append(kept, a)
	}
	return kept
}
