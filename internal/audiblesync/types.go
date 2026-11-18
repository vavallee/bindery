package audiblesync

// The shapes below mirror Audible Sync's response models
// (src/audible_sync/api/schemas.py there, exported as web/openapi.json).
// Only the fields Bindery reads are declared; unknown fields are ignored, so
// Audible Sync can add to its answers without breaking this client.

// Health is GET /api/health, the one route that needs no token.
type Health struct {
	Status  string `json:"status"`
	Version string `json:"version"`
}

// Account is one Audible account Audible Sync holds (AccountOut).
type Account struct {
	ID           int    `json:"id"`
	Name         string `json:"name"`
	CountryCode  string `json:"country_code"`
	CustomerName string `json:"customer_name"`
	Enabled      bool   `json:"enabled"`
	NeedsLogin   bool   `json:"needs_login"`
	LastSyncedAt string `json:"last_synced_at"`
}

// CatalogAuthor is an author of a catalogue book.
type CatalogAuthor struct {
	Name string `json:"name"`
	ASIN string `json:"asin"`
}

// SeriesEntry is a series a book belongs to. Sequence is Audible's own
// string ("4.5", "1-3"), not a number.
type SeriesEntry struct {
	Title      string `json:"title"`
	Sequence   string `json:"sequence"`
	SeriesASIN string `json:"series_asin"`
}

// CatalogBook is one book of the Audible catalogue (CatalogBookOut).
type CatalogBook struct {
	ASIN          string          `json:"asin"`
	Title         string          `json:"title"`
	Subtitle      string          `json:"subtitle"`
	Authors       []CatalogAuthor `json:"authors"`
	Narrators     []string        `json:"narrators"`
	Series        []SeriesEntry   `json:"series"`
	PrimarySeries *SeriesEntry    `json:"primary_series"`
	Genres        []string        `json:"genres"`
	LengthSeconds *int            `json:"length_seconds"`
	ReleaseDate   string          `json:"release_date"`
	Language      string          `json:"language"`
	Publisher     string          `json:"publisher"`
	Abridged      *bool           `json:"abridged"`
	CoverURL      string          `json:"cover_url"`
	Rating        *float64        `json:"rating"`
	RatingCount   int             `json:"rating_count"`
	Multipart     bool            `json:"multipart"`
	// InLibrary is true when any account of this marketplace owns the book.
	InLibrary bool `json:"in_library"`
	// OnWishlist lists the accounts of this marketplace wishing for it.
	OnWishlist []int `json:"on_wishlist"`
}

// WishlistItem is a book on one account's wishlist.
type WishlistItem struct {
	CatalogBook
	AccountID int    `json:"account_id"`
	AddedAt   string `json:"added_at"`
}

// WishlistAccount is where one account's wishlist stands.
type WishlistAccount struct {
	AccountID  int    `json:"account_id"`
	ReadAt     string `json:"read_at"`
	Error      string `json:"error"`
	NeedsLogin bool   `json:"needs_login"`
}

// WishlistPage is GET /api/wishlist.
type WishlistPage struct {
	Items    []WishlistItem    `json:"items"`
	Total    int               `json:"total"`
	Page     int               `json:"page"`
	PageSize int               `json:"page_size"`
	Accounts []WishlistAccount `json:"accounts"`
}

// Library book statuses, as Audible Sync's pipeline names them.
const (
	LibraryStatusWaiting     = "waiting_download"
	LibraryStatusDownloading = "downloading"
	LibraryStatusDownloaded  = "downloaded"
	// LibraryStatusUnavailable is a book Audible will not let the account
	// download, usually a title withdrawn from the Plus catalogue. Audible
	// Sync treats it as temporary: Audible does bring titles back.
	LibraryStatusUnavailable = "unavailable"
	LibraryStatusFailed      = "failed"
)

// LibraryBook is one book of an account's library (BookOut). Unlike a
// catalogue book its authors are plain names and Length is in minutes.
type LibraryBook struct {
	ID            int           `json:"id"`
	AccountID     int           `json:"account_id"`
	ASIN          string        `json:"asin"`
	Title         string        `json:"title"`
	Subtitle      string        `json:"subtitle"`
	Authors       []string      `json:"authors"`
	Narrators     []string      `json:"narrators"`
	Series        []SeriesEntry `json:"series"`
	PrimarySeries *SeriesEntry  `json:"primary_series"`
	Genres        []string      `json:"genres"`
	Length        int           `json:"length"`
	ReleaseDate   string        `json:"release_date"`
	CoverURL      string        `json:"cover_url"`
	Status        string        `json:"status"`
}

// LibraryPage is GET /api/books.
type LibraryPage struct {
	Items    []LibraryBook `json:"items"`
	Total    int           `json:"total"`
	Page     int           `json:"page"`
	PageSize int           `json:"page_size"`
}

// CatalogBook gives a library book the catalogue shape, so the import list
// sync handles both the same way. The book is owned, so InLibrary is set.
func (b LibraryBook) CatalogBook() CatalogBook {
	authors := make([]CatalogAuthor, 0, len(b.Authors))
	for _, a := range b.Authors {
		authors = append(authors, CatalogAuthor{Name: a})
	}
	cb := CatalogBook{
		ASIN: b.ASIN, Title: b.Title, Subtitle: b.Subtitle, Authors: authors, Narrators: b.Narrators,
		Series: b.Series, PrimarySeries: b.PrimarySeries, Genres: b.Genres, ReleaseDate: b.ReleaseDate,
		CoverURL: b.CoverURL, InLibrary: true,
	}
	if b.Length > 0 {
		secs := b.Length * 60
		cb.LengthSeconds = &secs
	}
	return cb
}
