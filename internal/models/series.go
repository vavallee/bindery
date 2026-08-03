package models

import "time"

type Series struct {
	ID               int64     `json:"id"`
	ForeignID        string    `json:"foreignSeriesId"`
	Title            string    `json:"title"`
	Description      string    `json:"description"`
	Monitored        bool      `json:"monitored"`
	GenreOverride    []string  `json:"-"`
	GenreOverrideSet bool      `json:"-"`
	CreatedAt        time.Time `json:"createdAt"`

	// Joined data
	Books         []SeriesBook         `json:"books,omitempty"`
	HardcoverLink *SeriesHardcoverLink `json:"hardcoverLink,omitempty"`
}

type SeriesHardcoverLink struct {
	ID                  int64     `json:"id"`
	SeriesID            int64     `json:"seriesId"`
	HardcoverSeriesID   string    `json:"hardcoverSeriesId"`
	HardcoverProviderID string    `json:"hardcoverProviderId"`
	HardcoverTitle      string    `json:"hardcoverTitle"`
	HardcoverAuthorName string    `json:"hardcoverAuthorName"`
	HardcoverBookCount  int       `json:"hardcoverBookCount"`
	Confidence          float64   `json:"confidence"`
	LinkedBy            string    `json:"linkedBy"`
	LinkedAt            time.Time `json:"linkedAt"`
	CreatedAt           time.Time `json:"createdAt"`
	UpdatedAt           time.Time `json:"updatedAt"`
}

type SeriesBook struct {
	SeriesID         int64  `json:"seriesId"`
	BookID           int64  `json:"bookId"`
	PositionInSeries string `json:"positionInSeries"`
	PrimarySeries    bool   `json:"primarySeries"`

	// Joined
	Book *Book `json:"book,omitempty"`
}
