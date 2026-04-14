// Package models defines the domain types shared across the API, database,
// scheduler, and indexer layers.
package models

import "time"

type Author struct {
	ID                    int64      `json:"id"`
	ForeignID             string     `json:"foreignAuthorId"`
	Name                  string     `json:"authorName"`
	SortName              string     `json:"sortName"`
	Description           string     `json:"description"`
	ImageURL              string     `json:"imageUrl"`
	Disambiguation        string     `json:"disambiguation"`
	RatingsCount          int        `json:"ratingsCount"`
	AverageRating         float64    `json:"averageRating"`
	Monitored             bool       `json:"monitored"`
	QualityProfileID      *int64     `json:"qualityProfileId"`
	MetadataProfileID     *int64     `json:"metadataProfileId"`
	RootFolderID          *int64     `json:"rootFolderId"`
	MetadataProvider      string     `json:"metadataProvider"`
	LastMetadataRefreshAt *time.Time `json:"lastMetadataRefreshAt"`
	CreatedAt             time.Time  `json:"createdAt"`
	UpdatedAt             time.Time  `json:"updatedAt"`

	// Joined data
	Books      []Book        `json:"books,omitempty"`
	Statistics *AuthorStats  `json:"statistics,omitempty"`
	Aliases    []AuthorAlias `json:"aliases,omitempty"`
}

type AuthorStats struct {
	BookCount      int `json:"bookCount"`
	AvailableBooks int `json:"availableBookCount"`
	WantedBooks    int `json:"wantedBookCount"`
}

// AuthorAlias is an alternate name that resolves to a canonical Author.
// Populated by the merge flow so duplicates like "RR Haywood" and
// "R.R. Haywood" collapse into a single row without losing the original
// OpenLibrary id.
type AuthorAlias struct {
	ID         int64     `json:"id"`
	AuthorID   int64     `json:"authorId"`
	Name       string    `json:"name"`
	SourceOLID string    `json:"sourceOlId,omitempty"`
	CreatedAt  time.Time `json:"createdAt"`
}
