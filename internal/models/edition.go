package models

import "time"

type Edition struct {
	ID          int64      `json:"id"`
	ForeignID   string     `json:"foreignEditionId"`
	BookID      int64      `json:"bookId"`
	Title       string     `json:"title"`
	ISBN13      *string    `json:"isbn13"`
	ISBN10      *string    `json:"isbn10"`
	ASIN        *string    `json:"asin"`
	Publisher   string     `json:"publisher"`
	PublishDate *time.Time `json:"publishDate"`
	Format      string     `json:"format"`
	NumPages    *int       `json:"numPages"`
	Language    string     `json:"language"`
	ImageURL    string     `json:"imageUrl"`
	IsEbook     bool       `json:"isEbook"`
	EditionInfo string     `json:"editionInformation"`
	Monitored   bool       `json:"monitored"`
	CreatedAt   time.Time  `json:"createdAt"`
	UpdatedAt   time.Time  `json:"updatedAt"`
	// DurationSeconds carries provider audio runtime to book hydration; it is not stored on editions.
	DurationSeconds int `json:"-"`
	// LockedFields lists publication fields the user set by hand, see the
	// EditionField* constants. Both edition upserts test the stored set inside
	// their SQL and keep a locked column's value, whether the incoming write
	// would overwrite it or fill it. Persisted as a JSON array (migration 115).
	LockedFields []string `json:"lockedFields"`
	// UpdatedAtRaw is editions.updated_at exactly as stored, for the
	// compare-and-swap in EditionRepo.UpdateFields; see Book.UpdatedAtRaw for
	// why the stored text, not a reformatted time, is what gets compared.
	UpdatedAtRaw string `json:"-"`
}

// Lockable edition fields. These are the JSON names the API accepts in an
// edition's lockedFields and the names the upsert guards test.
const (
	EditionFieldPublisher   = "publisher"
	EditionFieldPublishDate = "publishDate"
	EditionFieldISBN13      = "isbn13"
	EditionFieldISBN10      = "isbn10"
	EditionFieldASIN        = "asin"
)

// LockableEditionFields is the closed set of names accepted in
// Edition.LockedFields, in display order.
var LockableEditionFields = []string{
	EditionFieldPublisher, EditionFieldPublishDate,
	EditionFieldISBN13, EditionFieldISBN10, EditionFieldASIN,
}

// IsLockableEditionField reports whether name is one of LockableEditionFields.
func IsLockableEditionField(name string) bool {
	for _, f := range LockableEditionFields {
		if f == name {
			return true
		}
	}
	return false
}

// IsFieldLocked reports whether the named field was set by hand and must not
// be overwritten or filled by an import or hydration.
func (e *Edition) IsFieldLocked(field string) bool {
	for _, f := range e.LockedFields {
		if f == field {
			return true
		}
	}
	return false
}

// CanWrite reports whether an import or hydration path may write the named
// field. It answers ownership only; whether the column is empty is a separate
// test the caller still makes, because a lock blocks a fill as well as an
// overwrite (#2757). Nil safe.
func (e *Edition) CanWrite(field string) bool {
	if e == nil {
		return true
	}
	return !e.IsFieldLocked(field)
}

// LockField adds the named field to LockedFields if not already present.
func (e *Edition) LockField(field string) {
	if !e.IsFieldLocked(field) {
		e.LockedFields = append(e.LockedFields, field)
	}
}
