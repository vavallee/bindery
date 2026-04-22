package models

import "time"

// SeriesRef carries series membership for a book returned by a metadata
// provider. Not persisted in the books table — used during ingestion to
// populate the series and series_books tables.
type SeriesRef struct {
	ForeignID string
	Title     string
	Position  string
	Primary   bool
}

type Book struct {
	ID                    int64      `json:"id"`
	ForeignID             string     `json:"foreignBookId"`
	AuthorID              int64      `json:"authorId"`
	Title                 string     `json:"title"`
	SortTitle             string     `json:"sortTitle"`
	OriginalTitle         string     `json:"originalTitle"`
	Description           string     `json:"description"`
	ImageURL              string     `json:"imageUrl"`
	ReleaseDate           *time.Time `json:"releaseDate"`
	Genres                []string   `json:"genres"`
	AverageRating         float64    `json:"averageRating"`
	RatingsCount          int        `json:"ratingsCount"`
	Monitored             bool       `json:"monitored"`
	Status                string     `json:"status"`
	AnyEditionOK          bool       `json:"anyEditionOk"`
	SelectedEditionID     *int64     `json:"selectedEditionId"`
	FilePath              string     `json:"filePath"`
	Language              string     `json:"language"`
	MediaType             string     `json:"mediaType"`
	Narrator              string     `json:"narrator"`
	DurationSeconds       int        `json:"durationSeconds"`
	ASIN                  string     `json:"asin"`
	CalibreID             *int64     `json:"calibre_id,omitempty"`
	MetadataProvider      string     `json:"metadataProvider"`
	LastMetadataRefreshAt *time.Time `json:"lastMetadataRefreshAt"`
	CreatedAt             time.Time  `json:"createdAt"`
	UpdatedAt             time.Time  `json:"updatedAt"`

	Excluded bool `json:"excluded"`

	// Deprecated: the book_files table is the source of truth for on-disk paths.
	// These fields are computed views derived from book_files (first path per
	// format) and kept for API backwards compatibility. Do not write to them
	// directly; use BookRepo.AddBookFile instead.
	EbookFilePath     string `json:"ebookFilePath"`
	AudiobookFilePath string `json:"audiobookFilePath"`

	// Joined data
	Author    *Author    `json:"author,omitempty"`
	Editions  []Edition  `json:"editions,omitempty"`
	BookFiles []BookFile `json:"bookFiles,omitempty"`

	// Transport-only: series data from the metadata provider, used during
	// ingestion to populate series/series_books. Never stored in books table.
	SeriesRefs []SeriesRef `json:"-"`
}

// WantsEbook reports whether the ebook format is monitored for this book.
func (b *Book) WantsEbook() bool {
	return b.MediaType == MediaTypeEbook || b.MediaType == MediaTypeBoth
}

// WantsAudiobook reports whether the audiobook format is monitored for this book.
func (b *Book) WantsAudiobook() bool {
	return b.MediaType == MediaTypeAudiobook || b.MediaType == MediaTypeBoth
}

// NeedsEbook reports whether the ebook format is monitored but not yet on disk.
func (b *Book) NeedsEbook() bool {
	return b.WantsEbook() && b.EbookFilePath == ""
}

// NeedsAudiobook reports whether the audiobook format is monitored but not yet on disk.
func (b *Book) NeedsAudiobook() bool {
	return b.WantsAudiobook() && b.AudiobookFilePath == ""
}

const (
	BookStatusWanted      = "wanted"
	BookStatusDownloading = "downloading"
	BookStatusDownloaded  = "downloaded"
	BookStatusImported    = "imported"
	BookStatusSkipped     = "skipped"
)

// MediaType distinguishes ebook from audiobook editions so the search,
// grab, and import pipelines can apply the right categories, formats,
// and destination directories.
const (
	MediaTypeEbook     = "ebook"
	MediaTypeAudiobook = "audiobook"
	// MediaTypeBoth requests both an ebook and an audiobook for the same
	// work. The search and import pipelines handle each format independently;
	// the book's aggregate status flips to "imported" only when both are on disk.
	MediaTypeBoth = "both"
)
