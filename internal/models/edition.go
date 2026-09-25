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
}
